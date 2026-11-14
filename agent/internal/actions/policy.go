package actions

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Second audit, the privilege boundary: the command policy of the root helper.
//
// The worker of the agent is not root. It asks the root helper to run each command that
// needs root, and the helper runs a command only when Allow accepts it. The policy has
// three types of command:
//
//   - A command as the Unix user of a website ("runuser -u <user> -- ..."). The worker can
//     run each program as that user: the user has only the rights of its website.
//   - A command of a fixed form as the user of a service, for example a database client in a
//     database container. The worker cannot run a program of its choice as cf-db or cf-edge.
//   - A command as root. Each one has a fixed form, and each path in it is a path that the
//     helper makes from its own settings.
//
// All other root work is an operation with a name (RootOps), not a command. When an action
// needs a new command, add its form here. Do not add a form that lets the worker select a
// program, an option or a path of root.

// Allow is the command policy of the root helper (gate.Server.Allow). d is the Deps of the
// helper.
func (d *Deps) Allow(c gate.Command) error {
	argv := c.Argv
	if len(argv) == 0 {
		return errors.New("no command")
	}
	if len(c.Env) > 0 && argv[0] != "restic" {
		return errors.New("only restic can have environment variables")
	}
	switch argv[0] {
	case "runuser":
		return d.allowRunuser(argv[1:])
	case "restic":
		return d.allowRestic(argv[1:], c.Env)
	case "systemctl":
		if len(argv) >= 3 && argv[1] == "--user" {
			return d.allowUserSystemctl(argv[2:])
		}
	}
	if slices.ContainsFunc(rootCommands(), func(allowed []string) bool { return slices.Equal(allowed, argv) }) {
		return nil
	}
	return errors.New("the policy of the root helper does not have this command")
}

// statusServices are the system services whose state the status report reads.
var statusServices = []string{"cf-agent", "ssh", "cf-sftp"}

// rootCommands are the commands that root runs for the worker with no change: each one is a
// full list of arguments.
func rootCommands() [][]string {
	cmds := [][]string{
		// The status report.
		{"podman", "version", "--format", "{{.Client.Version}}"},
		{"nft", "list", "table", "inet", "cloudfactory"},
		// The reboot job of the panel.
		{"systemctl", "reboot"},
	}
	for _, service := range statusServices {
		cmds = append(cmds, []string{"systemctl", "is-active", service})
	}
	// The updates of the operating system.
	return append(cmds, hostupdate.Commands()...)
}

// allowRunuser checks "runuser -u <user> -- <command>".
func (d *Deps) allowRunuser(args []string) error {
	if len(args) < 4 || args[0] != "-u" || args[2] != "--" {
		return errors.New(`runuser must be "runuser -u <user> -- <command>"`)
	}
	user, command := args[1], args[3:]
	switch user {
	case DBUser:
		return d.allowDatabasePodman(command)
	case host.EdgeUser:
		podman, err := d.podmanOf(user, command)
		if err != nil {
			return err
		}
		return allowImage(podman, []string{d.EdgeImage})
	}
	// The user of a website has only the rights of its website, so the worker can run each
	// program as that user.
	if err := d.existingWebsiteUser(user); err != nil {
		return fmt.Errorf("runuser: %w", err)
	}
	return nil
}

// podmanOf checks that command is a Podman command of a user, and gives the arguments of
// Podman: "env XDG_RUNTIME_DIR=/run/user/<UID> podman <arguments>" (host.Host.Podman).
func (d *Deps) podmanOf(user string, command []string) ([]string, error) {
	uid, err := d.Host.LookupUID(user)
	if err != nil {
		return nil, err
	}
	if len(command) < 4 || command[0] != "env" || command[1] != "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(uid) || command[2] != "podman" {
		return nil, fmt.Errorf("the worker can run only Podman as %s", user)
	}
	return command[3:], nil
}

