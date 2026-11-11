// Package pki makes the certificate of the agent and checks certificate fingerprints.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Generate makes a self-signed certificate for the agent port. The panel keeps the
// certificate from the join request and trusts only that certificate for this server.
func Generate(certFile, keyFile string, hosts []string, validFor time.Duration) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hosts[0], Organization: []string{"Cloudfactory agent"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		// The certificate signs itself, so that the panel can use it as its own trust anchor.
		IsCA: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// Fingerprint gives the SHA-256 fingerprint of a DER certificate as lower-case hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// FileFingerprint reads a PEM certificate and gives its fingerprint.
func FileFingerprint(certFile string) (string, []byte, error) {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return "", nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", nil, fmt.Errorf("%s does not hold a PEM certificate", certFile)
	}
	return Fingerprint(block.Bytes), b, nil
}

// PinnedConnection gives a function for tls.Config.VerifyConnection. Go calls it for each
// connection, also for a resumed TLS session, so the pin is checked every time (R-12).
func PinnedConnection(fingerprint string) func(tls.ConnectionState) error {
	check := PinnedClient(fingerprint)
	return func(cs tls.ConnectionState) error {
		raw := make([][]byte, 0, len(cs.PeerCertificates))
		for _, c := range cs.PeerCertificates {
			raw = append(raw, c.Raw)
		}
		return check(raw, nil)
	}
}

// PinnedClient gives a function for tls.Config.VerifyPeerCertificate. It accepts only a
// client certificate with the given fingerprint.
func PinnedClient(fingerprint string) func([][]byte, [][]*x509.Certificate) error {
	want := strings.ToLower(fingerprint)
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if want == "" {
			return errors.New("the agent has no panel certificate fingerprint; run cf-agent join")
		}
		if len(raw) == 0 {
			return errors.New("the client sent no certificate")
		}
		got := Fingerprint(raw[0])
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return fmt.Errorf("the client certificate %s is not the panel certificate", got)
		}
		cert, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return errors.New("the panel certificate has expired")
		}
		return nil
	}
}

// PinnedServer gives a function for tls.Config.VerifyConnection of a client. It accepts only a
// server with the given certificate. The agent on the panel server uses it for the HTTPS port
// of the panel API, which has the certificate of the panel and no public name.
func PinnedServer(fingerprint string) func(tls.ConnectionState) error {
	want := strings.ToLower(fingerprint)
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the panel sent no certificate")
		}
		cert := cs.PeerCertificates[0]
		got := Fingerprint(cert.Raw)
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return fmt.Errorf("the certificate %s of the panel is not the certificate %s that this agent expects", got, want)
		}
		if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return errors.New("the panel certificate has expired")
		}
		return nil
	}
}

// PinnedCA gives a function for tls.Config.VerifyConnection of a client, with
// InsecureSkipVerify. It accepts a server certificate for host when a root in roots signs it,
// as with no pin. A nil roots means the system roots. It also accepts a server certificate for
// host from the CA with the given root fingerprint.
//
// The agents on the other servers use it when the panel has a certificate from its own CA
// (caddy.PanelInternal). The edge proxy of the panel sends the root certificate of that CA
// with the certificate of the panel. Thus the agent needs only the fingerprint of the root, and
// it continues to work when the panel changes to a public certificate.
func PinnedCA(fingerprint, host string, roots *x509.CertPool) func(tls.ConnectionState) error {
	want := strings.ToLower(fingerprint)
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the panel sent no certificate")
		}
		leaf := cs.PeerCertificates[0]
		intermediates := x509.NewCertPool()
		var pinned *x509.Certificate
		for _, c := range cs.PeerCertificates[1:] {
			intermediates.AddCert(c)
			if c.IsCA && subtle.ConstantTimeCompare([]byte(Fingerprint(c.Raw)), []byte(want)) == 1 {
				pinned = c
			}
		}
		opts := x509.VerifyOptions{DNSName: host, Intermediates: intermediates, Roots: roots}
		_, err := leaf.Verify(opts)
		if err == nil {
			return nil
		}
		if pinned == nil {
			return fmt.Errorf("the panel certificate is not from a public certificate authority or from the panel CA %s: %w", want, err)
		}
		opts.Roots = x509.NewCertPool()
		opts.Roots.AddCert(pinned)
		opts.Intermediates = x509.NewCertPool()
		if _, err := leaf.Verify(opts); err != nil {
			return fmt.Errorf("the panel certificate is not valid: %w", err)
		}
		return nil
	}
}

// CheckFingerprint makes sure that the text is a SHA-256 fingerprint in hex.
func CheckFingerprint(fingerprint string) error {
	b, err := hex.DecodeString(fingerprint)
	if err != nil || len(b) != sha256.Size {
		return errors.New("a certificate fingerprint has 64 hex characters (SHA-256)")
	}
	return nil
}
