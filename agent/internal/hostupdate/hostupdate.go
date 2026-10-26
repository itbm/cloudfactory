// Package hostupdate reads and installs the updates of the host operating system
// (spec section 7.11.2). It uses apt on Debian 13 and Ubuntu 26.04 LTS.
package hostupdate

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// Held are the packages that only the agent updates (FR-1207). The agent updates them on
// one server first, then on the other servers (FR-1113).
var Held = []string{"podman", "conmon", "netavark", "aardvark-dns", "passt", "crun"}

// Update is one pending package update.
type Update struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Security bool   `json:"security"`
}

// Proto gives the update in the format of the agent API.
func (u Update) Proto() *agentv1.PendingUpdate {
	return &agentv1.PendingUpdate{Name: u.Name, From: u.From, To: u.To, Security: u.Security}
}

// Apt runs apt commands.
type Apt struct {
	Run execx.Runner
	// RebootFile exists when the host needs a reboot.
	RebootFile string
}

// New gives an Apt with the paths of Debian and Ubuntu.
func New(run execx.Runner) Apt { return Apt{Run: run, RebootFile: "/var/run/reboot-required"} }

// "Inst openssl [3.5.1-1] (3.5.1-2 Debian-Security:13/stable-security [amd64])"
var instRE = regexp.MustCompile(`^Inst (\S+) (?:\[(\S+)\] )?\((\S+) ([^\[]*)`)

// Parse reads the output of "apt-get -s upgrade".
func Parse(out string) []Update {
	var updates []Update
	for _, line := range strings.Split(out, "\n") {
		m := instRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		updates = append(updates, Update{Name: m[1], From: m[2], To: m[3], Security: strings.Contains(strings.ToLower(m[4]), "security")})
	}
	return updates
}

// Pending gives the updates that apt can install now.
func (a Apt) Pending(ctx context.Context) ([]Update, error) {
	out, err := a.Run.Run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade", "--with-new-pkgs")
	if err != nil {
		return nil, err
	}
	return Parse(out), nil
}

// RebootRequired reports whether an update needs a reboot.
func (a Apt) RebootRequired() bool {
	_, err := os.Stat(a.RebootFile)
	return err == nil
}

// Hold marks the container tools so that a normal upgrade does not change them.
func (a Apt) Hold(ctx context.Context) error {
	_, err := a.Run.Run(ctx, "apt-mark", append([]string{"hold"}, Held...)...)
	return err
}

// Download gets the package lists and the packages, but does not install them.
func (a Apt) Download(ctx context.Context) error {
	if _, err := a.Run.Run(ctx, "apt-get", "update"); err != nil {
		return err
	}
	_, err := a.Run.Run(ctx, "apt-get", "-y", "-d", "upgrade", "--with-new-pkgs")
	return err
}

// Install installs updates with the policy of the server (FR-1112).
// "security" installs only security updates. "all" installs all updates except the
// held packages. A manual install from the panel uses the security policy.
func (a Apt) Install(ctx context.Context, policy string) error {
	switch policy {
	case "security", "manual":
		// unattended-upgrades installs only from the security origins of its configuration.
		if _, err := a.Run.Run(ctx, "unattended-upgrade", "--verbose"); err != nil {
			return err
		}
	case "all":
		if _, err := a.Run.Run(ctx, "apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "upgrade", "--with-new-pkgs"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("hostupdate: the policy %q is not valid", policy)
	}
	return nil
}

// RestartServices restarts only the services that use changed files (FR-1114).
func (a Apt) RestartServices(ctx context.Context) error {
	_, err := a.Run.Run(ctx, "needrestart", "-r", "a", "-b")
	return err
}
