package actions

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// The tests of the WordPress sites (FR-206). WP-CLI runs in the container of the website as
// the website user, so each command goes through the gate as "runuser -u acme_x7k2".

const (
	// wpStart is the start of each WP-CLI command of an install or an update.
	wpStart = asSite + "env XDG_RUNTIME_DIR=/run/user/20001 timeout -k 10 930 podman exec "
	wpTail  = "--workdir /app/public --env HOME=/tmp cf-acme_x7k2 timeout -k 10 900 wp --path=/app/public "
	// wpCmd is the start of a WP-CLI command with no stdin, wpInput of one with stdin.
	wpCmd   = wpStart + wpTail
	wpInput = wpStart + "-i " + wpTail
	// wpFlags are the options of the commands of an update.
	wpFlags = " --skip-plugins --skip-themes"
	adminPW = "Adminpassword1234567"
	dbPW    = "Databasepassword1234"
)

// wpRules are the answers of WP-CLI for a site with WordPress 6.8.1, one plugin and one theme.
// A PHP notice comes before the list of plugins.
func wpRules() []execx.Rule {
	return []execx.Rule{
		{Prefix: wpCmd + "core version", Output: "6.8.1\n"},
		{Prefix: wpCmd + "core check-update", Output: `[{"version":"6.8.3","update_type":"minor","package_url":"https://downloads.wordpress.org/release/wordpress-6.8.3.zip"},{"version":"6.9","update_type":"major","package_url":"x"}]`},
		{Prefix: wpCmd + "plugin list", Output: "PHP Notice: something\n" + `[{"name":"akismet","title":"Akismet Anti-spam","status":"active","version":"5.3","update_version":"5.4"}]`},
		{Prefix: wpCmd + "theme list", Output: `[{"name":"twentytwentyfive","title":"Twenty Twenty-Five","status":"active","version":"1.2","update_version":""}]`},
		{Prefix: wpCmd + "option get siteurl", Output: "https://acme.example\n"},
		{Prefix: wpCmd + "config get DB_NAME", Output: "acme_x7k2_wp\n"},
		{Prefix: wpCmd + "eval", Output: "cf-wordpress-ok"},
	}
}

func installRequest() *agentv1.InstallWordPressRequest {
	return &agentv1.InstallWordPressRequest{JobId: jobID2, WebsiteId: websiteID, DatabaseServer: databaseServer("mariadb"),
		DatabaseName: "acme_x7k2_wp", DatabaseUser: "acme_x7k2_wp", DatabasePassword: dbPW,
		Title: "Acme shop", AdminUser: "owner", AdminEmail: "owner@acme.example", AdminPassword: adminPW, Locale: "en_GB", Url: "https://acme.example"}
}

// withWordPress gives a website with the answers of WP-CLI.
func withWordPress(t *testing.T) (*Deps, *execx.Fake) {
	t.Helper()
	d, f, _ := withSite(t)
	customerServers(t, d)
	f.Rules = append(f.Rules, wpRules()...)
	return d, f
}

