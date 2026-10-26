package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// DBUser is the Unix user of the shared database containers.
const DBUser = "cf-db"

var passwordRE = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)

func dbContainer(engine string) (string, error) {
	switch engine {
	case "mariadb":
		return "cf-mariadb", nil
	case "postgresql":
		return "cf-postgresql", nil
	}
	return "", fmt.Errorf("the database engine %q is not valid", engine)
}

// sql sends statements on stdin to the database client in the shared container.
func (d *Deps) sql(ctx context.Context, engine, statements string) (string, error) {
	c, err := dbContainer(engine)
	if err != nil {
		return "", err
	}
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return "", err
	}
	args := []string{"-u", DBUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", "-i", c}
	if engine == "mariadb" {
		args = append(args, "mariadb", "--user=root", "--batch", "--skip-column-names")
	} else {
		args = append(args, "psql", "--username=postgres", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align")
	}
	return d.Run.RunInput(ctx, statements, "runuser", args...)
}

// CreateDatabase makes a database and a user with access to that database only (FR-501, FR-502).
// The names are checked, so they are safe in SQL. The password has only letters and digits.
func (d *Deps) CreateDatabase(req *agentv1.CreateDatabaseRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.DatabaseName(req.GetName()), validate.DatabaseName(req.GetUser())); err != nil {
		return jobs.Job{}, err
	}
	if _, err := dbContainer(req.GetEngine()); err != nil {
		return jobs.Job{}, err
	}
	if !passwordRE.MatchString(req.GetPassword()) {
		return jobs.Job{}, errors.New("the database password must have 16 to 64 letters and digits")
	}
	name, user, pass, engine := req.GetName(), req.GetUser(), req.GetPassword(), req.GetEngine()
	maria := engine == "mariadb"
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Create database", Do: func(ctx context.Context) error {
			if maria {
				_, err := d.sql(ctx, engine, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name))
				return err
			}
			out, err := d.sql(ctx, engine, fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s';\n", name))
			if err != nil || strings.TrimSpace(out) == "1" {
				return err
			}
			_, err = d.sql(ctx, engine, fmt.Sprintf("CREATE DATABASE \"%s\";\n", name))
			return err
		}},
		{Name: "Create user", Do: func(ctx context.Context) error {
			if maria {
				_, err := d.sql(ctx, engine, fmt.Sprintf("CREATE USER IF NOT EXISTS '%[1]s'@'%%' IDENTIFIED BY '%[2]s';\nALTER USER '%[1]s'@'%%' IDENTIFIED BY '%[2]s';\n", user, pass))
				return err
			}
			_, err := d.sql(ctx, engine, fmt.Sprintf("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN CREATE ROLE \"%[1]s\" LOGIN; END IF; END $$;\nALTER ROLE \"%[1]s\" WITH LOGIN PASSWORD '%[2]s';\n", user, pass))
			return err
		}},
		{Name: "Allow the website container only", Do: func(ctx context.Context) error {
			// The database port listens only on the host. Each user has access to its own
			// database only.
			if maria {
				_, err := d.sql(ctx, engine, fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%';\nFLUSH PRIVILEGES;\n", name, user))
				return err
			}
			_, err := d.sql(ctx, engine, fmt.Sprintf("REVOKE ALL ON DATABASE \"%[1]s\" FROM PUBLIC;\nALTER DATABASE \"%[1]s\" OWNER TO \"%[2]s\";\n", name, user))
			return err
		}},
	}}, nil
}

