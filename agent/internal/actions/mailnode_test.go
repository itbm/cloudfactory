package actions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/mailcluster"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const (
	mailDBImage   = "ghcr.io/itbm/mail-db:0.2.0"
	mailKeepImage = "ghcr.io/itbm/mail-keep:0.2.0"
	mailSecret    = "0123456789abcdef0123456789abcdef"
	// mailPodmanPrefix is the start of each Podman command of the Unix user of the mail server.
	mailPodmanPrefix = "runuser -u cf-mail -- env XDG_RUNTIME_DIR=/run/user/988 podman "
)

// mailNodeDeps gives a worker and a root helper of a server that can be a member of a mail
// cluster: it has the firewall of the installer.
func mailNodeDeps(t *testing.T) (*Deps, *execx.Fake) {
	t.Helper()
	d, f, _ := deps(t)
	f.MakeDirs = true
	dir := t.TempDir()
	both(d, func(d *Deps) {
		d.MailImage, d.MailDBImage, d.MailKeepImage = mailImage, mailDBImage, mailKeepImage
		d.MailHome = filepath.Join(dir, "home")
		d.MailConfigDir = filepath.Join(dir, "etc")
		d.WireGuardDir = filepath.Join(dir, "wireguard")
		d.MailNodePoll = time.Millisecond
		d.MailNodeWait = 2 * time.Second
	})
	if err := os.WriteFile(d.FirewallFile, []byte("table inet cloudfactory {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d, f
}

// mailClusterOf gives a cluster of three members. Member 1 has the keys of a server. The
// other members have new keys.
func mailClusterOf(t *testing.T, keys mailNodeKeys) mailcluster.Cluster {
	t.Helper()
	c := mailcluster.Cluster{Name: "mail-1", Hostname: "mail.example.net", Self: 1}
	for i := 1; i <= 3; i++ {
		m := mailcluster.Member{ID: i, Name: fmt.Sprintf("mail%d.example.net", i), Endpoint: fmt.Sprintf("203.0.113.%d", i), Address: fmt.Sprintf("10.77.25.%d", i), Region: i - 1,
			WireGuardKey: keys.WireGuardKey, KeepKey: keys.KeepKey, NodeKey: keys.NodeKey}
		if i > 1 {
			var err error
			if _, m.WireGuardKey, err = mailcluster.NewWireGuardKey(); err != nil {
				t.Fatal(err)
			}
			if _, m.KeepKey, err = mailcluster.NewKey(); err != nil {
				t.Fatal(err)
			}
			if _, m.NodeKey, err = mailcluster.NewKey(); err != nil {
				t.Fatal(err)
			}
		}
		c.Members = append(c.Members, m)
	}
	return c
}

func mailNodeInOf(c mailcluster.Cluster, bootstrap bool) mailNodeIn {
	return mailNodeIn{Cluster: c, DBPassword: mailSecret, KeepSecret: strings.Repeat("ab", 16), PeerSecret: strings.Repeat("cd", 16), Bootstrap: bootstrap}
}

func TestTheRootHelperMakesAMemberOfAMailCluster(t *testing.T) {
	d, f := mailNodeDeps(t)
	ctx := context.Background()
	root := rootOf(d)
	keys, err := d.prepareMailNode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := d.prepareMailNode(ctx); err != nil || again != keys {
		t.Fatalf("the second time gives different keys: %v", err)
	}
	if fi, err := os.Stat(root.mailNetworkKey()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key of the private network: %v, %v", fi, err)
	}
	cluster := mailClusterOf(t, keys)
	set, err := d.setMailNode(ctx, mailNodeInOf(cluster, true))
	if err != nil {
		t.Fatal(err)
	}
	if !set.Keep || !set.DB {
		t.Fatalf("the first time: %+v", set)
	}
	// The private network. Its settings do not have the private key.
	network, _ := os.ReadFile(filepath.Join(root.WireGuardDir, "cf-mail.conf"))
	private, _ := os.ReadFile(root.mailNetworkKey())
	if strings.Contains(string(network), strings.TrimSpace(string(private))) {
		t.Error("the settings of the private network have the private key")
	}
	for _, want := range []string{"Address = 10.77.25.1/32", "Endpoint = 203.0.113.2:51877", "AllowedIPs = 10.77.25.3/32", "PublicKey = " + cluster.Members[1].WireGuardKey} {
		if !strings.Contains(string(network), want) {
			t.Errorf("the private network does not have %q:\n%s", want, network)
		}
	}
	for _, want := range []string{"systemctl enable wg-quick@cf-mail.service", "systemctl restart wg-quick@cf-mail.service", "nft -f " + root.FirewallFile} {
		if !f.Called(want) {
			t.Errorf("the root helper did not run %q:\n%s", want, strings.Join(f.Calls, "\n"))
		}
	}
	rules, _ := os.ReadFile(filepath.Join(root.FirewallDir, mailFirewallFile))
	for _, want := range []string{`iifname "cf-mail" ip saddr { 10.77.25.1, 10.77.25.2, 10.77.25.3 } tcp dport { 4567, 4568, 4444, 7000, 7100 } accept`, "udp dport 51877 accept", "meta skuid != { 0, "} {
		if !strings.Contains(string(rules), want) {
			t.Errorf("the firewall rules do not have %q:\n%s", want, rules)
		}
	}
	// The first node starts a new database cluster one time only.
	dir, _ := root.Host.UnitDir(host.MailUser)
	unit := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	for _, want := range []string{"Image=" + mailDBImage, "Network=host", "Exec=--wsrep-new-cluster", "DropCapability=all", "ReadOnly=true", "User=999"} {
		if !strings.Contains(unit(mailDBUnit), want) {
			t.Errorf("the unit of the database does not have %q:\n%s", want, unit(mailDBUnit))
		}
	}
	for _, want := range []string{"Image=" + mailKeepImage, "--start bootstrap", "Volume=" + root.mailKeepDir() + ":/etc/cf-keep:ro,Z"} {
		if !strings.Contains(unit(mailKeepUnit), want) {
			t.Errorf("the unit of the cluster store does not have %q:\n%s", want, unit(mailKeepUnit))
		}
	}
	if quadlet.MailClusterDir != mailcluster.KeepDir {
		t.Error("the units and the settings name different directories")
	}
	for name, mode := range map[string]os.FileMode{"keep/keep.key": 0o640, "keep/cluster.secret": 0o640, "keep/peer.secret": 0o640, "node/node.key": 0o640, "secrets.json": 0o600, "db.env": 0o640} {
		if fi, err := os.Stat(filepath.Join(root.mailClusterDir(), name)); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v", name, fi, err)
		}
	}
	if set, err := d.setMailNode(ctx, mailNodeInOf(cluster, true)); err != nil || set.Keep || set.DB {
		t.Fatalf("the second time: %+v, %v", set, err)
	}
	if changed, err := d.mailNodeStarted(ctx); err != nil || !changed {
		t.Fatalf("mailNodeStarted: %v, %v", changed, err)
	}
	if strings.Contains(unit(mailDBUnit), "wsrep-new-cluster") || !strings.Contains(unit(mailKeepUnit), "--start rejoin") {
		t.Errorf("a node that started keeps the options of a new cluster:\n%s\n%s", unit(mailDBUnit), unit(mailKeepUnit))
	}
	// A second request to start a new cluster does not split the cluster.
	if _, err := d.setMailNode(ctx, mailNodeInOf(cluster, true)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unit(mailDBUnit), "wsrep-new-cluster") {
		t.Error("the node starts a new cluster a second time")
	}

	// The mail server of a member uses the database node and the cluster store of the member.
	prepared, err := d.prepareMail(ctx, "mail1.example.net")
	if err != nil || !prepared.Setup {
		t.Fatalf("prepareMail: %+v, %v", prepared, err)
	}
	config, _ := os.ReadFile(filepath.Join(root.mailConfigDir(), "config.json"))
	if !strings.Contains(string(config), `"@type":"MySql"`) || !strings.Contains(string(config), `"host":"10.77.25.1"`) || strings.Contains(string(config), mailSecret) {
		t.Errorf("config.json: %s", config)
	}
	env, _ := os.ReadFile(root.mailEnvFile())
	for _, want := range []string{"STALWART_HOSTNAME=mail1.example.net\n", "CF_MAIL_DB_PASSWORD=" + mailSecret + "\n", "CF_MAIL_KEEP_SECRET=" + strings.Repeat("ab", 16) + "\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the environment file does not have %q", want)
		}
	}
	for _, want := range []string{"Volume=" + root.mailNodeDir() + ":/etc/cf-keep:ro,Z", "After=cf-mail-db.service cf-mail-keep.service"} {
		if !strings.Contains(unit(mailUnit), want) {
			t.Errorf("the unit of the mail server does not have %q:\n%s", want, unit(mailUnit))
		}
	}
	info, err := d.mailInfo(ctx)
	if err != nil || info.Cluster == nil || info.Cluster.Hostname != "mail.example.net" {
		t.Fatalf("mailInfo: %+v, %v", info, err)
	}
}

