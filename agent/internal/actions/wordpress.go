package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// WordPress sites (FR-206). The agent runs WP-CLI in the container of the website, as the
// website user, with no shell between the panel and WP-CLI. The PHP images have WP-CLI at
// /usr/local/bin/wp (images/frankenphp, images/apache-php-fpm). WP-CLI thus has only the
// rights of the website: the same rights as the code of the website.
//
// Before an update, the agent saves a restore point in the home directory of the website
// user: the files of the site with no uploads, and a dump of its database. When an update or
// the health check after it fails, the agent puts back the restore point. The restore point
// stays until the next update, so that the owner can roll back the last update.

const (
	// wpLimit is the longest time of one WP-CLI command of an install or an update.
	wpLimit = 15 * time.Minute
	// wpCliLimit is the longest time of one WP-CLI command from the panel.
	wpCliLimit = 20 * time.Minute
	// wpRestoreDir is the directory of the restore point in the home directory of the
	// website user. The website and SFTP cannot change it: they have only the directory of
	// the website.
	wpRestoreDir = ".cf-wordpress"
	// wpItemsMax is the largest number of plugins or of themes in the result of a check.
	wpItemsMax = 500
	// wpCliOutputMax is the size of the end of the output of a WP-CLI command from the panel
	// that the agent keeps.
	wpCliOutputMax = 16000
	// wpDefaultPage is the text of the default page of a new website (defaultPage).
	wpDefaultPage = "Cloudfactory made this page"
)

// wpKeep are the directories in wp-content that a restore point does not have, and that a
// restore does not change: the files that visitors and editors add, and the caches. They
// are often large, and an update does not change them.
var wpKeep = []string{"uploads", "cache", "upgrade", "upgrade-temp-backup", "wflogs", "updraft", "ai1wm-backups", "backups", "backup-db"}

// wpMaintenance are the global options of WP-CLI for the commands of an update: WP-CLI
// does not load the plugins and the theme, so a plugin with an error does not stop the update
// that repairs it.
var wpMaintenance = []string{"--skip-plugins", "--skip-themes"}

// wpItem is one plugin or theme in the result of a check.
type wpItem struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Version string `json:"version"`
	// Update is the version of an available update, or "".
	Update string `json:"update,omitempty"`
}

// wpInventory is the result of a check of a WordPress site.
type wpInventory struct {
	Version string `json:"version"`
	// CoreMinor is the latest version with the same major version, when it is newer.
	CoreMinor string `json:"coreMinor,omitempty"`
	// CoreLatest is the latest version, when it is a newer major version.
	CoreLatest string `json:"coreLatest,omitempty"`
	SiteURL    string `json:"siteUrl"`
	// DatabaseName is the database in wp-config.php, or "".
	DatabaseName string   `json:"databaseName,omitempty"`
	Plugins      []wpItem `json:"plugins"`
	Themes       []wpItem `json:"themes"`
	CheckedAt    string   `json:"checkedAt"`
}

func (inv wpInventory) result() map[string]any {
	b, _ := json.Marshal(inv)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// wordpressSite gives the record of a running PHP website.
func (d *Deps) wordpressSite(websiteID string) (*Site, error) {
	site, err := d.State.Get(websiteID)
	if err != nil {
		return nil, err
	}
	if site == nil || site.Website == nil {
		return nil, ErrUnknownWebsite
	}
	if !isPHP(site.Website) {
		return nil, errors.New("WordPress needs a PHP runtime; change the runtime of the website first")
	}
	if site.Stopped {
		return nil, errors.New("the website is stopped; start it first")
	}
	return site, nil
}

// siteRoot gives the document root of a website on the host.
func (d *Deps) siteRoot(site *Site) string {
	return filepath.Join(d.Host.AppDir(site.UnixUser), strings.TrimPrefix(documentRoot(site.Website), "/app"))
}

// restoreDir gives the directory of the restore point of a website on the host.
func (d *Deps) restoreDir(site *Site) string {
	return filepath.Join(d.Host.Home(site.UnixUser), wpRestoreDir)
}

// wpArgv gives the command that runs WP-CLI in the container of a website, as the website
// user. The first "timeout" is on the host and stops "podman exec". The second one is in the
// container and stops WP-CLI: the command of an exec session continues in the container when
// its client stops (finding G5-13).
func (d *Deps) wpArgv(site *Site, limit time.Duration, stdin bool) ([]string, error) {
	uid, err := d.Host.LookupUID(site.UnixUser)
	if err != nil {
		return nil, err
	}
	secs := int(limit.Seconds())
	root := documentRoot(site.Website)
	argv := []string{"runuser", "-u", site.UnixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid),
		"timeout", "-k", "10", strconv.Itoa(secs + 30), "podman", "exec"}
	if stdin {
		argv = append(argv, "-i")
	}
	container := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}.ContainerName()
	return append(argv, "--workdir", root, "--env", "HOME=/tmp", container,
		"timeout", "-k", "10", strconv.Itoa(secs), "wp", "--path="+root), nil
}

