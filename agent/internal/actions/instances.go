package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// A customer has its own database servers on a server. A database server is one database
// container with one engine. It has its own limits (vCPUs, memory and disk space), and each
// database is in one database server. Thus a customer cannot use the memory, the disk space,
// the connections, the settings or the superuser of the database server of a different
// customer.
//
//   - The user cf-db runs each container in its own gVisor sandbox, with its own data
//     directory and its own superuser password.
//   - All database servers of one customer publish their ports on one address of
//     quadlet.DatabaseNetwork: the address of the customer. Each database server has its own
//     port. The host names "mariadb.internal", "postgres.internal" and "valkey.internal" in
//     each website container point to the address of the customer of the website.
//   - The host firewall lets a website connect only to the ports of the database servers of
//     its own customer, on the address of its own customer (the set database_access).
//
// The panel sends the ID of the customer with a new website (Website.database_instance). The
// agent records the website in the state, with the customer (DatabaseInstance.Websites). A
// website from an earlier release has no record: it uses the shared containers of that
// release until MoveDatabases moves its databases.
//
// The panel sends each database server with the request that needs it first, and the ID of
// the database server with each later request. The agent records each database server in the
// state (DatabaseInstance.Servers).

// dbServer is one database container of this server.
type dbServer struct {
	// ID is the ID of a database server in lower case. Empty is the shared container of an
	// earlier release, for all customers.
	ID string
	// Engine is "mariadb", "postgresql" or "valkey".
	Engine string
}

// container is the name of the Podman container and of its unit.
func (s dbServer) container() string { return quadlet.DatabaseContainer(s.Engine, s.ID) }

// String names the container in an error message.
func (s dbServer) String() string {
	if s.ID == "" {
		return "shared " + s.Engine
	}
	return "the " + s.Engine + " database server " + s.ID
}

// errNoSharedValkey is the answer when a website from an earlier release asks for Valkey.
var errNoSharedValkey = errors.New("this website uses the shared database containers of an earlier release, which have no Valkey; move the databases of the website to database servers of its customer first")

// instanceKey checks the ID of a customer from the panel, and gives the key of its record:
// the ID in lower case. An empty ID gives an empty key.
func instanceKey(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	if err := validate.ID(id); err != nil {
		return "", fmt.Errorf("the database instance: %w", err)
	}
	return strings.ToLower(id), nil
}

// serverKey checks the ID of a database server from the panel, and gives its key: the ID in
// lower case. An empty ID gives an empty key.
func serverKey(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	if err := validate.ID(id); err != nil {
		return "", fmt.Errorf("the database server: %w", err)
	}
	return strings.ToLower(id), nil
}

// checkDatabaseServer checks a database server from the panel. An empty database server is
// correct: it means the shared container of an earlier release.
func checkDatabaseServer(s *agentv1.DatabaseServer) error {
	if s.GetId() == "" {
		return nil
	}
	_, idErr := serverKey(s.GetId())
	_, customerErr := instanceKey(s.GetCustomer())
	errs := []error{idErr, customerErr}
	if s.GetCustomer() == "" {
		errs = append(errs, errors.New("the database server has no customer"))
	}
	if _, err := dbContainer(s.GetEngine()); err != nil {
		errs = append(errs, err)
	} else if err := quadlet.CheckDatabasePort(s.GetEngine(), int(s.GetPort())); err != nil {
		errs = append(errs, err)
	}
	l := s.GetLimits()
	if l.GetVcpus() < 0 || l.GetMemoryBytes() < 0 || l.GetDiskBytes() < 0 {
		errs = append(errs, errors.New("a limit of the database server is less than 0"))
	}
	if m := l.GetMemoryBytes(); m > 0 && m < minDatabaseMemory {
		errs = append(errs, fmt.Errorf("the memory limit of a database server must be %d MB or more", minDatabaseMemory>>20))
	}
	if b := l.GetDiskBytes(); b > 0 && b < minDatabaseDisk {
		errs = append(errs, fmt.Errorf("the disk limit of a database server must be %d MB or more", minDatabaseDisk>>20))
	}
	return errors.Join(errs...)
}

