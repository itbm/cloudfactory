package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/actions"
	"github.com/itbm/cloudfactory/agent/internal/config"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/images"
)

// workerID gives a UID and a GID for the worker of a test. Only root can give a file to a
// different user, so a test that is not root uses its own user.
func workerID() (uid, gid int) {
	if os.Geteuid() == 0 {
		return 65534, 65534
	}
	return os.Getuid(), os.Getgid()
}

func owner(t *testing.T, path string) int {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Sys().(*syscall.Stat_t).Uid)
}

// The first start with a worker gives the records of the jobs to the worker.
func TestTheWorkerOwnsTheRecordsOfTheJobs(t *testing.T) {
	uid, gid := workerID()
	data := t.TempDir()
	// A new server has no directory.
	if err := ownJobs(data, uid, gid); err != nil {
		t.Fatal(err)
	}
	jobs := filepath.Join(data, "jobs")
	if fi, err := os.Stat(jobs); err != nil || fi.Mode().Perm() != 0o700 || owner(t, jobs) != uid {
		t.Errorf("jobs: %v, owner %d", err, owner(t, jobs))
	}
	// A server from an earlier release: root made the directory and the records.
	data = t.TempDir()
	jobs = filepath.Join(data, "jobs")
	if err := os.Mkdir(jobs, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(jobs, "01JBQ3Z5K8N6T4W2X9Y7R5P3M2.json")
	if err := os.WriteFile(record, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ownJobs(data, uid, gid); err != nil {
		t.Fatal(err)
	}
	if owner(t, jobs) != uid || owner(t, record) != uid {
		t.Errorf("owner of the directory = %d, of the record = %d, want %d", owner(t, jobs), owner(t, record), uid)
	}
}

// After the first start, the worker can write in the directory of the records. Root then
// changes only a file of root with one name, and never the target of a link.
func TestRootDoesNotFollowTheLinksOfTheWorker(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can give a file to a different user")
	}
	uid, gid := workerID()
	data := t.TempDir()
	if err := ownJobs(data, uid, gid); err != nil {
		t.Fatal(err)
	}
	jobs := filepath.Join(data, "jobs")
	secret := filepath.Join(data, "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(data, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(other, "file")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// What a worker that an attacker controls can put in its directory.
	if err := os.Symlink(secret, filepath.Join(jobs, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(jobs, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(jobs, "hard.json")); err != nil {
		t.Fatal(err)
	}
	// A record that root made while the binary of an earlier release operated.
	record := filepath.Join(jobs, "01JBQ3Z5K8N6T4W2X9Y7R5P3M2.json")
	if err := os.WriteFile(record, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ownJobs(data, uid, gid); err != nil {
		t.Fatal(err)
	}
	if owner(t, record) != uid {
		t.Errorf("owner of the record of root = %d", owner(t, record))
	}
	for _, path := range []string{secret, other, inside} {
		if owner(t, path) != 0 {
			t.Errorf("root gave %s to the worker", path)
		}
	}
}

// The root helper closes the directory of its socket for all users except the worker. On a
// test server, the socket was in /run/cloudfactory: the user cf-db could then not read its
// secret files below that directory, and the restore of a database failed.
func TestTheSocketHasItsOwnDirectory(t *testing.T) {
	dir := filepath.Dir(gate.DefaultSocket)
	if filepath.Dir(dir) != "/run" {
		t.Errorf("the directory of the socket is %s; it must be a directory of its own in /run", dir)
	}
	d := &actions.Deps{}
	for name, path := range map[string]string{
		"the secret files of the database clients": d.SecretFilesDir(),
		"the run directory of the shared images":   images.DefaultRunRoot,
		"the data directory":                       config.Defaults().DataDir,
		"the directory of the websites":            config.Defaults().SitesDir,
	} {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			t.Errorf("%s (%s) is in the directory of the socket", name, path)
		}
	}
}

// Only root and the group of the worker can connect to the root helper.
func TestTheSocketOfTheRootHelperIsForTheWorkerOnly(t *testing.T) {
	_, gid := workerID()
	path := filepath.Join(t.TempDir(), "run", "gate.sock")
	l, err := listenGate(path, gid)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o660 {
		t.Errorf("socket: %v, %v", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("directory: %v, %v", fi.Mode().Perm(), err)
	}
	// A socket from an agent process that stopped does not stop the next start.
	l.Close()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err = listenGate(path, gid)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}
