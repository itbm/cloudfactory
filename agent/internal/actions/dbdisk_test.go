package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// diskDeps gives the dependencies of a server with loop devices, and the MariaDB database
// server of the tests with its directory.
func diskDeps(t *testing.T) (*Deps, *execx.Fake, dbServer) {
	t.Helper()
	d, f, _ := deps(t)
	users(d)
	both(d, func(d *Deps) { d.Host.LookupGID = func(string) (int, error) { return 991, nil } })
	if err := os.WriteFile(d.LoopControl, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := dbServer{ID: mariaServerKey, Engine: "mariadb"}
	if err := os.MkdirAll(d.dbDir(s), 0o700); err != nil {
		t.Fatal(err)
	}
	return d, f, s
}

func sizeOf(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// A new database server with a disk limit gets a disk image with the size of the limit. A
// systemd mount unit mounts it now and at each start of the server.
func TestANewDatabaseServerGetsADisk(t *testing.T) {
	d, f, s := diskDeps(t)
	image, dir := d.diskImage(s), d.diskDir(s)
	// The limit is not a multiple of the block size of the file system.
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30+100); err != nil {
		t.Fatal(err)
	}
	if got := sizeOf(t, image); got != 5<<30 {
		t.Errorf("size of the image = %d", got)
	}
	if fi, _ := os.Stat(image); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode of the image = %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(image + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temporary image stays: %v", err)
	}
	unit := `var-lib-cf\x2ddb-servers-` + mariaServerKey + "-disk.mount"
	if got := mountUnit("/var/lib/cf-db/servers/" + mariaServerKey + "/disk"); got != unit {
		t.Errorf("mount unit = %q, want %q", got, unit)
	}
	unit = mountUnit(dir)
	content, err := os.ReadFile(filepath.Join(filepath.Dir(d.AddressUnit), unit))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"RequiresMountsFor=" + d.dbDir(s) + "\n",
		"[Mount]\nWhat=" + image + "\nWhere=" + dir + "\nType=ext4\nOptions=loop,noatime,nodev,nosuid,discard\n",
		"[Install]\nWantedBy=local-fs.target\n",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("the mount unit has no %q:\n%s", want, content)
		}
	}
	// The file system is complete before the mount. Its top directory belongs to cf-db.
	mkfs := index(f.Calls, "mkfs.ext4 -q -F -m 0 -b 4096 -E root_owner=990:991 "+image+".new")
	reload := index(f.Calls, "systemctl daemon-reload")
	mount := index(f.Calls, "systemctl enable --now "+unit)
	mode := index(f.Calls, "chmod 0700 "+dir)
	if !(mkfs >= 0 && mkfs < reload && reload < mount && mount < mode) {
		t.Errorf("mkfs %d, reload %d, mount %d, mode %d in %q", mkfs, reload, mount, mode, f.Calls)
	}
	// Only root can open the mount point while the disk is not there.
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("mount point: %v %v", fi, err)
	}
	if svc := d.diskService(); svc.State != "running" || !svc.Optional {
		t.Errorf("service = %+v", svc)
	}
	// The same limit again: the image stays as it is.
	f.Calls = nil
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "mkfs") || strings.HasPrefix(c, "resize2fs") || strings.HasPrefix(c, "e2fsck") || c == "systemctl daemon-reload" {
			t.Errorf("the same limit did: %s", c)
		}
	}
	// A mkfs that fails leaves no image: the next attempt starts again.
	d, f, s = diskDeps(t)
	f.Rules = []execx.Rule{{Prefix: "mkfs.ext4", Err: errors.New("no space left on device")}}
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(d.diskImage(s)); !os.IsNotExist(err) || f.Called("systemctl enable --now "+mountUnit(d.diskDir(s))) {
		t.Errorf("an image with no file system: %v %q", err, f.Calls)
	}
	f.Rules = nil
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err != nil || sizeOf(t, d.diskImage(s)) != 5<<30 {
		t.Errorf("the second attempt: %v", err)
	}
}

