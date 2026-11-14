package host

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

func fakeHost(t *testing.T, exists bool) (*Host, *execx.Fake) {
	f := &execx.Fake{}
	dir := t.TempDir()
	h := &Host{Run: f, SitesDir: filepath.Join(dir, "sites"), UnitRoot: filepath.Join(dir, "units"), ConfigRoot: filepath.Join(dir, "config"), LookupUID: func(name string) (int, error) {
		if !exists {
			return 0, user.UnknownUserError(name)
		}
		return 20001, nil
	}}
	return h, f
}

func TestEnsureUserMakesTheUserOnce(t *testing.T) {
	h, f := fakeHost(t, false)
	// useradd makes the user.
	f.Rules = []execx.Rule{{Prefix: "useradd", Do: func() error {
		h.LookupUID = func(string) (int, error) { return 20001, nil }
		return nil
	}}}
	if err := h.EnsureUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	// Audit finding S1: the UID and the GID are in the range that the firewall limits.
	if !f.Called("useradd -K UID_MIN=20000 -K UID_MAX=59999 -K GID_MIN=20000 -K GID_MAX=59999 --create-home --home-dir "+h.Home("acme_x7k2")+" --shell /usr/sbin/nologin --user-group") || !f.Called("loginctl enable-linger acme_x7k2") || !f.Called("chmod 0700 "+h.Home("acme_x7k2")) || !f.Called("systemctl start user@20001.service") {
		t.Errorf("calls = %v", f.Calls)
	}
	// CF-04: the edge proxy is not in the group of the website user.
	if f.Called("usermod") {
		t.Errorf("the edge user got the group of the website: %v", f.Calls)
	}

	h, f = fakeHost(t, true)
	if err := h.EnsureUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	if f.Called("useradd") {
		t.Error("useradd ran for a user that exists")
	}
	if err := h.EnsureUser(context.Background(), "root"); err == nil {
		t.Error("the user root was accepted")
	}

	// A user with a UID that the firewall does not limit: the agent does not use it.
	h, f = fakeHost(t, true)
	h.LookupUID = func(string) (int, error) { return 1001, nil }
	if err := h.EnsureUser(context.Background(), "acme_x7k2"); err == nil || !strings.Contains(err.Error(), "not in the range") {
		t.Errorf("a user with the UID 1001 was accepted: %v", err)
	}
	if f.Called("loginctl") || f.Called("systemctl") {
		t.Errorf("the agent started the services of a user that is not in the range: %v", f.Calls)
	}
}

func TestWriteUnitReportsAChange(t *testing.T) {
	h, _ := fakeHost(t, true)
	changed, err := h.WriteUnit("acme_x7k2", "cf-acme_x7k2.container", "a")
	if err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	if changed, _ := h.WriteUnit("acme_x7k2", "cf-acme_x7k2.container", "a"); changed {
		t.Error("same content reported a change")
	}
	if _, err := os.Stat(filepath.Join(h.UnitRoot, "20001", "cf-acme_x7k2.container")); err != nil {
		t.Error(err)
	}
	if _, err := h.WriteUnit("acme_x7k2", "../evil", "a"); err == nil {
		t.Error("a path in the unit name was accepted")
	}
}

