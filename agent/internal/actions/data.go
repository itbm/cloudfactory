package actions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// DBUser is the Unix user of the database containers.
const DBUser = "cf-db"

var passwordRE = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)

// dbContainer checks an engine, and gives the name of its shared container of an earlier
// release. dbServer.container gives the name of the container of a database server.
func dbContainer(engine string) (string, error) {
	switch engine {
	case "mariadb", "postgresql", "valkey":
		return quadlet.DatabaseContainer(engine, ""), nil
	}
	return "", fmt.Errorf("the database engine %q is not valid", engine)
}

// sqlEngine is dbContainer for the actions that use SQL or a dump. Valkey has neither.
func sqlEngine(engine string) error {
	if engine == "valkey" {
		return errValkeyNoCopy
	}
	_, err := dbContainer(engine)
	return err
}

// sql sends statements on stdin to the database client in a database container. It starts the
// container first if it does not operate.
//
// The agent starts a database container one time and then records that it answered. When
// statements fail, the container possibly stopped after that, for example because the server
// had no memory for it. Then the agent forgets the record, so that the next call starts the
// container again and waits for it. Without this, each later job would fail until the agent
// starts again.
func (d *Deps) sql(ctx context.Context, s dbServer, statements string) (string, error) {
	if err := d.EnsureDatabaseService(ctx, s); err != nil {
		return "", err
	}
	out, err := d.sqlNow(ctx, s, statements)
	if err != nil && ctx.Err() == nil && d.databaseListens(ctx, s) != nil {
		d.dbMu.Lock()
		delete(d.dbReady, s)
		d.dbMu.Unlock()
	}
	return out, err
}

// sqlNow is sql with no start of the container.
func (d *Deps) sqlNow(ctx context.Context, s dbServer, statements string) (string, error) {
	engine := s.Engine
	if engine == "valkey" {
		return "", errors.New("Valkey has no SQL")
	}
	if _, err := dbContainer(engine); err != nil {
		return "", err
	}
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return "", err
	}
	args := []string{"-u", DBUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", "-i", s.container()}
	if engine == "mariadb" {
		args = append(args, "mariadb", "--user=root", "--batch", "--skip-column-names")
	} else {
		args = append(args, "psql", "--username=postgres", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align")
	}
	return d.Run.RunInput(ctx, statements, "runuser", args...)
}

// mariaHost is the host part of a MariaDB user (CF-19). "website" users can connect only from
// the address that the database server sees for the website containers. "anywhere" users can
// connect from any address, with TLS.
//
// A database server of a customer sees quadlet.DatabaseClientAddress for each website
// container. The shared container of an earlier release sees quadlet.ServiceAddress.
func (d *Deps) mariaHost(s dbServer, access string) (string, error) {
	switch access {
	case "", "website":
		host := quadlet.DatabaseClientAddress
		if s.ID == "" {
			host = d.DatabaseClientHost
			if host == "" {
				host = quadlet.ServiceAddress
			}
		}
		if !clientHostRE.MatchString(host) {
			return "", fmt.Errorf("the database client host %q is not valid", host)
		}
		return host, nil
	case "anywhere":
		return "%", nil
	}
	return "", fmt.Errorf("the database access %q is not valid", access)
}

var clientHostRE = regexp.MustCompile(`^[A-Za-z0-9.:%/_-]{1,255}$`)

// mariaDB gives a database name for GRANT. In GRANT, "_" and "%" are patterns, so the agent
// escapes "_". Then a grant on "acme_x7k2_shop" never matches a different database.
func mariaDB(name string) string { return "`" + strings.ReplaceAll(name, "_", `\_`) + "`" }

// The maximum number of connections of one database user at the same time. A database
// server accepts 151 connections (MariaDB) or 100 connections (PostgreSQL, with 3 of them for
// the superuser). With a limit for each user, the application of one website cannot use all
// connections of its database server: the other websites of the customer and the agent can
// connect.
const (
	mariaUserConnections = 100
	pgUserConnections    = 80
)

// mariaRequire gives the options of a MariaDB user: TLS for a user that can connect from any
// address, and the connection limit.
func mariaRequire(access string) string {
	require := "REQUIRE NONE"
	if access == "anywhere" {
		require = "REQUIRE SSL"
	}
	return fmt.Sprintf("%s WITH MAX_USER_CONNECTIONS %d", require, mariaUserConnections)
}

// pgHarden is the SQL that the agent sends to a PostgreSQL server each time it starts the
// server. By default, each user can connect to the databases "postgres" and "template1",
// and read the names of all databases and all users there. The users of the websites get no
// access to these two databases. A user connects only to its own databases
// (pgCreateDatabase). The superuser of the agent can always connect.
const pgHarden = "REVOKE CONNECT ON DATABASE postgres, template1 FROM PUBLIC;\n"

// mariaPrivileges gives the MariaDB privileges for "readonly" and "readwrite".
func mariaPrivileges(p string) (string, error) {
	switch p {
	case "readonly":
		return "SELECT, SHOW VIEW", nil
	case "readwrite":
		return "ALL PRIVILEGES", nil
	}
	return "", fmt.Errorf("the database privileges %q are not valid", p)
}

// pgRole gives the group role of a PostgreSQL database for "readonly" or "readwrite". The
// read-write role owns the database. A read-write user takes this role when it connects, so that
// all objects have one owner. The ":" cannot be in a user name, so a user cannot have these names.
func pgRole(database, privileges string) string {
	if privileges == "readonly" {
		return database + ":ro"
	}
	return database + ":rw"
}

// exists runs a query that gives one row for each object, and reports whether it gave a row.
func (d *Deps) exists(ctx context.Context, s dbServer, query string) (bool, error) {
	out, err := d.sql(ctx, s, query)
	return strings.TrimSpace(out) != "", err
}

// CreateDatabase makes a database, and optionally a user with read-write access to it (FR-501,
// FR-502). The names are checked, so they are safe in SQL. The password has only letters and
// digits. CF-01: each name starts with the Unix user of the website, and the job fails when the
// database or the user exists.
func (d *Deps) CreateDatabase(req *agentv1.CreateDatabaseRequest) (jobs.Job, error) {
	unixUser, name, user, pass, engine := req.GetUnixUser(), req.GetName(), req.GetUser(), req.GetPassword(), req.GetEngine()
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.TenantDatabaseName(unixUser, name)); err != nil {
		return jobs.Job{}, err
	}
	if _, err := dbContainer(engine); err != nil {
		return jobs.Job{}, err
	}
	if user != "" {
		if err := validate.TenantDatabaseName(unixUser, user); err != nil {
			return jobs.Job{}, err
		}
		if !passwordRE.MatchString(pass) {
			return jobs.Job{}, errors.New("the database password must have 16 to 64 letters and digits")
		}
	}
	if _, err := d.mariaHost(dbServer{}, req.GetAccess()); err != nil {
		return jobs.Job{}, err
	}
	if err := checkWebsiteServer(req.GetDatabaseServer(), engine); err != nil {
		return jobs.Job{}, err
	}
	if engine == "valkey" {
		return d.createValkeyDatabase(req)
	}
	maria := engine == "mariadb"
	steps := []jobs.Step{
		{Name: "Create database", Do: func(ctx context.Context) error {
			srv, err := d.websiteServer(ctx, req.GetWebsiteId(), req.GetDatabaseServer(), engine)
			if err != nil {
				return err
			}
			if maria {
				// No "IF NOT EXISTS": the job fails when the database exists.
				_, err := d.sql(ctx, srv, fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name))
				return err
			}
			found, err := d.exists(ctx, srv, fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%[1]s' UNION ALL SELECT 1 FROM pg_roles WHERE rolname IN ('%[1]s:rw', '%[1]s:ro');\n", name))
			if err != nil {
				return err
			}
			if found {
				return fmt.Errorf("the database %s exists", name)
			}
			_, err = d.sql(ctx, srv, pgCreateDatabase(name))
			return err
		}},
	}
	if user != "" {
		steps = append(steps,
			jobs.Step{Name: "Create user", Do: func(ctx context.Context) error {
				srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer().GetId(), engine)
				if err != nil {
					return err
				}
				host, err := d.mariaHost(srv, req.GetAccess())
				if err != nil {
					return err
				}
				return d.createUser(ctx, srv, user, pass, host, req.GetAccess())
			}},
			jobs.Step{Name: "Allow the website container only", Do: func(ctx context.Context) error {
				srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer().GetId(), engine)
				if err != nil {
					return err
				}
				host, err := d.mariaHost(srv, req.GetAccess())
				if err != nil {
					return err
				}
				_, err = d.sql(ctx, srv, grantSQL(engine, user, host, name, "readwrite"))
				return err
			}},
		)
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: steps}, nil
}

