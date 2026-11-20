package actions

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// pgDriver is the driver of PostgreSQL. The helpers pgSafe, pgHarden, pgRole,
// pgCreateDatabase, pgRevokeSQL, pgDisownSQL, pgCloseSQL and pgUnlockDatabaseSQL are in
// data.go and dbsafe.go.
type pgDriver struct{}

func (pgDriver) adjustUnit(*quadlet.Database, DatabaseServer) {}

func (pgDriver) prepareSecret(ctx context.Context, d *Deps, s dbServer, u quadlet.Database) error {
	return d.writeSuperuserSecret(ctx, s, u.EnvFile)
}

func (pgDriver) ready(ctx context.Context, d *Deps, s dbServer) error { return sqlReady(ctx, d, s) }

func (pgDriver) started(ctx context.Context, d *Deps, s dbServer) error {
	// The users of the websites cannot connect to the databases of the server itself.
	_, err := d.sqlNow(ctx, s, pgHarden)
	return err
}

func (pgDriver) secretPath(d *Deps, s dbServer) string {
	return filepath.Join(d.dbDir(s), s.Engine+".env")
}

func (pgDriver) checkSecret(secret []byte) error {
	return checkEnvSecret("postgresql", map[string]bool{"POSTGRES_PASSWORD": true}, secret)
}

func (pgDriver) writeSecret(d *Deps, s dbServer, secret []byte) error {
	return d.writeEnvSecret(s, secret)
}

func (pgDriver) sizes(ctx context.Context, d *Deps, s dbServer) ([]panel.DatabaseSize, error) {
	return sqlSizes(ctx, d, s, "SELECT datname || '|' || pg_database_size(datname) FROM pg_database WHERE NOT datistemplate;\n")
}

func (pgDriver) dropUser(ctx context.Context, d *Deps, s dbServer, user string) error {
	found, err := d.exists(ctx, s, fmt.Sprintf("SELECT 1 FROM pg_roles WHERE rolname = '%s';\n", user))
	if err != nil || !found {
		return err
	}
	// The user can own objects and can have direct privileges, in each database of the
	// server. DROP ROLE fails while it has them (finding G3-03). DROP ROLE also removes the
	// memberships and the settings of the user.
	databases, err := d.pgDatabases(ctx, s)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ALTER ROLE \"%s\" NOLOGIN;\n", user)
	b.WriteString(pgCloseSQL(user))
	for _, db := range databases {
		b.WriteString(pgDisownSQL(user, db))
	}
	fmt.Fprintf(&b, "DROP ROLE IF EXISTS \"%s\";\n", user)
	_, err = d.sql(ctx, s, b.String())
	return err
}

func (pgDriver) dropDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	// The group roles of the database can own objects in the other databases of the website,
	// and DROP ROLE then fails (finding G3-05).
	databases, err := d.pgDatabases(ctx, s)
	if err != nil {
		return err
	}
	var disown strings.Builder
	if found, err := d.exists(ctx, s, fmt.Sprintf("SELECT 1 FROM pg_roles WHERE rolname = '%s:rw';\n", name)); err != nil {
		return err
	} else if found {
		for _, other := range databases {
			if other != name {
				fmt.Fprintf(&disown, "\\connect \"%[1]s\"\nREASSIGN OWNED BY \"%[2]s:rw\", \"%[2]s:ro\" TO \"%[1]s:rw\";\nDROP OWNED BY \"%[2]s:rw\", \"%[2]s:ro\";\n", other, name)
			}
		}
		disown.WriteString("\\connect postgres\n")
	}
	_, err = d.sql(ctx, s, pgUnlockDatabaseSQL(name)+disown.String()+fmt.Sprintf("DROP DATABASE IF EXISTS \"%[1]s\" WITH (FORCE);\nDROP ROLE IF EXISTS \"%[1]s:rw\";\nDROP ROLE IF EXISTS \"%[1]s:ro\";\n", name))
	return err
}

func (pgDriver) client() []string {
	return []string{"psql", "--username=postgres", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align"}
}

func (pgDriver) prepareSQL(statements string) string { return pgSafe(statements) }

func (pgDriver) listensArgs() []string {
	return []string{"pg_isready", "--quiet", "--host=127.0.0.1", "--port=5432", "--timeout=5"}
}

func (pgDriver) superuserSecret(pass string) string { return "POSTGRES_PASSWORD=" + pass + "\n" }

func (pgDriver) createDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	found, err := d.exists(ctx, s, fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%[1]s' UNION ALL SELECT 1 FROM pg_roles WHERE rolname IN ('%[1]s:rw', '%[1]s:ro');\n", name))
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("the database %s exists", name)
	}
	_, err = d.sql(ctx, s, pgCreateDatabase(name))
	return err
}

