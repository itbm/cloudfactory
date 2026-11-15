// Package host does the changes on the host for one hosting account: the Unix user, the
// Quadlet units, systemd and Podman. Each method is idempotent (spec section 6.3, step 6).
package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// EdgeUser is the Unix user of the edge proxy. It cannot read the files of the websites (CF-04).
const EdgeUser = "cf-edge"

// AgentUser is the Unix user of the worker of the agent. The worker has the connection to
// the panel and the jobs. It is not root: the root helper of the agent does the work that
// needs root (package gate).
const AgentUser = "cf-agent"

// WebsiteIDMin and WebsiteIDMax are the limits of the UID and the GID of a website user. The
// installer has the same values (WEBSITE_UID_MIN and WEBSITE_UID_MAX in install.sh).
//
// Audit finding S1: pasta opens each connection of a website container as the Unix user of
// the website. The firewall of the installer finds these connections by the UID range, and
// refuses the connections to the services of the host, to private networks and to the
// metadata service of a cloud provider.
const (
	WebsiteIDMin = 20000
	WebsiteIDMax = 59999
)

// DatabaseIDMin and DatabaseIDMax are the limits of the UID and the GID of the Unix user of a
// database server. The installer has the same values (DATABASE_UID_MIN and DATABASE_UID_MAX in
// install.sh). The range is not the range of the website users: the firewall gives the two
// ranges different rights. A database server opens no connection.
const (
	DatabaseIDMin = 10000
	DatabaseIDMax = 19999
)

// DatabaseUserPrefix starts the name of the Unix user of a database server. The name of a
// website user has no "-", so the two types of user never have the same name.
const DatabaseUserPrefix = "db-"

// SitesGroup is the group of all website users and of the users of the database servers. Only
// this group can read the shared images (package images).
const SitesGroup = "cf-sites"

var databaseUserRE = regexp.MustCompile(`^db-[0-9a-z]{26}$`)

// DatabaseUser gives the Unix user of a database server. id is the ID of the database server
// in lower case.
func DatabaseUser(id string) string { return DatabaseUserPrefix + id }

// CheckDatabaseUser checks the name of the Unix user of a database server: "db-" and the ID
// of the database server in lower case.
func CheckDatabaseUser(name string) error {
	if !databaseUserRE.MatchString(name) {
		return fmt.Errorf("host: %q is not the Unix user of a database server", name)
	}
	return nil
}

// Host changes the host.
type Host struct {
	Run execx.Runner
	// SitesDir holds one directory for each website user, with the files of the website in
	// "app". Root owns the directory of each user, so that SFTP can lock the user in it.
	SitesDir string
	// HomesDir holds the home directory of each website user, with its rootless Podman storage.
	// SFTP cannot reach it. The default is "homes" next to SitesDir.
	HomesDir string
	// UnitRoot holds the rootless Quadlet units of each user, in a directory named by the UID.
	UnitRoot string
	// ConfigRoot holds the files that the agent makes for each website user, for example the
	// php.ini (R-16). Only root can write in it, so the agent never writes as root in a
	// directory that the website user controls.
	ConfigRoot string
	// StorageConfig is the Podman storage configuration of all website users, with the shared
	// image store (package images). Empty means that the website users keep the defaults.
	StorageConfig string
	// LookupUID gives the UID of a user. Tests replace it.
	LookupUID func(name string) (int, error)
	// LookupGID gives the primary GID of a user. Nil means the user database of the host.
	LookupGID func(name string) (int, error)
	// RemoveTries and RemoveDelay are the attempts of userdel in RemoveUser and the time
	// between them. Zero means 20 attempts with 500 ms. Tests use a short time.
	RemoveTries int
	RemoveDelay time.Duration
	// ReadQuota gives the disk space that a UID uses on the file system at mount, and its hard
	// limit, in bytes. Nil means quotactl(2). Tests replace it.
	ReadQuota func(mount string, uid int) (used, limit int64, err error)
	// Root is set in the worker of the agent, which is not root. The methods that change a
	// user, a unit or a quota then ask the root helper. Run is then the connection to the
	// root helper too, and the helper checks each command of Systemctl and Podman.
	Root Root
}

