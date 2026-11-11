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

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// certificateFor makes a certificate for a name. With a nil parent, it makes a root
// certificate.
func certificateFor(t *testing.T, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	if parent == nil {
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.KeyUsage = true, true, x509.KeyUsageCertSign
		parent, parentKey = tmpl, key
	} else {
		tmpl.DNSNames, tmpl.ExtKeyUsage = []string{name}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

// panelServer makes a panel settings file for the domain, and an edge proxy that serves the
// certificate of the method that it has. Let's Encrypt gives a certificate only when public is
// true.
func panelServer(t *testing.T, domain string, public bool) (*Deps, *fakeEdge) {
	t.Helper()
	d, _, edge := deps(t)
	env := PanelEnvFile(d.DataDir)
	os.MkdirAll(filepath.Dir(env), 0o700)
	os.WriteFile(env, []byte("APP_KEY=x\nAPP_URL=https://"+domain+"\n"), 0o600)
	root, rootKey := certificateFor(t, "Cloudfactory panel CA", nil, nil)
	acmeRoot, acmeKey := certificateFor(t, "Public CA", nil, nil)
	internal, _ := certificateFor(t, domain, root, rootKey)
	acme, _ := certificateFor(t, domain, acmeRoot, acmeKey)
	edge.root = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}))
	d.PublicRoots = x509.NewCertPool()
	d.PublicRoots.AddCert(acmeRoot)
	d.PanelCertificateWait, d.PanelCertificatePoll = 50*time.Millisecond, 5*time.Millisecond
	d.ServedCertificate = func(context.Context, string) ([]*x509.Certificate, error) {
		edge.mu.Lock()
		defer edge.mu.Unlock()
		if edge.panel == caddy.PanelLetsEncrypt {
			if !public {
				return nil, os.ErrDeadlineExceeded
			}
			return []*x509.Certificate{acme}, nil
		}
		return []*x509.Certificate{internal, root}, nil
	}
	return d, edge
}

func TestChangeThePanelCertificateToLetsEncrypt(t *testing.T) {
	d, edge := panelServer(t, "panel.example.com", true)
	edge.panel = caddy.PanelInternal

	job, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: jobID, Method: caddy.PanelLetsEncrypt})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if edge.panel != caddy.PanelLetsEncrypt {
		t.Errorf("method = %q", edge.panel)
	}
	st, _ := job.Result()["panelCertificate"].(*panel.PanelCertificate)
	if st == nil || st.Method != caddy.PanelLetsEncrypt || st.State != "valid" || st.ExpiresAt == "" || st.Domain != "panel.example.com" || !strings.Contains(st.CACertificate, "BEGIN CERTIFICATE") {
		t.Errorf("result = %+v", st)
	}
}

// Caddy stops serving the old certificate when the method changes. When Let's Encrypt gives no
// certificate, the agent goes back to the previous method, so that the panel stays available.
func TestThePanelGoesBackWhenLetsEncryptGivesNoCertificate(t *testing.T) {
	d, edge := panelServer(t, "panel.example.com", false)
	edge.panel = caddy.PanelInternal

	job, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: jobID, Method: caddy.PanelLetsEncrypt})
	err = runSteps(t, job, err)
	if err == nil || !strings.Contains(err.Error(), "Let's Encrypt did not give a certificate for panel.example.com") || !strings.Contains(err.Error(), "uses its previous certificate again (the panel CA)") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(edge.methods, ",") != "letsencrypt,internal" || edge.panel != caddy.PanelInternal {
		t.Errorf("methods = %v, now %q", edge.methods, edge.panel)
	}
}

func TestChangeThePanelCertificateToTheInternalCA(t *testing.T) {
	d, edge := panelServer(t, "panel.example.com", true)
	edge.panel = caddy.PanelLetsEncrypt

	job, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: jobID, Method: caddy.PanelInternal})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	st, _ := job.Result()["panelCertificate"].(*panel.PanelCertificate)
	if edge.panel != caddy.PanelInternal || st == nil || st.State != "valid" {
		t.Errorf("method = %q, result = %+v", edge.panel, st)
	}
}

func TestThePanelCertificateRequestIsChecked(t *testing.T) {
	d, _, _ := deps(t)
	if _, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: jobID, Method: caddy.PanelInternal}); err == nil || !strings.Contains(err.Error(), "no panel") {
		t.Errorf("a server with no panel: %v", err)
	}
	d, _ = panelServer(t, "panel.localhost", false)
	if _, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: jobID, Method: caddy.PanelLetsEncrypt}); err == nil || !strings.Contains(err.Error(), "not a public domain") {
		t.Errorf("Let's Encrypt for panel.localhost: %v", err)
	}
	if _, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: jobID, Method: "self-signed"}); err == nil {
		t.Error("an unknown method was accepted")
	}
	if _, err := d.SetPanelCertificate(&agentv1.SetPanelCertificateRequest{JobId: "1", Method: caddy.PanelInternal}); err == nil {
		t.Error("a bad job ID was accepted")
	}
}

func TestTheReportOfThePanelServerHasThePanelCertificate(t *testing.T) {
	d, edge := panelServer(t, "panel.localhost", false)
	edge.panel = caddy.PanelInternal

	var r panel.Report
	d.FillReport(context.Background(), &r)
	if r.PanelCertificate == nil || r.PanelCertificate.Method != caddy.PanelInternal || r.PanelCertificate.State != "valid" || r.PanelCertificate.CACertificate != edge.root {
		t.Errorf("panel certificate = %+v", r.PanelCertificate)
	}

	// Other servers do not report it.
	d, _, _ = deps(t)
	r = panel.Report{}
	d.FillReport(context.Background(), &r)
	if r.PanelCertificate != nil {
		t.Errorf("panel certificate of a server with no panel = %+v", r.PanelCertificate)
	}
}
