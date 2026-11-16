package actions

import (
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

func exampleZone() dnsZone {
	return dnsZone{Name: "example.com", Serial: 1794000000, Records: []dnsRecord{
		{Name: "@", Type: "NS", TTL: 3600, Value: "ns2.panel.example"},
		{Name: "@", Type: "NS", TTL: 3600, Value: "ns1.panel.example"},
		{Name: "@", Type: "A", TTL: 300, Value: "203.0.113.10"},
		{Name: "@", Type: "AAAA", TTL: 300, Value: "2001:db8::10"},
		{Name: "www", Type: "CNAME", TTL: 3600, Value: "example.com"},
		{Name: "@", Type: "MX", TTL: 3600, Value: "mx1.mail.example", Priority: 10},
		{Name: "@", Type: "TXT", TTL: 3600, Value: `v=spf1 include:_spf.mail.example -all; "quoted" back\slash`},
		{Name: "_sip._tcp", Type: "SRV", TTL: 3600, Value: "5 5060 sip.voice.example", Priority: 10},
		{Name: "@", Type: "CAA", TTL: 3600, Value: `0 issue "letsencrypt.org"`},
		{Name: "*.dev", Type: "A", TTL: 60, Value: "198.51.100.7"},
	}}
}

func TestRenderZone(t *testing.T) {
	got, err := renderZone(exampleZone())
	if err != nil {
		t.Fatal(err)
	}
	// The primary name server of the SOA record is the first NS record of the job.
	want := `; Managed by the Cloudfactory agent. Change the records in the panel.
$ORIGIN example.com.
$TTL 3600
@ 3600 IN SOA ns2.panel.example. hostmaster.example.com. 1794000000 3600 900 1209600 300
*.dev 60 IN A 198.51.100.7
@ 300 IN A 203.0.113.10
@ 300 IN AAAA 2001:db8::10
@ 3600 IN CAA 0 issue "letsencrypt.org"
@ 3600 IN MX 10 mx1.mail.example.
@ 3600 IN NS ns1.panel.example.
@ 3600 IN NS ns2.panel.example.
@ 3600 IN TXT "v=spf1 include:_spf.mail.example -all; \"quoted\" back\\slash"
_sip._tcp 3600 IN SRV 10 5 5060 sip.voice.example.
www 3600 IN CNAME example.com.
`
	if got != want {
		t.Errorf("zone file:\n%s", got)
	}
}

func TestRenderZoneDividesALongText(t *testing.T) {
	z := exampleZone()
	z.Records = append(z.Records, dnsRecord{Name: "long", Type: "TXT", TTL: 3600, Value: strings.Repeat("a", 255) + strings.Repeat("b", 45)})
	got, err := renderZone(z)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `long 3600 IN TXT "`+strings.Repeat("a", 255)+`" "`+strings.Repeat("b", 45)+`"`+"\n") {
		t.Errorf("zone file:\n%s", got)
	}
}

