package quadlet

import (
	"fmt"
	"strings"
)

// The mail server (Stalwart) of a server with the email role. All customers of the server use
// this one container: a server has one port 25 for all mail domains. The panel keeps the
// customers apart.
//
// Stalwart does not run as root, so it cannot listen on a port below 1024 in the container.
// It listens on the ports below, and the firewall of the host redirects the public ports to
// them, as it redirects 80 and 443 to the edge proxy.
const (
	// MailSMTPPort gets the mail from other mail servers (public port 25).
	MailSMTPPort = 2025
	// MailSubmissionsPort is for mail programs, with TLS from the first byte (public port 465).
	MailSubmissionsPort = 2465
	// MailSubmissionPort is for mail programs, with STARTTLS (public port 587).
	MailSubmissionPort = 2587
	// MailIMAPSPort is IMAP with TLS (public port 993).
	MailIMAPSPort = 2993
	// MailHTTPPort is the management API in the container.
	MailHTTPPort = 8080
	// MailAdminPort is the port of the management API on ServiceAddress. Only root can
	// connect to it: the root helper of the agent opens the connection for the worker.
	MailAdminPort = 19025

	// MailCertFile and MailKeyFile are the certificate of the mail host name in the container.
	MailCertFile = "/etc/stalwart-certs/tls.crt"
	MailKeyFile  = "/etc/stalwart-certs/tls.key"
	// MailContainerUser is the user "stalwart" of the image. The Unix user of the mail server
	// on the host has this ID in the container. The build of the image checks the ID.
	MailContainerUser = "2000"
)

// MailPublicPorts gives each public mail port with its port in the container.
func MailPublicPorts() [][2]int {
	return [][2]int{{25, MailSMTPPort}, {465, MailSubmissionsPort}, {587, MailSubmissionPort}, {993, MailIMAPSPort}}
}

// Mail is the input for the unit of the mail server.
type Mail struct {
	Image  string
	Gvisor Runtime
	// ConfigDir holds config.json, which names the data store. Root owns it.
	ConfigDir string
	// CertDir holds the certificate of the mail host name and its key. Root owns it.
	CertDir string
	// DataDir holds the data store: the settings, the accounts and the messages.
	DataDir string
	// EnvFile holds the password of the administrator of the management API. Only root and
	// the Unix user of the mail server can read it.
	EnvFile string
	// ClusterDir is set on a member of a mail cluster. It holds the key of the mail server for
	// the cluster store and the public keys of the cluster store. Root owns it.
	ClusterDir string
}

// MailClusterDir is the directory in the mail containers with the files for the cluster store.
// The settings of the mail server and of the cluster store name files in it.
const MailClusterDir = "/etc/cf-keep"

const (
	// MailDBContainerUser is the user "mysql" of the image of the database.
	MailDBContainerUser = "999"
	// MailKeepContainerUser is the user "keep" of the image of the cluster store.
	MailKeepContainerUser = "2000"
)

// MailUnit gives the container unit of the mail server.
func MailUnit(m Mail) string {
	lines := []string{
		"[Unit]",
		"Description=Cloudfactory mail server",
	}
	if m.ClusterDir != "" {
		// The mail server of a cluster member needs the database node and the cluster store
		// of the same member.
		lines = append(lines, "Wants=cf-mail-db.service cf-mail-keep.service", "After=cf-mail-db.service cf-mail-keep.service")
	}
	lines = append(lines,
		"",
		"[Container]",
		"ContainerName=cf-mail",
		"Image="+m.Image,
		"Pull=never",
		"GlobalArgs="+m.Gvisor.GlobalArgs(),
		// The mail server sees the real address of each client. It needs the address for the
		// SPF check and for the limits of each address.
		"Network="+PastaNetwork,
	)
	for _, p := range MailPublicPorts() {
		lines = append(lines, fmt.Sprintf("PublishPort=%d:%d", p[1], p[1]))
	}
	lines = append(lines,
		// The management API has all rights on the mail server. It is only on the private
		// service address, which the firewall keeps for root.
		fmt.Sprintf("PublishPort=%s:%d:%d", ServiceAddress, MailAdminPort, MailHTTPPort),
		"Volume="+m.ConfigDir+"/config.json:/etc/stalwart/config.json:ro,Z",
		"Volume="+m.CertDir+":/etc/stalwart-certs:ro,Z",
		"Volume="+m.DataDir+":/var/lib/stalwart:Z",
	)
	if m.ClusterDir != "" {
		lines = append(lines, "Volume="+m.ClusterDir+":"+MailClusterDir+":ro,Z")
	}
	lines = append(lines,
		"EnvironmentFile="+m.EnvFile,
		"NoNewPrivileges=true",
		"DropCapability=all",
		"UserNS=keep-id:uid="+MailContainerUser+",gid="+MailContainerUser,
		"User="+MailContainerUser,
		"Group="+MailContainerUser,
		"ReadOnly=true",
		"Tmpfs=/tmp",
		"",
		"[Service]",
		"Restart=always",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	)
	return Header + strings.Join(lines, "\n")
}