const (
	// minDatabaseMemory is the smallest memory limit of a database server. A database server
	// with less memory does not start in gVisor.
	minDatabaseMemory = 256 << 20
	// minDatabaseDisk is the smallest disk limit of a database server. An empty MariaDB or
	// PostgreSQL data directory and the journal of the file system need some hundred MB.
	minDatabaseDisk = 1 << 30
)

// recordOf gives the record of a database server from the panel.
func recordOf(s *agentv1.DatabaseServer) DatabaseServer {
	l := s.GetLimits()
	return DatabaseServer{Engine: s.GetEngine(), Port: int(s.GetPort()), VCPUs: l.GetVcpus(), MemoryBytes: l.GetMemoryBytes(), DiskBytes: l.GetDiskBytes()}
}

// server gives the database container for a database or a database user of a website. id is
// the ID of the database server from the request. An empty ID is the shared container of an
// earlier release. A step calls the function when it runs, because an earlier job can move
// the databases of the website.
//
// The function refuses a database server of a different customer, and a request that does
// not agree with the record of the website: MoveDatabases can run between the request of the
// panel and the step.
func (d *Deps) server(websiteID, id, engine string) (dbServer, error) {
	if _, err := dbContainer(engine); err != nil {
		return dbServer{}, err
	}
	customer, _, err := d.State.WebsiteDatabases(websiteID)
	if err != nil {
		return dbServer{}, err
	}
	if id == "" {
		if customer != "" {
			return dbServer{}, errors.New("the website uses the database servers of its customer, and the request has no database server; send the request again")
		}
		return dbServer{Engine: engine}, nil
	}
	if customer == "" {
		return dbServer{}, errors.New("the website uses the shared database containers of an earlier release, and the request has a database server; move the databases of the website first")
	}
	return d.serverOf(customer, id, engine)
}

// serverOf gives the container of a database server of a customer. It refuses a database
// server that this server does not have, or that has a different customer or engine.
func (d *Deps) serverOf(customer, id, engine string) (dbServer, error) {
	key, err := serverKey(id)
	if err != nil {
		return dbServer{}, err
	}
	owner, record, _, err := d.State.DatabaseServer(key)
	if err != nil {
		return dbServer{}, err
	}
	switch {
	case owner == "":
		return dbServer{}, fmt.Errorf("this server does not have the database server %s", key)
	case owner != customer:
		return dbServer{}, fmt.Errorf("the database server %s is not a database server of the customer %s", key, customer)
	case record.Engine != engine:
		return dbServer{}, fmt.Errorf("the database server %s has the engine %s, not %s", key, record.Engine, engine)
	}
	return dbServer{ID: key, Engine: engine}, nil
}

// checkWebsiteServer checks the database server of a request for a database or a database
// user of an engine.
func checkWebsiteServer(s *agentv1.DatabaseServer, engine string) error {
	if err := checkDatabaseServer(s); err != nil {
		return err
	}
	if s.GetId() != "" && s.GetEngine() != engine {
		return fmt.Errorf("the database server has the engine %s, not %s", s.GetEngine(), engine)
	}
	return nil
}

// websiteServer is server for a request that has the database server itself, not only its ID.
// It makes a database server of the customer of the website that this server does not have,
// and starts it.
func (d *Deps) websiteServer(ctx context.Context, websiteID string, s *agentv1.DatabaseServer, engine string) (dbServer, error) {
	if s.GetId() != "" {
		customer, _, err := d.State.WebsiteDatabases(websiteID)
		if err != nil {
			return dbServer{}, err
		}
		if key, _ := instanceKey(s.GetCustomer()); customer != "" && customer != key {
			return dbServer{}, fmt.Errorf("the database server %s is not a database server of the customer of the website", s.GetId())
		}
		// With no customer, server refuses the request: the website uses the shared containers.
		if customer != "" {
			if _, err := d.ensureServer(ctx, s, false); err != nil {
				return dbServer{}, err
			}
		}
	}
	return d.server(websiteID, s.GetId(), engine)
}

