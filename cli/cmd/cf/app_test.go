package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// fakePanel answers like the panel API for one website and one job.
func fakePanel(t *testing.T, failJob bool) (*httptest.Server, *[]string) {
	var calls []string
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cft_test" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Unauthenticated."}`))
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/websites" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"W1","name":"acme.example","status":"running","application":"WordPress","updateReady":true},{"id":"W2","name":"shop.acme.example","status":"stopped"}]}`))
		case r.URL.Path == "/api/v1/websites" && r.Method == http.MethodPost:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] == "bad" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"message":"invalid","errors":{"name":["The name field format is invalid."]}}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"website":{"id":"W3"},"job":{"id":"J1","title":"Create website new.example","state":"queued"}}`))
		case r.URL.Path == "/api/v1/websites/W1/power":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":{"id":"J1","title":"Restart acme.example","state":"queued"}}`))
		case r.URL.Path == "/api/v1/jobs/J1":
			n := polls.Add(1)
			state, errMsg := "running", "null"
			if n >= 3 {
				state = "succeeded"
				if failJob {
					state, errMsg = "failed", `"Health check: status 502"`
				}
			}
			w.Write([]byte(`{"data":{"id":"J1","title":"Restart acme.example","state":"` + state + `","steps":["Change container state","Health check"],"step":` + map[bool]string{true: "2", false: "1"}[state == "succeeded"] + `,"error":` + errMsg + `}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func testApp(srv *httptest.Server) (*app, *bytes.Buffer) {
	var out bytes.Buffer
	return &app{out: &out, err: &out, api: client.New(srv.URL, "cft_test"), poll: time.Millisecond}, &out
}

func TestWebsitesList(t *testing.T) {
	srv, _ := fakePanel(t, false)
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"websites", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acme.example") || !strings.Contains(out.String(), "NAME") || !strings.Contains(out.String(), "yes") {
		t.Errorf("output:\n%s", out)
	}
}

func TestRestartWaitsForTheJob(t *testing.T) {
	srv, calls := fakePanel(t, false)
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"websites", "restart", "acme.example"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[2/2] Health check") || !strings.Contains(out.String(), "Restart acme.example: complete.") {
		t.Errorf("output:\n%s", out)
	}
	if (*calls)[1] != "POST /api/v1/websites/W1/power" {
		t.Errorf("calls = %v", *calls)
	}
}

func TestAFailedJobIsAnError(t *testing.T) {
	srv, _ := fakePanel(t, true)
	defer srv.Close()
	a, _ := testApp(srv)

	err := a.dispatch(context.Background(), []string{"websites", "restart", "acme.example"})
	if err == nil || err.Error() != "Restart acme.example: Health check: status 502" {
		t.Errorf("err = %v", err)
	}
}

func TestValidationErrorsShowTheField(t *testing.T) {
	srv, _ := fakePanel(t, false)
	defer srv.Close()
	a, _ := testApp(srv)

	err := a.dispatch(context.Background(), []string{"websites", "create", "bad", "--image", "I", "--plan", "P"})
	if err == nil || err.Error() != "name: The name field format is invalid." {
		t.Errorf("err = %v", err)
	}
}

func TestNoWaitAndJSON(t *testing.T) {
	srv, calls := fakePanel(t, false)
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"websites", "create", "new.example", "--image", "I", "--plan", "P", "--no-wait"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Job J1 is in the queue") || len(*calls) != 1 {
		t.Errorf("output %s calls %v", out, *calls)
	}
	out.Reset()
	a.noWait = false
	if err := a.dispatch(context.Background(), []string{"websites", "get", "shop.acme.example", "--json"}); err != nil {
		t.Fatal(err)
	}
	var w map[string]any
	if json.Unmarshal(out.Bytes(), &w) != nil || w["id"] != "W2" {
		t.Errorf("json output: %s", out)
	}
}
