package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// DNS hosting. A server with the role "dns" answers the DNS queries for the zones of the
// customers. One container, cf-dns, runs Knot DNS in gVisor as the Unix user cf-dns.
//
// The panel sends each zone, with all its records, to each DNS server (SetDnsZones). The DNS
// servers of the panel do not copy zones between them. A secondary DNS server outside the
// panel can copy the zones with zone transfers (dns_transfers.go).
//
// The root helper does all the work, because it writes the configuration of a service:
//   - It checks each record again, and writes each zone file itself. The worker gives
//     records, never the text of a zone file or of the configuration.
//   - It keeps the list of the zones in a file that only root can read (dnsState).
//   - It runs the tools of Knot in the container, as the user cf-dns. The worker cannot run
//     a program as that user.
//
// The directories, in DataDir/dns:
//   - etc: knot.conf and zones/<name>.zone. Root owns them, and the container reads them.
//   - db: the journal, the timers and the DNSSEC keys of Knot. The user cf-dns owns it.

const (
	// dnsContainer is the name of the DNS container (quadlet.DnsUnit).
	dnsContainer = "cf-dns"
	// dnsServiceName is the service that Quadlet makes from quadlet.DnsUnitName.
	dnsServiceName = "cf-dns.service"
	// dnsRuleMarker marks the firewall rules of the DNS server in the file of the installer.
	dnsRuleMarker = "# cloudfactory-dns"
	// dnsKey serialises the DNS jobs of a server.
	dnsKey = "dns"
)

// dnsRecord and dnsZone are a zone as the worker gives it to the root helper.
type (
	dnsRecord struct {
		Name, Type, Value string
		TTL, Priority     int
	}
	dnsZone struct {
		Name    string
		Serial  int64
		Records []dnsRecord
		// Keys are the DNSSEC keys: none when DNSSEC is off, one key, or two keys during a
		// change of the key.
		Keys []dnssecKey
		// Transfers are the transfers to the secondaries of the owner of the zone, or nil.
		Transfers *dnsTransfers
	}
	// dnssecKey is a DNSSEC key of a zone: the private key in the PEM format, its key tag, and
	// its state (keyActive, keyReady or keySubmit).
	dnssecKey struct {
		Key   string `json:"key,omitempty"`
		Tag   int    `json:"tag"`
		State string `json:"state"`
	}
	dnsWriteIn struct {
		Zones []dnsZone
	}
	dnsNamesIn struct {
		Names []string
	}
	// dnsStatusOut is what the worker knows about the DNS server.
	dnsStatusOut struct {
		// Enabled is true when this server got a zone job. The agent then keeps the DNS server
		// in operation.
		Enabled bool
		// Zones are the zones of this server, for the report to the panel.
		Zones []panel.DNSZoneState
		// Secondaries is the version of the list of the secondaries of this server.
		Secondaries int64
	}
)

// dnsLayout gives the paths of Knot as the tools of Knot see them. In the container they are
// fixed. A test with a Knot server on the host gives the paths of the host.
type dnsLayout struct {
	Conf, Zones, Storage, Run string
	// NotifyPort is the port of the secondaries for NOTIFY. 0 is port 53. Only a test gives a
	// different port.
	NotifyPort int
}

// DnsTest lets a test run Knot on the host, with no container.
type DnsTest struct {
	// Knot runs a tool of Knot, for example "knotc".
	Knot func(ctx context.Context, argv ...string) (string, error)
	// Listen is the address of the server, for example "127.0.0.1@8053".
	Listen []string
	// NotifyPort is the port of the secondaries for NOTIFY.
	NotifyPort int
	// Restart restarts the DNS server.
	Restart func(ctx context.Context) error
}

func (d *Deps) dnsDir() string       { return filepath.Join(d.DataDir, "dns") }
func (d *Deps) dnsConfigDir() string { return filepath.Join(d.dnsDir(), "etc") }
func (d *Deps) dnsZonesDir() string  { return filepath.Join(d.dnsConfigDir(), "zones") }
func (d *Deps) dnsDataDir() string   { return filepath.Join(d.dnsDir(), "db") }
func (d *Deps) dnsImportDir() string { return filepath.Join(d.dnsDataDir(), "import") }
func (d *Deps) dnsStateFile() string { return filepath.Join(d.DataDir, "dns.json") }

func (d *Deps) dnsLayout() dnsLayout {
	if d.DnsTest != nil {
		return dnsLayout{Conf: filepath.Join(d.dnsConfigDir(), "knot.conf"), Zones: d.dnsZonesDir(), Storage: d.dnsDataDir(), Run: filepath.Join(d.dnsDir(), "run"), NotifyPort: d.DnsTest.NotifyPort}
	}
	return dnsLayout{Conf: "/etc/knot/knot.conf", Zones: "/etc/knot/zones", Storage: "/var/lib/knot", Run: "/run/knot"}
}

// knot runs a tool of Knot in the DNS container, as the DNS user. Only the root helper
// calls it.
func (d *Deps) knot(ctx context.Context, tool string, args ...string) (string, error) {
	argv := append([]string{tool, "-c", d.dnsLayout().Conf}, args...)
	if d.DnsTest != nil {
		return d.DnsTest.Knot(ctx, argv...)
	}
	return d.Host.Podman(ctx, host.DnsUser, append([]string{"exec", dnsContainer}, argv...)...)
}

