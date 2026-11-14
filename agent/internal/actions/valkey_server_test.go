package actions

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// These tests use a real Valkey server. The image workflow starts the Valkey image with the
// options of its unit, and gives the address of the server in CF_TEST_VALKEY and the password
// of its administrator in CF_TEST_VALKEY_PASSWORD. With no address, the tests do nothing.
//
// The tests are the proof that a website can use only its own Valkey database: the unit tests
// use a server of the tests, which does not know the rules of Valkey.

const (
	valkeyKeepName     = "keep_k1p2_cache"
	valkeyKeepPassword = "Keepkeepkeepkeep1234"
)

// valkeyServer gives the dependencies for a real server, or skips the test.
func valkeyServer(t *testing.T) *Deps {
	t.Helper()
	address := os.Getenv("CF_TEST_VALKEY")
	if address == "" {
		t.Skip("CF_TEST_VALKEY is not set: no real Valkey server")
	}
	d, _, _ := deps(t)
	// The server of the tests is the Valkey container of one customer. The agent connects to
	// the address of the test, not to the address of the customer.
	d.ValkeyDial = func(ctx context.Context, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", address)
	}
	if _, err := d.useDatabases(customerKey, websiteID, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.dbDir(customerValkey), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.valkeySecretFile(customerValkey), []byte(os.Getenv("CF_TEST_VALKEY_PASSWORD")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The workflow started the container. The agent must not start it.
	d.dbReady = map[dbServer]bool{customerValkey: true}
	return d
}

// rawValkey opens a connection with no login.
func rawValkey(t *testing.T, d *Deps) *valkeyConn {
	t.Helper()
	c, err := d.ValkeyDial(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &valkeyConn{c: c, r: bufio.NewReader(c)}
}

func TestValkeyServer(t *testing.T) {
	d := valkeyServer(t)
	ctx := context.Background()
	if err := d.valkeyReady(ctx, customerValkey); err != nil {
		t.Fatal(err)
	}
	login := func(name, password string) *valkeyConn {
		t.Helper()
		v, err := d.valkeyConnect(ctx, customerValkey, name, password)
		if err != nil {
			t.Fatalf("log in as %s: %v", name, err)
		}
		t.Cleanup(func() { v.Close() })
		return v
	}
	// ok runs a command that the user can run, and gives its answer.
	ok := func(v *valkeyConn, args ...string) any {
		t.Helper()
		res, err := v.do(args...)
		if err != nil {
			t.Errorf("%s: %v", strings.Join(args, " "), err)
		}
		return res
	}
	// denied runs a command that the rules of the user must refuse.
	denied := func(v *valkeyConn, args ...string) {
		t.Helper()
		res, err := v.do(args...)
		var answer valkeyError
		if !errors.As(err, &answer) || !strings.Contains(string(answer), "NOPERM") {
			t.Errorf("%s: Valkey did not refuse the command: %v %v", strings.Join(args, " "), res, err)
		}
	}
	// scriptDenied runs a script in which Valkey must refuse a command.
	scriptDenied := func(v *valkeyConn, script string, args ...string) {
		t.Helper()
		res, err := v.do(append([]string{"EVAL", script}, args...)...)
		var answer valkeyError
		if !errors.As(err, &answer) {
			t.Errorf("script %q: Valkey did not refuse the command: %v %v", script, res, err)
		}
	}

	const nameA, passA = "site1_ab12_cache", "Aaaaaaaaaaaaaaaa1111"
	const nameB, passB = "site2_cd34_cache", "Bbbbbbbbbbbbbbbb2222"
	// A run before this one can leave its databases.
	for _, name := range []string{nameA, nameB, "site3_ef56_cache", valkeyKeepName} {
		if err := d.valkeyDelete(ctx, customerValkey, name); err != nil {
			t.Fatal(err)
		}
	}
	numberA, err := d.valkeyCreate(ctx, customerValkey, nameA, "site1_ab12_", passA)
	if err != nil {
		t.Fatal(err)
	}
	numberB, err := d.valkeyCreate(ctx, customerValkey, nameB, "site2_cd34_", passB)
	if err != nil {
		t.Fatal(err)
	}
	if numberA < 1 || numberB < 1 || numberA == numberB {
		t.Fatalf("numbers = %d and %d", numberA, numberB)
	}
	dbA, dbB := strconv.Itoa(numberA), strconv.Itoa(numberB)

	// A connection with no login can do nothing.
	if _, err := rawValkey(t, d).do("PING"); err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Errorf("PING with no login: %v", err)
	}
	if _, err := d.valkeyConnect(ctx, customerValkey, nameA, passB); err == nil {
		t.Error("a wrong password was accepted")
	}

	a := login(nameA, passA)
	// A new connection is in database 0, where the user can use no key.
	denied(a, "SET", "k", "v")
	denied(a, "GET", "k")
	denied(a, "SELECT", "0")
	denied(a, "SELECT", dbB)
	// The commands that a library sends before the first key work in database 0.
	ok(a, "PING")
	ok(a, "CLIENT", "SETNAME", "app")
	ok(a, "CLIENT", "SETINFO", "lib-name", "test")
	ok(a, "CLIENT", "ID")
	ok(a, "INFO")
	ok(a, "SELECT", dbA)

	// The usual commands of an application.
	ok(a, "SET", "k", "v")
	if got := ok(a, "GET", "k"); got != "v" {
		t.Errorf("GET k = %v", got)
	}
	ok(a, "SET", "counter", "1", "EX", "300")
	ok(a, "INCR", "counter")
	ok(a, "HSET", "h", "f", "1")
	ok(a, "LPUSH", "queue", "job")
	ok(a, "EXPIRE", "h", "300")
	ok(a, "DEL", "h", "queue", "counter")
	ok(a, "MULTI")
	ok(a, "SET", "t", "1")
	ok(a, "EXEC")
	ok(a, "DEL", "t")
	ok(a, "SCAN", "0")
	if got, _ := ok(a, "KEYS", "*").([]any); len(got) != 1 || got[0] != "k" {
		t.Errorf("KEYS * = %v", got)
	}
	if got := ok(a, "DBSIZE"); got != int64(1) {
		t.Errorf("DBSIZE = %v", got)
	}
	if got := ok(a, "EVAL", "return redis.call('GET', KEYS[1])", "1", "k"); got != "v" {
		t.Errorf("EVAL GET = %v", got)
	}
	ok(a, "COPY", "k", "k2")
	ok(a, "DEL", "k2")
	ok(a, "DUMP", "k")
	ok(a, "PUBLISH", "site1_ab12_events", "hello")

	// The other databases, in each way.
	denied(a, "SWAPDB", dbA, dbB)
	denied(a, "MOVE", "k", dbB)
	denied(a, "COPY", "k", "k2", "DB", dbB)
	denied(a, "COPY", "k", "k2", "DB", dbB, "REPLACE")
	denied(a, "FLUSHALL")
	scriptDenied(a, "return redis.call('SELECT', ARGV[1])", "0", dbB)
	scriptDenied(a, "redis.call('SELECT', ARGV[1]) return redis.call('DBSIZE')", "0", dbB)
	scriptDenied(a, "return redis.call('CONFIG', 'GET', 'maxmemory')", "0")
	if got := ok(a, "GET", "k"); got != "v" {
		t.Errorf("after the refused commands, GET k = %v: the connection left its database", got)
	}

	// The server and the other connections.
	denied(a, "CONFIG", "GET", "maxmemory")
	denied(a, "CONFIG", "SET", "maxmemory", "1")
	denied(a, "ACL", "LIST")
	denied(a, "ACL", "SETUSER", nameA, "alldbs")
	denied(a, "CLIENT", "LIST")
	denied(a, "CLIENT", "KILL", "USER", nameB)
	denied(a, "CLIENT", "PAUSE", "1000")
	denied(a, "SLOWLOG", "GET")
	denied(a, "SAVE")
	denied(a, "BGSAVE")
	denied(a, "BGREWRITEAOF")
	denied(a, "REPLICAOF", "NO", "ONE")
	denied(a, "DEBUG", "SLEEP", "0")
	denied(a, "MIGRATE", "192.0.2.1", "6379", "k", "0", "1000")
	denied(a, "RESTORE", "k3", "0", "x")
	denied(a, "MODULE", "LIST")
	// The parts that all databases share.
	denied(a, "FUNCTION", "LIST")
	denied(a, "FUNCTION", "FLUSH")
	denied(a, "FCALL", "f", "0")
	denied(a, "SCRIPT", "FLUSH")
	denied(a, "SCRIPT", "KILL")
	denied(a, "PUBSUB", "CHANNELS")
	denied(a, "PUBLISH", "site2_cd34_events", "hello")
	denied(a, "PUBLISH", "events", "hello")
	// These commands change the connection when Valkey accepts them, so each has its own connection.
	for _, command := range [][]string{{"MONITOR"}, {"SYNC"}, {"PSYNC", "?", "-1"}, {"SUBSCRIBE", "site2_cd34_events"}, {"PSUBSCRIBE", "*"}} {
		denied(login(nameA, passA), command...)
	}
	if got, _ := ok(login(nameA, passA), "SUBSCRIBE", "site1_ab12_events").([]any); len(got) != 3 || got[0] != "subscribe" {
		t.Errorf("SUBSCRIBE to a channel of the website = %v", got)
	}

	// The second website does not see the key of the first website, and the first website
	// cannot remove the keys of the second website.
	b := login(nameB, passB)
	denied(b, "SELECT", dbA)
	ok(b, "SELECT", dbB)
	if got := ok(b, "GET", "k"); got != nil {
		t.Errorf("website 2 reads the key of website 1: %v", got)
	}
	if got := ok(b, "DBSIZE"); got != int64(0) {
		t.Errorf("DBSIZE of website 2 = %v", got)
	}
	ok(b, "SET", "k", "other")
	ok(a, "FLUSHDB")
	if got := ok(b, "GET", "k"); got != "other" {
		t.Errorf("FLUSHDB of website 1 removed the key of website 2: %v", got)
	}
	ok(a, "SET", "k", "v")

	// The report has the number and the keys of each database.
	sizes, err := d.valkeySizes(ctx, customerValkey)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, s := range sizes {
		if (s.Name == nameA && s.Number == numberA || s.Name == nameB && s.Number == numberB) && s.Keys != nil && *s.Keys == 1 {
			found++
		}
	}
	if found != 2 {
		t.Errorf("sizes = %+v", sizes)
	}

	// A new password: the old password and the old connections stop, and the data stays.
	const newPassA = "Aaaaaaaaaaaaaaaa9999"
	if err := d.valkeySetPassword(ctx, customerValkey, nameA, newPassA); err != nil {
		t.Fatal(err)
	}
	if _, err := a.do("GET", "k"); err == nil {
		t.Error("a connection with the old password still works")
	}
	if _, err := d.valkeyConnect(ctx, customerValkey, nameA, passA); err == nil {
		t.Error("the old password still works")
	}
	a = login(nameA, newPassA)
	denied(a, "SELECT", dbB)
	ok(a, "SELECT", dbA)
	if got := ok(a, "GET", "k"); got != "v" {
		t.Errorf("after the new password, GET k = %v", got)
	}
	// The same job again gives the same number and keeps the data.
	if n, err := d.valkeyCreate(ctx, customerValkey, nameA, "site1_ab12_", newPassA); err != nil || n != numberA {
		t.Errorf("the same job again: %d %v", n, err)
	}
	if _, err := d.valkeyCreate(ctx, customerValkey, nameA, "site1_ab12_", "Different12345678901"); err == nil {
		t.Error("a second database with the same name was made")
	}

	// A removed database: no user, no connection, no keys. The next database gets its number,
	// and it is empty.
	if err := d.valkeyDelete(ctx, customerValkey, nameB); err != nil {
		t.Fatal(err)
	}
	if _, err := b.do("GET", "k"); err == nil {
		t.Error("a connection of the removed database still works")
	}
	if _, err := d.valkeyConnect(ctx, customerValkey, nameB, passB); err == nil {
		t.Error("the user of the removed database still works")
	}
	numberC, err := d.valkeyCreate(ctx, customerValkey, "site3_ef56_cache", "site3_ef56_", "Cccccccccccccccc3333")
	if err != nil || numberC != numberB {
		t.Fatalf("number of the third database = %d (website 1: %d, website 2: %d): %v", numberC, numberA, numberB, err)
	}
	c := login("site3_ef56_cache", "Cccccccccccccccc3333")
	ok(c, "SELECT", strconv.Itoa(numberC))
	if got := ok(c, "DBSIZE"); got != int64(0) {
		t.Errorf("the new database has %v keys", got)
	}
	// The administrator has the last database, and no database after it.
	admin, err := d.valkeyAdminConn(ctx, customerValkey)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ok(admin, "SELECT", strconv.Itoa(quadlet.ValkeyDatabases-1))
	if _, err := admin.do("SELECT", strconv.Itoa(quadlet.ValkeyDatabases)); err == nil {
		t.Error("the server has more databases than quadlet.ValkeyDatabases")
	}
	// The server stops last: when Valkey accepts SHUTDOWN, the later tests fail.
	denied(a, "SHUTDOWN", "NOSAVE")

	// For TestValkeyServerAfterRestart: a database with one key.
	if _, err := d.valkeyCreate(ctx, customerValkey, valkeyKeepName, "keep_k1p2_", valkeyKeepPassword); err != nil {
		t.Fatal(err)
	}
	keep := login(valkeyKeepName, valkeyKeepPassword)
	users, err := valkeyUsers(admin)
	if err != nil {
		t.Fatal(err)
	}
	ok(keep, "SELECT", strconv.Itoa(users[valkeyKeepName]))
	ok(keep, "SET", "stays", "yes")
}

// The workflow starts the container again and then runs this test: Valkey keeps the users, the
// database numbers and the data.
func TestValkeyServerAfterRestart(t *testing.T) {
	d := valkeyServer(t)
	if os.Getenv("CF_TEST_VALKEY_RESTARTED") == "" {
		t.Skip("CF_TEST_VALKEY_RESTARTED is not set")
	}
	ctx := context.Background()
	admin, err := d.valkeyAdminConn(ctx, customerValkey)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	users, err := valkeyUsers(admin)
	if err != nil {
		t.Fatal(err)
	}
	number, found := users[valkeyKeepName]
	if !found || number < 1 {
		t.Fatalf("after the restart, the users are %v", users)
	}
	keep, err := d.valkeyConnect(ctx, customerValkey, valkeyKeepName, valkeyKeepPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer keep.Close()
	if _, err := keep.do("SELECT", strconv.Itoa(number)); err != nil {
		t.Fatal(err)
	}
	// Valkey writes the append-only file each second, and the workflow waits before the restart.
	if got, err := keep.do("GET", "stays"); err != nil || got != "yes" {
		t.Errorf("after the restart, GET stays = %v %v", got, err)
	}
	if _, err := keep.do("SELECT", "0"); err == nil {
		t.Error("after the restart, the user can use database 0")
	}
}

// The image workflow starts Valkey with the options of the unit.
func TestTheImageWorkflowStartsValkeyLikeTheUnit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "images.yml"))
	if err != nil {
		t.Skip("no workflow file: ", err)
	}
	want := "valkey-server " + strings.Join(quadlet.ValkeyArgs(quadlet.Database{MemoryMB: 64}), " ")
	if !strings.Contains(string(b), want) {
		t.Errorf("images.yml does not start Valkey with %q", want)
	}
}
