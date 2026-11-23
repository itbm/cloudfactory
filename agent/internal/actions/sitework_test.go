package actions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/sitefiles"
	"github.com/itbm/cloudfactory/agent/internal/sitelogs"
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

func TestSearchLogsMergesTheAccessLogAndTheContainers(t *testing.T) {
	d, f, _ := deps(t)
	d.AccessLogs = &sitelogs.Store{Dir: filepath.Join(t.TempDir(), "logs")}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website(siteImage)})); err != nil {
		t.Fatal(err)
	}
	f.Calls, f.Inputs = nil, nil
	at := func(s int) time.Time { return time.Date(2026, 11, 14, 10, 0, s, 0, time.UTC) }
	// The website got the directory of its access log when the agent made its route.
	for _, a := range []sitelogs.Access{
		{At: at(1), Client: "203.0.113.7", Method: "GET", Host: "acme.example", URI: "/", Proto: "HTTP/2.0", Status: 200, Size: 512, Duration: 0.02, UserAgent: "Mozilla/5.0"},
		{At: at(4), Client: "203.0.113.8", Method: "POST", Host: "acme.example", URI: "/cart", Status: 500},
	} {
		if err := d.AccessLogs.Append(websiteID, a.Line()); err != nil {
			t.Fatal(err)
		}
	}
	log := d.Host.Home("acme_x7k2") + "/website.log"
	container, _ := json.Marshal(map[string]any{"entries": []sitelogs.Entry{
		{At: at(5), Source: sitelogs.SourceError, Text: "PHP Fatal error", Pos: sitelogs.Position{Inode: 9, Offset: 40}},
		{At: at(2), Source: sitelogs.SourceOutput, Text: "started", Pos: sitelogs.Position{Inode: 9, Offset: 0}},
	}, "summary": sitelogs.Summary{Output: 1, Error: 1, Oldest: at(0)}})
	f.Rules = append(f.Rules, execx.Rule{Prefix: asSite + "/usr/local/bin/cf-agent log-search " + log, Output: string(container)})

	res, err := d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{WebsiteId: websiteID, Limit: 2, Summary: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range res.GetEntries() {
		got = append(got, e.GetSource()+" "+e.GetAt())
	}
	want := []string{"error 2026-11-14T10:00:05Z", "access 2026-11-14T10:00:04Z"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") || res.GetNext() == "" {
		t.Fatalf("entries = %v, next %q", got, res.GetNext())
	}
	if a := res.GetEntries()[1].GetAccess(); a.GetStatus() != 500 || a.GetMethod() != "POST" || a.GetUri() != "/cart" || res.GetEntries()[0].GetText() != "PHP Fatal error" {
		t.Errorf("entries = %v", res.GetEntries())
	}
	sum := res.GetSummary()
	if sum.GetAccess() != 2 || sum.GetError() != 1 || sum.GetOutput() != 1 || sum.GetStatuses()["5xx"] != 1 || sum.GetSizeBytes() != 512 || sum.GetOldest() != "2026-11-14T10:00:00Z" {
		t.Errorf("summary = %v", sum)
	}
	// The worker reads the log of the containers as the website user, with the search on the
	// standard input. The unit of the website sends the output of the container to that file.
	call, input := called(f, asSite)
	if call != asSite+"/usr/local/bin/cf-agent log-search "+log || !strings.Contains(input, `"query":{"sources":["access","output","error"],"limit":2,"summary":true}`) {
		t.Errorf("call = %q, input = %q", call, input)
	}
	unit := quadletUnit(t, d)
	if !strings.Contains(unit, "LogDriver=k8s-file\n") || !strings.Contains(unit, "--log-opt=path="+log+" --log-opt=max-size=10mb") {
		t.Errorf("unit = %s", unit)
	}

	// The next page: the access log continues before its last entry, and the search of the
	// containers gets the position of its last entry.
	f.Calls, f.Inputs = nil, nil
	f.Rules = []execx.Rule{{Prefix: asSite, Output: `{"entries":[{"at":"2026-11-14T10:00:02Z","source":"output","text":"started","pos":{"i":9}}]}`}}
	res, err = d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{WebsiteId: websiteID, Limit: 3, Cursor: res.GetNext()})
	if err != nil || len(res.GetEntries()) != 2 || res.GetEntries()[0].GetText() != "started" || res.GetEntries()[1].GetAccess().GetUri() != "/" || res.GetNext() != "" {
		t.Fatalf("page 2 = %v, %v", res, err)
	}
	if _, input := called(f, asSite); !strings.Contains(input, `"position":{"i":9,"o":40}`) {
		t.Errorf("input = %q", input)
	}

	// A filter of the access log does not read the log of the containers.
	f.Calls = nil
	res, err = d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{WebsiteId: websiteID, Sources: []string{"access"}, Statuses: []string{"2xx"}})
	if err != nil || len(res.GetEntries()) != 1 || len(f.Calls) != 0 {
		t.Errorf("access only = %v, %v, calls %v", res, err, f.Calls)
	}

	// The errors of a search.
	var fe *FileError
	if _, err := d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{WebsiteId: websiteID, Statuses: []string{"600"}}); !errors.As(err, &fe) || fe.Code != sitefiles.CodeInvalid {
		t.Errorf("bad status = %v", err)
	}
	if _, err := d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{WebsiteId: jobID2}); !errors.Is(err, ErrUnknownWebsite) {
		t.Errorf("unknown website = %v", err)
	}
	f.Rules = []execx.Rule{{Prefix: asSite, Output: `{"error":"the log file cannot be read"}`}}
	if _, err := d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{WebsiteId: websiteID}); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("error of the search = %v", err)
	}

	// With no website, the search is in the access log of the routes that are not for a
	// website. It reads no file of a website.
	if err := d.AccessLogs.Append(sitelogs.ServerLog, sitelogs.Access{At: at(3), Host: "panel.example", URI: "/api/v1/me", Status: 200}.Line()); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	res, err = d.SearchLogs(context.Background(), &agentv1.SearchLogsRequest{Sources: []string{"error"}})
	if err != nil || len(res.GetEntries()) != 1 || res.GetEntries()[0].GetAccess().GetHost() != "panel.example" || len(f.Calls) != 0 {
		t.Errorf("server log = %v, %v, calls %v", res, err, f.Calls)
	}

	// The removal of the website removes its access log.
	if err := run(t)(d.DeleteWebsite(&agentv1.DeleteWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.AccessLogs.Dir, websiteID)); !os.IsNotExist(err) {
		t.Errorf("access log after the removal: %v", err)
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
	if got := strings.Join(names, "|"); got != "Get the code|Build|Migrate the database|Check image signature|Start new container on a copy|Health check|Switch traffic|Stop old container|Remove old releases" {
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
	if r["commit"] != "4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d" || r["message"] != "fix: the checkout page" || r["releaseId"] != releaseID || len(r["removed"].([]string)) != 1 || r["migrated"] != false || r["healthError"] != "" {
		t.Errorf("result = %v", r)
	}
}

// buildCalls gives the commands of the build containers that do not get the code.
func buildCalls(f *execx.Fake) []string {
	var out []string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, buildStart) && !strings.Contains(c, "git clone") {
			out = append(out, c)
		}
	}
	return out
}

