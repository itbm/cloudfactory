package actions

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
)

// observer keeps the values of the status report that take time to read, so that each report
// is fast. The agent reads the disk use and the certificates less often than the report.
type observer struct {
	mu sync.Mutex
	// cpu is the last CPU time of each website, to give the CPU use since the last report.
	cpu map[string]cpuSample
	// cgroups is the cgroup directory of each website service.
	cgroups map[string]string

	disk map[string]int64
	// image is true for each website that has a disk image.
	image    map[string]bool
	diskAt   time.Time
	domains  []panel.DomainState
	domainAt time.Time
	// domainRun is open while a check of the domains is in progress.
	domainRun chan struct{}
	dbSizes   []panel.DatabaseSize
	dbAt      time.Time
	// dbCgroups is the cgroup directory of the service of each database server, and dbDisk is
	// the disk space that each database server with no disk image uses.
	dbCgroups map[string]string
	dbDisk    map[string]int64
	// dbLimited is the number of database servers with a disk limit. dbNoLimit is the number
	// of them that have no user quota and no disk image: the server does not apply their limit.
	dbLimited, dbNoLimit int
	// traffic is the last traffic counter of each host of the edge proxy, to give the traffic
	// since the last report. trafficRead is false until the first read.
	traffic     map[string]caddy.Traffic
	trafficRead bool
}

// trafficReader is the part of the edge proxy that gives the traffic of each host
// (caddy.Client.Traffic).
type trafficReader interface {
	Traffic(ctx context.Context) (map[string]caddy.Traffic, error)
}

// trafficLimit is the time limit to read the metrics of the edge proxy.
const trafficLimit = 5 * time.Second

type cpuSample struct {
	usec int64
	at   time.Time
}

// Intervals of the slow parts of the report.
const (
	diskEvery     = 30 * time.Minute
	domainEvery   = 5 * time.Minute
	databaseEvery = 10 * time.Minute
)

// Limits of the domain checks (audit finding S6). A customer controls the DNS of a domain, and
// thus the time that a check takes. The checks must not delay the report, because the panel
// marks a server without reports as offline.
const (
	// domainWorkers is the number of domains that the agent checks at the same time.
	domainWorkers = 16
	// domainLimit is the time limit for one check of all domains.
	domainLimit = 2 * time.Minute
	// domainWait is the time that a report waits for a check that it started.
	domainWait = 5 * time.Second
)

