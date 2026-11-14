package actions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// fakeValkey is a small Valkey server for the tests. It keeps the users, the number of keys of
// each database, and the commands that it got.
type fakeValkey struct {
	mu      sync.Mutex
	version string
	// admin gives the password of the user "default".
	admin func() string
	// passwords and rules of each user, without the administrator.
	passwords map[string]string
	rules     map[string][]string
	keys      map[int]int
	// commands has each command with its arguments, without the passwords.
	commands []string
	saves    int
}

func newFakeValkey(d *Deps) *fakeValkey {
	f := &fakeValkey{version: "9.1.3", passwords: map[string]string{}, rules: map[string][]string{}, keys: map[int]int{}}
	f.admin = func() string {
		b, _ := os.ReadFile(d.valkeySecretFile())
		return strings.TrimSpace(string(b))
	}
	// The tests never read or change the firewall file of the computer.
	if d.FirewallFile == "" {
		d.FirewallFile = filepath.Join(filepath.Dir(d.DBHome), "cloudfactory.nft")
	}
	d.ValkeyDial = func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go f.serve(server)
		return client, nil
	}
	return f
}

func (f *fakeValkey) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	user, db := "", 0
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		answer := f.answer(args, &user, &db)
		if _, err := io.WriteString(c, answer); err != nil {
			return
		}
	}
}

// readCommand reads one command: a list of strings.
func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		size, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		l, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(size, "$")))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, l+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:l]))
	}
	return args, nil
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func (f *fakeValkey) answer(args []string, user *string, db *int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := strings.ToUpper(args[0])
	if command == "AUTH" {
		f.commands = append(f.commands, "AUTH "+args[1])
		want, ok := f.passwords[args[1]]
		if args[1] == valkeyAdmin {
			want, ok = f.admin(), true
		}
		if !ok || want != args[2] {
			return "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
		}
		*user = args[1]
		return "+OK\r\n"
	}
	if *user != valkeyAdmin {
		return "-NOPERM this user has no permissions to run the command\r\n"
	}
	shown := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, ">") {
			a = ">PASSWORD"
		}
		shown = append(shown, a)
	}
	f.commands = append(f.commands, strings.Join(shown, " "))
	switch command + " " + strings.ToUpper(strings.Join(args[1:min(2, len(args))], "")) {
	case "ACL LIST":
		lines := []string{"user default on #0000 ~* &* +@all"}
		for _, name := range slices.Sorted(mapKeysOf(f.rules)) {
			lines = append(lines, "user "+name+" on #1111 "+strings.Join(f.rules[name], " "))
		}
		out := fmt.Sprintf("*%d\r\n", len(lines))
		for _, l := range lines {
			out += bulk(l)
		}
		return out
	case "ACL SETUSER":
		name := args[2]
		for _, rule := range args[3:] {
			switch {
			case rule == "reset":
				f.rules[name] = nil
				delete(f.passwords, name)
			case rule == "resetpass":
				delete(f.passwords, name)
			case strings.HasPrefix(rule, ">"):
				f.passwords[name] = rule[1:]
			case rule == "on":
			default:
				f.rules[name] = append(f.rules[name], rule)
			}
		}
		return "+OK\r\n"
	case "ACL DELUSER":
		delete(f.rules, args[2])
		delete(f.passwords, args[2])
		return ":1\r\n"
	case "ACL SAVE":
		f.saves++
		return "+OK\r\n"
	case "INFO SERVER":
		return bulk("# Server\r\nvalkey_version:" + f.version + "\r\nredis_version:7.2.4\r\n")
	case "INFO KEYSPACE":
		out := "# Keyspace\r\n"
		for _, n := range slices.Sorted(intKeysOf(f.keys)) {
			if f.keys[n] > 0 {
				out += fmt.Sprintf("db%d:keys=%d,expires=0,avg_ttl=0\r\n", n, f.keys[n])
			}
		}
		return bulk(out)
	case "CLIENT KILL":
		return ":0\r\n"
	}
	switch command {
	case "SELECT":
		*db, _ = strconv.Atoi(args[1])
		return "+OK\r\n"
	case "FLUSHDB":
		f.keys[*db] = 0
		return "+OK\r\n"
	case "PING":
		return "+PONG\r\n"
	}
	return "-ERR unknown command\r\n"
}

