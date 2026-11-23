package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebsitesMove(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		switch {
		case r.URL.Path == "/api/v1/websites" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"W1","name":"shop.example","status":"running","serverId":"S1","unixUser":"shop_a1b2","runtime":{"image":"ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"}}]}`))
		case r.URL.Path == "/api/v1/servers":
			w.Write([]byte(`{"data":[{"id":"S1","name":"web-1"},{"id":"S2","name":"web-2"}]}`))
		case r.URL.Path == "/api/v1/websites/W1/move":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"website":{"id":"W1","name":"shop.example","status":"moving","serverId":"S1","movingToServerId":"S2"},"jobs":[` +
				`{"id":"J1","title":"Receive website shop.example from web-1","state":"queued","steps":["Create Unix user","Receive files"],"step":0},` +
				`{"id":"J2","title":"Send website shop.example to web-2","state":"queued","steps":["Stop website","Send files"],"step":0}]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	defer srv.Close()
	a, out := testApp(srv)
	a.noWait = true

	if err := a.dispatch(context.Background(), []string{"websites", "move", "shop.example"}); err == nil || !strings.Contains(err.Error(), "--server") {
		t.Errorf("a move with no server: %v", err)
	}
	if err := a.dispatch(context.Background(), []string{"websites", "move", "shop.example", "--server", "web-2"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ", "); got != `POST /api/v1/websites/W1/move {"serverId":"S2"}` {
		t.Errorf("calls = %s", got)
	}
	for _, want := range []string{"Website shop.example moves to web-2.", "Job J1 is in the queue: Receive website shop.example from web-1", "Job J2 is in the queue: Send website"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output has no %q:\n%s", want, out.String())
		}
	}
}
