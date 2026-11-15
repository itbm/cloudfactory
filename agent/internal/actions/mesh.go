package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/mesh"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// The mesh: the encrypted tunnels of this server to other servers (package mesh).
//
// The panel sends the full state (SetMesh). The worker checks it and gives it to the root
// helper. The helper checks it again, keeps it in a file, and applies it: the WireGuard
// interface, its address and route, and the elements of the firewall sets. The helper
// applies the file again when it starts, so the tunnels operate after a restart of the
// server with no connection to the panel.
//
// The private key of the server never leaves the root helper. The worker and the panel get
// only the public key.

// The names of the mesh operations of the root helper.
const (
	opMeshApply  = "mesh.apply"
	opMeshStatus = "mesh.status"
	opMeshRotate = "mesh.rotate"
	// The operation of the networks is in network.go: opNetworkApply.
)

// The files of the mesh in the mesh directory. Only root can read the directory.
const (
	meshKeyFile    = "private.key"
	meshStateFile  = "mesh.json"
	meshConfigFile = mesh.Interface + ".conf"
	// meshFirewallFile is the file with the elements of the firewall sets, in the firewall
	// directory of the agent. It has no secret.
	meshFirewallFile = "mesh.nft"
	// meshJobKey makes the mesh jobs of a server run one at a time.
	meshJobKey = "mesh"
)

type meshIn struct {
	State mesh.State
}

func (d *Deps) meshDir() string {
	if d.MeshDir != "" {
		return d.MeshDir
	}
	return "/etc/cloudfactory/mesh"
}

// meshFromRequest gives the state of a request.
func meshFromRequest(m *agentv1.Mesh) mesh.State {
	s := mesh.State{Version: m.GetVersion(), Range: m.GetRange(), Address: m.GetAddress(), Port: int(m.GetPort()), Peers: []mesh.Peer{}}
	for _, p := range m.GetPeers() {
		s.Peers = append(s.Peers, mesh.Peer{Server: p.GetServer(), PublicKey: p.GetPublicKey(), PresharedKey: p.GetPresharedKey(), Endpoint: p.GetEndpoint(), Address: p.GetAddress(), Members: p.GetMembers()})
	}
	return s
}

// SetMesh sets the tunnels of this server to the state of the request. The job does not
// fail when a peer does not answer: the other server can be offline, or it can get its own
// job a moment later. The result has the state of each tunnel, and the panel shows it.
func (d *Deps) SetMesh(req *agentv1.SetMeshRequest) (jobs.Job, error) {
	if req.GetMesh() == nil {
		return jobs.Job{}, errors.New("the request has no mesh state")
	}
	state := meshFromRequest(req.GetMesh())
	if err := state.Check(); err != nil {
		return jobs.Job{}, err
	}
	network := networkFromRequest(req.GetMesh())
	if err := network.Check(); err != nil {
		return jobs.Job{}, err
	}
	var status *panel.Mesh
	return jobs.Job{ID: req.GetJobId(), Key: meshJobKey, Steps: []jobs.Step{
		{Name: "Set tunnels and firewall", Do: func(ctx context.Context) error {
			return d.applyMesh(ctx, state)
		}},
		{Name: "Set names and access of networks", Do: func(ctx context.Context) error {
			if err := d.State.SetNetwork(network); err != nil {
				return err
			}
			return d.syncNetwork(ctx)
		}},
		{Name: "Check tunnels", Do: func(ctx context.Context) error {
			var err error
			status, err = d.waitForMesh(ctx, orDefault(d.MeshWait, 10*time.Second))
			return err
		}},
	}, Result: func() map[string]any {
		return map[string]any{"mesh": status}
	}}, nil
}

// RotateMeshKey makes a new tunnel key. The result has the new public key.
func (d *Deps) RotateMeshKey(req *agentv1.RotateMeshKeyRequest) (jobs.Job, error) {
	var public string
	return jobs.Job{ID: req.GetJobId(), Key: meshJobKey, Steps: []jobs.Step{
		{Name: "Make new key", Do: func(ctx context.Context) error {
			var err error
			public, err = d.rotateMeshKey(ctx)
			return err
		}},
	}, Result: func() map[string]any {
		return map[string]any{"publicKey": public}
	}}, nil
}

