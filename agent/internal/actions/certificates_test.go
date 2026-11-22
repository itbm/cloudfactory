package actions

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
)

const (
	certA = "01jbq3z5k8n6t4w2x9y7r5p3d1"
	certB = "01jbq3z5k8n6t4w2x9y7r5p3d2"
)

// panelCert makes a certificate for the names, signed by a customer CA, in PEM format.
func panelCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, names ...string) (*x509.Certificate, string, string) {
	t.Helper()
	cert, key := certificateFor(t, names[0], ca, caKey)
	if len(names) > 1 {
		// certificateFor gives one name. Make a certificate with all names.
		tmpl := *cert
		tmpl.DNSNames = names
		der, err := x509.CreateCertificate(rand.Reader, &tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ = x509.ParseCertificate(der)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestSetCertificatesLoadsAndChecksTheCertificates(t *testing.T) {
	d, _, edge := deps(t)
	ca, caKey := certificateFor(t, "Customer CA", nil, nil)
	wild, wildPEM, wildKey := panelCert(t, ca, caKey, "*.example.com", "example.com")
	shop, shopPEM, shopKey := panelCert(t, ca, caKey, "shop.example.org")
	served := map[string]*x509.Certificate{"example.com": wild, "www.example.com": wild, "shop.example.org": shop}
	d.ServedCertificate = func(_ context.Context, name string) ([]*x509.Certificate, error) {
		if c, ok := served[name]; ok {
			return []*x509.Certificate{c}, nil
		}
		return nil, errors.New("no certificate")
	}
	req := &agentv1.SetCertificatesRequest{JobId: jobID, Certificates: []*agentv1.EdgeCertificate{
		{Id: certA, Hosts: []string{"www.example.com", "Example.com"}, CertificatePem: wildPEM + pemOf(ca), PrivateKeyPem: wildKey},
		{Id: certB, Hosts: []string{"shop.example.org"}, CertificatePem: shopPEM, PrivateKeyPem: shopKey},
	}}
	job, err := d.SetCertificates(req)
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if len(edge.certs) != 2 || edge.certs[0].ID != certA || strings.Join(edge.certs[0].Hosts, ",") != "example.com,www.example.com" || edge.certs[1].KeyPEM != shopKey {
		t.Fatalf("certificates = %+v", edge.certs)
	}
	if job.Key != "certificates" || job.Steps[0].Name != "Load certificates" || job.Steps[1].Name != "Check certificates" {
		t.Errorf("job = %s %q %q", job.Key, job.Steps[0].Name, job.Steps[1].Name)
	}
	loaded, _ := job.Result()["certificates"].([]caddy.LoadedCertificate)
	if len(loaded) != 2 || loaded[0].SHA256 != caddy.LeafFingerprint(wild) {
		t.Errorf("result = %+v", job.Result())
	}

	// The report of the server has the certificates, and the domains that use one are valid
	// when the edge proxy sends that certificate. The system roots do not matter.
	states := d.certificateStates(context.Background())
	if len(states) != 2 || states[1].ID != certB || states[1].SHA256 != caddy.LeafFingerprint(shop) {
		t.Errorf("report = %+v", states)
	}
	hosts := d.panelCertificateHosts(context.Background())
	if st := d.domainState(context.Background(), "www.example.com", hosts["www.example.com"]); st.Certificate != "valid" || st.ExpiresAt != wild.NotAfter.UTC().Format(time.RFC3339) {
		t.Errorf("www.example.com = %+v", st)
	}
	// The edge proxy sends a different certificate: the check fails.
	served["shop.example.org"] = wild
	if st := d.domainState(context.Background(), "shop.example.org", hosts["shop.example.org"]); st.Certificate != "failed" {
		t.Errorf("shop.example.org = %+v", st)
	}
	job, err = d.SetCertificates(req)
	if err := runSteps(t, job, err); err == nil || !strings.Contains(err.Error(), "does not send the certificate "+certB+" for shop.example.org") {
		t.Errorf("check with a different certificate: %v", err)
	}
}

func pemOf(c *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
}

func TestTheCertificatesRequestIsChecked(t *testing.T) {
	d, _, _ := deps(t)
	ca, caKey := certificateFor(t, "Customer CA", nil, nil)
	_, wildPEM, wildKey := panelCert(t, ca, caKey, "*.example.com")
	_, _, otherKey := panelCert(t, ca, caKey, "other.example.com")
	good := func() *agentv1.EdgeCertificate {
		return &agentv1.EdgeCertificate{Id: certA, Hosts: []string{"www.example.com"}, CertificatePem: wildPEM, PrivateKeyPem: wildKey}
	}
	if _, err := d.SetCertificates(&agentv1.SetCertificatesRequest{JobId: jobID, Certificates: []*agentv1.EdgeCertificate{good()}}); err != nil {
		t.Fatalf("a good request: %v", err)
	}
	for name, change := range map[string]func(*agentv1.EdgeCertificate){
		"bad ID":               func(c *agentv1.EdgeCertificate) { c.Id = "../x" },
		"no hosts":             func(c *agentv1.EdgeCertificate) { c.Hosts = nil },
		"bad host":             func(c *agentv1.EdgeCertificate) { c.Hosts = []string{"www example.com"} },
		"wildcard host":        func(c *agentv1.EdgeCertificate) { c.Hosts = []string{"*.example.com"} },
		"host not covered":     func(c *agentv1.EdgeCertificate) { c.Hosts = []string{"example.com"} },
		"two levels":           func(c *agentv1.EdgeCertificate) { c.Hosts = []string{"a.b.example.com"} },
		"key of a different":   func(c *agentv1.EdgeCertificate) { c.PrivateKeyPem = otherKey },
		"no key":               func(c *agentv1.EdgeCertificate) { c.PrivateKeyPem = "" },
		"not a certificate":    func(c *agentv1.EdgeCertificate) { c.CertificatePem = "hello" },
		"certificate too long": func(c *agentv1.EdgeCertificate) { c.CertificatePem += strings.Repeat("x", 70000) },
	} {
		c := good()
		change(c)
		if _, err := d.SetCertificates(&agentv1.SetCertificatesRequest{JobId: jobID, Certificates: []*agentv1.EdgeCertificate{c}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// One host in two certificates, and one certificate two times.
	second := good()
	second.Id = certB
	if _, err := d.SetCertificates(&agentv1.SetCertificatesRequest{JobId: jobID, Certificates: []*agentv1.EdgeCertificate{good(), second}}); err == nil {
		t.Error("one host in two certificates was accepted")
	}
	second.Hosts = []string{"shop.example.com"}
	second.Id = certA
	if _, err := d.SetCertificates(&agentv1.SetCertificatesRequest{JobId: jobID, Certificates: []*agentv1.EdgeCertificate{good(), second}}); err == nil {
		t.Error("one certificate two times was accepted")
	}
	if _, err := d.SetCertificates(&agentv1.SetCertificatesRequest{JobId: "1"}); err == nil {
		t.Error("a bad job ID was accepted")
	}
	// No certificates is a good request: the edge proxy then has no certificates from the panel.
	if _, err := d.SetCertificates(&agentv1.SetCertificatesRequest{JobId: jobID}); err != nil {
		t.Errorf("no certificates: %v", err)
	}
}