// allowImage checks "image exists <image>" and "pull --quiet <image>" for the images of a
// service of this agent version.
func allowImage(podman, images []string) error {
	if len(podman) == 3 && (slices.Equal(podman[:2], []string{"image", "exists"}) || slices.Equal(podman[:2], []string{"pull", "--quiet"})) {
		if podman[2] != "" && slices.Contains(images, podman[2]) {
			return nil
		}
		return fmt.Errorf("%q is not an image of a service of this agent version", podman[2])
	}
	return errors.New("the policy of the root helper does not have this Podman command")
}

// databaseClients are the programs that the worker can run in a database container.
var databaseClients = []string{"mariadb", "mariadb-admin", "mariadb-dump", "psql", "pg_isready", "pg_dump", "pg_restore"}

// allowDatabasePodman checks a Podman command as cf-db: the image of a database container,
// or a database client in a database container. The client runs in the container, so its
// arguments cannot name a file of the host.
//
//	podman exec [-i] [--env-file <secret file>] <database container> <client> <arguments>
func (d *Deps) allowDatabasePodman(command []string) error {
	podman, err := d.podmanOf(DBUser, command)
	if err != nil {
		return err
	}
	if podman[0] != "exec" {
		images := make([]string, 0, len(Engines))
		for _, engine := range Engines {
			images = append(images, d.serviceImage(engine))
		}
		return allowImage(podman, images)
	}
	rest := podman[1:]
	if len(rest) > 0 && rest[0] == "-i" {
		rest = rest[1:]
	}
	if len(rest) > 1 && rest[0] == "--env-file" {
		if !d.isSecretFile(rest[1]) {
			return fmt.Errorf("%q is not a secret file of the agent", rest[1])
		}
		rest = rest[2:]
	}
	if len(rest) < 2 {
		return errors.New("podman exec needs a container and a program")
	}
	if !isDatabaseContainer(rest[0]) {
		return fmt.Errorf("%q is not a database container", rest[0])
	}
	if !slices.Contains(databaseClients, rest[1]) {
		return fmt.Errorf("%q is not a database client", rest[1])
	}
	return nil
}

var (
	// serviceUnitRE is the name of a service of a user manager.
	serviceUnitRE = regexp.MustCompile(`^[a-z0-9][A-Za-z0-9@_.-]{0,200}\.service$`)
	// limitPropertyRE is one limit of a website (quadlet.LimitProperties).
	limitPropertyRE = regexp.MustCompile(`^(CPUQuota=[0-9]{1,9}%|(MemoryMax|MemorySwapMax|TasksMax)=[0-9]{1,19}|IO(Read|Write)BandwidthMax=/dev/[A-Za-z0-9/_.-]{1,100} [0-9]{1,19})$`)
)

// allowUserSystemctl checks a systemctl command for the user manager of a website user or of
// a service user. args are the arguments after "systemctl --user".
//
// An option such as "--system" or "--host" would send the command to a different manager.
// Thus each form has a fixed list of options.
func (d *Deps) allowUserSystemctl(args []string) error {
	user, ok := strings.CutPrefix(args[0], "--machine=")
	user, at := strings.CutSuffix(user, "@")
	if !ok || !at {
		return errors.New(`systemctl must be "systemctl --user --machine=<user>@ ..."`)
	}
	if err := d.managedUser(user); err != nil {
		return fmt.Errorf("systemctl: %w", err)
	}
	args = args[1:]
	unit := func(s string) bool { return serviceUnitRE.MatchString(s) }
	switch {
	case slices.Equal(args, []string{"daemon-reload"}):
		return nil
	case len(args) == 2 && slices.Contains([]string{"start", "stop", "restart", "is-active"}, args[0]) && unit(args[1]):
		return nil
	case len(args) == 4 && slices.Equal(args[:3], []string{"show", "--property=ControlGroup", "--value"}) && unit(args[3]):
		return nil
	case len(args) >= 4 && slices.Equal(args[:2], []string{"set-property", "--runtime"}) && unit(args[2]):
		// The limits of the service of a website.
		if d.websiteUser(user) != nil {
			break
		}
		for _, property := range args[3:] {
			if !limitPropertyRE.MatchString(property) {
				return fmt.Errorf("%q is not a limit of a website", property)
			}
		}
		return nil
	}
	return errors.New("the policy of the root helper does not have this systemctl command")
}

