package actions

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// observer keeps the values of the status report that take time to read, so that each report
// is fast. The agent reads the disk use and the certificates less often than the report.
type observer struct {
	mu sync.Mutex
	// cpu is the last CPU time of each website, to give the CPU use since the last report.
	cpu map[string]cpuSample
	// cgroups is the cgroup directory of each website service.
	cgroups map[string]string

	disk     map[string]int64
	diskAt   time.Time
	domains  []panel.DomainState
	domainAt time.Time
	// domainRun is open while a check of the domains is in progress.
	domainRun chan struct{}
	dbSizes   []panel.DatabaseSize
	dbAt      time.Time
}

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
	// The panel server reports the certificate of the panel domain, and the root certificate
	// of the panel CA, which the panel offers for download.
	if domain, err := PanelDomain(PanelEnvFile(d.DataDir)); err == nil && d.Edge != nil {
		r.PanelCertificate = d.PanelCertificateState(ctx, domain)
	}
	sites, err := d.State.All()
	if err != nil {
		return
	}
	o := &d.obs
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cpu == nil {
		o.cpu, o.cgroups, o.disk = map[string]cpuSample{}, map[string]string{}, map[string]int64{}
	}
	now := time.Now()
	if now.Sub(o.diskAt) > diskEvery {
		for _, s := range sites {
			o.disk[s.ID] = dirSize(d.Host.AppDir(s.UnixUser))
		}
		o.diskAt = now
	}
	for _, s := range sites {
		u := panel.Usage{ID: s.ID, DiskBytes: o.disk[s.ID]}
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
	r.Services = append(r.Services, d.firewallService(ctx), d.quotaService())
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

// quotaService reports the disk limits of the websites. A server can operate without them.
func (d *Deps) quotaService() panel.Service {
	if d.QuotaMount != "" {
		return panel.Service{Name: "disk-quotas", State: "running", Optional: true}
	}
	// The panel shows that the server does not apply the disk limits of the websites.
	return panel.Service{Name: "disk-quotas", State: "stopped", Optional: true,
		Detail: "User quotas are off on the file system of the websites, so this server does not apply the disk limit of a plan. To turn them on, see \"Disk quotas\" in the installation guide, and then restart cf-agent."}
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

// dirSize gives the size of the files in dir. It does not follow links.
func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total
}

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
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(domainWorkers, len(names)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				st := d.domainState(ctx, names[i])
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
func (d *Deps) domainState(ctx context.Context, name string) panel.DomainState {
	st := panel.DomainState{Name: name, DNSPointsHere: d.DNSPointsHere(ctx, name), Certificate: "pending"}
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
	dialer := tls.Dialer{Config: &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(quadlet.ServiceAddress, "8443"))
	if err != nil {
		var verify *tls.CertificateVerificationError
		var alert tls.AlertError
		switch {
		case errors.As(err, &verify), errors.As(err, &alert):
			// Caddy has no publicly trusted certificate for the domain yet.
			return time.Time{}, errNoCertificate
		}
		return time.Time{}, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
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

// RestoreLocalNames sets the routes of the websites that have a local name again, so that a
// website from an earlier agent version gets the certificate policy of its local names.
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
		site := d.site(s.Website, s.Port)
		if len(caddy.LocalNames(site)) > 0 {
			errs = append(errs, d.Edge.Apply(ctx, site))
		}
	}
	return errors.Join(errs...)
}

// databaseSizes gives the size of each database in the shared database containers that operate.
func (d *Deps) databaseSizes(ctx context.Context) []panel.DatabaseSize {
	out := []panel.DatabaseSize{}
	d.dbMu.Lock()
	ready := map[string]bool{}
	for k, v := range d.dbReady {
		ready[k] = v
	}
	d.dbMu.Unlock()
	queries := map[string]string{
		"mariadb":    "SELECT table_schema, COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables GROUP BY table_schema;\n",
		"postgresql": "SELECT datname || '|' || pg_database_size(datname) FROM pg_database WHERE NOT datistemplate;\n",
	}
	for _, engine := range Engines {
		if !ready[engine] {
			continue
		}
		res, err := d.sqlNow(ctx, engine, queries[engine])
		if err != nil {
			continue
		}
		for _, line := range strings.Split(res, "\n") {
			f := strings.FieldsFunc(line, func(r rune) bool { return r == '\t' || r == '|' })
			if len(f) != 2 {
				continue
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); err == nil {
				out = append(out, panel.DatabaseSize{Engine: engine, Name: strings.TrimSpace(f[0]), SizeBytes: n})
			}
		}
	}
	return out
}