func mapKeysOf(m map[string][]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func intKeysOf(m map[int]int) func(func(int) bool) {
	return func(yield func(int) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (f *fakeValkey) got(command string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.commands, func(c string) bool { return strings.HasPrefix(c, command) })
}

func valkeyRequest(name string) *agentv1.CreateDatabaseRequest {
	return &agentv1.CreateDatabaseRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey", Name: name, User: name, Password: "Abcdefghijklmnop1234", Access: "website"}
}

// A Valkey database is a database number and a user that can use only that number.
func TestValkeyDatabaseGetsANumberAndAUser(t *testing.T) {
	d, f, _ := deps(t)
	d.ValkeyMemoryMB = 128
	d.ServiceImage = func(name string) string { return "ghcr.io/cloudfactory/" + name + ":0.1.0" }
	f.Rules = []execx.Rule{{Prefix: "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman image exists", Err: errors.New("no image")}}
	v := newFakeValkey(d)
	v.keys[1] = 5
	job, err := d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if n := job.Result()["number"]; n != 1 {
		t.Errorf("result number = %v, want 1", n)
	}
	rules := strings.Join(v.rules["acme_x7k2_cache"], " ")
	for _, want := range []string{"~*", "resetchannels &acme_x7k2_*", "db=1", "+@all -@admin -@dangerous", "+flushdb", "+info", "-function", "-script|flush", "-pubsub"} {
		if !strings.Contains(rules, want) {
			t.Errorf("the rules have no %q: %s", want, rules)
		}
	}
	for _, bad := range []string{"alldbs", "allchannels", "&*", "+flushall", "+config", "+client|kill", "+client|list", "+restore"} {
		if slices.Contains(v.rules["acme_x7k2_cache"], bad) {
			t.Errorf("the rules have %q: %s", bad, rules)
		}
	}
	if v.passwords["acme_x7k2_cache"] != "Abcdefghijklmnop1234" {
		t.Errorf("password = %q", v.passwords["acme_x7k2_cache"])
	}
	// The new database is empty, and Valkey writes its user file.
	if v.keys[1] != 0 || v.saves != 1 {
		t.Errorf("keys = %d, saves = %d", v.keys[1], v.saves)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "Abcdefghijklmnop1234") {
			t.Fatalf("the password is in the arguments: %s", c)
		}
	}
	for _, want := range []string{
		"runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman pull --quiet ghcr.io/cloudfactory/valkey:0.1.0",
		"install -d -o cf-db -g cf-db -m 0700 " + filepath.Join(d.DBHome, "valkey"),
		"chown cf-db:cf-db " + filepath.Join(d.DBHome, "valkey", "users.acl"),
		"systemctl --user --machine=cf-db@ restart cf-valkey.service",
	} {
		if !f.Called(want) {
			t.Errorf("no call %q in %v", want, f.Calls)
		}
	}
	u := unitFile(t, d, DBUser, "cf-valkey.container")
	for _, want := range []string{
		"PublishPort=169.254.77.1:6379:6379\n", "Volume=" + filepath.Join(d.DBHome, "valkey") + ":/data:Z\n",
		"Exec=valkey-server --bind 0.0.0.0 --port 6379 --databases 1024 --aclfile /data/users.acl --appendonly yes --save \"\" --maxmemory 128mb --maxmemory-policy allkeys-lru\n",
		"GlobalArgs=--runtime=/usr/local/bin/runsc ", "ReadOnly=true\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit has no %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "EnvironmentFile") {
		t.Errorf("the Valkey unit has an environment file:\n%s", u)
	}
	// The user file has only the hash of the password of the administrator.
	secret, err := os.ReadFile(d.valkeySecretFile())
	if err != nil || len(strings.TrimSpace(string(secret))) != 48 {
		t.Fatalf("secret: %q %v", secret, err)
	}
	if fi, _ := os.Stat(d.valkeySecretFile()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode of the secret = %v", fi.Mode().Perm())
	}
	users, _ := os.ReadFile(filepath.Join(d.DBHome, "valkey", "users.acl"))
	if !regexp.MustCompile(`^user default on #[0-9a-f]{64} ~\* &\* \+@all\n$`).Match(users) || strings.Contains(string(users), strings.TrimSpace(string(secret))) {
		t.Errorf("user file: %q", users)
	}
}

// The agent gives the smallest free number, and a removed database gives its number back.
func TestValkeyNumbersAreNotSharedAndComeBack(t *testing.T) {
	d, _, _ := deps(t)
	v := newFakeValkey(d)
	numbers := []int{}
	for _, name := range []string{"acme_x7k2_cache", "acme_x7k2_queue", "acme_x7k2_sessions"} {
		job, err := d.CreateDatabase(valkeyRequest(name))
		if err := run(t)(job, err); err != nil {
			t.Fatal(err)
		}
		numbers = append(numbers, job.Result()["number"].(int))
	}
	if !slices.Equal(numbers, []int{1, 2, 3}) {
		t.Fatalf("numbers = %v", numbers)
	}
	v.keys[2] = 9
	req := &agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey", Name: "acme_x7k2_queue", Users: []string{"acme_x7k2_queue"}}
	if err := run(t)(d.DeleteDatabase(req)); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.rules["acme_x7k2_queue"]; ok || v.keys[2] != 0 {
		t.Errorf("the user or the keys stay: %v, %d keys", v.rules, v.keys[2])
	}
	// A second removal does nothing: the database is gone.
	if err := run(t)(d.DeleteDatabase(req)); err != nil {
		t.Errorf("second removal: %v", err)
	}
	job, err := d.CreateDatabase(valkeyRequest("acme_x7k2_other"))
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if n := job.Result()["number"]; n != 2 {
		t.Errorf("the new database has number %v, want 2", n)
	}
}

func TestValkeyDatabaseThatExists(t *testing.T) {
	d, _, _ := deps(t)
	v := newFakeValkey(d)
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); err != nil {
		t.Fatal(err)
	}
	v.keys[1] = 3
	// The same job again, after a restart of the agent: the same number, and the data stays.
	job, err := d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))
	if err := run(t)(job, err); err != nil {
		t.Fatalf("the same job again: %v", err)
	}
	if job.Result()["number"] != 1 || v.keys[1] != 3 {
		t.Errorf("number = %v, keys = %d", job.Result()["number"], v.keys[1])
	}
	// A different job with the same name fails.
	other := valkeyRequest("acme_x7k2_cache")
	other.Password = "Zyxwvutsrqponmlk9876"
	if err := run(t)(d.CreateDatabase(other)); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("err = %v", err)
	}
	if v.passwords["acme_x7k2_cache"] != "Abcdefghijklmnop1234" {
		t.Error("the password of the database changed")
	}
}