func TestTheRootHelperRefusesAMemberThatIsNotSafe(t *testing.T) {
	d, f := mailNodeDeps(t)
	ctx := context.Background()
	keys, err := d.prepareMailNode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	good := mailClusterOf(t, keys)
	change := func(do func(in *mailNodeIn)) mailNodeIn {
		in := mailNodeInOf(good, false)
		in.Cluster.Members = append([]mailcluster.Member(nil), good.Members...)
		do(&in)
		return in
	}
	other := mailClusterOf(t, keys)
	for name, in := range map[string]mailNodeIn{
		"two members":                     change(func(in *mailNodeIn) { in.Cluster.Members = in.Cluster.Members[:2] }),
		"a secret with a line":            change(func(in *mailNodeIn) { in.DBPassword = mailSecret + "\nMARIADB_ROOT_HOST=%" }),
		"a short secret":                  change(func(in *mailNodeIn) { in.KeepSecret = "abc" }),
		"a name with a line":              change(func(in *mailNodeIn) { in.Cluster.Name = "a\nwsrep_provider=/tmp/x.so" }),
		"a member name with a line":       change(func(in *mailNodeIn) { in.Cluster.Members[1].Name = "a\nPostUp = id" }),
		"an endpoint with a command":      change(func(in *mailNodeIn) { in.Cluster.Members[1].Endpoint = "203.0.113.2\nPostUp = id" }),
		"a public address as the address": change(func(in *mailNodeIn) { in.Cluster.Members[1].Address = "8.8.8.8" }),
		"a WireGuard key with a line":     change(func(in *mailNodeIn) { in.Cluster.Members[1].WireGuardKey += "\nPostUp = id" }),
		"a key that is not a key": change(func(in *mailNodeIn) {
			in.Cluster.Members[2].KeepKey = "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"
		}),
		"the keys of a different server":     change(func(in *mailNodeIn) { in.Cluster.Members[0].KeepKey = other.Members[1].KeepKey }),
		"a server that is not a member":      change(func(in *mailNodeIn) { in.Cluster.Self = 9 }),
		"a host name of the cluster: a path": change(func(in *mailNodeIn) { in.Cluster.Hostname = "../etc" }),
	} {
		if _, err := d.setMailNode(ctx, in); err == nil {
			t.Errorf("setMailNode accepted %s", name)
		}
	}
	if f.Called("systemctl enable wg-quick") {
		t.Error("a refused member made a private network")
	}
	if _, err := d.setMailNode(ctx, mailNodeInOf(good, false)); err != nil {
		t.Fatal(err)
	}
	// A member of one cluster does not go to a different cluster.
	if _, err := d.setMailNode(ctx, change(func(in *mailNodeIn) { in.Cluster.Name = "mail-2" })); err == nil || !strings.Contains(err.Error(), "mail-1") {
		t.Errorf("a different cluster: %v", err)
	}
}

