package gvisor

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseServer(t *testing.T, badChecksum bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // version/arch/name
		name := parts[len(parts)-1]
		body := "binary " + parts[0] + " " + strings.TrimSuffix(name, ".sha512")
		if strings.HasSuffix(name, ".sha512") {
			sum := sha512.Sum512([]byte(body))
			if badChecksum {
				sum[0] ^= 1
			}
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + strings.TrimSuffix(name, ".sha512") + "\n"))
			return
		}
		w.Write([]byte(body))
	}))
}

func TestInstallKeepsThePreviousVersion(t *testing.T) {
	srv := releaseServer(t, false)
	defer srv.Close()
	dir := t.TempDir()
	in := Installer{BaseURL: srv.URL, Root: filepath.Join(dir, "lib"), BinDir: dir}
	ctx := context.Background()

	if err := in.Install(ctx, "20260901.0", "x86_64"); err != nil {
		t.Fatal(err)
	}
	if err := in.Install(ctx, "20261020.0", "x86_64"); err != nil {
		t.Fatal(err)
	}
	if got := in.Current(); got != "20261020.0" {
		t.Errorf("current = %s", got)
	}
	b, _ := os.ReadFile(in.Link())
	if string(b) != "binary 20261020.0 runsc" {
		t.Errorf("runsc content = %q", b)
	}
	if err := in.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := in.Current(); got != "20260901.0" {
		t.Errorf("after rollback current = %s", got)
	}
}

func TestInstallRefusesABadChecksum(t *testing.T) {
	srv := releaseServer(t, true)
	defer srv.Close()
	dir := t.TempDir()
	in := Installer{BaseURL: srv.URL, Root: filepath.Join(dir, "lib"), BinDir: dir}

	err := in.Install(context.Background(), "20261020.0", "aarch64")
	if err == nil || !strings.Contains(err.Error(), "SHA-512") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(in.Link()); err == nil {
		t.Error("runsc link exists after a bad checksum")
	}
	if err := in.Install(context.Background(), "../../etc", "x86_64"); err == nil {
		t.Error("bad version accepted")
	}
}