// createValkeyDatabase makes a Valkey database in a Valkey database server of the customer of
// the website. It always has one user with the same name: the user is the record of the
// database number. Only the website containers can connect. The result of the job has the
// number.
func (d *Deps) createValkeyDatabase(req *agentv1.CreateDatabaseRequest) (jobs.Job, error) {
	name := req.GetName()
	if req.GetUser() != name {
		return jobs.Job{}, errors.New("a Valkey database must have a user with the same name")
	}
	if a := req.GetAccess(); a != "" && a != "website" {
		return jobs.Job{}, errors.New("only the website containers can connect to Valkey")
	}
	var number int
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Create database and user", Do: func(ctx context.Context) error {
			srv, err := d.websiteServer(ctx, req.GetWebsiteId(), req.GetDatabaseServer(), "valkey")
			if err != nil {
				return err
			}
			n, err := d.valkeyCreate(ctx, srv, name, req.GetUnixUser()+"_", req.GetPassword())
			number = n
			return err
		}},
	}, Result: func() map[string]any { return map[string]any{"number": number} }}, nil
}

// pgCreateDatabase makes the database and its two group roles. Only the roles can connect.
func pgCreateDatabase(name string) string {
	return fmt.Sprintf(`CREATE ROLE "%[1]s:rw" NOLOGIN;
CREATE ROLE "%[1]s:ro" NOLOGIN;
CREATE DATABASE "%[1]s" OWNER "%[1]s:rw";
REVOKE ALL ON DATABASE "%[1]s" FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE "%[1]s" TO "%[1]s:ro";
\connect "%[1]s"
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO "%[1]s:ro";
ALTER DEFAULT PRIVILEGES FOR ROLE "%[1]s:rw" IN SCHEMA public GRANT SELECT ON TABLES TO "%[1]s:ro";
ALTER DEFAULT PRIVILEGES FOR ROLE "%[1]s:rw" IN SCHEMA public GRANT SELECT ON SEQUENCES TO "%[1]s:ro";
`, name)
}

// createUser makes a user. It fails when a user with the name exists, with any host.
func (d *Deps) createUser(ctx context.Context, s dbServer, user, pass, host, access string) error {
	if s.Engine == "mariadb" {
		found, err := d.exists(ctx, s, fmt.Sprintf("SELECT 1 FROM mysql.user WHERE User = '%s';\n", user))
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("the database user %s exists", user)
		}
		_, err = d.sql(ctx, s, fmt.Sprintf("CREATE USER '%s'@'%s' IDENTIFIED BY '%s' %s;\n", user, host, pass, mariaRequire(access)))
		return err
	}
	found, err := d.exists(ctx, s, fmt.Sprintf("SELECT 1 FROM pg_roles WHERE rolname = '%s';\n", user))
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("the database user %s exists", user)
	}
	_, err = d.sql(ctx, s, fmt.Sprintf("CREATE ROLE \"%s\" LOGIN CONNECTION LIMIT %d PASSWORD '%s';\n", user, pgUserConnections, pass))
	return err
}

// grantSQL gives one database to a user with "readonly" or "readwrite" privileges.
func grantSQL(engine, user, host, database, privileges string) string {
	if engine == "mariadb" {
		p, _ := mariaPrivileges(privileges)
		return fmt.Sprintf("GRANT %s ON %s.* TO '%s'@'%s';\n", p, mariaDB(database), user, host)
	}
	if privileges == "readwrite" {
		return fmt.Sprintf("GRANT \"%[1]s\" TO \"%[2]s\";\nALTER ROLE \"%[2]s\" IN DATABASE \"%[3]s\" SET role = '%[1]s';\n", pgRole(database, privileges), user, database)
	}
	return fmt.Sprintf("GRANT \"%[1]s\" TO \"%[2]s\";\n", pgRole(database, privileges), user)
}

// pgRevokeSQL removes the access of a PostgreSQL user to one database.
func pgRevokeSQL(user, database string) string {
	return fmt.Sprintf("REVOKE \"%[2]s:rw\" FROM \"%[1]s\";\nREVOKE \"%[2]s:ro\" FROM \"%[1]s\";\nALTER ROLE \"%[1]s\" IN DATABASE \"%[2]s\" RESET role;\n", user, database)
}

