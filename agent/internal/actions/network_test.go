package actions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
)

const (
	// memberAddress is the member address of the MariaDB database server of the tests, and
	// remoteMember is the address of a member on the peer meshPeerB.
	memberAddress = "10.207.1.10"
	remoteMember  = "10.207.1.20"
)

// networkRequest gives the mesh state of the tests with the networks: the MariaDB database
// server of the first customer is a member, a website of the peer meshPeerB can connect to
// it, and the website of the tests can connect to it and to a member on meshPeerB.
func networkRequest(version int64) *agentv1.SetMeshRequest {
	req := meshRequest(version)
	req.Mesh.Peers[0].Members = []string{remoteMember}
	req.Mesh.Members = []*agentv1.NetworkMember{{Id: mariaServerID, Address: memberAddress, Port: 3306, Servers: []string{"10.207.0.2"}}}
	req.Mesh.Websites = []*agentv1.NetworkWebsite{{Id: websiteID, Names: []*agentv1.NetworkName{
		{Name: "shop-db.internal", Address: memberAddress, Port: 3306},
		{Name: "reports.internal", Address: remoteMember, Port: 5432},
	}}}
	return req
}

func networkFile(t *testing.T, d *Deps) string {
	t.Helper()
	// A server with no networks has no file.
	b, err := os.ReadFile(filepath.Join(d.firewallDir(), networkFirewallFile))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetMeshWithNetworks(t *testing.T) {
	d, f := meshServer(t)
	root := rootOf(d)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	customerServers(t, d, "mariadb")
	job, err := d.SetMesh(networkRequest(7))
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	// The firewall: the website (UID 20001) can connect to the two members. The peer can
	// connect to the member of this server. The member address goes to the container.
	fw := networkFile(t, d)
	for _, want := range []string{
		"add element inet cloudfactory network_access { 20001 . 10.207.1.10 . 3306, 20001 . 10.207.1.20 . 5432 }\n",
		"add element inet cloudfactory mesh_access { 10.207.0.2 . 10.207.1.10 . 3306 }\n",
		"add element inet cloudfactory mesh_members { 10.207.1.10 . 3306 : " + customerAddress + " . 3306 }\n",
	} {
		if !strings.Contains(fw, want) {
			t.Errorf("no %q in:\n%s", want, fw)
		}
	}
	if callIndex(f.Calls, "nft -f "+filepath.Join(root.firewallDir(), networkFirewallFile)) < 0 {
		t.Errorf("the agent did not load the firewall file of the networks:\n%s", strings.Join(f.Calls, "\n"))
	}
	// The firewall file of the installer has the rules of the networks.
	if b, _ := os.ReadFile(root.FirewallFile); !strings.Contains(string(b), "set network_access {") || !strings.Contains(string(b), "dnat ip to ip daddr . tcp dport map @mesh_members") {
		t.Errorf("the firewall file has no rules of the networks:\n%s", b)
	}
	// The hosts file of the website has the names, with the names of the engines.
	hosts := websiteHostsFile(t, d, "acme_x7k2")
	if !hasDatabaseNames(hosts, customerAddress) || !strings.Contains(hosts, "10.207.1.10 shop-db.internal\n10.207.1.20 reports.internal\n") {
		t.Errorf("the hosts file:\n%s", hosts)
	}
	// The tunnel to the peer sends the traffic for the member of the peer.
	if conf, _ := os.ReadFile(filepath.Join(root.MeshDir, "cf-mesh.conf")); !strings.Contains(string(conf), "AllowedIPs = 10.207.0.2/32, 10.207.1.20/32\n") {
		t.Errorf("the tunnel configuration:\n%s", conf)
	}

	// A second website of the customer, after the networks: the panel sends it in the next
	// state. Before that, it has the names of the engines and no access to a member.
	second := customerWebsite()
	second.Id, second.UnixUser = otherWebsiteID, "acme_s1t2"
	second.Domains = []*agentv1.Domain{{Name: "staging.acme.example", Kind: "primary"}}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID2, Website: second})); err != nil {
		t.Fatal(err)
	}
	if hosts := websiteHostsFile(t, d, "acme_s1t2"); !hasDatabaseNames(hosts, customerAddress) || strings.Contains(hosts, "shop-db") {
		t.Errorf("the hosts file of the second website:\n%s", hosts)
	}
	if u := unitFile(t, d, "acme_s1t2", "cf-acme_s1t2.container"); !strings.Contains(u, "Volume="+d.hostsPath("acme_s1t2")+":/etc/hosts:ro\n") {
		t.Errorf("the unit of the second website:\n%s", u)
	}
	if networkFile(t, d) != fw {
		t.Errorf("the second website changed the access with no state of the panel:\n%s", networkFile(t, d))
	}

	// The agent removes the first website: its access goes, because a later website can get
	// its UID. The member of this server stays.
	if err := run(t)(d.DeleteWebsite(&agentv1.DeleteWebsiteRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2"})); err != nil {
		t.Fatal(err)
	}
	fw = networkFile(t, d)
	if strings.Contains(fw, "add element inet cloudfactory network_access") || !strings.Contains(fw, "add element inet cloudfactory mesh_members") {
		t.Errorf("the firewall file after the website is gone:\n%s", fw)
	}

	// A state with no networks removes each element.
	job, err = d.SetMesh(meshRequest(8))
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if fw := networkFile(t, d); strings.Contains(fw, "add element") {
		t.Errorf("the firewall file with no networks:\n%s", fw)
	}
	if n, _ := d.State.Network(); !n.Empty() {
		t.Errorf("the state keeps the networks: %+v", n)
	}
}