// ensureServer makes sure that this server has a database server from the panel, and that
// the database server operates. It gives the container. An empty database server gives the
// shared container of an earlier release, which the function does not start.
//
// A database server that the agent has keeps its limits. Only SetDatabaseServer changes them
// (update), because a change restarts the database server.
func (d *Deps) ensureServer(ctx context.Context, s *agentv1.DatabaseServer, update bool) (dbServer, error) {
	if s.GetId() == "" {
		return dbServer{Engine: s.GetEngine()}, nil
	}
	key, _ := serverKey(s.GetId())
	customer, _ := instanceKey(s.GetCustomer())
	srv := dbServer{ID: key, Engine: s.GetEngine()}
	// put records the limits, and makes the next step write the unit and the disk again.
	put := func(record DatabaseServer, update bool) (bool, error) {
		lock := d.instanceLock(key)
		lock.Lock()
		defer lock.Unlock()
		changed, err := d.putServer(customer, key, record, update)
		if changed {
			d.dbMu.Lock()
			delete(d.dbReady, srv)
			d.dbMu.Unlock()
		}
		return changed, err
	}
	owner, old, _, err := d.State.DatabaseServer(key)
	if err != nil {
		return srv, err
	}
	// A new database server: the firewall lets the websites of the customer connect to its
	// port. New limits: the unit and the disk get them.
	changed, err := put(recordOf(s), update)
	if err != nil {
		return srv, err
	}
	err = d.EnsureDatabaseService(ctx, srv)
	if changed && owner != "" && errors.Is(err, errDiskKeepsSize) {
		// The disk cannot have the new limit. The database server keeps all its old limits,
		// and operates with them: a job that fails changes nothing.
		_, putErr := put(old, true)
		return srv, errors.Join(err, putErr, d.EnsureDatabaseService(ctx, srv))
	}
	return srv, err
}

// putServer is State.PutDatabaseServer. It holds dbMu, so that it is one step with the check
// in releaseDatabases.
func (d *Deps) putServer(customer, key string, record DatabaseServer, update bool) (bool, error) {
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	return d.State.PutDatabaseServer(customer, key, record, update)
}

// dbDir holds the files of one database server, or of the shared containers: the data
// directory and the secret of each engine.
func (d *Deps) dbDir(s dbServer) string {
	if s.ID == "" {
		return d.dbHome()
	}
	return filepath.Join(d.dbHome(), "servers", s.ID)
}

// record gives the record of a customer in f, or a new record with the smallest free address
// number.
func (f *stateFile) record(key string) (*DatabaseInstance, error) {
	if f.Databases == nil {
		f.Databases = map[string]*DatabaseInstance{}
	}
	if inst := f.Databases[key]; inst != nil {
		return inst, nil
	}
	used := map[int]bool{}
	for _, i := range f.Databases {
		used[i.Number] = true
	}
	n := 1
	for used[n] {
		n++
	}
	if n > quadlet.DatabaseAddresses {
		return nil, fmt.Errorf("this server has database servers for %d customers, and it has no address for one more", quadlet.DatabaseAddresses)
	}
	inst := &DatabaseInstance{Number: n, Websites: []string{}}
	f.Databases[key] = inst
	return inst, nil
}

// forget removes the record of a customer that has no website and no database server.
func (f *stateFile) forget(key string) {
	if inst := f.Databases[key]; inst != nil && len(inst.Websites) == 0 && len(inst.Servers) == 0 {
		delete(f.Databases, key)
	}
}

// UseDatabases records that a website can use the database servers of a customer, and gives
// the address of the customer. The first record of a customer gets the smallest free address
// number. The search and the record are one step with one lock. With no website, the function
// only makes the record of the customer.
func (s *State) UseDatabases(key, websiteID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return "", err
	}
	if websiteID != "" {
		for other, inst := range f.Databases {
			if other != key && slices.Contains(inst.Websites, websiteID) {
				return "", fmt.Errorf("the website %s uses the database servers of the customer %s", websiteID, other)
			}
		}
	}
	known := f.Databases[key] != nil
	inst, err := f.record(key)
	if err != nil {
		return "", err
	}
	changed := !known
	if websiteID != "" && !slices.Contains(inst.Websites, websiteID) {
		inst.Websites, changed = append(inst.Websites, websiteID), true
	}
	if changed {
		if err := s.write(f); err != nil {
			return "", err
		}
	}
	return quadlet.DatabaseAddress(inst.Number)
}

