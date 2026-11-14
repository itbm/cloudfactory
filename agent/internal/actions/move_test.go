package actions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const (
	mariaHash  = "*94BDCEBE19083CE2A1F959FD02F964C7AF4CFC29"
	pgVerifier = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0c2FsdA==$c3RvcmVka2V5c3RvcmVka2V5c3RvcmVka2V5MTI=:c2VydmVya2V5c2VydmVya2V5c2VydmVya2V5MTI="
)

// moveRequest gives the move of one MariaDB database and one PostgreSQL database, each with
// one user, of the website of the tests. The databases go to the MariaDB database server and
// the PostgreSQL database server of the customer of the website.
func moveRequest() *agentv1.MoveDatabasesRequest {
	return &agentv1.MoveDatabasesRequest{
		JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", DatabaseInstance: customerID,
		DatabaseServers: []*agentv1.DatabaseServer{databaseServer("mariadb"), databaseServer("postgresql")},
		Databases: []*agentv1.DatabaseRef{
			{Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: mariaServerID},
			{Engine: "postgresql", Name: "acme_x7k2_data", DatabaseServer: pgServerID},
		},
		Users: []*agentv1.MoveDatabaseUser{
			{Engine: "mariadb", Name: "acme_x7k2_shop", Access: "website", DatabaseServer: mariaServerID, Grants: []*agentv1.DatabaseGrant{{Database: "acme_x7k2_shop", Privileges: "readwrite"}}},
			{Engine: "postgresql", Name: "acme_x7k2_reader", Access: "website", DatabaseServer: pgServerID, Grants: []*agentv1.DatabaseGrant{{Database: "acme_x7k2_data", Privileges: "readonly"}}},
		},
	}
}

// movedServers checks the database servers of the customer after a move that failed: the
// database servers stay, because the panel has them, and the website cannot use them.
func movedServers(t *testing.T, d *Deps) {
	t.Helper()
	for engine, key := range map[string]string{"mariadb": mariaServerKey, "postgresql": pgServerKey} {
		if !d.serverExists(dbServer{ID: key, Engine: engine}) {
			t.Errorf("the %s database server of the customer is gone", engine)
		}
		if !d.serverExists(dbServer{Engine: engine}) {
			t.Errorf("the shared %s container is gone", engine)
		}
	}
	instances, _ := d.State.DatabaseInstances()
	if inst := instances[customerKey]; len(inst.Servers) != 2 || len(inst.Websites) != 0 {
		t.Errorf("record of the customer: %+v", inst)
	}
}

// oldWebsite makes a website of an earlier release with the shared database containers, and
// gives the rules for the password hashes of its database users.
func oldWebsite(t *testing.T, d *Deps, f *execx.Fake) {
	t.Helper()
	users(d)
	newFirewall(t, d)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website(runtimeImage)})); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"mariadb", "postgresql"} {
		if err := d.EnsureDatabaseService(t.Context(), dbServer{Engine: engine}); err != nil {
			t.Fatal(err)
		}
	}
	f.Rules = append(f.Rules,
		execx.Rule{Prefix: podmanDB + "exec -i cf-mariadb ", Input: "SELECT plugin, authentication_string FROM mysql.user WHERE User = 'acme_x7k2_shop'", Output: "mysql_native_password\t" + mariaHash + "\n"},
		execx.Rule{Prefix: podmanDB + "exec -i cf-mariadb ", Input: "SELECT Host FROM mysql.user WHERE User = 'acme_x7k2_shop'", Output: "169.254.77.1\n"},
		execx.Rule{Prefix: podmanDB + "exec -i cf-postgresql ", Input: "SELECT rolpassword FROM pg_authid WHERE rolname = 'acme_x7k2_reader'", Output: pgVerifier + "\n"},
	)
}

// inputsOf gives the stdin of each command that starts with prefix, in one text.
func inputsOf(f *execx.Fake, prefix string) string {
	var b strings.Builder
	for i, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			b.WriteString(f.Inputs[i])
		}
	}
	return b.String()
}