// waitForMesh gives the state of the tunnels. It asks again until each peer answers, for
// the time limit at most.
func (d *Deps) waitForMesh(ctx context.Context, limit time.Duration) (*panel.Mesh, error) {
	deadline := time.Now().Add(limit)
	for {
		status, err := d.meshStatus(ctx)
		if err != nil {
			return nil, err
		}
		all := !slices.ContainsFunc(status.Tunnels, func(t panel.Tunnel) bool { return !t.Reachable })
		if all || time.Now().After(deadline) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, nil
		case <-time.After(min(time.Second, max(time.Until(deadline), time.Millisecond))):
		}
	}
}

// meshKey gives the private key of this server. It makes the key when the server has none.
func (d *Deps) meshKey() (string, error) {
	path := filepath.Join(d.meshDir(), meshKeyFile)
	b, err := os.ReadFile(path)
	if err == nil {
		key := strings.TrimSpace(string(b))
		if err := mesh.CheckKey(key); err != nil {
			return "", fmt.Errorf("the file %s: %w", path, err)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	key, err := mesh.NewKey()
	if err != nil {
		return "", err
	}
	return key, d.writeMeshFile(meshKeyFile, key+"\n")
}

// writeMeshFile writes a file that only root can read.
func (d *Deps) writeMeshFile(name, content string) error {
	if err := os.MkdirAll(d.meshDir(), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(d.meshDir(), 0o700); err != nil {
		return err
	}
	_, err := host.WriteFile(filepath.Join(d.meshDir(), name), content, 0o600)
	return err
}

// savedMesh gives the mesh state that the helper applied last. ok is false with no state.
func (d *Deps) savedMesh() (s mesh.State, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(d.meshDir(), meshStateFile))
	if os.IsNotExist(err) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, false, err
	}
	// The file is from this helper, but the helper never applies a state with no check.
	return s, true, s.Check()
}

// applyMesh makes the tunnels of this server agree with the state.
func (d *Deps) applyMesh(ctx context.Context, s mesh.State) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opMeshApply, meshIn{State: s}, nil)
	}
	if err := s.Check(); err != nil {
		return err
	}
	d.meshMu.Lock()
	defer d.meshMu.Unlock()
	previous, had, _ := d.savedMesh()
	// A job of the panel that comes late must not put an old state back.
	if had && s.Version < previous.Version {
		return fmt.Errorf("the server has version %d of the mesh state, and the request has the older version %d", previous.Version, s.Version)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := d.writeMeshFile(meshStateFile, string(b)+"\n"); err != nil {
		return err
	}
	return d.loadMesh(ctx, s, previous, true)
}

// RestoreMesh applies the saved mesh state when the root helper starts. After a restart of
// the server, the interface is gone, and the firewall has its sets from the files.
func (d *Deps) RestoreMesh(ctx context.Context) error {
	if d.Gate != nil {
		return errors.New("actions: RestoreMesh is for the root helper")
	}
	d.meshMu.Lock()
	defer d.meshMu.Unlock()
	s, ok, err := d.savedMesh()
	if err != nil || !ok || s.Off() {
		return err
	}
	return d.loadMesh(ctx, s, s, false)
}

