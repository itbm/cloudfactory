package actions

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/relay"
	"github.com/itbm/cloudfactory/agent/internal/wsock"
)

const (
	sessionID     = "01m82jppdpt215s2n6f4p03hwd"
	terminalStart = asSite + "env XDG_RUNTIME_DIR=/run/user/20001 podman exec -it --workdir /app -e TERM=xterm-256color cf-acme_x7k2 sh -c "
	terminalStop  = asSite + "env XDG_RUNTIME_DIR=/run/user/20001 podman exec cf-acme_x7k2 sh -c "
)

// terminalPanel is the panel of a test. It keeps the commands and the end of each session.
type terminalPanel struct {
	mu       sync.Mutex
	commands []string
	ends     []panel.TerminalEnd
	refuse   bool
	ended    chan struct{}
	tls      *tls.Config
}

func (p *terminalPanel) TerminalCommands(_ context.Context, id string, commands []panel.TerminalCommand) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refuse {
		return errors.New("panel: status 503")
	}
	for _, c := range commands {
		p.commands = append(p.commands, c.Command)
	}
	return nil
}

func (p *terminalPanel) TerminalEnd(_ context.Context, id string, e panel.TerminalEnd) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range e.Commands {
		p.commands = append(p.commands, c.Command)
	}
	p.ends = append(p.ends, e)
	close(p.ended)
	return nil
}

func (p *terminalPanel) Dialer() (*tls.Config, func(ctx context.Context, network, address string) (net.Conn, error)) {
	return p.tls, nil
}

