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
	dbSizes  []panel.DatabaseSize
	dbAt     time.Time
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

// FillReport adds the use of each website (FR-203), the state of each domain (FR-402, FR-403),
// the size of each database and the state of the disk quotas to a status report.
func (d *Deps) FillReport(ctx context.Context, r *panel.Report) {
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
	if now.Sub(o.domainAt) > domainEvery {
		o.domains = d.domainStates(ctx, sites)
		o.domainAt = now
	}
	r.Domains = o.domains
	if now.Sub(o.dbAt) > databaseEvery {
		o.dbSizes = d.databaseSizes(ctx)
		o.dbAt = now
	}
	r.Databases = o.dbSizes
	quota := "running"
	if d.QuotaMount == "" {
		// The panel shows that the server does not apply the disk limits of the websites.
		quota = "stopped"
	}
	r.Services = append(r.Services, panel.Service{Name: "disk-quotas", State: quota})
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

// domainStates checks the DNS and the TLS certificate of each domain. The agent asks the edge
// proxy for the certificate of each domain, as a browser does, and checks it with the system
// roots. A domain with a valid certificate also points to this server, for example behind NAT.
func (d *Deps) domainStates(ctx context.Context, sites []*Site) []panel.DomainState {
	out := []panel.DomainState{}
	for _, s := range sites {
		for _, dom := range s.Website.GetDomains() {
			st := panel.DomainState{Name: dom.GetName(), DNSPointsHere: d.DNSPointsHere(ctx, dom.GetName()), Certificate: "pending"}
			if exp, err := d.certificate(ctx, dom.GetName()); err == nil {
				st.Certificate, st.ExpiresAt, st.DNSPointsHere = "valid", exp.UTC().Format(time.RFC3339), true
				if time.Until(exp) < 0 {
					st.Certificate = "expired"
				}
			} else if st.DNSPointsHere && !errors.Is(err, errNoCertificate) {
				// The DNS points here, but Caddy could not get a certificate.
				st.Certificate = "failed"
			}
			out = append(out, st)
		}
	}
	return out
}

var errNoCertificate = errors.New("the edge proxy has no certificate for the domain")

// certificate gives the expiry time of the certificate that the edge proxy serves for a domain.
func (d *Deps) certificate(ctx context.Context, domain string) (time.Time, error) {
	if d.Certificate != nil {
		return d.Certificate(ctx, domain)
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
