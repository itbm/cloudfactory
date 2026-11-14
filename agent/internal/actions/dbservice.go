package actions

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// Engines are the database engines of the database containers.
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

// dbUnit gives the input for the unit of a database container. A container of a customer gets
// the address of the customer and its limits.
func (d *Deps) dbUnit(s dbServer) (quadlet.Database, error) {
	dir := d.dbDir(s)
	u := quadlet.Database{
		Engine:   s.Engine,
		Instance: s.Instance,
		Image:    d.serviceImage(s.Engine),
		DataDir:  filepath.Join(dir, s.Engine),
		EnvFile:  filepath.Join(dir, s.Engine+".env"),
		Gvisor:   d.runtime(),
	}
	if s.Engine == "valkey" {
		u.MemoryMB = d.valkeyMemoryMB()
	}
	if s.Instance != "" {
		// The website of the job has the record of the customer already (useDatabases).
		address, err := d.useDatabases(s.Instance, "", false)
		if err != nil {
			return u, err
		}
		u.Address, u.Limits = address, d.databaseLimits(s.Engine)
	}
	return u, nil
}

// EnsureDatabaseService starts a database container, if it does not operate, and waits until
// it accepts connections. The agent starts a container when a website needs the engine for
// the first time. systemd starts it again at boot.
//
// The start of a container can take minutes. Only the containers of the same customer wait
// for it: each customer has its own lock.
func (d *Deps) EnsureDatabaseService(ctx context.Context, s dbServer) error {
	if _, err := dbContainer(s.Engine); err != nil {
		return err
	}
	if s.Engine == "valkey" && s.Instance == "" {
		return errNoSharedValkey
	}
	ready := func() bool {
		d.dbMu.Lock()
		defer d.dbMu.Unlock()
		return d.dbReady[s]
	}
	if ready() {
		return nil
	}
	lock := d.instanceLock(s.Instance)
	lock.Lock()
	defer lock.Unlock()
	if ready() {
		return nil
	}
	if s.Instance != "" {
		// The address of the container must be on "lo" before Podman publishes the port.
		if err := d.syncDatabaseAccess(ctx); err != nil {
			return err
		}
	}
	u, err := d.dbUnit(s)
	if err != nil {
		return err
	}
	if _, err := d.Host.Podman(ctx, DBUser, "image", "exists", u.Image); err != nil {
		// The signature policy of Podman refuses an image with no valid signature (FR-1101).
		if _, err := d.Host.Podman(ctx, DBUser, "pull", "--quiet", u.Image); err != nil {
			return err
		}
	}
	// Only cf-db can open the data. The entry point of the image sets the owner in the container.
	dirs := []string{u.DataDir}
	if s.Instance != "" {
		dirs = []string{filepath.Dir(d.dbDir(s)), d.dbDir(s), u.DataDir}
	}
	for _, dir := range dirs {
		if err := d.makeDataDir(ctx, dir, DBUser); err != nil {
			return err
		}
	}
	if s.Engine == "valkey" {
		// Valkey has a user file, not an environment file.
		if err := d.writeValkeyUsers(ctx, s, u.DataDir); err != nil {
			return err
		}
	} else if err := d.writeSuperuserSecret(ctx, s.Engine, u.EnvFile); err != nil {
		return err
	}
	changed, err := d.Host.WriteUnit(DBUser, s.container()+".container", quadlet.DatabaseUnit(u))
	if err != nil {
		return err
	}
	service := s.container() + ".service"
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
	if err := d.waitDatabase(ctx, s); err != nil {
		return err
	}
	d.dbMu.Lock()
	if d.dbReady == nil {
		d.dbReady = map[dbServer]bool{}
	}
	d.dbReady[s] = true
	d.dbMu.Unlock()
	return nil
}

// databaseServers gives the database containers that this server has: the containers of each
// customer, and the shared containers of an earlier release. A container is there when cf-db
// has its unit.
func (d *Deps) databaseServers() ([]dbServer, error) {
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return nil, err
	}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return nil, err
	}
	keys := append([]string{""}, slices.Sorted(maps.Keys(instances))...)
	var out []dbServer
	for _, key := range keys {
		for _, engine := range Engines {
			s := dbServer{Instance: key, Engine: engine}
			if _, err := os.Stat(filepath.Join(dir, s.container()+".container")); err == nil {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// RefreshDatabaseServices writes the units of the database containers that the server already
// has again, for example with the image of a new agent version. It also loads the database
// access of the websites into the firewall.
func (d *Deps) RefreshDatabaseServices(ctx context.Context) error {
	d.removeOldInstanceData()
	servers, err := d.databaseServers()
	if err != nil {
		return err
	}
	var errs []error
	if instances, err := d.State.DatabaseInstances(); err != nil {
		errs = append(errs, err)
	} else if len(instances) > 0 {
		errs = append(errs, d.syncDatabaseAccess(ctx))
	}
	for _, s := range servers {
		if err := d.EnsureDatabaseService(ctx, s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s, err))
		}
	}
	return errors.Join(errs...)
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
func (d *Deps) waitDatabase(ctx context.Context, s dbServer) error {
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		var err error
		if s.Engine == "valkey" {
			err = d.valkeyReady(ctx, s)
		} else {
			_, err = d.sqlNow(ctx, s, "SELECT 1;\n")
		}
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the container of %s did not answer in %s: %w", s, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
