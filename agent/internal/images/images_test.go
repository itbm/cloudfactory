package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const (
	ref    = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1@sha256:08af88488ce218936d530b80f6f55dc2afe4d0d91f13f9e6ae350a9e7752128e"
	refNew = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.1-1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	layer  = "dd57f90311a2567b55ff22eeacff1602fd04ad7566e315957731359eab173b90"
	image  = "745a3dfee8c74d346f6d0d6d58de5ba21cb2113df5bde601fe1019622640b993"
)

func store(t *testing.T) (*Store, *execx.Fake) {
	dir := t.TempDir()
	f := &execx.Fake{}
	s := &Store{Run: f, Root: filepath.Join(dir, "images"), Config: filepath.Join(dir, "etc", "website-storage.conf"), RunRoot: filepath.Join(dir, "run"), Group: "cf-sites", MountProgram: FuseOverlayfs}
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, f
}

// fakeStore makes the files that "podman pull" makes in a new store, with the modes of Podman.
func fakeStore(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{
		{"", 0o700}, {"overlay", 0o700}, {"overlay/l", 0o755}, {"overlay/" + layer, 0o700}, {"overlay/" + layer + "/diff", 0o750},
		{"overlay/" + layer + "/diff/etc", 0o755}, {"overlay/" + layer + "/work", 0o700},
		{"overlay-images", 0o700}, {"overlay-images/" + image, 0o700}, {"overlay-layers", 0o700}, {"libpod", 0o700},
	} {
		if err := os.MkdirAll(filepath.Join(dir, d.path), 0o700); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(filepath.Join(dir, d.path), d.mode)
	}
	for _, f := range []string{
		"overlay/" + layer + "/link", "overlay/" + layer + "/diff/etc/shadow", "overlay-images/images.json", "overlay-images/" + image + "/manifest",
		"overlay-layers/layers.json", "overlay-layers/volatile-layers.json", "overlay-layers/" + layer + ".tar-split.gz", "libpod/db.sql",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../"+layer+"/diff", filepath.Join(dir, "overlay/l/ABC")); err != nil {
		t.Fatal(err)
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func pullRule(t *testing.T, s *Store, r string) execx.Rule {
	return execx.Rule{Prefix: "podman pull --quiet " + r, Do: func() error {
		fakeStore(t, s.path(".pull-"+StoreName(r)))
		return nil
	}}
}

func TestShareable(t *testing.T) {
	if !Shareable(ref) {
		t.Error("a reference with a digest is not shareable")
	}
	for _, r := range []string{"ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1", "x@sha256:abc", ref + "\n"} {
		if Shareable(r) {
			t.Errorf("%q is shareable", r)
		}
	}
	if StoreName(ref) == StoreName(refNew) || len(StoreName(ref)) != 64 {
		t.Error("the store names are not unique")
	}
}

func TestInitMakesTheRootForTheWebsiteUsers(t *testing.T) {
	s, f := store(t)
	if !f.Called("chown root:cf-sites "+s.Root) || mode(t, s.Root) != 0o750 {
		t.Errorf("calls = %v, mode = %v", f.Calls, mode(t, s.Root))
	}
	conf, err := os.ReadFile(s.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), `mount_program = "/usr/bin/fuse-overlayfs"`) || !strings.Contains(string(conf), "additionalimagestores = [\n]") {
		t.Errorf("config = %s", conf)
	}
	if mode(t, s.Config) != 0o644 {
		t.Errorf("the website users cannot read the config: %v", mode(t, s.Config))
	}

	// A pull that did not finish goes at the next start.
	os.MkdirAll(s.path(".pull-"+StoreName(ref)), 0o700)
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path(".pull-" + StoreName(ref))); !os.IsNotExist(err) {
		t.Error("the unfinished pull is still there")
	}
}

