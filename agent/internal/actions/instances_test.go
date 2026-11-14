package actions

import (
	"context"
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

const runtimeImage = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"

// podmanDB is the start of each Podman command of the user cf-db in these tests.
const podmanDB = "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/990 podman "

// users gives each Unix user of the tests its own UID, as on a server.
func users(d *Deps) {
	ids := map[string]int{DBUser: 990, "acme_x7k2": 20001, "bolt_k3m9": 20002, "acme_s1t2": 20003}
	d.Host.LookupUID = func(name string) (int, error) {
		if id, ok := ids[name]; ok {
			return id, nil
		}
		return 0, user.UnknownUserError(name)
	}
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

func mariaRequest(id, unixUser, name string) *agentv1.CreateDatabaseRequest {
	return &agentv1.CreateDatabaseRequest{JobId: jobID2, WebsiteId: id, UnixUser: unixUser, Engine: "mariadb", Name: name, User: name, Password: "Abcdefghijklmnop1234", Access: "website"}
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

// Each customer has its own database containers: its own container name, address, data
// directory, superuser password and memory limit. A statement for a website goes to the
// container of its customer only.
func TestEachCustomerHasItsOwnDatabaseContainers(t *testing.T) {
	d, f, _ := deps(t)
	users(d)
	newFirewall(t, d)
	d.DatabaseMemoryMB = 1024
	for _, w := range []*agentv1.Website{customerWebsite(), otherWebsite()} {
		if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop"))); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(otherWebsiteID, "bolt_k3m9", "bolt_k3m9_shop"))); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]bool{}
	for key, address := range map[string]string{customerKey: customerAddress, otherCustomerKey: otherAddress} {
		data := filepath.Join(d.DBHome, "customers", key, "mariadb")
		u := unitFile(t, d, DBUser, "cf-mariadb-"+key+".container")
		for _, want := range []string{
			"ContainerName=cf-mariadb-" + key + "\n",
			"PublishPort=" + address + ":3306:3306\n",
			"Volume=" + data + ":/var/lib/mysql:Z\n",
			"EnvironmentFile=" + filepath.Join(d.DBHome, "customers", key, "mariadb.env") + "\n",
			"Label=io.cloudfactory.customer=" + key + "\n",
			"GlobalArgs=--runtime=/usr/local/bin/runsc ",
			"MemoryMax=1073741824\n", "MemorySwapMax=0\n",
		} {
			if !strings.Contains(u, want) {
				t.Errorf("the unit of %s has no %q:\n%s", key, want, u)
			}
		}
		if strings.Contains(u, quadlet.ServiceAddress) {
			t.Errorf("the unit of %s uses the service address:\n%s", key, u)
		}
		secret, err := os.ReadFile(filepath.Join(d.DBHome, "customers", key, "mariadb.env"))
		if err != nil || !strings.Contains(string(secret), "MARIADB_ROOT_PASSWORD=") {
			t.Fatalf("superuser secret of %s: %q %v", key, secret, err)
		}
		secrets[string(secret)] = true
		for _, want := range []string{
			"install -d -o cf-db -g cf-db -m 0700 " + data,
			"systemctl --user --machine=cf-db@ restart cf-mariadb-" + key + ".service",
		} {
			if !f.Called(want) {
				t.Errorf("no call %q", want)
			}
		}
	}
	if len(secrets) != 2 {
		t.Error("the two customers have the same superuser password")
	}
	// Each statement is in the container of the customer of the website. The host part of a
	// user is the address that the containers of the customers see for the websites.
	for i, c := range f.Calls {
		in := f.Inputs[i]
		switch {
		case strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+customerKey+" "):
			if strings.Contains(in, "bolt_k3m9") {
				t.Errorf("a statement of the second customer is in the container of the first: %s", in)
			}
		case strings.HasPrefix(c, podmanDB+"exec -i cf-mariadb-"+otherCustomerKey+" "):
			if strings.Contains(in, "acme_x7k2") {
				t.Errorf("a statement of the first customer is in the container of the second: %s", in)
			}
		case strings.Contains(c, "exec -i cf-mariadb "), strings.Contains(c, "cf-mariadb.service"):
			t.Errorf("the agent used the shared container: %s", c)
		}
	}
	in := strings.Join(f.Inputs, "")
	for _, want := range []string{
		"CREATE USER 'acme_x7k2_shop'@'169.254.80.1' IDENTIFIED BY 'Abcdefghijklmnop1234' REQUIRE NONE;",
		"GRANT ALL PRIVILEGES ON `bolt\\_k3m9\\_shop`.* TO 'bolt_k3m9_shop'@'169.254.80.1';",
	} {
		if !strings.Contains(in, want) {
			t.Errorf("no statement %q in:\n%s", want, in)
		}
	}
	// The database host names of each website point to the containers of its customer.
	for name, address := range map[string]string{"acme_x7k2": customerAddress, "bolt_k3m9": otherAddress} {
		u := unitFile(t, d, name, "cf-"+name+".container")
		for _, host := range []string{"mariadb.internal", "postgres.internal", "valkey.internal"} {
			if want := "AddHost=" + host + ":" + address + "\n"; !strings.Contains(u, want) {
				t.Errorf("the unit of %s has no %q:\n%s", name, want, u)
			}
		}
	}
	// The firewall lets each website connect to the address of its customer only.
	access := accessFile(t, d)
	want := "flush set inet cloudfactory database_access\nadd element inet cloudfactory database_access { 20001 . 169.254.80.2, 20002 . 169.254.80.3 }\n"
	if !strings.HasSuffix(access, want) || !f.Called("nft -f "+filepath.Join(d.firewallDir(), databaseAccessFile)) {
		t.Errorf("access file:\n%s\ncalls: %v", access, f.Calls)
	}
	if !f.Called("ip address replace 169.254.80.1/20 dev lo") {
		t.Errorf("the database network is not on lo: %v", f.Calls)
	}
	// The report has the databases of all customers.
	f.Rules = []execx.Rule{
		{Prefix: podmanDB + "exec -i cf-mariadb-" + customerKey + " ", Output: "acme_x7k2_shop\t4096\n"},
		{Prefix: podmanDB + "exec -i cf-mariadb-" + otherCustomerKey + " ", Output: "bolt_k3m9_shop\t8192\n"},
	}
	sizes := d.databaseSizes(context.Background())
	if len(sizes) != 2 || sizes[0].Name != "acme_x7k2_shop" || sizes[0].SizeBytes != 4096 || sizes[1].Name != "bolt_k3m9_shop" || sizes[1].SizeBytes != 8192 {
		t.Errorf("sizes = %+v", sizes)
	}
}

// The websites of one customer use the same containers. The containers go with the last
// website of the customer on the server, and the firewall forgets each website that goes.
func TestTheLastWebsiteOfACustomerTakesItsDatabaseContainers(t *testing.T) {
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
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop"))); err != nil {
		t.Fatal(err)
	}
	if u := unitFile(t, d, "acme_s1t2", "cf-acme_s1t2.container"); !strings.Contains(u, "AddHost=mariadb.internal:"+customerAddress+"\n") {
		t.Errorf("the second website of the customer has a different database address:\n%s", u)
	}
	if access := accessFile(t, d); !strings.Contains(access, "{ 20001 . 169.254.80.2, 20003 . 169.254.80.2 }") {
		t.Errorf("access file:\n%s", access)
	}
	data := filepath.Join(d.DBHome, "customers", customerKey)
	os.MkdirAll(filepath.Join(data, "mariadb"), 0o700)
	unit := "cf-mariadb-" + customerKey
	// The first website goes: the second one keeps the containers.
	req := &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2",
		Databases: []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "acme_x7k2_shop"}}, DatabaseUsers: []*agentv1.DatabaseRef{{Engine: "mariadb", Name: "acme_x7k2_shop"}}}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.Inputs, ""), "DROP DATABASE IF EXISTS `acme_x7k2_shop`;") {
		t.Error("the database of the website stays")
	}
	if _, err := os.Stat(data); err != nil || f.Called("systemctl --user --machine=cf-db@ stop "+unit+".service") {
		t.Fatalf("the containers of the customer went with the first website: %v", err)
	}
	if access := accessFile(t, d); !strings.Contains(access, "{ 20003 . 169.254.80.2 }") {
		t.Errorf("access file after the first website went:\n%s", access)
	}
	// The last website goes: the containers and their data go too.
	req = &agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: staging.GetId(), UnixUser: "acme_s1t2"}
	if err := run(t)(d.DeleteWebsite(req)); err != nil {
		t.Fatal(err)
	}
	if !f.Called("systemctl --user --machine=cf-db@ stop " + unit + ".service") {
		t.Errorf("the container did not stop: %v", f.Calls)
	}
	dir, _ := d.Host.UnitDir(DBUser)
	if _, err := os.Stat(filepath.Join(dir, unit+".container")); !os.IsNotExist(err) {
		t.Errorf("the unit stays: %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("the data stays: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(d.DBHome, "customers")); len(left) != 0 {
		t.Errorf("files stay in the directory of the customers: %v", left)
	}
	if instances, _ := d.State.DatabaseInstances(); len(instances) != 0 {
		t.Errorf("the state keeps the containers: %+v", instances)
	}
	if access := accessFile(t, d); strings.Contains(access, "add element") {
		t.Errorf("access file with no website:\n%s", access)
	}
	// A later customer gets the address again, and starts with new containers.
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: otherWebsite()})); err != nil {
		t.Fatal(err)
	}
	if address, _ := d.State.DatabaseAddress(otherCustomerKey); address != customerAddress {
		t.Errorf("the address of the next customer = %q", address)
	}
}