// ReleaseDatabases records that a website does not use the database servers of a customer.
// The record of a customer with no website and no database server goes, and its address is
// free for a different customer.
func (s *State) ReleaseDatabases(key, websiteID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	inst := f.Databases[key]
	if inst == nil {
		return nil
	}
	inst.Websites = slices.DeleteFunc(inst.Websites, func(id string) bool { return id == websiteID })
	f.forget(key)
	return s.write(f)
}

// PutDatabaseServer records a database server of a customer. It reports true when the record
// is new or changed. The engine, the port and the customer of a database server do not
// change. With update not set, a database server that the state has keeps its limits.
func (s *State) PutDatabaseServer(customer, key string, record DatabaseServer, update bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return false, err
	}
	for other, inst := range f.Databases {
		if old := inst.Servers[key]; old != nil && other != customer {
			return false, fmt.Errorf("the database server %s is a database server of the customer %s", key, other)
		}
	}
	inst, err := f.record(customer)
	if err != nil {
		return false, err
	}
	if old := inst.Servers[key]; old != nil {
		switch {
		case old.Engine != record.Engine:
			return false, fmt.Errorf("the database server %s has the engine %s, not %s", key, old.Engine, record.Engine)
		case old.Port != record.Port:
			return false, fmt.Errorf("the database server %s has the port %d, not %d", key, old.Port, record.Port)
		case !update || *old == record:
			return false, nil
		}
	} else {
		for other, srv := range inst.Servers {
			if srv.Port == record.Port {
				return false, fmt.Errorf("the database server %s of the customer has the port %d", other, record.Port)
			}
		}
	}
	if inst.Servers == nil {
		inst.Servers = map[string]*DatabaseServer{}
	}
	inst.Servers[key] = &record
	return true, s.write(f)
}

// DeleteDatabaseServer removes the record of a database server. The record of a customer with
// no website and no database server goes too.
func (s *State) DeleteDatabaseServer(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	for customer, inst := range f.Databases {
		if inst.Servers[key] != nil {
			delete(inst.Servers, key)
			f.forget(customer)
			return s.write(f)
		}
	}
	return nil
}

// DatabaseServer gives the customer, the record and the address of a database server. The
// customer is "" when the state does not have the database server.
func (s *State) DatabaseServer(key string) (customer string, record DatabaseServer, address string, err error) {
	all, err := s.DatabaseInstances()
	if err != nil {
		return "", record, "", err
	}
	for customer, inst := range all {
		if srv := inst.Servers[key]; srv != nil {
			address, err := quadlet.DatabaseAddress(inst.Number)
			return customer, *srv, address, err
		}
	}
	return "", record, "", nil
}

// WebsiteDatabases gives the customer whose database servers a website can use, and the
// address of that customer. Both are "" for a website that uses the shared containers of an
// earlier release.
func (s *State) WebsiteDatabases(websiteID string) (key, address string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return "", "", err
	}
	for key, inst := range f.Databases {
		if slices.Contains(inst.Websites, websiteID) {
			address, err := quadlet.DatabaseAddress(inst.Number)
			return key, address, err
		}
	}
	return "", "", nil
}

// DatabaseInstances gives a copy of the record of each customer.
func (s *State) DatabaseInstances() (map[string]DatabaseInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := map[string]DatabaseInstance{}
	for key, inst := range f.Databases {
		c := DatabaseInstance{Number: inst.Number, Websites: slices.Clone(inst.Websites), Servers: map[string]*DatabaseServer{}}
		for id, srv := range inst.Servers {
			record := *srv
			c.Servers[id] = &record
		}
		out[key] = c
	}
	return out, nil
}

// databaseAddress gives the address for the database host names of a website container: the
// address of its customer, or "" for the shared containers.
func (d *Deps) databaseAddress(websiteID string) string {
	_, address, err := d.State.WebsiteDatabases(websiteID)
	if err != nil {
		slog.Error("read the database servers of a website", "website", websiteID, "err", err)
	}
	return address
}

// useDatabases is State.UseDatabases. It holds dbMu, as each change of the records does.
func (d *Deps) useDatabases(key, websiteID string) (string, error) {
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	return d.State.UseDatabases(key, websiteID)
}

