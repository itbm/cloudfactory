// Package jobs runs the jobs from the panel. A job is a list of steps. The runner reports
// each step to the panel, and keeps the result on disk, so that a job that the panel sends
// two times runs only once.
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
}

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
	// Timeout is the maximum time for one job.
	Timeout time.Duration

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
	if r.known[job.ID] {
		r.mu.Unlock()
		return true, nil
	}
	if rec, err := r.load(job.ID); err == nil {
		r.mu.Unlock()
		if rec.State == "succeeded" || rec.State == "failed" {
			go r.Reporter.JobEvent(context.Background(), job.ID, panel.Event{State: rec.State, Step: rec.Step, Error: rec.Error, Result: rec.Result})
		}
		return true, nil
	}
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

func (r *Runner) run(job Job) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	log := r.logger().With("job", job.ID)

	rec := Record{ID: job.ID, State: "running"}
	for i, step := range job.Steps {
		if i < job.StartAt {
			continue
		}
		rec.Step = i
		r.save(rec)
		r.report(ctx, job.ID, panel.Event{State: "running", Step: i})
		log.Info("step", "n", i+1, "name", step.Name)
		if err := step.Do(ctx); err != nil {
			log.Error("step failed", "name", step.Name, "err", err)
			if job.OnFailure != nil {
				job.OnFailure(context.WithoutCancel(ctx))
			}
			rec.State, rec.Error, rec.Finished = "failed", fmt.Sprintf("%s: %v", step.Name, err), time.Now()
			r.save(rec)
			r.report(context.WithoutCancel(ctx), job.ID, panel.Event{State: "failed", Step: i, Error: rec.Error})
			return
		}
	}
	rec.State, rec.Step, rec.Finished = "succeeded", len(job.Steps), time.Now()
	if job.Result != nil {
		rec.Result = job.Result()
	}
	r.save(rec)
	r.report(ctx, job.ID, panel.Event{State: "succeeded", Step: rec.Step, Result: rec.Result})
	if job.After != nil {
		job.After()
	}
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