func TestInstallWordPressSendsThePasswordsOnStdin(t *testing.T) {
	d, f := withWordPress(t)
	job, err := d.InstallWordPress(installRequest())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range job.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, "|"); got != "Check the website|Create database|Download WordPress|Install WordPress|Check WordPress" {
		t.Errorf("steps = %s", got)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, adminPW) || strings.Contains(c, dbPW) {
			t.Fatalf("a password is in the arguments: %s", c)
		}
	}
	for prefix, input := range map[string]string{
		wpInput + "config create --dbname=acme_x7k2_wp --dbuser=acme_x7k2_wp --dbhost=mariadb.internal --dbcharset=utf8mb4 --locale=en_GB --force --prompt=dbpass":     dbPW + "\n",
		wpInput + "core install --url=https://acme.example --title=Acme shop --admin_user=owner --admin_email=owner@acme.example --skip-email --prompt=admin_password": adminPW + "\n",
	} {
		call, in := called(f, prefix)
		if call == "" || in != input {
			t.Errorf("no call %q with its password on stdin (call %q, stdin %q)", prefix, call, in)
		}
	}
	for _, want := range []string{
		wpCmd + "cli version",
		wpCmd + "core download --locale=en_GB --force",
		wpCmd + "config set AUTOMATIC_UPDATER_DISABLED true --raw",
		wpCmd + "rewrite structure /%postname%/",
	} {
		if !f.Called(want) {
			t.Errorf("no call %q", want)
		}
	}
	in := inputsOf(f, execOf("mariadb", mariaServerKey))
	for _, want := range []string{
		"CREATE DATABASE `acme_x7k2_wp` CHARACTER SET utf8mb4",
		"CREATE USER 'acme_x7k2_wp'@'169.254.80.1' IDENTIFIED BY '" + dbPW + "'",
		"GRANT ALL PRIVILEGES ON `acme\\_x7k2\\_wp`.* TO 'acme_x7k2_wp'@'169.254.80.1';",
	} {
		if !strings.Contains(in, want) {
			t.Errorf("SQL does not contain %s:\n%s", want, in)
		}
	}
	// FrankenPHP needs no .htaccess file.
	if f.Called(asSite + "sh -c [ -e \"$1/.htaccess\" ]") {
		t.Error("the agent wrote .htaccess for FrankenPHP")
	}
	wp := job.Result()["wordpress"].(map[string]any)
	if wp["version"] != "6.8.1" || wp["coreMinor"] != "6.8.3" || wp["coreLatest"] != "6.9" || wp["siteUrl"] != "https://acme.example" || wp["databaseName"] != "acme_x7k2_wp" {
		t.Errorf("result = %v", wp)
	}
	plugins := wp["plugins"].([]any)
	if len(plugins) != 1 || plugins[0].(map[string]any)["update"] != "5.4" {
		t.Errorf("plugins = %v", plugins)
	}
}

func TestInstallWordPressNeedsAnEmptyDocumentRoot(t *testing.T) {
	d, f := withWordPress(t)
	f.Rules = append([]execx.Rule{{Prefix: asSite + "sh -c mkdir -p", Output: "used\n"}}, f.Rules...)
	err := run(t)(d.InstallWordPress(installRequest()))
	if err == nil || !strings.Contains(err.Error(), "has files") {
		t.Fatalf("err = %v", err)
	}
	if f.Called(wpCmd+"core download") || strings.Contains(inputsOf(f, execOf("mariadb", mariaServerKey)), "CREATE") {
		t.Errorf("the agent continued: %v", f.Calls)
	}
}

func TestInstallWordPressNeedsWPCLIInTheImage(t *testing.T) {
	d, f := withWordPress(t)
	f.Rules = append([]execx.Rule{{Prefix: wpCmd + "cli version", Err: errors.New("runuser: exit status 127: wp: not found")}}, f.Rules...)
	err := run(t)(d.InstallWordPress(installRequest()))
	if err == nil || !strings.Contains(err.Error(), "has no WP-CLI") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallWordPressRefusesBadInput(t *testing.T) {
	d, _ := withWordPress(t)
	for name, change := range map[string]func(r *agentv1.InstallWordPressRequest){
		"user admin":          func(r *agentv1.InstallWordPressRequest) { r.AdminUser = "admin" },
		"locale":              func(r *agentv1.InstallWordPressRequest) { r.Locale = "en-GB" },
		"title with new line": func(r *agentv1.InstallWordPressRequest) { r.Title = "a\nb" },
		"mail address":        func(r *agentv1.InstallWordPressRequest) { r.AdminEmail = "owner" },
		"short password":      func(r *agentv1.InstallWordPressRequest) { r.AdminPassword = "short" },
		"database password":   func(r *agentv1.InstallWordPressRequest) { r.DatabasePassword = "a'b" },
		"foreign database":    func(r *agentv1.InstallWordPressRequest) { r.DatabaseName = "other_x7k2_wp" },
		"foreign user":        func(r *agentv1.InstallWordPressRequest) { r.DatabaseUser = "root" },
		"PostgreSQL":          func(r *agentv1.InstallWordPressRequest) { r.DatabaseServer = databaseServer("postgresql") },
		"plain HTTP":          func(r *agentv1.InstallWordPressRequest) { r.Url = "http://acme.example" },
		"address with a path": func(r *agentv1.InstallWordPressRequest) { r.Url = "https://acme.example/blog --skip-email" },
	} {
		req := installRequest()
		change(req)
		if _, err := d.InstallWordPress(req); err == nil {
			t.Errorf("%s: the request was accepted", name)
		}
	}
}

func TestInstallWordPressWritesHtaccessForApache(t *testing.T) {
	d, f := withWordPress(t)
	site, _ := d.State.Get(websiteID)
	site.Website.Runtime = "apache-php-fpm"
	if err := d.State.Put(site); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.InstallWordPress(installRequest())); err != nil {
		t.Fatal(err)
	}
	if _, in := called(f, asSite+"sh -c [ -e \"$1/.htaccess\" ]"); !strings.Contains(in, "RewriteRule . /index.php [L]") {
		t.Errorf("no .htaccess file: %v", f.Calls)
	}
}

