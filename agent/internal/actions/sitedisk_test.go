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
	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// siteImage is in sitework_test.go.
const (
	siteUser    = "acme_x7k2"
	siteService = "systemctl --user --machine=acme_x7k2@ "
)

// siteDiskDeps gives the dependencies of a server with loop devices and with no user quotas.
// "install -d" of the fake runner makes no directory, so the directory of the websites is
// there.
func siteDiskDeps(t *testing.T) (*Deps, *execx.Fake) {
	t.Helper()
	d, f, _ := deps(t)
	if err := os.WriteFile(d.LoopControl, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.Host.SitesDir, 0o711); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loopFreeTries, siteUnmountTries = 10, 10 })
	loopFreeTries, siteUnmountTries = 1, 1
	return d, f
}

// limited gives the website of the tests with a disk limit.
func limited(bytes int64) *agentv1.Website {
	w := website(siteImage)
	w.Limits.DiskBytes = bytes
	return w
}

// newSite makes the website of the tests with a disk limit.
func newSite(t *testing.T, d *Deps, bytes int64) {
	t.Helper()
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: limited(bytes)})); err != nil {
		t.Fatal(err)
	}
}

func setDisk(d *Deps, t *testing.T, job string, bytes int64) error {
	t.Helper()
	l := &agentv1.Limits{Vcpus: 2, MemoryBytes: 2 << 30, Processes: 50, DiskBytes: bytes}
	return run(t)(d.SetLimits(&agentv1.SetLimitsRequest{JobId: job, WebsiteId: websiteID, Limits: l}))
}

func quotaPart(t *testing.T, d *Deps) panel.Service {
	t.Helper()
	var r panel.Report
	d.FillReport(context.Background(), &r)
	for _, s := range r.Services {
		if s.Name == "disk-quotas" {
			return s
		}
	}
	t.Fatalf("no disk-quotas part in %+v", r.Services)
	return panel.Service{}
}

// On a server with no user quotas, a new website with a disk limit gets a disk image with the
// size of the limit. The root helper mounts it before it makes the directory of the files.
func TestANewWebsiteGetsADiskImage(t *testing.T) {
	d, f := siteDiskDeps(t)
	// The limit is not a multiple of the block size of the file system.
	newSite(t, d, 10<<30+100)
	k := d.siteDisk(siteUser)
	root, app := d.Host.SiteRoot(siteUser), d.Host.AppDir(siteUser)
	if k.Image != filepath.Join(filepath.Dir(d.Host.SitesDir), "disks", siteUser+".img") || k.Dir != root {
		t.Fatalf("disk = %+v", k)
	}
	if got := sizeOf(t, k.Image); got != 10<<30 {
		t.Errorf("size of the image = %d", got)
	}
	if fi, _ := os.Stat(k.Image); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode of the image = %v", fi.Mode().Perm())
	}
	// Only root can open the directory of the disk images.
	if fi, err := os.Stat(filepath.Dir(k.Image)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("directory of the images: %v %v", fi, err)
	}
	unit := mountUnit(root)
	if want := `srv-cloudfactory-sites-acme_x7k2.mount`; mountUnit("/srv/cloudfactory/sites/acme_x7k2") != want {
		t.Errorf("mount unit = %q, want %q", mountUnit("/srv/cloudfactory/sites/acme_x7k2"), want)
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(d.AddressUnit), unit))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Description=Disk of the Cloudfactory website user acme_x7k2\n",
		"RequiresMountsFor=" + filepath.Dir(k.Image) + "\n",
		"[Mount]\nWhat=" + k.Image + "\nWhere=" + root + "\nType=ext4\nOptions=loop,noatime,nodev,nosuid,discard\n",
		"[Install]\nWantedBy=local-fs.target\n",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("the mount unit has no %q:\n%s", want, content)
		}
	}
	// The top directory of the file system belongs to root and to the group of the website
	// user, as the directory of a website with no disk image does. The files of the website
	// go on the disk: the helper makes "app" after the mount.
	steps := []string{
		"mkfs.ext4 -q -F -m 0 -b 4096 -E root_owner=0:20001 " + k.Image + ".new",
		"systemctl daemon-reload",
		"systemctl enable --now " + unit,
		"chmod 0750 " + root,
		"install -d -o root -g acme_x7k2 -m 0750 " + root,
		"install -d -o acme_x7k2 -g acme_x7k2 -m 0750 " + app,
		siteService + "restart cf-acme_x7k2.service",
	}
	last := -1
	for _, step := range steps {
		i := index(f.Calls, step)
		if i <= last {
			t.Errorf("the step %q is not in its position: %q", step, f.Calls)
		}
		last = i
	}
	if f.Called("setquota") {
		t.Errorf("a server with no user quotas set a quota: %q", f.Calls)
	}
	// The server applies the disk limit of each of its websites.
	if part := quotaPart(t, d); part.State != "running" || !part.Optional || part.Detail != "" {
		t.Errorf("part = %+v", part)
	}
	// A job that runs again keeps the image.
	f.Calls = nil
	newSite(t, d, 10<<30)
	if f.Called("mkfs.ext4") || f.Called("resize2fs") {
		t.Errorf("the second job made a new file system: %q", f.Calls)
	}
}