// releaseDatabases records that a website does not use the database servers of its customer.
// The database servers of the customer stay: only DeleteDatabaseServer removes one.
func (d *Deps) releaseDatabases(key, websiteID string) error {
	if key == "" {
		return nil
	}
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	return d.State.ReleaseDatabases(key, websiteID)
}

// instanceLock gives the lock of one database server. "" is the shared containers. The start
// and the removal of a database server hold it.
func (d *Deps) instanceLock(key string) *sync.Mutex {
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	if d.dbStart == nil {
		d.dbStart = map[string]*sync.Mutex{}
	}
	if d.dbStart[key] == nil {
		d.dbStart[key] = &sync.Mutex{}
	}
	return d.dbStart[key]
}

// stopServer stops the container of a database server and removes its unit. It is safe to
// run it again after a failure. The caller holds the lock of the database server.
func (d *Deps) stopServer(ctx context.Context, s dbServer) error {
	d.dbMu.Lock()
	delete(d.dbReady, s)
	d.dbMu.Unlock()
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return err
	}
	unit := s.container() + ".container"
	if _, err := os.Stat(filepath.Join(dir, unit)); err != nil {
		return nil
	}
	if _, err := d.Host.Systemctl(ctx, DBUser, "stop", s.container()+".service"); err != nil {
		return err
	}
	if err := d.Host.RemoveUnit(DBUser, unit); err != nil {
		return err
	}
	_, err = d.Host.Systemctl(ctx, DBUser, "daemon-reload")
	return err
}

// removeServerData removes the disk and the files of a database server that does not
// operate. It is safe to run it again after a failure. The caller holds the lock of the
// database server.
func (d *Deps) removeServerData(ctx context.Context, s dbServer) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDBRemoveData, serverIn{Server: s}, nil)
	}
	data := d.dbDir(s)
	if _, err := os.Lstat(data); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := d.removeDisk(ctx, s); err != nil {
		return err
	}
	// The directory gets a different name in one step, so that a new database server with the
	// same ID never starts with a part of the old data.
	removed := filepath.Join(filepath.Dir(data), removedPrefix+s.ID+"-"+randomHex(8))
	if err := os.Rename(data, removed); err != nil {
		return err
	}
	return os.RemoveAll(removed)
}

// removedPrefix starts the name of a data directory that removeServerData removes.
const removedPrefix = ".removed-"

// removeOldServerData removes the data directories of a removal that did not complete, for
// example because the agent stopped.
func (d *Deps) removeOldServerData(ctx context.Context) {
	if d.Gate != nil {
		if err := d.Gate.Call(ctx, opDBRemoveOld, nil, nil); err != nil {
			slog.Warn("remove the data of old database servers", "err", err)
		}
		return
	}
	dir := filepath.Join(d.dbHome(), "servers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), removedPrefix) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				slog.Warn("remove the data of an old database server", "dir", e.Name(), "err", err)
			}
		}
	}
}

// retireSharedDatabases stops the shared database containers of an earlier release when no
// website on this server uses them. Their data directories stay on the disk: an administrator
// removes them.
func (d *Deps) retireSharedDatabases(ctx context.Context) error {
	lock := d.instanceLock("")
	lock.Lock()
	defer lock.Unlock()
	sites, err := d.State.All()
	if err != nil {
		return err
	}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return err
	}
	moved := map[string]bool{}
	for _, inst := range instances {
		for _, id := range inst.Websites {
			moved[id] = true
		}
	}
	for _, s := range sites {
		if !moved[s.ID] {
			return nil
		}
	}
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return err
	}
	reload := false
	for _, engine := range Engines {
		s := dbServer{Engine: engine}
		unit := s.container() + ".container"
		if _, err := os.Stat(filepath.Join(dir, unit)); err != nil {
			continue
		}
		d.dbMu.Lock()
		delete(d.dbReady, s)
		d.dbMu.Unlock()
		if _, err := d.Host.Systemctl(ctx, DBUser, "stop", s.container()+".service"); err != nil {
			return err
		}
		if err := d.Host.RemoveUnit(DBUser, unit); err != nil {
			return err
		}
		reload = true
		slog.Info("stopped a shared database container of an earlier release; its data stays on the disk", "engine", engine, "data", filepath.Join(d.dbHome(), engine))
	}
	if reload {
		_, err = d.Host.Systemctl(ctx, DBUser, "daemon-reload")
	}
	return err
}

