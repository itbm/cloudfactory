package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/images"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const (
	websiteID = "01JBQ3Z5K8N6T4W2X9Y7R5P3M1"
	jobID     = "01JBQ3Z5K8N6T4W2X9Y7R5P3M2"
	jobID2    = "01JBQ3Z5K8N6T4W2X9Y7R5P3M3"
	// customerID is the ID of the customer of a website that uses the database servers of its
	// customer. customerKey is the key of the record of the customer, and customerAddress is
	// the address of the first customer of a server.
	customerID      = "01JBQ3Z5K8N6T4W2X9Y7R5P3C1"
	customerKey     = "01jbq3z5k8n6t4w2x9y7r5p3c1"
	customerAddress = "169.254.80.2"
)

type fakeEdge struct {
	mu    sync.Mutex
	sites map[string]caddy.Site
	// panel is the certificate method of the panel domain, and root is the root certificate
	// of the panel CA.
	panel, root string
	// methods are the methods that SetPanelCertificate got, in order.
	methods []string
}

func (f *fakeEdge) PanelCertificate(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panel, nil
}
func (f *fakeEdge) SetPanelCertificate(_ context.Context, _, method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.panel = method
	f.methods = append(f.methods, method)
	return nil
}
func (f *fakeEdge) PanelCARoot(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.root, nil
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
	h := &host.Host{Run: run, SitesDir: filepath.Join(dir, "sites"), UnitRoot: filepath.Join(dir, "units"), ConfigRoot: filepath.Join(dir, "config"), LookupUID: func(string) (int, error) { return 20001, nil }}
	edge := &fakeEdge{sites: map[string]caddy.Site{}}
	d := &Deps{
		Host: h, Run: run, Edge: edge, State: NewState(dir),
		Gvisor:        quadlet.Runtime{RunscPath: "/usr/local/bin/runsc", Platform: "systrap"},
		Health:        func(context.Context, int, string) (int, error) { return 200, nil },
		DNSPointsHere: func(context.Context, string) bool { return true },
		Reboot:        func(context.Context) error { return nil },
		DataDir:       filepath.Join(dir, "data"),
		SecretsDir:    filepath.Join(dir, "secrets"),
		DBHome:        filepath.Join(dir, "cf-db"),
		SftpKeysDir:   filepath.Join(dir, "sftp"),
		SftpUsersDir:  filepath.Join(dir, "sftp-users"),
		// The tests never read or change the firewall and the units of the computer. With no
		// firewall file, the agent does not run nft.
		FirewallFile: filepath.Join(dir, "cloudfactory.nft"),
		FirewallDir:  filepath.Join(dir, "firewall"),
		AddressUnit:  filepath.Join(dir, "cloudfactory-address.service"),
		// A server with no loop devices: the disk of a database server is a directory, and
		// the tests mount nothing.
		LoopControl: filepath.Join(dir, "no-loop-control"),
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
		func(w *agentv1.Website) { w.Domains = []*agentv1.Domain{{Name: "x.example", Kind: "alias"}} },
		func(w *agentv1.Website) {
			w.Domains = append(w.Domains, &agentv1.Domain{Name: "y.example", Kind: "primary"})
		},
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
		"runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman pull --quiet ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1",
		"systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service",
	} {
		if !f.Called(want) {
			t.Errorf("not called: %s\ncalls: %v", want, f.Calls)
		}
	}
	unit, err := os.ReadFile(filepath.Join(d.Host.UnitRoot, "20001", "cf-acme_x7k2.container"))
	if err != nil || !strings.Contains(string(unit), "--runtime=/usr/local/bin/runsc") {
		t.Errorf("unit: %v\n%s", err, unit)
	}
	if ini, _ := os.ReadFile(filepath.Join(d.Host.ConfigDir("acme_x7k2"), "php.ini")); !strings.Contains(string(ini), `memory_limit = "256M"`) {
		t.Errorf("php.ini = %s", ini)
	}
	if edge.sites[websiteID].Port != 20000 {
		t.Errorf("edge = %+v", edge.sites)
	}
	if job.Result()["dnsPointsHere"] != true {
		t.Error("no DNS result")
	}
	// A new PHP website gets the default page. The website user writes it, and only in an empty directory.
	page := `runuser -u acme_x7k2 -- sh -c [ -n "$(ls -A "$1" 2>/dev/null)" ] || cat >"$1/index.php" sh ` + filepath.Join(d.Host.AppDir("acme_x7k2"), "public")
	if !f.Called(page) {
		t.Errorf("no default page: %v", f.Calls)
	}
}

