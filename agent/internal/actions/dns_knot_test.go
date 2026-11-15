package actions

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
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
	server := exec.Command("knotd", "-c", d.dnsLayout().Conf)
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
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
