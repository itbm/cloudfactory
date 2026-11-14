package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

var (
	// mariaPluginRE and mariaHashRE check the authentication plugin and the password hash of a
	// MariaDB user before the agent puts them in a statement.
	mariaPluginRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	mariaHashRE   = regexp.MustCompile(`^[A-Za-z0-9*+/=.$-]{1,512}$`)
	// pgVerifierRE checks the SCRAM-SHA-256 verifier of a PostgreSQL user.
	pgVerifierRE = regexp.MustCompile(`^SCRAM-SHA-256\$[0-9]{1,9}:[A-Za-z0-9+/=]{1,128}\$[A-Za-z0-9+/=]{1,128}:[A-Za-z0-9+/=]{1,128}$`)
)

// serverExists reports whether cf-db has the unit of a database container. A statement that
// only removes data does not start a container that the server does not have.
func (d *Deps) serverExists(s dbServer) bool {
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, s.container()+".container"))
	return err == nil
}

// MoveDatabases moves the databases and the database users of a website from the shared
// database containers of an earlier release to the database containers of its customer. The
// panel sends it for each website of a server from an earlier release.
//
// The website does not operate during the copy, so that the copy has all the data. The
// password of each user stays the same: the agent copies its hash. The host names and the
// ports in the website container stay the same too, so the website needs no new settings.
//
// The website uses the new containers from the step "Start website". Before that step, a
// failure starts the website again with the shared containers. After it, the job only
// removes the old copies, and a job that runs again does only that.
func (d *Deps) MoveDatabases(req *agentv1.MoveDatabasesRequest) (jobs.Job, error) {
	id, unixUser := req.GetWebsiteId(), req.GetUnixUser()
	key, err := instanceKey(req.GetDatabaseInstance())
	errs := []error{validate.ID(id), validate.UnixUser(unixUser), err}
	if req.GetDatabaseInstance() == "" {
		errs = append(errs, errors.New("the request has no database instance"))
	}
	names := map[string]bool{}
	for _, db := range req.GetDatabases() {
		errs = append(errs, sqlEngine(db.GetEngine()), validate.TenantDatabaseName(unixUser, db.GetName()))
		names[db.GetEngine()+":"+db.GetName()] = true
	}
	for _, u := range req.GetUsers() {
		errs = append(errs, sqlEngine(u.GetEngine()), validate.TenantDatabaseName(unixUser, u.GetName()))
		if _, err := d.mariaHost(dbServer{}, u.GetAccess()); err != nil {
			errs = append(errs, err)
		}
		for _, g := range u.GetGrants() {
			if _, err := mariaPrivileges(g.GetPrivileges()); err != nil {
				errs = append(errs, err)
			}
			if !names[u.GetEngine()+":"+g.GetDatabase()] {
				errs = append(errs, fmt.Errorf("the database %s is not a database of the request", g.GetDatabase()))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	// The engines of the request, in the order of Engines.
	var engines []string
	for _, engine := range Engines {
		used := false
		for _, db := range req.GetDatabases() {
			used = used || db.GetEngine() == engine
		}
		for _, u := range req.GetUsers() {
			used = used || u.GetEngine() == engine
		}
		if used {
			engines = append(engines, engine)
		}
	}
	old := func(engine string) dbServer { return dbServer{Engine: engine} }
	next := func(engine string) dbServer { return dbServer{Instance: key, Engine: engine} }

	var site *Site
	// moved is true when the website uses the containers of its customer already: an earlier
	// run of this job came to the step "Start website".
	var moved bool
	// stopped is true while this job keeps the website stopped.
	var stopped bool
	return jobs.Job{ID: req.GetJobId(), Key: id, Long: true, Steps: []jobs.Step{
		{Name: "Start database containers", Do: func(ctx context.Context) error {
			var err error
			if site, err = d.State.Get(id); err != nil {
				return err
			}
			if site == nil {
				return errors.New("the agent does not know this website")
			}
			if site.UnixUser != unixUser {
				return fmt.Errorf("the website has the Unix user %s, not %s", site.UnixUser, unixUser)
			}
			current, _, err := d.State.WebsiteDatabases(id)
			if err != nil {
				return err
			}
			if current != "" && current != key {
				return fmt.Errorf("the website uses the database containers of the customer %s", current)
			}
			if moved = current == key; moved {
				return nil
			}
			// The record keeps the containers on the server during the copy.
			if _, err := d.useDatabases(key, id, true); err != nil {
				return err
			}
			for _, engine := range engines {
				if err := d.EnsureDatabaseService(ctx, next(engine)); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "Stop website", Do: func(ctx context.Context) error {
			if moved || site.Stopped || len(req.GetDatabases()) == 0 {
				return nil
			}
			u := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}
			stopped = true
			_, err := d.Host.Systemctl(ctx, site.UnixUser, "stop", u.ServiceName())
			return err
		}},
		{Name: "Copy databases", Do: func(ctx context.Context) error {
			if moved {
				return nil
			}
			for _, db := range req.GetDatabases() {
				if err := d.copyDatabase(ctx, old(db.GetEngine()), db.GetName(), next(db.GetEngine()), db.GetName()); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "Copy users", Do: func(ctx context.Context) error {
			if moved {
				return nil
			}
			for _, u := range req.GetUsers() {
				if err := d.copyUser(ctx, old(u.GetEngine()), next(u.GetEngine()), u); err != nil {
					return fmt.Errorf("copy the user %s: %w", u.GetName(), err)
				}
			}
			return nil
		}},
		{Name: "Start website", Do: func(ctx context.Context) error {
			// From this record, each action of the website uses the containers of its customer.
			if _, err := d.useDatabases(key, id, false); err != nil {
				return err
			}
			moved, stopped = true, false
			if err := d.syncDatabaseAccess(ctx); err != nil {
				return err
			}
			site.Website.DatabaseInstance = req.GetDatabaseInstance()
			if err := d.State.Put(site); err != nil {
				return err
			}
			// The new unit gives the database host names the address of the new containers.
			return d.restartWithUnit(ctx, site)
		}},
		{Name: "Remove old copies", Do: func(ctx context.Context) error {
			if err := d.removeMoved(ctx, req, old); err != nil {
				return err
			}
			// The shared containers stop when the last website of the server has moved.
			return d.retireSharedDatabases(ctx)
		}},
	}, OnFailure: func(ctx context.Context) {
		if moved || site == nil {
			return
		}
		// The job has failed already. The agent logs an error of the way back.
		if stopped {
			u := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}
			if _, err := d.Host.Systemctl(ctx, site.UnixUser, "start", u.ServiceName()); err != nil {
				slog.Error("start the website again with the shared database containers", "website", id, "err", err)
			}
		}
		// The website does not use the new containers. When a different website of the customer
		// uses them, the copies of this job go. If not, the containers go, with the copies.
		if instances, err := d.State.DatabaseInstances(); err == nil && len(instances[key].Websites) > 0 {
			if err := d.removeMoved(ctx, req, next); err != nil {
				slog.Error("remove the copies of a move that failed", "website", id, "err", err)
			}
		}
		if err := d.releaseDatabases(ctx, key, id); err != nil {
			slog.Error("remove the database containers of a move that failed", "website", id, "err", err)
		}
	}}, nil
}

// removeMoved removes the users and the databases of a move from the containers that server
// gives. It does not start a container that the server does not have.
func (d *Deps) removeMoved(ctx context.Context, req *agentv1.MoveDatabasesRequest, server func(engine string) dbServer) error {
	for _, u := range req.GetUsers() {
		if s := server(u.GetEngine()); d.serverExists(s) {
			if err := d.dropUser(ctx, s, u.GetName()); err != nil {
				return err
			}
		}
	}
	for _, db := range req.GetDatabases() {
		s := server(db.GetEngine())
		if !d.serverExists(s) {
			continue
		}
		if s.Engine == "mariadb" {
			if _, err := d.sql(ctx, s, mariaRevokeDatabase(db.GetName())); err != nil {
				return err
			}
		}
		if err := d.dropDatabase(ctx, s, db.GetName()); err != nil {
			return err
		}
	}
	return nil
}

// copyUser makes a database user of the container from in the container to, with the same
// password, and gives it the access of the request. The agent reads the hash of the password,
// not the password. A job that runs again makes the user again.
func (d *Deps) copyUser(ctx context.Context, from, to dbServer, u *agentv1.MoveDatabaseUser) error {
	name := u.GetName()
	host, err := d.mariaHost(to, u.GetAccess())
	if err != nil {
		return err
	}
	var create string
	if from.Engine == "mariadb" {
		out, err := d.sql(ctx, from, fmt.Sprintf("SELECT plugin, authentication_string FROM mysql.user WHERE User = '%s';\n", name))
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		f := strings.Split(lines[0], "\t")
		if len(lines) != 1 || len(f) != 2 || !mariaPluginRE.MatchString(f[0]) || !mariaHashRE.MatchString(f[1]) {
			return errors.New("the shared MariaDB container does not have one user with this name and a password")
		}
		create = fmt.Sprintf("CREATE USER '%s'@'%s' IDENTIFIED VIA %s USING '%s' %s;\n", name, host, f[0], f[1], mariaRequire(u.GetAccess()))
	} else {
		out, err := d.sql(ctx, from, fmt.Sprintf("SELECT rolpassword FROM pg_authid WHERE rolname = '%s';\n", name))
		if err != nil {
			return err
		}
		verifier := strings.TrimSpace(out)
		if !pgVerifierRE.MatchString(verifier) {
			return errors.New("the shared PostgreSQL container does not have a user with this name and a SCRAM-SHA-256 password")
		}
		// PostgreSQL keeps a password that is a SCRAM-SHA-256 verifier as it is.
		create = fmt.Sprintf("CREATE ROLE \"%s\" LOGIN PASSWORD '%s';\n", name, verifier)
	}
	if err := d.dropUser(ctx, to, name); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(create)
	for _, g := range u.GetGrants() {
		b.WriteString(grantSQL(to.Engine, name, host, g.GetDatabase(), g.GetPrivileges()))
	}
	_, err = d.sql(ctx, to, b.String())
	return err
}