// A value from the panel never adds a line, a directive or a second record to a zone file.
func TestRenderZoneRefusesValuesOutsideTheRules(t *testing.T) {
	for name, change := range map[string]func(z *dnsZone){
		"a zone name with a path":       func(z *dnsZone) { z.Name = "../etc/passwd" },
		"a zone name in upper case":     func(z *dnsZone) { z.Name = "Example.com" },
		"no serial":                     func(z *dnsZone) { z.Serial = 0 },
		"a serial that is too large":    func(z *dnsZone) { z.Serial = 1 << 32 },
		"no NS record":                  func(z *dnsZone) { z.Records = z.Records[2:] },
		"a CNAME record for the zone":   func(z *dnsZone) { z.Records[4].Name = "@" },
		"a directive as a name":         func(z *dnsZone) { z.Records[2].Name = "$INCLUDE" },
		"a name with a space":           func(z *dnsZone) { z.Records[2].Name = "www 60 IN A 192.0.2.1\nx" },
		"a full name":                   func(z *dnsZone) { z.Records[2].Name = "www.example.com." },
		"a record type of Knot only":    func(z *dnsZone) { z.Records[2].Type = "TYPE65534" },
		"a type in lower case":          func(z *dnsZone) { z.Records[2].Type = "a" },
		"a TTL that is too small":       func(z *dnsZone) { z.Records[2].TTL = 5 },
		"a TTL that is too large":       func(z *dnsZone) { z.Records[2].TTL = 700000 },
		"a priority that is too large":  func(z *dnsZone) { z.Records[5].Priority = 70000 },
		"an IPv6 address in an A":       func(z *dnsZone) { z.Records[2].Value = "2001:db8::1" },
		"an IPv4 address in an AAAA":    func(z *dnsZone) { z.Records[3].Value = "203.0.113.10" },
		"an address with a second line": func(z *dnsZone) { z.Records[2].Value = "203.0.113.10\n@ 60 IN A 192.0.2.1" },
		"a host with a dot at the end":  func(z *dnsZone) { z.Records[4].Value = "example.com." },
		"a host with a space":           func(z *dnsZone) { z.Records[5].Value = "mx1.mail.example 20 mx2.mail.example" },
		"a text with a line break":      func(z *dnsZone) { z.Records[6].Value = "one\ntwo" },
		"a text that is not ASCII":      func(z *dnsZone) { z.Records[6].Value = "caf\u00e9" },
		"an empty text":                 func(z *dnsZone) { z.Records[6].Value = "" },
		"an SRV value with no port":     func(z *dnsZone) { z.Records[7].Value = "5 sip.voice.example" },
		"a CAA value with a quote":      func(z *dnsZone) { z.Records[8].Value = `0 issue "a" "b"` },
		"a CAA value with a backslash":  func(z *dnsZone) { z.Records[8].Value = `0 issue "a\"` },
		"a key that is not a PEM key":   func(z *dnsZone) { z.Key, z.KeyTag = "key", 1 },
		"more records than the limit": func(z *dnsZone) {
			for i := 0; i < 1001; i++ {
				z.Records = append(z.Records, dnsRecord{Name: "h", Type: "TXT", TTL: 60, Value: "x"})
			}
		},
	} {
		z := exampleZone()
		change(&z)
		if text, err := renderZone(z); err == nil {
			t.Errorf("%s: the agent wrote the zone:\n%s", name, text)
		}
	}
}

const firewallWithEdge = `table inet cloudfactory {
  chain input {
    tcp dport 22 accept
    tcp dport 2200 accept # cloudfactory-sftp
  }
  chain prerouting {
    type nat hook prerouting priority dstnat;
    tcp dport 80 redirect to :8080
    tcp dport 443 redirect to :8443
  }
  chain output_nat {
    fib daddr type local tcp dport 80 redirect to :8080
  }
}
`

func TestSetDnsRules(t *testing.T) {
	got, err := setDnsRules(firewallWithEdge)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"    tcp dport 22 accept\n    meta l4proto { tcp, udp } th dport { 53, 8053 } accept # cloudfactory-dns\n",
		"    tcp dport 80 redirect to :8080\n    meta l4proto { tcp, udp } th dport 53 redirect to :8053 # cloudfactory-dns\n    tcp dport 443",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, dnsRuleMarker) != 2 {
		t.Errorf("the file must have two DNS rules:\n%s", got)
	}
	// The second time changes nothing.
	if again, err := setDnsRules(got); err != nil || again != got {
		t.Errorf("the second time changed the file: %v\n%s", err, again)
	}
	if _, err := setDnsRules("table inet other {}\n"); err == nil {
		t.Error("a file of an administrator must be an error")
	}
}

// dnsDeps gives a worker and its root helper on a server that has the DNS user. The fake
// runner answers for the tools of Knot.
func dnsDeps(t *testing.T) (*Deps, *execx.Fake) {
	t.Helper()
	d, f, _ := deps(t)
	f.MakeDirs = true
	both(d, func(d *Deps) {
		d.DnsImage = "ghcr.io/cloudfactory/dns:0.2.0"
		d.ReadyTimeout = time.Millisecond
		d.Host.LookupUID = func(name string) (int, error) {
			if name == host.DnsUser {
				return 987, nil
			}
			return testUID(name)
		}
	})
	return d, f
}