// SetDnsZones writes DNS zones on this server, and starts the DNS server if it does not
// operate. Each zone replaces the zone with the same name.
func (d *Deps) SetDnsZones(req *agentv1.SetDnsZonesRequest) (jobs.Job, error) {
	zones := make([]dnsZone, 0, len(req.GetZones()))
	for _, z := range req.GetZones() {
		zone := dnsZone{Name: z.GetName(), Serial: z.GetSerial(), Transfers: transfersFrom(z.GetTransfers())}
		for _, k := range z.GetDnssecKeys() {
			zone.Keys = append(zone.Keys, dnssecKey{Key: k.GetKey(), Tag: int(k.GetKeyTag()), State: k.GetState()})
		}
		for _, r := range z.GetRecords() {
			zone.Records = append(zone.Records, dnsRecord{Name: r.GetName(), Type: r.GetType(), Value: r.GetValue(), TTL: int(r.GetTtl()), Priority: int(r.GetPriority())})
		}
		zones = append(zones, zone)
	}
	if err := errors.Join(validate.ID(req.GetJobId()), checkDnsZones(zones)); err != nil {
		return jobs.Job{}, err
	}
	names := make([]string, len(zones))
	for i, z := range zones {
		names[i] = z.Name
	}
	return jobs.Job{ID: req.GetJobId(), Key: dnsKey, Steps: []jobs.Step{
		{Name: "Start DNS server", Do: func(ctx context.Context) error { return d.startDns(ctx) }},
		{Name: "Write zones", Do: func(ctx context.Context) error { return d.writeDnsZones(ctx, zones) }},
		{Name: "Load zones", Do: func(ctx context.Context) error { return d.loadDnsZones(ctx, names) }},
	}}, nil
}

// DeleteDnsZones removes DNS zones from this server, with their DNSSEC keys.
func (d *Deps) DeleteDnsZones(req *agentv1.DeleteDnsZonesRequest) (jobs.Job, error) {
	names := req.GetNames()
	if err := errors.Join(validate.ID(req.GetJobId()), checkDnsNames(names)); err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{ID: req.GetJobId(), Key: dnsKey, Steps: []jobs.Step{
		{Name: "Remove zones", Do: func(ctx context.Context) error { return d.removeDnsZones(ctx, names) }},
	}}, nil
}

