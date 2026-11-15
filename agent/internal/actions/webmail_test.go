package actions

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/webmail"
)

const webmailImage = "ghcr.io/itbm/webmail:0.2.0"

func webmailDeps(t *testing.T) (*Deps, *fakeEdge) {
	t.Helper()
	d, f, edge := deps(t)
	f.MakeDirs = true
	dir := t.TempDir()
	both(d, func(d *Deps) {
		d.MailImage = mailImage
		d.WebmailImage = webmailImage
		d.MailHome = filepath.Join(dir, "home")
		d.MailConfigDir = filepath.Join(dir, "etc")
		d.WebmailHome = filepath.Join(dir, "webmail-home")
		d.WebmailConfigDir = filepath.Join(dir, "webmail-etc")
	})
	return d, edge
}

func testCA(t *testing.T) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Cloudfactory test CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestTheWebmailStartsWithItsOwnUserAndKeepsItsSecret(t *testing.T) {
	d, edge := webmailDeps(t)
	ctx := context.Background()
	if err := d.EnsureWebmail(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	root := rootOf(d)
	env, _ := os.ReadFile(root.webmailEnvFile())
	for _, want := range []string{"JMAP_SERVER_URL=https://" + mailHost, "BULWARK_TELEMETRY=off", "STALWART_ADMIN_ACCESS=off", "SESSION_SECRET="} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the settings do not have %q:\n%s", want, env)
		}
	}
	// A public name has a certificate of a public CA: no added CA.
	if strings.Contains(string(env), "NODE_EXTRA_CA_CERTS") {
		t.Errorf("a public name has an added CA:\n%s", env)
	}
	if fi, err := os.Stat(root.webmailEnvFile()); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("the settings file: %v, %v", fi, err)
	}
	dir, _ := root.Host.UnitDir(host.WebmailUser)
	unit, _ := os.ReadFile(filepath.Join(dir, webmailUnit))
	for _, want := range []string{"Image=" + webmailImage, "PublishPort=" + quadlet.ServiceAddress + ":19027:3000", "ReadOnly=true", "DropCapability=all", "User=1001"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("the unit does not have %q:\n%s", want, unit)
		}
	}
	// The settings of the users are encrypted with the secret: it stays the same.
	secret, _ := root.webmailSecret()
	if err := d.EnsureWebmail(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	if again, _ := root.webmailSecret(); again != secret || len(secret) != 64 {
		t.Errorf("the secret changed: %q, %q", secret, again)
	}

	// The edge proxy sends the JMAP paths to the mail server and the rest to the webmail.
	if err := d.mailRouteFor(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	site := edge.sites[mailRoute]
	if site.Page != "" || site.Port != quadlet.WebmailPort || len(site.Paths) != 1 || site.Paths[0].Port != quadlet.MailAdminPort {
		t.Fatalf("the route: %+v", site)
	}
	for _, p := range site.Paths[0].Paths {
		if strings.HasPrefix(p, "/api") {
			t.Errorf("the management API is on the public host name: %s", p)
		}
	}
}

func TestTheWebmailTrustsThePanelCAForALocalName(t *testing.T) {
	d, edge := webmailDeps(t)
	edge.root = testCA(t)
	if err := d.EnsureWebmail(context.Background(), "mail.cf.test"); err != nil {
		t.Fatal(err)
	}
	root := rootOf(d)
	env, _ := os.ReadFile(root.webmailEnvFile())
	if !strings.Contains(string(env), "NODE_EXTRA_CA_CERTS="+quadlet.WebmailCAFile) {
		t.Errorf("the settings do not name the CA:\n%s", env)
	}
	if ca, _ := os.ReadFile(root.webmailCAFile()); string(ca) != edge.root {
		t.Error("the webmail does not have the root certificate")
	}
	// The root helper accepts only a CA certificate.
	if _, err := root.prepareWebmail(context.Background(), webmailIn{Hostname: "mail.cf.test", CA: "not a certificate"}); err == nil {
		t.Error("a CA that is not a certificate must fail")
	}
}

func TestNoWebmailImageKeepsTheShortPage(t *testing.T) {
	d, edge := webmailDeps(t)
	both(d, func(d *Deps) { d.WebmailImage = "" })
	if err := d.EnsureWebmail(context.Background(), mailHost); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rootOf(d).webmailEnvFile()); !os.IsNotExist(err) {
		t.Error("the agent made a webmail with no image")
	}
	if err := d.mailRouteFor(context.Background(), mailHost); err != nil {
		t.Fatal(err)
	}
	if edge.sites[mailRoute].Page != mailPage {
		t.Errorf("the route: %+v", edge.sites[mailRoute])
	}
}

func TestTheWebmailSettings(t *testing.T) {
	if _, err := webmail.Env("mail.example.net", "short", ""); err == nil {
		t.Error("a short secret must fail")
	}
	if _, err := webmail.Env("mail.example.net\nX=1", strings.Repeat("a", 32), ""); err == nil {
		t.Error("a host name with a new line must fail")
	}
}
