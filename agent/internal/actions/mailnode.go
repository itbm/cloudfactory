package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/mailcluster"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// A member of a mail cluster (package mailcluster). A member has three containers of the
// Unix user of the mail server: the mail server, a node of the database cluster and a member
// of the cluster store. The members connect through a private network (WireGuard).
//
// The panel makes a cluster in three steps:
//
//  1. PrepareMailNode on each server. The server makes its keys and gives the public keys.
//  2. SetMailNode on each server, with the public keys of all members. The first server
//     starts the database cluster. The other servers join it.
//  3. SetMailServer on each server. The mail server then uses the database node and the
//     cluster store of its server.
//
// The worker of the agent is not root. The root helper checks each value (Cluster.Validate),
// makes the keys, and writes the files, the private network and the firewall rules.

const (
	opMailNodePrepare = "mail.node.prepare"
	opMailNodeSet     = "mail.node.set"
	opMailNodeStatus  = "mail.node.status"
	opMailNodeStarted = "mail.node.started"

	mailDBUnit      = "cf-mail-db.container"
	mailDBService   = "cf-mail-db.service"
	mailKeepUnit    = "cf-mail-keep.container"
	mailKeepService = "cf-mail-keep.service"
	// mailNetworkService is the system service of the private network.
	mailNetworkService = "wg-quick@" + mailcluster.Interface + ".service"
	// mailNodeWait is the maximum time that a job waits for the database node. A node that
	// joins a cluster with much mail gets a full copy of the data first.
	mailNodeWait = 30 * time.Minute
	// mailStateSynced is the state of a database node that has all the data.
	mailStateSynced = "Synced"
)

type (
	// mailNodeKeys are the public keys of a member.
	mailNodeKeys struct {
		WireGuardKey string `json:"wireguardKey"`
		KeepKey      string `json:"keepKey"`
		NodeKey      string `json:"nodeKey"`
	}
	mailNodeIn struct {
		Cluster mailcluster.Cluster
		// DBPassword is the password of the database user of the mail servers. KeepSecret and
		// PeerSecret are the two secrets of the cluster store.
		DBPassword, KeepSecret, PeerSecret string
		// Bootstrap is true for the server that starts a new database cluster.
		Bootstrap bool
	}
	// mailNodeSet tells the worker which services must start again.
	mailNodeSet struct {
		Keep, DB bool
	}
	mailNodeStatus struct {
		// Size is the number of database nodes that this node sees, with itself.
		Size int `json:"size"`
		// State is "Synced" when the node has all the data.
		State string `json:"state"`
	}
	mailSecrets struct {
		DBPassword, KeepSecret, PeerSecret string
	}
)

func (d *Deps) mailClusterDir() string  { return filepath.Join(d.mailConfigDir(), "cluster") }
func (d *Deps) mailClusterFile() string { return filepath.Join(d.mailClusterDir(), "cluster.json") }
func (d *Deps) mailKeepDir() string     { return filepath.Join(d.mailClusterDir(), "keep") }
func (d *Deps) mailNodeDir() string     { return filepath.Join(d.mailClusterDir(), "node") }
func (d *Deps) mailNetworkKey() string  { return filepath.Join(d.mailClusterDir(), "wireguard.key") }

func (d *Deps) wireGuardDir() string {
	if d.WireGuardDir != "" {
		return d.WireGuardDir
	}
	return "/etc/wireguard"
}

func (d *Deps) mailNodeRootOps() map[string]gate.Handler {
	return map[string]gate.Handler{
		opMailNodePrepare: gate.Op(func(ctx context.Context, _ gate.None) (mailNodeKeys, error) { return d.prepareMailNode(ctx) }),
		opMailNodeSet:     gate.Op(func(ctx context.Context, in mailNodeIn) (mailNodeSet, error) { return d.setMailNode(ctx, in) }),
		opMailNodeStatus:  gate.Op(func(ctx context.Context, _ gate.None) (mailNodeStatus, error) { return d.mailNodeStatus(ctx) }),
		opMailNodeStarted: gate.Op(func(ctx context.Context, _ gate.None) (bool, error) { return d.mailNodeStarted(ctx) }),
	}
}

