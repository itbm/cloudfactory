package actions

// Parity tests: they record what the agent does for each operation of each database engine and
// of each runtime, and compare it with the record in testdata/parity. The record has each
// command, its stdin and its environment, the files that the agent writes, the step names and
// the result of each job, and the commands that a Valkey server gets.
//
// The records were made from the code before the engines and the runtimes moved behind the
// driver interfaces. A change of a record is a change of the behaviour of the agent: look at
// the difference before you accept it. To write the records again:
//
//	go test ./internal/actions -run Parity -update-parity

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"google.golang.org/protobuf/proto"
)

var updateParity = flag.Bool("update-parity", false, "write the records of the parity tests again")

// parityRecord collects the record of one parity test.
type parityRecord struct {
	t   *testing.T
	dir string
	b   strings.Builder
}

func newParityRecord(t *testing.T, d *Deps) *parityRecord {
	// All directories of the test are in one temporary directory: the parent of DataDir.
	return &parityRecord{t: t, dir: filepath.Dir(d.DataDir)}
}

func (p *parityRecord) section(name string) { fmt.Fprintf(&p.b, "\n== %s\n", name) }

func (p *parityRecord) line(format string, args ...any) {
	fmt.Fprintf(&p.b, format+"\n", args...)
}

// calls adds the commands of the fake runner since the last call, and clears them.
func (p *parityRecord) calls(f *execx.Fake) {
	for i, c := range f.Calls {
		p.line("$ %s", c)
		if i < len(f.Envs) && len(f.Envs[i]) > 0 {
			p.line("  env: %s", strings.Join(f.Envs[i], " "))
		}
		if i < len(f.Inputs) && f.Inputs[i] != "" {
			for _, l := range strings.Split(strings.TrimRight(f.Inputs[i], "\n"), "\n") {
				p.line("  | %s", l)
			}
		}
	}
	f.Calls, f.Inputs, f.Envs = nil, nil, nil
}

// job adds the step names of a job, runs it, and adds the error and the result.
func (p *parityRecord) job(f *execx.Fake, name string) func(jobs.Job, error) {
	return func(job jobs.Job, err error) { p.runJob(f, name, job, err) }
}

func (p *parityRecord) runJob(f *execx.Fake, name string, job jobs.Job, err error) {
	p.section(name)
	if err != nil {
		p.line("refused: %v", err)
		return
	}
	steps := make([]string, 0, len(job.Steps))
	for _, s := range job.Steps {
		steps = append(steps, s.Name)
	}
	p.line("steps: %s", strings.Join(steps, " | "))
	p.line("long: %v", job.Long)
	if err := runSteps(p.t, job, nil); err != nil {
		p.line("error: %v", err)
	}
	p.calls(f)
	if job.Result != nil {
		res := job.Result()
		keys := make([]string, 0, len(res))
		for k := range res {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p.line("result %s: %v", k, res[k])
		}
	}
}

// files adds each file below the directories of the test, with its content.
func (p *parityRecord) files() {
	p.section("files")
	var paths []string
	_ = filepath.WalkDir(p.dir, func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	slices.Sort(paths)
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, _ := os.Stat(path)
		p.line("--- %s (%v)", path, info.Mode().Perm())
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			p.line("  %s", l)
		}
	}
}

// Values that change in each run: random passwords and names, and the temporary directory.
var (
	parityHex    = regexp.MustCompile(`\b[0-9a-f]{16,}\b`)
	parityRandom = regexp.MustCompile(`(cfrestore_|cfdefiner_|cfcopy_)[0-9a-z]+`)
	paritySecret = regexp.MustCompile(`/(env|secret)-[0-9]+`)
)

func (p *parityRecord) text() string {
	s := strings.ReplaceAll(p.b.String(), p.dir, "$DIR")
	s = parityRandom.ReplaceAllString(s, "${1}RANDOM")
	s = paritySecret.ReplaceAllString(s, "/${1}-RANDOM")
	return parityHex.ReplaceAllStringFunc(s, func(h string) string {
		// The IDs of the tests are not random. They are ULIDs in lower case: 26 characters
		// with letters that are not hexadecimal digits, so this pattern does not take them.
		return fmt.Sprintf("HEX%d", len(h))
	})
}

