package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// networkPanel answers like the panel API for the private networks of one customer. It
// records each change as "METHOD path body".
func networkPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const base = `{"id":"N1","customerId":"C1","name":"default","isDefault":true,"websiteIds":["W1","W2"],"databaseServerIds":["D1"]}`
	const named = `{"id":"N2","customerId":"C1","name":"reports","isDefault":false,"websiteIds":["W2"],"databaseServerIds":["D1"]}`
	const websites = `{"id":"W1","name":"shop.example.com","networkHost":"shopexam.internal","networkPort":80},{"id":"W2","name":"jobs.internal","networkHost":null,"networkPort":null}`
	const databases = `{"id":"D1","name":"mariadb-reports","customerId":"C1","networkHost":"mariadb-reports.internal","networkPort":3306}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case path == "/networks" && r.Method == http.MethodGet:
			if c := r.URL.Query().Get("customerId"); c != "" {
				calls = append(calls, "GET /networks?customerId="+c)
			}
			w.Write([]byte(`{"data":[` + base + `,` + named + `]}`))
		case path == "/networks" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"N3","customerId":"C1","name":"backend","isDefault":false,"websiteIds":["W1"],"databaseServerIds":["D1"]}`))
		case path == "/networks/N2" && r.Method == http.MethodPatch:
			w.Write([]byte(named))
		case path == "/networks/N2" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case path == "/networks/N1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"Each customer has the network \"default\". The panel does not remove it."}`))
		case path == "/websites" && r.URL.Query().Get("q") != "":
			w.Write([]byte(`{"data":[{"id":"W1","name":"shop.example.com"}]}`))
		case path == "/websites":
			w.Write([]byte(`{"data":[` + websites + `]}`))
		case path == "/websites/W2":
			w.Write([]byte(`{"data":{"id":"W2","name":"jobs.internal"}}`))
		case path == "/database-servers":
			w.Write([]byte(`{"data":[` + databases + `]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestNetworksListAndGet(t *testing.T) {
	srv, calls := networkPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"networks", "list", "--customer", "C1"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || row(lines[0]) != "NAME DEFAULT WEBSITES DATABASESERVERS CUSTOMERID ID" ||
		row(lines[1]) != "default yes 2 1 C1 N1" || row(lines[2]) != "reports no 1 1 C1 N2" {
		t.Errorf("output:\n%s", out)
	}
	if len(*calls) != 1 || (*calls)[0] != "GET /networks?customerId=C1" {
		t.Errorf("calls: %v", *calls)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"networks", "get", "Reports"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Name:          reports (named)\n",
		"Website:       jobs.internal (no network name)\n",
		"Database:      mariadb-reports (mariadb-reports.internal:3306)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if err := a.dispatch(ctx, []string{"networks", "get", "other"}); err == nil || !strings.Contains(err.Error(), "there is no network other") {
		t.Errorf("error: %v", err)
	}
}

func TestNetworksChanges(t *testing.T) {
	srv, calls := networkPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	for _, args := range [][]string{
		{"networks", "create", "Backend", "--website", "shop.example.com", "--database-server", "mariadb-reports"},
		{"networks", "add", "reports", "--website", "shop.example.com", "--website", "W2"},
		{"networks", "remove", "reports", "--website", "W2", "--database-server", "D1"},
		{"networks", "rename", "N2", "Analytics"},
		{"networks", "delete", "reports"},
	} {
		if err := a.dispatch(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	want := []string{
		`POST /api/v1/networks {"databaseServerIds":["D1"],"name":"backend","websiteIds":["W1"]}`,
		`PATCH /api/v1/networks/N2 {"databaseServerIds":["D1"],"websiteIds":["W2","W1"]}`,
		`PATCH /api/v1/networks/N2 {"databaseServerIds":[],"websiteIds":[]}`,
		`PATCH /api/v1/networks/N2 {"name":"analytics"}`,
		`DELETE /api/v1/networks/N2`,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
	if !strings.Contains(out.String(), "The network reports is removed. Its members stay.") {
		t.Errorf("output:\n%s", out)
	}

	// The API refuses to remove the network "default". A change needs a member.
	if err := a.dispatch(ctx, []string{"networks", "delete", "default"}); err == nil || !strings.Contains(err.Error(), "does not remove it") {
		t.Errorf("error: %v", err)
	}
	if err := a.dispatch(ctx, []string{"networks", "add", "reports"}); err == nil || !strings.Contains(err.Error(), "give --website or --database-server") {
		t.Errorf("error: %v", err)
	}
}
