package actions

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Zone transfers to secondary DNS servers outside the panel. The DNS servers of the panel are
// the primary servers: they get each zone from the panel, and a secondary copies it with AXFR
// or IXFR. Only an address of a secondary can ask for a transfer, and a TSIG key signs the
// transfer when the secondary has one.
//
//   - The secondaries of the panel (SetDnsSecondaries) copy all zones. A secondary with
//     "catalog" also copies the catalog zone (RFC 9432), the list of the zones, so that it
//     finds a new zone with no work of an administrator.
//   - The owner of a zone can give more secondaries for the zone (DnsZone.transfers). Their
//     addresses must be public addresses.
//
// The DNS server sends NOTIFY to a secondary with "notify" after each change of a zone. The
// TSIG secrets are in keys.conf, which only root and the DNS user can read.

// transferKeyPrefix starts the name of the TSIG key of the transfers of one zone.
const transferKeyPrefix = "transfer."

type (
	// dnsTsig is a TSIG key: its name, its algorithm and its secret in base64.
	dnsTsig struct {
		Name      string `json:"name"`
		Algorithm string `json:"algorithm"`
		Secret    string `json:"secret"`
	}
	// dnsTransfers are the transfers of one zone to the secondaries of the owner of the zone.
	dnsTransfers struct {
		Addresses []string `json:"addresses"`
		Tsig      *dnsTsig `json:"tsig,omitempty"`
		Notify    bool     `json:"notify,omitempty"`
	}
	// dnsSecondary is a secondary of the panel. It copies all zones.
	dnsSecondary struct {
		ID        string   `json:"id"`
		Addresses []string `json:"addresses"`
		Tsig      *dnsTsig `json:"tsig,omitempty"`
		Notify    bool     `json:"notify,omitempty"`
		Catalog   bool     `json:"catalog,omitempty"`
	}
	dnsSecondariesIn struct {
		Secondaries []dnsSecondary
		Catalog     string
		Version     int64
	}
)

func tsigFrom(k *agentv1.TsigKey) *dnsTsig {
	if k == nil || (k.GetName() == "" && k.GetSecret() == "") {
		return nil
	}
	return &dnsTsig{Name: k.GetName(), Algorithm: k.GetAlgorithm(), Secret: k.GetSecret()}
}

func transfersFrom(t *agentv1.DnsTransfers) *dnsTransfers {
	if t == nil || len(t.GetAddresses()) == 0 {
		return nil
	}
	return &dnsTransfers{Addresses: t.GetAddresses(), Tsig: tsigFrom(t.GetTsig()), Notify: t.GetNotify()}
}

// SetDnsSecondaries sets the secondaries that copy all the zones of this server, and starts
// the DNS server if it does not operate.
func (d *Deps) SetDnsSecondaries(req *agentv1.SetDnsSecondariesRequest) (jobs.Job, error) {
	in := dnsSecondariesIn{Catalog: req.GetCatalogZone(), Version: req.GetVersion()}
	for _, s := range req.GetSecondaries() {
		in.Secondaries = append(in.Secondaries, dnsSecondary{ID: s.GetId(), Addresses: s.GetAddresses(), Tsig: tsigFrom(s.GetTsig()), Notify: s.GetNotify(), Catalog: s.GetCatalog()})
	}
	if err := errors.Join(validate.ID(req.GetJobId()), checkDnsSecondaries(in)); err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{ID: req.GetJobId(), Key: dnsKey, Steps: []jobs.Step{
		{Name: "Start DNS server", Do: func(ctx context.Context) error { return d.startDns(ctx) }},
		{Name: "Set secondary DNS servers", Do: func(ctx context.Context) error { return d.writeDnsSecondaries(ctx, in) }},
	}}, nil
}

// checkTsig checks a TSIG key.
func checkTsig(k *dnsTsig) error {
	if k == nil {
		return nil
	}
	if err := errors.Join(validate.TSIGName(k.Name), validate.TSIGAlgorithm(k.Algorithm), validate.TSIGSecret(k.Secret)); err != nil {
		return err
	}
	b, err := base64.StdEncoding.DecodeString(k.Secret)
	if err != nil || len(b) < 16 || len(b) > 64 {
		return errors.New("the TSIG secret is not 16 to 64 bytes in base64")
	}
	return nil
}

// publicAddress reports whether an address is on the public internet. An address of the
// owner of a zone must be public, so that the DNS server does not send NOTIFY into a private
// network.
func publicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() || a.IsLinkLocalMulticast() {
		return false
	}
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}

// checkAddresses checks the addresses of a secondary: 1 to MaxDnsTransferAddresses addresses
// in their usual form, each one time. With "public", each address must be public.
func checkAddresses(addresses []string, public bool) error {
	if len(addresses) == 0 || len(addresses) > validate.MaxDnsTransferAddresses {
		return fmt.Errorf("a secondary has 1 to %d addresses", validate.MaxDnsTransferAddresses)
	}
	for _, s := range addresses {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" || a.String() != s {
			return fmt.Errorf("%q is not an IP address in its usual form", s)
		}
		if a.IsUnspecified() || a.IsMulticast() {
			return fmt.Errorf("%s is not the address of a server", s)
		}
		if public && !publicAddress(a) {
			return fmt.Errorf("%s is not a public address", s)
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(addresses)))) != len(addresses) {
		return errors.New("a secondary has each address only one time")
	}
	return nil
}

