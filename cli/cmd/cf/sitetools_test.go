package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			// Two pages, the newest entry first. Only the first page has a summary.
			if r.URL.Query().Get("cursor") == "c2" {
				w.Write([]byte(`{"entries":[{"at":"2026-11-14T10:00:00Z","source":"error","text":"PHP Warning","access":null}],"next":null,"summary":null}`))
				return
			}
			summary := "null"
			if r.URL.Query().Get("summary") == "1" {
				summary = `{"total":3,"sources":{"access":2,"output":0,"error":1},"statuses":{"1xx":0,"2xx":1,"3xx":0,"4xx":1,"5xx":0},"topPaths":[{"value":"/wp-login.php","count":2}],"topClients":[{"value":"203.0.113.7","count":2}],"sizeBytes":5120,"oldest":"2026-11-13T08:00:00Z"}`
			}
			w.Write([]byte(`{"entries":[` +
				`{"at":"2026-11-14T10:00:02Z","source":"access","text":"","access":{"client":"203.0.113.7","method":"POST","host":"acme.example","uri":"/wp-login.php","proto":"HTTP/2.0","status":404,"sizeBytes":548,"durationMs":1.2,"userAgent":"zgrab","referer":""}},` +
				`{"at":"2026-11-14T10:00:01Z","source":"access","text":"","access":{"client":"203.0.113.7","method":"GET","host":"acme.example","uri":"/","proto":"HTTP/2.0","status":200,"sizeBytes":4572,"durationMs":12.5,"userAgent":"curl/8.11.1","referer":""}}` +
				`],"next":"c2","summary":` + summary + `}`))
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
	ctx := context.Background()
	args := []string{"logs", "acme.example", "--search", "wp-login -bot", "--status", "4xx,200", "--method", "POST", "--client", "203.0.113.0/24",
		"--since", "2026-11-14T09:00:00+01:00", "--until", "2026-11-14T10:30:00Z", "--source", "access,error", "--limit", "3", "--summary"}
	if err := a.dispatch(ctx, args); err != nil {
		t.Fatal(err)
	}
	var logCalls []siteCall
	for _, c := range *calls {
		if c.Path == "/websites/W1/logs" {
			logCalls = append(logCalls, c)
		}
	}
	first := "client=203.0.113.0%2F24&from=2026-11-14T08%3A00%3A00Z&limit=3&method=POST&q=wp-login+-bot&sources=access%2Cerror&status=4xx%2C200&summary=1&to=2026-11-14T10%3A30%3A00Z"
	second := "client=203.0.113.0%2F24&cursor=c2&from=2026-11-14T08%3A00%3A00Z&limit=1&method=POST&q=wp-login+-bot&sources=access%2Cerror&status=4xx%2C200&to=2026-11-14T10%3A30%3A00Z"
	if len(logCalls) != 2 || logCalls[0].Query != first || logCalls[1].Query != second {
		t.Fatalf("calls = %+v", logCalls)
	}
	// The summary comes first, then the oldest entry first.
	want := []string{
		"Requests: 2 (2xx 1, 3xx 0, 4xx 1, 5xx 0), 5.0KB sent",
		"Top paths: /wp-login.php (2)",
		"The oldest entry is from 2026-11-13T08:00:00Z.",
		"2026-11-14T10:00:00Z error  PHP Warning",
		`2026-11-14T10:00:01Z access 200 GET acme.example/ 203.0.113.7 4.5KB 12.5ms "" "curl/8.11.1"`,
		`2026-11-14T10:00:02Z access 404 POST acme.example/wp-login.php 203.0.113.7 548B 1.2ms "" "zgrab"`,
	}
	text := out.String()
	last := -1
	for _, w := range want {
		i := strings.Index(text, w)
		if i <= last {
			t.Fatalf("the output does not have %q after the earlier lines:\n%s", w, text)
		}
		last = i
	}
	if strings.Contains(text, "There are older entries") {
		t.Errorf("the search has no older entries:\n%s", text)
	}

	// With a smaller limit, the command reads one page and says that there are older entries.
	out.Reset()
	if err := a.dispatch(ctx, []string{"logs", "acme.example", "--since", "2h", "--limit", "2"}); err != nil {
		t.Fatal(err)
	}
	q := lastSite(calls).Query
	if !strings.Contains(q, "from=") || strings.Contains(q, "summary") || !strings.Contains(out.String(), "There are older entries. Use --limit with a number larger than 2, or --until 2026-11-14T10:00:01Z.") {
		t.Errorf("query = %s, out = %s", q, out)
	}

	for _, bad := range [][]string{{"--limit", "0"}, {"--limit", "10001"}, {"--since", "yesterday"}, {"--since", "-2h"}, {"--until", "0d"}, {"wp-login"}} {
		if err := a.dispatch(ctx, append([]string{"logs", "acme.example"}, bad...)); err == nil {
			t.Errorf("%v gave no error", bad)
		}
	}
}

func TestLogTime(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"15m":                  now.Add(-15 * time.Minute),
		"2h":                   now.Add(-2 * time.Hour),
		"7d":                   now.AddDate(0, 0, -7),
		"2026-11-14T09:00:00Z": now.Add(-time.Hour),
	} {
		if got, err := logTime(in, now); err != nil || !got.Equal(want) {
			t.Errorf("logTime(%q) = %v, %v; want %v", in, got, err, want)
		}
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

func TestServerAccessLog(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/api/v1/servers":
			w.Write([]byte(`{"data":[{"id":"S1","name":"web-1"}],"meta":{"last_page":1,"total":1}}`))
		case "/api/v1/servers/S1/access-log":
			w.Write([]byte(`{"entries":[{"at":"2026-11-14T10:00:00Z","source":"access","text":"","access":{"client":"192.0.2.1","method":"GET","host":"panel.example","uri":"/","proto":"HTTP/2.0","status":200,"sizeBytes":812,"durationMs":4.1,"userAgent":"","referer":""}}],"next":null,"summary":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"servers", "access-log", "web-1", "--status", "2xx"}); err != nil {
		t.Fatal(err)
	}
	if paths[len(paths)-1] != "/api/v1/servers/S1/access-log?limit=100&status=2xx" || !strings.Contains(out.String(), "access 200 GET panel.example/ 192.0.2.1 812B 4.1ms") {
		t.Errorf("paths = %v, out = %s", paths, out)
	}
	// The access log of a server has no sources.
	if err := a.dispatch(context.Background(), []string{"servers", "access-log", "web-1", "--source", "output"}); err == nil {
		t.Error("--source gave no error")
	}
}
