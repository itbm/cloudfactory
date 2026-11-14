package mesh

import (
	"errors"
	"regexp"
	"strings"
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

// AddRules gives the firewall file of the installer with the rules of the mesh. A file of
// this release has them already. It gives an error when the file does not have the rules of
// the installer.
func AddRules(content string) (string, error) {
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

// InstallerRules gives the three groups of rules, for the test of the installer.
func InstallerRules() (table, input, output []string) { return tableRules, inputRules, outputRules }
