// Package status collects the actual state of the host for the panel (FR-1118).
package status

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

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
	// ProcDir is "/proc". Tests use a different directory.
	ProcDir string
	// DiskPath is the file system to measure, for example "/srv/cloudfactory".
	DiskPath string
	// Extra adds the parts of the report that the actions know, for example the use of each
	// website.
	Extra func(ctx context.Context, r *panel.Report)
}

// Report gives the report for the panel. A part that the agent cannot read stays empty.
func (c Collector) Report(ctx context.Context) panel.Report {
	r := panel.Report{AgentVersion: c.AgentVersion, PendingUpdates: []any{}, Services: []panel.Service{}, Websites: []panel.Usage{}, Domains: []panel.DomainState{}, Databases: []panel.DatabaseSize{}}
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
	r.Usage = c.hostUse()
	if c.Extra != nil {
		c.Extra(ctx, &r)
	}
	return r
}

// hostUse gives the CPU load, the memory use and the disk use. The CPU value is the load
// average of one minute for each CPU, so that it needs no second sample.
func (c Collector) hostUse() *panel.HostUse {
	proc := c.ProcDir
	if proc == "" {
		proc = "/proc"
	}
	u := &panel.HostUse{}
	if b, err := os.ReadFile(filepath.Join(proc, "loadavg")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if load, err := strconv.ParseFloat(f[0], 64); err == nil {
				u.CPUPercent = percent(load, float64(runtime.NumCPU()))
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(proc, "meminfo")); err == nil {
		mem := map[string]float64{}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 {
				v, _ := strconv.ParseFloat(f[1], 64)
				mem[strings.TrimSuffix(f[0], ":")] = v
			}
		}
		if total := mem["MemTotal"]; total > 0 {
			u.MemoryPercent = percent(total-mem["MemAvailable"], total)
		}
	}
	path := c.DiskPath
	if path == "" {
		path = "/"
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err == nil && fs.Blocks > 0 {
		used := float64(fs.Blocks-fs.Bfree) * float64(fs.Bsize)
		avail := float64(fs.Bavail) * float64(fs.Bsize)
		u.DiskPercent = percent(used, used+avail)
	}
	return u
}

// percent gives part/whole as a percent from 0 to 100, with one decimal.
func percent(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return math.Round(math.Min(100, math.Max(0, part/whole*100))*10) / 10
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
