package gate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

var errKnown = errors.New("a known error")

type sum struct {
	A, B int
}

// start gives a client that is connected to a helper with a small policy. The helper runs
// the commands on this computer.
func start(t *testing.T) (*Client, *Server) {
	t.Helper()
	RegisterError("test-known", errKnown)
	dir := t.TempDir()
	echo, err := net.Listen("unix", filepath.Join(dir, "echo.sock"))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	s := &Server{
		Run: execx.Host{},
		Allow: func(c Command) error {
			switch c.Argv[0] {
			case "echo", "cat", "sleep", "tr", "sh", "env":
				return nil
			}
			return errors.New("not in the test list")
		},
		Ops: map[string]Handler{
			"add":  Op(func(_ context.Context, in sum) (int, error) { return in.A + in.B, nil }),
			"none": Op(func(context.Context, None) (None, error) { return None{}, nil }),
			"gone": Op(func(context.Context, None) (None, error) {
				return None{}, &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrNotExist}
			}),
			"known": Op(func(context.Context, None) (None, error) { return None{}, errors.Join(errors.New("first"), errKnown) }),
		},
		Dials: map[string]func(context.Context) (net.Conn, error){
			"echo": func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", echo.Addr().String())
			},
		},
		UIDs: []uint32{uint32(os.Getuid())},
	}
	path := filepath.Join(dir, "gate.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(l)
	t.Cleanup(func() { l.Close(); echo.Close() })
	return &Client{Path: path}, s
}

// gates gives the two forms of the gate: a connection, and the same server in this process.
func gates(t *testing.T) map[string]Gate {
	c, s := start(t)
	return map[string]Gate{"socket": c, "local": s.Local()}
}

func TestRunOnlyAllowedCommands(t *testing.T) {
	for name, g := range gates(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			out, err := g.Run(ctx, "echo", "one", "two")
			if err != nil || out != "one two\n" {
				t.Fatalf("echo = %q, %v", out, err)
			}
			out, err = g.RunInput(ctx, "from stdin", "cat")
			if err != nil || out != "from stdin" {
				t.Fatalf("cat = %q, %v", out, err)
			}
			out, err = g.RunEnv(ctx, []string{"CF_TEST_VALUE=secret"}, "", "sh", "-c", `printf %s "$CF_TEST_VALUE"`)
			if err != nil || out != "secret" {
				t.Fatalf("env = %q, %v", out, err)
			}
			_, err = g.Run(ctx, "id")
			if err == nil || !strings.Contains(err.Error(), "not in the list") {
				t.Fatalf("a command that is not in the list was not refused: %v", err)
			}
			// The text of an error is the same on both sides.
			_, err = g.Run(ctx, "sh", "-c", "echo problem >&2; exit 7")
			if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "problem") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPipeAndStream(t *testing.T) {
	for name, g := range gates(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if err := g.Pipe(ctx, []string{"echo", "x"}, []string{"cat"}); err != nil {
				t.Fatal(err)
			}
			if err := g.Pipe(ctx, []string{"echo", "x"}, []string{"id"}); err == nil {
				t.Fatal("a pipe to a command that is not in the list was not refused")
			}
			// 3 MB go through the command and come back.
			in := bytes.Repeat([]byte("abcdefgh"), 3<<17)
			var out bytes.Buffer
			if err := g.Stream(ctx, bytes.NewReader(in), &out, "tr", "a-h", "A-H"); err != nil {
				t.Fatal(err)
			}
			if out.Len() != len(in) || !bytes.HasPrefix(out.Bytes(), []byte("ABCDEFGH")) {
				t.Fatalf("stream gave %d bytes, want %d", out.Len(), len(in))
			}
			out.Reset()
			if err := g.Stream(ctx, nil, &out, "echo", "only output"); err != nil || out.String() != "only output\n" {
				t.Fatalf("stream with no input = %q, %v", out.String(), err)
			}
			// A command that does not read its input.
			if err := g.Stream(ctx, bytes.NewReader(in), nil, "echo", "ignore"); err != nil {
				t.Fatal(err)
			}
			if err := g.Stream(ctx, nil, &out, "sh", "-c", "exit 3"); err == nil || !strings.Contains(err.Error(), "exit status 3") {
				t.Fatalf("stream error = %v", err)
			}
		})
	}
}

func TestCallOperations(t *testing.T) {
	for name, g := range gates(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var got int
			if err := g.Call(ctx, "add", sum{A: 2, B: 3}, &got); err != nil || got != 5 {
				t.Fatalf("add = %d, %v", got, err)
			}
			if err := g.Call(ctx, "none", nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := g.Call(ctx, "remove-all", nil, nil); err == nil || !strings.Contains(err.Error(), "not in the list") {
				t.Fatalf("an operation that is not in the list was not refused: %v", err)
			}
			if err := g.Call(ctx, "add", map[string]any{"A": 1, "path": "/etc"}, &got); err == nil {
				t.Fatal("a value that the operation does not have was not refused")
			}
			err := g.Call(ctx, "gone", nil, nil)
			if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, errKnown) {
				t.Fatalf("the error is not fs.ErrNotExist only: %v", err)
			}
			err = g.Call(ctx, "known", nil, nil)
			if !errors.Is(err, errKnown) || !strings.Contains(err.Error(), "first") {
				t.Fatalf("the known error did not go through the gate: %v", err)
			}
		})
	}
}

func TestDialOnlyListedSockets(t *testing.T) {
	for name, g := range gates(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			conn, err := g.Dial(ctx, "echo")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte("hello")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 5)
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
				t.Fatalf("echo = %q, %v", buf, err)
			}
			if _, err := g.Dial(ctx, "docker"); err == nil {
				t.Fatal("a socket that is not in the list was not refused")
			}
		})
	}
}

// The time limit of a job stops the command in the helper.
func TestTheEndOfTheRequestStopsTheCommand(t *testing.T) {
	c, _ := start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin := time.Now()
	_, err := c.Run(ctx, "sleep", "30")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if time.Since(begin) > 5*time.Second {
		t.Fatal("the request did not stop at its time limit")
	}
}

func TestOnlyTheWorkerUserCanConnect(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can always connect")
	}
	c, s := start(t)
	s.UIDs = nil
	if _, err := c.Run(context.Background(), "echo", "x"); err == nil {
		t.Fatal("a user that is not in the list could connect")
	}
}