// A larger limit makes the disk larger while the database server operates.
func TestALargerLimitMakesTheDiskLarger(t *testing.T) {
	d, f, s := diskDeps(t)
	image, dir := d.diskImage(s), d.diskDir(s)
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err != nil {
		t.Fatal(err)
	}
	if _, err := rootOf(d).Host.WriteUnit(DBUser, s.container()+".container", "[Container]\n"); err != nil {
		t.Fatal(err)
	}
	find := "findmnt --noheadings --output SOURCE --mountpoint " + dir
	f.Rules = []execx.Rule{{Prefix: find, Output: "/dev/loop3\n"}, {Prefix: "resize2fs", Err: errors.New("no memory")}}
	f.Calls = nil
	// The file system did not get its new size.
	if err := rootOf(d).ensureDisk(context.Background(), s, 10<<30); err == nil {
		t.Fatal("no error")
	}
	// The job runs again with the same limit, and completes the step.
	f.Rules = f.Rules[:1]
	f.Calls = nil
	if err := rootOf(d).ensureDisk(context.Background(), s, 10<<30); err != nil {
		t.Fatal(err)
	}
	if got := sizeOf(t, image); got != 10<<30 {
		t.Errorf("size of the image = %d", got)
	}
	capacity, resize := index(f.Calls, "losetup --set-capacity /dev/loop3"), index(f.Calls, "resize2fs /dev/loop3")
	if !(capacity >= 0 && capacity < resize) {
		t.Errorf("capacity %d, resize %d in %q", capacity, resize, f.Calls)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, " stop ") || strings.HasPrefix(c, "e2fsck") {
			t.Errorf("a larger limit stopped the database server: %s", c)
		}
	}
	// The step is complete: the same limit does nothing more.
	f.Calls = nil
	if err := rootOf(d).ensureDisk(context.Background(), s, 10<<30); err != nil || f.Called("resize2fs") {
		t.Errorf("the same limit again: %v %q", err, f.Calls)
	}
	// A disk that is not on a loop device stays as it is.
	f.Rules = []execx.Rule{{Prefix: find, Output: "\n"}}
	if err := rootOf(d).ensureDisk(context.Background(), s, 20<<30); err == nil || !strings.Contains(err.Error(), "not on a loop device") {
		t.Errorf("no loop device: %v", err)
	}
}