// FillReport adds the use of each website (FR-203), the state of each domain (FR-402, FR-403),
// the size of each database, the state of the disk quotas and, on the panel server, the
// certificate of the panel to a status report.
func (d *Deps) FillReport(ctx context.Context, r *panel.Report) {
	r.SftpPort = d.SftpPortInUse()
	r.DNS = true
	r.Addresses = d.ReportAddresses()
	r.DNSZones = d.DnsZones(ctx)
	// The DNS server is a service of this server only after its first zone job.
	if d.DnsEnabled(ctx) {
		state := "stopped"
		out, _ := d.Host.Systemctl(ctx, host.DnsUser, "is-active", dnsServiceName)
		switch strings.TrimSpace(out) {
		case "active", "activating", "reloading":
			state = "running"
		case "failed":
			state = "failed"
		}
		r.Services = append(r.Services, panel.Service{Name: dnsContainer, State: state})
	}
	r.CronRuns = d.cronReport()
	// The panel server reports the certificate of the panel domain, and the root certificate
	// of the panel CA, which the panel offers for download.
	if domain, err := d.panelDomain(ctx); err == nil && d.Edge != nil {
		r.PanelCertificate = d.PanelCertificateState(ctx, domain)
	}
	r.Mesh = d.meshReport(ctx)
	sites, err := d.State.All()
	if err != nil {
		return
	}
	o := &d.obs
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cpu == nil {
		o.cpu, o.cgroups, o.disk, o.image = map[string]cpuSample{}, map[string]string{}, map[string]int64{}, map[string]bool{}
	}
	now := time.Now()
	// walk is true when this report measures the directories again.
	walk := now.Sub(o.diskAt) > diskEvery
	if walk {
		clear(o.image)
		for _, s := range sites {
			// A website with a disk image: the kernel gives the use, and the agent does not
			// read the files.
			st := d.siteDiskState(ctx, s.UnixUser, true)
			o.disk[s.ID], o.image[s.ID] = max(st.Used, 0), st.Known
		}
		o.diskAt = now
	}
	traffic := d.trafficSince(ctx)
	for _, s := range sites {
		// A website that the server got after the last measurement.
		if _, ok := o.image[s.ID]; !ok {
			o.image[s.ID] = d.siteDiskState(ctx, s.UnixUser, false).Known
		}
		u := panel.Usage{ID: s.ID, DiskBytes: o.disk[s.ID]}
		// The traffic of all domains of the website, also the redirect domains.
		for _, dom := range s.Website.GetDomains() {
			t := traffic[strings.ToLower(dom.GetName())]
			u.BytesIn += t.In
			u.BytesOut += t.Out
		}
		if !s.Stopped {
			d.cgroupUse(ctx, s, &u, now)
		}
		r.Websites = append(r.Websites, u)
	}
	// The check of the domains runs apart from the report. The report waits a short time for
	// the check that it started, and then uses the last complete result.
	var started chan struct{}
	if now.Sub(o.domainAt) > domainEvery && o.domainRun == nil {
		started = make(chan struct{})
		o.domainRun = started
		go d.checkDomains(sites, started)
	}
	if started != nil {
		o.mu.Unlock()
		wait := time.NewTimer(orDefault(d.DomainWait, domainWait))
		select {
		case <-started:
		case <-wait.C:
		case <-ctx.Done():
		}
		wait.Stop()
		o.mu.Lock()
	}
	r.Domains = o.domains
	if now.Sub(o.dbAt) > databaseEvery {
		o.dbSizes = d.databaseSizes(ctx)
		o.dbAt = now
	}
	r.Databases = o.dbSizes
	r.DatabaseServers = d.databaseServerUse(ctx, walk)
	r.DatabaseEngines = engines()
	for _, rt := range quadlet.Runtimes() {
		r.Runtimes = append(r.Runtimes, rt.Name)
	}
	r.Packs = d.PackStates()
	r.Certificates = d.certificateStates(ctx)
	if d.ValkeyBackups {
		r.Features = append(r.Features, "valkey-backups")
	}
	if d.ManageTrial {
		r.Features = append(r.Features, "manage-trial")
	}
	// The import jobs of an account from a different panel (imports.go).
	r.Features = append(r.Features, "imports")
	r.DatabaseInstances = true
	r.Services = append(r.Services, d.firewallService(ctx), d.helperService(), d.quotaService(sites), d.diskService())
	r.Mail = d.mailReport(ctx)
}

// trafficSince gives the traffic of each host since the last report. The caller holds the
// lock of the observer. The first read after a start of the agent gives no traffic: it only
// keeps the counters. When the edge proxy started again, its counters start at 0, and the
// traffic is the new counter.
func (d *Deps) trafficSince(ctx context.Context) map[string]caddy.Traffic {
	edge, ok := d.Edge.(trafficReader)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, trafficLimit)
	defer cancel()
	now, err := edge.Traffic(ctx)
	if err != nil {
		// The next read gives the traffic of both intervals.
		return nil
	}
	o := &d.obs
	out := map[string]caddy.Traffic{}
	if o.trafficRead {
		for host, t := range now {
			prev := o.traffic[host]
			delta := caddy.Traffic{In: t.In - prev.In, Out: t.Out - prev.Out}
			if delta.In < 0 || delta.Out < 0 {
				delta = t
			}
			out[host] = delta
		}
	}
	o.traffic, o.trafficRead = now, true
	return out
}