const knotInContainer = "runuser -u cf-dns -- env XDG_RUNTIME_DIR=/run/user/987 podman exec cf-dns "

func protoZone(z dnsZone) *agentv1.DnsZone {
	out := &agentv1.DnsZone{Name: z.Name, Serial: z.Serial, DnssecKey: z.Key, DnssecKeyTag: int32(z.KeyTag)}
	for _, r := range z.Records {
		out.Records = append(out.Records, &agentv1.DnsRecord{Name: r.Name, Type: r.Type, Ttl: int32(r.TTL), Value: r.Value, Priority: int32(r.Priority)})
	}
	return out
}

func TestSetDnsZonesStartsTheServerAndWritesTheZone(t *testing.T) {
	d, f := dnsDeps(t)
	root := rootOf(d)
	os.WriteFile(d.FirewallFile, []byte(firewallWithEdge), 0o644)
	f.Rules = []execx.Rule{
		{Prefix: knotInContainer + "knotc -c /etc/knot/knot.conf zone-status", Output: "[example.com.] serial: 1794000000\n"},
		{Prefix: "runuser -u cf-dns -- env XDG_RUNTIME_DIR=/run/user/987 podman image exists", Err: errors.New("no image")},
	}
	if got := d.DnsEnabled(context.Background()); got {
		t.Error("a server with no zone job has no DNS server")
	}
	// The report has a list, not null: the panel then knows that this server has no zone.
	if zones := d.DnsZones(context.Background()); zones == nil || len(zones) != 0 {
		t.Errorf("zones of a new server: %#v", zones)
	}
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(exampleZone())}})); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"runuser -u cf-dns -- env XDG_RUNTIME_DIR=/run/user/987 podman pull --quiet ghcr.io/cloudfactory/dns:0.2.0",
		"systemctl --user --machine=cf-dns@ daemon-reload",
		"systemctl --user --machine=cf-dns@ restart cf-dns.service",
		"nft -f " + d.FirewallFile,
		knotInContainer + "knotc -c /etc/knot/knot.conf reload",
		knotInContainer + "knotc -c /etc/knot/knot.conf -b zone-reload example.com.",
	} {
		if !f.Called(want) {
			t.Errorf("no call %q: %v", want, f.Calls)
		}
	}
	if info, err := os.Lstat(root.dnsImportDir()); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("the import directory of the DNS server: %v, %v", info, err)
	}
	if f.Called(knotInContainer + "keymgr") {
		t.Errorf("a zone with no DNSSEC needs no key: %v", f.Calls)
	}
	unitDir, _ := root.Host.UnitDir(host.DnsUser)
	unit, _ := os.ReadFile(filepath.Join(unitDir, quadlet.DnsUnitName))
	for _, want := range []string{"Image=ghcr.io/cloudfactory/dns:0.2.0\n", "PublishPort=8053:8053/udp\n", "PublishPort=8053:8053/tcp\n", "Volume=" + root.dnsConfigDir() + ":/etc/knot:ro\n", "DropCapability=all\n", "--runtime"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("no %q in the unit:\n%s", want, unit)
		}
	}
	zone, _ := os.ReadFile(filepath.Join(root.dnsZonesDir(), "example.com.zone"))
	if !strings.Contains(string(zone), "@ 3600 IN MX 10 mx1.mail.example.\n") {
		t.Errorf("zone file:\n%s", zone)
	}
	conf, _ := os.ReadFile(filepath.Join(root.dnsConfigDir(), "knot.conf"))
	for _, want := range []string{"    listen: 0.0.0.0@8053\n", "zone:\n  - domain: example.com.\n", "    storage: \"/etc/knot/zones\"\n", "    rate-limit: 50\n"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("no %q in knot.conf:\n%s", want, conf)
		}
	}
	if strings.Contains(string(conf), "template: signed") {
		t.Errorf("the zone has no DNSSEC:\n%s", conf)
	}
	fw, _ := os.ReadFile(d.FirewallFile)
	if strings.Count(string(fw), dnsRuleMarker) != 2 {
		t.Errorf("firewall:\n%s", fw)
	}
	if info, err := os.Stat(root.dnsStateFile()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("only root reads the list of the zones: %v %v", info, err)
	}
	if !d.DnsEnabled(context.Background()) {
		t.Error("the server has a DNS server now")
	}
	// The report gives the serial that the DNS server loaded, so that the panel knows what
	// this server answers with.
	if zones := d.DnsZones(context.Background()); len(zones) != 1 || zones[0].Name != "example.com" || zones[0].Serial != exampleZone().Serial {
		t.Errorf("zones in the report: %#v", zones)
	}

	// An older change that comes later does not replace the zone.
	older := exampleZone()
	older.Serial--
	older.Records = older.Records[:3]
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(older)}})); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(root.dnsZonesDir(), "example.com.zone")); string(after) != string(zone) {
		t.Errorf("an older zone replaced the zone:\n%s", after)
	}

	// The removal takes the zone out of the configuration, and removes its file.
	f.Calls = nil
	if err := run(t)(d.DeleteDnsZones(&agentv1.DeleteDnsZonesRequest{JobId: jobID, Names: []string{"example.com", "not-here.example"}})); err != nil {
		t.Fatal(err)
	}
	if !f.Called(knotInContainer + "knotc -c /etc/knot/knot.conf -f zone-purge +orphan example.com.") {
		t.Errorf("calls: %v", f.Calls)
	}
	if f.Called(knotInContainer + "knotc -c /etc/knot/knot.conf -f zone-purge +orphan not-here.example.") {
		t.Errorf("the server did not have this zone: %v", f.Calls)
	}
	if _, err := os.Stat(filepath.Join(root.dnsZonesDir(), "example.com.zone")); !os.IsNotExist(err) {
		t.Error("the zone file is still there")
	}
	conf, _ = os.ReadFile(filepath.Join(root.dnsConfigDir(), "knot.conf"))
	if strings.Contains(string(conf), "example.com") {
		t.Errorf("knot.conf:\n%s", conf)
	}
}

