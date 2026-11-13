package actions

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// PanelUser is the Unix user of the panel containers.
const PanelUser = "cf-panel"

// panelServices are the services of the panel that need the database, in the order of their start.
var panelServices = []string{"cf-panel-api.service", "cf-panel-queue.service", "cf-panel-scheduler.service", "cf-panel-web.service"}

// InstallPanel pulls the images of the panel, writes its units and starts them (FR-1808).
// It is safe to run it again: it keeps the database and its password, and it starts a service
// again only when its unit changed.
func (d *Deps) InstallPanel(ctx context.Context, p quadlet.Panel) error {
	// The signature policy of Podman refuses an image with no valid signature (FR-1101).
	for _, image := range []string{p.Registry + "/postgresql:" + p.Version, p.Registry + "/panel-api:" + p.Version, p.Registry + "/panel-web:" + p.Version} {
		if _, err := d.Host.Podman(ctx, PanelUser, "pull", "--quiet", image); err != nil {
			return err
		}
	}
	// Only the user of the panel can open the data. The entry point of the image sets the
	// owner in the container, so a directory that exists keeps its owner (makeDataDir).
	if err := d.makeDataDir(ctx, p.PanelDatabaseDir(), PanelUser); err != nil {
		return err
	}
	password, err := d.panelDatabasePassword(ctx, p.PanelDatabaseEnv())
	if err != nil {
		return err
	}
	if err := d.setPanelVersion(ctx, p.DataDir+"/panel.env", p.Version); err != nil {
		return err
	}
	changed := map[string]bool{}
	for name, content := range quadlet.PanelUnits(p) {
		c, err := d.Host.WriteUnit(PanelUser, name, content)
		if err != nil {
			return err
		}
		changed[strings.TrimSuffix(name, ".container")+".service"] = c
	}
	if _, err := d.Host.Systemctl(ctx, PanelUser, "daemon-reload"); err != nil {
		return err
	}
	// A service that operates keeps its old unit until it starts again. Thus start a service
	// again when its unit changed.
	start := func(service string) error {
		verb := "start"
		if changed[service] {
			verb = "restart"
		}
		_, err := d.Host.Systemctl(ctx, PanelUser, verb, service)
		return err
	}
	// The API makes the tables when it starts, so the database comes first.
	if err := start(quadlet.PanelDatabaseContainer + ".service"); err != nil {
		return err
	}
	if err := d.waitPanelDatabase(ctx); err != nil {
		return err
	}
	if err := d.setPanelDatabasePassword(ctx, password); err != nil {
		return err
	}
	for _, service := range panelServices {
		if err := start(service); err != nil {
			return err
		}
	}
	return nil
}

var panelLatestRE = regexp.MustCompile(`(?m)^CF_AGENT_LATEST=.*$`)

// setPanelVersion writes the version of this release in the settings of the panel. The panel
// offers this version of the agent to the other servers (audit finding S7). The installer
// makes the file. Without the file, the function does nothing.
func (d *Deps) setPanelVersion(ctx context.Context, envFile, version string) error {
	b, err := os.ReadFile(envFile)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	line := "CF_AGENT_LATEST=" + strings.TrimPrefix(version, "v")
	content := string(b)
	if panelLatestRE.MatchString(content) {
		content = panelLatestRE.ReplaceAllLiteralString(content, line)
	} else {
		content = strings.TrimRight(content, "\n") + "\n" + line + "\n"
	}
	changed, err := host.WriteFile(envFile, content, 0o600)
	if err != nil || !changed {
		return err
	}
	_, err = d.Run.Run(ctx, "chown", PanelUser+":"+PanelUser, envFile)
	return err
}

// panelAPIContainer is the container of the panel API (quadlet.PanelUnits).
const panelAPIContainer = "cf-panel-api"

// PanelCommand runs a command of the panel ("php artisan") in the API container, as the user
// of the panel. It gives the output of the command. The arguments go to the command as they
// are, with no shell. "--" before the values keeps a value that starts with "-" from being
// read as an option.
func (d *Deps) PanelCommand(ctx context.Context, command string, options []string, values ...string) (string, error) {
	args := append([]string{"exec", panelAPIContainer, "php", "artisan", command}, options...)
	args = append(append(args, "--"), values...)
	return d.Host.Podman(ctx, PanelUser, args...)
}

