// Package mesh has the encrypted tunnels between the servers of one panel (the mesh).
//
// Each server has one WireGuard interface. The panel is the directory of the public keys: it
// sends each server the full list of its peers (the action SetMesh). A server has a tunnel
// only to the servers in that list. The traffic between two servers goes through the public
// internet, and WireGuard encrypts it.
//
// This package has no effect on the host. It checks the state from the panel, and it makes
// the text of the WireGuard configuration and of the firewall file. The actions apply them
// (actions/mesh.go).
package mesh

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

const (
	// Interface is the name of the WireGuard interface of the mesh.
	Interface = "cf-mesh"
	// MTU is the MTU of the interface. It is sufficient for a path with an MTU of 1460 and
	// an IPv6 header.
	MTU = 1380
	// DefaultRange is the address range of the mesh when the panel has no other value.
	DefaultRange = "10.207.0.0/16"
	// DefaultPort is the UDP port of the tunnels when the panel has no other value.
	DefaultPort = 51820
	// MaxPeers is the maximum number of tunnels of one server.
	MaxPeers = 250
	// MaxMembers is the maximum number of network members in one state.
	MaxMembers = 4000
)

// Peer is one other server that this server has a tunnel to.
type Peer struct {
	// Server is the ID of the other server in the panel.
	Server string `json:"server"`
	// PublicKey is the public key of the other server.
	PublicKey string `json:"publicKey"`
	// PresharedKey is the secret key of this pair of servers. The panel makes it.
	PresharedKey string `json:"presharedKey"`
	// Endpoint is the public address and the UDP port of the other server.
	Endpoint string `json:"endpoint"`
	// Address is the mesh address of the other server.
	Address string `json:"address"`
	// Members are the mesh addresses of the network members on the other server that a
	// website of this server can connect to (network.go). The tunnel sends the traffic for
	// these addresses to the other server.
	Members []string `json:"members,omitempty"`
}

// State is the full mesh state of one server. The panel sends all of it each time.
type State struct {
	// Version is the version of the state in the panel. The agent reports the version that
	// it has.
	Version int64 `json:"version"`
	// Range is the address range of the mesh, for example "10.207.0.0/16". A server with
	// no tunnel can have the range: the network members of the server have addresses of it.
	Range string `json:"range"`
	// Address is the mesh address of this server. An empty address with no peers means
	// that the server is not in the mesh.
	Address string `json:"address"`
	// Port is the UDP port of the tunnels on this server.
	Port  int    `json:"port"`
	Peers []Peer `json:"peers"`
}

// Off reports whether the state takes the server out of the mesh.
func (s State) Off() bool { return s.Address == "" && len(s.Peers) == 0 }

// CheckKey checks a WireGuard key: 32 bytes in standard base64.
func CheckKey(key string) error {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != key {
		return errors.New("a tunnel key must be 32 bytes in base64")
	}
	return nil
}

// NewKey makes a private key.
func NewKey() (string, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key.Bytes()), nil
}