// mariaCurrentHost gives the host part of a MariaDB user.
func (d *Deps) mariaCurrentHost(ctx context.Context, s dbServer, user string) (string, error) {
	out, err := d.sql(ctx, s, fmt.Sprintf("SELECT Host FROM mysql.user WHERE User = '%s';\n", user))
	if err != nil {
		return "", err
	}
	hosts := strings.Fields(out)
	if len(hosts) != 1 || !clientHostRE.MatchString(hosts[0]) {
		return "", fmt.Errorf("the database user %s has %d hosts, not 1", user, len(hosts))
	}
	return hosts[0], nil
}

// SetDatabaseUser sets the full state of a database user: its password, where it can connect
// from, and its access to each database of the website in its database server (FR-502). A
// user can read one database and change a different database, for example.
func (d *Deps) SetDatabaseUser(req *agentv1.SetDatabaseUserRequest) (jobs.Job, error) {
	unixUser, user, engine := req.GetUnixUser(), req.GetName(), req.GetEngine()
	errs := []error{validate.ID(req.GetWebsiteId()), validate.TenantDatabaseName(unixUser, user)}
	if _, err := dbContainer(engine); err != nil {
		errs = append(errs, err)
	}
	all := map[string]bool{}
	for _, db := range req.GetWebsiteDatabases() {
		errs = append(errs, validate.TenantDatabaseName(unixUser, db))
		all[db] = true
	}
	for _, g := range req.GetGrants() {
		errs = append(errs, validate.TenantDatabaseName(unixUser, g.GetDatabase()))
		if _, err := mariaPrivileges(g.GetPrivileges()); err != nil {
			errs = append(errs, err)
		}
		if !all[g.GetDatabase()] {
			errs = append(errs, fmt.Errorf("the database %s is not a database of the website", g.GetDatabase()))
		}
	}
	pass := req.GetPassword()
	if (pass != "" || req.GetCreate()) && !passwordRE.MatchString(pass) {
		errs = append(errs, errors.New("the database password must have 16 to 64 letters and digits"))
	}
	_, err := d.mariaHost(dbServer{}, req.GetAccess())
	errs = append(errs, err, checkWebsiteServer(req.GetDatabaseServer(), engine))
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	// server gives the container of the user, and the host part of the user.
	server := func(ctx context.Context) (dbServer, string, error) {
		srv, err := d.websiteServer(ctx, req.GetWebsiteId(), req.GetDatabaseServer(), engine)
		if err != nil {
			return srv, "", err
		}
		host, err := d.mariaHost(srv, req.GetAccess())
		return srv, host, err
	}
	if engine == "valkey" {
		// The user of a Valkey database comes and goes with its database, and has access to
		// that database only. Thus only its password can change.
		if req.GetCreate() || (req.GetAccess() != "" && req.GetAccess() != "website") {
			return jobs.Job{}, errors.New("a Valkey database has one user, and only the website containers can connect")
		}
		return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
			{Name: "Set password", Do: func(ctx context.Context) error {
				if pass == "" {
					return nil
				}
				srv, _, err := server(ctx)
				if err != nil {
					return err
				}
				return d.valkeySetPassword(ctx, srv, user, pass)
			}},
		}}, nil
	}
	maria := engine == "mariadb"
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Set user and password", Do: func(ctx context.Context) error {
			srv, host, err := server(ctx)
			if err != nil {
				return err
			}
			if req.GetCreate() {
				return d.createUser(ctx, srv, user, pass, host, req.GetAccess())
			}
			if !maria {
				// A user from before the connection limit gets the limit with each change.
				statement := fmt.Sprintf("ALTER ROLE \"%s\" WITH LOGIN CONNECTION LIMIT %d", user, pgUserConnections)
				if pass != "" {
					statement += fmt.Sprintf(" PASSWORD '%s'", pass)
				}
				_, err := d.sql(ctx, srv, statement+";\n")
				return err
			}
			old, err := d.mariaCurrentHost(ctx, srv, user)
			if err != nil {
				return err
			}
			var b strings.Builder
			if old != host {
				fmt.Fprintf(&b, "RENAME USER '%s'@'%s' TO '%s'@'%s';\n", user, old, user, host)
			}
			fmt.Fprintf(&b, "ALTER USER '%s'@'%s' %s;\n", user, host, mariaRequire(req.GetAccess()))
			if pass != "" {
				fmt.Fprintf(&b, "ALTER USER '%s'@'%s' IDENTIFIED BY '%s';\n", user, host, pass)
			}
			_, err = d.sql(ctx, srv, b.String())
			return err
		}},
		{Name: "Set access to databases", Do: func(ctx context.Context) error {
			srv, host, err := server(ctx)
			if err != nil {
				return err
			}
			var b strings.Builder
			if maria {
				// Remove all grants, then give the grants of the request.
				fmt.Fprintf(&b, "REVOKE ALL PRIVILEGES, GRANT OPTION FROM '%s'@'%s';\n", user, host)
			} else {
				for _, db := range req.GetWebsiteDatabases() {
					b.WriteString(pgRevokeSQL(user, db))
				}
			}
			for _, g := range req.GetGrants() {
				b.WriteString(grantSQL(engine, user, host, g.GetDatabase(), g.GetPrivileges()))
			}
			_, err = d.sql(ctx, srv, b.String())
			return err
		}},
	}}, nil
}

// DeleteDatabaseUser removes a database user.
func (d *Deps) DeleteDatabaseUser(req *agentv1.DeleteDatabaseUserRequest) (jobs.Job, error) {
	unixUser, user, engine := req.GetUnixUser(), req.GetName(), req.GetEngine()
	_, serverErr := serverKey(req.GetDatabaseServer())
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.TenantDatabaseName(unixUser, user), serverErr); err != nil {
		return jobs.Job{}, err
	}
	if _, err := dbContainer(engine); err != nil {
		return jobs.Job{}, err
	}
	if engine == "valkey" {
		return jobs.Job{}, errors.New("the user of a Valkey database goes with its database; remove the database")
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Remove user", Do: func(ctx context.Context) error {
			srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer(), engine)
			if err != nil {
				return err
			}
			return d.dropUser(ctx, srv, user)
		}},
	}}, nil
}

func (d *Deps) dropUser(ctx context.Context, s dbServer, user string) error {
	engine := s.Engine
	if engine == "valkey" {
		// dropDatabase removes the user of a Valkey database.
		return nil
	}
	if engine == "mariadb" {
		out, err := d.sql(ctx, s, fmt.Sprintf("SELECT Host FROM mysql.user WHERE User = '%s';\n", user))
		if err != nil {
			return err
		}
		var b strings.Builder
		for _, h := range strings.Fields(out) {
			if clientHostRE.MatchString(h) {
				fmt.Fprintf(&b, "DROP USER IF EXISTS '%s'@'%s';\n", user, h)
			}
		}
		if b.Len() == 0 {
			return nil
		}
		_, err = d.sql(ctx, s, b.String())
		return err
	}
	// The group roles own all objects, so the user owns nothing. DROP ROLE also removes the
	// memberships and the settings of the user.
	_, err := d.sql(ctx, s, fmt.Sprintf("DROP ROLE IF EXISTS \"%s\";\n", user))
	return err
}

