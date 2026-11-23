package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type siteCall struct {
	Method, Path, Query string
	Body                map[string]any
}

// sitePanel answers like the panel API for the file manager, the logs, the cron jobs and the
// deploys of one website.
func sitePanel(t *testing.T) (*httptest.Server, *[]siteCall) {
	t.Helper()
	var calls []siteCall
	deploy := `{"source":{"repository":"git@github.com:acme/shop.git","branch":"main","webDirectory":"public","trigger":"webhook","checkMinutes":5,"buildCommands":["composer install"],"migrationCommand":"php artisan migrate --force","deployKey":"ssh-ed25519 AAAA cloudfactory","webhookUrl":"https://panel.example/api/v1/hooks/deploy/tok","webhookSecret":"hook-secret","deployQueued":false,"secrets":["NPM_TOKEN"]},"releases":[{"id":"R2","createdAt":"2026-11-14T10:00:00Z","state":"ready","live":true,"trigger":"webhook","migration":true,"healthError":"the health check failed after the switch: 9 of 30 checks failed.","commit":"4f9a1c2d5e6f7a8b","message":"Fix the cart"}]}`
	previews := `{"settings":{"enabled":true,"domain":"preview.acme.example","limit":1,"copyEnvironment":false},"suggestedDomain":"preview.acme.example","maxLimit":10,"previews":[{"number":42,"state":"live","name":"pr-42.preview.acme.example","branch":"feature/csv","title":"Add CSV","error":null},{"number":47,"state":"failed","name":null,"branch":"bump","title":"Bump","error":"The limit of the previews of this website is 1."}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := siteCall{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v1"), Query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &call.Body)
		}
		calls = append(calls, call)
		w.Header().Set("Content-Type", "application/json")
		job := `"job":{"id":"J1","title":"Site job","state":"queued"}`
		switch key := r.Method + " " + call.Path; key {
		case "GET /websites":
			w.Write([]byte(`{"data":[{"id":"W1","name":"acme.example","status":"running"}]}`))
		case "GET /jobs/J1":
			w.Write([]byte(`{"data":{"id":"J1","title":"Site job","state":"succeeded","steps":["One"],"step":1,"error":null}}`))
		case "GET /websites/W1/files":
			w.Write([]byte(`{"path":"/public","entries":[{"name":"css","path":"/public/css","type":"directory","sizeBytes":0,"modifiedAt":"2026-11-14T10:00:00Z","mode":"0750"},{"name":"index.php","path":"/public/index.php","type":"file","sizeBytes":13,"modifiedAt":"2026-11-14T10:00:00Z","mode":"0640"}],"truncated":true}`))
		case "GET /websites/W1/files/content":
			if r.URL.Query().Get("path") == "/logo.png" {
				w.Write([]byte(`{"file":{"name":"logo.png"},"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}) + `"}`))
				return
			}
			w.Write([]byte(`{"file":{"name":"index.php"},"encoding":"utf8","content":"<?php echo 1;\n"}`))
		case "PUT /websites/W1/files/content", "POST /websites/W1/files/move":
			w.Write([]byte(`{"file":{"name":"x","type":"file"}}`))
		case "POST /websites/W1/files/directories":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"file":{"name":"x","type":"directory"}}`))
		case "DELETE /websites/W1/files":
			w.WriteHeader(http.StatusNoContent)
		case "GET /websites/W1/logs":
			w.Write([]byte(`{"lines":[{"at":"2026-11-14T10:00:00Z","stream":"access","text":"GET /"},{"at":"2026-11-14T10:00:01Z","stream":"error","text":"PHP Warning"}]}`))
		case "GET /websites/W1/cron-jobs":
			w.Write([]byte(`{"data":[{"id":"C1","schedule":"*/5 * * * *","command":"php artisan schedule:run","enabled":true,"lastRunAt":null,"lastResult":null}]}`))
		case "POST /websites/W1/cron-jobs", "PATCH /websites/W1/cron-jobs/C1", "DELETE /websites/W1/cron-jobs/C1", "POST /websites/W1/cron-jobs/C1/run",
			"POST /websites/W1/deploy/releases", "POST /websites/W1/deploy/releases/R1/activate":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{` + job + `}`))
		case "GET /websites/W1/deploy", "PUT /websites/W1/deploy", "PUT /websites/W1/deploy/secrets/NPM_TOKEN", "DELETE /websites/W1/deploy/secrets/NPM_TOKEN", "POST /websites/W1/deploy/webhook":
			w.Write([]byte(deploy))
		case "DELETE /websites/W1/deploy":
			w.WriteHeader(http.StatusNoContent)
		case "GET /websites/W1/security":
			w.Write([]byte(`{"security":{"firewall":{"mode":"detect","excludedRules":[942100],"skippedPaths":["/wp-admin/"]},"ipRules":{"allow":[],"block":["198.51.100.7"]},"protectedPaths":[{"path":"/staging/","users":[{"name":"client"},{"name":"agency"}]}],"signInLimit":{"requestsPerMinute":0,"paths":[]}}}`))
		case "PATCH /websites/W1/security":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"security":{},` + job + `}`))
		case "GET /websites/W1/security/events":
			w.Write([]byte(`{"events":[{"at":"2026-11-22T21:26:59Z","clientIp":"203.0.113.9","host":"acme.example","uri":"/?q=<script>","action":"blocked","score":20,"rules":[{"id":941100,"message":"XSS Attack Detected via libinjection","data":"Matched Data: XSS"}]}]}`))
		case "GET /websites/W1/previews", "PUT /websites/W1/previews":
			w.Write([]byte(previews))
		case "POST /websites/W1/previews":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"status":"started","message":"The panel makes the preview pr-51.preview.acme.example.","preview":{"number":51,"name":"pr-51.preview.acme.example","state":"creating"}}`))
		case "DELETE /websites/W1/previews/42":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{` + job + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// lastSite gives the last call that is not the search for the website or a job.
