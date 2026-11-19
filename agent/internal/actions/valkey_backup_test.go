package actions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
)

// With the switch off (the default), a backup or a restore of a Valkey database fails, as
// before. The parity record of Valkey has the same result.
func TestValkeyBackupsAreOffByDefault(t *testing.T) {
	d, _ := paritySite(t)
	newFakeValkey(d)
	server := databaseServer("valkey")
	_, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3:b/c",
		Databases: []*agentv1.DatabaseRef{{Engine: "valkey", Name: "acme_x7k2_cache", DatabaseServer: server.GetId()}}})
	if !errors.Is(err, errValkeyNoCopy) {
		t.Errorf("Backup = %v", err)
	}
	if _, err := d.valkeyDumpToFile(t.Context(), customerValkey, "acme_x7k2", "acme_x7k2_cache"); !errors.Is(err, errNoValkeyBackups) {
		t.Errorf("valkeyDumpToFile = %v", err)
	}
	// The root helper refuses too, also when the worker has the switch on.
	d.ValkeyBackups = true
	if _, err := d.valkeyDumpToFile(t.Context(), customerValkey, "acme_x7k2", "acme_x7k2_cache"); err == nil || !strings.Contains(err.Error(), "valkeyBackups") {
		t.Errorf("valkeyDumpToFile with the switch off in root = %v", err)
	}
}

// A dump has each key of the database number, with its expire time. A restore makes the
// database number the same as the dump, and does not touch the other numbers.
func TestValkeyBackupAndRestore(t *testing.T) {
	d, _ := paritySite(t)
	fv := newFakeValkey(d)
	// Root makes the dump and loads it, so it connects to the fake server too.
	rootOf(d).ValkeyDial = d.ValkeyDial
	both(d, func(d *Deps) { d.ValkeyBackups, d.ReadyTimeout = true, time.Second })
	server := databaseServer("valkey")
	pass := "Abcdefghijklmnop1234"
	for _, name := range []string{"acme_x7k2_cache", "acme_x7k2_jobs"} {
		if err := run(t)(d.CreateDatabase(&agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "valkey",
			Name: name, User: name, Password: pass, Access: "website", DatabaseServer: server})); err != nil {
			t.Fatal(err)
		}
	}
	fv.mu.Lock()
	fv.set(1, "session:1", "alice", 0)
	fv.set(1, "session:2", "bob\r\n$3\r\nbinary\x00", 4102444800000)
	fv.set(2, "job:1", "other website", 0)
	fv.mu.Unlock()

	// The job of a backup with the switch on accepts the Valkey database.
	if _, err := d.Backup(&agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3:b/c",
		Databases: []*agentv1.DatabaseRef{{Engine: "valkey", Name: "acme_x7k2_cache", DatabaseServer: server.GetId()}}}); err != nil {
		t.Fatalf("Backup = %v", err)
	}

	if err := d.resetDumps(t.Context(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	sum, err := d.valkeyDumpToFile(t.Context(), customerValkey, "acme_x7k2", "acme_x7k2_cache")
	if err != nil {
		t.Fatal(err)
	}
	root := rootOf(d)
	dump, err := os.ReadFile(filepath.Join(root.dumpDir("acme_x7k2"), dumpFile("valkey", "acme_x7k2_cache")))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 64 {
		t.Errorf("hash %q", sum)
	}
	var got []valkeyRecord
	if err := readValkeyDump(bytes.NewReader(dump), func(r valkeyRecord) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].key != "session:1" || got[1].expireAt != 4102444800000 || got[1].value != "DUMP\x00bob\r\n$3\r\nbinary\x00" {
		t.Fatalf("dump %+v", got)
	}

	// The database changes after the backup.
	fv.mu.Lock()
	fv.set(1, "session:1", "mallory", 0)
	fv.set(1, "session:3", "new", 0)
	fv.mu.Unlock()

	stage := filepath.Join(root.stageDir(jobID), strings.TrimPrefix(root.dumpDir("acme_x7k2"), "/"))
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(content []byte) {
		if err := os.WriteFile(filepath.Join(stage, dumpFile("valkey", "acme_x7k2_cache")), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A dump that is not complete stops the restore before the database changes.
	write(dump[:len(dump)-2])
	if err := d.valkeyRestoreStaged(t.Context(), jobID, "acme_x7k2", customerValkey, "acme_x7k2_cache"); err == nil {
		t.Error("a dump that is not complete is restored")
	}
	fv.mu.Lock()
	if fv.data[1]["session:1"].value != "mallory" {
		t.Error("a dump that is not complete changed the database")
	}
	fv.mu.Unlock()

	write(dump)
	if err := d.valkeyRestoreStaged(t.Context(), jobID, "acme_x7k2", customerValkey, "acme_x7k2_cache"); err != nil {
		t.Fatal(err)
	}
	fv.mu.Lock()
	defer fv.mu.Unlock()
	want := map[string]fakeKey{"session:1": {"alice", 0}, "session:2": {"bob\r\n$3\r\nbinary\x00", 4102444800000}}
	if len(fv.data[1]) != len(want) {
		t.Errorf("database 1 has %d keys: %v", len(fv.data[1]), fv.data[1])
	}
	for k, v := range want {
		if fv.data[1][k] != v {
			t.Errorf("%s = %+v, want %+v", k, fv.data[1][k], v)
		}
	}
	if fv.data[2]["job:1"].value != "other website" {
		t.Error("the restore changed a different database number")
	}
}

func TestReadValkeyDumpRefusesBadFiles(t *testing.T) {
	var good bytes.Buffer
	good.WriteString(valkeyDumpMagic)
	if err := writeValkeyRecord(&good, "a", "b", 0); err != nil {
		t.Fatal(err)
	}
	good.Write([]byte{'E', 1})
	none := func(valkeyRecord) error { return nil }
	if err := readValkeyDump(bytes.NewReader(good.Bytes()), none); err != nil {
		t.Fatalf("good dump: %v", err)
	}
	for name, b := range map[string][]byte{
		"empty":           nil,
		"other magic":     []byte("REDIS0011"),
		"no end":          good.Bytes()[:good.Len()-2],
		"wrong count":     append(append([]byte{}, good.Bytes()[:good.Len()-1]...), 2),
		"data after end":  append(append([]byte{}, good.Bytes()...), 'x'),
		"unknown record":  append([]byte(valkeyDumpMagic), 'Z'),
		"value too large": append([]byte(valkeyDumpMagic), 'K', 0xff, 0xff, 0xff, 0xff, 0x0f),
	} {
		if err := readValkeyDump(bytes.NewReader(b), none); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
