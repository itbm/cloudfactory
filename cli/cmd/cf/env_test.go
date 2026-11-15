package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// envPanel answers for one Node.js website, and keeps the bodies of the changes.
func envPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/websites" {
			w.Write([]byte(`{"data":[{"id":"W1","name":"app.example","status":"running","command":"npm start","installCommand":"npm ci","environment":["DATABASE_URL","NODE_ENV"],"runtime":{"version":"24"}}]}`))
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"job":null}`))
	}))
	return srv, &calls
}

func TestEnvListShowsTheNamesOnly(t *testing.T) {
	srv, _ := envPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"env", "list", "app.example"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "DATABASE_URL\nNODE_ENV\n" {
		t.Errorf("output: %q", out)
	}
}

func TestEnvSetReadsTheValueFromStdinAndUnsetSendsNull(t *testing.T) {
	srv, calls := envPanel(t)
	defer srv.Close()
	a, _ := testApp(srv)
	a.in = strings.NewReader("postgres://app:s3cret@db/app\n")
	if err := a.dispatch(context.Background(), []string{"env", "set", "app.example", "DATABASE_URL"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), []string{"env", "unset", "app.example", "NODE_ENV", "DEBUG"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %v", *calls)
	}
	var set, unset struct {
		Variables map[string]*string `json:"variables"`
	}
	json.Unmarshal([]byte(strings.TrimPrefix((*calls)[0], "PATCH /api/v1/websites/W1/environment ")), &set)
	json.Unmarshal([]byte(strings.TrimPrefix((*calls)[1], "PATCH /api/v1/websites/W1/environment ")), &unset)
	if v := set.Variables["DATABASE_URL"]; v == nil || *v != "postgres://app:s3cret@db/app" {
		t.Errorf("set: %v", (*calls)[0])
	}
	if v, ok := unset.Variables["NODE_ENV"]; !ok || v != nil || len(unset.Variables) != 2 {
		t.Errorf("unset: %v", (*calls)[1])
	}
}

func TestRuntimeInstallAndCommands(t *testing.T) {
	srv, calls := envPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	for _, args := range [][]string{
		{"runtime", "install", "app.example"},
		{"runtime", "set", "app.example", "--command", "node server.js", "--install", ""},
		{"websites", "get", "app.example"},
	} {
		if err := a.dispatch(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	if (*calls)[0] != "POST /api/v1/websites/W1/runtime/install " {
		t.Errorf("install: %q", (*calls)[0])
	}
	if (*calls)[1] != `PUT /api/v1/websites/W1/runtime {"command":"node server.js","installCommand":""}` {
		t.Errorf("set: %q", (*calls)[1])
	}
	for _, want := range []string{"Version:  24\n", "Install:  npm ci\n", "Env:      DATABASE_URL, NODE_ENV\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}
