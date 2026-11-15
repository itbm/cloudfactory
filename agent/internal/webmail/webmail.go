// Package webmail gives the settings of the webmail (Bulwark) of a mail server.
//
// The webmail is on the host name of the mail server. The edge proxy sends the JMAP and DAV
// paths to the mail server, and all other paths to the webmail. The browser thus uses one
// origin for the webmail and for JMAP, and the mail server needs no CORS settings.
//
// The webmail signs in to the mail server with the address and the password of the mailbox.
// It keeps no mail. It keeps the settings of each user, encrypted with SESSION_SECRET.
package webmail

import (
	"fmt"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// ContainerPort is the port of the webmail in its container.
const ContainerPort = 3000

// DataDir is the directory of the webmail in its container.
const DataDir = "/app/data"

// MailPaths are the paths that the edge proxy sends to the mail server. They are the JMAP API
// that the webmail and mail programs use, and CalDAV and CardDAV for the calendars and the
// contacts. The management API of the mail server (/api) is not in the list.
var MailPaths = []string{"/.well-known/jmap", "/jmap", "/jmap/*", "/.well-known/caldav", "/.well-known/carddav", "/dav", "/dav/*"}

// Routes gives the routes of the edge proxy for the host name of the mail server: the paths of
// the mail server go to mailPort, and all other paths to webmailPort.
func Routes(hostname string, webmailPort, mailPort int) caddy.Site {
	return caddy.Site{
		WebsiteID: "mail",
		Hosts:     []string{hostname},
		Primary:   hostname,
		Port:      webmailPort,
		Paths:     []caddy.PathRoute{{Paths: MailPaths, Port: mailPort}},
	}
}

// Env gives the environment file of the webmail container. secret is the key of the settings
// of the users and of the sessions: 32 or more characters. extraCA is the file in the container
// with an added certificate authority, for a host name that has a certificate of the panel CA.
// It is empty for a certificate of a public CA.
func Env(hostname, secret, extraCA string) (string, error) {
	if err := validate.Domain(hostname); err != nil {
		return "", err
	}
	if len(secret) < 32 || strings.ContainsAny(secret, "\n\r") {
		return "", fmt.Errorf("the secret of the webmail must have 32 or more characters")
	}
	lines := []string{
		"HOSTNAME=0.0.0.0",
		fmt.Sprintf("PORT=%d", ContainerPort),
		"APP_NAME=Webmail",
		"LOGIN_COMPANY_NAME=" + hostname,
		// The webmail examines each sign-in on the mail server, through the edge proxy.
		"JMAP_SERVER_URL=https://" + hostname,
		"STALWART_FEATURES=true",
		"SESSION_SECRET=" + secret,
		"SETTINGS_SYNC_ENABLED=true",
		// No data about the server goes to the makers of the webmail. The administrator of the
		// panel cannot change this in the webmail.
		"BULWARK_TELEMETRY=off",
		// The webmail does not ask for updates: the panel updates the image.
		"BULWARK_UPDATE_CHECK=off",
		// The administrator of the mail server is not a person: the agent uses it. Thus the
		// rights of that account do not open the admin dashboard of the webmail.
		"STALWART_ADMIN_ACCESS=off",
	}
	if extraCA != "" {
		lines = append(lines, "NODE_EXTRA_CA_CERTS="+extraCA)
	}
	return strings.Join(lines, "\n") + "\n", nil
}
