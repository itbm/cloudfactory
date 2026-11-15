package mesh

import (
	"os"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const (
	memberID  = "01HZX3K9PQ7R5T8V2W4Y6Z0DB1"
	websiteID = "01HZX3K9PQ7R5T8V2W4Y6Z0WS1"
)

func network() Network {
	return Network{
		Members:  []Member{{ID: memberID, Address: "10.207.1.10", Port: 3306, Servers: []string{"10.207.0.2"}}},
		Websites: []Website{{ID: websiteID, Names: []Name{{Name: "shop-db.internal", Address: "10.207.1.10", Port: 3306}, {Name: "cache.internal", Address: "10.207.1.11", Port: 6379}}}},
	}
}

func rules() Rules {
	return Rules{
		Members: []MemberRule{
			{Address: "10.207.1.10", Port: 3306, Local: "169.254.80.5", LocalPort: 3307, Servers: []string{"10.207.0.2", "10.207.0.3"}},
			// A website: its container is on the private service address.
			{Address: "10.207.1.12", Port: 80, Local: quadlet.ServiceAddress, LocalPort: 20005, Servers: []string{"10.207.0.2"}},
		},
		Access: []AccessRule{{UID: 20001, Address: "10.207.1.10", Port: 3306}, {UID: 20002, Address: "10.207.1.11", Port: 6379}, {UID: 20002, Address: "10.207.1.12", Port: 80}},
	}
}

var meshServers = []string{"10.207.0.1", "10.207.0.2", "10.207.0.3"}

func TestNetworkCheck(t *testing.T) {
	if err := network().Check(); err != nil {
		t.Fatal(err)
	}
	if err := (Network{}).Check(); err != nil || !(Network{}).Empty() {
		t.Errorf("no networks: %v", err)
	}
	for name, change := range map[string]func(*Network){
		"member ID":             func(n *Network) { n.Members[0].ID = "../x" },
		"public member address": func(n *Network) { n.Members[0].Address = "8.8.8.8" },
		"IPv6 member address":   func(n *Network) { n.Members[0].Address = "fd00::1" },
		"port of SSH":           func(n *Network) { n.Members[0].Port = 22 },
		"member two times":      func(n *Network) { n.Members = append(n.Members, n.Members[0]) },
		"public server":         func(n *Network) { n.Members[0].Servers = []string{"203.0.113.9"} },
		"website ID":            func(n *Network) { n.Websites[0].ID = "a b" },
		"website two times":     func(n *Network) { n.Websites = append(n.Websites, n.Websites[0]) },
		"name with a dot":       func(n *Network) { n.Websites[0].Names[0].Name = "shop.db.internal" },
		"name with a space":     func(n *Network) { n.Websites[0].Names[0].Name = "shop db.internal" },
		"name with a line":      func(n *Network) { n.Websites[0].Names[0].Name = "a.internal\n1.2.3.4 b.internal" },
		"name of an engine":     func(n *Network) { n.Websites[0].Names[0].Name = "mariadb.internal" },
		"name of a real domain": func(n *Network) { n.Websites[0].Names[0].Name = "example.com" },
		"name two times":        func(n *Network) { n.Websites[0].Names[1].Name = "shop-db.internal" },
		"public name address":   func(n *Network) { n.Websites[0].Names[0].Address = "1.1.1.1" },
		"port of a name":        func(n *Network) { n.Websites[0].Names[0].Port = 7443 },
	} {
		n := network()
		change(&n)
		if err := n.Check(); err == nil {
			t.Errorf("%s: Check accepts the networks", name)
		}
	}
}

func TestRulesCheck(t *testing.T) {
	if err := rules().Check("10.207.0.0/16", meshServers, 20000, 59999, 20000, 29999); err != nil {
		t.Fatal(err)
	}
	// A server with no networks needs no mesh range.
	if err := (Rules{}).Check("", nil, 20000, 59999, 20000, 29999); err != nil {
		t.Error(err)
	}
	if err := rules().Check("", nil, 20000, 59999, 20000, 29999); err == nil {
		t.Error("Check accepts rules on a server with no mesh range")
	}
	for name, change := range map[string]func(*Rules){
		"member outside the range":        func(r *Rules) { r.Members[0].Address = "10.208.1.10" },
		"member with a server address":    func(r *Rules) { r.Members[0].Address = "10.207.0.2" },
		"member port":                     func(r *Rules) { r.Members[0].Port = 22 },
		"container on the service net":    func(r *Rules) { r.Members[0].Local = "169.254.77.1" },
		"container on a public address":   func(r *Rules) { r.Members[0].Local = "203.0.113.1" },
		"container on the client address": func(r *Rules) { r.Members[0].Local = quadlet.DatabaseClientAddress },
		"container port of the agent":     func(r *Rules) { r.Members[0].LocalPort = 7443 },
		"container port of other engine":  func(r *Rules) { r.Members[0].LocalPort = 5432 },
		"member two times":                func(r *Rules) { r.Members = append(r.Members, r.Members[0]) },
		"server outside the range":        func(r *Rules) { r.Members[0].Servers = []string{"192.168.1.1"} },
		"server with a member address":    func(r *Rules) { r.Members[0].Servers = []string{"10.207.1.10"} },
		"website on the database network": func(r *Rules) { r.Members[1].Local = "169.254.80.5" },
		"website port of an engine":       func(r *Rules) { r.Members[1].LocalPort = 3306 },
		"website port of the agent":       func(r *Rules) { r.Members[1].LocalPort = 7443 },
		"website port of the proxy":       func(r *Rules) { r.Members[1].LocalPort = 8080 },
		"website port above the range":    func(r *Rules) { r.Members[1].LocalPort = 30000 },
		"database on the service address": func(r *Rules) { r.Members[0].Local, r.Members[0].LocalPort = quadlet.ServiceAddress, 20005 },
		"two members on one container": func(r *Rules) {
			r.Members = append(r.Members, MemberRule{Address: "10.207.1.13", Port: 80, Local: quadlet.ServiceAddress, LocalPort: 20005})
		},
		"UID of root":                     func(r *Rules) { r.Access[0].UID = 0 },
		"UID of a database server":        func(r *Rules) { r.Access[0].UID = 60001 },
		"access to a server address":      func(r *Rules) { r.Access[0].Address = "10.207.0.3" },
		"access outside the range":        func(r *Rules) { r.Access[0].Address = "169.254.80.5" },
		"access to the port of the agent": func(r *Rules) { r.Access[0].Port = 7443 },
	} {
		r := rules()
		change(&r)
		if err := r.Check("10.207.0.0/16", meshServers, 20000, 59999, 20000, 29999); err == nil {
			t.Errorf("%s: Check accepts the rules", name)
		}
	}
}

func TestRulesFirewall(t *testing.T) {
	got := rules().Firewall("# header\n")
	for _, want := range []string{
		"# header\n",
		"flush set inet cloudfactory network_access\nadd element inet cloudfactory network_access { 20001 . 10.207.1.10 . 3306, 20002 . 10.207.1.11 . 6379, 20002 . 10.207.1.12 . 80 }\n",
		"flush set inet cloudfactory mesh_access\nadd element inet cloudfactory mesh_access { 10.207.0.2 . 10.207.1.10 . 3306, 10.207.0.2 . 10.207.1.12 . 80, 10.207.0.3 . 10.207.1.10 . 3306 }\n",
		"flush map inet cloudfactory mesh_members\nadd element inet cloudfactory mesh_members { 10.207.1.10 . 3306 : 169.254.80.5 . 3307, 10.207.1.12 . 80 : 169.254.77.1 . 20005 }\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	// With no rules, the file only removes the elements.
	if empty := (Rules{}).Firewall(""); strings.Contains(empty, "add element") || strings.Count(empty, "flush ") != 3 {
		t.Errorf("the file with no rules:\n%s", empty)
	}
}

func TestHostsFile(t *testing.T) {
	got, err := HostsFile("# header\n", "169.254.80.5", []HostLine{{"10.207.1.11", "cache.internal"}, {"10.207.1.10", "shop-db.internal"}}, "10.207.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# header\n127.0.0.1 localhost\n::1 localhost\n", "169.254.80.5 mariadb.internal postgres.internal valkey.internal\n", "10.207.1.10 shop-db.internal\n10.207.1.11 cache.internal\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	// A website with no customer and no network has only the local names.
	if got, err := HostsFile("", "", nil, ""); err != nil || got != "127.0.0.1 localhost\n::1 localhost\n" {
		t.Errorf("hosts file = %q, %v", got, err)
	}
	for name, call := range map[string]func() (string, error){
		"database address outside the network": func() (string, error) { return HostsFile("", "10.0.0.1", nil, "10.207.0.0/16") },
		"name to a public address": func() (string, error) {
			return HostsFile("", "", []HostLine{{"1.1.1.1", "a.internal"}}, "10.207.0.0/16")
		},
		"name to the database network": func() (string, error) {
			return HostsFile("", "", []HostLine{{"169.254.80.5", "a.internal"}}, "10.207.0.0/16")
		},
		"name of a real domain": func() (string, error) {
			return HostsFile("", "", []HostLine{{"10.207.1.10", "bank.example"}}, "10.207.0.0/16")
		},
		"name with a second line": func() (string, error) {
			return HostsFile("", "", []HostLine{{"10.207.1.10", "a.internal\n1.1.1.1 b"}}, "10.207.0.0/16")
		},
		"name with no mesh range": func() (string, error) { return HostsFile("", "", []HostLine{{"10.207.1.10", "a.internal"}}, "") },
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s: HostsFile accepts the value", name)
		}
	}
}

func TestStateWithMembers(t *testing.T) {
	s := state()
	s.Peers[0].Members = []string{"10.207.1.11", "10.207.1.10"}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	// The tunnel sends the traffic for the members of a peer to that peer.
	if config := s.Config(testPrivate); !strings.Contains(config, "AllowedIPs = "+s.Peers[0].Address+"/32, 10.207.1.10/32, 10.207.1.11/32\n") {
		t.Errorf("the configuration:\n%s", config)
	}
	for name, members := range map[string][]string{
		"member outside the range":           {"10.208.0.9"},
		"member with the address of a peer":  {s.Peers[1].Address},
		"member with the address of the own": {s.Address},
		"member two times":                   {"10.207.1.10", "10.207.1.10"},
		"member that is a network":           {"10.207.1.0/24"},
	} {
		bad := state()
		bad.Peers[0].Members = members
		if err := bad.Check(); err == nil {
			t.Errorf("%s: Check accepts the state", name)
		}
	}
	// Two peers cannot have the same member address.
	bad := state()
	bad.Peers[0].Members, bad.Peers[1].Members = []string{"10.207.1.10"}, []string{"10.207.1.10"}
	if err := bad.Check(); err == nil {
		t.Error("Check accepts one member address for two peers")
	}
	// A server with no tunnel can have the range, for its own members.
	alone := State{Version: 3, Range: "10.207.0.0/16"}
	if err := alone.Check(); err != nil || !alone.Off() {
		t.Errorf("a server with no tunnel: %v", err)
	}
	if fw := alone.Firewall(""); !strings.Contains(fw, "add element inet cloudfactory mesh_range { 10.207.0.0/16 }") || strings.Contains(fw, "add element inet cloudfactory mesh_port") {
		t.Errorf("the firewall file of a server with no tunnel:\n%s", fw)
	}
	if err := (State{Range: "8.8.8.0/24"}).Check(); err == nil {
		t.Error("Check accepts a public range for a server with no tunnel")
	}
}

func TestAddNetworkRules(t *testing.T) {
	b, err := os.ReadFile("../actions/testdata/firewall-0.1.9.nft")
	if err != nil {
		t.Fatal(err)
	}
	// A server that has the rules of the mesh from before the networks.
	withMesh, err := addMeshRules(string(b))
	if err != nil {
		t.Fatal(err)
	}
	next, err := AddRules(withMesh)
	if err != nil {
		t.Fatal(err)
	}
	if direct, _ := AddRules(string(b)); direct != next {
		t.Error("the two paths give different files")
	}
	inOrder(t, next, []string{"table inet cloudfactory {"}, networkTableRules)
	inOrder(t, next, []string{"chain mesh_input {"}, networkMeshInputRules, []string{"drop", "}"})
	inOrder(t, next, []string{"chain output {", "type filter hook output priority filter; policy accept;"}, networkOutputFirstRules, []string{"# FR-1703: only root (the agent) and the edge proxy can connect to the website and panel"})
	inOrder(t, next, []string{"chain output {"}, networkOutputRules, outputRules[len(outputRules)-1:])
	inOrder(t, next, []string{"chain prerouting {", "type nat hook prerouting priority dstnat;"}, networkPreroutingRules, []string{"tcp dport 80 redirect to :8080"})
	inOrder(t, next, []string{"chain output_nat {", "type nat hook output priority -100;"}, networkOutputNATRules, []string{"fib daddr type local tcp dport 80 redirect to :8080"})
	inOrder(t, next, []string{"chain postrouting_nat {", "type nat hook postrouting priority 100;"}, networkPostroutingRules)
	inOrder(t, next, lines(withMesh))
	if again, err := AddRules(next); err != nil || again != next {
		t.Errorf("a second call changes the file: %v", err)
	}
	if _, err := addNetworkRules("table inet cloudfactory {\n  set mesh_port {\n  }\n}\n"); err == nil {
		t.Error("addNetworkRules accepts a file with no chains")
	}
}

// The installer of this release writes the same rules of the networks as AddRules.
func TestTheInstallerHasTheNetworkRules(t *testing.T) {
	b, err := os.ReadFile("../../../install/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	// The installer has the address as a variable.
	text := strings.ReplaceAll(string(b), "$DATABASE_CLIENT_ADDRESS", quadlet.DatabaseClientAddress)
	inOrder(t, text, []string{"table inet cloudfactory {"}, networkTableRules)
	inOrder(t, text, []string{"chain mesh_input {"}, networkMeshInputRules, []string{"drop", "}"})
	inOrder(t, text, []string{"chain output {", "type filter hook output priority filter; policy accept;"}, networkOutputFirstRules, []string{"# FR-1703: only root (the agent) and the edge proxy can connect to the website and panel"})
	inOrder(t, text, []string{"chain output {"}, networkOutputRules, outputRules[len(outputRules)-1:])
	inOrder(t, text, []string{"chain prerouting {", "type nat hook prerouting priority dstnat;"}, networkPreroutingRules, []string{"tcp dport 80 redirect to :8080"})
	inOrder(t, text, []string{"chain output_nat {", "type nat hook output priority -100;"}, networkOutputNATRules, []string{"fib daddr type local tcp dport 80 redirect to :8080"})
	inOrder(t, text, []string{"chain postrouting_nat {", "type nat hook postrouting priority 100;"}, networkPostroutingRules)
}