func TestPodmanRunsAsTheUser(t *testing.T) {
	h, f := fakeHost(t, true)
	h.Podman(context.Background(), "acme_x7k2", "pull", "img")
	if !f.Called("runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman pull img") {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestEnsureUserLinksTheSharedStorageConfig(t *testing.T) {
	h, f := fakeHost(t, true)
	h.StorageConfig = "/etc/cloudfactory/website-storage.conf"
	if err := h.EnsureUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	home := h.Home("acme_x7k2")
	for _, want := range []string{
		"install -d -o acme_x7k2 -g acme_x7k2 -m 0700 " + home + "/.config",
		// Root owns the directory of the link, so the user cannot change it.
		"install -d -o root -g root -m 0755 " + home + "/.config/containers",
		"ln -sfn /etc/cloudfactory/website-storage.conf " + home + "/.config/containers/storage.conf",
	} {
		if !f.Called(want) {
			t.Errorf("not called: %s\ncalls: %v", want, f.Calls)
		}
	}
}

func TestLinkStorageConfigRefusesALink(t *testing.T) {
	h, f := fakeHost(t, true)
	h.StorageConfig = "/etc/cloudfactory/website-storage.conf"
	home := h.Home("acme_x7k2")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	if err := h.LinkStorageConfig(context.Background(), "acme_x7k2"); err == nil {
		t.Error("a link in place of ~/.config was accepted")
	}
	if f.Called("install") || f.Called("ln") {
		t.Errorf("calls = %v", f.Calls)
	}
}

// Audit finding B7: userdel refuses a user that has a process. RemoveUser stops the user
// manager first, and waits for it.
func TestRemoveUserStopsTheUserManagerBeforeUserdel(t *testing.T) {
	h, f := fakeHost(t, true)
	for _, dir := range []string{h.SiteRoot("acme_x7k2"), h.ConfigDir("acme_x7k2"), filepath.Join(h.UnitRoot, "20001")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.RemoveUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"loginctl disable-linger acme_x7k2",
		"systemctl stop user@20001.service",
		"systemctl stop user-20001.slice",
		"pkill --signal KILL --uid 20001",
		"userdel --remove acme_x7k2",
	}
	if !slices.Equal(f.Calls, want) {
		t.Errorf("calls = %v, want %v", f.Calls, want)
	}
	// A later user can get the UID 20001. It must not get the units of this user.
	for _, dir := range []string{h.SiteRoot("acme_x7k2"), h.ConfigDir("acme_x7k2"), filepath.Join(h.UnitRoot, "20001")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s is not removed: %v", dir, err)
		}
	}
}

func TestRemoveUserTriesUserdelAgain(t *testing.T) {
	h, f := fakeHost(t, true)
	h.RemoveTries, h.RemoveDelay = 5, time.Millisecond
	attempts := 0
	f.Rules = []execx.Rule{
		// pkill gives status 1 when the user has no process.
		{Prefix: "pkill", Err: errors.New("exit status 1")},
		// A unit that is not loaded is not a reason to stop.
		{Prefix: "systemctl stop user-20001.slice", Err: errors.New("Unit user-20001.slice not loaded.")},
		{Prefix: "userdel", Do: func() error {
			attempts++
			if attempts < 3 {
				return errors.New("userdel: user acme_x7k2 is currently used by process 4242")
			}
			return nil
		}},
	}
	if err := h.RemoveUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Errorf("userdel ran %d times, want 3", attempts)
	}
	kills := 0
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "pkill ") {
			kills++
		}
	}
	if kills != 3 {
		t.Errorf("pkill ran %d times, want one time before each userdel", kills)
	}
}

func TestRemoveUserStopsAfterTheLastAttempt(t *testing.T) {
	h, f := fakeHost(t, true)
	h.RemoveTries, h.RemoveDelay = 3, time.Millisecond
	if err := os.MkdirAll(h.SiteRoot("acme_x7k2"), 0o755); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	f.Rules = []execx.Rule{{Prefix: "userdel", Do: func() error {
		attempts++
		return errors.New("userdel: user acme_x7k2 is currently used by process 4242")
	}}}
	err := h.RemoveUser(context.Background(), "acme_x7k2")
	if err == nil || !strings.Contains(err.Error(), "currently used by process") {
		t.Fatalf("err = %v", err)
	}
	if attempts != 3 {
		t.Errorf("userdel ran %d times, want 3", attempts)
	}
	// The files stay while the user stays, so that the next attempt has the same start.
	if _, err := os.Stat(h.SiteRoot("acme_x7k2")); err != nil {
		t.Errorf("the files of the user are gone although the user stays: %v", err)
	}
}

