package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// databaseServerPanel answers like the panel API for the database servers of two customers.
// It records each change as "METHOD path body", and each list of database servers with its
// query. With admin false, the panel refuses the list of servers.
func databaseServerPanel(t *testing.T, admin bool) (*httptest.Server, *[]string) {
	var calls []string
	const reports = `{"id":"DS2","name":"mariadb-reports","engine":"mariadb","customerId":"C1","serverId":"S1","planId":"P1","status":"failed","host":"mariadb.internal","port":3307,` +
		`"limits":{"vcpus":1,"memoryBytes":536870912,"diskBytes":5368709120},"usage":null,"databases":0,"maxDatabases":5}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		switch {
		case r.URL.Path == "/api/v1/database-servers" && r.Method == http.MethodGet:
			q := r.URL.Query()
			q.Del("page")
			q.Del("perPage")
			if len(q) > 0 {
				calls = append(calls, "GET "+r.URL.Path+"?"+q.Encode())
			}
			w.Write([]byte(`{"data":[{"id":"DS1","name":"mariadb-1","engine":"mariadb","customerId":"C1","serverId":"S1","planId":"P2","status":"running","host":"mariadb.internal","port":3306,` +
				`"limits":{"vcpus":2,"memoryBytes":1073741824,"diskBytes":10737418240},"usage":{"memoryBytes":536870912,"diskBytes":1610612736,"diskSizeBytes":10737418240},"databases":3,"maxDatabases":10},` +
				reports + `,` +
				`{"id":"DS3","name":"mariadb-1","engine":"mariadb","customerId":"C2","serverId":"S1","planId":"P1","status":"running","host":"mariadb.internal","port":3306,` +
				`"limits":{"vcpus":1,"memoryBytes":536870912,"diskBytes":5368709120},"usage":null,"databases":1,"maxDatabases":5}]}`))
		case r.URL.Path == "/api/v1/servers":
			if !admin {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"message":"This action is unauthorized."}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"S1","name":"web-1"}]}`))
		case r.URL.Path == "/api/v1/plans":
			w.Write([]byte(`{"data":[{"id":"P1","name":"Standard","websites":5,"mailboxes":10,"databaseServers":3,"databases":5,"databaseLimits":{"vcpus":1,"memoryBytes":536870912,"diskBytes":5368709120}},` +
				`{"id":"P2","name":"Business","websites":20,"mailboxes":50,"databaseServers":6,"databases":10,"databaseLimits":{"vcpus":2,"memoryBytes":1073741824,"diskBytes":10737418240}}]}`))
		case r.URL.Path == "/api/v1/database-servers" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"databaseServer":{"id":"DS9","name":"valkey-sessions","engine":"valkey","host":"valkey.internal","port":6380},"job":null}`))
		case r.URL.Path == "/api/v1/database-servers/DS2" && r.Method == http.MethodPatch:
			w.Write([]byte(`{"databaseServer":` + reports + `,"job":null}`))
		case r.URL.Path == "/api/v1/database-servers/DS2/retry":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"databaseServer":` + reports + `,"job":null}`))
		case r.URL.Path == "/api/v1/database-servers/DS2" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":null}`))
		case r.URL.Path == "/api/v1/database-servers/DS1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"invalid","errors":{"databaseServer":["This database server has 3 databases. Remove the databases first."]}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