// databaseServerUse gives the state, the memory and the disk space of each database server of
// a customer. The caller holds the lock of the observer. With walk set, the function also
// measures the directory of each database server that has no user quota and no disk image.
func (d *Deps) databaseServerUse(ctx context.Context, walk bool) []panel.DatabaseServerUse {
	out := []panel.DatabaseServerUse{}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return out
	}
	o := &d.obs
	if o.dbCgroups == nil {
		o.dbCgroups, o.dbDisk = map[string]string{}, map[string]int64{}
	}
	o.dbLimited, o.dbNoLimit = 0, 0
	for _, customer := range slices.Sorted(maps.Keys(instances)) {
		servers := instances[customer].Servers
		for _, id := range slices.Sorted(maps.Keys(servers)) {
			s := dbServer{ID: id, Engine: servers[id].Engine}
			u := panel.DatabaseServerUse{ID: id, State: "stopped"}
			_, measured := o.dbDisk[id]
			st := d.diskState(ctx, s, walk || !measured)
			if st.Known {
				u.DiskBytes, u.DiskSizeBytes = st.Used, st.Size
			} else {
				if st.Used >= 0 {
					o.dbDisk[id] = st.Used
				}
				u.DiskBytes = o.dbDisk[id]
			}
			// The part "database-disk-limits" of the report (diskService).
			if servers[id].DiskBytes > 0 {
				o.dbLimited++
				if !st.Known {
					o.dbNoLimit++
				}
			}
			dir := o.dbCgroups[id]
			if dir == "" {
				out, err := d.Host.Systemctl(ctx, dbUser(s), "show", "--property=ControlGroup", "--value", s.container()+".service")
				if cg := strings.TrimSpace(out); err == nil && cg != "" && !strings.Contains(cg, "..") {
					dir = filepath.Join(d.cgroupRoot(), cg)
					o.dbCgroups[id] = dir
				}
			}
			// A service that does not operate has no cgroup.
			if b, err := os.ReadFile(filepath.Join(dir, "memory.current")); dir != "" && err == nil {
				u.State = "running"
				u.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			} else {
				delete(o.dbCgroups, id)
			}
			out = append(out, u)
		}
	}
	return out
}

// firewallService reports the firewall of Cloudfactory. The agent checks the table in the
// kernel, not the nftables service: the installer loads the rules with nft, and the service
// starts for the first time only when the server starts again.
func (d *Deps) firewallService(ctx context.Context) panel.Service {
	s := panel.Service{Name: "firewall", State: "running"}
	if _, err := d.Run.Run(ctx, "nft", "list", "table", "inet", "cloudfactory"); err != nil {
		s.State = "stopped"
		s.Detail = "The nftables table \"inet cloudfactory\" is not loaded. Run the installer again on this server, or run: nft -f " + d.firewallFile()
	}
	return s
}

// helperService reports the two processes of the agent. A server can operate with one root
// process, but then a fault in the agent gives an attacker the rights of root.
func (d *Deps) helperService() panel.Service {
	if !d.OneProcess {
		return panel.Service{Name: "agent-root-helper", State: "running", Optional: true}
	}
	return panel.Service{Name: "agent-root-helper", State: "stopped", Optional: true,
		Detail: "The agent operates as one root process, not as a root helper and a worker with fewer rights. To see the cause, run on the server: journalctl -u cf-agent | grep \"one root process\""}
}

// quotaService reports the disk limits of the websites. A server can operate without them.
// The caller holds the lock of the observations.
//
// The panel shows "Not applied on this server" next to the disk limit of each website of a
// server that reports this part as stopped. The part is thus stopped when the server does not
// apply the disk limit of one of its websites.
func (d *Deps) quotaService(sites []*Site) panel.Service {
	running := panel.Service{Name: "disk-quotas", State: "running", Optional: true}
	if d.QuotaMount != "" {
		return running
	}
	const guide = " Refer to \"Disk quotas\" in the installation guide."
	if !d.loopDevices() {
		return panel.Service{Name: "disk-quotas", State: "stopped", Optional: true,
			Detail: "User quotas are off on the file system of the websites, and this server has no loop devices (" + d.loopControl() + "), so it does not apply the disk limit of a website." + guide}
	}
	// With no user quotas, a new website gets a disk image. A website that has a directory
	// keeps it, and has no limit.
	limited, none := 0, 0
	for _, s := range sites {
		if s.Website.GetLimits().GetDiskBytes() <= 0 {
			continue
		}
		limited++
		if !d.obs.image[s.ID] {
			none++
		}
	}
	if none == 0 {
		return running
	}
	return panel.Service{Name: "disk-quotas", State: "stopped", Optional: true,
		Detail: fmt.Sprintf("User quotas are off on the file system of the websites. Websites with a disk limit and no disk image: %d of %d. This server does not apply the disk limit of these websites. Only a new website gets a disk image.", none, limited) + guide}
}

