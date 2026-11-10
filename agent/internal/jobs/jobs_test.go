package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// Audit finding R1: the panel sends a job again when it did not get the final report. The
// agent that ran the job must report the result again.
func TestTheSameRunnerReportsAFinishedJobAgain(t *testing.T) {
	rec := &recorder{}
	r := &Runner{Dir: t.TempDir(), Reporter: rec}
	runs := 0
	job := Job{ID: id, Key: "w", Steps: []Step{{"one", func(context.Context) error { runs++; return nil }}}}
	r.Submit(job)
	r.Wait()
	rec.mu.Lock()
	before := len(rec.events)
	rec.mu.Unlock()

	if dup, err := r.Submit(job); !dup || err != nil {
		t.Fatalf("Submit: %v %v", dup, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec.mu.Lock()
		n := len(rec.events)
		rec.mu.Unlock()
		if n == before+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the result was not reported again: %d events, %d before", n, before)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := rec.last(); got.State != "succeeded" || got.Step != 1 {
		t.Errorf("event = %+v", got)
	}
	if runs != 1 {
		t.Errorf("runs = %d", runs)
	}
}

// A job that still runs is a duplicate, and the agent sends no result.
func TestAJobThatRunsIsADuplicate(t *testing.T) {
	rec := &recorder{}
	r := &Runner{Dir: t.TempDir(), Reporter: rec}
	release := make(chan struct{})
	job := Job{ID: id, Key: "w", Steps: []Step{{"wait", func(context.Context) error { <-release; return nil }}}}
	r.Submit(job)
	if dup, _ := r.Submit(job); !dup {
		t.Error("the second submit was not a duplicate")
	}
	close(release)
	r.Wait()
	finals := 0
	for _, e := range rec.events {
		if e.State == "succeeded" {
			finals++
		}
	}
	if finals != 1 {
		t.Errorf("final reports = %d", finals)
	}
}

// Audit finding R3: a long job gets the long time limit.
func TestALongJobGetsTheLongTimeLimit(t *testing.T) {
	r := &Runner{}
	if got := r.timeout(Job{}); got != 30*time.Minute {
		t.Errorf("default = %v", got)
	}
	if got := r.timeout(Job{Long: true}); got != 170*time.Minute {
		t.Errorf("default long = %v", got)
	}
	r = &Runner{Timeout: time.Minute, LongTimeout: time.Hour}
	if r.timeout(Job{}) != time.Minute || r.timeout(Job{Long: true}) != time.Hour {
		t.Error("the settings of the runner are not used")
	}

	rec := &recorder{}
	r = &Runner{Dir: t.TempDir(), Reporter: rec, Timeout: 20 * time.Millisecond, LongTimeout: 5 * time.Second}
	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return nil
		}
	}
	r.Submit(Job{ID: id, Key: "a", Steps: []Step{{"copy", slow}}})
	r.Submit(Job{ID: "01JBQ3Z5K8N6T4W2X9Y7R5P3M2", Key: "b", Long: true, Steps: []Step{{"copy", slow}}})
	r.Wait()
	states := map[string]int{}
	for _, e := range rec.events {
		states[e.State]++
	}
	if states["failed"] != 1 || states["succeeded"] != 1 {
		t.Errorf("events = %+v", rec.events)
	}
}

// Audit finding R5: the panel keeps 4,000 characters of an error text.
func TestALongErrorIsCut(t *testing.T) {
	rec := &recorder{}
	r := &Runner{Dir: t.TempDir(), Reporter: rec}
	long := "podman pull: " + strings.Repeat("é", 9000) + " the last line"
	r.Submit(Job{ID: id, Key: "w", Steps: []Step{{"Pull image", func(context.Context) error { return errors.New(long) }}}})
	r.Wait()
	got := rec.last().Error
	if n := len([]rune(got)); n > maxErrorLength {
		t.Errorf("length = %d", n)
	}
	if !strings.HasPrefix(got, "Pull image: podman pull: ") || !strings.HasSuffix(got, " the last line") || !strings.Contains(got, "[...]") {
		t.Errorf("error = %.80q ... %q", got, got[len(got)-30:])
	}
}

// Audit finding R10: a step that stops because the agent stops gives no result. The job
// continues after the restart.
func TestAnInterruptedJobSendsNoResult(t *testing.T) {
	rec := &recorder{}
	r := &Runner{Dir: t.TempDir(), Reporter: rec}
	after := false
	job := Job{ID: id, Key: "host", Steps: []Step{
		{"Reboot", func(context.Context) error { return fmt.Errorf("wait for the shutdown: %w", ErrInterrupted) }},
		{"Start containers", func(context.Context) error { after = true; return nil }},
	}, OnFailure: func(context.Context) { t.Error("OnFailure ran") }}
	r.Submit(job)
	r.Wait()
	if after {
		t.Error("the next step ran")
	}
	for _, e := range rec.events {
		if e.State != "running" {
			t.Errorf("event = %+v", e)
		}
	}
	if saved, err := r.load(id); err != nil || saved.State != "running" || saved.Step != 0 {
		t.Errorf("record = %+v %v", saved, err)
	}

	// After the restart, the rest of the job runs and reports the result.
	r2 := &Runner{Dir: r.Dir, Reporter: rec}
	job.StartAt = 1
	r2.Resume(job)
	r2.Wait()
	if got := rec.last(); !after || got.State != "succeeded" {
		t.Errorf("after = %v, last = %+v", after, got)
	}
}
