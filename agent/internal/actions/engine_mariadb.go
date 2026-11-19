package actions

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// mariaDriver is the driver of MariaDB. The helpers mariaHost, mariaRequire, mariaPrivileges,
// mariaDB, mariaDefiner, mariaRevokeDatabase and withoutDefiners are in data.go and dbsafe.go.
type mariaDriver struct{}

func (mariaDriver) adjustUnit(*quadlet.Database, DatabaseServer) {}

func (mariaDriver) prepareSecret(ctx context.Context, d *Deps, s dbServer, u quadlet.Database) error {
	return d.writeSuperuserSecret(ctx, s, u.EnvFile)
}

func (mariaDriver) ready(ctx context.Context, d *Deps, s dbServer) error { return sqlReady(ctx, d, s) }

func (mariaDriver) started(context.Context, *Deps, dbServer) error { return nil }

func (mariaDriver) secretPath(d *Deps, s dbServer) string {
	return filepath.Join(d.dbDir(s), s.Engine+".env")
}

func (mariaDriver) checkSecret(secret []byte) error {
	return checkEnvSecret("mariadb", map[string]bool{"MARIADB_ROOT_PASSWORD": true, "MYSQL_PWD": true}, secret)
}

func (mariaDriver) writeSecret(d *Deps, s dbServer, secret []byte) error {
	return d.writeEnvSecret(s, secret)
}

func (mariaDriver) sizes(ctx context.Context, d *Deps, s dbServer) ([]panel.DatabaseSize, error) {
	return sqlSizes(ctx, d, s, "SELECT table_schema, COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables GROUP BY table_schema;\n")
}

func (mariaDriver) dropUser(ctx context.Context, d *Deps, s dbServer, user string) error {
	out, err := d.sql(ctx, s, fmt.Sprintf("SELECT Host FROM mysql.user WHERE User = '%s';\n", user))
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, h := range strings.Fields(out) {
		if validate.DatabaseClientHost(h) == nil {
			fmt.Fprintf(&b, "DROP USER IF EXISTS '%s'@'%s';\n", user, h)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	if _, err = d.sql(ctx, s, b.String()); err != nil {
		return err
	}
	d.closeSessions(ctx, s, user)
	return nil
}

func (mariaDriver) dropDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	_, err := d.sql(ctx, s, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\nDROP USER IF EXISTS '%s'@'localhost';\n", name, mariaDefiner(name)))
	return err
}

func (mariaDriver) client() []string {
	return []string{"mariadb", "--user=root", "--batch", "--skip-column-names"}
}

func (mariaDriver) prepareSQL(statements string) string { return statements }

func (mariaDriver) listensArgs() []string {
	// "ping" is successful when the server answers, also when the server refuses the user.
	// The server refuses the user here, and writes one warning for that in its log.
	return []string{"mariadb-admin", "--protocol=tcp", "--host=127.0.0.1", "--port=3306", "--connect-timeout=5", "ping"}
}

func (mariaDriver) superuserSecret(pass string) string {
	return "MARIADB_ROOT_PASSWORD=" + pass + "\nMYSQL_PWD=" + pass + "\n"
}

func (mariaDriver) createDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	// No "IF NOT EXISTS": the job fails when the database exists.
	_, err := d.sql(ctx, s, fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name))
	return err
}

func (mariaDriver) ensureDatabase(ctx context.Context, d *Deps, s dbServer, name string) error {
	found, err := d.exists(ctx, s, fmt.Sprintf("SHOW DATABASES LIKE '%s';\n", strings.ReplaceAll(name, "_", `\\_`)))
	if err != nil || found {
		return err
	}
	_, err = d.sql(ctx, s, fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name))
	return err
}

func (mariaDriver) createUser(ctx context.Context, d *Deps, s dbServer, user, pass, host, access string) error {
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

func (mariaDriver) grantSQL(user, host, database, privileges string) string {
	p, _ := mariaPrivileges(privileges)
	return fmt.Sprintf("GRANT %s ON %s.* TO '%s'@'%s';\n", p, mariaDB(database), user, host)
}

func (mariaDriver) setUser(ctx context.Context, d *Deps, s dbServer, user, pass, host, access string) error {
	old, err := d.mariaCurrentHost(ctx, s, user)
	if err != nil {
		return err
	}
	var b strings.Builder
	if old != host {
		fmt.Fprintf(&b, "RENAME USER '%s'@'%s' TO '%s'@'%s';\n", user, old, user, host)
	}
	fmt.Fprintf(&b, "ALTER USER '%s'@'%s' %s;\n", user, host, mariaRequire(access))
	if pass != "" {
		fmt.Fprintf(&b, "ALTER USER '%s'@'%s' IDENTIFIED BY '%s';\n", user, host, pass)
	}
	_, err = d.sql(ctx, s, b.String())
	return err
}

func (m mariaDriver) accessSQL(user, host string, _ []string, grants []grant) string {
	var b strings.Builder
	// Remove all grants, then give the grants of the request.
	fmt.Fprintf(&b, "REVOKE ALL PRIVILEGES, GRANT OPTION FROM '%s'@'%s';\n", user, host)
	for _, g := range grants {
		b.WriteString(m.grantSQL(user, host, g.database, g.privileges))
	}
	return b.String()
}

func (mariaDriver) revokeDatabaseSQL(name string, _ []string) string {
	return mariaRevokeDatabase(name)
}

func (mariaDriver) removeSQL(name string) string { return mariaRevokeDatabase(name) }

func (mariaDriver) closeSQL(user string) string { return fmt.Sprintf("KILL USER '%s';\n", user) }

func (mariaDriver) dumpArgs(name string) []string {
	return []string{"mariadb-dump", "--user=root", "--single-transaction", "--routines", "--events", name}
}

func (mariaDriver) dumpFilter() func(io.Reader) io.Reader { return withoutDefiners }

func (mariaDriver) restoreAccount(name, pass string) (user, create, drop, passwordEnv string) {
	// The user of the restore is the definer account of the database (mariaDefiner). It
	// stays after the restore, locked.
	user = mariaDefiner(name)
	create = fmt.Sprintf("CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\nALTER USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s' ACCOUNT UNLOCK;\nGRANT ALL PRIVILEGES ON %[3]s.* TO '%[1]s'@'localhost';\n", user, pass, mariaDB(name))
	drop = fmt.Sprintf("ALTER USER IF EXISTS '%[1]s'@'localhost' IDENTIFIED BY '%[2]s' ACCOUNT LOCK;\n", user, randomHex(24))
	return user, create, drop, "MYSQL_PWD"
}

func (mariaDriver) restoreClients(user, name string) (restore, check []string) {
	// --sandbox refuses client commands such as "system" and "source".
	client := []string{"mariadb", "--sandbox", "--user=" + user, "--batch", name}
	return client, client
}

// emptySQL drops and makes the database again. MariaDB keeps the privileges on a database
// when the database is dropped.
func (mariaDriver) emptySQL(name string) string {
	return fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`;\nCREATE DATABASE `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name)
}
