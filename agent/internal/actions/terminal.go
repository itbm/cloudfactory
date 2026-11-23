package actions

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/termlog"
	"github.com/itbm/cloudfactory/agent/internal/validate"
	"github.com/itbm/cloudfactory/agent/internal/wsock"
)

// The web terminal (FR-802): a user of the panel opens a shell in the container of a website in
// the browser. The shell runs as the Unix user of the website, in the gVisor sandbox, with the
// limits of the website, the same as "Run a command".
//
// The browser and the agent both connect to the terminal relay of the panel (internal/relay).
// The agent connects out, so the server needs no new open port. The root helper runs the shell
// in a pseudo-terminal (gate.Gate.Terminal). The worker copies the keys from the browser to the
// shell and the output of the shell to the browser. It finds each command line in the output
// (termlog.Tracker) and sends it to the panel, which adds it to the command log of the website.
// When the session ends, the agent sends the recording of the output (asciicast v2).
//
// The commands must be in the log: when the panel does not get them for terminalLogWait, the
// agent closes the terminal.

const (
	// terminalSiteSlots is the number of terminals of one website that are open at the same time.
	terminalSiteSlots = 2
	// terminalPairWait is the longest time that the job waits for the browser. The relay waits
	// one minute.
	terminalPairWait = 75 * time.Second
	// terminalFlush is the time between two reports of the commands.
	terminalFlush = 2 * time.Second
	// terminalRecordingMax is the maximum size of the recording of a session.
	terminalRecordingMax = 1 << 20
	// terminalIdle closes the shell when it waits at the prompt for this time (TMOUT of bash).
	// The relay closes the session after the same time with no input.
	terminalIdle = 30 * time.Minute
	// terminalMax is the longest time of a session. The relay has the same limit.
	terminalMax = 12 * time.Hour
)

// terminalLogWait is the longest time that a command can wait for the panel. Tests change it.
var terminalLogWait = 2 * time.Minute

// TerminalPanel is the part of the client of the panel for the web terminal (panel.Client).
type TerminalPanel interface {
	TerminalCommands(ctx context.Context, sessionID string, commands []panel.TerminalCommand) error
	TerminalEnd(ctx context.Context, sessionID string, e panel.TerminalEnd) error
	// Dialer gives the TLS settings and the dial function for the connection to the relay.
	Dialer() (*tls.Config, func(ctx context.Context, network, address string) (net.Conn, error))
}

// terminalShell starts the shell in the container. It keeps its process ID in a file, so that
// the agent can stop the shell when the session ends (terminalHangUp): the processes of an
// exec session continue in the container when the "podman exec" client stops (finding G5-13).
// The arguments are the session and the idle time in seconds. The script first removes the
// files of the sessions whose shell ended.
const terminalShell = `for f in /tmp/.cf-terminal-*; do p=$(cat "$f" 2>/dev/null) && [ -n "$p" ] && ! kill -0 "$p" 2>/dev/null && rm -f "$f"; done; echo "$$" > "/tmp/.cf-terminal-$1" 2>/dev/null; export TMOUT="$2"; if command -v bash >/dev/null 2>&1; then exec bash -l; fi; exec sh -l`

// terminalHangUp stops the shell of a session in the container, as a closed terminal does.
const terminalHangUp = `p=$(cat "/tmp/.cf-terminal-$1" 2>/dev/null) && kill -HUP "$p" 2>/dev/null; rm -f "/tmp/.cf-terminal-$1"`

// The texts of the end of a session. The browser shows them.
const (
	terminalShellEnded = "The shell ended."
	terminalStopped    = "The agent of the server stopped. Open the terminal again."
	terminalNoLog      = "The terminal closed, because the panel did not get the command log."
)

// OpenTerminal opens a shell in the container of a website for the web terminal. The job
// succeeds when the browser is connected and the shell started. The session then continues
// apart from the job.
func (d *Deps) OpenTerminal(req *agentv1.OpenTerminalRequest) (jobs.Job, error) {
	size := execx.TerminalSize{Cols: uint16(min(req.GetCols(), 65535)), Rows: uint16(min(req.GetRows(), 65535))}
	err := errors.Join(validate.ID(req.GetJobId()), validate.ID(req.GetWebsiteId()), validate.ID(req.GetSessionId()), checkRelayURL(req.GetRelayUrl()))
	if err == nil && (req.GetTicket() == "" || len(req.GetTicket()) > 100 || strings.ContainsAny(req.GetTicket(), " \r\n")) {
		err = errors.New("the ticket of the terminal is not valid")
	}
	if err == nil && !size.Valid() {
		err = fmt.Errorf("the terminal size %dx%d is not valid", req.GetCols(), req.GetRows())
	}
	if err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{ID: req.GetJobId(), Key: "terminal:" + req.GetJobId(), Steps: []jobs.Step{
		{Name: "Open the terminal", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if site == nil {
				return ErrUnknownWebsite
			}
			if site.Stopped {
				return errors.New("the website is stopped; start it first")
			}
			if d.Gate == nil || d.TerminalPanel == nil {
				return errors.New("this agent cannot open a terminal")
			}
			if !d.terminals.start(site.ID, terminalSiteSlots) {
				return fmt.Errorf("%d terminals of this website are open now; close one first", terminalSiteSlots)
			}
			s, err := d.startTerminal(ctx, req, site, size)
			if err != nil {
				d.terminals.end(site.ID)
				return err
			}
			d.terminalRuns.Add(1)
			go s.run()
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"sessionId": req.GetSessionId()}
	}}, nil
}

