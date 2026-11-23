package actions

import (
	"os"
	"regexp"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const (
	packServerID  = "01JBQ3Z5K8N6T4W2X9Y7R5P3D6"
	packServerKey = "01jbq3z5k8n6t4w2x9y7r5p3d6"
	// packImage is the image of the version 8.4 of the example MySQL pack, with its digest.
	packImage = "ghcr.io/example/cf-mysql:8.4@sha256:0000000000000000000000000000000000000000000000000000000000000084"
)

// mysqlPack gives the manifest of the example database pack in the repository.
func mysqlPack(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../plugins/packs/mysql/pack.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// packServer gives a database server of the engine of the example database pack, as the panel
// sends it: the first port of the block of the pack, and the version 8.4.
func packServer() *agentv1.DatabaseServer {
	return &agentv1.DatabaseServer{Id: packServerID, Customer: customerID, Engine: "mysql", Version: "8.4", Port: quadlet.PackDatabasePortMin,
		Limits: &agentv1.Limits{Vcpus: 1, MemoryBytes: 1 << 30, DiskBytes: 5 << 30}}
}

// withMySQLPack installs the example database pack on the server of the tests. The image of
// the pack answers each operation of /cf/manage.
func withMySQLPack(t *testing.T, d *Deps, f *execx.Fake) string {
	t.Helper()
	withPolicy(t, d)
	t.Cleanup(func() { _ = quadlet.SetPackEngines(nil) })
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{signedPack(t, mysqlPack(t))}})); err != nil {
		t.Fatal(err)
	}
	manage := execOf("mysql", packServerKey) + "/cf/manage "
	f.Rules = append([]execx.Rule{
		{Prefix: manage + "ready", Output: `{"ready":true}`},
		{Prefix: manage + "sizes", Output: `{"sizes":[{"name":"acme_x7k2_shop","bytes":1048576}]}`},
		{Prefix: manage + "dump", Output: "-- MySQL dump\n"},
		{Prefix: manage, Output: "{}"},
	}, f.Rules...)
	f.Calls, f.Inputs, f.Envs = nil, nil, nil
	return manage
}

func TestADatabasePackAddsAnEngine(t *testing.T) {
	d, f := paritySite(t)
	withMySQLPack(t, d, f)
	if !isManaged("mysql") || isManaged("mariadb") || !strings.Contains(strings.Join(engines(), " "), "mysql") {
		t.Fatalf("engines = %v", engines())
	}
	// The websites connect on the port of the manifest. The host port is in the block of the
	// pack engines, which does not overlap the blocks of the built-in engines.
	if quadlet.DatabasePort("mysql") != 3306 {
		t.Errorf("port = %d", quadlet.DatabasePort("mysql"))
	}
	for port, ok := range map[int]bool{quadlet.PackDatabasePortMin: true, quadlet.PackDatabasePortMax: true, 3306: false, 19999: false, quadlet.PackDatabasePortMax + 1: false} {
		if err := quadlet.CheckDatabasePort("mysql", port); (err == nil) != ok {
			t.Errorf("port %d: %v", port, err)
		}
	}
	if err := quadlet.CheckDatabasePort("mariadb", quadlet.PackDatabasePortMin); err == nil {
		t.Error("a MariaDB database server can have a port of the pack engines")
	}
	// The root helper has the engine too: it makes the unit of the database server.
	if !strings.Contains(strings.Join(rootOf(d).serviceImages(), " "), packImage) {
		t.Errorf("the root helper does not accept the image of the pack: %v", rootOf(d).serviceImages())
	}
}

