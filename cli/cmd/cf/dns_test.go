package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dnsPanel answers like the panel API for the DNS zones of one customer. It records each
// change as "METHOD path body".
func dnsPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const zone = `{"id":"Z1","name":"example.com","customerId":"C1","status":"active","serial":1794000000,"recordCount":3,` +
		`"nameServers":["ns1.panel.example","ns2.panel.example"],` +
		`"delegation":{"state":"elsewhere","nameServers":["ns1.registrar.example"],"checkedAt":"2026-11-14T10:00:00+00:00"},` +
		`"dnssec":{"enabled":true,"ds":{"keyTag":22976,"algorithm":13,"digestType":2,"digest":"3A81","record":"example.com. IN DS 22976 13 2 3A81"},"dnskey":"example.com. IN DNSKEY 257 3 13 abc"},` +
		`"servers":[{"nameServer":"ns1.panel.example","state":"ok"},{"nameServer":"ns2.panel.example","state":"failed"}],"websites":1,"createdAt":"2026-11-01T10:00:00+00:00",` +
		`"records":[{"id":"R1","name":"@","type":"NS","value":"ns1.panel.example","ttl":3600,"priority":null,"managed":"ns"},` +
		`{"id":"R2","name":"@","type":"A","value":"203.0.113.10","ttl":300,"priority":null,"managed":"website"},` +
		`{"id":"R3","name":"@","type":"MX","value":"mx1.mail.example","ttl":3600,"priority":10,"managed":null}]}`
	const job = `{"id":"J1","title":"Add TXT record to example.com","state":"succeeded","steps":["Start DNS server","Write zones","Load zones"],"step":3}`
	const failed = `{"id":"J2","title":"Add TXT record to example.com","state":"failed","steps":["Start DNS server","Write zones","Load zones"],"step":2,"error":"The DNS server did not load the zone."}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case path == "/dns-zones" && r.Method == http.MethodGet:
			if c := r.URL.Query().Get("customerId"); c != "" {
				calls = append(calls, "GET /dns-zones?customerId="+c)
			}
			w.Write([]byte(`{"data":[` + zone + `],"nameServers":["ns1.panel.example","ns2.panel.example"]}`))
		case path == "/dns-zones/Z1" && r.Method == http.MethodGet:
			w.Write([]byte(zone))
		case path == "/dns-zones" && r.Method == http.MethodPost, path == "/dns-zones/Z1/dnssec", path == "/dns-zones/Z1/sync":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"zone":` + zone + `,"jobs":[` + job + `]}`))
		case path == "/dns-zones/Z1/records" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"record":{"id":"R9","name":"@","type":"TXT","value":"v=spf1 -all","ttl":3600,"priority":null,"managed":null},"jobs":[` + job + `,` + failed + `]}`))
		case path == "/dns-zones/Z1/records/R3" && (r.Method == http.MethodPatch || r.Method == http.MethodDelete), path == "/dns-zones/Z1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":[` + job + `]}`))
		case path == "/dns-zones/Z1/check":
			w.Write([]byte(strings.Replace(zone, `"state":"elsewhere"`, `"state":"here"`, 1)))
		case path == "/jobs/J1":
			w.Write([]byte(`{"data":` + job + `}`))
		case path == "/jobs/J2":
			w.Write([]byte(`{"data":` + failed + `}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestDnsListAndGet(t *testing.T) {
	srv, calls := dnsPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"dns", "list", "--customer", "C1"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || row(lines[0]) != "NAME STATUS PUBLICDNS DNSSEC RECORDS CUSTOMERID ID" ||
		row(lines[1]) != "example.com active elsewhere on 3 C1 Z1" || lines[2] != "Name servers: ns1.panel.example, ns2.panel.example" {
		t.Errorf("output:\n%s", out)
	}
	if len(*calls) != 1 || (*calls)[0] != "GET /dns-zones?customerId=C1" {
		t.Errorf("calls: %v", *calls)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "get", "Example.com."}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Name server:   ns2.panel.example (failed)\n",
		"Public DNS:    the registrar sends the queries to other name servers: ns1.registrar.example\n",
		"DS record:     example.com. IN DS 22976 13 2 3A81\n",
		"Then run: cf dns check example.com\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "records", "Z1", "--type", "mx"}); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || row(lines[0]) != "NAME TYPE VALUE PRIORITY TTL FROM ID" || row(lines[1]) != "@ MX mx1.mail.example 10 3600 you R3" {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"dns", "get", "other.example"}); err == nil || !strings.Contains(err.Error(), "there is no DNS zone other.example") {
		t.Errorf("error: %v", err)
	}
}

func TestDnsChanges(t *testing.T) {
	srv, calls := dnsPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"dns", "create", "Example.com", "--customer", "C1"}, `POST /api/v1/dns-zones {"customerId":"C1","name":"example.com"}`},
		{[]string{"dns", "set", "example.com", "R3", "--value", "mx2.mail.example", "--priority", "0", "--ttl", "300"}, `PATCH /api/v1/dns-zones/Z1/records/R3 {"priority":0,"ttl":300,"value":"mx2.mail.example"}`},
		{[]string{"dns", "remove", "example.com", "R3"}, `DELETE /api/v1/dns-zones/Z1/records/R3`},
		{[]string{"dns", "dnssec", "example.com", "on"}, `PUT /api/v1/dns-zones/Z1/dnssec {"enabled":true}`},
		{[]string{"dns", "dnssec", "example.com", "off"}, `PUT /api/v1/dns-zones/Z1/dnssec {"enabled":false}`},
		{[]string{"dns", "sync", "example.com"}, `POST /api/v1/dns-zones/Z1/sync`},
		{[]string{"dns", "check", "example.com"}, `POST /api/v1/dns-zones/Z1/check`},
		{[]string{"dns", "delete", "example.com"}, `DELETE /api/v1/dns-zones/Z1`},
	} {
		*calls = nil
		out.Reset()
		if err := a.dispatch(ctx, tc.args); err != nil {
			t.Errorf("%v: %v", tc.args, err)
		}
		if len(*calls) != 1 || (*calls)[0] != tc.want {
			t.Errorf("%v: calls %v", tc.args, *calls)
		}
		switch tc.args[1] {
		case "check":
			if out.String() != "example.com: the registrar sends the queries to the panel.\n" {
				t.Errorf("output:\n%s", out)
			}
		default:
			if !strings.Contains(out.String(), "Add TXT record to example.com: complete.\n") {
				t.Errorf("%v: output:\n%s", tc.args, out)
			}
		}
		if tc.args[2] != "" && tc.args[len(tc.args)-1] == "on" && !strings.Contains(out.String(), "Give the DS record to the registrar") {
			t.Errorf("output:\n%s", out)
		}
	}
	for _, args := range [][]string{{"dns", "dnssec", "example.com", "maybe"}, {"dns", "set", "example.com", "R3"}, {"dns", "add", "example.com", "TXT"}, {"dns", "explode", "example.com"}} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

// One DNS server took the record, and one did not. The command shows both results and fails.
func TestDnsAddShowsTheJobOfEachServer(t *testing.T) {
	srv, calls := dnsPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	err := a.dispatch(context.Background(), []string{"dns", "add", "example.com", "txt", "@", "v=spf1 -all", "--ttl", "600"})
	if err == nil || !strings.Contains(err.Error(), "The DNS server did not load the zone.") {
		t.Errorf("error: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != `POST /api/v1/dns-zones/Z1/records {"name":"@","ttl":600,"type":"TXT","value":"v=spf1 -all"}` {
		t.Errorf("calls: %v", *calls)
	}
	if !strings.Contains(out.String(), "Record R9: @ TXT v=spf1 -all\n") || !strings.Contains(out.String(), "Add TXT record to example.com: complete.\n") {
		t.Errorf("output:\n%s", out)
	}
}
