package actions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

const commandStart = asSite + "env XDG_RUNTIME_DIR=/run/user/20001 sh -c "

func TestRunCommandRunsInTheContainerOfTheWebsite(t *testing.T) {
	d, f, _ := withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: commandStart, Output: "Migrated: 2026_11_22_create_orders\ncf-exit:0\n"})
	job, err := d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "php artisan migrate --force", TimeoutSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	if job.Key != "command:"+jobID2 || !job.Long {
		t.Errorf("key = %q, long = %v", job.Key, job.Long)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	if r := job.Result(); r["exitCode"] != 0 || r["output"] != "Migrated: 2026_11_22_create_orders" || r["timedOut"] != false {
		t.Errorf("result = %v", r)
	}
	// The command runs as the website user, with podman exec in the container of the website,
	// with the time limit of the request.
	call, _ := called(f, commandStart)
	if !strings.HasSuffix(call, " sh 120 cf-acme_x7k2 262144 php artisan migrate --force") || !strings.Contains(call, `podman exec --workdir /app "$2" sh -c '`) {
		t.Errorf("call = %q", call)
	}
}

// A command with an exit code that is not 0 ran: the job succeeds, and the result has the code.
func TestRunCommandGivesTheExitCodeOfAFailedCommand(t *testing.T) {
	d, f, _ := withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: commandStart, Output: "grep: config.php: No such file or directory\ncf-exit:2\n"})
	job, err := d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "grep DB_HOST config.php"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	if r := job.Result(); r["exitCode"] != 2 || r["output"] != "grep: config.php: No such file or directory" {
		t.Errorf("result = %v", r)
	}
	// The default time limit is 10 minutes.
	if call, _ := called(f, commandStart); !strings.Contains(call, " sh 600 cf-acme_x7k2 ") {
		t.Errorf("call = %q", call)
	}

	// A command that the agent stopped at its time limit.
	f.Rules = []execx.Rule{{Prefix: commandStart, Output: "started\ncf-exit:124\n"}}
	job, _ = d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "sleep 99", TimeoutSeconds: 5})
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	if r := job.Result(); r["exitCode"] != 124 || r["timedOut"] != true || !strings.Contains(r["output"].(string), "did not end in 5s") {
		t.Errorf("result = %v", r)
	}
}

// The job fails when the command did not start.
func TestRunCommandFailsWhenTheCommandDoesNotStart(t *testing.T) {
	d, f, _ := withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: commandStart, Err: errors.New("runuser: user does not exist")})
	job, _ := d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "ls"})
	if err := runSteps(t, job, nil); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Errorf("err = %v", err)
	}
	if r := job.Result(); r["exitCode"] != -1 {
		t.Errorf("result = %v", r)
	}

	// A stopped website runs no command.
	f.Calls = nil
	site, _ := d.State.Get(websiteID)
	site.Stopped = true
	if err := d.State.Put(site); err != nil {
		t.Fatal(err)
	}
	job, _ = d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "ls"})
	if err := runSteps(t, job, nil); err == nil || !strings.Contains(err.Error(), "stopped") || f.Called(commandStart) {
		t.Errorf("err = %v, calls = %v", err, f.Calls)
	}
	// An unknown website runs no command.
	job, _ = d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: jobID, Command: "ls"})
	if err := runSteps(t, job, nil); !errors.Is(err, ErrUnknownWebsite) {
		t.Errorf("unknown website: err = %v", err)
	}
}

func TestRunCommandRefusesBadInput(t *testing.T) {
	d, _, _ := withSite(t)
	for name, req := range map[string]*agentv1.RunCommandRequest{
		"two lines":  {JobId: jobID2, WebsiteId: websiteID, Command: "true\nrm -rf /"},
		"no command": {JobId: jobID2, WebsiteId: websiteID},
		"too long":   {JobId: jobID2, WebsiteId: websiteID, Command: strings.Repeat("a", 1001)},
		"website":    {JobId: jobID2, WebsiteId: "../x", Command: "ls"},
		"job":        {JobId: "x", WebsiteId: websiteID, Command: "ls"},
		"time limit": {JobId: jobID2, WebsiteId: websiteID, Command: "ls", TimeoutSeconds: 3601},
	} {
		if _, err := d.RunCommand(req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A website runs two commands at the same time. A third command fails at once.
func TestRunCommandLimitsTheCommandsOfOneWebsite(t *testing.T) {
	d, f, _ := withSite(t)
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)
	f.Rules = append(f.Rules, execx.Rule{Prefix: commandStart, Output: "cf-exit:0\n"})
	// The fake runner holds a lock while it runs a command, so the wait is before the fake.
	d.Run = slowRunner{Runner: d.Run, prefix: "runuser", before: func() {
		started.Done()
		<-release
	}}
	errs := make(chan error, 2)
	for range 2 {
		job, _ := d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "sleep 60"})
		go func() { errs <- job.Steps[0].Do(context.Background()) }()
	}
	started.Wait()
	job, _ := d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "ls"})
	if err := runSteps(t, job, nil); err == nil || !strings.Contains(err.Error(), "2 commands of this website run now") {
		t.Errorf("third command: err = %v", err)
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	// The slots are free again.
	d.Run = d.Gate
	job, _ = d.RunCommand(&agentv1.RunCommandRequest{JobId: jobID2, WebsiteId: websiteID, Command: "ls"})
	if err := runSteps(t, job, nil); err != nil {
		t.Errorf("after the two commands: %v", err)
	}
}
