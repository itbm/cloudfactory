package actions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/sitefiles"
)

// The tests of the file manager, the logs, the cron jobs and the deploys. Each command goes
// through the gate, so the tests also show that the policy of the root helper accepts the
// commands: all of them run as the Unix user of the website.

const (
	siteImage = "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"
	releaseID = "01JBQ3Z5K8N6T4W2X9Y7R5P3R1"
	oldID     = "01JBQ3Z5K8N6T4W2X9Y7R5P3R0"
	cronID    = "01JBQ3Z5K8N6T4W2X9Y7R5P3K1"
	asSite    = "runuser -u acme_x7k2 -- "
)

func withSite(t *testing.T) (*Deps, *execx.Fake, *fakeEdge) {
	t.Helper()
	d, f, edge := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website(siteImage)})); err != nil {
		t.Fatal(err)
	}
	f.Calls, f.Inputs = nil, nil
	return d, f, edge
}

// called gives the first command that starts with prefix, and its standard input.
func called(f *execx.Fake, prefix string) (string, string) {
	for i, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return c, f.Inputs[i]
		}
	}
	return "", ""
}

func TestFilesRunAsTheWebsiteUser(t *testing.T) {
	d, f, _ := withSite(t)
	answer, _ := json.Marshal(sitefiles.Response{Entries: []sitefiles.Entry{{Name: "public", Type: "directory"}}})
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "/usr/local/bin/cf-agent site-files ", Output: string(answer)})

	resp, err := d.Files(context.Background(), websiteID, sitefiles.Request{Op: sitefiles.OpList, Path: "/"})
	if err != nil || len(resp.Entries) != 1 || resp.Entries[0].Name != "public" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	call, input := called(f, asSite)
	if call != asSite+"/usr/local/bin/cf-agent site-files "+d.Host.AppDir("acme_x7k2") || !strings.Contains(input, `"op":"list"`) {
		t.Errorf("call = %q, input = %q", call, input)
	}
}

func TestFilesRefuseBadRequests(t *testing.T) {
	d, f, _ := withSite(t)
	ctx := context.Background()
	var fe *FileError
	if _, err := d.Files(ctx, websiteID, sitefiles.Request{Op: sitefiles.OpRead, Path: "/../other"}); !errors.As(err, &fe) || fe.Code != sitefiles.CodeInvalid {
		t.Errorf("path with .. = %v", err)
	}
	if _, err := d.Files(ctx, websiteID, sitefiles.Request{Op: sitefiles.OpMove, Path: "/a", To: "b"}); !errors.As(err, &fe) || fe.Code != sitefiles.CodeInvalid {
		t.Errorf("move to a bad path = %v", err)
	}
	if _, err := d.Files(ctx, websiteID, sitefiles.Request{Op: sitefiles.OpWrite, Path: "/a", Content: make([]byte, sitefiles.MaxWrite+1)}); !errors.As(err, &fe) || fe.Code != sitefiles.CodeTooLarge {
		t.Errorf("large file = %v", err)
	}
	if _, err := d.Files(ctx, jobID2, sitefiles.Request{Op: sitefiles.OpList, Path: "/"}); !errors.Is(err, ErrUnknownWebsite) {
		t.Errorf("unknown website = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("the agent ran commands: %v", f.Calls)
	}
	// An error of the file manager keeps its code.
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite, Output: `{"code":"not_found","error":"the file or the directory does not exist"}`})
	if _, err := d.Files(ctx, websiteID, sitefiles.Request{Op: sitefiles.OpRead, Path: "/none"}); !errors.As(err, &fe) || fe.Code != sitefiles.CodeNotFound {
		t.Errorf("file manager error = %v", err)
	}
}

