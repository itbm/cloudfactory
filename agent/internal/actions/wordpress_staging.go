package actions

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The staging copy of a WordPress site (FR-205, FR-206). The copy has the files and a copy of
// the database of the live site. Without changes, WordPress in the copy connects to the
// database of the live site, and sends each visitor to the address of the live site. Thus:
//
//   - In CreateWebsite, the agent sets the database copy and its user in wp-config.php of the
//     copy, before the container of the copy starts.
//   - StageWordPress then replaces the address of the live site in the database copy, and asks
//     search engines not to index the copy.

// wpQuoted is a PHP string in single or double quotes.
const wpQuoted = `'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"`

// wpDatabaseConstants are the constants of the database connection in wp-config.php that the
// copy changes. DB_HOST stays: the database copy is in the database server of the source.
var wpDatabaseConstants = []string{"DB_NAME", "DB_USER", "DB_PASSWORD"}

// wpDefines finds the definition of each constant with a text value, for example
// define( 'DB_NAME', 'acme_x7k2_wp' ).
var wpDefines = func() map[string]*regexp.Regexp {
	m := map[string]*regexp.Regexp{}
	for _, name := range wpDatabaseConstants {
		m[name] = regexp.MustCompile(`(\b(?i:define)\s*\(\s*['"]` + name + `['"]\s*,\s*)(?:` + wpQuoted + `)`)
	}
	return m
}()

// setWPConfigDatabase gives wp-config.php with new values of DB_NAME, DB_USER and DB_PASSWORD.
// Each value has only letters, digits and "_", so it needs no escape in single quotes.
func setWPConfigDatabase(config string, values map[string]string) (string, error) {
	for _, name := range wpDatabaseConstants {
		re := wpDefines[name]
		if !re.MatchString(config) {
			return "", fmt.Errorf("wp-config.php of the copy does not set %s with a text value, so the agent cannot connect the copy to its database copy", name)
		}
		config = re.ReplaceAllString(config, "${1}'"+values[name]+"'")
	}
	return config, nil
}

// checkWordPressCopy checks the WordPress site of a staging copy: its database is a MariaDB
// database copy, and its user is a MariaDB user of the copy in the same database server.
func checkWordPressCopy(req *agentv1.WebsiteRequest) error {
	wp := req.GetCopyWordpress()
	if wp == nil {
		return nil
	}
	if req.GetCopyFrom() == "" {
		return errors.New("only a staging copy has a WordPress copy")
	}
	user := req.GetWebsite().GetUnixUser()
	if err := errors.Join(validate.TenantDatabaseName(user, wp.GetDatabase()), validate.TenantDatabaseName(user, wp.GetUser())); err != nil {
		return err
	}
	i := slices.IndexFunc(req.GetCopyDatabases(), func(c *agentv1.DatabaseCopy) bool {
		return c.GetEngine() == "mariadb" && c.GetTo() == wp.GetDatabase()
	})
	if i < 0 {
		return errors.New("the database of the WordPress copy is not a MariaDB database copy")
	}
	server := req.GetCopyDatabases()[i].GetToServer()
	if wordPressCopyUser(req) == nil || !strings.EqualFold(wordPressCopyUser(req).GetDatabaseServer(), server) {
		return errors.New("the user of the WordPress copy is not a MariaDB user of the copy in the database server of its database")
	}
	return nil
}

// wordPressCopyUser gives the user of the WordPress copy in copy_users, or nil.
func wordPressCopyUser(req *agentv1.WebsiteRequest) *agentv1.DatabaseUserCopy {
	for _, u := range req.GetCopyUsers() {
		if u.GetEngine() == "mariadb" && u.GetName() == req.GetCopyWordpress().GetUser() {
			return u
		}
	}
	return nil
}

// wpConfigScript prints the path and the text of the first file that exists. WordPress reads
// wp-config.php in its directory, or in the directory above it.
const wpConfigScript = `for f do
if [ -f "$f" ]; then printf '%s\n' "$f"; exec cat -- "$f"; fi
done
exit 3`

