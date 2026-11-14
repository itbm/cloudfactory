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

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Each customer has its own database containers on a server: one container for each engine
// that the customer uses. The websites of the customer on the server use them. Thus a
// customer cannot use the memory, the connections, the settings or the superuser of the
// database server of a different customer.
//
//   - The user cf-db runs each container in its own gVisor sandbox, with its own data
//     directory, its own superuser password, and its own memory limit.
//   - The containers of one customer publish their ports on one address of
//     quadlet.DatabaseNetwork. The host names "mariadb.internal", "postgres.internal" and
//     "valkey.internal" in each website container point to the address of its customer.
//   - The host firewall lets a website connect only to the address of its own customer
//     (the set database_access).
//
// The panel sends the ID of the customer with a new website (Website.database_instance). The
// agent records the website in the state, with the database containers of the customer
// (DatabaseInstance.Websites). That record tells each later action which containers the
// website uses. A website from an earlier release has no record: it uses the shared
// containers of that release until MoveDatabases moves its databases.

// dbServer is one database container of this server.
type dbServer struct {
	// Instance is the ID of a customer in lower case. Empty is the shared container of an
	// earlier release, for all customers.
	Instance string
	// Engine is "mariadb", "postgresql" or "valkey".
	Engine string
}

// container is the name of the Podman container and of its unit.
func (s dbServer) container() string { return quadlet.DatabaseContainer(s.Engine, s.Instance) }

// String names the container in an error message.
func (s dbServer) String() string {
	if s.Instance == "" {
		return "shared " + s.Engine
	}
	return s.Engine + " of customer " + s.Instance
}

// errNoSharedValkey is the answer when a website from an earlier release asks for Valkey.
var errNoSharedValkey = errors.New("this website uses the shared database containers of an earlier release, which have no Valkey; move the databases of the website to the database containers of its customer first")

// instanceKey checks the ID of a customer from the panel, and gives the key of its database
// containers: the ID in lower case. An empty ID gives an empty key.
func instanceKey(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	if err := validate.ID(id); err != nil {
		return "", fmt.Errorf("the database instance: %w", err)
	}
	return strings.ToLower(id), nil
}

// server gives the database container of an engine for a website: the container of the
// customer of the website, or the shared container of an earlier release. A step calls it when
// it runs, because an earlier job can move the databases of the website.
func (d *Deps) server(websiteID, engine string) (dbServer, error) {
	if _, err := dbContainer(engine); err != nil {
		return dbServer{}, err
	}
	key, _, err := d.State.WebsiteDatabases(websiteID)
	return dbServer{Instance: key, Engine: engine}, err
}

// dbDir holds the files of the database containers of one customer, or of the shared
// containers: the data directory and the secret of each engine.
func (d *Deps) dbDir(s dbServer) string {
	if s.Instance == "" {
		return d.dbHome()
	}
	return filepath.Join(d.dbHome(), "customers", s.Instance)
}

// UseDatabases records that a website uses the database containers of a customer, and gives
// the address of the containers. The first website of a customer gets the smallest free
// address number for the customer. The search and the record are one step with one lock.
//
// With moving set, the website only keeps the containers on the server: MoveDatabases copies
// the databases of the website into them, and the website uses them after a second call with
// moving not set. With no website, the function only makes the record of the customer.
func (s *State) UseDatabases(key, websiteID string, moving bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return "", err
	}
	if f.Databases == nil {
		f.Databases = map[string]*DatabaseInstance{}
	}
	if websiteID != "" {
		for other, inst := range f.Databases {
			if other != key && slices.Contains(inst.Websites, websiteID) {
				return "", fmt.Errorf("the website %s uses the database containers of the customer %s", websiteID, other)
			}
		}
	}
	inst, changed := f.Databases[key], false
	if inst == nil {
		used := map[int]bool{}
		for _, i := range f.Databases {
			used[i.Number] = true
		}
		n := 1
		for used[n] {
			n++
		}
		if n > quadlet.DatabaseAddresses {
			return "", fmt.Errorf("this server has database containers for %d customers, and it has no address for one more", quadlet.DatabaseAddresses)
		}
		inst = &DatabaseInstance{Number: n, Websites: []string{}}
		f.Databases[key], changed = inst, true
	}
	switch {
	case websiteID == "" || slices.Contains(inst.Websites, websiteID):
	case moving:
		if !slices.Contains(inst.Moving, websiteID) {
			inst.Moving, changed = append(inst.Moving, websiteID), true
		}
	default:
		inst.Moving = slices.DeleteFunc(inst.Moving, func(id string) bool { return id == websiteID })
		inst.Websites, changed = append(inst.Websites, websiteID), true
	}
	if changed {
		if err := s.write(f); err != nil {
			return "", err
		}
	}
	return quadlet.DatabaseAddress(inst.Number)
}

