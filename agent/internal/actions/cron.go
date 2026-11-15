package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/cron"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The cron jobs of a website. The agent keeps them in its state and runs each command in the
// container of the website: as the Unix user of the website, in the gVisor sandbox, with the
// limits of the website. The host has no crontab of a website.

// CronEntry is one cron job in the state of the agent.
type CronEntry struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Enabled  bool   `json:"enabled"`
}

// cronRuns has the cron jobs that run now and the last result of each cron job.
type cronRuns struct {
	mu      sync.Mutex
	running map[string]bool
	last    map[string]*cronResult
	// slots limits the commands that run at the same time on the server.
	slots chan struct{}
}

type cronResult struct {
	run panel.CronRun
	// sent is the number of status reports that had this result.
	sent int
}

const (
	// cronSlots is the number of cron commands that run at the same time on a server.
	cronSlots = 8
	// cronReports is the number of status reports that have the result of a run. The panel
	// can miss a report.
	cronReports = 3
	// cronOutputMax is the size of the end of the output that the agent keeps.
	cronOutputMax = 4000
)

// SetCron records the cron jobs of a website.
func (s *State) SetCron(websiteID string, entries []CronEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	if f.Sites[websiteID] == nil {
		return ErrUnknownWebsite
	}
	if f.Cron == nil {
		f.Cron = map[string][]CronEntry{}
	}
	if len(entries) == 0 {
		delete(f.Cron, websiteID)
	} else {
		f.Cron[websiteID] = entries
	}
	return s.write(f)
}

// cronSites gives each website that has cron jobs, with its cron jobs.
func (s *State) cronSites() (map[*Site][]CronEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := map[*Site][]CronEntry{}
	for id, entries := range f.Cron {
		if site := f.Sites[id]; site != nil {
			out[site] = entries
		}
	}
	return out, nil
}

func checkCronJob(c *agentv1.CronJob) error {
	if err := errors.Join(validate.ID(c.GetId()), validate.CronSchedule(c.GetSchedule()), validate.CommandLine(c.GetCommand())); err != nil {
		return err
	}
	_, err := cron.Parse(c.GetSchedule())
	return err
}

// SetCronJobs replaces the cron jobs of a website.
func (d *Deps) SetCronJobs(req *agentv1.SetCronJobsRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetWebsiteId()); err != nil {
		return jobs.Job{}, err
	}
	if len(req.GetCronJobs()) > validate.MaxCronJobs {
		return jobs.Job{}, fmt.Errorf("a website can have %d cron jobs", validate.MaxCronJobs)
	}
	entries := make([]CronEntry, 0, len(req.GetCronJobs()))
	seen := map[string]bool{}
	for _, c := range req.GetCronJobs() {
		if err := checkCronJob(c); err != nil {
			return jobs.Job{}, err
		}
		if seen[c.GetId()] {
			return jobs.Job{}, errors.New("two cron jobs have the same ID")
		}
		seen[c.GetId()] = true
		entries = append(entries, CronEntry{ID: c.GetId(), Schedule: c.GetSchedule(), Command: c.GetCommand(), Enabled: c.GetEnabled()})
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Save the cron jobs", Do: func(context.Context) error {
			return d.State.SetCron(req.GetWebsiteId(), entries)
		}},
	}}, nil
}

// RunCronJob runs the command of a cron job now. The job fails when the command fails.
func (d *Deps) RunCronJob(req *agentv1.RunCronJobRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), checkCronJob(req.GetCronJob())); err != nil {
		return jobs.Job{}, err
	}
	c := req.GetCronJob()
	var run panel.CronRun
	// The key is not the website: a long command does not stop the other jobs of the website.
	return jobs.Job{ID: req.GetJobId(), Key: "cron:" + req.GetWebsiteId() + ":" + c.GetId(), Long: true, Steps: []jobs.Step{
		{Name: "Run the command", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if site == nil {
				return ErrUnknownWebsite
			}
			if site.Stopped {
				return errors.New("the website is stopped; start it first")
			}
			run = d.runCron(ctx, site, CronEntry{ID: c.GetId(), Command: c.GetCommand()})
			if run.ExitCode != 0 {
				return fmt.Errorf("the command ended with exit code %d: %s", run.ExitCode, lastLines(run.Output, 600))
			}
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"cronJobId": c.GetId(), "exitCode": run.ExitCode, "output": run.Output, "ranAt": run.At}
	}}, nil
}

var cronExit = regexp.MustCompile(`(?s)^(.*)cf-exit:(\d+)\n?$`)