func TestDeployRunsTheMigrationCommandBeforeTheSwitch(t *testing.T) {
	d, f, edge := withSite(t)
	home := d.Host.Home("acme_x7k2")
	req := deployRequest()
	req.BuildCommands = []string{"composer install --no-dev"}
	req.MigrationCommand = "php artisan migrate --force"
	// The edge has the old port while the migration runs.
	f.Rules = append(f.Rules, execx.Rule{Prefix: buildStart, Do: func() error {
		if edge.sites[websiteID].Port != 20000 {
			return errors.New("the switch was before the migration")
		}
		return nil
	}})
	job, err := d.Deploy(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	builds := buildCalls(f)
	if len(builds) != 2 || !strings.Contains(builds[0], "exec 1>&2\ncomposer install --no-dev sh") || !strings.Contains(builds[1], "exec 1>&2\nphp artisan migrate --force sh") {
		t.Fatalf("builds = %q", builds)
	}
	// The migration gets the same environment file as the build. The agent writes it once.
	if !strings.Contains(builds[1], "--env-file="+home+"/.cf-deploy/"+releaseID+".env") || !strings.Contains(builds[1], "--workdir=/app/releases/"+releaseID) {
		t.Errorf("migration = %s", builds[1])
	}
	writes := 0
	for _, c := range f.Calls {
		if strings.HasSuffix(c, home+"/.cf-deploy "+releaseID+".env") {
			writes++
		}
	}
	if writes != 1 || !f.Called(asSite+"rm -rf -- "+home+"/.cf-deploy") {
		t.Errorf("env file writes = %d, calls = %v", writes, f.Calls)
	}
	if r := job.Result(); r["migrated"] != true || r["healthError"] != "" {
		t.Errorf("result = %v", r)
	}
}

func TestDeployStopsWhenTheMigrationCommandFails(t *testing.T) {
	d, f, edge := withSite(t)
	req := deployRequest()
	req.MigrationCommand = "php artisan migrate --force"
	f.Rules = append(f.Rules, execx.Rule{Prefix: buildStart, Do: func() error {
		if strings.Contains(f.Calls[len(f.Calls)-1], "migrate --force sh") && !strings.Contains(f.Calls[len(f.Calls)-1], "composer") {
			return errors.New("runuser -u acme_x7k2 -- env …: exit status 1: SQLSTATE[42S01]: Base table or view already exists")
		}
		return nil
	}})
	job, err := d.Deploy(req)
	if err != nil {
		t.Fatal(err)
	}
	err = runSteps(t, job, nil)
	if err == nil || err.Error() != "the migration command failed: exit status 1: SQLSTATE[42S01]: Base table or view already exists" {
		t.Fatalf("err = %v", err)
	}
	if site, _ := d.State.Get(websiteID); site.Suffix != "" || edge.sites[websiteID].Port != 20000 {
		t.Errorf("the website changed: %+v", site)
	}
	if !f.Called(asSite + "rm -rf -- " + d.Host.Home("acme_x7k2") + "/.cf-deploy") {
		t.Error("the agent did not remove the files of the deploy")
	}
}

// failAfterSwitch makes each check of the new container fail after the switch.
func failAfterSwitch(d *Deps, edge *fakeEdge) {
	d.MaxWatch = 50 * time.Millisecond
	d.Health = func(_ context.Context, port int, _ string) (int, error) {
		if port == 20001 && edge.sites[websiteID].Port == 20001 {
			return 500, nil
		}
		return 200, nil
	}
}

func TestDeployGoesBackWhenTheHealthCheckAfterTheSwitchFails(t *testing.T) {
	d, f, edge := withSite(t)
	failAfterSwitch(d, edge)
	req := deployRequest()
	req.HealthCheck = &agentv1.HealthCheck{MaxErrorRate: 0.05, Seconds: 300}
	job, err := d.Deploy(req)
	if err != nil {
		t.Fatal(err)
	}
	err = runSteps(t, job, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "the health check failed after the switch: ") {
		t.Fatalf("err = %v", err)
	}
	// FR-1008: the website goes back to the previous release.
	if site, _ := d.State.Get(websiteID); site.Suffix != "" || site.Website.GetDocumentRoot() != "" || edge.sites[websiteID].Port != 20000 {
		t.Errorf("the website did not go back: %+v", site)
	}
	if !f.Called("systemctl --user --machine=acme_x7k2@ stop cf-acme_x7k2-b.service") || !f.Called(asSite+"rm -rf -- "+d.Host.AppDir("acme_x7k2")+"/releases/"+releaseID) {
		t.Errorf("calls = %v", f.Calls)
	}
}

func TestDeployWithAMigrationKeepsTheReleaseWhenTheHealthCheckFails(t *testing.T) {
	d, f, edge := withSite(t)
	failAfterSwitch(d, edge)
	req := deployRequest()
	req.MigrationCommand = "php artisan migrate --force"
	req.HealthCheck = &agentv1.HealthCheck{MaxErrorRate: 0.05, Seconds: 300}
	job, err := d.Deploy(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatalf("the job failed: %v", err)
	}
	// FR-1009: the website keeps the new release, and the result has the error.
	site, _ := d.State.Get(websiteID)
	if site.Suffix != "-b" || site.Website.GetDocumentRoot() != "releases/"+releaseID+"/public" || edge.sites[websiteID].Port != site.Port {
		t.Errorf("the website went back: %+v", site)
	}
	// The step "Get the code" empties the release first. Nothing removes it after that.
	removes := 0
	for _, c := range f.Calls {
		if c == asSite+"rm -rf -- "+d.Host.AppDir("acme_x7k2")+"/releases/"+releaseID {
			removes++
		}
	}
	if removes != 1 {
		t.Errorf("the agent removed the new release: %d removes", removes)
	}
	r := job.Result()
	if r["migrated"] != true || !strings.HasPrefix(r["healthError"].(string), "the health check failed after the switch: ") || !strings.Contains(r["healthError"].(string), "last error: status 500") {
		t.Errorf("result = %v", r)
	}
}

const lsRemoteStart = buildStart + "-p RuntimeMaxSec=45 "

func TestCheckBranchesGivesTheLastCommitOfEachBranch(t *testing.T) {
	d, f, _ := withSite(t)
	home := d.Host.Home("acme_x7k2")
	head := "4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d4f9a1c2d"
	// The fake gives the same output for each check: a branch "gone" is not in it.
	f.Rules = append(f.Rules, execx.Rule{Prefix: lsRemoteStart, Output: head + "\trefs/heads/main\n"})
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n"
	heads, err := d.CheckBranches(context.Background(), &agentv1.CheckBranchesRequest{Checks: []*agentv1.BranchCheck{
		{WebsiteId: websiteID, Repository: "git@github.com:acme/shop.git", Branch: "main", DeployKey: key},
		{WebsiteId: websiteID, Repository: "git@github.com:acme/shop.git", Branch: "gone"},
		{WebsiteId: jobID2, Repository: "git@github.com:acme/shop.git", Branch: "main"},
		{WebsiteId: websiteID, Repository: "--upload-pack=x", Branch: "main"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := heads.GetHeads()
	if len(got) != 4 || got[0].GetCommit() != head || got[0].GetError() != "" || got[0].GetWebsiteId() != websiteID {
		t.Fatalf("heads = %v", got)
	}
	if got[1].GetCommit() != "" || got[1].GetError() != "the branch gone is not in the repository" {
		t.Errorf("missing branch = %v", got[1])
	}
	if got[2].GetError() != "the agent does not know this website" || got[3].GetError() == "" {
		t.Errorf("bad checks = %v, %v", got[2], got[3])
	}

	// The check runs in a build container of the website with the key of the website. The
	// key is on the standard input, in a directory that a deploy does not remove. The checks
	// of one website run in the order of the request, so the first check is "main".
	call, _ := called(f, lsRemoteStart)
	if second := strings.Index(strings.Join(f.Calls, "\n"), " sh gone git@github.com"); second < strings.Index(strings.Join(f.Calls, "\n"), " sh main git@github.com") {
		t.Errorf("the checks of one website did not run in order:\n%s", strings.Join(f.Calls, "\n"))
	}
	for _, want := range []string{
		"--volume=" + home + "/.cf-check/deploy.key:/run/cf-deploy-key:ro,Z",
		"--volume=" + home + "/.cf-ssh:/run/cf-ssh:Z",
		"--workdir=/tmp",
		siteImage + " sh -c set -e\ngit ls-remote --heads -- \"$2\" \"refs/heads/$1\" sh main git@github.com:acme/shop.git",
	} {
		if !strings.Contains(call, want) {
			t.Errorf("the check has no %q:\n%s", want, call)
		}
	}
	if _, in := called(f, asSite+"sh -c umask 077 && mkdir -p \"$1\" && cat > \"$1/$2\" sh "+home+"/.cf-check deploy.key"); in != key {
		t.Errorf("key = %q", in)
	}
	if !f.Called(asSite + "rm -rf -- " + home + "/.cf-check") {
		t.Error("the agent did not remove the key")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "PRIVATE KEY") {
			t.Errorf("the key is in a command line: %s", c)
		}
	}

	if _, err := d.CheckBranches(context.Background(), &agentv1.CheckBranchesRequest{Checks: make([]*agentv1.BranchCheck, MaxBranchChecks+1)}); err == nil {
		t.Error("too many checks: no error")
	}
}

func TestCheckBranchesExplainsAChangedHostKey(t *testing.T) {
	d, f, _ := withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: lsRemoteStart, Err: errors.New("exit status 128: @@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@")})
	heads, err := d.CheckBranches(context.Background(), &agentv1.CheckBranchesRequest{Checks: []*agentv1.BranchCheck{
		{WebsiteId: websiteID, Repository: "git@github.com:acme/shop.git", Branch: "main", DeployKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n"},
	}})
	if err != nil || !strings.Contains(heads.GetHeads()[0].GetError(), "start a deploy and select to forget the key of the Git host") {
		t.Fatalf("heads = %v, err = %v", heads, err)
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
