package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/wsock"
)

const sessionID = "01m82jppdpt215s2n6f4p03hwd"

func startRelay(t *testing.T, change func(*Relay)) (*Relay, *httptest.Server) {
	t.Helper()
	r := New(Key("base64:test-app-key"))
	r.PingInterval = 0
	if change != nil {
		change(r)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return r, srv
}

func dialBrowser(srv *httptest.Server, r *Relay, ticket string) (*wsock.Conn, error) {
	if ticket == "" {
		ticket = Ticket(r.Key, Browser, sessionID, time.Now().Add(time.Minute))
	}
	return wsock.Dial(context.Background(), srv.URL+"/api/terminal/"+sessionID+"/browser?ticket="+ticket, wsock.Options{})
}

func dialAgent(srv *httptest.Server, r *Relay) (*wsock.Conn, error) {
	ticket := Ticket(r.Key, Agent, sessionID, time.Now().Add(5*time.Minute))
	return wsock.Dial(context.Background(), srv.URL+"/api/terminal/"+sessionID+"/agent", wsock.Options{Header: http.Header{"Authorization": {"Bearer " + ticket}}})
}

func read(t *testing.T, c *wsock.Conn) (int, string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return kind, string(msg)
}

func closeReason(t *testing.T, c *wsock.Conn) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := c.ReadMessage()
	var ce *wsock.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v, want a close frame", err)
	}
	return ce.Reason
}

func TestTheRelayConnectsTheBrowserToTheAgent(t *testing.T) {
	r, srv := startRelay(t, nil)
	browser, err := dialBrowser(srv, r, "")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dialAgent(srv, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*wsock.Conn{browser, agent} {
		if kind, msg := read(t, c); kind != wsock.Text || msg != `{"type":"paired"}` {
			t.Fatalf("got %d %q", kind, msg)
		}
	}
	browser.WriteMessage(wsock.Binary, []byte("ls\r"))
	if kind, msg := read(t, agent); kind != wsock.Binary || msg != "ls\r" {
		t.Fatalf("got %d %q", kind, msg)
	}
	browser.WriteMessage(wsock.Text, []byte(`{"type":"resize","cols":100,"rows":30}`))
	if kind, msg := read(t, agent); kind != wsock.Text || !strings.Contains(msg, "resize") {
		t.Fatalf("got %d %q", kind, msg)
	}
	agent.WriteMessage(wsock.Binary, []byte("file\r\n"))
	if _, msg := read(t, browser); msg != "file\r\n" {
		t.Fatalf("got %q", msg)
	}
	// The end of the shell closes both sides, with the reason of the agent.
	agent.WriteClose(wsock.CloseNormal, "The shell ended.")
	if reason := closeReason(t, browser); reason != "The shell ended." {
		t.Fatalf("got %q", reason)
	}
}

func TestTheRelayRefusesABadOrUsedTicket(t *testing.T) {
	r, srv := startRelay(t, nil)
	var he *wsock.HandshakeError
	// A ticket of a different session, an old ticket and a ticket of the agent role.
	for _, ticket := range []string{
		Ticket(r.Key, Browser, "01m82jppdpt215s2n6f4p03hwe", time.Now().Add(time.Minute)),
		Ticket(r.Key, Browser, sessionID, time.Now().Add(-time.Second)),
		Ticket(r.Key, Agent, sessionID, time.Now().Add(time.Minute)),
		Ticket(Key("a different key"), Browser, sessionID, time.Now().Add(time.Minute)),
		"x",
	} {
		if _, err := dialBrowser(srv, r, ticket); !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("ticket %q: got %v", ticket, err)
		}
	}
	if _, err := dialBrowser(srv, r, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dialBrowser(srv, r, ""); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("a second connection with a ticket: got %v", err)
	}
	resp, err := http.Get(srv.URL + "/api/terminal/" + sessionID + "/other")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %v %v", resp, err)
	}
	resp, err = http.Get(srv.URL + "/up")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("got %v %v", resp, err)
	}
}

func TestOneSideWaitsForTheOtherSideToATimeLimit(t *testing.T) {
	r, srv := startRelay(t, func(r *Relay) { r.PairTimeout = 200 * time.Millisecond })
	browser, err := dialBrowser(srv, r, "")
	if err != nil {
		t.Fatal(err)
	}
	if reason := closeReason(t, browser); reason != "The other side of the terminal did not connect." {
		t.Fatalf("got %q", reason)
	}
	// The agent comes too late.
	agent, err := dialAgent(srv, r)
	if err == nil {
		if reason := closeReason(t, agent); reason == "" {
			t.Fatal("want a reason")
		}
	}
}

func TestASessionWithNoInputCloses(t *testing.T) {
	r, srv := startRelay(t, func(r *Relay) { r.IdleTimeout = 300 * time.Millisecond })
	browser, _ := dialBrowser(srv, r, "")
	agent, err := dialAgent(srv, r)
	if err != nil {
		t.Fatal(err)
	}
	read(t, browser)
	read(t, agent)
	// Output from the agent does not keep the session open.
	agent.WriteMessage(wsock.Binary, []byte("tail -f\r\n"))
	read(t, browser)
	if reason := closeReason(t, agent); !strings.Contains(reason, "with no input") {
		t.Fatalf("got %q", reason)
	}
}

func TestShutdownClosesTheSessions(t *testing.T) {
	r, srv := startRelay(t, nil)
	browser, _ := dialBrowser(srv, r, "")
	agent, _ := dialAgent(srv, r)
	read(t, browser)
	read(t, agent)
	r.Shutdown(context.Background())
	if reason := closeReason(t, browser); reason != "The terminal relay of the panel stopped." {
		t.Fatalf("got %q", reason)
	}
}

func TestTheTicketAgreesWithThePanel(t *testing.T) {
	// The same values as api/tests/Unit/TerminalTicketsTest.php.
	key := Key("base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	got := Ticket(key, Browser, sessionID, time.Unix(1_800_000_000, 0))
	if want := "1800000000.e46bb9ed9ff1529010dfd652150346e64ec303c352143aae23cd1f4354223af5"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if !check(key, Browser, sessionID, got, time.Unix(1_799_999_999, 0)) {
		t.Fatal("the ticket is not good")
	}
}

func TestTheCloseReasonsGiveTimesForPeople(t *testing.T) {
	for d, want := range map[time.Duration]string{12 * time.Hour: "12 hours", time.Hour: "1 hour", 30 * time.Minute: "30 minutes", 300 * time.Millisecond: "0 seconds", 90 * time.Second: "90 seconds"} {
		if got := duration(d); got != want {
			t.Errorf("duration(%s) = %q, want %q", d, got, want)
		}
	}
}
