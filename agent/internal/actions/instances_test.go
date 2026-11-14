package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// A second customer and its website, for the tests that need two customers on one server.
const (
	otherCustomerID  = "01JBQ3Z5K8N6T4W2X9Y7R5P3C2"
	otherCustomerKey = "01jbq3z5k8n6t4w2x9y7r5p3c2"
	otherWebsiteID   = "01JBQ3Z5K8N6T4W2X9Y7R5P3N1"
	otherAddress     = "169.254.80.3"
)

// The database servers of the tests. The first customer has one database server of each
// engine, and a second MariaDB database server. The second customer has one MariaDB database
// server. A key is the ID in lower case.
const (
	mariaServerID   = "01JBQ3Z5K8N6T4W2X9Y7R5P3D1"
	mariaServerKey  = "01jbq3z5k8n6t4w2x9y7r5p3d1"
	pgServerID      = "01JBQ3Z5K8N6T4W2X9Y7R5P3D2"
	pgServerKey     = "01jbq3z5k8n6t4w2x9y7r5p3d2"
	valkeyServerID  = "01JBQ3Z5K8N6T4W2X9Y7R5P3D3"
	valkeyServerKey = "01jbq3z5k8n6t4w2x9y7r5p3d3"
	otherServerID   = "01JBQ3Z5K8N6T4W2X9Y7R5P3D4"
	otherServerKey  = "01jbq3z5k8n6t4w2x9y7r5p3d4"
	secondServerID  = "01JBQ3Z5K8N6T4W2X9Y7R5P3D5"
	secondServerKey = "01jbq3z5k8n6t4w2x9y7r5p3d5"
)

// databaseServer gives the database server of an engine of the first customer of the tests,
// as the panel sends it: the usual port of the engine, 1 vCPU, 1 GB of memory and 5 GB of
// disk space.
func databaseServer(engine string) *agentv1.DatabaseServer {
	id := map[string]string{"mariadb": mariaServerID, "postgresql": pgServerID, "valkey": valkeyServerID}[engine]
	return &agentv1.DatabaseServer{Id: id, Customer: customerID, Engine: engine, Port: int32(quadlet.DatabasePort(engine)),
		Limits: &agentv1.Limits{Vcpus: 1, MemoryBytes: 1 << 30, DiskBytes: 5 << 30}}
}

// otherServer gives the MariaDB database server of the second customer of the tests.
func otherServer() *agentv1.DatabaseServer {
	s := databaseServer("mariadb")
	s.Id, s.Customer = otherServerID, otherCustomerID
	return s
}

// secondServer gives the second MariaDB database server of the first customer of the tests.
func secondServer() *agentv1.DatabaseServer {
	s := databaseServer("mariadb")
	s.Id, s.Port = secondServerID, 3307
	return s
}

// serverDir holds the files of a database server of the tests, and dataOf is its data directory.
func serverDir(d *Deps, key string) string { return filepath.Join(d.DBHome, "servers", key) }
func dataOf(d *Deps, key string) string    { return filepath.Join(serverDir(d, key), "disk", "data") }

const runtimeImage = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"

// podmanDB is the start of each Podman command of the user cf-db in these tests.
const podmanDB = "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/990 podman "

// users gives each Unix user of the tests its own UID, as on a server.
func users(d *Deps) {
	ids := map[string]int{DBUser: 990, "acme_x7k2": 20001, "bolt_k3m9": 20002, "acme_s1t2": 20003}
	both(d, func(d *Deps) {
		d.Host.LookupUID = func(name string) (int, error) {
			if id, ok := ids[name]; ok {
				return id, nil
			}
			return 0, user.UnknownUserError(name)
		}
	})
}

// customerWebsite gives a website of the first customer of the tests.
func customerWebsite() *agentv1.Website {
	w := website(runtimeImage)
	w.DatabaseInstance = customerID
	return w
}

// otherWebsite gives a website of the second customer of the tests.
func otherWebsite() *agentv1.Website {
	w := website(runtimeImage)
	w.Id, w.UnixUser, w.DatabaseInstance = otherWebsiteID, "bolt_k3m9", otherCustomerID
	w.Domains = []*agentv1.Domain{{Name: "bolt.example", Kind: "primary"}}
	return w
}

// mariaRequest gives the request for a MariaDB database of a website in a database server. A
// nil database server is the shared container of an earlier release.
func mariaRequest(id, unixUser, name string, server *agentv1.DatabaseServer) *agentv1.CreateDatabaseRequest {
	return &agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: id, UnixUser: unixUser, Engine: "mariadb", Name: name, User: name, Password: "Abcdefghijklmnop1234", Access: "website", DatabaseServer: server}
}

