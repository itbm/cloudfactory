package mesh

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The private networks of the customers.
//
// A network has members. A member has a name ("<name>.internal") and one address of the mesh
// range (the member address). The name and the address are the same on each server, so a
// website finds a member with the same name when the member is on the same server and when
// it is on a different server. In this release, a member is a database server. It answers on
// the usual port of its engine.
//
// The panel sends the networks of a server with the mesh state (the action SetMesh):
//
//   - The members of this server, with the servers that have a website in a network of
//     the member. Only these servers can connect to the member through a tunnel.
//   - The names that each website of this server can use.
//
// The firewall of the server does the control of access, in two places. The server of a
// website lets only the Unix user of that website connect to the member address
// (SetNetworkAccess). The server of a member accepts a connection from a tunnel only from
// a server that has a website in a network of the member (SetMeshAccess). A name gives no
// access: a website that writes an address in its own configuration gets the same answer
// from the firewall.

// Network is the part of the mesh state for the networks of the customers.
type Network struct {
	// Members are the network members on this server.
	Members []Member `json:"members,omitempty"`
	// Websites are the websites of this server that are in a network with a member.
	Websites []Website `json:"websites,omitempty"`
}

// Member is one network member on this server: a database server.
type Member struct {
	// ID is the ID of the database server in the panel.
	ID string `json:"id"`
	// Address is the member address.
	Address string `json:"address"`
	// Port is the port that the member answers on: the usual port of its engine.
	Port int `json:"port"`
	// Servers are the mesh addresses of the other servers that can connect to the member.
	Servers []string `json:"servers,omitempty"`
}

// Website is one website of this server, with the members that it can connect to.
type Website struct {
	// ID is the ID of the website in the panel.
	ID    string `json:"id"`
	Names []Name `json:"names"`
}

// Name is the name of one member for a website.
type Name struct {
	// Name is the host name, for example "shop-db.internal".
	Name string `json:"name"`
	// Address is the member address.
	Address string `json:"address"`
	// Port is the port of the member.
	Port int `json:"port"`
}

// Empty reports whether the server has no member and no website with a name.
func (n Network) Empty() bool { return len(n.Members) == 0 && len(n.Websites) == 0 }