// wp runs one WP-CLI command for an install or an update. stdin goes to WP-CLI, for the
// --prompt option: a password is never in the arguments of a command.
func (d *Deps) wp(ctx context.Context, site *Site, stdin string, args ...string) (string, error) {
	argv, err := d.wpArgv(site, wpLimit, stdin != "")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, wpLimit+time.Minute)
	defer cancel()
	out, err := d.Run.RunInput(ctx, stdin, argv[0], append(argv[1:], args...)...)
	if err != nil {
		return out, wpError(args, err)
	}
	return out, nil
}

// errNoWPCLI is the error of a WP-CLI command in an image with no WP-CLI: an image from
// before FR-206.
var errNoWPCLI = errors.New("the image of the website has no WP-CLI; update the runtime of the website to get a new image, then try again")

// wpError gives the error of a WP-CLI command: the command and the message of WP-CLI, with
// no command line of the host. The owner of the website reads it.
func wpError(args []string, err error) error {
	text := err.Error()
	i := strings.LastIndex(text, "exit status ")
	if i < 0 {
		return commandError("wp "+wpCommandName(args), err)
	}
	status, message, _ := strings.Cut(text[i+len("exit status "):], ": ")
	if status == "126" || status == "127" {
		return errNoWPCLI
	}
	if message = strings.TrimSpace(message); message == "" {
		return fmt.Errorf("wp %s: exit code %s", wpCommandName(args), status)
	}
	return fmt.Errorf("wp %s: %s", wpCommandName(args), lastLines(cleanText(message), 600))
}

// wpCommandName gives the WP-CLI command of a list of arguments, with no options and no
// values, for an error message.
func wpCommandName(args []string) string {
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(words) == 2 {
			break
		}
		words = append(words, a)
	}
	return strings.Join(words, " ")
}

// checkWPCLI makes sure that the image of the website has WP-CLI.
func (d *Deps) checkWPCLI(ctx context.Context, site *Site) error {
	_, err := d.wp(ctx, site, "", "cli", "version")
	return err
}

// wpListed is one plugin or theme as WP-CLI writes it with --format=json.
type wpListed struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Version       string `json:"version"`
	UpdateVersion string `json:"update_version"`
}

// jsonPart gives the JSON array or object in the output of WP-CLI. A plugin or PHP can
// write a notice before it.
func jsonPart(out string) string {
	i := strings.IndexAny(out, "[{")
	if i < 0 {
		return ""
	}
	return out[i:]
}

// wpText cleans a short text from WordPress for the panel.
func wpText(s string, max int) string {
	s = strings.TrimSpace(cleanText(s))
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}
	return s
}

func wpItems(out string) ([]wpItem, error) {
	var listed []wpListed
	if err := json.Unmarshal([]byte(jsonPart(out)), &listed); err != nil {
		return nil, fmt.Errorf("WP-CLI wrote a list that the agent cannot read: %w", err)
	}
	if len(listed) > wpItemsMax {
		listed = listed[:wpItemsMax]
	}
	items := make([]wpItem, 0, len(listed))
	for _, l := range listed {
		items = append(items, wpItem{Name: wpText(l.Name, 100), Title: wpText(l.Title, 200), Status: wpText(l.Status, 20),
			Version: wpText(l.Version, 40), Update: wpText(l.UpdateVersion, 40)})
	}
	return items, nil
}