// newFirewall writes a firewall file of this release, so that the agent loads the database
// access of the websites.
func newFirewall(t *testing.T, d *Deps) {
	t.Helper()
	old, err := os.ReadFile("testdata/firewall-0.1.9.nft")
	if err != nil {
		t.Fatal(err)
	}
	next, err := addDatabaseRules(string(old), d.firewallDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.firewallFile(), []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
}

func accessFile(t *testing.T, d *Deps) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.firewallDir(), databaseAccessFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each database server has its own container: its own name, address, data directory,
// superuser password and limits. A statement for a database goes to its database server only.
func TestEachDatabaseServerHasItsOwnContainer(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	newFirewall(t, d)
	for _, w := range []*agentv1.Website{customerWebsite(), otherWebsite()} {
		if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(otherWebsiteID, "bolt_k3m9", "bolt_k3m9_shop", otherServer()))); err != nil {
		t.Fatal(err)
	}
	if !f.Called("install -d -o cf-db -g cf-db -m 0700 " + filepath.Join(d.DBHome, "servers")) {
		t.Errorf("no directory for the database servers: %v", f.Calls)
	}
	secrets := map[string]bool{}
	for key, c := range map[string]struct{ customer, address string }{mariaServerKey: {customerKey, customerAddress}, otherServerKey: {otherCustomerKey, otherAddress}} {
		data := dataOf(d, key)
		u := unitFile(t, d, DBUser, "cf-mariadb-"+key+".container")
		for _, want := range []string{
			"Description=Cloudfactory MariaDB database server " + key + "\n",
			"ContainerName=cf-mariadb-" + key + "\n",
			"PublishPort=" + c.address + ":3306:3306\n",
			"Volume=" + data + ":/var/lib/mysql:Z\n",
			"EnvironmentFile=" + filepath.Join(serverDir(d, key), "mariadb.env") + "\n",
			"Label=io.cloudfactory.database-server=" + key + "\n",
			"Label=io.cloudfactory.customer=" + c.customer + "\n",
			"GlobalArgs=--runtime=/usr/local/bin/runsc ",
			// The limits of the database server.
			"CPUQuota=100%\n", "MemoryMax=1073741824\n", "MemorySwapMax=0\n",
		} {
			if !strings.Contains(u, want) {
				t.Errorf("the unit of %s has no %q:\n%s", key, want, u)
			}
		}
		if strings.Contains(u, quadlet.ServiceAddress) {
			t.Errorf("the unit of %s uses the service address:\n%s", key, u)
		}
		secret, err := os.ReadFile(filepath.Join(serverDir(d, key), "mariadb.env"))
		if err != nil || !strings.Contains(string(secret), "MARIADB_ROOT_PASSWORD=") {
			t.Fatalf("superuser secret of %s: %q %v", key, secret, err)
		}
		secrets[string(secret)] = true
		for _, want := range []string{
			"install -d -o cf-db -g cf-db -m 0700 " + serverDir(d, key),
			// A server with no loop devices: the disk of the database server is a directory.
			"install -d -o cf-db -g cf-db -m 0700 " + filepath.Join(serverDir(d, key), "disk"),
			"install -d -o cf-db -g cf-db -m 0700 " + data,
			"systemctl --user --machine=cf-db@ restart cf-mariadb-" + key + ".service",
		} {
			if !f.Called(want) {
				t.Errorf("no call %q", want)
			}
		}
	}
	if len(secrets) != 2 {
		t.Error("the two database servers have the same superuser password")
	}
	// Each statement is in the database server of the database. The host part of a user is
	// the address that the database servers see for the websites.
	for i, c := range f.Calls {
		in := f.Inputs[i]
		switch {
		case strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+mariaServerKey+" "):
			if strings.Contains(in, "bolt_k3m9") {
				t.Errorf("a statement of the second customer is in the database server of the first: %s", in)
			}
		case strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+otherServerKey+" "):
			if strings.Contains(in, "acme_x7k2") {
				t.Errorf("a statement of the first customer is in the database server of the second: %s", in)
			}
		case strings.Contains(c, "exec -i cf-mariadb "), strings.Contains(c, "cf-mariadb.service"):
			t.Errorf("the agent used the shared container: %s", c)
		}
	}
	in := strings.Join(f.Inputs, "")
	for _, want := range []string{
		"CREATE USER 'acme_x7k2_shop'@'169.254.80.1' IDENTIFIED BY 'Abcdefghijklmnop1234' REQUIRE NONE WITH MAX_USER_CONNECTIONS 100;",
		"GRANT ALL PRIVILEGES ON `bolt\\_k3m9\\_shop`.* TO 'bolt_k3m9_shop'@'169.254.80.1';",
	} {
		if !strings.Contains(in, want) {
			t.Errorf("no statement %q in:\n%s", want, in)
		}
	}
	// The database host names of each website point to the address of its customer.
	for name, address := range map[string]string{"acme_x7k2": customerAddress, "bolt_k3m9": otherAddress} {
		u := unitFile(t, d, name, "cf-"+name+".container")
		for _, host := range []string{"mariadb.internal", "postgres.internal", "valkey.internal"} {
			if want := "AddHost=" + host + ":" + address + "\n"; !strings.Contains(u, want) {
				t.Errorf("the unit of %s has no %q:\n%s", name, want, u)
			}
		}
	}
	// The firewall lets each website connect only to the database servers of its customer.
	access := accessFile(t, d)
	want := "flush set inet cloudfactory database_access\nadd element inet cloudfactory database_access { 20001 . 169.254.80.2 . 3306, 20002 . 169.254.80.3 . 3306 }\n"
	if !strings.HasSuffix(access, want) || !f.Called("nft -f "+filepath.Join(d.firewallDir(), databaseAccessFile)) {
		t.Errorf("access file:\n%s\ncalls: %v", access, f.Calls)
	}
	if !f.Called("ip address replace 169.254.80.1/20 dev lo") {
		t.Errorf("the database network is not on lo: %v", f.Calls)
	}
	// The report has the databases of all database servers.
	f.Rules = []execx.Rule{
		{Prefix: podmanDB + "exec -i cf-mariadb-" + mariaServerKey + " ", Output: "acme_x7k2_shop\t4096\n"},
		{Prefix: podmanDB + "exec -i cf-mariadb-" + otherServerKey + " ", Output: "bolt_k3m9_shop\t8192\n"},
	}
	sizes := d.databaseSizes(context.Background())
	if len(sizes) != 2 || sizes[0].Name != "acme_x7k2_shop" || sizes[0].SizeBytes != 4096 || sizes[1].Name != "bolt_k3m9_shop" || sizes[1].SizeBytes != 8192 {
		t.Errorf("sizes = %+v", sizes)
	}
}