// The agent as one root process, with no worker, gives a new website a disk image too.
func TestOneRootProcessGivesAWebsiteADiskImage(t *testing.T) {
	d, f, _ := direct(t)
	os.WriteFile(d.LoopControl, nil, 0o600)
	os.MkdirAll(d.Host.SitesDir, 0o711)
	newSite(t, d, 5<<30)
	if got := sizeOf(t, d.siteDisk(siteUser).Image); got != 5<<30 || !f.Called("systemctl enable --now "+mountUnit(d.Host.SiteRoot(siteUser))) {
		t.Errorf("size = %d, calls = %q", got, f.Calls)
	}
}

// A mkfs that fails leaves no image and no directory of the website: the next attempt starts
// again, and the website does not start with a directory that has no limit.
func TestADiskImageThatFailsStopsTheNewWebsite(t *testing.T) {
	d, f := siteDiskDeps(t)
	f.Rules = []execx.Rule{{Prefix: "mkfs.ext4", Err: errors.New("no space left on device")}}
	err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: limited(5 << 30)}))
	if err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(d.siteDisk(siteUser).Image); !os.IsNotExist(err) {
		t.Errorf("an image with no file system: %v", err)
	}
	if f.Called("install -d -o acme_x7k2") || f.Called(siteService+"restart") || f.Called("systemctl enable --now") {
		t.Errorf("the website has a directory with no limit: %q", f.Calls)
	}
	// The next attempt makes the image.
	f.Rules, f.Calls = nil, nil
	newSite(t, d, 5<<30)
	if got := sizeOf(t, d.siteDisk(siteUser).Image); got != 5<<30 {
		t.Errorf("size of the image = %d", got)
	}
}

// A server with user quotas gives a website no disk image: the user quota is the limit.
func TestAWebsiteWithAUserQuotaGetsNoDiskImage(t *testing.T) {
	d, f := siteDiskDeps(t)
	both(d, func(d *Deps) { d.QuotaMount = "/srv" })
	newSite(t, d, 10<<30)
	if _, err := os.Stat(d.siteDisk(siteUser).Image); !os.IsNotExist(err) || f.Called("mkfs.ext4") || f.Called("systemctl enable --now") {
		t.Errorf("a disk image on a server with user quotas: %v %q", err, f.Calls)
	}
	if !f.Called("setquota --user acme_x7k2 9961472 10485760 0 0 /srv") {
		t.Errorf("no quota: %q", f.Calls)
	}
	// A new limit changes only the quota.
	f.Calls = nil
	if err := setDisk(d, t, jobID2, 5<<30); err != nil {
		t.Fatal(err)
	}
	if !f.Called("setquota --user acme_x7k2 4980736 5242880 0 0 /srv") || f.Called("resize2fs") || f.Called(siteService+"stop") {
		t.Errorf("calls = %q", f.Calls)
	}
}

// A website with no disk limit gets no disk image: an image always has a size.
func TestAWebsiteWithNoDiskLimitGetsNoDiskImage(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 0)
	if _, err := os.Stat(d.siteDisk(siteUser).Image); !os.IsNotExist(err) || f.Called("mkfs.ext4") {
		t.Errorf("a disk image with no limit: %v %q", err, f.Calls)
	}
	// The website has no limit to apply, so the part is not off.
	if part := quotaPart(t, d); part.State != "running" {
		t.Errorf("part = %+v", part)
	}
}