// A smaller limit stops the database server, makes the file system smaller, and mounts it
// again. When the data needs more space, the disk and the database server are as before.
func TestASmallerLimitMakesTheDiskSmaller(t *testing.T) {
	d, f, s := diskDeps(t)
	image, dir := d.diskImage(s), d.diskDir(s)
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err != nil {
		t.Fatal(err)
	}
	if _, err := rootOf(d).Host.WriteUnit(DBUser, s.container()+".container", "[Container]\n"); err != nil {
		t.Fatal(err)
	}
	unit := mountUnit(dir)
	steps := []string{
		"systemctl --user --machine=cf-db@ stop " + s.container() + ".service",
		"systemctl stop " + unit,
		"e2fsck -f -p " + image,
		"resize2fs " + image + " 2097152K",
		"systemctl start " + unit,
	}
	f.Calls = nil
	if err := rootOf(d).ensureDisk(context.Background(), s, 2<<30); err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, step := range steps {
		i := index(f.Calls, step)
		if i <= last {
			t.Errorf("the step %q is not in its position: %q", step, f.Calls)
		}
		last = i
	}
	if got := sizeOf(t, image); got != 2<<30 {
		t.Errorf("size of the image = %d", got)
	}
	// The data and the tables of the file system need more space than the new limit.
	start := "systemctl --user --machine=cf-db@ start " + s.container() + ".service"
	f.Rules = []execx.Rule{{Prefix: "resize2fs", Err: errors.New("New size smaller than minimum (300000)")}}
	f.Calls = nil
	err := rootOf(d).ensureDisk(context.Background(), s, 1<<30)
	if !errors.Is(err, errDiskKeepsSize) || !strings.Contains(err.Error(), "cannot have the new limit of 1024 MB") {
		t.Errorf("err = %v", err)
	}
	if got := sizeOf(t, image); got != 2<<30 {
		t.Errorf("size of the image after the failure = %d", got)
	}
	if mount, again := index(f.Calls, "systemctl start "+unit), index(f.Calls, start); !(mount >= 0 && mount < again) {
		t.Errorf("the database server does not operate again: %q", f.Calls)
	}
	// The data needs more space than the new limit: the database server does not stop.
	f.Rules, f.Calls = nil, nil
	os.MkdirAll(filepath.Join(dir, "data"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "data", "ibdata1"), make([]byte, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rootOf(d).resizeDisk(context.Background(), s, 2<<20); !errors.Is(err, errDiskKeepsSize) || !strings.Contains(err.Error(), "uses 3 MB") {
		t.Errorf("err = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("the check stopped the database server: %q", f.Calls)
	}
}

// A database server whose disk cannot have a smaller limit keeps all its old limits.
func TestADiskLimitThatFailsChangesNothing(t *testing.T) {
	d, f, _ := diskDeps(t)
	spec := databaseServer("mariadb")
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: spec})); err != nil {
		t.Fatal(err)
	}
	s := dbServer{ID: mariaServerKey, Engine: "mariadb"}
	if got := sizeOf(t, d.diskImage(s)); got != 5<<30 {
		t.Fatalf("size of the image = %d", got)
	}
	if u := unitFile(t, d, DBUser, s.container()+".container"); !strings.Contains(u, "Volume="+d.dataDir(s)+":/var/lib/mysql:Z\n") {
		t.Errorf("the data directory is not on the disk:\n%s", u)
	}
	// The data directory is on the disk: the agent makes it after the mount.
	if mount, data := index(f.Calls, "systemctl enable --now "+mountUnit(d.diskDir(s))), index(f.Calls, "install -d -o cf-db -g cf-db -m 0700 "+d.dataDir(s)); !(mount >= 0 && mount < data) {
		t.Errorf("mount %d, data directory %d in %q", mount, data, f.Calls)
	}
	smaller := databaseServer("mariadb")
	smaller.Limits = &agentv1.Limits{Vcpus: 2, MemoryBytes: 2 << 30, DiskBytes: 1 << 30}
	f.Rules = []execx.Rule{{Prefix: "resize2fs", Err: errors.New("New size smaller than minimum (300000)")}}
	f.Calls = nil
	err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID2, DatabaseServer: smaller}))
	if !errors.Is(err, errDiskKeepsSize) {
		t.Fatalf("err = %v", err)
	}
	if _, record, _, _ := d.State.DatabaseServer(mariaServerKey); record != recordOf(spec) {
		t.Errorf("record after the failure = %+v", record)
	}
	if u := unitFile(t, d, DBUser, s.container()+".container"); !strings.Contains(u, "MemoryMax=1073741824\n") || strings.Contains(u, "CPUQuota=200%") {
		t.Errorf("the unit has the new limits:\n%s", u)
	}
	if got := sizeOf(t, d.diskImage(s)); got != 5<<30 {
		t.Errorf("size of the image after the failure = %d", got)
	}
	if !f.Called("systemctl --user --machine=cf-db@ start " + s.container() + ".service") {
		t.Errorf("the database server does not operate again: %q", f.Calls)
	}
	// The removal of the database server unmounts its disk and removes the mount unit.
	f.Rules, f.Calls = nil, nil
	if err := run(t)(d.DeleteDatabaseServer(&agentv1.DeleteDatabaseServerRequest{JobId: jobID2, DatabaseServer: mariaServerID})); err != nil {
		t.Fatal(err)
	}
	unit := mountUnit(d.diskDir(s))
	stop, unmount := index(f.Calls, "systemctl --user --machine=cf-db@ stop "+s.container()+".service"), index(f.Calls, "systemctl disable --now "+unit)
	if !(stop >= 0 && stop < unmount) {
		t.Errorf("stop %d, unmount %d in %q", stop, unmount, f.Calls)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(d.AddressUnit), unit)); !os.IsNotExist(err) {
		t.Errorf("the mount unit stays: %v", err)
	}
	if _, err := os.Stat(d.dbDir(s)); !os.IsNotExist(err) {
		t.Errorf("the disk image stays: %v", err)
	}
}

