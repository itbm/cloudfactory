package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const (
	websiteID = "01JBQ3Z5K8N6T4W2X9Y7R5P3M1"
	jobID     = "01JBQ3Z5K8N6T4W2X9Y7R5P3M2"
	jobID2    = "01JBQ3Z5K8N6T4W2X9Y7R5P3M3"
)

type fakeEdge struct {
	mu    sync.Mutex
	sites map[string]caddy.Site
}

func (f *fakeEdge) EnsureBase(context.Context) error { return nil }
func (f *fakeEdge) Apply(_ context.Context, s caddy.Site) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sites[s.WebsiteID] = s
	return nil
}
func (f *fakeEdge) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sites, id)
	return nil
}

func deps(t *testing.T) (*Deps, *execx.Fake, *fakeEdge) {
	dir := t.TempDir()
	run := &execx.Fake{}
	h := &host.Host{Run: run, SitesDir: filepath.Join(dir, "sites"), UnitRoot: filepath.Join(dir, "units"), LookupUID: func(string) (int, error) { return 1001, nil }}
	edge := &fakeEdge{sites: map[string]caddy.Site{}}
	d := &Deps{
		Host: h, Run: run, Edge: edge, State: NewState(dir),
		Gvisor:        quadlet.Runtime{RunscPath: "/usr/local/bin/runsc", Platform: "systrap"},
		Health:        func(context.Context, int, string) (int, error) { return 200, nil },
		DNSPointsHere: func(context.Context, string) bool { return true },
		Reboot:        func(context.Context) error { return nil },
	}
	return d, run, edge
}

func website(image string) *agentv1.Website {
	return &agentv1.Website{
		Id: websiteID, UnixUser: "acme_x7k2", Image: image, Runtime: "frankenphp",
		Extensions: []string{"opcache"}, PhpIni: map[string]string{"memory_limit": "256M"},
		Limits:  &agentv1.Limits{Vcpus: 1, MemoryBytes: 1 << 30, Processes: 100},
		Domains: []*agentv1.Domain{{Name: "acme.example", Kind: "primary"}},
	}
}

// run gives a function that runs all steps of a job in order, like the runner.
func run(t *testing.T) func(jobs.Job, error) error {
	return func(job jobs.Job, err error) error {
		t.Helper()
		return runSteps(t, job, err)
	}
}

func runSteps(t *testing.T, job jobs.Job, err error) error {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range job.Steps {
		if err := s.Do(context.Background()); err != nil {
			if job.OnFailure != nil {
				job.OnFailure(context.Background())
			}
			return err
		}
	}
	return nil
}

func TestCheckWebsiteRefusesBadValues(t *testing.T) {
	for _, change := range []func(*agentv1.Website){
		func(w *agentv1.Website) { w.UnixUser = "root" },
		func(w *agentv1.Website) { w.Image = "--privileged" },
		func(w *agentv1.Website) { w.Runtime = "runc" },
		func(w *agentv1.Website) { w.PhpIni["x"] = "1\nextension=/tmp/evil.so" },
		func(w *agentv1.Website) { w.Extensions = append(w.Extensions, "../evil") },
		func(w *agentv1.Website) { w.Domains = nil },
		func(w *agentv1.Website) { w.Domains[0].Name = "acme.example\nevil" },
	} {
		w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
		change(w)
		if err := CheckWebsite(w); err == nil {
			t.Errorf("accepted %+v", w)
		}
	}
}

func TestCreateWebsite(t *testing.T) {
	d, f, edge := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	job, err := d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})
	if len(job.Steps) != 6 {
		t.Fatalf("steps = %d, want 6 (the panel shows 6)", len(job.Steps))
	}
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"loginctl enable-linger acme_x7k2",
		"runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/1001 podman pull --quiet ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1",
		"systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service",
	} {
		if !f.Called(want) {
			t.Errorf("not called: %s\ncalls: %v", want, f.Calls)
		}
	}
	unit, err := os.ReadFile(filepath.Join(d.Host.UnitRoot, "1001", "cf-acme_x7k2.container"))
	if err != nil || !strings.Contains(string(unit), "--runtime=/usr/local/bin/runsc") {
		t.Errorf("unit: %v\n%s", err, unit)
	}
	if ini, _ := os.ReadFile(filepath.Join(d.Host.Home("acme_x7k2"), "php.ini")); !strings.Contains(string(ini), `memory_limit = "256M"`) {
		t.Errorf("php.ini = %s", ini)
	}
	if edge.sites[websiteID].Port != 20000 {
		t.Errorf("edge = %+v", edge.sites)
	}
	if job.Result()["dnsPointsHere"] != true {
		t.Error("no DNS result")
	}
}

