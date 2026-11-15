// Package mailcluster has the settings of a mail cluster: mail servers on three or five
// servers that share their data, so that the mail service continues when one server fails.
//
// Each member of a cluster runs three containers:
//
//   - the mail server (Stalwart). It keeps no data of its own;
//   - a node of the database cluster (MariaDB with Galera). Each node has all the data: the
//     settings, the accounts and the messages. A change is on the majority of the nodes
//     before the database confirms it;
//   - a member of the cluster store (Stalwart Keep). It has the locks and the counters that
//     the mail servers share, and it carries the messages between the mail servers.
//
// The members connect through a private network (WireGuard) that only they are in. The
// database and the cluster store listen only on that network.
//
// This package only makes texts from values. The root helper of the agent checks the values
// with Validate, writes the files and starts the containers.
package mailcluster

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

const (
	// WireGuardPort is the UDP port of the private network on each member.
	WireGuardPort = 51877
	// Interface is the name of the network interface of the private network.
	Interface = "cf-mail"

	// DBPort is the port of the database for the mail server of the same member. It is on
	// the private service address of the host.
	DBPort = 19026
	// GaleraPort (TCP and UDP) carries the changes between the database nodes.
	GaleraPort = 4567
	// GaleraISTPort carries the changes that a node missed while it was away.
	GaleraISTPort = 4568
	// GaleraSSTPort carries a full copy of the data to a new or an empty node.
	GaleraSSTPort = 4444
	// KeepPort is the port of the cluster store for the mail servers.
	KeepPort = 7000
	// KeepPeerPort (TCP and UDP) is the port between the members of the cluster store.
	KeepPeerPort = 7100

	// Database and DatabaseUser are the database of the mail server and its user.
	Database     = "stalwart"
	DatabaseUser = "stalwart"

	// DBPasswordVariable and KeepSecretVariable are the environment variables of the mail
	// server with the two secrets. The settings name the variables, so no file of the mail
	// server and no row of the database has a secret.
	DBPasswordVariable = "CF_MAIL_DB_PASSWORD"
	KeepSecretVariable = "CF_MAIL_KEEP_SECRET"

	// KeepDir is the directory of the files for the cluster store in the two containers.
	KeepDir = "/etc/cf-keep"
)

// Member is one server of a cluster.
type Member struct {
	// ID is the number of the member, from 1. It does not change.
	ID int `json:"id"`
	// Name is the host name of the server. It is the name of the node in the logs.
	Name string `json:"name"`
	// Endpoint is the public IP address of the server.
	Endpoint string `json:"endpoint"`
	// Address is the address of the member in the private network.
	Address string `json:"address"`
	// Region groups the members that are near to each other, from 0. The database sends
	// its traffic between two regions through one member of each region.
	Region int `json:"region"`
	// WireGuardKey is the public key of the member in the private network (base64).
	WireGuardKey string `json:"wireguardKey"`
	// KeepKey is the public key of the member of the cluster store (PEM).
	KeepKey string `json:"keepKey"`
	// NodeKey is the public key of the mail server, as a client of the cluster store (PEM).
	NodeKey string `json:"nodeKey"`
}

// Cluster is a cluster as one of its members sees it.
type Cluster struct {
	// Name is the name of the cluster in the database software.
	Name string `json:"name"`
	// Hostname is the host name that all members share. Its DNS records give the addresses
	// of all members. The mail servers give this name to other mail servers.
	Hostname string `json:"hostname"`
	// Self is the ID of this member.
	Self    int      `json:"self"`
	Members []Member `json:"members"`
}