// mailCluster reads the cluster of this server. A server that is not a member gives nil.
func (d *Deps) mailCluster() (*mailcluster.Cluster, error) {
	b, err := os.ReadFile(d.mailClusterFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c mailcluster.Cluster
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("the file of the mail cluster: %w", err)
	}
	return &c, c.Validate()
}

// mailSecrets reads the secrets of the cluster of this server.
func (d *Deps) mailSecrets() (s mailSecrets, err error) {
	b, err := os.ReadFile(filepath.Join(d.mailClusterDir(), "secrets.json"))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// groupFile writes a file that root owns and that the group of the mail server can read.
func groupFile(file, content string, gid int) (bool, error) {
	changed, err := host.WriteFile(file, content, 0o640)
	if err != nil {
		return false, err
	}
	return changed, chownRoot(file, gid)
}

// groupDir makes a directory that root owns and that the group of the mail server can read.
func groupDir(dir string, gid int) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return err
	}
	return chownRoot(dir, gid)
}

// prepareMailNode makes the keys of this server for a mail cluster, and gives the public
// keys. It keeps keys that exist, so the panel can ask again.
func (d *Deps) prepareMailNode(ctx context.Context) (keys mailNodeKeys, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailNodePrepare, nil, &keys)
		return keys, err
	}
	if d.MailImage == "" || d.MailDBImage == "" || d.MailKeepImage == "" {
		return keys, errors.New("the root helper does not have the images of a mail cluster")
	}
	// The database and the cluster store use the network of the host. Only the firewall of
	// the installer keeps their ports for the members.
	if _, err := os.Stat(d.firewallFile()); err != nil {
		return keys, errors.New("a member of a mail cluster needs the firewall of the Cloudfactory installer. This server has a firewall that the administrator manages")
	}
	if _, err := d.Run.Run(ctx, "wg", "--version"); err != nil {
		return keys, errors.New("this server does not have WireGuard. Install the package wireguard-tools, then do the step again")
	}
	cluster, err := d.mailCluster()
	if err != nil {
		return keys, err
	}
	if _, err := os.Stat(d.mailHostFile()); err == nil && cluster == nil {
		return keys, errors.New("this server has a mail server with its own data. Cloudfactory cannot move that data into a cluster")
	}
	if err := d.Host.EnsureServiceUser(ctx, host.MailUser, d.mailHome()); err != nil {
		return keys, err
	}
	_, gid, err := d.Host.FileOwner(host.MailUser)
	if err != nil {
		return keys, err
	}
	if err := os.MkdirAll(d.mailClusterDir(), 0o755); err != nil {
		return keys, err
	}
	for _, dir := range []string{d.mailKeepDir(), d.mailNodeDir()} {
		if err := groupDir(dir, gid); err != nil {
			return keys, err
		}
	}
	// The key of the private network. Only root reads it.
	network, err := os.ReadFile(d.mailNetworkKey())
	if os.IsNotExist(err) {
		private, _, err := mailcluster.NewWireGuardKey()
		if err != nil {
			return keys, err
		}
		if _, err := host.WriteFile(d.mailNetworkKey(), private+"\n", 0o600); err != nil {
			return keys, err
		}
		network = []byte(private)
	} else if err != nil {
		return keys, err
	}
	if keys.WireGuardKey, err = mailcluster.WireGuardPublic(string(network)); err != nil {
		return keys, fmt.Errorf("the key of the private network: %w", err)
	}
	// The key of the cluster store, and the key of the mail server as a client of the
	// cluster store.
	for file, public := range map[string]*string{filepath.Join(d.mailKeepDir(), "keep.key"): &keys.KeepKey, filepath.Join(d.mailNodeDir(), "node.key"): &keys.NodeKey} {
		private, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			made, _, err := mailcluster.NewKey()
			if err != nil {
				return keys, err
			}
			if _, err := groupFile(file, made, gid); err != nil {
				return keys, err
			}
			private = []byte(made)
		} else if err != nil {
			return keys, err
		}
		if *public, err = mailcluster.PublicOf(string(private)); err != nil {
			return keys, fmt.Errorf("%s: %w", filepath.Base(file), err)
		}
	}
	return keys, nil
}

