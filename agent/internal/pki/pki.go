// Package pki makes the certificate of the agent and checks certificate fingerprints.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