// valkeyDataMB gives the memory limit of a Valkey database server for its data, in MB, from
// the memory limit of its container. Valkey needs memory above its data limit: for the copy
// of the data when it writes the append-only file again, for the connections, and for the
// gVisor sandbox. With no limit of the container, the data limit is 256 MB.
func valkeyDataMB(memoryBytes int64) int {
	if memoryBytes <= 0 {
		return 256
	}
	return max(int(memoryBytes>>20-128)/2, 32)
}

func (d *Deps) firewallDir() string {
	if d.FirewallDir != "" {
		return d.FirewallDir
	}
	return "/etc/cloudfactory/firewall"
}

func (d *Deps) addressUnit() string {
	if d.AddressUnit != "" {
		return d.AddressUnit
	}
	return "/etc/systemd/system/cloudfactory-address.service"
}

// databaseAccessSet is the nftables set of the installer with the database access of the
// websites, and databaseAccessFile is the file of the agent with its elements. Each element is
// the UID of a website, the address of its customer, and the port of a database server of its
// customer.
const (
	databaseAccessSet  = "database_access"
	databaseAccessFile = "database-access.nft"
)

// databaseAddressArgs are the arguments of "ip" that add the database network to "lo". The
// installer has the same command in cloudfactory-address.service.
func databaseAddressArgs() []string {
	return []string{"address", "replace", quadlet.DatabaseClientAddress + "/" + strconv.Itoa(quadlet.DatabasePrefix), "dev", "lo"}
}

// addDatabaseAddress adds the database network to "lo", now and at each start of the server.
// A server from an earlier release has the address unit of the installer with no line for
// the network: the agent adds the line in a drop-in file.
func (d *Deps) addDatabaseAddress(ctx context.Context) error {
	unit := d.addressUnit()
	line := "ExecStart=/usr/sbin/ip " + strings.Join(databaseAddressArgs(), " ")
	b, err := os.ReadFile(unit)
	switch {
	case os.IsNotExist(err):
		slog.Warn("no address unit of the installer; the agent adds the database network to lo only while it operates", "unit", unit)
	case err != nil:
		return err
	case !strings.Contains(string(b), line):
		if err := os.MkdirAll(unit+".d", 0o755); err != nil {
			return err
		}
		content := quadlet.Header + "# The network of the database containers of the customers (quadlet.DatabaseNetwork).\n[Service]\n" + line + "\n"
		changed, err := host.WriteFile(filepath.Join(unit+".d", "database-network.conf"), content, 0o644)
		if err != nil {
			return err
		}
		if changed {
			if _, err := d.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
				return err
			}
		}
	}
	_, err = d.Run.Run(ctx, "ip", databaseAddressArgs()...)
	return err
}

// keepStopTime gives the user services of the server the time to stop their containers when
// the server stops or starts again (quadlet.UserStopSeconds). The installer of this release
// writes the drop-in file. A server from an earlier release does not have the file: the
// agent writes it. Without it, Ubuntu stops the database servers with SIGKILL after 5
// seconds, before they write their data to the disk.
func (d *Deps) keepStopTime(ctx context.Context) error {
	dir := filepath.Join(filepath.Dir(d.addressUnit()), "user@.service.d")
	file := filepath.Join(dir, quadlet.UserStopFile)
	if _, err := os.Stat(file); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := host.WriteFile(file, quadlet.Header+quadlet.UserStopUnit(), 0o644); err != nil {
		return err
	}
	// systemd applies the new time to the user services that operate now.
	_, err := d.Run.Run(ctx, "systemctl", "daemon-reload")
	return err
}

var websiteJumpRE = regexp.MustCompile(`^(\s*)ct state new meta skuid (\d+)-(\d+) jump websites\s*$`)