// DeleteDatabase removes the access of all users to a database, then the database.
func (d *Deps) DeleteDatabase(req *agentv1.DeleteDatabaseRequest) (jobs.Job, error) {
	unixUser, name, engine := req.GetUnixUser(), req.GetName(), req.GetEngine()
	_, serverErr := serverKey(req.GetDatabaseServer())
	errs := []error{validate.ID(req.GetWebsiteId()), validate.TenantDatabaseName(unixUser, name), serverErr}
	for _, u := range req.GetUsers() {
		errs = append(errs, validate.TenantDatabaseName(unixUser, u))
	}
	if _, err := dbContainer(engine); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	if engine == "valkey" {
		return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
			{Name: "Remove database and user", Do: func(ctx context.Context) error {
				srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer(), engine)
				if err != nil {
					return err
				}
				return d.dropDatabase(ctx, srv, name)
			}},
		}}, nil
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Remove access of users", Do: func(ctx context.Context) error {
			srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer(), engine)
			if err != nil {
				return err
			}
			if engine == "mariadb" {
				_, err := d.sql(ctx, srv, mariaRevokeDatabase(name))
				return err
			}
			var b strings.Builder
			for _, u := range req.GetUsers() {
				b.WriteString(pgRevokeSQL(u, name))
			}
			if b.Len() == 0 {
				return nil
			}
			_, err = d.sql(ctx, srv, b.String())
			return err
		}},
		{Name: "Remove database", Do: func(ctx context.Context) error {
			srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer(), engine)
			if err != nil {
				return err
			}
			return d.dropDatabase(ctx, srv, name)
		}},
	}}, nil
}

// mariaRevokeDatabase removes all grants on one database, for all users. MariaDB keeps the
// grants of a database that it drops, so a later database with the same name would get them.
func mariaRevokeDatabase(name string) string {
	escaped := strings.ReplaceAll(name, "_", `\\_`)
	var b strings.Builder
	for _, table := range []string{"db", "tables_priv", "columns_priv", "procs_priv"} {
		fmt.Fprintf(&b, "DELETE FROM mysql.%s WHERE Db IN ('%s', '%s');\n", table, name, escaped)
	}
	b.WriteString("FLUSH PRIVILEGES;\n")
	return b.String()
}

func (d *Deps) dropDatabase(ctx context.Context, s dbServer, name string) error {
	if s.Engine == "valkey" {
		return d.valkeyDelete(ctx, s, name)
	}
	if s.Engine == "mariadb" {
		_, err := d.sql(ctx, s, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\n", name))
		return err
	}
	_, err := d.sql(ctx, s, fmt.Sprintf("DROP DATABASE IF EXISTS \"%[1]s\" WITH (FORCE);\nDROP ROLE IF EXISTS \"%[1]s:rw\";\nDROP ROLE IF EXISTS \"%[1]s:ro\";\n", name))
	return err
}

var targetRE = regexp.MustCompile(`^(s3|b2|azure|gs):\S+$`)

// resticEnv are the environment variables of restic that the panel can set for the object
// storage. The agent refuses all other names, so that the panel cannot change how restic runs.
var resticEnv = map[string]bool{
	// The password of the repository. The panel keeps it, so that all servers use the same
	// repository with the same password.
	"RESTIC_PASSWORD":   true,
	"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_DEFAULT_REGION": true,
	"B2_ACCOUNT_ID": true, "B2_ACCOUNT_KEY": true,
	"AZURE_ACCOUNT_NAME": true, "AZURE_ACCOUNT_KEY": true, "AZURE_ACCOUNT_SAS": true,
	"GOOGLE_PROJECT_ID": true, "GOOGLE_ACCESS_TOKEN": true,
}

// checkCredentials checks the names and the values of the object storage credentials.
func checkCredentials(creds map[string]string) error {
	for k, v := range creds {
		if !resticEnv[k] {
			return fmt.Errorf("the backup credential %q is not allowed", k)
		}
		if len(v) > 4096 || strings.ContainsAny(v, "\x00\n\r") {
			return fmt.Errorf("the backup credential %s is not valid", k)
		}
	}
	return nil
}

// repo is a restic repository and the credentials of its object storage.
type repo struct {
	target string
	creds  map[string]string
}

func newRepo(target string, creds map[string]string) (repo, error) {
	// "s3://bucket" is a common mistake for "s3:bucket". Change only the "://" after the type,
	// not the one in "s3:https://host/bucket".
	if kind, rest, ok := strings.Cut(target, "://"); ok && !strings.Contains(kind, ":") {
		target = kind + ":" + rest
	}
	if !targetRE.MatchString(target) {
		return repo{}, errors.New("no valid backup target is set; an administrator sets it in Backups")
	}
	if err := checkCredentials(creds); err != nil {
		return repo{}, err
	}
	return repo{target: target, creds: creds}, nil
}

// restic runs restic on the repository. The credentials go in the environment, not in the
// arguments. restic waits for a lock of a different job, for example a prune.
func (d *Deps) restic(ctx context.Context, r repo, args ...string) (string, error) {
	base := []string{"--repo", r.target}
	if r.creds["RESTIC_PASSWORD"] == "" {
		// The panel sends the password of the repository of this server (RESTIC_PASSWORD).
		// A local key file is only for a manual set-up.
		base = append(base, "--password-file", d.ResticPasswordFile)
	}
	base = append(base, "--json", "--retry-lock", "30m")
	env := make([]string, 0, len(r.creds))
	for _, k := range slices.Sorted(maps.Keys(r.creds)) {
		env = append(env, k+"="+r.creds[k])
	}
	return d.Run.RunEnv(ctx, env, "", "restic", append(base, args...)...)
}

// ensureRepo makes the repository when it does not exist, for example in a new bucket.
// restic gives exit code 10 when there is no repository at the target.
func (d *Deps) ensureRepo(ctx context.Context, r repo) error {
	_, err := d.restic(ctx, r, "cat", "config")
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "exit status 10") && !strings.Contains(err.Error(), "repository does not exist") {
		return err
	}
	_, err = d.restic(ctx, r, "init")
	return err
}

