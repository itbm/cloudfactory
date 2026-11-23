// Package relay is the terminal relay of the panel (FR-802). It connects the browser of a
// user to the agent of the server of a website, for the terminal of that website.
//
// The relay keeps no data and has no connection to the database. Both sides connect to it
// with a WebSocket and a ticket of the panel API:
//
//	browser ── wss://<panel>/api/terminal/<session>/browser?ticket=… ──┐
//	                                                                    relay
//	agent ──── wss://<panel>/api/terminal/<session>/agent (Bearer …) ──┘
//
// A ticket is the HMAC of the role, the session and the time limit, with a key that the API
// makes from its APP_KEY. A ticket is good for one connection. When both sides are
// connected, the relay sends {"type":"paired"} to each side and then copies each message to
// the other side with no change. The agent runs the shell and records the commands. The
// relay does not read the messages.
package relay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/wsock"
)

// The roles of the two sides of a session.
const (
	Browser = "browser"
	Agent   = "agent"
)

// keyLabel is the message of the HMAC that makes the key of the tickets from APP_KEY. The
// panel API uses the same label (App\Services\TerminalTickets).
const keyLabel = "cloudfactory terminal relay"

// Key gives the key of the tickets from the APP_KEY of the panel.
func Key(appKey string) []byte {
	m := hmac.New(sha256.New, []byte(appKey))
	m.Write([]byte(keyLabel))
	return m.Sum(nil)
}

// Ticket gives the ticket of a role for a session, good to the time limit.
func Ticket(key []byte, role, session string, until time.Time) string {
	exp := strconv.FormatInt(until.Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(role + "\n" + session + "\n" + exp))
	return exp + "." + hex.EncodeToString(m.Sum(nil))
}

var (
	sessionRE = regexp.MustCompile(`^[0-9A-Za-z]{26}$`)
	ticketRE  = regexp.MustCompile(`^[0-9]{1,12}\.[0-9a-f]{64}$`)
)

// check reports whether the ticket is good for the role and the session at the time now.
func check(key []byte, role, session, ticket string, now time.Time) bool {
	if !ticketRE.MatchString(ticket) {
		return false
	}
	exp, _, _ := strings.Cut(ticket, ".")
	until, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > until {
		return false
	}
	return hmac.Equal([]byte(ticket), []byte(Ticket(key, role, session, time.Unix(until, 0))))
}

// Relay is the HTTP handler of the relay.
type Relay struct {
	// Key is the key of the tickets (Key).
	Key []byte
	// PairTimeout is the time that one side waits for the other side.
	PairTimeout time.Duration
	// IdleTimeout closes a session when the browser sends no message for this time.
	IdleTimeout time.Duration
	// MaxDuration closes a session after this time.
	MaxDuration time.Duration
	// PingInterval is the time between two pings to each side. The pings keep the
	// connections open through proxies.
	PingInterval time.Duration
	// Now is the clock. The default is time.Now.
	Now func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	// used are the sessions that had a connection, so that a ticket works only once. An entry
	// stays until the tickets of the session are no longer good.
	used map[string]time.Time
}

type session struct {
	id    string
	conns map[string]*wsock.Conn
	timer *time.Timer
	done  chan struct{}
	once  sync.Once
}

// New gives a relay with the usual limits.
func New(key []byte) *Relay {
	return &Relay{Key: key, PairTimeout: time.Minute, IdleTimeout: 30 * time.Minute, MaxDuration: 12 * time.Hour, PingInterval: 30 * time.Second}
}

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// ticketLife is the longest time that the API gives a ticket. The relay keeps a used session
// for this time.
const ticketLife = 10 * time.Minute

func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/up" {
		w.Write([]byte("ok\n"))
		return
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/api/terminal/")
	id, role, _ := strings.Cut(rest, "/")
	if !ok || !sessionRE.MatchString(id) || (role != Browser && role != Agent) {
		http.NotFound(w, req)
		return
	}
	ticket := req.URL.Query().Get("ticket")
	if role == Agent {
		ticket, _ = strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	}
	if !check(r.Key, role, id, ticket, r.now()) {
		http.Error(w, "The ticket of the terminal is not valid or is too old.", http.StatusForbidden)
		return
	}
	s, err := r.claim(id, role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	conn, err := wsock.Upgrade(w, req)
	if err != nil {
		r.end(s, wsock.CloseGoingAway, "The other side could not connect.")
		return
	}
	r.attach(s, role, conn)
}