func (pgDriver) ensureDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	found, err := d.exists(ctx, s, fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s';\n", name))
	if err != nil || found {
		return err
	}
	_, err = d.sql(ctx, s, pgCreateDatabase(name))
	return err
}

func (pgDriver) createUser(ctx context.Context, d *Deps, s dbServer, user, pass, _, _ string) error {
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

func (pgDriver) grantSQL(user, _, database, privileges string) string {
	if privileges == "readwrite" {
		return fmt.Sprintf("GRANT \"%[1]s\" TO \"%[2]s\";\nALTER ROLE \"%[2]s\" IN DATABASE \"%[3]s\" SET role = '%[1]s';\n", pgRole(database, privileges), user, database)
	}
	return fmt.Sprintf("GRANT \"%[1]s\" TO \"%[2]s\";\n", pgRole(database, privileges), user)
}

func (pgDriver) setUser(ctx context.Context, d *Deps, s dbServer, user, pass, _, _ string) error {
	// A user from before the connection limit gets the limit with each change.
	statement := fmt.Sprintf("ALTER ROLE \"%s\" WITH LOGIN CONNECTION LIMIT %d", user, pgUserConnections)
	if pass != "" {
		statement += fmt.Sprintf(" PASSWORD '%s'", pass)
	}
	_, err := d.sql(ctx, s, statement+";\n")
	return err
}

func (p pgDriver) accessSQL(user, host string, websiteDatabases []string, grants []grant) string {
	var b strings.Builder
	for _, db := range websiteDatabases {
		b.WriteString(pgRevokeSQL(user, db))
	}
	for _, g := range grants {
		b.WriteString(p.grantSQL(user, host, g.database, g.privileges))
	}
	// The objects and the direct privileges of the user in each database that it loses
	// (finding G3-03). The memberships above come first: the script stops at the first error.
	keeps := map[string]bool{}
	for _, g := range grants {
		keeps[g.database] = true
	}
	for _, db := range websiteDatabases {
		if !keeps[db] {
			b.WriteString(pgDisownSQL(user, db))
		}
	}
	return b.String()
}

func (pgDriver) revokeDatabaseSQL(name string, users []string) string {
	var b strings.Builder
	for _, u := range users {
		b.WriteString(pgRevokeSQL(u, name))
	}
	return b.String()
}

func (pgDriver) removeSQL(string) string { return "" }

func (pgDriver) closeSQL(user string) string { return pgCloseSQL(user) }

func (pgDriver) dumpArgs(name string) []string {
	return []string{"pg_dump", "--username=postgres", "--format=custom", "--no-owner", "--no-privileges", name}
}

func (pgDriver) dumpFilter() func(io.Reader) io.Reader { return nil }

func (pgDriver) restoreAccount(name, pass string) (user, create, drop, passwordEnv string) {
	user = "cfrestore_" + randomHex(8)
	create = fmt.Sprintf("CREATE ROLE \"%[1]s\" LOGIN PASSWORD '%[2]s';\nGRANT \"%[3]s\" TO \"%[1]s\";\n", user, pass, pgRole(name, "readwrite"))
	drop = fmt.Sprintf("DROP ROLE IF EXISTS \"%s\";\n", user)
	return user, create, drop, "PGPASSWORD"
}

func (pgDriver) restoreClients(user, name string) (restore, check []string) {
	restore = []string{"pg_restore", "--host=127.0.0.1", "--username=" + user, "--role=" + pgRole(name, "readwrite"), "--dbname=" + name, "--no-owner", "--no-privileges", "--clean", "--if-exists", "--exit-on-error"}
	check = []string{"psql", "--host=127.0.0.1", "--username=" + user, "--dbname=" + name, "--no-psqlrc", "--quiet", "--set=ON_ERROR_STOP=1"}
	return restore, check
}

// emptySQL removes the schemas of the website and the schema public. Then public comes back
// with the privileges of pgCreateDatabase.
func (pgDriver) emptySQL(name string) string {
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