// loadMesh applies a checked state to the host. The caller holds meshMu. previous is the
// state from before. With install set, the function installs the WireGuard tools when the
// server does not have them.
func (d *Deps) loadMesh(ctx context.Context, s, previous mesh.State, install bool) error {
	run := d.Run
	exists := func() bool {
		_, err := run.Run(ctx, "ip", "link", "show", "dev", mesh.Interface)
		return err == nil
	}
	if s.Off() {
		if exists() {
			if _, err := run.Run(ctx, "ip", "link", "delete", "dev", mesh.Interface); err != nil {
				return err
			}
		}
		if err := os.Remove(filepath.Join(d.meshDir(), meshConfigFile)); err != nil && !os.IsNotExist(err) {
			return err
		}
		_, err := d.loadMeshFirewall(ctx, s)
		return err
	}
	key, err := d.meshKey()
	if err != nil {
		return err
	}
	if _, err := run.Run(ctx, "wg", "--version"); err != nil {
		if !install {
			return fmt.Errorf("the server has no WireGuard tools: %w", err)
		}
		// A server from an earlier release. The installer of this release installs the package.
		if _, err := run.Run(ctx, "apt-get", "install", "-y", "-q", "--no-install-recommends", "wireguard-tools"); err != nil {
			return fmt.Errorf("install the package wireguard-tools: %w", err)
		}
	}
	// The firewall is first: the tunnel port is open for the peers when the interface starts.
	managed, err := d.loadMeshFirewall(ctx, s)
	if err != nil {
		return err
	}
	if !managed {
		slog.Warn("no Cloudfactory firewall file; open the tunnel port for the peers in your firewall, and accept from the mesh interface only the traffic that you permit", "port", s.Port, "interface", mesh.Interface)
	}
	if err := d.writeMeshFile(meshConfigFile, s.Config(key)); err != nil {
		return err
	}
	if !exists() {
		if _, err := run.Run(ctx, "ip", "link", "add", "dev", mesh.Interface, "type", "wireguard"); err != nil {
			return fmt.Errorf("the kernel of this server cannot make a WireGuard interface: %w", err)
		}
	} else if previous.Address != s.Address || previous.Range != s.Range {
		// A new address or a new range: the old address and the old route go.
		if _, err := run.Run(ctx, "ip", "address", "flush", "dev", mesh.Interface); err != nil {
			return err
		}
		if _, err := run.Run(ctx, "ip", "route", "flush", "dev", mesh.Interface); err != nil {
			return err
		}
	}
	// "syncconf" changes only the peers that are different. The tunnels of the other peers
	// continue with no stop.
	for _, argv := range [][]string{
		{"wg", "syncconf", mesh.Interface, filepath.Join(d.meshDir(), meshConfigFile)},
		{"ip", "address", "replace", s.Address + "/32", "dev", mesh.Interface},
		{"ip", "link", "set", "dev", mesh.Interface, "mtu", strconv.Itoa(mesh.MTU), "up"},
		// The source address makes each connection of this server to the mesh come from its
		// mesh address. A peer accepts no other address from this server.
		{"ip", "route", "replace", s.Range, "dev", mesh.Interface, "src", s.Address},
	} {
		if _, err := run.Run(ctx, argv[0], argv[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// loadMeshFirewall makes sure that the firewall file of the installer has the rules of the
// mesh, writes the elements of the sets, and loads them. managed is false when the server
// has no firewall file of the installer: then the administrator manages the firewall.
func (d *Deps) loadMeshFirewall(ctx context.Context, s mesh.State) (managed bool, err error) {
	managed, err = d.addMeshRules(ctx, map[string]string{meshFirewallFile: s.Firewall(quadlet.Header)})
	if err != nil || !managed {
		return managed, err
	}
	// The kernel can have no elements after a restart of the firewall by hand, so the agent
	// loads the file each time. The load is one step for the kernel.
	_, err = d.Run.Run(ctx, "nft", "-f", filepath.Join(d.firewallDir(), meshFirewallFile))
	return true, err
}

// addMeshRules makes sure that the firewall file of the installer has the rules of the mesh
// and of the networks, and writes files of the agent in the firewall directory (the name and
// the content of each file). It does not load a file of the agent, but when it adds the
// rules, it loads the file of the installer, which includes them. managed is false when the
// server has no firewall file of the installer.
func (d *Deps) addMeshRules(ctx context.Context, files map[string]string) (managed bool, err error) {
	path := d.firewallFile()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(d.firewallDir(), 0o755); err != nil {
		return false, err
	}
	// A file of an earlier release does not include the files of the agent. The rules of
	// the database servers add the include.
	if err := d.openDatabaseNetwork(ctx); err != nil {
		return false, err
	}
	if b, err = os.ReadFile(path); err != nil {
		return false, err
	}
	next, err := mesh.AddRules(string(b))
	if err != nil {
		return false, fmt.Errorf("the firewall file %s: %w; run the installer again on this server", path, err)
	}
	write := func() error {
		for name, content := range files {
			if _, err := host.WriteFile(filepath.Join(d.firewallDir(), name), content, 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	if next == string(b) {
		return true, write()
	}
	// The file of the installer includes the files of the agent, so they change in one
	// step: nftables checks the new rules with the new elements.
	check := path + ".new"
	if _, err := host.WriteFile(check, next, 0o644); err != nil {
		return false, err
	}
	defer os.Remove(check)
	if _, err := d.Run.Run(ctx, "nft", "-c", "-f", check); err != nil {
		return false, fmt.Errorf("nftables refuses the rules of the mesh for %s (%w); run the installer again on this server", path, err)
	}
	if _, err := host.WriteFile(path, next, 0o644); err != nil {
		return false, err
	}
	if err := write(); err != nil {
		return false, err
	}
	slog.Info("added the rules of the mesh and of the networks to the firewall", "file", path)
	_, err = d.Run.Run(ctx, "nft", "-f", path)
	return true, err
}

// rotateMeshKey makes a new private key, and gives the new public key. The tunnels stop
// until each peer has the new public key.
func (d *Deps) rotateMeshKey(ctx context.Context) (public string, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMeshRotate, nil, &public)
		return public, err
	}
	d.meshMu.Lock()
	defer d.meshMu.Unlock()
	key, err := mesh.NewKey()
	if err != nil {
		return "", err
	}
	if public, err = mesh.PublicKey(key); err != nil {
		return "", err
	}
	if err := d.writeMeshFile(meshKeyFile, key+"\n"); err != nil {
		return "", err
	}
	s, ok, err := d.savedMesh()
	if err != nil || !ok || s.Off() {
		return public, err
	}
	return public, d.loadMesh(ctx, s, s, false)
}

// meshStatus gives the public key of this server and the state of each tunnel. It sends
// one echo request to each peer, which also starts the handshake of a tunnel that has none.
func (d *Deps) meshStatus(ctx context.Context) (*panel.Mesh, error) {
	if d.Gate != nil {
		out := &panel.Mesh{}
		return out, d.Gate.Call(ctx, opMeshStatus, nil, out)
	}
	d.meshMu.Lock()
	defer d.meshMu.Unlock()
	key, err := d.meshKey()
	if err != nil {
		return nil, err
	}
	out := &panel.Mesh{Tunnels: []panel.Tunnel{}, Networks: true, Websites: true, Moves: true}
	if out.PublicKey, err = mesh.PublicKey(key); err != nil {
		return nil, err
	}
	s, ok, err := d.savedMesh()
	if err != nil || !ok || s.Off() {
		if ok {
			out.Version = s.Version
		}
		return out, err
	}
	out.Version, out.Address = s.Version, s.Address
	own, _ := netip.ParseAddr(s.Address)
	ping := d.MeshPing
	if ping == nil {
		ping = mesh.Ping
	}
	tunnels := make([]panel.Tunnel, len(s.Peers))
	var wg sync.WaitGroup
	for i, p := range s.Peers {
		tunnels[i].Server = p.Server
		to, err := netip.ParseAddr(p.Address)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rtt, err := ping(ctx, own, to, time.Second); err == nil {
				tunnels[i].Reachable = true
				tunnels[i].RoundTripMicros = max(rtt.Microseconds(), 1)
			}
		}()
	}
	wg.Wait()
	// The handshake times come after the echo requests, so they show a new handshake.
	text, _ := d.Run.Run(ctx, "wg", "show", mesh.Interface, "latest-handshakes")
	handshakes := mesh.Handshakes(text)
	for i, p := range s.Peers {
		tunnels[i].HandshakeAt = handshakes[p.PublicKey]
	}
	slices.SortFunc(tunnels, func(a, b panel.Tunnel) int { return strings.Compare(a.Server, b.Server) })
	out.Tunnels = tunnels
	return out, nil
}

// meshReport gives the mesh part of a status report, or nil when the agent cannot read it.
func (d *Deps) meshReport(ctx context.Context) *panel.Mesh {
	status, err := d.meshStatus(ctx)
	if err != nil {
		slog.Warn("read the state of the mesh", "err", err)
		return nil
	}
	return status
}
