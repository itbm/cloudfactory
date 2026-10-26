package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// InstallHostUpdates installs updates of the host operating system now (FR-1111 to FR-1114).
func (d *Deps) InstallHostUpdates(req *agentv1.HostRequest) (jobs.Job, error) {
	policy := req.GetPolicy()
	if policy == "" {
		policy = "security"
	}
	if policy != "security" && policy != "all" && policy != "manual" {
		return jobs.Job{}, errors.New("the update policy is not valid")
	}
	return jobs.Job{ID: req.GetJobId(), Key: "host", Steps: []jobs.Step{
		{Name: "Download packages", Do: d.Apt.Download},
		{Name: "Install packages", Do: func(ctx context.Context) error { return d.Apt.Install(ctx, policy) }},
		{Name: "Restart changed services only", Do: d.Apt.RestartServices},
	}, Result: func() map[string]any { return map[string]any{"rebootRequired": d.Apt.RebootRequired()} }}, nil
}

// rebootMarker holds the ID of a reboot job, so that the agent can complete it after the reboot.
func (d *Deps) rebootMarker() string { return filepath.Join(filepath.Dir(d.State.Path), "reboot-job") }

func (d *Deps) allServices(ctx context.Context, verb string) error {
	sites, err := d.State.All()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sites {
		u := quadlet.Website{UnixUser: s.UnixUser, Suffix: s.Suffix}
		if _, err := d.Host.Systemctl(ctx, s.UnixUser, verb, u.ServiceName()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Deps) rebootSteps() []jobs.Step {
	return []jobs.Step{
		// Caddy finishes the open requests when its container stops.
		{Name: "Drain the edge proxy", Do: func(context.Context) error { return nil }},
		{Name: "Stop containers", Do: func(ctx context.Context) error { return d.allServices(ctx, "stop") }},
		{Name: "Reboot", Do: func(ctx context.Context) error { return d.Reboot(ctx) }},
		{Name: "Start containers", Do: func(ctx context.Context) error { return d.allServices(ctx, "start") }},
		// FR-1117: after a reboot, all containers and services must operate.
		{Name: "Check all services", Do: func(ctx context.Context) error {
			sites, err := d.State.All()
			if err != nil {
				return err
			}
			var errs []error
			for _, s := range sites {
				if err := d.waitReady(ctx, s.Port, primaryDomain(s.Website)); err != nil {
					errs = append(errs, errors.New(primaryDomain(s.Website)+": "+err.Error()))
				}
			}
			return errors.Join(errs...)
		}},
	}
}

// Reboot reboots the server. The agent writes the job ID first, and completes the last
// two steps when it starts after the reboot (see ResumeReboot).
func (d *Deps) RebootJob(req *agentv1.HostRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetJobId()); err != nil {
		return jobs.Job{}, err
	}
	steps := d.rebootSteps()
	reboot := steps[2].Do
	steps[2].Do = func(ctx context.Context) error {
		if err := os.WriteFile(d.rebootMarker(), []byte(req.GetJobId()), 0o600); err != nil {
			return err
		}
		return reboot(ctx)
	}
	return jobs.Job{ID: req.GetJobId(), Key: "host", Steps: steps}, nil
}

// ResumeReboot gives the rest of a reboot job after a reboot, or false.
func (d *Deps) ResumeReboot() (jobs.Job, bool) {
	b, err := os.ReadFile(d.rebootMarker())
	if err != nil {
		return jobs.Job{}, false
	}
	os.Remove(d.rebootMarker())
	id := strings.TrimSpace(string(b))
	if validate.ID(id) != nil {
		return jobs.Job{}, false
	}
	return jobs.Job{ID: id, Key: "host", Steps: d.rebootSteps(), StartAt: 3}, true
}

// UpdateAgent installs a new agent binary and restarts the agent (FR-1812).
func (d *Deps) UpdateAgent(req *agentv1.UpdateAgentRequest) (jobs.Job, error) {
	if err := d.Updater.CheckVersion(req.GetVersion(), req.GetAllowDowngrade()); err != nil {
		return jobs.Job{}, err
	}
	var bin, sig []byte
	return jobs.Job{ID: req.GetJobId(), Key: "agent", Steps: []jobs.Step{
		{Name: "Download agent binary", Do: func(ctx context.Context) (err error) {
			bin, sig, err = d.Updater.Fetch(ctx, req.GetVersion(), d.Arch)
			return err
		}},
		{Name: "Check signature", Do: func(context.Context) error { return d.Updater.Verify(bin, sig) }},
		{Name: "Keep previous binary for rollback", Do: func(context.Context) error { return d.Updater.KeepPrevious() }},
		{Name: "Restart agent", Do: func(context.Context) error { return d.Updater.Replace(bin) }},
	}, Result: func() map[string]any { return map[string]any{"version": strings.TrimPrefix(req.GetVersion(), "v")} },
		// systemd starts the new binary. If it does not start, systemd runs "cf-agent rollback".
		After: d.Restart}, nil
}

// EnsureEdge starts the edge proxy as the user cf-edge, if it does not operate.
func (d *Deps) EnsureEdge(ctx context.Context, image string, dataDir string) error {
	if _, err := d.Host.Podman(ctx, host.EdgeUser, "image", "exists", image); err != nil {
		if _, err := d.Host.Podman(ctx, host.EdgeUser, "pull", "--quiet", image); err != nil {
			return err
		}
	}
	// The admin socket directory: only the edge user (and root) can open it (CF-13).
	if _, err := d.Run.Run(ctx, "install", "-d", "-o", host.EdgeUser, "-g", host.EdgeUser, "-m", "0700", filepath.Join(dataDir, quadlet.EdgeAdminDir)); err != nil {
		return err
	}
	unit := quadlet.EdgeUnit(quadlet.Edge{Image: image, DataDir: dataDir, Gvisor: d.Gvisor, HTTPPort: 8080, HTTPSPort: 8443})
	changed, err := d.Host.WriteUnit(host.EdgeUser, "cf-edge.container", unit)
	if err != nil {
		return err
	}
	if changed {
		if _, err := d.Host.Systemctl(ctx, host.EdgeUser, "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := d.Host.Systemctl(ctx, host.EdgeUser, "start", "cf-edge.service"); err != nil {
		return err
	}
	return d.Edge.EnsureBase(ctx)
}
