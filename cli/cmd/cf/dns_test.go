package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		case path == "/dns-zones/Z1/import":
			dry := strings.Contains(string(body), `"dryRun":true`)
			if !dry {
				w.WriteHeader(http.StatusAccepted)
			}
			jobs := ``
			if !dry {
				jobs = job
			}
			w.Write([]byte(`{"dryRun":` + map[bool]string{true: "true", false: "false"}[dry] + `,"added":[{"line":2,"name":"www","type":"A","value":"192.0.2.1","ttl":3600,"priority":null,"note":null}],` +
				`"existing":1,"skipped":[{"line":1,"text":"@ IN SOA ns1. host. 1 2 3 4 5","code":"soa","reason":"The panel makes the SOA record of the zone."}],"zone":` + zone + `,"jobs":[` + jobs + `]}`))
		case path == "/dns-zones/Z1/export":
			w.Write([]byte(`{"name":"example.com","zoneFile":"$ORIGIN example.com.\n@ 3600 IN MX 10 mx1.mail.example.\n"}`))
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
	if len(lines) != 3 || row(lines[0]) != "NAME STATUS DNSPROVIDER PUBLICDNS DNSSEC RECORDS CUSTOMERID ID" ||
		row(lines[1]) != "example.com active panel elsewhere on 3 C1 Z1" || lines[2] != "Name servers: ns1.panel.example, ns2.panel.example" {
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
func TestDnsKeyChange(t *testing.T) {
	var calls []string
	ds := func(tag string) string {
		return `{"keyTag":` + tag + `,"algorithm":13,"digestType":2,"digest":"AB` + tag + `","record":"example.com. IN DS ` + tag + ` 13 2 AB` + tag + `"}`
	}
	zone := func(rollover, parent string) string {
		return `{"id":"Z1","name":"example.com","customerId":"C1","status":"active","serial":1,"recordCount":1,"nameServers":["ns1.panel.example"],` +
			`"delegation":{"state":"here","nameServers":["ns1.panel.example"],"checkedAt":null},"servers":[],"websites":0,"createdAt":null,` +
			`"dnssec":{"enabled":true,"state":"active","ds":` + ds("200") + `,"dnskey":null,"rollover":` + rollover + `,"parent":` + parent + `}}`
	}
	const job = `{"id":"J1","title":"Publish a new DNSSEC key for example.com","state":"succeeded","steps":["Write zones"],"step":1}`
	answer := zone(`null`, `null`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		if r.Method != http.MethodGet {
			calls = append(calls, r.Method+" "+path)
		}
		switch {
		case path == "/dns-zones" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + answer + `],"nameServers":["ns1.panel.example"]}`))
		case path == "/dns-zones/Z1" && r.Method == http.MethodGet, path == "/dns-zones/Z1/check":
			w.Write([]byte(answer))
		case path == "/dns-zones/Z1/dnssec/rollover":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"zone":` + answer + `,"jobs":[` + job + `]}`))
		case path == "/jobs/J1":
			w.Write([]byte(`{"data":` + job + `}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	expect := func(args []string, want ...string) {
		t.Helper()
		out.Reset()
		if err := a.dispatch(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		for _, w := range want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%v: no %q in:\n%s", args, w, out)
			}
		}
	}

	answer = zone(`{"step":"publish","startedAt":"2026-11-14T10:00:00+00:00","until":null,"newDs":`+ds("200")+`,"oldDs":`+ds("100")+`}`, `{"state":"ok","keyTags":[100],"otherKeyTags":[],"checkedAt":"2026-11-14T10:00:00+00:00"}`)
	expect([]string{"dns", "dnssec", "example.com", "rollover"},
		"Key change:    step 1 of 3: the DNS servers publish the new key.", "Registry:      has the DS record of key 100 (checked 2026-11-14T10:00:00+00:00)\n",
		"Publish a new DNSSEC key for example.com: complete.\n", "In approximately 3 hours, give the new DS record to the registrar.")

	answer = zone(`{"step":"submit","startedAt":"2026-11-14T13:00:00+00:00","until":null,"newDs":`+ds("200")+`,"oldDs":`+ds("100")+`}`, `{"state":"ok","keyTags":[100],"otherKeyTags":[],"checkedAt":"2026-11-14T13:00:00+00:00"}`)
	expect([]string{"dns", "get", "example.com"}, "New DS record: example.com. IN DS 200 13 2 AB200\n", "Key change:    step 2 of 3: give the new DS record to the registrar of the domain\n")

	answer = zone(`{"step":"retire","startedAt":"2026-11-14T14:00:00+00:00","until":"2026-11-15T15:00:00+00:00","newDs":`+ds("200")+`,"oldDs":`+ds("100")+`}`, `{"state":"ok","keyTags":[100,200],"otherKeyTags":[],"checkedAt":"2026-11-14T14:00:00+00:00"}`)
	expect([]string{"dns", "check", "example.com"}, "example.com: the registrar sends the queries to the panel.\n", "Step ends:     not before 2026-11-15T15:00:00+00:00\n",
		"Old DS record: example.com. IN DS 100 13 2 AB100 (remove it at the registrar)\n", "Registry:      has the DS record of key 100, 200 (checked")

	answer = zone(`null`, `{"state":"other","keyTags":[],"otherKeyTags":[4242],"checkedAt":"2026-11-14T14:00:00+00:00"}`)
	expect([]string{"dns", "dnssec", "example.com", "cancel"}, "Registry:      has a DS record of key 4242, which the zone does not have. Resolvers that check DNSSEC refuse the answers")
	if strings.Contains(out.String(), "Key change:") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Join(calls, "; ") != "POST /dns-zones/Z1/dnssec/rollover; POST /dns-zones/Z1/check; DELETE /dns-zones/Z1/dnssec/rollover" {
		t.Errorf("calls: %v", calls)
	}
}

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