// PublicKey gives the public key of a private key.
func PublicKey(private string) (string, error) {
	if err := CheckKey(private); err != nil {
		return "", err
	}
	b, _ := base64.StdEncoding.DecodeString(private)
	key, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// checkRange checks the address range of the mesh: a private IPv4 network with a prefix
// length from 12 to 24. Thus a faulty job cannot send the traffic for a public address, or
// for an address of this server, into a tunnel.
func checkRange(text string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(text)
	if err != nil || !p.Addr().Is4() || p.Masked() != p || p.String() != text {
		return netip.Prefix{}, fmt.Errorf("the mesh range %q is not an IPv4 network", text)
	}
	if !p.Addr().IsPrivate() || p.Bits() < 12 || p.Bits() > 24 {
		return netip.Prefix{}, fmt.Errorf("the mesh range %s must be a private network with a prefix length from 12 to 24", text)
	}
	return p, nil
}

// checkAddress checks a mesh address: an address of the range that is not the first and not
// the last address.
func checkAddress(text string, r netip.Prefix) (netip.Addr, error) {
	a, err := netip.ParseAddr(text)
	if err != nil || !a.Is4() || a.String() != text {
		return netip.Addr{}, fmt.Errorf("the mesh address %q is not an IPv4 address", text)
	}
	if !r.Contains(a) || a == r.Addr() || !r.Contains(a.Next()) {
		return netip.Addr{}, fmt.Errorf("the mesh address %s is not an address of the mesh range %s", text, r)
	}
	return a, nil
}

// CheckEndpoint checks the public address and the port of a peer, for example
// "203.0.113.2:51820" or "[2001:db8::2]:51820". The address must be a public address.
func CheckEndpoint(text string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(text)
	if err != nil || ap.String() != text || ap.Addr().Zone() != "" || ap.Addr().Is4In6() {
		return netip.AddrPort{}, fmt.Errorf("the tunnel address %q is not an IP address and a port", text)
	}
	a := ap.Addr()
	if !a.IsGlobalUnicast() || a.IsPrivate() || ap.Port() < 1024 {
		return netip.AddrPort{}, fmt.Errorf("the tunnel address %s is not a public address with a port above 1023", text)
	}
	return ap, nil
}

// Check checks each value of the state. The root helper applies only a state that passes.
func (s State) Check() error {
	if s.Version < 0 {
		return errors.New("the version of the mesh state is less than 0")
	}
	if s.Off() {
		if s.Range == "" {
			return nil
		}
		_, err := checkRange(s.Range)
		return err
	}
	r, err := checkRange(s.Range)
	if err != nil {
		return err
	}
	own, err := checkAddress(s.Address, r)
	if err != nil {
		return err
	}
	if s.Port < 1024 || s.Port > 65535 {
		return fmt.Errorf("the tunnel port %d is not a port from 1024 to 65535", s.Port)
	}
	if len(s.Peers) > MaxPeers {
		return fmt.Errorf("the mesh state has %d peers; the maximum is %d", len(s.Peers), MaxPeers)
	}
	servers, keys, addresses := map[string]bool{}, map[string]bool{}, map[netip.Addr]bool{own: true}
	members := 0
	for _, p := range s.Peers {
		if err := validate.ID(p.Server); err != nil {
			return fmt.Errorf("the server of a peer: %w", err)
		}
		if err := errors.Join(CheckKey(p.PublicKey), CheckKey(p.PresharedKey)); err != nil {
			return fmt.Errorf("the peer %s: %w", p.Server, err)
		}
		if _, err := CheckEndpoint(p.Endpoint); err != nil {
			return fmt.Errorf("the peer %s: %w", p.Server, err)
		}
		a, err := checkAddress(p.Address, r)
		if err != nil {
			return fmt.Errorf("the peer %s: %w", p.Server, err)
		}
		if servers[p.Server] || keys[p.PublicKey] || addresses[a] {
			return fmt.Errorf("the peer %s is in the mesh state two times, or it has the key or the address of a different server", p.Server)
		}
		servers[p.Server], keys[p.PublicKey], addresses[a] = true, true, true
	}
	// An address is the address of one server or of one member of one server. Thus a peer
	// cannot get the traffic for a different server or for a member of a different server.
	for _, p := range s.Peers {
		for _, m := range p.Members {
			a, err := checkAddress(m, r)
			if err != nil {
				return fmt.Errorf("a member of the peer %s: %w", p.Server, err)
			}
			if addresses[a] {
				return fmt.Errorf("the member address %s of the peer %s is in the mesh state two times", m, p.Server)
			}
			addresses[a] = true
			members++
		}
	}
	if members > MaxMembers {
		return fmt.Errorf("the mesh state has %d member addresses; the maximum is %d", members, MaxMembers)
	}
	return nil
}

// sorted gives the peers in the sequence of their mesh addresses, so that the same state
// always gives the same files.
func (s State) sorted() []Peer {
	peers := slices.Clone(s.Peers)
	slices.SortFunc(peers, func(a, b Peer) int { return strings.Compare(a.Address+a.Server, b.Address+b.Server) })
	return peers
}

// Config gives the WireGuard configuration of the interface, for "wg syncconf". It has the
// private key and the pre-shared keys, so only root can read the file.
//
// A peer can send only from its own mesh address. Thus the address that the firewall sees
// is the address of the server that has the key. AllowedIPs also has the addresses of the
// network members on the peer: WireGuard sends the traffic for these addresses to the peer.
// A network member opens no connection, so a packet from a member address is an answer.
func (s State) Config(privateKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nListenPort = %d\n", privateKey, s.Port)
	for _, p := range s.sorted() {
		allowed := p.Address + "/32"
		for _, m := range slices.Sorted(slices.Values(p.Members)) {
			allowed += ", " + m + "/32"
		}
		fmt.Fprintf(&b, "\n# Server %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nEndpoint = %s\nAllowedIPs = %s\n", p.Server, p.PublicKey, p.PresharedKey, p.Endpoint, allowed)
	}
	return b.String()
}

// The nftables sets of the mesh. The installer makes them, with the rules that use them
// (Rules). The agent writes their elements.
const (
	// SetPort has the UDP port of the tunnels.
	SetPort = "mesh_port"
	// SetRange has the address range of the mesh.
	SetRange = "mesh_range"
	// SetEndpoints4 and SetEndpoints6 have the public addresses of the peers.
	SetEndpoints4 = "mesh_endpoints4"
	SetEndpoints6 = "mesh_endpoints6"
)

// Firewall gives the content of the firewall file of the mesh: the elements of the sets.
// header is the first line of each file of the agent. Call Check before.
func (s State) Firewall(header string) string {
	var v4, v6 []string
	for _, p := range s.sorted() {
		ap, err := netip.ParseAddrPort(p.Endpoint)
		if err != nil {
			continue
		}
		if ap.Addr().Is4() {
			v4 = append(v4, ap.Addr().String())
		} else {
			v6 = append(v6, ap.Addr().String())
		}
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("# The mesh: the UDP port of the tunnels, the address range, and the public addresses of the\n# servers that this server has a tunnel to. No other address can send to the tunnel port.\n")
	sets := []struct {
		name     string
		elements []string
	}{{SetPort, nil}, {SetRange, nil}, {SetEndpoints4, v4}, {SetEndpoints6, v6}}
	if !s.Off() {
		sets[0].elements = []string{strconv.Itoa(s.Port)}
	}
	if s.Range != "" {
		sets[1].elements = []string{s.Range}
	}
	for _, set := range sets {
		fmt.Fprintf(&b, "flush set inet cloudfactory %s\n", set.name)
		elements := slices.Compact(slices.Sorted(slices.Values(set.elements)))
		if len(elements) > 0 {
			fmt.Fprintf(&b, "add element inet cloudfactory %s { %s }\n", set.name, strings.Join(elements, ", "))
		}
	}
	return b.String()
}

// Handshakes reads the output of "wg show <interface> latest-handshakes": one line for each
// peer, with the public key and the time of the last handshake in seconds (0 means never).
func Handshakes(output string) map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if t, err := strconv.ParseInt(f[1], 10, 64); err == nil && CheckKey(f[0]) == nil {
			out[f[0]] = t
		}
	}
	return out
}