// Root has the methods of Host that only root can do. The root helper has a Host with no
// Root, and does them there.
type Root interface {
	EnsureUser(ctx context.Context, unixUser string) error
	LinkStorageConfig(ctx context.Context, unixUser string) error
	RemoveUser(ctx context.Context, unixUser string) error
	WriteUnit(unixUser, name, content string) (bool, error)
	RemoveUnit(unixUser, name string) error
	SetDiskQuota(ctx context.Context, unixUser, mount string, bytes int64) error
	BlockDevice(ctx context.Context, dir string) (string, error)
	QuotaMount(ctx context.Context, dir string) string
}

// New gives a Host with the standard paths.
func New(run execx.Runner, sitesDir string) *Host {
	return &Host{Run: run, SitesDir: sitesDir, UnitRoot: "/etc/containers/systemd/users", ConfigRoot: "/etc/cloudfactory/websites", LookupUID: lookupUID}
}

func lookupUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

// GID gives the primary GID of a user.
func (h *Host) GID(name string) (int, error) {
	if h.LookupGID != nil {
		return h.LookupGID(name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Gid)
}

// Home is the home directory of a website user.
func (h *Host) Home(unixUser string) string {
	dir := h.HomesDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.SitesDir), "homes")
	}
	return filepath.Join(dir, unixUser)
}

// SiteRoot is the directory of a website user in SitesDir. Root owns it. The SFTP container
// locks the user in it (ChrootDirectory).
func (h *Host) SiteRoot(unixUser string) string { return filepath.Join(h.SitesDir, unixUser) }

// AppDir holds the files of a website. The container mounts it at /app.
func (h *Host) AppDir(unixUser string) string { return filepath.Join(h.SiteRoot(unixUser), "app") }

// ConfigDir is the directory of the generated files of a website user. Root owns it.
func (h *Host) ConfigDir(unixUser string) string { return filepath.Join(h.ConfigRoot, unixUser) }

// EnsureUser makes the Unix user of a website if it does not exist. The user has no
// password and no login shell. systemd keeps the user services on when the user is not
// signed in ("linger"), so that the containers start at boot.
func (h *Host) EnsureUser(ctx context.Context, unixUser string) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	if h.Root != nil {
		return h.Root.EnsureUser(ctx, unixUser)
	}
	if err := h.EnsureAccount(ctx, unixUser); err != nil {
		return err
	}
	// Audit finding F01: "install -d" follows a symbolic link, and changes the owner and the
	// mode of the directory that the link points to. Thus root changes only the two directories
	// that the website user cannot replace: only root can write in SitesDir and in the directory
	// of the user. A path there that is not a directory is not from the agent, so the agent
	// stops. The website user controls all that is in "app". Root changes nothing there.
	for _, dir := range []string{h.SiteRoot(unixUser), h.AppDir(unixUser)} {
		if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
			return fmt.Errorf("host: %s is not a directory", dir)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, args := range [][]string{
		// CF-04: only the website user can read its files. The edge proxy does not read them.
		{"chmod", "0700", h.Home(unixUser)},
		// SFTP needs a directory that root owns and that the user cannot change (ChrootDirectory).
		{"install", "-d", "-o", "root", "-g", unixUser, "-m", "0750", h.SiteRoot(unixUser)},
		{"install", "-d", "-o", unixUser, "-g", unixUser, "-m", "0750", h.AppDir(unixUser)},
		{"loginctl", "enable-linger", unixUser},
	} {
		if _, err := h.Run.Run(ctx, args[0], args[1:]...); err != nil {
			return err
		}
	}
	// The website user makes the default document root. A link at "public" is then only a link
	// of the user to a place that the user can change. A website operates with no "public"
	// directory, for example with a different document root, so an error is only in the log.
	public := filepath.Join(h.AppDir(unixUser), "public")
	if _, err := h.Run.Run(ctx, "runuser", "-u", unixUser, "--", "mkdir", "-p", "-m", "0750", public); err != nil {
		slog.Warn("the website user cannot make the default document root", "user", unixUser, "dir", public, "err", err)
	}
	if err := h.LinkStorageConfig(ctx, unixUser); err != nil {
		return err
	}
	return h.startUserManager(ctx, unixUser)
}

// EnsureAccount makes the Unix user of a website and its home directory, if the user does not
// exist. It does not make the directory of the website in SitesDir: the root helper can put
// a file system of its own there first (a disk image). Only root runs it.
func (h *Host) EnsureAccount(ctx context.Context, unixUser string) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	// Other users can go through the directories, but cannot list them.
	if _, err := h.Run.Run(ctx, "install", "-d", "-m", "0711", filepath.Dir(h.Home(unixUser)), h.SitesDir); err != nil {
		return err
	}
	// The firewall limits only the users in the range. The agent does not start a website as a
	// user that the firewall does not find, for example a user that an administrator made.
	_, err := h.addUser(ctx, unixUser, h.Home(unixUser), WebsiteIDMin, WebsiteIDMax, "website users")
	return err
}

