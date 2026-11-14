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

// dbUnit gives the input for the unit of a database container, and the record of a database
// server. A database server gets the address of its customer, its port and its limits.
func (d *Deps) dbUnit(s dbServer) (quadlet.Database, DatabaseServer, error) {
	dir := d.dbDir(s)
	u := quadlet.Database{
		Engine:  s.Engine,
		ID:      s.ID,
		Image:   d.serviceImage(s.Engine),
		DataDir: filepath.Join(dir, s.Engine),
		EnvFile: filepath.Join(dir, s.Engine+".env"),
		Gvisor:  d.runtime(),
	}
	if s.ID == "" {
		return u, DatabaseServer{}, nil
	}
	customer, record, address, err := d.State.DatabaseServer(s.ID)
	if err != nil {
		return u, record, err
	}
	if customer == "" || record.Engine != s.Engine {
		return u, record, fmt.Errorf("this server does not have %s", s)
	}
	u.Customer, u.Address, u.Port = customer, address, record.Port
	u.DataDir = d.dataDir(s)
	u.Limits = quadlet.Limits{VCPUs: record.VCPUs, MemoryBytes: record.MemoryBytes}
	u.DiskBytes = record.DiskBytes
	if s.Engine == "valkey" {
		u.MemoryMB = valkeyDataMB(record.MemoryBytes)
	}
	return u, record, nil
}

// EnsureDatabaseService starts a database container, if it does not operate, and waits until
// it accepts connections. systemd starts it again at boot. The state has the record of a
// database server before this function starts it (ensureServer).
//
// The start of a container can take minutes. Only the jobs of the same database server wait
// for it: each database server has its own lock.
func (d *Deps) EnsureDatabaseService(ctx context.Context, s dbServer) error {
	if _, err := dbContainer(s.Engine); err != nil {
		return err
	}
	if s.Engine == "valkey" && s.ID == "" {
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
	lock := d.instanceLock(s.ID)
	lock.Lock()
	defer lock.Unlock()
	if ready() {
		return nil
	}
	u, record, err := d.dbUnit(s)
	if err != nil {
		return err
	}
	if s.ID != "" {
		// The address of the container must be on "lo" before Podman publishes the port. The
		// firewall lets the websites of the customer connect to the port.
		if err := d.syncDatabaseAccess(ctx); err != nil {
			return err
		}
	}
	if _, err := d.Host.Podman(ctx, DBUser, "image", "exists", u.Image); err != nil {
		// The signature policy of Podman refuses an image with no valid signature (FR-1101).
		if _, err := d.Host.Podman(ctx, DBUser, "pull", "--quiet", u.Image); err != nil {
			return err
		}
	}
	// Only cf-db can open the data. The entry point of the image sets the owner in the container.
	dirs := []string{u.DataDir}
	if s.ID != "" {
		dirs = []string{filepath.Dir(d.dbDir(s)), d.dbDir(s)}
	}
	for _, dir := range dirs {
		if err := d.makeDataDir(ctx, dir, DBUser); err != nil {
			return err
		}
	}
	if s.ID != "" {
		// The disk of the database server has the data directory.
		if err := d.ensureDisk(ctx, s, record.DiskBytes); err != nil {
			return err
		}
		if err := d.makeDataDir(ctx, u.DataDir, DBUser); err != nil {
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
	if s.Engine == "postgresql" {
		// The users of the websites cannot connect to the databases of the server itself.
		if _, err := d.sqlNow(ctx, s, pgHarden); err != nil {
			return err
		}
	}
	d.dbMu.Lock()
	if d.dbReady == nil {
		d.dbReady = map[dbServer]bool{}
	}
	d.dbReady[s] = true
	d.dbMu.Unlock()
	return nil
}

// databaseServers gives the database containers that this server has: the database servers
// of each customer, and the shared containers of an earlier release. A shared container is
// there when cf-db has its unit.
func (d *Deps) databaseServers() ([]dbServer, error) {
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return nil, err
	}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return nil, err
	}
	var out []dbServer
	for _, engine := range Engines {
		s := dbServer{Engine: engine}
		if _, err := os.Stat(filepath.Join(dir, s.container()+".container")); err == nil {
			out = append(out, s)
		}
	}
	for _, customer := range slices.Sorted(maps.Keys(instances)) {
		servers := instances[customer].Servers
		for _, id := range slices.Sorted(maps.Keys(servers)) {
			out = append(out, dbServer{ID: id, Engine: servers[id].Engine})
		}
	}
	return out, nil
}

// RefreshDatabaseServices writes the units of the database containers that the server already
// has again, for example with the image of a new agent version. It also mounts the disk of
// each database server, and loads the database access of the websites into the firewall.
func (d *Deps) RefreshDatabaseServices(ctx context.Context) error {
	d.removeOldServerData()
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

// databaseListens checks that the SQL server of a container accepts TCP connections.
func (d *Deps) databaseListens(ctx context.Context, s dbServer) error {
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return err
	}
	args := []string{"-u", DBUser, "--", "env", fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid), "podman", "exec", "-i", s.container()}
	if s.Engine == "mariadb" {
		// "ping" is successful when the server answers, also when the server refuses the user.
		// The server refuses the user here, and writes one warning for that in its log.
		args = append(args, "mariadb-admin", "--protocol=tcp", "--host=127.0.0.1", "--port=3306", "--connect-timeout=5", "ping")
	} else {
		args = append(args, "pg_isready", "--quiet", "--host=127.0.0.1", "--port=5432", "--timeout=5")
	}
	_, err = d.Run.Run(ctx, "runuser", args...)
	return err
}

// waitDatabase waits until the database server answers in its container. A new data
// directory takes some time.
//
// The entry point of the MariaDB image and of the PostgreSQL image first starts a temporary
// server that has only the socket, to prepare a new data directory. Then it stops that server
// and starts the real server. The SQL of the agent uses the socket, so an answer to SQL does
// not show that the real server operates. Thus the check first makes sure that the server
// accepts TCP connections: the temporary server does not. Without this check, the next step
// can send its SQL while the temporary server stops.
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
		} else if err = d.databaseListens(ctx, s); err == nil {
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