// addDatabaseRules gives the firewall file of the installer with the rules of the database
// containers of the customers. A file of this release has them already. In a file of an
// earlier release, the function adds:
//
//   - the set database_access,
//   - a rule that lets only root and the websites connect to the database network,
//   - a rule that lets a website connect to the database servers of its customer, and
//   - the include of the files of the agent in dir.
//
// It gives an error when the file does not have the rules of the installer.
func addDatabaseRules(content, dir string) (string, error) {
	if strings.Contains(content, "set "+databaseAccessSet+" {") {
		return content, nil
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	out := make([]string, 0, len(lines)+16)
	var table, jump, chain bool
	for _, l := range lines {
		if m := websiteJumpRE.FindStringSubmatch(l); m != nil && !jump {
			out = append(out,
				m[1]+"# The database servers of the customers. Only root (the agent) and the websites can",
				m[1]+"# connect to them. The chain \"websites\" lets a website connect only to the database",
				m[1]+"# servers of its own customer.",
				m[1]+fmt.Sprintf("ct state new ip daddr %s meta skuid != 0 meta skuid != %s-%s jump refuse", quadlet.DatabaseNetwork, m[2], m[3]),
			)
			jump = true
		}
		out = append(out, l)
		in := indent(l) + "  "
		switch strings.TrimSpace(l) {
		case "table inet cloudfactory {":
			if !table {
				out = append(out,
					in+"# Each element is the UID of a website, the address of its customer, and the port of a",
					in+"# database server of its customer. The agent writes the elements in",
					in+"# "+filepath.Join(dir, databaseAccessFile)+".",
					in+"set "+databaseAccessSet+" {",
					in+"  typeof meta skuid . ip daddr . tcp dport",
					in+"}",
				)
				table = true
			}
		case "chain websites {":
			if !chain {
				out = append(out,
					in+"# The database servers of the customer of the website: MariaDB, PostgreSQL and Valkey.",
					in+"# A website cannot connect to a database server of a different customer.",
					in+fmt.Sprintf("meta skuid . ip daddr . tcp dport @%s accept", databaseAccessSet),
				)
				chain = true
			}
		}
	}
	if !table || !jump || !chain || !strings.Contains(content, "chain refuse {") {
		return "", errors.New("the file does not have the rules of the installer, so the agent cannot add the rules of the database servers")
	}
	out = append(out, "# The elements of the set "+databaseAccessSet+". The agent writes them.", `include "`+dir+`/*.nft"`, "")
	return strings.Join(out, "\n"), nil
}

// openDatabaseNetwork makes sure that the firewall of the installer has the rules of the
// database servers of the customers. With no file, the administrator manages the firewall.
func (d *Deps) openDatabaseNetwork(ctx context.Context) error {
	path := d.firewallFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		slog.Warn("no Cloudfactory firewall file; let each website connect only to the database servers of its customer in your firewall", "file", path, "network", quadlet.DatabaseNetwork)
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.firewallDir(), 0o755); err != nil {
		return err
	}
	next, err := addDatabaseRules(string(b), d.firewallDir())
	if err != nil {
		return fmt.Errorf("the firewall file %s: %w; run the installer again on this server", path, err)
	}
	if next == string(b) {
		return nil
	}
	// nftables checks the new rules before they replace the file of the installer.
	check := path + ".new"
	if _, err := host.WriteFile(check, next, 0o644); err != nil {
		return err
	}
	defer os.Remove(check)
	if _, err := d.Run.Run(ctx, "nft", "-c", "-f", check); err != nil {
		return fmt.Errorf("nftables refuses the rules of the database servers for %s (%w); run the installer again on this server", path, err)
	}
	if _, err := host.WriteFile(path, next, 0o644); err != nil {
		return err
	}
	slog.Info("added the rules of the database servers of the customers to the firewall", "file", path)
	_, err = d.Run.Run(ctx, "nft", "-f", path)
	return err
}

// databaseAccess gives the database access of the websites: one element for each website and
// each database server of its customer.
func (d *Deps) databaseAccess() ([]accessElement, error) {
	sites, err := d.State.All()
	if err != nil {
		return nil, err
	}
	users := map[string]string{}
	for _, s := range sites {
		users[s.ID] = s.UnixUser
	}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return nil, err
	}
	elements := []accessElement{}
	for _, inst := range instances {
		for _, id := range inst.Websites {
			// A new website has no container before the state has it. A website that the
			// agent removes has no Unix user at the end of the job.
			user, ok := users[id]
			if !ok {
				continue
			}
			uid, err := d.Host.LookupUID(user)
			if err != nil {
				continue
			}
			for _, srv := range inst.Servers {
				elements = append(elements, accessElement{UID: uid, Number: inst.Number, Port: srv.Port})
			}
		}
	}
	return elements, nil
}

