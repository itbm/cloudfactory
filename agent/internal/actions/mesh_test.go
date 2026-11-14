package actions

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/mesh"
	"github.com/itbm/cloudfactory/agent/internal/panel"
)

const (
	meshPeerB  = "01HZX3K9PQ7R5T8V2W4Y6Z0ABC"
	meshPeerC  = "01HZX3K9PQ7R5T8V2W4Y6Z0ABD"
	meshPublic = "8SFd5kQjgQrl8xOImYUfj5aJzucT/q9wJVtqBywVY2I="
	meshOther  = "WFA+DXGA/AXqy58eOAuS2fBQU67RSXe1DUy6jVj7+x4="
	meshShared = "aCXXF4PGbVcwBKVtU98fLE5YBpIGmWwVSFnv2N9lLHg="
)

func meshRequest(version int64) *agentv1.SetMeshRequest {
	return &agentv1.SetMeshRequest{JobId: jobID, Mesh: &agentv1.Mesh{
		Version: version, Range: "10.207.0.0/16", Address: "10.207.0.1", Port: 51820,
		Peers: []*agentv1.MeshPeer{
			{Server: meshPeerB, PublicKey: meshPublic, PresharedKey: meshShared, Endpoint: "203.0.113.2:51820", Address: "10.207.0.2"},
			{Server: meshPeerC, PublicKey: meshOther, PresharedKey: meshShared, Endpoint: "[2001:db8::3]:51820", Address: "10.207.0.3"},
		},
	}}
}

// meshServer gives a worker on a server with the firewall file of the installer of an
// earlier release, with no WireGuard interface, and with one peer that answers.
func meshServer(t *testing.T) (*Deps, *execx.Fake) {
	t.Helper()
	d, f, _ := deps(t)
	b, err := os.ReadFile("testdata/firewall-0.1.9.nft")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.FirewallFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
	link := false
	f.Rules = append(f.Rules,
		execx.Rule{Prefix: "ip link show dev cf-mesh", Do: func() error {
			if link {
				return nil
			}
			return errors.New("Device \"cf-mesh\" does not exist.")
		}},
		execx.Rule{Prefix: "ip link add dev cf-mesh", Do: func() error { link = true; return nil }},
		execx.Rule{Prefix: "ip link delete dev cf-mesh", Do: func() error { link = false; return nil }},
		execx.Rule{Prefix: "wg show cf-mesh latest-handshakes", Output: meshPublic + "\t1794000000\n" + meshOther + "\t0\n"},
	)
	both(d, func(d *Deps) {
		d.MeshPing = func(_ context.Context, from, to netip.Addr, _ time.Duration) (time.Duration, error) {
			if from.String() == "10.207.0.1" && to.String() == "10.207.0.2" {
				return 1500 * time.Microsecond, nil
			}
			return 0, errors.New("no answer")
		}
	})
	return d, f
}

