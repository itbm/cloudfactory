package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// transfersPanel answers like the panel API for the secondary DNS servers and the zone
// transfers of one zone. It records each change as "METHOD path body".
func transfersPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const secondary = `{"id":"S1","name":"Backup DNS","addresses":["198.51.100.53","2001:db8::53"],"nameServer":"ns3.backup.example",` +
		`"tsig":{"name":"cf-backup","algorithm":"hmac-sha256"},"notify":true,"catalog":true,"createdAt":"2026-11-20T10:00:00+00:00"}`
	const job = `{"id":"J1","title":"Add secondary DNS server","state":"succeeded","steps":["Start DNS server","Set secondary DNS servers"],"step":2}`
	transfers := `{"addresses":["192.0.2.200"],"notify":true,"nameServers":["ns.backup.example"],"tsig":{"name":"transfer.example.com","algorithm":"hmac-sha256"},"primaries":["203.0.113.10","2001:db8:10::1"]}`
	zone := func(transfers string) string {
		return `{"id":"Z1","name":"example.com","customerId":"C1","status":"active","serial":1794000000,"recordCount":3,"nameServers":["ns1.panel.example"],` +
			`"delegation":{"state":"here","nameServers":["ns1.panel.example"],"checkedAt":null},"dnssec":{"enabled":false,"ds":null,"dnskey":null},` +
			`"servers":[],"websites":0,"createdAt":null,"records":[],"transfers":` + transfers + `}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case path == "/dns-secondaries" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + secondary + `],"catalogZone":"catalog.cloudfactory.invalid",` +
				`"primaries":[{"nameServer":"ns1.panel.example","addresses":["203.0.113.10","2001:db8:10::1"]}],"servers":[{"server":"ns1.panel.example","state":"ok"}]}`))
		case path == "/dns-secondaries" && r.Method == http.MethodPost, path == "/dns-secondaries/S1" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"secondary":` + secondary + `,"jobs":[` + job + `]}`))
		case path == "/dns-secondaries/S1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":[` + job + `]}`))
		case path == "/dns-secondaries/S1/secret":
			calls = append(calls, "GET "+r.URL.Path)
			w.Write([]byte(`{"name":"cf-backup","algorithm":"hmac-sha256","secret":"c2VjcmV0"}`))
		case path == "/dns-zones" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + zone(transfers) + `],"nameServers":["ns1.panel.example"]}`))
		case path == "/dns-zones/Z1" && r.Method == http.MethodGet:
			w.Write([]byte(zone(transfers)))
		case path == "/dns-zones/Z1/transfers" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"zone":` + zone(transfers) + `,"jobs":[` + job + `]}`))
		case path == "/dns-zones/Z1/transfers" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"zone":` + zone(`{"addresses":[],"notify":false,"nameServers":[],"tsig":null,"primaries":["203.0.113.10"]}`) + `,"jobs":[` + job + `]}`))
		case path == "/dns-zones/Z1/transfers/secret":
			calls = append(calls, "GET "+r.URL.Path)
			w.Write([]byte(`{"name":"transfer.example.com","algorithm":"hmac-sha256","secret":"em9uZQ=="}`))
		case path == "/jobs/J1":
			w.Write([]byte(`{"data":` + job + `}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestDnsSecondaries(t *testing.T) {
	srv, calls := transfersPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"dns", "secondaries"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 || row(lines[0]) != "NAME ADDRESSES NAMESERVER KEY NOTIFY COPIES ID" ||
		row(lines[1]) != "Backup DNS 198.51.100.53, 2001:db8::53 ns3.backup.example cf-backup (hmac-sha256) yes all zones and the catalog zone S1" ||
		lines[2] != "Copy the zones from: ns1.panel.example (203.0.113.10, 2001:db8:10::1)" ||
		lines[3] != "Catalog zone: catalog.cloudfactory.invalid" || lines[4] != "DNS servers: ns1.panel.example has the list" {
		t.Errorf("output:\n%s", out)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"dns", "secondaries", "add", "Office", "--address", "192.0.2.53", "--address", "2001:db8::54", "--name-server", "NS4.Office.example.", "--catalog"},
			`POST /api/v1/dns-secondaries {"addresses":["192.0.2.53","2001:db8::54"],"algorithm":"hmac-sha256","catalog":true,"name":"Office","nameServer":"ns4.office.example","notify":true,"secret":null,"tsig":true}`},
		{[]string{"dns", "secondaries", "add", "Plain", "--address", "10.0.0.53", "--tsig=false", "--notify=false"},
			`POST /api/v1/dns-secondaries {"addresses":["10.0.0.53"],"catalog":false,"name":"Plain","notify":false,"tsig":false}`},
		{[]string{"dns", "secondaries", "set", "backup dns", "--address", "198.51.100.54", "--no-name-server", "--catalog=false", "--new-secret"},
			`PATCH /api/v1/dns-secondaries/S1 {"addresses":["198.51.100.54"],"catalog":false,"nameServer":null,"newSecret":true}`},
		{[]string{"dns", "secondaries", "remove", "S1"}, `DELETE /api/v1/dns-secondaries/S1`},
	} {
		*calls = nil
		out.Reset()
		if err := a.dispatch(ctx, tc.args); err != nil {
			t.Errorf("%v: %v", tc.args, err)
		}
		if len(*calls) != 1 || (*calls)[0] != tc.want {
			t.Errorf("%v: calls %v", tc.args, *calls)
		}
	}
	if !strings.Contains(out.String(), "First remove it at the registrar of each domain.") {
		t.Errorf("remove output:\n%s", out)
	}

	// The secret of the secondary DNS service comes from the environment, not from an argument.
	*calls = nil
	out.Reset()
	t.Setenv("CF_TSIG_SECRET", "b3du")
	if err := a.dispatch(ctx, []string{"dns", "secondaries", "add", "Own", "--address", "192.0.2.55", "--key-name", "office-key", "--algorithm", "hmac-sha512", "--own-secret"}); err != nil {
		t.Fatal(err)
	}
	if want := `POST /api/v1/dns-secondaries {"addresses":["192.0.2.55"],"algorithm":"hmac-sha512","catalog":false,"keyName":"office-key","name":"Own","notify":true,"secret":"b3du","tsig":true}`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	if strings.Contains(out.String(), "Give the TSIG key") {
		t.Errorf("an own secret needs no hint:\n%s", out)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "secondaries", "secret", "Backup DNS"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Secret:     c2VjcmV0\n") || !strings.Contains(out.String(), `key "cf-backup" { algorithm hmac-sha256; secret "c2VjcmV0"; };`) {
		t.Errorf("output:\n%s", out)
	}

	for _, args := range [][]string{
		{"dns", "secondaries", "add", "No addresses"},
		{"dns", "secondaries", "set", "S1"},
		{"dns", "secondaries", "set", "S1", "--name-server", "ns.example", "--no-name-server"},
		{"dns", "secondaries", "set", "Other", "--name", "X"},
		{"dns", "secondaries", "rotate", "S1"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func TestDnsTransfers(t *testing.T) {
	srv, calls := transfersPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"dns", "transfers", "example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Transfers to:  192.0.2.200 (NOTIFY after each change)\n",
		"TSIG key:      transfer.example.com (hmac-sha256). Show the secret with: cf dns transfers example.com secret\n",
		"Their NS:      ns.backup.example. Give them to the registrar too.\n",
		"Copy from:     203.0.113.10, 2001:db8:10::1\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}

	// A flag that you do not give keeps the current value.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"dns", "transfers", "example.com", "set", "--address", "192.0.2.201"},
			`PUT /api/v1/dns-zones/Z1/transfers {"addresses":["192.0.2.201"],"algorithm":"hmac-sha256","nameServers":["ns.backup.example"],"notify":true,"tsig":true}`},
		{[]string{"dns", "transfers", "example.com", "set", "--no-name-servers", "--notify=false", "--algorithm", "hmac-sha512", "--new-secret"},
			`PUT /api/v1/dns-zones/Z1/transfers {"addresses":["192.0.2.200"],"algorithm":"hmac-sha512","nameServers":[],"newSecret":true,"notify":false,"tsig":true}`},
		{[]string{"dns", "transfers", "example.com", "set", "--name-server", "NS1.Backup.example.", "--name-server", "ns2.backup.example", "--tsig=false"},
			`PUT /api/v1/dns-zones/Z1/transfers {"addresses":["192.0.2.200"],"algorithm":"hmac-sha256","nameServers":["ns1.backup.example","ns2.backup.example"],"notify":true,"tsig":false}`},
		{[]string{"dns", "transfers", "example.com", "stop"}, `DELETE /api/v1/dns-zones/Z1/transfers`},
		{[]string{"dns", "transfers", "example.com", "secret"}, `GET /api/v1/dns-zones/Z1/transfers/secret`},
	} {
		*calls = nil
		out.Reset()
		if err := a.dispatch(ctx, tc.args); err != nil {
			t.Errorf("%v: %v", tc.args, err)
		}
		if len(*calls) != 1 || (*calls)[0] != tc.want {
			t.Errorf("%v: calls %v", tc.args, *calls)
		}
	}
	if !strings.Contains(out.String(), `key "transfer.example.com" { algorithm hmac-sha256; secret "em9uZQ=="; };`) {
		t.Errorf("secret output:\n%s", out)
	}

	*calls = nil
	a.in = strings.NewReader("c2VjcmV0c2VjcmV0\n")
	if err := a.dispatch(ctx, []string{"dns", "transfers", "example.com", "set", "--own-secret"}); err != nil {
		t.Fatal(err)
	}
	if want := `PUT /api/v1/dns-zones/Z1/transfers {"addresses":["192.0.2.200"],"algorithm":"hmac-sha256","nameServers":["ns.backup.example"],"notify":true,"secret":"c2VjcmV0c2VjcmV0","tsig":true}`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	for _, args := range [][]string{
		{"dns", "transfers", "example.com", "set", "--name-server", "ns.example", "--no-name-servers"},
		{"dns", "transfers", "example.com", "pause"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
