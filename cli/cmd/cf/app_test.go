package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// CF-02: a new token needs the password.
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

// pagedPanel gives 250 websites on 3 pages and 120 jobs, like the Laravel paginator.
func pagedPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		per, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
		if per == 0 {
			per = 25
		}
		paged := func(total int, row func(i int) string) {
			var rows []string
			for i := (page - 1) * per; i < page*per && i < total; i++ {
				rows = append(rows, row(i))
			}
			next := "null"
			if page*per < total {
				next = `"http://x/next"`
			}
			fmt.Fprintf(w, `{"data":[%s],"links":{"next":%s},"meta":{"total":%d,"last_page":%d}}`, strings.Join(rows, ","), next, total, (total+per-1)/per)
		}
		switch {
		case r.URL.Path == "/api/v1/websites" && r.Method == http.MethodGet:
			paged(250, func(i int) string {
				return fmt.Sprintf(`{"id":"W%d","name":"site%d.example","status":"running","sftp":{"enabled":false,"port":22,"username":"u%d","authorizedKeys":[]}}`, i, i, i)
			})
		case r.URL.Path == "/api/v1/websites/W240/sftp":
			b, _ := io.ReadAll(r.Body)
			calls = append(calls, string(b))
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":null}`))
		case r.URL.Path == "/api/v1/jobs":
			paged(120, func(i int) string { return fmt.Sprintf(`{"id":"J%d","title":"Job %d","state":"succeeded"}`, i, i) })
		case r.URL.Path == "/api/v1/backup-policy/repository-password":
			w.Write([]byte(`{"password":"repo-pass"}`))
		case r.URL.Path == "/api/v1/backup-policy" && r.Method == http.MethodGet:
			w.Write([]byte(`{"daily":7,"weekly":4,"monthly":6,"time":"03:00","target":"s3:old/path","credentialNames":[]}`))
		case r.URL.Path == "/api/v1/backup-policy" && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			calls = append(calls, string(b))
			w.Write(b)
		case r.URL.Path == "/api/v1/servers":
			w.Write([]byte(`{"data":[{"id":"S1","name":"web-1","pendingUpdates":[{"name":"podman","from":"5.6.0","to":"5.6.1","containerTool":true}]}]}`))
		case r.URL.Path == "/api/v1/servers/S1/install-updates":
			b, _ := io.ReadAll(r.Body)
			calls = append(calls, string(b))
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestListsReadEachPage(t *testing.T) {
	srv, _ := pagedPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"websites", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "site249.example") || strings.Contains(out.String(), "Showing") {
		t.Errorf("websites list does not show all pages:\n%s", out.String()[len(out.String())-200:])
	}
	out.Reset()
	if err := a.dispatch(context.Background(), []string{"jobs", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Showing 50 of 120. Add --all to see all.") {
		t.Errorf("jobs list does not say that rows are missing:\n%s", out)
	}
	out.Reset()
	if err := a.dispatch(context.Background(), []string{"jobs", "list", "--all"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Job 119") || strings.Contains(out.String(), "Showing") {
		t.Errorf("jobs list --all:\n%s", out)
	}
}

func TestFindWebsiteSearchesEachPage(t *testing.T) {
	srv, calls := pagedPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	keyFile := filepath.Join(t.TempDir(), "id.pub")
	os.WriteFile(keyFile, []byte("# my key\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA me@laptop\n"), 0o600)
	// site240.example is on page 3.
	if err := a.dispatch(context.Background(), []string{"sftp", "set", "site240.example", "--on", "--key-file", keyFile}); err != nil {
		t.Fatal(err)
	}
	body := (*calls)[len(*calls)-1]
	if strings.Contains(body, "assword") {
		t.Errorf("SFTP accepts keys only, but the body asks for a password: %s", body)
	}
	for _, want := range []string{`"enabled":true`, `"authorizedKeys":["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIA me@laptop"]`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s does not have %s", body, want)
		}
	}
	// The flags for a password are gone.
	if err := a.dispatch(context.Background(), []string{"sftp", "set", "site240.example", "--new-password"}); err == nil {
		t.Error("--new-password is accepted")
	}
	_ = out
}

func TestBackupCredentialsComeFromTheEnvironment(t *testing.T) {
	srv, calls := pagedPanel(t)
	defer srv.Close()
	a, _ := testApp(srv)
	for _, k := range credentialNames {
		t.Setenv(k, "")
	}
	if err := a.dispatch(context.Background(), []string{"backups", "policy", "set", "--credentials-from-env"}); err == nil {
		t.Error("no credentials in the environment must be an error")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-value")
	if err := a.dispatch(context.Background(), []string{"backups", "policy", "set", "--target", "s3:s3.amazonaws.com/b/p", "--daily", "14", "--credentials-from-env"}); err != nil {
		t.Fatal(err)
	}
	body := (*calls)[len(*calls)-1]
	for _, want := range []string{`"AWS_SECRET_ACCESS_KEY":"secret-value"`, `"daily":14`, `"target":"s3:s3.amazonaws.com/b/p"`, `"weekly":4`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s does not have %s", body, want)
		}
	}
}

func TestBackupRepositoryPassword(t *testing.T) {
	srv, _ := pagedPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"backups", "policy", "password"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "repo-pass" {
		t.Errorf("output %q, want the password", got)
	}
}

func TestServerUpdateWithTheToolsPolicy(t *testing.T) {
	srv, calls := pagedPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"servers", "get", "web-1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--policy tools") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(context.Background(), []string{"servers", "update", "web-1", "--policy", "tools"}); err != nil {
		t.Fatal(err)
	}
	if body := (*calls)[len(*calls)-1]; body != `{"policy":"tools"}` {
		t.Errorf("body = %s", body)
	}
}