// forget removes the snapshots of a website that the retention policy does not keep
// (FR-902). It gives the IDs of the removed snapshots. The data stays in the repository until
// PruneBackups.
//
// Audit finding R8: the policy is for the snapshots of one kind. The retention of the
// scheduled backups thus does not remove the backups that a user made.
func (d *Deps) forget(ctx context.Context, r repo, websiteID, kind string, keep *agentv1.Retention) ([]string, error) {
	if keep.GetDaily() <= 0 && keep.GetWeekly() <= 0 && keep.GetMonthly() <= 0 && keep.GetLast() <= 0 {
		return nil, nil
	}
	args := []string{"forget", "--tag", "website=" + websiteID + ",kind=" + kind, "--group-by", ""}
	if keep.GetLast() > 0 {
		args = append(args, "--keep-last", fmt.Sprint(keep.GetLast()))
	}
	if keep.GetDaily() > 0 || keep.GetWeekly() > 0 || keep.GetMonthly() > 0 {
		args = append(args, "--keep-daily", fmt.Sprint(keep.GetDaily()), "--keep-weekly", fmt.Sprint(keep.GetWeekly()), "--keep-monthly", fmt.Sprint(keep.GetMonthly()))
	}
	out, err := d.restic(ctx, r, args...)
	if err != nil {
		return nil, err
	}
	var groups []struct {
		Remove []struct {
			ID string `json:"id"`
		} `json:"remove"`
	}
	if err := json.Unmarshal([]byte(lastJSONLine(out)), &groups); err != nil {
		return nil, fmt.Errorf("read the output of restic forget: %w", err)
	}
	ids := []string{}
	for _, g := range groups {
		for _, s := range g.Remove {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// forgetWebsite removes all snapshots of a website that the panel deleted (audit finding R8).
func (d *Deps) forgetWebsite(ctx context.Context, r repo, websiteID string) error {
	out, err := d.restic(ctx, r, "snapshots", "--tag", "website="+websiteID)
	if err != nil {
		return err
	}
	var snapshots []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(lastJSONLine(out)), &snapshots); err != nil {
		return fmt.Errorf("read the output of restic snapshots: %w", err)
	}
	ids := []string{}
	for _, s := range snapshots {
		if snapshotID.MatchString(s.ID) {
			ids = append(ids, s.ID)
		}
	}
	// Some snapshots for each command, so that the command line stays short.
	for chunk := range slices.Chunk(ids, 100) {
		if _, err := d.restic(ctx, r, append([]string{"forget"}, chunk...)...); err != nil {
			return err
		}
	}
	return nil
}

var snapshotID = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// lastJSONLine gives the last line of the output that is not empty.
func lastJSONLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}

// PruneBackups removes the data that no snapshot uses from the repository (FR-902). The panel
// sends it to one server after the nightly backups. Before that, the agent removes the
// snapshots of the websites that the panel deleted some time ago (audit finding R8).
func (d *Deps) PruneBackups(req *agentv1.PruneBackupsRequest) (jobs.Job, error) {
	r, err := newRepo(req.GetTarget(), req.GetCredentials())
	if err != nil {
		return jobs.Job{}, err
	}
	for _, id := range req.GetForgetWebsiteIds() {
		if err := validate.ID(id); err != nil {
			return jobs.Job{}, err
		}
	}
	return jobs.Job{ID: req.GetJobId(), Key: "backup-repository", Long: true, Steps: []jobs.Step{
		{Name: "Remove backups of deleted websites", Do: func(ctx context.Context) error {
			if len(req.GetForgetWebsiteIds()) == 0 {
				return nil
			}
			if err := d.ensureRepo(ctx, r); err != nil {
				return err
			}
			for _, id := range req.GetForgetWebsiteIds() {
				if err := d.forgetWebsite(ctx, r, id); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "Remove unused data", Do: func(ctx context.Context) error {
			if err := d.ensureRepo(ctx, r); err != nil {
				return err
			}
			_, err := d.restic(ctx, r, "prune")
			return err
		}},
	}}, nil
}

// BackupSlots is the number of backups that dump or upload at the same time. The panel starts
// the nightly backups of all websites together, and each one takes memory and disk I/O.
const BackupSlots = 2

// backupSlot waits for a free backup slot. The caller calls release when the step is done.
func (d *Deps) backupSlot(ctx context.Context) (release func(), err error) {
	d.dbMu.Lock()
	if d.backupSlots == nil {
		d.backupSlots = make(chan struct{}, BackupSlots)
	}
	slots := d.backupSlots
	d.dbMu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dumpDir holds the database dumps of one website. Only root can read it, and the website
// cannot change it (CF-06). Each backup snapshot includes it.
func (d *Deps) dumpDir(unixUser string) string { return filepath.Join(d.DataDir, "dumps", unixUser) }

func dumpFile(engine, name string) string { return engine + "-" + name + ".dump" }

// Backup makes an encrypted, incremental snapshot of the files and databases of a website (FR-901).
// The result has the SHA-256 hash of each dump. The panel keeps it, and a restore checks it.
func (d *Deps) Backup(req *agentv1.BackupRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.UnixUser(req.GetUnixUser())); err != nil {
		return jobs.Job{}, err
	}
	r, err := newRepo(req.GetTarget(), req.GetCredentials())
	if err != nil {
		return jobs.Job{}, err
	}
	for _, db := range req.GetDatabases() {
		if err := sqlEngine(db.GetEngine()); err != nil {
			return jobs.Job{}, err
		}
		if err := validate.TenantDatabaseName(req.GetUnixUser(), db.GetName()); err != nil {
			return jobs.Job{}, err
		}
		if _, err := serverKey(db.GetDatabaseServer()); err != nil {
			return jobs.Job{}, err
		}
	}
	// The kind is a tag of the snapshot, and the retention policy uses it.
	if k := req.GetKind(); k != "manual" && k != "scheduled" {
		return jobs.Job{}, fmt.Errorf("the backup kind %q is not valid", k)
	}
	unixUser := req.GetUnixUser()
	app := d.Host.AppDir(unixUser)
	dumps := d.dumpDir(unixUser)
	// Audit finding R8: the dumps are plain text. They stay on the disk only until the upload
	// is complete or the job fails.
	removeDumps := func(ctx context.Context) { _ = d.removeDumps(context.WithoutCancel(ctx), unixUser) }
	hashes := map[string]any{}
	var snapshotID string
	var size float64
	forgotten := []string{}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Long: true, Steps: []jobs.Step{
		{Name: "Snapshot files", Do: func(ctx context.Context) error {
			_, err := d.appExists(ctx, unixUser)
			return err
		}},
		{Name: "Dump databases", Do: func(ctx context.Context) error {
			release, err := d.backupSlot(ctx)
			if err != nil {
				return err
			}
			defer release()
			// A new directory for each backup, so that the snapshot has no old dumps.
			if err := d.resetDumps(ctx, unixUser); err != nil {
				return err
			}
			for _, db := range req.GetDatabases() {
				srv, err := d.server(req.GetWebsiteId(), db.GetDatabaseServer(), db.GetEngine())
				if err != nil {
					return err
				}
				sum, err := d.dumpToFile(ctx, srv, unixUser, db.GetName())
				if err != nil {
					return err
				}
				hashes[db.GetEngine()+":"+db.GetName()] = sum
			}
			return nil
		}},
		{Name: "Upload to object storage", Do: func(ctx context.Context) error {
			release, err := d.backupSlot(ctx)
			if err != nil {
				return err
			}
			defer release()
			defer removeDumps(ctx)
			if err := d.ensureRepo(ctx, r); err != nil {
				return err
			}
			out, err := d.restic(ctx, r, "backup", app, dumps, "--tag", "website="+req.GetWebsiteId(), "--tag", "kind="+req.GetKind())
			if err != nil {
				return err
			}
			snapshotID, size = parseResticSummary(out)
			if snapshotID == "" {
				return errors.New("restic did not give a snapshot ID")
			}
			return nil
		}},
		{Name: "Apply retention", Do: func(ctx context.Context) error {
			if req.GetRetention() == nil {
				return nil
			}
			ids, err := d.forget(ctx, r, req.GetWebsiteId(), req.GetKind(), req.GetRetention())
			forgotten = ids
			return err
		}},
	}, OnFailure: removeDumps, Result: func() map[string]any {
		return map[string]any{"snapshotId": snapshotID, "sizeBytes": int64(size), "dumps": hashes, "forgotten": forgotten}
	}}, nil
}

// dumpCommand gives the command of a dump that a database user can restore: no owners and no
// privileges. The PostgreSQL dump uses the custom format, which pg_restore reads. It has no
// psql commands. The command writes the dump to its stdout.
func (d *Deps) dumpCommand(ctx context.Context, s dbServer, name string) ([]string, error) {
	if err := d.EnsureDatabaseService(ctx, s); err != nil {
		return nil, err
	}
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return nil, err
	}
	args := []string{"runuser", "-u", DBUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", s.container()}
	if s.Engine == "mariadb" {
		args = append(args, "mariadb-dump", "--user=root", "--single-transaction", "--routines", name)
	} else {
		args = append(args, "pg_dump", "--username=postgres", "--format=custom", "--no-owner", "--no-privileges", name)
	}
	return args, nil
}

// dumpToFile writes the dump of a database of a website to its file in the dump directory of
// the website, and gives the SHA-256 hash of the dump.
func (d *Deps) dumpToFile(ctx context.Context, s dbServer, unixUser, name string) (string, error) {
	args, err := d.dumpCommand(ctx, s, name)
	if err != nil {
		return "", err
	}
	return d.writeDump(ctx, unixUser, s.Engine, name, args)
}

// resetDumps makes the dump directory of a website new and empty. Only root can read it.
func (d *Deps) resetDumps(ctx context.Context, unixUser string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDumpsReset, userIn{User: unixUser}, nil)
	}
	dumps := d.dumpDir(unixUser)
	if err := os.RemoveAll(dumps); err != nil {
		return err
	}
	return os.MkdirAll(dumps, 0o700)
}

