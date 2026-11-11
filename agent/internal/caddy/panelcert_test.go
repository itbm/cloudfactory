package caddy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testCA is a root certificate that signs leaf certificates, like PanelCA with sign_with_root.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert, key}
}

func (ca testCA) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}))
}

func (ca testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// leaf gives a certificate for the names, and its key.
func (ca testCA) leaf(t *testing.T, names ...string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: names,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func TestANewPanelGetsTheInternalCertificate(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	if err := c.ApplyPanel(ctx, "panel.localhost", PanelInternal, 19080, 19081); err != nil {
		t.Fatal(err)
	}
	if len(fake.tls) != 1 {
		t.Fatalf("policies = %v", fake.tls)
	}
	b, _ := json.Marshal(fake.tls[0])
	for _, want := range []string{`"@id":"cf-panel-tls"`, `"subjects":["panel.localhost"]`, `"module":"internal"`, `"ca":"cloudfactory"`, `"sign_with_root":true`, `"lifetime":"2160h"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("policy does not contain %s: %s", want, b)
		}
	}
	if ca := fake.cas[PanelCA]; ca == nil || ca["install_trust"] != false || ca["name"] != PanelCAName {
		t.Errorf("CA = %v", fake.cas)
	}
	// The policy comes before the route, so that Caddy does not start an ACME order.
	calls := strings.Join(fake.calls, ", ")
	if p, r := strings.Index(calls, "PUT /config/apps/tls/automation/policies"), strings.Index(calls, "PUT /config/apps/http/servers/edge/routes/0"); p < 0 || r < p {
		t.Errorf("calls = %v", fake.calls)
	}
	if m, err := c.PanelCertificate(ctx, "panel.localhost"); err != nil || m != PanelInternal {
		t.Errorf("PanelCertificate = %q, %v", m, err)
	}
	if root, err := c.PanelCARoot(ctx); err != nil || !strings.Contains(root, "BEGIN CERTIFICATE") {
		t.Errorf("PanelCARoot = %q, %v", root, err)
	}
}

func TestTheMethodChangesInPlaceAndKeepsOtherPolicies(t *testing.T) {
	other := map[string]any{"subjects": []any{"shop.example"}, "issuers": []any{map[string]any{"module": "acme"}}}
	fake := &fakeCaddy{loaded: true, tls: []map[string]any{other}, cas: map[string]map[string]any{PanelCA: {"name": "old"}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	if err := c.SetPanelCertificate(ctx, "panel.example.com", PanelLetsEncrypt); err != nil {
		t.Fatal(err)
	}
	if len(fake.tls) != 2 || fake.tls[0]["@id"] != "cf-panel-tls" {
		t.Fatalf("the panel policy must be first: %v", fake.tls)
	}
	if m, _ := c.PanelCertificate(ctx, "panel.example.com"); m != PanelLetsEncrypt {
		t.Errorf("method = %q", m)
	}
	if err := c.SetPanelCertificate(ctx, "panel.example.com", PanelInternal); err != nil {
		t.Fatal(err)
	}
	if len(fake.tls) != 2 {
		t.Fatalf("policies = %v", fake.tls)
	}
	if m, _ := c.PanelCertificate(ctx, "panel.example.com"); m != PanelInternal {
		t.Errorf("method = %q", m)
	}
	if fake.cas[PanelCA]["name"] != PanelCAName {
		t.Errorf("the CA was not changed: %v", fake.cas)
	}
	if err := c.SetPanelCertificate(ctx, "panel.example.com", "zerossl"); err == nil {
		t.Error("an unknown method was accepted")
	}
}

// A panel from before the certificate methods has a route and no policy. Caddy then uses its
// own defaults.
func TestThePanelCertificateOfAnOlderPanel(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	if m, err := c.PanelCertificate(ctx, "panel.example.com"); err != nil || m != "" {
		t.Errorf("with no panel: %q, %v", m, err)
	}
	if root, err := c.PanelCARoot(ctx); err != nil || root != "" {
		t.Errorf("with no CA: %q, %v", root, err)
	}
	fake.routes = []map[string]any{{"@id": "cf-panel"}}
	if m, _ := c.PanelCertificate(ctx, "panel.example.com"); m != PanelLetsEncrypt {
		t.Errorf("public domain: %q", m)
	}
	if m, _ := c.PanelCertificate(ctx, "panel.localhost"); m != PanelInternal {
		t.Errorf("internal domain: %q", m)
	}
}

func TestPublicName(t *testing.T) {
	for name, want := range map[string]bool{
		"panel.example.com": true, "cloudfactory.io": true, "Panel.Example.Com.": true,
		"localhost": false, "panel.localhost": false, "panel.local": false, "panel.internal": false,
		"panel.home.arpa": false, "panel.test": false, "panel.example": false, "panel.invalid": false,
		"panel": false, "10.0.0.1": false, "::1": false,
	} {
		if got := PublicName(name); got != want {
			t.Errorf("PublicName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCheckPanelCertificate(t *testing.T) {
	panelCA := newTestCA(t, "Cloudfactory panel CA")
	public := newTestCA(t, "Public CA")
	internal, _ := panelCA.leaf(t, "panel.example.com")
	acme, _ := public.leaf(t, "panel.example.com")
	now := time.Now()

	// sign_with_root: the edge proxy sends the root with the certificate.
	if err := CheckPanelCertificate([]*x509.Certificate{internal, panelCA.cert}, "panel.example.com", PanelInternal, panelCA.pem(), public.pool(), now); err != nil {
		t.Errorf("internal: %v", err)
	}
	if err := CheckPanelCertificate([]*x509.Certificate{internal}, "other.example.com", PanelInternal, panelCA.pem(), public.pool(), now); err == nil {
		t.Error("internal: a certificate for a different name was accepted")
	}
	if err := CheckPanelCertificate([]*x509.Certificate{acme}, "panel.example.com", PanelInternal, panelCA.pem(), public.pool(), now); err == nil {
		t.Error("internal: a public certificate was accepted")
	}
	if err := CheckPanelCertificate([]*x509.Certificate{internal}, "panel.example.com", PanelInternal, "", public.pool(), now); err == nil {
		t.Error("internal: accepted with no CA")
	}
	if err := CheckPanelCertificate([]*x509.Certificate{acme}, "panel.example.com", PanelLetsEncrypt, panelCA.pem(), public.pool(), now); err != nil {
		t.Errorf("letsencrypt: %v", err)
	}
	// The administrator added the panel CA to the trust store of the server. The certificate
	// of the panel CA is still not the certificate from Let's Encrypt.
	both := public.pool()
	both.AddCert(panelCA.cert)
	if err := CheckPanelCertificate([]*x509.Certificate{internal, panelCA.cert}, "panel.example.com", PanelLetsEncrypt, panelCA.pem(), both, now); err == nil || !strings.Contains(err.Error(), "no certificate from Let's Encrypt") {
		t.Errorf("letsencrypt with the panel CA in the roots: %v", err)
	}
	if err := CheckPanelCertificate([]*x509.Certificate{acme}, "panel.example.com", PanelLetsEncrypt, "", public.pool(), now.Add(48*time.Hour)); err == nil {
		t.Error("letsencrypt: an expired certificate was accepted")
	}
	if err := CheckPanelCertificate(nil, "panel.example.com", PanelLetsEncrypt, "", public.pool(), now); err == nil {
		t.Error("accepted with no certificate")
	}
}

func TestServedCertificateGivesTheChain(t *testing.T) {
	ca := newTestCA(t, "Cloudfactory panel CA")
	leaf, key := ca.leaf(t, "panel.localhost")
	cert := tls.Certificate{Certificate: [][]byte{leaf.Raw, ca.cert.Raw}, PrivateKey: key}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()

	chain, err := ServedCertificate(context.Background(), ln.Addr().String(), "panel.localhost")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPanelCertificate(chain, "panel.localhost", PanelInternal, ca.pem(), nil, time.Now()); err != nil {
		t.Error(err)
	}
	if _, err := ServedCertificate(context.Background(), net.JoinHostPort("127.0.0.1", "1"), "panel.localhost"); err == nil {
		t.Error("no error with no edge proxy")
	}
}