// The state gives each customer the smallest free address number.
func TestDatabaseAddressNumbers(t *testing.T) {
	d, _, _ := deps(t)
	use := func(key, id string) string {
		t.Helper()
		address, err := d.State.UseDatabases(key, id, false)
		if err != nil {
			t.Fatal(err)
		}
		return address
	}
	if a, b, again := use("a", "w1"), use("b", "w2"), use("a", "w3"); a != "169.254.80.2" || b != "169.254.80.3" || again != a {
		t.Errorf("addresses = %s %s %s", a, b, again)
	}
	if unused, _ := d.State.ReleaseDatabases("a", "w1"); unused {
		t.Error("the containers are unused while a website uses them")
	}
	if unused, _ := d.State.ReleaseDatabases("a", "w3"); !unused {
		t.Error("the containers are in use with no website")
	}
	if c := use("c", "w4"); c != "169.254.80.2" {
		t.Errorf("the free address is not used again: %s", c)
	}
	// A website uses the containers of one customer only.
	if _, err := d.State.UseDatabases("b", "w4", false); err == nil {
		t.Error("a website got the containers of a second customer")
	}
	// A website that moves keeps the containers, but does not use them.
	if _, err := d.State.UseDatabases("d", "w5", true); err != nil {
		t.Fatal(err)
	}
	if key, _, _ := d.State.WebsiteDatabases("w5"); key != "" {
		t.Errorf("a website that moves uses the containers of %q", key)
	}
	use("d", "w5")
	if key, address, _ := d.State.WebsiteDatabases("w5"); key != "d" || address != "169.254.80.4" {
		t.Errorf("containers of the website = %q %q", key, address)
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
}

// A website from an earlier release keeps the shared containers until its databases move.
// A change of its runtime does not move it, and it cannot have a Valkey database.
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
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop"))); err != nil {
		t.Fatal(err)
	}
	if !f.Called("systemctl --user --machine=cf-db@ restart cf-mariadb.service") || !strings.Contains(strings.Join(f.Inputs, ""), "CREATE USER 'acme_x7k2_shop'@'169.254.77.1'") {
		t.Errorf("the database is not in the shared container: %v", f.Calls)
	}
	if u := unitFile(t, d, "acme_x7k2", "cf-acme_x7k2-b.container"); !strings.Contains(u, "AddHost=mariadb.internal:169.254.77.1\n") {
		t.Errorf("the website does not use the service address:\n%s", u)
	}
	if instances, _ := d.State.DatabaseInstances(); len(instances) != 0 {
		t.Errorf("the website got containers of a customer: %+v", instances)
	}
	if err := run(t)(d.CreateDatabase(valkeyRequest("acme_x7k2_cache"))); !errors.Is(err, errNoSharedValkey) {
		t.Errorf("Valkey for a website of an earlier release: %v", err)
	}
}