// addUser makes a Unix user with a UID and a GID in a range, if the user does not exist. The
// user has no password and no login shell. It gives the UID. A user that exists with a UID
// outside the range is an error: the agent did not make that user. what names the range in
// the error.
func (h *Host) addUser(ctx context.Context, unixUser, home string, min, max int, what string) (int, error) {
	if _, err := h.LookupUID(unixUser); err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return 0, err
		}
		lo, hi := strconv.Itoa(min), strconv.Itoa(max)
		if _, err := h.Run.Run(ctx, "useradd", "-K", "UID_MIN="+lo, "-K", "UID_MAX="+hi, "-K", "GID_MIN="+lo, "-K", "GID_MAX="+hi,
			"--create-home", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--user-group", "--groups", SitesGroup, unixUser); err != nil {
			return 0, err
		}
	}
	uid, err := h.LookupUID(unixUser)
	if err != nil {
		return 0, err
	}
	if uid < min || uid > max {
		return 0, fmt.Errorf("the user %s has the UID %d, which is not in the range of the %s (%d to %d)", unixUser, uid, what, min, max)
	}
	return uid, nil
}

// startUserManager waits until the user manager of a user is ready.
//
// Audit batch 2, finding 12: enable-linger starts the user manager, but does not wait. The
// start of user@<uid>.service waits until the manager and /run/user/<uid> are ready.
func (h *Host) startUserManager(ctx context.Context, unixUser string) error {
	uid, err := h.LookupUID(unixUser)
	if err != nil {
		return err
	}
	_, err = h.Run.Run(ctx, "systemctl", "start", fmt.Sprintf("user@%d.service", uid))
	return err
}

// EnsureDatabaseUser makes the Unix user of a database server if it does not exist, with its
// home directory. The user is as a website user: it has no password and no login shell, its
// user services start at boot, and its Podman uses the shared image store. Only root calls
// this function (the root helper of the agent).
func (h *Host) EnsureDatabaseUser(ctx context.Context, unixUser, home string) error {
	if err := CheckDatabaseUser(unixUser); err != nil {
		return err
	}
	if _, err := h.addUser(ctx, unixUser, home, DatabaseIDMin, DatabaseIDMax, "users of the database servers"); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"chmod", "0700", home},
		{"loginctl", "enable-linger", unixUser},
	} {
		if _, err := h.Run.Run(ctx, args[0], args[1:]...); err != nil {
			return err
		}
	}
	if err := h.linkStorageConfig(unixUser, home); err != nil {
		return err
	}
	return h.startUserManager(ctx, unixUser)
}

// RemoveDatabaseUser stops the user services of the user of a database server, and removes
// the user and its home directory. It is safe to run it again after a failure. Only root
// calls this function.
func (h *Host) RemoveDatabaseUser(ctx context.Context, unixUser string) error {
	if err := CheckDatabaseUser(unixUser); err != nil {
		return err
	}
	return h.removeAccount(ctx, unixUser)
}

// LinkStorageConfig makes ~/.config/containers/storage.conf of a website user a link to
// StorageConfig, so that each Podman command of the user, also the ones that systemd starts,
// uses the shared image store. Root owns ~/.config/containers, so the user cannot change the
// link (R-16).
//
// Second audit, the privilege boundary: the website user owns its home directory, and can put
// a link in place of each path in it. Thus root opens these paths with no symbolic links
// (package safefs). A link in place of ~/.config or of ~/.config/containers is an error.
func (h *Host) LinkStorageConfig(ctx context.Context, unixUser string) error {
	if h.Root != nil {
		return h.Root.LinkStorageConfig(ctx, unixUser)
	}
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	return h.linkStorageConfig(unixUser, h.Home(unixUser))
}

