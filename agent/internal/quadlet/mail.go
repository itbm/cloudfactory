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
}

// MailUnit gives the container unit of the mail server.
func MailUnit(m Mail) string {
	lines := []string{
		"[Unit]",
		"Description=Cloudfactory mail server",
		"",
		"[Container]",
		"ContainerName=cf-mail",
		"Image=" + m.Image,
		"Pull=never",
		"GlobalArgs=" + m.Gvisor.GlobalArgs(),
		// The mail server sees the real address of each client. It needs the address for the
		// SPF check and for the limits of each address.
		"Network=" + PastaNetwork,
	}
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