// A move copies the databases and the users of a website into database servers of its
// customer, with the same password hashes, while the website does not operate. Then the
// website uses the database servers and the old copies go.
func TestMoveDatabases(t *testing.T) {
	d, f, _ := deps(t)
	oldWebsite(t, d, f)
	f.Calls, f.Inputs = nil, nil
	if err := run(t)(d.MoveDatabases(moveRequest())); err != nil {
		t.Fatal(err)
	}
	mariaNew, pgNew := podmanDB+"exec -i cf-mariadb-"+mariaServerKey+" ", podmanDB+"exec -i cf-postgresql-"+pgServerKey+" "
	mariaOld, pgOld := podmanDB+"exec -i cf-mariadb ", podmanDB+"exec -i cf-postgresql "

	// The database servers have the limits of the request.
	for engine, key := range map[string]string{"mariadb": mariaServerKey, "postgresql": pgServerKey} {
		if u := unitFile(t, d, DBUser, "cf-"+engine+"-"+key+".container"); !strings.Contains(u, "MemoryMax=1073741824\n") || !strings.Contains(u, "PublishPort="+customerAddress+":") {
			t.Errorf("the unit of the %s database server:\n%s", engine, u)
		}
	}
	// The copy goes from the shared container to the database server of the customer.
	for want, key := range map[string]string{
		podmanDB + "exec cf-mariadb mariadb-dump": mariaServerKey,
		podmanDB + "exec cf-postgresql pg_dump":   pgServerKey,
	} {
		found := false
		for _, c := range f.Calls {
			from, to, pipe := strings.Cut(c, " | ")
			found = found || pipe && strings.HasPrefix(from, want) && strings.Contains(to, "-"+key+" ")
		}
		if !found {
			t.Errorf("no copy %q into the database server %s: %q", want, key, f.Calls)
		}
	}
	// The users have the hashes of their passwords, the address that a database server sees
	// for a website, and their access.
	for _, want := range []string{
		"CREATE USER 'acme_x7k2_shop'@'169.254.80.1' IDENTIFIED VIA mysql_native_password USING '" + mariaHash + "' REQUIRE NONE;\n",
		"GRANT ALL PRIVILEGES ON `acme\\_x7k2\\_shop`.* TO 'acme_x7k2_shop'@'169.254.80.1';\n",
	} {
		if in := inputsOf(f, mariaNew); !strings.Contains(in, want) {
			t.Errorf("the MariaDB database server of the customer got no %q:\n%s", want, in)
		}
	}
	for _, want := range []string{
		"CREATE ROLE \"acme_x7k2_reader\" LOGIN PASSWORD '" + pgVerifier + "';\n",
		"GRANT \"acme_x7k2_data:ro\" TO \"acme_x7k2_reader\";\n",
	} {
		if in := inputsOf(f, pgNew); !strings.Contains(in, want) {
			t.Errorf("the PostgreSQL database server of the customer got no %q:\n%s", want, in)
		}
	}
	// The website stops before the copy and starts after it, with the address of the customer.
	stop := index(f.Calls, "systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2.service")
	dump := index(f.Calls, podmanDB+"exec cf-mariadb mariadb-dump")
	start := index(f.Calls, "systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service")
	if !(stop >= 0 && stop < dump && dump < start) {
		t.Errorf("stop %d, copy %d, start %d: %q", stop, dump, start, f.Calls)
	}
	u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container")
	for _, host := range []string{"mariadb", "postgres", "valkey"} {
		if want := "AddHost=" + host + ".internal:" + customerAddress + "\n"; !strings.Contains(u, want) {
			t.Errorf("the unit of the website has no %q:\n%s", want, u)
		}
	}
	if key, address, err := d.State.WebsiteDatabases(websiteID); key != customerKey || address != customerAddress || err != nil {
		t.Errorf("the website uses %q at %q: %v", key, address, err)
	}
	if got, want := accessFile(t, d), "{ 20001 . "+customerAddress+" . 3306, 20001 . "+customerAddress+" . 5432 }"; !strings.Contains(got, want) {
		t.Errorf("the firewall access has no %q:\n%s", want, got)
	}
	// The old copies go, after the website started.
	for prefix, want := range map[string]string{
		mariaOld: "DROP USER IF EXISTS 'acme_x7k2_shop'@'169.254.77.1';\n",
		pgOld:    "DROP ROLE IF EXISTS \"acme_x7k2_reader\";\n",
	} {
		if in := inputsOf(f, prefix); !strings.Contains(in, want) {
			t.Errorf("%s got no %q:\n%s", prefix, want, in)
		}
	}
	for prefix, name := range map[string]string{mariaOld: "acme_x7k2_shop", pgOld: "acme_x7k2_data"} {
		if in := inputsOf(f, prefix); !strings.Contains(in, "DROP DATABASE") || !strings.Contains(in, name) {
			t.Errorf("%s did not remove the database %s:\n%s", prefix, name, in)
		}
	}
	for i, c := range f.Calls {
		if (strings.HasPrefix(c, mariaOld) || strings.HasPrefix(c, pgOld)) && strings.Contains(f.Inputs[i], "DROP ") && i < start {
			t.Errorf("the agent removed old data before the website started: %s", f.Inputs[i])
		}
	}
	// This was the last website with the shared containers, so they stop. Their data stays.
	for _, engine := range []string{"mariadb", "postgresql"} {
		if !f.Called("systemctl --user --machine=cf-db@ stop cf-" + engine + ".service") {
			t.Errorf("the shared %s container did not stop", engine)
		}
		if d.serverExists(dbServer{Engine: engine}) {
			t.Errorf("the unit of the shared %s container stays", engine)
		}
	}

	// A job that runs again does not copy again: it only removes old copies, and there are none.
	f.Calls, f.Inputs = nil, nil
	if err := run(t)(d.MoveDatabases(moveRequest())); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, " | ") || strings.Contains(c, " stop cf-acme_x7k2.service") || strings.HasPrefix(c, mariaOld) || strings.HasPrefix(c, pgOld) {
			t.Errorf("the second run did: %s", c)
		}
	}
	if in := inputsOf(f, mariaNew) + inputsOf(f, pgNew); strings.Contains(in, "CREATE") || strings.Contains(in, "DROP") {
		t.Errorf("the second run changed the database servers of the customer:\n%s", in)
	}
}