// cgroupUse reads the memory, CPU and process use of a website from the cgroup of its service.
func (d *Deps) cgroupUse(ctx context.Context, s *Site, u *panel.Usage, now time.Time) {
	o := &d.obs
	dir := o.cgroups[s.ID]
	if dir == "" {
		svc := quadlet.Website{UnixUser: s.UnixUser, Suffix: s.Suffix}.ServiceName()
		out, err := d.Host.Systemctl(ctx, s.UnixUser, "show", "--property=ControlGroup", "--value", svc)
		cg := strings.TrimSpace(out)
		if err != nil || cg == "" || strings.Contains(cg, "..") {
			return
		}
		dir = filepath.Join(d.cgroupRoot(), cg)
		o.cgroups[s.ID] = dir
	}
	u.MemoryBytes = readInt(filepath.Join(dir, "memory.current"))
	u.Processes = readInt(filepath.Join(dir, "pids.current"))
	b, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		// The service has a new cgroup, for example after a runtime change.
		delete(o.cgroups, s.ID)
		return
	}
	var usec int64
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			usec, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if prev, ok := o.cpu[s.ID]; ok && usec >= prev.usec && now.After(prev.at) {
		// The share of one CPU. 100 means one full CPU.
		u.CPUPercent = percentOf(float64(usec-prev.usec), float64(now.Sub(prev.at).Microseconds()))
	}
	o.cpu[s.ID] = cpuSample{usec: usec, at: now}
}

func (d *Deps) cgroupRoot() string {
	if d.CgroupRoot != "" {
		return d.CgroupRoot
	}
	return "/sys/fs/cgroup"
}

func percentOf(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(int64(part/whole*1000)) / 10
}

func readInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v
}

// dirSize gives the disk space of the files in dir. It does not follow links, and it has a
// limit of entries and of time (safefs.Size).
func dirSize(dir string) int64 { return safefs.TreeSize(dir) }

// checkDomains checks all domains and keeps the result for the subsequent reports.
func (d *Deps) checkDomains(sites []*Site, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), orDefault(d.DomainLimit, domainLimit))
	defer cancel()
	states := d.domainStates(ctx, sites)
	o := &d.obs
	o.mu.Lock()
	o.domains, o.domainAt, o.domainRun = states, time.Now(), nil
	o.mu.Unlock()
	close(done)
}

func orDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

// domainStates checks the DNS and the TLS certificate of each domain. It checks some domains
// at the same time, and it stops when ctx ends. A domain that was not checked in time is not
// in the result, so the panel keeps the last state of that domain.
func (d *Deps) domainStates(ctx context.Context, sites []*Site) []panel.DomainState {
	// The first domain of each website, then the second of each website, and so on. Thus one
	// website with many slow domains does not use all the time of the other websites.
	names := []string{}
	for i, more := 0, true; more; i++ {
		more = false
		for _, s := range sites {
			if doms := s.Website.GetDomains(); i < len(doms) {
				names, more = append(names, doms[i].GetName()), true
			}
		}
	}
	states := make([]*panel.DomainState, len(names))
	// The hosts that use a certificate from the panel.
	custom := d.panelCertificateHosts(ctx)
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(domainWorkers, len(names)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				st := d.domainState(ctx, names[i], custom[names[i]])
				// A check that the time limit stopped gives no reliable result.
				if ctx.Err() == nil {
					states[i] = &st
				}
			}
		}()
	}
feed:
	for i := range names {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	out := []panel.DomainState{}
	for _, st := range states {
		if st != nil {
			out = append(out, *st)
		}
	}
	return out
}

// domainState checks one domain. The agent asks the edge proxy for the certificate of the
// domain, as a browser does, and checks it with the system roots. A domain with a valid
// certificate also points to this server, for example behind NAT.
//
// panelSHA256 is the fingerprint of the certificate from the panel that the domain uses, or
// "". The agent then checks that the edge proxy sends that certificate. The DNS is not
// important for such a certificate.
func (d *Deps) domainState(ctx context.Context, name, panelSHA256 string) panel.DomainState {
	st := panel.DomainState{Name: name, DNSPointsHere: d.DNSPointsHere(ctx, name), Certificate: "pending"}
	if panelSHA256 != "" {
		exp, err := d.panelCertificate(ctx, name, panelSHA256)
		switch {
		case err != nil:
			st.Certificate = "failed"
		case time.Until(exp) < 0:
			st.Certificate, st.ExpiresAt = "expired", exp.UTC().Format(time.RFC3339)
		default:
			st.Certificate, st.ExpiresAt = "valid", exp.UTC().Format(time.RFC3339)
		}
		return st
	}
	if exp, err := d.certificate(ctx, name); err == nil {
		st.Certificate, st.ExpiresAt, st.DNSPointsHere = "valid", exp.UTC().Format(time.RFC3339), true
		if time.Until(exp) < 0 {
			st.Certificate = "expired"
		}
	} else if st.DNSPointsHere && !errors.Is(err, errNoCertificate) {
		// The DNS points here, but Caddy could not get a certificate.
		st.Certificate = "failed"
	}
	return st
}

