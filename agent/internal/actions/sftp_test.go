package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// firewall01 is the input chain of the firewall file of release 0.1.
const firewall01 = `table inet cloudfactory {
  chain input {
    type filter hook input priority filter; policy drop;
    ct state established,related accept
    iif lo accept
    tcp dport 22 accept
    tcp dport { 80, 443, 8080, 8443 } accept
  }
}
`

const hostSftp01 = "# Managed by the Cloudfactory installer.\nMatch Group cf-sites\n\tChrootDirectory /srv/cloudfactory/sites/%u\n"

func sftpDeps(t *testing.T) (*Deps, *execx.Fake) {
	d, f, _ := deps(t)
	dir := t.TempDir()
	both(d, func(d *Deps) {
		d.SftpKeysDir = filepath.Join(dir, "sftp")
		d.SftpUsersDir = filepath.Join(dir, "sftp-users")
		d.SystemUnitDir = filepath.Join(dir, "systemd")
		d.FirewallFile = filepath.Join(dir, "firewall.nft")
		d.HostSftpConfig = filepath.Join(dir, "50-cloudfactory-sftp.conf")
		d.SftpImage = "ghcr.io/cloudfactory/sftp:0.2.0"
		d.Host.LookupGID = func(string) (int, error) { return 20001, nil }
	})
	d.SftpReady = func(context.Context, int) error { return nil }
	d.ReadyTimeout = time.Millisecond
	return d, f
}

// A server from release 0.1: the agent starts the container, opens port 2200 after port 22,
// and then removes the SFTP configuration from the SSH service of the host.
func TestEnsureSftpMovesSftpFromTheHost(t *testing.T) {
	d, f := sftpDeps(t)
	os.WriteFile(d.FirewallFile, []byte(firewall01), 0o644)
	os.WriteFile(d.HostSftpConfig, []byte(hostSftp01), 0o644)
	os.MkdirAll(d.SftpKeysDir, 0o755)
	os.WriteFile(filepath.Join(d.SftpKeysDir, "acme_x7k2.keys"), []byte("# Managed\nrestrict ssh-ed25519 AAAA owner\n"), 0o644)
	os.WriteFile(filepath.Join(d.SftpKeysDir, "other_ab12.keys"), []byte("# Managed\n"), 0o644)
	if got := d.SftpPortInUse(); got != 22 {
		t.Errorf("before the move, the port is %d, not 22", got)
	}
	f.Rules = []execx.Rule{{Prefix: "podman image exists", Err: errors.New("no image")}}
	if err := d.EnsureSftp(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"podman pull --quiet ghcr.io/cloudfactory/sftp:0.2.0",
		"systemctl daemon-reload",
		"systemctl restart cf-sftp.service",
		"nft -f " + d.FirewallFile,
		"systemctl try-reload-or-restart ssh.service",
	} {
		if !f.Called(want) {
			t.Errorf("no call %q: %v", want, f.Calls)
		}
	}
	unit, _ := os.ReadFile(filepath.Join(d.SystemUnitDir, quadlet.SftpUnitName))
	if !strings.Contains(string(unit), "PublishPort=2200:2200\n") {
		t.Errorf("unit:\n%s", unit)
	}
	fw, _ := os.ReadFile(d.FirewallFile)
	if !strings.Contains(string(fw), "    tcp dport 22 accept\n    tcp dport 2200 accept # cloudfactory-sftp\n") {
		t.Errorf("firewall:\n%s", fw)
	}
	if _, err := os.Stat(d.HostSftpConfig); !os.IsNotExist(err) {
		t.Error("the SFTP configuration of the host is still there")
	}
	passwd, _ := os.ReadFile(filepath.Join(d.SftpUsersDir, "passwd"))
	if !strings.Contains(string(passwd), "acme_x7k2:x:20001:20001:") || strings.Contains(string(passwd), "other_ab12") {
		t.Errorf("only the users with a key can sign in:\n%s", passwd)
	}
	if got := d.SftpPortInUse(); got != 2200 {
		t.Errorf("after the move, the port is %d, not 2200", got)
	}

	// The second start changes nothing.
	f.Calls = nil
	if err := d.EnsureSftp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.Called("systemctl restart") || f.Called("nft") || f.Called("systemctl try-reload-or-restart") || !f.Called("systemctl start cf-sftp.service") {
		t.Errorf("calls: %v", f.Calls)
	}

	// A new port changes the unit and the firewall rule.
	f.Calls = nil
	both(d, func(d *Deps) { d.SftpPort = 2222 })
	if err := d.EnsureSftp(context.Background()); err != nil {
		t.Fatal(err)
	}
	fw, _ = os.ReadFile(d.FirewallFile)
	if !strings.Contains(string(fw), "    tcp dport 2222 accept # cloudfactory-sftp\n") || strings.Contains(string(fw), "2200") {
		t.Errorf("firewall:\n%s", fw)
	}
	if !f.Called("systemctl restart cf-sftp.service") || !f.Called("nft -f") {
		t.Errorf("calls: %v", f.Calls)
	}
}