func TestSetDnsZonesImportsTheKeyOnlyOnce(t *testing.T) {
	d, f := dnsDeps(t)
	root := rootOf(d)
	z := exampleZone()
	z.Key, z.KeyTag = testDnssecKey(t)
	const listRule = 0
	f.Rules = []execx.Rule{
		{Prefix: knotInContainer + "keymgr -c /etc/knot/knot.conf example.com. list"},
		{Prefix: knotInContainer + "knotc -c /etc/knot/knot.conf zone-status", Output: "[example.com.] serial: 1794000001\n"},
		{Prefix: knotInContainer + "keymgr -c /etc/knot/knot.conf example.com. import-pem", Do: func() error {
			// The key file is there while Knot reads it, and only the DNS user can read it.
			info, err := os.Stat(filepath.Join(root.dnsImportDir(), "example.com.pem"))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("key file: %v %v", info, err)
			}
			// Knot has the new key now, and a key from an earlier time.
			f.Rules[listRule].Output = "0123456789abcdef0123456789abcdef01234567 " + fmtInt(z.KeyTag) + " CSK ECDSAP256SHA256 publish=1 ready=1 active=1\n" +
				"1111111111111111111111111111111111111111 4242 CSK ECDSAP256SHA256 publish=1 ready=1 active=1\n"
			return nil
		}},
	}
	// Lchown needs root. The test replaces only the owner of the file.
	if os.Geteuid() != 0 {
		t.Skip("the test changes the owner of the key file, which needs root")
	}
	both(d, func(d *Deps) { d.Host.LookupGID = func(string) (int, error) { return 987, nil } })
	request := &agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(z)}}
	if err := run(t)(d.SetDnsZones(request)); err != nil {
		t.Fatal(err)
	}
	if !f.Called(knotInContainer+"keymgr -c /etc/knot/knot.conf example.com. import-pem /var/lib/knot/import/example.com.pem ksk=yes zsk=yes algorithm=13") ||
		// A key from an earlier time goes.
		!f.Called(knotInContainer+"keymgr -c /etc/knot/knot.conf example.com. delete 1111111111111111111111111111111111111111") ||
		f.Called(knotInContainer+"keymgr -c /etc/knot/knot.conf example.com. delete 0123") {
		t.Errorf("calls: %v", f.Calls)
	}
	if _, err := os.Stat(filepath.Join(root.dnsImportDir(), "example.com.pem")); !os.IsNotExist(err) {
		t.Error("the key file is still there")
	}
	conf, _ := os.ReadFile(filepath.Join(root.dnsConfigDir(), "knot.conf"))
	if !strings.Contains(string(conf), "  - domain: example.com.\n    template: signed\n") {
		t.Errorf("knot.conf:\n%s", conf)
	}
	// The state does not have the key.
	if state, _ := os.ReadFile(root.dnsStateFile()); strings.Contains(string(state), "PRIVATE") {
		t.Errorf("state:\n%s", state)
	}

	// The second job has the same key: Knot has it already.
	f.Rules[listRule].Output = strings.SplitAfter(f.Rules[listRule].Output, "\n")[0]
	f.Calls = nil
	if err := run(t)(d.SetDnsZones(request)); err != nil {
		t.Fatal(err)
	}
	if f.Called(knotInContainer+"keymgr -c /etc/knot/knot.conf example.com. import-pem") || f.Called(knotInContainer+"keymgr -c /etc/knot/knot.conf example.com. delete") {
		t.Errorf("calls: %v", f.Calls)
	}

	// DNSSEC is off: the keys and the signatures go.
	z.Key, z.KeyTag, z.Serial = "", 0, z.Serial+1
	f.Calls = nil
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(z)}})); err != nil {
		t.Fatal(err)
	}
	if !f.Called(knotInContainer+"keymgr -c /etc/knot/knot.conf example.com. delete 0123456789abcdef0123456789abcdef01234567") ||
		!f.Called(knotInContainer+"knotc -c /etc/knot/knot.conf -f zone-purge example.com. +journal +timers +kaspdb") {
		t.Errorf("calls: %v", f.Calls)
	}
}