func TestRemoveUserStopsWhenTheJobStops(t *testing.T) {
	h, f := fakeHost(t, true)
	h.RemoveTries, h.RemoveDelay = 1000, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	f.Rules = []execx.Rule{{Prefix: "userdel", Do: func() error {
		cancel()
		return errors.New("userdel: user acme_x7k2 is currently used by process 4242")
	}}}
	if err := h.RemoveUser(ctx, "acme_x7k2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// A first attempt removed the user and then failed. The second attempt removes the files,
// and runs no command for a user that does not exist.
func TestRemoveUserIsSafeToRunAgain(t *testing.T) {
	h, f := fakeHost(t, false)
	for _, dir := range []string{h.SiteRoot("acme_x7k2"), h.ConfigDir("acme_x7k2")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.RemoveUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("calls = %v", f.Calls)
	}
	for _, dir := range []string{h.SiteRoot("acme_x7k2"), h.ConfigDir("acme_x7k2")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s is not removed: %v", dir, err)
		}
	}
	if err := h.RemoveUser(context.Background(), "root"); err == nil {
		t.Error("the user root was accepted")
	}
}

// dirRunner does the directory commands of EnsureUser on a real file system. "install -d"
// follows a symbolic link, as the command of the host does. The runner records each directory
// that a root command changed, with all links resolved, and each directory that the website
// user made.
type dirRunner struct {
	execx.Fake
	byRoot, byUser []string
}

func (r *dirRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	switch {
	case name == "install" && len(args) > 0 && args[0] == "-d":
		for i := 1; i < len(args); i++ {
			if args[i] == "-o" || args[i] == "-g" || args[i] == "-m" {
				i++
				continue
			}
			if err := os.MkdirAll(args[i], 0o750); err != nil {
				return "", err
			}
			// os.Chmod follows a link, like the chmod and the chown of "install -d".
			if err := os.Chmod(args[i], 0o750); err != nil {
				return "", err
			}
			dir, err := filepath.EvalSymlinks(args[i])
			if err != nil {
				return "", err
			}
			r.byRoot = append(r.byRoot, dir)
		}
		return "", nil
	case name == "runuser" && slices.Contains(args, "mkdir"):
		dir := args[len(args)-1]
		r.byUser = append(r.byUser, dir)
		return "", os.MkdirAll(dir, 0o750)
	}
	return r.Fake.Run(ctx, name, args...)
}

// Audit finding F01: the website user controls "app/public". When the agent makes a website
// again, for example after "Try again", root must not change the directory that a link at
// "app/public" points to.
func TestEnsureUserDoesNotFollowALinkOfTheWebsiteUser(t *testing.T) {
	h, _ := fakeHost(t, true)
	r := &dirRunner{}
	h.Run = r
	ctx := context.Background()
	if err := h.EnsureUser(ctx, "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(h.AppDir("acme_x7k2"), "public")
	if fi, err := os.Stat(public); err != nil || !fi.IsDir() {
		t.Fatalf("no default document root: %v", err)
	}
	if !slices.Contains(r.byUser, public) || slices.Contains(r.byRoot, public) {
		t.Errorf("the website user must make the document root: root %v, user %v", r.byRoot, r.byUser)
	}

	// The document root of a different customer. The website user has no access to it.
	other := filepath.Join(h.AppDir("other_a1b2"), "public")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(other, 0o700); err != nil {
		t.Fatal(err)
	}
	// The website user replaces its document root with a link, for example with SFTP.
	if err := os.Remove(public); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, public); err != nil {
		t.Fatal(err)
	}
	r.byRoot = nil
	if err := h.EnsureUser(ctx, "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	inOther := func() {
		t.Helper()
		for _, dir := range r.byRoot {
			if strings.HasPrefix(dir, h.SiteRoot("other_a1b2")) {
				t.Errorf("root changed %s through a link of the website user", dir)
			}
		}
		if fi, err := os.Stat(other); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("the mode of the other document root changed: %v %v", fi.Mode().Perm(), err)
		}
	}
	inOther()

	// A link in place of "app" is not from the agent: only root can write in the directory of
	// the user. The agent stops, and changes nothing.
	if err := os.RemoveAll(h.AppDir("acme_x7k2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(h.AppDir("other_a1b2"), h.AppDir("acme_x7k2")); err != nil {
		t.Fatal(err)
	}
	r.byRoot = nil
	if err := h.EnsureUser(ctx, "acme_x7k2"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("a link in place of app was accepted: %v", err)
	}
	inOther()
	// Only the two parent directories, which root owns, are made before the check.
	for _, dir := range r.byRoot {
		if dir != h.SitesDir && dir != filepath.Dir(h.Home("acme_x7k2")) {
			t.Errorf("root changed %s after the check failed", dir)
		}
	}
}