// checkDnsNames checks a list of zone names from the panel or from the worker.
func checkDnsNames(names []string) error {
	if len(names) == 0 || len(names) > validate.MaxDnsZonesPerJob {
		return fmt.Errorf("a DNS job has 1 to %d zones", validate.MaxDnsZonesPerJob)
	}
	var errs []error
	for _, name := range names {
		errs = append(errs, validate.Domain(name))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	sorted := slices.Sorted(slices.Values(names))
	if len(slices.Compact(sorted)) != len(names) {
		return errors.New("a DNS job has each zone only one time")
	}
	return nil
}

// checkDnsZones checks the zones of a job. The root helper does this check again.
func checkDnsZones(zones []dnsZone) error {
	names := make([]string, len(zones))
	for i, z := range zones {
		names[i] = z.Name
	}
	if err := checkDnsNames(names); err != nil {
		return err
	}
	for _, z := range zones {
		if _, err := renderZone(z); err != nil {
			return fmt.Errorf("DNS zone %s: %w", z.Name, err)
		}
		if err := checkTransfers(z.Name, z.Transfers); err != nil {
			return fmt.Errorf("DNS zone %s: %w", z.Name, err)
		}
	}
	return nil
}

// renderZone checks a zone and gives the text of its zone file. Each value goes through a
// rule of schema/input-rules.json, so no value can add a line or a directive to the file.
//
// The SOA record is from the agent: the primary name server is the first NS record of the
// zone, and the mailbox is hostmaster@<zone>. The times are those of RIPE-203, with a
// negative TTL of 5 minutes.
func renderZone(z dnsZone) (string, error) {
	if err := validate.Domain(z.Name); err != nil {
		return "", err
	}
	if z.Serial <= 0 || z.Serial > math.MaxUint32 {
		return "", errors.New("the serial is not from 1 to 4294967295")
	}
	if len(z.Records) > validate.MaxDnsRecordsPerZone {
		return "", fmt.Errorf("the zone has more than %d records", validate.MaxDnsRecordsPerZone)
	}
	if err := checkDnsKeys(z.Keys); err != nil {
		return "", err
	}
	var lines []string
	primary := ""
	for _, r := range z.Records {
		line, err := renderRecord(z.Name, r)
		if err != nil {
			return "", fmt.Errorf("record %s %s: %w", r.Name, r.Type, err)
		}
		if r.Name == "@" && r.Type == "CNAME" {
			return "", errors.New("the zone itself cannot have a CNAME record")
		}
		if r.Name == "@" && r.Type == "NS" && primary == "" {
			primary = r.Value
		}
		lines = append(lines, line)
	}
	if primary == "" {
		return "", errors.New("the zone has no NS record with the name @")
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	var b strings.Builder
	b.WriteString("; Managed by the Cloudfactory agent. Change the records in the panel.\n")
	fmt.Fprintf(&b, "$ORIGIN %s.\n$TTL 3600\n", z.Name)
	fmt.Fprintf(&b, "@ 3600 IN SOA %s. hostmaster.%s. %d 3600 900 1209600 300\n", primary, z.Name, z.Serial)
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
	return b.String(), nil
}

// renderRecord gives one line of a zone file.
func renderRecord(zone string, r dnsRecord) (string, error) {
	if err := errors.Join(validate.DNSRecordName(r.Name), validate.DNSRecordType(r.Type)); err != nil {
		return "", err
	}
	if r.Name != "@" && len(r.Name)+1+len(zone) > 253 {
		return "", errors.New("the full name has more than 253 characters")
	}
	if r.TTL < validate.MinDnsShortTtl || r.TTL > validate.MaxDnsTtl {
		return "", fmt.Errorf("the TTL is not from %d to %d", validate.MinDnsShortTtl, validate.MaxDnsTtl)
	}
	if r.Priority < 0 || r.Priority > math.MaxUint16 {
		return "", errors.New("the priority is not from 0 to 65535")
	}
	var data string
	switch r.Type {
	case "A", "AAAA":
		rule := validate.IPv4
		if r.Type == "AAAA" {
			rule = validate.IPv6
		}
		if err := rule(r.Value); err != nil {
			return "", err
		}
		// The address in its usual form, from the parser of Go.
		address, err := netip.ParseAddr(r.Value)
		if err != nil || address.Is4() != (r.Type == "A") || address.Zone() != "" {
			return "", fmt.Errorf("%q is not an address for this record type", r.Value)
		}
		data = address.String()
	case "CNAME", "NS":
		if err := validate.DNSHost(r.Value); err != nil {
			return "", err
		}
		data = r.Value + "."
	case "MX":
		if err := validate.DNSHost(r.Value); err != nil {
			return "", err
		}
		data = fmt.Sprintf("%d %s.", r.Priority, r.Value)
	case "SRV":
		if err := validate.DNSSrv(r.Value); err != nil {
			return "", err
		}
		data = fmt.Sprintf("%d %s.", r.Priority, r.Value)
	case "CAA":
		if err := validate.DNSCaa(r.Value); err != nil {
			return "", err
		}
		data = r.Value
	case "TXT":
		if err := validate.DNSTxt(r.Value); err != nil {
			return "", err
		}
		data = quoteTxt(r.Value)
	}
	return fmt.Sprintf("%s %d IN %s %s", r.Name, r.TTL, r.Type, data), nil
}

// quoteTxt gives a text as the strings of a TXT record. One string has at most 255
// characters. The text has only printable ASCII characters (validate.DNSTxt).
func quoteTxt(text string) string {
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	var parts []string
	for len(text) > 0 {
		n := min(len(text), 255)
		parts = append(parts, `"`+escape.Replace(text[:n])+`"`)
		text = text[n:]
	}
	return strings.Join(parts, " ")
}

// dnsState is the list of the zones of this server. Only root reads and writes the file.
type dnsState struct {
	Zones map[string]dnsZoneState `json:"zones"`
	// Secondaries copy all zones. Catalog is the name of the catalog zone. SecondariesVersion
	// is the version of the list in the panel.
	Secondaries        []dnsSecondary `json:"secondaries,omitempty"`
	Catalog            string         `json:"catalog,omitempty"`
	SecondariesVersion int64          `json:"secondariesVersion,omitempty"`
}

type dnsZoneState struct {
	Serial int64 `json:"serial"`
	// Loaded is the serial of the zone that the DNS server loaded last. It is smaller than
	// Serial when the server has the zone file but could not load it.
	Loaded int64 `json:"loaded,omitempty"`
	// Keys are the DNSSEC keys of the zone with their states. The DNS server signs the zone
	// when it has a key.
	Keys []dnssecKey `json:"keys,omitempty"`
	// Transfers are the transfers to the secondaries of the owner of the zone.
	Transfers *dnsTransfers `json:"transfers,omitempty"`
}

// readDnsState gives the zones of this server. ok is false when this server never got a
// zone job. The caller holds dnsMu.
func (d *Deps) readDnsState() (st dnsState, ok bool, err error) {
	b, err := os.ReadFile(d.dnsStateFile())
	if os.IsNotExist(err) {
		return dnsState{Zones: map[string]dnsZoneState{}}, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, false, fmt.Errorf("%s: %w", d.dnsStateFile(), err)
	}
	if st.Zones == nil {
		st.Zones = map[string]dnsZoneState{}
	}
	return st, true, nil
}

func (d *Deps) writeDnsState(st dnsState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	_, err = host.WriteFile(d.dnsStateFile(), string(b)+"\n", 0o600)
	return err
}

// knotConf gives the configuration of Knot for the zones of the state.
//
// A zone with no DNSSEC comes only from its zone file. A signed zone keeps its signatures in
// the journal, and Knot makes its serial: Knot signs the zone again before the signatures
// expire, and each new signature needs a larger serial than the serial of the panel.
func knotConf(l dnsLayout, listen []string, st dnsState) string {
	var b strings.Builder
	b.WriteString("# Managed by the Cloudfactory agent. The agent replaces changes to this file.\n")
	b.WriteString("server:\n")
	fmt.Fprintf(&b, "    rundir: %q\n", l.Run)
	for _, address := range listen {
		fmt.Fprintf(&b, "    listen: %s\n", address)
	}
	// The server does not give its name or its version.
	b.WriteString("    identity: \"\"\n    version: \"\"\n    nsid: \"\"\n")
	b.WriteString("log:\n  - target: stderr\n    any: info\n")
	// The TSIG keys are in a file that only root and the DNS user can read.
	fmt.Fprintf(&b, "include: %q\n", filepath.Join(filepath.Dir(l.Conf), "keys.conf"))
	fmt.Fprintf(&b, "database:\n    storage: %q\n", l.Storage)
	tr := transfersConf(st, l.NotifyPort)
	b.WriteString(tr.text)
	// The rate limit of the answers to one network, against the use of the server in an
	// attack on a different address.
	b.WriteString("mod-rrl:\n  - id: default\n    rate-limit: 50\n    slip: 2\n")
	// One key signs the zone and its keys (CSK). The panel makes the key, and all DNS
	// servers have the same key. NSEC3 with no more iterations and no salt (RFC 9276). The
	// panel changes the key in steps, and gives the state of each key (refer to dnssecKey). The
	// TTL of the DNSKEY records is fixed, because the panel waits for it in a change of the key.
	policy := "    manual: on\n    single-type-signing: on\n    algorithm: ecdsap256sha256\n    nsec3: on\n    nsec3-iterations: 0\n    nsec3-salt-length: 0\n    dnskey-ttl: 3600\n"
	b.WriteString("policy:\n  - id: shared\n" + policy + "    cds-cdnskey-publish: none\n")
	b.WriteString("  - id: submit\n" + policy + "    cds-cdnskey-publish: rollover\n")
	// The secondaries of the panel can copy each zone, and get NOTIFY after each change.
	shared := ""
	if len(tr.acl) > 0 {
		shared += "    acl: " + list(tr.acl) + "\n"
	}
	if len(tr.notify) > 0 {
		shared += "    notify: " + list(tr.notify) + "\n"
	}
	if st.catalogOn() {
		shared += fmt.Sprintf("    catalog-role: member\n    catalog-zone: %s.\n", st.Catalog)
	}
	template := func(id, more string) {
		fmt.Fprintf(&b, "  - id: %s\n    storage: %q\n    file: \"%%s.zone\"\n    zonefile-sync: -1\n    semantic-checks: on\n%s", id, l.Zones, more)
	}
	b.WriteString("template:\n")
	// Knot accepts a module for all zones only in the template "default".
	template("default", "    global-module: mod-rrl/default\n    zonefile-load: whole\n    journal-content: none\n"+shared)
	signed := "    zonefile-load: difference-no-serial\n    journal-content: all\n    serial-policy: unixtime\n    dnssec-signing: on\n"
	template("signed", signed+"    dnssec-policy: shared\n"+shared)
	template("submit", signed+"    dnssec-policy: submit\n"+shared)
	if st.catalogOn() {
		// Knot makes the catalog zone from the zones with "catalog-role: member". It has no
		// zone file.
		fmt.Fprintf(&b, "  - id: catalog\n    storage: %q\n    zonefile-sync: -1\n    zonefile-load: none\n    journal-content: all\n", l.Zones)
	}
	b.WriteString("zone:\n")
	if st.catalogOn() {
		fmt.Fprintf(&b, "  - domain: %s.\n    template: catalog\n    catalog-role: generate\n    acl: %s\n", st.Catalog, list(tr.catalogACL))
		if len(tr.catalogNotify) > 0 {
			fmt.Fprintf(&b, "    notify: %s\n", list(tr.catalogNotify))
		}
	}
	for _, name := range slices.Sorted(mapKeys(st.Zones)) {
		fmt.Fprintf(&b, "  - domain: %s.\n", name)
		if z := st.Zones[name]; z.signed() {
			// The CDS and CDNSKEY records of a new key are in the zone only in the step
			// "submit" of a change of the key (template "submit").
			if slices.ContainsFunc(z.Keys, func(k dnssecKey) bool { return k.State == keySubmit }) {
				b.WriteString("    template: submit\n")
			} else {
				b.WriteString("    template: signed\n")
			}
		}
		if acl, ok := tr.zoneACL[name]; ok {
			fmt.Fprintf(&b, "    acl: %s\n", list(acl))
		}
		if notify, ok := tr.zoneNotify[name]; ok {
			fmt.Fprintf(&b, "    notify: %s\n", list(notify))
		}
	}
	return b.String()
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (d *Deps) dnsListen() []string {
	if d.DnsTest != nil {
		return d.DnsTest.Listen
	}
	port := strconv.Itoa(quadlet.DnsPort)
	return []string{"0.0.0.0@" + port, "::@" + port}
}

// writeKnotConf writes keys.conf and knot.conf. It reports true when knot.conf changed.
func (d *Deps) writeKnotConf(st dnsState) (bool, error) {
	if err := d.writeKnotKeys(st); err != nil {
		return false, err
	}
	return host.WriteFile(filepath.Join(d.dnsConfigDir(), "knot.conf"), knotConf(d.dnsLayout(), d.dnsListen(), st), 0o644)
}

// DnsEnabled reports whether this server has a DNS server: it got a zone job.
func (d *Deps) DnsEnabled(ctx context.Context) bool {
	return d.dnsStatus(ctx).Enabled
}

// DnsZones gives the zones of this server with the serial that the DNS server loaded, for
// the report to the panel. The panel sends a zone again when this server does not have its
// last serial, and removes a zone that this server must not have. The list is never nil.
func (d *Deps) DnsZones(ctx context.Context) []panel.DNSZoneState {
	if zones := d.dnsStatus(ctx).Zones; zones != nil {
		return zones
	}
	return []panel.DNSZoneState{}
}

func (d *Deps) dnsStatus(ctx context.Context) dnsStatusOut {
	if d.Gate != nil {
		var out dnsStatusOut
		if err := d.Gate.Call(ctx, opDnsStatus, nil, &out); err != nil {
			return dnsStatusOut{}
		}
		return out
	}
	d.dnsMu.Lock()
	defer d.dnsMu.Unlock()
	st, ok, _ := d.readDnsState()
	out := dnsStatusOut{Enabled: ok, Secondaries: st.SecondariesVersion}
	for _, name := range slices.Sorted(mapKeys(st.Zones)) {
		out.Zones = append(out.Zones, panel.DNSZoneState{Name: name, Serial: st.Zones[name].Loaded})
	}
	return out
}

// EnsureDns starts the DNS server again when this server has one, for example with the image
// of a new agent version. The agent does this at each start.
func (d *Deps) EnsureDns(ctx context.Context) error {
	if !d.DnsEnabled(ctx) {
		return nil
	}
	return d.startDns(ctx)
}

// startDns makes the user, the directories and the unit of the DNS server, starts the
// container, and opens port 53 in the firewall. Each step is idempotent. The root helper does
// all of this with its own settings: the worker gives no image and no path.
func (d *Deps) startDns(ctx context.Context) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDnsStart, nil, nil)
	}
	d.dnsMu.Lock()
	defer d.dnsMu.Unlock()
	st, _, err := d.readDnsState()
	if err != nil {
		return err
	}
	if d.DnsTest == nil {
		if d.DnsImage == "" {
			return errors.New("DNS: no image")
		}
		if err := d.Host.EnsureServiceUser(ctx, host.DnsUser, ""); err != nil {
			return err
		}
	}
	if err := d.makeDnsDirs(ctx); err != nil {
		return err
	}
	// From now on, this server has a DNS server, also with no zone.
	if err := d.writeDnsState(st); err != nil {
		return err
	}
	confChanged, err := d.writeKnotConf(st)
	if err != nil {
		return err
	}
	if d.DnsTest != nil {
		return nil
	}
	if _, err := d.Host.Podman(ctx, host.DnsUser, "image", "exists", d.DnsImage); err != nil {
		if _, err := d.Host.Podman(ctx, host.DnsUser, "pull", "--quiet", d.DnsImage); err != nil {
			return err
		}
	}
	unit := quadlet.DnsUnit(quadlet.Dns{Image: d.DnsImage, ConfigDir: d.dnsConfigDir(), DataDir: d.dnsDataDir(), Gvisor: d.runtime()})
	unitChanged, err := d.Host.WriteUnit(host.DnsUser, quadlet.DnsUnitName, unit)
	if err != nil {
		return err
	}
	verb := "start"
	if unitChanged {
		if _, err := d.Host.Systemctl(ctx, host.DnsUser, "daemon-reload"); err != nil {
			return err
		}
		// A new image or a different gVisor platform.
		verb = "restart"
	}
	if _, err := d.Host.Systemctl(ctx, host.DnsUser, verb, dnsServiceName); err != nil {
		return err
	}
	if err := d.waitDns(ctx); err != nil {
		return fmt.Errorf("DNS: the DNS server does not answer: %w", err)
	}
	if confChanged && !unitChanged {
		if _, err := d.knot(ctx, "knotc", "reload"); err != nil {
			return err
		}
	}
	return d.openDnsPort(ctx)
}