// linkStorageConfig is LinkStorageConfig for a user with the home directory home.
func (h *Host) linkStorageConfig(unixUser, home string) error {
	if h.StorageConfig == "" {
		return nil
	}
	uid, gid, err := h.FileOwner(unixUser)
	if err != nil {
		return err
	}
	rootUID, rootGID := 0, 0
	if os.Geteuid() != 0 {
		rootUID, rootGID = safefs.Keep, safefs.Keep
	}
	dir, err := safefs.Open(home)
	if err != nil {
		return err
	}
	defer dir.Close()
	const config, containers = ".config", ".config/containers"
	for _, d := range []struct {
		rel      string
		mode     os.FileMode
		uid, gid int
	}{{config, 0o700, uid, gid}, {containers, 0o755, rootUID, rootGID}} {
		if err := dir.MkdirAll(d.rel, d.mode, d.uid, d.gid); err != nil {
			return err
		}
		// A directory from an earlier release gets the owner and the mode again.
		if err := dir.SetDir(d.rel, d.mode, d.uid, d.gid); err != nil {
			return err
		}
	}
	return dir.Symlink(h.StorageConfig, containers+"/storage.conf")
}

// FileOwner gives the UID and the GID for the files of a user. Only root can give a file
// to a different user: a process that is not root, for example a test, keeps the owner.
func (h *Host) FileOwner(unixUser string) (uid, gid int, err error) {
	if os.Geteuid() != 0 {
		return safefs.Keep, safefs.Keep, nil
	}
	if uid, err = h.LookupUID(unixUser); err != nil {
		return 0, 0, err
	}
	gid, err = h.GID(unixUser)
	return uid, gid, err
}

// RemoveUser stops the user services and removes the user and its files. It is safe to run it
// again after a failure.
//
// Audit finding B7: userdel refuses a user that has a process ("user ... is currently used by
// process ..."). "loginctl disable-linger" only asks systemd to stop the user manager, and
// does not wait. Thus this function stops the user manager and waits for it, stops the
// processes that remain, and tries userdel again for a short time.
func (h *Host) RemoveUser(ctx context.Context, unixUser string) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	if h.Root != nil {
		return h.Root.RemoveUser(ctx, unixUser)
	}
	if err := h.removeAccount(ctx, unixUser); err != nil {
		return err
	}
	// With no user, a first attempt removed the user and then failed. Remove the files that
	// have the name of the user. os.RemoveAll removes a link, not the file that the link
	// points to.
	if err := os.RemoveAll(h.SiteRoot(unixUser)); err != nil {
		return err
	}
	return os.RemoveAll(h.ConfigDir(unixUser))
}

// RemoveAccount stops the user services of a website user and removes the user, its home
// directory and its units. It keeps the directory of the website in SitesDir. A user that
// does not exist is not an error. Only root runs it.
func (h *Host) RemoveAccount(ctx context.Context, unixUser string) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	return h.removeAccount(ctx, unixUser)
}

