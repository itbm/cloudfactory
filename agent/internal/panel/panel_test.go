package panel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/pki"
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
	c := New(srv.URL, "cfa_x", nil, "")
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

	_, err := New(srv.URL, "", nil, "").Join(context.Background(), JoinRequest{Token: "cfj_x"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	if want := "panel: /api/agent/v1/join: status 401: The join token is not valid or has expired."; err.Error() != want {
		t.Errorf("err = %q", err)
	}
}

// The agent on the panel server connects to the HTTPS port of the API. The certificate of the
// panel has no public name, so the agent accepts only the certificate with the fingerprint
// that the install script gave. The agent sends its own certificate, and the panel sees it.
func TestAPinnedClientAcceptsOnlyThePanelCertificate(t *testing.T) {
	dir := t.TempDir()
	agentCert, agentKey := filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key")
	if err := pki.Generate(agentCert, agentKey, []string{"web-1"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	agentFingerprint, _, err := pki.FileFingerprint(agentCert)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(agentCert, agentKey)
	if err != nil {
		t.Fatal(err)
	}

	var seen atomic.Value
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 1 {
			seen.Store(pki.Fingerprint(r.TLS.PeerCertificates[0].Raw))
		}
		w.Write([]byte(`{"serverId":"S1","agentToken":"cfa_new","panelCertificateSha256":"ab"}`))
	}))
	// The same mode as the HTTPS port of the API: the server asks for a client certificate and
	// does not check it. The API compares its fingerprint.
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()
	panelFingerprint := pki.Fingerprint(srv.Certificate().Raw)

	resp, err := New(srv.URL, "", &cert, panelFingerprint).Join(context.Background(), JoinRequest{Token: "cfj_x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AgentToken != "cfa_new" {
		t.Errorf("response = %+v", resp)
	}
	if seen.Load() != agentFingerprint {
		t.Errorf("the panel saw the client certificate %v, want %s", seen.Load(), agentFingerprint)
	}

	// A different certificate on the same address is not the panel.
	wrong := strings.Repeat("ab", 32)
	if _, err := New(srv.URL, "", &cert, wrong).Join(context.Background(), JoinRequest{Token: "cfj_x"}); err == nil || !strings.Contains(err.Error(), "is not the certificate") {
		t.Errorf("err = %v, want a pin error", err)
	}
	// With no pin, the client uses the public certificate authorities. They do not know the
	// certificate of the test server.
	if _, err := New(srv.URL, "", &cert, "").Join(context.Background(), JoinRequest{Token: "cfj_x"}); err == nil {
		t.Error("a client with no pin accepted a certificate that no authority signed")
	}
}