// newFakeValkeyFor sets the fake Valkey servers with no website of the tests in the state.
func newFakeValkeyFor(d *Deps) {
	newFakeValkey(d)
	if _, err := d.State.ReleaseDatabases(customerKey, websiteID); err != nil {
		panic(err)
	}
}

// The agent adds the rules of the database containers to the firewall file of an earlier
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
	accept := line("meta skuid . ip daddr @database_access tcp dport { 3306, 5432, 6379 } accept")
	local := line("fib daddr type { local, broadcast, multicast } jump refuse")
	include := line(`include "` + dir + `/*.nft"`)
	if !(line("table inet cloudfactory {") < set && set < only && only < jump && jump < chain && chain < accept && accept < local && local < include) {
		t.Errorf("the order of the rules is wrong:\n%s", next)
	}
	if line("typeof meta skuid . ip daddr") != set+1 {
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
	os.WriteFile(filepath.Join(dir, databaseAccessFile), []byte("flush set inet cloudfactory database_access\nadd element inet cloudfactory database_access { 20001 . 169.254.80.2 }\n"), 0o644)
	if out, err := exec.Command("nft", "-c", "-f", file).CombinedOutput(); err != nil {
		t.Errorf("nft refuses the rules: %v\n%s", err, out)
	}
}

// On a server from an earlier release, the agent adds the rules and the database network when
// the first website uses the database containers of its customer.
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
	// A unit of this release has the line, so it gets no drop-in file.
	d, _, _ = deps(t)
	users(d)
	os.WriteFile(d.addressUnit(), []byte("[Service]\nExecStart=/usr/sbin/ip address replace 169.254.77.1/32 dev lo\nExecStart=/usr/sbin/ip address replace 169.254.80.1/20 dev lo\n"), 0o644)
	if err := d.syncDatabaseAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.addressUnit() + ".d"); !os.IsNotExist(err) {
		t.Errorf("a unit of this release got a drop-in file: %v", err)
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
	probes := map[string]string{
		"mariadb":    podman + "exec -i cf-mariadb-" + customerKey + " mariadb-admin --protocol=tcp --host=127.0.0.1 --port=3306 --connect-timeout=5 ping",
		"postgresql": podman + "exec -i cf-postgresql-" + customerKey + " pg_isready --quiet --host=127.0.0.1 --port=5432 --timeout=5",
	}
	sql := map[string]string{
		"mariadb":    podman + "exec -i cf-mariadb-" + customerKey + " mariadb --user=root",
		"postgresql": podman + "exec -i cf-postgresql-" + customerKey + " psql",
	}
	for engine, probe := range probes {
		d, f, _ := deps(t)
		d.ReadyTimeout = time.Nanosecond
		s := dbServer{Instance: customerKey, Engine: engine}

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
