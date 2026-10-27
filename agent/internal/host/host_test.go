package host

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

func fakeHost(t *testing.T, exists bool) (*Host, *execx.Fake) {
	f := &execx.Fake{}
	dir := t.TempDir()
	h := &Host{Run: f, SitesDir: filepath.Join(dir, "sites"), UnitRoot: filepath.Join(dir, "units"), ConfigRoot: filepath.Join(dir, "config"), LookupUID: func(name string) (int, error) {
		if !exists {
			return 0, user.UnknownUserError(name)
		}
		return 1001, nil
	}}
	return h, f
}

func TestEnsureUserMakesTheUserOnce(t *testing.T) {
	h, f := fakeHost(t, false)
	// useradd makes the user.
	f.Rules = []execx.Rule{{Prefix: "useradd", Do: func() error {
		h.LookupUID = func(string) (int, error) { return 1001, nil }
		return nil
	}}}
	if err := h.EnsureUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	if !f.Called("useradd --create-home --home-dir "+h.Home("acme_x7k2")+" --shell /usr/sbin/nologin") || !f.Called("loginctl enable-linger acme_x7k2") || !f.Called("chmod 0700 "+h.Home("acme_x7k2")) || !f.Called("systemctl start user@1001.service") {
		t.Errorf("calls = %v", f.Calls)
	}
	// CF-04: the edge proxy is not in the group of the website user.
	if f.Called("usermod") {
		t.Errorf("the edge user got the group of the website: %v", f.Calls)
	}

	h, f = fakeHost(t, true)
	if err := h.EnsureUser(context.Background(), "acme_x7k2"); err != nil {
		t.Fatal(err)
	}
	if f.Called("useradd") {
		t.Error("useradd ran for a user that exists")
	}
	if err := h.EnsureUser(context.Background(), "root"); err == nil {
		t.Error("the user root was accepted")
	}
}

func TestWriteUnitReportsAChange(t *testing.T) {
	h, _ := fakeHost(t, true)
	changed, err := h.WriteUnit("acme_x7k2", "cf-acme_x7k2.container", "a")
	if err != nil || !changed {
		t.Fatalf("first write: %v %v", changed, err)
	}
	if changed, _ := h.WriteUnit("acme_x7k2", "cf-acme_x7k2.container", "a"); changed {
		t.Error("same content reported a change")
	}
	if _, err := os.Stat(filepath.Join(h.UnitRoot, "1001", "cf-acme_x7k2.container")); err != nil {
		t.Error(err)
	}
	if _, err := h.WriteUnit("acme_x7k2", "../evil", "a"); err == nil {
		t.Error("a path in the unit name was accepted")
	}
}

func TestPodmanRunsAsTheUser(t *testing.T) {
	h, f := fakeHost(t, true)
	h.Podman(context.Background(), "acme_x7k2", "pull", "img")
	if !f.Called("runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/1001 podman pull img") {
		t.Errorf("calls = %v", f.Calls)
	}
}
