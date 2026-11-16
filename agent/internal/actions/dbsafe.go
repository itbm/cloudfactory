package actions

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// This file has the protections of the SQL that the agent runs as the administrator of a
// database server, against the owner of a database (adversarial review, area G3).

// pgSafePath is the first statement of each PostgreSQL session of the agent. The owner of a
// database can set a search path for all sessions of the database, also for the sessions of
// the superuser: ALTER DATABASE ... SET search_path. A function of the customer with the name
// of a system function then ran as the superuser (finding G3-01). With this path, each name
// with no schema is a name of the system catalogue.
const pgSafePath = "SET search_path = pg_catalog, pg_temp;\n"

var pgConnect = regexp.MustCompile(`(?m)^\\connect .*\n`)

// pgSafe adds pgSafePath to the start of a script and after each change of the database. A
// new connection has the settings of its database again.
func pgSafe(statements string) string {
	return pgSafePath + pgConnect.ReplaceAllStringFunc(statements, func(m string) string { return m + pgSafePath })
}

// pgDatabases gives the databases of the customers on a PostgreSQL server. A name that is
// not a database name of a website is not in the list.
func (d *Deps) pgDatabases(ctx context.Context, s dbServer) ([]string, error) {
	out, err := d.sql(ctx, s, "SELECT datname FROM pg_database WHERE datname NOT IN ('postgres', 'template0', 'template1');\n")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Fields(out) {
		if validate.DatabaseName(name) == nil {
			names = append(names, name)
		}
	}
	return names, nil
}

// pgDisownSQL gives the objects of a login user in one database to the group role of the
// database, and removes each privilege that the user has directly. A user can own objects:
// it can leave its group role (RESET ROLE) or make a large object. The owner of a database
// can also give privileges to a login user directly. DROP ROLE then failed, and the user kept
// its access after the panel removed it (finding G3-03).
func pgDisownSQL(user, database string) string {
	return fmt.Sprintf("\\connect \"%[2]s\"\nREASSIGN OWNED BY \"%[1]s\" TO \"%[2]s:rw\";\nDROP OWNED BY \"%[1]s\";\n", user, database)
}

// pgCloseSQL closes the open sessions of a user. A session keeps its role and its privileges
// after a change (finding G3-06).
func pgCloseSQL(user string) string {
	return fmt.Sprintf("SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE usename = '%s' AND pid <> pg_backend_pid();\n", user)
}

// closeSessions closes the open sessions of a database user after a change of its password
// or of its access. An error is only in the log: the change is complete.
func (d *Deps) closeSessions(ctx context.Context, s dbServer, user string) {
	var err error
	if s.Engine == "mariadb" {
		_, err = d.sql(ctx, s, fmt.Sprintf("KILL USER '%s';\n", user))
	} else {
		_, err = d.sql(ctx, s, pgCloseSQL(user))
	}
	if err != nil {
		slog.Warn("close the sessions of a database user", "user", user, "err", err)
	}
}

// pgUnlockDatabaseSQL removes the settings with which the owner of a database stops its
// removal: a template database cannot be dropped (finding G3-05).
func pgUnlockDatabaseSQL(name string) string {
	return fmt.Sprintf(`DO $$BEGIN
  IF EXISTS (SELECT FROM pg_database WHERE datname = '%[1]s') THEN
    EXECUTE 'ALTER DATABASE "%[1]s" IS_TEMPLATE false';
    EXECUTE 'ALTER DATABASE "%[1]s" RESET ALL';
  END IF;
END$$;
`, name)
}

// mariaDefiner gives the name of the definer account of a MariaDB database.
//
// A dump of root has "DEFINER=<user>@<host>" for each view, trigger, routine and event. The
// user of a restore has no right to make an object for a different definer, so the restore
// stopped at the first such object, after the database lost its data (findings A4-02 and
// G3-02). The agent does not give that right to the restore: with it, a dump could make a
// routine that runs as root.
//
// Thus the restore removes the definer from the dump, and the objects get the user of the
// restore as their definer. That user stays as a locked account with rights on this database
// only: nobody can sign in with it, and the objects continue to operate.
func mariaDefiner(database string) string {
	sum := sha256.Sum256([]byte(database))
	return "cfdefiner_" + hex.EncodeToString(sum[:8])
}

// The start of a line of mariadb-dump that has a definer.
var definerLine = regexp.MustCompile("^(/\\*![0-9]{5} |/\\*M?![0-9]{5,6} CREATE\\*/ /\\*M?![0-9]{5,6} |CREATE (?:OR REPLACE )?)DEFINER=`(?:[^`\n]|``)*`@`(?:[^`\n]|``)*`(\\*/)? ?")

// withoutDefiners gives a dump of mariadb-dump with no definer at the start of a line.
// A definer that is in a different place stays, and the server then refuses the statement.
func withoutDefiners(dump io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(dump, 64<<10)
		lineStart := true
		for {
			part, err := br.ReadSlice('\n')
			if len(part) > 0 {
				if lineStart && (part[0] == '/' || part[0] == 'C') {
					part = removeDefiner(part)
				}
				lineStart = part[len(part)-1] == '\n'
				if _, werr := pw.Write(part); werr != nil {
					return
				}
			}
			if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}

func removeDefiner(line []byte) []byte {
	m := definerLine.FindSubmatchIndex(line)
	if m == nil {
		return line
	}
	prefix := string(line[m[2]:m[3]])
	rest := line[m[1]:]
	switch {
	case strings.HasPrefix(prefix, "CREATE"):
		// CREATE DEFINER=`u`@`h` PROCEDURE ... becomes CREATE PROCEDURE ...
		return append([]byte(prefix), rest...)
	case strings.Contains(prefix, "CREATE*/"):
		// /*!50003 CREATE*/ /*!50017 DEFINER=`u`@`h`*/ /*!50003 TRIGGER ... loses its
		// second comment.
		i := strings.Index(prefix, "CREATE*/") + len("CREATE*/ ")
		return append([]byte(prefix[:i]), rest...)
	}
	// /*!50013 DEFINER=`u`@`h` SQL SECURITY DEFINER */ keeps the rest of the comment.
	return append([]byte(prefix), rest...)
}
