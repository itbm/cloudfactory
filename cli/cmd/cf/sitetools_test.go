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
	deploy := `{"source":{"repository":"git@github.com:acme/shop.git","branch":"main","webDirectory":"public","trigger":"manual","buildCommands":["composer install"],"deployKey":"ssh-ed25519 AAAA cloudfactory","webhookUrl":"https://panel.example/api/v1/hooks/deploy/tok","secrets":["NPM_TOKEN"]},"releases":[{"id":"R2","createdAt":"2026-11-14T10:00:00Z","state":"ready","live":true,"commit":"4f9a1c2d5e6f7a8b","message":"Fix the cart"}]}`
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
		case "GET /websites/W1/deploy", "PUT /websites/W1/deploy", "PUT /websites/W1/deploy/secrets/NPM_TOKEN", "DELETE /websites/W1/deploy/secrets/NPM_TOKEN":
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
	for _, want := range []string{"git@github.com:acme/shop.git", "ssh-ed25519 AAAA cloudfactory", "hooks/deploy/tok", "NPM_TOKEN", "4f9a1c2 ", "Fix the cart"} {
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
