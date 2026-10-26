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
	"sync/atomic"
	"testing"
)

// releaseServer serves testdata/<archive> as gvisor.tar.bz2 for each version and
// architecture, with its checksum file. It counts the downloads of the archive.
func releaseServer(t *testing.T, archive string, badChecksum bool, downloads *atomic.Int32) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", archive))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+Archive+".sha512"):
			sum := sha512.Sum512(body)
			if badChecksum {
				sum[0] ^= 1
			}
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + Archive + "\n"))
		case strings.HasSuffix(r.URL.Path, "/"+Archive):
			if downloads != nil {
				downloads.Add(1)
			}
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallKeepsThePreviousVersion(t *testing.T) {
	var downloads atomic.Int32
	srv := releaseServer(t, "good.tar.bz2", false, &downloads)
	dir := t.TempDir()
	in := Installer{BaseURL: srv.URL, Root: filepath.Join(dir, "lib"), BinDir: dir}
	ctx := context.Background()

	if err := in.Install(ctx, "20261012.0", "x86_64"); err != nil {
		t.Fatal(err)
	}
	if err := in.Install(ctx, "20261019.0", "x86_64"); err != nil {
		t.Fatal(err)
	}
	if got := in.Current(); got != "20261019.0" {
		t.Errorf("current = %s", got)
	}
	b, _ := os.ReadFile(in.Link())
	if string(b) != "binary runsc" {
		t.Errorf("runsc content = %q", b)
	}
	// runsc starts the Sentry from gvisor-bin, next to the real runsc file.
	target, _ := os.Readlink(in.Link())
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "gvisor-bin", "gvisor_sentry")); err != nil {
		t.Errorf("gvisor-bin is not next to runsc: %v", err)
	}
	if st, err := os.Stat(target); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("runsc mode = %v, %v", st.Mode(), err)
	}

	// The same version again does not download the archive again.
	if err := in.Install(ctx, "20261019.0", "x86_64"); err != nil {
		t.Fatal(err)
	}
	if n := downloads.Load(); n != 2 {
		t.Errorf("downloads = %d, want 2", n)
	}

	if err := in.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := in.Current(); got != "20261012.0" {
		t.Errorf("after rollback current = %s", got)
	}
}

func TestInstallRefusesABadChecksum(t *testing.T) {
	srv := releaseServer(t, "good.tar.bz2", true, nil)
	dir := t.TempDir()
	in := Installer{BaseURL: srv.URL, Root: filepath.Join(dir, "lib"), BinDir: dir}

	err := in.Install(context.Background(), "20261019.0", "aarch64")
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

func TestInstallRefusesUnsafeArchives(t *testing.T) {
	for _, archive := range []string{"escape.tar.bz2", "link.tar.bz2"} {
		t.Run(archive, func(t *testing.T) {
			srv := releaseServer(t, archive, false, nil)
			dir := t.TempDir()
			in := Installer{BaseURL: srv.URL, Root: filepath.Join(dir, "lib"), BinDir: dir}
			if err := in.Install(context.Background(), "20261019.0", "x86_64"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Lstat(filepath.Join(dir, "escape")); err == nil {
				t.Error("a file was written out of the version directory")
			}
			if _, err := os.Lstat(in.Link()); err == nil {
				t.Error("runsc link exists after an unsafe archive")
			}
		})
	}
}