// A server with no user quotas and no loop devices has no disk limits for its websites, and
// tells the panel.
func TestAServerWithNoLoopDevicesGivesAWebsiteNoDiskLimit(t *testing.T) {
	d, f := siteDiskDeps(t)
	os.Remove(d.LoopControl)
	newSite(t, d, 10<<30)
	if _, err := os.Stat(d.siteDisk(siteUser).Image); !os.IsNotExist(err) || f.Called("mkfs.ext4") {
		t.Errorf("a disk image with no loop devices: %v %q", err, f.Calls)
	}
	part := quotaPart(t, d)
	if part.State != "stopped" || !part.Optional || !strings.Contains(part.Detail, "no loop devices ("+d.LoopControl+")") || !strings.Contains(part.Detail, "installation guide") {
		t.Errorf("part = %+v", part)
	}
	if d.SiteDiskLimits() {
		t.Error("the server reports disk limits")
	}
}

// A website that has a directory keeps it: the agent does not move the files of a website
// into a disk image. The server tells the panel that it does not apply the limit.
func TestAWebsiteThatHasADirectoryKeepsIt(t *testing.T) {
	d, f := siteDiskDeps(t)
	// The agent made the website when the server had no loop devices.
	os.Remove(d.LoopControl)
	newSite(t, d, 10<<30)
	if err := os.MkdirAll(d.Host.AppDir(siteUser), 0o750); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(d.LoopControl, nil, 0o600)
	f.Calls = nil
	newSite(t, d, 10<<30)
	if err := setDisk(d, t, jobID2, 20<<30); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.siteDisk(siteUser).Image); !os.IsNotExist(err) || f.Called("mkfs.ext4") || f.Called("systemctl enable --now") || f.Called("resize2fs") {
		t.Errorf("the website got a disk image: %v %q", err, f.Calls)
	}
	part := quotaPart(t, d)
	if part.State != "stopped" || !strings.Contains(part.Detail, "Websites with a disk limit and no disk image: 1 of 1.") {
		t.Errorf("part = %+v", part)
	}
}

// A larger disk limit makes the file system of a website larger while the website operates.
func TestALargerLimitMakesTheDiskOfAWebsiteLarger(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 5<<30)
	k := d.siteDisk(siteUser)
	f.Rules = []execx.Rule{{Prefix: "findmnt --noheadings --output SOURCE --mountpoint " + k.Dir, Output: "/dev/loop7\n"}}
	f.Calls = nil
	if err := setDisk(d, t, jobID2, 10<<30); err != nil {
		t.Fatal(err)
	}
	if got := sizeOf(t, k.Image); got != 10<<30 {
		t.Errorf("size of the image = %d", got)
	}
	capacity, resize := index(f.Calls, "losetup --set-capacity /dev/loop7"), index(f.Calls, "resize2fs /dev/loop7")
	if !(capacity >= 0 && capacity < resize) {
		t.Errorf("capacity %d, resize %d in %q", capacity, resize, f.Calls)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, " stop ") || strings.HasPrefix(c, "e2fsck") || strings.Contains(c, "pkill") {
			t.Errorf("a larger limit stopped the website: %s", c)
		}
	}
	// The other limits changed too.
	site, _ := d.State.Get(websiteID)
	if l := site.Website.GetLimits(); l.GetDiskBytes() != 10<<30 || l.GetVcpus() != 2 {
		t.Errorf("limits = %+v", l)
	}
	// The same limit again changes nothing on the disk.
	f.Calls = nil
	if err := setDisk(d, t, "01JBQ3Z5K8N6T4W2X9Y7R5P3M4", 10<<30); err != nil || f.Called("resize2fs") || f.Called("losetup") {
		t.Errorf("the same limit again: %v %q", err, f.Calls)
	}
	// With no limit, the disk image keeps its size.
	if err := setDisk(d, t, "01JBQ3Z5K8N6T4W2X9Y7R5P3M5", 0); err != nil || f.Called("resize2fs") || sizeOf(t, k.Image) != 10<<30 {
		t.Errorf("no limit: %v %q", err, f.Calls)
	}
}

