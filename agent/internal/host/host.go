// Package host does the changes on the host for one hosting account: the Unix user, the
// Quadlet units, systemd and Podman. Each method is idempotent (spec section 6.3, step 6).
package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// EdgeUser is the Unix user of the edge proxy. It cannot read the files of the websites (CF-04).
const EdgeUser = "cf-edge"

// Host changes the host.
type Host struct {
	Run execx.Runner
	// SitesDir holds one home directory for each website user.
	SitesDir string
	// UnitRoot holds the rootless Quadlet units of each user, in a directory named by the UID.
	UnitRoot string
	// LookupUID gives the UID of a user. Tests replace it.
	LookupUID func(name string) (int, error)
}

// New gives a Host with the standard paths.
func New(run execx.Runner, sitesDir string) *Host {
	return &Host{Run: run, SitesDir: sitesDir, UnitRoot: "/etc/containers/systemd/users", LookupUID: lookupUID}
}

func lookupUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

// Home is the home directory of a website user.
func (h *Host) Home(unixUser string) string { return filepath.Join(h.SitesDir, unixUser) }

// EnsureUser makes the Unix user of a website if it does not exist. The user has no
// password and no login shell. systemd keeps the user services on when the user is not
// signed in ("linger"), so that the containers start at boot.
func (h *Host) EnsureUser(ctx context.Context, unixUser string) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	if _, err := h.LookupUID(unixUser); err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return err
		}
		if _, err := h.Run.Run(ctx, "useradd", "--create-home", "--home-dir", h.Home(unixUser), "--shell", "/usr/sbin/nologin", "--user-group", unixUser); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		// CF-04: only the website user can read its files. The edge proxy does not read them.
		{"chmod", "0700", h.Home(unixUser)},
		{"install", "-d", "-o", unixUser, "-g", unixUser, "-m", "0750", filepath.Join(h.Home(unixUser), "app"), filepath.Join(h.Home(unixUser), "app", "public")},
		{"loginctl", "enable-linger", unixUser},
	} {
		if _, err := h.Run.Run(ctx, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// RemoveUser stops the user services and removes the user and its files.
func (h *Host) RemoveUser(ctx context.Context, unixUser string) error {
	if err := validate.UnixUser(unixUser); err != nil {
		return err
	}
	if _, err := h.LookupUID(unixUser); err != nil {
		return nil // The user does not exist. There is nothing to do.
	}
	if _, err := h.Run.Run(ctx, "loginctl", "disable-linger", unixUser); err != nil {
		return err
	}
	_, err := h.Run.Run(ctx, "userdel", "--remove", unixUser)
	return err
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
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