func TestValkeyRefusesBadRequests(t *testing.T) {
	d, _, _ := deps(t)
	newFakeValkey(d)
	for name, change := range map[string]func(*agentv1.CreateDatabaseRequest){
		"no user":           func(r *agentv1.CreateDatabaseRequest) { r.User = "" },
		"a different user":  func(r *agentv1.CreateDatabaseRequest) { r.User = "acme_x7k2_other" },
		"a foreign name":    func(r *agentv1.CreateDatabaseRequest) { r.Name, r.User = "other_a1b2_cache", "other_a1b2_cache" },
		"a short password":  func(r *agentv1.CreateDatabaseRequest) { r.Password = "short" },
		"a password rule":   func(r *agentv1.CreateDatabaseRequest) { r.Password = "Abcdefghijklmnop +@all" },
		"access from all":   func(r *agentv1.CreateDatabaseRequest) { r.Access = "anywhere" },
		"an unknown engine": func(r *agentv1.CreateDatabaseRequest) { r.Engine = "redis" },
	} {
		req := valkeyRequest("acme_x7k2_cache")
		change(req)
		if _, err := d.CreateDatabase(req); err == nil {
			t.Errorf("%s: the agent accepted the request", name)
		}
	}
	user := &agentv1.SetDatabaseUserRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey", Name: "acme_x7k2_two", Password: "Abcdefghijklmnop1234", Create: true}
	if _, err := d.SetDatabaseUser(user); err == nil {
		t.Error("the agent made a second Valkey user")
	}
	if _, err := d.DeleteDatabaseUser(&agentv1.DeleteDatabaseUserRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey", Name: "acme_x7k2_cache"}); err == nil {
		t.Error("the agent removed a Valkey user without its database")
	}
	// A backup, a restore and a staging copy have no Valkey data.
	backup := &agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Target: "s3:bucket", Kind: "manual", Databases: []*agentv1.DatabaseRef{{Engine: "valkey", Name: "acme_x7k2_cache"}}}
	if _, err := d.Backup(backup); !errors.Is(err, errValkeyNoCopy) {
		t.Errorf("backup: %v", err)
	}
	restore := &agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Target: "s3:bucket", SnapshotId: "0123abcd", Scope: "database", Item: "valkey:acme_x7k2_cache", DumpSha256: strings.Repeat("a", 64)}
	if _, err := d.Restore(restore); !errors.Is(err, errValkeyNoCopy) {
		t.Errorf("restore: %v", err)
	}
	copyReq := &agentv1.WebsiteRequest{CopyFrom: "01JBQ3Z5K8N6T4W2X9Y7R5P3M9", CopyFromUnixUser: "acme_x7k2", Website: &agentv1.Website{UnixUser: "acme_s1t2"},
		CopyDatabases: []*agentv1.DatabaseCopy{{Engine: "valkey", From: "acme_x7k2_cache", To: "acme_s1t2_cache"}}}
	if err := checkCopies(copyReq); !errors.Is(err, errValkeyNoCopy) {
		t.Errorf("staging copy: %v", err)
	}
}