// A smaller disk limit stops the website and its SFTP sessions, makes the file system
// smaller, and starts the website again.
func TestASmallerLimitMakesTheDiskOfAWebsiteSmaller(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 5<<30)
	k := d.siteDisk(siteUser)
	unit := mountUnit(k.Dir)
	steps := []string{
		siteService + "stop cf-acme_x7k2.service",
		"podman exec cf-sftp pkill --signal KILL --euid 20001",
		"systemctl stop " + unit,
		"losetup --associated " + k.Image,
		"e2fsck -f -p " + k.Image,
		"resize2fs " + k.Image + " 2097152K",
		"systemctl start " + unit,
		siteService + "start cf-acme_x7k2.service",
		// The other limits change after the disk.
		siteService + "set-property --runtime cf-acme_x7k2.service",
	}
	f.Calls = nil
	if err := setDisk(d, t, jobID2, 2<<30); err != nil {
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
	if got := sizeOf(t, k.Image); got != 2<<30 {
		t.Errorf("size of the image = %d", got)
	}

	// The data and the tables of the file system need more space than the new limit: the
	// website operates again with the disk and the limits that it had.
	f.Rules = []execx.Rule{{Prefix: "resize2fs", Err: errors.New("New size smaller than minimum (300000)")}}
	f.Calls = nil
	err := setDisk(d, t, "01JBQ3Z5K8N6T4W2X9Y7R5P3M4", 1<<30)
	if !errors.Is(err, errDiskKeepsSize) || !strings.Contains(err.Error(), "the disk of the website user acme_x7k2 cannot have the new limit of 1024 MB") {
		t.Errorf("err = %v", err)
	}
	if got := sizeOf(t, k.Image); got != 2<<30 {
		t.Errorf("size of the image after the failure = %d", got)
	}
	if mount, again := index(f.Calls, "systemctl start "+unit), index(f.Calls, siteService+"start cf-acme_x7k2.service"); !(mount >= 0 && mount < again) {
		t.Errorf("the website does not operate again: %q", f.Calls)
	}
	site, _ := d.State.Get(websiteID)
	if l := site.Website.GetLimits(); l.GetDiskBytes() != 2<<30 || f.Called(siteService+"set-property") {
		t.Errorf("the failed job changed a limit: %+v %q", l, f.Calls)
	}

	// The files need more space than the new limit: the website does not stop.
	f.Rules, f.Calls = nil, nil
	os.MkdirAll(d.Host.AppDir(siteUser), 0o750)
	if err := os.WriteFile(filepath.Join(d.Host.AppDir(siteUser), "video.mp4"), make([]byte, 3<<20), 0o640); err != nil {
		t.Fatal(err)
	}
	err = setDisk(d, t, "01JBQ3Z5K8N6T4W2X9Y7R5P3M5", 2<<20)
	if !errors.Is(err, errDiskKeepsSize) || !strings.Contains(err.Error(), "the website uses 3 MB") {
		t.Errorf("err = %v", err)
	}
	if f.Called(siteService+"stop") || f.Called("systemctl stop") || f.Called("pkill") {
		t.Errorf("the check stopped the website: %q", f.Calls)
	}
}

// A file system that a process uses does not change: the image keeps its loop device when a
// mount namespace that systemd does not see has the mount. The website operates again.
func TestADiskInUseKeepsItsSize(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 5<<30)
	k := d.siteDisk(siteUser)
	unit := mountUnit(k.Dir)
	f.Rules = []execx.Rule{{Prefix: "losetup --associated " + k.Image, Output: "/dev/loop7: [2049]:131 (" + k.Image + ")\n"}}
	f.Calls = nil
	err := setDisk(d, t, jobID2, 2<<30)
	if err == nil || !strings.Contains(err.Error(), "a process uses the disk of the website user acme_x7k2") {
		t.Fatalf("err = %v", err)
	}
	if f.Called("e2fsck") || f.Called("resize2fs") || sizeOf(t, k.Image) != 5<<30 {
		t.Errorf("the image changed: %q", f.Calls)
	}
	if mount, again := index(f.Calls, "systemctl start "+unit), index(f.Calls, siteService+"start cf-acme_x7k2.service"); !(mount >= 0 && mount < again) {
		t.Errorf("the website does not operate again: %q", f.Calls)
	}
	// An unmount that fails, for example with an open SFTP transfer, changes nothing too.
	f.Rules = []execx.Rule{{Prefix: "systemctl stop " + unit, Err: errors.New("umount: target is busy")}}
	f.Calls = nil
	err = setDisk(d, t, "01JBQ3Z5K8N6T4W2X9Y7R5P3M4", 2<<30)
	if err == nil || !strings.Contains(err.Error(), "unmount the disk of the website user acme_x7k2") || f.Called("e2fsck") || !f.Called(siteService+"start cf-acme_x7k2.service") {
		t.Errorf("err = %v, calls = %q", err, f.Calls)
	}
}

