package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/mesh"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// The networks of the customers on this server (package mesh, network.go).
//
// The panel sends the networks with the mesh state (SetMesh). The worker keeps them in its
// state. From the networks and from its records of the websites and of the database servers,
// the worker makes the firewall rules and the names of each website (networkRules). The root
// helper checks them, writes the hosts file of each website, and loads the firewall elements.
//
// The worker does this again after each change of the websites and of the database servers
// (syncDatabaseAccess), and when the agent starts. Thus a new website gets its names and its
// access before its container starts, and the access of a website that the agent removes
// goes: a later website can get the same UID.

const (
	opNetworkApply = "network.apply"
	// networkFirewallFile is the file with the firewall elements of the networks, in the
	// firewall directory of the agent.
	networkFirewallFile = "network.nft"
	// hostsFile is the name of the hosts file of a website in its configuration directory.
	hostsFile = "hosts"
)

// websiteHosts are the names of one website for its hosts file.
type websiteHosts struct {
	// User is the Unix user of the website.
	User string
	// Database is the address of the database servers of the customer on this server, or "".
	Database string
	// Names are the members that the website can connect to.
	Names []mesh.HostLine
}

type networkIn struct {
	Rules mesh.Rules
	Hosts []websiteHosts
}

// networkFromRequest gives the networks of a request.
func networkFromRequest(m *agentv1.Mesh) mesh.Network {
	var n mesh.Network
	for _, member := range m.GetMembers() {
		n.Members = append(n.Members, mesh.Member{ID: member.GetId(), Address: member.GetAddress(), Port: int(member.GetPort()), Servers: member.GetServers()})
	}
	for _, w := range m.GetWebsites() {
		site := mesh.Website{ID: w.GetId(), Names: []mesh.Name{}}
		for _, name := range w.GetNames() {
			site.Names = append(site.Names, mesh.Name{Name: name.GetName(), Address: name.GetAddress(), Port: int(name.GetPort())})
		}
		n.Websites = append(n.Websites, site)
	}
	return n
}

// SetNetwork keeps the networks of this server in the state.
func (s *State) SetNetwork(n mesh.Network) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	f.Network = nil
	if !n.Empty() {
		f.Network = &n
	}
	return s.write(f)
}

// Network gives the networks of this server.
func (s *State) Network() (mesh.Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil || f.Network == nil {
		return mesh.Network{}, err
	}
	return *f.Network, nil
}

// hostsPath gives the path of the hosts file of a website.
func (d *Deps) hostsPath(unixUser string) string {
	return filepath.Join(d.Host.ConfigDir(unixUser), hostsFile)
}

// hostsFileOf gives the path of the hosts file of a website when the website has the file.
// The unit of a website with no file has the names of the engines itself (quadlet).
func (d *Deps) hostsFileOf(unixUser string) string {
	if fi, err := os.Lstat(d.hostsPath(unixUser)); err == nil && fi.Mode().IsRegular() {
		return d.hostsPath(unixUser)
	}
	return ""
}

// networkRules makes the firewall rules and the names of the websites from the networks of
// the panel. It leaves out a member and a website that this server does not have: the panel
// can send the networks before the job that makes the database server or the website.
func (d *Deps) networkRules() (networkIn, error) {
	in := networkIn{Hosts: []websiteHosts{}}
	network, err := d.State.Network()
	if err != nil {
		return in, err
	}
	sites, err := d.State.All()
	if err != nil {
		return in, err
	}
	instances, err := d.State.DatabaseInstances()
	if err != nil {
		return in, err
	}
	databases := map[string]string{}
	for _, inst := range instances {
		address, err := quadlet.DatabaseAddress(inst.Number)
		if err != nil {
			return in, err
		}
		for _, id := range inst.Websites {
			databases[id] = address
		}
		for id, srv := range inst.Servers {
			for _, m := range network.Members {
				// A member answers on the usual port of its engine.
				if strings.ToLower(m.ID) == id && m.Port == quadlet.DatabasePort(srv.Engine) {
					in.Rules.Members = append(in.Rules.Members, mesh.MemberRule{Address: m.Address, Port: m.Port, Local: address, LocalPort: srv.Port, Servers: m.Servers})
				}
			}
		}
	}
	// A website of this server that is a member answers on WebsitePort. Its container has a
	// port on the private service address. A website that its owner stopped stays a member:
	// its port has no listener, so a connection gets a refusal.
	for _, m := range network.Members {
		if m.Port != mesh.WebsitePort {
			continue
		}
		for _, s := range sites {
			if strings.EqualFold(s.ID, m.ID) && s.UnixUser != "" {
				in.Rules.Members = append(in.Rules.Members, mesh.MemberRule{Address: m.Address, Port: m.Port, Local: quadlet.ServiceAddress, LocalPort: s.Port, Servers: m.Servers})
			}
		}
	}
	names := map[string][]mesh.Name{}
	for _, w := range network.Websites {
		names[strings.ToLower(w.ID)] = w.Names
	}
	for _, s := range sites {
		if s.UnixUser == "" {
			continue
		}
		hosts := websiteHosts{User: s.UnixUser, Database: databases[s.ID], Names: []mesh.HostLine{}}
		// A website that the agent removes has no Unix user at the end of the job. It gets
		// no access.
		if uid, err := d.Host.LookupUID(s.UnixUser); err == nil {
			for _, n := range names[strings.ToLower(s.ID)] {
				hosts.Names = append(hosts.Names, mesh.HostLine{Address: n.Address, Name: n.Name})
				in.Rules.Access = append(in.Rules.Access, mesh.AccessRule{UID: uid, Address: n.Address, Port: n.Port})
			}
		}
		in.Hosts = append(in.Hosts, hosts)
	}
	slices.SortFunc(in.Hosts, func(a, b websiteHosts) int { return strings.Compare(a.User, b.User) })
	return in, nil
}