// A custom image and a website with a different document root get no default page.
func TestNoDefaultPageForACustomImage(t *testing.T) {
	d, f, _ := deps(t)
	custom := website("ghcr.io/acme/app:1")
	custom.Runtime, custom.Extensions, custom.PhpIni = "custom", nil, nil
	d.writeDefaultPage(context.Background(), custom)
	other := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	other.DocumentRoot = "web"
	d.writeDefaultPage(context.Background(), other)
	if f.Called("runuser -u acme_x7k2 -- sh -c") {
		t.Errorf("calls = %v", f.Calls)
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

// sharedDeps gives deps with a shared image store. Each pull makes an empty store.
func sharedDeps(t *testing.T) (*Deps, *execx.Fake) {
	d, f, _ := deps(t)
	dir := t.TempDir()
	d.Images = &images.Store{Run: f, Root: filepath.Join(dir, "images"), Config: filepath.Join(dir, "website-storage.conf"), RunRoot: filepath.Join(dir, "run"), Group: host.SitesGroup, MountProgram: images.FuseOverlayfs}
	d.Host.StorageConfig = d.Images.Config
	f.Rules = append(f.Rules, execx.Rule{Prefix: "podman pull --quiet ", Do: func() error {
		entries, err := os.ReadDir(d.Images.Root)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".pull-") {
				return nil
			}
		}
		// The fake does not know the reference, so the test pulls one image at a time.
		return os.MkdirAll(filepath.Join(d.Images.Root, ".pull-"+images.StoreName(sharedRef)), 0o700)
	}})
	if err := d.ShareImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d, f
}

const sharedDigest = "sha256:08af88488ce218936d530b80f6f55dc2afe4d0d91f13f9e6ae350a9e7752128e"
const sharedRef = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1@" + sharedDigest

func TestCreateWebsiteUsesTheSharedImageStore(t *testing.T) {
	d, f := sharedDeps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.ImageDigest = sharedDigest
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
		t.Fatal(err)
	}
	// Root pulls the image into the shared store. The website user does not pull.
	if !f.Called("podman pull --quiet "+sharedRef) || f.Called("runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman pull") {
		t.Errorf("calls = %v", f.Calls)
	}
	if _, err := os.Stat(filepath.Join(d.Images.Root, images.StoreName(sharedRef))); err != nil {
		t.Error(err)
	}

	// A second website with the same image starts with no pull.
	f.Calls = nil
	w2 := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w2.Id, w2.UnixUser, w2.ImageDigest = "01JBQ3Z5K8N6T4W2X9Y7R5P3M9", "bravo_a1b2", sharedDigest
	w2.Domains = []*agentv1.Domain{{Name: "bravo.example", Kind: "primary"}}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID2, Website: w2})); err != nil {
		t.Fatal(err)
	}
	if f.Called("podman pull") {
		t.Errorf("the second website pulled: %v", f.Calls)
	}
}

