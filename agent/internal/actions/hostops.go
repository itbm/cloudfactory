package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// InstallHostUpdates installs updates of the host operating system now (FR-1111 to FR-1114).
func (d *Deps) InstallHostUpdates(req *agentv1.HostRequest) (jobs.Job, error) {
	policy := req.GetPolicy()
	if policy == "" {
		policy = "security"
	}
	if policy != "security" && policy != "all" && policy != "manual" && policy != "tools" {
		return jobs.Job{}, errors.New("the update policy is not valid")
	}
	return jobs.Job{ID: req.GetJobId(), Key: "host", Long: true, Steps: []jobs.Step{
		{Name: "Download packages", Do: d.Apt.Download},
		{Name: "Install packages", Do: func(ctx context.Context) error { return d.Apt.Install(ctx, policy) }},
		{Name: "Restart changed services only", Do: func(ctx context.Context) error {
			if err := d.Apt.RestartServices(ctx); err != nil {
				return err
			}
			if policy != "tools" {
				return nil
			}
			// New container tools: each website must still answer, or the job fails and the
			// panel stops the update before the other servers (FR-1113).
			return d.checkAllWebsites(ctx)
		}},
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
		if s.Stopped && verb == "start" {
			continue
		}
		u := quadlet.Website{UnixUser: s.UnixUser, Suffix: s.Suffix}
		if _, err := d.Host.Systemctl(ctx, s.UnixUser, verb, u.ServiceName()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// checkAllWebsites waits until each website that is not stopped answers.
func (d *Deps) checkAllWebsites(ctx context.Context) error {
	sites, err := d.State.All()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sites {
		if s.Stopped {
			continue
		}
		if err := d.waitReady(ctx, s.Port, primaryDomain(s.Website)); err != nil {
			errs = append(errs, errors.New(primaryDomain(s.Website)+": "+err.Error()))
		}
	}
	return errors.Join(errs...)
}

// rebootAndWait starts the reboot and waits for the shutdown.
//
// Audit finding R10: "systemctl reboot" returns immediately. If this step ended then, the job
// would continue with "Start containers" while the server shuts down, fail, and report
// "failed". Thus the step waits until the agent gets the stop signal of the shutdown, and
// then ends the job with no result (jobs.ErrInterrupted). After the reboot, the agent
// continues the job (ResumeReboot), and only that part sends the result.
func (d *Deps) rebootAndWait(ctx context.Context) error {
	if err := d.Reboot(ctx); err != nil {
		d.clearRebootMark(ctx)
		return err
	}
	wait := d.RebootWait
	if wait == 0 {
		wait = 10 * time.Minute
	}
	select {
	case <-d.Stopping:
		return jobs.ErrInterrupted
	case <-time.After(wait):
		// The server did not shut down. The job must not continue after a later restart.
		d.clearRebootMark(ctx)
		return fmt.Errorf("the server did not shut down in %s after the reboot command", wait)
	}
}

func (d *Deps) rebootSteps() []jobs.Step {
	return []jobs.Step{
		// Caddy finishes the open requests when its container stops.
		{Name: "Drain the edge proxy", Do: func(context.Context) error { return nil }},
		{Name: "Stop containers", Do: func(ctx context.Context) error { return d.allServices(ctx, "stop") }},
		{Name: "Reboot", Do: d.rebootAndWait},
		{Name: "Start containers", Do: func(ctx context.Context) error { return d.allServices(ctx, "start") }},
		// FR-1117: after a reboot, all containers and services must operate.
		{Name: "Check all services", Do: d.checkAllWebsites},
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
		if err := d.markReboot(ctx, req.GetJobId()); err != nil {
			return err
		}
		return reboot(ctx)
	}
	return jobs.Job{ID: req.GetJobId(), Key: "host", Steps: steps}, nil
}

// ResumeReboot gives the rest of a reboot job after a reboot, or false.
func (d *Deps) ResumeReboot() (jobs.Job, bool) {
	id := d.takeRebootMark(context.Background())
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
	var rel selfupdate.Release
	return jobs.Job{ID: req.GetJobId(), Key: "agent", Steps: []jobs.Step{
		{Name: "Download agent binary", Do: func(ctx context.Context) (err error) {
			rel, err = d.Updater.Fetch(ctx, req.GetVersion(), d.Arch)
			return err
		}},
		{Name: "Check signature", Do: func(context.Context) error { return d.Updater.Verify(rel) }},
		{Name: "Keep previous binary for rollback", Do: func(context.Context) error { return d.keepAgent() }},
		// The root helper checks the version and the signature again before it changes the binary.
		{Name: "Restart agent", Do: func(context.Context) error { return d.replaceAgent(rel, req.GetAllowDowngrade()) }},
	}, Result: func() map[string]any { return map[string]any{"version": strings.TrimPrefix(req.GetVersion(), "v")} },
		// systemd starts the new binary. If it does not start, systemd runs "cf-agent rollback".
		After: d.Restart}, nil
}

// EnsureEdge starts the edge proxy as the user cf-edge, if it does not operate.
//
// Audit finding S7: when the unit changed, for example with the image of a new agent version
// or a different gVisor platform, the agent starts the edge proxy again. Without that, the
// proxy continues with the old image until the next reboot. Caddy keeps its routes in the
// data directory, and loads them when it starts.
func (d *Deps) EnsureEdge(ctx context.Context, image string, dataDir string) error {
	if _, err := d.Host.Podman(ctx, host.EdgeUser, "image", "exists", image); err != nil {
		if _, err := d.Host.Podman(ctx, host.EdgeUser, "pull", "--quiet", image); err != nil {
			return err
		}
	}
	// The admin socket directory, and the unit.
	changed, err := d.prepareEdge(ctx, image, dataDir)
	if err != nil {
		return err
	}
	verb := "start"
	if changed {
		if _, err := d.Host.Systemctl(ctx, host.EdgeUser, "daemon-reload"); err != nil {
			return err
		}
		verb = "restart"
	}
	if _, err := d.Host.Systemctl(ctx, host.EdgeUser, verb, "cf-edge.service"); err != nil {
		return err
	}
	// Caddy makes its admin socket some seconds after the container starts.
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		err := d.Edge.EnsureBase(ctx)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// ConfigureHost changes the gVisor platform of the server (FR-310). The agent writes the units
// of all containers again and restarts them, one at a time.
func (d *Deps) ConfigureHost(req *agentv1.ConfigureHostRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetJobId()); err != nil {
		return jobs.Job{}, err
	}
	platform := req.GetGvisorPlatform()
	if platform != "systrap" && platform != "kvm" {
		return jobs.Job{}, errors.New("the gVisor platform must be systrap or kvm")
	}
	return jobs.Job{ID: req.GetJobId(), Key: "host", Steps: []jobs.Step{
		{Name: "Save settings", Do: func(ctx context.Context) error {
			if platform == "kvm" {
				if _, err := os.Stat("/dev/kvm"); err != nil {
					return errors.New("the KVM platform needs /dev/kvm, which this server does not have")
				}
			}
			return d.savePlatform(platform)
		}},
		{Name: "Restart websites", Do: func(ctx context.Context) error {
			sites, err := d.State.All()
			if err != nil {
				return err
			}
			var errs []error
			for _, s := range sites {
				if err := d.restartWithUnit(ctx, s); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", primaryDomain(s.Website), err))
				}
			}
			return errors.Join(errs...)
		}},
		{Name: "Restart services", Do: func(ctx context.Context) error {
			var errs []error
			if d.EdgeImage != "" {
				errs = append(errs, d.EnsureEdge(ctx, d.EdgeImage, d.DataDir))
			}
			d.dbMu.Lock()
			d.dbReady = nil
			d.dbMu.Unlock()
			errs = append(errs, d.RefreshDatabaseServices(ctx))
			if d.SftpImage != "" {
				errs = append(errs, d.EnsureSftp(ctx))
			}
			return errors.Join(errs...)
		}},
	}}, nil
}

// restartWithUnit writes the unit of a website again and restarts it, if it operates. A
// stopped website stays stopped.
func (d *Deps) restartWithUnit(ctx context.Context, s *Site) error {
	u := d.unit(s.Website, s.Port, s.Suffix)
	u.Stopped = s.Stopped
	if _, err := d.Host.WriteUnit(s.UnixUser, u.UnitName(), quadlet.ContainerUnit(u)); err != nil {
		return err
	}
	if _, err := d.Host.Systemctl(ctx, s.UnixUser, "daemon-reload"); err != nil {
		return err
	}
	if s.Stopped {
		return nil
	}
	if _, err := d.Host.Systemctl(ctx, s.UnixUser, "restart", u.ServiceName()); err != nil {
		return err
	}
	return d.waitReady(ctx, s.Port, primaryDomain(s.Website))
}
