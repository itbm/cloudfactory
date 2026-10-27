package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/panel"
)

type recorder struct {
	mu     sync.Mutex
	events []panel.Event
}

func (r *recorder) JobEvent(_ context.Context, _ string, e panel.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) last() panel.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

const id = "01JBQ3Z5K8N6T4W2X9Y7R5P3M1"

func TestJobReportsEachStepAndRunsOnce(t *testing.T) {
	rec := &recorder{}
	r := &Runner{Dir: t.TempDir(), Reporter: rec}
	runs := 0
	job := Job{ID: id, Key: "w", Steps: []Step{
		{"one", func(context.Context) error { runs++; return nil }},
		{"two", func(context.Context) error { return nil }},
	}, Result: func() map[string]any { return map[string]any{"snapshotId": "abc"} }}

	if dup, err := r.Submit(job); dup || err != nil {
		t.Fatalf("Submit: %v %v", dup, err)
	}
	r.Wait()
	if got := rec.last(); got.State != "succeeded" || got.Step != 2 || got.Result["snapshotId"] != "abc" {
		t.Errorf("last event = %+v", got)
	}
	if len(rec.events) != 3 {
		t.Errorf("events = %+v", rec.events)
	}

	// A new runner (after a restart) reads the record and does not run the job again.
	r2 := &Runner{Dir: r.Dir, Reporter: rec}
	if dup, _ := r2.Submit(job); !dup {
		t.Error("the second submit was not a duplicate")
	}
	time.Sleep(20 * time.Millisecond)
	if runs != 1 {
		t.Errorf("runs = %d", runs)
	}
	if got := rec.last(); got.State != "succeeded" {
		t.Errorf("the result was not reported again: %+v", got)
	}
}

func TestAFailedStepStopsTheJob(t *testing.T) {
	rec := &recorder{}
	r := &Runner{Dir: t.TempDir(), Reporter: rec}
	undone := false
	third := false
	r.Submit(Job{ID: id, Key: "w", Steps: []Step{
		{"Pull image", func(context.Context) error { return nil }},
		{"Health check", func(context.Context) error { return errors.New("status 502") }},
		{"Switch traffic", func(context.Context) error { third = true; return nil }},
	}, OnFailure: func(context.Context) { undone = true }})
	r.Wait()

	got := rec.last()
	if got.State != "failed" || got.Step != 1 || got.Error != "Health check: status 502" {
		t.Errorf("event = %+v", got)
	}
	if third || !undone {
		t.Errorf("third=%v undone=%v", third, undone)
	}
}

func TestBadJobIDIsRefused(t *testing.T) {
	r := &Runner{Dir: t.TempDir(), Reporter: &recorder{}}
	if _, err := r.Submit(Job{ID: "../../etc/passwd"}); err == nil {
		t.Error("bad ID accepted")
	}
}

// Batch 1, finding 1: a job that stopped when the agent stopped runs again when the panel sends
// it again.
func TestAnInterruptedJobRunsAgainAfterARestart(t *testing.T) {
	rec := &recorder{}
	dir := t.TempDir()
	r := &Runner{Dir: dir, Reporter: rec}
	// The record of the first agent: the job was at step 1 when the agent stopped.
	if err := r.save(Record{ID: id, State: "running", Step: 1}); err != nil {
		t.Fatal(err)
	}
	runs := 0
	job := Job{ID: id, Key: "w", Steps: []Step{{"one", func(context.Context) error { runs++; return nil }}}}
	r2 := &Runner{Dir: dir, Reporter: rec}
	if dup, err := r2.Submit(job); dup || err != nil {
		t.Fatalf("Submit: %v %v", dup, err)
	}
	r2.Wait()
	if runs != 1 || rec.last().State != "succeeded" {
		t.Errorf("runs = %d, last event = %+v", runs, rec.last())
	}
}