// checkTransfers checks the transfers of one zone.
func checkTransfers(zone string, t *dnsTransfers) error {
	if t == nil {
		return nil
	}
	if err := checkAddresses(t.Addresses, true); err != nil {
		return err
	}
	if t.Tsig != nil && t.Tsig.Name != transferKeyPrefix+zone {
		return fmt.Errorf("the TSIG key of the transfers of %s must have the name %s%s", zone, transferKeyPrefix, zone)
	}
	return checkTsig(t.Tsig)
}

// checkDnsSecondaries checks the secondaries of the panel. The root helper does this check again.
func checkDnsSecondaries(in dnsSecondariesIn) error {
	if len(in.Secondaries) > validate.MaxDnsSecondaries {
		return fmt.Errorf("the panel has at most %d secondary DNS servers", validate.MaxDnsSecondaries)
	}
	if in.Version < 0 {
		return errors.New("the version is not 0 or more")
	}
	ids := map[string]bool{}
	keys := map[string]bool{}
	catalog := false
	for _, s := range in.Secondaries {
		if err := validate.ID(s.ID); err != nil {
			return err
		}
		if ids[strings.ToLower(s.ID)] {
			return fmt.Errorf("the secondary %s is in the list two times", s.ID)
		}
		ids[strings.ToLower(s.ID)] = true
		if err := errors.Join(checkAddresses(s.Addresses, false), checkTsig(s.Tsig)); err != nil {
			return fmt.Errorf("secondary %s: %w", s.ID, err)
		}
		if s.Tsig != nil {
			// The keys of the zones have their own names.
			if strings.HasPrefix(s.Tsig.Name, transferKeyPrefix) {
				return fmt.Errorf("secondary %s: the name of a TSIG key cannot start with %q", s.ID, transferKeyPrefix)
			}
			if keys[s.Tsig.Name] {
				return fmt.Errorf("secondary %s: two secondaries have the TSIG key %s", s.ID, s.Tsig.Name)
			}
			keys[s.Tsig.Name] = true
		}
		catalog = catalog || s.Catalog
	}
	if in.Catalog != "" {
		if err := validate.Domain(in.Catalog); err != nil {
			return fmt.Errorf("the catalog zone: %w", err)
		}
	} else if catalog {
		return errors.New("a secondary copies the catalog zone, and the catalog zone has no name")
	}
	return nil
}

// writeDnsSecondaries keeps the secondaries, writes the configuration and loads it. Each zone
// then sends NOTIFY to the secondaries with "notify", so that a new secondary copies the
// zones now.
func (d *Deps) writeDnsSecondaries(ctx context.Context, in dnsSecondariesIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDnsSecondaries, in, nil)
	}
	if err := checkDnsSecondaries(in); err != nil {
		return err
	}
	d.dnsMu.Lock()
	defer d.dnsMu.Unlock()
	st, ok, err := d.readDnsState()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("DNS: the DNS server of this server did not start")
	}
	if _, hosted := st.Zones[in.Catalog]; hosted {
		return fmt.Errorf("the catalog zone %s has the name of a zone of a customer", in.Catalog)
	}
	before := st.catalogOn()
	catalogChanged := before != (dnsState{Secondaries: in.Secondaries, Catalog: in.Catalog}).catalogOn() || (before && st.Catalog != in.Catalog)
	st.Secondaries, st.Catalog, st.SecondariesVersion = in.Secondaries, in.Catalog, in.Version
	if _, err := d.writeKnotConf(st); err != nil {
		return err
	}
	if err := d.writeDnsState(st); err != nil {
		return err
	}
	if catalogChanged && len(st.Zones) > 0 {
		// Knot reads the catalog role of a zone only when it makes the zone. A reload does
		// not put the zones in a new catalog zone, or take them out of an old one.
		if err := d.restartDns(ctx); err != nil {
			return err
		}
	} else if _, err := d.knot(ctx, "knotc", "reload"); err != nil {
		return err
	}
	if len(st.Zones) > 0 && slices.ContainsFunc(st.Secondaries, func(s dnsSecondary) bool { return s.Notify }) {
		// A NOTIFY that does not arrive is not an error: the secondary also asks for the SOA
		// record at the refresh time of the zone.
		if _, err := d.knot(ctx, "knotc", "zone-notify"); err != nil {
			return err
		}
	}
	return nil
}

// restartDns restarts the DNS server and waits until it answers. The DNS server does not
// answer for a short time.
func (d *Deps) restartDns(ctx context.Context) error {
	if d.DnsTest != nil {
		if d.DnsTest.Restart == nil {
			return errors.New("DNS: the test cannot restart the DNS server")
		}
		if err := d.DnsTest.Restart(ctx); err != nil {
			return err
		}
	} else if _, err := d.Host.Systemctl(ctx, host.DnsUser, "restart", dnsServiceName); err != nil {
		return err
	}
	if err := d.waitDns(ctx); err != nil {
		return fmt.Errorf("DNS: the DNS server does not answer: %w", err)
	}
	return nil
}