// A customer can have two database servers of one engine. The second one has the next port
// on the address of the customer, and all websites of the customer can connect to it.
func TestACustomerCanHaveTwoDatabaseServersOfOneEngine(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	newFirewall(t, d)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_blog", secondServer()))); err != nil {
		t.Fatal(err)
	}
	u := unitFile(t, d, DBUser, "cf-mariadb-"+secondServerKey+".container")
	for _, want := range []string{
		"PublishPort=" + customerAddress + ":3307:3306\n",
		"Volume=" + dataOf(d, secondServerKey) + ":/var/lib/mysql:Z\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit of the second database server has no %q:\n%s", want, u)
		}
	}
	if access := accessFile(t, d); !strings.Contains(access, "{ 20001 . 169.254.80.2 . 3306, 20001 . 169.254.80.2 . 3307 }") {
		t.Errorf("access file:\n%s", access)
	}
	// Each database is in its database server only.
	for i, c := range f.Calls {
		in := f.Inputs[i]
		first := strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+mariaServerKey+" ")
		second := strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+secondServerKey+" ")
		if first && strings.Contains(in, "acme_x7k2_blog") || second && strings.Contains(in, "acme_x7k2_shop") {
			t.Errorf("a statement is in the incorrect database server: %s\n%s", c, in)
		}
	}
	if !strings.Contains(inputsOf(f, podmanDB+"exec -i cf-mariadb-"+secondServerKey+" "), "CREATE DATABASE `acme_x7k2_blog`") {
		t.Errorf("the second database server has no database:\n%s", strings.Join(f.Inputs, ""))
	}
	// A later request has only the ID of the database server.
	req := &agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_blog", Users: []string{"acme_x7k2_blog"}, DatabaseServer: secondServerID}
	f.Calls, f.Inputs = nil, nil
	if err := run(t)(d.DeleteDatabase(req)); err != nil {
		t.Fatal(err)
	}
	for i, c := range f.Calls {
		if strings.Contains(f.Inputs[i], "DROP DATABASE") && !strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+secondServerKey+" ") {
			t.Errorf("the agent removed the database in a different database server: %s", c)
		}
	}
	if !strings.Contains(strings.Join(f.Inputs, ""), "DROP DATABASE IF EXISTS `acme_x7k2_blog`;") {
		t.Errorf("the database stays:\n%s", strings.Join(f.Inputs, ""))
	}
	// Two database servers of a customer cannot have the same port.
	third := secondServer()
	third.Id = "01JBQ3Z5K8N6T4W2X9Y7R5P3D6"
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: third})); err == nil || !strings.Contains(err.Error(), "has the port 3307") {
		t.Errorf("a second database server with the same port: %v", err)
	}
}

// A website cannot use a database server of a different customer.
func TestAWebsiteCannotUseADatabaseServerOfADifferentCustomer(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	for _, w := range []*agentv1.Website{customerWebsite(), otherWebsite()} {
		if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(otherWebsiteID, "bolt_k3m9", "bolt_k3m9_shop", otherServer()))); err != nil {
		t.Fatal(err)
	}
	f.Calls, f.Inputs = nil, nil
	// The database server of the second customer, as it is.
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", otherServer()))); err == nil || !strings.Contains(err.Error(), "not a database server of the customer of the website") {
		t.Errorf("a database server of a different customer: %v", err)
	}
	// The ID of the database server of the second customer, with the first customer.
	stolen := otherServer()
	stolen.Customer = customerID
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", stolen))); err == nil || !strings.Contains(err.Error(), "is a database server of the customer "+otherCustomerKey) {
		t.Errorf("the ID of a database server of a different customer: %v", err)
	}
	// A later request with only the ID.
	for name, job := range map[string]func() error{
		"remove a database": func() error {
			return run(t)(d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: otherServerID}))
		},
		"remove a user": func() error {
			return run(t)(d.DeleteDatabaseUser(&agentv1.DeleteDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: otherServerID}))
		},
	} {
		if err := job(); err == nil || !strings.Contains(err.Error(), "is not a database server of the customer "+customerKey) {
			t.Errorf("%s in a database server of a different customer: %v", name, err)
		}
	}
	if strings.Contains(strings.Join(f.Inputs, ""), "acme_x7k2") {
		t.Errorf("the agent sent a statement:\n%s", strings.Join(f.Inputs, ""))
	}
	// The engine of a database server does not change.
	wrong := otherServer()
	wrong.Engine, wrong.Port = "postgresql", 5432
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: wrong})); err == nil || !strings.Contains(err.Error(), "has the engine mariadb") {
		t.Errorf("a different engine for a database server: %v", err)
	}
}

// A database server keeps its limits when a request for a database has different limits.
// SetDatabaseServer gives it new limits, and restarts it.
func TestSetDatabaseServerChangesTheLimits(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	newFirewall(t, d)
	// The panel can make a database server before the customer has a website on the server.
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: databaseServer("postgresql")})); err != nil {
		t.Fatal(err)
	}
	unit := "cf-postgresql-" + pgServerKey
	restart := "systemctl --user --machine=cf-db@ restart " + unit + ".service"
	if u := unitFile(t, d, DBUser, unit+".container"); !strings.Contains(u, "MemoryMax=1073741824\n") || !strings.Contains(u, "PublishPort="+customerAddress+":5432:5432\n") || !f.Called(restart) {
		t.Fatalf("the database server did not start: %v\n%s", f.Calls, u)
	}
	if access := accessFile(t, d); strings.Contains(access, "add element") {
		t.Errorf("access file with no website:\n%s", access)
	}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	if access := accessFile(t, d); !strings.Contains(access, "{ 20001 . 169.254.80.2 . 5432 }") {
		t.Errorf("access file with the website:\n%s", access)
	}
	// A request for a database has the database server with different limits.
	larger := databaseServer("postgresql")
	larger.Limits = &agentv1.Limits{Vcpus: 2, MemoryBytes: 2 << 30, DiskBytes: 10 << 30}
	f.Calls = nil
	req := &agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "postgresql", Name: "acme_x7k2_shop", User: "acme_x7k2_shop", Password: "Abcdefghijklmnop1234", Access: "website", DatabaseServer: larger}
	if err := run(t)(d.CreateDatabase(req)); err != nil {
		t.Fatal(err)
	}
	if u := unitFile(t, d, DBUser, unit+".container"); !strings.Contains(u, "MemoryMax=1073741824\n") || f.Called(restart) {
		t.Errorf("a request for a database changed the limits: %v\n%s", f.Calls, u)
	}
	// The same limits again: no restart.
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: databaseServer("postgresql")})); err != nil || f.Called(restart) {
		t.Errorf("the same limits restarted the database server: %v %v", err, f.Calls)
	}
	// New limits.
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: larger})); err != nil {
		t.Fatal(err)
	}
	u := unitFile(t, d, DBUser, unit+".container")
	for _, want := range []string{"CPUQuota=200%\n", "MemoryMax=2147483648\n", "MemorySwapMax=0\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("the unit has no %q:\n%s", want, u)
		}
	}
	if !f.Called(restart) {
		t.Errorf("new limits did not restart the database server: %v", f.Calls)
	}
	if _, record, _, _ := d.State.DatabaseServer(pgServerKey); record != (DatabaseServer{Engine: "postgresql", Port: 5432, VCPUs: 2, MemoryBytes: 2 << 30, DiskBytes: 10 << 30}) {
		t.Errorf("record = %+v", record)
	}
	// The port of a database server does not change.
	moved := databaseServer("postgresql")
	moved.Port = 5433
	if err := run(t)(d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID, DatabaseServer: moved})); err == nil || !strings.Contains(err.Error(), "has the port 5432") {
		t.Errorf("a different port for a database server: %v", err)
	}
	if _, err := d.SetDatabaseServer(&agentv1.DatabaseServerRequest{JobId: jobID}); err == nil {
		t.Error("the agent accepted a request with no database server")
	}
}

