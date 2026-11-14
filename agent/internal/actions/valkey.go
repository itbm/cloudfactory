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
	"maps"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// Valkey is the third engine of the shared database containers. One Valkey server has
// numbered databases, not named databases. Thus a Valkey database of a website is:
//
//   - one database number of the shared server, from 1 to quadlet.ValkeyDatabases-1, and
//   - one user with the name of the database, which can use only that number.
//
// The user file of Valkey (quadlet.ValkeyUsersFile) is the only record of the numbers: the rule
// "db=N" of each user. The agent gives a new database the smallest number that no user has.
// The panel gets the number in the result of the job and in each report.
//
// The agent does not use a client in the container. It connects to the published port as the
// user "default", the administrator, with a password that only root can read.

// valkeyAdmin is the name of the administrator. It is the user of a connection with no user name.
const valkeyAdmin = "default"

// valkeyMinVersion is the first release in which a user cannot leave its database with COPY
// or MOVE. The rule "db=" is new in release 9.1.
var valkeyMinVersion = [3]int{9, 1, 2}

// valkeyTimeout is the time limit of one connection of the agent to Valkey.
const valkeyTimeout = 30 * time.Second

// valkeyError is an error answer of Valkey, for example "WRONGPASS invalid username-password pair".
type valkeyError string

func (e valkeyError) Error() string { return "valkey: " + string(e) }

// valkeyConn is one connection to Valkey. It speaks the protocol of Valkey (RESP2).
type valkeyConn struct {
	c net.Conn
	r *bufio.Reader
}

func (v *valkeyConn) Close() error { return v.c.Close() }

// do sends one command and gives the answer: a string, an int64, a list or nil. An error
// answer of Valkey is a valkeyError.
func (v *valkeyConn) do(args ...string) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(v.c, b.String()); err != nil {
		return nil, err
	}
	return v.read(0)
}

// maxValkeyAnswer is the maximum size of one string of an answer. The answers that the agent
// asks for are small.
const maxValkeyAnswer = 16 << 20

func (v *valkeyConn) read(depth int) (any, error) {
	line, err := v.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" || depth > 8 {
		return nil, fmt.Errorf("valkey: unexpected answer %q", line)
	}
	rest := line[1:]
	switch line[0] {
	case '+':
		return rest, nil
	case '-':
		return nil, valkeyError(rest)
	case ':':
		return strconv.ParseInt(rest, 10, 64)
	case '$':
		n, err := strconv.Atoi(rest)
		if err != nil || n > maxValkeyAnswer {
			return nil, fmt.Errorf("valkey: unexpected answer %q", line)
		}
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(v.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(rest)
		if err != nil || n > maxValkeyAnswer {
			return nil, fmt.Errorf("valkey: unexpected answer %q", line)
		}
		if n < 0 {
			return nil, nil
		}
		items := make([]any, 0, n)
		for range n {
			item, err := v.read(depth + 1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	}
	return nil, fmt.Errorf("valkey: unexpected answer %q", line)
}

// valkeyConnect opens a connection and logs in.
func (d *Deps) valkeyConnect(ctx context.Context, user, password string) (*valkeyConn, error) {
	dial := d.ValkeyDial
	if dial == nil {
		dial = func(ctx context.Context) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, "tcp", net.JoinHostPort(quadlet.ServiceAddress, strconv.Itoa(quadlet.DatabasePort("valkey"))))
		}
	}
	c, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(valkeyTimeout))
	v := &valkeyConn{c: c, r: bufio.NewReader(c)}
	if _, err := v.do("AUTH", user, password); err != nil {
		c.Close()
		return nil, err
	}
	return v, nil
}

// valkeySecretFile holds the password of the administrator of Valkey. Only root can read it.
func (d *Deps) valkeySecretFile() string { return filepath.Join(d.dbHome(), "valkey.secret") }

// valkeyAdminConn opens a connection as the administrator. It does not start the container.
func (d *Deps) valkeyAdminConn(ctx context.Context) (*valkeyConn, error) {
	b, err := os.ReadFile(d.valkeySecretFile())
	if err != nil {
		return nil, fmt.Errorf("read the password of the Valkey administrator: %w", err)
	}
	return d.valkeyConnect(ctx, valkeyAdmin, strings.TrimSpace(string(b)))
}