// removeDumps removes the dump directory of a website.
func (d *Deps) removeDumps(ctx context.Context, unixUser string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDumpsRemove, userIn{User: unixUser}, nil)
	}
	return os.RemoveAll(d.dumpDir(unixUser))
}

// writeDump runs a dump command and writes its output to the dump file of a database. It
// gives the SHA-256 hash of the dump. The dump goes from the client to the file, so that a
// large database does not stay in the memory of the agent (audit batch 2, finding 6).
//
// The root helper makes the path of the file. It runs the command only when its policy
// accepts the command.
func (d *Deps) writeDump(ctx context.Context, unixUser, engine, name string, argv []string) (sum string, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opDumpsWrite, dumpIn{User: unixUser, Engine: engine, Name: name, Argv: argv}, &sum)
		return sum, err
	}
	f, err := os.OpenFile(filepath.Join(d.dumpDir(unixUser), dumpFile(engine, name)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = d.Run.Stream(ctx, nil, io.MultiWriter(f, h), argv[0], argv[1:]...)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileHash gives the SHA-256 hash of an open file. It reads the file in parts.
func fileHash(f io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
//
// CF-05: restic restores as root into a new, empty directory that only root can read. Then the
// website user copies the files into place, so that a link in the website cannot make root
// write outside the website.
//
// CF-06: the agent restores a dump only when its hash agrees with the hash from the backup. It
// restores the dump as a temporary database user with access to that database only, not as the
// superuser, and the client cannot run commands on the database server.
func (d *Deps) Restore(req *agentv1.RestoreRequest) (jobs.Job, error) {
	unixUser := req.GetUnixUser()
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(req.GetWebsiteId()), validate.UnixUser(unixUser)); err != nil {
		return jobs.Job{}, err
	}
	if !snapshotID.MatchString(req.GetSnapshotId()) {
		return jobs.Job{}, errors.New("the snapshot ID is not valid")
	}
	r, err := newRepo(req.GetTarget(), req.GetCredentials())
	if err != nil {
		return jobs.Job{}, err
	}
	app := d.Host.AppDir(unixUser)
	// include is the absolute path in the snapshot. rel is the path in the app directory.
	var include, rel string
	var dbEngine, dbName string
	switch req.GetScope() {
	case "website":
		include, rel = app, "."
	case "file":
		if err := validate.RestorePath(req.GetItem()); err != nil {
			return jobs.Job{}, err
		}
		include, rel = app+req.GetItem(), strings.TrimPrefix(req.GetItem(), "/")
	case "database":
		engine, name, ok := strings.Cut(req.GetItem(), ":")
		if !ok {
			return jobs.Job{}, errors.New(`the database must be "engine:name"`)
		}
		if err := sqlEngine(engine); err != nil {
			return jobs.Job{}, err
		}
		_, serverErr := serverKey(req.GetDatabaseServer())
		if err := errors.Join(validate.TenantDatabaseName(unixUser, name), validate.SHA256(req.GetDumpSha256()), serverErr); err != nil {
			return jobs.Job{}, err
		}
		dbEngine, dbName = engine, name
		include = filepath.Join(d.dumpDir(unixUser), dumpFile(engine, name))
	default:
		return jobs.Job{}, fmt.Errorf("the restore scope %q is not valid", req.GetScope())
	}
	if req.GetDeleteExtra() && req.GetScope() != "website" {
		return jobs.Job{}, errors.New("only the restore of a full website can remove the other files")
	}
	staging := d.stageDir(req.GetJobId())
	cleanup := func(ctx context.Context) { _ = d.removeStage(context.WithoutCancel(ctx), req.GetJobId()) }
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Long: true, Steps: []jobs.Step{
		{Name: "Download snapshot", Do: func(ctx context.Context) error {
			_, err := d.restic(ctx, r, "snapshots", req.GetSnapshotId())
			return err
		}},
		{Name: "Restore", Do: func(ctx context.Context) error {
			defer cleanup(ctx)
			if err := d.makeStage(ctx, req.GetJobId()); err != nil {
				return err
			}
			if _, err := d.restic(ctx, r, "restore", req.GetSnapshotId(), "--target", staging, "--include", include); err != nil {
				return err
			}
			if dbName == "" {
				if req.GetDeleteExtra() {
					// The snapshot is on this server now, so the old files can go.
					if err := d.emptyAsUser(ctx, unixUser, app); err != nil {
						return err
					}
				}
				return d.copyStaged(ctx, req.GetJobId(), unixUser, rel)
			}
			sum, err := d.stagedDumpHash(ctx, req.GetJobId(), unixUser, dbEngine, dbName)
			if err != nil {
				return err
			}
			if sum != req.GetDumpSha256() {
				return errors.New("the dump does not agree with the hash from the backup")
			}
			srv, err := d.server(req.GetWebsiteId(), req.GetDatabaseServer(), dbEngine)
			if err != nil {
				return err
			}
			return d.restoreDump(ctx, srv, dbName, func(ctx context.Context, client []string) error {
				return d.loadStagedDump(ctx, req.GetJobId(), unixUser, dbEngine, dbName, client)
			})
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
	}, OnFailure: cleanup}, nil
}

// stageDir is the directory of one restore job. restic restores a snapshot into it. Only
// root can read it.
func (d *Deps) stageDir(jobID string) string { return filepath.Join(d.DataDir, "restore", jobID) }

// makeStage makes the directory of a restore job new and empty.
func (d *Deps) makeStage(ctx context.Context, jobID string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opRestoreStage, jobIn{Job: jobID}, nil)
	}
	staging := d.stageDir(jobID)
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	return os.MkdirAll(staging, 0o700)
}

// removeStage removes the directory of a restore job.
func (d *Deps) removeStage(ctx context.Context, jobID string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opRestoreClean, jobIn{Job: jobID}, nil)
	}
	return os.RemoveAll(d.stageDir(jobID))
}

