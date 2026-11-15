package actions

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/mesh"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
)

// The tests do not wait for a tunnel that never comes.
func init() { peerWait = 10 * time.Millisecond }

const moveToken = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// writeMeshState gives the root helper of d a mesh state with one peer, 10.207.0.2.
func writeMeshState(t *testing.T, root *Deps) {
	t.Helper()
	st := mesh.State{Version: 1, Range: "10.207.0.0/16", Address: "10.207.0.1", Port: 51820, Peers: []mesh.Peer{
		{Server: meshPeerB, PublicKey: meshPublic, PresharedKey: meshShared, Endpoint: "203.0.113.2:51820", Address: "10.207.0.2"},
	}}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.meshDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.meshDir(), meshStateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheRootHelperMovesADatabaseServerOnlyToAPeer(t *testing.T) {
	d, f := boundary(t)
	root := rootOf(d)
	writeMeshState(t, root)
	maria := dbServer{ID: mariaServerKey, Engine: "mariadb"}
	for _, op := range []string{opDBSend, opDBReceive} {
		err := call(d, op, transferIn{Server: maria, Peer: "10.207.0.9", Token: moveToken})
		if err == nil || !strings.Contains(err.Error(), "is not a peer") {
			t.Errorf("%s to a server that is not a peer: %v", op, err)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("the root helper ran: %q", f.Calls)
	}
	own, peer, err := root.transferAddresses(transferIn{Server: maria, Peer: "10.207.0.2", Token: moveToken})
	if err != nil || own.String() != "10.207.0.1" || peer.String() != "10.207.0.2" {
		t.Errorf("the addresses of a move: %s %s %v", own, peer, err)
	}
}

// moveDirs gives the root settings of two servers, and a MariaDB database server with data on
// the first server.
func moveDirs(t *testing.T) (from, to *Deps, s dbServer) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("the test gives the files the owner of a database server, which needs root")
	}
	from, to = settings(t.TempDir(), nil, nil), settings(t.TempDir(), nil, nil)
	s = dbServer{ID: mariaServerKey, Engine: "mariadb"}
	data := from.dataDir(s)
	for _, dir := range []string{data, filepath.Join(data, "shop"), filepath.Join(to.dataDir(s))} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(data, "ibdata1"):              strings.Repeat("x", 3<<20),
		filepath.Join(data, "shop", "orders.ibd"):   "orders",
		filepath.Join(to.dataDir(s), "stale-file"):  "from an earlier move",
		filepath.Join(from.dbDir(s), "mariadb.env"): "MARIADB_ROOT_PASSWORD=00ff00ff00ff00ff\nMYSQL_PWD=00ff00ff00ff00ff\n",
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("shop/orders.ibd", filepath.Join(data, "link")); err != nil {
		t.Fatal(err)
	}
	return from, to, s
}

func TestAMoveCarriesTheDataAndTheSecret(t *testing.T) {
	from, to, s := moveDirs(t)
	uid, gid, err := to.Host.FileOwner(dbUser(s))
	if err != nil {
		t.Fatal(err)
	}
	disk, err := safefs.Open(from.diskDir(s))
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	secret, err := os.ReadFile(from.secretPath(s))
	if err != nil {
		t.Fatal(err)
	}

	a, b := net.Pipe()
	sent := make(chan error, 1)
	go func() {
		_, _, err := writeTransfer(a, moveToken, secret, disk)
		sent <- err
	}()
	in := transferIn{Server: s, Peer: "10.207.0.2", Token: moveToken}
	ok, err := to.receiveFrom(b, in, uid, gid, 0)
	if !ok || err != nil {
		t.Fatalf("receive: %v %v", ok, err)
	}
	if err := <-sent; err != nil {
		t.Fatalf("send: %v", err)
	}
	b.Close()

	data := to.dataDir(s)
	for rel, want := range map[string]string{"ibdata1": strings.Repeat("x", 3<<20), "shop/orders.ibd": "orders"} {
		p := filepath.Join(data, rel)
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Errorf("%s: %d bytes, %v", rel, len(got), err)
		}
		if fi, err := os.Lstat(p); err != nil || fi.Mode().Perm() != 0o640 || owner(fi) != [2]int{uid, gid} {
			t.Errorf("%s: %v %v", rel, fi.Mode(), err)
		}
	}
	if target, err := os.Readlink(filepath.Join(data, "link")); err != nil || target != "shop/orders.ibd" {
		t.Errorf("the link: %q %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(data, "stale-file")); !os.IsNotExist(err) {
		t.Errorf("the data directory kept a file of an earlier move: %v", err)
	}
	if fi, err := os.Lstat(data); err != nil || fi.Mode().Perm() != 0o700 || owner(fi) != [2]int{uid, gid} {
		t.Errorf("the data directory: %v %v", fi.Mode(), err)
	}
	got, err := os.ReadFile(to.secretPath(s))
	if err != nil || string(got) != string(secret) {
		t.Errorf("the secret: %q %v", got, err)
	}
	if fi, err := os.Lstat(to.secretPath(s)); err != nil || fi.Mode().Perm() != 0o640 || owner(fi) != [2]int{0, gid} {
		t.Errorf("the secret file: %v %v", fi.Mode(), err)
	}
}

