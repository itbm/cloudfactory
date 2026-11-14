package mesh

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

// A key pair from "wg genkey" and "wg pubkey", and a key from "wg genpsk".
const (
	testPrivate = "aCXXF4PGbVcwBKVtU98fLE5YBpIGmWwVSFnv2N9lLHg="
	testPublic  = "8SFd5kQjgQrl8xOImYUfj5aJzucT/q9wJVtqBywVY2I="
	testShared  = "WFA+DXGA/AXqy58eOAuS2fBQU67RSXe1DUy6jVj7+x4="

	serverB = "01HZX3K9PQ7R5T8V2W4Y6Z0ABC"
	serverC = "01HZX3K9PQ7R5T8V2W4Y6Z0ABD"
)

func state() State {
	other, _ := NewKey()
	otherPublic, _ := PublicKey(other)
	return State{Version: 3, Range: DefaultRange, Address: "10.207.0.1", Port: DefaultPort, Peers: []Peer{
		{Server: serverC, PublicKey: otherPublic, PresharedKey: testShared, Endpoint: "[2001:db8::3]:51820", Address: "10.207.0.3"},
		{Server: serverB, PublicKey: testPublic, PresharedKey: testShared, Endpoint: "203.0.113.2:51820", Address: "10.207.0.2"},
	}}
}

func TestKeys(t *testing.T) {
	public, err := PublicKey(testPrivate)
	if err != nil || public != testPublic {
		t.Fatalf("PublicKey = %q, %v; WireGuard gives %q", public, err, testPublic)
	}
	a, err := NewKey()
	if err != nil || CheckKey(a) != nil {
		t.Fatalf("NewKey = %q, %v", a, err)
	}
	if b, _ := NewKey(); a == b {
		t.Fatal("two new keys are the same")
	}
	for _, bad := range []string{"", "abc", testPublic[:43], testPublic + "=", strings.Repeat("A", 44), "8SFd5kQjgQrl8xOImYUfj5aJzucT/q9wJVtqBywVY2I=\nPostUp = id"} {
		if CheckKey(bad) == nil {
			t.Errorf("CheckKey accepts %q", bad)
		}
	}
}

func TestCheckAcceptsAStateOfThePanel(t *testing.T) {
	if err := state().Check(); err != nil {
		t.Fatal(err)
	}
	if err := (State{Version: 9, Peers: []Peer{}}).Check(); err != nil {
		t.Fatalf("a server that is not in the mesh: %v", err)
	}
}

func TestCheckRefusesAFaultyState(t *testing.T) {
	for name, change := range map[string]func(s *State){
		"a negative version":                     func(s *State) { s.Version = -1 },
		"a public range":                         func(s *State) { s.Range = "8.8.0.0/16" },
		"the range of all private addresses":     func(s *State) { s.Range = "10.0.0.0/8" },
		"a range with host bits":                 func(s *State) { s.Range = "10.207.0.1/16" },
		"an IPv6 range":                          func(s *State) { s.Range = "fd00::/64" },
		"a range with a rule":                    func(s *State) { s.Range = "10.207.0.0/16 } ; add rule" },
		"an address outside the range":           func(s *State) { s.Address = "10.208.0.1" },
		"the network address":                    func(s *State) { s.Address = "10.207.0.0" },
		"the broadcast address":                  func(s *State) { s.Address = "10.207.255.255" },
		"no address with peers":                  func(s *State) { s.Address = "" },
		"a low port":                             func(s *State) { s.Port = 22 },
		"a peer outside the range":               func(s *State) { s.Peers[0].Address = "192.168.1.1" },
		"a peer with the address of this server": func(s *State) { s.Peers[0].Address = s.Address },
		"two peers with one address":             func(s *State) { s.Peers[0].Address = s.Peers[1].Address },
		"two peers with one key":                 func(s *State) { s.Peers[0].PublicKey = s.Peers[1].PublicKey },
		"two peers with one server":              func(s *State) { s.Peers[0].Server = s.Peers[1].Server },
		"a peer with no server":                  func(s *State) { s.Peers[0].Server = "" },
		"a short key":                            func(s *State) { s.Peers[0].PublicKey = "abcd" },
		"no pre-shared key":                      func(s *State) { s.Peers[0].PresharedKey = "" },
		"a key with a second line":               func(s *State) { s.Peers[0].PresharedKey = testShared + "\nPostUp = id" },
		"a host name as the tunnel address":      func(s *State) { s.Peers[0].Endpoint = "db.example:51820" },
		"a private tunnel address":               func(s *State) { s.Peers[0].Endpoint = "192.168.1.5:51820" },
		"a loopback tunnel address":              func(s *State) { s.Peers[0].Endpoint = "127.0.0.1:51820" },
		"a link-local tunnel address":            func(s *State) { s.Peers[0].Endpoint = "169.254.77.1:51820" },
		"a tunnel address with a zone":           func(s *State) { s.Peers[0].Endpoint = "[fe80::1%eth0]:51820" },
		"a tunnel address with no port":          func(s *State) { s.Peers[0].Endpoint = "203.0.113.2" },
		"a tunnel address with a low port":       func(s *State) { s.Peers[0].Endpoint = "203.0.113.2:22" },
		"too many peers":                         func(s *State) { s.Peers = make([]Peer, MaxPeers+1) },
	} {
		s := state()
		change(&s)
		if err := s.Check(); err == nil {
			t.Errorf("Check accepts %s", name)
		}
	}
}