var (
	resticTagRE  = regexp.MustCompile(`^website=[0-9A-Za-z]{26}$`)
	resticKindRE = regexp.MustCompile(`^website=[0-9A-Za-z]{26},kind=(manual|scheduled)$`)
	countRE      = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// allowRestic checks a restic command (Deps.restic). restic runs as root: a backup reads the
// files of a website and its database dumps, and a restore writes into the directory of a
// restore job. The policy lets restic read and write no other path.
//
//	restic --repo <target> [--password-file <key file>] --json --retry-lock 30m <command>
func (d *Deps) allowRestic(args, env []string) error {
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if err := checkCredentials(map[string]string{name: value}); err != nil {
			return err
		}
	}
	if len(args) < 2 || args[0] != "--repo" || !targetRE.MatchString(args[1]) {
		return errors.New("restic needs a valid backup target")
	}
	args = args[2:]
	if len(args) >= 2 && args[0] == "--password-file" {
		if args[1] != d.ResticPasswordFile {
			return errors.New("restic can read only the key file of the agent")
		}
		args = args[2:]
	}
	if len(args) < 4 || !slices.Equal(args[:3], []string{"--json", "--retry-lock", "30m"}) {
		return errors.New("the policy of the root helper does not have this restic command")
	}
	command, args := args[3], args[4:]
	snapshots := func(ids []string) bool {
		return len(ids) > 0 && !slices.ContainsFunc(ids, func(id string) bool { return !snapshotID.MatchString(id) })
	}
	switch command {
	case "init", "prune":
		if len(args) == 0 {
			return nil
		}
	case "cat":
		if slices.Equal(args, []string{"config"}) {
			return nil
		}
	case "snapshots":
		if len(args) == 1 && snapshotID.MatchString(args[0]) {
			return nil
		}
		if len(args) == 2 && args[0] == "--tag" && resticTagRE.MatchString(args[1]) {
			return nil
		}
	case "forget":
		if snapshots(args) {
			return nil
		}
		if len(args) >= 4 && args[0] == "--tag" && resticKindRE.MatchString(args[1]) && args[2] == "--group-by" && args[3] == "" {
			return allowRetention(args[4:])
		}
	case "backup":
		// The directory of a website and its dump directory.
		if len(args) != 6 || args[2] != "--tag" || !resticTagRE.MatchString(args[3]) || args[4] != "--tag" || (args[5] != "kind=manual" && args[5] != "kind=scheduled") {
			break
		}
		user := filepath.Base(filepath.Dir(args[0]))
		if err := d.websiteUser(user); err != nil {
			return fmt.Errorf("restic backup: %w", err)
		}
		if args[0] != d.Host.AppDir(user) || args[1] != d.dumpDir(user) {
			return errors.New("restic can make a backup only of the files and the database dumps of a website")
		}
		return nil
	case "restore":
		if len(args) != 5 || !snapshotID.MatchString(args[0]) || args[1] != "--target" || args[3] != "--include" {
			break
		}
		// The target is the directory of a restore job, which only root can read.
		job := filepath.Base(args[2])
		if validate.ID(job) != nil || args[2] != d.stageDir(job) {
			return errors.New("restic can restore only into the directory of a restore job")
		}
		include := args[4]
		if filepath.Clean(include) != include || !(strings.HasPrefix(include, d.Host.SitesDir+"/") || strings.HasPrefix(include, filepath.Join(d.DataDir, "dumps")+"/")) {
			return errors.New("restic can restore only the files and the database dumps of a website")
		}
		return nil
	}
	return errors.New("the policy of the root helper does not have this restic command")
}

// allowRetention checks the retention options of "restic forget" (Deps.forget).
func allowRetention(args []string) error {
	keep := []string{"--keep-last", "--keep-daily", "--keep-weekly", "--keep-monthly"}
	if len(args) == 0 || len(args)%2 != 0 {
		return errors.New("restic forget needs a retention policy")
	}
	for i := 0; i < len(args); i += 2 {
		if !slices.Contains(keep, args[i]) || !countRE.MatchString(args[i+1]) {
			return fmt.Errorf("%q is not a retention option", args[i])
		}
	}
	return nil
}