func TestACustomImageStaysWithTheWebsiteUser(t *testing.T) {
	d, f := sharedDeps(t)
	w := website("registry.example/acme/app:1.0")
	w.Runtime, w.ImageDigest = "custom", sharedDigest
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
		t.Fatal(err)
	}
	if !f.Called("runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman pull --quiet registry.example/acme/app:1.0@"+sharedDigest) || f.Called("podman pull") {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestSetRuntimeRemovesTheOldCopyOfTheWebsiteUser(t *testing.T) {
	d, f := sharedDeps(t)
	// A website from before the shared store, with its own copy of the image.
	old := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	if err := d.State.Put(&Site{ID: websiteID, UnixUser: "acme_x7k2", Port: 20000, Website: old}); err != nil {
		t.Fatal(err)
	}
	// The user of an existing website gets the shared storage configuration when the agent starts.
	if err := d.ShareImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.Called("ln -sfn " + d.Images.Config + " " + d.Host.Home("acme_x7k2") + "/.config/containers/storage.conf") {
		t.Errorf("calls = %v", f.Calls)
	}
	const id = "745a3dfee8c74d346f6d0d6d58de5ba21cb2113df5bde601fe1019622640b993"
	f.Rules = append([]execx.Rule{
		{Prefix: "runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman image inspect --format {{.Id}} ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1", Output: id + "\n"},
		{Prefix: "runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman images --noheading --no-trunc --filter readonly=false", Output: "sha256:" + id + "\n"},
	}, f.Rules...)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.ImageDigest = sharedDigest
	if err := run(t)(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: w})); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"podman pull --quiet " + sharedRef,
		"runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman rmi --ignore " + id,
	} {
		if !f.Called(want) {
			t.Errorf("not called: %s\ncalls: %v", want, f.Calls)
		}
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
	// A new primary domain makes the old primary domain an alias. Redirects go to the new one.
	run(t)(d.AddDomain(&agentv1.DomainRequest{JobId: jobID2, WebsiteId: websiteID, Domain: "www.acme.example", Kind: "redirect"}))
	if err := run(t)(d.AddDomain(&agentv1.DomainRequest{JobId: jobID2, WebsiteId: websiteID, Domain: "x.example", Kind: "primary"})); err != nil {
		t.Fatal(err)
	}
	site, _ := d.State.Get(websiteID)
	if err := CheckWebsite(site.Website); err != nil {
		t.Fatal(err)
	}
	if got := edge.sites[websiteID]; got.Primary != "x.example" || len(got.Hosts) != 2 || got.Redirects[0] != "www.acme.example" {
		t.Errorf("edge site = %+v", got)
	}
}

func TestCreateDatabaseSendsThePasswordOnStdin(t *testing.T) {
	d, f, _ := deps(t)
	req := &agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", User: "acme_x7k2_shop", Password: "Abcdefghijklmnop1234", Access: "website"}
	if err := run(t)(d.CreateDatabase(req)); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, req.Password) {
			t.Fatalf("the password is in the arguments: %s", c)
		}
	}
	in := strings.Join(f.Inputs, "")
	for _, want := range []string{
		"CREATE DATABASE `acme_x7k2_shop` CHARACTER SET",
		"CREATE USER 'acme_x7k2_shop'@'169.254.77.1' IDENTIFIED BY 'Abcdefghijklmnop1234' REQUIRE NONE;",
		"GRANT ALL PRIVILEGES ON `acme\\_x7k2\\_shop`.* TO 'acme_x7k2_shop'@'169.254.77.1';",
	} {
		if !strings.Contains(in, want) {
			t.Errorf("inputs do not contain %s:\n%s", want, in)
		}
	}
	if strings.Contains(in, "IF NOT EXISTS") || strings.Contains(in, "'%'") {
		t.Errorf("inputs = %s", in)
	}
	req.Password = "short'; DROP"
	if _, err := d.CreateDatabase(req); err == nil {
		t.Error("a bad password was accepted")
	}
}

// CF-01: the agent refuses a name that does not start with the Unix user of the website, and a
// system name, even when the panel sends it.
func TestCreateDatabaseRefusesSystemAndForeignNames(t *testing.T) {
	d, _, _ := deps(t)
	for _, name := range []string{"mysql", "postgres", "sys", "information_schema", "template1", "other_x7k2_shop", "acme_x7k2_", "acme_x7k2"} {
		req := &agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: name}
		if _, err := d.CreateDatabase(req); err == nil {
			t.Errorf("the name %q was accepted", name)
		}
		req = &agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", User: name, Password: "Abcdefghijklmnop1234"}
		if _, err := d.CreateDatabase(req); err == nil {
			t.Errorf("the user %q was accepted", name)
		}
	}
}

// CF-01: the job fails when the database or the user exists, so that a tenant cannot get access
// to an object that exists.
func TestCreateDatabaseFailsWhenTheDatabaseExists(t *testing.T) {
	d, f, _ := deps(t)
	f.Rules = []execx.Rule{{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec -i cf-postgresql", Output: "1\n"}}
	err := run(t)(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "postgresql", Name: "acme_x7k2_shop"}))
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("err = %v", err)
	}
	for _, in := range f.Inputs {
		if strings.Contains(in, "CREATE DATABASE") || strings.Contains(in, "GRANT") {
			t.Errorf("the agent changed the database server: %s", in)
		}
	}
}