// The panel can send the networks before the job that makes a database server or a website.
// The agent leaves them out, and adds them when the server has them.
func TestNetworksWaitForTheDatabaseServer(t *testing.T) {
	d, _ := meshServer(t)
	job, err := d.SetMesh(networkRequest(7))
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if fw := networkFile(t, d); strings.Contains(fw, "add element") {
		t.Errorf("the firewall has elements for a member that the server does not have:\n%s", fw)
	}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	if fw := networkFile(t, d); !strings.Contains(fw, "20001 . 10.207.1.10 . 3306") || strings.Contains(fw, "mesh_members {") {
		t.Errorf("the firewall after the website:\n%s", fw)
	}
	if err := run(t)(d.CreateDatabase(mariaRequest(websiteID, "acme_x7k2", "acme_x7k2_shop", databaseServer("mariadb")))); err != nil {
		t.Fatal(err)
	}
	if fw := networkFile(t, d); !strings.Contains(fw, "add element inet cloudfactory mesh_members { 10.207.1.10 . 3306 : "+customerAddress+" . 3306 }") {
		t.Errorf("the firewall after the database server:\n%s", fw)
	}
}

// A server with no tunnel has its own members: a website finds a database server of the
// same server with the same name.
func TestNetworksOnAServerWithNoTunnel(t *testing.T) {
	d, f := meshServer(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	customerServers(t, d, "mariadb")
	req := networkRequest(7)
	req.Mesh.Address, req.Mesh.Port, req.Mesh.Peers = "", 0, nil
	req.Mesh.Members[0].Servers = nil
	req.Mesh.Websites[0].Names = req.Mesh.Websites[0].Names[:1]
	job, err := d.SetMesh(req)
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if i := callIndex(f.Calls, "ip link add"); i >= 0 {
		t.Errorf("the server made an interface: %s", f.Calls[i])
	}
	if fw := networkFile(t, d); !strings.Contains(fw, "20001 . 10.207.1.10 . 3306") || !strings.Contains(fw, "10.207.1.10 . 3306 : "+customerAddress+" . 3306") {
		t.Errorf("the firewall:\n%s", fw)
	}
	if b, _ := os.ReadFile(filepath.Join(d.firewallDir(), meshFirewallFile)); !strings.Contains(string(b), "add element inet cloudfactory mesh_range { 10.207.0.0/16 }") {
		t.Errorf("the firewall file of the mesh has no range:\n%s", b)
	}
}

// The root helper checks each rule and each name of the worker.
func TestTheHelperRefusesFaultyNetworkRules(t *testing.T) {
	d, _ := meshServer(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: customerWebsite()})); err != nil {
		t.Fatal(err)
	}
	customerServers(t, d, "mariadb")
	job, err := d.SetMesh(networkRequest(7))
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	before := networkFile(t, d)
	hosts := websiteHostsFile(t, d, "acme_x7k2")
	good := func() networkIn {
		in, err := d.networkRules()
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	for name, change := range map[string]func(*networkIn){
		"access for root":                   func(in *networkIn) { in.Rules.Access[0].UID = 0 },
		"access to the address of a peer":   func(in *networkIn) { in.Rules.Access[0].Address = "10.207.0.2" },
		"access to the port of the agent":   func(in *networkIn) { in.Rules.Access[0].Port = 7443 },
		"member to the service address":     func(in *networkIn) { in.Rules.Members[0].Local = "169.254.77.1" },
		"member to the port of SSH":         func(in *networkIn) { in.Rules.Members[0].LocalPort = 22 },
		"member with the own mesh address":  func(in *networkIn) { in.Rules.Members[0].Address = "10.207.0.1" },
		"hosts file of a system user":       func(in *networkIn) { in.Hosts[0].User = "root" },
		"hosts file with a path":            func(in *networkIn) { in.Hosts[0].User = "../../etc" },
		"name to a public address":          func(in *networkIn) { in.Hosts[0].Names[0].Address = "203.0.113.80" },
		"name of a real domain":             func(in *networkIn) { in.Hosts[0].Names[0].Name = "bank.example" },
		"engine names to a mesh address":    func(in *networkIn) { in.Hosts[0].Database = "10.207.1.20" },
		"name with a second line of a file": func(in *networkIn) { in.Hosts[0].Names[0].Name = "a.internal\n203.0.113.80 bank.example" },
	} {
		in := good()
		change(&in)
		if err := d.applyNetwork(t.Context(), in); err == nil {
			t.Errorf("%s: the helper accepts the rules", name)
		}
	}
	if networkFile(t, d) != before || websiteHostsFile(t, d, "acme_x7k2") != hosts {
		t.Error("a refused request changed a file")
	}
}