func TestAServerThatCannotBeAMember(t *testing.T) {
	ctx := context.Background()
	// A server with a firewall that the administrator manages.
	d, _ := mailNodeDeps(t)
	os.Remove(d.FirewallFile)
	if _, err := d.prepareMailNode(ctx); err == nil || !strings.Contains(err.Error(), "firewall") {
		t.Errorf("no firewall of the installer: %v", err)
	}
	// A server with no WireGuard.
	d, f := mailNodeDeps(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: "wg --version", Err: os.ErrNotExist})
	if _, err := d.prepareMailNode(ctx); err == nil || !strings.Contains(err.Error(), "wireguard-tools") {
		t.Errorf("no WireGuard: %v", err)
	}
	// A server with a mail server that has its own data.
	d, _ = mailNodeDeps(t)
	if _, err := d.prepareMail(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	if _, err := d.prepareMailNode(ctx); err == nil || !strings.Contains(err.Error(), "own data") {
		t.Errorf("a mail server with its own data: %v", err)
	}
}

func mailNodeRequest(c mailcluster.Cluster, bootstrap bool) *agentv1.SetMailNodeRequest {
	req := &agentv1.SetMailNodeRequest{JobId: jobID, Cluster: c.Name, Hostname: c.Hostname, Self: int32(c.Self),
		DbPassword: mailSecret, KeepSecret: strings.Repeat("ab", 16), PeerSecret: strings.Repeat("cd", 16), Bootstrap: bootstrap}
	for _, m := range c.Members {
		req.Members = append(req.Members, &agentv1.MailClusterMember{Id: int32(m.ID), Name: m.Name, Endpoint: m.Endpoint, Address: m.Address, Region: int32(m.Region),
			WireguardKey: m.WireGuardKey, KeepKey: m.KeepKey, NodeKey: m.NodeKey})
	}
	return req
}

