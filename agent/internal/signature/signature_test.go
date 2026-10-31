package signature

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
)

// Sign makes a signature in the same format as cosign sign-blob. Tests use it.
func sign(t *testing.T, key *ecdsa.PrivateKey, data []byte) string {
	digest := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub, err := ParsePublicKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("cf-agent binary")
	sig := sign(t, key, data)

	if err := Verify(pub, data, sig+"\n"); err != nil {
		t.Errorf("good signature: %v", err)
	}
	if err := Verify(pub, []byte("changed binary"), sig); err == nil {
		t.Error("changed data accepted")
	}
	if err := Verify(pub, data, "not base64!"); err == nil {
		t.Error("bad base64 accepted")
	}
	if _, err := ParsePublicKey([]byte("junk")); err == nil {
		t.Error("junk key accepted")
	}
}
