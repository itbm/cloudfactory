package panel

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// TerminalCommand is one command that a user typed in the web terminal of a website.
type TerminalCommand struct {
	Command string    `json:"command"`
	At      time.Time `json:"at"`
}

// TerminalEnd is the end of a session of the web terminal.
type TerminalEnd struct {
	// Reason is the text that the browser showed, for example "The shell ended.".
	Reason string `json:"reason"`
	// ExitCode is the exit code of the shell, or nil when the shell did not end by itself.
	ExitCode *int `json:"exitCode"`
	// Commands are the commands that the panel did not get yet.
	Commands []TerminalCommand `json:"commands"`
	// Recording is the output of the terminal in the asciicast v2 format, and RecordingCut is
	// true when the recording stopped at its maximum size.
	Recording    string    `json:"recording"`
	RecordingCut bool      `json:"recordingCut"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt"`
}

// TerminalCommands adds commands to the command log of a session of the web terminal.
func (c *Client) TerminalCommands(ctx context.Context, sessionID string, commands []TerminalCommand) error {
	return c.post(ctx, "/api/agent/v1/terminal-sessions/"+sessionID+"/commands", map[string]any{"commands": commands}, nil)
}

// TerminalEnd reports the end of a session of the web terminal, with its recording.
func (c *Client) TerminalEnd(ctx context.Context, sessionID string, e TerminalEnd) error {
	if e.Commands == nil {
		e.Commands = []TerminalCommand{}
	}
	return c.post(ctx, "/api/agent/v1/terminal-sessions/"+sessionID+"/end", e, nil)
}

// Dialer gives the TLS settings and the dial function of the client, for a connection that is
// not HTTP, for example the WebSocket of the terminal relay. The connection then has the same
// checks of the certificate of the panel as the reports.
func (c *Client) Dialer() (*tls.Config, func(ctx context.Context, network, address string) (net.Conn, error)) {
	tr, ok := c.HTTP.Transport.(*http.Transport)
	if !ok {
		return nil, nil
	}
	return tr.TLSClientConfig, tr.DialContext
}