func TestValkeyNewPasswordClosesTheOldConnections(t *testing.T) {
	d, _, _ := deps(t)
	v := newFakeValkey(d)
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); err != nil {
		t.Fatal(err)
	}
	rules := slices.Clone(v.rules["acme_x7k2_cache"])
	req := &agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey", Name: "acme_x7k2_cache", Password: "Zyxwvutsrqponmlk9876", Access: "website",
		Grants: []*agentv1.DatabaseGrant{{Database: "acme_x7k2_cache", Privileges: "readwrite"}}, WebsiteDatabases: []string{"acme_x7k2_cache"}}
	if err := run(t)(d.SetDatabaseUser(req)); err != nil {
		t.Fatal(err)
	}
	if v.passwords["acme_x7k2_cache"] != "Zyxwvutsrqponmlk9876" || !slices.Equal(v.rules["acme_x7k2_cache"], rules) {
		t.Errorf("password = %q, rules = %v", v.passwords["acme_x7k2_cache"], v.rules["acme_x7k2_cache"])
	}
	if !v.got("ACL SETUSER acme_x7k2_cache resetpass >PASSWORD") || !v.got("CLIENT KILL USER acme_x7k2_cache") || v.saves != 2 {
		t.Errorf("commands = %v, saves = %d", v.commands, v.saves)
	}
	// A user that does not exist gets no password.
	req.Name = "acme_x7k2_none"
	if err := run(t)(d.SetDatabaseUser(req)); err == nil {
		t.Error("the agent set the password of a user that does not exist")
	}
	// A request with no password changes nothing.
	req.Name, req.Password = "acme_x7k2_cache", ""
	n := len(v.commands)
	if err := run(t)(d.SetDatabaseUser(req)); err != nil || len(v.commands) != n {
		t.Errorf("err = %v, new commands = %v", err, v.commands[n:])
	}
}

// A release before 9.1.2 cannot keep a user in one database. The agent does not use it.
func TestValkeyRefusesAnOldRelease(t *testing.T) {
	for version, ok := range map[string]bool{"9.0.3": false, "9.1.1": false, "9.1.2": true, "9.2.0": true, "10.0.0": true} {
		d, _, _ := deps(t)
		d.ReadyTimeout = time.Nanosecond
		v := newFakeValkey(d)
		v.version = version
		err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache")))
		if (err == nil) != ok {
			t.Errorf("release %s: err = %v", version, err)
		}
		if !ok && len(v.rules) != 0 {
			t.Errorf("release %s: the agent made a user", version)
		}
	}
}

