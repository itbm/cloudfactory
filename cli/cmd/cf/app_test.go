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

// securityPanel records the body of each change for the tests of the security fixes.
func securityPanel(t *testing.T) (*httptest.Server, *[]map[string]any) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method != http.MethodGet {
			bodies = append(bodies, map[string]any{"call": r.Method + " " + r.URL.Path, "body": body})
		}
		switch {
		case r.URL.Path == "/api/v1/websites":
			w.Write([]byte(`{"data":[{"id":"W1","name":"shop.example","status":"running"}]}`))
		case r.URL.Path == "/api/v1/websites/W1/databases" && r.Method == http.MethodGet:
			w.Write([]byte(`{"prefix":"u1a2_","databases":[{"id":"D1","engine":"mariadb","name":"u1a2_prod"},{"id":"D2","engine":"mariadb","name":"u1a2_staging"}],` +
				`"users":[{"id":"U1","engine":"mariadb","name":"u1a2_sync","access":"website","grants":[{"databaseId":"D1","database":"u1a2_prod","privileges":"readonly"}]}]}`))
		case r.URL.Path == "/api/v1/websites/W1/domains":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"domain":{"name":"new.example","verified":false,"verification":{"type":"TXT","name":"_cloudfactory-challenge.new.example","value":"cf-abc"}},"job":null}`))
		case r.URL.Path == "/api/v1/tokens":
			if body["password"] != "secret" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"message":"invalid","errors":{"password":["The password is not correct."]}}`))
				return
			}
			w.Write([]byte(`{"id":7,"token":"7|abc","expiresAt":"2027-01-01"}`))
		default:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"password":"pw-1","job":null}`))
		}
	}))
	return srv, &bodies
}

// CF-01: a sync user can read the production database and change the staging database.
func TestDatabaseUserGrantsUseTheDatabaseIDs(t *testing.T) {
	srv, bodies := securityPanel(t)
	defer srv.Close()
	a, out := testApp(srv)

	err := a.dispatch(context.Background(), []string{"database-users", "create", "shop.example", "--name", "deploy", "--grant", "prod=readonly", "--grant", "u1a2_staging=readwrite"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal((*bodies)[0])
	want := `{"body":{"access":"website","engine":"mariadb","grants":[{"databaseId":"D1","privileges":"readonly"},{"databaseId":"D2","privileges":"readwrite"}],"name":"deploy"},"call":"POST /api/v1/websites/W1/database-users"}`
	if string(got) != want {
		t.Errorf("request = %s", got)
	}
	if !strings.Contains(out.String(), "Password: pw-1") {
		t.Errorf("output: %s", out)
	}

	if err := a.dispatch(context.Background(), []string{"database-users", "set", "shop.example", "sync", "--grant", "staging=readwrite"}); err != nil {
		t.Fatal(err)
	}
	if (*bodies)[1]["call"] != "PATCH /api/v1/websites/W1/database-users/U1" {
		t.Errorf("call = %v", (*bodies)[1]["call"])
	}
	if err := a.dispatch(context.Background(), []string{"database-users", "create", "shop.example", "--name", "x", "--grant", "other=readonly"}); err == nil || !strings.Contains(err.Error(), `no database "other"`) {
		t.Errorf("err = %v", err)
	}
	if err := a.dispatch(context.Background(), []string{"database-users", "create", "shop.example", "--name", "x", "--grant", "prod=all"}); err == nil {
		t.Error("an unknown permission must be an error")
	}

	out.Reset()
	if err := a.dispatch(context.Background(), []string{"database-users", "list", "shop.example"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "u1a2_prod=readonly") {
		t.Errorf("output: %s", out)
	}
}

// CF-03: a new domain shows the TXT record to add.
func TestANewDomainShowsTheTXTRecord(t *testing.T) {
	srv, bodies := securityPanel(t)
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"domains", "add", "shop.example", "new.example", "--kind", "alias"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `_cloudfactory-challenge.new.example TXT "cf-abc"`) {
		t.Errorf("output: %s", out)
	}
	if err := a.dispatch(context.Background(), []string{"domains", "verify", "shop.example", "new.example"}); err != nil {
		t.Fatal(err)
	}
	if (*bodies)[1]["call"] != "POST /api/v1/websites/W1/domains/new.example/verify" {
		t.Errorf("call = %v", (*bodies)[1]["call"])
	}
}

// CF-07: a new token needs the password.
func TestANewTokenSendsThePassword(t *testing.T) {
	srv, _ := securityPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	t.Setenv("CF_PASSWORD", "")

	a.in = strings.NewReader("wrong\n")
	if err := a.dispatch(context.Background(), []string{"tokens", "create", "--name", "ci", "--scope", "websites:read"}); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("err = %v", err)
	}
	a.in = strings.NewReader("secret\n")
	if err := a.dispatch(context.Background(), []string{"tokens", "create", "--name", "ci", "--scope", "websites:read"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Token: 7|abc") {
		t.Errorf("output: %s", out)
	}
	a.in = strings.NewReader("")
	if err := a.dispatch(context.Background(), []string{"tokens", "create", "--name", "ci", "--scope", "websites:read"}); err == nil {
		t.Error("an empty password must be an error")
	}
}