// staged opens the directory of a restore job. A snapshot has the full path of each file,
// so the files of a website are in the directory "<stage>/<path of the website>".
//
// Second audit, the privilege boundary: a snapshot that does not come from this agent can
// have a symbolic link in place of a directory. Root would then read files outside the
// directory of the job. Thus root opens each path below the directory with no symbolic links.
func (d *Deps) staged(jobID string) (*safefs.Dir, error) { return safefs.Open(d.stageDir(jobID)) }

// copyStaged copies rel of the restored files of a website into the directory of the
// website. rel is a path in the directory of the website, or "." for all files.
func (d *Deps) copyStaged(ctx context.Context, jobID, unixUser, rel string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opRestoreCopy, copyIn{Job: jobID, User: unixUser, Rel: rel}, nil)
	}
	app := d.Host.AppDir(unixUser)
	stage, err := d.staged(jobID)
	if err != nil {
		return err
	}
	defer stage.Close()
	src := strings.TrimPrefix(app, "/")
	if fi, err := stage.Lstat(src); err != nil {
		return fmt.Errorf("the snapshot does not have the files of the website: %w", err)
	} else if !fi.IsDir() {
		return errors.New("the snapshot does not have the directory of the website")
	}
	if rel != "." {
		// The last part of rel can be a link: tar copies the link, not the file that it
		// points to.
		if _, err := stage.Lstat(filepath.Join(src, rel)); err != nil {
			return fmt.Errorf("the snapshot does not have %s: %w", rel, err)
		}
	}
	return d.copyAsUser(ctx, filepath.Join(d.stageDir(jobID), app), rel, unixUser, app)
}

// stagedDump opens the restored dump of a database of a website.
func (d *Deps) stagedDump(jobID, unixUser, engine, name string) (*os.File, error) {
	stage, err := d.staged(jobID)
	if err != nil {
		return nil, err
	}
	defer stage.Close()
	rel := strings.TrimPrefix(filepath.Join(d.dumpDir(unixUser), dumpFile(engine, name)), "/")
	return stage.OpenFile(rel, os.O_RDONLY, 0)
}

// stagedDumpHash gives the SHA-256 hash of the restored dump of a database.
func (d *Deps) stagedDumpHash(ctx context.Context, jobID, unixUser, engine, name string) (sum string, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opRestoreHash, restoreIn{Job: jobID, User: unixUser, Engine: engine, Name: name}, &sum)
		return sum, err
	}
	f, err := d.stagedDump(jobID, unixUser, engine, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return fileHash(f)
}

// loadStagedDump runs a database client with the restored dump of a database on its stdin.
// The root helper runs the command only when its policy accepts the command.
func (d *Deps) loadStagedDump(ctx context.Context, jobID, unixUser, engine, name string, client []string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opRestoreLoad, restoreIn{Job: jobID, User: unixUser, Engine: engine, Name: name, Argv: client}, nil)
	}
	f, err := d.stagedDump(jobID, unixUser, engine, name)
	if err != nil {
		return err
	}
	defer f.Close()
	return d.Run.Stream(ctx, f, nil, client[0], client[1:]...)
}

// copyAsUser copies rel from the directory src (that only root can read) into dst, as the
// website user. root only reads. The website user writes, so it cannot write outside the
// files that it can already change.
func (d *Deps) copyAsUser(ctx context.Context, src, rel, unixUser, dst string) error {
	return d.Run.Pipe(ctx,
		[]string{"tar", "--create", "--file=-", "--directory=" + src, "--", rel},
		[]string{"runuser", "-u", unixUser, "--", "tar", "--extract", "--file=-", "--directory=" + dst, "--no-same-owner", "--preserve-permissions"},
	)
}