func TestPostgreSQLDatabaseHasGroupRoles(t *testing.T) {
	d, f, _ := deps(t)
	req := &agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "postgresql", Name: "acme_x7k2_shop", User: "acme_x7k2_shop", Password: "Abcdefghijklmnop1234"}
	if err := run(t)(d.CreateDatabase(req)); err != nil {
		t.Fatal(err)
	}
	in := strings.Join(f.Inputs, "")
	for _, want := range []string{
		`CREATE DATABASE "acme_x7k2_shop" OWNER "acme_x7k2_shop:rw";`,
		`REVOKE ALL ON DATABASE "acme_x7k2_shop" FROM PUBLIC;`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "acme_x7k2_shop:rw" IN SCHEMA public GRANT SELECT ON TABLES TO "acme_x7k2_shop:ro";`,
		`CREATE ROLE "acme_x7k2_shop" LOGIN PASSWORD`,
		`GRANT "acme_x7k2_shop:rw" TO "acme_x7k2_shop";`,
		`ALTER ROLE "acme_x7k2_shop" IN DATABASE "acme_x7k2_shop" SET role = 'acme_x7k2_shop:rw';`,
	} {
		if !strings.Contains(in, want) {
			t.Errorf("inputs do not contain %s", want)
		}
	}
}

// The user's example: one sync user with read-only access to production and read-write access
// to staging.
func TestSetDatabaseUserGivesAPermissionForEachDatabase(t *testing.T) {
	for _, engine := range []string{"mariadb", "postgresql"} {
		d, f, _ := deps(t)
		f.Rules = []execx.Rule{{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec -i cf-mariadb mariadb", Output: "169.254.77.1\n"}}
		req := &agentv1.SetDatabaseUserRequest{
			JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine, Name: "acme_x7k2_sync", Access: "anywhere",
			Grants: []*agentv1.DatabaseGrant{
				{Database: "acme_x7k2_prod", Privileges: "readonly"},
				{Database: "acme_x7k2_staging", Privileges: "readwrite"},
			},
			WebsiteDatabases: []string{"acme_x7k2_old", "acme_x7k2_prod", "acme_x7k2_staging"},
		}
		if err := run(t)(d.SetDatabaseUser(req)); err != nil {
			t.Fatal(err)
		}
		in := strings.Join(f.Inputs, "")
		want := []string{
			"RENAME USER 'acme_x7k2_sync'@'169.254.77.1' TO 'acme_x7k2_sync'@'%';",
			"ALTER USER 'acme_x7k2_sync'@'%' REQUIRE SSL;",
			"REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'acme_x7k2_sync'@'%';",
			"GRANT SELECT, SHOW VIEW ON `acme\\_x7k2\\_prod`.* TO 'acme_x7k2_sync'@'%';",
			"GRANT ALL PRIVILEGES ON `acme\\_x7k2\\_staging`.* TO 'acme_x7k2_sync'@'%';",
		}
		if engine == "postgresql" {
			want = []string{
				`REVOKE "acme_x7k2_old:rw" FROM "acme_x7k2_sync";`,
				`GRANT "acme_x7k2_prod:ro" TO "acme_x7k2_sync";`,
				`GRANT "acme_x7k2_staging:rw" TO "acme_x7k2_sync";`,
				`ALTER ROLE "acme_x7k2_sync" IN DATABASE "acme_x7k2_staging" SET role = 'acme_x7k2_staging:rw';`,
			}
			if strings.Contains(in, `GRANT "acme_x7k2_prod:rw"`) {
				t.Error("the sync user can change production")
			}
		}
		for _, w := range want {
			if !strings.Contains(in, w) {
				t.Errorf("%s: inputs do not contain %s:\n%s", engine, w, in)
			}
		}
	}
}

func TestSetDatabaseUserRefusesADatabaseOfADifferentWebsite(t *testing.T) {
	d, _, _ := deps(t)
	for _, g := range []*agentv1.DatabaseGrant{
		{Database: "other_x7k2_prod", Privileges: "readonly"},
		{Database: "acme_x7k2_prod", Privileges: "superuser"},
		{Database: "acme_x7k2_notlisted", Privileges: "readonly"},
	} {
		_, err := d.SetDatabaseUser(&agentv1.SetDatabaseUserRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_sync",
			Grants: []*agentv1.DatabaseGrant{g}, WebsiteDatabases: []string{"acme_x7k2_prod", "other_x7k2_prod"}})
		if err == nil {
			t.Errorf("accepted %v", g)
		}
	}
}

// CF-01: MariaDB keeps the grants of a database that it drops. The agent removes them first.
func TestDeleteDatabaseRemovesTheGrants(t *testing.T) {
	d, f, _ := deps(t)
	if err := run(t)(d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", Users: []string{"acme_x7k2_sync"}})); err != nil {
		t.Fatal(err)
	}
	in := strings.Join(f.Inputs, "")
	if !strings.Contains(in, `DELETE FROM mysql.db WHERE Db IN ('acme_x7k2_shop', 'acme\\_x7k2\\_shop');`) || !strings.Contains(in, "DROP DATABASE IF EXISTS `acme_x7k2_shop`;") {
		t.Errorf("inputs = %s", in)
	}
	if strings.Index(in, "DELETE FROM mysql.db") > strings.Index(in, "DROP DATABASE") {
		t.Error("the agent dropped the database before it removed the grants")
	}
}

func TestBackupGivesTheSnapshotID(t *testing.T) {
	d, f, _ := deps(t)
	os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755)
	dumps := filepath.Join(d.DataDir, "dumps", "acme_x7k2")
	var dumpMode os.FileMode
	var dumpNames []string
	f.Rules = []execx.Rule{
		{Prefix: "restic --repo s3:backups/cf --password-file  --json --retry-lock 30m backup", Output: `{"message_type":"status"}` + "\n" + `{"message_type":"summary","snapshot_id":"1a2b3c4d","total_bytes_processed":2048}`, Do: func() error {
			// The state of the dump directory at the time of the upload.
			if info, err := os.Stat(dumps); err == nil {
				dumpMode = info.Mode().Perm()
			}
			entries, _ := os.ReadDir(dumps)
			for _, e := range entries {
				dumpNames = append(dumpNames, e.Name())
			}
			return nil
		}},
		{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec cf-mariadb mariadb-dump", Output: "CREATE TABLE t (id int);\n"},
	}

	job, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3:backups/cf", Databases: []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "acme_x7k2_shop"}}})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("CREATE TABLE t (id int);\n"))
	r := job.Result()
	if r["snapshotId"] != "1a2b3c4d" || r["sizeBytes"] != int64(2048) || r["dumps"].(map[string]any)["mariadb:acme_x7k2_shop"] != hex.EncodeToString(sum[:]) {
		t.Errorf("result = %v", r)
	}
	// CF-06: the dumps are in the data directory of the agent, not in the home of the website.
	if dumpMode != 0o700 || len(dumpNames) != 1 || dumpNames[0] != "mariadb-acme_x7k2_shop.dump" {
		t.Errorf("dump directory at the upload: mode %o, files %v", dumpMode, dumpNames)
	}
	// Audit finding R8: the dumps do not stay on the disk after the upload.
	if _, err := os.Stat(dumps); !os.IsNotExist(err) {
		t.Errorf("the dumps are on the disk after the backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.Host.SiteRoot("acme_x7k2"), ".cloudfactory")); !os.IsNotExist(err) {
		t.Error("the agent wrote a dump in the home of the website")
	}
	if !f.Called("restic --repo s3:backups/cf --password-file  --json --retry-lock 30m backup " + d.Host.AppDir("acme_x7k2") + " " + dumps) {
		t.Errorf("calls = %v", f.Calls)
	}
	if _, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "/tmp"}); err == nil {
		t.Error("a local target was accepted")
	}
	// The kind is a tag of the snapshot. A value with a comma would add a second tag.
	for _, kind := range []string{"", "nightly", "manual,website=" + websiteID} {
		if _, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: kind, Target: "s3:backups/cf"}); err == nil {
			t.Errorf("the kind %q was accepted", kind)
		}
	}
}

// Audit finding R8: a backup that fails does not leave the dumps on the disk.
func TestAFailedBackupRemovesTheDumps(t *testing.T) {
	d, f, _ := deps(t)
	os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755)
	f.Rules = []execx.Rule{
		{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec cf-mariadb mariadb-dump --user=root --single-transaction --routines acme_x7k2_blog", Err: errors.New("mariadb-dump: exit status 2")},
		{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec cf-mariadb mariadb-dump", Output: "CREATE TABLE t (id int);\n"},
	}
	job, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "scheduled", Target: "s3:backups/cf",
		Databases: []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "acme_x7k2_shop"}, {Engine: "mariadb", Name: "acme_x7k2_blog"}}})
	if err := run(t)(job, err); err == nil {
		t.Fatal("the backup did not fail")
	}
	if _, err := os.Stat(filepath.Join(d.DataDir, "dumps", "acme_x7k2")); !os.IsNotExist(err) {
		t.Errorf("the dumps are on the disk after the failed backup: %v", err)
	}
}

// Audit finding R8: a full restore can remove the files that are not in the snapshot. The
// website user removes them, after restic has the snapshot on the server.
func TestAFullRestoreCanRemoveTheOtherFiles(t *testing.T) {
	d, f, _ := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	app := d.Host.AppDir("acme_x7k2")
	req := &agentv1.RestoreRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "website", Target: "s3:b/c", DeleteExtra: true}
	f.Calls = nil
	if err := run(t)(d.Restore(req)); err != nil {
		t.Fatal(err)
	}
	order := []string{}
	for _, c := range f.Calls {
		switch {
		case strings.Contains(c, " restore 1a2b3c4d "):
			order = append(order, "download")
		case c == "runuser -u acme_x7k2 -- find "+app+" -mindepth 1 -type d -exec chmod u+rwx -- {} +":
			order = append(order, "open")
		case c == "runuser -u acme_x7k2 -- find "+app+" -mindepth 1 -delete":
			order = append(order, "remove")
		case strings.Contains(c, "| runuser -u acme_x7k2 -- tar --extract"):
			order = append(order, "copy")
		}
	}
	if strings.Join(order, " ") != "download open remove copy" {
		t.Errorf("order = %v\ncalls = %v", order, f.Calls)
	}
	// Without the option, the agent removes nothing.
	req.DeleteExtra = false
	f.Calls = nil
	if err := run(t)(d.Restore(req)); err != nil {
		t.Fatal(err)
	}
	if f.Called("runuser -u acme_x7k2 -- find") {
		t.Errorf("the agent removed files without the option: %v", f.Calls)
	}
	// The option is only for the full website.
	if _, err := d.Restore(&agentv1.RestoreRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "file", Item: "/public/index.php", Target: "s3:b/c", DeleteExtra: true}); err == nil {
		t.Error("the restore of one file can remove the other files")
	}
}

// CF-05: restic restores into an empty directory of root, and the website user copies the files.
func TestRestoreCopiesFilesAsTheWebsiteUser(t *testing.T) {
	d, f, _ := deps(t)
	err := run(t)(d.Restore(&agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "file", Item: "/public/index.php", Target: "s3:b/c"}))
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(d.DataDir, "restore", jobID)
	app := d.Host.AppDir("acme_x7k2")
	if !f.Called("restic --repo s3:b/c --password-file  --json --retry-lock 30m restore 1a2b3c4d --target " + staging + " --include " + app + "/public/index.php") {
		t.Errorf("calls = %v", f.Calls)
	}
	want := "tar --create --file=- --directory=" + staging + app + " -- public/index.php | runuser -u acme_x7k2 -- tar --extract --file=- --directory=" + app + " --no-same-owner"
	if !f.Called(want) {
		t.Errorf("not called: %s\ncalls: %v", want, f.Calls)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "--target /") && !strings.Contains(c, "--target "+staging) {
			t.Errorf("restic restored to the root: %s", c)
		}
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("the agent did not remove the staging directory")
	}
}

// CF-06: the agent restores a dump only when its hash agrees, and as a temporary user of that
// database only, with no client commands.
func TestRestoreDatabaseChecksTheHashAndUsesATemporaryUser(t *testing.T) {
	d, f, _ := deps(t)
	dump := []byte("CREATE TABLE t (id int);\n")
	sum := sha256.Sum256(dump)
	staging := filepath.Join(d.DataDir, "restore", jobID)
	file := filepath.Join(staging, d.DataDir, "dumps", "acme_x7k2", "mariadb-acme_x7k2_shop.dump")
	// The fake restic "restores" the dump.
	f.Rules = []execx.Rule{{Prefix: "restic --repo s3:b/c --password-file  --json --retry-lock 30m restore", Do: func() error { return writeFile(file, dump) }}}
	req := &agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "database", Item: "mariadb:acme_x7k2_shop", Target: "s3:b/c", DumpSha256: hex.EncodeToString(sum[:])}
	if err := run(t)(d.Restore(req)); err != nil {
		t.Fatal(err)
	}
	var restore string
	for _, c := range f.Calls {
		if strings.Contains(c, "--env-file") {
			restore = c
		}
	}
	if !strings.Contains(restore, "mariadb --sandbox --user=cfrestore_") || strings.Contains(restore, "--user=root") || !strings.HasSuffix(restore, "--batch acme_x7k2_shop") {
		t.Errorf("restore command = %q", restore)
	}
	in := strings.Join(f.Inputs, "")
	if !strings.Contains(in, "GRANT ALL PRIVILEGES ON `acme\\_x7k2\\_shop`.* TO 'cfrestore_") || !strings.Contains(in, "DROP USER IF EXISTS 'cfrestore_") {
		t.Errorf("inputs = %s", in)
	}
	if entries, _ := os.ReadDir(d.SecretsDir); len(entries) != 0 {
		t.Errorf("the agent did not remove the password file: %v", entries)
	}

	// A dump that the hash does not agree with is not restored.
	f.Rules = []execx.Rule{{Prefix: "restic --repo s3:b/c --password-file  --json --retry-lock 30m restore", Do: func() error { return writeFile(file, []byte("DROP DATABASE mysql;\n")) }}}
	f.Calls, f.Inputs = nil, nil
	if err := run(t)(d.Restore(req)); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("err = %v", err)
	}
	if f.Called("runuser -u cf-db") {
		t.Errorf("the agent sent a changed dump to the database server: %v", f.Calls)
	}
	req.DumpSha256 = ""
	if _, err := d.Restore(req); err == nil {
		t.Error("a restore with no hash was accepted")
	}
}

// writeFile makes the file that a fake command makes.
func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// CF-05: a staging copy does not run cp as root in the files of a tenant.
func TestStagingCopyCopiesAsTheWebsiteUsers(t *testing.T) {
	d, f, _ := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.UnixUser = "staging_ab12"
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w, CopyFrom: "01JBQ3Z5K8N6T4W2X9Y7R5P3M9", CopyFromUnixUser: "acme_x7k2"})); err != nil {
		t.Fatal(err)
	}
	want := "runuser -u acme_x7k2 -- tar --create --file=- --directory=" + d.Host.AppDir("acme_x7k2") + " -- . | runuser -u staging_ab12 -- tar --extract"
	if !f.Called(want) || f.Called("cp ") || f.Called("chown -R") {
		t.Errorf("calls = %v", f.Calls)
	}
}

// CF-01: a website that the panel removes takes its databases and users with it.
func TestDeleteWebsiteRemovesItsDatabases(t *testing.T) {
	d, f, _ := deps(t)
	// The databases are in a container that the server has.
	if err := d.EnsureDatabaseService(context.Background(), dbServer{Engine: "postgresql"}); err != nil {
		t.Fatal(err)
	}
	req := &agentv1.DeleteWebsiteRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2",
		Databases:     []*agentv1.DatabaseRef{{Engine: "postgresql", Name: "acme_x7k2_shop"}, {Engine: "mariadb", Name: "acme_x7k2_blog"}},
		DatabaseUsers: []*agentv1.DatabaseRef{{Engine: "postgresql", Name: "acme_x7k2_sync"}}}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	// The server has no MariaDB container, so the job does not start one to remove a database.
	if f.Called("systemctl --user --machine=cf-db@ start cf-mariadb.service") || f.Called("systemctl --user --machine=cf-db@ restart cf-mariadb.service") {
		t.Errorf("the job started a MariaDB container: %v", f.Calls)
	}
	in := strings.Join(f.Inputs, "")
	if !strings.Contains(in, `DROP ROLE IF EXISTS "acme_x7k2_sync";`) || !strings.Contains(in, `DROP DATABASE IF EXISTS "acme_x7k2_shop" WITH (FORCE);`) {
		t.Errorf("inputs = %s", in)
	}
	req.Databases[0].Name = "mysql"
	if _, err := d.DeleteWebsite(req); err == nil {
		t.Error("the agent accepted a system database")
	}
}

// Audit finding B7: a second delete job completes the work of a first job that removed the
// Unix user and then failed.
func TestDeleteWebsiteCanRunAgainAfterTheUserIsGone(t *testing.T) {
	d, f, edge := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")})); err != nil {
		t.Fatal(err)
	}
	// The first job removed the user. The record of the website is still in the state.
	d.Host.LookupUID = func(name string) (int, error) { return 0, user.UnknownUserError(name) }
	f.Calls = nil
	req := &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2"}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	if f.Called("systemctl --user") || f.Called("userdel") || f.Called("loginctl") {
		t.Errorf("a command ran for a user that does not exist: %v", f.Calls)
	}
	if site, err := d.State.Get(websiteID); err != nil || site != nil {
		t.Errorf("the website is still in the state: %+v %v", site, err)
	}
	if _, ok := edge.sites[websiteID]; ok {
		t.Error("the edge route is still there")
	}
	if _, err := os.Stat(d.Host.SiteRoot("acme_x7k2")); !os.IsNotExist(err) {
		t.Errorf("the files of the website are still there: %v", err)
	}
}

// CF-03: a website with no verified domain starts, and gets its primary domain later.
func TestAWebsiteCanStartWithNoDomain(t *testing.T) {
	d, _, edge := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.Domains = nil
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
		t.Fatal(err)
	}
	if len(edge.sites[websiteID].Hosts) != 0 {
		t.Errorf("edge = %+v", edge.sites[websiteID])
	}
	if err := run(t)(d.AddDomain(&agentv1.DomainRequest{JobId: jobID2, WebsiteId: websiteID, Domain: "acme.example", Kind: "primary"})); err != nil {
		t.Fatal(err)
	}
	if got := edge.sites[websiteID]; len(got.Hosts) != 1 || got.Primary != "acme.example" {
		t.Errorf("edge = %+v", got)
	}
}

func TestRestoreRefusesAPathOutsideTheWebsite(t *testing.T) {
	d, _, _ := deps(t)
	_, err := d.Restore(&agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "file", Item: "/../../etc/shadow", Target: "s3:b/c"})
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
	// The shutdown sends the stop signal to the agent a short time after the reboot command.
	stopping := make(chan struct{})
	d.Stopping = stopping
	d.Reboot = func(context.Context) error {
		time.AfterFunc(20*time.Millisecond, func() { close(stopping) })
		return nil
	}
	for i, s := range job.Steps[:3] {
		err := s.Do(context.Background())
		// Audit finding R10: the reboot step ends the job with no result.
		if i == 2 && !errors.Is(err, jobs.ErrInterrupted) {
			t.Fatalf("the reboot step gave %v", err)
		}
		if i < 2 && err != nil {
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

// Audit finding R10: a server that does not shut down fails the job, and the job does not
// continue after a later restart of the agent.
func TestARebootThatDoesNotStartFailsTheJob(t *testing.T) {
	d, _, _ := deps(t)
	d.RebootWait = 20 * time.Millisecond
	job, err := d.RebootJob(&agentv1.HostRequest{JobId: jobID})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Steps[2].Do(context.Background()); err == nil || errors.Is(err, jobs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := d.ResumeReboot(); ok {
		t.Error("the job continues after a reboot that did not start")
	}

	d.Reboot = func(context.Context) error { return errors.New("systemctl: access denied") }
	job, _ = d.RebootJob(&agentv1.HostRequest{JobId: jobID2})
	if err := job.Steps[2].Do(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if _, ok := d.ResumeReboot(); ok {
		t.Error("the job continues after a reboot command that failed")
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
