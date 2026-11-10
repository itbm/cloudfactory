// Package jobs runs the jobs from the panel. A job is a list of steps. The runner reports
// each step to the panel, and keeps the result on disk, so that a job that the panel sends
// two times runs only once. When the agent stops during a job, the panel sends the job again
// (it checks the jobs that take too long), and the job runs again.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Step is one idempotent part of a job.
type Step struct {
	Name string
	Do   func(ctx context.Context) error
}

// Job is the work for one request from the panel.
type Job struct {
	ID string
	// Key serialises jobs: jobs with the same key (for example a website ID) run one at a time.
	Key   string
	Steps []Step
	// Result gives the result fields after the last step, for example a snapshot ID.
	Result func() map[string]any
	// OnFailure runs when a step fails, for example to go back to the previous container.
	OnFailure func(ctx context.Context)
	// After runs when the panel has the result, for example to restart the agent.
	After func()
	// StartAt is the first step to run. Resume uses it after a reboot.
	StartAt int
	// Long is true for a job that can take hours, for example a backup, a restore or a copy
	// of a website. It gets the long time limit of the runner.
	Long bool
}

// ErrInterrupted is the error of a step that stopped because the agent stops, for example
// the reboot step when the server shuts down. The runner then ends the job with no result:
// the record stays "running", and the job continues or runs again after the restart.
var ErrInterrupted = errors.New("the agent stops")

// maxErrorLength is the maximum length of the error text that the agent sends to the panel.
// Audit finding R5: the panel keeps 4,000 characters.
const maxErrorLength = 3500

// Reporter sends job events to the panel.
type Reporter interface {
	JobEvent(ctx context.Context, jobID string, e panel.Event) error
}

// Record is the state of a job on disk.
type Record struct {
	ID       string         `json:"id"`
	State    string         `json:"state"`
	Step     int            `json:"step"`
	Error    string         `json:"error,omitempty"`
	Result   map[string]any `json:"result,omitempty"`
	Finished time.Time      `json:"finished,omitzero"`
}

// Runner runs jobs in the background.
type Runner struct {
	Dir      string // for example "/var/lib/cloudfactory/jobs"
	Reporter Reporter
	Log      *slog.Logger
	// Timeout is the maximum time for one job. The default is 30 minutes.
	Timeout time.Duration
	// LongTimeout is the maximum time for a job with Long set. The default is 170 minutes,
	// because the panel fails a job that has no result after 3 hours (audit finding R3).
	LongTimeout time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	known map[string]bool
	wg    sync.WaitGroup
}

// Submit starts a job. It returns true when the agent already has this job. Then the
// agent reports the result again and does not run the job again.
func (r *Runner) Submit(job Job) (duplicate bool, err error) {
	if err := validate.ID(job.ID); err != nil {
		return false, err
	}
	r.mu.Lock()
	if r.known == nil {
		r.known = map[string]bool{}
		r.locks = map[string]*sync.Mutex{}
	}
	// A record of a finished job: report the result again. Audit finding R1: this check comes
	// first, also for a job that this agent ran. The panel sends a job again when the final
	// report did not arrive, and it needs the result then.
	if rec, err := r.load(job.ID); err == nil && (rec.State == "succeeded" || rec.State == "failed") {
		r.mu.Unlock()
		go r.Reporter.JobEvent(context.Background(), job.ID, panel.Event{State: rec.State, Step: rec.Step, Error: rec.Error, Result: rec.Result})
		return true, nil
	}
	// The job runs now.
	if r.known[job.ID] {
		r.mu.Unlock()
		return true, nil
	}
	// A record that is not finished and that this agent does not run is from a job that
	// stopped when the agent stopped. The job runs again from its first step. Each step is
	// idempotent.
	r.known[job.ID] = true
	lock := r.locks[job.Key]
	if lock == nil {
		lock = &sync.Mutex{}
		r.locks[job.Key] = lock
	}
	r.mu.Unlock()

	if err := r.save(Record{ID: job.ID, State: "queued"}); err != nil {
		return false, err
	}
	r.start(job, lock)
	return false, nil
}