func TestTheJobsOfAMailClusterMember(t *testing.T) {
	d, f := mailNodeDeps(t)
	ctx := context.Background()
	job, err := d.PrepareMailNode(&agentv1.PrepareMailNodeRequest{JobId: jobID})
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Steps[0].Do(ctx); err != nil {
		t.Fatal(err)
	}
	keys := job.Result()["keys"].(mailNodeKeys)
	if len(keys.WireGuardKey) != 44 || !strings.Contains(keys.KeepKey, "PUBLIC KEY") || keys.KeepKey == keys.NodeKey {
		t.Fatalf("keys: %+v", keys)
	}
	cluster := mailClusterOf(t, keys)

	// The server that starts a new cluster is ready when it is the only node.
	f.Rules = append(f.Rules, execx.Rule{Prefix: mailPodmanPrefix + "exec cf-mail-db", Output: "wsrep_cluster_size\t1\nwsrep_local_state_comment\tSynced\n"})
	job, err = d.SetMailNode(mailNodeRequest(cluster, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range job.Steps {
		if err := step.Do(ctx); err != nil {
			t.Fatalf("%s: %v\n%s", step.Name, err, strings.Join(f.Calls, "\n"))
		}
	}
	for _, want := range []string{mailPodmanPrefix + "image exists " + mailDBImage, mailPodmanPrefix + "image exists " + mailKeepImage,
		"systemctl --user --machine=cf-mail@ restart cf-mail-keep.service", "systemctl --user --machine=cf-mail@ restart cf-mail-db.service"} {
		if !f.Called(want) {
			t.Errorf("the job did not run %q:\n%s", want, strings.Join(f.Calls, "\n"))
		}
	}
	if status := job.Result()["database"].(mailNodeStatus); status != (mailNodeStatus{Size: 1, State: "Synced"}) {
		t.Errorf("the result: %+v", status)
	}
	dir, _ := rootOf(d).Host.UnitDir(host.MailUser)
	if unit, _ := os.ReadFile(filepath.Join(dir, mailDBUnit)); strings.Contains(string(unit), "wsrep-new-cluster") {
		t.Error("the node that started keeps the option of a new cluster")
	}

	// A server that joins a cluster is not ready while it is the only node.
	both(d, func(d *Deps) { d.MailNodeWait = 20 * time.Millisecond })
	job, err = d.SetMailNode(mailNodeRequest(cluster, false))
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range job.Steps {
		err := step.Do(ctx)
		if i < 2 && err != nil {
			t.Fatalf("%s: %v\n%s", step.Name, err, strings.Join(f.Calls, "\n"))
		}
		if i == 2 && (err == nil || !strings.Contains(err.Error(), "UDP port 51877")) {
			t.Errorf("a node that is alone: %v", err)
		}
	}

	// The job checks its values before it starts.
	bad := mailNodeRequest(cluster, false)
	bad.Members = bad.Members[:2]
	if _, err := d.SetMailNode(bad); err == nil {
		t.Error("SetMailNode accepted two members")
	}
}

// The firewall rules of a member must have the rules of the cluster before the rules that
// refuse private networks.
func TestTheFirewallRulesOfAMember(t *testing.T) {
	keys := mailNodeKeys{}
	var err error
	if _, keys.WireGuardKey, err = mailcluster.NewWireGuardKey(); err != nil {
		t.Fatal(err)
	}
	_, keys.KeepKey, _ = mailcluster.NewKey()
	_, keys.NodeKey, _ = mailcluster.NewKey()
	cluster := mailClusterOf(t, keys)
	rules := MailFirewall(985, 0, &cluster)
	accept, refuse := strings.Index(rules, "ip daddr 10.77.25.1 tcp dport 19026 accept"), strings.Index(rules, "fib daddr type { local, broadcast, multicast } jump refuse")
	if accept < 0 || refuse < 0 || accept > refuse {
		t.Errorf("the rules of the cluster are not before the rules that refuse:\n%s", rules)
	}
	if !strings.Contains(rules, "th dport { 4567, 4568, 4444, 7000, 7100, 19026 } meta skuid != { 0, 985 } jump refuse") {
		t.Errorf("a different user of this server can connect to the database:\n%s", rules)
	}
	checkFirewall(t, rules)
}
