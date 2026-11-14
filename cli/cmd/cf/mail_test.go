package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mailPanel answers like the panel API for the email of one customer. It records each change
// as "METHOD path body".
func mailPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const domain = `{"id":"MD1","name":"acme.example.com","customerId":"C1","serverId":"S1","status":"active","mailHost":"mail-1.panel.example","dnsReady":false,"dnsCheckedAt":"2026-11-14T10:00:00+00:00","mailboxes":2,"aliases":1,` +
		`"records":[{"type":"MX","name":"acme.example.com.","value":"10 mail-1.panel.example.","purpose":"mx","found":true},{"type":"TXT","name":"202611e._domainkey.acme.example.com.","value":"v=DKIM1; k=ed25519; p=abc","purpose":"dkim","found":false}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		switch {
		case r.URL.Path == "/api/v1/mail/domains" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + domain + `]}`))
		case r.URL.Path == "/api/v1/mail/domains" && strings.Contains(string(body), "taken.example.org"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"Show that you control this domain.","errors":{"name":["Show that you control this domain."]},"verification":{"type":"TXT","name":"_cloudfactory-challenge.taken.example.org","value":"cf-abc"}}`))
		case r.URL.Path == "/api/v1/mail/domains/MD1/check":
			w.Write([]byte(domain))
		case r.URL.Path == "/api/v1/mail/mailboxes" && r.Method == http.MethodGet:
			if q := r.URL.RawQuery; strings.Contains(q, "domainId") {
				calls = append(calls, "GET "+r.URL.Path+"?domainId="+r.URL.Query().Get("domainId"))
			}
			w.Write([]byte(`{"data":[{"id":"MB1","address":"info@acme.example.com","status":"active","quotaBytes":1073741824,"usedBytes":536870912,"customerId":"C1"}]}`))
		case r.URL.Path == "/api/v1/mail/mailboxes" || r.URL.Path == "/api/v1/mail/mailboxes/MB1" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"mailbox":{"id":"MB2"},"password":"correct horse","job":null}`))
		case r.URL.Path == "/api/v1/mail/aliases" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"MA1","address":"sales@acme.example.com","kind":"alias","destinations":["info@acme.example.com","jo@acme.example.com"],"status":"active"}]}`))
		case r.URL.Path == "/api/v1/servers" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"S1","name":"mail-1"}]}`))
		default:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestMailDomains(t *testing.T) {
	srv, calls := mailPanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"mail", "domains", "list"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || row(lines[1]) != "acme.example.com active records missing 2 1 mail-1.panel.example C1 MD1" {
		t.Errorf("output:\n%s", out)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"mail", "domains", "check", "ACME.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Mail host:  mail-1.panel.example (IMAP port 993, SMTP port 465 or 587)", "found", "missing", "v=DKIM1; k=ed25519; p=abc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not have %q:\n%s", want, out)
		}
	}
	if err := a.dispatch(ctx, []string{"mail", "domains", "add", "shop.example.com", "--customer", "C1", "--server", "mail-1"}); err != nil {
		t.Fatal(err)
	}
	// The panel asks for a DNS record when a different customer can own the domain.
	err := a.dispatch(ctx, []string{"mail", "domains", "add", "taken.example.org"})
	if err == nil || !strings.Contains(err.Error(), `_cloudfactory-challenge.taken.example.org TXT "cf-abc"`) {
		t.Errorf("err = %v", err)
	}
	if err := a.dispatch(ctx, []string{"mail", "domains", "remove", "acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, []string{"mail", "domains", "get", "other.example.com"}); err == nil {
		t.Error("a domain that the panel does not have must be an error")
	}
	want := []string{
		"POST /api/v1/mail/domains/MD1/check",
		`POST /api/v1/mail/domains {"customerId":"C1","name":"shop.example.com","serverId":"S1"}`,
		`POST /api/v1/mail/domains {"name":"taken.example.org"}`,
		"DELETE /api/v1/mail/domains/MD1",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
}

func TestMailboxesAndAliases(t *testing.T) {
	srv, calls := mailPanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "list", "--domain", "acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "512 MB of 1 GB") {
		t.Errorf("output:\n%s", out)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "create", "Jo@acme.example.com", "--quota-mb", "2048"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Password: correct horse") {
		t.Errorf("the output does not have the password:\n%s", out)
	}
	for _, args := range [][]string{
		{"mail", "mailboxes", "set", "info@acme.example.com", "--quota-mb", "4096", "--new-password"},
		{"mail", "mailboxes", "delete", "info@acme.example.com"},
		{"mail", "aliases", "create", "team@acme.example.com", "--to", "info@acme.example.com", "--to", "jo@acme.example.com"},
		{"mail", "aliases", "create", "away@acme.example.com", "--to", "jo@example.org", "--forward"},
		{"mail", "aliases", "delete", "sales@acme.example.com"},
		{"servers", "mail", "mail-1", "--hostname", "mail.example.net"},
	} {
		if err := a.dispatch(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"mail", "aliases", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "info@acme.example.com, jo@acme.example.com") {
		t.Errorf("output:\n%s", out)
	}
	for _, args := range [][]string{
		{"mail", "mailboxes", "create", "jo"},
		{"mail", "mailboxes", "create", "jo@other.example.com"},
		{"mail", "mailboxes", "set", "info@acme.example.com"},
		{"mail", "aliases", "create", "team@acme.example.com"},
		{"mail", "mailboxes", "delete", "nobody@acme.example.com"},
		{"servers", "mail", "mail-1"},
		{"mail", "lists", "list"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v must be an error", args)
		}
	}
	want := []string{
		"GET /api/v1/mail/mailboxes?domainId=MD1",
		`POST /api/v1/mail/mailboxes {"domainId":"MD1","localPart":"jo","quotaBytes":2147483648}`,
		`PATCH /api/v1/mail/mailboxes/MB1 {"newPassword":true,"quotaBytes":4294967296}`,
		"DELETE /api/v1/mail/mailboxes/MB1",
		`POST /api/v1/mail/aliases {"destinations":["info@acme.example.com","jo@acme.example.com"],"domainId":"MD1","kind":"alias","localPart":"team"}`,
		`POST /api/v1/mail/aliases {"destinations":["jo@example.org"],"domainId":"MD1","kind":"forwarder","localPart":"away"}`,
		"DELETE /api/v1/mail/aliases/MA1",
		`PUT /api/v1/servers/S1/mail {"hostname":"mail.example.net"}`,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
}