func TestDeleteWebsiteRemovesItsValkeyDatabases(t *testing.T) {
	d, _, _ := deps(t)
	v := newFakeValkey(d)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); err != nil {
		t.Fatal(err)
	}
	v.keys[1] = 4
	req := &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2",
		Databases: []*agentv1.DatabaseRef{{Engine: "valkey", Name: "acme_x7k2_cache"}}, DatabaseUsers: []*agentv1.DatabaseRef{{Engine: "valkey", Name: "acme_x7k2_cache"}}}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	if len(v.rules) != 0 || v.keys[1] != 0 {
		t.Errorf("users = %v, keys = %d", v.rules, v.keys[1])
	}
}

// A website from an earlier release has no host name for Valkey. The first Valkey database
// writes its unit again and starts the container again. A later database does not.
func TestValkeyGivesTheHostNameToAnOldWebsite(t *testing.T) {
	d, f, _ := deps(t)
	newFakeValkey(d)
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	dir, _ := d.Host.UnitDir("acme_x7k2")
	path := filepath.Join(dir, "cf-acme_x7k2.container")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(b), "AddHost=valkey.internal:169.254.77.1\n", "", 1)
	if old == string(b) {
		t.Fatalf("the unit of a new website has no Valkey host name:\n%s", b)
	}
	os.WriteFile(path, []byte(old), 0o644)
	restart := "systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service"
	count := func() (n int) {
		for _, c := range f.Calls {
			if c == restart {
				n++
			}
		}
		return n
	}
	before := count()
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); err != nil {
		t.Fatal(err)
	}
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"); !strings.Contains(u, "AddHost=valkey.internal:169.254.77.1\n") || count() != before+1 {
		t.Errorf("restarts = %d, want %d; unit:\n%s", count(), before+1, u)
	}
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_queue"))); err != nil {
		t.Fatal(err)
	}
	if count() != before+1 {
		t.Errorf("the second database started the container again: %d restarts", count()-before)
	}
}

func TestValkeyReportHasTheNumberAndTheKeys(t *testing.T) {
	d, _, _ := deps(t)
	v := newFakeValkey(d)
	for _, name := range []string{"acme_x7k2_cache", "acme_x7k2_queue"} {
		if err := run(t)(d.CreateDatabase(valkeyRequest(name))); err != nil {
			t.Fatal(err)
		}
	}
	v.keys[2] = 42
	sizes := d.databaseSizes(context.Background())
	if len(sizes) != 2 {
		t.Fatalf("sizes = %+v", sizes)
	}
	for i, want := range []struct {
		name   string
		number int
		keys   int64
	}{{"acme_x7k2_cache", 1, 0}, {"acme_x7k2_queue", 2, 42}} {
		s := sizes[i]
		if s.Engine != "valkey" || s.Name != want.name || s.Number != want.number || s.Keys == nil || *s.Keys != want.keys || s.SizeBytes != 0 {
			t.Errorf("size %d = %+v, want %+v", i, s, want)
		}
	}
}