// cronScript runs the command in the container and then writes the exit code. The output of
// the command has its standard output and its standard error. "timeout" stops a command
// that does not end. "tail" reads all the output and gives only the end of it to the agent,
// so a command that writes much output continues and uses no memory of the agent
// (adversarial review, finding G5-01). The agent keeps cronOutputMax bytes of that end.
const cronScript = `{ timeout -k 10 "$1" podman exec --workdir /app "$2" sh -c "$3" 2>&1; echo "cf-exit:$?"; } | tail -c 16384`

// runCron runs the command of a cron job in the container of the website and gives the result.
func (d *Deps) runCron(ctx context.Context, site *Site, e CronEntry) panel.CronRun {
	limit := orDefault(d.CronLimit, time.Hour)
	run := panel.CronRun{WebsiteID: site.ID, ID: e.ID, At: time.Now().UTC().Format(time.RFC3339), ExitCode: -1}
	uid, err := d.Host.LookupUID(site.UnixUser)
	if err != nil {
		run.Output = "the website has no Unix user"
		return run
	}
	container := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}.ContainerName()
	ctx, cancel := context.WithTimeout(ctx, limit+time.Minute)
	defer cancel()
	out, err := d.Run.Run(ctx, "runuser", "-u", site.UnixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(uid),
		"sh", "-c", cronScript, "sh", strconv.Itoa(int(limit.Seconds())), container, e.Command)
	m := cronExit.FindStringSubmatch(out)
	switch {
	case m != nil:
		run.Output = m[1]
		run.ExitCode, _ = strconv.Atoi(m[2])
		if run.ExitCode == 124 {
			run.Output += fmt.Sprintf("\nThe command did not end in %s. The agent stopped it.", limit)
		}
	case err != nil:
		run.Output = "the command did not start: " + err.Error()
	default:
		run.Output = out
	}
	run.Output = lastLines(strings.ToValidUTF8(run.Output, "�"), cronOutputMax)
	return run
}

// lastLines gives the end of a text, at most max bytes.
func lastLines(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "…" + strings.ToValidUTF8(s[len(s)-max:], "")
}

// RunCron runs the cron jobs of all websites until ctx ends. It examines the schedules one
// time each minute. The time of a schedule is UTC.
func (d *Deps) RunCron(ctx context.Context) {
	for {
		now := time.Now()
		wait := time.NewTimer(now.Truncate(time.Minute).Add(time.Minute).Sub(now))
		select {
		case <-ctx.Done():
			wait.Stop()
			return
		case t := <-wait.C:
			d.CronTick(ctx, t.UTC())
		}
	}
}

// CronTick starts each cron job whose schedule agrees with the minute of now. A cron job
// that still runs from an earlier minute does not start again. A stopped website has no runs.
func (d *Deps) CronTick(ctx context.Context, now time.Time) {
	sites, err := d.State.cronSites()
	if err != nil {
		slog.Error("read the cron jobs", "err", err)
		return
	}
	c := &d.cron
	c.mu.Lock()
	if c.running == nil {
		c.running, c.last, c.slots = map[string]bool{}, map[string]*cronResult{}, make(chan struct{}, cronSlots)
	}
	c.mu.Unlock()
	for site, entries := range sites {
		if site.Stopped {
			continue
		}
		for _, e := range entries {
			s, err := cron.Parse(e.Schedule)
			if !e.Enabled || err != nil || !s.Matches(now) {
				continue
			}
			key := site.ID + "/" + e.ID
			c.mu.Lock()
			busy := c.running[key]
			c.running[key] = true
			c.mu.Unlock()
			if busy {
				continue
			}
			go func() {
				defer func() {
					c.mu.Lock()
					delete(c.running, key)
					c.mu.Unlock()
				}()
				select {
				case c.slots <- struct{}{}:
					defer func() { <-c.slots }()
				case <-ctx.Done():
					return
				}
				run := d.runCron(ctx, site, e)
				if ctx.Err() != nil {
					return
				}
				c.mu.Lock()
				c.last[key] = &cronResult{run: run}
				c.mu.Unlock()
			}()
		}
	}
}

// cronReport gives the results of the cron jobs that the panel does not have yet. Each
// result is in more than one report, because the panel can miss a report.
func (d *Deps) cronReport() []panel.CronRun {
	c := &d.cron
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []panel.CronRun
	for key, r := range c.last {
		if r.sent >= cronReports {
			delete(c.last, key)
			continue
		}
		r.sent++
		out = append(out, r.run)
	}
	return out
}