func TestConfig(t *testing.T) {
	s := state()
	got := s.Config(testPrivate)
	want := "[Interface]\nPrivateKey = " + testPrivate + "\nListenPort = 51820\n" +
		"\n# Server " + serverB + "\n[Peer]\nPublicKey = " + testPublic + "\nPresharedKey = " + testShared + "\nEndpoint = 203.0.113.2:51820\nAllowedIPs = 10.207.0.2/32\n" +
		"\n# Server " + serverC + "\n[Peer]\nPublicKey = " + s.Peers[0].PublicKey + "\nPresharedKey = " + testShared + "\nEndpoint = [2001:db8::3]:51820\nAllowedIPs = 10.207.0.3/32\n"
	if got != want {
		t.Errorf("Config:\n%s\nwant:\n%s", got, want)
	}
}

func TestFirewall(t *testing.T) {
	got := state().Firewall("# header\n")
	for _, line := range []string{
		"# header",
		"flush set inet cloudfactory mesh_port",
		"add element inet cloudfactory mesh_port { 51820 }",
		"flush set inet cloudfactory mesh_range",
		"add element inet cloudfactory mesh_range { 10.207.0.0/16 }",
		"flush set inet cloudfactory mesh_endpoints4",
		"add element inet cloudfactory mesh_endpoints4 { 203.0.113.2 }",
		"flush set inet cloudfactory mesh_endpoints6",
		"add element inet cloudfactory mesh_endpoints6 { 2001:db8::3 }",
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("no line %q in:\n%s", line, got)
		}
	}
	off := State{Version: 4}.Firewall("")
	if strings.Contains(off, "add element") || strings.Count(off, "flush set") != 4 {
		t.Errorf("a server that is not in the mesh has elements:\n%s", off)
	}
}

func TestHandshakes(t *testing.T) {
	got := Handshakes(testPublic + "\t1794000000\n" + testShared + "\t0\nnot a key\t5\n\n")
	if len(got) != 2 || got[testPublic] != 1794000000 || got[testShared] != 0 {
		t.Errorf("Handshakes = %v", got)
	}
}

// lines gives the trimmed lines of a text that are not empty.
func lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// inOrder reports whether each rule is a line of the text, in the same sequence.
func inOrder(t *testing.T, text string, rules ...[]string) {
	t.Helper()
	all := lines(text)
	i := 0
	for _, group := range rules {
		for _, rule := range group {
			rule = strings.TrimSpace(rule)
			for i < len(all) && all[i] != rule {
				i++
			}
			if i == len(all) {
				t.Fatalf("no line %q after the rules before it", rule)
			}
		}
	}
}

func TestAddRulesToTheFileOfAnEarlierRelease(t *testing.T) {
	b, err := os.ReadFile("../actions/testdata/firewall-0.1.9.nft")
	if err != nil {
		t.Fatal(err)
	}
	old := string(b)
	next, err := AddRules(old)
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, next, []string{"table inet cloudfactory {"}, tableRules, []string{"chain input {", "iif lo accept"}, inputRules, []string{"chain output {"}, outputRules)
	// Each line of the old file stays, in its sequence.
	inOrder(t, next, lines(old))
	// The rule for the traffic of a tunnel is before the rules of the public ports.
	inOrder(t, next, []string{`iifname "cf-mesh" jump mesh_input`, "tcp dport 22 accept"})
	// A second call changes nothing.
	if again, err := AddRules(next); err != nil || again != next {
		t.Errorf("a second call changes the file: %v", err)
	}
	if _, err := AddRules("table inet other {\n}\n"); err == nil {
		t.Error("AddRules accepts a file that is not from the installer")
	}
}

// The installer of this release writes the same rules as AddRules.
func TestTheInstallerHasTheRules(t *testing.T) {
	b, err := os.ReadFile("../../../install/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	inOrder(t, string(b), []string{"table inet cloudfactory {"}, tableRules, []string{"chain input {", "iif lo accept"}, inputRules, []string{"chain output {"}, outputRules, []string{"ct state new meta skuid $WEBSITE_UID_MIN-$WEBSITE_UID_MAX jump websites"})
	if !strings.Contains(string(b), "wireguard-tools") {
		t.Error("the installer does not install the package wireguard-tools")
	}
}

func TestPing(t *testing.T) {
	local := netip.MustParseAddr("127.0.0.1")
	rtt, err := Ping(context.Background(), local, local, time.Second)
	if err != nil {
		// Only root can open the socket.
		t.Skipf("no echo request: %v", err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Errorf("round-trip time = %v", rtt)
	}
	// An address that does not answer: the function stops at the time limit.
	if _, err := Ping(context.Background(), local, netip.MustParseAddr("127.255.255.254"), 50*time.Millisecond); err == nil {
		t.Log("127.255.255.254 answered")
	}
}