// If the container does not answer, SFTP stays on the SSH service of the host.
func TestEnsureSftpKeepsTheHostWhenTheContainerFails(t *testing.T) {
	d, f := sftpDeps(t)
	os.WriteFile(d.FirewallFile, []byte(firewall01), 0o644)
	os.WriteFile(d.HostSftpConfig, []byte(hostSftp01), 0o644)
	d.SftpReady = func(context.Context, int) error { return errors.New("connection refused") }
	if err := d.EnsureSftp(context.Background()); err == nil || !strings.Contains(err.Error(), "SFTP stays on the SSH service of the host") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(d.HostSftpConfig); err != nil || f.Called("systemctl try-reload-or-restart") {
		t.Errorf("the agent removed the SFTP configuration of the host: %v", f.Calls)
	}
	if d.SftpPortInUse() != 22 {
		t.Error("the panel must show port 22 while the host serves SFTP")
	}
}

// The agent keeps a file that an administrator changed, and does not touch a firewall that it
// does not know.
func TestEnsureSftpKeepsFilesOfTheAdministrator(t *testing.T) {
	d, f := sftpDeps(t)
	os.WriteFile(d.HostSftpConfig, []byte("# My own SFTP settings\n"), 0o644)
	if err := d.EnsureSftp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.HostSftpConfig); err != nil || f.Called("nft") || f.Called("systemctl try-reload-or-restart") {
		t.Errorf("calls: %v", f.Calls)
	}
	os.WriteFile(d.FirewallFile, []byte("table inet filter {\n}\n"), 0o644)
	if err := d.EnsureSftp(context.Background()); err == nil {
		t.Error("a firewall file with no SSH rule must give an error")
	}
}

// The agent never gives a system user, for example root, to the SFTP container.
func TestSyncSftpUsersSkipsSystemUsers(t *testing.T) {
	d, _ := sftpDeps(t)
	os.MkdirAll(d.SftpKeysDir, 0o755)
	for _, u := range []string{"acme_x7k2", "other_ab12"} {
		os.WriteFile(filepath.Join(d.SftpKeysDir, u+".keys"), []byte("restrict ssh-ed25519 AAAA\n"), 0o644)
	}
	os.WriteFile(filepath.Join(d.SftpKeysDir, "root.keys"), []byte("restrict ssh-ed25519 AAAA\n"), 0o644)
	both(d, func(d *Deps) {
		d.Host.LookupUID = func(name string) (int, error) {
			if name == "other_ab12" {
				return 0, nil
			}
			return 20001, nil
		}
	})
	if err := d.SyncSftpUsers(); err != nil {
		t.Fatal(err)
	}
	passwd, _ := os.ReadFile(filepath.Join(d.SftpUsersDir, "passwd"))
	if !strings.Contains(string(passwd), "acme_x7k2:") || strings.Contains(string(passwd), "other_ab12") || strings.Contains(string(passwd), "root") {
		t.Errorf("passwd:\n%s", passwd)
	}
}

func TestSetSftpRule(t *testing.T) {
	got, err := setSftpRule(firewall01, 2200)
	if err != nil || !strings.Contains(got, "\n    tcp dport 2200 accept # cloudfactory-sftp\n    tcp dport { 80") {
		t.Fatalf("%v\n%s", err, got)
	}
	again, _ := setSftpRule(got, 2200)
	if again != got {
		t.Errorf("the second change is different:\n%s", again)
	}
	if _, err := setSftpRule("table inet x {}\n", 2200); err == nil {
		t.Error("no error for a file with no SSH rule")
	}
}
