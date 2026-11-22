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

// certificatePanel answers like the panel API for the certificates of one customer. The order
// of the certificate C2 runs for two reads. It records each change as "METHOD path body".
func certificatePanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	reads := 0
	const valid = `{"id":"C1","customerId":"K1","source":"upload","names":["shop.example.com","www.example.com"],"state":"valid","issuer":"Example CA",` +
		`"fingerprint":"ab12","notBefore":"2026-11-01T00:00:00+00:00","expiresAt":"2027-11-01T00:00:00+00:00","trusted":false,"renewAt":null,"order":null,"error":null,"nextTryAt":null,` +
		`"usedBy":[{"domain":"shop.example.com","websiteId":"W1","websiteName":"shop.example.com","serverName":"web-1"}],"createdAt":"2026-11-01T00:00:00+00:00"}`
	order := func(stage string) string {
		return `{"id":"C2","customerId":"K1","source":"acme","names":["example.com","*.example.com"],"state":"issuing","issuer":null,"fingerprint":null,"notBefore":null,"expiresAt":null,` +
			`"trusted":null,"renewAt":null,"order":{"stage":"` + stage + `","startedAt":"2026-11-14T10:00:00+00:00","records":[{"name":"_acme-challenge.example.com","value":"abc"}]},"error":null,"nextTryAt":null,"usedBy":[],"createdAt":"2026-11-14T10:00:00+00:00"}`
	}
	const issued = `{"id":"C2","customerId":"K1","source":"acme","names":["example.com","*.example.com"],"state":"valid","issuer":"Let's Encrypt (R12)","fingerprint":"cd34",` +
		`"notBefore":"2026-11-14T10:00:00+00:00","expiresAt":"2027-02-12T10:00:00+00:00","trusted":true,"renewAt":"2027-01-13T10:00:00+00:00","order":null,"error":null,"nextTryAt":null,"usedBy":[],"createdAt":"2026-11-14T10:00:00+00:00"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case path == "/certificates" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + valid + `,` + order("dns") + `]}`))
		case path == "/certificates/C1" && r.Method == http.MethodGet:
			w.Write([]byte(strings.Replace(valid, `"createdAt"`, `"certificatePem":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n","createdAt"`, 1)))
		case path == "/certificates/C1" && r.Method == http.MethodPut:
			w.Write([]byte(valid))
		case path == "/certificates/C1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":1}`))
		case path == "/certificates" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			if strings.Contains(string(body), `"upload"`) {
				w.Write([]byte(valid))
			} else {
				w.Write([]byte(order("new")))
			}
		case path == "/certificates/C2" && r.Method == http.MethodGet:
			reads++
			switch reads {
			case 1:
				w.Write([]byte(order("dns")))
			case 2:
				w.Write([]byte(order("validating")))
			default:
				w.Write([]byte(issued))
			}
		case path == "/certificates/C2/renew":
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"The zone has a CNAME record.","errors":{"certificate":["The zone example.com has a CNAME record for _acme-challenge.example.com."]}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestCertificatesListAndGet(t *testing.T) {
	srv, _ := certificatePanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"certificates", "list"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || row(lines[0]) != "NAMES SOURCE STATE EXPIRESAT DOMAINS CUSTOMERID ID" ||
		row(lines[1]) != "shop.example.com, www.example.com upload valid 2027-11-01T00:00:00+00:00 1 K1 C1" {
		t.Errorf("output:\n%s", out)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"certificates", "get", "Shop.example.com."}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Names:         shop.example.com, www.example.com\n",
		"Source:        uploaded\n",
		"Browsers do not trust the issuer of this certificate.\n",
		"Used by:       shop.example.com (shop.example.com, web-1)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"certificates", "get", "C1", "--pem"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n" {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"certificates", "get", "other.example"}); err == nil || !strings.Contains(err.Error(), "there is no certificate other.example") {
		t.Errorf("error: %v", err)
	}
}

func TestCertificatesRequestWaitsForTheOrder(t *testing.T) {
	srv, calls := certificatePanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"certificates", "request", "Example.com", "--wildcard", "--customer", "K1"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != `POST /api/v1/certificates {"customerId":"K1","domain":"example.com","source":"acme","wildcard":true}` {
		t.Errorf("calls: %v", *calls)
	}
	for _, want := range []string{
		"[new] Make the order and add the TXT records\n[dns] Wait for the DNS servers\n[validating] Let's Encrypt checks the records\n",
		"State:         valid\n",
		"Renewal:       2027-01-13T10:00:00+00:00\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
}

func TestCertificatesChanges(t *testing.T) {
	srv, calls := certificatePanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"), 0o600)
	os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----\n"), 0o600)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"certificates", "upload", "--cert", cert, "--key", key}, `POST /api/v1/certificates {"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n","privateKey":"-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----\n","source":"upload"}`},
		{[]string{"certificates", "replace", "C1", "--cert", cert, "--key", key}, `PUT /api/v1/certificates/C1 {"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n","privateKey":"-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----\n"}`},
		{[]string{"certificates", "delete", "shop.example.com"}, `DELETE /api/v1/certificates/C1`},
	} {
		*calls = nil
		if err := a.dispatch(ctx, tc.args); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if len(*calls) != 1 || (*calls)[0] != tc.want {
			t.Errorf("%v: calls %q", tc.args, *calls)
		}
	}
	if !strings.Contains(out.String(), "Removed the certificate shop.example.com, www.example.com.") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"certificates", "upload", "--cert", cert}); err == nil || !strings.Contains(err.Error(), "give the file of the private key (--key)") {
		t.Errorf("error: %v", err)
	}
	if err := a.dispatch(ctx, []string{"certificates", "renew", "example.com"}); err == nil || !strings.Contains(err.Error(), "CNAME record") {
		t.Errorf("error: %v", err)
	}
}