func TestADatabaseServerOfAPackEngine(t *testing.T) {
	d, f := paritySite(t)
	manage := withMySQLPack(t, d, f)
	newFirewall(t, d)
	req := &agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mysql", Name: "acme_x7k2_shop",
		User: "acme_x7k2_shop", Password: "Abcdefghijklmnop1234", Access: "website", DatabaseServer: packServer()}
	if err := run(t)(d.CreateDatabase(req)); err != nil {
		t.Fatalf("%v\n%v", err, f.Calls)
	}

	// The unit: the image of the version from the pack, the data directory of the manifest,
	// the password of the administrator, and the same isolation as a built-in engine.
	account := host.DatabaseUser(packServerKey)
	u := unitFile(t, d, account, "cf-mysql-"+packServerKey+".container")
	for _, want := range []string{
		"Image=" + packImage + "\nPull=never\n",
		"PublishPort=" + customerAddress + ":20000:3306\n",
		"Volume=" + dataOf(d, packServerKey) + ":/var/lib/mysql:Z\n",
		"EnvironmentFile=" + serverDir(d, packServerKey) + "/mysql.env\n",
		"ReadOnly=true\n", "DropCapability=all\n", "User=999\n", "Tmpfs=/tmp\n",
		"Description=Cloudfactory MySQL database server " + packServerKey + "\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit does not have %q:\n%s", want, u)
		}
	}
	secret, err := os.ReadFile(serverDir(d, packServerKey) + "/mysql.env")
	if err != nil || !regexp.MustCompile(`^CF_SUPERUSER_PASSWORD=[0-9a-f]{48}\n$`).Match(secret) {
		t.Errorf("secret = %q %v", secret, err)
	}

	// Each task is one operation of /cf/manage, with a JSON object on stdin.
	for op, want := range map[string]string{
		"create-database": `{"name":"acme_x7k2_shop"}` + "\n",
		"set-user":        `{"user":"acme_x7k2_shop","password":"Abcdefghijklmnop1234","databases":[{"name":"acme_x7k2_shop","access":"readwrite"}]}` + "\n",
	} {
		if got := inputsOf(f, manage+op); got != want {
			t.Errorf("%s: stdin = %q, want %q", op, got, want)
		}
	}

	// A new password and read access. A user that keeps its password gets no password.
	f.Calls, f.Inputs = nil, nil
	set := &agentv1.SetDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mysql", Name: "acme_x7k2_shop", Access: "website",
		WebsiteDatabases: []string{"acme_x7k2_shop"}, Grants: []*agentv1.DatabaseGrant{{Database: "acme_x7k2_shop", Privileges: "readonly"}}, DatabaseServer: packServer()}
	if err := run(t)(d.SetDatabaseUser(set)); err != nil {
		t.Fatal(err)
	}
	if got := inputsOf(f, manage+"set-user"); got != `{"user":"acme_x7k2_shop","databases":[{"name":"acme_x7k2_shop","access":"readonly"}]}`+"\n" {
		t.Errorf("set-user: %q", got)
	}

	// The sizes come from the image.
	sizes := d.databaseSizes(t.Context())
	if len(sizes) != 1 || sizes[0].Engine != "mysql" || sizes[0].Name != "acme_x7k2_shop" || sizes[0].SizeBytes != 1<<20 {
		t.Errorf("sizes = %+v", sizes)
	}

	// The dump of a backup names the database on the first line of stdin. The root helper
	// writes that line.
	f.Calls, f.Inputs = nil, nil
	if err := d.resetDumps(t.Context(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	srv := dbServer{ID: packServerKey, Engine: "mysql"}
	if _, err := d.dumpToFile(t.Context(), srv, "acme_x7k2", "acme_x7k2_shop"); err != nil {
		t.Fatal(err)
	}
	if got := inputsOf(f, manage+"dump"); got != `{"name":"acme_x7k2_shop"}`+"\n" {
		t.Errorf("dump: stdin = %q", got)
	}
	dump, err := os.ReadFile(d.dumpDir("acme_x7k2") + "/mysql-acme_x7k2_shop.dump")
	if err != nil || string(dump) != "-- MySQL dump\n" {
		t.Errorf("dump = %q %v", dump, err)
	}

	// The removal of the user and of the database.
	f.Calls, f.Inputs = nil, nil
	if err := run(t)(d.DeleteDatabaseUser(&agentv1.DeleteDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mysql", Name: "acme_x7k2_shop", DatabaseServer: packServerID})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mysql", Name: "acme_x7k2_shop", DatabaseServer: packServerID, Users: []string{"acme_x7k2_shop"}})); err != nil {
		t.Fatal(err)
	}
	if inputsOf(f, manage+"drop-user") != `{"user":"acme_x7k2_shop"}`+"\n" || inputsOf(f, manage+"drop-database") != `{"name":"acme_x7k2_shop"}`+"\n" {
		t.Errorf("drop: %v %q", f.Calls, f.Inputs)
	}

	// The pack cannot go while the database server uses it.
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: nil})); err == nil || !strings.Contains(err.Error(), "uses the engine mysql") {
		t.Errorf("remove the pack: %v", err)
	}
}

// The first version of cf-database/1 has no access from anywhere, no move and no change of
// the version. The agent refuses them with a clear message.
func TestAPackEngineRefusesWhatTheContractDoesNotHave(t *testing.T) {
	d, f := paritySite(t)
	withMySQLPack(t, d, f)
	newFirewall(t, d)
	anywhere := &agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mysql", Name: "acme_x7k2_shop",
		User: "acme_x7k2_shop", Password: "Abcdefghijklmnop1234", Access: "anywhere", DatabaseServer: packServer()}
	if _, err := d.CreateDatabase(anywhere); err == nil || !strings.Contains(err.Error(), "access from anywhere") {
		t.Errorf("access from anywhere: %v", err)
	}
	if err := checkTransfer(transferIn{Server: dbServer{ID: packServerKey, Engine: "mysql"}, Peer: "10.77.0.2", Token: strings.Repeat("a", 64)}); err == nil || !strings.Contains(err.Error(), "move") {
		t.Errorf("move: %v", err)
	}
	for version, want := range map[string]string{"": "needs a version", "9.0": `no version "9.0"`} {
		s := packServer()
		s.Version = version
		_, err := d.ensureServer(t.Context(), s, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("version %q: %v", version, err)
		}
	}
	maria := databaseServer("mariadb")
	maria.Version = "11.4"
	if _, err := d.ensureServer(t.Context(), maria, false); err == nil || !strings.Contains(err.Error(), "no versions") {
		t.Errorf("a version of a built-in engine: %v", err)
	}
	// The root helper checks the version again from its own packs.
	in := serverIn{Server: dbServer{ID: packServerKey, Engine: "mysql"}, Customer: customerKey, Address: customerAddress,
		Record: DatabaseServer{Engine: "mysql", Version: "9.0", Port: quadlet.PackDatabasePortMin}}
	if err := d.Gate.Call(t.Context(), opDBPrepare, in, new(bool)); err == nil || !strings.Contains(err.Error(), "9.0") {
		t.Errorf("root: %v", err)
	}
	// A database server keeps the version of its engine.
	if _, err := d.ensureServer(t.Context(), packServer(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.putServer(customerKey, packServerKey, DatabaseServer{Engine: "mysql", Version: "8.0", Port: quadlet.PackDatabasePortMin}, true); err == nil {
		t.Error("the version of a database server changed")
	}
}
