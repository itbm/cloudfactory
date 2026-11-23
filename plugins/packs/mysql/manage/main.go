// Command manage is the program /cf/manage of the example MySQL pack: the management contract
// cf-database/1 (docs/packs.md).
//
// The agent runs "podman exec -i <container> /cf/manage <operation>" in the container of a
// database server. The first line of stdin is one JSON object. The program writes one JSON
// object on stdout. An exit code that is not 0 is an error, and stderr has its text.
//
// The operations "dump" and "restore" stream the data: "dump" writes the dump to stdout, and
// "restore" reads the dump from stdin, after the first line.
//
// The agent sends only names and passwords that obey the rules of the panel. The program
// checks them again, and it puts no other text in SQL.
//
// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const contract = "cf-database/1"

// The socket of the server, from /etc/mysql/conf.d/cloudfactory.cnf. The root file system is
// read-only, so the socket is in /tmp.
const socket = "/tmp/mysqld.sock"

// port is the port of the engine in the container (pack.json).
const port = "3306"

// The rules of the panel (schema/input-rules.json): "databaseName" and "databasePassword".
var (
	namePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
	// The agent makes the password of the administrator: hexadecimal digits.
	superuserPattern = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
	versionPattern   = regexp.MustCompile(`Ver ([0-9][0-9A-Za-z.\-]*)`)
)

// reserved are the databases and the users of MySQL itself. The panel refuses these names too.
var reserved = map[string]bool{
	"mysql": true, "sys": true, "information_schema": true, "performance_schema": true,
	"root": true, "mysql.sys": true, "mysql.session": true, "mysql.infoschema": true,
}

// maxUser is the length limit of a MySQL user name. MariaDB has a larger limit.
const maxUser = 32

// maxLine is the limit of the first line of stdin.
const maxLine = 1 << 20

var operations = []string{"describe", "ready", "sizes", "create-database", "drop-database", "set-user", "drop-user", "dump", "restore"}

// runner runs a client program of MySQL. The tests replace it.
type runner interface {
	run(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			return fmt.Errorf("%s: %w", name, err)
		}
		return fmt.Errorf("%s: %w: %s", name, err, text)
	}
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	m := &manager{run: execRunner{}, password: os.Getenv("CF_SUPERUSER_PASSWORD"), tmp: os.TempDir()}
	op := ""
	if len(os.Args) > 1 {
		op = os.Args[1]
	}
	if err := m.do(ctx, op, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cf-manage:", err)
		os.Exit(1)
	}
}

// manager runs the operations.
type manager struct {
	run runner
	// password is the password of root, from CF_SUPERUSER_PASSWORD.
	password string
	// tmp is the directory of the option file with the password.
	tmp string
}

// do runs one operation.
func (m *manager) do(ctx context.Context, op string, stdin io.Reader, stdout io.Writer) error {
	in := bufio.NewReader(stdin)
	line, err := firstLine(in)
	if err != nil {
		return err
	}
	switch op {
	case "describe":
		if err := decode(line, &struct{}{}); err != nil {
			return err
		}
		var out bytes.Buffer
		if err := m.run.run(ctx, nil, &out, "mysqld", "--version"); err != nil {
			return err
		}
		version := ""
		if v := versionPattern.FindStringSubmatch(out.String()); v != nil {
			version = v[1]
		}
		return write(stdout, map[string]any{"contract": contract, "engine": "mysql", "version": version, "operations": operations})
	case "ready":
		if err := decode(line, &struct{}{}); err != nil {
			return err
		}
		// The entrypoint of the base image starts a server with no network for the first
		// setup. Only the final server answers on TCP. "mysqladmin ping" needs no password.
		err := m.run.run(ctx, nil, io.Discard, "mysqladmin", "--no-defaults", "--protocol=tcp", "--host=127.0.0.1", "--port="+port, "--connect-timeout=5", "ping")
		return write(stdout, map[string]bool{"ready": err == nil})
	case "sizes":
		if err := decode(line, &struct{}{}); err != nil {
			return err
		}
		return m.sizes(ctx, stdout)
	case "create-database":
		var req nameRequest
		if err := decodeName(line, &req); err != nil {
			return err
		}
		// An error when the database exists: the agent asks for a new database only.
		return m.exec(ctx, stdout, fmt.Sprintf("CREATE DATABASE %s CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n", ident(req.Name)))
	case "drop-database":
		var req nameRequest
		if err := decodeName(line, &req); err != nil {
			return err
		}
		return m.dropDatabase(ctx, stdout, req.Name)
	case "set-user":
		var req userRequest
		if err := decode(line, &req); err != nil {
			return err
		}
		return m.setUser(ctx, stdout, req)
	case "drop-user":
		var req struct {
			User string `json:"user"`
		}
		if err := decode(line, &req); err != nil {
			return err
		}
		if err := checkUser(req.User); err != nil {
			return err
		}
		return m.exec(ctx, stdout, fmt.Sprintf("DROP USER IF EXISTS %s;\n", account(req.User)))
	case "dump":
		var req nameRequest
		if err := decodeName(line, &req); err != nil {
			return err
		}
		return m.withOptions(func(options string) error {
			// No CREATE DATABASE and no USE: the dump goes into a database of any name.
			return m.run.run(ctx, nil, stdout, "mysqldump", "--defaults-extra-file="+options, "--single-transaction", "--routines", "--events", "--triggers",
				"--no-tablespaces", "--set-gtid-purged=OFF", "--hex-blob", "--default-character-set=utf8mb4", req.Name)
		})
	case "restore":
		var req nameRequest
		if err := decodeName(line, &req); err != nil {
			return err
		}
		return m.restore(ctx, stdout, req.Name, in)
	}
	return fmt.Errorf("the operation %q is not known; the operations are %s", op, strings.Join(operations, ", "))
}