// setMailNode makes this server a member of a cluster: the private network, the firewall
// rules, and the units of the cluster store and of the database node.
func (d *Deps) setMailNode(ctx context.Context, in mailNodeIn) (out mailNodeSet, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailNodeSet, in, &out)
		return out, err
	}
	if err := in.Cluster.Validate(); err != nil {
		return out, err
	}
	for name, secret := range map[string]string{"password of the database": in.DBPassword, "secret of the cluster store": in.KeepSecret, "second secret of the cluster store": in.PeerSecret} {
		if !mailcluster.SecretRule.MatchString(secret) {
			return out, fmt.Errorf("the %s is not 32 hexadecimal digits", name)
		}
	}
	current, err := d.mailCluster()
	if err != nil {
		return out, err
	}
	if current != nil && current.Name != in.Cluster.Name {
		return out, fmt.Errorf("this server is a member of the mail cluster %q", current.Name)
	}
	// The panel sends the public keys that this server gave. Different keys show that the
	// keys of this server are new, or that the panel has the keys of a different server.
	keys, err := d.prepareMailNode(ctx)
	if err != nil {
		return out, err
	}
	me := in.Cluster.Me()
	if me.WireGuardKey != keys.WireGuardKey || strings.TrimSpace(me.KeepKey) != strings.TrimSpace(keys.KeepKey) || strings.TrimSpace(me.NodeKey) != strings.TrimSpace(keys.NodeKey) {
		return out, errors.New("the keys of this member are not the keys of this server. Prepare the server again")
	}
	_, gid, err := d.Host.FileOwner(host.MailUser)
	if err != nil {
		return out, err
	}
	c := in.Cluster
	text, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return out, err
	}
	secrets, err := json.Marshal(mailSecrets{DBPassword: in.DBPassword, KeepSecret: in.KeepSecret, PeerSecret: in.PeerSecret})
	if err != nil {
		return out, err
	}
	if _, err := host.WriteFile(filepath.Join(d.mailClusterDir(), "secrets.json"), string(secrets)+"\n", 0o600); err != nil {
		return out, err
	}
	if _, err := host.WriteFile(d.mailClusterFile(), string(text)+"\n", 0o644); err != nil {
		return out, err
	}

	// The private network.
	if err := os.MkdirAll(d.wireGuardDir(), 0o700); err != nil {
		return out, err
	}
	network, err := host.WriteFile(filepath.Join(d.wireGuardDir(), mailcluster.Interface+".conf"), c.WireGuard(d.mailNetworkKey()), 0o600)
	if err != nil {
		return out, err
	}
	if _, err := d.Run.Run(ctx, "systemctl", "enable", mailNetworkService); err != nil {
		return out, err
	}
	verb := "start"
	if network {
		verb = "restart"
	}
	if _, err := d.Run.Run(ctx, "systemctl", verb, mailNetworkService); err != nil {
		return out, fmt.Errorf("start the private network: %w", err)
	}
	if err := d.openMailPorts(ctx); err != nil {
		return out, err
	}

	// The cluster store.
	keep := d.mailKeepDir()
	for name, content := range map[string]string{
		"keep.toml": c.Keep(1), "cluster.secret": in.KeepSecret + "\n", "peer.secret": in.PeerSecret + "\n",
		"clients.pem": c.ClientKeys(), "servers.pem": c.ServerKeys(),
	} {
		changed, err := groupFile(filepath.Join(keep, name), content, gid)
		if err != nil {
			return out, err
		}
		out.Keep = out.Keep || changed
	}
	if _, err := groupFile(filepath.Join(d.mailNodeDir(), "servers.pem"), c.ServerKeys(), gid); err != nil {
		return out, err
	}

	// The database node.
	data := filepath.Join(d.mailHome(), "db")
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", host.MailUser, "-g", host.MailUser, "-m", "0700", data); err != nil {
		return out, err
	}
	config := filepath.Join(d.mailClusterDir(), "db.cnf")
	if out.DB, err = host.WriteFile(config, c.Galera(), 0o644); err != nil {
		return out, err
	}
	env := "MARIADB_RANDOM_ROOT_PASSWORD=1\nMARIADB_DATABASE=" + mailcluster.Database + "\nMARIADB_USER=" + mailcluster.DatabaseUser + "\nMARIADB_PASSWORD=" + in.DBPassword + "\n"
	envFile := filepath.Join(d.mailClusterDir(), "db.env")
	changed, err := groupFile(envFile, env, gid)
	if err != nil {
		return out, err
	}
	out.DB = out.DB || changed
	if in.Bootstrap {
		// The mark stays until the node started one time (mailNodeStarted).
		if _, err := os.Stat(d.mailMark("db-started")); err != nil {
			if _, err := host.WriteFile(d.mailMark("db-bootstrap"), "\n", 0o644); err != nil {
				return out, err
			}
		}
	}
	units, err := d.writeMailNodeUnits()
	out.Keep, out.DB = out.Keep || units.Keep, out.DB || units.DB
	return out, err
}

