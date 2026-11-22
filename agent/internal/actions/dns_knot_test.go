package actions

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fmtInt(n int) string { return strconv.Itoa(n) }

// testDnssecKey makes a DNSSEC key as the panel makes it: an ECDSA P-256 key in the PKCS #8
// PEM format, with its key tag (RFC 4034, appendix B).
func testDnssecKey(t *testing.T) (string, int) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	text := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	// The DNSKEY record: the flags 257, the protocol 3, the algorithm 13, and the public key.
	rdata := []byte{1, 1, 3, 13}
	rdata = append(rdata, key.X.FillBytes(make([]byte, 32))...)
	rdata = append(rdata, key.Y.FillBytes(make([]byte, 32))...)
	sum := 0
	for i, b := range rdata {
		if i%2 == 0 {
			sum += int(b) << 8
		} else {
			sum += int(b)
		}
	}
	sum += (sum >> 16) & 0xffff
	return text, sum & 0xffff
}

// These tests use a real Knot DNS server on this computer, with no container. They are the
// proof that Knot accepts the configuration and the zone files of the agent, and that the
// sequence of the commands is correct. Set CF_TEST_KNOT=1 on a computer that has knotd, knotc,
// keymgr and kdig. With no value, the tests do nothing.
func knotServer(t *testing.T) (*Deps, func(args ...string) string) {
	t.Helper()
	if os.Getenv("CF_TEST_KNOT") == "" {
		t.Skip("CF_TEST_KNOT is not set: no real Knot DNS server")
	}
	d, _, _ := direct(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	d.DnsTest = &DnsTest{Listen: []string{fmt.Sprintf("127.0.0.1@%d", port)}, Knot: func(ctx context.Context, argv ...string) (string, error) {
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, out)
		}
		return string(out), nil
	}}
	ctx := context.Background()
	if err := d.startDns(ctx); err != nil {
		t.Fatal(err)
	}
	var server *exec.Cmd
	start := func() error {
		server = exec.Command("knotd", "-c", d.dnsLayout().Conf)
		server.Stderr = os.Stderr
		return server.Start()
	}
	if err := start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	d.DnsTest.Restart = func(context.Context) error {
		server.Process.Signal(os.Interrupt)
		server.Wait()
		return start()
	}
	d.ReadyTimeout = 10 * time.Second
	if err := d.waitDns(ctx); err != nil {
		t.Fatal(err)
	}
	dig := func(args ...string) string {
		out, _ := exec.Command("kdig", append([]string{"@127.0.0.1", "-p", strconv.Itoa(port), "+norec", "+tcp"}, args...)...).CombinedOutput()
		return string(out)
	}
	return d, dig
}

