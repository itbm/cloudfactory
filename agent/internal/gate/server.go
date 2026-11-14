package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// Command is a command that the worker asks for.
type Command struct {
	// Argv is the program and its arguments.
	Argv []string
	// Env has more environment variables for the command.
	Env []string
}

func (c Command) String() string { return strings.Join(c.Argv, " ") }

// Handler runs one operation. in is the JSON value of the worker. The result becomes JSON.
type Handler func(ctx context.Context, in json.RawMessage) (any, error)

// Op makes a Handler from a function with typed values. The function must check its
// values: the worker is not trusted.
func Op[In, Out any](fn func(ctx context.Context, in In) (Out, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in In
		if len(raw) > 0 {
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				return nil, fmt.Errorf("gate: the values of the operation are not valid: %w", err)
			}
		}
		return fn(ctx, in)
	}
}

// None is the value of an operation with no values or no result.
type None struct{}

// Server is the side of the root helper. The lists Allow, Ops and Dial are all that the
// worker can do with the rights of root.
type Server struct {
	// Run runs a command that Allow accepts.
	Run execx.Runner
	// Allow is the command policy. It gives an error for a command that the worker cannot
	// run. Nil refuses all commands.
	Allow func(Command) error
	// Ops are the operations with typed values.
	Ops map[string]Handler
	// Dial opens a connection to a local socket or a local address for the worker. It gives
	// an error for a name that is not in its list. Nil refuses all names.
	Dial func(ctx context.Context, name string) (net.Conn, error)
	// UIDs are the users that can connect, apart from root. It is the user of the worker.
	UIDs []uint32
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// peerUID gives the user of the process at the other end of a Unix socket.
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("gate: not a Unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	uid, err := peerUID(conn)
	if err != nil || (uid != 0 && !slices.Contains(s.UIDs, uid)) {
		slog.Warn("root helper: refused a connection", "uid", uid, "err", err)
		return
	}
	kind, content, err := readMessage(conn)
	if err != nil || kind != msgRequest {
		return
	}
	var req request
	if err := json.Unmarshal(content, &req); err != nil {
		s.respond(conn, failure(fmt.Errorf("gate: the request is not valid: %w", err)))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if req.Kind == kindDial {
		s.relay(ctx, conn, req)
		return
	}

	// The worker closes the connection when it stops to wait, for example at the time limit
	// of a job. The command then stops too.
	input, inputWriter := io.Pipe()
	// A command that does not read all its input must not stop the reader of the connection.
	defer input.Close()
	go func() {
		defer cancel()
		for {
			kind, content, err := readMessage(conn)
			switch {
			case err != nil:
				inputWriter.CloseWithError(io.ErrUnexpectedEOF)
				return
			case kind == msgInput:
				if _, err := inputWriter.Write(content); err != nil {
					return
				}
			case kind == msgInputEnd:
				inputWriter.Close()
				// Continue to read: the end of the connection stops the command.
			}
		}
	}()

	var out sync.Mutex
	resp := s.do(ctx, req, input, writerFunc(func(p []byte) (int, error) {
		out.Lock()
		defer out.Unlock()
		if err := writeMessage(conn, msgOutput, p); err != nil {
			return 0, err
		}
		return len(p), nil
	}))
	out.Lock()
	defer out.Unlock()
	s.respond(conn, resp)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func (s *Server) respond(conn net.Conn, resp response) {
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(failure(err))
	}
	_ = writeMessage(conn, msgResponse, b)
}

// allow checks a command against the policy.
func (s *Server) allow(argv, env []string) error {
	c := Command{Argv: argv, Env: env}
	if len(argv) == 0 {
		return errors.New("root helper: no command")
	}
	if s.Allow == nil {
		return fmt.Errorf("root helper: the command is not in the list: %s", c)
	}
	if err := s.Allow(c); err != nil {
		slog.Warn("root helper: refused a command", "command", c.String(), "err", err)
		return fmt.Errorf("root helper: the command is not in the list: %s: %w", c, err)
	}
	return nil
}

// do runs one request of the kinds run, pipe, stream and call.
func (s *Server) do(ctx context.Context, req request, input io.Reader, output io.Writer) response {
	switch req.Kind {
	case kindRun:
		if err := s.allow(req.Argv, req.Env); err != nil {
			return failure(err)
		}
		out, err := s.Run.RunEnv(ctx, req.Env, req.Stdin, req.Argv[0], req.Argv[1:]...)
		if err != nil {
			resp := failure(err)
			resp.Out = out
			return resp
		}
		return response{Out: out}
	case kindPipe:
		if err := errors.Join(s.allow(req.Argv, nil), s.allow(req.To, nil)); err != nil {
			return failure(err)
		}
		if err := s.Run.Pipe(ctx, req.Argv, req.To); err != nil {
			return failure(err)
		}
		return response{}
	case kindStream:
		if err := s.allow(req.Argv, nil); err != nil {
			return failure(err)
		}
		var in io.Reader
		if req.SendsInput {
			in = input
		}
		var out io.Writer
		if req.WantsOutput {
			out = output
		}
		if err := s.Run.Stream(ctx, in, out, req.Argv[0], req.Argv[1:]...); err != nil {
			return failure(err)
		}
		return response{}
	case kindCall:
		return s.call(ctx, req.Op, req.In)
	}
	return failure(fmt.Errorf("root helper: the request kind %q is not valid", req.Kind))
}

func (s *Server) call(ctx context.Context, op string, in json.RawMessage) response {
	h := s.Ops[op]
	if h == nil {
		slog.Warn("root helper: refused an operation", "operation", op)
		return failure(fmt.Errorf("root helper: the operation %q is not in the list", op))
	}
	result, err := h(ctx, in)
	if err != nil {
		return failure(err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return failure(err)
	}
	return response{Result: b}
}

// relay connects the worker to a local socket of the list.
func (s *Server) relay(ctx context.Context, conn net.Conn, req request) {
	target, err := s.dial(ctx, req.Name)
	if err != nil {
		s.respond(conn, failure(err))
		return
	}
	defer target.Close()
	s.respond(conn, response{})
	done := make(chan struct{}, 2)
	go func() { io.Copy(target, conn); closeWrite(target); done <- struct{}{} }()
	go func() { io.Copy(conn, target); closeWrite(conn); done <- struct{}{} }()
	<-done
	<-done
}

// dial opens a connection of the list of the helper.
func (s *Server) dial(ctx context.Context, name string) (net.Conn, error) {
	if s.Dial == nil {
		return nil, fmt.Errorf("root helper: the connection %q is not in the list", name)
	}
	conn, err := s.Dial(ctx, name)
	if err != nil {
		slog.Warn("root helper: refused a connection", "name", name, "err", err)
		return nil, fmt.Errorf("root helper: the connection %q: %w", name, err)
	}
	return conn, nil
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

// Local gives a Gate in this process. It uses the same lists and the same checks as a
// connection from the worker. Tests use it, and an agent that operates as one root process.
func (s *Server) Local() Gate { return local{s} }

type local struct{ s *Server }

func (l local) Run(ctx context.Context, name string, args ...string) (string, error) {
	return l.RunEnv(ctx, nil, "", name, args...)
}

func (l local) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return l.RunEnv(ctx, nil, stdin, name, args...)
}

func (l local) RunEnv(ctx context.Context, env []string, stdin, name string, args ...string) (string, error) {
	resp := l.s.do(ctx, request{Kind: kindRun, Argv: append([]string{name}, args...), Env: env, Stdin: stdin}, nil, nil)
	return resp.Out, resp.err()
}

func (l local) Pipe(ctx context.Context, from, to []string) error {
	return l.s.do(ctx, request{Kind: kindPipe, Argv: from, To: to}, nil, nil).err()
}

func (l local) Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	req := request{Kind: kindStream, Argv: append([]string{name}, args...), SendsInput: stdin != nil, WantsOutput: stdout != nil}
	return l.s.do(ctx, req, stdin, stdout).err()
}

func (l local) Call(ctx context.Context, op string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp := l.s.call(ctx, op, raw)
	if err := resp.err(); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func (l local) Dial(ctx context.Context, name string) (net.Conn, error) {
	return l.s.dial(ctx, name)
}