func TestValkeyPortInTheFirewall(t *testing.T) {
	old := "    ct state new ip daddr 169.254.77.1 tcp dport != { 3306, 5432, 19432 } meta skuid != { 0, 991 } reject with tcp reset\n" +
		"  chain websites {\n    ip daddr 169.254.77.1 tcp dport { 3306, 5432 } accept\n"
	want := "    ct state new ip daddr 169.254.77.1 tcp dport != { 3306, 5432, 6379, 19432 } meta skuid != { 0, 991 } reject with tcp reset\n" +
		"  chain websites {\n    ip daddr 169.254.77.1 tcp dport { 3306, 5432, 6379 } accept\n"
	if got, ok := addValkeyPort(old); !ok || got != want {
		t.Errorf("ok = %v, file:\n%s", ok, got)
	}
	if got, ok := addValkeyPort(want); !ok || got != want {
		t.Errorf("a file of this release changed: ok = %v, file:\n%s", ok, got)
	}
	if _, ok := addValkeyPort("table inet filter {}\n"); ok {
		t.Error("a file with no database rule is OK")
	}

	d, f, _ := deps(t)
	newFakeValkey(d)
	d.FirewallFile = filepath.Join(t.TempDir(), "cloudfactory.nft")
	os.WriteFile(d.FirewallFile, []byte(old), 0o644)
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(d.FirewallFile); string(b) != want || !f.Called("nft -f "+d.FirewallFile) {
		t.Errorf("firewall file:\n%s\ncalls: %v", b, f.Calls)
	}
	// A firewall file that the agent cannot change stops the job, because the websites could
	// not connect to Valkey.
	d, _, _ = deps(t)
	v := newFakeValkey(d)
	d.ReadyTimeout = time.Nanosecond
	d.FirewallFile = filepath.Join(t.TempDir(), "cloudfactory.nft")
	os.WriteFile(d.FirewallFile, []byte("table inet cloudfactory {}\n"), 0o644)
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); err == nil || !strings.Contains(err.Error(), "run the installer again") || len(v.rules) != 0 {
		t.Errorf("err = %v, users = %v", err, v.rules)
	}
}

func TestParseValkeyUser(t *testing.T) {
	for line, want := range map[string]struct {
		name    string
		numbers []int
	}{
		"user acme_x7k2_cache on #5e88 ~* resetchannels &acme_x7k2_* db=7 +@all -@admin": {"acme_x7k2_cache", []int{7}},
		"user alice on nopass ~* resetchannels db=0,1 +@all":                             {"alice", []int{0, 1}},
		"user bob on nopass resetchannels -@all (~* resetchannels db=3 -@all +@write)":   {"bob", []int{3}},
		"user default on #0000 ~* &* +@all":                                              {"default", nil},
		"not a user line":                                                                {"", nil},
	} {
		name, numbers := parseValkeyUser(line)
		if name != want.name || !slices.Equal(numbers, want.numbers) {
			t.Errorf("%q: %q %v, want %q %v", line, name, numbers, want.name, want.numbers)
		}
	}
}

func TestValkeyAnswers(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for _, answer := range []string{"+OK\r\n", ":42\r\n", "$-1\r\n", "$5\r\nab\r\nc\r\n", "*2\r\n$1\r\na\r\n*1\r\n:7\r\n", "-NOPERM no\r\n", "?\r\n"} {
			if _, err := readCommand(r); err != nil {
				return
			}
			io.WriteString(server, answer)
		}
	}()
	v := &valkeyConn{c: client, r: bufio.NewReader(client)}
	for i, want := range []any{"OK", int64(42), nil, "ab\r\nc", []any{"a", []any{int64(7)}}} {
		got, err := v.do("PING")
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("answer %d = %v %v, want %v", i, got, err, want)
		}
	}
	var failure valkeyError
	if _, err := v.do("PING"); !errors.As(err, &failure) || failure != "NOPERM no" {
		t.Errorf("error answer = %v", err)
	}
	if _, err := v.do("PING"); err == nil {
		t.Error("an unknown answer gave no error")
	}
}

func TestValkeyMemory(t *testing.T) {
	for total, want := range map[int]int{0: 64, 256: 64, 2048: 256, 4096: 512, 16384: 1024, 262144: 1024} {
		if got := defaultValkeyMemoryMB(total); got != want {
			t.Errorf("%d MB: %d, want %d", total, got, want)
		}
	}
	path := filepath.Join(t.TempDir(), "meminfo")
	os.WriteFile(path, []byte("MemTotal:        4000000 kB\nMemFree:  100 kB\n"), 0o644)
	if got := hostMemoryMB(path); got != 3906 {
		t.Errorf("memory = %d MB", got)
	}
	if got := hostMemoryMB(filepath.Join(t.TempDir(), "none")); got != 0 {
		t.Errorf("memory with no file = %d", got)
	}
	if args := strings.Join(quadlet.ValkeyArgs(quadlet.Database{}), " "); !strings.Contains(args, "--maxmemory 256mb") {
		t.Errorf("args = %s", args)
	}
}