// writeValkeyUsers makes the user file of a new Valkey server, and the password of its
// administrator. The file has only the SHA-256 hash of the password. The password itself
// stays in a file that only root can read, because the container does not need it.
//
// The file belongs to cf-db, which is root in the container. The entry point of the image
// then gives it to the user valkey of the container, with the data directory.
func (d *Deps) writeValkeyUsers(ctx context.Context, dataDir string) error {
	users := filepath.Join(dataDir, quadlet.ValkeyUsersFile)
	if _, err := os.Lstat(users); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	password := randomHex(48)
	if _, err := host.WriteFile(d.valkeySecretFile(), password+"\n", 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(password))
	if _, err := host.WriteFile(users, "user "+valkeyAdmin+" on #"+hex.EncodeToString(sum[:])+" ~* &* +@all\n", 0o600); err != nil {
		return err
	}
	_, err := d.Run.Run(ctx, "chown", DBUser+":"+DBUser, users)
	return err
}

// valkeyReady checks that Valkey answers the administrator, and that its release can limit a
// user to one database.
func (d *Deps) valkeyReady(ctx context.Context) error {
	v, err := d.valkeyAdminConn(ctx)
	if err != nil {
		return err
	}
	defer v.Close()
	info, err := v.do("INFO", "server")
	if err != nil {
		return err
	}
	text, _ := info.(string)
	version := valkeyVersion(text)
	if slices.Compare(version[:], valkeyMinVersion[:]) < 0 {
		return fmt.Errorf("the Valkey image has release %d.%d.%d, and the agent needs %d.%d.%d or later", version[0], version[1], version[2], valkeyMinVersion[0], valkeyMinVersion[1], valkeyMinVersion[2])
	}
	return nil
}

var valkeyVersionRE = regexp.MustCompile(`(?m)^valkey_version:(\d+)\.(\d+)\.(\d+)`)

// valkeyVersion reads the release from the answer of "INFO server".
func valkeyVersion(info string) (v [3]int) {
	if m := valkeyVersionRE.FindStringSubmatch(info); m != nil {
		for i := range v {
			v[i], _ = strconv.Atoi(m[i+1])
		}
	}
	return v
}

// valkeyRules gives the rules of the user of one Valkey database (ACL SETUSER).
//
// The user can use all keys of its database, and no other database. A new connection is in
// database 0, where the user can use no key: the application must select its database. The
// channels are for all databases together, so the user can use only the channels that start
// with the prefix of the website.
//
// The user gets no command of an administrator and no dangerous command. Then it gets the
// commands that applications and their libraries use, and that change or show only the
// database or the connection of the user. INFO shows numbers of the server, for example the
// number of keys of each database, but no data.
//
// The functions, the script cache and the list of the channels are for all databases
// together, so the user cannot change or read them. MOVE has no use with one database.
func valkeyRules(prefix string, number int) []string {
	return []string{
		"resetkeys", "~*",
		"resetchannels", "&" + prefix + "*",
		"db=" + strconv.Itoa(number),
		"+@all", "-@admin", "-@dangerous",
		"+info", "+keys", "+flushdb", "+sort", "+sort_ro",
		"+client|setname", "+client|setinfo", "+client|id", "+client|getname", "+client|info", "+client|reply",
		"+client|tracking", "+client|trackinginfo", "+client|caching", "+client|getredir", "+client|no-touch",
		"-function", "-fcall", "-fcall_ro", "-script|flush", "-script|kill", "-pubsub", "-move",
	}
}

// valkeyUsers gives the database number of each user of Valkey, without the administrator.
// The number is -1 for a user that does not have one database.
func valkeyUsers(v *valkeyConn) (map[string]int, error) {
	res, err := v.do("ACL", "LIST")
	if err != nil {
		return nil, err
	}
	lines, _ := res.([]any)
	users := map[string]int{}
	for _, l := range lines {
		line, _ := l.(string)
		name, numbers := parseValkeyUser(line)
		if name == "" || name == valkeyAdmin {
			continue
		}
		users[name] = -1
		if len(numbers) == 1 {
			users[name] = numbers[0]
		}
	}
	return users, nil
}

// parseValkeyUser reads one line of "ACL LIST", for example
// "user acme_x7k2_cache on #5e88... ~* resetchannels &acme_x7k2_* db=7 +@all -@admin".
func parseValkeyUser(line string) (name string, numbers []int) {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "user" {
		return "", nil
	}
	for _, rule := range f[2:] {
		// A selector is a list of rules in "(" and ")".
		list, ok := strings.CutPrefix(strings.Trim(rule, "()"), "db=")
		if !ok {
			continue
		}
		for _, n := range strings.Split(list, ",") {
			if number, err := strconv.Atoi(n); err == nil {
				numbers = append(numbers, number)
			}
		}
	}
	return f[1], numbers
}