func (d *Deps) mailMark(name string) string { return filepath.Join(d.mailClusterDir(), name) }

// writeMailNodeUnits writes the units of the cluster store and of the database node.
func (d *Deps) writeMailNodeUnits() (out mailNodeSet, err error) {
	exists := func(mark string) bool { _, err := os.Stat(d.mailMark(mark)); return err == nil }
	keep := quadlet.MailKeepUnit(quadlet.MailKeep{Image: d.MailKeepImage, Dir: d.mailKeepDir(), Bootstrap: !exists("keep-started")})
	if out.Keep, err = d.Host.WriteUnit(host.MailUser, mailKeepUnit, keep); err != nil {
		return out, err
	}
	db := quadlet.MailDBUnit(quadlet.MailDB{Image: d.MailDBImage, ConfigFile: filepath.Join(d.mailClusterDir(), "db.cnf"), DataDir: filepath.Join(d.mailHome(), "db"),
		EnvFile: filepath.Join(d.mailClusterDir(), "db.env"), Bootstrap: exists("db-bootstrap") && !exists("db-started")})
	out.DB, err = d.Host.WriteUnit(host.MailUser, mailDBUnit, db)
	return out, err
}

// mailNodeStarted records that the cluster store and the database node of this server
// started one time. Their units then have no option for a new cluster: a node that starts a
// new cluster a second time would split the cluster. It reports true when a unit changed.
func (d *Deps) mailNodeStarted(ctx context.Context) (changed bool, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailNodeStarted, nil, &changed)
		return changed, err
	}
	if cluster, err := d.mailCluster(); err != nil || cluster == nil {
		return false, err
	}
	for _, mark := range []string{"keep-started", "db-started"} {
		if _, err := host.WriteFile(d.mailMark(mark), "\n", 0o644); err != nil {
			return false, err
		}
	}
	if err := os.Remove(d.mailMark("db-bootstrap")); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	out, err := d.writeMailNodeUnits()
	return out.Keep || out.DB, err
}

// mailNodeStatus asks the database node of this server for its state.
func (d *Deps) mailNodeStatus(ctx context.Context) (status mailNodeStatus, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opMailNodeStatus, nil, &status)
		return status, err
	}
	const query = `exec mariadb -u"$MARIADB_USER" -p"$MARIADB_PASSWORD" -N -e "show status like 'wsrep_cluster_size'; show status like 'wsrep_local_state_comment'"`
	out, err := d.Host.Podman(ctx, host.MailUser, "exec", "cf-mail-db", "sh", "-c", query)
	if err != nil {
		return status, err
	}
	fields := strings.Fields(out)
	for i := 0; i+1 < len(fields); i += 2 {
		switch fields[i] {
		case "wsrep_cluster_size":
			status.Size, _ = strconv.Atoi(fields[i+1])
		case "wsrep_local_state_comment":
			status.State = fields[i+1]
		}
	}
	return status, nil
}