var errNoCertificate = errors.New("the edge proxy has no certificate for the domain")

// edgeHTTPS is the address of the HTTPS port of the edge proxy.
var edgeHTTPS = net.JoinHostPort(quadlet.ServiceAddress, strconv.Itoa(edgeHTTPSPort))

// certificate gives the expiry time of the certificate that the edge proxy serves for a domain.
func (d *Deps) certificate(ctx context.Context, domain string) (time.Time, error) {
	if d.Certificate != nil {
		return d.Certificate(ctx, domain)
	}
	if !caddy.PublicName(domain) {
		return d.localCertificate(ctx, domain)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := d.DialLocal(ctx, edgeHTTPS)
	if err != nil {
		return time.Time{}, err
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		var verify *tls.CertificateVerificationError
		var alert tls.AlertError
		switch {
		case errors.As(err, &verify), errors.As(err, &alert):
			// Caddy has no publicly trusted certificate for the domain yet.
			return time.Time{}, errNoCertificate
		}
		return time.Time{}, err
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, errNoCertificate
	}
	return certs[0].NotAfter, nil
}

// localCertificate gives the expiry time of the certificate of a local name, for example
// shop.localhost. The panel CA of the edge proxy signs these certificates (caddy.LocalPolicy),
// and the system does not trust that CA, so the agent checks the chain with the root
// certificate of the panel CA. Before, the certificate of a local name was "pending" for all time.
func (d *Deps) localCertificate(ctx context.Context, domain string) (time.Time, error) {
	chain, err := d.servedCertificate(ctx, domain)
	if err != nil {
		return time.Time{}, errNoCertificate
	}
	root, err := d.Edge.PanelCARoot(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if err := caddy.CheckPanelCertificate(chain, domain, caddy.PanelInternal, root, nil, time.Now()); err != nil {
		// For example a certificate from the default CA of Caddy, from before the policy.
		return time.Time{}, errNoCertificate
	}
	return chain[0].NotAfter, nil
}

// RestoreLocalNames sets the routes of the websites again, so that a website from an earlier
// agent version gets the certificate policy of its local names, and a website that an earlier
// agent version stopped gets the page of a stopped website.
func (d *Deps) RestoreLocalNames(ctx context.Context) error {
	sites, err := d.State.All()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sites {
		if s.Website == nil || s.Port == 0 {
			continue
		}
		// Each website gets its route again: an edge proxy that lost its configuration has
		// all routes again after a start of the agent, and a route from an earlier agent
		// version gets the matcher of this version (adversarial review, findings G6-07 and
		// G6-02). Caddy does nothing for a route that did not change. The access log of the
		// website gets its directory before the first entry.
		errs = append(errs, d.ensureAccessLog(s.Website.GetId()), d.Edge.Apply(ctx, d.edgeSite(s, s.Website)))
	}
	return errors.Join(errs...)
}

// databaseSizes gives the size of each database in the database servers that operate. The
// name of a database is for one website, so it is in one database server only.
func (d *Deps) databaseSizes(ctx context.Context) []panel.DatabaseSize {
	out := []panel.DatabaseSize{}
	d.dbMu.Lock()
	servers := slices.Collect(maps.Keys(d.dbReady))
	d.dbMu.Unlock()
	slices.SortFunc(servers, func(a, b dbServer) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Engine, b.Engine))
	})
	for _, s := range servers {
		drv, err := driverOf(s.Engine)
		if err != nil {
			continue
		}
		if sizes, err := drv.sizes(ctx, d, s); err == nil {
			out = append(out, sizes...)
		}
	}
	return out
}