// check compares the record with its file in testdata/parity.
func (p *parityRecord) check(name string) {
	p.t.Helper()
	path := filepath.Join("testdata", "parity", name+".txt")
	got := p.text()
	if *updateParity {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			p.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			p.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		p.t.Fatalf("no record %s: %v (run with -update-parity to make it)", path, err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < max(len(gl), len(wl)); i++ {
			g, w := "", ""
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				p.t.Fatalf("the behaviour of %s changed at line %d of %s:\nwant: %s\ngot:  %s", name, i+1, path, w, g)
			}
		}
	}
}

// parityDatabaseSQLEngines are the engines with SQL, dumps and users.
var paritySQLEngines = []string{"mariadb", "postgresql"}

// paritySite makes the website of the tests, with its app directory, and clears the commands.
func paritySite(t *testing.T) (*Deps, *execx.Fake) {
	d, f, _ := deps(t)
	users(d)
	if err := os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	f.Calls, f.Inputs, f.Envs = nil, nil, nil
	return d, f
}

func TestParityOfTheSQLEngines(t *testing.T) {
	for _, engine := range paritySQLEngines {
		t.Run(engine, func(t *testing.T) {
			d, f := paritySite(t)
			p := newParityRecord(t, d)
			server := databaseServer(engine)
			serverID := server.GetId()
			key := strings.ToLower(serverID)
			pass := "Abcdefghijklmnop1234"
			// The answers of the database server to the queries of the agent.
			f.Rules = []execx.Rule{
				{Prefix: execOf(engine, key), Input: "SELECT Host FROM mysql.user WHERE User = 'acme_x7k2_app'", Output: "169.254.80.1\n"},
				{Prefix: execOf(engine, key), Input: "SELECT datname FROM pg_database", Output: "acme_x7k2_shop\nacme_x7k2_blog\n"},
				{Prefix: execOf(engine, key), Input: "SELECT 1 FROM pg_roles WHERE rolname = 'acme_x7k2_old'", Output: "1\n"},
				{Prefix: execOf(engine, key), Input: "SELECT 1 FROM pg_roles WHERE rolname = 'acme_x7k2_blog:rw'", Output: "1\n"},
				{Prefix: execOf(engine, key), Input: "information_schema.tables", Output: "acme_x7k2_shop\t4096\nacme_x7k2_blog\t8192\n"},
				{Prefix: execOf(engine, key), Input: "pg_database_size", Output: "acme_x7k2_shop|4096\nacme_x7k2_blog|8192\n"},
			}

			for _, name := range []string{"acme_x7k2_shop", "acme_x7k2_blog"} {
				p.job(f, "CreateDatabase "+name)(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
					Name: name, User: name, Password: pass, Access: "website", DatabaseServer: server}))
			}
			p.job(f, "CreateDatabase with no user")(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
				Name: "acme_x7k2_logs", DatabaseServer: server}))
			p.job(f, "SetDatabaseUser create")(d.SetDatabaseUser(&agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
				Name: "acme_x7k2_app", Password: pass, Access: "website", Create: true, DatabaseServer: server,
				WebsiteDatabases: []string{"acme_x7k2_shop", "acme_x7k2_blog"},
				Grants:           []*agentv1.DatabaseGrant{{Database: "acme_x7k2_shop", Privileges: "readwrite"}, {Database: "acme_x7k2_blog", Privileges: "readonly"}}}))
			p.job(f, "SetDatabaseUser change")(d.SetDatabaseUser(&agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
				Name: "acme_x7k2_app", Password: pass, Access: "anywhere", DatabaseServer: server,
				WebsiteDatabases: []string{"acme_x7k2_shop", "acme_x7k2_blog"},
				Grants:           []*agentv1.DatabaseGrant{{Database: "acme_x7k2_blog", Privileges: "readwrite"}}}))
			p.job(f, "SetDatabaseUser bad privileges")(d.SetDatabaseUser(&agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
				Name: "acme_x7k2_app", DatabaseServer: server, WebsiteDatabases: []string{"acme_x7k2_shop"},
				Grants: []*agentv1.DatabaseGrant{{Database: "acme_x7k2_shop", Privileges: "all"}}}))
			p.job(f, "DeleteDatabaseUser")(d.DeleteDatabaseUser(&agentv1.DeleteDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
				Name: "acme_x7k2_old", WebsiteDatabases: []string{"acme_x7k2_shop"}, DatabaseServer: serverID, Customer: customerID}))

			// Backup and restore of one database.
			dump := "DUMP OF acme_x7k2_shop\n"
			sum := sha256.Sum256([]byte(dump))
			staged := filepath.Join(d.DataDir, "restore", jobID, d.DataDir, "dumps", "acme_x7k2", engine+"-acme_x7k2_shop.dump")
			f.Rules = append([]execx.Rule{
				{Prefix: podmanOf(key) + "exec cf-" + engine + "-" + key + " ", Output: dump},
				{Prefix: "/usr/local/bin/cf-agent jail --ro ", Output: `{"message_type":"summary","snapshot_id":"1a2b3c4d","total_bytes_processed":2048}`},
				{Prefix: "restic --repo s3:b/c --password-file  --json --retry-lock 30m restore", Do: func() error { return writeFile(staged, []byte(dump)) }},
				{Prefix: "restic --repo s3:b/c --password-file  --json --retry-lock 30m forget", Output: "[]"},
				ownSnapshot,
			}, f.Rules...)
			p.job(f, "Backup")(d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3:b/c",
				Databases: []*agentv1.DatabaseRef{{Engine: engine, Name: "acme_x7k2_shop", DatabaseServer: serverID}},
				Retention: &agentv1.Retention{Last: 3}}))
			p.job(f, "Backup of the databases only")(d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "scheduled", Target: "s3:b/c",
				Databases: []*agentv1.DatabaseRef{{Engine: engine, Name: "acme_x7k2_shop", DatabaseServer: serverID}}, DatabasesOnly: true, Customer: customerID}))
			p.job(f, "Restore")(d.Restore(&agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "database",
				Item: engine + ":acme_x7k2_shop", Target: "s3:b/c", DumpSha256: hex.EncodeToString(sum[:]), DatabaseServer: serverID}))
			p.job(f, "Restore with a wrong hash")(d.Restore(&agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "database",
				Item: engine + ":acme_x7k2_shop", Target: "s3:b/c", DumpSha256: strings.Repeat("0", 64), DatabaseServer: serverID}))

			p.section("databaseSizes")
			for _, s := range d.databaseSizes(t.Context()) {
				p.line("%s %s %d", s.Engine, s.Name, s.SizeBytes)
			}
			p.calls(f)

			p.job(f, "DeleteDatabase")(d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: engine,
				Name: "acme_x7k2_blog", Users: []string{"acme_x7k2_app", "acme_x7k2_blog"}, DatabaseServer: serverID, Customer: customerID}))

			p.section("move secret")
			s := dbServer{ID: key, Engine: engine}
			p.line("path: %s", d.secretPath(s))
			for _, secret := range []string{
				"MARIADB_ROOT_PASSWORD=00112233\nMYSQL_PWD=00112233\n", "POSTGRES_PASSWORD=00112233\n", "00112233\n",
				"MYSQL_PWD=00112233\n", "POSTGRES_PASSWORD=0011\nOTHER=1\n", "",
			} {
				p.line("%q: %v", secret, checkSecret(engine, []byte(secret)))
			}
			p.files()
			p.check("engine-" + engine)
		})
	}
}

