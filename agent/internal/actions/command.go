package actions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Run a command: a user of the panel runs one command line now in the container of a website,
// for example "php artisan migrate" or "wp plugin list". The command runs as the Unix user of
// the website, in the gVisor sandbox, with the limits of the website, the same as a cron job.
// The panel records each command in the command log of the website.

const (
	// commandLimit is the time limit of a command when the request gives none.
	commandLimit = 10 * time.Minute
	// commandLimitMax is the longest time limit that a request can give.
	commandLimitMax = time.Hour
	// commandOutputMax is the size of the end of the output that the agent keeps and sends.
	commandOutputMax = 64 << 10
	// commandSiteSlots is the number of commands of one website that run at the same time.
	commandSiteSlots = 2
)

// commandRuns limits the commands that run at the same time for each website.
type commandRuns struct {
	mu      sync.Mutex
	running map[string]int
}

// start records one more command of a website. It gives false when the website has
// commandSiteSlots commands that run now.
func (c *commandRuns) start(websiteID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running = map[string]int{}
	}
	if c.running[websiteID] >= commandSiteSlots {
		return false
	}
	c.running[websiteID]++
	return true
}

func (c *commandRuns) end(websiteID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[websiteID]--; c.running[websiteID] <= 0 {
		delete(c.running, websiteID)
	}
}

// RunCommand runs a command line now in the container of a website. The job succeeds when the
// command ran, also when its exit code is not 0: the result has the exit code. The job fails
// when the command did not start, for example when the website is stopped.
func (d *Deps) RunCommand(req *agentv1.RunCommandRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(req.GetWebsiteId()), validate.CommandLine(req.GetCommand())); err != nil {
		return jobs.Job{}, err
	}
	limit := commandLimit
	if s := req.GetTimeoutSeconds(); s > 0 {
		limit = time.Duration(s) * time.Second
	}
	if limit > commandLimitMax {
		return jobs.Job{}, fmt.Errorf("the time limit of a command is %d seconds at most", int(commandLimitMax.Seconds()))
	}
	var run containerRun
	var took time.Duration
	// The key is the job: a long command does not stop the other jobs of the website.
	return jobs.Job{ID: req.GetJobId(), Key: "command:" + req.GetJobId(), Long: true, Steps: []jobs.Step{
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
			if !d.commands.start(site.ID) {
				return fmt.Errorf("%d commands of this website run now; try again when one ends", commandSiteSlots)
			}
			defer d.commands.end(site.ID)
			start := time.Now()
			run = d.runInContainer(ctx, site, req.GetCommand(), limit, commandOutputMax)
			took = time.Since(start)
			if run.ExitCode < 0 {
				return errors.New(run.Output)
			}
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"exitCode": run.ExitCode, "output": run.Output, "timedOut": run.TimedOut, "durationMs": took.Milliseconds()}
	}}, nil
}