// catalogOn reports whether the DNS server makes the catalog zone.
func (st dnsState) catalogOn() bool {
	return st.Catalog != "" && slices.ContainsFunc(st.Secondaries, func(s dnsSecondary) bool { return s.Catalog })
}

// knotKeys gives keys.conf: the TSIG keys of the secondaries and of the zones.
func knotKeys(st dnsState) string {
	var b strings.Builder
	b.WriteString("# Managed by the Cloudfactory agent. Only root and the DNS user can read this file.\n")
	var keys []*dnsTsig
	for _, s := range st.Secondaries {
		if s.Tsig != nil {
			keys = append(keys, s.Tsig)
		}
	}
	for _, name := range slices.Sorted(mapKeys(st.Zones)) {
		if t := st.Zones[name].Transfers; t != nil && t.Tsig != nil {
			keys = append(keys, t.Tsig)
		}
	}
	if len(keys) == 0 {
		return b.String()
	}
	b.WriteString("key:\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  - id: %s\n    algorithm: %s\n    secret: %s\n", k.Name, k.Algorithm, k.Secret)
	}
	return b.String()
}

// remoteAddress gives the address of a secondary in knot.conf. A test can send NOTIFY to a
// different port.
func remoteAddress(a string, port int) string {
	if port == 0 {
		return a
	}
	return fmt.Sprintf("%s@%d", a, port)
}

// knotTransfers gives the parts of knot.conf for the transfers: the remotes for NOTIFY, the
// ACLs of the transfers, and the ACL and NOTIFY lists of the templates and of each zone.
type knotTransfers struct {
	text string
	// acl and notify are the lists of the templates: the secondaries of the panel.
	acl, notify []string
	// zoneACL and zoneNotify are the lists of the zones with their own transfers.
	zoneACL, zoneNotify map[string][]string
	// catalogACL and catalogNotify are the lists of the catalog zone.
	catalogACL, catalogNotify []string
}

func transfersConf(st dnsState, notifyPort int) knotTransfers {
	out := knotTransfers{zoneACL: map[string][]string{}, zoneNotify: map[string][]string{}}
	var remotes, acls strings.Builder
	write := func(b *strings.Builder, id string, addresses []string, key *dnsTsig, port int, action string) {
		list := make([]string, len(addresses))
		for i, a := range addresses {
			list[i] = remoteAddress(a, port)
		}
		fmt.Fprintf(b, "  - id: %s\n    address: [%s]\n", id, strings.Join(list, ", "))
		if key != nil {
			fmt.Fprintf(b, "    key: %s\n", key.Name)
		}
		if action != "" {
			fmt.Fprintf(b, "    action: %s\n", action)
		}
	}
	for _, s := range st.Secondaries {
		id := "secondary-" + strings.ToLower(s.ID)
		write(&acls, id, s.Addresses, s.Tsig, 0, "transfer")
		out.acl = append(out.acl, id)
		if s.Catalog {
			out.catalogACL = append(out.catalogACL, id)
		}
		if s.Notify {
			write(&remotes, id, s.Addresses, s.Tsig, notifyPort, "")
			out.notify = append(out.notify, id)
			if s.Catalog {
				out.catalogNotify = append(out.catalogNotify, id)
			}
		}
	}
	for _, name := range slices.Sorted(mapKeys(st.Zones)) {
		t := st.Zones[name].Transfers
		if t == nil {
			continue
		}
		id := "zone-" + name
		write(&acls, id, t.Addresses, t.Tsig, 0, "transfer")
		// A list of a zone replaces the list of its template.
		out.zoneACL[name] = append(slices.Clone(out.acl), id)
		if t.Notify {
			write(&remotes, id, t.Addresses, t.Tsig, notifyPort, "")
			out.zoneNotify[name] = append(slices.Clone(out.notify), id)
		}
	}
	var b strings.Builder
	if remotes.Len() > 0 {
		b.WriteString("remote:\n" + remotes.String())
	}
	if acls.Len() > 0 {
		b.WriteString("acl:\n" + acls.String())
	}
	out.text = b.String()
	return out
}

// list gives a list of knot.conf, for example "[a, b]".
func list(items []string) string {
	return "[" + strings.Join(items, ", ") + "]"
}

// writeKnotKeys writes keys.conf. Root owns it, and the group of the DNS user can read it.
func (d *Deps) writeKnotKeys(st dnsState) error {
	path := filepath.Join(d.dnsConfigDir(), "keys.conf")
	if _, err := host.WriteFile(path, knotKeys(st), 0o640); err != nil {
		return err
	}
	// Only root can give a file to a different group. A test is not root.
	if os.Geteuid() != 0 {
		return nil
	}
	_, gid, err := d.dnsOwner()
	if err != nil {
		return err
	}
	return os.Lchown(path, 0, gid)
}
