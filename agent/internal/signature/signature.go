// Package signature checks the signatures of release files, for example a new agent binary
// (FR-1812). The release pipeline signs each file with "cosign sign-blob --key". The
// signature is ECDSA P-256 over the SHA-256 digest of the file, in base64.
package signature

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ParsePublicKey reads a PEM public key, for example the file "cosign.pub".
func ParsePublicKey(pemBytes []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("signature: the key is not a PEM public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("signature: the key is not an ECDSA key")
	}
	return ec, nil
}

// Verify checks a base64 signature of data.
func Verify(key *ecdsa.PublicKey, data []byte, sigBase64 string) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigBase64))
	if err != nil {
		return fmt.Errorf("signature: not base64: %w", err)
	}
	digest := sha256.Sum256(data)
	if !ecdsa.VerifyASN1(key, digest[:], sig) {
		return errors.New("signature: the signature is not valid")
	}
	return nil
}