// The worker cannot use the DNS user, and the root helper checks each zone again.
func TestTheWorkerCannotControlTheDnsServer(t *testing.T) {
	d, f := dnsDeps(t)
	both(d, func(d *Deps) {
		d.Host.LookupUID = func(name string) (int, error) {
			switch name {
			case host.DnsUser:
				return 987, nil
			case "acme_x7k2":
				return 20001, nil
			}
			return 0, user.UnknownUserError(name)
		}
	})
	for _, command := range []string{
		knotInContainer + "knotc conf-set server.listen 0.0.0.0@53",
		knotInContainer + "sh",
		"runuser -u cf-dns -- env XDG_RUNTIME_DIR=/run/user/987 podman run --rm docker.io/library/alpine",
		"runuser -u cf-dns -- sh -c id",
		"systemctl --user --machine=cf-dns@ set-property --runtime cf-dns.service MemoryMax=1",
	} {
		if _, err := d.Run.Run(context.Background(), words(command)[0], words(command)[1:]...); err == nil {
			t.Errorf("the root helper ran %q", command)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("the root helper ran: %v", f.Calls)
	}
	bad := exampleZone()
	bad.Records[2].Value = "203.0.113.10\n$INCLUDE /etc/shadow"
	path := exampleZone()
	path.Name = "../../etc/cron.d/x"
	for name, tc := range map[string]struct {
		op string
		in any
	}{
		"a record with a directive":  {opDnsWrite, dnsWriteIn{Zones: []dnsZone{bad}}},
		"a zone name with a path":    {opDnsWrite, dnsWriteIn{Zones: []dnsZone{path}}},
		"no zone":                    {opDnsWrite, dnsWriteIn{}},
		"a zone two times":           {opDnsWrite, dnsWriteIn{Zones: []dnsZone{exampleZone(), exampleZone()}}},
		"load a name with a path":    {opDnsLoad, dnsNamesIn{Names: []string{"../x"}}},
		"load a name with an option": {opDnsLoad, dnsNamesIn{Names: []string{"-f"}}},
		"remove a name with a path":  {opDnsRemove, dnsNamesIn{Names: []string{"/etc"}}},
		"a zone before the start":    {opDnsWrite, dnsWriteIn{Zones: []dnsZone{exampleZone()}}},
	} {
		if err := d.Gate.Call(context.Background(), tc.op, tc.in, nil); err == nil {
			t.Errorf("%s: the root helper did the operation", name)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("the root helper ran: %v", f.Calls)
	}
	// The status service of the worker reads the state of the DNS service.
	if _, err := d.Run.Run(context.Background(), "systemctl", "--user", "--machine=cf-dns@", "is-active", "cf-dns.service"); err != nil {
		t.Error(err)
	}
}

func TestReportAddresses(t *testing.T) {
	d, _, _ := direct(t)
	d.InterfaceAddrs = func() ([]net.Addr, error) {
		var out []net.Addr
		for _, cidr := range []string{"127.0.0.1/8", "10.0.0.5/24", "203.0.113.10/24", "169.254.77.1/32", "100.64.0.9/10", "::1/128", "fe80::1/64", "fd00::1/64", "2001:db8::10/64", "203.0.113.10/32"} {
			ip, n, _ := net.ParseCIDR(cidr)
			n.IP = ip
			out = append(out, n)
		}
		return out, nil
	}
	if got := strings.Join(d.ReportAddresses(), " "); got != "2001:db8::10 203.0.113.10" {
		t.Errorf("addresses: %q", got)
	}
	// A server behind NAT: the addresses of the settings.
	d.PublicAddresses = []string{"198.51.100.4"}
	if got := strings.Join(d.ReportAddresses(), " "); got != "198.51.100.4" {
		t.Errorf("addresses: %q", got)
	}
}

// A zone that the DNS server could not load stays in the report with the serial of the last
// good load. The panel then sends the zone again.
func TestTheReportHasTheSerialThatTheDnsServerLoaded(t *testing.T) {
	d, f := dnsDeps(t)
	os.WriteFile(d.FirewallFile, []byte(firewallWithEdge), 0o644)
	status := execx.Rule{Prefix: knotInContainer + "knotc -c /etc/knot/knot.conf zone-status", Output: "[example.com.] serial: 1794000000\n"}
	f.Rules = []execx.Rule{status}
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(exampleZone())}})); err != nil {
		t.Fatal(err)
	}
	newer := exampleZone()
	newer.Serial++
	f.Rules = []execx.Rule{{Prefix: knotInContainer + "knotc -c /etc/knot/knot.conf -b zone-reload", Err: errors.New("operation timed out")}, status}
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(newer)}})); err == nil {
		t.Fatal("the job must fail when the DNS server does not load the zone")
	}
	if zones := d.DnsZones(context.Background()); len(zones) != 1 || zones[0].Serial != exampleZone().Serial {
		t.Errorf("the report must have the serial of the last good load: %#v", zones)
	}
	// The panel sends the same zone again, and the DNS server loads it.
	f.Rules = []execx.Rule{status}
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(newer)}})); err != nil {
		t.Fatal(err)
	}
	if zones := d.DnsZones(context.Background()); len(zones) != 1 || zones[0].Serial != newer.Serial {
		t.Errorf("zones in the report: %#v", zones)
	}
}

// Adversarial review, finding G7-01: the DNS container can write its data directory. Root does
// not follow a symbolic link there.
func TestDnsDirsRefuseASymbolicLinkOfTheContainer(t *testing.T) {
	d, _ := dnsDeps(t)
	root := rootOf(d)
	target := t.TempDir()
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.dnsDataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root.dnsImportDir()); err != nil {
		t.Fatal(err)
	}
	if err := root.makeDnsDirs(context.Background()); err == nil {
		t.Error("the agent used a symbolic link as the import directory")
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("the target of the link changed: %v, %v", info, err)
	}
}