// When the copy fails, the website starts again with the shared containers, the copies in
// the database servers of the customer go, and the shared containers keep the data.
func TestMoveDatabasesFailure(t *testing.T) {
	d, f, _ := deps(t)
	oldWebsite(t, d, f)
	mariaNew := podmanDB + "exec -i cf-mariadb-" + mariaServerKey + " "
	f.Rules = append([]execx.Rule{
		{Prefix: podmanDB + "exec cf-postgresql pg_dump", Err: errors.New("no space left on device")},
		{Prefix: mariaNew, Input: "SELECT Host FROM mysql.user WHERE User = 'acme_x7k2_shop'", Output: "169.254.80.1\n"},
	}, f.Rules...)
	f.Calls, f.Inputs = nil, nil
	err := run(t)(d.MoveDatabases(moveRequest()))
	if err == nil || !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("error: %v", err)
	}
	stop := index(f.Calls, "systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2.service")
	start := index(f.Calls, "systemctl --user --machine=acme_x7k2@ start cf-acme_x7k2.service")
	if !(stop >= 0 && stop < start) {
		t.Errorf("stop %d, start %d: %q", stop, start, f.Calls)
	}
	if key, _, _ := d.State.WebsiteDatabases(websiteID); key != "" {
		t.Errorf("the website uses the database servers of %q", key)
	}
	movedServers(t, d)
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"); !strings.Contains(u, "AddHost=mariadb.internal:169.254.77.1\n") {
		t.Errorf("the website does not use the shared containers:\n%s", u)
	}
	old := inputsOf(f, podmanDB+"exec -i cf-mariadb ") + inputsOf(f, podmanDB+"exec -i cf-postgresql ")
	if strings.Contains(old, "DROP ") {
		t.Errorf("the shared containers lost data:\n%s", old)
	}
	if got := accessFile(t, d); strings.Contains(got, customerAddress) {
		t.Errorf("the website has access to the address of the customer:\n%s", got)
	}
	// No copy stays in the database servers for the next move.
	in := inputsOf(f, mariaNew)
	for _, want := range []string{"DROP USER IF EXISTS 'acme_x7k2_shop'@'169.254.80.1';\n", "DROP DATABASE IF EXISTS `acme_x7k2_shop`"} {
		if !strings.Contains(in, want) {
			t.Errorf("the MariaDB database server of the customer got no %q:\n%s", want, in)
		}
	}
	// The next move uses the same database servers.
	f.Rules = f.Rules[1:]
	if err := run(t)(d.MoveDatabases(moveRequest())); err != nil {
		t.Fatalf("the second move: %v", err)
	}
	if key, _, _ := d.State.WebsiteDatabases(websiteID); key != customerKey {
		t.Errorf("after the second move, the website uses the database servers of %q", key)
	}
}