type nameRequest struct {
	Name string `json:"name"`
}

type access struct {
	Name   string `json:"name"`
	Access string `json:"access"`
}

type userRequest struct {
	User      string   `json:"user"`
	Password  string   `json:"password"`
	Databases []access `json:"databases"`
}

// firstLine reads the JSON object on the first line of stdin. An empty stdin is "{}".
func firstLine(in *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, prefix, err := in.ReadLine()
		if err == io.EOF && len(line) == 0 {
			return []byte("{}"), nil
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		line = append(line, part...)
		if len(line) > maxLine {
			return nil, errors.New("the first line of stdin is too long")
		}
		if !prefix || err == io.EOF {
			break
		}
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return []byte("{}"), nil
	}
	return line, nil
}

// decode reads the JSON object of an operation. An unknown key is an error.
func decode(line []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the input is not valid: %w", err)
	}
	if dec.More() {
		return errors.New("the input is not valid: more than one JSON value")
	}
	return nil
}

func decodeName(line []byte, req *nameRequest) error {
	if err := decode(line, req); err != nil {
		return err
	}
	return checkName(req.Name)
}

func checkName(name string) error {
	if !namePattern.MatchString(name) || reserved[name] {
		return fmt.Errorf("the database name %q is not valid", name)
	}
	return nil
}

func checkUser(user string) error {
	if !namePattern.MatchString(user) || reserved[user] {
		return fmt.Errorf("the user name %q is not valid", user)
	}
	if len(user) > maxUser {
		return fmt.Errorf("the user name %q has more than %d characters; MySQL permits at most %d", user, maxUser, maxUser)
	}
	return nil
}

// ident gives a checked name in backticks.
func ident(name string) string { return "`" + name + "`" }

// grantName gives a database name for GRANT and REVOKE. In GRANT, "_" matches each character:
// with no backslash, a grant for "a_b" also gives access to "axb".
func grantName(name string) string { return "`" + strings.ReplaceAll(name, "_", `\_`) + "`" }

// account gives the account of a website user. The host firewall lets only the websites of the
// customer connect, so the host part is "%".
func account(user string) string { return "'" + user + "'@'%'" }

// privileges gives the privileges of an access level, as for the built-in MariaDB engine.
func privileges(level string) (string, error) {
	switch level {
	case "readonly":
		return "SELECT, SHOW VIEW", nil
	case "readwrite":
		return "ALL PRIVILEGES", nil
	}
	return "", fmt.Errorf("the access %q is not valid; use readonly or readwrite", level)
}

