package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobEventTriesAgainWhenThePanelFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cfa_x" || r.URL.Path != "/api/agent/v1/jobs/J1/events" {
			t.Errorf("request %s %s", r.Header.Get("Authorization"), r.URL.Path)
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var e Event
		json.NewDecoder(r.Body).Decode(&e)
		if e.State != "succeeded" || e.Step != 6 {
			t.Errorf("event = %+v", e)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "cfa_x", nil)
	c.Backoff = time.Millisecond

	if err := c.JobEvent(context.Background(), "J1", Event{State: "succeeded", Step: 6}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestJoinDoesNotTryAgain(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"The join token is not valid or has expired."}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "", nil).Join(context.Background(), JoinRequest{Token: "cfj_x"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	if want := "panel: /api/agent/v1/join: status 401: The join token is not valid or has expired."; err.Error() != want {
		t.Errorf("err = %q", err)
	}
}