// A server with no loop devices has no disk limits: the disk of a database server is a
// directory, and the report tells the panel.
func TestAServerWithNoLoopDevicesHasNoDiskLimits(t *testing.T) {
	d, f, _ := deps(t)
	s := dbServer{ID: mariaServerKey, Engine: "mariadb"}
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err != nil {
		t.Fatal(err)
	}
	if !f.Called("install -d -o cf-db -g cf-db -m 0700 "+d.diskDir(s)) || f.Called("mkfs.ext4") || f.Called("systemctl enable") {
		t.Errorf("calls = %q", f.Calls)
	}
	if svc := d.diskService(); svc.Name != "database-disk-limits" || svc.State != "stopped" || !svc.Optional || !strings.Contains(svc.Detail, "no loop devices") {
		t.Errorf("service = %+v", svc)
	}
	// A database server that started with a directory keeps it, also when the server gets
	// loop devices later: its data is in the directory.
	os.MkdirAll(d.dataDir(s), 0o700)
	os.WriteFile(d.LoopControl, nil, 0o600)
	f.Calls = nil
	if err := rootOf(d).ensureDisk(context.Background(), s, 5<<30); err != nil || f.Called("mkfs.ext4") || f.Called("systemctl enable") {
		t.Errorf("a database server with a directory got a disk image: %v %q", err, f.Calls)
	}
	// A database server with no disk limit has a directory too.
	other := dbServer{ID: pgServerKey, Engine: "postgresql"}
	if err := rootOf(d).ensureDisk(context.Background(), other, 0); err != nil || f.Called("mkfs.ext4") {
		t.Errorf("a database server with no limit got a disk image: %v %q", err, f.Calls)
	}
	used, size := rootOf(d).diskUse(s)
	if used != 0 || size != 0 {
		t.Errorf("disk use of a directory = %d of %d", used, size)
	}
}

// The report has the state, the memory and the disk space of each database server.
func TestTheReportHasTheUseOfEachDatabaseServer(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	d.CgroupRoot = t.TempDir()
	for _, spec := range []*agentv1.DatabaseServer{databaseServer("mariadb"), databaseServer("postgresql")} {
		if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: spec})); err != nil {
			t.Fatal(err)
		}
	}
	maria := dbServer{ID: mariaServerKey, Engine: "mariadb"}
	// MariaDB operates: its service has a cgroup. PostgreSQL does not operate.
	cg := "/user.slice/user-990.slice/user@990.service/app.slice/" + maria.container() + ".service"
	os.MkdirAll(filepath.Join(d.CgroupRoot, cg), 0o755)
	os.WriteFile(filepath.Join(d.CgroupRoot, cg, "memory.current"), []byte("209715200\n"), 0o644)
	os.MkdirAll(d.dataDir(maria), 0o700)
	os.WriteFile(filepath.Join(d.dataDir(maria), "ibdata1"), make([]byte, 4096), 0o600)
	f.Rules = []execx.Rule{{Prefix: "systemctl --user --machine=cf-db@ show --property=ControlGroup --value " + maria.container() + ".service", Output: cg + "\n"}}
	use := d.databaseServerUse(context.Background(), true)
	if len(use) != 2 {
		t.Fatalf("use = %+v", use)
	}
	// The disk of a server with no loop devices is a directory, which has no size.
	if got := use[0]; got.ID != mariaServerKey || got.State != "running" || got.MemoryBytes != 209715200 || got.DiskBytes != 4096 || got.DiskSizeBytes != 0 {
		t.Errorf("MariaDB = %+v", got)
	}
	if got := use[1]; got.ID != pgServerKey || got.State != "stopped" || got.MemoryBytes != 0 {
		t.Errorf("PostgreSQL = %+v", got)
	}
	// The agent does not measure a directory again in each report.
	os.WriteFile(filepath.Join(d.dataDir(maria), "ib_logfile0"), make([]byte, 8192), 0o600)
	if got := d.databaseServerUse(context.Background(), false)[0]; got.DiskBytes != 4096 {
		t.Errorf("the report with no measurement = %+v", got)
	}
	if got := d.databaseServerUse(context.Background(), true)[0]; got.DiskBytes != 12288 {
		t.Errorf("the report with a measurement = %+v", got)
	}
	// The database server stops: its cgroup goes.
	os.RemoveAll(filepath.Join(d.CgroupRoot, cg))
	if got := d.databaseServerUse(context.Background(), false)[0]; got.State != "stopped" || got.MemoryBytes != 0 {
		t.Errorf("after the stop = %+v", got)
	}
}
