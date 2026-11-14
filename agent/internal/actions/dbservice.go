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
var Engines = []string{"mariadb", "postgresql", "valkey"}

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

// makeDataDir makes the data directory of a database container, with the owner user and mode
// 0700. It does not change a directory that exists.
//
// The entry point of the database image gives the directory to the database user of the
// container (for example postgres, UID 999), which is a different UID on the host. The database
// server then opens each file through this directory. "install -d" on a directory that exists
// sets the owner and the mode again. A database server that operates then cannot open its files
// ("could not open file global/pg_filenode.map: Permission denied"), until the container starts
// again. This occurred when the install script ran again while the panel database operated.
func (d *Deps) makeDataDir(ctx context.Context, dir, user string) error {
	if _, err := os.Lstat(dir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	_, err := d.Run.Run(ctx, "install", "-d", "-o", user, "-g", user, "-m", "0700", dir)
	return err
}

func (d *Deps) dbUnit(engine string) quadlet.Database {
	home := d.dbHome()
	u := quadlet.Database{
		Engine:  engine,
		Image:   d.serviceImage(engine),
		DataDir: filepath.Join(home, engine),
		EnvFile: filepath.Join(home, engine+".env"),
		Gvisor:  d.runtime(),
	}
	if engine == "valkey" {
		u.MemoryMB = d.valkeyMemoryMB()
	}
	return u
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
	if err := d.makeDataDir(ctx, u.DataDir, DBUser); err != nil {
		return err
	}
	if engine == "valkey" {
		// Valkey has a user file, not an environment file. The websites of a server from an
		// earlier release also need the port of Valkey in the firewall.
		if err := d.writeValkeyUsers(ctx, u.DataDir); err != nil {
			return err
		}
		if err := d.openValkeyPort(ctx); err != nil {
			return err
		}
	} else if err := d.writeSuperuserSecret(ctx, engine, u.EnvFile); err != nil {
		return err
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

// writeSuperuserSecret makes the password of the superuser once. The entry point of the image
// sets it at the first start. The agent connects to PostgreSQL through the socket with no
// password. MariaDB needs the password, so the client gets it in MYSQL_PWD from the
// environment of the container.
func (d *Deps) writeSuperuserSecret(ctx context.Context, engine, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	pass := randomHex(48)
	content := "POSTGRES_PASSWORD=" + pass + "\n"
	if engine == "mariadb" {
		content = "MARIADB_ROOT_PASSWORD=" + pass + "\nMYSQL_PWD=" + pass + "\n"
	}
	if _, err := host.WriteFile(path, content, 0o600); err != nil {
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
		var err error
		if engine == "valkey" {
			err = d.valkeyReady(ctx)
		} else {
			_, err = d.sqlNow(ctx, engine, "SELECT 1;\n")
		}
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