// ResetPanelPassword gives a user of the panel a new temporary password (audit finding B5).
// It is the way back for an administrator who lost the password or the TOTP device. Only
// root on the panel server can do it.
func (d *Deps) ResetPanelPassword(ctx context.Context, email string, removeTotp bool) (string, error) {
	if err := panelEmail(email); err != nil {
		return "", err
	}
	options := []string{}
	if removeTotp {
		options = append(options, "--remove-totp")
	}
	return d.PanelCommand(ctx, "cloudfactory:reset-password", options, email)
}

// UnlockPanelSignIn removes the sign-in lock of an account (audit finding S3). With an
// address, it also removes the lock for that client address.
func (d *Deps) UnlockPanelSignIn(ctx context.Context, email, address string) (string, error) {
	if err := panelEmail(email); err != nil {
		return "", err
	}
	options := []string{}
	if address != "" {
		if net.ParseIP(address) == nil {
			return "", fmt.Errorf("%q is not an IP address", address)
		}
		options = append(options, "--address="+address)
	}
	return d.PanelCommand(ctx, "cloudfactory:unlock", options, email)
}

func panelEmail(email string) error {
	if len(email) > 254 || !strings.Contains(email, "@") || strings.ContainsFunc(email, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("%q is not an email address", email)
	}
	return nil
}

// PanelDomain reads the domain of the panel from the settings file that the installer wrote.
func PanelDomain(envFile string) (string, error) {
	b, err := os.ReadFile(envFile)
	if err != nil {
		return "", err
	}
	m := panelURLRE.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("%s has no APP_URL with the domain of the panel", envFile)
	}
	return string(m[1]), nil
}

var panelURLRE = regexp.MustCompile(`(?m)^APP_URL=https://([a-z0-9.-]+)$`)

var panelPasswordRE = regexp.MustCompile(`(?m)^DB_PASSWORD=([0-9a-f]{32,})$`)

// panelDatabasePassword makes the password of the panel database once, and gives it. The file
// has the password two times: POSTGRES_PASSWORD for the entry point of the database image, and
// DB_PASSWORD for the API.
func (d *Deps) panelDatabasePassword(ctx context.Context, path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		m := panelPasswordRE.FindSubmatch(b)
		if m == nil {
			return "", fmt.Errorf("%s has no DB_PASSWORD; remove the file to make a new password", path)
		}
		return string(m[1]), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	password := randomHex(48)
	content := "POSTGRES_USER=" + quadlet.PanelDatabaseName + "\nPOSTGRES_DB=" + quadlet.PanelDatabaseName +
		"\nPOSTGRES_PASSWORD=" + password + "\nDB_PASSWORD=" + password + "\n"
	if _, err := host.WriteFile(path, content, 0o600); err != nil {
		return "", err
	}
	if _, err := d.Run.Run(ctx, "chown", PanelUser+":"+PanelUser, path); err != nil {
		return "", err
	}
	return password, nil
}

// panelDatabaseExec gives the arguments of runuser for a command in the database container.
func (d *Deps) panelDatabaseExec(args ...string) ([]string, error) {
	uid, err := d.Host.LookupUID(PanelUser)
	if err != nil {
		return nil, err
	}
	base := []string{"-u", PanelUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", "-i", quadlet.PanelDatabaseContainer}
	return append(base, args...), nil
}

// waitPanelDatabase waits until the database accepts TCP connections. The entry point of the
// image first starts a temporary server that has only the socket, to make the database. The
// check uses TCP, so that it does not see that temporary server.
func (d *Deps) waitPanelDatabase(ctx context.Context) error {
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	args, err := d.panelDatabaseExec("pg_isready", "--quiet", "--host=127.0.0.1", "--username="+quadlet.PanelDatabaseName, "--dbname="+quadlet.PanelDatabaseName)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		_, err := d.Run.Run(ctx, "runuser", args...)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the panel database did not answer in %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// setPanelDatabasePassword makes the password of the database user the same as the password
// in the file. The entry point of the image sets the password only for a new data directory.
// Without this step, a lost file with an old data directory stops the panel. The connection
// uses the socket in the container, and the password goes through stdin, not the process list.
func (d *Deps) setPanelDatabasePassword(ctx context.Context, password string) error {
	args, err := d.panelDatabaseExec("psql", "--username="+quadlet.PanelDatabaseName, "--dbname="+quadlet.PanelDatabaseName, "--set=ON_ERROR_STOP=1", "--quiet")
	if err != nil {
		return err
	}
	// The password has only hex characters (panelPasswordRE), so it needs no escape.
	_, err = d.Run.RunInput(ctx, "ALTER ROLE "+quadlet.PanelDatabaseName+" PASSWORD '"+password+"';\n", "runuser", args...)
	return err
}
