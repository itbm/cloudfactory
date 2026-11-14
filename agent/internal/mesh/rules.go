package mesh

import (
	"errors"
	"regexp"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// The firewall rules of the mesh. The installer of this release writes them in its file
// (install/install.sh, firewall_rules). A server from an earlier release does not have them:
// the agent adds them with AddRules. A test compares the two.

// tableRules are the sets and the chains of the mesh in the table "inet cloudfactory".
var tableRules = []string{
	"# The mesh: the encrypted tunnels between the servers of one panel (the interface " + Interface + ").",
	"# The agent writes the elements of the four sets in its file mesh.nft: the UDP port of the",
	"# tunnels, the address range of the mesh, and the public addresses of the peers.",
	"set " + SetPort + " {",
	"  typeof udp dport",
	"}",
	"set " + SetRange + " {",
	"  typeof ip daddr",
	"  flags interval",
	"}",
	"set " + SetEndpoints4 + " {",
	"  typeof ip saddr",
	"}",
	"set " + SetEndpoints6 + " {",
	"  typeof ip6 saddr",
	"}",
	"# All traffic that comes out of a tunnel. A peer can measure the round-trip time. All other",
	"# traffic is dropped: also the ports that are open for the internet, such as SSH.",
	"chain mesh_input {",
	"  icmp type echo-request limit rate 50/second accept",
	"  drop",
	"}",
	"# This server never sends the traffic of a tunnel to a different server or network, and",
	"# never sends the traffic of a different network into a tunnel.",
	"chain mesh_forward {",
	"  type filter hook forward priority filter; policy accept;",
	`  iifname "` + Interface + `" drop`,
	`  oifname "` + Interface + `" drop`,
	"}",
}

// inputRules go into the chain "input", after the rule for "lo".
var inputRules = []string{
	"# The mesh: only a peer can send to the tunnel port. The traffic that comes out of a",
	"# tunnel has its own chain, before the rules of the public ports.",
	"udp dport @" + SetPort + " ip saddr @" + SetEndpoints4 + " accept",
	"udp dport @" + SetPort + " ip6 saddr @" + SetEndpoints6 + " accept",
	`iifname "` + Interface + `" jump mesh_input`,
}

// outputRules go into the chain "output", before the jump to the chain "websites".
var outputRules = []string{
	"# The mesh: only root (the agent) can connect to an address of the mesh.",
	"ct state new ip daddr @" + SetRange + " meta skuid != 0 jump refuse",
}

var websitesJumpRE = regexp.MustCompile(`^\s*ct state new meta skuid \d+-\d+ jump websites\s*$`)

func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func add(out []string, indent string, lines []string) []string {
	for _, l := range lines {
		out = append(out, indent+l)
	}
	return out
}

// The rules of the networks of the customers (network.go). They are a second group, because
// a server can have the rules of the mesh from before the networks.

// networkTableRules are the sets, the map and the chain of the networks in the table.
var networkTableRules = []string{
	"# The networks of the customers. A network member is a database server with a name and an",
	"# address of the mesh range. The agent writes the elements in its file network.nft.",
	"# The UID of a website of this server, the address of a member, and the port of the member.",
	"set " + SetNetworkAccess + " {",
	"  typeof meta skuid . ip daddr . tcp dport",
	"}",
	"# The mesh address of a different server, the address of a member of this server, and the",
	"# port of the member.",
	"set " + SetMeshAccess + " {",
	"  typeof ip saddr . ip daddr . tcp dport",
	"}",
	"# The address and the port of a member of this server give the address and the port of",
	"# its container on the database network.",
	"map " + MapMembers + " {",
	"  typeof ip daddr . tcp dport : ip daddr . tcp dport",
	"}",
	"# A database server sees each website container as " + quadlet.DatabaseClientAddress + ". A connection from a",
	"# tunnel to a member gets the same source address, so the users of the databases do not",
	"# change when a website is on a different server.",
	"chain mesh_input_nat {",
	"  type nat hook input priority 100;",
	`  iifname "` + Interface + `" ct status dnat snat ip to ` + quadlet.DatabaseClientAddress,
	"}",
}

// networkMeshInputRules go into the chain "mesh_input", before the rule "drop".
var networkMeshInputRules = []string{
	"# A server that has a website in a network of a member can connect to that member, on the",
	"# port of the member. The rule compares the address and the port from before the address",
	"# change (the chain prerouting).",
	"ct state new ct status dnat ip saddr . ct original ip daddr . ct original proto-dst @" + SetMeshAccess + " accept",
}

// networkOutputRules go into the chain "output", before the rule that refuses the mesh range.
var networkOutputRules = []string{
	"# The networks: a website can connect to the members of its networks. The first two rules",
	"# are for a member on this server, after the address change (the chain output_nat). The",
	"# third rule is for a member on a different server.",
	"ct state new meta l4proto tcp ct status dnat meta skuid . ct original ip daddr . ct original proto-dst @" + SetNetworkAccess + " accept",
	"ct state new ct status dnat ct original ip daddr @" + SetRange + " meta skuid != 0 jump refuse",
	"ct state new meta skuid . ip daddr . tcp dport @" + SetNetworkAccess + " accept",
}

// networkPreroutingRules go into the chain "prerouting", before the redirects.
var networkPreroutingRules = []string{
	"# A connection from a tunnel to a member of this server goes to the container of the member.",
	`iifname "` + Interface + `" dnat ip to ip daddr . tcp dport map @` + MapMembers,
}

// networkOutputNATRules go into the chain "output_nat", before the redirects.
var networkOutputNATRules = []string{
	"# A website of this server uses the same member address as a website of a different server.",
	"dnat ip to ip daddr . tcp dport map @" + MapMembers,
}

// networkPostroutingRules go into the chain "postrouting_nat".
var networkPostroutingRules = []string{
	"# A connection of this server to one of its own members gets the source address that a",
	"# database server sees for each website container.",
	"oif lo ct status dnat ct original ip daddr @" + SetRange + " snat ip to " + quadlet.DatabaseClientAddress,
}

var natTypeRE = regexp.MustCompile(`^type nat hook \w+ priority `)

// addNetworkRules adds the rules of the networks to a file that has the rules of the mesh.
func addNetworkRules(content string) (string, error) {
	if strings.Contains(content, "set "+SetNetworkAccess+" {") {
		return content, nil
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var out []string
	done := map[string]bool{}
	chain := ""
	for _, l := range lines {
		text := strings.TrimSpace(l)
		switch {
		case chain == "mesh_input" && text == "drop" && !done["mesh_input"]:
			out = add(out, indentOf(l), networkMeshInputRules)
			done["mesh_input"] = true
		case chain == "output" && text == outputRules[len(outputRules)-1] && !done["output"]:
			out = add(out, indentOf(l), networkOutputRules)
			done["output"] = true
		}
		out = append(out, l)
		switch {
		case text == "table inet cloudfactory {" && !done["table"]:
			out = add(out, indentOf(l)+"  ", networkTableRules)
			done["table"] = true
		case strings.HasPrefix(text, "chain ") && strings.HasSuffix(text, "{"):
			chain = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "chain "), "{"))
		case text == "}":
			chain = ""
		case natTypeRE.MatchString(text) && !done[chain]:
			if rules := map[string][]string{"prerouting": networkPreroutingRules, "output_nat": networkOutputNATRules, "postrouting_nat": networkPostroutingRules}[chain]; rules != nil {
				out = add(out, indentOf(l), rules)
				done[chain] = true
			}
		}
	}
	for _, part := range []string{"table", "mesh_input", "output", "prerouting", "output_nat", "postrouting_nat"} {
		if !done[part] {
			return "", errors.New("the file does not have the rules of the installer, so the agent cannot add the rules of the networks")
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

// AddRules gives the firewall file of the installer with the rules of the mesh and of the
// networks. A file of this release has them already. It gives an error when the file does
// not have the rules of the installer.
func AddRules(content string) (string, error) {
	next, err := addMeshRules(content)
	if err != nil {
		return "", err
	}
	return addNetworkRules(next)
}

// addMeshRules adds the rules of the mesh.
func addMeshRules(content string) (string, error) {
	if strings.Contains(content, "set "+SetPort+" {") {
		return content, nil
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(tableRules)+len(inputRules)+len(outputRules))
	var table, input, output, inInput bool
	for _, l := range lines {
		if websitesJumpRE.MatchString(l) && !output {
			out = add(out, indentOf(l), outputRules)
			output = true
		}
		out = append(out, l)
		switch text := strings.TrimSpace(l); {
		case text == "table inet cloudfactory {" && !table:
			out = add(out, indentOf(l)+"  ", tableRules)
			table = true
		case text == "chain input {":
			inInput = true
		case text == "}":
			inInput = false
		case text == "iif lo accept" && inInput && !input:
			out = add(out, indentOf(l), inputRules)
			input = true
		}
	}
	if !table || !input || !output || !strings.Contains(content, "chain refuse {") {
		return "", errors.New("the file does not have the rules of the installer, so the agent cannot add the rules of the mesh")
	}
	return strings.Join(out, "\n") + "\n", nil
}