func callIndex(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestSetMesh(t *testing.T) {
	d, f := meshServer(t)
	root := rootOf(d)
	job, err := d.SetMesh(meshRequest(7))
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if job.Key != meshJobKey {
		t.Errorf("the key of the job = %q", job.Key)
	}
	conf := filepath.Join(root.MeshDir, "cf-mesh.conf")
	// The firewall opens the tunnel port before the interface starts.
	sequence := []string{
		"nft -c -f " + root.FirewallFile + ".new",
		"nft -f " + root.FirewallFile,
		"ip link add dev cf-mesh type wireguard",
		"wg syncconf cf-mesh " + conf,
		"ip address replace 10.207.0.1/32 dev cf-mesh",
		"ip link set dev cf-mesh mtu 1380 up",
		"ip route replace 10.207.0.0/16 dev cf-mesh src 10.207.0.1",
	}
	last := -1
	for _, command := range sequence {
		i := callIndex(f.Calls, command)
		if i < 0 {
			t.Fatalf("no command %q in:\n%s", command, strings.Join(f.Calls, "\n"))
		}
		// The rules of the database servers also load the firewall file, before.
		if command == "nft -f "+root.FirewallFile {
			for j := len(f.Calls) - 1; j >= 0; j-- {
				if f.Calls[j] == command {
					i = j
					break
				}
			}
		}
		if i < last {
			t.Errorf("the command %q is too early:\n%s", command, strings.Join(f.Calls, "\n"))
		}
		last = i
	}

	// The files: the key and the configuration are for root only.
	for _, name := range []string{"private.key", "mesh.json", "cf-mesh.conf"} {
		fi, err := os.Stat(filepath.Join(root.MeshDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has the mode %v", name, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(root.MeshDir); fi.Mode().Perm() != 0o700 {
		t.Errorf("the mesh directory has the mode %v", fi.Mode().Perm())
	}
	key, _ := os.ReadFile(filepath.Join(root.MeshDir, "private.key"))
	public, err := mesh.PublicKey(strings.TrimSpace(string(key)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(conf)
	for _, text := range []string{"PrivateKey = " + strings.TrimSpace(string(key)), "ListenPort = 51820", "PublicKey = " + meshPublic, "AllowedIPs = 10.207.0.2/32", "Endpoint = [2001:db8::3]:51820"} {
		if !strings.Contains(string(b), text) {
			t.Errorf("no %q in the configuration:\n%s", text, b)
		}
	}
	rules, _ := os.ReadFile(root.FirewallFile)
	if !strings.Contains(string(rules), `iifname "cf-mesh" jump mesh_input`) || !strings.Contains(string(rules), "set database_access {") {
		t.Errorf("the firewall file has no rules of the mesh:\n%s", rules)
	}
	elements, _ := os.ReadFile(filepath.Join(root.FirewallDir, "mesh.nft"))
	for _, text := range []string{"mesh_port { 51820 }", "mesh_range { 10.207.0.0/16 }", "mesh_endpoints4 { 203.0.113.2 }", "mesh_endpoints6 { 2001:db8::3 }"} {
		if !strings.Contains(string(elements), text) {
			t.Errorf("no %q in the elements:\n%s", text, elements)
		}
	}

	// The result: the public key and the state of each tunnel. It has no private key.
	status, ok := job.Result()["mesh"].(*panel.Mesh)
	if !ok || status == nil {
		t.Fatalf("the result = %v", job.Result())
	}
	if status.PublicKey != public || status.Version != 7 || status.Address != "10.207.0.1" || len(status.Tunnels) != 2 {
		t.Fatalf("the state = %+v", status)
	}
	if got := status.Tunnels[0]; got != (panel.Tunnel{Server: meshPeerB, HandshakeAt: 1794000000, Reachable: true, RoundTripMicros: 1500}) {
		t.Errorf("the tunnel to the first peer = %+v", got)
	}
	if got := status.Tunnels[1]; got != (panel.Tunnel{Server: meshPeerC}) {
		t.Errorf("the tunnel to the second peer = %+v", got)
	}

	// The status report has the same part.
	var r panel.Report
	d.FillReport(context.Background(), &r)
	if r.Mesh == nil || r.Mesh.PublicKey != public || len(r.Mesh.Tunnels) != 2 {
		t.Errorf("the report = %+v", r.Mesh)
	}

	// The same job again changes no file and makes no new interface.
	f.Calls = nil
	job, err = d.SetMesh(meshRequest(7))
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if f.Called("ip link add") || f.Called("nft -f "+root.FirewallFile) || f.Called("ip address flush") {
		t.Errorf("the second job changes the server:\n%s", strings.Join(f.Calls, "\n"))
	}
	if !f.Called("wg syncconf cf-mesh") || !f.Called("nft -f "+filepath.Join(root.FirewallDir, "mesh.nft")) {
		t.Errorf("the second job does not apply the state:\n%s", strings.Join(f.Calls, "\n"))
	}
}

func TestSetMeshRefusesAFaultyState(t *testing.T) {
	d, f := meshServer(t)
	for name, change := range map[string]func(m *agentv1.Mesh){
		"no state":                 nil,
		"a public range":           func(m *agentv1.Mesh) { m.Range = "0.0.0.0/0" },
		"a key with a second line": func(m *agentv1.Mesh) { m.Peers[0].PublicKey += "\nPostUp = id" },
		"a private tunnel address": func(m *agentv1.Mesh) { m.Peers[0].Endpoint = "169.254.77.1:19432" },
		"a peer outside the range": func(m *agentv1.Mesh) { m.Peers[0].Address = "169.254.80.5" },
	} {
		req := meshRequest(1)
		if change == nil {
			req.Mesh = nil
		} else {
			change(req.Mesh)
		}
		if _, err := d.SetMesh(req); err == nil {
			t.Errorf("the worker accepts %s", name)
		}
		// A worker that an attacker controls sends the state directly to the root helper.
		if req.Mesh != nil {
			if err := d.Gate.Call(context.Background(), opMeshApply, meshIn{State: meshFromRequest(req.Mesh)}, nil); err == nil {
				t.Errorf("the root helper accepts %s", name)
			}
		}
	}
	if len(f.Calls) > 0 {
		t.Errorf("a refused state changes the server:\n%s", strings.Join(f.Calls, "\n"))
	}
	if _, err := os.Stat(filepath.Join(rootOf(d).MeshDir, "mesh.json")); !os.IsNotExist(err) {
		t.Errorf("a refused state is in a file: %v", err)
	}
}

func TestSetMeshRefusesAnOlderVersion(t *testing.T) {
	d, _ := meshServer(t)
	if err := run(t)(d.SetMesh(meshRequest(7))); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.SetMesh(meshRequest(6))); err == nil || !strings.Contains(err.Error(), "older version 6") {
		t.Errorf("the error of an older version = %v", err)
	}
}

func TestSetMeshWithANewAddress(t *testing.T) {
	d, f := meshServer(t)
	if err := run(t)(d.SetMesh(meshRequest(1))); err != nil {
		t.Fatal(err)
	}
	req := meshRequest(2)
	req.Mesh.Address = "10.207.0.9"
	f.Calls = nil
	if err := run(t)(d.SetMesh(req)); err != nil {
		t.Fatal(err)
	}
	flush, replace := callIndex(f.Calls, "ip address flush dev cf-mesh"), callIndex(f.Calls, "ip address replace 10.207.0.9/32 dev cf-mesh")
	if flush < 0 || !f.Called("ip route flush dev cf-mesh") || replace < flush {
		t.Errorf("the old address stays:\n%s", strings.Join(f.Calls, "\n"))
	}
}

func TestSetMeshTakesTheServerOutOfTheMesh(t *testing.T) {
	d, f := meshServer(t)
	root := rootOf(d)
	if err := run(t)(d.SetMesh(meshRequest(1))); err != nil {
		t.Fatal(err)
	}
	job, err := d.SetMesh(&agentv1.SetMeshRequest{JobId: jobID, Mesh: &agentv1.Mesh{Version: 2}})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if !f.Called("ip link delete dev cf-mesh") {
		t.Errorf("the interface stays:\n%s", strings.Join(f.Calls, "\n"))
	}
	if _, err := os.Stat(filepath.Join(root.MeshDir, "cf-mesh.conf")); !os.IsNotExist(err) {
		t.Errorf("the configuration stays: %v", err)
	}
	elements, _ := os.ReadFile(filepath.Join(root.FirewallDir, "mesh.nft"))
	if strings.Contains(string(elements), "add element") {
		t.Errorf("the firewall keeps elements:\n%s", elements)
	}
	// The server keeps its key, and reports it.
	status := job.Result()["mesh"].(*panel.Mesh)
	if status.PublicKey == "" || status.Version != 2 || status.Address != "" || len(status.Tunnels) != 0 {
		t.Errorf("the state = %+v", status)
	}
}

func TestRotateMeshKey(t *testing.T) {
	d, f := meshServer(t)
	root := rootOf(d)
	if err := run(t)(d.SetMesh(meshRequest(1))); err != nil {
		t.Fatal(err)
	}
	before, _ := d.meshStatus(context.Background())
	f.Calls = nil
	job, err := d.RotateMeshKey(&agentv1.RotateMeshKeyRequest{JobId: jobID})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	public, _ := job.Result()["publicKey"].(string)
	if mesh.CheckKey(public) != nil || public == before.PublicKey {
		t.Fatalf("the new public key = %q, the old key = %q", public, before.PublicKey)
	}
	key, _ := os.ReadFile(filepath.Join(root.MeshDir, "private.key"))
	if got, _ := mesh.PublicKey(strings.TrimSpace(string(key))); got != public {
		t.Errorf("the key file has a different key")
	}
	if strings.Contains(public, strings.TrimSpace(string(key))) {
		t.Error("the result has the private key")
	}
	conf, _ := os.ReadFile(filepath.Join(root.MeshDir, "cf-mesh.conf"))
	if !strings.Contains(string(conf), "PrivateKey = "+strings.TrimSpace(string(key))) || !f.Called("wg syncconf cf-mesh") {
		t.Errorf("the interface does not have the new key:\n%s", strings.Join(f.Calls, "\n"))
	}
}

func TestRestoreMeshAfterARestart(t *testing.T) {
	d, f := meshServer(t)
	root := rootOf(d)
	// A server that was never in the mesh: nothing to do.
	if err := root.RestoreMesh(context.Background()); err != nil || len(f.Calls) > 0 {
		t.Fatalf("RestoreMesh with no state: %v, %v", err, f.Calls)
	}
	if err := run(t)(d.SetMesh(meshRequest(1))); err != nil {
		t.Fatal(err)
	}
	// The restart removes the interface.
	if _, err := f.Run(context.Background(), "ip", "link", "delete", "dev", "cf-mesh"); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	if err := root.RestoreMesh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"ip link add dev cf-mesh type wireguard", "wg syncconf cf-mesh", "ip link set dev cf-mesh mtu 1380 up", "ip route replace 10.207.0.0/16 dev cf-mesh src 10.207.0.1"} {
		if !f.Called(command) {
			t.Errorf("no command %q:\n%s", command, strings.Join(f.Calls, "\n"))
		}
	}
	if f.Called("apt-get") {
		t.Error("the start of the helper installs a package")
	}
	if err := d.RestoreMesh(context.Background()); err == nil {
		t.Error("the worker can call RestoreMesh")
	}
}

func TestSetMeshInstallsTheToolsOfAnEarlierRelease(t *testing.T) {
	d, f := meshServer(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: "wg --version", Err: errors.New("executable file not found")})
	if err := run(t)(d.SetMesh(meshRequest(1))); err != nil {
		t.Fatal(err)
	}
	if !f.Called("apt-get install -y -q --no-install-recommends wireguard-tools") {
		t.Errorf("no installation:\n%s", strings.Join(f.Calls, "\n"))
	}
}

// The worker has no command for the tunnels: it cannot read the keys, and it cannot change
// the interface with its own arguments.
func TestTheWorkerHasNoTunnelCommand(t *testing.T) {
	d, _ := boundary(t)
	root := rootOf(d)
	for _, command := range []string{
		"wg show cf-mesh dump",
		"wg showconf cf-mesh",
		"wg show cf-mesh latest-handshakes",
		"wg set cf-mesh private-key /tmp/key",
		"wg syncconf cf-mesh /tmp/conf",
		"ip link delete dev cf-mesh",
		"ip route replace 0.0.0.0/0 dev cf-mesh",
		"cat " + filepath.Join(root.MeshDir, "private.key"),
	} {
		if err := root.allowArgv(strings.Fields(command)); err == nil {
			t.Errorf("the policy accepts %q", command)
		}
	}
}
