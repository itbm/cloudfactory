package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func notificationPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const server = `{"configured":true,"enabled":true,"host":"smtp.example.com","port":587,"encryption":"starttls","username":"panel","hasPassword":true,"fromAddress":"panel@example.com","fromName":"Example","testedAt":null,"testError":null}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		switch path := strings.TrimPrefix(r.URL.Path, "/api/v1"); {
		case path == "/notifications":
			calls = append(calls, "GET /notifications?"+r.URL.RawQuery)
			w.Write([]byte(`{"data":[{"id":"E1","event":"job.failed","recipient":"a@example.com","subject":"Failed: Create database","state":"failed","error":"552 Mailbox full","createdAt":"2026-11-15T01:00:00Z"}],"meta":{"currentPage":1,"lastPage":1,"total":1}}`))
		case path == "/me/notification-preferences":
			w.Write([]byte(`{"data":[{"event":"job.failed","label":"Failed jobs","email":false,"required":false},{"event":"signin.new_device","label":"Sign-in from a new device","email":true,"required":true}]}`))
		case path == "/mail-server" && r.Method == http.MethodPut, path == "/mail-server":
			w.Write([]byte(server))
		case path == "/mail-server/test":
			w.Write([]byte(`{"sent":false,"to":"a@example.com","error":"Connection refused"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &calls
}

func TestNotifications(t *testing.T) {
	srv, calls := notificationPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()

	if err := a.dispatch(ctx, []string{"notifications", "list", "--state", "failed"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "552 Mailbox full") || !strings.Contains((*calls)[0], "state=failed") {
		t.Errorf("output:\n%s\ncalls: %v", out, *calls)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"notifications", "off", "job.failed"}); err != nil {
		t.Fatal(err)
	}
	if (*calls)[len(*calls)-1] != `PUT /api/v1/me/notification-preferences {"events":{"job.failed":false}}` {
		t.Errorf("calls: %v", *calls)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 3 || row(lines[1]) != "job.failed off Failed jobs" || row(lines[2]) != "signin.new_device always on Sign-in from a new device" {
		t.Errorf("output:\n%s", out)
	}
}

func TestMailServer(t *testing.T) {
	srv, calls := notificationPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	t.Setenv("CF_SMTP_PASSWORD", "hunter2")

	if err := a.dispatch(ctx, []string{"mail-server", "set", "--host", "smtp.example.com", "--port", "465", "--user", "panel", "--from", "panel@example.com"}); err != nil {
		t.Fatal(err)
	}
	want := `PUT /api/v1/mail-server {"enabled":true,"encryption":"tls","fromAddress":"panel@example.com","fromName":"","host":"smtp.example.com","password":"hunter2","port":465,"replyTo":"","username":"panel"}`
	if (*calls)[0] != want {
		t.Errorf("call:\n%s\nwant:\n%s", (*calls)[0], want)
	}
	if !strings.Contains(out.String(), "Server:   smtp.example.com:587 (starttls), on") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"mail-server", "test"}); err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("error: %v", err)
	}
}