// nameRE is a member name: one DNS label and ".internal".
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.internal$`)

// engineNames are the names that each website has for the database servers of its customer
// on its own server. A member cannot have one of them.
var engineNames = []string{quadlet.MariaDBHost, quadlet.PostgreSQLHost, quadlet.ValkeyHost}

// CheckName checks the name of a member.
func CheckName(name string) error {
	if !nameRE.MatchString(name) || slices.Contains(engineNames, name) {
		return fmt.Errorf("%q is not the name of a network member", name)
	}
	return nil
}

// WebsitePort is the port that a website answers on as a network member. It is HTTP: the
// tunnel between two servers encrypts the traffic, and a connection on one server does not
// leave the server.
const WebsitePort = 80

// MemberPorts are the ports that a member can answer on: the usual ports of the engines for
// a database server, and WebsitePort for a website.
var MemberPorts = []int{quadlet.DatabasePort("mariadb"), quadlet.DatabasePort("postgresql"), quadlet.DatabasePort("valkey"), WebsitePort}

func checkMemberPort(port int) error {
	if !slices.Contains(MemberPorts, port) {
		return fmt.Errorf("%d is not a port of a network member", port)
	}
	return nil
}

// privateAddress checks an address of a mesh range that the caller does not know: a private
// IPv4 address.
func privateAddress(text string) (netip.Addr, error) {
	a, err := netip.ParseAddr(text)
	if err != nil || !a.Is4() || a.String() != text || !a.IsPrivate() {
		return netip.Addr{}, fmt.Errorf("%q is not a private IPv4 address", text)
	}
	return a, nil
}

// Check checks each value of the networks. The worker of the agent keeps only networks that
// pass. The root helper checks the rules that the worker makes from them (Rules.Check).
func (n Network) Check() error {
	if len(n.Members) > MaxMembers || len(n.Websites) > MaxMembers {
		return fmt.Errorf("the networks have too many members or websites; the maximum is %d", MaxMembers)
	}
	ids, addresses := map[string]bool{}, map[string]bool{}
	for _, m := range n.Members {
		if err := validate.ID(m.ID); err != nil {
			return fmt.Errorf("the ID of a member: %w", err)
		}
		if _, err := privateAddress(m.Address); err != nil {
			return fmt.Errorf("the member %s: %w", m.ID, err)
		}
		if err := checkMemberPort(m.Port); err != nil {
			return fmt.Errorf("the member %s: %w", m.ID, err)
		}
		if ids[strings.ToLower(m.ID)] || addresses[m.Address] {
			return fmt.Errorf("the member %s or its address is in the networks two times", m.ID)
		}
		ids[strings.ToLower(m.ID)], addresses[m.Address] = true, true
		if len(m.Servers) > MaxPeers {
			return fmt.Errorf("the member %s has too many servers", m.ID)
		}
		for _, s := range m.Servers {
			if _, err := privateAddress(s); err != nil {
				return fmt.Errorf("a server of the member %s: %w", m.ID, err)
			}
		}
	}
	websites := map[string]bool{}
	for _, w := range n.Websites {
		if err := validate.ID(w.ID); err != nil {
			return fmt.Errorf("the ID of a website: %w", err)
		}
		if websites[strings.ToLower(w.ID)] {
			return fmt.Errorf("the website %s is in the networks two times", w.ID)
		}
		websites[strings.ToLower(w.ID)] = true
		if len(w.Names) > MaxMembers {
			return fmt.Errorf("the website %s has too many names", w.ID)
		}
		names := map[string]bool{}
		for _, name := range w.Names {
			if err := CheckName(name.Name); err != nil {
				return fmt.Errorf("the website %s: %w", w.ID, err)
			}
			if names[name.Name] {
				return fmt.Errorf("the website %s has the name %s two times", w.ID, name.Name)
			}
			names[name.Name] = true
			if _, err := privateAddress(name.Address); err != nil {
				return fmt.Errorf("the name %s: %w", name.Name, err)
			}
			if err := checkMemberPort(name.Port); err != nil {
				return fmt.Errorf("the name %s: %w", name.Name, err)
			}
		}
	}
	return nil
}

// The nftables sets of the networks. The installer makes them, with the rules that use them
// (rules.go). The agent writes their elements.
const (
	// SetNetworkAccess has the access of the websites of this server: the UID of a website,
	// a member address and the port of the member.
	SetNetworkAccess = "network_access"
	// SetMeshAccess has the access of the other servers to the members of this server: the
	// mesh address of a server, a member address and the port of the member.
	SetMeshAccess = "mesh_access"
	// MapMembers has the container of each member of this server: the member address and
	// the port of the member give the address and the port of the container on this server.
	MapMembers = "mesh_members"
	// SetTransfers has the moves of database servers to this server: the mesh address of the
	// server that sends, the mesh address of this server and TransferPort.
	SetTransfers = "mesh_transfers"
)

// TransferPort is the TCP port on the mesh address of a server that receives a database
// server from a different server.
const TransferPort = 7445

// MemberRule is one member of this server for the firewall.
type MemberRule struct {
	// Address and Port are the member address and the port that the member answers on.
	Address string
	Port    int
	// Local and LocalPort are the address and the port of the container on this server: an
	// address of quadlet.DatabaseNetwork and the port of a database server.
	Local     string
	LocalPort int
	// Servers are the mesh addresses of the other servers that can connect.
	Servers []string
}

// AccessRule lets one website of this server connect to one member.
type AccessRule struct {
	// UID is the UID of the Unix user of the website.
	UID     int
	Address string
	Port    int
}

// Rules are the networks of a server for the firewall. The worker of the agent makes them
// from the Network of the panel and from its records of the websites and of the database
// servers. The root helper checks them and writes the firewall file.
type Rules struct {
	Members []MemberRule
	Access  []AccessRule
}

// Check checks each rule. meshRange is the address range of the mesh on this server, and
// servers are the mesh addresses of this server and of its peers. uidMin and uidMax are the
// limits of the UID of a website user. portMin and portMax are the limits of the port of a
// website container on the private service address.
//
// After the check, a rule can open only an address of the mesh range that is not the address
// of a server, only for the Unix user of a website or for a server, and only on the usual
// port of an engine or on WebsitePort. The address change of a member can go only to a port
// of a database server on the database network, or to a port of a website container on the
// private service address.
func (r Rules) Check(meshRange string, servers []string, uidMin, uidMax, portMin, portMax int) error {
	if len(r.Members) == 0 && len(r.Access) == 0 {
		return nil
	}
	if len(r.Members) > MaxMembers || len(r.Access) > MaxMembers*8 {
		return errors.New("the networks have too many rules")
	}
	if meshRange == "" {
		return errors.New("this server has no mesh range, so it cannot have network members")
	}
	mr, err := checkRange(meshRange)
	if err != nil {
		return err
	}
	member := func(text string) error {
		if _, err := checkAddress(text, mr); err != nil {
			return err
		}
		if slices.Contains(servers, text) {
			return fmt.Errorf("the address %s is the mesh address of a server", text)
		}
		return nil
	}
	seen, locals := map[string]bool{}, map[string]bool{}
	for _, m := range r.Members {
		if err := errors.Join(member(m.Address), checkMemberPort(m.Port)); err != nil {
			return err
		}
		if m.Port == WebsitePort {
			// A website: its container is on the private service address.
			if m.Local != quadlet.ServiceAddress || m.LocalPort < portMin || m.LocalPort > portMax {
				return fmt.Errorf("the member %s: %s . %d is not the address and the port of a website container", m.Address, m.Local, m.LocalPort)
			}
		} else if _, err := quadlet.DatabaseNumber(m.Local); err != nil {
			return fmt.Errorf("the member %s: %w", m.Address, err)
		} else if !slices.ContainsFunc([]string{"mariadb", "postgresql", "valkey"}, func(engine string) bool {
			return quadlet.DatabasePort(engine) == m.Port && quadlet.CheckDatabasePort(engine, m.LocalPort) == nil
		}) {
			return fmt.Errorf("the member %s: %d is not a port of a database server for the port %d", m.Address, m.LocalPort, m.Port)
		}
		local := fmt.Sprintf("%s . %d", m.Local, m.LocalPort)
		if seen[m.Address] || locals[local] {
			return fmt.Errorf("the member %s or its container is in the rules two times", m.Address)
		}
		seen[m.Address], locals[local] = true, true
		for _, s := range m.Servers {
			if _, err := checkAddress(s, mr); err != nil {
				return fmt.Errorf("a server of the member %s: %w", m.Address, err)
			}
		}
	}
	for _, a := range r.Access {
		if a.UID < uidMin || a.UID > uidMax {
			return fmt.Errorf("the UID %d is not the UID of a website user", a.UID)
		}
		if err := errors.Join(member(a.Address), checkMemberPort(a.Port)); err != nil {
			return err
		}
	}
	// A member address of this server never names a member of a different server. If it
	// did, the address change would send the connection to the local container.
	for _, m := range r.Members {
		for _, s := range m.Servers {
			if seen[s] {
				return fmt.Errorf("the address %s is a member address and the address of a server", s)
			}
		}
	}
	return nil
}

// Firewall gives the content of the firewall file of the networks: the elements of the two
// sets and of the map. header is the first line of each file of the agent. Call Check before.
func (r Rules) Firewall(header string) string {
	var access, servers, members []string
	for _, a := range r.Access {
		access = append(access, fmt.Sprintf("%d . %s . %d", a.UID, a.Address, a.Port))
	}
	for _, m := range r.Members {
		members = append(members, fmt.Sprintf("%s . %d : %s . %d", m.Address, m.Port, m.Local, m.LocalPort))
		for _, s := range m.Servers {
			servers = append(servers, fmt.Sprintf("%s . %s . %d", s, m.Address, m.Port))
		}
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("# The networks of the customers. " + SetNetworkAccess + ": the UID of a website of this server, the\n# address of a network member and its port. " + SetMeshAccess + ": the mesh address of a server that\n# has such a website, and a member of this server. " + MapMembers + ": a member of this server, and\n# the address and the port of its container.\n")
	for _, set := range []struct {
		kind, name string
		elements   []string
	}{{"set", SetNetworkAccess, access}, {"set", SetMeshAccess, servers}, {"map", MapMembers, members}} {
		fmt.Fprintf(&b, "flush %s inet cloudfactory %s\n", set.kind, set.name)
		elements := slices.Compact(slices.Sorted(slices.Values(set.elements)))
		if len(elements) > 0 {
			fmt.Fprintf(&b, "add element inet cloudfactory %s { %s }\n", set.name, strings.Join(elements, ", "))
		}
	}
	return b.String()
}

// HostLine is one line of the hosts file of a website container.
type HostLine struct {
	Address string
	Name    string
}

// HostsFile gives the content of the hosts file of a website container. database is the
// address of the database servers of the customer on this server, or "". names are the
// members that the website can connect to.
//
// It checks each value: a member name goes only to an address of the mesh range, and the
// names of the engines go only to an address of the database network.
func HostsFile(header, database string, names []HostLine, meshRange string) (string, error) {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("127.0.0.1 localhost\n::1 localhost\n")
	if database != "" {
		if _, err := quadlet.DatabaseNumber(database); err != nil {
			return "", err
		}
		b.WriteString("# The database servers of the customer on this server. Each one has its own port.\n")
		fmt.Fprintf(&b, "%s %s\n", database, strings.Join(engineNames, " "))
	}
	if len(names) == 0 {
		return b.String(), nil
	}
	mr, err := checkRange(meshRange)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(names))
	for _, n := range names {
		if err := CheckName(n.Name); err != nil {
			return "", err
		}
		if _, err := checkAddress(n.Address, mr); err != nil {
			return "", err
		}
		lines = append(lines, n.Address+" "+n.Name)
	}
	slices.Sort(lines)
	b.WriteString("# The members of the networks of the website. A name is the same on each server.\n")
	b.WriteString(strings.Join(slices.Compact(lines), "\n") + "\n")
	return b.String(), nil
}