func TestKnotServer(t *testing.T) {
	d, dig := knotServer(t)
	ctx := context.Background()
	set := func(z dnsZone) {
		t.Helper()
		if err := d.writeDnsZones(ctx, []dnsZone{z}); err != nil {
			t.Fatal(err)
		}
		if err := d.loadDnsZones(ctx, []string{z.Name}); err != nil {
			t.Fatal(err)
		}
	}
	z := exampleZone()
	set(z)
	for query, want := range map[string]string{
		"example.com SOA":           "ns2.panel.example. hostmaster.example.com. 1794000000 3600 900 1209600 300",
		"example.com A":             "203.0.113.10",
		"example.com AAAA":          "2001:db8::10",
		"example.com MX":            "10 mx1.mail.example.",
		"example.com TXT":           `"v=spf1 include:_spf.mail.example -all; \"quoted\" back\\slash"`,
		"example.com CAA":           `0 issue "letsencrypt.org"`,
		"www.example.com CNAME":     "example.com.",
		"_sip._tcp.example.com SRV": "10 5 5060 sip.voice.example.",
		"x.dev.example.com A":       "198.51.100.7",
	} {
		if out := dig(strings.Fields(query)...); !strings.Contains(out, want) || !strings.Contains(out, "status: NOERROR") {
			t.Errorf("%s: no %q in:\n%s", query, want, out)
		}
	}
	// The server does not answer for a zone that it does not have, and gives no version.
	if out := dig("other.example", "A"); !strings.Contains(out, "status: REFUSED") {
		t.Errorf("a zone that the server does not have:\n%s", out)
	}
	if out := dig("version.server", "TXT", "CH"); strings.Contains(out, "Knot") {
		t.Errorf("the server gives its version:\n%s", out)
	}

	// A change of a record.
	z.Serial++
	z.Records[2].Value = "203.0.113.99"
	set(z)
	if out := dig("example.com", "A"); !strings.Contains(out, "203.0.113.99") {
		t.Errorf("the changed record:\n%s", out)
	}

	// DNSSEC on: the server signs the zone with the key of the panel.
	z.Serial++
	z.Key, z.KeyTag = testDnssecKey(t)
	set(z)
	out := dig("example.com", "DNSKEY", "+dnssec", "+multiline")
	if !strings.Contains(out, "id = "+strconv.Itoa(z.KeyTag)) || !strings.Contains(out, "RRSIG") {
		t.Errorf("the key with the tag %d:\n%s", z.KeyTag, out)
	}
	if out := dig("example.com", "A", "+dnssec"); !strings.Contains(out, "RRSIG") || !strings.Contains(out, "203.0.113.99") {
		t.Errorf("a signed answer:\n%s", out)
	}
	if out := dig("nothing.example.com", "A", "+dnssec"); !strings.Contains(out, "NSEC3") {
		t.Errorf("a signed answer for a name that is not there:\n%s", out)
	}
	// A change of a signed zone. The server has only one key after the same key comes again.
	z.Serial++
	z.Records[2].Value = "203.0.113.50"
	set(z)
	if out := dig("example.com", "A", "+dnssec"); !strings.Contains(out, "RRSIG") || !strings.Contains(out, "203.0.113.50") {
		t.Errorf("a changed signed zone:\n%s", out)
	}
	if keys, err := d.dnsKeys(ctx, z.Name); err != nil || len(keys) != 1 {
		t.Errorf("keys: %v %v", keys, err)
	}

	// A new key replaces the key.
	z.Serial++
	z.Key, z.KeyTag = testDnssecKey(t)
	set(z)
	if out := dig("example.com", "DNSKEY", "+dnssec", "+multiline"); !strings.Contains(out, "id = "+strconv.Itoa(z.KeyTag)) || strings.Count(out, ", id = ") != 1 {
		t.Errorf("the new key with the tag %d:\n%s", z.KeyTag, out)
	}

	// DNSSEC off: the signatures and the keys go.
	z.Serial++
	z.Key, z.KeyTag = "", 0
	set(z)
	if out := dig("example.com", "A", "+dnssec"); strings.Contains(out, "RRSIG") || !strings.Contains(out, "203.0.113.50") {
		t.Errorf("a zone with no DNSSEC:\n%s", out)
	}
	if out := dig("example.com", "SOA"); !strings.Contains(out, strconv.FormatInt(z.Serial, 10)) {
		t.Errorf("the serial of the panel:\n%s", out)
	}
	if keys, err := d.dnsKeys(ctx, z.Name); err != nil || len(keys) != 0 {
		t.Errorf("keys: %v %v", keys, err)
	}

	// A second zone, and then the removal of the first one.
	second := exampleZone()
	second.Name = "second.example"
	second.Key, second.KeyTag = testDnssecKey(t)
	set(second)
	if err := d.removeDnsZones(ctx, []string{"example.com"}); err != nil {
		t.Fatal(err)
	}
	if out := dig("example.com", "A"); !strings.Contains(out, "status: REFUSED") {
		t.Errorf("a removed zone:\n%s", out)
	}
	if out := dig("second.example", "A", "+dnssec"); !strings.Contains(out, "RRSIG") {
		t.Errorf("the second zone:\n%s", out)
	}
	if err := d.removeDnsZones(ctx, []string{"second.example"}); err != nil {
		t.Fatal(err)
	}
	if keys, err := d.dnsKeys(ctx, "second.example"); err == nil && len(keys) != 0 {
		t.Errorf("the keys of a removed zone: %v", keys)
	}
}

// Knot refuses a zone with a record that the rules of DNS do not allow. The job then fails.
func TestKnotServerReportsAZoneThatDoesNotLoad(t *testing.T) {
	d, _ := knotServer(t)
	ctx := context.Background()
	z := exampleZone()
	// A CNAME record and a different record with the same name. The panel refuses this, and
	// the agent leaves the check to Knot.
	z.Records = append(z.Records, dnsRecord{Name: "www", Type: "A", TTL: 60, Value: "192.0.2.1"})
	if err := d.writeDnsZones(ctx, []dnsZone{z}); err != nil {
		t.Fatal(err)
	}
	if err := d.loadDnsZones(ctx, []string{z.Name}); err == nil {
		t.Error("the job must fail when Knot does not load the zone")
	}
}

// notice is a NOTIFY message that the DNS server sent to a secondary.
type notice struct {
	zone string
	msg  []byte
}

