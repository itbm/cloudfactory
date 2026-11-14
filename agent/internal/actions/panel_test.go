package actions

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

func testPanel(dir string) quadlet.Panel {
	return quadlet.Panel{Registry: "ghcr.io/cloudfactory", Version: "0.2.0", Domain: "panel.example.com", DataDir: filepath.Join(dir, "panel"),
		Gvisor:  quadlet.Runtime{RunscPath: "/usr/local/bin/runsc", Platform: "systrap"},
		APIPort: quadlet.PanelAPIPort, WebPort: quadlet.PanelWebPort, AgentPort: quadlet.PanelAgentPort, DatabasePort: quadlet.PanelDatabasePort}
}

func index(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// Audit findings B4 and R4: the panel keeps its data in PostgreSQL. The database starts
// before the API, and the API gets the password of the database.
func TestInstallPanelStartsTheDatabaseFirst(t *testing.T) {
	d, f, _ := direct(t)
	p := testPanel(t.TempDir())
	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	asPanel := "runuser -u cf-panel -- env XDG_RUNTIME_DIR=/run/user/20001 podman "
	order := []string{
		asPanel + "pull --quiet ghcr.io/cloudfactory/postgresql:0.2.0",
		asPanel + "pull --quiet ghcr.io/cloudfactory/panel-api:0.2.0",
		asPanel + "pull --quiet ghcr.io/cloudfactory/panel-web:0.2.0",
		"install -d -o cf-panel -g cf-panel -m 0700 " + p.PanelDatabaseDir(),
		"chown cf-panel:cf-panel " + p.PanelDatabaseEnv(),
		"systemctl --user --machine=cf-panel@ daemon-reload",
		"systemctl --user --machine=cf-panel@ restart cf-panel-db.service",
		asPanel + "exec -i cf-panel-db pg_isready --quiet --host=127.0.0.1 --username=cloudfactory --dbname=cloudfactory",
		asPanel + "exec -i cf-panel-db psql --username=cloudfactory --dbname=cloudfactory --set=ON_ERROR_STOP=1 --quiet",
		"systemctl --user --machine=cf-panel@ restart cf-panel-api.service",
		"systemctl --user --machine=cf-panel@ restart cf-panel-queue.service",
		"systemctl --user --machine=cf-panel@ restart cf-panel-scheduler.service",
		"systemctl --user --machine=cf-panel@ restart cf-panel-web.service",
	}
	last := -1
	for _, want := range order {
		i := index(f.Calls, want)
		if i < 0 {
			t.Fatalf("not called: %s\ncalls: %v", want, f.Calls)
		}
		if i < last {
			t.Errorf("%s was called too early\ncalls: %v", want, f.Calls)
		}
		last = i
	}

	// The file has the same password for the database server and for the API. Only its owner
	// can read it.
	info, err := os.Stat(p.PanelDatabaseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode of the password file = %v, want 0600", info.Mode().Perm())
	}
	b, _ := os.ReadFile(p.PanelDatabaseEnv())
	m := panelPasswordRE.FindStringSubmatch(string(b))
	if m == nil || len(m[1]) != 48 {
		t.Fatalf("the password file has no password:\n%s", b)
	}
	for _, want := range []string{"POSTGRES_USER=cloudfactory\n", "POSTGRES_DB=cloudfactory\n", "POSTGRES_PASSWORD=" + m[1] + "\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the password file does not contain %q", want)
		}
	}
	// The password goes to psql through stdin. It is not in a command line.
	psql := index(f.Calls, asPanel+"exec -i cf-panel-db psql")
	if want := "ALTER ROLE cloudfactory PASSWORD '" + m[1] + "';\n"; f.Inputs[psql] != want {
		t.Errorf("psql input = %q, want %q", f.Inputs[psql], want)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, m[1]) {
			t.Errorf("the password is in a command line: %s", c)
		}
	}
	for _, name := range []string{"cf-panel-db.container", "cf-panel-api.container", "cf-panel-queue.container", "cf-panel-scheduler.container", "cf-panel-web.container"} {
		unitFile(t, d, PanelUser, name)
	}
}

// A second run keeps the password and does not stop a service that operates.
func TestInstallPanelAgainKeepsThePasswordAndTheServices(t *testing.T) {
	d, f, _ := direct(t)
	p := testPanel(t.TempDir())
	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p.PanelDatabaseEnv())
	f.Calls, f.Inputs = nil, nil

	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p.PanelDatabaseEnv())
	if string(before) != string(after) {
		t.Error("the second run changed the password file")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, " restart ") {
			t.Errorf("the second run started a service again: %s", c)
		}
	}
	if !f.Called("systemctl --user --machine=cf-panel@ start cf-panel-api.service") {
		t.Errorf("calls: %v", f.Calls)
	}

	// A new version changes the units. Each service then starts again with its new unit.
	f.Calls, f.Inputs = nil, nil
	p.Version = "0.2.1"
	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, service := range append([]string{"cf-panel-db.service"}, panelServices...) {
		if !f.Called("systemctl --user --machine=cf-panel@ restart " + service) {
			t.Errorf("%s did not start again\ncalls: %v", service, f.Calls)
		}
	}
}