func updateRequest() *agentv1.UpdateWordPressRequest {
	return &agentv1.UpdateWordPressRequest{JobId: jobID2, WebsiteId: websiteID, Core: "minor", Plugins: []string{"akismet"},
		Database:    &agentv1.DatabaseRef{Engine: "mariadb", Name: "acme_x7k2_wp", DatabaseServer: mariaServerID},
		HealthCheck: &agentv1.HealthCheck{}}
}

func TestUpdateWordPressSavesARestorePointFirst(t *testing.T) {
	d, f := withWordPress(t)
	customerServers(t, d, "mariadb")
	job, err := d.UpdateWordPress(updateRequest())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range job.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, "|"); got != "Check WordPress|Save a restore point|Update WordPress|Update plugins|Update themes|Health check" {
		t.Errorf("steps = %s", got)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	home := d.Host.Home("acme_x7k2")
	files := callIndex(f.Calls, asSite+"sh -c set -e\numask 077")
	dump := callIndex(f.Calls, podmanOf(mariaServerKey)+"exec cf-mariadb-"+mariaServerKey+" mariadb-dump --user=root --single-transaction --routines --events acme_x7k2_wp | "+asSite+`sh -c umask 077 && cat > "$1" sh `+home+"/.cf-wordpress.new/database.sql")
	keep := callIndex(f.Calls, asSite+`sh -c rm -rf "$1" && mv "$1.new" "$1" sh `+home+"/.cf-wordpress")
	core := callIndex(f.Calls, wpCmd+"core update"+wpFlags+" --minor")
	plugins := callIndex(f.Calls, wpCmd+"plugin update"+wpFlags+" akismet")
	health := callIndex(f.Calls, wpCmd+"eval")
	if files < 0 || dump < files || keep < dump || core < keep || !f.Called(wpCmd+"core update-db"+wpFlags) || plugins < core || health < plugins {
		t.Errorf("order: files %d, dump %d, keep %d, core %d, plugins %d, health %d\n%s", files, dump, keep, core, plugins, health, strings.Join(f.Calls, "\n"))
	}
	if call, _ := called(f, asSite+"sh -c set -e\numask 077"); !strings.Contains(call, " . sh "+home+"/.cf-wordpress "+d.Host.AppDir("acme_x7k2")+"/public --exclude=./wp-content/uploads ") {
		t.Errorf("tar call = %q", call)
	}
	if f.Called(wpCmd + "theme update") {
		t.Error("the agent updated themes that the request did not have")
	}
	r := job.Result()
	if r["before"].(map[string]any)["version"] != "6.8.1" || r["wordpress"].(map[string]any)["version"] != "6.8.1" {
		t.Errorf("result = %v", r)
	}
}