var (
	nameRule = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	hostRule = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	// SecretRule is the rule of each secret of a cluster: 32 hexadecimal digits. Stalwart Keep
	// accepts only this form, and the text is safe in each file that has it.
	SecretRule = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Validate refuses a cluster that the root helper must not write to files. The values come
// from the panel through the worker of the agent, which is not root.
func (c Cluster) Validate() error {
	if !nameRule.MatchString(c.Name) {
		return errors.New("the name of the cluster has only lower-case letters, digits and hyphens")
	}
	if !hostRule.MatchString(c.Hostname) || !strings.Contains(c.Hostname, ".") || strings.Contains(c.Hostname, "..") {
		return errors.New("the host name of the cluster is not a host name")
	}
	if n := len(c.Members); n != 3 && n != 5 {
		// With an even number, the two halves of a cut network are each not a majority.
		return fmt.Errorf("a mail cluster has 3 or 5 members, not %d", n)
	}
	seen := map[string]bool{}
	once := func(kind, value string) error {
		if seen[kind+" "+value] {
			return fmt.Errorf("two members have the same %s", kind)
		}
		seen[kind+" "+value] = true
		return nil
	}
	self := false
	for _, m := range c.Members {
		if m.ID < 1 || m.ID > 99 {
			return fmt.Errorf("the member ID %d is not from 1 to 99", m.ID)
		}
		if !hostRule.MatchString(m.Name) || strings.Contains(m.Name, "..") {
			return fmt.Errorf("member %d: the name is not a host name", m.ID)
		}
		if m.Region < 0 || m.Region > 99 {
			return fmt.Errorf("member %d: the region is not from 0 to 99", m.ID)
		}
		endpoint, err := netip.ParseAddr(m.Endpoint)
		if err != nil || endpoint.Zone() != "" || !endpoint.IsGlobalUnicast() && !endpoint.IsPrivate() {
			return fmt.Errorf("member %d: the endpoint is not an IP address of a server", m.ID)
		}
		address, err := netip.ParseAddr(m.Address)
		if err != nil || !address.Is4() || !address.IsPrivate() {
			// Stalwart Keep writes a member as address:port, which has one meaning only for IPv4.
			return fmt.Errorf("member %d: the address in the private network is not a private IPv4 address", m.ID)
		}
		if key, err := base64.StdEncoding.DecodeString(m.WireGuardKey); err != nil || len(key) != 32 || len(m.WireGuardKey) != 44 {
			return fmt.Errorf("member %d: the WireGuard key is not a public key", m.ID)
		}
		for what, key := range map[string]string{"key of the cluster store": m.KeepKey, "key of the mail server": m.NodeKey} {
			if _, err := publicKey(key); err != nil {
				return fmt.Errorf("member %d: the %s: %w", m.ID, what, err)
			}
		}
		for kind, value := range map[string]string{"ID": fmt.Sprint(m.ID), "name": m.Name, "endpoint": endpoint.String(), "address": address.String(),
			"WireGuard key": m.WireGuardKey, "key of the cluster store": m.KeepKey, "key of the mail server": m.NodeKey} {
			if err := once(kind, value); err != nil {
				return err
			}
		}
		self = self || m.ID == c.Self
	}
	if !self {
		return errors.New("this server is not a member of the cluster")
	}
	return nil
}

// publicKey reads one Ed25519 public key in PEM and gives it back in one form.
func publicKey(text string) (string, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "PUBLIC KEY" || strings.TrimSpace(string(rest)) != "" {
		return "", errors.New("not one public key in PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", errors.New("not a public key")
	}
	if _, ok := key.(ed25519.PublicKey); !ok {
		return "", errors.New("not an Ed25519 key")
	}
	return string(pem.EncodeToMemory(block)), nil
}

// NewKey makes an Ed25519 key for the cluster store. It gives the private key and the public
// key in PEM.
func NewKey() (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})), nil
}