// wpVersion is a WordPress version, for example 6.8 or 6.8.3.
var wpVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?([-.][A-Za-z0-9]+)*$`)

// checkWordPress reads the versions of WordPress, of the plugins and of the themes, and the
// available updates.
func (d *Deps) checkWordPress(ctx context.Context, site *Site) (wpInventory, error) {
	var inv wpInventory
	out, err := d.wp(ctx, site, "", append([]string{"core", "version"}, wpMaintenance...)...)
	if err != nil {
		return inv, fmt.Errorf("WordPress does not operate in %s: %w", documentRoot(site.Website), err)
	}
	if inv.Version = strings.TrimSpace(out); !wpVersion.MatchString(inv.Version) {
		return inv, fmt.Errorf("WP-CLI gave the version %q, which is not a WordPress version", wpText(inv.Version, 40))
	}
	out, err = d.wp(ctx, site, "", append([]string{"core", "check-update", "--format=json"}, wpMaintenance...)...)
	if err != nil {
		return inv, err
	}
	// With no update, WP-CLI writes a message and no list.
	if part := jsonPart(out); strings.HasPrefix(part, "[") {
		var updates []struct {
			Version    string `json:"version"`
			UpdateType string `json:"update_type"`
		}
		if err := json.Unmarshal([]byte(part), &updates); err != nil {
			return inv, fmt.Errorf("WP-CLI wrote updates that the agent cannot read: %w", err)
		}
		for _, u := range updates {
			v := wpText(u.Version, 40)
			if !wpVersion.MatchString(v) {
				continue
			}
			switch {
			case u.UpdateType == "minor" && inv.CoreMinor == "":
				inv.CoreMinor = v
			case u.UpdateType == "major" && inv.CoreLatest == "":
				inv.CoreLatest = v
			}
		}
	}
	fields := "--fields=name,title,status,version,update_version"
	if out, err = d.wp(ctx, site, "", append([]string{"plugin", "list", "--format=json", fields}, wpMaintenance...)...); err != nil {
		return inv, err
	}
	if inv.Plugins, err = wpItems(out); err != nil {
		return inv, err
	}
	if out, err = d.wp(ctx, site, "", append([]string{"theme", "list", "--format=json", fields}, wpMaintenance...)...); err != nil {
		return inv, err
	}
	if inv.Themes, err = wpItems(out); err != nil {
		return inv, err
	}
	if out, err = d.wp(ctx, site, "", append([]string{"option", "get", "siteurl"}, wpMaintenance...)...); err != nil {
		return inv, err
	}
	inv.SiteURL = wpText(out, 300)
	// The panel finds the database of a site that it did not install. A site can set its
	// database in a different way, so this value can be missing.
	if out, err = d.wp(ctx, site, "", append([]string{"config", "get", "DB_NAME"}, wpMaintenance...)...); err == nil && validate.DatabaseName(strings.TrimSpace(out)) == nil {
		inv.DatabaseName = strings.TrimSpace(out)
	}
	inv.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	return inv, nil
}

// CheckWordPress reads the versions and the available updates of a WordPress site.
func (d *Deps) CheckWordPress(req *agentv1.CheckWordPressRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetWebsiteId()); err != nil {
		return jobs.Job{}, err
	}
	var inv wpInventory
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Check WordPress", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			inv, err = d.checkWordPress(ctx, site)
			return err
		}},
	}, Result: func() map[string]any { return map[string]any{"wordpress": inv.result()} }}, nil
}

// checkInstall checks a request to install WordPress.
func checkInstall(req *agentv1.InstallWordPressRequest) error {
	errs := []error{validate.ID(req.GetWebsiteId()), checkWebsiteServer(req.GetDatabaseServer(), "mariadb"),
		validate.WordpressTitle(req.GetTitle()), validate.WordpressUser(req.GetAdminUser()),
		validate.MailAddress(req.GetAdminEmail()), validate.WordpressLocale(req.GetLocale())}
	if host, ok := strings.CutPrefix(req.GetUrl(), "https://"); !ok || validate.Domain(host) != nil {
		errs = append(errs, errors.New("the address of the site must be https:// and a domain"))
	}
	if validate.DatabasePassword(req.GetDatabasePassword()) != nil || validate.DatabasePassword(req.GetAdminPassword()) != nil {
		errs = append(errs, errors.New("each password must have 16 to 64 letters and digits"))
	}
	return errors.Join(errs...)
}

// wpHtaccess is the .htaccess file of WordPress for its permalinks, for the Apache runtime.
// FrankenPHP sends each unknown path to index.php with no file.
const wpHtaccess = `# BEGIN WordPress
<IfModule mod_rewrite.c>
RewriteEngine On
RewriteRule .* - [E=HTTP_AUTHORIZATION:%{HTTP:Authorization}]
RewriteBase /
RewriteRule ^index\.php$ - [L]
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
</IfModule>
# END WordPress
`

// InstallWordPress makes the database, downloads WordPress and installs it in the document
// root of a website.
func (d *Deps) InstallWordPress(req *agentv1.InstallWordPressRequest) (jobs.Job, error) {
	if err := checkInstall(req); err != nil {
		return jobs.Job{}, err
	}
	site, err := d.State.Get(req.GetWebsiteId())
	if err != nil {
		return jobs.Job{}, err
	}
	if site == nil {
		return jobs.Job{}, ErrUnknownWebsite
	}
	// CF-01: the names of the database and the user start with the Unix user of the website.
	if err := errors.Join(validate.TenantDatabaseName(site.UnixUser, req.GetDatabaseName()), validate.TenantDatabaseName(site.UnixUser, req.GetDatabaseUser())); err != nil {
		return jobs.Job{}, err
	}
	srv := req.GetDatabaseServer()
	dbHost := quadlet.MariaDBHost
	if p := srv.GetPort(); p != int32(quadlet.DatabasePort("mariadb")) {
		dbHost += ":" + strconv.Itoa(int(p))
	}
	var inv wpInventory
	steps := []jobs.Step{
		{Name: "Check the website", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if err := d.checkWPCLI(ctx, site); err != nil {
				return err
			}
			// The document root must be empty, or have only the default page. A step that
			// runs again after a restart finds the files of its first run.
			out, err := d.asUser(ctx, site.UnixUser, "", "sh", "-c", `mkdir -p "$1" && cd "$1" || exit 1
