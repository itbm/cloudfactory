package actions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// SFTP access (spec section 10). One container on each server, cf-sftp, serves SFTP for all
// websites on the SFTP port (2200 by default). It runs OpenSSH in gVisor (images/sftp). The
// SSH service of the host is only for the administrators.
//
// The agent gives the container two things, as read-only directories:
//   - the SSH keys of each website user, in SftpKeysDir/<user>.keys;
//   - the website users that have a key, in SftpUsersDir/passwd and SftpUsersDir/group.
//
// sshd reads both at each sign-in, so a change has effect with no restart of the container.
// After the sign-in, the session runs as the Unix user of the website (with the same UID and
// GID as on the host), locked in the directory of the website. Thus a new file has the correct
// owner on the host and in the website container.

// hostSftpHeader is the first line of the SSH configuration that release 0.1 added to the host.
const hostSftpHeader = "# Managed by the Cloudfactory installer."

// sftpRuleMarker marks the firewall rule of the SFTP port in the file of the installer.
const sftpRuleMarker = "# cloudfactory-sftp"

// sftpServiceName is the systemd service that Quadlet makes from quadlet.SftpUnitName.
const sftpServiceName = "cf-sftp.service"

// minWebsiteID is the lowest UID and GID of a website user. The agent never gives a lower ID,
// for example root, to the SFTP container.
const minWebsiteID = host.WebsiteIDMin

// sftpKeysDir holds the authorized keys of each website user. Root owns the files, so that
// the website cannot change them.
func (d *Deps) sftpKeysDir() string {
	if d.SftpKeysDir != "" {
		return d.SftpKeysDir
	}
	return "/etc/cloudfactory/sftp"
}

func (d *Deps) sftpUsersDir() string {
	if d.SftpUsersDir != "" {
		return d.SftpUsersDir
	}
	return "/etc/cloudfactory/sftp-users"
}

func (d *Deps) sftpHostKeysDir() string {
	return filepath.Join(d.DataDir, "sftp-host-keys")
}

func (d *Deps) systemUnitDir() string {
	if d.SystemUnitDir != "" {
		return d.SystemUnitDir
	}
	return "/etc/containers/systemd"
}

func (d *Deps) firewallFile() string {
	if d.FirewallFile != "" {
		return d.FirewallFile
	}
	return "/etc/nftables.d-cloudfactory.nft"
}

func (d *Deps) hostSftpConfig() string {
	if d.HostSftpConfig != "" {
		return d.HostSftpConfig
	}
	return "/etc/ssh/sshd_config.d/50-cloudfactory-sftp.conf"
}

func (d *Deps) sftpPort() int {
	if d.SftpPort > 0 {
		return d.SftpPort
	}
	return quadlet.DefaultSftpPort
}

// SftpPortInUse is the port that the website owners use now. It is 22 while the SSH service
// of the host still has the SFTP configuration of release 0.1, because the move to the
// container is not complete.
func (d *Deps) SftpPortInUse() int {
	if b, err := os.ReadFile(d.hostSftpConfig()); err == nil && strings.HasPrefix(string(b), hostSftpHeader) {
		return 22
	}
	return d.sftpPort()
}

// SetSftp sets the SFTP access of a website: on or off, and the SSH keys.
func (d *Deps) SetSftp(req *agentv1.SetSftpRequest) (jobs.Job, error) {
	user := req.GetUnixUser()
	errs := []error{validate.ID(req.GetWebsiteId()), validate.UnixUser(user)}
	for _, k := range req.GetAuthorizedKeys() {
		errs = append(errs, validate.SSHKey(k))
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	keysFile := filepath.Join(d.sftpKeysDir(), user+".keys")
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Set SFTP access", Do: func(ctx context.Context) error {
			// "restrict" turns off forwarding and terminals for each key.
			var keys strings.Builder
			keys.WriteString("# Managed by the Cloudfactory agent. Change the keys in the panel.\n")
			if req.GetEnabled() {
				for _, k := range req.GetAuthorizedKeys() {
					keys.WriteString("restrict " + k + "\n")
				}
			}
			// sshd reads the file as the user, so the user can read it but not change it.
			if _, err := host.WriteFile(keysFile, keys.String(), 0o644); err != nil {
				return err
			}
			// "!" locks the password, also a password from an earlier version, so that the
			// website user cannot sign in to the SSH service of the host with a password.
			if _, err := d.Run.Run(ctx, "usermod", "--password", "!", user); err != nil {
				return err
			}
			// Only a user with a key is in the user list of the SFTP container.
			return d.SyncSftpUsers()
		}},
	}}, nil
}

