package actions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const (
	// Two TSIG secrets of 32 bytes in base64.
	testSecret      = "c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0MTI="
	testOtherSecret = "b3RoZXJzZWNyZXRvdGhlcnNlY3JldG90aGVyc2VjcjE="
	secondaryID     = "01JD2M4W3H6YA8C0VVV1Q8X0ZR"
)

func exampleSecondaries() *agentv1.SetDnsSecondariesRequest {
	return &agentv1.SetDnsSecondariesRequest{JobId: jobID, CatalogZone: "catalog.cloudfactory.invalid", Version: 3, Secondaries: []*agentv1.DnsSecondary{
		{Id: secondaryID, Addresses: []string{"192.0.2.53", "2001:db8::53"}, Tsig: &agentv1.TsigKey{Name: "cf-anycast", Algorithm: "hmac-sha256", Secret: testSecret}, Notify: true, Catalog: true},
		{Id: "01JD2M4W3H6YA8C0VVV1Q8X0ZS", Addresses: []string{"10.0.0.53"}},
	}}
}

func TestSetDnsSecondariesWritesTheKeysTheTransfersAndTheCatalogZone(t *testing.T) {
	d, f := dnsDeps(t)
	root := rootOf(d)
	os.WriteFile(d.FirewallFile, []byte(firewallWithEdge), 0o644)
	f.Rules = []execx.Rule{{Prefix: knotInContainer + "knotc -c /etc/knot/knot.conf zone-status", Output: "[example.com.] serial: 1794000000\n"}}
	z := exampleZone()
	z.Transfers = &dnsTransfers{Addresses: []string{"198.51.100.53"}, Notify: true, Tsig: &dnsTsig{Name: "transfer.example.com", Algorithm: "hmac-sha512", Secret: testOtherSecret}}
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(z)}})); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	if err := run(t)(d.SetDnsSecondaries(exampleSecondaries())); err != nil {
		t.Fatal(err)
	}
	// Knot puts the zones in a new catalog zone only when it starts.
	for _, want := range []string{"systemctl --user --machine=cf-dns@ restart cf-dns.service", knotInContainer + "knotc -c /etc/knot/knot.conf zone-notify"} {
		if !f.Called(want) {
			t.Errorf("no call %q: %v", want, f.Calls)
		}
	}
	// The same catalog zone again: a reload is sufficient.
	f.Calls = nil
	if err := run(t)(d.SetDnsSecondaries(exampleSecondaries())); err != nil {
		t.Fatal(err)
	}
	if !f.Called(knotInContainer+"knotc -c /etc/knot/knot.conf reload") || f.Called("systemctl --user --machine=cf-dns@ restart cf-dns.service") {
		t.Errorf("a reload, and no restart: %v", f.Calls)
	}
	conf, _ := os.ReadFile(filepath.Join(root.dnsConfigDir(), "knot.conf"))
	for _, want := range []string{
		"include: \"/etc/knot/keys.conf\"\n",
		"remote:\n  - id: secondary-01jd2m4w3h6ya8c0vvv1q8x0zr\n    address: [192.0.2.53, 2001:db8::53]\n    key: cf-anycast\n  - id: zone-example.com\n    address: [198.51.100.53]\n    key: transfer.example.com\n",
		"acl:\n  - id: secondary-01jd2m4w3h6ya8c0vvv1q8x0zr\n    address: [192.0.2.53, 2001:db8::53]\n    key: cf-anycast\n    action: transfer\n  - id: secondary-01jd2m4w3h6ya8c0vvv1q8x0zs\n    address: [10.0.0.53]\n    action: transfer\n",
		"    acl: [secondary-01jd2m4w3h6ya8c0vvv1q8x0zr, secondary-01jd2m4w3h6ya8c0vvv1q8x0zs]\n    notify: [secondary-01jd2m4w3h6ya8c0vvv1q8x0zr]\n    catalog-role: member\n    catalog-zone: catalog.cloudfactory.invalid.\n",
		"zone:\n  - domain: catalog.cloudfactory.invalid.\n    template: catalog\n    catalog-role: generate\n    acl: [secondary-01jd2m4w3h6ya8c0vvv1q8x0zr]\n    notify: [secondary-01jd2m4w3h6ya8c0vvv1q8x0zr]\n",
		"  - domain: example.com.\n    acl: [secondary-01jd2m4w3h6ya8c0vvv1q8x0zr, secondary-01jd2m4w3h6ya8c0vvv1q8x0zs, zone-example.com]\n    notify: [secondary-01jd2m4w3h6ya8c0vvv1q8x0zr, zone-example.com]\n",
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("no %q in knot.conf:\n%s", want, conf)
		}
	}
	// The secrets are only in keys.conf, which other users cannot read.
	if strings.Contains(string(conf), testSecret) {
		t.Errorf("knot.conf has a secret:\n%s", conf)
	}
	keys, _ := os.ReadFile(filepath.Join(root.dnsConfigDir(), "keys.conf"))
	for _, want := range []string{"  - id: cf-anycast\n    algorithm: hmac-sha256\n    secret: " + testSecret + "\n", "  - id: transfer.example.com\n    algorithm: hmac-sha512\n    secret: " + testOtherSecret + "\n"} {
		if !strings.Contains(string(keys), want) {
			t.Errorf("no %q in keys.conf:\n%s", want, keys)
		}
	}
	if info, err := os.Stat(filepath.Join(root.dnsConfigDir(), "keys.conf")); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("keys.conf: %v %v", info, err)
	}
	// The report has the version of the list, so that the panel can send it again.
	if got := root.dnsStatus(context.Background()).Secondaries; got != 3 {
		t.Errorf("the version in the report: %d", got)
	}

	// With no secondaries, the configuration has no transfers of the panel and no catalog zone.
	if err := run(t)(d.SetDnsSecondaries(&agentv1.SetDnsSecondariesRequest{JobId: jobID, Version: 4})); err != nil {
		t.Fatal(err)
	}
	conf, _ = os.ReadFile(filepath.Join(root.dnsConfigDir(), "knot.conf"))
	if strings.Contains(string(conf), "secondary-") || strings.Contains(string(conf), "catalog") {
		t.Errorf("knot.conf with no secondaries:\n%s", conf)
	}
	if !strings.Contains(string(conf), "    acl: [zone-example.com]\n    notify: [zone-example.com]\n") {
		t.Errorf("the transfers of the zone stay:\n%s", conf)
	}
}