func TestDnsImportAndExport(t *testing.T) {
	srv, calls := dnsPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "example.com.db")
	if err := os.WriteFile(file, []byte("@ IN SOA ns1. host. 1 2 3 4 5\nwww IN A 192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.dispatch(ctx, []string{"dns", "import", "example.com", file, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	want := `POST /api/v1/dns-zones/Z1/import {"dryRun":true,"zoneFile":"@ IN SOA ns1. host. 1 2 3 4 5\nwww IN A 192.0.2.1\n"}`
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	for _, line := range []string{
		"Would add 1 record. 1 record is in the zone already. 1 line is not imported.",
		"Line 1: The panel makes the SOA record of the zone.",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("no %q in:\n%s", line, out)
		}
	}

	*calls = nil
	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "import", "example.com", file}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Added 1 record.") || len(*calls) != 1 || !strings.Contains((*calls)[0], `"dryRun":false`) {
		t.Errorf("calls %v, output:\n%s", *calls, out)
	}
	if err := a.dispatch(ctx, []string{"dns", "import", "example.com"}); err == nil || !strings.Contains(err.Error(), "a zone file") {
		t.Errorf("error: %v", err)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "export", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "$ORIGIN example.com.\n@ 3600 IN MX 10 mx1.mail.example.\n" {
		t.Errorf("output:\n%s", out)
	}
	saved := filepath.Join(t.TempDir(), "out.zone")
	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "export", "example.com", "--file", saved}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(saved); !strings.HasPrefix(string(b), "$ORIGIN example.com.") {
		t.Errorf("file: %q", b)
	}
}

