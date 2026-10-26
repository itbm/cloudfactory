package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// CheckWebsite checks each value of a website before the agent uses it.
func CheckWebsite(w *agentv1.Website) error {
	if w == nil {
		return errors.New("the request has no website")
	}
	errs := []error{validate.ID(w.GetId()), validate.UnixUser(w.GetUnixUser()), validate.Image(w.GetImage()), validate.Digest(w.GetImageDigest())}
	switch w.GetRuntime() {
	case "frankenphp", "apache-php-fpm", "custom":
	default:
		errs = append(errs, fmt.Errorf("the runtime %q is not valid", w.GetRuntime()))
	}
	for _, e := range w.GetExtensions() {
		errs = append(errs, validate.Extension(e))
	}
	for k, v := range w.GetPhpIni() {
		errs = append(errs, validate.IniKey(k), validate.IniValue(v))
	}
	primary := 0
	for _, d := range w.GetDomains() {
		errs = append(errs, validate.Domain(d.GetName()))
		switch d.GetKind() {
		case "primary":
			primary++
		case "redirect", "alias":
		default:
			errs = append(errs, fmt.Errorf("the domain kind %q is not valid", d.GetKind()))
		}
	}
	// CF-03: the panel sends only verified domains, so a new website can have no domain yet.
	if primary > 1 {
		errs = append(errs, errors.New("a website must have one primary domain"))
	}
	if primary == 0 && len(w.GetDomains()) > 0 {
		errs = append(errs, errors.New("a website with domains must have a primary domain"))
	}
	return errors.Join(errs...)
}

func imageRef(w *agentv1.Website) string {
	if w.GetImageDigest() != "" {
		return w.GetImage() + "@" + w.GetImageDigest()
	}
	return w.GetImage()
}

func primaryDomain(w *agentv1.Website) string {
	for _, d := range w.GetDomains() {
		if d.GetKind() == "primary" {
			return d.GetName()
		}
	}
	return ""
}

func (d *Deps) iniPath(unixUser, suffix string) string {
	return filepath.Join(d.Host.Home(unixUser), "php"+suffix+".ini")
}

func (d *Deps) unit(w *agentv1.Website, port int, suffix string) quadlet.Website {
	l := w.GetLimits()
	return quadlet.Website{
		ID: w.GetId(), Name: primaryDomain(w), UnixUser: w.GetUnixUser(), Image: imageRef(w), Port: port,
		SiteDir: filepath.Join(d.Host.Home(w.GetUnixUser()), "app"), IniFile: d.iniPath(w.GetUnixUser(), suffix),
		Runtime: w.GetRuntime(), WorkerMode: w.GetWorkerMode(), Gvisor: d.Gvisor, Suffix: suffix,
		Limits: quadlet.Limits{VCPUs: l.GetVcpus(), MemoryBytes: l.GetMemoryBytes(), Processes: l.GetProcesses(), DiskIOBytesPerSecond: l.GetDiskIoBytesPerSecond(), BlockDevice: d.BlockDevice},
	}
}

func (d *Deps) site(w *agentv1.Website, port int) caddy.Site {
	s := caddy.Site{WebsiteID: w.GetId(), Port: port, Primary: primaryDomain(w)}
	for _, dom := range w.GetDomains() {
		if dom.GetKind() == "redirect" {
			s.Redirects = append(s.Redirects, dom.GetName())
		} else {
			s.Hosts = append(s.Hosts, dom.GetName())
		}
	}
	return s
}

// startContainer writes the php.ini and the unit, and starts the container.
func (d *Deps) startContainer(ctx context.Context, w *agentv1.Website, port int, suffix string) error {
	u := d.unit(w, port, suffix)
	if w.GetRuntime() != "custom" {
		if _, err := host.WriteFile(u.IniFile, quadlet.PHPIni(w.GetPhpVersion(), w.GetExtensions(), w.GetPhpIni()), 0o644); err != nil {
			return err
		}
	}
	if _, err := d.Host.WriteUnit(w.GetUnixUser(), u.UnitName(), quadlet.ContainerUnit(u)); err != nil {
		return err
	}
	if _, err := d.Host.Systemctl(ctx, w.GetUnixUser(), "daemon-reload"); err != nil {
		return err
	}
	_, err := d.Host.Systemctl(ctx, w.GetUnixUser(), "restart", u.ServiceName())
	return err
}