func (p *terminalPanel) wait(t *testing.T) panel.TerminalEnd {
	t.Helper()
	select {
	case <-p.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ends[0]
}

// openTerminal starts a relay, connects a browser and runs the job of the agent.
func openTerminal(t *testing.T) (*Deps, *execx.Fake, *terminalPanel, *wsock.Conn) {
	t.Helper()
	d, f, _ := withSite(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: terminalStart, Output: "app$ "})
	r := relay.New(relay.Key("base64:test-app-key"))
	r.PingInterval = 0
	srv := httptest.NewTLSServer(r)
	t.Cleanup(srv.Close)
	p := &terminalPanel{ended: make(chan struct{}), tls: srv.Client().Transport.(*http.Transport).TLSClientConfig}
	d.TerminalPanel = p
	ticket := relay.Ticket(r.Key, relay.Browser, sessionID, time.Now().Add(time.Minute))
	browser, err := wsock.Dial(context.Background(), srv.URL+"/api/terminal/"+sessionID+"/browser?ticket="+ticket, wsock.Options{TLS: p.tls})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { browser.Close() })
	job, err := d.OpenTerminal(&agentv1.OpenTerminalRequest{JobId: jobID2, WebsiteId: websiteID, SessionId: sessionID,
		RelayUrl: srv.URL, Ticket: relay.Ticket(r.Key, relay.Agent, sessionID, time.Now().Add(time.Minute)), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if job.Key != "terminal:"+jobID2 {
		t.Errorf("key = %q", job.Key)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	if kind, msg := readMessage(t, browser); kind != wsock.Text || msg != `{"type":"paired"}` {
		t.Fatalf("got %d %q", kind, msg)
	}
	return d, f, p, browser
}

func readMessage(t *testing.T, c *wsock.Conn) (int, string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return kind, string(msg)
}

// readUntil reads output of the terminal until it contains text.
func readUntil(t *testing.T, c *wsock.Conn, text string) string {
	t.Helper()
	var screen strings.Builder
	for !strings.Contains(screen.String(), text) {
		_, msg := readMessage(t, c)
		screen.WriteString(msg)
	}
	return screen.String()
}

func TestTheTerminalConnectsTheShellOfTheWebsiteToTheBrowser(t *testing.T) {
	d, f, p, browser := openTerminal(t)
	readUntil(t, browser, "app$ ")
	browser.WriteMessage(wsock.Binary, []byte("ls\r"))
	readUntil(t, browser, "ls\r\napp$ ")
	browser.WriteMessage(wsock.Text, []byte(`{"type":"resize","cols":120,"rows":40}`))
	// A size that is not valid has no effect.
	browser.WriteMessage(wsock.Text, []byte(`{"type":"resize","cols":2,"rows":40}`))
	browser.WriteMessage(wsock.Binary, []byte("wp plugin list\r"))
	readUntil(t, browser, "wp plugin list\r\napp$ ")
	browser.WriteMessage(wsock.Binary, []byte("exit\r"))
	if kind, msg := readMessage(t, browser); kind != wsock.Text || msg != `{"type":"exit","code":0}` {
		t.Fatalf("got %d %q", kind, msg)
	}
	browser.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := browser.ReadMessage()
	var ce *wsock.CloseError
	if !errors.As(err, &ce) || ce.Reason != "The shell ended." {
		t.Fatalf("got %v", err)
	}

	end := p.wait(t)
	if end.Reason != "The shell ended." || end.ExitCode == nil || *end.ExitCode != 0 || end.RecordingCut {
		t.Errorf("end = %+v", end)
	}
	if !strings.Contains(strings.SplitN(end.Recording, "\n", 2)[0], `"height":24,`) || !strings.Contains(end.Recording, `"o","wp plugin list\r\napp$ "]`) || !strings.Contains(end.Recording, `"r","120x40"]`) {
		t.Errorf("recording = %q", end.Recording)
	}
	p.mu.Lock()
	if want := []string{"ls", "wp plugin list"}; !slices.Equal(p.commands, want) {
		t.Errorf("commands = %q, want %q", p.commands, want)
	}
	p.mu.Unlock()
	// The shell runs as the website user, with podman exec in the container of the website.
	call, _ := called(f, terminalStart)
	if !strings.HasSuffix(call, " sh "+sessionID+" 1800") || !strings.Contains(call, `exec bash -l; fi; exec sh -l`) {
		t.Errorf("call = %q", call)
	}
	if sizes, want := f.TerminalSizes(), []execx.TerminalSize{{Cols: 80, Rows: 24}, {Cols: 120, Rows: 40}}; !slices.Equal(sizes, want) {
		t.Errorf("sizes = %v, want %v", sizes, want)
	}
	// The shell ended by itself, so the agent does not stop it.
	if c, _ := called(f, terminalStop); c != "" {
		t.Errorf("call = %q", c)
	}
	d.WaitTerminals(time.Second)
	if !d.terminals.start(websiteID, terminalSiteSlots) || !d.terminals.start(websiteID, terminalSiteSlots) {
		t.Error("the session did not give back its slot")
	}
}

func TestTheAgentStopsTheShellWhenTheBrowserCloses(t *testing.T) {
	_, f, p, browser := openTerminal(t)
	readUntil(t, browser, "app$ ")
	browser.WriteMessage(wsock.Binary, []byte("tail -f storage/logs/laravel.log\r"))
	readUntil(t, browser, "laravel.log\r\n")
	browser.WriteClose(wsock.CloseNormal, "")
	end := p.wait(t)
	if end.Reason != "The other side of the terminal closed." || end.ExitCode != nil {
		t.Errorf("end = %+v", end)
	}
	if call, _ := called(f, terminalStop); !strings.HasSuffix(call, " sh "+sessionID) || !strings.Contains(call, `kill -HUP "$p"`) {
		t.Errorf("call = %q", call)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if want := []string{"tail -f storage/logs/laravel.log"}; !slices.Equal(p.commands, want) {
		t.Errorf("commands = %q, want %q", p.commands, want)
	}
}

func TestTheTerminalClosesWhenThePanelDoesNotGetTheCommands(t *testing.T) {
	old := terminalLogWait
	terminalLogWait = 0
	t.Cleanup(func() { terminalLogWait = old })
	_, _, p, browser := openTerminal(t)
	p.mu.Lock()
	p.refuse = true
	p.mu.Unlock()
	readUntil(t, browser, "app$ ")
	browser.WriteMessage(wsock.Binary, []byte("ls\r"))
	browser.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, _, err := browser.ReadMessage()
		var ce *wsock.CloseError
		if errors.As(err, &ce) {
			if ce.Reason != terminalNoLog {
				t.Fatalf("reason = %q", ce.Reason)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// The end report has the command.
	if end := p.wait(t); len(end.Commands) != 1 || end.Commands[0].Command != "ls" {
		t.Errorf("end = %+v", end)
	}
}

func TestOpenTerminalFailsWhenTheBrowserDoesNotConnect(t *testing.T) {
	d, _, _ := withSite(t)
	r := relay.New(relay.Key("base64:test-app-key"))
	r.PairTimeout = 100 * time.Millisecond
	srv := httptest.NewTLSServer(r)
	t.Cleanup(srv.Close)
	d.TerminalPanel = &terminalPanel{ended: make(chan struct{}), tls: srv.Client().Transport.(*http.Transport).TLSClientConfig}
	job, err := d.OpenTerminal(&agentv1.OpenTerminalRequest{JobId: jobID2, WebsiteId: websiteID, SessionId: sessionID,
		RelayUrl: srv.URL, Ticket: relay.Ticket(r.Key, relay.Agent, sessionID, time.Now().Add(time.Minute)), Cols: 80, Rows: 24})
	if err := runSteps(t, job, err); err == nil || err.Error() != "The other side of the terminal did not connect." {
		t.Fatalf("err = %v", err)
	}
	// A ticket that the relay refuses.
	job, err = d.OpenTerminal(&agentv1.OpenTerminalRequest{JobId: jobID2, WebsiteId: websiteID, SessionId: sessionID,
		RelayUrl: srv.URL, Ticket: "1.00", Cols: 80, Rows: 24})
	if err := runSteps(t, job, err); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	// The failed jobs gave back their slots.
	if !d.terminals.start(websiteID, terminalSiteSlots) || !d.terminals.start(websiteID, terminalSiteSlots) || d.terminals.start(websiteID, terminalSiteSlots) {
		t.Error("the slots are not free")
	}
	d.terminals.end(websiteID)
	d.terminals.end(websiteID)

	job, _ = d.OpenTerminal(&agentv1.OpenTerminalRequest{JobId: jobID2, WebsiteId: jobID, SessionId: sessionID, RelayUrl: srv.URL, Ticket: "x", Cols: 80, Rows: 24})
	if err := runSteps(t, job, nil); !errors.Is(err, ErrUnknownWebsite) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenTerminalRefusesBadInput(t *testing.T) {
	d, _, _ := withSite(t)
	good := func() *agentv1.OpenTerminalRequest {
		return &agentv1.OpenTerminalRequest{JobId: jobID2, WebsiteId: websiteID, SessionId: sessionID, RelayUrl: "https://panel.example.com", Ticket: "1.ab", Cols: 80, Rows: 24}
	}
	if _, err := d.OpenTerminal(good()); err != nil {
		t.Fatal(err)
	}
	// The relay on the panel server has no TLS. It is on the private service address.
	r := good()
	r.RelayUrl = "http://169.254.77.1:19082"
	if _, err := d.OpenTerminal(r); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*agentv1.OpenTerminalRequest){
		"session":     func(r *agentv1.OpenTerminalRequest) { r.SessionId = "../x" },
		"http":        func(r *agentv1.OpenTerminalRequest) { r.RelayUrl = "http://panel.example.com" },
		"other port":  func(r *agentv1.OpenTerminalRequest) { r.RelayUrl = "http://169.254.77.1:19080" },
		"path":        func(r *agentv1.OpenTerminalRequest) { r.RelayUrl = "https://panel.example.com/x" },
		"user":        func(r *agentv1.OpenTerminalRequest) { r.RelayUrl = "https://a:b@panel.example.com" },
		"scheme":      func(r *agentv1.OpenTerminalRequest) { r.RelayUrl = "file:///etc/passwd" },
		"ticket":      func(r *agentv1.OpenTerminalRequest) { r.Ticket = "" },
		"ticket line": func(r *agentv1.OpenTerminalRequest) { r.Ticket = "1.ab\r\nX: y" },
		"small":       func(r *agentv1.OpenTerminalRequest) { r.Cols = 5 },
		"large":       func(r *agentv1.OpenTerminalRequest) { r.Rows = 70000 },
	} {
		r := good()
		change(r)
		if _, err := d.OpenTerminal(r); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