n=$(ls -A | wc -l)
if [ "$n" -eq 0 ]; then echo empty
elif [ "$n" -eq 1 ] && [ -f index.php ] && grep -q "$2" index.php; then echo empty
elif [ -f wp-includes/version.php ] && [ -f wp-config.php ] && grep -q "$3" wp-config.php; then echo ours
else echo used; fi`, "sh", d.siteRoot(site), wpDefaultPage, req.GetDatabaseName())
			if err != nil {
				return commandError("read the document root", err)
			}
			if strings.TrimSpace(out) == "used" {
				return fmt.Errorf("the document root %s has files; WordPress installs only in an empty document root", documentRoot(site.Website))
			}
			return nil
		}},
		{Name: "Create database", Do: func(ctx context.Context) error {
			s, err := d.websiteServer(ctx, req.GetWebsiteId(), srv, "mariadb")
			if err != nil {
				return err
			}
			drv, err := sqlOf(s.Engine)
			if err != nil {
				return err
			}
			if err := drv.ensureDatabase(ctx, d, s, req.GetDatabaseName()); err != nil {
				return err
			}
			// A step that runs again after a restart makes the user again.
			if err := d.dropUser(ctx, s, req.GetDatabaseUser()); err != nil {
				return err
			}
			host, err := mariaHost("website")
			if err != nil {
				return err
			}
			if err := d.createUser(ctx, s, req.GetDatabaseUser(), req.GetDatabasePassword(), host, "website"); err != nil {
				return err
			}
			_, err = d.sql(ctx, s, grantSQL(s.Engine, req.GetDatabaseUser(), host, req.GetDatabaseName(), "readwrite"))
			return err
		}},
		{Name: "Download WordPress", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			_, err = d.wp(ctx, site, "", "core", "download", "--locale="+req.GetLocale(), "--force")
			return err
		}},
		{Name: "Install WordPress", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			// The passwords go on stdin (--prompt), not in the arguments.
			if _, err := d.wp(ctx, site, req.GetDatabasePassword()+"\n", "config", "create", "--dbname="+req.GetDatabaseName(),
				"--dbuser="+req.GetDatabaseUser(), "--dbhost="+dbHost, "--dbcharset=utf8mb4", "--locale="+req.GetLocale(), "--force", "--prompt=dbpass"); err != nil {
				return err
			}
			if _, err := d.wp(ctx, site, req.GetAdminPassword()+"\n", "core", "install", "--url="+req.GetUrl(),
				"--title="+req.GetTitle(), "--admin_user="+req.GetAdminUser(), "--admin_email="+req.GetAdminEmail(), "--skip-email", "--prompt=admin_password"); err != nil {
				return err
			}
			// The panel makes the updates, with a restore point and a health check.
			if _, err := d.wp(ctx, site, "", "config", "set", "AUTOMATIC_UPDATER_DISABLED", "true", "--raw"); err != nil {
				return err
			}
			if _, err := d.wp(ctx, site, "", "rewrite", "structure", "/%postname%/"); err != nil {
				return err
			}
			if site.Website.GetRuntime() == "apache-php-fpm" {
				if _, err := d.asUser(ctx, site.UnixUser, wpHtaccess, "sh", "-c", `[ -e "$1/.htaccess" ] || cat > "$1/.htaccess"`, "sh", d.siteRoot(site)); err != nil {
					return commandError("write .htaccess", err)
				}
			}
			return nil
		}},
		{Name: "Check WordPress", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			inv, err = d.checkWordPress(ctx, site)
			return err
		}},
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: steps, Result: func() map[string]any {
		return map[string]any{"wordpress": inv.result()}
	}}, nil
}

// wpDatabase gives the database server and the name of the database of a WordPress site, or
// a zero server for a site with no database in the request.
func (d *Deps) wpDatabase(site *Site, ref *agentv1.DatabaseRef) (dbServer, string, error) {
	if ref.GetName() == "" {
		return dbServer{}, "", nil
	}
	if ref.GetEngine() != "mariadb" {
		return dbServer{}, "", errors.New("the database of a WordPress site must be a MariaDB database")
	}
	if err := validate.TenantDatabaseName(site.UnixUser, ref.GetName()); err != nil {
		return dbServer{}, "", err
	}
	s, err := d.server(site.ID, ref.GetDatabaseServer(), ref.GetEngine())
	return s, ref.GetName(), err
}

// saveRestorePoint saves the files of a WordPress site, with no uploads, and a dump of its
// database in a new restore point. The old restore point stays until the new one is complete.
func (d *Deps) saveRestorePoint(ctx context.Context, site *Site, s dbServer, name string) error {
	dir := d.restoreDir(site)
	args := []string{"sh", "-c", `set -e