// ReleaseDatabases records that a website does not use the database containers of a customer.
// It reports true, and removes the record of the containers, when no website uses them then.
// With no record, it also reports true: a first attempt removed the record.
func (s *State) ReleaseDatabases(key, websiteID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return false, err
	}
	inst := f.Databases[key]
	if inst == nil {
		return true, nil
	}
	same := func(id string) bool { return id == websiteID }
	inst.Websites, inst.Moving = slices.DeleteFunc(inst.Websites, same), slices.DeleteFunc(inst.Moving, same)
	unused := len(inst.Websites) == 0 && len(inst.Moving) == 0
	if unused {
		delete(f.Databases, key)
	}
	return unused, s.write(f)
}

// WebsiteDatabases gives the key and the address of the database containers that a website
// uses. Both are "" for a website that uses the shared containers of an earlier release.
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

// DatabaseInstances gives a copy of the record of the database containers of each customer.
func (s *State) DatabaseInstances() (map[string]DatabaseInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := map[string]DatabaseInstance{}
	for key, inst := range f.Databases {
		out[key] = *inst
	}
	return out, nil
}

// DatabaseAddress gives the address of the database containers of a customer, or "" when the
// customer has none on this server.
func (s *State) DatabaseAddress(key string) (string, error) {
	all, err := s.DatabaseInstances()
	if err != nil {
		return "", err
	}
	inst, ok := all[key]
	if !ok {
		return "", nil
	}
	return quadlet.DatabaseAddress(inst.Number)
}

// databaseAddress gives the address for the database host names of a website container: the
// address of the containers of its customer, or "" for the shared containers.
func (d *Deps) databaseAddress(websiteID string) string {
	_, address, err := d.State.WebsiteDatabases(websiteID)
	if err != nil {
		slog.Error("read the database containers of a website", "website", websiteID, "err", err)
	}
	return address
}

// useDatabases is State.UseDatabases. It holds dbMu, so that it is one step with the check in
// releaseDatabases.
func (d *Deps) useDatabases(key, websiteID string, moving bool) (string, error) {
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	return d.State.UseDatabases(key, websiteID, moving)
}

// instanceLock gives the lock of the containers of one customer. "" is the shared containers.
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

// releaseDatabases records that a website does not use the database containers of its
// customer. When that was the last website of the customer on this server, the agent stops
// the containers of the customer and removes their data. The panel removes the databases of a
// website before the website, so the containers have no data of a website then.
//
// A new website of the same customer can start at the same time. The lock of the customer
// and dbMu make each of the two wait for the other: the new website either keeps the
// containers, or gets new ones after the removal is complete.
func (d *Deps) releaseDatabases(ctx context.Context, key, websiteID string) error {
	if key == "" {
		return nil
	}
	lock := d.instanceLock(key)
	lock.Lock()
	defer lock.Unlock()
	d.dbMu.Lock()
	unused, err := d.State.ReleaseDatabases(key, websiteID)
	if unused {
		for _, engine := range Engines {
			delete(d.dbReady, dbServer{Instance: key, Engine: engine})
		}
	}
	d.dbMu.Unlock()
	if err != nil || !unused {
		return err
	}
	return d.removeInstance(ctx, key)
}

