package actions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// Engines are the database engines of the shared database containers.
var Engines = []string{"mariadb", "postgresql"}

// dbHome is the home directory of the user cf-db. The data of the database containers is there.
func (d *Deps) dbHome() string {
	if d.DBHome != "" {
		return d.DBHome
	}
	return "/var/lib/" + DBUser
}

// serviceImage gives the signed image of a service of this agent version, for example
// "ghcr.io/cloudfactory/mariadb:0.1.0".
func (d *Deps) serviceImage(name string) string {
	if d.ServiceImage != nil {
		return d.ServiceImage(name)
	}
	return "ghcr.io/cloudfactory/" + name + ":latest"
}

func (d *Deps) dbUnit(engine string) quadlet.Database {
	home := d.dbHome()
	return quadlet.Database{
		Engine:  engine,
		Image:   d.serviceImage(engine),
		DataDir: filepath.Join(home, engine),
		EnvFile: filepath.Join(home, engine+".env"),
		Gvisor:  d.runtime(),
	}
}

// EnsureDatabaseService starts the shared container of an engine, if it does not operate, and
// waits until it accepts connections. The agent starts a container when a website needs the
// engine for the first time. systemd starts it again at boot.
func (d *Deps) EnsureDatabaseService(ctx context.Context, engine string) error {
	if _, err := dbContainer(engine); err != nil {
		return err
	}
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	if d.dbReady == nil {
		d.dbReady = map[string]bool{}
	}
	if d.dbReady[engine] {
		return nil
	}
	u := d.dbUnit(engine)
	if _, err := d.Host.Podman(ctx, DBUser, "image", "exists", u.Image); err != nil {
		// The signature policy of Podman refuses an image with no valid signature (FR-1101).
		if _, err := d.Host.Podman(ctx, DBUser, "pull", "--quiet", u.Image); err != nil {
			return err
		}
	}
	// Only cf-db can open the data. The entry point of the image sets the owner in the container.
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", DBUser, "-g", DBUser, "-m", "0700", u.DataDir); err != nil {
		return err
	}
	if engine == "postgresql" {
		if err := d.writeSuperuserSecret(ctx, u.EnvFile); err != nil {
			return err
		}
	}
	changed, err := d.Host.WriteUnit(DBUser, quadlet.DatabaseContainer(engine)+".container", quadlet.DatabaseUnit(u))
	if err != nil {
		return err
	}
	service := quadlet.DatabaseContainer(engine) + ".service"
	if changed {
		if _, err := d.Host.Systemctl(ctx, DBUser, "daemon-reload"); err != nil {
			return err
		}
		// A new image or new settings take effect with a restart.
		if _, err := d.Host.Systemctl(ctx, DBUser, "restart", service); err != nil {
			return err
		}
	} else if _, err := d.Host.Systemctl(ctx, DBUser, "start", service); err != nil {
		return err
	}
	if err := d.waitDatabase(ctx, engine); err != nil {
		return err
	}
	d.dbReady[engine] = true
	return nil
}

// RefreshDatabaseServices writes the units of the database containers that the server already
// has again, for example with the image of a new agent version.
func (d *Deps) RefreshDatabaseServices(ctx context.Context) error {
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return err
	}
	for _, engine := range Engines {
		if _, err := os.Stat(filepath.Join(dir, quadlet.DatabaseContainer(engine)+".container")); err != nil {
			continue
		}
		if err := d.EnsureDatabaseService(ctx, engine); err != nil {
			return fmt.Errorf("%s: %w", engine, err)
		}
	}
	return nil
}

// writeSuperuserSecret makes the password of the PostgreSQL superuser once. The entry point
// of the image needs it. The agent does not use it: it connects through the socket.
func (d *Deps) writeSuperuserSecret(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if _, err := host.WriteFile(path, "POSTGRES_PASSWORD="+randomHex(48)+"\n", 0o600); err != nil {
		return err
	}
	_, err := d.Run.Run(ctx, "chown", DBUser+":"+DBUser, path)
	return err
}

// waitDatabase waits until the database server answers in its container. A new data
// directory takes some time.
func (d *Deps) waitDatabase(ctx context.Context, engine string) error {
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		_, err := d.sqlNow(ctx, engine, "SELECT 1;\n")
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the %s container did not answer in %s: %w", engine, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
