package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// uptimePanel answers like the panel API for the uptime checks of one website.
func uptimePanel(t *testing.T) (*httptest.Server, *[]siteCall) {
	t.Helper()
	var calls []siteCall
	check := `{"id":"U1","websiteId":"W1","websiteName":"acme.example","domain":"acme.example","scheme":"https","path":"/","url":"https://acme.example/","intervalSeconds":60,"timeoutSeconds":10,"expectedStatus":null,"keyword":"Welcome","failuresBeforeAlert":2,"enabled":true,"state":"down","pausedReason":null,"since":"2026-11-14T09:35:00+00:00","failures":3,"lastCheckedAt":"2026-11-14T10:00:00+00:00","lastResult":{"ok":false,"status":503,"responseMs":120,"error":"HTTP status 503."},"nextCheckAt":"2026-11-14T10:01:00+00:00","uptime":{"day":98.5,"week":99.78,"month":99.95},"incident":null,"createdAt":"2026-11-01T00:00:00+00:00"}`
	incident := `{"id":"I1","startedAt":"2026-11-14T09:35:00+00:00","resolvedAt":null,"durationSeconds":1500,"error":"HTTP status 503.","endedByPause":false`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := siteCall{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v1"), Query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &call.Body)
		}
		calls = append(calls, call)
		w.Header().Set("Content-Type", "application/json")
		switch key := r.Method + " " + call.Path; key {
		case "GET /websites":
			w.Write([]byte(`{"data":[{"id":"W1","name":"acme.example","status":"running","domains":[{"name":"www.acme.example","kind":"redirect"},{"name":"acme.example","kind":"primary"}]}]}`))
		case "GET /uptime-checks":
			w.Write([]byte(`{"data":[` + check + `],"meta":{"current_page":1,"last_page":1,"per_page":100,"total":1},"summary":{"total":1,"up":0,"down":1,"pending":0,"paused":0}}`))
		case "GET /uptime-incidents":
			w.Write([]byte(`{"data":[` + incident + `,"checkId":"U1","websiteId":"W1","websiteName":"acme.example","url":"https://acme.example/"}]}`))
		case "GET /websites/W1/uptime-checks/U1":
			w.Write([]byte(strings.TrimSuffix(check, "}") + `,"history":[],"days":[],"incidents":[` + incident + `}]}`))
		case "POST /websites/W1/uptime-checks":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"uptimeCheck":` + check + `}`))
		case "PATCH /websites/W1/uptime-checks/U1":
			w.Write([]byte(`{"uptimeCheck":` + check + `}`))
		case "POST /websites/W1/uptime-checks/U1/run":
			w.Write([]byte(`{"uptimeCheck":` + check + `,"result":{"ok":false,"status":503,"responseMs":120,"error":"HTTP status 503."}}`))
		case "DELETE /websites/W1/uptime-checks/U1":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestUptimeChecks(t *testing.T) {
	srv, calls := uptimePanel(t)
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"uptime-checks", "list", "acme.example", "--state", "down"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Path != "/uptime-checks" || !strings.Contains(c.Query, "state=down") || !strings.Contains(c.Query, "websiteId=W1") {
		t.Errorf("list = %+v", c)
	}
	for _, s := range []string{"https://acme.example/", "down", "HTTP status 503.", "98.5%", "99.95%"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("list does not show %q:\n%s", s, out)
		}
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"uptime-checks", "get", "acme.example", "U1"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"State:       down since 2026-11-14T09:35:00+00:00", `Expects:     status 200 to 399 and the text "Welcome"`, "email after 2 failed requests", "98.5% in 24 hours", "down for 25m"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("get does not show %q:\n%s", s, out)
		}
	}

	// The default address is the primary domain of the website.
	if err := a.dispatch(ctx, []string{"uptime-checks", "add", "acme.example", "--path", "/health", "--interval", "60", "--text", "ok", "--off"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"domain": "acme.example", "path": "/health", "intervalSeconds": float64(60), "keyword": "ok", "enabled": false}
	if c := lastSite(calls); c.Method != "POST" || !equalBody(c.Body, want) {
		t.Errorf("add = %+v", c)
	}

	if err := a.dispatch(ctx, []string{"uptime-checks", "set", "acme.example", "U1", "--https", "--any-status", "--no-text", "--on", "--failures", "3"}); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"scheme": "https", "expectedStatus": nil, "keyword": nil, "enabled": true, "failuresBeforeAlert": float64(3)}
	if c := lastSite(calls); c.Method != "PATCH" || !equalBody(c.Body, want) {
		t.Errorf("set = %+v", c)
	}
	if err := a.dispatch(ctx, []string{"uptime-checks", "set", "acme.example", "U1", "--on", "--off"}); err == nil {
		t.Error("set accepted --on and --off")
	}
	if err := a.dispatch(ctx, []string{"uptime-checks", "set", "acme.example", "U1"}); err == nil {
		t.Error("set accepted no change")
	}

	// A failed request gives the exit code 1, for a script.
	out.Reset()
	err := a.dispatch(ctx, []string{"uptime-checks", "run", "acme.example", "U1"})
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 1 || !strings.Contains(out.String(), "the request failed: HTTP status 503.") {
		t.Errorf("run = %v, out = %s", err, out)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"uptime-checks", "incidents", "--open"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Query != "state=open" || !strings.Contains(out.String(), "down for 25m") || !strings.Contains(out.String(), "acme.example") {
		t.Errorf("incidents = %+v, out = %s", c, out)
	}

	if err := a.dispatch(ctx, []string{"uptime-checks", "remove", "acme.example", "U1"}); err != nil {
		t.Fatal(err)
	}
	if c := lastSite(calls); c.Method != "DELETE" || c.Path != "/websites/W1/uptime-checks/U1" {
		t.Errorf("remove = %+v", c)
	}
	if !builtin["uptime-checks"] {
		t.Error("a plugin can replace cf uptime-checks")
	}
}

func equalBody(got, want map[string]any) bool {
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	return string(a) == string(b)
}

func TestLasting(t *testing.T) {
	for seconds, want := range map[int]string{30: "<1m", 240: "4m", 3900: "1h5m", 90061: "1d1h"} {
		if got := lasting(seconds); got != want {
			t.Errorf("lasting(%d) = %q, want %q", seconds, got, want)
		}
	}
}
