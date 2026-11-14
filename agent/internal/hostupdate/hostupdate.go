// Package hostupdate reads and installs the updates of the host operating system
// (spec section 7.11.2). It uses apt on Debian 13 and Ubuntu 26.04 LTS.
package hostupdate

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

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
	// ContainerTool is true for a held package (FR-1113). The policy "tools" installs it.
	ContainerTool bool `json:"containerTool,omitempty"`
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
	// ListsFile changes at each "apt-get update". Pending reads the package lists again when
	// the file is older than ListsMaxAge (audit batch 2, finding 9).
	ListsFile   string
	ListsMaxAge time.Duration
}

// New gives an Apt with the paths of Debian and Ubuntu.
func New(run execx.Runner) Apt {
	return Apt{Run: run, RebootFile: "/var/run/reboot-required", ListsFile: "/var/cache/apt/pkgcache.bin", ListsMaxAge: 6 * time.Hour}
}

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

// Commands gives each command that this package runs, as a program and its arguments.
//
// Second audit, the privilege boundary: the worker of the agent is not root, and the root
// helper runs these commands for it. The helper accepts only the commands of this list. Add
// a command here when a method runs a new command.
func Commands() [][]string {
	held := func(args ...string) []string { return append(args, Held...) }
	return [][]string{
		{"apt-get", "-qq", "update"},
		{"apt-get", "update"},
		{"apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade", "--with-new-pkgs"},
		held("apt-get", "-s", "-o", "Debug::NoLocking=1", "install", "--only-upgrade", "--allow-change-held-packages"),
		{"apt-get", "-y", "-d", "upgrade", "--with-new-pkgs"},
		{"apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "upgrade", "--with-new-pkgs"},
		held("apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "install", "--only-upgrade", "--allow-change-held-packages"),
		held("apt-mark", "hold"),
		{"unattended-upgrade", "--verbose"},
		{"needrestart", "-r", "a", "-l", "-u", "NeedRestart::UI::stdio"},
	}
}

// Pending gives the updates that apt can install now, and the updates of the held container
// tools. It gets new package lists first when the lists are old.
func (a Apt) Pending(ctx context.Context) ([]Update, error) {
	if a.ListsFile != "" {
		if fi, err := os.Stat(a.ListsFile); err != nil || time.Since(fi.ModTime()) > a.ListsMaxAge {
			if _, err := a.Run.Run(ctx, "apt-get", "-qq", "update"); err != nil {
				return nil, err
			}
		}
	}
	out, err := a.Run.Run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade", "--with-new-pkgs")
	if err != nil {
		return nil, err
	}
	updates := Parse(out)
	// A held package is not in the upgrade. A simulated install of the held packages shows
	// their new versions.
	out, err = a.Run.Run(ctx, "apt-get", append([]string{"-s", "-o", "Debug::NoLocking=1", "install", "--only-upgrade", "--allow-change-held-packages"}, Held...)...)
	if err != nil {
		return updates, nil
	}
	for _, u := range Parse(out) {
		u.ContainerTool = true
		updates = append(updates, u)
	}
	return updates, nil
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
// held packages. "tools" installs only the held container tools. A manual install from the
// panel uses the security policy.
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
	case "tools":
		// FR-1113: the panel updates the container tools on one server first, then on the
		// other servers. The packages stay held after the update.
		args := append([]string{"-y", "-o", "Dpkg::Options::=--force-confold", "install", "--only-upgrade", "--allow-change-held-packages"}, Held...)
		if _, err := a.Run.Run(ctx, "apt-get", args...); err != nil {
			return err
		}
		if err := a.Hold(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("hostupdate: the policy %q is not valid", policy)
	}
	return nil
}

// RestartServices restarts only the services that use changed files (FR-1114).
//
// Audit finding R9: the command has no batch option (-b), because needrestart in batch mode
// only prints the services. The option -l skips the kernel and microcode checks, which the
// agent reports apart with the reboot file. needrestart keeps its own list of services that
// it does not restart, for example D-Bus and the user managers of the websites. The installer
// adds the agent to that list.
func (a Apt) RestartServices(ctx context.Context) error {
	_, err := a.Run.Run(ctx, "needrestart", "-r", "a", "-l", "-u", "NeedRestart::UI::stdio")
	return err
}
