package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// Tests for the findings of the completeness audit.

func unitFile(t *testing.T, d *Deps, user, name string) string {
	t.Helper()
	dir, _ := d.Host.UnitDir(user)
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read unit %s: %v", name, err)
	}
	return string(b)
}

// Blocker 1: the agent starts the shared database container before the first statement.
func TestTheFirstDatabaseStartsTheDatabaseContainer(t *testing.T) {
	d, f, _ := deps(t)
	d.ServiceImage = func(name string) string { return "ghcr.io/cloudfactory/" + name + ":0.1.0" }
	f.Rules = []execx.Rule{{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/1001 podman image exists", Err: errors.New("no image")}}
	job, err := d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "postgresql", Name: "acme_x7k2_shop"})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/1001 podman pull --quiet ghcr.io/cloudfactory/postgresql:0.1.0",
		"install -d -o cf-db -g cf-db -m 0700 " + filepath.Join(d.DBHome, "postgresql"),
		"systemctl --user --machine=cf-db@ restart cf-postgresql.service",
	} {
		if !f.Called(want) {
			t.Errorf("no call %q in %v", want, f.Calls)
		}
	}
	u := unitFile(t, d, DBUser, "cf-postgresql.container")
	for _, want := range []string{"PublishPort=5432:5432", "--auth-host=scram-sha-256", "Volume=" + filepath.Join(d.DBHome, "postgresql") + ":/var/lib/postgresql/data:Z"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit has no %q:\n%s", want, u)
		}
	}
	secret, err := os.ReadFile(filepath.Join(d.DBHome, "postgresql.env"))
	if err != nil || !strings.HasPrefix(string(secret), "POSTGRES_PASSWORD=") {
		t.Errorf("superuser secret: %q %v", secret, err)
	}
	// The container starts once. Later statements use it.
	n := len(f.Calls)
	run(t)(d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "postgresql", Name: "acme_x7k2_shop"}))
	for _, c := range f.Calls[n:] {
		if strings.Contains(c, "systemctl") {
			t.Errorf("the container started again: %s", c)
		}
	}
}

// Blocker 2: each website container has a host name for each database engine.
func TestWebsitesHaveDatabaseHostNames(t *testing.T) {
	d, _, _ := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container")
	for _, want := range []string{"AddHost=mariadb.internal:host-gateway\n", "AddHost=postgres.internal:host-gateway\n", "Environment=CF_DOCUMENT_ROOT=/app/public\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit has no %q:\n%s", want, u)
		}
	}
}

// Blocker 3: the agent makes the repository, and restic gets the credentials in its environment.
func TestBackupMakesTheRepositoryAndUsesTheCredentials(t *testing.T) {
	d, f, _ := deps(t)
	os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755)
	f.Rules = []execx.Rule{
		{Prefix: "restic --repo s3:s3.eu-west-1.amazonaws.com/b --password-file  --json --retry-lock 30m cat config", Err: errors.New("restic: exit status 10: Fatal: repository does not exist")},
		{Prefix: "restic --repo s3:s3.eu-west-1.amazonaws.com/b --password-file  --json --retry-lock 30m backup", Output: `{"message_type":"summary","snapshot_id":"aa11","total_bytes_processed":1}`},
		{Prefix: "restic --repo s3:s3.eu-west-1.amazonaws.com/b --password-file  --json --retry-lock 30m forget", Output: `[{"keep":[{"id":"aa11"}],"remove":[{"id":"0011"},{"id":"0022"}]}]`},
	}
	creds := map[string]string{"AWS_ACCESS_KEY_ID": "AKIA1", "AWS_SECRET_ACCESS_KEY": "s3cret"}
	job, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "nightly", Target: "s3:s3.eu-west-1.amazonaws.com/b",
		Credentials: creds, Retention: &agentv1.Retention{Daily: 7, Weekly: 4, Monthly: 6}})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if !f.Called("restic --repo s3:s3.eu-west-1.amazonaws.com/b --password-file  --json --retry-lock 30m init") {
		t.Errorf("no restic init: %v", f.Calls)
	}
	if !f.Called("restic --repo s3:s3.eu-west-1.amazonaws.com/b --password-file  --json --retry-lock 30m forget --tag website=" + websiteID + " --group-by  --keep-daily 7 --keep-weekly 4 --keep-monthly 6") {
		t.Errorf("no restic forget: %v", f.Calls)
	}
	for i, c := range f.Calls {
		if strings.HasPrefix(c, "restic") {
			if strings.Contains(c, "s3cret") {
				t.Errorf("a secret is in the arguments: %s", c)
			}
			if strings.Join(f.Envs[i], " ") != "AWS_ACCESS_KEY_ID=AKIA1 AWS_SECRET_ACCESS_KEY=s3cret" {
				t.Errorf("environment of %s = %v", c, f.Envs[i])
			}
		}
	}
	if got := job.Result()["forgotten"].([]string); len(got) != 2 || got[1] != "0022" {
		t.Errorf("forgotten = %v", got)
	}
	// The panel cannot set other environment variables of restic.
	if _, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Target: "s3:b/c", Credentials: map[string]string{"RESTIC_PASSWORD_COMMAND": "sh"}}); err == nil {
		t.Error("a different variable was accepted")
	}
	if _, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Target: ""}); err == nil {
		t.Error("no target was accepted")
	}
}

