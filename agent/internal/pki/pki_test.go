package pki

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateAndPin(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")
	if err := Generate(cert, key, []string{"web-1.internal", "10.0.0.5"}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
	fp, pemBytes, err := FileFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp) != 64 {
		t.Fatalf("fingerprint %q", fp)
	}
	block, _ := pem.Decode(pemBytes)

	if err := PinnedClient(fp)([][]byte{block.Bytes}, nil); err != nil {
		t.Errorf("pinned certificate refused: %v", err)
	}
	if err := PinnedClient(fp)(nil, nil); err == nil {
		t.Error("no certificate accepted")
	}
	other := filepath.Join(dir, "b.crt")
	if err := Generate(other, filepath.Join(dir, "b.key"), []string{"x"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	_, otherPEM, _ := FileFingerprint(other)
	ob, _ := pem.Decode(otherPEM)
	if err := PinnedClient(fp)([][]byte{ob.Bytes}, nil); err == nil {
		t.Error("a different certificate was accepted")
	}
	if err := PinnedClient("")([][]byte{block.Bytes}, nil); err == nil {
		t.Error("an empty pin accepted a certificate")
	}
}