// The database servers of a customer stay when its websites go: only DeleteDatabaseServer
// removes a database server and its data. The firewall forgets each website that goes.
func TestOnlyDeleteDatabaseServerRemovesADatabaseServer(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	newFirewall(t, d)
	staging := customerWebsite()
	staging.Id, staging.UnixUser = "01JBQ3Z5K8N6T4W2X9Y7R5P3M8", "acme_s1t2"
	staging.Domains = []*agentv1.Domain{{Name: "staging.acme.example", Kind: "primary"}}
	for _, w := range []*agentv1.Website{customerWebsite(), staging} {
		if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	if u := unitFile(t, d, "acme_s1t2", "cf-acme_s1t2.container"); !strings.Contains(u, "AddHost=mariadb.internal:"+customerAddress+"\n") {
		t.Errorf("the second website of the customer has a different database address:\n%s", u)
	}
	// Each website of the customer can connect to the database server.
	if access := accessFile(t, d); !strings.Contains(access, "{ 20001 . 169.254.80.2 . 3306, 20003 . 169.254.80.2 . 3306 }") {
		t.Errorf("access file:\n%s", access)
	}
	data := serverDir(d, mariaServerKey)
	unit := "cf-mariadb-" + mariaServerKey
	stop := "systemctl --user --machine=cf-db@ stop " + unit + ".service"
	// The first website goes: its database goes, and the database server stays.
	req := &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2",
		Databases:     []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: mariaServerID}},
		DatabaseUsers: []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: mariaServerID}}}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.Inputs, ""), "DROP DATABASE IF EXISTS `acme_x7k2_shop`;") {
		t.Error("the database of the website stays")
	}
	if access := accessFile(t, d); !strings.Contains(access, "{ 20003 . 169.254.80.2 . 3306 }") {
		t.Errorf("access file after the first website went:\n%s", access)
	}
	// The last website goes: the database server stays, with its data.
	req = &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: staging.GetId(), UnixUser: "acme_s1t2"}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); err != nil || f.Called(stop) {
		t.Fatalf("the database server went with the websites: %v", err)
	}
	if instances, _ := d.State.DatabaseInstances(); len(instances[customerKey].Servers) != 1 || len(instances[customerKey].Websites) != 0 {
		t.Errorf("the state after the websites went: %+v", instances)
	}
	if access := accessFile(t, d); strings.Contains(access, "add element") {
		t.Errorf("access file with no website:\n%s", access)
	}
	// A different customer does not get the address of a customer that has a database server.
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: otherWebsite()})); err != nil {
		t.Fatal(err)
	}
	if _, address, _ := d.State.WebsiteDatabases(otherWebsiteID); address != otherAddress {
		t.Errorf("the address of the next customer = %q", address)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(otherWebsiteID, "bolt_k3m9", "bolt_k3m9_shop", otherServer()))); err != nil {
		t.Fatal(err)
	}
	// The removal of the database server.
	remove := &agentv1.DeleteDatabaseServerRequest{JobId: jobID2, DatabaseServer: mariaServerID}
	if err := run(t)(d.DeleteDatabaseServer(remove)); err != nil {
		t.Fatal(err)
	}
	if !f.Called(stop) {
		t.Errorf("the container did not stop: %v", f.Calls)
	}
	dir, _ := d.Host.UnitDir(DBUser)
	if _, err := os.Stat(filepath.Join(dir, unit+".container")); !os.IsNotExist(err) {
		t.Errorf("the unit stays: %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("the data stays: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(d.DBHome, "servers")); len(left) != 1 || left[0].Name() != otherServerKey {
		t.Errorf("the directory of the database servers has %v", left)
	}
	// The customer has no website and no database server: its record goes.
	if instances, _ := d.State.DatabaseInstances(); len(instances) != 1 || instances[otherCustomerKey].Servers[otherServerKey] == nil {
		t.Errorf("the state after the removal: %+v", instances)
	}
	if access := accessFile(t, d); !strings.Contains(access, "{ 20002 . 169.254.80.3 . 3306 }") {
		t.Errorf("access file after the removal:\n%s", access)
	}
	// The same job again does nothing, and the database server of the other customer stays.
	f.Calls = nil
	if err := run(t)(d.DeleteDatabaseServer(remove)); err != nil || len(f.Calls) != 0 {
		t.Errorf("the second removal: %v %v", err, f.Calls)
	}
	if _, err := os.Stat(serverDir(d, otherServerKey)); err != nil {
		t.Errorf("the database server of the other customer: %v", err)
	}
	for _, bad := range []string{"", "../../etc"} {
		if _, err := d.DeleteDatabaseServer(&agentv1.DeleteDatabaseServerRequest{JobId: jobID2, DatabaseServer: bad}); err == nil {
			t.Errorf("the agent accepted the database server %q", bad)
		}
	}
	// A removal that did not complete left a directory. The agent removes it at its start.
	old := filepath.Join(d.DBHome, "servers", removedPrefix+mariaServerKey+"-0123abcd")
	os.MkdirAll(filepath.Join(old, "disk", "data"), 0o700)
	d.removeOldServerData(context.Background())
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the directory of an old removal stays: %v", err)
	}
}

