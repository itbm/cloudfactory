package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pluginPanel answers like the panel API for the plugins. It records each change as
// "METHOD path body".
func pluginPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const uptime = `{"id":"P1","slug":"uptime","name":"Uptime monitor","version":"1.4.0","publisher":"Example Ltd","enabled":true,"baseUrl":"https://uptime.example.net","webhookUrl":"https://uptime.example.net/hooks","scopes":["audit:read","websites:read"],"events":["job.finished"],"pages":[{"title":"Uptime","roles":["owner"],"url":"https://uptime.example.net/panel"}],"actions":[{"title":"Check now","target":"website","roles":["owner"]}],"hooks":[{"event":"website.removing","onError":"allow"}],"commands":[{"name":"uptime","description":"Shows the uptime."}],"signedBy":{"name":"Example Ltd"},"tokenExpiresAt":"2027-11-15T00:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		switch path := strings.TrimPrefix(r.URL.Path, "/api/v1"); {
		case path == "/plugins" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + uptime + `]}`))
		case path == "/plugins" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"plugin":` + uptime + `,"token":"7|plugintoken","webhookSecret":"whsec_abc"}`))
		case path == "/plugins/P1" && r.Method == http.MethodPatch:
			w.Write([]byte(strings.Replace(uptime, `"enabled":true`, `"enabled":false`, 1)))
		case path == "/plugins/P1/test":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"id":"D1","state":"pending"}`))
		case path == "/plugin-keys" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"K1","name":"Example Ltd","fingerprint":"ab12"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestPluginsInstallAndChange(t *testing.T) {
	srv, calls := pluginPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	t.Setenv("CF_PASSWORD", "secret")
	dir := t.TempDir()
	manifest := filepath.Join(dir, "plugin.json")
	os.WriteFile(manifest, []byte(`{"schema":1}`), 0o600)
	os.WriteFile(manifest+".sig", []byte("c2lnbmF0dXJl\n"), 0o600)

	if err := a.dispatch(ctx, []string{"plugins", "install", "--manifest", manifest, "--address", "https://uptime.example.net", "--totp", "123456"}); err != nil {
		t.Fatal(err)
	}
	want := `POST /api/v1/plugins {"baseUrl":"https://uptime.example.net","manifest":"{\"schema\":1}","password":"secret","signature":"c2lnbmF0dXJl","totp":"123456"}`
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	if !strings.Contains(out.String(), "CF_TOKEN=7|plugintoken") || !strings.Contains(out.String(), "CF_WEBHOOK_SECRET=whsec_abc") {
		t.Errorf("output:\n%s", out)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"plugins", "get", "Uptime"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Uptime monitor 1.4.0 (uptime)", "signed by Example Ltd", "Scopes:        audit:read,websites:read", "Command:       cf uptime", "Action:        Check now (on each website, for owner)", "Before hook:   website.removing (when the plugin does not answer: allow)"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("no %q in:\n%s", s, out)
		}
	}
	if err := a.dispatch(ctx, []string{"plugins", "disable", "uptime"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, []string{"plugins", "test", "P1"}); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1] != `PATCH /api/v1/plugins/P1 {"enabled":false}` || (*calls)[2] != "POST /api/v1/plugins/P1/test" {
		t.Errorf("calls: %v", *calls)
	}
	if err := a.dispatch(ctx, []string{"plugins", "get", "other"}); err == nil || !strings.Contains(err.Error(), "there is no plugin other") {
		t.Errorf("error: %v", err)
	}
}

func TestPluginsInstallNeedsAnAddress(t *testing.T) {
	srv, calls := pluginPanel(t)
	defer srv.Close()
	a, _ := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"plugins", "install", "--manifest", "x.json"}); err == nil || !strings.Contains(err.Error(), "--address") {
		t.Errorf("error: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("calls: %v", *calls)
	}
}

// "cf NAME" runs cf-NAME from the PATH, with the sign-in in CF_PANEL and CF_TOKEN. A plugin
// cannot replace a command of cf.
func TestExternalCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"args=$* panel=$CF_PANEL token=$CF_TOKEN\"\n"
	os.WriteFile(filepath.Join(dir, "cf-uptime"), []byte(script), 0o755)
	os.WriteFile(filepath.Join(dir, "cf-websites"), []byte("#!/bin/sh\necho replaced\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CF_PANEL", "https://panel.example.com")
	t.Setenv("CF_TOKEN", "cft_test")

	srv, _ := fakePanel(t, false)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"uptime", "--all", "shop"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "args=--all shop panel=https://panel.example.com token=cft_test" {
		t.Errorf("output: %q", got)
	}
	out.Reset()
	if err := a.dispatch(context.Background(), []string{"websites", "list"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "replaced") {
		t.Errorf("a plugin replaced a command of cf:\n%s", out)
	}
	if err := a.dispatch(context.Background(), []string{"nothing-here", "x"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error: %v", err)
	}
}