// valkeyService starts the container if it does not operate, and opens a connection as the
// administrator. The caller holds valkeyMu.
func (d *Deps) valkeyService(ctx context.Context) (*valkeyConn, error) {
	if err := d.EnsureDatabaseService(ctx, "valkey"); err != nil {
		return nil, err
	}
	return d.valkeyAdminConn(ctx)
}

// valkeyCreate makes a Valkey database: a free database number, and a user with the name of
// the database that can use only that number. It gives the number.
//
// A job that runs again after a restart of the agent finds the user of its first run. The
// password of the job is the proof: then the result is the number of the first run.
func (d *Deps) valkeyCreate(ctx context.Context, name, prefix, password string) (int, error) {
	d.valkeyMu.Lock()
	defer d.valkeyMu.Unlock()
	v, err := d.valkeyService(ctx)
	if err != nil {
		return 0, err
	}
	defer v.Close()
	users, err := valkeyUsers(v)
	if err != nil {
		return 0, err
	}
	number, found := users[name]
	if found {
		c, err := d.valkeyConnect(ctx, name, password)
		if err != nil || number < 1 {
			return 0, fmt.Errorf("the database %s exists", name)
		}
		c.Close()
	} else {
		used := map[int]bool{}
		for _, n := range users {
			used[n] = true
		}
		for number = 1; number < quadlet.ValkeyDatabases && used[number]; number++ {
		}
		if number >= quadlet.ValkeyDatabases {
			return 0, fmt.Errorf("this server has no free Valkey database: all %d databases are in use", quadlet.ValkeyDatabases-1)
		}
		// The database must be empty, also after a removal that did not complete.
		if _, err := v.do("SELECT", strconv.Itoa(number)); err != nil {
			return 0, err
		}
		if _, err := v.do("FLUSHDB"); err != nil {
			return 0, err
		}
	}
	rules := append([]string{"ACL", "SETUSER", name, "reset", "on", ">" + password}, valkeyRules(prefix, number)...)
	if _, err := v.do(rules...); err != nil {
		return 0, err
	}
	// Valkey keeps the users in memory until this command.
	if _, err := v.do("ACL", "SAVE"); err != nil {
		return 0, err
	}
	return number, nil
}

// valkeyDelete removes a Valkey database: its user first, which also closes the connections
// of the user, and then all keys of its database number. It does nothing when the database
// does not exist.
func (d *Deps) valkeyDelete(ctx context.Context, name string) error {
	d.valkeyMu.Lock()
	defer d.valkeyMu.Unlock()
	v, err := d.valkeyService(ctx)
	if err != nil {
		return err
	}
	defer v.Close()
	users, err := valkeyUsers(v)
	if err != nil {
		return err
	}
	number, found := users[name]
	if !found {
		return nil
	}
	if _, err := v.do("ACL", "DELUSER", name); err != nil {
		return err
	}
	if _, err := v.do("ACL", "SAVE"); err != nil {
		return err
	}
	if number < 1 {
		return nil
	}
	if _, err := v.do("SELECT", strconv.Itoa(number)); err != nil {
		return err
	}
	_, err = v.do("FLUSHDB")
	return err
}

// valkeySetPassword gives the user of a Valkey database a new password, and closes the
// connections that used the old password.
func (d *Deps) valkeySetPassword(ctx context.Context, name, password string) error {
	d.valkeyMu.Lock()
	defer d.valkeyMu.Unlock()
	v, err := d.valkeyService(ctx)
	if err != nil {
		return err
	}
	defer v.Close()
	users, err := valkeyUsers(v)
	if err != nil {
		return err
	}
	if _, found := users[name]; !found {
		return fmt.Errorf("the Valkey database %s does not exist", name)
	}
	if _, err := v.do("ACL", "SETUSER", name, "resetpass", ">"+password); err != nil {
		return err
	}
	if _, err := v.do("ACL", "SAVE"); err != nil {
		return err
	}
	if _, err := v.do("CLIENT", "KILL", "USER", name); err != nil {
		slog.Warn("Valkey: close the connections of a user", "user", name, "err", err)
	}
	return nil
}

var valkeyKeysRE = regexp.MustCompile(`(?m)^db(\d+):keys=(\d+)`)