// A container that has the hosts file as a mount keeps the file after a rename. Thus the
// agent changes the file in place.
func TestWriteInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "hosts")
	if err := writeInPlace(path, "127.0.0.1 localhost\n10.207.1.10 a-long-name-of-a-member.internal\n"); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeInPlace(path, "127.0.0.1 localhost\n"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.Stat(path)
	if !os.SameFile(first, second) {
		t.Error("the file is a new file")
	}
	if b, _ := os.ReadFile(path); string(b) != "127.0.0.1 localhost\n" || second.Mode().Perm() != 0o644 {
		t.Errorf("content = %q, mode = %v", b, second.Mode().Perm())
	}
	// The function does not write through a symbolic link.
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeInPlace(link, "new\n"); err == nil {
		t.Error("writeInPlace follows a symbolic link")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Errorf("the target of the link changed: %q", b)
	}
}

func TestSetMeshRefusesFaultyNetworks(t *testing.T) {
	d, _ := meshServer(t)
	for name, change := range map[string]func(*agentv1.Mesh){
		"member port":          func(m *agentv1.Mesh) { m.Members[0].Port = 22 },
		"name of an engine":    func(m *agentv1.Mesh) { m.Websites[0].Names[0].Name = "mariadb.internal" },
		"member of a peer":     func(m *agentv1.Mesh) { m.Peers[0].Members = []string{"10.207.0.3"} },
		"public member":        func(m *agentv1.Mesh) { m.Peers[0].Members = []string{"198.51.100.7"} },
		"name with an address": func(m *agentv1.Mesh) { m.Websites[0].Names[0].Address = "169.254.169.254" },
	} {
		req := networkRequest(7)
		change(req.Mesh)
		if _, err := d.SetMesh(req); err == nil {
			t.Errorf("%s: SetMesh accepts the request", name)
		}
	}
}