// claim records the connection of a role. Each role connects one time.
func (r *Relay) claim(id, role string) (*session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions, r.used = map[string]*session{}, map[string]time.Time{}
	}
	now := r.now()
	for k, at := range r.used {
		if now.Sub(at) > ticketLife {
			delete(r.used, k)
		}
	}
	key := id + "/" + role
	if _, ok := r.used[key]; ok {
		return nil, errors.New("This ticket was used. Open the terminal again.")
	}
	r.used[key] = now
	s := r.sessions[id]
	if s == nil {
		s = &session{id: id, conns: map[string]*wsock.Conn{}, done: make(chan struct{})}
		r.sessions[id] = s
		s.timer = time.AfterFunc(r.PairTimeout, func() {
			r.end(s, wsock.CloseGoingAway, "The other side of the terminal did not connect.")
		})
	}
	return s, nil
}

// attach adds the connection of a role. The second connection starts the session.
func (r *Relay) attach(s *session, role string, conn *wsock.Conn) {
	r.mu.Lock()
	select {
	case <-s.done:
		r.mu.Unlock()
		conn.WriteClose(wsock.CloseGoingAway, "The other side of the terminal did not connect.")
		conn.Close()
		return
	default:
	}
	s.conns[role] = conn
	paired := len(s.conns) == 2
	if paired {
		s.timer.Stop()
		s.timer = time.AfterFunc(r.MaxDuration, func() {
			r.end(s, wsock.ClosePolicy, "The terminal closed after "+duration(r.MaxDuration)+".")
		})
	}
	r.mu.Unlock()
	slog.Info("terminal relay: connected", "session", s.id, "side", role, "address", conn.RemoteAddr().String())
	if !paired {
		return
	}
	browser, agent := s.conns[Browser], s.conns[Agent]
	for _, c := range []*wsock.Conn{browser, agent} {
		if err := c.WriteMessage(wsock.Text, []byte(`{"type":"paired"}`)); err != nil {
			r.end(s, wsock.CloseGoingAway, "The other side of the terminal closed.")
			return
		}
	}
	go r.copy(s, browser, agent, r.IdleTimeout)
	go r.copy(s, agent, browser, 0)
	go r.ping(s, browser, agent)
}

// copy sends each message of from to to. With an idle time, a read that waits longer ends
// the session.
func (r *Relay) copy(s *session, from, to *wsock.Conn, idle time.Duration) {
	for {
		if idle > 0 {
			from.SetReadDeadline(time.Now().Add(idle))
		}
		kind, msg, err := from.ReadMessage()
		if err != nil {
			reason := "The other side of the terminal closed."
			code := wsock.CloseNormal
			var ce *wsock.CloseError
			if errors.As(err, &ce) && ce.Reason != "" {
				reason, code = ce.Reason, ce.Code
			} else if idle > 0 && isTimeout(err) {
				reason, code = "The terminal closed after "+duration(idle)+" with no input.", wsock.ClosePolicy
			}
			r.end(s, code, reason)
			return
		}
		if err := to.WriteMessage(kind, msg); err != nil {
			r.end(s, wsock.CloseGoingAway, "The other side of the terminal closed.")
			return
		}
	}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func (r *Relay) ping(s *session, conns ...*wsock.Conn) {
	if r.PingInterval <= 0 {
		return
	}
	t := time.NewTicker(r.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			for _, c := range conns {
				c.Ping()
			}
		}
	}
}

// end closes the connections of a session with a reason, one time.
func (r *Relay) end(s *session, code int, reason string) {
	s.once.Do(func() {
		r.mu.Lock()
		close(s.done)
		s.timer.Stop()
		if r.sessions[s.id] == s {
			delete(r.sessions, s.id)
		}
		conns := make([]*wsock.Conn, 0, 2)
		for _, c := range s.conns {
			conns = append(conns, c)
		}
		r.mu.Unlock()
		for _, c := range conns {
			c.WriteClose(code, reason)
			c.Close()
		}
		slog.Info("terminal relay: closed", "session", s.id, "reason", reason)
	})
}

// Shutdown closes all sessions, for example when the relay stops.
func (r *Relay) Shutdown(context.Context) {
	r.mu.Lock()
	all := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		all = append(all, s)
	}
	r.mu.Unlock()
	for _, s := range all {
		r.end(s, wsock.CloseGoingAway, "The terminal relay of the panel stopped.")
	}
}

// duration gives a time for a user, for example "12 hours" or "30 minutes".
func duration(d time.Duration) string {
	unit, n := "second", int(d/time.Second)
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		unit, n = "hour", int(d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		unit, n = "minute", int(d/time.Minute)
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