func TestAMoveRefusesAWrongToken(t *testing.T) {
	_, to, s := moveDirs(t)
	a, b := net.Pipe()
	go func() {
		_, _ = a.Write([]byte(transferHello + strings.Repeat("f", 64) + "\n"))
	}()
	ok, err := to.receiveFrom(b, transferIn{Server: s, Token: moveToken}, 0, 0, 0)
	if ok || err != nil {
		t.Errorf("a wrong token: %v %v", ok, err)
	}
	// The data directory did not change.
	if _, err := os.Lstat(filepath.Join(to.dataDir(s), "stale-file")); err != nil {
		t.Error(err)
	}
}

func TestAMoveRefusesFilesOutsideTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	disk, err := safefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	secret := tar.Header{Typeflag: tar.TypeReg, Name: transferSecret, Size: 2}
	uid, gid := os.Getuid(), os.Getgid()
	for name, tc := range map[string]struct {
		headers []tar.Header
		limit   int64
	}{
		"a path above the data":   {headers: []tar.Header{secret, {Typeflag: tar.TypeReg, Name: "data/../x", Size: 1}}},
		"an absolute path":        {headers: []tar.Header{secret, {Typeflag: tar.TypeReg, Name: "/etc/x", Size: 1}}},
		"a name with no data":     {headers: []tar.Header{secret, {Typeflag: tar.TypeReg, Name: "x", Size: 1}}},
		"a hard link":             {headers: []tar.Header{secret, {Typeflag: tar.TypeLink, Name: "data/x", Linkname: "/etc/shadow"}}},
		"a device":                {headers: []tar.Header{secret, {Typeflag: tar.TypeChar, Name: "data/x"}}},
		"a file through a link":   {headers: []tar.Header{secret, {Typeflag: tar.TypeSymlink, Name: "data/l", Linkname: "/etc"}, {Typeflag: tar.TypeReg, Name: "data/l/x", Size: 1}}},
		"more than the disk":      {headers: []tar.Header{secret, {Typeflag: tar.TypeReg, Name: "data/x", Size: 1}, {Typeflag: tar.TypeReg, Name: "data/y", Size: 1}}, limit: 1},
		"no secret":               {headers: []tar.Header{{Typeflag: tar.TypeReg, Name: "data/x", Size: 1}}},
		"a secret that is larger": {headers: []tar.Header{{Typeflag: tar.TypeReg, Name: transferSecret, Size: maxSecret + 1}}},
	} {
		if err := disk.RemoveAll("data"); err != nil {
			t.Fatal(err)
		}
		if err := disk.Mkdir("data", 0o700, uid, gid); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, h := range tc.headers {
			h.Mode = 0o600
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				_, _ = tw.Write(bytes.Repeat([]byte("a"), int(h.Size)))
			}
		}
		_ = tw.Close()
		if _, err := receiveTree(disk, tar.NewReader(&buf), uid, gid, tc.limit); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTheSecretOfAMoveIsChecked(t *testing.T) {
	for _, tc := range []struct {
		engine, secret string
		ok             bool
	}{
		{"mariadb", "MARIADB_ROOT_PASSWORD=00ff00ff00ff00ff\nMYSQL_PWD=00ff00ff00ff00ff\n", true},
		{"postgresql", "POSTGRES_PASSWORD=00ff00ff00ff00ff\n", true},
		{"valkey", strings.Repeat("ab", 48) + "\n", true},
		{"postgresql", "MARIADB_ROOT_PASSWORD=00ff00ff00ff00ff\n", false},
		{"mariadb", "MYSQL_PWD=00ff00ff00ff00ff\nLD_PRELOAD=/tmp/x.so\n", false},
		{"mariadb", "MYSQL_PWD=$(id)\n", false},
		{"valkey", "user default on nopass\n", false},
	} {
		if err := checkSecret(tc.engine, []byte(tc.secret)); (err == nil) != tc.ok {
			t.Errorf("%s %q: %v", tc.engine, tc.secret, err)
		}
	}
}

// owner gives the UID and the GID of a file.
func owner(fi os.FileInfo) [2]int {
	st := fi.Sys().(*syscall.Stat_t)
	return [2]int{int(st.Uid), int(st.Gid)}
}