func lastSite(calls *[]siteCall) siteCall {
	for i := len(*calls) - 1; i >= 0; i-- {
		if c := (*calls)[i]; c.Path != "/websites" && !strings.HasPrefix(c.Path, "/jobs/") {
			return c
		}
	}
	return siteCall{}
}

func TestFiles(t *testing.T) {
	srv, calls := sitePanel(t)
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"files", "list", "acme.example", "public/"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Query != "path=%2Fpublic" || !strings.Contains(out.String(), "index.php") || !strings.Contains(out.String(), "more entries") {
		t.Errorf("call = %+v, out = %s", c, out)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"files", "get", "acme.example", "/public/index.php"}); err != nil || out.String() != "<?php echo 1;\n" {
		t.Errorf("get = %q, %v", out, err)
	}
	// A file that is not text needs --output.
	if err := a.dispatch(ctx, []string{"files", "get", "acme.example", "/logo.png"}); err == nil {
		t.Error("a binary file went to the terminal")
	}
	dir := t.TempDir()
	if err := a.dispatch(ctx, []string{"files", "get", "acme.example", "/logo.png", "--output", filepath.Join(dir, "logo.png")}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "logo.png")); string(b) != "\x89PNG" {
		t.Errorf("saved = %q", b)
	}

	local := filepath.Join(dir, "new.txt")
	os.WriteFile(local, []byte("héllo"), 0o600)
	if err := a.dispatch(ctx, []string{"files", "put", "acme.example", "/public/new.txt", "--from", local, "--create-only"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "PUT" || c.Body["path"] != "/public/new.txt" || c.Body["content"] != base64.StdEncoding.EncodeToString([]byte("héllo")) || c.Body["encoding"] != "base64" || c.Body["createOnly"] != true {
		t.Errorf("put = %+v", c)
	}
	a.in = strings.NewReader("from stdin")
	if err := a.dispatch(ctx, []string{"files", "put", "acme.example", "/a.txt", "--from", "-"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Body["content"] != base64.StdEncoding.EncodeToString([]byte("from stdin")) {
		t.Errorf("put from stdin = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"files", "mkdir", "acme.example", "/public/images"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, []string{"files", "move", "acme.example", "/a.txt", "/public/b.txt"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Body["from"] != "/a.txt" || c.Body["to"] != "/public/b.txt" {
		t.Errorf("move = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"files", "delete", "acme.example", "/public/b.txt"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "DELETE" || c.Query != "path=%2Fpublic%2Fb.txt" {
		t.Errorf("delete = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"files", "delete", "acme.example"}); err == nil {
		t.Error("delete with no path gave no error")
	}
}

func TestLogs(t *testing.T) {
	srv, calls := sitePanel(t)
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"logs", "acme.example", "--lines", "50", "--type", "error"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Query != "lines=50&stream=error" || !strings.Contains(out.String(), "2026-11-14T10:00:01Z error  PHP Warning") {
		t.Errorf("call = %+v, out = %s", c, out)
	}
}

func TestCron(t *testing.T) {
	srv, calls := sitePanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"cron", "list", "acme.example"}); err != nil || !strings.Contains(out.String(), "php artisan schedule:run") {
		t.Fatalf("list = %s, %v", out, err)
	}
	if err := a.dispatch(ctx, []string{"cron", "add", "acme.example", "--schedule", "0 3 * * *", "--command", "php bin/report.php"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "POST" || c.Body["schedule"] != "0 3 * * *" || c.Body["command"] != "php bin/report.php" || c.Body["enabled"] != true {
		t.Errorf("add = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"cron", "set", "acme.example", "C1", "--off"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "PATCH" || c.Path != "/websites/W1/cron-jobs/C1" || c.Body["enabled"] != false || len(c.Body) != 1 {
		t.Errorf("set = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"cron", "set", "acme.example", "C1"}); err == nil {
		t.Error("set with no change gave no error")
	}
	if err := a.dispatch(ctx, []string{"cron", "run", "acme.example", "C1"}); err != nil || lastSite(calls).Path != "/websites/W1/cron-jobs/C1/run" {
		t.Errorf("run = %+v, %v", lastSite(calls), err)
	}
	if err := a.dispatch(ctx, []string{"cron", "delete", "acme.example", "C1"}); err != nil || lastSite(calls).Method != "DELETE" {
		t.Errorf("delete = %+v, %v", lastSite(calls), err)
	}
	if !strings.Contains(out.String(), "Site job: complete.") {
		t.Errorf("out = %s", out)
	}
}

func TestDeploys(t *testing.T) {
	srv, calls := sitePanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"deploys", "show", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git@github.com:acme/shop.git", "ssh-ed25519 AAAA cloudfactory", "hooks/deploy/tok", "hook-secret", "php artisan migrate --force", "NPM_TOKEN", "4f9a1c2 ", "Fix the cart", "ready, check failed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show has no %q: %s", want, out)
		}
	}
	// A change keeps the repository and the branch that the command does not give.
	if err := a.dispatch(ctx, []string{"deploys", "connect", "acme.example", "--trigger", "webhook", "--build", "composer install", "--build", "npm ci"}); err != nil {
		t.Fatal(err)
	}
	c := lastSite(calls)
	if c.Method != "PUT" || c.Body["repository"] != "git@github.com:acme/shop.git" || c.Body["branch"] != "main" || c.Body["trigger"] != "webhook" || len(c.Body["buildCommands"].([]any)) != 2 {
		t.Errorf("connect = %+v", c)
	}
	if _, has := c.Body["webDirectory"]; has {
		t.Errorf("connect sent a web directory: %+v", c)
	}
	if _, has := c.Body["migrationCommand"]; has {
		t.Errorf("connect sent a migration command: %+v", c)
	}
	// FR-1004 and FR-1009: a check of the branch on a schedule, and a migration command.
	if err := a.dispatch(ctx, []string{"deploys", "connect", "acme.example", "--trigger", "schedule", "--check-minutes", "15", "--migration", "php artisan migrate --force"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Body["trigger"] != "schedule" || c.Body["checkMinutes"] != float64(15) || c.Body["migrationCommand"] != "php artisan migrate --force" {
		t.Errorf("schedule = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"deploys", "connect", "acme.example", "--no-migration"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Body["migrationCommand"] != nil {
		t.Errorf("no migration = %+v", c)
	} else if _, has := c.Body["migrationCommand"]; !has {
		t.Errorf("no migration sent no migrationCommand: %+v", c)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"deploys", "webhook", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "POST" || c.Path != "/websites/W1/deploy/webhook" || !strings.Contains(out.String(), "hook-secret") {
		t.Errorf("webhook = %+v: %s", c, out)
	}
	// The value of a secret is not an argument.
	a.in = strings.NewReader("s3cret\n")
	if err := a.dispatch(ctx, []string{"deploys", "secret", "set", "acme.example", "NPM_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Path != "/websites/W1/deploy/secrets/NPM_TOKEN" || c.Body["value"] != "s3cret" {
		t.Errorf("secret set = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"deploys", "secret", "remove", "acme.example", "NPM_TOKEN"}); err != nil || lastSite(calls).Method != "DELETE" {
		t.Errorf("secret remove = %+v, %v", lastSite(calls), err)
	}
	if err := a.dispatch(ctx, []string{"deploys", "start", "acme.example"}); err != nil || lastSite(calls).Path != "/websites/W1/deploy/releases" {
		t.Errorf("start = %+v, %v", lastSite(calls), err)
	}
	if err := a.dispatch(ctx, []string{"deploys", "activate", "acme.example", "R1"}); err != nil || lastSite(calls).Path != "/websites/W1/deploy/releases/R1/activate" {
		t.Errorf("activate = %+v, %v", lastSite(calls), err)
	}
	if err := a.dispatch(ctx, []string{"deploys", "disconnect", "acme.example"}); err != nil || lastSite(calls).Method != "DELETE" {
		t.Errorf("disconnect = %+v, %v", lastSite(calls), err)
	}
}

func TestSecurity(t *testing.T) {
	srv, calls := sitePanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"security", "show", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"detect", "942100", "/wp-admin/", "198.51.100.7", "Protected path /staging/  users client agency"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show does not have %q:\n%s", want, out)
		}
	}
	body := func(part string) any {
		t.Helper()
		c := lastSite(calls)
		if c.Method != "PATCH" || c.Path != "/websites/W1/security" || len(c.Body) != 1 {
			t.Fatalf("call = %+v", c)
		}
		b, _ := json.Marshal(c.Body[part])
		return string(b)
	}
	// A change keeps the values that the command does not give.
	if err := a.dispatch(ctx, []string{"security", "firewall", "acme.example", "--mode", "block", "--exclude", "920450", "--include", "942100"}); err != nil {
		t.Fatal(err)
	}
	if got := body("firewall"); got != `{"excludedRules":[920450],"mode":"block","skippedPaths":["/wp-admin/"]}` {
		t.Errorf("firewall = %s", got)
	}
	if err := a.dispatch(ctx, []string{"security", "firewall", "acme.example", "--exclude", "x"}); err == nil {
		t.Error("a rule that is not a number gave no error")
	}
	if err := a.dispatch(ctx, []string{"security", "ip", "acme.example", "--allow", "203.0.113.0/24", "--remove", "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	if got := body("ipRules"); got != `{"allow":["203.0.113.0/24"],"block":[]}` {
		t.Errorf("ipRules = %s", got)
	}
	if err := a.dispatch(ctx, []string{"security", "sign-in-limit", "acme.example", "--per-minute", "10", "--wordpress"}); err != nil {
		t.Fatal(err)
	}
	if got := body("signInLimit"); got != `{"paths":["/wp-login.php","/xmlrpc.php"],"requestsPerMinute":10}` {
		t.Errorf("signInLimit = %s", got)
	}
	// The password comes from the environment. The other users keep their passwords.
	t.Setenv("CF_PATH_PASSWORD", "correct horse")
	if err := a.dispatch(ctx, []string{"security", "protect", "acme.example", "/staging/", "--user", "client"}); err != nil {
		t.Fatal(err)
	}
	if got := body("protectedPaths"); got != `[{"path":"/staging/","users":[{"name":"client","password":"correct horse"},{"name":"agency"}]}]` {
		t.Errorf("protect = %s", got)
	}
	if err := a.dispatch(ctx, []string{"security", "protect", "acme.example", "/admin/", "--user", "me"}); err != nil {
		t.Fatal(err)
	}
	if got := body("protectedPaths"); got != `[{"path":"/staging/","users":[{"name":"client"},{"name":"agency"}]},{"path":"/admin/","users":[{"name":"me","password":"correct horse"}]}]` {
		t.Errorf("protect a new path = %s", got)
	}
	if err := a.dispatch(ctx, []string{"security", "unprotect", "acme.example", "/staging/", "--user", "agency"}); err != nil {
		t.Fatal(err)
	}
	if got := body("protectedPaths"); got != `[{"path":"/staging/","users":[{"name":"client"}]}]` {
		t.Errorf("unprotect a user = %s", got)
	}
	if err := a.dispatch(ctx, []string{"security", "unprotect", "acme.example", "/staging/"}); err != nil {
		t.Fatal(err)
	}
	if got := body("protectedPaths"); got != `[]` {
		t.Errorf("unprotect = %s", got)
	}
	if err := a.dispatch(ctx, []string{"security", "unprotect", "acme.example", "/other/"}); err == nil {
		t.Error("unprotect of a path with no password gave no error")
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"security", "events", "acme.example", "--limit", "5"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Query != "limit=5" || !strings.Contains(out.String(), "blocked  203.0.113.9 score 20") || !strings.Contains(out.String(), "rule 941100: XSS") {
		t.Errorf("events: %+v\n%s", c, out)
	}
}

// commandPanel answers like the panel API for the command log of one website. The command runs
// for two checks, and then ends with exitCode.
func commandPanel(t *testing.T, exitCode int) (*httptest.Server, *[]siteCall) {
	t.Helper()
	var calls []siteCall
	checks := 0
	entry := func(state string) string {
		exit := "null"
		if state == "done" {
			exit = strconv.Itoa(exitCode)
		}
		return `{"command":{"id":"K1","command":"php artisan migrate --force","state":"` + state + `","exitCode":` + exit + `,"timedOut":false,"actor":"Ann","ip":"203.0.113.5","createdAt":"2026-11-22T10:00:00Z","output":"   INFO  Nothing to migrate."}}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := siteCall{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v1"), Query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &call.Body)
		}
		calls = append(calls, call)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + call.Path {
		case "GET /websites":
			w.Write([]byte(`{"data":[{"id":"W1","name":"acme.example","status":"running"}]}`))
		case "GET /websites/W1/commands":
			w.Write([]byte(`{"data":[{"id":"K1","command":"php artisan migrate --force","state":"done","exitCode":0,"actor":"Ann","createdAt":"2026-11-22T10:00:00Z"}],"meta":{"currentPage":1,"lastPage":1,"total":1}}`))
		case "POST /websites/W1/commands":
			checks = 0
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(entry("running")))
		case "GET /websites/W1/commands/K1":
			if checks++; checks < 2 {
				w.Write([]byte(entry("running")))
				return
			}
			w.Write([]byte(entry("done")))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestCommandsRunWaitsForTheResult(t *testing.T) {
	srv, calls := commandPanel(t, 0)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, a.globalFlags([]string{"commands", "run", "acme.example", "--timeout", "120", "--", "php", "artisan", "migrate", "--force"})); err != nil {
		t.Fatal(err)
	}
	var post siteCall
	for _, c := range *calls {
		if c.Method == "POST" {
			post = c
		}
	}
	if post.Path != "/websites/W1/commands" || post.Body["command"] != "php artisan migrate --force" || post.Body["timeoutSeconds"] != float64(120) {
		t.Errorf("post = %+v", post)
	}
	if !strings.Contains(out.String(), "Nothing to migrate.") {
		t.Errorf("out = %s", out)
	}
	if err := a.dispatch(ctx, []string{"commands", "run", "acme.example"}); err == nil {
		t.Error("run with no command gave no error")
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"commands", "list", "acme.example", "--search", "migrate"}); err != nil || !strings.Contains(out.String(), "php artisan migrate --force") {
		t.Errorf("list = %s, %v", out, err)
	}
	if c := lastSite(calls); c.Query != "page=1&perPage=50&q=migrate" && !strings.Contains(c.Query, "q=migrate") {
		t.Errorf("list query = %q", c.Query)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"commands", "get", "acme.example", "K1"}); err != nil || !strings.Contains(out.String(), "By: Ann from 203.0.113.5") {
		t.Errorf("get = %s, %v", out, err)
	}
}

// The exit code of cf is the exit code of the command.
func TestCommandsRunGivesTheExitCodeOfTheCommand(t *testing.T) {
	srv, _ := commandPanel(t, 3)
	a, _ := testApp(srv)
	err := a.dispatch(context.Background(), []string{"commands", "run", "acme.example", "--", "php", "artisan", "migrate", "--force"})
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 3 || !strings.Contains(err.Error(), "exit code 3") {
		t.Errorf("err = %v", err)
	}
}

func TestPreviews(t *testing.T) {
	srv, calls := sitePanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"previews", "list", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pr-NUMBER.preview.acme.example", "1 at the same time", "pr-42.preview.acme.example", "feature/csv", "#47: The limit of the previews"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list has no %q: %s", want, out)
		}
	}
	// A change keeps the settings that the command does not give.
	if err := a.dispatch(ctx, []string{"previews", "set", "acme.example", "--limit", "3", "--copy-environment"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "PUT" || c.Body["enabled"] != true || c.Body["limit"] != float64(3) || c.Body["copyEnvironment"] != true {
		t.Errorf("set = %+v", c)
	} else if _, has := c.Body["domain"]; has {
		t.Errorf("set sent a domain: %+v", c)
	}
	if err := a.dispatch(ctx, []string{"previews", "set", "acme.example", "--off"}); err != nil || lastSite(calls).Body["enabled"] != false {
		t.Errorf("set --off = %+v, %v", lastSite(calls), err)
	}
	if err := a.dispatch(ctx, []string{"previews", "set", "acme.example", "--on", "--off"}); err == nil {
		t.Error("set accepted --on and --off")
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"previews", "create", "acme.example", "--number", "51", "--branch", "feature/menu", "--title", "New menu"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "POST" || c.Body["number"] != float64(51) || c.Body["branch"] != "feature/menu" || c.Body["title"] != "New menu" {
		t.Errorf("create = %+v", c)
	}
	if !strings.Contains(out.String(), "https://pr-51.preview.acme.example") {
		t.Errorf("create out = %s", out)
	}
	if err := a.dispatch(ctx, []string{"previews", "create", "acme.example", "--number", "51"}); err == nil {
		t.Error("create accepted no branch")
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"previews", "remove", "acme.example", "#42"}); err != nil || lastSite(calls).Path != "/websites/W1/previews/42" {
		t.Errorf("remove = %+v, %v", lastSite(calls), err)
	}
	if !strings.Contains(out.String(), "Site job: complete.") {
		t.Errorf("remove out = %s", out)
	}
	if err := a.dispatch(ctx, []string{"previews", "remove", "acme.example", "main"}); err == nil {
		t.Error("remove accepted a number that is not a number")
	}
}