// The entry point of the database image gives the data directory to the user postgres of the
// container. When the install script ran again while the database operated, "install -d" gave
// the directory back to cf-panel. The database then could not open its files ("could not open
// file global/pg_filenode.map: Permission denied"), and step 14 of the script stopped.
func TestInstallPanelAgainKeepsTheOwnerOfTheDatabaseDirectory(t *testing.T) {
	d, f, _ := direct(t)
	p := testPanel(t.TempDir())
	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !f.Called("install -d -o cf-panel -g cf-panel -m 0700 " + p.PanelDatabaseDir()) {
		t.Fatalf("the first run did not make the database directory\ncalls: %v", f.Calls)
	}
	// The fake runner does not run "install". The entry point of the image made the directory.
	if err := os.MkdirAll(p.PanelDatabaseDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f.Calls, f.Inputs = nil, nil

	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasSuffix(c, " "+p.PanelDatabaseDir()) && (strings.HasPrefix(c, "install ") || strings.HasPrefix(c, "chown ") || strings.HasPrefix(c, "chmod ")) {
			t.Errorf("the second run changed the database directory: %s", c)
		}
	}
}

// A password file with no password is a fault that the administrator must see.
func TestInstallPanelStopsOnABadPasswordFile(t *testing.T) {
	d, _, _ := direct(t)
	p := testPanel(t.TempDir())
	os.MkdirAll(p.DataDir, 0o700)
	os.WriteFile(p.PanelDatabaseEnv(), []byte("POSTGRES_PASSWORD=x\n"), 0o600)
	if err := d.InstallPanel(context.Background(), p); err == nil || !strings.Contains(err.Error(), "has no DB_PASSWORD") {
		t.Errorf("err = %v", err)
	}
}

// Audit finding S7: an update of the panel sets the version that the panel offers to the
// other servers, and keeps the other settings.
func TestInstallPanelSetsTheVersionInTheSettings(t *testing.T) {
	d, f, _ := direct(t)
	p := testPanel(t.TempDir())
	env := p.DataDir + "/panel.env"
	os.MkdirAll(p.DataDir, 0o755)
	os.WriteFile(env, []byte("APP_KEY=base64:abc\nAPP_URL=https://panel.example.com\nCF_AGENT_LATEST=0.1.0\n"), 0o600)
	if err := d.InstallPanel(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(env)
	if string(b) != "APP_KEY=base64:abc\nAPP_URL=https://panel.example.com\nCF_AGENT_LATEST="+p.Version+"\n" {
		t.Errorf("settings = %q", b)
	}
	if info, _ := os.Stat(env); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	if !f.Called("chown cf-panel:cf-panel " + env) {
		t.Errorf("the user of the panel does not own the settings: %v", f.Calls)
	}
	if domain, err := PanelDomain(env); err != nil || domain != "panel.example.com" {
		t.Errorf("domain = %q, %v", domain, err)
	}
}

// Audit findings B5 and S3: root on the panel server can reset a password and remove a lock.
func TestThePanelAccountCommandsRunInTheAPIContainer(t *testing.T) {
	d, f, _ := direct(t)
	f.Rules = []execx.Rule{{Prefix: "runuser -u cf-panel", Output: "Temporary password for admin@example.com: abc\n"}}
	out, err := d.ResetPanelPassword(context.Background(), "admin@example.com", true)
	if err != nil || !strings.Contains(out, "Temporary password") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	asPanel := "runuser -u cf-panel -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec cf-panel-api php artisan "
	if f.Calls[len(f.Calls)-1] != asPanel+"cloudfactory:reset-password --remove-totp -- admin@example.com" {
		t.Errorf("calls = %v", f.Calls)
	}
	if _, err := d.UnlockPanelSignIn(context.Background(), "admin@example.com", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if f.Calls[len(f.Calls)-1] != asPanel+"cloudfactory:unlock --address=203.0.113.9 -- admin@example.com" {
		t.Errorf("calls = %v", f.Calls)
	}
	// A value that is not an address or an email does not go to the command.
	n := len(f.Calls)
	if _, err := d.UnlockPanelSignIn(context.Background(), "admin@example.com", "203.0.113.9 --help"); err == nil {
		t.Error("a bad address was accepted")
	}
	for _, email := range []string{"", "admin", "a b@example.com", "admin@example.com\n--remove-totp"} {
		if _, err := d.ResetPanelPassword(context.Background(), email, false); err == nil {
			t.Errorf("the email %q was accepted", email)
		}
	}
	if len(f.Calls) != n {
		t.Errorf("a command ran with a bad value: %v", f.Calls[n:])
	}
}