// Resume continues a job that stopped, for example at a reboot. It does not check the record.
func (r *Runner) Resume(job Job) {
	r.mu.Lock()
	if r.known == nil {
		r.known = map[string]bool{}
		r.locks = map[string]*sync.Mutex{}
	}
	r.known[job.ID] = true
	r.mu.Unlock()
	r.start(job, &sync.Mutex{})
}

func (r *Runner) start(job Job, lock *sync.Mutex) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		lock.Lock()
		defer lock.Unlock()
		r.run(job)
	}()
}

// Wait waits for all jobs. Tests and a clean stop use it.
func (r *Runner) Wait() { r.wg.Wait() }

// saveOrLog saves the record of a job. A record that the agent cannot save does not stop the
// job: the panel still gets the events. After a restart, the job can run again (audit batch 2,
// finding 14).
func (r *Runner) saveOrLog(log *slog.Logger, rec Record) {
	if err := r.save(rec); err != nil {
		log.Error("save job record", "state", rec.State, "err", err)
	}
}

func (r *Runner) run(job Job) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout(job))
	defer cancel()
	log := r.logger().With("job", job.ID)

	rec := Record{ID: job.ID, State: "running"}
	for i, step := range job.Steps {
		if i < job.StartAt {
			continue
		}
		rec.Step = i
		r.saveOrLog(log, rec)
		r.report(ctx, job.ID, panel.Event{State: "running", Step: i})
		log.Info("step", "n", i+1, "name", step.Name)
		if err := step.Do(ctx); err != nil {
			if errors.Is(err, ErrInterrupted) {
				log.Info("the job stops with the agent", "step", step.Name)
				r.mu.Lock()
				delete(r.known, job.ID)
				r.mu.Unlock()
				return
			}
			log.Error("step failed", "name", step.Name, "err", err)
			if job.OnFailure != nil {
				job.OnFailure(context.WithoutCancel(ctx))
			}
			rec.State, rec.Error, rec.Finished = "failed", cutError(fmt.Sprintf("%s: %v", step.Name, err)), time.Now()
			r.saveOrLog(log, rec)
			r.report(context.WithoutCancel(ctx), job.ID, panel.Event{State: "failed", Step: i, Error: rec.Error})
			return
		}
	}
	rec.State, rec.Step, rec.Finished = "succeeded", len(job.Steps), time.Now()
	if job.Result != nil {
		rec.Result = job.Result()
	}
	r.saveOrLog(log, rec)
	r.report(ctx, job.ID, panel.Event{State: "succeeded", Step: rec.Step, Result: rec.Result})
	if job.After != nil {
		job.After()
	}
}

// timeout gives the time limit of a job.
func (r *Runner) timeout(job Job) time.Duration {
	if job.Long {
		if r.LongTimeout > 0 {
			return r.LongTimeout
		}
		return 170 * time.Minute
	}
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 30 * time.Minute
}

// cutError keeps the start and the end of a long error text. The start has the step and the
// command, and the end has the last lines of the output.
func cutError(s string) string {
	runes := []rune(s)
	if len(runes) <= maxErrorLength {
		return s
	}
	const head = 1000
	return string(runes[:head]) + "\n[...]\n" + string(runes[len(runes)-(maxErrorLength-head-7):])
}

func (r *Runner) report(ctx context.Context, id string, e panel.Event) {
	if err := r.Reporter.JobEvent(ctx, id, e); err != nil {
		r.logger().Warn("report to panel failed", "job", id, "err", err)
	}
}

func (r *Runner) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Runner) path(id string) string { return filepath.Join(r.Dir, id+".json") }

func (r *Runner) load(id string) (Record, error) {
	var rec Record
	b, err := os.ReadFile(r.path(id))
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(b, &rec)
}

func (r *Runner) save(rec Record) error {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(rec)
	tmp := r.path(rec.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path(rec.ID))
}

// Prune removes the records of jobs that finished before the given time.
func (r *Runner) Prune(before time.Time) error {
	entries, err := os.ReadDir(r.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		id := e.Name()[:len(e.Name())-len(filepath.Ext(e.Name()))]
		if rec, err := r.load(id); err == nil && !rec.Finished.IsZero() && rec.Finished.Before(before) {
			os.Remove(r.path(id))
		}
	}
	return nil
}
