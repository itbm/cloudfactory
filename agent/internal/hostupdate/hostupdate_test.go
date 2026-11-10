package hostupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !f.Called("unattended-upgrade") || f.Called("apt-get -y -o") || !f.Called("needrestart -r a -l") {
		t.Errorf("calls = %v", f.Calls)
	}
	// Audit finding R9: in batch mode, needrestart restarts nothing.
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "needrestart") && strings.Contains(c+" ", " -b ") {
			t.Errorf("needrestart runs in batch mode: %s", c)
		}
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

// Audit batch 2, findings 8 and 9: the lists are new, and the held container tools have updates.
func TestPendingReadsNewListsAndHeldTools(t *testing.T) {
	f := &execx.Fake{Rules: []execx.Rule{
		{Prefix: "apt-get -s -o Debug::NoLocking=1 install", Output: "Inst podman [5.4.2-1] (5.4.2-2 Debian:13.2/stable [amd64])\n"},
		{Prefix: "apt-get -s", Output: sample},
	}}
	a := Apt{Run: f, ListsFile: filepath.Join(t.TempDir(), "pkgcache.bin"), ListsMaxAge: time.Hour}
	got, err := a.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !f.Called("apt-get -qq update") || len(got) != 5 || !got[4].ContainerTool || got[4].Name != "podman" || got[0].ContainerTool {
		t.Errorf("updates = %+v, calls = %v", got, f.Calls)
	}
	// New lists: no update.
	os.WriteFile(a.ListsFile, nil, 0o644)
	f.Calls = nil
	a.Pending(context.Background())
	if f.Called("apt-get -qq update") {
		t.Error("the lists are new, but apt-get update ran")
	}
	f.Calls = nil
	if err := a.Install(context.Background(), "tools"); err != nil || !f.Called("apt-get -y -o Dpkg::Options::=--force-confold install --only-upgrade --allow-change-held-packages podman conmon") || !f.Called("apt-mark hold podman") {
		t.Errorf("tools: %v %v", err, f.Calls)
	}
}
