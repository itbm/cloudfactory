package hostupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const sample = `Reading package lists...
Building dependency tree...
The following packages will be upgraded:
  libssl3t64 openssl tzdata
Inst libssl3t64 [3.5.1-1] (3.5.1-2 Debian-Security:13/stable-security [amd64])
Inst openssl [3.5.1-1] (3.5.1-2 Debian-Security:13/stable-security [amd64])
Inst tzdata [2026a-1] (2026b-0+deb13u1 Debian:13.2/stable [all])
Inst linux-image-6.12.0-9-amd64 (6.12.48-1 Debian-Security:13/stable-security [amd64])
Conf openssl (3.5.1-2 Debian-Security:13/stable-security [amd64])`

func TestParse(t *testing.T) {
	got := Parse(sample)
	if len(got) != 4 {
		t.Fatalf("got %d updates: %+v", len(got), got)
	}
	if got[1] != (Update{Name: "openssl", From: "3.5.1-1", To: "3.5.1-2", Security: true}) {
		t.Errorf("openssl = %+v", got[1])
	}
	if got[2].Security {
		t.Error("tzdata is not a security update")
	}
	if got[3].From != "" || got[3].Name != "linux-image-6.12.0-9-amd64" {
		t.Errorf("new package = %+v", got[3])
	}
}

func TestInstallPolicies(t *testing.T) {
	f := &execx.Fake{}
	a := Apt{Run: f, RebootFile: filepath.Join(t.TempDir(), "reboot-required")}
	ctx := context.Background()

	if err := a.Download(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Install(ctx, "security"); err != nil {
		t.Fatal(err)
	}
	if err := a.RestartServices(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.Called("unattended-upgrade") || f.Called("apt-get -y -o") || !f.Called("needrestart -r a") {
		t.Errorf("calls = %v", f.Calls)
	}
	f.Calls = nil
	if err := a.Install(ctx, "all"); err != nil || !f.Called("apt-get -y -o Dpkg::Options::=--force-confold upgrade") {
		t.Errorf("all: %v %v", err, f.Calls)
	}
	if err := a.Install(ctx, "everything"); err == nil {
		t.Error("bad policy accepted")
	}
	if a.RebootRequired() {
		t.Error("reboot required with no file")
	}
	os.WriteFile(a.RebootFile, nil, 0o644)
	if !a.RebootRequired() {
		t.Error("reboot file ignored")
	}
	if err := a.Hold(ctx); err != nil || !f.Called("apt-mark hold "+strings.Join(Held, " ")) {
		t.Errorf("hold: %v", f.Calls)
	}
}