func (d *Deps) stopContainer(ctx context.Context, unixUser, suffix string) error {
	u := quadlet.Website{UnixUser: unixUser, Suffix: suffix}
	if _, err := d.Host.Systemctl(ctx, unixUser, "stop", u.ServiceName()); err != nil {
		return err
	}
	if err := d.Host.RemoveUnit(unixUser, u.UnitName()); err != nil {
		return err
	}
	_, err := d.Host.Systemctl(ctx, unixUser, "daemon-reload")
	return err
}

// ready reports whether a website answers. Any status below 500 means that the container
// operates. The application can still need setup, for example a new WordPress.
func (d *Deps) ready(ctx context.Context, port int, hostname string) error {
	status, err := d.Health(ctx, port, hostname)
	if err != nil {
		return err
	}
	if status >= 500 {
		return fmt.Errorf("status %d", status)
	}
	return nil
}

// waitReady waits until the website answers on its port.
func (d *Deps) waitReady(ctx context.Context, port int, hostname string) error {
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var err error
	for {
		if err = d.ready(ctx, port, hostname); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the website did not answer in %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// watch checks the website for some time after a change. A check passes when the website
// gives the expected status, for example 200. The watch fails when the share of failed checks
// is more than maxErrorRate (FR-1104, FR-1105).
func (d *Deps) watch(ctx context.Context, port int, hostname string, hc *agentv1.HealthCheck) error {
	seconds := time.Duration(hc.GetSeconds()) * time.Second
	if d.MaxWatch > 0 && seconds > d.MaxWatch {
		seconds = d.MaxWatch
	}
	if seconds <= 0 {
		return nil
	}
	interval := seconds / 10
	var checks, failed int
	var last error
	for end := time.Now().Add(seconds); time.Now().Before(end); {
		checks++
		status, err := d.Health(ctx, port, hostname)
		if err == nil && hc.GetStatus() != 0 && int32(status) != hc.GetStatus() {
			err = fmt.Errorf("status %d, not %d", status, hc.GetStatus())
		}
		if err != nil {
			failed++
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	if checks > 0 && float64(failed)/float64(checks) > hc.GetMaxErrorRate() {
		return fmt.Errorf("%d of %d health checks failed: %v", failed, checks, last)
	}
	return nil
}

// CreateWebsite makes the user, the network and the container, and adds the edge route.
func (d *Deps) CreateWebsite(req *agentv1.WebsiteRequest) (jobs.Job, error) {
	w := req.GetWebsite()
	if err := CheckWebsite(w); err != nil {
		return jobs.Job{}, err
	}
	if req.GetCopyFrom() != "" {
		if err := validate.UnixUser(req.GetCopyFromUnixUser()); err != nil {
			return jobs.Job{}, err
		}
	}
	user := w.GetUnixUser()
	var port int
	var dnsHere bool
	return jobs.Job{ID: req.GetJobId(), Key: w.GetId(), Steps: []jobs.Step{
		{Name: "Create Unix user", Do: func(ctx context.Context) error {
			if err := d.Host.EnsureUser(ctx, user); err != nil {
				return err
			}
			if req.GetCopyFrom() == "" {
				return nil
			}
			// A staging copy (FR-205) starts with the files of the source website. CF-05: the
			// source user reads and the new user writes, so root never follows a link that a
			// tenant made.
			from := filepath.Join(d.Host.Home(req.GetCopyFromUnixUser()), "app")
			to := filepath.Join(d.Host.Home(user), "app")
			return d.Run.Pipe(ctx,
				[]string{"runuser", "-u", req.GetCopyFromUnixUser(), "--", "tar", "--create", "--file=-", "--directory=" + from, "--", "."},
				[]string{"runuser", "-u", user, "--", "tar", "--extract", "--file=-", "--directory=" + to, "--no-same-owner", "--preserve-permissions"},
			)
		}},
		{Name: "Create container network", Do: func(ctx context.Context) error {
			if _, err := d.Host.WriteUnit(user, quadlet.NetworkName(user)+".network", quadlet.NetworkUnit(user)); err != nil {
				return err
			}
			_, err := d.Host.Systemctl(ctx, user, "daemon-reload")
			return err
		}},
		{Name: "Pull and check image signature", Do: func(ctx context.Context) error {
			// The signature policy of Podman (/etc/containers/policy.json) refuses an image
			// with no valid signature (FR-1101).
			_, err := d.Host.Podman(ctx, user, "pull", "--quiet", imageRef(w))
			return err
		}},
		{Name: "Start container in gVisor", Do: func(ctx context.Context) error {
			old, err := d.State.Get(w.GetId())
			if err != nil {
				return err
			}
			if old != nil {
				port = old.Port
			} else if port, err = d.State.FreePort(); err != nil {
				return err
			}
			if err := d.State.Put(&Site{ID: w.GetId(), UnixUser: user, Port: port, Website: w}); err != nil {
				return err
			}
			if err := d.startContainer(ctx, w, port, ""); err != nil {
				return err
			}
			return d.waitReady(ctx, port, primaryDomain(w))
		}},
		{Name: "Configure edge proxy", Do: func(ctx context.Context) error {
			if err := d.Edge.EnsureBase(ctx); err != nil {
				return err
			}
			return d.Edge.Apply(ctx, d.site(w, port))
		}},
		{Name: "Get TLS certificate", Do: func(ctx context.Context) error {
			// Caddy gets the certificate when the DNS of the domain points to this server.
			dnsHere = d.DNSPointsHere(ctx, primaryDomain(w))
			return nil
		}},
	}, Result: func() map[string]any { return map[string]any{"port": port, "dnsPointsHere": dnsHere} }}, nil
}

// SetRuntime changes the image or the PHP settings with no stop (FR-1108). The agent starts
// a second container, checks it, sends traffic to it, and only then stops the old container.
// If a check fails, the old container continues to serve the website (FR-1105).
func (d *Deps) SetRuntime(req *agentv1.WebsiteRequest) (jobs.Job, error) {
	w := req.GetWebsite()
	if err := CheckWebsite(w); err != nil {
		return jobs.Job{}, err
	}
	user := w.GetUnixUser()
	hostname := primaryDomain(w)
	var old *Site
	var newPort int
	var newSuffix string
	switched := false
	return jobs.Job{ID: req.GetJobId(), Key: w.GetId(), Steps: []jobs.Step{
		{Name: "Check image signature", Do: func(ctx context.Context) error {
			var err error
			if old, err = d.State.Get(w.GetId()); err != nil {
				return err
			}
			if old == nil {
				return errors.New("the agent does not know this website; create it first")
			}
			_, err = d.Host.Podman(ctx, user, "pull", "--quiet", imageRef(w))
			return err
		}},
		{Name: "Start new container on a copy", Do: func(ctx context.Context) error {
			newSuffix = "-b"
			if old.Suffix == "-b" {
				newSuffix = ""
			}
			var err error
			if newPort, err = d.State.FreePort(old.Port); err != nil {
				return err
			}
			return d.startContainer(ctx, w, newPort, newSuffix)
		}},
		{Name: "Health check", Do: func(ctx context.Context) error {
			return d.waitReady(ctx, newPort, hostname)
		}},
		{Name: "Switch traffic", Do: func(ctx context.Context) error {
			if err := d.Edge.Apply(ctx, d.site(w, newPort)); err != nil {
				return err
			}
			switched = true
			return d.watch(ctx, newPort, hostname, req.GetHealthCheck())
		}},
		{Name: "Stop old container", Do: func(ctx context.Context) error {
			if err := d.State.Put(&Site{ID: w.GetId(), UnixUser: user, Port: newPort, Suffix: newSuffix, Website: w}); err != nil {
				return err
			}
			return d.stopContainer(ctx, user, old.Suffix)
		}},
	}, OnFailure: func(ctx context.Context) {
		if old == nil || newPort == 0 {
			return
		}
		if switched {
			d.Edge.Apply(ctx, d.site(old.Website, old.Port))
		}
		d.stopContainer(ctx, user, newSuffix)
	}}, nil
}

// DeleteWebsite removes the container, the network, the edge route, the databases and the user.
func (d *Deps) DeleteWebsite(req *agentv1.DeleteWebsiteRequest) (jobs.Job, error) {
	errs := []error{validate.ID(req.GetWebsiteId()), validate.UnixUser(req.GetUnixUser())}
	for _, db := range append(slices.Clone(req.GetDatabases()), req.GetDatabaseUsers()...) {
		errs = append(errs, validate.TenantDatabaseName(req.GetUnixUser(), db.GetName()))
		if _, err := dbContainer(db.GetEngine()); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	user := req.GetUnixUser()
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Stop container", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil || site == nil {
				return err
			}
			return d.stopContainer(ctx, user, site.Suffix)
		}},
		{Name: "Remove container and network", Do: func(ctx context.Context) error {
			if err := d.Host.RemoveUnit(user, quadlet.NetworkName(user)+".network"); err != nil {
				return err
			}
			d.Host.Podman(ctx, user, "network", "rm", "--force", quadlet.NetworkName(user))
			return nil
		}},
		{Name: "Remove edge proxy route", Do: func(ctx context.Context) error {
			return d.Edge.Remove(ctx, req.GetWebsiteId())
		}},
		{Name: "Remove Unix user", Do: func(ctx context.Context) error {
			// The names of the databases start with the Unix user. Remove them, so that a later
			// website with the same Unix user does not get them (CF-01).
			for _, u := range req.GetDatabaseUsers() {
				if err := d.dropUser(ctx, u.GetEngine(), u.GetName()); err != nil {
					return err
				}
			}
			for _, db := range req.GetDatabases() {
				if db.GetEngine() == "mariadb" {
					if _, err := d.sql(ctx, db.GetEngine(), mariaRevokeDatabase(db.GetName())); err != nil {
						return err
					}
				}
				if err := d.dropDatabase(ctx, db.GetEngine(), db.GetName()); err != nil {
					return err
				}
			}
			if err := os.RemoveAll(d.dumpDir(user)); err != nil {
				return err
			}
			if err := d.Host.RemoveUser(ctx, user); err != nil {
				return err
			}
			return d.State.Delete(req.GetWebsiteId())
		}},
	}}, nil
}

