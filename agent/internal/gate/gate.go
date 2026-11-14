// Package gate is the connection between the two processes of the agent.
//
// Second audit, the privilege boundary: the agent service has a worker and a root helper.
// The worker is the user cf-agent. It has the connection to the panel, the jobs and the
// state. It cannot change the host. The root helper is root. It has no network connection
// to the panel, and it does only the operations of a fixed list:
//
//   - a command that the policy of the helper accepts (Server.Allow), and
//   - an operation with a name and typed values (Server.Ops).
//
// The worker asks the helper through a Unix socket. Each request has its own connection.
// The helper accepts a connection only from the user of the worker (and from root).
package gate

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"sync"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// Gate is all that the worker can do with the rights of root.
type Gate interface {
	// The commands go to the helper, which runs only a command that its policy accepts.
	execx.Runner
	// Call runs one operation of the list of the helper. in and out are JSON values. out
	// can be nil.
	Call(ctx context.Context, op string, in, out any) error
	// Dial opens a connection to a local socket or a local address of the list of the helper,
	// for example the admin socket of the edge proxy.
	Dial(ctx context.Context, name string) (net.Conn, error)
}

// The kinds of a request.
const (
	kindRun    = "run"
	kindPipe   = "pipe"
	kindStream = "stream"
	kindCall   = "call"
	kindDial   = "dial"
)

// request is the first message of a connection.
type request struct {
	Kind string `json:"kind"`
	// Argv is the program and its arguments (run, pipe, stream).
	Argv []string `json:"argv,omitempty"`
	// To is the second command of a pipe.
	To []string `json:"to,omitempty"`
	// Env has more environment variables for the command (run).
	Env []string `json:"env,omitempty"`
	// Stdin is the input of the command (run).
	Stdin string `json:"stdin,omitempty"`
	// SendsInput: the worker sends the input of the command in more messages (stream).
	SendsInput bool `json:"sendsInput,omitempty"`
	// WantsOutput: the helper sends the output of the command in more messages (stream).
	WantsOutput bool `json:"wantsOutput,omitempty"`
	// Op and In are the name and the values of an operation (call).
	Op string          `json:"op,omitempty"`
	In json.RawMessage `json:"in,omitempty"`
	// Name is the name of a socket (dial).
	Name string `json:"name,omitempty"`
}

// response is the last message of the helper.
type response struct {
	// Out is the standard output of a command (run).
	Out string `json:"out,omitempty"`
	// Result is the result of an operation (call).
	Result json.RawMessage `json:"result,omitempty"`
	// Failed is true when the command or the operation gave an error. Err is its text.
	Failed bool   `json:"failed,omitempty"`
	Err    string `json:"err,omitempty"`
	// Codes are the names of the known errors that the error is (RegisterError).
	Codes []string `json:"codes,omitempty"`
}

// The types of a message. Each message is one type byte, the length of the content as 4
// bytes, and the content.
const (
	msgRequest  = 'Q'
	msgInput    = 'I'
	msgInputEnd = 'E'
	msgOutput   = 'O'
	msgResponse = 'R'
)

// maxMessage is the maximum size of one message. A request with a new agent binary is the
// largest message.
const maxMessage = 256 << 20

func writeMessage(w io.Writer, kind byte, content []byte) error {
	head := make([]byte, 5, 5+len(content))
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(content)))
	_, err := w.Write(append(head, content...))
	return err
}

func readMessage(r io.Reader) (kind byte, content []byte, err error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > maxMessage {
		return 0, nil, fmt.Errorf("gate: a message of %d bytes is too large", n)
	}
	content = make([]byte, n)
	if _, err := io.ReadFull(r, content); err != nil {
		return 0, nil, err
	}
	return head[0], content, nil
}

// Error is an error of the helper on the side of the worker. Its text is the text of the
// error in the helper.
type Error struct {
	Text  string
	Codes []string
}

func (e *Error) Error() string { return e.Text }

// Is reports whether the error in the helper was the known error target.
func (e *Error) Is(target error) bool {
	known.Lock()
	defer known.Unlock()
	for _, code := range e.Codes {
		if known.errs[code] == target {
			return true
		}
	}
	return false
}

var known = struct {
	sync.Mutex
	errs map[string]error
}{errs: map[string]error{"not-exist": fs.ErrNotExist, "exist": fs.ErrExist}}

// RegisterError gives a known error a name. errors.Is then finds the error on the side of
// the worker, also after it went through the gate.
func RegisterError(code string, err error) {
	known.Lock()
	defer known.Unlock()
	known.errs[code] = err
}

// codesOf gives the names of the known errors that err is.
func codesOf(err error) []string {
	known.Lock()
	defer known.Unlock()
	var codes []string
	for code, k := range known.errs {
		if errors.Is(err, k) {
			codes = append(codes, code)
		}
	}
	return codes
}

// failure makes the response for an error.
func failure(err error) response {
	return response{Failed: true, Err: err.Error(), Codes: codesOf(err)}
}

// err gives the error of a response, or nil.
func (r response) err() error {
	if !r.Failed {
		return nil
	}
	return &Error{Text: r.Err, Codes: r.Codes}
}
