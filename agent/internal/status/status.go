// Package status collects the actual state of the host for the panel (FR-1118).
package status

import (
	"context"
	"os"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// Collector reads the state of the host.
type Collector struct {
	Run           execx.Runner
	Apt           hostupdate.Apt
	AgentVersion  string
	GVisorVersion func() string
	// Services are the system services to check, for example "cf-agent".
	Services []string
	// UserServices are services of other users, as "user:service", for example "cf-edge:cf-edge".
	UserServices []string
}

// Report gives the report for the panel. A part that the agent cannot read stays empty.
func (c Collector) Report(ctx context.Context) panel.Report {
	r := panel.Report{AgentVersion: c.AgentVersion, PendingUpdates: []any{}, Services: []panel.Service{}, Websites: []panel.Usage{}}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		r.Kernel = strings.TrimSpace(string(b))
	}
	if out, err := c.Run.Run(ctx, "podman", "version", "--format", "{{.Client.Version}}"); err == nil {
		r.PodmanVersion = strings.TrimSpace(out)
	}
	if c.GVisorVersion != nil {
		r.GVisorVersion = c.GVisorVersion()
	}
	if updates, err := c.Apt.Pending(ctx); err == nil {
		for _, u := range updates {
			r.PendingUpdates = append(r.PendingUpdates, u)
		}
	}
	r.RebootRequired = c.Apt.RebootRequired()
	for _, s := range c.Services {
		r.Services = append(r.Services, panel.Service{Name: s, State: c.state(ctx, "systemctl", "is-active", s)})
	}
	for _, us := range c.UserServices {
		user, svc, _ := strings.Cut(us, ":")
		r.Services = append(r.Services, panel.Service{Name: svc, State: c.state(ctx, "systemctl", "--user", "--machine="+user+"@", "is-active", svc)})
	}
	return r
}

func (c Collector) state(ctx context.Context, name string, args ...string) string {
	out, _ := c.Run.Run(ctx, name, args...)
	switch strings.TrimSpace(out) {
	case "active", "activating", "reloading":
		return "running"
	case "failed":
		return "failed"
	}
	return "stopped"
}
