package actions

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// engineDriver is the work of the agent with one database engine. Each engine has one driver
// (engine_*.go). The units come from quadlet.EngineSpec. The other code of the agent uses
// the methods of the driver, and the capabilities below, not the name of the engine.
//
// A new engine adds one quadlet.EngineSpec and one driver. The parity tests
// (parity_test.go) record what each driver does.
type engineDriver interface {
	// adjustUnit sets the values of the unit that only this engine has.
	adjustUnit(u *quadlet.Database, record DatabaseServer)
	// prepareSecret writes the secret of the superuser, or its equivalent, once.
	prepareSecret(ctx context.Context, d *Deps, s dbServer, u quadlet.Database) error
	// ready reports whether the server answers in its container.
	ready(ctx context.Context, d *Deps, s dbServer) error
	// started runs the work after each start of the server.
	started(ctx context.Context, d *Deps, s dbServer) error
	// secretPath gives the file with the secret of the superuser.
	secretPath(d *Deps, s dbServer) string
	// checkSecret checks a secret from a different server, before writeSecret.
	checkSecret(secret []byte) error
	// writeSecret replaces the secret of the superuser.
	writeSecret(d *Deps, s dbServer, secret []byte) error
	// sizes gives the size of each database of the server.
	sizes(ctx context.Context, d *Deps, s dbServer) ([]panel.DatabaseSize, error)
	// dropUser removes a user. It is not an error when the user is not there.
	dropUser(ctx context.Context, d *Deps, s dbServer, user string) error
	// dropDatabase removes a database. It is not an error when the database is not there.
	dropDatabase(ctx context.Context, d *Deps, s dbServer, name string) error
}

// sqlDriver is the capability of an engine with SQL, users with grants, and dumps: MariaDB
// and PostgreSQL. The agent copies, backs up and restores only the databases of such engines.
type sqlDriver interface {
	engineDriver
	// client gives the client command (in the container) that reads statements on stdin.
	client() []string
	// prepareSQL changes the statements before they go to the client.
	prepareSQL(statements string) string
	// listensArgs gives the command (in the container) that checks that the server accepts
	// TCP connections.
	listensArgs() []string
	// superuserSecret gives the content of the environment file of the superuser.
	superuserSecret(pass string) string
	// createDatabase makes a new database. It fails when the database exists.
	createDatabase(ctx context.Context, d *Deps, s dbServer, name string) error
	// ensureDatabase makes a database when it is not there.
	ensureDatabase(ctx context.Context, d *Deps, s dbServer, name string) error
	// createUser makes a user. It fails when a user with the name exists.
	createUser(ctx context.Context, d *Deps, s dbServer, user, pass, host, access string) error
	// grantSQL gives one database to a user with "readonly" or "readwrite" privileges.
	grantSQL(user, host, database, privileges string) string
	// setUser changes the password and the access options of a user.
	setUser(ctx context.Context, d *Deps, s dbServer, user, pass, host, access string) error
	// accessSQL sets the full access of a user to the databases of a website.
	accessSQL(user, host string, websiteDatabases []string, grants []grant) string
	// revokeDatabaseSQL removes the access of the users to a database. It can be empty.
	revokeDatabaseSQL(name string, users []string) string
	// removeSQL runs before dropDatabase when the agent removes a website. It can be empty.
	removeSQL(name string) string
	// closeSQL closes the open sessions of a user.
	closeSQL(user string) string
	// dumpArgs gives the dump command (in the container). It writes the dump to stdout.
	dumpArgs(name string) []string
	// dumpFilter changes a dump before a restore. It can be nil.
	dumpFilter() func(io.Reader) io.Reader
	// restoreAccount gives the temporary user of a restore, the SQL that makes it and the SQL
	// that removes it, and the environment variable for its password.
	restoreAccount(name, pass string) (user, create, drop, passwordEnv string)
	// restoreClients gives the restore command and the command that checks the sign-in.
	restoreClients(user, name string) (restore, check []string)
	// emptySQL removes all data of a database, and keeps the database and its users.
	emptySQL(name string) string
}

// grant is one database of a user with its privileges.
type grant struct{ database, privileges string }

// numberedDriver is the capability of an engine with numbered databases and one user for
// each database, and no SQL: Valkey.
type numberedDriver interface {
	engineDriver
	numbered()
}

// engineDrivers are the drivers of the engines of quadlet.Engines.
var engineDrivers = map[string]engineDriver{
	"mariadb":    mariaDriver{},
	"postgresql": pgDriver{},
	"valkey":     valkeyDriver{},
}

// driverOf gives the driver of an engine.
func driverOf(engine string) (engineDriver, error) {
	if drv, ok := engineDrivers[engine]; ok {
		return drv, nil
	}
	return nil, fmt.Errorf("the database engine %q is not valid", engine)
}

// sqlDriverOf gives the driver of an engine with SQL.
func sqlDriverOf(engine string) (sqlDriver, bool) {
	drv, ok := engineDrivers[engine].(sqlDriver)
	return drv, ok
}

// isNumbered reports whether an engine has numbered databases (Valkey).
func isNumbered(engine string) bool {
	_, ok := engineDrivers[engine].(numberedDriver)
	return ok
}

// engineLabel gives the name of an engine for people.
func engineLabel(engine string) string {
	if spec, ok := quadlet.Engine(engine); ok {
		return spec.Label
	}
	return engine
}

// sqlReady is the ready check of an engine with SQL. The server first accepts TCP
// connections (see waitDatabase), then answers SQL.
func sqlReady(ctx context.Context, d *Deps, s dbServer) error {
	if err := d.databaseListens(ctx, s); err != nil {
		return err
	}
	_, err := d.sqlNow(ctx, s, "SELECT 1;\n")
	return err
}

// sqlSizes runs a query that gives "name<tab or |>bytes" for each database.
func sqlSizes(ctx context.Context, d *Deps, s dbServer, query string) ([]panel.DatabaseSize, error) {
	res, err := d.sqlNow(ctx, s, query)
	if err != nil {
		return nil, err
	}
	var out []panel.DatabaseSize
	for _, line := range strings.Split(res, "\n") {
		f := strings.FieldsFunc(line, func(r rune) bool { return r == '\t' || r == '|' })
		if len(f) != 2 {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); err == nil {
			out = append(out, panel.DatabaseSize{Engine: s.Engine, Name: strings.TrimSpace(f[0]), SizeBytes: n})
		}
	}
	return out, nil
}