// SetWebsitePower starts, stops or restarts a website (FR-204).
func (d *Deps) SetWebsitePower(req *agentv1.SetWebsitePowerRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetWebsiteId()); err != nil {
		return jobs.Job{}, err
	}
	action := req.GetPower()
	if action != "start" && action != "stop" && action != "restart" {
		return jobs.Job{}, fmt.Errorf("the power action %q is not valid", action)
	}
	var site *Site
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Change container state", Do: func(ctx context.Context) error {
			var err error
			if site, err = d.State.Get(req.GetWebsiteId()); err != nil {
				return err
			}
			if site == nil {
				return errors.New("the agent does not know this website")
			}
			u := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}
			_, err = d.Host.Systemctl(ctx, site.UnixUser, action, u.ServiceName())
			return err
		}},
		{Name: "Health check", Do: func(ctx context.Context) error {
			if action == "stop" {
				return nil
			}
			return d.waitReady(ctx, site.Port, primaryDomain(site.Website))
		}},
	}}, nil
}

// SetLimits changes the cgroup limits of a running container, and writes them to the unit
// for the next start (FR-202).
func (d *Deps) SetLimits(req *agentv1.SetLimitsRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetWebsiteId()); err != nil {
		return jobs.Job{}, err
	}
	l := req.GetLimits()
	if l.GetVcpus() <= 0 || l.GetMemoryBytes() <= 0 || l.GetProcesses() <= 0 {
		return jobs.Job{}, errors.New("the limits must be more than zero")
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Update cgroup limits", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if site == nil {
				return errors.New("the agent does not know this website")
			}
			w := proto.Clone(site.Website).(*agentv1.Website)
			w.Limits = l
			u := d.unit(w, site.Port, site.Suffix)
			if _, err := d.Host.WriteUnit(site.UnixUser, u.UnitName(), quadlet.ContainerUnit(u)); err != nil {
				return err
			}
			if _, err := d.Host.Systemctl(ctx, site.UnixUser, "daemon-reload"); err != nil {
				return err
			}
			mem := fmt.Sprint(l.GetMemoryBytes())
			if _, err := d.Host.Podman(ctx, site.UnixUser, "update", "--cpus="+fmt.Sprint(l.GetVcpus()), "--memory="+mem, "--memory-swap="+mem, "--pids-limit="+fmt.Sprint(l.GetProcesses()), u.ContainerName()); err != nil {
				return err
			}
			site.Website = w
			return d.State.Put(site)
		}},
	}}, nil
}

