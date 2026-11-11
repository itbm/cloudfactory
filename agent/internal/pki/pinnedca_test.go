package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testCertificate(t *testing.T, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, ca bool) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if ca {
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.KeyUsage = true, true, x509.KeyUsageCertSign
	} else {
		tmpl.DNSNames, tmpl.ExtKeyUsage = []string{name}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

// The edge proxy of a panel with its own CA sends the root of the CA with the certificate of the
// panel (sign_with_root). The agent accepts the root by its fingerprint.
func TestPinnedCA(t *testing.T) {
	root, rootKey := testCertificate(t, "Cloudfactory panel CA", nil, nil, true)
	leaf, _ := testCertificate(t, "panel.example.com", root, rootKey, false)
	public, publicKey := testCertificate(t, "Public CA", nil, nil, true)
	publicLeaf, _ := testCertificate(t, "panel.example.com", public, publicKey, false)
	other, otherKey := testCertificate(t, "Other CA", nil, nil, true)
	otherLeaf, _ := testCertificate(t, "panel.example.com", other, otherKey, false)
	publicRoots := x509.NewCertPool()
	publicRoots.AddCert(public)
	pin := Fingerprint(root.Raw)
	state := func(certs ...*x509.Certificate) tls.ConnectionState {
		return tls.ConnectionState{PeerCertificates: certs}
	}

	if err := PinnedCA(pin, "panel.example.com", publicRoots)(state(leaf, root)); err != nil {
		t.Errorf("certificate of the panel CA: %v", err)
	}
	// After a change to Let's Encrypt, the agent accepts the public certificate.
	if err := PinnedCA(pin, "panel.example.com", publicRoots)(state(publicLeaf)); err != nil {
		t.Errorf("public certificate: %v", err)
	}
	if err := PinnedCA(strings.ToUpper(pin), "panel.example.com", publicRoots)(state(leaf, root)); err != nil {
		t.Errorf("upper-case pin: %v", err)
	}
	// A different CA that sends its own root does not agree with the pin.
	if err := PinnedCA(pin, "panel.example.com", publicRoots)(state(otherLeaf, other)); err == nil {
		t.Error("a different CA was accepted")
	}
	if err := PinnedCA(pin, "other.example.com", publicRoots)(state(leaf, root)); err == nil {
		t.Error("a certificate for a different name was accepted")
	}
	// A certificate from a different CA with the pinned root in the chain.
	if err := PinnedCA(pin, "panel.example.com", publicRoots)(state(otherLeaf, root)); err == nil {
		t.Error("a certificate that the pinned root did not sign was accepted")
	}
	if err := PinnedCA(pin, "panel.example.com", publicRoots)(state()); err == nil {
		t.Error("no certificate was accepted")
	}
}