// providerPanel answers like the panel API for the DNS accounts of a customer, and a zone at
// Cloudflare.
func providerPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const accounts = `{"data":[{"id":"A1","name":"Main","customerId":"C1","pluginId":"P1","provider":"cloudflare","providerName":"Cloudflare","available":true,` +
		`"account":"it@example.com","zones":1,"credentials":{"apiToken":null,"accountId":"abc"}},` +
		`{"id":"A2","name":"Panel Cloudflare","customerId":null,"pluginId":"P1","provider":"cloudflare","providerName":"Cloudflare","available":true,"account":"Hosting","zones":0,"credentials":{}}],` +
		`"providers":[{"pluginId":"P1","pluginName":"Cloudflare DNS","id":"cloudflare","name":"Cloudflare","dnssec":true,"credentials":[` +
		`{"id":"apiToken","label":"API token","secret":true,"optional":false},{"id":"accountId","label":"Account ID","secret":false,"optional":true}]}]}`
	const zone = `{"id":"Z1","name":"example.com","customerId":"C1","status":"active","serial":1794000000,"recordCount":3,` +
		`"nameServers":["ada.ns.cloudflare.com","bob.ns.cloudflare.com"],"servers":[],` +
		`"provider":{"accountId":"A1","accountName":"Main","name":"Cloudflare","state":"failed","error":"Cloudflare does not accept the API token.","active":false,"handover":{"endsAt":null}},` +
		`"delegation":{"state":"here","nameServers":["ada.ns.cloudflare.com"]},"dnssec":{"enabled":false,"ds":null},"records":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)))
		}
		switch path := strings.TrimPrefix(r.URL.Path, "/api/v1"); {
		case path == "/dns-accounts" && r.Method == http.MethodGet:
			w.Write([]byte(accounts))
		case path == "/dns-accounts" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"A3","name":"New","account":"it@example.com"}`))
		case path == "/dns-accounts/A1" && r.Method == http.MethodPatch:
			w.Write([]byte(`{"id":"A1","name":"Renamed"}`))
		case path == "/dns-accounts/A1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"x","errors":{"account":["1 DNS zone uses this account."]}}`))
		case path == "/dns-zones" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + zone + `],"nameServers":["ns1.panel.example"]}`))
		case path == "/dns-zones/Z1" && r.Method == http.MethodGet:
			w.Write([]byte(zone))
		case path == "/dns-zones" && r.Method == http.MethodPost, path == "/dns-zones/Z1/move":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"zone":` + zone + `,"jobs":[]}`))
		case path == "/dns-zones/Z1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestDnsAccounts(t *testing.T) {
	srv, calls := providerPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"dns", "accounts"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || row(lines[0]) != "NAME PROVIDERNAME ACCOUNT OWNER ZONES ID" || row(lines[1]) != "Main Cloudflare it@example.com C1 1 A1" ||
		!strings.HasSuffix(row(lines[2]), "panel 0 A2") ||
		lines[3] != "Provider cloudflare: Cloudflare, from the plugin Cloudflare DNS. Fields: apiToken (secret: CF_API_TOKEN or stdin), accountId (optional)." {
		t.Errorf("output:\n%s", out)
	}

	// The secret comes from stdin, not from an argument.
	a.in = strings.NewReader("token-1\n")
	out.Reset()
	if err := a.dispatch(ctx, []string{"dns", "accounts", "add", "Cloudflare", "New", "--set", "accountId=abc", "--customer", "C1"}); err != nil {
		t.Fatal(err)
	}
	if want := `POST /api/v1/dns-accounts {"credentials":{"accountId":"abc","apiToken":"token-1"},"customerId":"C1","name":"New","pluginId":"P1","provider":"cloudflare"}`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	if !strings.Contains(out.String(), "Added the DNS account New (Cloudflare, it@example.com): A3.") {
		t.Errorf("output:\n%s", out)
	}
	*calls = nil
	t.Setenv("CF_API_TOKEN", "token-2")
	if err := a.dispatch(ctx, []string{"dns", "accounts", "add", "cloudflare", "Shared", "--panel"}); err != nil {
		t.Fatal(err)
	}
	if want := `POST /api/v1/dns-accounts {"credentials":{"apiToken":"token-2"},"customerId":null,"name":"Shared","pluginId":"P1","provider":"cloudflare"}`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	for _, args := range [][]string{
		{"dns", "accounts", "add", "cloudflare", "X", "--set", "apiToken=secret"},
		{"dns", "accounts", "add", "cloudflare", "X", "--set", "colour=blue"},
		{"dns", "accounts", "add", "route53", "X"},
		{"dns", "accounts", "add", "cloudflare", "X", "--panel", "--customer", "C1"},
		{"dns", "accounts", "set", "Main"},
		{"dns", "accounts", "set", "Other", "--name", "X"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v: no error", args)
		}
	}

	*calls = nil
	if err := a.dispatch(ctx, []string{"dns", "accounts", "set", "main", "--name", "Renamed", "--credentials"}); err != nil {
		t.Fatal(err)
	}
	if want := `PATCH /api/v1/dns-accounts/A1 {"credentials":{"apiToken":"token-2"},"name":"Renamed"}`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls: %v", *calls)
	}
	if err := a.dispatch(ctx, []string{"dns", "accounts", "remove", "A1"}); err == nil || !strings.Contains(err.Error(), "1 DNS zone uses this account.") {
		t.Errorf("error: %v", err)
	}
}

func TestDnsZoneAtProvider(t *testing.T) {
	srv, calls := providerPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"dns", "get", "example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Provider:      Cloudflare (account Main): could not take the last change\n",
		"Error:         Cloudflare does not accept the API token.\n",
		"Handover:      the name servers of the panel also answer until two days after the registrar has the new name servers\n",
		"Name servers:  ada.ns.cloudflare.com, bob.ns.cloudflare.com (from Cloudflare)\n",
		"Public DNS:    the registrar sends the queries to Cloudflare\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"dns", "create", "example.com", "--account", "Main"}, `POST /api/v1/dns-zones {"accountId":"A1","importRecords":true,"name":"example.com"}`},
		{[]string{"dns", "create", "example.com", "--account", "A2", "--no-import"}, `POST /api/v1/dns-zones {"accountId":"A2","importRecords":false,"name":"example.com"}`},
		{[]string{"dns", "move", "example.com", "panel"}, `POST /api/v1/dns-zones/Z1/move {"accountId":null,"importRecords":true}`},
		{[]string{"dns", "move", "example.com", "Panel Cloudflare", "--no-import"}, `POST /api/v1/dns-zones/Z1/move {"accountId":"A2","importRecords":false}`},
		{[]string{"dns", "delete", "example.com", "--keep-at-provider"}, `DELETE /api/v1/dns-zones/Z1?keepAtProvider=1`},
	} {
		*calls = nil
		if err := a.dispatch(ctx, tc.args); err != nil {
			t.Errorf("%v: %v", tc.args, err)
		}
		if len(*calls) != 1 || (*calls)[0] != tc.want {
			t.Errorf("%v: calls %v", tc.args, *calls)
		}
	}
	if err := a.dispatch(ctx, []string{"dns", "move", "example.com"}); err == nil {
		t.Error("move with no account: no error")
	}
}