// PrepareMailNode makes the keys of this server for a mail cluster. The result of the job
// has "keys": the public keys that the panel gives to the other members.
func (d *Deps) PrepareMailNode(req *agentv1.PrepareMailNodeRequest) (jobs.Job, error) {
	var keys mailNodeKeys
	return jobs.Job{ID: req.GetJobId(), Key: mailKey,
		Steps: []jobs.Step{{Name: "Make keys", Do: func(ctx context.Context) (err error) {
			keys, err = d.prepareMailNode(ctx)
			return err
		}}},
		Result: func() map[string]any { return map[string]any{"keys": keys} },
	}, nil
}

// SetMailNode makes this server a member of a mail cluster, and starts its cluster store and
// its database node. The result of the job has "database": the state of the database node.
func (d *Deps) SetMailNode(req *agentv1.SetMailNodeRequest) (jobs.Job, error) {
	in := mailNodeIn{
		Cluster:    mailcluster.Cluster{Name: req.GetCluster(), Hostname: req.GetHostname(), Self: int(req.GetSelf())},
		DBPassword: req.GetDbPassword(), KeepSecret: req.GetKeepSecret(), PeerSecret: req.GetPeerSecret(), Bootstrap: req.GetBootstrap(),
	}
	for _, m := range req.GetMembers() {
		in.Cluster.Members = append(in.Cluster.Members, mailcluster.Member{ID: int(m.GetId()), Name: m.GetName(), Endpoint: m.GetEndpoint(), Address: m.GetAddress(),
			Region: int(m.GetRegion()), WireGuardKey: m.GetWireguardKey(), KeepKey: m.GetKeepKey(), NodeKey: m.GetNodeKey()})
	}
	if err := in.Cluster.Validate(); err != nil {
		return jobs.Job{}, err
	}
	if d.MailDBImage == "" || d.MailKeepImage == "" {
		return jobs.Job{}, errors.New("the agent does not have the images of a mail cluster")
	}
	var set mailNodeSet
	var status mailNodeStatus
	service := func(ctx context.Context, name string, changed bool) error {
		verb := "start"
		if changed {
			verb = "restart"
		}
		_, err := d.Host.Systemctl(ctx, host.MailUser, verb, name)
		return err
	}
	return jobs.Job{ID: req.GetJobId(), Key: mailKey,
		Steps: []jobs.Step{
			{Name: "Make private network", Do: func(ctx context.Context) (err error) {
				if set, err = d.setMailNode(ctx, in); err != nil {
					return err
				}
				for _, image := range []string{d.MailKeepImage, d.MailDBImage} {
					if _, err := d.Host.Podman(ctx, host.MailUser, "image", "exists", image); err != nil {
						if _, err := d.Host.Podman(ctx, host.MailUser, "pull", "--quiet", image); err != nil {
							return err
						}
					}
				}
				_, err = d.Host.Systemctl(ctx, host.MailUser, "daemon-reload")
				return err
			}},
			{Name: "Start cluster store", Do: func(ctx context.Context) error { return service(ctx, mailKeepService, set.Keep) }},
			{Name: "Start database", Do: func(ctx context.Context) (err error) {
				if err := service(ctx, mailDBService, set.DB); err != nil {
					return err
				}
				// A server that joins a cluster is ready when it sees a different node. The
				// server that starts a new cluster is the only node at first.
				want := 2
				if in.Bootstrap {
					want = 1
				}
				deadline := time.Now().Add(orDefault(d.MailNodeWait, mailNodeWait))
				for {
					status, err = d.mailNodeStatus(ctx)
					if err == nil && status.State == mailStateSynced && status.Size >= want {
						break
					}
					if time.Now().After(deadline) {
						if err != nil {
							return fmt.Errorf("the database node does not answer: %w", err)
						}
						return fmt.Errorf("the database node is not ready: it sees %d nodes and its state is %q. Make sure that the servers can connect to each other on UDP port %d", status.Size, status.State, mailcluster.WireGuardPort)
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(orDefault(d.MailNodePoll, 3*time.Second)):
					}
				}
				changed, err := d.mailNodeStarted(ctx)
				if err != nil || !changed {
					return err
				}
				_, err = d.Host.Systemctl(ctx, host.MailUser, "daemon-reload")
				return err
			}},
		},
		Result: func() map[string]any { return map[string]any{"database": status} },
	}, nil
}