// A copy of a website copies the databases of each SQL engine, and makes the users.
func TestParityOfACopyOfAWebsite(t *testing.T) {
	for _, engine := range paritySQLEngines {
		t.Run(engine, func(t *testing.T) {
			d, f, _ := deps(t)
			p := newParityRecord(t, d)
			const source = "01JBQ3Z5K8N6T4W2X9Y7R5P3M9"
			if _, err := d.useDatabases(customerKey, source); err != nil {
				t.Fatal(err)
			}
			server := databaseServer(engine)
			w := website(runtimeImage)
			w.UnixUser = "staging_ab12"
			p.job(f, "CreateWebsite with a copy")(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w, CopyFrom: source, CopyFromUnixUser: "acme_x7k2",
				CopyDatabases:   []*agentv1.DatabaseCopy{{Engine: engine, From: "acme_x7k2_shop", To: "staging_ab12_shop", FromServer: server.GetId(), ToServer: server.GetId()}},
				CopyUsers:       []*agentv1.DatabaseUserCopy{{Engine: engine, Name: "staging_ab12_app", Password: "Abcdefgh12345678", DatabaseServer: server.GetId()}},
				DatabaseServers: []*agentv1.DatabaseServer{server}}))
			p.files()
			p.check("copy-" + engine)
		})
	}
}

