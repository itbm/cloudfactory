package selfupdate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallChecksTheSignatureAndKeepsThePreviousBinary(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	newBin := []byte("new agent")
	digest := sha256.Sum256(newBin)
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	tamper := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0.2.0/cf-agent_linux_amd64":
			if tamper {
				w.Write([]byte("evil agent"))
				return
			}
			w.Write(newBin)
		case "/v0.2.0/cf-agent_linux_amd64.sig":
			w.Write([]byte(base64.StdEncoding.EncodeToString(sig)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "cf-agent")
	os.WriteFile(bin, []byte("old agent"), 0o755)
	u := Updater{ReleaseURL: srv.URL, Binary: bin, Key: &key.PublicKey}

	tamper = true
	if err := u.Install(context.Background(), "0.2.0", "amd64", false); err == nil {
		t.Fatal("a changed binary was accepted")
	}
	if b, _ := os.ReadFile(bin); string(b) != "old agent" {
		t.Fatalf("binary changed after a bad signature: %q", b)
	}

	tamper = false
	if err := u.Install(context.Background(), "0.2.0", "amd64", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new agent" {
		t.Errorf("binary = %q", b)
	}
	if b, _ := os.ReadFile(u.Previous()); string(b) != "old agent" {
		t.Errorf("previous = %q", b)
	}
	if err := u.Rollback(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old agent" {
		t.Errorf("after rollback = %q", b)
	}
	if err := u.Install(context.Background(), "latest", "amd64", false); err == nil {
		t.Error("bad version accepted")
	}
}

// CF-16: a signed older version is refused unless the administrator asks for it.
func TestAnOlderVersionNeedsADowngradeOnPurpose(t *testing.T) {
	u := Updater{Current: "0.2.0"}
	if err := u.CheckVersion("0.1.9", false); err == nil {
		t.Error("an older version was accepted")
	}
	if err := u.CheckVersion("0.1.9", true); err != nil {
		t.Errorf("a downgrade on purpose: %v", err)
	}
	for _, v := range []string{"0.2.0", "0.2.1", "v0.10.0", "1.0.0"} {
		if err := u.CheckVersion(v, false); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	for a, b := range map[string]string{"0.2.0-rc.1": "0.2.0", "0.9.9": "0.10.0", "0.2.0-rc.1x": "0.2.0-rc.2"} {
		if !Older(a, b) || Older(b, a) {
			t.Errorf("Older(%s, %s) is wrong", a, b)
		}
	}
}