// A website that answered with the shared containers, and that does not answer with the
// database servers of its customer, gets the shared containers again.
func TestMoveDatabasesGoesBackWhenTheWebsiteDoesNotAnswer(t *testing.T) {
	d, f, _ := deps(t)
	oldWebsite(t, d, f)
	d.ReadyTimeout = time.Nanosecond
	// The website answers only while its unit has the shared containers.
	d.Health = func(context.Context, int, string) (int, error) {
		if strings.Contains(unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"), "AddHost=mariadb.internal:"+customerAddress+"\n") {
			return 503, nil
		}
		return 200, nil
	}
	f.Calls, f.Inputs = nil, nil
	err := run(t)(d.MoveDatabases(moveRequest()))
	if err == nil || !strings.Contains(err.Error(), "it gets the shared containers again") || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("error: %v", err)
	}
	if key, _, _ := d.State.WebsiteDatabases(websiteID); key != "" {
		t.Errorf("the website uses the database servers of %q", key)
	}
	if site, _ := d.State.Get(websiteID); site.Website.GetDatabaseInstance() != "" {
		t.Errorf("the record of the website has the customer %q", site.Website.GetDatabaseInstance())
	}
	movedServers(t, d)
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"); !strings.Contains(u, "AddHost=mariadb.internal:169.254.77.1\n") {
		t.Errorf("the website does not use the shared containers:\n%s", u)
	}
	// The website started two times: with the database servers, and then with the shared containers.
	restarts := 0
	for _, c := range f.Calls {
		if c == "systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service" {
			restarts++
		}
	}
	if restarts != 2 {
		t.Errorf("%d restarts of the website: %q", restarts, f.Calls)
	}
	old := inputsOf(f, podmanDB+"exec -i cf-mariadb ") + inputsOf(f, podmanDB+"exec -i cf-postgresql ")
	if strings.Contains(old, "DROP ") {
		t.Errorf("the shared containers lost data:\n%s", old)
	}
	if got := accessFile(t, d); strings.Contains(got, customerAddress) {
		t.Errorf("the website has access to the address of the customer:\n%s", got)
	}
	// The copies in the database servers go.
	if in := inputsOf(f, podmanDB+"exec -i cf-postgresql-"+pgServerKey+" "); !strings.Contains(in, "DROP ROLE IF EXISTS \"acme_x7k2_reader\";\n") || !strings.Contains(in, "DROP DATABASE") {
		t.Errorf("the PostgreSQL database server of the customer keeps the copies:\n%s", in)
	}
}

// When the website did not answer before the move, the move is not the cause. The move
// completes, so that such a website does not keep the shared containers on the server.
func TestMoveDatabasesOfAWebsiteThatDoesNotAnswer(t *testing.T) {
	d, f, _ := deps(t)
	oldWebsite(t, d, f)
	d.ReadyTimeout = time.Nanosecond
	d.Health = func(context.Context, int, string) (int, error) { return 503, nil }
	if err := run(t)(d.MoveDatabases(moveRequest())); err != nil {
		t.Fatal(err)
	}
	if key, _, _ := d.State.WebsiteDatabases(websiteID); key != customerKey {
		t.Errorf("the website uses the database servers of %q", key)
	}
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2.container"); !strings.Contains(u, "AddHost=mariadb.internal:"+customerAddress+"\n") {
		t.Errorf("the website does not use the database servers of its customer:\n%s", u)
	}
	// The old copies are gone, and the shared containers too: no website uses them.
	if old := inputsOf(f, podmanDB+"exec -i cf-mariadb "); !strings.Contains(old, "DROP DATABASE") {
		t.Errorf("the old copies stay:\n%s", old)
	}
	for _, engine := range []string{"mariadb", "postgresql"} {
		if d.serverExists(dbServer{Engine: engine}) {
			t.Errorf("the shared %s container stays", engine)
		}
	}
}