// MailConfig is config.json of the mail server. It names only the data store: Stalwart keeps
// all other settings in the data store, and the agent changes them through the management API.
const MailConfig = `{"@type":"RocksDb","path":"/var/lib/stalwart/"}` + "\n"

// MailDB is the input for the unit of the database node of a mail cluster member.
type MailDB struct {
	Image string
	// ConfigFile has the settings of the node. Root owns it.
	ConfigFile string
	// DataDir holds the data of the node.
	DataDir string
	// EnvFile holds the password of the database user of the mail server.
	EnvFile string
	// Bootstrap is true only for the first start of the first node of a new cluster. A node
	// that starts a new cluster a second time would split the cluster.
	Bootstrap bool
}

// MailDBUnit gives the container unit of the database node (MariaDB with Galera).
//
// The node uses the network of the host: the database software gives its address in the
// private network to the other nodes, and it must own that address. The settings of the node
// bind each port to that address, and the firewall lets only the members connect.
func MailDBUnit(m MailDB) string {
	lines := []string{
		"[Unit]",
		"Description=Cloudfactory mail cluster database",
		"After=wg-quick@cf-mail.service",
		"",
		"[Container]",
		"ContainerName=cf-mail-db",
		"Image=" + m.Image,
		"Pull=never",
		"Network=host",
		"Volume=" + m.ConfigFile + ":/etc/mysql/conf.d/cluster.cnf:ro,Z",
		"Volume=" + m.DataDir + ":/var/lib/mysql:Z",
		"EnvironmentFile=" + m.EnvFile,
		"NoNewPrivileges=true",
		"DropCapability=all",
		"UserNS=keep-id:uid=" + MailDBContainerUser + ",gid=" + MailDBContainerUser,
		"User=" + MailDBContainerUser,
		"Group=" + MailDBContainerUser,
		"ReadOnly=true",
		"Tmpfs=/tmp",
		"Tmpfs=/run/mysqld",
	}
	if m.Bootstrap {
		lines = append(lines, "Exec=--wsrep-new-cluster")
	}
	lines = append(lines,
		"",
		"[Service]",
		"Restart=always",
		"RestartSec=5",
		// A node that gets a full copy of the data needs time before it is ready.
		"TimeoutStartSec=900",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	)
	return Header + strings.Join(lines, "\n")
}

// MailKeep is the input for the unit of the cluster store of a mail cluster member.
type MailKeep struct {
	Image string
	// Dir holds the settings, the key and the secrets of the member. Root owns it.
	Dir string
	// Bootstrap is true only for the first start in a new cluster. On each later start the
	// member first waits until no mail server can hold a lock from before the stop.
	Bootstrap bool
}

// MailKeepUnit gives the container unit of the cluster store (Stalwart Keep). It uses the
// network of the host for the same reason as the database node.
func MailKeepUnit(m MailKeep) string {
	start := "rejoin"
	if m.Bootstrap {
		start = "bootstrap"
	}
	lines := []string{
		"[Unit]",
		"Description=Cloudfactory mail cluster store",
		"After=wg-quick@cf-mail.service",
		"",
		"[Container]",
		"ContainerName=cf-mail-keep",
		"Image=" + m.Image,
		"Pull=never",
		"Network=host",
		"Volume=" + m.Dir + ":" + MailClusterDir + ":ro,Z",
		"Exec=--config " + MailClusterDir + "/keep.toml --start " + start,
		"NoNewPrivileges=true",
		"DropCapability=all",
		"UserNS=keep-id:uid=" + MailKeepContainerUser + ",gid=" + MailKeepContainerUser,
		"User=" + MailKeepContainerUser,
		"Group=" + MailKeepContainerUser,
		"ReadOnly=true",
		"",
		"[Service]",
		"Restart=always",
		"RestartSec=5",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}
	return Header + strings.Join(lines, "\n")
}