// connectWordPressCopy sets the database copy and its user in wp-config.php of a staging copy.
// The website user of the copy reads and writes the file (CF-05). The password goes on stdin.
func (d *Deps) connectWordPressCopy(ctx context.Context, req *agentv1.WebsiteRequest) error {
	wp := req.GetCopyWordpress()
	if wp == nil {
		return nil
	}
	w := req.GetWebsite()
	app := d.Host.AppDir(w.GetUnixUser())
	root := filepath.Join(app, strings.TrimPrefix(documentRoot(w), "/app"))
	paths := []string{filepath.Join(root, "wp-config.php")}
	if root != app {
		paths = append(paths, filepath.Join(filepath.Dir(root), "wp-config.php"))
	}
	out, err := d.asUser(ctx, w.GetUnixUser(), "", append([]string{"sh", "-c", wpConfigScript, "sh"}, paths...)...)
	if err != nil {
		if strings.Contains(err.Error(), "exit status 3") {
			return fmt.Errorf("the copy has no wp-config.php in %s, so the agent cannot connect the copy to its database copy", documentRoot(w))
		}
		return commandError("read wp-config.php", err)
	}
	path, config, _ := strings.Cut(out, "\n")
	if !slices.Contains(paths, path) {
		return errors.New("the agent could not read wp-config.php of the copy")
	}
	config, err = setWPConfigDatabase(config, map[string]string{
		"DB_NAME": wp.GetDatabase(), "DB_USER": wp.GetUser(), "DB_PASSWORD": wordPressCopyUser(req).GetPassword(),
	})
	if err != nil {
		return err
	}
	if _, err := d.asUser(ctx, w.GetUnixUser(), config, "sh", "-c", `cat > "$1"`, "sh", path); err != nil {
		return commandError("write wp-config.php", err)
	}
	return nil
}

// wpHost gives the host of an address of a WordPress site, for example acme.example from
// https://acme.example/blog.
func wpHost(address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" || validate.Domain(u.Hostname()) != nil {
		return "", fmt.Errorf("%q is not the address of a site: http:// or https:// and a domain", wpText(address, 100))
	}
	return strings.ToLower(u.Hostname()), nil
}

// wpReplaceOptions are the options of the search-replace of a staging copy. WordPress
// recommends to keep the GUIDs of the posts. With --all-tables-with-prefix, the replace also
// changes the tables of the plugins.
var wpReplaceOptions = []string{"--all-tables-with-prefix", "--skip-columns=guid", "--report-changed-only"}

// StageWordPress makes the WordPress site of a new staging copy use the address of the copy.
func (d *Deps) StageWordPress(req *agentv1.StageWordPressRequest) (jobs.Job, error) {
	errs := []error{validate.ID(req.GetWebsiteId())}
	to, err := wpHost(req.GetUrl())
	if err != nil || !strings.HasPrefix(req.GetUrl(), "https://") {
		errs = append(errs, errors.New("the address of the copy must be https:// and a domain"))
	}
	if len(req.GetFromUrls()) > 10 {
		errs = append(errs, errors.New("the panel can send 10 addresses of the live site"))
	}
	var from []string
	for _, u := range req.GetFromUrls() {
		h, err := wpHost(u)
		errs = append(errs, err)
		from = append(from, h)
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	var inv wpInventory
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Replace the site address", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			hosts := slices.Clone(from)
			// The database copy can have a different address than the panel knows.
			for _, option := range []string{"home", "siteurl"} {
				out, err := d.wp(ctx, site, "", append([]string{"option", "get", option}, wpMaintenance...)...)
				if err != nil {
					return fmt.Errorf("WordPress does not operate in the copy: %w", err)
				}
				if h, err := wpHost(strings.TrimSpace(out)); err == nil {
					hosts = append(hosts, h)
				}
			}
			hosts = slices.DeleteFunc(hosts, func(h string) bool { return h == to })
			// The longest host first: acme.example.org before acme.example.
			slices.SortFunc(hosts, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
			hosts = slices.Compact(hosts)
			for _, h := range hosts {
				// "//" keeps http and https. "\/\/" is the same address in JSON, for example in
				// the blocks of the editor.
				for _, slash := range []string{"//", `\/\/`} {
					args := append([]string{"search-replace", slash + h, slash + to}, wpReplaceOptions...)
					if _, err := d.wp(ctx, site, "", append(args, wpMaintenance...)...); err != nil {
						return err
					}
				}
			}
			// wp-config.php can set the address in WP_HOME and WP_SITEURL.
			for _, name := range []string{"WP_HOME", "WP_SITEURL"} {
				out, err := d.wp(ctx, site, "", "config", "get", name)
				if err != nil {
					continue
				}
				value := strings.TrimSpace(out)
				next := value
				for _, h := range hosts {
					next = strings.ReplaceAll(next, "//"+h, "//"+to)
				}
				if next == value || !strings.HasPrefix(next, "http") {
					continue
				}
				if _, err := d.wp(ctx, site, "", "config", "set", name, next, "--type=constant"); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "Hide from search engines", Do: func(ctx context.Context) error {
			site, err := d.wordpressSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if _, err := d.wp(ctx, site, "", append([]string{"option", "update", "blog_public", "0"}, wpMaintenance...)...); err != nil {
				return err
			}
			// Plugins read the type of the environment, for example to send no email from a
			// copy. A wp-config.php with no anchor for a new constant is not an error.
			if _, err := d.wp(ctx, site, "", "config", "set", "WP_ENVIRONMENT_TYPE", "staging", "--type=constant"); err != nil {
				slog.Warn("no WP_ENVIRONMENT_TYPE in the staging copy", "website", req.GetWebsiteId(), "err", err)
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
	}, Result: func() map[string]any { return map[string]any{"wordpress": inv.result()} }}, nil
}