// DeleteDatabase removes a database and its user.
func (d *Deps) DeleteDatabase(req *agentv1.DeleteDatabaseRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.DatabaseName(req.GetName())); err != nil {
		return jobs.Job{}, err
	}
	if _, err := dbContainer(req.GetEngine()); err != nil {
		return jobs.Job{}, err
	}
	name, engine := req.GetName(), req.GetEngine()
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Remove database", Do: func(ctx context.Context) error {
			if engine == "mariadb" {
				_, err := d.sql(ctx, engine, fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`;\nDROP USER IF EXISTS '%[1]s'@'%%';\n", name))
				return err
			}
			_, err := d.sql(ctx, engine, fmt.Sprintf("DROP DATABASE IF EXISTS \"%[1]s\" WITH (FORCE);\nDROP ROLE IF EXISTS \"%[1]s\";\n", name))
			return err
		}},
	}}, nil
}

var targetRE = regexp.MustCompile(`^(s3|b2|azure|gs):\S+$`)

func (d *Deps) restic(ctx context.Context, target string, args ...string) (string, error) {
	base := []string{"--repo", target, "--password-file", d.ResticPasswordFile, "--json"}
	return d.Run.Run(ctx, "restic", append(base, args...)...)
}

func dumpDir(home string) string { return filepath.Join(home, ".cloudfactory", "dumps") }

// Backup makes an encrypted, incremental snapshot of the files and databases of a website (FR-901).
func (d *Deps) Backup(req *agentv1.BackupRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.UnixUser(req.GetUnixUser())); err != nil {
		return jobs.Job{}, err
	}
	target := strings.Replace(req.GetTarget(), "://", ":", 1)
	if !targetRE.MatchString(target) {
		return jobs.Job{}, fmt.Errorf("the backup target %q is not valid", req.GetTarget())
	}
	for _, db := range req.GetDatabases() {
		if _, err := dbContainer(db.GetEngine()); err != nil {
			return jobs.Job{}, err
		}
		if err := validate.DatabaseName(db.GetName()); err != nil {
			return jobs.Job{}, err
		}
	}
	home := d.Host.Home(req.GetUnixUser())
	var snapshotID string
	var size float64
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Snapshot files", Do: func(context.Context) error {
			_, err := os.Stat(filepath.Join(home, "app"))
			return err
		}},
		{Name: "Dump databases", Do: func(ctx context.Context) error {
			if err := os.MkdirAll(dumpDir(home), 0o700); err != nil {
				return err
			}
			for _, db := range req.GetDatabases() {
				out, err := d.dump(ctx, db.GetEngine(), db.GetName())
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dumpDir(home), db.GetEngine()+"-"+db.GetName()+".sql"), []byte(out), 0o600); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "Upload to object storage", Do: func(ctx context.Context) error {
			out, err := d.restic(ctx, target, "backup", home, "--tag", "website="+req.GetWebsiteId(), "--tag", "kind="+req.GetKind())
			if err != nil {
				return err
			}
			snapshotID, size = parseResticSummary(out)
			if snapshotID == "" {
				return errors.New("restic did not give a snapshot ID")
			}
			return nil
		}},
	}, Result: func() map[string]any { return map[string]any{"snapshotId": snapshotID, "sizeBytes": int64(size)} }}, nil
}

func (d *Deps) dump(ctx context.Context, engine, name string) (string, error) {
	c, _ := dbContainer(engine)
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return "", err
	}
	args := []string{"-u", DBUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", c}
	if engine == "mariadb" {
		args = append(args, "mariadb-dump", "--user=root", "--single-transaction", "--routines", name)
	} else {
		args = append(args, "pg_dump", "--username=postgres", "--clean", "--if-exists", name)
	}
	return d.Run.Run(ctx, "runuser", args...)
}

// parseResticSummary reads the "summary" line of "restic backup --json".
func parseResticSummary(out string) (id string, size float64) {
	for _, line := range strings.Split(out, "\n") {
		var m struct {
			Type       string  `json:"message_type"`
			SnapshotID string  `json:"snapshot_id"`
			Bytes      float64 `json:"total_bytes_processed"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Type == "summary" {
			return m.SnapshotID, m.Bytes
		}
	}
	return "", 0
}

// Restore puts back a full website, one database or one file from a snapshot (FR-903).
func (d *Deps) Restore(req *agentv1.RestoreRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.UnixUser(req.GetUnixUser())); err != nil {
		return jobs.Job{}, err
	}
	if !regexp.MustCompile(`^[0-9a-f]{8,64}$`).MatchString(req.GetSnapshotId()) {
		return jobs.Job{}, errors.New("the snapshot ID is not valid")
	}
	target := strings.Replace(req.GetTarget(), "://", ":", 1)
	if !targetRE.MatchString(target) {
		return jobs.Job{}, errors.New("no backup target is set")
	}
	home := d.Host.Home(req.GetUnixUser())
	var include string
	var dbEngine, dbName string
	switch req.GetScope() {
	case "website":
		include = home
	case "file":
		if err := validate.RestorePath(req.GetItem()); err != nil {
			return jobs.Job{}, err
		}
		include = filepath.Join(home, "app") + req.GetItem()
	case "database":
		engine, name, ok := strings.Cut(req.GetItem(), ":")
		if !ok {
			engine, name = "mariadb", req.GetItem()
		}
		if _, err := dbContainer(engine); err != nil {
			return jobs.Job{}, err
		}
		if err := validate.DatabaseName(name); err != nil {
			return jobs.Job{}, err
		}
		dbEngine, dbName = engine, name
		include = filepath.Join(dumpDir(home), engine+"-"+name+".sql")
	default:
		return jobs.Job{}, fmt.Errorf("the restore scope %q is not valid", req.GetScope())
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Download snapshot", Do: func(ctx context.Context) error {
			_, err := d.restic(ctx, target, "snapshots", req.GetSnapshotId())
			return err
		}},
		{Name: "Restore", Do: func(ctx context.Context) error {
			if _, err := d.restic(ctx, target, "restore", req.GetSnapshotId(), "--target", "/", "--include", include); err != nil {
				return err
			}
			if dbName == "" {
				return nil
			}
			sql, err := os.ReadFile(include)
			if err != nil {
				return err
			}
			if dbEngine == "mariadb" {
				sql = append([]byte("USE `"+dbName+"`;\n"), sql...)
			} else {
				sql = append([]byte("\\connect \""+dbName+"\"\n"), sql...)
			}
			_, err = d.sql(ctx, dbEngine, string(sql))
			return err
		}},
		{Name: "Health check", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil || site == nil {
				return err
			}
			if req.GetScope() == "website" {
				u := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}
				if _, err := d.Host.Systemctl(ctx, site.UnixUser, "restart", u.ServiceName()); err != nil {
					return err
				}
			}
			return d.waitReady(ctx, site.Port, primaryDomain(site.Website))
		}},
	}}, nil
}