// removeInstance stops the database containers of a customer and removes their units and
// their data. It is safe to run it again after a failure. The caller holds the lock of the
// customer.
func (d *Deps) removeInstance(ctx context.Context, key string) error {
	dir, err := d.Host.UnitDir(DBUser)
	if err != nil {
		return err
	}
	reload := false
	for _, engine := range Engines {
		s := dbServer{Instance: key, Engine: engine}
		unit := s.container() + ".container"
		if _, err := os.Stat(filepath.Join(dir, unit)); err != nil {
			continue
		}
		if _, err := d.Host.Systemctl(ctx, DBUser, "stop", s.container()+".service"); err != nil {
			return err
		}
		if err := d.Host.RemoveUnit(DBUser, unit); err != nil {
			return err
		}
		reload = true
	}
	if reload {
		if _, err := d.Host.Systemctl(ctx, DBUser, "daemon-reload"); err != nil {
			return err
		}
	}
	// The directory gets a different name in one step, so that new containers of the same
	// customer never start with a part of the old data.
	data := d.dbDir(dbServer{Instance: key})
	if _, err := os.Lstat(data); err == nil {
		removed := filepath.Join(filepath.Dir(data), removedPrefix+key+"-"+randomHex(8))
		if err := os.Rename(data, removed); err != nil {
			return err
		}
		if err := os.RemoveAll(removed); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// removedPrefix starts the name of a data directory that removeInstance removes.
const removedPrefix = ".removed-"

// removeOldInstanceData removes the data directories of a removal that did not complete, for
// example because the agent stopped.
func (d *Deps) removeOldInstanceData() {
	dir := filepath.Join(d.dbHome(), "customers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), removedPrefix) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				slog.Warn("remove the data of old database containers", "dir", e.Name(), "err", err)
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

// databaseLimits gives the limits of a database container of a customer.
func (d *Deps) databaseLimits(engine string) quadlet.Limits {
	mb := d.databaseMemoryMB()
	if engine == "valkey" {
		// Valkey needs memory above its data limit: for the copy of the data when it writes
		// the append-only file again, for the connections, and for the gVisor sandbox.
		mb = 2*d.valkeyMemoryMB() + 128
	}
	return quadlet.Limits{VCPUs: d.DatabaseCPUs, MemoryBytes: int64(mb) << 20}
}

// databaseMemoryMB gives the memory limit of a MariaDB or PostgreSQL container of a customer:
// the setting of the agent, or one quarter of the memory of the server.
func (d *Deps) databaseMemoryMB() int {
	if d.DatabaseMemoryMB > 0 {
		return d.DatabaseMemoryMB
	}
	return defaultDatabaseMemoryMB(hostMemoryMB("/proc/meminfo"))
}

// defaultDatabaseMemoryMB is one quarter of the memory of the server, from 512 MB to 2048 MB.
func defaultDatabaseMemoryMB(totalMB int) int {
	return min(max(totalMB/4, 512), 2048)
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
// websites, and databaseAccessFile is the file of the agent with its elements.
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

var websiteJumpRE = regexp.MustCompile(`^(\s*)ct state new meta skuid (\d+)-(\d+) jump websites\s*$`)

// addDatabaseRules gives the firewall file of the installer with the rules of the database
// containers of the customers. A file of this release has them already. In a file of an
// earlier release, the function adds:
//
//   - the set database_access,
//   - a rule that lets only root and the websites connect to the database network,
//   - a rule that lets a website connect to the address of its customer, and
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
				m[1]+"# The database containers of the customers. Only root (the agent) and the websites can",
				m[1]+"# connect to them. The chain \"websites\" lets a website connect only to the containers of",
				m[1]+"# its own customer.",
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
					in+"# Each element is the UID of a website and the address of the database containers of",
					in+"# its customer. The agent writes the elements in "+filepath.Join(dir, databaseAccessFile)+".",
					in+"set "+databaseAccessSet+" {",
					in+"  typeof meta skuid . ip daddr",
					in+"}",
				)
				table = true
			}
		case "chain websites {":
			if !chain {
				out = append(out,
					in+"# The database containers of the customer of the website: MariaDB, PostgreSQL and Valkey.",
					in+"# A website cannot connect to the database containers of a different customer.",
					in+fmt.Sprintf("meta skuid . ip daddr @%s tcp dport { 3306, 5432, 6379 } accept", databaseAccessSet),
				)
				chain = true
			}
		}
	}
	if !table || !jump || !chain || !strings.Contains(content, "chain refuse {") {
		return "", errors.New("the file does not have the rules of the installer, so the agent cannot add the rules of the database containers")
	}
	out = append(out, "# The elements of the set "+databaseAccessSet+". The agent writes them.", `include "`+dir+`/*.nft"`, "")
	return strings.Join(out, "\n"), nil
}

