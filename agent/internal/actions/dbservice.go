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

// dbHome holds the data of the database servers. Root owns it.
//
//	servers/<id>   the files of one database server (dbDir). Root owns the directory, and
//	               only root and the Unix user of the database server can open it.
//	homes/<user>   the home directory of the Unix user of a database server, with its Podman
//	               storage (dbUserHome)
func (d *Deps) dbHome() string {
	if d.DBHome != "" {
		return d.DBHome
	}
	return "/var/lib/cf-db"
}

// DatabaseDir gives the directory of the data of the database servers. The agent reads the
// user quotas of its file system at its start.
func (d *Deps) DatabaseDir() string { return d.dbHome() }

// dbUserHome is the home directory of the Unix user of a database server.
func (d *Deps) dbUserHome(s dbServer) string {
	return filepath.Join(d.dbHome(), "homes", dbUser(s))
}

// serviceImages gives the images of the database engines of this agent version.
func (d *Deps) serviceImages() []string {
	out := make([]string, 0, len(Engines))
	for _, engine := range Engines {
		out = append(out, d.serviceImage(engine))
	}
	return out
}

// serviceImage gives the signed image of a service of this agent version, for example
// "ghcr.io/cloudfactory/mariadb:0.1.0".
func (d *Deps) serviceImage(name string) string {
	if d.ServiceImage != nil {
		return d.ServiceImage(name)
	}
	return "ghcr.io/cloudfactory/" + name + ":latest"
}

// makeDataDir makes a data directory with the owner user and mode 0700. It does not change a
// directory that exists.
//
// The entry point of the image of the panel database gives its directory to the database
// user of the container (postgres, UID 999), which is a different UID on the host. The
// database server then opens each file through this directory. "install -d" on a directory
// that exists sets the owner and the mode again. A database server that operates then cannot
// open its files ("could not open file global/pg_filenode.map: Permission denied"), until the
// container starts again. This occurred when the install script ran again while the panel
// database operated.
func (d *Deps) makeDataDir(ctx context.Context, dir, user string) error {
	if _, err := os.Lstat(dir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	_, err := d.Run.Run(ctx, "install", "-d", "-o", user, "-g", user, "-m", "0700", dir)
	return err
}

// databaseUnit gives the input for the unit of a database server: the address of its
// customer, its port and its limits (record).
func (d *Deps) databaseUnit(s dbServer, customer, address string, record DatabaseServer) quadlet.Database {
	u := quadlet.Database{
		Engine:    s.Engine,
		ID:        s.ID,
		Image:     d.serviceImage(s.Engine),
		DataDir:   d.dataDir(s),
		EnvFile:   filepath.Join(d.dbDir(s), s.Engine+".env"),
		Gvisor:    d.runtime(),
		Customer:  customer,
		Address:   address,
		Port:      record.Port,
		Limits:    quadlet.Limits{VCPUs: record.VCPUs, MemoryBytes: record.MemoryBytes},
		DiskBytes: record.DiskBytes,
	}
	if s.Engine == "valkey" {
		u.MemoryMB = valkeyDataMB(record.MemoryBytes)
	}
	return u
}

// dbUnit gives the input for the unit of a database server, and the record of the database
// server, from the state.
func (d *Deps) dbUnit(s dbServer) (quadlet.Database, DatabaseServer, error) {
	customer, record, address, err := d.State.DatabaseServer(s.ID)
	if err != nil {
		return quadlet.Database{}, record, err
	}
	if s.ID == "" || customer == "" || record.Engine != s.Engine {
		return quadlet.Database{}, record, fmt.Errorf("this server does not have %s", s)
	}
	return d.databaseUnit(s, customer, address, record), record, nil
}

// prepareServer makes the Unix user, the directories, the disk, the secret and the unit of a
// database server. It reports true when the unit changed.
//
// The root helper makes the unit from the values of the database server and from its own
// settings. The worker cannot give the text of the unit of a database server: a unit can
// run a program with the rights of its user on the host, and that user owns the data of a
// customer.
func (d *Deps) prepareServer(ctx context.Context, s dbServer, customer, address string, record DatabaseServer) (changed bool, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opDBPrepare, serverIn{Server: s, Customer: customer, Address: address, Record: record}, &changed)
		return changed, err
	}
	u := d.databaseUnit(s, customer, address, record)
	user := dbUser(s)
	if err := d.prepareAccount(ctx, s); err != nil {
		return false, err
	}
	// The disk of the database server has the data directory. Root owns the directory of the
	// disk, so the user cannot put a link in the place of the data directory.
	if err := d.ensureDisk(ctx, s, record.DiskBytes); err != nil {
		return false, err
	}
	if err := d.makeDataDir(ctx, u.DataDir, user); err != nil {
		return false, err
	}
	if s.Engine == "valkey" {
		// Valkey has a user file, not an environment file.
		if err := d.writeValkeyUsers(s, u.DataDir); err != nil {
			return false, err
		}
	} else if err := d.writeSuperuserSecret(ctx, s, u.EnvFile); err != nil {
		return false, err
	}
	if d.Images != nil {
		// The image is in the shared store of root, so it does not use the disk limit of the
		// database server. With no shared store, the user of the database server pulls it
		// (EnsureDatabaseService).
		if err := d.Images.EnsureService(ctx, u.Image); err != nil {
			return false, err
		}
	}
	return d.Host.WriteUnit(user, s.container()+".container", quadlet.DatabaseUnit(u))
}