// SyncSftpUsers writes the user list of the SFTP container: each website user that has at
// least one SSH key in SftpKeysDir. A user with no key does not exist in the container, so it
// cannot sign in.
func (d *Deps) SyncSftpUsers() error {
	d.sftpMu.Lock()
	defer d.sftpMu.Unlock()
	entries, err := os.ReadDir(d.sftpKeysDir())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var users []string
	for _, e := range entries {
		user, ok := strings.CutSuffix(e.Name(), ".keys")
		if !ok || !e.Type().IsRegular() || validate.UnixUser(user) != nil {
			continue
		}
		if hasKey(filepath.Join(d.sftpKeysDir(), e.Name())) {
			users = append(users, user)
		}
	}
	slices.Sort(users)
	var passwd, group strings.Builder
	passwd.WriteString("# Managed by the Cloudfactory agent: the website users of the SFTP service.\n")
	group.WriteString("# Managed by the Cloudfactory agent: the website users of the SFTP service.\n")
	for _, user := range users {
		uid, err := d.Host.LookupUID(user)
		if err != nil {
			slog.Warn("SFTP: the website user does not exist", "user", user, "err", err)
			continue
		}
		gid, err := d.Host.GID(user)
		if err != nil {
			slog.Warn("SFTP: the website user does not exist", "user", user, "err", err)
			continue
		}
		if uid < minWebsiteID || gid < minWebsiteID {
			slog.Warn("SFTP: the website user has a system ID", "user", user, "uid", uid, "gid", gid)
			continue
		}
		// The home directory is in the chroot of the session (ChrootDirectory).
		fmt.Fprintf(&passwd, "%s:x:%d:%d:Cloudfactory website:/app:/usr/sbin/nologin\n", user, uid, gid)
		fmt.Fprintf(&group, "%s:x:%d:\n", user, gid)
	}
	if err := os.MkdirAll(d.sftpUsersDir(), 0o755); err != nil {
		return err
	}
	// The group file first: sshd reads the groups of a user after it finds the user.
	if _, err := host.WriteFile(filepath.Join(d.sftpUsersDir(), "group"), group.String(), 0o644); err != nil {
		return err
	}
	_, err = host.WriteFile(filepath.Join(d.sftpUsersDir(), "passwd"), passwd.String(), 0o644)
	return err
}

// hasKey reports whether a keys file has at least one key.
func hasKey(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}
	return false
}