// A stopped website stays stopped when its disk becomes smaller.
func TestAStoppedWebsiteStaysStoppedWhenItsDiskChanges(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 5<<30)
	if err := run(t)(d.SetWebsitePower(&agentv1.SetWebsitePowerRequest{JobId: jobID2, WebsiteId: websiteID, Power: "stop"})); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	if err := setDisk(d, t, "01JBQ3Z5K8N6T4W2X9Y7R5P3M4", 2<<30); err != nil {
		t.Fatal(err)
	}
	if !f.Called("resize2fs "+d.siteDisk(siteUser).Image+" 2097152K") || f.Called(siteService+"start") || f.Called(siteService+"stop") {
		t.Errorf("calls = %q", f.Calls)
	}
}

// The agent removes the disk image with the website. It closes the SFTP sessions first: an
// open session has its root directory on the disk.
func TestTheAgentRemovesTheDiskImageWithTheWebsite(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 5<<30)
	k := d.siteDisk(siteUser)
	unit := mountUnit(k.Dir)
	file := filepath.Join(filepath.Dir(d.AddressUnit), unit)
	req := &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: siteUser}

	// The unmount fails: the image and its unit stay, and the job can run again.
	f.Rules = []execx.Rule{{Prefix: "systemctl disable --now " + unit, Err: errors.New("umount: target is busy")}}
	if err := run(t)(d.DeleteWebsite(req)); err == nil || !strings.Contains(err.Error(), "unmount the disk of the website user acme_x7k2") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(k.Image); err != nil {
		t.Errorf("the image went before its unmount: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("the mount unit went before its unmount: %v", err)
	}

	f.Rules, f.Calls = nil, nil
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	steps := []string{
		"podman exec cf-sftp pkill --signal KILL --euid 20001",
		"userdel --remove acme_x7k2",
		"systemctl disable --now " + unit,
		"systemctl daemon-reload",
	}
	last := -1
	for _, step := range steps {
		i := index(f.Calls, step)
		if i <= last {
			t.Errorf("the step %q is not in its position: %q", step, f.Calls)
		}
		last = i
	}
	for _, path := range []string{k.Image, file, k.Dir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s stays: %v", path, err)
		}
	}
	// A website with no disk image has no unit to remove.
	d, f = siteDiskDeps(t)
	os.Remove(d.LoopControl)
	newSite(t, d, 5<<30)
	f.Calls = nil
	if err := run(t)(d.DeleteWebsite(req)); err != nil || f.Called("systemctl disable") {
		t.Errorf("err = %v, calls = %q", err, f.Calls)
	}
}

// A website with a disk image keeps the image when the server gets user quotas later. A new
// limit changes the image and the quota.
func TestAWebsiteWithADiskImageKeepsItWithUserQuotas(t *testing.T) {
	d, f := siteDiskDeps(t)
	newSite(t, d, 5<<30)
	k := d.siteDisk(siteUser)
	both(d, func(d *Deps) { d.QuotaMount = "/srv" })
	f.Rules = []execx.Rule{{Prefix: "findmnt --noheadings --output SOURCE --mountpoint " + k.Dir, Output: "/dev/loop7\n"}}
	f.Calls = nil
	if err := setDisk(d, t, jobID2, 10<<30); err != nil {
		t.Fatal(err)
	}
	if got := sizeOf(t, k.Image); got != 10<<30 || !f.Called("resize2fs /dev/loop7") || !f.Called("setquota --user acme_x7k2 9961472 10485760 0 0 /srv") {
		t.Errorf("size = %d, calls = %q", got, f.Calls)
	}
	if part := quotaPart(t, d); part.State != "running" {
		t.Errorf("part = %+v", part)
	}
}