// AddDomain and RemoveDomain change the domains of a website (FR-401 to FR-403).
func (d *Deps) AddDomain(req *agentv1.DomainRequest) (jobs.Job, error) {
	return d.changeDomain(req, true)
}

func (d *Deps) RemoveDomain(req *agentv1.DomainRequest) (jobs.Job, error) {
	return d.changeDomain(req, false)
}

func (d *Deps) changeDomain(req *agentv1.DomainRequest, add bool) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.Domain(req.GetDomain())); err != nil {
		return jobs.Job{}, err
	}
	if add && req.GetKind() != "redirect" && req.GetKind() != "alias" && req.GetKind() != "primary" {
		return jobs.Job{}, fmt.Errorf("the domain kind %q is not valid", req.GetKind())
	}
	route := jobs.Step{Name: "Add edge proxy route", Do: func(ctx context.Context) error {
		site, err := d.State.Get(req.GetWebsiteId())
		if err != nil {
			return err
		}
		if site == nil {
			return errors.New("the agent does not know this website")
		}
		w := proto.Clone(site.Website).(*agentv1.Website)
		var domains []*agentv1.Domain
		for _, dom := range w.GetDomains() {
			if dom.GetName() != req.GetDomain() {
				domains = append(domains, dom)
			}
		}
		// The panel adds the primary domain after the owner verifies it (CF-03). A website
		// has only one primary domain.
		if add && req.GetKind() == "primary" && primaryDomain(&agentv1.Website{Domains: domains}) != "" {
			return errors.New("the website has a primary domain")
		}
		if add {
			domains = append(domains, &agentv1.Domain{Name: req.GetDomain(), Kind: req.GetKind()})
		}
		w.Domains = domains
		if err := d.Edge.Apply(ctx, d.site(w, site.Port)); err != nil {
			return err
		}
		site.Website = w
		return d.State.Put(site)
	}}
	if !add {
		route.Name = "Remove edge proxy route"
		return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{route}}, nil
	}
	var dnsHere bool
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		route,
		{Name: "Check DNS", Do: func(ctx context.Context) error {
			dnsHere = d.DNSPointsHere(ctx, req.GetDomain())
			return nil
		}},
		// Caddy gets the certificate itself when the DNS points here (FR-404).
		{Name: "Get TLS certificate", Do: func(context.Context) error { return nil }},
	}, Result: func() map[string]any { return map[string]any{"dnsPointsHere": dnsHere} }}, nil
}