// Valkey has numbered databases, one user for each database, and no SQL or dumps.
func TestParityOfValkey(t *testing.T) {
	d, f := paritySite(t)
	fv := newFakeValkey(d)
	p := newParityRecord(t, d)
	server := databaseServer("valkey")
	pass := "Abcdefghijklmnop1234"
	valkeyCommands := func() {
		fv.mu.Lock()
		defer fv.mu.Unlock()
		for _, c := range fv.commands {
			p.line("valkey> %s", c)
		}
		fv.commands = nil
	}
	for _, name := range []string{"acme_x7k2_cache", "acme_x7k2_jobs"} {
		p.job(f, "CreateDatabase "+name)(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
			Name: name, User: name, Password: pass, Access: "website", DatabaseServer: server}))
		valkeyCommands()
	}
	p.job(f, "CreateDatabase with a different user")(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
		Name: "acme_x7k2_x", User: "acme_x7k2_y", Password: pass, DatabaseServer: server}))
	p.job(f, "CreateDatabase for anywhere")(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
		Name: "acme_x7k2_x", User: "acme_x7k2_x", Password: pass, Access: "anywhere", DatabaseServer: server}))
	p.job(f, "SetDatabaseUser password")(d.SetDatabaseUser(&agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
		Name: "acme_x7k2_cache", Password: "Zyxwvutsrqponmlk9876", DatabaseServer: server}))
	valkeyCommands()
	p.job(f, "SetDatabaseUser create")(d.SetDatabaseUser(&agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
		Name: "acme_x7k2_cache", Password: pass, Create: true, DatabaseServer: server}))
	p.job(f, "DeleteDatabaseUser")(d.DeleteDatabaseUser(&agentv1.DeleteDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
		Name: "acme_x7k2_cache", DatabaseServer: server.GetId(), Customer: customerID}))
	p.job(f, "Backup")(d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3:b/c",
		Databases: []*agentv1.DatabaseRef{{Engine: "valkey", Name: "acme_x7k2_cache", DatabaseServer: server.GetId()}}}))
	p.job(f, "Restore")(d.Restore(&agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "database",
		Item: "valkey:acme_x7k2_cache", Target: "s3:b/c", DumpSha256: strings.Repeat("0", 64), DatabaseServer: server.GetId()}))

	p.section("databaseSizes")
	for _, s := range d.databaseSizes(t.Context()) {
		p.line("%s %s %d", s.Engine, s.Name, s.SizeBytes)
	}
	p.calls(f)
	valkeyCommands()

	p.job(f, "DeleteDatabase")(d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
		Name: "acme_x7k2_jobs", Users: []string{"acme_x7k2_jobs"}, DatabaseServer: server.GetId(), Customer: customerID}))
	valkeyCommands()

	p.section("move secret")
	s := dbServer{ID: valkeyServerKey, Engine: "valkey"}
	p.line("path: %s", d.secretPath(s))
	for _, secret := range []string{strings.Repeat("ab", 24) + "\n", "short\n", "POSTGRES_PASSWORD=00112233\n", ""} {
		p.line("%q: %v", secret, checkSecret("valkey", []byte(secret)))
	}
	p.line("unknown engine: %v", checkSecret("mongodb", []byte("x\n")))
	p.files()
	p.check("engine-valkey")
}

// The container of a website for each runtime: the unit, the php.ini file, the environment
// and the commands.
func TestParityOfTheRuntimes(t *testing.T) {
	cases := map[string]func(w *agentv1.Website){
		"frankenphp": func(w *agentv1.Website) {},
		"frankenphp-worker-mode": func(w *agentv1.Website) {
			w.WorkerMode = true
		},
		"apache-php-fpm": func(w *agentv1.Website) {
			w.Runtime, w.Image = "apache-php-fpm", "ghcr.io/cloudfactory/apache-php8.4:8.4.0-1"
		},
		"node": func(w *agentv1.Website) {
			w.Runtime, w.Image, w.Extensions, w.PhpIni = "node", "ghcr.io/cloudfactory/node22:22.11.0-1", nil, nil
			w.Command = []string{"npm", "start"}
			w.DocumentRoot = "."
		},
		"python": func(w *agentv1.Website) {
			w.Runtime, w.Image, w.Extensions, w.PhpIni = "python", "ghcr.io/cloudfactory/python3.13:3.13.0-1", nil, nil
			w.Command = []string{"python", "app.py"}
			w.DocumentRoot = "src"
		},
		"custom": func(w *agentv1.Website) {
			w.Runtime, w.Image, w.Extensions, w.PhpIni = "custom", "registry.example/app:1.0", nil, nil
			w.ContainerPort, w.Command = 3000, []string{"/srv/app", "--serve"}
		},
		"worker": func(w *agentv1.Website) {
			w.Worker, w.Command, w.Domains = true, []string{"php", "artisan", "queue:work"}, nil
		},
		"node-with-no-command": func(w *agentv1.Website) {
			w.Runtime, w.Image, w.Extensions, w.PhpIni = "node", "ghcr.io/cloudfactory/node22:22.11.0-1", nil, nil
		},
		"unknown-runtime": func(w *agentv1.Website) {
			w.Runtime = "golang"
		},
	}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range cases {
			if !yield(k) {
				return
			}
		}
	}) {
		t.Run(name, func(t *testing.T) {
			d, f, _ := deps(t)
			users(d)
			p := newParityRecord(t, d)
			w := customerWebsite()
			w.Environment = map[string]string{"APP_ENV": "production"}
			cases[name](w)
			if err := os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755); err != nil {
				t.Fatal(err)
			}
			p.job(f, "CreateWebsite")(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w}))
			changed := proto.Clone(w).(*agentv1.Website)
			changed.Limits.MemoryBytes = 2 << 30
			p.job(f, "SetRuntime")(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: changed}))
			p.files()
			p.check("runtime-" + name)
		})
	}
}

