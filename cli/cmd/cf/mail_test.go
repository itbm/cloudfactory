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
		case r.URL.Path == "/api/v1/mail/mailboxes/MB1/autoresponder" && r.Method == http.MethodGet:
			w.Write([]byte(`{"autoresponder":{"enabled":false,"subject":null,"text":"Back soon.","startsAt":null,"endsAt":"2027-01-04T23:59:59Z","filterScript":"filters"}}`))
		case r.URL.Path == "/api/v1/mail/mailboxes/MB1/autoresponder":
			w.Write([]byte(`{"autoresponder":` + string(body) + `}`))
		case r.URL.Path == "/api/v1/mail/mailboxes/MB1/private-key":
			w.Write([]byte(`{"address":"info@acme.example.com","privateKey":"-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nxYYE\n-----END PGP PRIVATE KEY BLOCK-----\n","passwordCurrent":false}`))
		case r.URL.Path == "/api/v1/mail/aliases" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"MA1","address":"sales@acme.example.com","kind":"alias","destinations":["info@acme.example.com","jo@acme.example.com"],"status":"active"}]}`))
		case r.URL.Path == "/api/v1/servers" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"S1","name":"mail-1"},{"id":"S2","name":"mail-2"},{"id":"S3","name":"mail-3"}]}`))
		case r.URL.Path == "/api/v1/mail-clusters" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"MC1","name":"eu","hostname":"mail.example.net","status":"failed","phase":"join","error":"mail-2: the agent stopped","domains":0,` +
				`"members":[{"number":1,"serverName":"mail-1","hostname":"m1.example.net","endpoint":"192.0.2.1","address":"10.77.16.1","region":0,"step":"joined","serverState":"online"}],` +
				`"records":[{"type":"A","name":"mail.example.net","value":"192.0.2.1"}]}]}`))
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
	// The panel makes the keys: a new mailbox, and a mailbox that exists gets a new password too.
	out.Reset()
	for _, args := range [][]string{
		{"mail", "mailboxes", "create", "olu@acme.example.com", "--encrypt"},
		{"mail", "mailboxes", "set", "info@acme.example.com", "--encrypt"},
	} {
		if err := a.dispatch(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "private-key", "info@acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	// The panel says that the key has an older password.
	if !strings.Contains(out.String(), "-----BEGIN PGP PRIVATE KEY BLOCK-----\n") || !strings.Contains(out.String(), "older password") {
		t.Errorf("output:\n%s", out)
	}
	file := filepath.Join(t.TempDir(), "key.asc")
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "private-key", "info@acme.example.com", "--output", file}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the key file: %v, %v", fi, err)
	}
	a.in = strings.NewReader("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmDME\n-----END PGP PUBLIC KEY BLOCK-----\n")
	for _, args := range [][]string{
		{"mail", "mailboxes", "set", "info@acme.example.com", "--quota-mb", "4096", "--new-password"},
		{"mail", "mailboxes", "set", "info@acme.example.com", "--public-key", "-"},
		{"mail", "mailboxes", "set", "info@acme.example.com", "--no-encryption"},
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
		{"mail", "mailboxes", "set", "info@acme.example.com", "--public-key", "-", "--no-encryption"},
		{"mail", "mailboxes", "set", "info@acme.example.com", "--public-key", "/no/such/file.asc"},
		{"mail", "mailboxes", "set", "info@acme.example.com", "--encrypt", "--no-encryption"},
		{"mail", "mailboxes", "private-key", "nobody@acme.example.com"},
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
		`POST /api/v1/mail/mailboxes {"domainId":"MD1","encrypt":true,"localPart":"olu","quotaBytes":1073741824}`,
		`PATCH /api/v1/mail/mailboxes/MB1 {"encrypt":true,"newPassword":true}`,
		`PATCH /api/v1/mail/mailboxes/MB1 {"newPassword":true,"quotaBytes":4294967296}`,
		`PATCH /api/v1/mail/mailboxes/MB1 {"publicKey":"-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmDME\n-----END PGP PUBLIC KEY BLOCK-----\n"}`,
		`PATCH /api/v1/mail/mailboxes/MB1 {"publicKey":null}`,
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

func TestMailClusters(t *testing.T) {
	srv, calls := mailPanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"mail", "clusters", "list"}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 || row(lines[1]) != "eu mail.example.net failed join 1 0 MC1" {
		t.Errorf("output:\n%s", out)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"mail", "clusters", "get", "eu"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"failed (step join)", "mail-2: the agent stopped", "cf mail clusters retry eu", "10.77.16.1", "mail.example.net  192.0.2.1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not have %q:\n%s", want, out)
		}
	}
	for _, args := range [][]string{
		{"mail", "clusters", "create", "eu", "--hostname", "mail.example.net", "--member", "mail-1=m1.example.net=192.0.2.1=0", "--member", "mail-2=m2.example.net=192.0.2.2", "--member", "mail-3=m3.example.net=192.0.2.3=1"},
		{"mail", "clusters", "retry", "eu"},
	} {
		if err := a.dispatch(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"mail", "clusters", "create", "eu", "--member", "mail-1=m1.example.net=192.0.2.1"},
		{"mail", "clusters", "create", "eu", "--hostname", "mail.example.net", "--member", "mail-1=m1.example.net=192.0.2.1", "--member", "mail-2=m2.example.net=192.0.2.2"},
		{"mail", "clusters", "create", "eu", "--hostname", "mail.example.net", "--member", "mail-1=m1", "--member", "mail-2=m2.example.net=192.0.2.2", "--member", "mail-3=m3.example.net=192.0.2.3"},
		{"mail", "clusters", "create", "eu", "--hostname", "mail.example.net", "--member", "mail-1=m1.example.net=192.0.2.1=x", "--member", "mail-2=m2.example.net=192.0.2.2", "--member", "mail-3=m3.example.net=192.0.2.3"},
		{"mail", "clusters", "get", "us"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v must be an error", args)
		}
	}
	want := []string{
		`POST /api/v1/mail-clusters {"hostname":"mail.example.net","members":[{"endpoint":"192.0.2.1","hostname":"m1.example.net","region":0,"serverId":"S1"},{"endpoint":"192.0.2.2","hostname":"m2.example.net","serverId":"S2"},{"endpoint":"192.0.2.3","hostname":"m3.example.net","region":1,"serverId":"S3"}],"name":"eu"}`,
		"POST /api/v1/mail-clusters/MC1/retry",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
}