// checkRelayURL checks the address of the relay: HTTPS, or HTTP to the relay port on the
// private service address of the panel server.
func checkRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || u.Hostname() == "" {
		return fmt.Errorf("the address of the terminal relay %q is not valid", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if u.Host == net.JoinHostPort(quadlet.ServiceAddress, strconv.Itoa(quadlet.PanelTerminalPort)) {
			return nil
		}
	}
	return fmt.Errorf("the address of the terminal relay %q must use HTTPS", raw)
}

// terminalSession is an open session of the web terminal.
type terminalSession struct {
	d       *Deps
	id      string
	site    *Site
	conn    *wsock.Conn
	term    gate.Terminal
	started time.Time
	// cancel stops the pseudo-terminal of the root helper.
	cancel context.CancelFunc

	// mu protects the fields below.
	mu      sync.Mutex
	tracker *termlog.Tracker
	rec     *termlog.Recorder
	// pending are the commands that the panel did not get yet.
	pending []panel.TerminalCommand

	once      sync.Once
	ending    chan struct{}
	reason    string
	exit      *int
	shellDone chan struct{}
}

// startTerminal connects to the relay, waits for the browser and starts the shell.
func (d *Deps) startTerminal(ctx context.Context, req *agentv1.OpenTerminalRequest, site *Site, size execx.TerminalSize) (*terminalSession, error) {
	uid, err := d.Host.LookupUID(site.UnixUser)
	if err != nil {
		return nil, errors.New("the website has no Unix user")
	}
	tlsConfig, dial := d.TerminalPanel.Dialer()
	address := strings.TrimRight(req.GetRelayUrl(), "/") + "/api/terminal/" + req.GetSessionId() + "/" + "agent"
	conn, err := wsock.Dial(ctx, address, wsock.Options{Header: http.Header{"Authorization": {"Bearer " + req.GetTicket()}}, TLS: tlsConfig, DialContext: dial})
	if err != nil {
		return nil, fmt.Errorf("connect to the terminal relay of the panel: %w", err)
	}
	// The relay sends {"type":"paired"} when the browser is connected.
	conn.SetReadDeadline(time.Now().Add(terminalPairWait))
	kind, msg, err := conn.ReadMessage()
	if err == nil && (kind != wsock.Text || string(msg) != `{"type":"paired"}`) {
		err = errors.New("the relay sent an unknown message")
	}
	if err != nil {
		conn.Close()
		var ce *wsock.CloseError
		if errors.As(err, &ce) && ce.Reason != "" {
			return nil, errors.New(ce.Reason)
		}
		return nil, fmt.Errorf("the browser did not connect to the terminal: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	start := time.Now()
	s := &terminalSession{d: d, id: req.GetSessionId(), site: site, conn: conn, started: start,
		rec: termlog.NewRecorder(start, int(size.Cols), int(size.Rows), terminalRecordingMax), ending: make(chan struct{}), shellDone: make(chan struct{})}
	s.tracker = termlog.NewTracker(int(size.Cols), func(command string) {
		// The tracker calls this with s.mu held.
		s.pending = append(s.pending, panel.TerminalCommand{Command: command, At: time.Now().UTC()})
	})
	shellCtx, cancel := context.WithCancel(context.Background())
	container := quadlet.Website{UnixUser: site.UnixUser, Suffix: site.Suffix}.ContainerName()
	term, err := d.Gate.Terminal(shellCtx, size, s, "runuser", "-u", site.UnixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(uid),
		"podman", "exec", "-it", "--workdir", "/app", "-e", "TERM=xterm-256color", container,
		"sh", "-c", terminalShell, "sh", s.id, strconv.Itoa(int(terminalIdle.Seconds())))
	if err != nil {
		cancel()
		conn.WriteClose(wsock.CloseGoingAway, "The shell did not start.")
		conn.Close()
		return nil, fmt.Errorf("start the shell: %w", err)
	}
	s.term, s.cancel = term, cancel
	return s, nil
}

// Write receives the output of the shell from the root helper and sends it to the browser.
func (s *terminalSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.tracker.Output(p)
	s.rec.Output(time.Now(), p)
	s.mu.Unlock()
	// A failed write ends the session in readBrowser.
	s.conn.WriteMessage(wsock.Binary, p)
	return len(p), nil
}

// end starts the end of the session, one time. exit is the exit code of the shell, or nil.
func (s *terminalSession) end(reason string, exit *int) {
	s.once.Do(func() {
		s.reason, s.exit = reason, exit
		close(s.ending)
	})
}

// readBrowser copies the keys of the browser to the shell. A text message is a new size of
// the terminal: {"type":"resize","cols":100,"rows":30}.
func (s *terminalSession) readBrowser() {
	for {
		kind, msg, err := s.conn.ReadMessage()
		if err != nil {
			reason := "The browser closed the terminal."
			var ce *wsock.CloseError
			if errors.As(err, &ce) && ce.Reason != "" {
				reason = ce.Reason
			}
			s.end(reason, nil)
			return
		}
		if kind == wsock.Binary {
			s.mu.Lock()
			s.tracker.Input(msg)
			s.mu.Unlock()
			if _, err := s.term.Write(msg); err != nil {
				return
			}
			continue
		}
		var m struct {
			Type string `json:"type"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		size := execx.TerminalSize{}
		if json.Unmarshal(msg, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 && m.Cols < 65536 && m.Rows < 65536 {
			size = execx.TerminalSize{Cols: uint16(m.Cols), Rows: uint16(m.Rows)}
		}
		if !size.Valid() {
			continue
		}
		s.mu.Lock()
		s.tracker.Resize(m.Cols)
		s.rec.Resize(time.Now(), m.Cols, m.Rows)
		s.mu.Unlock()
		s.term.Resize(size)
	}
}

// run operates the session until it ends, and then reports the end to the panel.
func (s *terminalSession) run() {
	defer s.d.terminalRuns.Done()
	defer s.d.terminals.end(s.site.ID)
	slog.Info("terminal: opened", "website", s.site.ID, "session", s.id)
	go s.readBrowser()
	go func() {
		defer close(s.shellDone)
		code, err := s.term.Wait()
		if err != nil {
			s.end("The terminal of the server stopped.", nil)
			return
		}
		s.end(terminalShellEnded, &code)
	}()
	limit := time.NewTimer(terminalMax)
	defer limit.Stop()
	tick := time.NewTicker(terminalFlush)
	defer tick.Stop()
loop:
	for {
		select {
		case <-s.ending:
			break loop
		case <-tick.C:
			s.flush()
		case <-limit.C:
			s.end(fmt.Sprintf("The terminal closed after %d hours.", int(terminalMax.Hours())), nil)
		case <-s.d.Stopping:
			s.end(terminalStopped, nil)
		}
	}
	if s.exit != nil {
		s.conn.WriteMessage(wsock.Text, fmt.Appendf(nil, `{"type":"exit","code":%d}`, *s.exit))
		s.conn.WriteClose(wsock.CloseNormal, s.reason)
	} else {
		s.conn.WriteClose(wsock.CloseGoingAway, s.reason)
	}
	s.conn.Close()
	s.cancel()
	if s.exit == nil {
		s.hangUp()
	}
	select {
	case <-s.shellDone:
	case <-time.After(5 * time.Second):
	}
	s.report()
	slog.Info("terminal: closed", "website", s.site.ID, "session", s.id, "reason", s.reason)
}

// hangUp stops the shell in the container.
func (s *terminalSession) hangUp() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	uid, err := s.d.Host.LookupUID(s.site.UnixUser)
	if err != nil {
		return
	}
	container := quadlet.Website{UnixUser: s.site.UnixUser, Suffix: s.site.Suffix}.ContainerName()
	if _, err := s.d.Run.Run(ctx, "runuser", "-u", s.site.UnixUser, "--", "env", "XDG_RUNTIME_DIR=/run/user/"+strconv.Itoa(uid),
		"podman", "exec", container, "sh", "-c", terminalHangUp, "sh", s.id); err != nil {
		slog.Warn("terminal: stop the shell in the container", "session", s.id, "err", err)
	}
}

// flush sends the new commands to the panel. When a command waits too long, it closes the
// terminal: each command must be in the log.
func (s *terminalSession) flush() {
	s.mu.Lock()
	commands := append([]panel.TerminalCommand(nil), s.pending...)
	s.mu.Unlock()
	if len(commands) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.d.TerminalPanel.TerminalCommands(ctx, s.id, commands); err != nil {
		slog.Warn("terminal: send the commands", "session", s.id, "err", err)
		if time.Since(commands[0].At) > terminalLogWait {
			s.end(terminalNoLog, nil)
		}
		return
	}
	s.mu.Lock()
	s.pending = s.pending[len(commands):]
	s.mu.Unlock()
}

// report sends the end of the session, the last commands and the recording to the panel.
func (s *terminalSession) report() {
	s.mu.Lock()
	recording, cut := s.rec.Recording()
	end := panel.TerminalEnd{Reason: s.reason, ExitCode: s.exit, Commands: append([]panel.TerminalCommand(nil), s.pending...),
		Recording: string(recording), RecordingCut: cut, StartedAt: s.started.UTC(), EndedAt: time.Now().UTC()}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.d.TerminalPanel.TerminalEnd(ctx, s.id, end); err != nil {
		slog.Error("terminal: report the end of the session", "session", s.id, "err", err)
	}
}

// WaitTerminals waits for the end reports of the terminals, at most for the time limit. The
// sessions end when Stopping closes.
func (d *Deps) WaitTerminals(limit time.Duration) {
	done := make(chan struct{})
	go func() {
		d.terminalRuns.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}
