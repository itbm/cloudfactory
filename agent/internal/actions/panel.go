package actions

import (
	"context"
	"fmt"
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
	// owner in the container.
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", PanelUser, "-g", PanelUser, "-m", "0700", p.PanelDatabaseDir()); err != nil {
		return err
	}
	password, err := d.panelDatabasePassword(ctx, p.PanelDatabaseEnv())
	if err != nil {
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
