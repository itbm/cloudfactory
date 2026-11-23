package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// wordpressPanel answers like the panel API for the WordPress of one website.
func wordpressPanel(t *testing.T, exitCode int) (*httptest.Server, *[]siteCall) {
	t.Helper()
	var calls []siteCall
	site := `{"wordpress":{"status":"ready","version":"6.8.1","coreMinor":"6.8.3","coreLatest":null,"siteUrl":"https://acme.example","adminUrl":"https://acme.example/wp-admin/",
		"plugins":[{"name":"akismet","title":"Akismet","status":"active","version":"5.4","update":"5.5"}],"themes":[{"name":"twentytwentyfive","title":"Twenty Twenty-Five","status":"active","version":"1.3","update":null}],
		"checkedAt":"2026-11-22T10:00:00+00:00","autoUpdates":{"core":"minor","plugins":true,"themes":false},"lastUpdate":null,
		"restorePoint":{"at":"2026-11-20T03:00:00+00:00","version":"6.8.0","database":true},"error":null},"commands":[],"busy":null}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := siteCall{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v1"), Query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &call.Body)
		}
		calls = append(calls, call)
		w.Header().Set("Content-Type", "application/json")
		job := `"job":{"id":"J1","title":"Update WordPress","state":"queued"}`
		switch key := r.Method + " " + call.Path; key {
		case "GET /websites":
			w.Write([]byte(`{"data":[{"id":"W1","name":"acme.example","status":"running"}]}`))
		case "GET /jobs/J1":
			w.Write([]byte(`{"data":{"id":"J1","title":"Update WordPress","state":"succeeded","steps":["One"],"step":1,"error":null}}`))
		case "GET /websites/W1/wordpress":
			w.Write([]byte(site))
		case "PATCH /websites/W1/wordpress":
			w.Write([]byte(site))
		case "POST /websites/W1/wordpress":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"wordpress":{"status":"installing"},"admin":{"user":"olu","password":"Secret-once-1234","loginUrl":"https://acme.example/wp-login.php"},` + job + `}`))
		case "POST /websites/W1/wordpress/check", "POST /websites/W1/wordpress/updates", "POST /websites/W1/wordpress/restore":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{` + job + `}`))
		case "POST /websites/W1/staging":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"website":{"id":"W2","name":"staging.acme.example"},"databaseCredentials":[{"engine":"mariadb","user":"staging_ab12_app","password":"Copy-secret-1234","databases":["staging_ab12_wp"]}],` +
				`"wordpress":{"database":"staging_ab12_wp","user":"staging_ab12_app"},` + job + `}`))
		case "POST /websites/W1/wordpress/cli":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"command":{"id":"C1","command":"wp plugin list","status":"running"},` + job + `}`))
		case "GET /websites/W1/wordpress/commands":
			w.Write([]byte(`{"data":[{"id":"C1","command":"wp plugin list","status":"done","exitCode":` + itoa(exitCode) + `,"output":"name\tstatus\nakismet\tactive\n","ranAt":"2026-11-22T10:00:00+00:00","user":"Olu"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestWordPressShowAndInstall(t *testing.T) {
	srv, calls := wordpressPanel(t, 0)
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"wordpress", "show", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WordPress 6.8.1 (ready)", "Update: 6.8.3", "Automatic updates: WordPress minor, plugins on, themes off", "WordPress 6.8.0 (files with no uploads, and the database)", "akismet", "5.5"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show has no %q:\n%s", want, out)
		}
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"wordpress", "install", "acme.example", "--title", "Acme", "--admin-user", "olu", "--admin-email", "olu@acme.example"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "POST" || c.Path != "/websites/W1/wordpress" || !reflect.DeepEqual(c.Body, map[string]any{"title": "Acme", "adminUser": "olu", "adminEmail": "olu@acme.example", "locale": "en_GB"}) {
		t.Errorf("install = %+v", c)
	}
	if !strings.Contains(out.String(), "Password: Secret-once-1234") || !strings.Contains(out.String(), "https://acme.example/wp-login.php") || !strings.Contains(out.String(), "complete") {
		t.Errorf("install output = %s", out)
	}
	if err := a.dispatch(ctx, []string{"wordpress", "install", "acme.example", "--title", "Acme"}); err == nil {
		t.Error("install with no administrator gave no error")
	}
}

func TestWordPressUpdates(t *testing.T) {
	srv, calls := wordpressPanel(t, 0)
	a, _ := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"wordpress", "update", "acme.example", "--core", "minor", "--plugin", "akismet", "--plugin", "hello", "--theme", "twentytwentyfive"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"core": "minor", "plugins": []any{"akismet", "hello"}, "themes": []any{"twentytwentyfive"}}
	if c := lastSite(calls); c.Path != "/websites/W1/wordpress/updates" || !reflect.DeepEqual(c.Body, want) {
		t.Errorf("update = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"wordpress", "update", "acme.example", "--all"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); !reflect.DeepEqual(c.Body, map[string]any{"all": true}) {
		t.Errorf("update all = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"wordpress", "update", "acme.example"}); err == nil {
		t.Error("an update with nothing selected gave no error")
	}
	if err := a.dispatch(ctx, []string{"wordpress", "restore", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "POST" || c.Path != "/websites/W1/wordpress/restore" {
		t.Errorf("restore = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"wordpress", "settings", "acme.example", "--core", "off", "--themes", "on"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "PATCH" || !reflect.DeepEqual(c.Body, map[string]any{"autoUpdates": map[string]any{"core": "off", "themes": true}}) {
		t.Errorf("settings = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"wordpress", "settings", "acme.example", "--plugins", "yes"}); err == nil {
		t.Error("--plugins yes gave no error")
	}
}

func TestWordPressStagingCopy(t *testing.T) {
	srv, _ := wordpressPanel(t, 0)
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"websites", "staging", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Database user staging_ab12_app (mariadb) for staging_ab12_wp: Copy-secret-1234", "WordPress in the copy uses the database copy staging_ab12_wp with the user staging_ab12_app."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	// The settings of a site also have the key "wordpress", with no database copy.
	out.Reset()
	if err := a.dispatch(context.Background(), []string{"wordpress", "settings", "acme.example", "--core", "off"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "database copy") {
		t.Errorf("settings output = %s", out)
	}
}

func TestWordPressCli(t *testing.T) {
	srv, calls := wordpressPanel(t, 0)
	a, out := testApp(srv)
	ctx := context.Background()

	// The words after "--" go to WP-CLI as they are, also the options that cf knows.
	if err := a.dispatch(ctx, []string{"wp", "cli", "acme.example", "--", "wp", "plugin", "list", "--json", "--format=table"}); err != nil {
		t.Fatal(err)
	}
	var sent siteCall
	for _, c := range *calls {
		if c.Path == "/websites/W1/wordpress/cli" {
			sent = c
		}
	}
	if !reflect.DeepEqual(sent.Body, map[string]any{"args": []any{"plugin", "list", "--json", "--format=table"}}) {
		t.Errorf("cli = %+v", sent)
	}
	if out.String() != "name\tstatus\nakismet\tactive\n" {
		t.Errorf("output = %q", out)
	}

	srv, _ = wordpressPanel(t, 1)
	a, _ = testApp(srv)
	if err := a.dispatch(ctx, []string{"wordpress", "cli", "acme.example", "--", "plugin", "list"}); err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("exit code 1 gave %v", err)
	}
	if err := a.dispatch(ctx, []string{"wordpress", "cli", "acme.example"}); err == nil {
		t.Error("cli with no command gave no error")
	}
}