// withOptions writes an option file with the password of root, which only this process can
// read, for one client program. The password is not in the arguments of the program.
func (m *manager) withOptions(fn func(path string) error) error {
	if !superuserPattern.MatchString(m.password) {
		return errors.New("the password of the administrator in CF_SUPERUSER_PASSWORD is not valid")
	}
	f, err := os.CreateTemp(m.tmp, "cf-manage-*.cnf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := fmt.Fprintf(f, "[client]\nuser=root\npassword=%s\nsocket=%s\n", m.password, socket); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fn(f.Name())
}

// query runs SQL as root and gives the rows, with a tab between the columns.
func (m *manager) query(ctx context.Context, sql string) ([][]string, error) {
	var out bytes.Buffer
	err := m.withOptions(func(options string) error {
		return m.run.run(ctx, strings.NewReader(sql), &out, "mysql", "--defaults-extra-file="+options, "--batch", "--skip-column-names", "--raw", "--default-character-set=utf8mb4")
	})
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

// exec runs SQL as root and answers "{}".
func (m *manager) exec(ctx context.Context, stdout io.Writer, sql string) error {
	if _, err := m.query(ctx, sql); err != nil {
		return err
	}
	return write(stdout, struct{}{})
}

// sizes gives each database of the customer, also an empty one: the agent looks for a
// database in this list.
func (m *manager) sizes(ctx context.Context, stdout io.Writer) error {
	// Without this setting, MySQL gives sizes from a cache of up to 24 hours.
	rows, err := m.query(ctx, `SET SESSION information_schema_stats_expiry = 0;
SELECT s.SCHEMA_NAME, COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0)
FROM information_schema.SCHEMATA s LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
WHERE s.SCHEMA_NAME NOT IN ('mysql', 'sys', 'information_schema', 'performance_schema')
GROUP BY s.SCHEMA_NAME ORDER BY s.SCHEMA_NAME;
`)
	if err != nil {
		return err
	}
	type size struct {
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
	}
	sizes := []size{}
	for _, row := range rows {
		if len(row) != 2 {
			return fmt.Errorf("the server gave a row that is not valid: %q", strings.Join(row, "\t"))
		}
		n, err := strconv.ParseInt(row[1], 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("the server gave a size that is not valid: %q", row[1])
		}
		sizes = append(sizes, size{row[0], n})
	}
	return write(stdout, map[string]any{"sizes": sizes})
}

// dropDatabase removes a database and the access of each user to it. MySQL keeps a grant
// after DROP DATABASE: a new database with the same name would give access to the old users.
func (m *manager) dropDatabase(ctx context.Context, stdout io.Writer, name string) error {
	escaped := strings.ReplaceAll(name, "_", `\_`)
	// In a string in SQL, a backslash needs a second backslash.
	rows, err := m.query(ctx, fmt.Sprintf("SELECT User, Host, Db FROM mysql.db WHERE Db IN ('%s', '%s');\n", name, strings.ReplaceAll(escaped, `\`, `\\`)))
	if err != nil {
		return err
	}
	var sql strings.Builder
	for _, row := range rows {
		if len(row) != 3 || !namePattern.MatchString(row[0]) || row[1] != "%" || (row[2] != name && row[2] != escaped) {
			// A grant that this program did not make. It is not for a website user.
			continue
		}
		fmt.Fprintf(&sql, "REVOKE IF EXISTS ALL PRIVILEGES ON `%s`.* FROM %s;\n", row[2], account(row[0]))
	}
	fmt.Fprintf(&sql, "DROP DATABASE IF EXISTS %s;\n", ident(name))
	return m.exec(ctx, stdout, sql.String())
}

// setUser makes the user when it is not there, and sets its databases to exactly the list. No
// password keeps the password of the user.
func (m *manager) setUser(ctx context.Context, stdout io.Writer, req userRequest) error {
	if err := checkUser(req.User); err != nil {
		return err
	}
	if req.Password != "" && !passwordPattern.MatchString(req.Password) {
		return errors.New("the password is not valid: use 16 to 64 letters and digits")
	}
	if len(req.Databases) > 1000 {
		return errors.New("a user can have access to at most 1000 databases")
	}
	seen := map[string]bool{}
	var grants strings.Builder
	for _, a := range req.Databases {
		if err := checkName(a.Name); err != nil {
			return err
		}
		if seen[a.Name] {
			return fmt.Errorf("the database %s is in the list more than one time", a.Name)
		}
		seen[a.Name] = true
		p, err := privileges(a.Access)
		if err != nil {
			return err
		}
		fmt.Fprintf(&grants, "GRANT %s ON %s.* TO %s;\n", p, grantName(a.Name), account(req.User))
	}
	rows, err := m.query(ctx, fmt.Sprintf("SELECT COUNT(*) FROM mysql.user WHERE User = '%s' AND Host = '%%';\n", req.User))
	if err != nil {
		return err
	}
	exists := len(rows) == 1 && len(rows[0]) == 1 && rows[0][0] != "0"
	var sql strings.Builder
	switch {
	case !exists && req.Password == "":
		return fmt.Errorf("the user %s does not exist; give a password to make it", req.User)
	case !exists:
		fmt.Fprintf(&sql, "CREATE USER %s IDENTIFIED BY '%s';\n", account(req.User), req.Password)
	case req.Password != "":
		fmt.Fprintf(&sql, "ALTER USER %s IDENTIFIED BY '%s';\n", account(req.User), req.Password)
	}
	fmt.Fprintf(&sql, "REVOKE ALL PRIVILEGES, GRANT OPTION FROM %s;\n", account(req.User))
	sql.WriteString(grants.String())
	return m.exec(ctx, stdout, sql.String())
}

// restore replaces all data of a database with a dump from stdin. The database keeps the
// access of its users: MySQL keeps the grants of a database after DROP DATABASE.
func (m *manager) restore(ctx context.Context, stdout io.Writer, name string, dump io.Reader) error {
	if _, err := m.query(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %[1]s;\nCREATE DATABASE %[1]s CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n", ident(name))); err != nil {
		return err
	}
	err := m.withOptions(func(options string) error {
		return m.run.run(ctx, dump, io.Discard, "mysql", "--defaults-extra-file="+options, "--batch", "--default-character-set=utf8mb4", "--database="+name)
	})
	if err != nil {
		return err
	}
	return write(stdout, struct{}{})
}

func write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}