// PublicOf gives the public key of an Ed25519 private key in PEM.
func PublicOf(private string) (string, error) {
	block, _ := pem.Decode([]byte(private))
	if block == nil {
		return "", errors.New("not a private key in PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("not an Ed25519 key")
	}
	der, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// NewWireGuardKey makes a key of the private network. It gives the private key and the
// public key in base64, as WireGuard writes them.
func NewWireGuardKey() (private, public string, err error) {
	key, err := ecdh.X25519().GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(key.Bytes()), base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// WireGuardPublic gives the public key of a WireGuard private key.
func WireGuardPublic(private string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(private))
	if err != nil {
		return "", err
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// sorted gives the members in the order of their IDs. Each member must write the same lists.
func (c Cluster) sorted() []Member {
	members := append([]Member(nil), c.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return members
}

// Me gives this member.
func (c Cluster) Me() Member {
	for _, m := range c.Members {
		if m.ID == c.Self {
			return m
		}
	}
	return Member{}
}

// Peers gives the other members, in the order of their IDs.
func (c Cluster) Peers() []Member {
	var peers []Member
	for _, m := range c.sorted() {
		if m.ID != c.Self {
			peers = append(peers, m)
		}
	}
	return peers
}

// WireGuard gives the settings of the private network for wg-quick. privateKeyFile is a file
// that only root can read: the settings do not have the key.
func (c Cluster) WireGuard(privateKeyFile string) string {
	me := c.Me()
	lines := []string{
		"# The private network of the mail cluster. The Cloudfactory agent writes this file. Do not change it.",
		"[Interface]",
		"Address = " + me.Address + "/32",
		fmt.Sprintf("ListenPort = %d", WireGuardPort),
		"PostUp = wg set %i private-key " + privateKeyFile,
	}
	for _, p := range c.Peers() {
		endpoint := netip.AddrPortFrom(netip.MustParseAddr(p.Endpoint), WireGuardPort)
		lines = append(lines, "",
			"[Peer]",
			"# "+p.Name,
			"PublicKey = "+p.WireGuardKey,
			"Endpoint = "+endpoint.String(),
			"AllowedIPs = "+p.Address+"/32",
			// A member behind a firewall that tracks connections stays reachable.
			"PersistentKeepalive = 25",
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

// Galera gives the settings of the database node.
func (c Cluster) Galera() string {
	me := c.Me()
	var addresses []string
	for _, m := range c.sorted() {
		addresses = append(addresses, m.Address)
	}
	options := []string{
		fmt.Sprintf("gmcast.listen_addr=tcp://%s:%d", me.Address, GaleraPort),
		fmt.Sprintf("ist.recv_bind=%s", me.Address),
		// The members of one region send to another region through one connection.
		fmt.Sprintf("gmcast.segment=%d", me.Region),
		// Regions are far from each other: a slow answer is not a failure at once. A member
		// that does not answer for 30 seconds is out of the cluster.
		"evs.keepalive_period=PT3S", "evs.suspect_timeout=PT30S", "evs.inactive_timeout=PT1M", "evs.install_timeout=PT1M",
		// After all members stopped at the same time, they find their last state again.
		"pc.recovery=true",
	}
	return strings.Join([]string{
		"# A node of the database of the mail cluster. The Cloudfactory agent writes this file. Do not change it.",
		"[mariadbd]",
		// The node uses the network of the host. It listens only on the address of the member
		// in the private network. The mail server of the same member connects to that address.
		"bind_address=" + me.Address,
		fmt.Sprintf("port=%d", DBPort),
		"binlog_format=ROW",
		"default_storage_engine=InnoDB",
		"innodb_autoinc_lock_mode=2",
		// The other nodes have each change, so one node does not sync each change to its disk.
		"innodb_flush_log_at_trx_commit=2",
		// A message is one value in the database.
		"max_allowed_packet=256M",
		"skip_name_resolve=ON",
		"wsrep_on=ON",
		"wsrep_provider=/usr/lib/galera/libgalera_smm.so",
		"wsrep_cluster_name=" + c.Name,
		"wsrep_cluster_address=gcomm://" + strings.Join(addresses, ","),
		"wsrep_node_address=" + me.Address,
		"wsrep_node_name=" + me.Name,
		"wsrep_sst_method=rsync",
		"wsrep_sst_receive_address=" + me.Address,
		// A mail server reads from its own node. The node first applies the changes that the
		// other nodes confirmed, so that a read gives the mail that a different member got.
		"wsrep_sync_wait=1",
		// A change that two nodes make to the same row at the same time fails on one node.
		// The node does that change again.
		"wsrep_retry_autocommit=4",
		`wsrep_provider_options="` + strings.Join(options, "; ") + `"`,
		"",
	}, "\n")
}

// Keep gives the settings of the member of the cluster store. shards is the number of
// processor threads that the member uses.
func (c Cluster) Keep(shards int) string {
	me := c.Me()
	var members []string
	for _, m := range c.sorted() {
		// Each member writes the same number of shards for a member, or the members do not
		// agree on the place of a key. All members thus use the same number.
		members = append(members, fmt.Sprintf("  %q,", fmt.Sprintf("%d@%s:%d/%d/%d", m.ID, m.Address, KeepPort, KeepPeerPort, shards)))
	}
	return strings.Join([]string{
		"# A member of the cluster store of the mail cluster. The Cloudfactory agent writes this file. Do not change it.",
		fmt.Sprintf("node = %d", c.Self),
		fmt.Sprintf("shards = %d", shards),
		// The member uses the network of the host. It listens only on its address in the
		// private network.
		fmt.Sprintf(`listen = "%s:%d"`, me.Address, KeepPort),
		fmt.Sprintf(`peer-listen = "%s:%d"`, me.Address, KeepPeerPort),
		// A server has other work for its processors.
		`pin = "never"`,
		"members = [",
		strings.Join(members, "\n"),
		"]",
		`secret-file = "` + KeepDir + `/cluster.secret"`,
		`peer-secret-file = "` + KeepDir + `/peer.secret"`,
		`tls-key = "` + KeepDir + `/keep.key"`,
		`tls-clients = "` + KeepDir + `/clients.pem"`,
		`tls-servers = "` + KeepDir + `/servers.pem"`,
		"",
	}, "\n")
}

// ServerKeys gives the public keys of all members of the cluster store. ClientKeys gives the
// public keys of all mail servers. The two lists are apart, so that the key of a mail server
// cannot act as a member of the cluster store.
func (c Cluster) ServerKeys() string { return c.keys(func(m Member) string { return m.KeepKey }) }
func (c Cluster) ClientKeys() string { return c.keys(func(m Member) string { return m.NodeKey }) }

func (c Cluster) keys(of func(Member) string) string {
	var out strings.Builder
	for _, m := range c.sorted() {
		key, _ := publicKey(of(m))
		out.WriteString(key)
	}
	return out.String()
}

// DataStore gives config.json of the mail server of a member: the database node of the same
// member. host is the address of that node as the container of the mail server sees it.
func DataStore(host string) string {
	b, _ := json.Marshal(map[string]any{
		"@type": "MySql", "host": host, "port": DBPort, "database": Database, "authUsername": DatabaseUser,
		"authSecret": map[string]string{"@type": "EnvironmentVariable", "variableName": DBPasswordVariable},
	})
	return string(b) + "\n"
}

// InMemoryStore gives the setting of the mail servers for the cluster store. All mail servers
// of a cluster have the same setting: it is in the database.
func (c Cluster) InMemoryStore() map[string]any {
	hosts := map[string]any{}
	for _, m := range c.Members {
		hosts[fmt.Sprintf("%s:%d", m.Address, KeepPort)] = true
	}
	return map[string]any{
		"@type": "Keep", "hosts": hosts, "clusterDiscovery": true,
		"secret": map[string]any{"@type": "EnvironmentVariable", "variableName": KeepSecretVariable},
		"tls":    map[string]any{"@type": "RawPublicKeys", "keyPath": KeepDir + "/node.key", "serverKeysPath": KeepDir + "/servers.pem"},
	}
}

// Firewall gives the firewall rules of a member, as lines for three chains of the firewall of
// the mail server. uid is the UID of the Unix user of the mail containers.
//
//   - input: other members can connect to the private network. Only members can connect to
//     the database and to the cluster store, and only through the private network.
//   - output: only root and the mail containers can connect to the database and to the
//     cluster store of this server. The database software has no password between its
//     nodes: a different program on this server must not reach it.
//   - mail: the mail containers can connect to the database and to the cluster store of each
//     member. These lines go before the lines that refuse private networks.
func (c Cluster) Firewall(uid int) (input, output, mail []string) {
	var endpoints4, endpoints6, addresses []string
	for _, p := range c.Peers() {
		if netip.MustParseAddr(p.Endpoint).Is4() {
			endpoints4 = append(endpoints4, p.Endpoint)
		} else {
			endpoints6 = append(endpoints6, p.Endpoint)
		}
	}
	for _, m := range c.sorted() {
		addresses = append(addresses, m.Address)
	}
	members := "{ " + strings.Join(addresses, ", ") + " }"
	tcp := fmt.Sprintf("{ %d, %d, %d, %d, %d }", GaleraPort, GaleraISTPort, GaleraSSTPort, KeepPort, KeepPeerPort)
	udp := fmt.Sprintf("{ %d, %d }", GaleraPort, KeepPeerPort)
	local := fmt.Sprintf("{ %d, %d, %d, %d, %d, %d }", GaleraPort, GaleraISTPort, GaleraSSTPort, KeepPort, KeepPeerPort, DBPort)
	if len(endpoints4) > 0 {
		input = append(input, fmt.Sprintf("ip saddr { %s } udp dport %d accept", strings.Join(endpoints4, ", "), WireGuardPort))
	}
	if len(endpoints6) > 0 {
		input = append(input, fmt.Sprintf("ip6 saddr { %s } udp dport %d accept", strings.Join(endpoints6, ", "), WireGuardPort))
	}
	input = append(input,
		fmt.Sprintf("iifname %q ip saddr %s tcp dport %s accept", Interface, members, tcp),
		fmt.Sprintf("iifname %q ip saddr %s udp dport %s accept", Interface, members, udp),
	)
	output = []string{
		fmt.Sprintf("ct state new fib daddr type local meta l4proto { tcp, udp } th dport %s meta skuid != { 0, %d } jump refuse", local, uid),
	}
	mail = []string{
		"# The database and the cluster store of the mail cluster.",
		fmt.Sprintf("ip daddr %s tcp dport %s accept", members, tcp),
		fmt.Sprintf("ip daddr %s udp dport %s accept", members, udp),
		fmt.Sprintf("ip daddr %s tcp dport %d accept", c.Me().Address, DBPort),
	}
	return input, output, mail
}