// prepareAccount makes the Unix user of a database server, and the directories that lead to
// its files. Only the root helper calls it.
//
//   - The users of the database servers go through dbHome, "servers" and "homes". They cannot
//     list them (mode 0711).
//   - Root owns the directory of the database server. Only root and the user of the database
//     server can open it (mode 0750), and the user cannot change it.
func (d *Deps) prepareAccount(ctx context.Context, s dbServer) error {
	// Root owns the three directories. "install -d" also sets the owner and the mode of a
	// directory that exists.
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", "root", "-g", "root", "-m", "0711", d.dbHome(), filepath.Dir(d.dbDir(s)), filepath.Dir(d.dbUserHome(s))); err != nil {
		return err
	}
	user := dbUser(s)
	if err := d.Host.EnsureDatabaseUser(ctx, user, d.dbUserHome(s)); err != nil {
		return err
	}
	_, err := d.Run.Run(ctx, "install", "-d", "-o", "root", "-g", user, "-m", "0750", d.dbDir(s))
	return err
}

// EnsureDatabaseService starts a database server, if it does not operate, and waits until it
// accepts connections. systemd starts it again at boot. The state has the record of a
// database server before this function starts it (ensureServer).
//
// The start of a container can take minutes. Only the jobs of the same database server wait
// for it: each database server has its own lock.
func (d *Deps) EnsureDatabaseService(ctx context.Context, s dbServer) error {
	if err := checkEngine(s.Engine); err != nil {
		return err
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
	if record.Held {
		return fmt.Errorf("%s: %w", s, errServerMoves)
	}
	// The address of the container must be on "lo" before Podman publishes the port. The
	// firewall lets the websites of the customer connect to the port.
	if err := d.syncDatabaseAccess(ctx); err != nil {
		return err
	}
	// The root helper makes the Unix user of the database server first: the commands below
	// run as that user.
	changed, err := d.prepareServer(ctx, s, u.Customer, u.Address, record)
	if err != nil {
		return err
	}
	user := dbUser(s)
	// A database server finds its image in the shared store of root. With no shared store,
	// the user pulls the image.
	if _, err := d.Host.Podman(ctx, user, "image", "exists", u.Image); err != nil {
		if d.Images != nil {
			// The user does not pull a copy: a copy would use the disk limit of the database
			// server, and the firewall refuses the connections of the user.
			return fmt.Errorf("the user %s cannot read the image %s in the shared image store: %w", user, u.Image, err)
		}
		// The signature policy of Podman refuses an image with no valid signature (FR-1101).
		if _, err := d.Host.Podman(ctx, user, "pull", "--quiet", u.Image); err != nil {
			return err
		}
	}
	service := s.container() + ".service"
	if changed {
		if _, err := d.Host.Systemctl(ctx, user, "daemon-reload"); err != nil {
			return err
		}
		// A new image or new settings take effect with a restart.
		if _, err := d.Host.Systemctl(ctx, user, "restart", service); err != nil {
			return err
		}
	} else if _, err := d.Host.Systemctl(ctx, user, "start", service); err != nil {
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

// databaseServers gives the database servers that this server has, for each customer.
func (d *Deps) databaseServers() ([]dbServer, error) {
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return nil, err
	}
	var out []dbServer
	for _, customer := range slices.Sorted(maps.Keys(instances)) {
		servers := instances[customer].Servers
		for _, id := range slices.Sorted(maps.Keys(servers)) {
			out = append(out, dbServer{ID: id, Engine: servers[id].Engine})
		}
	}
	return out, nil
}

// RefreshDatabaseServices writes the units of the database servers that the server already
// has again, for example with the image of a new agent version. It also mounts the disk of
// each database server, and loads the database access of the websites into the firewall.
func (d *Deps) RefreshDatabaseServices(ctx context.Context) error {
	d.removeOldServerData(ctx)
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
		// A database server that moves stays stopped after a restart of the agent.
		if err := d.EnsureDatabaseService(ctx, s); err != nil && !errors.Is(err, errServerMoves) {
			errs = append(errs, fmt.Errorf("%s: %w", s, err))
		}
	}
	return errors.Join(errs...)
}

// writeSuperuserSecret makes the password of the superuser once. The entry point of the image
// sets it at the first start. The agent connects to PostgreSQL through the socket with no
// password. MariaDB needs the password, so the client gets it in MYSQL_PWD from the
// environment of the container.
//
// Podman reads the file as the Unix user of the database server. Root owns the file and the
// group of the user can read it, so the user cannot change it.
func (d *Deps) writeSuperuserSecret(ctx context.Context, s dbServer, path string) error {
	owner, mode := "root:"+dbUser(s), os.FileMode(0o640)
	if _, err := os.Stat(path); err == nil {
		// A job that stopped after it wrote the file can leave the file with no owner and
		// mode for the user. Thus each job sets them again.
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		_, err := d.Run.Run(ctx, "chown", owner, path)
		return err
	}
	pass := randomHex(48)
	content := "POSTGRES_PASSWORD=" + pass + "\n"
	if s.Engine == "mariadb" {
		content = "MARIADB_ROOT_PASSWORD=" + pass + "\nMYSQL_PWD=" + pass + "\n"
	}
	// The file has its final mode only after it has its owner.
	if _, err := host.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	if _, err := d.Run.Run(ctx, "chown", owner, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// databaseListens checks that the SQL server of a container accepts TCP connections.
func (d *Deps) databaseListens(ctx context.Context, s dbServer) error {
	args, err := d.dbExec(s, "-i")
	if err != nil {
		return err
	}
	if s.Engine == "mariadb" {
		// "ping" is successful when the server answers, also when the server refuses the user.
		// The server refuses the user here, and writes one warning for that in its log.
		args = append(args, "mariadb-admin", "--protocol=tcp", "--host=127.0.0.1", "--port=3306", "--connect-timeout=5", "ping")
	} else {
		args = append(args, "pg_isready", "--quiet", "--host=127.0.0.1", "--port=5432", "--timeout=5")
	}
	_, err = d.Run.Run(ctx, args[0], args[1:]...)
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