func TestPruneRemovesUnusedData(t *testing.T) {
	d, f, _ := deps(t)
	if err := run(t)(d.PruneBackups(&agentv1.PruneBackupsRequest{JobId: jobID, Target: "b2:bucket:cf"})); err != nil {
		t.Fatal(err)
	}
	if !f.Called("restic --repo b2:bucket:cf --password-file  --json --retry-lock 30m prune") {
		t.Errorf("calls = %v", f.Calls)
	}
}

// SFTP: keys in a file of root, the password on stdin, and a lock when SFTP is off.
func TestSetSftp(t *testing.T) {
	d, f, _ := deps(t)
	d.SftpKeysDir = filepath.Join(t.TempDir(), "sftp")
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtQx2bS8ZJ4Wm1bZ7zO0Zr3wq8yXc2v1q9w8mL3h4kP owner@laptop"
	if err := run(t)(d.SetSftp(&agentv1.SetSftpRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Enabled: true, Password: "Abcdefgh12345678", AuthorizedKeys: []string{key}})); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d.SftpKeysDir, "acme_x7k2.keys"))
	if !strings.Contains(string(b), "restrict "+key+"\n") {
		t.Errorf("keys file:\n%s", b)
	}
	if !f.Called("usermod --append --groups cf-sites acme_x7k2") || !f.Called("chpasswd") || strings.Contains(strings.Join(f.Calls, " "), "Abcdefgh12345678") {
		t.Errorf("calls = %v", f.Calls)
	}
	if f.Inputs[len(f.Inputs)-1] != "acme_x7k2:Abcdefgh12345678\n" {
		t.Errorf("stdin = %q", f.Inputs[len(f.Inputs)-1])
	}
	if err := run(t)(d.SetSftp(&agentv1.SetSftpRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", AuthorizedKeys: []string{key}})); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(d.SftpKeysDir, "acme_x7k2.keys"))
	if strings.Contains(string(b), "ssh-ed25519") || !f.Called("usermod --password ! acme_x7k2") {
		t.Errorf("SFTP is still on: %s %v", b, f.Calls)
	}
	for _, bad := range []string{`command="sh" ` + key, key + "\nssh-rsa AAAA", "not a key"} {
		if _, err := d.SetSftp(&agentv1.SetSftpRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Enabled: true, AuthorizedKeys: []string{bad}}); err == nil {
			t.Errorf("accepted the key %q", bad)
		}
	}
}