// The state gives each customer the smallest free address number.
func TestDatabaseAddressNumbers(t *testing.T) {
	d, _, _ := deps(t)
	use := func(key, id string) string {
		t.Helper()
		address, err := d.State.UseDatabases(key, id)
		if err != nil {
			t.Fatal(err)
		}
		return address
	}
	if a, b, again := use("a", "w1"), use("b", "w2"), use("a", "w3"); a != "169.254.80.2" || b != "169.254.80.3" || again != a {
		t.Errorf("addresses = %s %s %s", a, b, again)
	}
	record := DatabaseServer{Engine: "mariadb", Port: 3306}
	if changed, err := d.State.PutDatabaseServer("a", "s1", record, false); err != nil || !changed {
		t.Fatalf("a new database server: %v %v", changed, err)
	}
	// The customer keeps its address while it has a website or a database server.
	for _, id := range []string{"w1", "w3"} {
		if err := d.State.ReleaseDatabases("a", id); err != nil {
			t.Fatal(err)
		}
	}
	if c := use("c", "w4"); c != "169.254.80.4" {
		t.Errorf("a customer got the address of a customer with a database server: %s", c)
	}
	if customer, got, address, err := d.State.DatabaseServer("s1"); err != nil || customer != "a" || got != record || address != "169.254.80.2" {
		t.Errorf("database server = %q %+v %q %v", customer, got, address, err)
	}
	if err := d.State.DeleteDatabaseServer("s1"); err != nil {
		t.Fatal(err)
	}
	if customer, _, _, _ := d.State.DatabaseServer("s1"); customer != "" {
		t.Errorf("the state has a removed database server of %q", customer)
	}
	if e := use("e", "w6"); e != "169.254.80.2" {
		t.Errorf("the free address is not used again: %s", e)
	}
	// A website uses the database servers of one customer only.
	if _, err := d.State.UseDatabases("b", "w4"); err == nil {
		t.Error("a website got the database servers of a second customer")
	}
	// With no website, the customer gets a record and an address only.
	if d5 := use("d", ""); d5 != "169.254.80.5" {
		t.Errorf("the address of a customer with no website = %s", d5)
	}
	if instances, _ := d.State.DatabaseInstances(); len(instances["d"].Websites) != 0 {
		t.Errorf("record = %+v", instances["d"])
	}
	if key, address, _ := d.State.WebsiteDatabases("w4"); key != "c" || address != "169.254.80.4" {
		t.Errorf("database servers of the website = %q %q", key, address)
	}
	// A database server has one customer, one engine and one port.
	if _, err := d.State.PutDatabaseServer("b", "s2", record, false); err != nil {
		t.Fatal(err)
	}
	for name, put := range map[string]func() (bool, error){
		"a different customer": func() (bool, error) { return d.State.PutDatabaseServer("c", "s2", record, true) },
		"a different engine": func() (bool, error) {
			return d.State.PutDatabaseServer("b", "s2", DatabaseServer{Engine: "postgresql", Port: 3306}, true)
		},
		"a different port": func() (bool, error) {
			return d.State.PutDatabaseServer("b", "s2", DatabaseServer{Engine: "mariadb", Port: 3307}, true)
		},
		"a port in use": func() (bool, error) { return d.State.PutDatabaseServer("b", "s3", record, false) },
	} {
		if _, err := put(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// The limits change only with an update.
	larger := DatabaseServer{Engine: "mariadb", Port: 3306, MemoryBytes: 1 << 30}
	if changed, err := d.State.PutDatabaseServer("b", "s2", larger, false); err != nil || changed {
		t.Errorf("new limits with no update: %v %v", changed, err)
	}
	if changed, err := d.State.PutDatabaseServer("b", "s2", larger, true); err != nil || !changed {
		t.Errorf("new limits with an update: %v %v", changed, err)
	}
	if changed, err := d.State.PutDatabaseServer("b", "s2", larger, true); err != nil || changed {
		t.Errorf("the same limits with an update: %v %v", changed, err)
	}
	for n, want := range map[int]string{1: "169.254.80.2", 254: "169.254.80.255", 255: "169.254.81.0", 4093: "169.254.95.254"} {
		if got, err := quadlet.DatabaseAddress(n); err != nil || got != want {
			t.Errorf("address %d = %q %v, want %q", n, got, err, want)
		}
	}
	for _, n := range []int{0, -1, 4094} {
		if _, err := quadlet.DatabaseAddress(n); err == nil {
			t.Errorf("address %d is valid", n)
		}
	}
	if _, err := instanceKey("../etc"); err == nil {
		t.Error("a path is a valid database instance")
	}
	if key, err := instanceKey(customerID); err != nil || key != customerKey {
		t.Errorf("key = %q %v", key, err)
	}
	if key, err := serverKey(mariaServerID); err != nil || key != mariaServerKey {
		t.Errorf("key of a database server = %q %v", key, err)
	}
}

// A database server from the panel needs an ID of the panel, a customer, an engine, a port of
// the engine and limits that a database server can start with.
func TestCheckDatabaseServer(t *testing.T) {
	if err := checkDatabaseServer(nil); err != nil {
		t.Errorf("no database server: %v", err)
	}
	for _, engine := range Engines {
		if err := checkDatabaseServer(databaseServer(engine)); err != nil {
			t.Errorf("%s: %v", engine, err)
		}
	}
	last := secondServer()
	last.Port = 3355
	none := databaseServer("valkey")
	none.Limits = nil
	for name, s := range map[string]*agentv1.DatabaseServer{"the last port": last, "no limits": none} {
		if err := checkDatabaseServer(s); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, change := range map[string]func(*agentv1.DatabaseServer){
		"a path as ID":               func(s *agentv1.DatabaseServer) { s.Id = "../../etc" },
		"no customer":                func(s *agentv1.DatabaseServer) { s.Customer = "" },
		"a path as customer":         func(s *agentv1.DatabaseServer) { s.Customer = "../../etc" },
		"an unknown engine":          func(s *agentv1.DatabaseServer) { s.Engine = "redis" },
		"no port":                    func(s *agentv1.DatabaseServer) { s.Port = 0 },
		"the port of another engine": func(s *agentv1.DatabaseServer) { s.Port = 5432 },
		"a port after the last one":  func(s *agentv1.DatabaseServer) { s.Port = 3356 },
		"a port of the server":       func(s *agentv1.DatabaseServer) { s.Port = 22 },
		"too little memory":          func(s *agentv1.DatabaseServer) { s.Limits.MemoryBytes = 64 << 20 },
		"too little disk space":      func(s *agentv1.DatabaseServer) { s.Limits.DiskBytes = 100 << 20 },
		"a limit less than 0":        func(s *agentv1.DatabaseServer) { s.Limits.Vcpus = -1 },
	} {
		s := databaseServer("mariadb")
		change(s)
		if err := checkDatabaseServer(s); err == nil {
			t.Errorf("%s: the agent accepted the database server", name)
		}
	}
	if err := checkWebsiteServer(databaseServer("postgresql"), "mariadb"); err == nil {
		t.Error("a MariaDB database in a PostgreSQL database server")
	}
}

// A website from an earlier release keeps the shared containers until its databases move.
// A change of its runtime does not move it, and it cannot have a Valkey database or a
// database in a database server.
func TestAWebsiteOfAnEarlierReleaseKeepsTheSharedContainers(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	newFakeValkeyFor(d)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website(runtimeImage)})); err != nil {
		t.Fatal(err)
	}
	// A newer panel sends the customer with each request for the website.
	if err := run(t)(d.SetRuntime(&agentv1.WebsiteRequest{JobId: jobID2, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", nil))); err != nil {
		t.Fatal(err)
	}
	if !f.Called("systemctl --user --machine=cf-db@ restart cf-mariadb.service") || !strings.Contains(strings.Join(f.Inputs, ""), "CREATE USER 'acme_x7k2_shop'@'169.254.77.1'") {
		t.Errorf("the database is not in the shared container: %v", f.Calls)
	}
	if u := unitFile(t, d, DBUser, "cf-mariadb.container"); !strings.Contains(u, "Volume="+filepath.Join(d.DBHome, "mariadb")+":/var/lib/mysql:Z\n") || !strings.Contains(u, "PublishPort=169.254.77.1:3306:3306\n") || strings.Contains(u, "MemoryMax") {
		t.Errorf("the unit of the shared container:\n%s", u)
	}
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2-b.container"); !strings.Contains(u, "AddHost=mariadb.internal:169.254.77.1\n") {
		t.Errorf("the website does not use the service address:\n%s", u)
	}
	cache := valkeyRequest("acme_x7k2_cache")
	cache.DatabaseServer = nil
	if err := run(t)(d.CreateDatabase(cache)); !errors.Is(err, errNoSharedValkey) {
		t.Errorf("Valkey for a website of an earlier release: %v", err)
	}
	// The databases of the website move first. Then it can have a database in a database server.
	for engine, req := range map[string]*agentv1.CreateDatabaseRequest{
		"mariadb": mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_blog", databaseServer("mariadb")),
		"valkey":  valkeyRequest("acme_x7k2_cache"),
	} {
		if err := run(t)(d.CreateDatabase(req)); err == nil || !strings.Contains(err.Error(), "move the databases of the website first") {
			t.Errorf("a %s database server for a website of an earlier release: %v", engine, err)
		}
	}
	if instances, _ := d.State.DatabaseInstances(); len(instances) != 0 {
		t.Errorf("the website got database servers of a customer: %+v", instances)
	}
	// A website of this release has no shared containers: a request with no database server
	// is not correct.
	d, f, _ = deps(t)
	users(d)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", nil))); err == nil || !strings.Contains(err.Error(), "the request has no database server") {
		t.Errorf("a request with no database server: %v", err)
	}
	if f.Called("systemctl --user --machine=cf-db@ restart cf-mariadb.service") {
		t.Errorf("the agent started the shared container: %v", f.Calls)
	}
}

