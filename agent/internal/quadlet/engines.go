package quadlet

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// EngineSpec is what the units need to know about one database engine: its port, the host
// name that the website containers use, and the lines of the container unit that are only for
// this engine. The agent keeps the work with the data of an engine (SQL, users, dumps) in its
// engine drivers (package actions). A new built-in engine adds one EngineSpec and one driver.
// A database pack (cf-database/1) adds an EngineSpec with SetPackEngines, and uses the driver
// of the pack engines.
type EngineSpec struct {
	// Name is the name of the engine in the requests of the panel, for example "mariadb".
	Name string
	// Label is the name for people, for example "MariaDB".
	Label string
	// Port is the usual port of the engine. It is the port of the engine in its container, and
	// the port of a database server of the engine at its member address. For a built-in
	// engine, it is also the port of the first database server of the engine on the address
	// of a customer.
	Port int
	// Host is the host name of the database servers of the engine in the website containers.
	// A pack engine has none: the websites connect to its database servers by their member
	// names.
	Host string
	// Lines gives the lines of the [Container] section that are only for this engine: the
	// data volume, the environment and the start command.
	Lines func(d Database) []string
	// Pack is the slug and the version of the pack of a pack engine, for example
	// "mysql 1.0.0". A built-in engine has none.
	Pack string
}

// The host ports of the database servers of the pack engines on the address of a customer.
// The panel gives each database pack a block of DatabasePorts ports in this range, one block
// for each pack (validate.MaxPacks).
const (
	PackDatabasePortMin = validate.MinDatabasePackPort
	PackDatabasePortMax = PackDatabasePortMin + validate.MaxPacks*DatabasePorts - 1
)

// engineSpecs are the engines of this agent version, in the order of their port ranges. The
// order is the order of the host names in a website container.
var engineSpecs = []EngineSpec{
	{Name: "mariadb", Label: "MariaDB", Port: MariaDBPort, Host: MariaDBHost, Lines: mariaDBLines},
	{Name: "postgresql", Label: "PostgreSQL", Port: PostgreSQLPort, Host: PostgreSQLHost, Lines: postgreSQLLines},
	{Name: "valkey", Label: "Valkey", Port: ValkeyPort, Host: ValkeyHost, Lines: valkeyLines},
}

var (
	packEnginesMu sync.RWMutex
	// packEngines are the engines of the database packs of the server, in the order of the
	// packs.
	packEngines []EngineSpec
)

// SetPackEngines sets the engines of the database packs of the server. It replaces the
// engines of an earlier call.
func SetPackEngines(specs []EngineSpec) error {
	for i, e := range specs {
		if slices.ContainsFunc(engineSpecs, func(b EngineSpec) bool { return b.Name == e.Name }) {
			return fmt.Errorf("quadlet: the pack engine %q has the name of a built-in engine", e.Name)
		}
		if e.Name == "" || e.Label == "" || e.Host != "" || e.Lines == nil || e.Pack == "" || e.Port < 1024 || e.Port > 65535 {
			return fmt.Errorf("quadlet: the pack engine %q is not complete", e.Name)
		}
		if slices.ContainsFunc(specs[:i], func(o EngineSpec) bool { return o.Name == e.Name }) {
			return fmt.Errorf("quadlet: two packs have the engine %q", e.Name)
		}
	}
	packEnginesMu.Lock()
	packEngines = slices.Clone(specs)
	packEnginesMu.Unlock()
	return nil
}

// Engines gives the engines of the server: the built-in engines in a fixed order, then the
// engines of the database packs.
func Engines() []EngineSpec {
	packEnginesMu.RLock()
	defer packEnginesMu.RUnlock()
	return slices.Concat(engineSpecs, packEngines)
}

// EngineNames gives the names of the engines of the server, in the order of Engines.
func EngineNames() []string {
	all := Engines()
	out := make([]string, 0, len(all))
	for _, e := range all {
		out = append(out, e.Name)
	}
	return out
}

// Engine gives the EngineSpec of an engine.
func Engine(name string) (EngineSpec, bool) {
	for _, e := range Engines() {
		if e.Name == name {
			return e, true
		}
	}
	return EngineSpec{}, false
}

// IsPackEngine reports whether an engine comes from a database pack.
func IsPackEngine(name string) bool {
	e, ok := Engine(name)
	return ok && e.Pack != ""
}

// PackEngineLines gives the lines of the unit of a pack engine (cf-database/1): the data
// directory of the database server at dataDir, and the password of the administrator in
// CF_SUPERUSER_PASSWORD. The pack gives dataDir, and packs.Parse checked it.
func PackEngineLines(dataDir string) func(d Database) []string {
	return func(d Database) []string {
		return []string{
			"Volume=" + d.DataDir + ":" + dataDir + ":Z",
			"EnvironmentFile=" + d.EnvFile,
		}
	}
}

// checkEngineSpecs makes sure that no two engines have a port in common, and that each engine
// has the values that a unit needs. The tests call it.
func checkEngineSpecs(specs []EngineSpec) error {
	for i, a := range specs {
		if a.Name == "" || a.Label == "" || a.Host == "" || a.Lines == nil || a.Port < 1024 || a.Port+DatabasePorts > 65536 {
			return fmt.Errorf("quadlet: the engine %q is not complete", a.Name)
		}
		for _, b := range specs[i+1:] {
			if a.Name == b.Name || a.Host == b.Host {
				return fmt.Errorf("quadlet: the engines %q and %q have the same name or host", a.Name, b.Name)
			}
			if a.Port < b.Port+DatabasePorts && b.Port < a.Port+DatabasePorts {
				return fmt.Errorf("quadlet: the ports of the engines %q and %q overlap", a.Name, b.Name)
			}
		}
	}
	return nil
}

func valkeyLines(d Database) []string {
	return []string{
		// The data and the user file. The files belong to the Unix user of the database
		// server, which is the user valkey of the container.
		"Volume=" + d.DataDir + ":/data:Z",
		"Exec=valkey-server " + strings.Join(ValkeyArgs(d), " "),
	}
}

func mariaDBLines(d Database) []string {
	return []string{
		"Volume=" + d.DataDir + ":/var/lib/mysql:Z",
		"Tmpfs=/run/mysqld",
		// Audit batch 2, finding 4: the image does not let root use the socket with no
		// password. The env file gives the root password to the server at the first start,
		// and to the client (MYSQL_PWD) in each "podman exec". No root user can use TCP.
		"EnvironmentFile=" + d.EnvFile,
		"Environment=MARIADB_ROOT_HOST=localhost",
		"Environment=MARIADB_AUTO_UPGRADE=1",
	}
}

func postgreSQLLines(d Database) []string {
	lines := []string{
		// PGDATA is the mount itself (images/postgresql/Containerfile).
		"Volume=" + d.DataDir + ":/var/lib/postgresql/data:Z",
		"Tmpfs=/var/run/postgresql",
		"ShmSize=256m",
		"EnvironmentFile=" + d.EnvFile,
		`Environment="POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 --auth-local=trust"`,
	}
	if wal := PostgresWALMB(d.DiskBytes); wal > 0 {
		// The write-ahead log does not fill a small disk. The smallest size that PostgreSQL
		// keeps is two log files of 16 MB.
		lines = append(lines, fmt.Sprintf("Exec=postgres -c max_wal_size=%dMB -c min_wal_size=32MB", wal))
	}
	return lines
}

// The usual ports of the engines of this agent version.
const (
	MariaDBPort    = 3306
	PostgreSQLPort = 5432
	ValkeyPort     = 6379
)