// row gives a line of a table with one space between the cells.
func row(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

func TestDatabaseServersListAndGet(t *testing.T) {
	srv, calls := databaseServerPanel(t, true)
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"database-servers", "list"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || row(lines[0]) != "NAME ENGINE STATUS ADDRESS LIMITS DATABASES CUSTOMERID ID" ||
		row(lines[1]) != "mariadb-1 mariadb running mariadb.internal:3306 2 vCPU, 1 GB memory, 10 GB disk 3 of 10 C1 DS1" ||
		row(lines[2]) != "mariadb-reports mariadb failed mariadb.internal:3307 1 vCPU, 512 MB memory, 5 GB disk 0 of 5 C1 DS2" {
		t.Errorf("output:\n%s", out)
	}

	// The name of a server becomes its ID in the query.
	if err := a.dispatch(context.Background(), []string{"database-servers", "list", "--customer", "C1", "--server", "web-1", "--engine", "mariadb"}); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[len(*calls)-1]; got != "GET /api/v1/database-servers?customerId=C1&engine=mariadb&serverId=S1" {
		t.Errorf("call = %s", got)
	}
	if err := a.dispatch(context.Background(), []string{"database-servers", "list", "--server", "web-9"}); err == nil || err.Error() != "there is no server web-9" {
		t.Errorf("err = %v", err)
	}

	out.Reset()
	if err := a.dispatch(context.Background(), []string{"database-servers", "get", "DS1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Name:       mariadb-1", "Customer:   C1", "Plan:       P2", "Address:    mariadb.internal:3306", "Limits:     2 vCPU, 1 GB memory, 10 GB disk", "Databases:  3 of 10", "Use:        512 MB memory, 1.5 GB of 10 GB disk"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not have %q:\n%s", want, out)
		}
	}
	// A database server that did not start has no report.
	out.Reset()
	if err := a.dispatch(context.Background(), []string{"database-servers", "get", "mariadb-reports"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ID:         DS2") || strings.Contains(out.String(), "Use:") {
		t.Errorf("output:\n%s", out)
	}

	// Two customers can have a database server with the same name.
	if err := a.dispatch(context.Background(), []string{"database-servers", "get", "mariadb-1"}); err == nil || !strings.Contains(err.Error(), "more than one customer has a database server with the name mariadb-1") {
		t.Errorf("err = %v", err)
	}
	if err := a.dispatch(context.Background(), []string{"database-servers", "get", "valkey-1"}); err == nil || err.Error() != "there is no database server valkey-1" {
		t.Errorf("err = %v", err)
	}
	if err := a.dispatch(context.Background(), []string{"database-servers", "get"}); err == nil {
		t.Error("a command with no database server must be an error")
	}
	if err := a.dispatch(context.Background(), []string{"database-servers", "start", "DS1"}); err == nil || err.Error() != "unknown command database-servers start" {
		t.Errorf("err = %v", err)
	}
}

func TestDatabaseServersCreate(t *testing.T) {
	srv, calls := databaseServerPanel(t, true)
	defer srv.Close()
	a, out := testApp(srv)
	last := func() string { return (*calls)[len(*calls)-1] }

	if err := a.dispatch(context.Background(), []string{"database-servers", "create", "--engine", "valkey", "--name", "valkey-sessions", "--customer", "C1", "--plan", "P2", "--server", "web-1"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != `POST /api/v1/database-servers {"customerId":"C1","engine":"valkey","name":"valkey-sessions","planId":"P2","serverId":"S1"}` {
		t.Errorf("call = %s", got)
	}
	if want := "Database server valkey-sessions: the websites of the customer on the same server connect to valkey.internal:6380"; !strings.Contains(out.String(), want) {
		t.Errorf("output does not have %q:\n%s", want, out)
	}

	// The panel gives the name, the customer, the plan and the server.
	if err := a.dispatch(context.Background(), []string{"database-servers", "create", "--engine", "mariadb"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != `POST /api/v1/database-servers {"engine":"mariadb"}` {
		t.Errorf("call = %s", got)
	}

	// The API needs the three limits together.
	if err := a.dispatch(context.Background(), []string{"database-servers", "create", "--engine", "mariadb", "--vcpus", "2", "--memory-mb", "1024", "--disk-gb", "10"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != `POST /api/v1/database-servers {"engine":"mariadb","limits":{"diskBytes":10737418240,"memoryBytes":1073741824,"vcpus":2}}` {
		t.Errorf("call = %s", got)
	}
	n := len(*calls)
	if err := a.dispatch(context.Background(), []string{"database-servers", "create", "--engine", "mariadb", "--vcpus", "2"}); err == nil || err.Error() != "give --vcpus, --memory-mb and --disk-gb together" {
		t.Errorf("err = %v", err)
	}
	if err := a.dispatch(context.Background(), []string{"database-servers", "create"}); err == nil || err.Error() != "give --engine mariadb, postgresql or valkey" {
		t.Errorf("err = %v", err)
	}
	if len(*calls) != n {
		t.Errorf("a command with an error sent a request: %v", (*calls)[n:])
	}
}

// Only an administrator can list the servers. For the other users, --server is an ID.
func TestDatabaseServersCreateWithAServerIDForAUserThatIsNotAnAdministrator(t *testing.T) {
	srv, calls := databaseServerPanel(t, false)
	defer srv.Close()
	a, _ := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"database-servers", "create", "--engine", "postgresql", "--server", "S1"}); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[len(*calls)-1]; got != `POST /api/v1/database-servers {"engine":"postgresql","serverId":"S1"}` {
		t.Errorf("call = %s", got)
	}
}

func TestDatabaseServersSet(t *testing.T) {
	srv, calls := databaseServerPanel(t, true)
	defer srv.Close()
	a, _ := testApp(srv)
	last := func() string { return (*calls)[len(*calls)-1] }

	if err := a.dispatch(context.Background(), []string{"database-servers", "set", "mariadb-reports", "--name", "reports", "--plan", "P2"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != `PATCH /api/v1/database-servers/DS2 {"name":"reports","planId":"P2"}` {
		t.Errorf("call = %s", got)
	}

	// A limit that is not given keeps its value.
	if err := a.dispatch(context.Background(), []string{"database-servers", "set", "mariadb-reports", "--memory-mb", "2048"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != `PATCH /api/v1/database-servers/DS2 {"limits":{"diskBytes":5368709120,"memoryBytes":2147483648,"vcpus":1}}` {
		t.Errorf("call = %s", got)
	}

	// With a new plan, a limit that is not given gets the value of the plan.
	if err := a.dispatch(context.Background(), []string{"database-servers", "set", "DS2", "--plan", "P2", "--disk-gb", "20"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got != `PATCH /api/v1/database-servers/DS2 {"limits":{"diskBytes":21474836480,"memoryBytes":1073741824,"vcpus":2},"planId":"P2"}` {
		t.Errorf("call = %s", got)
	}

	n := len(*calls)
	if err := a.dispatch(context.Background(), []string{"database-servers", "set", "mariadb-reports"}); err == nil || err.Error() != "give --name, --plan or a limit" {
		t.Errorf("err = %v", err)
	}
	if len(*calls) != n {
		t.Errorf("a command with an error sent a request: %v", (*calls)[n:])
	}
}

func TestDatabaseServersRetryAndDelete(t *testing.T) {
	srv, calls := databaseServerPanel(t, true)
	defer srv.Close()
	a, _ := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"database-servers", "retry", "mariadb-reports"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), []string{"database-servers", "delete", "mariadb-reports"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), []string{"db-servers", "rm", "DS2"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, ", "); got != "POST /api/v1/database-servers/DS2/retry, DELETE /api/v1/database-servers/DS2, DELETE /api/v1/database-servers/DS2" {
		t.Errorf("calls = %s", got)
	}
	// The panel refuses to remove a database server that has databases.
	if err := a.dispatch(context.Background(), []string{"database-servers", "delete", "DS1"}); err == nil || err.Error() != "databaseServer: This database server has 3 databases. Remove the databases first." {
		t.Errorf("err = %v", err)
	}
}

// "databases" of a plan is the number of databases of each database server.
func TestPlansListShowsTheDatabaseServers(t *testing.T) {
	srv, _ := databaseServerPanel(t, true)
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"plans", "list"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || row(lines[0]) != "NAME WEBSITES MAILBOXES DATABASESERVERS DATABASES ID" || row(lines[1]) != "Standard 5 10 3 5 P1" || row(lines[2]) != "Business 20 50 6 10 P2" {
		t.Errorf("output:\n%s", out)
	}
}

// A user with no database is in the database server that the command gives.
func TestDatabaseUsersShowAndSelectTheDatabaseServer(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/websites":
			w.Write([]byte(`{"data":[{"id":"W1","name":"shop.example","status":"running"}]}`))
		case r.URL.Path == "/api/v1/websites/W1/databases":
			w.Write([]byte(`{"prefix":"u1a2_","databases":[{"id":"D1","engine":"mariadb","name":"u1a2_prod","databaseServerId":"DS1","host":"mariadb.internal","port":3306}],` +
				`"users":[{"id":"U1","engine":"mariadb","name":"u1a2_prod","access":"website","databaseServerId":"DS1","grants":[{"databaseId":"D1","database":"u1a2_prod","privileges":"readwrite"}]},` +
				`{"id":"U2","engine":"mariadb","name":"u1a2_old","access":"website","databaseServerId":null,"grants":[]}],` +
				`"servers":[{"id":"DS1","name":"mariadb-1","engine":"mariadb"},{"id":"DS2","name":"mariadb-reports","engine":"mariadb"}]}`))
		case r.URL.Path == "/api/v1/websites/W1/database-users":
			body, _ := io.ReadAll(r.Body)
			calls = append(calls, string(body))
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"password":"pw-1","job":null}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"database-users", "create", "shop.example", "--name", "report", "--database-server", "mariadb-reports"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != `{"access":"website","databaseServerId":"DS2","engine":"mariadb","grants":[],"name":"report"}` {
		t.Errorf("calls = %v", calls)
	}
	if err := a.dispatch(context.Background(), []string{"database-users", "create", "shop.example", "--name", "report", "--database-server", "postgresql-1"}); err == nil || !strings.Contains(err.Error(), `cannot use a database server "postgresql-1"`) {
		t.Errorf("err = %v", err)
	}

	// A user in the shared database containers of an earlier release has no database server.
	out.Reset()
	if err := a.dispatch(context.Background(), []string{"database-users", "list", "shop.example"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || row(lines[0]) != "NAME ENGINE DATABASESERVER GRANTS ACCESS ID" || row(lines[1]) != "u1a2_prod mariadb mariadb-1 u1a2_prod=readwrite website U1" || row(lines[2]) != "u1a2_old mariadb - website U2" {
		t.Errorf("output:\n%s", out)
	}
}