// syncNetwork makes the hosts file of each website and the firewall agree with the networks
// in the state.
func (d *Deps) syncNetwork(ctx context.Context) error {
	d.networkMu.Lock()
	defer d.networkMu.Unlock()
	in, err := d.networkRules()
	if err != nil {
		return err
	}
	return d.applyNetwork(ctx, in)
}

// syncNetworkLater is syncNetwork for a job of a website or of a database server. A fault of
// the networks does not stop such a job: the website keeps the access that it has, and the
// agent tries again at the next change.
func (d *Deps) syncNetworkLater(ctx context.Context) {
	if err := d.syncNetwork(ctx); err != nil {
		slog.Error("set the names and the access of the networks", "err", err)
	}
}

// applyNetwork checks the rules and the names, writes the hosts file of each website, and
// loads the firewall elements.
func (d *Deps) applyNetwork(ctx context.Context, in networkIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opNetworkApply, in, nil)
	}
	d.meshMu.Lock()
	defer d.meshMu.Unlock()
	state, _, err := d.savedMesh()
	if err != nil {
		return err
	}
	servers := []string{}
	if state.Address != "" {
		servers = append(servers, state.Address)
	}
	for _, p := range state.Peers {
		servers = append(servers, p.Address)
	}
	// The check is first: the helper writes nothing when one value is not correct.
	if err := in.Rules.Check(state.Range, servers, host.WebsiteIDMin, host.WebsiteIDMax, WebsitePortMin, WebsitePortMax); err != nil {
		return err
	}
	contents := make([]string, len(in.Hosts))
	for i, h := range in.Hosts {
		if err := d.websiteUser(h.User); err != nil {
			return err
		}
		if contents[i], err = mesh.HostsFile(quadlet.Header, h.Database, h.Names, state.Range); err != nil {
			return fmt.Errorf("the names of the website user %s: %w", h.User, err)
		}
	}
	// The firewall is before the names: a website never has a name that the firewall refuses.
	if err := d.loadNetworkFirewall(ctx, in.Rules); err != nil {
		return err
	}
	var errs []error
	for i, h := range in.Hosts {
		errs = append(errs, writeInPlace(d.hostsPath(h.User), contents[i]))
	}
	return errors.Join(errs...)
}

// loadNetworkFirewall writes the firewall file of the networks and loads it. The caller
// holds meshMu.
func (d *Deps) loadNetworkFirewall(ctx context.Context, rules mesh.Rules) error {
	// A server with no networks keeps its firewall as it is.
	if _, err := os.Stat(filepath.Join(d.firewallDir(), networkFirewallFile)); os.IsNotExist(err) && len(rules.Members)+len(rules.Access) == 0 {
		return nil
	}
	content := rules.Firewall(quadlet.Header)
	managed, err := d.addMeshRules(ctx, map[string]string{networkFirewallFile: content})
	if err != nil {
		return err
	}
	if !managed {
		if len(rules.Members)+len(rules.Access) > 0 && !d.networkWarned {
			d.networkWarned = true
			slog.Warn("no Cloudfactory firewall file; the websites cannot connect to the members of their networks until your firewall has the rules of the installer")
		}
		return nil
	}
	if content == d.networkLoaded {
		return nil
	}
	// The load is one step for the kernel: the old elements go and the new elements come at
	// the same time.
	if _, err := d.Run.Run(ctx, "nft", "-f", filepath.Join(d.firewallDir(), networkFirewallFile)); err != nil {
		return err
	}
	d.networkLoaded = content
	return nil
}

// writeInPlace writes a small file with no rename. A container that has the file as a mount
// keeps the old file after a rename, so the hosts file of a website must change in place.
// The function does not follow a symbolic link.
func writeInPlace(path, content string) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