// openDatabaseNetwork makes sure that the firewall of the installer has the rules of the
// database containers of the customers. With no file, the administrator manages the firewall.
func (d *Deps) openDatabaseNetwork(ctx context.Context) error {
	path := d.firewallFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		slog.Warn("no Cloudfactory firewall file; let each website connect only to the database address of its customer in your firewall", "file", path, "network", quadlet.DatabaseNetwork)
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
		return fmt.Errorf("nftables refuses the rules of the database containers for %s (%w); run the installer again on this server", path, err)
	}
	if _, err := host.WriteFile(path, next, 0o644); err != nil {
		return err
	}
	slog.Info("added the rules of the database containers of the customers to the firewall", "file", path)
	_, err = d.Run.Run(ctx, "nft", "-f", path)
	return err
}

// databaseAccess gives the content of the database access file: one element for each website
// that uses the database containers of its customer.
func (d *Deps) databaseAccess() (string, error) {
	sites, err := d.State.All()
	if err != nil {
		return "", err
	}
	users := map[string]string{}
	for _, s := range sites {
		users[s.ID] = s.UnixUser
	}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return "", err
	}
	elements := []string{}
	for _, inst := range instances {
		address, err := quadlet.DatabaseAddress(inst.Number)
		if err != nil {
			return "", err
		}
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
			elements = append(elements, fmt.Sprintf("%d . %s", uid, address))
		}
	}
	slices.Sort(elements)
	elements = slices.Compact(elements)
	var b strings.Builder
	b.WriteString(quadlet.Header)
	b.WriteString("# Each element is the UID of a website and the address of the database containers of its\n# customer. A website can connect only to that address of the database network.\n")
	fmt.Fprintf(&b, "flush set inet cloudfactory %s\n", databaseAccessSet)
	if len(elements) > 0 {
		fmt.Fprintf(&b, "add element inet cloudfactory %s { %s }\n", databaseAccessSet, strings.Join(elements, ", "))
	}
	return b.String(), nil
}

// syncDatabaseAccess makes sure that "lo" has the database network, and that the firewall
// lets each website connect to the database containers of its own customer only. The agent
// calls it after each change of the websites that use database containers, and at its start.
//
// The file of the installer includes the access file, so the kernel gets the elements again
// when the server starts or when somebody loads the file of the installer.
func (d *Deps) syncDatabaseAccess(ctx context.Context) error {
	d.netMu.Lock()
	defer d.netMu.Unlock()
	if !d.netReady {
		if err := d.addDatabaseAddress(ctx); err != nil {
			return err
		}
		if err := d.openDatabaseNetwork(ctx); err != nil {
			return err
		}
		d.netReady = true
	}
	if _, err := os.Stat(d.firewallFile()); os.IsNotExist(err) {
		// The administrator manages the firewall (openDatabaseNetwork).
		return nil
	}
	content, err := d.databaseAccess()
	if err != nil {
		return err
	}
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