func TestUpdateWordPressPutsBackTheRestorePointWhenAnUpdateFails(t *testing.T) {
	d, f := withWordPress(t)
	customerServers(t, d, "mariadb")
	f.Rules = append([]execx.Rule{
		{Prefix: wpCmd + "plugin update", Err: errors.New("runuser: exit status 1: Error: Plugin update failed.")},
		{Prefix: asSite + "sh -c [ -f \"$1/files.tar.gz\" ]", Output: "files\ndatabase\n"},
	}, f.Rules...)
	err := run(t)(d.UpdateWordPress(updateRequest()))
	if err == nil || !strings.Contains(err.Error(), "Plugin update failed") || !strings.Contains(err.Error(), "the agent put back the restore point") {
		t.Fatalf("err = %v", err)
	}
	home := d.Host.Home("acme_x7k2")
	restore := callIndex(f.Calls, asSite+"sh -c set -e\ndir=$1 root=$2")
	load := callIndex(f.Calls, asSite+"cat -- "+home+"/.cf-wordpress/database.sql")
	if restore < 0 || load < restore {
		t.Errorf("restore %d, load %d\n%s", restore, load, strings.Join(f.Calls, "\n"))
	}
	if call, _ := called(f, asSite+"sh -c set -e\ndir=$1 root=$2"); !strings.HasSuffix(call, " uploads cache upgrade upgrade-temp-backup wflogs updraft ai1wm-backups backups backup-db") {
		t.Errorf("restore call = %q", call)
	}
	if f.Called(wpCmd + "eval") {
		t.Error("the health check ran after a failed update")
	}
}