// FR-205: a staging copy gets its own copies of the databases and its own user.
func TestStagingCopyCopiesTheDatabases(t *testing.T) {
	d, f, _ := deps(t)
	f.Rules = []execx.Rule{{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/1001 podman exec cf-mariadb mariadb-dump", Output: "INSERT INTO t VALUES (1);\n"}}
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.UnixUser = "staging_ab12"
	req := &agentv1.WebsiteRequest{JobId: jobID, Website: w, CopyFrom: "01JBQ3Z5K8N6T4W2X9Y7R5P3M9", CopyFromUnixUser: "acme_x7k2",
		CopyDatabases: []*agentv1.DatabaseCopy{{Engine: "mariadb", From: "acme_x7k2_shop", To: "staging_ab12_shop"}},
		CopyUsers:     []*agentv1.DatabaseUserCopy{{Engine: "mariadb", Name: "staging_ab12_app", Password: "Abcdefgh12345678"}}}
	if err := run(t)(d.CreateWebsite(req)); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.Inputs, "")
	for _, want := range []string{"CREATE DATABASE `staging_ab12_shop`", "INSERT INTO t VALUES (1);", "CREATE USER 'staging_ab12_app'@'127.0.0.1'", "GRANT ALL PRIVILEGES ON `staging\\_ab12\\_shop`.* TO 'staging_ab12_app'@'127.0.0.1'"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in the SQL:\n%s", want, all)
		}
	}
	if !f.Called("runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/1001 podman exec cf-mariadb mariadb-dump --user=root --single-transaction --routines acme_x7k2_shop") {
		t.Errorf("no dump of the source: %v", f.Calls)
	}
	// The copy cannot name a database of a different website.
	req.CopyDatabases[0].From = "other_zz99_shop"
	if _, err := d.CreateWebsite(req); err == nil {
		t.Error("a database of a different website was accepted")
	}
}

// Batch 1, finding 7: a runtime change of a stopped website does not start it.
func TestRuntimeChangeKeepsAStoppedWebsiteStopped(t *testing.T) {
	d, f, edge := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	run(t)(d.SetWebsitePower(&agentv1.SetWebsitePowerRequest{JobId: jobID2, WebsiteId: websiteID, Power: "stop"}))
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"); strings.Contains(u, "WantedBy") {
		t.Errorf("a stopped website starts at boot:\n%s", u)
	}
	port := edge.sites[websiteID].Port
	n := len(f.Calls)
	if err := run(t)(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.5:8.5.0-1")})); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls[n:] {
		if strings.Contains(c, " restart ") || strings.Contains(c, " start ") {
			t.Errorf("the website started: %s", c)
		}
	}
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"); !strings.Contains(u, "frankenphp-php8.5") {
		t.Errorf("the unit has the old image:\n%s", u)
	}
	if site, _ := d.State.Get(websiteID); !site.Stopped || site.Port != port || edge.sites[websiteID].Port != port {
		t.Errorf("site = %+v", site)
	}
}

func TestConfigureHostChangesThePlatformOfAllContainers(t *testing.T) {
	d, f, _ := deps(t)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	saved := ""
	d.SavePlatform = func(p string) error { saved = p; return nil }
	if err := run(t)(d.ConfigureHost(&agentv1.ConfigureHostRequest{JobId: jobID2, GvisorPlatform: "systrap"})); err != nil {
		t.Fatal(err)
	}
	if saved != "systrap" || !f.Called("systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service") {
		t.Errorf("saved = %q, calls = %v", saved, f.Calls)
	}
	if _, err := d.ConfigureHost(&agentv1.ConfigureHostRequest{JobId: jobID2, GvisorPlatform: "ptrace"}); err == nil {
		t.Error("ptrace was accepted")
	}
}