// newFakeValkeyFor sets the fake Valkey servers with no website and no database server of
// the tests in the state.
func newFakeValkeyFor(d *Deps) {
	newFakeValkey(d)
	if err := d.State.DeleteDatabaseServer(valkeyServerKey); err != nil {
		panic(err)
	}
	if err := d.State.ReleaseDatabases(customerKey, websiteID); err != nil {
		panic(err)
	}
}

// The agent adds the rules of the database servers to the firewall file of an earlier
// release, and nftables accepts the result.
func TestAddDatabaseRules(t *testing.T) {
	b, err := os.ReadFile("testdata/firewall-0.1.9.nft")
	if err != nil {
		t.Fatal(err)
	}
	old := string(b)
	dir := t.TempDir()
	next, err := addDatabaseRules(old, dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(next, "\n")
	line := func(text string) int {
		for i, l := range lines {
			if strings.TrimSpace(l) == text {
				return i
			}
		}
		t.Errorf("no line %q in:\n%s", text, next)
		return -1
	}
	set := line("set database_access {")
	only := line("ct state new ip daddr 169.254.80.0/20 meta skuid != 0 meta skuid != 20000-59999 jump refuse")
	jump := line("ct state new meta skuid 20000-59999 jump websites")
	chain := line("chain websites {")
	accept := line("meta skuid . ip daddr . tcp dport @database_access accept")
	local := line("fib daddr type { local, broadcast, multicast } jump refuse")
	include := line(`include "` + dir + `/*.nft"`)
	if !(line("table inet cloudfactory {") < set && set < only && only < jump && jump < chain && chain < accept && accept < local && local < include) {
		t.Errorf("the order of the rules is wrong:\n%s", next)
	}
	if line("typeof meta skuid . ip daddr . tcp dport") != set+1 {
		t.Errorf("the set has no type:\n%s", next)
	}
	// Each line of the old file stays, in its order.
	i := 0
	for _, l := range strings.Split(strings.TrimRight(old, "\n"), "\n") {
		for i < len(lines) && lines[i] != l {
			i++
		}
		if i == len(lines) {
			t.Fatalf("the line %q of the old file is gone", l)
		}
	}
	if again, err := addDatabaseRules(next, dir); err != nil || again != next {
		t.Errorf("a file of this release changed: %v", err)
	}
	for name, content := range map[string]string{
		"a file of an administrator": "table inet filter {\n}\n",
		"no chain of the websites":   strings.Replace(old, "chain websites {", "chain sites {", 1),
		"no jump to the websites":    strings.Replace(old, "jump websites", "jump sites", 1),
	} {
		if _, err := addDatabaseRules(content, dir); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// nftables checks the result, when the test runs as root on a system with nft.
	if _, err := exec.LookPath("nft"); err != nil || exec.Command("nft", "-c", "add table inet cloudfactory_test").Run() != nil {
		t.Log("nft cannot run here, so nftables did not check the rules")
		return
	}
	// The file is not in the directory of the include line.
	file := filepath.Join(t.TempDir(), "main.nft")
	os.WriteFile(file, []byte(next), 0o644)
	os.WriteFile(filepath.Join(dir, databaseAccessFile), []byte("flush set inet cloudfactory database_access\nadd element inet cloudfactory database_access { 20001 . 169.254.80.2 . 3306, 20001 . 169.254.80.2 . 3307 }\n"), 0o644)
	if out, err := exec.Command("nft", "-c", "-f", file).CombinedOutput(); err != nil {
		t.Errorf("nft refuses the rules: %v\n%s", err, out)
	}
}

// On a server from an earlier release, the agent adds the rules and the database network when
// the first website can use the database servers of its customer.
func TestTheFirstCustomerPreparesAServerOfAnEarlierRelease(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	old, _ := os.ReadFile("testdata/firewall-0.1.9.nft")
	os.WriteFile(d.firewallFile(), old, 0o644)
	os.WriteFile(d.addressUnit(), []byte("[Service]\nType=oneshot\nExecStart=/usr/sbin/ip address replace 169.254.77.1/32 dev lo\n"), 0o644)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(d.firewallFile())
	if !strings.Contains(string(b), "set database_access {") || !strings.Contains(string(b), `include "`+d.firewallDir()+`/*.nft"`) {
		t.Errorf("firewall file:\n%s", b)
	}
	// nftables checks the new file before it replaces the file of the installer.
	check, load, access := -1, -1, -1
	for i, c := range f.Calls {
		switch c {
		case "nft -c -f " + d.firewallFile() + ".new":
			check = i
		case "nft -f " + d.firewallFile():
			load = i
		case "nft -f " + filepath.Join(d.firewallDir(), databaseAccessFile):
			access = i
		}
	}
	if check < 0 || !(check < load && load < access) {
		t.Errorf("check = %d, load = %d, access = %d in %v", check, load, access, f.Calls)
	}
	dropIn, err := os.ReadFile(filepath.Join(d.addressUnit()+".d", "database-network.conf"))
	if err != nil || !strings.Contains(string(dropIn), "[Service]\nExecStart=/usr/sbin/ip address replace 169.254.80.1/20 dev lo\n") {
		t.Errorf("drop-in of the address unit: %q %v", dropIn, err)
	}
	if !f.Called("systemctl daemon-reload") || !f.Called("ip address replace 169.254.80.1/20 dev lo") {
		t.Errorf("calls = %v", f.Calls)
	}
	// A server of an earlier release gets the stop time of the user services.
	stop := filepath.Join(filepath.Dir(d.addressUnit()), "user@.service.d", "zz-cloudfactory-stop.conf")
	if b, err := os.ReadFile(stop); err != nil || !strings.HasSuffix(string(b), "[Service]\nTimeoutStopSec=120\n") {
		t.Errorf("stop time of the user services: %q %v", b, err)
	}
	// A unit of this release has the line, so it gets no drop-in file. The agent does not
	// change the stop time file of the installer.
	d, f, _ = deps(t)
	users(d)
	stop = filepath.Join(filepath.Dir(d.addressUnit()), "user@.service.d", "zz-cloudfactory-stop.conf")
	os.MkdirAll(filepath.Dir(stop), 0o755)
	os.WriteFile(stop, []byte("[Service]\nTimeoutStopSec=120\n"), 0o644)
	os.WriteFile(d.addressUnit(), []byte("[Service]\nExecStart=/usr/sbin/ip address replace 169.254.77.1/32 dev lo\nExecStart=/usr/sbin/ip address replace 169.254.80.1/20 dev lo\n"), 0o644)
	if err := d.syncDatabaseAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.addressUnit() + ".d"); !os.IsNotExist(err) {
		t.Errorf("a unit of this release got a drop-in file: %v", err)
	}
	if b, _ := os.ReadFile(stop); string(b) != "[Service]\nTimeoutStopSec=120\n" || f.Called("systemctl daemon-reload") {
		t.Errorf("the agent changed the stop time file of the installer: %q, calls = %v", b, f.Calls)
	}
	// A firewall file that nftables refuses stays as it is, and the job stops.
	d, f, _ = deps(t)
	users(d)
	os.WriteFile(d.firewallFile(), old, 0o644)
	f.Rules = []execx.Rule{{Prefix: "nft -c -f", Err: errors.New("syntax error")}}
	err = run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()}))
	if b, _ := os.ReadFile(d.firewallFile()); err == nil || !strings.Contains(err.Error(), "run the installer again") || string(b) != string(old) {
		t.Errorf("err = %v, file changed = %v", err, string(b) != string(old))
	}
	// A firewall file that is not from the installer stops the job too.
	d, _, _ = deps(t)
	users(d)
	os.WriteFile(d.firewallFile(), []byte("table inet filter {\n}\n"), 0o644)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err == nil || !strings.Contains(err.Error(), "run the installer again") {
		t.Errorf("err = %v", err)
	}
}