func TestEnsurePullsOnceAndSharesOnlyTheMetadata(t *testing.T) {
	s, f := store(t)
	f.Rules = []execx.Rule{pullRule(t, s, ref)}
	if err := s.Ensure(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	final := s.path(StoreName(ref))
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("no store: %v (calls %v)", err, f.Calls)
	}
	// The pull uses its own storage configuration, as root, with the signature policy.
	env := f.Envs[len(f.Envs)-1]
	if len(env) != 1 || !strings.HasPrefix(env[0], "CONTAINERS_STORAGE_CONF="+s.RunRoot) {
		t.Errorf("env = %v", env)
	}
	if _, err := os.Stat(filepath.Join(s.RunRoot, StoreName(ref))); !os.IsNotExist(err) {
		t.Error("the run directory of the pull is still there")
	}

	for path, want := range map[string]os.FileMode{
		"":                                      0o755,
		"overlay":                               0o755,
		"overlay/" + layer:                      0o755,
		"overlay/" + layer + "/link":            0o644,
		"overlay-images":                        0o755,
		"overlay-images/images.json":            0o644,
		"overlay-images/" + image:               0o755,
		"overlay-images/" + image + "/manifest": 0o644,
		"overlay-layers/layers.json":            0o644,
		"overlay-layers/volatile-layers.json":   0o644,
		// The files of the image keep their modes.
		"overlay/" + layer + "/diff":            0o750,
		"overlay/" + layer + "/diff/etc/shadow": 0o600,
		"overlay/" + layer + "/work":            0o700,
		// Podman's own state stays closed.
		"libpod": 0o700,
		"overlay-layers/" + layer + ".tar-split.gz": 0o600,
	} {
		if got := mode(t, filepath.Join(final, path)); got != want {
			t.Errorf("%s: mode %v, want %v", path, got, want)
		}
	}
	conf, _ := os.ReadFile(s.Config)
	if !strings.Contains(string(conf), `"`+final+`",`) {
		t.Errorf("config = %s", conf)
	}

	// The second website with the same image does not pull.
	f.Calls = nil
	if err := s.Ensure(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if f.Called("podman pull") {
		t.Error("the image was pulled two times")
	}
}

func TestEnsureRefusesATag(t *testing.T) {
	s, f := store(t)
	if err := s.Ensure(context.Background(), "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"); err == nil {
		t.Error("a reference with no digest was accepted")
	}
	if f.Called("podman pull") {
		t.Error("pulled a tag")
	}
}

func TestAFailedPullSharesNothing(t *testing.T) {
	s, f := store(t)
	f.Rules = []execx.Rule{{Prefix: "podman pull", Do: func() error {
		fakeStore(t, s.path(".pull-"+StoreName(ref)))
		return errors.New("Source image rejected: A signature was required, but no signature exists")
	}}}
	if err := s.Ensure(context.Background(), ref); err == nil {
		t.Fatal("the pull did not fail")
	}
	entries, _ := os.ReadDir(s.Root)
	if len(entries) != 0 {
		t.Errorf("the store has %v", entries)
	}
	if conf, _ := os.ReadFile(s.Config); strings.Contains(string(conf), s.Root+"/") {
		t.Errorf("config = %s", conf)
	}
}

func TestShareMetadataDoesNotFollowALink(t *testing.T) {
	dir := t.TempDir()
	fakeStore(t, filepath.Join(dir, "s"))
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte("x"), 0o600)
	os.Remove(filepath.Join(dir, "s", "overlay-images", "images.json"))
	os.Symlink(secret, filepath.Join(dir, "s", "overlay-images", "images.json"))
	if err := ShareMetadata(filepath.Join(dir, "s")); err == nil {
		t.Error("a link was accepted")
	}
	if mode(t, secret) != 0o600 {
		t.Error("the target of the link changed")
	}
}

func TestPruneRemovesAnUnusedImageInTwoSteps(t *testing.T) {
	s, f := store(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	f.Rules = []execx.Rule{pullRule(t, s, ref), pullRule(t, s, refNew)}
	for _, r := range []string{ref, refNew} {
		if err := s.Ensure(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	old, cur := s.path(StoreName(ref)), s.path(StoreName(refNew))

	// A job asked for both images in the last hour, so both stay.
	if err := s.Prune([]string{refNew}); err != nil {
		t.Fatal(err)
	}
	if conf, _ := os.ReadFile(s.Config); !strings.Contains(string(conf), old) {
		t.Errorf("a recent image went: %s", conf)
	}

	// Later, the first prune stops sharing the old image, but keeps its files.
	now = now.Add(2 * time.Hour)
	if err := s.Prune([]string{refNew}); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(s.Config)
	if strings.Contains(string(conf), old) || !strings.Contains(string(conf), cur) {
		t.Errorf("config = %s", conf)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("the first prune removed the files")
	}
	// The next prune removes the files. The image in use stays.
	if err := s.Prune([]string{refNew}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the unused image is still there")
	}
	if _, err := os.Stat(cur); err != nil {
		t.Error("the image in use went")
	}
}

func TestPruneListsAnImageInUseAgain(t *testing.T) {
	s, f := store(t)
	f.Rules = []execx.Rule{pullRule(t, s, ref)}
	if err := s.Ensure(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	// The agent stopped after the pull and before it wrote the config.
	s.mu.Lock()
	s.writeConfig(nil)
	s.recent = nil
	s.mu.Unlock()
	if err := s.Prune([]string{ref}); err != nil {
		t.Fatal(err)
	}
	if conf, _ := os.ReadFile(s.Config); !strings.Contains(string(conf), s.path(StoreName(ref))) {
		t.Errorf("config = %s", conf)
	}
}
