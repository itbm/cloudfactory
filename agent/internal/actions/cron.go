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
	// site limits the commands of each website that run at the same time.
	site map[string]chan struct{}
}

type cronResult struct {
	run panel.CronRun
	// sent is the number of status reports that had this result.
	sent int
}

const (
	// cronSlots is the number of scheduled cron commands that run at the same time on a server.
	cronSlots = 32
	// cronSiteSlots is the number of scheduled cron commands of one website that run at the
	// same time. Without this limit, the 20 cron jobs of one website held all the slots of
	// the server for an hour (adversarial review, findings A2-02 and G5-04).
	cronSiteSlots = 2
	// cronWait is the longest time that a scheduled run waits for a slot. A run that waits
	// longer does not start: the schedule did not ask for a run at the later time.
	cronWait = time.Minute
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
			// A manual run does not start next to a scheduled run of the same cron job.
			key := site.ID + "/" + c.GetId()
			if !d.cron.start(key) {
				return errors.New("this cron job runs now; try again when the run ends")
			}
			defer d.cron.end(key)
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

var containerExit = regexp.MustCompile(`(?s)^(.*)cf-exit:(\d+)\n?$`)

// containerScript runs a command in the container of a website and then writes the exit code.
// The output of the command has its standard output and its standard error. "timeout" stops a
// command that does not end. "tail" reads all the output and gives only the end of it ($3
// bytes) to the agent, so a command that writes much output continues and uses no memory of
// the agent (adversarial review, finding G5-01). The arguments are the time limit in seconds,
// the container, the number of bytes and the command.
//
// The first "timeout" is in the container, where it stops the command. The second "timeout"
// is on the host and stops only "podman exec": the command of an exec session continues in
// the container when its client stops (finding G5-13). It is for an image with no "timeout".
const containerScript = `{ timeout -k 10 "$(($1 + 30))" podman exec --workdir /app "$2" sh -c 'if command -v timeout >/dev/null 2>&1; then exec timeout -k 10 "$0" sh -c "$1"; fi; exec sh -c "$1"' "$1" "$4" 2>&1; echo "cf-exit:$?"; } | tail -c "$3"`

// containerRun is the result of a command in the container of a website.
type containerRun struct {
	Output   string
	ExitCode int
	// TimedOut is true when the agent stopped the command at its time limit.
	TimedOut bool
}

// runInContainer runs a command line in the container of a website, as the Unix user of the
// website, and gives the end of the output (at most keep bytes) and the exit code. The exit
// code is -1 when the command did not start.
func (d *Deps) runInContainer(ctx context.Context, site *Site, command string, limit time.Duration, keep int) containerRun {
	run := containerRun{ExitCode: -1}
	uid, err := d.Host.LookupUID(site.UnixUser)
	if err != nil {
		run.Output = "the website has no Unix user"
		return run
	}
	container := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}.ContainerName()
	ctx, cancel := context.WithTimeout(ctx, limit+time.Minute)
	defer cancel()
	// "tail" reads 4 times the size that the agent keeps: the text is valid UTF-8 again after
	// the cut, and the lines at the end are complete.
	out, err := d.Run.Run(ctx, "runuser", "-u", site.UnixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(uid),
		"sh", "-c", containerScript, "sh", strconv.Itoa(int(limit.Seconds())), container, strconv.Itoa(4*keep), command)
	m := containerExit.FindStringSubmatch(out)
	switch {
	case m != nil:
		run.Output = m[1]
		run.ExitCode, _ = strconv.Atoi(m[2])
		if run.ExitCode == 124 {
			run.TimedOut = true
			run.Output += fmt.Sprintf("\nThe command did not end in %s. The agent stopped it.", limit)
		}
	case err != nil:
		run.Output = "the command did not start: " + err.Error()
	default:
		run.Output = out
	}
	run.Output = lastLines(cleanText(run.Output), keep)
	return run
}

// runCron runs the command of a cron job in the container of the website and gives the result.
func (d *Deps) runCron(ctx context.Context, site *Site, e CronEntry) panel.CronRun {
	at := time.Now().UTC().Format(time.RFC3339)
	r := d.runInContainer(ctx, site, e.Command, orDefault(d.CronLimit, time.Hour), cronOutputMax)
	return panel.CronRun{WebsiteID: site.ID, ID: e.ID, At: at, ExitCode: r.ExitCode, Output: r.Output}
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
	c.init()
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
			if !c.start(key) {
				continue
			}
			go func() {
				defer c.end(key)
				wait, cancel := context.WithTimeout(ctx, cronWait)
				defer cancel()
				// The slot of the website comes first: a website that waits for its own
				// runs holds no slot of the server.
				own := c.siteSlots(site.ID)
				select {
				case own <- struct{}{}:
					defer func() { <-own }()
				case <-wait.Done():
					c.skipped(ctx, site.ID, e.ID, key, now)
					return
				}
				select {
				case c.slots <- struct{}{}:
					defer func() { <-c.slots }()
				case <-wait.Done():
					c.skipped(ctx, site.ID, e.ID, key, now)
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

func (c *cronRuns) init() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running, c.last, c.slots = map[string]bool{}, map[string]*cronResult{}, make(chan struct{}, cronSlots)
		c.site = map[string]chan struct{}{}
	}
}

// start records that a cron job runs. It gives false when the cron job runs already.
func (c *cronRuns) start(key string) bool {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[key] {
		return false
	}
	c.running[key] = true
	return true
}

func (c *cronRuns) end(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.running, key)
}

// siteSlots gives the slots of one website.
func (c *cronRuns) siteSlots(websiteID string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.site[websiteID] == nil {
		c.site[websiteID] = make(chan struct{}, cronSiteSlots)
	}
	return c.site[websiteID]
}

// skipped records a run that did not start because no slot became free.
func (c *cronRuns) skipped(ctx context.Context, websiteID, id, key string, at time.Time) {
	if ctx.Err() != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last[key] = &cronResult{run: panel.CronRun{WebsiteID: websiteID, ID: id, At: at.UTC().Format(time.RFC3339), ExitCode: -1,
		Output: "The run did not start: too many cron jobs ran at this time. Earlier runs of this website or of the server were not complete."}}
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