umask 077
rm -rf "$1.new"
mkdir -p "$1.new"
dir=$1 root=$2
shift 2
tar -C "$root" --anchored "$@" -czf "$dir.new/files.tar.gz" .`, "sh", dir, d.siteRoot(site)}
	for _, k := range wpKeep {
		args = append(args, "--exclude=./wp-content/"+k)
	}
	if _, err := d.asUser(ctx, site.UnixUser, "", args...); err != nil {
		return commandError("save the files", err)
	}
	if name != "" {
		dump, err := d.dumpCommand(ctx, s, name)
		if err != nil {
			return err
		}
		// The dump goes from the database server to the file as a stream. The website user
		// writes the file.
		if err := d.Run.Pipe(ctx, dump, []string{"runuser", "-u", site.UnixUser, "--", "sh", "-c", `umask 077 && cat > "$1"`, "sh", dir + ".new/database.sql"}); err != nil {
			return commandError("save the database", err)
		}
	}
	if _, err := d.asUser(ctx, site.UnixUser, "", "sh", "-c", `rm -rf "$1" && mv "$1.new" "$1"`, "sh", dir); err != nil {
		return commandError("keep the restore point", err)
	}
	return nil
}

// putBackRestorePoint puts back the files and the database of the restore point. The
// directories of wpKeep stay as they are.
func (d *Deps) putBackRestorePoint(ctx context.Context, site *Site, s dbServer, name string) error {
	dir := d.restoreDir(site)
	out, err := d.asUser(ctx, site.UnixUser, "", "sh", "-c", `[ -f "$1/files.tar.gz" ] || exit 0; echo files; [ -f "$1/database.sql" ] && echo database; exit 0`, "sh", dir)
	if err != nil {
		return commandError("read the restore point", err)
	}
	if !strings.Contains(out, "files") {
		return errors.New("the website has no restore point")
	}
	if name != "" && !strings.Contains(out, "database") {
		return errors.New("the restore point has no database")
	}
	args := []string{"sh", "-c", `set -e
