package actions

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
	"github.com/itbm/cloudfactory/agent/internal/webmail"
)

// The webmail (Bulwark) of the mail server. It is on the host name of the mail server: the
// edge proxy sends the JMAP and DAV paths to the mail server, and the other paths to the
// webmail. The users sign in with the address and the password of their mailbox.

const (
	opWebmailPrepare = "webmail.prepare"

	webmailUnit    = "cf-webmail.container"
	webmailService = "cf-webmail.service"
)

type (
	webmailIn struct {
		Hostname string
		// CA is the root certificate of the panel CA, for a host name that is not public. The
		// worker gets it from the edge proxy. It is empty for a public name.
		CA string
	}
	webmailPrepared struct {
		// Changed is true when the unit or the settings changed: the container must start again.
		Changed bool
	}
)

func (d *Deps) webmailHome() string {
	if d.WebmailHome != "" {
		return d.WebmailHome
	}
	return "/var/lib/cf-webmail"
}

func (d *Deps) webmailConfigDir() string {
	if d.WebmailConfigDir != "" {
		return d.WebmailConfigDir
	}
	return "/etc/cloudfactory/webmail"
}

func (d *Deps) webmailEnvFile() string { return filepath.Join(d.webmailConfigDir(), "webmail.env") }
func (d *Deps) webmailCAFile() string  { return filepath.Join(d.webmailConfigDir(), "ca.crt") }

func (d *Deps) webmailRootOps() map[string]gate.Handler {
	return map[string]gate.Handler{
		opWebmailPrepare: gate.Op(func(ctx context.Context, in webmailIn) (webmailPrepared, error) {
			return d.prepareWebmail(ctx, in)
		}),
	}
}

// prepareWebmail makes the Unix user of the webmail, its directories, its settings with a
// secret, and the unit of its container. Root owns the settings: the webmail can read them,
// but cannot change them.
func (d *Deps) prepareWebmail(ctx context.Context, in webmailIn) (out webmailPrepared, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opWebmailPrepare, in, &out)
		return out, err
	}
	if err := validate.Domain(in.Hostname); err != nil {
		return out, err
	}
	if d.WebmailImage == "" {
		return out, errors.New("the root helper has no image of the webmail")
	}
	caFile := ""
	if in.CA != "" {
		if err := checkCertificate(in.CA); err != nil {
			return out, err
		}
		caFile = d.webmailCAFile()
	}
	home := d.webmailHome()
	if err := d.Host.EnsureServiceUser(ctx, host.WebmailUser, home); err != nil {
		return out, err
	}
	_, gid, err := d.Host.FileOwner(host.WebmailUser)
	if err != nil {
		return out, err
	}
	data := filepath.Join(home, "data")
	if err := os.MkdirAll(d.webmailConfigDir(), 0o755); err != nil {
		return out, err
	}
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", host.WebmailUser, "-g", host.WebmailUser, "-m", "0700", data); err != nil {
		return out, err
	}
	changed := false
	if caFile != "" {
		if changed, err = host.WriteFile(caFile, in.CA, 0o644); err != nil {
			return out, err
		}
	}
	secret, err := d.webmailSecret("SESSION_SECRET")
	if err != nil {
		return out, err
	}
	adminPassword, err := d.webmailSecret("ADMIN_PASSWORD")
	if err != nil {
		return out, err
	}
	env, err := webmail.Env(in.Hostname, secret, adminPassword, map[bool]string{true: quadlet.WebmailCAFile}[caFile != ""])
	if err != nil {
		return out, err
	}
	envChanged, err := host.WriteFile(d.webmailEnvFile(), env, 0o640)
	if err != nil {
		return out, err
	}
	if err := chownRoot(d.webmailEnvFile(), gid); err != nil {
		return out, err
	}
	unit := quadlet.WebmailUnit(quadlet.Webmail{Image: d.WebmailImage, Gvisor: d.runtime(), EnvFile: d.webmailEnvFile(), DataDir: data, CAFile: caFile})
	unitChanged, err := d.Host.WriteUnit(host.WebmailUser, webmailUnit, unit)
	out.Changed = unitChanged || envChanged || changed
	return out, err
}

// webmailSecret gives a secret of the webmail: the value of name in the settings. It keeps a
// secret that exists: the settings of the users are encrypted with SESSION_SECRET, and the
// webmail keeps a hash of ADMIN_PASSWORD after the first start. Each new secret is random.
func (d *Deps) webmailSecret(name string) (string, error) {
	b, err := os.ReadFile(d.webmailEnvFile())
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if value, ok := strings.CutPrefix(line, name+"="); ok && len(value) >= 32 {
				return value, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	r := make([]byte, 32)
	if _, err := rand.Read(r); err != nil {
		return "", err
	}
	return hex.EncodeToString(r), nil
}

// checkCertificate accepts one PEM certificate of a CA.
func checkCertificate(s string) error {
	block, rest := pem.Decode([]byte(s))
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return errors.New("the root certificate of the panel CA is not one PEM certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if !c.IsCA {
		return errors.New("the root certificate of the panel CA is not a CA certificate")
	}
	return nil
}

// EnsureWebmail makes the webmail of the mail server and starts it. With no image of the
// webmail, it does nothing.
func (d *Deps) EnsureWebmail(ctx context.Context, hostname string) error {
	if d.WebmailImage == "" {
		return nil
	}
	in := webmailIn{Hostname: hostname}
	if !caddy.PublicName(hostname) {
		root, err := d.Edge.PanelCARoot(ctx)
		if err != nil {
			return err
		}
		in.CA = root
	}
	prepared, err := d.prepareWebmail(ctx, in)
	if err != nil {
		return err
	}
	if _, err := d.Host.Podman(ctx, host.WebmailUser, "image", "exists", d.WebmailImage); err != nil {
		if _, err := d.Host.Podman(ctx, host.WebmailUser, "pull", "--quiet", d.WebmailImage); err != nil {
			return err
		}
	}
	verb := "start"
	if prepared.Changed {
		if _, err := d.Host.Systemctl(ctx, host.WebmailUser, "daemon-reload"); err != nil {
			return err
		}
		verb = "restart"
	}
	_, err = d.Host.Systemctl(ctx, host.WebmailUser, verb, webmailService)
	return err
}

// mailSite gives the routes of the edge proxy for the host name of the mail server.
func (d *Deps) mailSite(hostname string) caddy.Site {
	if d.WebmailImage == "" {
		return caddy.Site{WebsiteID: mailRoute, Hosts: []string{hostname}, Primary: hostname, Page: mailPage}
	}
	return webmail.Routes(hostname, quadlet.WebmailPort, quadlet.MailAdminPort)
}
