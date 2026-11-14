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

// existingServer gives the container of a database server ID from a request, when this
// server has the container. An empty ID is the shared container of an earlier release.
func (d *Deps) existingServer(id, engine string) (dbServer, bool) {
	key, err := serverKey(id)
	if err != nil {
		return dbServer{}, false
	}
	s := dbServer{ID: key, Engine: engine}
	if key != "" {
		if customer, record, _, err := d.State.DatabaseServer(key); err != nil || customer == "" || record.Engine != engine {
			return s, false
		}
	}
	return s, d.serverExists(s)
}

// MoveDatabases moves the databases and the database users of a website from the shared
// database containers of an earlier release to database servers of its customer. The panel
// sends it for each website of a server from an earlier release, with the database servers
// that get the databases.
//
// The website does not operate during the copy, so that the copy has all the data. The
// password of each user stays the same: the agent copies its hash. The host names in the
// website container stay the same too. The ports stay the same when the database server is
// the first one of its engine for the customer, so the website needs no new settings then.
//
// The website uses the database servers from the step "Start website". A failure before the
// end of that step starts the website again with the shared containers, and removes the new
// copies. That includes a website that answered before the move and does not answer with the
// database servers: the agent does the same for a new runtime that does not answer. After that
// step, the job only removes the old copies, and a job that runs again does only that.
func (d *Deps) MoveDatabases(req *agentv1.MoveDatabasesRequest) (jobs.Job, error) {
	id, unixUser := req.GetWebsiteId(), req.GetUnixUser()
	key, err := instanceKey(req.GetDatabaseInstance())
	errs := []error{validate.ID(id), validate.UnixUser(unixUser), err}
	if req.GetDatabaseInstance() == "" {
		errs = append(errs, errors.New("the request has no database instance"))
	}
	// The engine of each database server of the request.
	servers := map[string]string{}
	for _, s := range req.GetDatabaseServers() {
		errs = append(errs, checkDatabaseServer(s), sqlEngine(s.GetEngine()))
		if s.GetId() == "" || s.GetCustomer() != req.GetDatabaseInstance() {
			errs = append(errs, errors.New("each database server of the request is a database server of the customer of the website"))
		}
		servers[s.GetId()] = s.GetEngine()
	}
	target := func(what, name, engine, server string) error {
		if got, ok := servers[server]; !ok || server == "" {
			return fmt.Errorf("the request does not have the database server of the %s %s", what, name)
		} else if got != engine {
			return fmt.Errorf("the database server of the %s %s has the engine %s, not %s", what, name, got, engine)
		}
		return nil
	}
	// The database server of each database of the request.
	names := map[string]string{}
	for _, db := range req.GetDatabases() {
		errs = append(errs, sqlEngine(db.GetEngine()), validate.TenantDatabaseName(unixUser, db.GetName()), target("database", db.GetName(), db.GetEngine(), db.GetDatabaseServer()))
		names[db.GetEngine()+":"+db.GetName()] = db.GetDatabaseServer()
	}
	for _, u := range req.GetUsers() {
		errs = append(errs, sqlEngine(u.GetEngine()), validate.TenantDatabaseName(unixUser, u.GetName()), target("user", u.GetName(), u.GetEngine(), u.GetDatabaseServer()))
		if _, err := d.mariaHost(dbServer{}, u.GetAccess()); err != nil {
			errs = append(errs, err)
		}
		for _, g := range u.GetGrants() {
			if _, err := mariaPrivileges(g.GetPrivileges()); err != nil {
				errs = append(errs, err)
			}
			if server, ok := names[u.GetEngine()+":"+g.GetDatabase()]; !ok {
				errs = append(errs, fmt.Errorf("the database %s is not a database of the request", g.GetDatabase()))
			} else if server != u.GetDatabaseServer() {
				errs = append(errs, fmt.Errorf("the database %s and the user %s do not have the same database server", g.GetDatabase(), u.GetName()))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	old := func(engine, _ string) (dbServer, bool) {
		s := dbServer{Engine: engine}
		return s, d.serverExists(s)
	}
	// next gives the database server of the customer that gets a database or a user.
	next := func(engine, server string) (dbServer, bool) {
		s, err := d.serverOf(key, server, engine)
		return s, err == nil && d.serverExists(s)
	}

	var site *Site
	// moved is true when the website uses the database servers of its customer already: an
	// earlier run of this job came to the step "Start website".
	var moved bool
	// stopped is true while this job keeps the website stopped.
	var stopped bool
	// answered is true when the website answered before the job changed it. Then the website
	// must answer with the database servers too.
	var answered bool
	// switched is true when the record and the unit of the website can have the address of the
	// customer.
	var switched bool
	return jobs.Job{ID: req.GetJobId(), Key: id, Long: true, Steps: []jobs.Step{
		{Name: "Start database servers", Do: func(ctx context.Context) error {
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
				return fmt.Errorf("the website uses the database servers of the customer %s", current)
			}
			if moved = current == key; moved {
				return nil
			}
			for _, s := range req.GetDatabaseServers() {
				if _, err := d.ensureServer(ctx, s, false); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "Stop website", Do: func(ctx context.Context) error {
			if moved || site.Stopped {
				return nil
			}
			answered = d.ready(ctx, site.Port, primaryDomain(site.Website)) == nil
			if len(req.GetDatabases()) == 0 {
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
				to, err := d.serverOf(key, db.GetDatabaseServer(), db.GetEngine())
				if err != nil {
					return err
				}
				if err := d.copyDatabase(ctx, dbServer{Engine: db.GetEngine()}, db.GetName(), to, db.GetName()); err != nil {
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
				to, err := d.serverOf(key, u.GetDatabaseServer(), u.GetEngine())
				if err != nil {
					return err
				}
				if err := d.copyUser(ctx, dbServer{Engine: u.GetEngine()}, to, u); err != nil {
					return fmt.Errorf("copy the user %s: %w", u.GetName(), err)
				}
			}
			return nil
		}},
		{Name: "Start website", Do: func(ctx context.Context) error {
			// From this record, each action of the website uses the database servers of its
			// customer, and the firewall lets the website connect to them.
			switched = true
			if _, err := d.useDatabases(key, id); err != nil {
				return err
			}
			if err := d.syncDatabaseAccess(ctx); err != nil {
				return err
			}
			site.Website.DatabaseInstance = req.GetDatabaseInstance()
			if err := d.State.Put(site); err != nil {
				return err
			}
			// The new unit gives the database host names the address of the customer.
			if err := d.restartWithUnit(ctx, site); err != nil {
				if answered {
					return fmt.Errorf("the website answered with the shared database containers, and it does not answer with the database servers of its customer, so it gets the shared containers again: %w", err)
				}
				// The move is not the cause: the website did not answer before the move.
				slog.Warn("the website does not answer after the move of its databases, as before the move", "website", id, "err", err)
			}
			moved, stopped = true, false
			return nil
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
		if stopped && !switched {
			u := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}
			if _, err := d.Host.Systemctl(ctx, site.UnixUser, "start", u.ServiceName()); err != nil {
				slog.Error("start the website again with the shared database containers", "website", id, "err", err)
			}
		}
		// The website does not use the database servers. The copies of this job go. The
		// database servers stay: they are database servers of the customer in the panel.
		if err := d.removeMoved(ctx, req, next); err != nil {
			slog.Error("remove the copies of a move that failed", "website", id, "err", err)
		}
		if !switched {
			return
		}
		// The step "Start website" gave the website the address of its customer. Without the
		// record of the website, the unit gets the shared containers again.
		if err := d.releaseDatabases(key, id); err != nil {
			slog.Error("remove the record of a move that failed", "website", id, "err", err)
		}
		site.Website.DatabaseInstance = ""
		if err := errors.Join(d.State.Put(site), d.syncDatabaseAccess(ctx)); err != nil {
			slog.Error("give the website the shared database containers again", "website", id, "err", err)
		}
		if err := d.restartWithUnit(ctx, site); err != nil {
			slog.Error("start the website again with the shared database containers", "website", id, "err", err)
		}
	}}, nil
}

// removeMoved removes the users and the databases of a move from the containers that server
// gives for an engine and a database server ID of the request. It does not start a container
// that the server does not have.
func (d *Deps) removeMoved(ctx context.Context, req *agentv1.MoveDatabasesRequest, server func(engine, id string) (dbServer, bool)) error {
	for _, u := range req.GetUsers() {
		if s, ok := server(u.GetEngine(), u.GetDatabaseServer()); ok {
			if err := d.dropUser(ctx, s, u.GetName()); err != nil {
				return err
			}
		}
	}
	for _, db := range req.GetDatabases() {
		s, ok := server(db.GetEngine(), db.GetDatabaseServer())
		if !ok {
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