func TestSecondariesAndTransfersOutsideTheRules(t *testing.T) {
	d, _ := dnsDeps(t)
	key := &agentv1.TsigKey{Name: "cf-anycast", Algorithm: "hmac-sha256", Secret: testSecret}
	for name, req := range map[string]*agentv1.SetDnsSecondariesRequest{
		"no address":         {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID}}},
		"not an address":     {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"ns1.example.net"}}}},
		"not the usual form": {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"2001:DB8::53"}}}},
		"a line in a list":   {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1]\nacl:"}}}},
		"multicast":          {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"224.0.0.1"}}}},
		"same address":       {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1", "192.0.2.1"}}}},
		"an ID":              {Secondaries: []*agentv1.DnsSecondary{{Id: "../x", Addresses: []string{"192.0.2.1"}}}},
		"two times":          {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}}, {Id: secondaryID, Addresses: []string{"192.0.2.2"}}}},
		"a key name":         {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Tsig: &agentv1.TsigKey{Name: "Key\nx", Algorithm: "hmac-sha256", Secret: testSecret}}}},
		"a key of a zone":    {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Tsig: &agentv1.TsigKey{Name: "transfer.example.com", Algorithm: "hmac-sha256", Secret: testSecret}}}},
		"an algorithm":       {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Tsig: &agentv1.TsigKey{Name: "k", Algorithm: "hmac-md5", Secret: testSecret}}}},
		"a short secret":     {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Tsig: &agentv1.TsigKey{Name: "k", Algorithm: "hmac-sha256", Secret: "c2hvcnQ="}}}},
		"the same key":       {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Tsig: key}, {Id: "01JD2M4W3H6YA8C0VVV1Q8X0ZS", Addresses: []string{"192.0.2.2"}, Tsig: key}}},
		"no catalog name":    {Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Catalog: true}}},
		"a catalog name":     {CatalogZone: "catalog..invalid", Secondaries: []*agentv1.DnsSecondary{{Id: secondaryID, Addresses: []string{"192.0.2.1"}, Catalog: true}}},
		"a negative version": {Version: -1},
	} {
		req.JobId = jobID
		if _, err := d.SetDnsSecondaries(req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// The secondaries of the owner of a zone have public addresses, and the key of the zone.
	for name, tr := range map[string]*dnsTransfers{
		"loopback":        {Addresses: []string{"127.0.0.1"}},
		"private":         {Addresses: []string{"10.1.2.3"}},
		"private IPv6":    {Addresses: []string{"fd00::1"}},
		"link-local":      {Addresses: []string{"169.254.169.254"}},
		"shared":          {Addresses: []string{"100.64.0.1"}},
		"IPv4 in IPv6":    {Addresses: []string{"::ffff:10.0.0.1"}},
		"6to4":            {Addresses: []string{"2002:a00:1::1"}},
		"a different key": {Addresses: []string{"198.51.100.53"}, Tsig: &dnsTsig{Name: "transfer.example.org", Algorithm: "hmac-sha256", Secret: testSecret}},
		"too many":        {Addresses: []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5", "198.51.100.6", "198.51.100.7", "198.51.100.8", "198.51.100.9", "198.51.100.10", "198.51.100.11"}},
	} {
		z := exampleZone()
		z.Transfers = tr
		if _, err := d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(z)}}); err == nil {
			t.Errorf("transfers, %s: no error", name)
		}
	}
}

// The catalog zone cannot have the name of a zone of a customer.
func TestTheCatalogZoneHasItsOwnName(t *testing.T) {
	d, f := dnsDeps(t)
	os.WriteFile(d.FirewallFile, []byte(firewallWithEdge), 0o644)
	f.Rules = []execx.Rule{{Prefix: knotInContainer + "knotc -c /etc/knot/knot.conf zone-status", Output: "[example.com.] serial: 1794000000\n"}}
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(exampleZone())}})); err != nil {
		t.Fatal(err)
	}
	req := exampleSecondaries()
	req.CatalogZone = "example.com"
	if err := run(t)(d.SetDnsSecondaries(req)); err == nil {
		t.Error("the catalog zone has the name of a zone")
	}
	if err := run(t)(d.SetDnsSecondaries(exampleSecondaries())); err != nil {
		t.Fatal(err)
	}
	z := exampleZone()
	z.Name = "catalog.cloudfactory.invalid"
	z.Serial++
	if err := run(t)(d.SetDnsZones(&agentv1.SetDnsZonesRequest{JobId: jobID, Zones: []*agentv1.DnsZone{protoZone(z)}})); err == nil {
		t.Error("a zone has the name of the catalog zone")
	}
}