// EnsureSftp starts the SFTP container, opens its port in the firewall, and then removes the
// SFTP configuration of release 0.1 from the SSH service of the host. Each step is idempotent,
// so the agent does this at each start. If the container does not answer, the agent keeps the
// configuration of release 0.1, so that SFTP continues to operate on port 22.
func (d *Deps) EnsureSftp(ctx context.Context) error {
	if d.SftpImage == "" {
		return errors.New("SFTP: no image")
	}
	port := d.sftpPort()
	if err := validate.SftpPort(port); err != nil {
		return err
	}
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{d.sftpKeysDir(), 0o755}, {d.sftpUsersDir(), 0o755}, {d.sftpHostKeysDir(), 0o700}} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return err
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return err
		}
	}
	if err := d.SyncSftpUsers(); err != nil {
		return err
	}
	// Root pulls the image into its own storage. The signature policy checks it (FR-1101).
	if _, err := d.Run.Run(ctx, "podman", "image", "exists", d.SftpImage); err != nil {
		if _, err := d.Run.Run(ctx, "podman", "pull", "--quiet", d.SftpImage); err != nil {
			return err
		}
	}
	unit := quadlet.SftpUnit(quadlet.Sftp{
		Image: d.SftpImage, Port: port, SitesDir: d.Host.SitesDir, KeysDir: d.sftpKeysDir(),
		UsersDir: d.sftpUsersDir(), HostKeysDir: d.sftpHostKeysDir(), Gvisor: d.runtime(),
	})
	changed, err := host.WriteFile(filepath.Join(d.systemUnitDir(), quadlet.SftpUnitName), unit, 0o644)
	if err != nil {
		return err
	}
	if changed {
		if _, err := d.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		// A new image or a new port. The open sessions stop.
		if _, err := d.Run.Run(ctx, "systemctl", "restart", sftpServiceName); err != nil {
			return err
		}
	} else if _, err := d.Run.Run(ctx, "systemctl", "start", sftpServiceName); err != nil {
		return err
	}
	if err := d.openSftpPort(ctx, port); err != nil {
		return err
	}
	ready := d.SftpReady
	if ready == nil {
		ready = sshBanner
	}
	if err := d.waitSftp(ctx, ready, port); err != nil {
		return fmt.Errorf("SFTP: the container does not answer on port %d, so SFTP stays on the SSH service of the host: %w", port, err)
	}
	return d.retireHostSftp(ctx)
}

func (d *Deps) waitSftp(ctx context.Context, ready func(context.Context, int) error, port int) error {
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		err := ready(ctx, port)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// sshBanner connects to the SFTP port and reads the SSH version line of the server. The agent
// uses ServiceAddress, not 127.0.0.1: pasta sends a connection from the host loopback address
// to the loopback address in the container, where gVisor does not listen (quadlet.ServiceAddress).
func sshBanner(ctx context.Context, port int) error {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(quadlet.ServiceAddress, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "SSH-2.0-") {
		return fmt.Errorf("not an SSH server: %q", strings.TrimSpace(line))
	}
	return nil
}

// openSftpPort opens the SFTP port in the firewall of the installer. A file from release 0.1
// has no rule for it: the agent adds the rule after the rule of port 22. If the port changes,
// the agent changes the rule. With no file, the administrator manages the firewall.
func (d *Deps) openSftpPort(ctx context.Context, port int) error {
	path := d.firewallFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		slog.Warn("SFTP: no Cloudfactory firewall file; open the SFTP port in your firewall", "file", path, "port", port)
		return nil
	}
	if err != nil {
		return err
	}
	next, err := setSftpRule(string(b), port)
	if err != nil {
		return fmt.Errorf("SFTP: %s: %w", path, err)
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

// setSftpRule gives the firewall file with one accept rule for the SFTP port.
func setSftpRule(content string, port int) (string, error) {
	rule := fmt.Sprintf("tcp dport %d accept %s", port, sftpRuleMarker)
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.Contains(l, sftpRuleMarker) {
			lines[i] = indent(l) + rule
			return strings.Join(lines, "\n"), nil
		}
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "tcp dport 22 accept" {
			lines = slices.Insert(lines, i+1, indent(l)+rule)
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", errors.New("the file has no rule for port 22, so the agent cannot add the SFTP port")
}

func indent(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// retireHostSftp removes the SFTP configuration of release 0.1 from the SSH service of the
// host. The agent removes only the file of the installer. If an administrator changed the
// file (the first line is different), the agent keeps it.
func (d *Deps) retireHostSftp(ctx context.Context) error {
	path := d.hostSftpConfig()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(b), hostSftpHeader) {
		slog.Warn("SFTP: the SSH configuration of release 0.1 was changed; remove it yourself", "file", path)
		return nil
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	// If ssh.service does not operate (for example ssh.socket on Ubuntu), it reads the new
	// configuration when it starts. A reload does not stop the open SSH sessions.
	if _, err := d.Run.Run(ctx, "systemctl", "try-reload-or-restart", "ssh.service"); err != nil {
		return fmt.Errorf("SFTP: removed %s, but the SSH service did not reload: %w", path, err)
	}
	slog.Info("SFTP: moved from the SSH service of the host to the SFTP container", "port", d.sftpPort())
	return nil
}