// A copy of a website gets a new, empty Valkey database for each Valkey user of the source.
func TestParityOfACopyWithValkey(t *testing.T) {
	d, f, _ := deps(t)
	fv := newFakeValkey(d)
	p := newParityRecord(t, d)
	const source = "01JBQ3Z5K8N6T4W2X9Y7R5P3M9"
	if _, err := d.useDatabases(customerKey, source); err != nil {
		t.Fatal(err)
	}
	server := databaseServer("valkey")
	w := website(runtimeImage)
	w.UnixUser = "staging_ab12"
	p.job(f, "CreateWebsite with a copy")(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w, CopyFrom: source, CopyFromUnixUser: "acme_x7k2",
		CopyUsers:       []*agentv1.DatabaseUserCopy{{Engine: "valkey", Name: "staging_ab12_cache", Password: "Abcdefgh12345678", DatabaseServer: server.GetId()}},
		DatabaseServers: []*agentv1.DatabaseServer{server}}))
	p.job(f, "CreateWebsite with a copy of a Valkey database")(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID2, Website: w, CopyFrom: source, CopyFromUnixUser: "acme_x7k2",
		CopyDatabases:   []*agentv1.DatabaseCopy{{Engine: "valkey", From: "acme_x7k2_cache", To: "staging_ab12_cache", FromServer: server.GetId(), ToServer: server.GetId()}},
		DatabaseServers: []*agentv1.DatabaseServer{server}}))
	fv.mu.Lock()
	for _, c := range fv.commands {
		p.line("valkey> %s", c)
	}
	fv.mu.Unlock()
	p.files()
	p.check("copy-valkey")
}

// A runtime from a manifest (plugins/runtimes/golang) runs as Node.js and Python run: the
// start command of the customer through cf-app. With no manifest, the agent refuses it
// (runtime-unknown-runtime).
func TestARuntimeFromAManifest(t *testing.T) {
	manifests := t.TempDir()
	b, err := os.ReadFile("../../../plugins/runtimes/golang/runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifests, "golang.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := quadlet.LoadRuntimeManifests(manifests); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { quadlet.LoadRuntimeManifests("") })

	d, f, _ := deps(t)
	users(d)
	p := newParityRecord(t, d)
	w := customerWebsite()
	w.Runtime, w.Image, w.Extensions, w.PhpIni = "golang", "ghcr.io/cloudfactory/golang1.25:1.25.0-1", nil, nil
	w.Command, w.DocumentRoot = []string{"./server"}, "."
	if err := os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.job(f, "CreateWebsite")(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w}))
	p.files()
	p.check("runtime-golang-from-a-manifest")

	unit := p.text()
	for _, want := range []string{"Environment=CF_APP_DIR=/app", "Environment=PORT=8080", `Exec="./server"`} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit of the Go website has no %q", want)
		}
	}
	if strings.Contains(unit, "CF_DOCUMENT_ROOT") || strings.Contains(unit, "zz-cloudfactory.ini") {
		t.Error("the Go website has the lines of a PHP runtime")
	}

	w.Command = nil
	if err := CheckWebsite(w); err == nil || !strings.Contains(err.Error(), "a golang website needs a start command") {
		t.Errorf("CheckWebsite with no start command = %v", err)
	}
}