// notifyListener takes the NOTIFY messages that the DNS server sends to a secondary on
// 127.0.0.1, on UDP and on TCP, with the same port. It gives the port.
func notifyListener(t *testing.T) (int, <-chan notice) {
	t.Helper()
	var tcp net.Listener
	var udp net.PacketConn
	for i := 0; tcp == nil; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		u, err := net.ListenPacket("udp", l.Addr().String())
		if err != nil {
			l.Close()
			if i == 10 {
				t.Fatal(err)
			}
			continue
		}
		tcp, udp = l, u
	}
	t.Cleanup(func() { tcp.Close(); udp.Close() })
	notices := make(chan notice, 100)
	// take keeps a NOTIFY and gives the answer: the same header and question, with the bit
	// QR. The answer has no TSIG record, so the server logs an error, but it does not wait.
	take := func(msg []byte) []byte {
		// The operation code of NOTIFY is 4. The name of the zone is the first question.
		if len(msg) < 12 || (msg[2]>>3)&0x0f != 4 {
			return nil
		}
		name, end := questionName(msg)
		if end < 0 || end+4 > len(msg) {
			return nil
		}
		notices <- notice{zone: name, msg: slices.Clone(msg)}
		answer := slices.Clone(msg[:end+4])
		answer[2] |= 0x80
		copy(answer[4:12], []byte{0, 1, 0, 0, 0, 0, 0, 0})
		return answer
	}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if answer := take(buf[:n]); answer != nil {
				udp.WriteTo(answer, from)
			}
		}
	}()
	go func() {
		for {
			c, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				for {
					var size [2]byte
					if _, err := io.ReadFull(c, size[:]); err != nil {
						return
					}
					msg := make([]byte, binary.BigEndian.Uint16(size[:]))
					if _, err := io.ReadFull(c, msg); err != nil {
						return
					}
					if answer := take(msg); answer != nil {
						c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(answer))))
						c.Write(answer)
					}
				}
			}()
		}
	}()
	return tcp.Addr().(*net.TCPAddr).Port, notices
}

// questionName gives the name of the first question of a DNS message, for example
// "example.com.", and the position after the name. The position is -1 when the message has
// no correct name.
func questionName(msg []byte) (string, int) {
	var labels []string
	for i := 12; i < len(msg); {
		n := int(msg[i])
		if n == 0 {
			return strings.Join(labels, ".") + ".", i + 1
		}
		if n&0xc0 != 0 || i+1+n > len(msg) {
			return "", -1
		}
		labels = append(labels, string(msg[i+1:i+1+n]))
		i += 1 + n
	}
	return "", -1
}

// waitNotify waits for a NOTIFY of a zone with the SOA record of a serial.
func waitNotify(t *testing.T, notices <-chan notice, zone string, serial int64) {
	t.Helper()
	want := binary.BigEndian.AppendUint32(nil, uint32(serial))
	timeout := time.After(10 * time.Second)
	for {
		select {
		case n := <-notices:
			if n.zone == zone && bytes.Contains(n.msg, want) {
				return
			}
		case <-timeout:
			t.Fatalf("no NOTIFY of %s with the serial %d", zone, serial)
		}
	}
}

// eventually asks a question again until the answer has each text, for at most 10 seconds.
func eventually(t *testing.T, ask func() string, want ...string) {
	t.Helper()
	var out string
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		out = ask()
		if !slices.ContainsFunc(want, func(w string) bool { return !strings.Contains(out, w) }) {
			return
		}
	}
	t.Errorf("no %q in:\n%s", want, out)
}