// An archive is a job. It runs the file manager as the website user with a low CPU
// priority, and gives the summary as the result.
func TestArchiveJobsRunAsTheWebsiteUser(t *testing.T) {
	d, f, _ := withSite(t)
	answer, _ := json.Marshal(sitefiles.Response{Summary: &sitefiles.Summary{Files: 12, Directories: 3, Kept: 2, SkippedNames: []string{"/out/../x"}, Skipped: 1}})
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "nice -n 10 /usr/local/bin/cf-agent site-files ", Output: string(answer)})

	job, err := d.ExtractArchive(&agentv1.ExtractArchiveRequest{JobId: jobID2, WebsiteId: websiteID, Path: "/site.zip", To: "/out", Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.Key != "files:"+websiteID || !job.Long || len(job.Steps) != 1 || job.Steps[0].Name != "Extract the archive" {
		t.Errorf("job = %+v", job)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	call, input := called(f, asSite)
	if call != asSite+"nice -n 10 /usr/local/bin/cf-agent site-files "+d.Host.AppDir("acme_x7k2") || !strings.Contains(input, `"op":"extract","path":"/site.zip","to":"/out"`) || !strings.Contains(input, `"overwrite":true`) {
		t.Errorf("call = %q, input = %q", call, input)
	}
	if r := job.Result(); r["files"] != 12 || r["kept"] != 2 || r["skipped"] != 1 || len(r["skippedNames"].([]string)) != 1 {
		t.Errorf("result = %v", r)
	}

	f.Calls, f.Inputs = nil, nil
	job, err = d.MakeArchive(&agentv1.MakeArchiveRequest{JobId: jobID2, WebsiteId: websiteID, Path: "/site.tar.gz", Paths: []string{"/public", "/composer.json"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil || job.Steps[0].Name != "Make the archive" {
		t.Fatal(err)
	}
	if _, input := called(f, asSite); !strings.Contains(input, `"op":"archive","path":"/site.tar.gz"`) || !strings.Contains(input, `"paths":["/public","/composer.json"]`) {
		t.Errorf("input = %q", input)
	}

	// An error of the file manager fails the job with its text.
	f.Rules = append([]execx.Rule{{Prefix: asSite, Output: `{"code":"exists","error":"a file or a directory with this name exists already"}`}}, f.Rules...)
	if err := runSteps(t, job, nil); err == nil || err.Error() != "a file or a directory with this name exists already" {
		t.Errorf("error = %v", err)
	}
}

func TestArchiveJobsRefuseBadRequests(t *testing.T) {
	d, f, _ := withSite(t)
	for _, c := range []struct {
		name string
		job  func() (any, error)
	}{
		{"path with ..", func() (any, error) {
			return d.ExtractArchive(&agentv1.ExtractArchiveRequest{JobId: jobID2, WebsiteId: websiteID, Path: "/../other/site.zip", To: "/"})
		}},
		{"bad destination", func() (any, error) {
			return d.ExtractArchive(&agentv1.ExtractArchiveRequest{JobId: jobID2, WebsiteId: websiteID, Path: "/site.zip", To: "out"})
		}},
		{"no paths", func() (any, error) {
			return d.MakeArchive(&agentv1.MakeArchiveRequest{JobId: jobID2, WebsiteId: websiteID, Path: "/site.zip"})
		}},
		{"bad path", func() (any, error) {
			return d.MakeArchive(&agentv1.MakeArchiveRequest{JobId: jobID2, WebsiteId: websiteID, Path: "/site.zip", Paths: []string{"/a/"}})
		}},
		{"bad website", func() (any, error) {
			return d.MakeArchive(&agentv1.MakeArchiveRequest{JobId: jobID2, WebsiteId: "../x", Path: "/site.zip", Paths: []string{"/a"}})
		}},
	} {
		if _, err := c.job(); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
	// A website that the agent does not know fails in the step.
	job, err := d.MakeArchive(&agentv1.MakeArchiveRequest{JobId: jobID2, WebsiteId: jobID2, Path: "/site.zip", Paths: []string{"/a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); !errors.Is(err, ErrUnknownWebsite) {
		t.Errorf("unknown website = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("the agent ran commands: %v", f.Calls)
	}
}

func TestLogsReadTheLogFileOfTheWebsite(t *testing.T) {
	d, f, _ := withSite(t)
	log := d.Host.Home("acme_x7k2") + "/website.log"
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "tail -n 50 " + log, Output: strings.Join([]string{
		`2026-11-14T10:00:00.123456789Z stdout F {"status":200,"uri":"/"}`,
		`2026-11-14T10:00:01.5+00:00 stderr F PHP Warning: something`,
		`not a log line`,
		`2026-11-14T10:00:02Z stdout P`,
		``,
	}, "\n")})
	lines, err := d.Logs(context.Background(), websiteID, 50)
	if err != nil || len(lines) != 3 {
		t.Fatalf("lines = %v, %v", lines, err)
	}
	if lines[0].Stream != "access" || lines[0].Text != `{"status":200,"uri":"/"}` || lines[0].At != "2026-11-14T10:00:00.123456789Z" {
		t.Errorf("line 0 = %+v", lines[0])
	}
	if lines[1].Stream != "error" || lines[1].Text != "PHP Warning: something" || lines[2].Text != "" {
		t.Errorf("lines = %+v %+v", lines[1], lines[2])
	}
	// The unit of the website sends the output of the container to the log file.
	unit := quadletUnit(t, d)
	if !strings.Contains(unit, "LogDriver=k8s-file\n") || !strings.Contains(unit, "--log-opt=path="+log+" --log-opt=max-size=10mb") {
		t.Errorf("unit = %s", unit)
	}
	// No log file gives no lines and no error.
	f.Rules = []execx.Rule{{Prefix: asSite + "tail", Err: errors.New("exit status 1")}}
	if lines, err := d.Logs(context.Background(), websiteID, 0); err != nil || len(lines) != 0 {
		t.Errorf("no log file = %v, %v", lines, err)
	}
}

func quadletUnit(t *testing.T, d *Deps) string {
	t.Helper()
	dir, err := d.Host.UnitDir("acme_x7k2")
	if err != nil {
		t.Fatal(err)
	}
	return readFile(t, dir+"/cf-acme_x7k2.container")
}

func cronJob(schedule string) *agentv1.CronJob {
	return &agentv1.CronJob{Id: cronID, Schedule: schedule, Command: "php artisan schedule:run", Enabled: true}
}

func TestCronRunsInTheContainerOfTheWebsite(t *testing.T) {
	d, f, _ := withSite(t)
	if err := run(t)(d.SetCronJobs(&agentv1.SetCronJobsRequest{JobId: jobID2, WebsiteId: websiteID, CronJobs: []*agentv1.CronJob{cronJob("*/5 * * * *")}})); err != nil {
		t.Fatal(err)
	}
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "env XDG_RUNTIME_DIR=/run/user/20001 sh -c ", Output: "done\ncf-exit:0\n"})
	ctx := context.Background()
	d.CronTick(ctx, time.Date(2026, 11, 14, 10, 7, 0, 0, time.UTC))
	if runs := waitCron(d, 0); len(runs) != 0 {
		t.Fatalf("the cron job ran at a minute out of its schedule: %+v", runs)
	}
	d.CronTick(ctx, time.Date(2026, 11, 14, 10, 5, 0, 0, time.UTC))
	runs := waitCron(d, 1)
	if len(runs) != 1 || runs[0].WebsiteID != websiteID || runs[0].ID != cronID || runs[0].ExitCode != 0 || runs[0].Output != "done" {
		t.Fatalf("runs = %+v", runs)
	}
	call, _ := called(f, asSite+"env")
	if !strings.HasSuffix(call, " sh 3600 cf-acme_x7k2 php artisan schedule:run") || !strings.Contains(call, `podman exec --workdir /app "$2" sh -c '`) || !strings.Contains(call, `exec timeout -k 10 "$0" sh -c "$1"`) {
		t.Errorf("call = %q", call)
	}
	// The result is in three reports, and then no more.
	if len(d.cronReport()) != 1 || len(d.cronReport()) != 1 || len(d.cronReport()) != 1 || len(d.cronReport()) != 0 {
		t.Error("the result was not in three reports")
	}

	// A stopped website and a cron job that is off have no runs.
	f.Calls = nil
	site, _ := d.State.Get(websiteID)
	site.Stopped = true
	if err := d.State.Put(site); err != nil {
		t.Fatal(err)
	}
	d.CronTick(ctx, time.Date(2026, 11, 14, 10, 10, 0, 0, time.UTC))
	site.Stopped = false
	if err := d.State.Put(site); err != nil {
		t.Fatal(err)
	}
	off := cronJob("* * * * *")
	off.Enabled = false
	if err := run(t)(d.SetCronJobs(&agentv1.SetCronJobsRequest{JobId: jobID2, WebsiteId: websiteID, CronJobs: []*agentv1.CronJob{off}})); err != nil {
		t.Fatal(err)
	}
	d.CronTick(ctx, time.Date(2026, 11, 14, 10, 10, 0, 0, time.UTC))
	if runs := waitCron(d, 0); len(runs) != 0 || f.Called(asSite+"env") {
		t.Errorf("runs = %+v, calls = %v", runs, f.Calls)
	}

	// The removal of the website removes its cron jobs.
	if err := d.State.Delete(websiteID); err != nil {
		t.Fatal(err)
	}
	if sites, err := d.State.cronSites(); err != nil || len(sites) != 0 {
		t.Errorf("cron jobs after the removal = %v, %v", sites, err)
	}
}

// One website runs two scheduled cron jobs at the same time. The other jobs of the website
// wait, and do not hold a slot of the server (adversarial review, findings A2-02 and G5-04).
func TestCronLimitsTheRunsOfOneWebsite(t *testing.T) {
	d, f, _ := withSite(t)
	var jobsOfSite []*agentv1.CronJob
	for i := range 5 {
		jobsOfSite = append(jobsOfSite, &agentv1.CronJob{Id: cronID[:len(cronID)-1] + strconv.Itoa(i), Schedule: "* * * * *", Command: "sleep 1", Enabled: true})
	}
	if err := run(t)(d.SetCronJobs(&agentv1.SetCronJobsRequest{JobId: jobID2, WebsiteId: websiteID, CronJobs: jobsOfSite})); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now, most := 0, 0
	release := make(chan struct{})
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "env XDG_RUNTIME_DIR=/run/user/20001 sh -c ", Output: "cf-exit:0\n", Do: func() error { return nil }})
	d.Run = slowRunner{Runner: d.Run, prefix: "runuser", before: func() {
		mu.Lock()
		now++
		most = max(most, now)
		mu.Unlock()
		<-release
		mu.Lock()
		now--
		mu.Unlock()
	}}
	d.CronTick(context.Background(), time.Date(2026, 11, 14, 10, 5, 0, 0, time.UTC))
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	got := now
	mu.Unlock()
	close(release)
	if got != cronSiteSlots {
		t.Fatalf("%d cron jobs of one website ran at the same time; want %d", got, cronSiteSlots)
	}
	if len(d.cron.slots) > cronSiteSlots {
		t.Errorf("the website held %d slots of the server", len(d.cron.slots))
	}
}

// slowRunner calls before for each command that starts with prefix.
type slowRunner struct {
	execx.Runner
	prefix string
	before func()
}

func (s slowRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == s.prefix {
		s.before()
	}
	return s.Runner.Run(ctx, name, args...)
}

// waitCron waits a short time for the results of n runs and gives them.
func waitCron(d *Deps, n int) (runs []struct {
	WebsiteID, ID, Output string
	ExitCode              int
}) {
	deadline := time.Now().Add(2 * time.Second)
	if n == 0 {
		deadline = time.Now().Add(50 * time.Millisecond)
	}
	for {
		d.cron.mu.Lock()
		runs = nil
		for _, r := range d.cron.last {
			runs = append(runs, struct {
				WebsiteID, ID, Output string
				ExitCode              int
			}{r.run.WebsiteID, r.run.ID, r.run.Output, r.run.ExitCode})
		}
		busy := len(d.cron.running)
		d.cron.mu.Unlock()
		if (len(runs) >= n && busy == 0 && n > 0) || time.Now().After(deadline) {
			return runs
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunCronJobFailsWhenTheCommandFails(t *testing.T) {
	d, f, _ := withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "env XDG_RUNTIME_DIR=/run/user/20001 sh -c ", Output: "PHP Fatal error\ncf-exit:255\n"})
	job, err := d.RunCronJob(&agentv1.RunCronJobRequest{JobId: jobID2, WebsiteId: websiteID, CronJob: cronJob("0 3 * * *")})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err == nil || !strings.Contains(err.Error(), "exit code 255") || !strings.Contains(err.Error(), "PHP Fatal error") {
		t.Errorf("err = %v", err)
	}
	if r := job.Result(); r["exitCode"] != 255 || r["output"] != "PHP Fatal error" || r["cronJobId"] != cronID {
		t.Errorf("result = %v", r)
	}
}

func TestCronRefusesBadInput(t *testing.T) {
	d, _, _ := withSite(t)
	for name, c := range map[string]*agentv1.CronJob{
		"schedule":  {Id: cronID, Schedule: "61 * * * *", Command: "true"},
		"two lines": {Id: cronID, Schedule: "* * * * *", Command: "true\nrm -rf /"},
		"id":        {Id: "../x", Schedule: "* * * * *", Command: "true"},
	} {
		if _, err := d.SetCronJobs(&agentv1.SetCronJobsRequest{JobId: jobID2, WebsiteId: websiteID, CronJobs: []*agentv1.CronJob{c}}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	many := make([]*agentv1.CronJob, 21)
	for i := range many {
		many[i] = cronJob("* * * * *")
	}
	if _, err := d.SetCronJobs(&agentv1.SetCronJobsRequest{JobId: jobID2, WebsiteId: websiteID, CronJobs: many}); err == nil {
		t.Error("21 cron jobs: no error")
	}
}

func deployRequest() *agentv1.DeployRequest {
	w := website(siteImage)
	w.DocumentRoot = "releases/" + releaseID + "/public"
	return &agentv1.DeployRequest{
		JobId: jobID2, Website: w, ReleaseId: releaseID, Repository: "git@github.com:acme/shop.git", Branch: "main",
		BuildCommands: []string{"composer install --no-dev", "php artisan migrate --force"},
		Secrets:       map[string]string{"NPM_TOKEN": "s3cret value", "APP_KEY": "base64:abc"},
		DeployKey:     "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n",
	}
}

const buildStart = asSite + "env XDG_RUNTIME_DIR=/run/user/20001 systemd-run --user --wait --collect --pipe --quiet "

func TestDeployBuildsInAContainerAndSwitchesWithNoStop(t *testing.T) {
	d, f, edge := withSite(t)
	app := d.Host.AppDir("acme_x7k2")
	home := d.Host.Home("acme_x7k2")
	f.Rules = append(f.Rules,
		execx.Rule{Prefix: buildStart, Input: "", Output: "cf-commit:4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d fix: the checkout page\n"},
		execx.Rule{Prefix: asSite + "ls -1 -- " + app + "/releases", Output: releaseID + "\n" + oldID + "\n01JBQ3Z5K8N6T4W2X9Y7R5P3Q9\nnotes.txt\n"},
	)
	req := deployRequest()
	req.KeepReleases = []string{oldID}
	job, err := d.Deploy(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, s := range job.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, "|"); got != "Get the code|Build|Check image signature|Start new container on a copy|Health check|Switch traffic|Stop old container|Remove old releases" {
		t.Errorf("steps = %s", got)
	}

	var fetch, build string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, buildStart) {
			if strings.Contains(c, "git clone") {
				fetch = c
			} else {
				build = c
			}
		}
	}
	for _, want := range []string{
		"-p CPUQuota=100% -p MemoryMax=1073741824 -p MemorySwapMax=0 -p TasksMax=100 -- podman --runtime=/usr/local/bin/runsc --runtime-flag=platform=systrap --runtime-flag=network=sandbox --runtime-flag=ignore-cgroups run --rm --pull=never --network=pasta --userns=keep-id --cap-drop=all --security-opt=no-new-privileges --read-only --tmpfs=/tmp --env=HOME=/tmp",
		"--volume=" + app + ":/app:Z --workdir=/app/releases/" + releaseID,
		"--volume=" + home + "/.cf-deploy/" + releaseID + ".key:/run/cf-deploy-key:ro,Z",
		siteImage + " sh -c set -e\ngit clone",
		" sh main git@github.com:acme/shop.git",
	} {
		if !strings.Contains(fetch, want) {
			t.Errorf("the fetch command has no %q:\n%s", want, fetch)
		}
	}
	if !strings.Contains(build, "--env-file="+home+"/.cf-deploy/"+releaseID+".env") || !strings.Contains(build, "set -e\nexec 1>&2\ncomposer install --no-dev\nphp artisan migrate --force sh") || strings.Contains(build, "cf-deploy-key") {
		t.Errorf("build = %s", build)
	}
	// The secrets and the key are on the standard input, not in a command line.
	for _, c := range f.Calls {
		if strings.Contains(c, "s3cret") || strings.Contains(c, "PRIVATE KEY") {
			t.Errorf("a secret is in a command line: %s", c)
		}
	}
	if _, in := called(f, asSite+"sh -c umask 077 && mkdir -p \"$1\" && cat > \"$1/$2\" sh "+home+"/.cf-deploy "+releaseID+".env"); in != "APP_KEY=base64:abc\nNPM_TOKEN=s3cret value\n" {
		t.Errorf("env file = %q", in)
	}
	if !f.Called(asSite + "rm -rf -- " + home + "/.cf-deploy") {
		t.Error("the agent did not remove the files of the deploy")
	}

	site, _ := d.State.Get(websiteID)
	if site.Suffix != "-b" || site.Website.GetDocumentRoot() != "releases/"+releaseID+"/public" || edge.sites[websiteID].Port != site.Port {
		t.Errorf("site = %+v", site)
	}
	if !strings.Contains(readFile(t, mustUnitDir(t, d)+"/cf-acme_x7k2-b.container"), "CF_DOCUMENT_ROOT=/app/releases/"+releaseID+"/public") {
		t.Error("the new container does not serve the release")
	}
	if !f.Called(asSite + "ln -sfnT -- releases/" + releaseID + " " + app + "/current") {
		t.Error("no link to the current release")
	}
	// Only the release that is not in the list goes. A file of the owner stays.
	if !f.Called(asSite+"rm -rf -- "+app+"/releases/01JBQ3Z5K8N6T4W2X9Y7R5P3Q9") || f.Called(asSite+"rm -rf -- "+app+"/releases/"+oldID) || f.Called(asSite+"rm -rf -- "+app+"/releases/notes.txt") {
		t.Errorf("calls = %v", f.Calls)
	}
	r := job.Result()
	if r["commit"] != "4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d" || r["message"] != "fix: the checkout page" || r["releaseId"] != releaseID || len(r["removed"].([]string)) != 1 {
		t.Errorf("result = %v", r)
	}
}

func mustUnitDir(t *testing.T, d *Deps) string {
	t.Helper()
	dir, err := d.Host.UnitDir("acme_x7k2")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDeployKeepsTheOldReleaseWhenTheBuildFails(t *testing.T) {
	d, f, edge := withSite(t)
	app := d.Host.AppDir("acme_x7k2")
	n := 0
	f.Rules = append(f.Rules, execx.Rule{Prefix: buildStart, Do: func() error {
		if n++; n == 2 {
			return errors.New("runuser -u acme_x7k2 -- env …: exit status 2: composer: lock file is out of date")
		}
		return nil
	}})
	job, err := d.Deploy(deployRequest())
	if err != nil {
		t.Fatal(err)
	}
	err = runSteps(t, job, nil)
	if err == nil || err.Error() != "a build command failed: exit status 2: composer: lock file is out of date" {
		t.Fatalf("err = %v", err)
	}
	site, _ := d.State.Get(websiteID)
	if site.Suffix != "" || site.Website.GetDocumentRoot() != "" || edge.sites[websiteID].Port != 20000 {
		t.Errorf("the website changed: %+v", site)
	}
	if !f.Called(asSite+"rm -rf -- "+app+"/releases/"+releaseID) || f.Called("systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2-b.service") {
		t.Errorf("calls = %v", f.Calls)
	}
	if !f.Called(asSite + "rm -rf -- " + d.Host.Home("acme_x7k2") + "/.cf-deploy") {
		t.Error("the agent did not remove the files of the deploy")
	}
}

func TestDeployKeepsTheOldReleaseWhenTheHealthCheckFails(t *testing.T) {
	d, f, edge := withSite(t)
	d.ReadyTimeout = 1
	d.Health = func(_ context.Context, port int, _ string) (int, error) {
		if port == 20001 {
			return 500, nil
		}
		return 200, nil
	}
	job, err := d.Deploy(deployRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err == nil {
		t.Fatal("the job did not fail")
	}
	site, _ := d.State.Get(websiteID)
	if site.Suffix != "" || edge.sites[websiteID].Port != 20000 {
		t.Errorf("the website changed: %+v", site)
	}
	if !f.Called("systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2-b.service") || !f.Called(asSite+"rm -rf -- "+d.Host.AppDir("acme_x7k2")+"/releases/"+releaseID) || f.Called(asSite+"ln ") {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestDeployRefusesBadInput(t *testing.T) {
	d, _, _ := withSite(t)
	for name, change := range map[string]func(r *agentv1.DeployRequest){
		"repository with a password": func(r *agentv1.DeployRequest) { r.Repository = "https://user:pass@github.com/acme/shop.git" },
		"repository option":          func(r *agentv1.DeployRequest) { r.Repository = "--upload-pack=touch /tmp/x" },
		"local repository":           func(r *agentv1.DeployRequest) { r.Repository = "file:///etc" },
		"branch option":              func(r *agentv1.DeployRequest) { r.Branch = "--config=x" },
		"two-line command":           func(r *agentv1.DeployRequest) { r.BuildCommands = []string{"a\nb"} },
		"secret name":                func(r *agentv1.DeployRequest) { r.Secrets = map[string]string{"a b": "x"} },
		"two-line secret":            func(r *agentv1.DeployRequest) { r.Secrets = map[string]string{"A": "x\nB=y"} },
		"key":                        func(r *agentv1.DeployRequest) { r.DeployKey = "not a key" },
		"release":                    func(r *agentv1.DeployRequest) { r.ReleaseId = "../.." },
		"document root":              func(r *agentv1.DeployRequest) { r.Website.DocumentRoot = "public" },
		"keep":                       func(r *agentv1.DeployRequest) { r.KeepReleases = []string{"../x"} },
	} {
		req := deployRequest()
		change(req)
		if _, err := d.Deploy(req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestActivateReleaseGoesBackToARelease(t *testing.T) {
	d, f, _ := withSite(t)
	w := website(siteImage)
	w.DocumentRoot = "releases/" + oldID + "/public"
	job, err := d.ActivateRelease(&agentv1.ActivateReleaseRequest{JobId: jobID2, Website: w, ReleaseId: oldID})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	app := d.Host.AppDir("acme_x7k2")
	site, _ := d.State.Get(websiteID)
	if site.Website.GetDocumentRoot() != w.DocumentRoot || !f.Called(asSite+"test -d "+app+"/releases/"+oldID) || !f.Called(asSite+"ln -sfnT -- releases/"+oldID+" "+app+"/current") {
		t.Errorf("site = %+v, calls = %v", site, f.Calls)
	}

	// A release that the server does not have.
	d, f, _ = withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "test -d", Err: errors.New("exit status 1")})
	job, _ = d.ActivateRelease(&agentv1.ActivateReleaseRequest{JobId: jobID2, Website: w, ReleaseId: oldID})
	if err := runSteps(t, job, nil); err == nil || f.Called("systemctl --user --machine=acme_x7k2@ restart") {
		t.Errorf("err = %v, calls = %v", err, f.Calls)
	}
	w.DocumentRoot = "releases/" + releaseID
	if _, err := d.ActivateRelease(&agentv1.ActivateReleaseRequest{JobId: jobID2, Website: w, ReleaseId: oldID}); err == nil {
		t.Error("a document root of a different release: no error")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