// makeDnsDirs makes the directories of the DNS server. Root owns the configuration and the
// zone files. The DNS user owns the data of Knot.
func (d *Deps) makeDnsDirs(ctx context.Context) error {
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{d.dnsDir(), 0o711}, {d.dnsConfigDir(), 0o755}, {d.dnsZonesDir(), 0o755}} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return err
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return err
		}
	}
	if d.DnsTest != nil {
		return errors.Join(os.MkdirAll(d.dnsImportDir(), 0o700), os.MkdirAll(d.dnsLayout().Run, 0o700))
	}
	// The DNS container can write its data directory, so it can put a symbolic link there.
	// Root does not follow a link in this directory: it would give a directory of the host
	// to the DNS user (adversarial review, finding G7-01).
	uid, gid, err := d.dnsOwner()
	if err != nil {
		return err
	}
	dir, err := safefs.Open(d.dnsDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	for _, rel := range []string{"db", filepath.Join("db", "import")} {
		if err := dir.MkdirAll(rel, 0o700, uid, gid); err != nil {
			return err
		}
		if err := dir.SetDir(rel, 0o700, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// dnsOwner gives the user ID and the group ID of the DNS user.
func (d *Deps) dnsOwner() (uid, gid int, err error) {
	// Only root can give a file to a different user. The root helper is always root. A test
	// is not.
	if os.Geteuid() != 0 {
		return os.Getuid(), os.Getgid(), nil
	}
	if uid, err = d.Host.LookupUID(host.DnsUser); err != nil {
		return 0, 0, err
	}
	gid, err = d.Host.GID(host.DnsUser)
	return uid, gid, err
}

// waitDns waits until the control socket of Knot answers.
func (d *Deps) waitDns(ctx context.Context) error {
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		_, err := d.knot(ctx, "knotc", "status")
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// writeDnsZones writes the zone files and the configuration, and gives the DNSSEC keys to
// Knot. The DNS server uses them after loadDnsZones.
//
// A zone with a serial that is smaller than the serial of the zone on this server stays: the
// jobs of two changes can come in the other sequence.
func (d *Deps) writeDnsZones(ctx context.Context, zones []dnsZone) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDnsWrite, dnsWriteIn{Zones: zones}, nil)
	}
	// The root helper does not trust the check of the worker.
	if err := checkDnsZones(zones); err != nil {
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
	var unsigned []string
	// written are the zones of the job that this server takes: not a zone with an older serial.
	written := map[string]bool{}
	for _, z := range zones {
		if z.Name == st.Catalog {
			return fmt.Errorf("DNS zone %s: the catalog zone of the secondaries has this name", z.Name)
		}
		was, known := st.Zones[z.Name]
		if known && was.Serial > z.Serial {
			slog.Info("DNS: the server has a newer zone", "zone", z.Name, "serial", was.Serial, "job", z.Serial)
			continue
		}
		text, err := renderZone(z)
		if err != nil {
			return err
		}
		if _, err := host.WriteFile(filepath.Join(d.dnsZonesDir(), z.Name+".zone"), text, 0o644); err != nil {
			return err
		}
		if was.signed() && len(z.Keys) == 0 {
			unsigned = append(unsigned, z.Name)
		}
		st.Zones[z.Name] = dnsZoneState{Serial: z.Serial, Loaded: was.Loaded, Keys: keyStates(z.Keys), Transfers: z.Transfers}
		written[z.Name] = true
	}
	// The configuration has each new zone before Knot gets its key.
	if _, err := d.writeKnotConf(st); err != nil {
		return err
	}
	if err := d.writeDnsState(st); err != nil {
		return err
	}
	for _, z := range zones {
		if len(z.Keys) > 0 && written[z.Name] {
			if err := d.setDnsKeys(ctx, z); err != nil {
				return fmt.Errorf("DNS zone %s: %w", z.Name, err)
			}
		}
	}
	// DNSSEC is off now for these zones: the keys and the signatures go.
	for _, name := range unsigned {
		if err := d.forgetDnsKeys(ctx, name, nil); err != nil {
			return fmt.Errorf("DNS zone %s: %w", name, err)
		}
		if _, err := d.knot(ctx, "knotc", "reload"); err != nil {
			return err
		}
		if _, err := d.knot(ctx, "knotc", "-f", "zone-purge", name+".", "+journal", "+timers", "+kaspdb"); err != nil {
			return fmt.Errorf("DNS zone %s: %w", name, err)
		}
	}
	return nil
}

// The states of a DNSSEC key. The panel changes the key of a zone in steps (double
// signature): the new key is "ready", then "submit" until the registry has its DS record, then
// both keys are "active" until the old DS record is out of the caches, and then the old key
// goes.
const (
	keyActive = "active"
	keyReady  = "ready"
	keySubmit = "submit"
)

// checkDnsKeys checks the DNSSEC keys of a zone: one or two keys, each with a different tag,
// and one key at least that signs the zone.
func checkDnsKeys(keys []dnssecKey) error {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > 2 {
		return errors.New("a zone has two DNSSEC keys at most")
	}
	active := false
	for i, k := range keys {
		if err := validate.DNSSECKey(k.Key); err != nil {
			return err
		}
		if k.Tag < 0 || k.Tag > math.MaxUint16 {
			return errors.New("the key tag is not from 0 to 65535")
		}
		if i > 0 && keys[0].Tag == k.Tag {
			return errors.New("two DNSSEC keys have the same key tag")
		}
		switch k.State {
		case keyActive:
			active = true
		case keyReady, keySubmit:
		default:
			return fmt.Errorf("the DNSSEC key state %q is not active, ready or submit", k.State)
		}
	}
	if !active {
		return errors.New("no DNSSEC key signs the zone")
	}
	return nil
}

// keyStates are the keys with no private key, for the state file.
func keyStates(keys []dnssecKey) []dnssecKey {
	out := make([]dnssecKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, dnssecKey{Tag: k.Tag, State: k.State})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// signed reports whether the DNS server signs the zone (DNSSEC).
func (z dnsZoneState) signed() bool { return len(z.Keys) > 0 }

// keyLineRE is one line of "keymgr <zone> list": the ID of the key and its key tag. The
// timestamps that are set follow, for example "publish=1795390608 ready=1795390608".
var keyLineRE = regexp.MustCompile(`^([0-9a-f]{40}) +([0-9]{1,5}) `)

// knotKey is a key of a zone in Knot.
type knotKey struct {
	ID string
	// Active: the key signs the zone. Ready: the key signs only the DNSKEY records.
	Active, Ready bool
}

// dnsKeys gives the keys of a zone in Knot by their key tags.
func (d *Deps) dnsKeys(ctx context.Context, zone string) (map[int]knotKey, error) {
	out, err := d.knot(ctx, "keymgr", zone+".", "list")
	if err != nil {
		return nil, err
	}
	keys := map[int]knotKey{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if m := keyLineRE.FindStringSubmatch(line); m != nil {
			tag, _ := strconv.Atoi(m[2])
			fields := strings.Fields(line)
			has := func(name string) bool {
				return slices.ContainsFunc(fields, func(f string) bool { return strings.HasPrefix(f, name+"=") && f != name+"=0" })
			}
			keys[tag] = knotKey{ID: m[1], Active: has("active"), Ready: has("ready")}
		}
	}
	return keys, nil
}

// forgetDnsKeys removes the keys of a zone from Knot, but not the keys with the tags in keep.
func (d *Deps) forgetDnsKeys(ctx context.Context, zone string, keep []int) error {
	keys, err := d.dnsKeys(ctx, zone)
	if err != nil {
		return err
	}
	for tag, k := range keys {
		if slices.Contains(keep, tag) {
			continue
		}
		if _, err := d.knot(ctx, "keymgr", zone+".", "delete", k.ID); err != nil {
			return err
		}
	}
	return nil
}

// keyTimes are the timestamps of Knot for a state of a key. "+0" is now, and 0 removes the
// timestamp.
func keyTimes(state string) []string {
	if state == keyActive {
		return []string{"publish=+0", "active=+0"}
	}
	return []string{"publish=+0", "ready=+0", "active=0"}
}

// setDnsKeys gives the DNSSEC keys of a zone to Knot, with their states. Knot imports a key
// only when it does not have a key with the same tag. A key from an earlier time goes. The
// next load of the zone signs it with the keys.
func (d *Deps) setDnsKeys(ctx context.Context, z dnsZone) error {
	keys, err := d.dnsKeys(ctx, z.Name)
	if err != nil {
		return err
	}
	tags := []int{}
	for _, k := range z.Keys {
		tags = append(tags, k.Tag)
		have, ok := keys[k.Tag]
		if !ok {
			if err := d.importDnsKey(ctx, z.Name, k); err != nil {
				return err
			}
			continue
		}
		if have.Active != (k.State == keyActive) || (k.State != keyActive && !have.Ready) {
			if _, err := d.knot(ctx, "keymgr", append([]string{z.Name + ".", "set", have.ID}, keyTimes(k.State)...)...); err != nil {
				return err
			}
		}
	}
	return d.forgetDnsKeys(ctx, z.Name, tags)
}

// importDnsKey gives one DNSSEC key of a zone to Knot, and checks that Knot has it.
func (d *Deps) importDnsKey(ctx context.Context, zone string, k dnssecKey) error {
	// The key is in a file for a moment, in a directory that only the DNS user can open.
	// Root does not follow a symbolic link in the data directory of the DNS container
	// (adversarial review, finding G7-01).
	uid, gid := os.Getuid(), os.Getgid()
	if d.DnsTest == nil {
		var err error
		if uid, gid, err = d.dnsOwner(); err != nil {
			return err
		}
	}
	dir, err := safefs.Open(d.dnsDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	file := fmt.Sprintf("%s-%d.pem", zone, k.Tag)
	rel := filepath.Join("db", "import", file)
	defer dir.Remove(rel)
	if _, err := dir.WriteFile(rel, []byte(k.Key), 0o600, uid, gid); err != nil {
		return err
	}
	seen := filepath.Join(d.dnsLayout().Storage, "import", file)
	if _, err := d.knot(ctx, "keymgr", append([]string{zone + ".", "import-pem", seen, "ksk=yes", "zsk=yes", "algorithm=13"}, keyTimes(k.State)...)...); err != nil {
		return err
	}
	after, err := d.dnsKeys(ctx, zone)
	if err != nil {
		return err
	}
	if _, ok := after[k.Tag]; !ok {
		return fmt.Errorf("the DNSSEC key does not have the key tag %d", k.Tag)
	}
	return nil
}

// loadDnsZones makes Knot read its configuration and the zone files again, and checks that
// each zone of the job is in operation.
func (d *Deps) loadDnsZones(ctx context.Context, names []string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDnsLoad, dnsNamesIn{Names: names}, nil)
	}
	if err := checkDnsNames(names); err != nil {
		return err
	}
	d.dnsMu.Lock()
	defer d.dnsMu.Unlock()
	st, _, err := d.readDnsState()
	if err != nil {
		return err
	}
	if _, err := d.knot(ctx, "knotc", "reload"); err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		zone, ok := st.Zones[name]
		if !ok {
			continue
		}
		// "-b" waits until the zone is loaded, and until a signed zone has its signatures.
		if _, err := d.knot(ctx, "knotc", "-b", "zone-reload", name+"."); err != nil {
			errs = append(errs, fmt.Errorf("DNS zone %s: %w", name, err))
			continue
		}
		// A zone that Knot could not load has no serial.
		out, err := d.knot(ctx, "knotc", "zone-status", name+".", "+serial")
		if err != nil || !regexp.MustCompile(`serial: [0-9]+`).MatchString(out) {
			errs = append(errs, fmt.Errorf("DNS zone %s: the DNS server did not load the zone: %s %v", name, strings.TrimSpace(out), err))
			continue
		}
		zone.Loaded = zone.Serial
		st.Zones[name] = zone
	}
	// The report to the panel has the serial that the DNS server loaded.
	if err := d.writeDnsState(st); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// removeDnsZones removes zones from the DNS server, with their keys and their files. A zone
// that this server does not have is not an error.
func (d *Deps) removeDnsZones(ctx context.Context, names []string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDnsRemove, dnsNamesIn{Names: names}, nil)
	}
	if err := checkDnsNames(names); err != nil {
		return err
	}
	d.dnsMu.Lock()
	defer d.dnsMu.Unlock()
	st, ok, err := d.readDnsState()
	if err != nil || !ok {
		// This server never had a DNS server, so it has no zone.
		return err
	}
	var gone []string
	for _, name := range names {
		if _, ok := st.Zones[name]; ok {
			gone = append(gone, name)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if _, err := d.knot(ctx, "knotc", "status"); err != nil {
		return fmt.Errorf("DNS: the DNS server does not answer: %w", err)
	}
	var errs []error
	for _, name := range gone {
		if st.Zones[name].signed() {
			errs = append(errs, d.forgetDnsKeys(ctx, name, nil))
		}
		delete(st.Zones, name)
	}
	if _, err := d.writeKnotConf(st); err != nil {
		return err
	}
	if err := d.writeDnsState(st); err != nil {
		return err
	}
	if _, err := d.knot(ctx, "knotc", "reload"); err != nil {
		return err
	}
	for _, name := range gone {
		// The journal, the timers and the key records of a zone that is not in the configuration.
		if _, err := d.knot(ctx, "knotc", "-f", "zone-purge", "+orphan", name+"."); err != nil {
			errs = append(errs, fmt.Errorf("DNS zone %s: %w", name, err))
		}
		if err := os.Remove(filepath.Join(d.dnsZonesDir(), name+".zone")); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// openDnsPort opens port 53 in the firewall of the installer, and redirects it to the port
// of the DNS container. With no file, the administrator manages the firewall.
func (d *Deps) openDnsPort(ctx context.Context) error {
	d.firewallMu.Lock()
	defer d.firewallMu.Unlock()
	path := d.firewallFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		slog.Warn("DNS: no Cloudfactory firewall file; open port 53 in your firewall and redirect it to the DNS port", "file", path, "port", quadlet.DnsPort)
		return nil
	}
	if err != nil {
		return err
	}
	next, err := setDnsRules(string(b))
	if err != nil {
		return fmt.Errorf("DNS: %s: %w", path, err)
	}
	if next == string(b) {
		return nil
	}
	return d.replaceFirewall(ctx, path, next)
}

// setDnsRules gives the firewall file with the two rules of the DNS server: the input chain
// accepts DNS queries, and the prerouting chain sends port 53 to the port of the container,
// as it sends port 80 to the edge proxy. The client address does not change.
func setDnsRules(content string) (string, error) {
	if strings.Contains(content, dnsRuleMarker) {
		return content, nil
	}
	accept := fmt.Sprintf("meta l4proto { tcp, udp } th dport { 53, %d } accept %s", quadlet.DnsPort, dnsRuleMarker)
	redirect := fmt.Sprintf("meta l4proto { tcp, udp } th dport 53 redirect to :%d %s", quadlet.DnsPort, dnsRuleMarker)
	lines := strings.Split(content, "\n")
	add := func(after, rule string) bool {
		for i, l := range lines {
			if strings.TrimSpace(l) == after {
				lines = slices.Insert(lines, i+1, indent(l)+rule)
				return true
			}
		}
		return false
	}
	if !add("tcp dport 22 accept", accept) {
		return "", errors.New("the file has no rule for port 22, so the agent cannot open the DNS port")
	}
	if !add("tcp dport 80 redirect to :8080", redirect) {
		return "", errors.New("the file has no redirect for port 80, so the agent cannot redirect the DNS port")
	}
	return strings.Join(lines, "\n"), nil
}

// ReportAddresses gives the public IP addresses of this server for the report: the addresses
// of the settings (a server behind NAT), or the global addresses of its network interfaces.
// The panel uses them for the A and AAAA records of the websites on this server.
func (d *Deps) ReportAddresses() []string {
	if len(d.PublicAddresses) > 0 {
		return d.PublicAddresses
	}
	interfaces := d.InterfaceAddrs
	if interfaces == nil {
		interfaces = net.InterfaceAddrs
	}
	addrs, err := interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if ip.IsGlobalUnicast() && !ip.IsPrivate() && !slices.Contains(out, ip.String()) && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("169.254.0.0/16").Contains(ip) {
			out = append(out, ip.String())
		}
	}
	slices.Sort(out)
	return out
}