// removeAccount stops the user services of a user and removes the user, its home directory and
// its units. A user that does not exist is not an error.
func (h *Host) removeAccount(ctx context.Context, unixUser string) error {
	uid, err := h.LookupUID(unixUser)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return nil
		}
		return err
	}
	if _, err := h.Run.Run(ctx, "loginctl", "disable-linger", unixUser); err != nil {
		return err
	}
	// "systemctl stop" waits until the unit has stopped. The user manager has all
	// containers of the user, and the slice has also the sessions and the runtime
	// directory. A unit that is not loaded is not an error here: userdel is the check.
	for _, unit := range []string{fmt.Sprintf("user@%d.service", uid), fmt.Sprintf("user-%d.slice", uid)} {
		if _, err := h.Run.Run(ctx, "systemctl", "stop", unit); err != nil {
			slog.Warn("stop the services of a user", "user", unixUser, "unit", unit, "err", err)
		}
	}
	tries, delay := h.RemoveTries, h.RemoveDelay
	if tries == 0 {
		tries, delay = 20, 500*time.Millisecond
	}
	for attempt := 1; ; attempt++ {
		// pkill gives status 1 when the user has no process. That is the usual result.
		h.Run.Run(ctx, "pkill", "--signal", "KILL", "--uid", strconv.Itoa(uid))
		if _, err = h.Run.Run(ctx, "userdel", "--remove", unixUser); err == nil {
			break
		}
		if attempt >= tries {
			return fmt.Errorf("remove the user %s: %w", unixUser, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	// A later user can get the same UID. It must not get the units of this user. The user is
	// not there now, so the directory comes from the UID that the user had.
	return os.RemoveAll(filepath.Join(h.UnitRoot, strconv.Itoa(uid)))
}

// UnitDir is the directory of the Quadlet units of a user.
func (h *Host) UnitDir(unixUser string) (string, error) {
	uid, err := h.LookupUID(unixUser)
	if err != nil {
		return "", err
	}
	return filepath.Join(h.UnitRoot, strconv.Itoa(uid)), nil
}

// WriteUnit writes a Quadlet unit for a user. It returns true when the file changed.
func (h *Host) WriteUnit(unixUser, name, content string) (bool, error) {
	if h.Root != nil {
		return h.Root.WriteUnit(unixUser, name, content)
	}
	if strings.ContainsAny(name, "/\\") {
		return false, fmt.Errorf("host: the unit name %q is not valid", name)
	}
	dir, err := h.UnitDir(unixUser)
	if err != nil {
		return false, err
	}
	return WriteFile(filepath.Join(dir, name), content, 0o644)
}

// RemoveUnit removes a Quadlet unit. A unit that does not exist is not an error.
func (h *Host) RemoveUnit(unixUser, name string) error {
	if h.Root != nil {
		return h.Root.RemoveUnit(unixUser, name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("host: the unit name %q is not valid", name)
	}
	dir, err := h.UnitDir(unixUser)
	if err != nil {
		return nil
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Systemctl runs systemctl for the user manager of a website user.
func (h *Host) Systemctl(ctx context.Context, unixUser string, args ...string) (string, error) {
	return h.Run.Run(ctx, "systemctl", append([]string{"--user", "--machine=" + unixUser + "@"}, args...)...)
}

// Podman runs Podman as a website user. Rootless Podman needs the runtime directory of the user.
func (h *Host) Podman(ctx context.Context, unixUser string, args ...string) (string, error) {
	uid, err := h.LookupUID(unixUser)
	if err != nil {
		return "", err
	}
	base := []string{"-u", unixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid), "podman"}
	return h.Run.Run(ctx, "runuser", append(base, args...)...)
}

// WriteFile writes a file in one step. It returns true when the content changed.
func WriteFile(path, content string, mode os.FileMode) (bool, error) {
	// R-16: never follow a symbolic link that somebody put in place of the file.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("host: %s is a symbolic link", path)
	}
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	// A new temporary file with a random name, so that a link with a known name cannot
	// redirect the write.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		return false, err
	}
	return true, os.Rename(f.Name(), path)
}

// BlockDevice gives the disk of the file system of dir, for example "/dev/vda" for "/dev/vda1".
// The I/O limit of cgroup v2 needs a whole disk or a device-mapper volume, not a partition.
func (h *Host) BlockDevice(ctx context.Context, dir string) (string, error) {
	if h.Root != nil {
		return h.Root.BlockDevice(ctx, dir)
	}
	out, err := h.Run.Run(ctx, "findmnt", "--noheadings", "--output", "SOURCE", "--target", dir)
	if err != nil {
		return "", err
	}
	source := strings.TrimSpace(out)
	if !strings.HasPrefix(source, "/dev/") {
		return "", fmt.Errorf("host: the file system of %s is not on a block device (%s)", dir, source)
	}
	if strings.HasPrefix(source, "/dev/mapper/") {
		return source, nil
	}
	parent, err := h.Run.Run(ctx, "lsblk", "--noheadings", "--nodeps", "--output", "PKNAME", source)
	if err != nil {
		return "", err
	}
	if p := strings.TrimSpace(parent); p != "" {
		return "/dev/" + p, nil
	}
	return source, nil
}

// QuotaMount gives the mount point of dir when user quotas are on there, or "".
func (h *Host) QuotaMount(ctx context.Context, dir string) string {
	if h.Root != nil {
		return h.Root.QuotaMount(ctx, dir)
	}
	out, err := h.Run.Run(ctx, "findmnt", "--noheadings", "--output", "TARGET", "--target", dir)
	if err != nil {
		return ""
	}
	mount := strings.TrimSpace(out)
	// "quotaon -p" prints "user quota on / (/dev/vda1) is on" and has a non-zero exit status
	// when a quota is on, so the agent reads only the output.
	state, _ := h.Run.Run(ctx, "quotaon", "--print-state", "--user", mount)
	if strings.Contains(state, "is on") {
		return mount
	}
	return ""
}

// SetDiskQuota limits the disk space of a website user on the file system at mount (FR-202).
// The hard limit is the plan limit. The soft limit is 95% of it.
func (h *Host) SetDiskQuota(ctx context.Context, unixUser, mount string, bytes int64) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	if h.Root != nil {
		return h.Root.SetDiskQuota(ctx, unixUser, mount, bytes)
	}
	return h.setQuota(ctx, unixUser, mount, bytes)
}