// The secondaries copy the zones with a TSIG key, find the zones in the catalog zone, and get
// NOTIFY after each change. A secondary of the owner of a zone copies only that zone.
func TestKnotServerTransfersTheZonesToTheSecondaries(t *testing.T) {
	d, dig := knotServer(t)
	port, notices := notifyListener(t)
	d.DnsTest.NotifyPort = port
	ctx := context.Background()
	set := func(z dnsZone) {
		t.Helper()
		if err := d.writeDnsZones(ctx, []dnsZone{z}); err != nil {
			t.Fatal(err)
		}
		if err := d.loadDnsZones(ctx, []string{z.Name}); err != nil {
			t.Fatal(err)
		}
	}
	tsig := func(k *dnsTsig) string { return "-y" + k.Algorithm + ":" + k.Name + ":" + k.Secret }
	z := exampleZone()
	set(z)
	signed := exampleZone()
	signed.Name = "signed.example"
	signed.Key, signed.KeyTag = testDnssecKey(t)
	set(signed)

	key := &dnsTsig{Name: "cf-anycast", Algorithm: "hmac-sha256", Secret: testSecret}
	in := dnsSecondariesIn{Catalog: "catalog.cloudfactory.invalid", Version: 1, Secondaries: []dnsSecondary{
		{ID: secondaryID, Addresses: []string{"127.0.0.1"}, Tsig: key, Notify: true, Catalog: true},
	}}
	if err := d.writeDnsSecondaries(ctx, in); err != nil {
		t.Fatal(err)
	}
	// A new secondary gets NOTIFY of each zone, so that it copies the zones now.
	waitNotify(t, notices, "example.com.", z.Serial)

	// The secondary copies each zone with its key. With no key, the server refuses the transfer.
	if out := dig("example.com", "AXFR", tsig(key)); !strings.Contains(out, "203.0.113.10") || !strings.Contains(out, "x.dev") && !strings.Contains(out, "*.dev") {
		t.Errorf("a transfer with the key:\n%s", out)
	}
	if out := dig("example.com", "AXFR"); strings.Contains(out, "203.0.113.10") {
		t.Errorf("a transfer with no key:\n%s", out)
	}
	if out := dig("example.com", "AXFR", tsig(&dnsTsig{Name: "cf-anycast", Algorithm: "hmac-sha256", Secret: testOtherSecret})); strings.Contains(out, "203.0.113.10") {
		t.Errorf("a transfer with an incorrect secret:\n%s", out)
	}
	if out := dig("signed.example", "AXFR", tsig(key)); !strings.Contains(out, "RRSIG") || !strings.Contains(out, "DNSKEY") {
		t.Errorf("a transfer of a signed zone:\n%s", out)
	}
	// The catalog zone lists the zones (RFC 9432, version 2).
	eventually(t, func() string { return dig("catalog.cloudfactory.invalid", "AXFR", tsig(key)) }, "example.com.", "signed.example.", `"2"`)
	if out := dig("catalog.cloudfactory.invalid", "AXFR"); strings.Contains(out, "signed.example.") {
		t.Errorf("a transfer of the catalog zone with no key:\n%s", out)
	}

	// A change of a zone sends NOTIFY with the new serial.
	z.Serial++
	z.Records[2].Value = "203.0.113.99"
	set(z)
	waitNotify(t, notices, "example.com.", z.Serial)
	if out := dig("example.com", "AXFR", tsig(key)); !strings.Contains(out, "203.0.113.99") {
		t.Errorf("a transfer after a change:\n%s", out)
	}

	// The owner of example.com gives a secondary of its own. The panel refuses an address that
	// is not public, so the test writes the state directly.
	zoneKey := &dnsTsig{Name: "transfer.example.com", Algorithm: "hmac-sha512", Secret: testOtherSecret}
	st, _, err := d.readDnsState()
	if err != nil {
		t.Fatal(err)
	}
	zs := st.Zones["example.com"]
	zs.Transfers = &dnsTransfers{Addresses: []string{"127.0.0.1"}, Tsig: zoneKey}
	st.Zones["example.com"] = zs
	if err := d.writeDnsState(st); err != nil {
		t.Fatal(err)
	}
	if err := d.writeDnsSecondaries(ctx, in); err != nil {
		t.Fatal(err)
	}
	if out := dig("example.com", "AXFR", tsig(zoneKey)); !strings.Contains(out, "203.0.113.99") {
		t.Errorf("a transfer to the secondary of the owner:\n%s", out)
	}
	if out := dig("signed.example", "AXFR", tsig(zoneKey)); strings.Contains(out, "DNSKEY") {
		t.Errorf("the key of example.com copies a different zone:\n%s", out)
	}
	if out := dig("example.com", "AXFR", tsig(key)); !strings.Contains(out, "203.0.113.99") {
		t.Errorf("a transfer to the secondary of the panel:\n%s", out)
	}

	// With no secondaries of the panel, only the secondary of the owner copies its zone, and
	// there is no catalog zone.
	if err := d.writeDnsSecondaries(ctx, dnsSecondariesIn{Version: 2}); err != nil {
		t.Fatal(err)
	}
	if out := dig("example.com", "AXFR", tsig(key)); strings.Contains(out, "203.0.113.99") {
		t.Errorf("a transfer to a removed secondary:\n%s", out)
	}
	if out := dig("example.com", "AXFR", tsig(zoneKey)); !strings.Contains(out, "203.0.113.99") {
		t.Errorf("a transfer to the secondary of the owner:\n%s", out)
	}
	if out := dig("catalog.cloudfactory.invalid", "SOA"); !strings.Contains(out, "status: REFUSED") {
		t.Errorf("the catalog zone with no secondaries:\n%s", out)
	}
}
