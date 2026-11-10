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
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// EdgeUser is the Unix user of the edge proxy. It cannot read the files of the websites (CF-04).
const EdgeUser = "cf-edge"

// SitesGroup is the group of all website users. Only this group can read the shared runtime
// images (package images).
const SitesGroup = "cf-sites"

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
	// Other users can go through the directories, but cannot list them.
	if _, err := h.Run.Run(ctx, "install", "-d", "-m", "0711", filepath.Dir(h.Home(unixUser)), h.SitesDir); err != nil {
		return err
	}
	if _, err := h.LookupUID(unixUser); err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return err
		}
		if _, err := h.Run.Run(ctx, "useradd", "--create-home", "--home-dir", h.Home(unixUser), "--shell", "/usr/sbin/nologin", "--user-group", "--groups", SitesGroup, unixUser); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		// CF-04: only the website user can read its files. The edge proxy does not read them.
		{"chmod", "0700", h.Home(unixUser)},
		// SFTP needs a directory that root owns and that the user cannot change (ChrootDirectory).
		{"install", "-d", "-o", "root", "-g", unixUser, "-m", "0750", h.SiteRoot(unixUser)},
		{"install", "-d", "-o", unixUser, "-g", unixUser, "-m", "0750", h.AppDir(unixUser), filepath.Join(h.AppDir(unixUser), "public")},
		{"loginctl", "enable-linger", unixUser},
	} {
		if _, err := h.Run.Run(ctx, args[0], args[1:]...); err != nil {
			return err
		}
	}
	if err := h.LinkStorageConfig(ctx, unixUser); err != nil {
		return err
	}
	// Audit batch 2, finding 12: enable-linger starts the user manager, but does not wait. The
	// start of user@<uid>.service waits until the manager and /run/user/<uid> are ready.
	uid, err := h.LookupUID(unixUser)
	if err != nil {
		return err
	}
	_, err = h.Run.Run(ctx, "systemctl", "start", fmt.Sprintf("user@%d.service", uid))
	return err
}

// LinkStorageConfig makes ~/.config/containers/storage.conf of a website user a link to
// StorageConfig, so that each Podman command of the user, also the ones that systemd starts,
// uses the shared image store. Root owns ~/.config/containers, so the user cannot change the
// link, and the agent never writes in a directory that the user controls (R-16).
func (h *Host) LinkStorageConfig(ctx context.Context, unixUser string) error {
	if h.StorageConfig == "" {
		return nil
	}
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	config := filepath.Join(h.Home(unixUser), ".config")
	dir := filepath.Join(config, "containers")
	for _, p := range []string{config, dir} {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("host: %s is a symbolic link", p)
		}
	}
	for _, args := range [][]string{
		{"install", "-d", "-o", unixUser, "-g", unixUser, "-m", "0700", config},
		{"install", "-d", "-o", "root", "-g", "root", "-m", "0755", dir},
		{"ln", "-sfn", h.StorageConfig, filepath.Join(dir, "storage.conf")},
	} {
		if _, err := h.Run.Run(ctx, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
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
	uid, err := h.LookupUID(unixUser)
	if err == nil {
		if _, err := h.Run.Run(ctx, "loginctl", "disable-linger", unixUser); err != nil {
			return err
		}
		// "systemctl stop" waits until the unit has stopped. The user manager has all
		// containers of the user, and the slice has also the sessions and the runtime
		// directory. A unit that is not loaded is not an error here: userdel is the check.
		for _, unit := range []string{fmt.Sprintf("user@%d.service", uid), fmt.Sprintf("user-%d.slice", uid)} {
			if _, err := h.Run.Run(ctx, "systemctl", "stop", unit); err != nil {
				slog.Warn("stop the services of a website user", "user", unixUser, "unit", unit, "err", err)
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
		// A later user can get the same UID. It must not get the units of this user.
		if dir, err := h.UnitDir(unixUser); err == nil {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	// With no user, a first attempt removed the user and then failed. Remove the files that
	// have the name of the user. os.RemoveAll removes a link, not the file that the link
	// points to.
	if err := os.RemoveAll(h.SiteRoot(unixUser)); err != nil {
		return err
	}
	return os.RemoveAll(h.ConfigDir(unixUser))
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
	hard := bytes / 1024
	soft := hard * 95 / 100
	_, err := h.Run.Run(ctx, "setquota", "--user", unixUser, strconv.FormatInt(soft, 10), strconv.FormatInt(hard, 10), "0", "0", mount)
	return err
}