// FR-202: the disk limit is a user quota, and the I/O limit is on the disk of the websites.
func TestLimitsSetTheQuotaAndTheIOLimit(t *testing.T) {
	d, f, _ := deps(t)
	d.QuotaMount, d.BlockDevice = "/srv", "/dev/vda"
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	l := &agentv1.Limits{Vcpus: 2, MemoryBytes: 1 << 30, Processes: 50, DiskBytes: 10 << 30, DiskIoBytesPerSecond: 50 << 20}
	if err := run(t)(d.SetLimits(&agentv1.SetLimitsRequest{JobId: jobID2, WebsiteId: websiteID, Limits: l})); err != nil {
		t.Fatal(err)
	}
	if !f.Called("setquota --user acme_x7k2 9961472 10485760 0 0 /srv") {
		t.Errorf("no quota: %v", f.Calls)
	}
	if !f.Called("runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/1001 podman update --cpus=2 --memory=1073741824 --memory-swap=1073741824 --pids-limit=50 --device-read-bps=/dev/vda:52428800 --device-write-bps=/dev/vda:52428800 cf-acme_x7k2") {
		t.Errorf("no I/O limit: %v", f.Calls)
	}
}

// FR-203 and FR-402: the report has the use of each website and the real certificate state.
func TestTheReportHasWebsiteUseAndCertificates(t *testing.T) {
	d, f, _ := deps(t)
	d.CgroupRoot = t.TempDir()
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	cg := "/user.slice/user-1001.slice/user@1001.service/app.slice/cf-acme_x7k2.service"
	os.MkdirAll(filepath.Join(d.CgroupRoot, cg), 0o755)
	os.WriteFile(filepath.Join(d.CgroupRoot, cg, "memory.current"), []byte("104857600\n"), 0o644)
	os.WriteFile(filepath.Join(d.CgroupRoot, cg, "pids.current"), []byte("12\n"), 0o644)
	os.WriteFile(filepath.Join(d.CgroupRoot, cg, "cpu.stat"), []byte("usage_usec 1000\n"), 0o644)
	os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755)
	os.WriteFile(filepath.Join(d.Host.AppDir("acme_x7k2"), "index.php"), []byte("<?php echo 1;"), 0o644)
	f.Rules = []execx.Rule{{Prefix: "systemctl --user --machine=acme_x7k2@ show --property=ControlGroup", Output: cg + "\n"}}
	expires := time.Now().Add(60 * 24 * time.Hour)
	d.Certificate = func(context.Context, string) (time.Time, error) { return expires, nil }
	d.DNSPointsHere = func(context.Context, string) bool { return false }

	var r panel.Report
	d.FillReport(context.Background(), &r)
	if len(r.Websites) != 1 || r.Websites[0].MemoryBytes != 104857600 || r.Websites[0].Processes != 12 || r.Websites[0].DiskBytes != 13 {
		t.Errorf("websites = %+v", r.Websites)
	}
	// A valid certificate means that the domain points here, also behind NAT.
	if len(r.Domains) != 1 || r.Domains[0].Certificate != "valid" || !r.Domains[0].DNSPointsHere || r.Domains[0].ExpiresAt == "" {
		t.Errorf("domains = %+v", r.Domains)
	}
	if r.Services[len(r.Services)-1] != (panel.Service{Name: "disk-quotas", State: "stopped"}) {
		t.Errorf("services = %+v", r.Services)
	}
}

func TestCustomImagePortAndCommand(t *testing.T) {
	d, _, _ := deps(t)
	w := website("ghcr.io/acme/app:1.0")
	w.Runtime, w.ContainerPort, w.Command = "custom", 3000, []string{"node", "server.js"}
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w}))
	u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container")
	if !strings.Contains(u, ":3000\n") || !strings.Contains(u, `Exec="node" "server.js"`+"\n") {
		t.Errorf("unit:\n%s", u)
	}
	w.Command = []string{"sh\n-c"}
	if err := CheckWebsite(w); err == nil {
		t.Error("a command with a new line was accepted")
	}
	w.Command, w.DocumentRoot = nil, "../etc"
	if err := CheckWebsite(w); err == nil {
		t.Error("a document root outside /app was accepted")
	}
}