// emptyAsUser removes all files in dir, as the website user (audit finding R8). After a
// restore, the website then has only the files of the snapshot, for example no file that an
// attacker added. The website user removes the files, so a link in the website cannot make
// root remove files outside the website. find does not follow links.
func (d *Deps) emptyAsUser(ctx context.Context, unixUser, dir string) error {
	// The user can remove a file only from a directory that the user can write.
	if _, err := d.Run.Run(ctx, "runuser", "-u", unixUser, "--", "find", dir, "-mindepth", "1", "-type", "d", "-exec", "chmod", "u+rwx", "--", "{}", "+"); err != nil {
		return err
	}
	_, err := d.Run.Run(ctx, "runuser", "-u", unixUser, "--", "find", dir, "-mindepth", "1", "-delete")
	return err
}

// restoreDump loads a dump as a temporary user that can use only this database. The password
// goes to the client in a file that only the database service user can read. feed runs the
// client command with the dump on its stdin.
//
// Audit batch 2, finding 5: the database is empty before the restore, so that the result is
// the database of the backup, with no tables that the website made later.
func (d *Deps) restoreDump(ctx context.Context, s dbServer, name string, feed func(ctx context.Context, client []string) error) error {
	engine, c := s.Engine, s.container()
	tmpUser := "cfrestore_" + randomHex(8)
	pass := randomHex(24)
	maria := engine == "mariadb"
	var create, drop string
	if maria {
		create = fmt.Sprintf("CREATE USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\nGRANT ALL PRIVILEGES ON %[3]s.* TO '%[1]s'@'localhost';\n", tmpUser, pass, mariaDB(name))
		drop = fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost';\n", tmpUser)
	} else {
		create = fmt.Sprintf("CREATE ROLE \"%[1]s\" LOGIN PASSWORD '%[2]s';\nGRANT \"%[3]s\" TO \"%[1]s\";\n", tmpUser, pass, pgRole(name, "readwrite"))
		drop = fmt.Sprintf("DROP ROLE IF EXISTS \"%s\";\n", tmpUser)
	}
	if _, err := d.sql(ctx, s, create); err != nil {
		return err
	}
	defer d.sql(context.WithoutCancel(ctx), s, drop)

	envFile, err := d.secretFile(ctx, map[bool]string{true: "MYSQL_PWD", false: "PGPASSWORD"}[maria]+"="+pass+"\n")
	if err != nil {
		return err
	}
	defer d.removeSecret(context.WithoutCancel(ctx), envFile)
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return err
	}
	base := []string{"runuser", "-u", DBUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", "-i", "--env-file", envFile, c}
	// --sandbox refuses client commands such as "system" and "source".
	client := []string{"mariadb", "--sandbox", "--user=" + tmpUser, "--batch", name}
	check := client
	if !maria {
		client = []string{"pg_restore", "--host=127.0.0.1", "--username=" + tmpUser, "--role=" + pgRole(name, "readwrite"), "--dbname=" + name, "--no-owner", "--no-privileges", "--clean", "--if-exists", "--exit-on-error"}
		check = []string{"psql", "--host=127.0.0.1", "--username=" + tmpUser, "--dbname=" + name, "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1"}
	}
	// The temporary user signs in before the database loses its data. A fault of the server,
	// for example a secret file that the database user cannot read, then stops the restore
	// while the database still has its data.
	if _, err := d.Run.RunInput(ctx, "SELECT 1;\n", base[0], slices.Concat(base[1:], check)...); err != nil {
		return fmt.Errorf("the restore did not start, and the database keeps its data: %w", err)
	}
	if _, err := d.sql(ctx, s, emptyDatabaseSQL(engine, name)); err != nil {
		return err
	}
	return feed(ctx, slices.Concat(base, client))
}

// emptyDatabaseSQL removes all data of a database, and keeps the database, its users and their
// privileges. MariaDB keeps the privileges on a database when the database is dropped. In
// PostgreSQL, the schemas of the website and the schema public go, and public comes back with
// the privileges of pgCreateDatabase.
func emptyDatabaseSQL(engine, name string) string {
	if engine == "mariadb" {
		return fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`;\nCREATE DATABASE `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name)
	}
	return fmt.Sprintf(`\connect "%[1]s"
DO $$DECLARE s name; BEGIN
  FOR s IN SELECT nspname FROM pg_namespace WHERE nspowner = '"%[1]s:rw"'::regrole AND nspname <> 'public' LOOP
    EXECUTE format('DROP SCHEMA %%I CASCADE', s);
  END LOOP;
END$$;
DROP SCHEMA IF EXISTS public CASCADE;
CREATE SCHEMA public AUTHORIZATION pg_database_owner;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO "%[1]s:ro";
ALTER DEFAULT PRIVILEGES FOR ROLE "%[1]s:rw" IN SCHEMA public GRANT SELECT ON TABLES TO "%[1]s:ro";
ALTER DEFAULT PRIVILEGES FOR ROLE "%[1]s:rw" IN SCHEMA public GRANT SELECT ON SEQUENCES TO "%[1]s:ro";
`, name)
}

// secretsDir holds short-lived files with passwords for the database clients.
func (d *Deps) secretsDir() string {
	if d.SecretsDir != "" {
		return d.SecretsDir
	}
	return "/run/cloudfactory/secrets"
}

// SecretFilesDir gives the directory of the secret files. The user cf-db must go through each
// directory of this path, so the root helper does not close one of them.
func (d *Deps) SecretFilesDir() string { return d.secretsDir() }

// secretFile writes a file that only the database service user can read, and gives its path.
func (d *Deps) secretFile(ctx context.Context, content string) (path string, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opSecretMake, secretIn{Content: content}, &path)
		return path, err
	}
	dir := d.secretsDir()
	// Other users can go through the directory, but cannot list it. The file has a random name.
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, secretPrefix+"*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Chmod(0o400); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if _, err := d.Run.Run(ctx, "chown", DBUser, f.Name()); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// secretPrefix starts the name of each file that secretFile makes.
const secretPrefix = "env-"

// isSecretFile reports whether path is a file that secretFile made: a file in the directory
// of the secrets, with a name of secretFile.
func (d *Deps) isSecretFile(path string) bool {
	name := filepath.Base(path)
	return path == filepath.Join(d.secretsDir(), name) && secretNameRE.MatchString(name)
}

var secretNameRE = regexp.MustCompile(`^` + secretPrefix + `[0-9]{1,20}$`)

// removeSecret removes a file that secretFile made.
func (d *Deps) removeSecret(ctx context.Context, path string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opSecretRemove, pathIn{Path: path}, nil)
	}
	if !d.isSecretFile(path) {
		return fmt.Errorf("%q is not a secret file of the agent", path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