func TestUpdateWordPressPutsBackTheRestorePointWhenTheSiteFails(t *testing.T) {
	d, f := withWordPress(t)
	customerServers(t, d, "mariadb")
	f.Rules = append([]execx.Rule{{Prefix: asSite + "sh -c [ -f \"$1/files.tar.gz\" ]", Output: "files\ndatabase\n"}}, f.Rules...)
	d.Health = func(context.Context, int, string) (int, error) { return 500, nil }
	req := updateRequest()
	req.HealthCheck = &agentv1.HealthCheck{Seconds: 1}
	err := run(t)(d.UpdateWordPress(req))
	if err == nil || !strings.Contains(err.Error(), "health checks failed") || !strings.Contains(err.Error(), "put back the restore point") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateWordPressWithNoDatabaseSavesOnlyTheFiles(t *testing.T) {
	d, f := withWordPress(t)
	req := updateRequest()
	req.Database = nil
	if err := run(t)(d.UpdateWordPress(req)); err != nil {
		t.Fatal(err)
	}
	if f.Called(podmanOf(mariaServerKey)) {
		t.Errorf("the agent made a dump: %v", f.Calls)
	}
}

func TestUpdateWordPressRefusesBadInput(t *testing.T) {
	d, _ := withWordPress(t)
	for name, change := range map[string]func(r *agentv1.UpdateWordPressRequest){
		"option as a plugin": func(r *agentv1.UpdateWordPressRequest) { r.Plugins = []string{"--all"} },
		"path as a theme":    func(r *agentv1.UpdateWordPressRequest) { r.Themes = []string{"../x"} },
		"core":               func(r *agentv1.UpdateWordPressRequest) { r.Core = "nightly" },
		"nothing":            func(r *agentv1.UpdateWordPressRequest) { r.Core, r.Plugins = "", nil },
		"many plugins":       func(r *agentv1.UpdateWordPressRequest) { r.Plugins = make([]string, 201) },
	} {
		req := updateRequest()
		change(req)
		if _, err := d.UpdateWordPress(req); err == nil {
			t.Errorf("%s: the request was accepted", name)
		}
	}
	// A database of a different website.
	req := updateRequest()
	req.Database.Name = "other_x7k2_wp"
	if err := run(t)(d.UpdateWordPress(req)); err == nil {
		t.Error("a foreign database was accepted")
	}
}

func TestRestoreWordPressNeedsARestorePoint(t *testing.T) {
	d, f := withWordPress(t)
	err := run(t)(d.RestoreWordPress(&agentv1.RestoreWordPressRequest{JobId: jobID2, WebsiteId: websiteID}))
	if err == nil || !strings.Contains(err.Error(), "no restore point") {
		t.Fatalf("err = %v", err)
	}
	f.Rules = append([]execx.Rule{{Prefix: asSite + "sh -c [ -f \"$1/files.tar.gz\" ]", Output: "files\n"}}, f.Rules...)
	if err := run(t)(d.RestoreWordPress(&agentv1.RestoreWordPressRequest{JobId: jobID2, WebsiteId: websiteID})); err != nil {
		t.Fatal(err)
	}
	if !f.Called(asSite + "sh -c set -e\ndir=$1 root=$2") {
		t.Errorf("no restore: %v", f.Calls)
	}
}

func TestRunWpCliGivesTheExitCodeAndTheOutput(t *testing.T) {
	d, f := withWordPress(t)
	f.Rules = append([]execx.Rule{{Prefix: asSite + "env XDG_RUNTIME_DIR=/run/user/20001 sh -c limit=$1", Output: "Error: no such plugin\ncf-exit:1\n"}}, f.Rules...)
	job, err := d.RunWpCli(&agentv1.RunWpCliRequest{JobId: jobID2, WebsiteId: websiteID, Args: []string{"plugin", "activate", "it's mine; rm -rf /"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	r := job.Result()
	if r["exitCode"] != 1 || r["output"] != "Error: no such plugin" || r["wordpress"] == nil {
		t.Errorf("result = %v", r)
	}
	// The arguments go to WP-CLI as they are: the shell gives them as "$@".
	call, _ := called(f, asSite+"env XDG_RUNTIME_DIR=/run/user/20001 sh -c limit=$1")
	if !strings.Contains(call, `wp "--path=$root" "$@" 2>&1`) || !strings.HasSuffix(call, " sh 1200 cf-acme_x7k2 /app/public plugin activate it's mine; rm -rf /") {
		t.Errorf("call = %q", call)
	}
}

func TestRunWpCliRefusesBadInput(t *testing.T) {
	d, _ := withWordPress(t)
	for name, args := range map[string][]string{
		"no command":      nil,
		"control":         {"eval", "echo 1;\necho 2;"},
		"many arguments":  make([]string, 41),
		"long argument":   {strings.Repeat("a", 1001)},
		"escape sequence": {"\x1b[31m"},
	} {
		if _, err := d.RunWpCli(&agentv1.RunWpCliRequest{JobId: jobID2, WebsiteId: websiteID, Args: args}); err == nil {
			t.Errorf("%s: the request was accepted", name)
		}
	}
}

func TestWordPressJobsNeedARunningPHPWebsite(t *testing.T) {
	d, _ := withWordPress(t)
	site, _ := d.State.Get(websiteID)
	site.Stopped = true
	if err := d.State.Put(site); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CheckWordPress(&agentv1.CheckWordPressRequest{JobId: jobID2, WebsiteId: websiteID})); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("stopped website: %v", err)
	}
	site.Stopped = false
	site.Website.Runtime = "node"
	if err := d.State.Put(site); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CheckWordPress(&agentv1.CheckWordPressRequest{JobId: jobID2, WebsiteId: websiteID})); err == nil || !strings.Contains(err.Error(), "PHP") {
		t.Errorf("Node.js website: %v", err)
	}
}

func TestCheckWordPressReadsAMessageAsNoUpdate(t *testing.T) {
	d, f := withWordPress(t)
	f.Rules = append([]execx.Rule{{Prefix: wpCmd + "core check-update", Output: "Success: WordPress is at the latest version.\n"}}, f.Rules...)
	job, err := d.CheckWordPress(&agentv1.CheckWordPressRequest{JobId: jobID2, WebsiteId: websiteID})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	wp := job.Result()["wordpress"].(map[string]any)
	if wp["coreMinor"] != nil || wp["coreLatest"] != nil || wp["version"] != "6.8.1" {
		t.Errorf("result = %v", wp)
	}
	if !f.Called(wpCmd + "core version" + wpFlags) {
		t.Errorf("calls = %v", f.Calls)
	}
}

// The owner of the website reads the message of WP-CLI, with no command line of the host.
func TestWordPressErrorsHaveTheMessageOfWPCLI(t *testing.T) {
	d, f := withWordPress(t)
	f.Rules = append([]execx.Rule{{Prefix: wpCmd + "core version", Err: errors.New("runuser -u acme_x7k2 -- env podman exec: exit status 1: Error: This does not seem to be a WordPress installation.")}}, f.Rules...)
	err := run(t)(d.CheckWordPress(&agentv1.CheckWordPressRequest{JobId: jobID2, WebsiteId: websiteID}))
	if err == nil || !strings.HasSuffix(err.Error(), "wp core version: Error: This does not seem to be a WordPress installation.") || strings.Contains(err.Error(), "runuser") || strings.Contains(err.Error(), "exit status") {
		t.Errorf("err = %v", err)
	}
}