// accessContent gives the content of the database access file for the elements. It checks
// each element: the UID of a website user, an address of the database network, and a port of
// a database server. Thus the file never opens a different address or port, and never opens
// a port for a user that is not a website.
func accessContent(elements []accessElement) (string, error) {
	lines := make([]string, 0, len(elements))
	for _, e := range elements {
		if e.UID < host.WebsiteIDMin || e.UID > host.WebsiteIDMax {
			return "", fmt.Errorf("the UID %d is not the UID of a website user", e.UID)
		}
		address, err := quadlet.DatabaseAddress(e.Number)
		if err != nil {
			return "", err
		}
		if !slices.ContainsFunc(Engines, func(engine string) bool { return quadlet.CheckDatabasePort(engine, e.Port) == nil }) {
			return "", fmt.Errorf("%d is not a port of a database server", e.Port)
		}
		lines = append(lines, fmt.Sprintf("%d . %s . %d", e.UID, address, e.Port))
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	var b strings.Builder
	b.WriteString(quadlet.Header)
	b.WriteString("# Each element is the UID of a website, the address of its customer, and the port of a\n# database server of its customer. A website can connect to no other address or port of the\n# database network.\n")
	fmt.Fprintf(&b, "flush set inet cloudfactory %s\n", databaseAccessSet)
	if len(lines) > 0 {
		fmt.Fprintf(&b, "add element inet cloudfactory %s { %s }\n", databaseAccessSet, strings.Join(lines, ", "))
	}
	return b.String(), nil
}

// syncDatabaseAccess makes sure that "lo" has the database network, and that the firewall
// lets each website connect to the database servers of its own customer only. The agent calls
// it after each change of the websites and of the database servers, and at its start. The
// first call also makes sure that the database servers have the time to stop
// (ensureDatabaseNetwork).
//
// The file of the installer includes the access file, so the kernel gets the elements again
// when the server starts or when somebody loads the file of the installer.
func (d *Deps) syncDatabaseAccess(ctx context.Context) error {
	d.netMu.Lock()
	defer d.netMu.Unlock()
	if !d.netReady {
		if err := d.ensureDatabaseNetwork(ctx); err != nil {
			return err
		}
		d.netReady = true
	}
	elements, err := d.databaseAccess()
	if err != nil {
		return err
	}
	return d.loadDatabaseAccess(ctx, elements)
}

// ensureDatabaseNetwork adds the database network to "lo", and the rules of the database
// servers to the firewall of the installer. It also makes sure that the database servers
// have the time to stop (keepStopTime).
func (d *Deps) ensureDatabaseNetwork(ctx context.Context) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDBNetwork, nil, nil)
	}
	if err := d.addDatabaseAddress(ctx); err != nil {
		return err
	}
	if err := d.openDatabaseNetwork(ctx); err != nil {
		return err
	}
	return d.keepStopTime(ctx)
}

// loadDatabaseAccess writes the database access file and loads it into the kernel.
func (d *Deps) loadDatabaseAccess(ctx context.Context, elements []accessElement) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDBAccess, accessIn{Elements: elements}, nil)
	}
	// The check is first: the helper accepts no element that is not correct, also when it
	// does not load the elements.
	content, err := accessContent(elements)
	if err != nil {
		return err
	}
	if _, err := os.Stat(d.firewallFile()); os.IsNotExist(err) {
		// The administrator manages the firewall (openDatabaseNetwork).
		return nil
	}
	d.accessMu.Lock()
	defer d.accessMu.Unlock()
	file := filepath.Join(d.firewallDir(), databaseAccessFile)
	if _, err := host.WriteFile(file, content, 0o644); err != nil {
		return err
	}
	if content == d.accessLoaded {
		return nil
	}
	if _, err := d.Run.Run(ctx, "nft", "-f", file); err != nil {
		return err
	}
	d.accessLoaded = content
	return nil
}