// A database instance that is not an ID of the panel stops the request.
func TestCreateWebsiteChecksTheDatabaseInstance(t *testing.T) {
	d, _, _ := deps(t)
	w := website(runtimeImage)
	w.DatabaseInstance = "../../etc"
	if _, err := d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w}); err == nil {
		t.Error("the agent accepted the database instance")
	}
}

// The entry point of a database image first starts a temporary server that answers on the
// socket only, and then stops it. The agent waits for the TCP port, which only the real
// server has, before it sends SQL.
func TestTheWaitForADatabaseServerNeedsItsTcpPort(t *testing.T) {
	// The test host gives each user the UID 20001.
	const podman = "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman "
	keys := map[string]string{"mariadb": mariaServerKey, "postgresql": pgServerKey}
	probes := map[string]string{
		"mariadb":    podman + "exec -i cf-mariadb-" + mariaServerKey + " mariadb-admin --protocol=tcp --host=127.0.0.1 --port=3306 --connect-timeout=5 ping",
		"postgresql": podman + "exec -i cf-postgresql-" + pgServerKey + " pg_isready --quiet --host=127.0.0.1 --port=5432 --timeout=5",
	}
	sql := map[string]string{
		"mariadb":    podman + "exec -i cf-mariadb-" + mariaServerKey + " mariadb --user=root",
		"postgresql": podman + "exec -i cf-postgresql-" + pgServerKey + " psql",
	}
	for engine, probe := range probes {
		d, f, _ := deps(t)
		d.ReadyTimeout = time.Nanosecond
		s := dbServer{ID: keys[engine], Engine: engine}

		// The temporary server: SQL on the socket has an answer, but the TCP port has none.
		f.Rules = []execx.Rule{{Prefix: probe, Err: errors.New("no response")}}
		if err := d.waitDatabase(context.Background(), s); err == nil || !strings.Contains(err.Error(), "did not answer") {
			t.Errorf("%s: the wait did not fail without the TCP port: %v", engine, err)
		}
		if f.Called(sql[engine]) {
			t.Errorf("%s: the agent sent SQL before the TCP port answered: %v", engine, f.Calls)
		}

		// The real server.
		f.Rules, f.Calls = nil, nil
		if err := d.waitDatabase(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		if p, q := index(f.Calls, probe), index(f.Calls, sql[engine]); p < 0 || q < p {
			t.Errorf("%s: the TCP check is not before the SQL: %v", engine, f.Calls)
		}
	}
}

// A backup reads each database from its database server, and a restore loads a database into
// its database server.
func TestBackupAndRestoreUseTheDatabaseServerOfADatabase(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_blog", secondServer()))); err != nil {
		t.Fatal(err)
	}
	dump := []byte("CREATE TABLE t (id int);\n")
	sum := sha256.Sum256(dump)
	staging := filepath.Join(d.DataDir, "restore", jobID)
	file := filepath.Join(staging, d.DataDir, "dumps", "acme_x7k2", "mariadb-acme_x7k2_blog.dump")
	f.Rules = []execx.Rule{
		{Prefix: "restic --repo s3:b/c --password-file  --json --retry-lock 30m backup", Output: `{"message_type":"summary","snapshot_id":"1a2b3c4d","total_bytes_processed":2048}`},
		{Prefix: "restic --repo s3:b/c --password-file  --json --retry-lock 30m restore", Do: func() error { return writeFile(file, dump) }},
	}
	f.Calls, f.Inputs = nil, nil
	backup := &agentv1.BackupRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Kind: "manual", Target: "s3:b/c", Databases: []*agentv1.DatabaseRef{
		{Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: mariaServerID},
		{Engine: "mariadb", Name: "acme_x7k2_blog", DatabaseServer: secondServerID},
	}}
	if err := run(t)(d.Backup(backup)); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"acme_x7k2_shop": mariaServerKey, "acme_x7k2_blog": secondServerKey} {
		found := false
		for _, c := range f.Calls {
			found = found || strings.HasPrefix(c, podmanDB+"exec cf-mariadb-"+key+" mariadb-dump") && strings.HasSuffix(c, " "+name)
		}
		if !found {
			t.Errorf("no dump of %s from the database server %s: %q", name, key, f.Calls)
		}
	}
	f.Calls, f.Inputs = nil, nil
	restore := &agentv1.RestoreRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", SnapshotId: "1a2b3c4d", Scope: "database", Item: "mariadb:acme_x7k2_blog", Target: "s3:b/c", DumpSha256: hex.EncodeToString(sum[:]), DatabaseServer: secondServerID}
	if err := run(t)(d.Restore(restore)); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "cf-mariadb-"+mariaServerKey) {
			t.Errorf("the restore used a different database server: %s", c)
		}
	}
	if in := inputsOf(f, podmanDB+"exec -i cf-mariadb-"+secondServerKey+" "); !strings.Contains(in, "GRANT ALL PRIVILEGES ON `acme\\_x7k2\\_blog`.* TO 'cfrestore_") {
		t.Errorf("the database server of the database got no restore:\n%s", in)
	}
	// A database server that is not an ID of the panel stops each request.
	backup.Databases[0].DatabaseServer = "../../etc"
	if _, err := d.Backup(backup); err == nil {
		t.Error("the agent accepted the database server of a backup")
	}
	restore.DatabaseServer = "../../etc"
	if _, err := d.Restore(restore); err == nil {
		t.Error("the agent accepted the database server of a restore")
	}
	if _, err := d.DeleteDatabase(&agentv1.DeleteDatabaseRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: "../../etc"}); err == nil {
		t.Error("the agent accepted the database server of a database to remove")
	}
	if _, err := d.DeleteDatabaseUser(&agentv1.DeleteDatabaseUserRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: "../../etc"}); err == nil {
		t.Error("the agent accepted the database server of a user to remove")
	}
}