dir=$1 root=$2
shift 2
mkdir -p "$root"
cd "$root"
for e in * .[!.]* ..?*; do
	{ [ -e "$e" ] || [ -L "$e" ]; } || continue
	[ "$e" = wp-content ] && continue
	rm -rf -- "$e"
done
if [ -d wp-content ] && [ ! -L wp-content ]; then
	cd wp-content
	for e in * .[!.]* ..?*; do
		{ [ -e "$e" ] || [ -L "$e" ]; } || continue
		keep=
		for k in "$@"; do [ "$e" = "$k" ] && keep=1; done
		[ -n "$keep" ] || rm -rf -- "$e"
	done
	cd ..
fi
tar -xzf "$dir/files.tar.gz"`, "sh", dir, d.siteRoot(site)}
	if _, err := d.asUser(ctx, site.UnixUser, "", append(args, wpKeep...)...); err != nil {
		return commandError("put back the files", err)
	}
	if name == "" {
		return nil
	}
	drv, err := sqlOf(s.Engine)
	if err != nil {
		return err
	}
	// The dump loads as a temporary user with access to this database only (CF-06), through
	// the filter of the engine (finding G3-02).
	return d.restoreDump(ctx, s, name, func(ctx context.Context, client []string) error {
		read := []string{"runuser", "-u", site.UnixUser, "--", "cat", "--", filepath.Join(dir, "database.sql")}
		return d.pipeFiltered(ctx, read, client, drv.dumpFilter())
	})
}

// pipeFiltered sends the output of the command from to the command to, through filter as a
// stream. filter can be nil.
func (d *Deps) pipeFiltered(ctx context.Context, from, to []string, filter func(io.Reader) io.Reader) error {
	if filter == nil {
		return d.Run.Pipe(ctx, from, to)
	}
	pr, pw := io.Pipe()
	sent := make(chan error, 1)
	go func() {
		err := d.Run.Stream(ctx, nil, pw, from[0], from[1:]...)
		pw.CloseWithError(err)
		sent <- err
	}()
	err := d.Run.Stream(ctx, filter(pr), nil, to[0], to[1:]...)
	pr.CloseWithError(err)
	if serr := <-sent; serr != nil && err == nil {
		err = serr
	}
	return err
}

// checkUpdate checks a request to update a WordPress site.
func checkUpdate(req *agentv1.UpdateWordPressRequest) error {
	errs := []error{validate.ID(req.GetWebsiteId())}
	switch req.GetCore() {
	case "", "minor", "latest":
	default:
		errs = append(errs, fmt.Errorf("the update of WordPress %q is not valid", req.GetCore()))
	}
	if len(req.GetPlugins())+len(req.GetThemes()) > validate.MaxWordpressUpdates {
		errs = append(errs, fmt.Errorf("an update can have %d plugins and themes", validate.MaxWordpressUpdates))
	}
	for _, slug := range slices.Concat(req.GetPlugins(), req.GetThemes()) {
		errs = append(errs, validate.WordpressSlug(slug))
	}
	if req.GetCore() == "" && len(req.GetPlugins()) == 0 && len(req.GetThemes()) == 0 {
		errs = append(errs, errors.New("the update has nothing to update"))
	}
	return errors.Join(errs...)
}

// wpHealth is the health check after an update when the request has none: most answers of
// the site in 30 seconds are not server errors.
var wpHealth = &agentv1.HealthCheck{Status: 0, MaxErrorRate: 0.1, Seconds: 30}

// wpHealthCheck checks that WordPress loads with its plugins and its theme, and that the site
// answers.
func (d *Deps) wpHealthCheck(ctx context.Context, site *Site, hc *agentv1.HealthCheck) error {
	out, err := d.wp(ctx, site, "", "eval", `echo "cf-wordpress-ok";`)
	if err != nil {
		return fmt.Errorf("WordPress does not load: %w", err)
	}
	if !strings.Contains(out, "cf-wordpress-ok") {
		return errors.New("WordPress does not load: WP-CLI gave no answer")
	}
	if hc == nil {
		hc = wpHealth
	}
	return d.watch(ctx, site.Port, primaryDomain(site.Website), hc)
}

// UpdateWordPress saves a restore point, updates WordPress, plugins and themes, and checks the
// site. When a step after the restore point fails, the agent puts back the restore point.
func (d *Deps) UpdateWordPress(req *agentv1.UpdateWordPressRequest) (jobs.Job, error) {
	if err := checkUpdate(req); err != nil {
		return jobs.Job{}, err
	}
	var before, after wpInventory
	// guarded runs one step of the update. When it fails, the restore point goes back.
	guarded := func(do func(ctx context.Context, site *Site) error) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			err = do(ctx, site)
			if err == nil {
				return nil
			}
			s, name, derr := d.wpDatabase(site, req.GetDatabase())
			if derr == nil {
				derr = d.putBackRestorePoint(context.WithoutCancel(ctx), site, s, name)
			}
			if derr != nil {
				return fmt.Errorf("%w; the agent could not put back the restore point: %v", err, derr)
			}
			return fmt.Errorf("%w; the agent put back the restore point", err)
		}
	}
	steps := []jobs.Step{
		{Name: "Check WordPress", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if err := d.checkWPCLI(ctx, site); err != nil {
				return err
			}
			before, err = d.checkWordPress(ctx, site)
			return err
		}},
		{Name: "Save a restore point", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			s, name, err := d.wpDatabase(site, req.GetDatabase())
			if err != nil {
				return err
			}
			return d.saveRestorePoint(ctx, site, s, name)
		}},
		{Name: "Update WordPress", Do: guarded(func(ctx context.Context, site *Site) error {
			if req.GetCore() == "" {
				return nil
			}
			args := append([]string{"core", "update"}, wpMaintenance...)
			if req.GetCore() == "minor" {
				args = append(args, "--minor")
			}
			if _, err := d.wp(ctx, site, "", args...); err != nil {
				return err
			}
			_, err := d.wp(ctx, site, "", append([]string{"core", "update-db"}, wpMaintenance...)...)
			return err
		})},
		{Name: "Update plugins", Do: guarded(func(ctx context.Context, site *Site) error {
			if len(req.GetPlugins()) == 0 {
				return nil
			}
			_, err := d.wp(ctx, site, "", slices.Concat([]string{"plugin", "update"}, wpMaintenance, req.GetPlugins())...)
			return err
		})},
		{Name: "Update themes", Do: guarded(func(ctx context.Context, site *Site) error {
			if len(req.GetThemes()) == 0 {
				return nil
			}
			_, err := d.wp(ctx, site, "", slices.Concat([]string{"theme", "update"}, wpMaintenance, req.GetThemes())...)
			return err
		})},
		{Name: "Health check", Do: guarded(func(ctx context.Context, site *Site) error {
			if err := d.wpHealthCheck(ctx, site, req.GetHealthCheck()); err != nil {
				return err
			}
			var err error
			after, err = d.checkWordPress(ctx, site)
			return err
		})},
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Long: true, Steps: steps, Result: func() map[string]any {
		return map[string]any{"before": before.result(), "wordpress": after.result()}
	}}, nil
}

// RestoreWordPress puts back the restore point of the last update.
func (d *Deps) RestoreWordPress(req *agentv1.RestoreWordPressRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetWebsiteId()); err != nil {
		return jobs.Job{}, err
	}
	var inv wpInventory
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Long: true, Steps: []jobs.Step{
		{Name: "Put back the restore point", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			s, name, err := d.wpDatabase(site, req.GetDatabase())
			if err != nil {
				return err
			}
			return d.putBackRestorePoint(ctx, site, s, name)
		}},
		{Name: "Check WordPress", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			inv, err = d.checkWordPress(ctx, site)
			return err
		}},
	}, Result: func() map[string]any { return map[string]any{"wordpress": inv.result()} }}, nil
}

// checkWpCli checks the arguments of a WP-CLI command from the panel.
func checkWpCli(args []string) error {
	if len(args) == 0 {
		return errors.New("enter a WP-CLI command")
	}
	if len(args) > validate.MaxWpCliArguments {
		return fmt.Errorf("a WP-CLI command can have %d arguments", validate.MaxWpCliArguments)
	}
	errs := make([]error, 0, len(args))
	for _, a := range args {
		errs = append(errs, validate.WpCliArgument(a))
	}
	return errors.Join(errs...)
}

// wpCliScript runs WP-CLI in the container ($2) with the time limit $1 and the document root
// $3, and then writes the exit code. The arguments of WP-CLI come after $3: they go to WP-CLI
// as they are, with no shell. "tail" gives only the end of the output to the agent, as for a
// cron job (cronScript).
const wpCliScript = `limit=$1 container=$2 root=$3
shift 3
{ timeout -k 10 "$((limit + 30))" podman exec --workdir "$root" --env HOME=/tmp "$container" timeout -k 10 "$limit" wp "--path=$root" "$@" 2>&1; echo "cf-exit:$?"; } | tail -c 16384`

// RunWpCli runs one WP-CLI command from the panel in the container of a website. The job
// succeeds when the command ran: the result has its exit code and the end of its output.
// Then the agent reads the versions again, because the command can change them.
func (d *Deps) RunWpCli(req *agentv1.RunWpCliRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), checkWpCli(req.GetArgs())); err != nil {
		return jobs.Job{}, err
	}
	exitCode, output, ranAt := -1, "", ""
	var inv *wpInventory
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Run the command", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			uid, err := d.Host.LookupUID(site.UnixUser)
			if err != nil {
				return err
			}
			container := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}.ContainerName()
			ranAt = time.Now().UTC().Format(time.RFC3339)
			ctx, cancel := context.WithTimeout(ctx, wpCliLimit+time.Minute)
			defer cancel()
			argv := append([]string{"-u", site.UnixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid), "sh", "-c", wpCliScript,
				"sh", strconv.Itoa(int(wpCliLimit.Seconds())), container, documentRoot(site.Website)}, req.GetArgs()...)
			out, err := d.Run.Run(ctx, "runuser", argv...)
			m := cronExit.FindStringSubmatch(out)
			if m == nil {
				if err != nil {
					return commandError("the command did not start", err)
				}
				return errors.New("the command did not start")
			}
			exitCode, _ = strconv.Atoi(m[2])
			output = m[1]
			switch exitCode {
			case 124:
				output += fmt.Sprintf("\nThe command did not end in %s. The agent stopped it.", wpCliLimit)
			case 126, 127:
				return errNoWPCLI
			}
			output = lastLines(cleanText(output), wpCliOutputMax)
			return nil
		}},
		{Name: "Check WordPress", Do: func(ctx context.Context) error {
			// The command can remove WordPress, so the job does not fail here.
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return nil
			}
			if got, err := d.checkWordPress(ctx, site); err == nil {
				inv = &got
			}
			return nil
		}},
	}, Result: func() map[string]any {
		r := map[string]any{"exitCode": exitCode, "output": output, "ranAt": ranAt}
		if inv != nil {
			r["wordpress"] = inv.result()
		}
		return r
	}}, nil
}