// When a different website of the customer uses a database server, a move that fails removes
// only its copies from it.
func TestMoveDatabasesFailureKeepsTheOtherDatabases(t *testing.T) {
	d, f, _ := deps(t)
	oldWebsite(t, d, f)
	second := customerWebsite()
	second.Id, second.UnixUser = otherWebsiteID, "acme_s1t2"
	second.Domains = []*agentv1.Domain{{Name: "second.example", Kind: "primary"}}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: second})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(otherWebsiteID, "acme_s1t2", "acme_s1t2_blog", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	mariaNew := podmanDB + "exec -i cf-mariadb-" + mariaServerKey + " "
	f.Rules = append([]execx.Rule{
		{Prefix: podmanDB + "exec cf-postgresql pg_dump", Err: errors.New("no space left on device")},
		{Prefix: mariaNew, Input: "SELECT Host FROM mysql.user WHERE User = 'acme_x7k2_shop'", Output: "169.254.80.1\n"},
	}, f.Rules...)
	f.Calls, f.Inputs = nil, nil
	if err := run(t)(d.MoveDatabases(moveRequest())); err == nil {
		t.Fatal("no error")
	}
	if !d.serverExists(dbServer{ID: mariaServerKey, Engine: "mariadb"}) {
		t.Error("the MariaDB database server of the customer is gone")
	}
	if f.Called("systemctl --user --machine=cf-db@ stop cf-mariadb-" + mariaServerKey + ".service") {
		t.Error("the MariaDB database server of the customer stopped")
	}
	in := inputsOf(f, mariaNew)
	for _, want := range []string{"DROP USER IF EXISTS 'acme_x7k2_shop'@'169.254.80.1';\n", "DROP DATABASE IF EXISTS `acme_x7k2_shop`"} {
		if !strings.Contains(in, want) {
			t.Errorf("the database server of the customer got no %q:\n%s", want, in)
		}
	}
	if strings.Contains(in, "acme_s1t2") {
		t.Errorf("the move changed the database of the second website:\n%s", in)
	}
	instances, _ := d.State.DatabaseInstances()
	if inst := instances[customerKey]; len(inst.Websites) != 1 || inst.Websites[0] != otherWebsiteID || len(inst.Servers) != 2 {
		t.Errorf("record of the customer: %+v", inst)
	}
	// The second website keeps its access to the database servers of the customer.
	if got, want := accessFile(t, d), "{ 20003 . "+customerAddress+" . 3306, 20003 . "+customerAddress+" . 5432 }"; !strings.Contains(got, want) {
		t.Errorf("the firewall access has no %q:\n%s", want, got)
	}
}

func TestMoveDatabasesChecksTheRequest(t *testing.T) {
	d, _, _ := deps(t)
	for name, change := range map[string]func(*agentv1.MoveDatabasesRequest){
		"no customer":                     func(r *agentv1.MoveDatabasesRequest) { r.DatabaseInstance = "" },
		"a customer that is not an ID":    func(r *agentv1.MoveDatabasesRequest) { r.DatabaseInstance = "../x" },
		"a database of another website":   func(r *agentv1.MoveDatabasesRequest) { r.Databases[0].Name = "bolt_k3m9_shop" },
		"a user of another website":       func(r *agentv1.MoveDatabasesRequest) { r.Users[0].Name = "bolt_k3m9_shop" },
		"a Valkey database":               func(r *agentv1.MoveDatabasesRequest) { r.Databases[0].Engine = "valkey" },
		"a grant on an unknown database":  func(r *agentv1.MoveDatabasesRequest) { r.Users[0].Grants[0].Database = "acme_x7k2_other" },
		"privileges that are not known":   func(r *agentv1.MoveDatabasesRequest) { r.Users[0].Grants[0].Privileges = "all" },
		"an access that is not known":     func(r *agentv1.MoveDatabasesRequest) { r.Users[0].Access = "everywhere" },
		"a Unix user that is not correct": func(r *agentv1.MoveDatabasesRequest) { r.UnixUser = "root" },
		"a database with no server":       func(r *agentv1.MoveDatabasesRequest) { r.Databases[0].DatabaseServer = "" },
		"a server that is not there":      func(r *agentv1.MoveDatabasesRequest) { r.Databases[1].DatabaseServer = otherServerID },
		"a server of another engine":      func(r *agentv1.MoveDatabasesRequest) { r.Databases[0].DatabaseServer = pgServerID },
		"a user in a different server":    func(r *agentv1.MoveDatabasesRequest) { r.Users[0].DatabaseServer = pgServerID },
		"a user with no server":           func(r *agentv1.MoveDatabasesRequest) { r.Users[1].DatabaseServer = "" },
		"a server of another customer":    func(r *agentv1.MoveDatabasesRequest) { r.DatabaseServers[0].Customer = otherCustomerID },
		"a Valkey database server": func(r *agentv1.MoveDatabasesRequest) {
			r.DatabaseServers = append(r.DatabaseServers, databaseServer("valkey"))
		},
		"a server with a bad port": func(r *agentv1.MoveDatabasesRequest) { r.DatabaseServers[1].Port = 3306 },
	} {
		req := moveRequest()
		change(req)
		if _, err := d.MoveDatabases(req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A website that the agent does not know.
	if err := run(t)(d.MoveDatabases(moveRequest())); err == nil {
		t.Error("a move for an unknown website: no error")
	}
}