// valkeySizes gives the number and the count of keys of each Valkey database. Valkey does not
// know the size of one database in bytes.
func (d *Deps) valkeySizes(ctx context.Context) ([]panel.DatabaseSize, error) {
	v, err := d.valkeyAdminConn(ctx)
	if err != nil {
		return nil, err
	}
	defer v.Close()
	users, err := valkeyUsers(v)
	if err != nil {
		return nil, err
	}
	info, err := v.do("INFO", "keyspace")
	if err != nil {
		return nil, err
	}
	text, _ := info.(string)
	keys := map[int]int64{}
	for _, m := range valkeyKeysRE.FindAllStringSubmatch(text, -1) {
		number, _ := strconv.Atoi(m[1])
		keys[number], _ = strconv.ParseInt(m[2], 10, 64)
	}
	out := []panel.DatabaseSize{}
	for _, name := range slices.Sorted(maps.Keys(users)) {
		number := users[name]
		if number < 1 {
			continue
		}
		count := keys[number]
		out = append(out, panel.DatabaseSize{Engine: "valkey", Name: name, Number: number, Keys: &count})
	}
	return out, nil
}

// valkeyMemoryMB gives the memory limit of Valkey: the setting of the agent, or one eighth of
// the memory of the server.
func (d *Deps) valkeyMemoryMB() int {
	if d.ValkeyMemoryMB > 0 {
		return d.ValkeyMemoryMB
	}
	return defaultValkeyMemoryMB(hostMemoryMB("/proc/meminfo"))
}

// defaultValkeyMemoryMB is one eighth of the memory of the server, from 64 MB to 1024 MB.
func defaultValkeyMemoryMB(totalMB int) int {
	return min(max(totalMB/8, 64), 1024)
}

// hostMemoryMB reads the memory of the server from /proc/meminfo. It gives 0 when it cannot.
func hostMemoryMB(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.Atoi(f[1])
			return kb / 1024
		}
	}
	return 0
}

var databasePortsRE = regexp.MustCompile(`\{ 3306, 5432(, 19432)? \}`)

// addValkeyPort gives the firewall file of the installer with the port of Valkey in each
// list of the database ports. A file of an earlier release has only the ports of MariaDB and
// PostgreSQL. ok is false when the file has no rule that lets the websites connect to the
// database ports.
func addValkeyPort(content string) (next string, ok bool) {
	next = databasePortsRE.ReplaceAllString(content, "{ 3306, 5432, 6379$1 }")
	return next, strings.Contains(next, "tcp dport { 3306, 5432, 6379 } accept")
}

// openValkeyPort lets the website containers connect to the port of Valkey on the service
// address. With no file, the administrator manages the firewall.
func (d *Deps) openValkeyPort(ctx context.Context) error {
	path := d.firewallFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		slog.Warn("Valkey: no Cloudfactory firewall file; let the websites connect to port 6379 on the service address", "file", path)
		return nil
	}
	if err != nil {
		return err
	}
	next, ok := addValkeyPort(string(b))
	if !ok {
		return fmt.Errorf("the firewall file %s has no rule for the database ports; run the installer again on this server", path)
	}
	if next == string(b) {
		return nil
	}
	if _, err := host.WriteFile(path, next, 0o644); err != nil {
		return err
	}
	_, err = d.Run.Run(ctx, "nft", "-f", path)
	return err
}

// addValkeyHost gives the host name of Valkey to the container of a website from an earlier
// release. A unit of this release has the name already. The container starts again only when
// its unit changes.
func (d *Deps) addValkeyHost(ctx context.Context, websiteID string) error {
	site, err := d.State.Get(websiteID)
	if err != nil || site == nil {
		return err
	}
	u := d.unit(site.Website, site.Port, site.Suffix)
	u.Stopped = site.Stopped
	changed, err := d.Host.WriteUnit(site.UnixUser, u.UnitName(), quadlet.ContainerUnit(u))
	if err != nil || !changed {
		return err
	}
	if _, err := d.Host.Systemctl(ctx, site.UnixUser, "daemon-reload"); err != nil {
		return err
	}
	if site.Stopped {
		return nil
	}
	if _, err := d.Host.Systemctl(ctx, site.UnixUser, "restart", u.ServiceName()); err != nil {
		return err
	}
	return d.waitReady(ctx, site.Port, primaryDomain(site.Website))
}

// errValkeyNoCopy is the answer to a backup, a restore or a staging copy of a Valkey database.
var errValkeyNoCopy = errors.New("a backup and a staging copy do not include Valkey databases")