func TestSetRuntimeSwitchesWithNoStop(t *testing.T) {
	d, f, edge := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))

	err := run(t)(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: website("ghcr.io/cloudfactory/frankenphp-php8.5:8.5.0-1")}))
	if err != nil {
		t.Fatal(err)
	}
	site, _ := d.State.Get(websiteID)
	if site.Port != 20001 || site.Suffix != "-b" || edge.sites[websiteID].Port != 20001 {
		t.Errorf("site = %+v edge = %+v", site, edge.sites[websiteID])
	}
	if !f.Called("systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2-b.service") || !f.Called("systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2.service") {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestSetRuntimeKeepsTheOldContainerWhenTheHealthCheckFails(t *testing.T) {
	d, f, edge := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	d.ReadyTimeout = 1
	d.Health = func(_ context.Context, port int, _ string) (int, error) {
		if port == 20001 {
			return 0, errors.New("connection refused")
		}
		return 200, nil
	}
	f.Calls = nil

	err := run(t)(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: website("ghcr.io/cloudfactory/frankenphp-php8.5:8.5.0-1")}))
	if err == nil {
		t.Fatal("the job did not fail")
	}
	site, _ := d.State.Get(websiteID)
	if site.Port != 20000 || site.Suffix != "" || edge.sites[websiteID].Port != 20000 {
		t.Errorf("the website did not stay on the old container: %+v %+v", site, edge.sites[websiteID])
	}
	if !f.Called("systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2-b.service") || f.Called("systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2.service") {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestDomainsChangeTheEdgeRoute(t *testing.T) {
	d, _, edge := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))

	if err := run(t)(d.AddDomain(&agentv1.DomainRequest{JobId: jobID2, WebsiteId: websiteID, Domain: "www.acme.example", Kind: "redirect"})); err != nil {
		t.Fatal(err)
	}
	if got := edge.sites[websiteID].Redirects; len(got) != 1 || got[0] != "www.acme.example" {
		t.Errorf("redirects = %v", got)
	}
	run(t)(d.RemoveDomain(&agentv1.DomainRequest{JobId: jobID2, WebsiteId: websiteID, Domain: "www.acme.example"}))
	if got := edge.sites[websiteID].Redirects; len(got) != 0 {
		t.Errorf("redirects after remove = %v", got)
	}
	if _, err := d.AddDomain(&agentv1.DomainRequest{JobId: jobID2, WebsiteId: websiteID, Domain: "x.example", Kind: "primary"}); err == nil {
		t.Error("a second primary domain was accepted")
	}
}

func TestCreateDatabaseSendsThePasswordOnStdin(t *testing.T) {
	d, f, _ := deps(t)
	req := &agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, Engine: "mariadb", Name: "shop", User: "shop", Password: "Abcdefghijklmnop1234"}
	if err := run(t)(d.CreateDatabase(req)); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, req.Password) {
			t.Fatalf("the password is in the arguments: %s", c)
		}
	}
	if !strings.Contains(strings.Join(f.Inputs, ""), "IDENTIFIED BY 'Abcdefghijklmnop1234'") {
		t.Errorf("inputs = %v", f.Inputs)
	}
	req.Password = "short'; DROP"
	if _, err := d.CreateDatabase(req); err == nil {
		t.Error("a bad password was accepted")
	}
}

func TestBackupGivesTheSnapshotID(t *testing.T) {
	d, f, _ := deps(t)
	os.MkdirAll(filepath.Join(d.Host.Home("acme_x7k2"), "app"), 0o755)
	f.Rules = []execx.Rule{{Prefix: "restic", Output: `{"message_type":"status"}` + "\n" + `{"message_type":"summary","snapshot_id":"1a2b3c4d","total_bytes_processed":2048}`}}

	job, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3://backups/cf", Databases: []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "shop"}}})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if r := job.Result(); r["snapshotId"] != "1a2b3c4d" || r["sizeBytes"] != int64(2048) {
		t.Errorf("result = %v", r)
	}
	if !f.Called("runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/1001 podman exec cf-mariadb mariadb-dump") || !f.Called("restic --repo s3:backups/cf") {
		t.Errorf("calls = %v", f.Calls)
	}
	if _, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Target: "/tmp"}); err == nil {
		t.Error("a local target was accepted")
	}
}

func TestRestoreRefusesAPathOutsideTheWebsite(t *testing.T) {
	d, _, _ := deps(t)
	_, err := d.Restore(&agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "file", Item: "/../../etc/shadow", Target: "s3://b/c"})
	if err == nil {
		t.Error("a path outside the website was accepted")
	}
}

func TestRebootResumesAfterTheReboot(t *testing.T) {
	d, _, _ := deps(t)
	job, err := d.RebootJob(&agentv1.HostRequest{JobId: jobID})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range job.Steps[:3] {
		if err := s.Do(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	resumed, ok := d.ResumeReboot()
	if !ok || resumed.ID != jobID || resumed.StartAt != 3 || len(resumed.Steps) != 5 {
		t.Fatalf("resume = %+v %v", resumed, ok)
	}
	if _, ok := d.ResumeReboot(); ok {
		t.Error("the reboot resumed two times")
	}
}

func TestTheWatchNeedsTheExpectedStatus(t *testing.T) {
	d, _, edge := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	d.MaxWatch = 50 * time.Millisecond
	// The new container answers, but with status 500 on each check after the switch.
	d.Health = func(_ context.Context, port int, _ string) (int, error) {
		if port == 20001 && edge.sites[websiteID].Port == 20001 {
			return 500, nil
		}
		return 200, nil
	}

	err := run(t)(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: website("ghcr.io/cloudfactory/frankenphp-php8.5:8.5.0-1"), HealthCheck: &agentv1.HealthCheck{Status: 200, MaxErrorRate: 0.01, Seconds: 300}}))
	if err == nil || !strings.Contains(err.Error(), "health checks failed") {
		t.Fatalf("err = %v", err)
	}
	if edge.sites[websiteID].Port != 20000 {
		t.Errorf("traffic did not go back to the old container: %+v", edge.sites[websiteID])
	}
}
