package status

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
)

func TestReport(t *testing.T) {
	f := &execx.Fake{Rules: []execx.Rule{
		{Prefix: "podman version", Output: "5.4.2\n"},
		{Prefix: "apt-get -s", Output: "Inst openssl [3.5.1-1] (3.5.1-2 Debian-Security:13/stable-security [amd64])\n"},
		{Prefix: "systemctl is-active cf-agent", Output: "active\n"},
		{Prefix: "systemctl --user --machine=cf-edge@ is-active cf-edge", Output: "failed\n"},
	}}
	c := Collector{Run: f, Apt: hostupdate.Apt{Run: f, RebootFile: filepath.Join(t.TempDir(), "none")}, AgentVersion: "0.1.0",
		GVisorVersion: func() string { return "20261019.0" }, Services: []string{"cf-agent"}, UserServices: []string{"cf-edge:cf-edge"}}

	r := c.Report(context.Background())
	if r.PodmanVersion != "5.4.2" || r.GVisorVersion != "20261019.0" || len(r.PendingUpdates) != 1 || r.RebootRequired {
		t.Errorf("report = %+v", r)
	}
	if r.Services[0].State != "running" || r.Services[1].State != "failed" {
		t.Errorf("services = %+v", r.Services)
	}
}
