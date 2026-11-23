package execx

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a buffer that the output goroutine and the test can use at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor waits until the output has the text.
func waitFor(t *testing.T, out *syncBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("the output %q does not have %q", out.String(), text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHostRunsACommandInAPseudoTerminal(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no pseudo-terminals on this computer")
	}
	in, keys := io.Pipe()
	out := &syncBuffer{}
	resize := make(chan TerminalSize, 1)
	done := make(chan struct{})
	var code int
	var err error
	go func() {
		defer close(done)
		code, err = Host{}.Terminal(context.Background(), TerminalSize{Cols: 100, Rows: 30}, in, out, resize,
			"sh", "-c", `stty size; [ -t 0 ] && echo tty; read line; stty size; echo "got $line"; exit 3`)
	}()
	waitFor(t, out, "30 100")
	waitFor(t, out, "tty")
	resize <- TerminalSize{Cols: 120, Rows: 40}
	time.Sleep(200 * time.Millisecond)
	keys.Write([]byte("hello\r"))
	<-done
	if err != nil || code != 3 {
		t.Fatalf("code %d, err %v", code, err)
	}
	// The terminal shows the keys (echo) and the output of the command.
	for _, want := range []string{"hello", "40 120", "got hello"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the output %q does not have %q", out.String(), want)
		}
	}
}

func TestTheEndOfTheContextStopsTheTerminal(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no pseudo-terminals on this computer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan error)
	go func() {
		_, err := Host{}.Terminal(ctx, TerminalSize{Cols: 80, Rows: 24}, nil, out, nil, "sh", "-c", "echo ready; sleep 60")
		done <- err
	}()
	waitFor(t, out, "ready")
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want the error of the context")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not stop")
	}
	if _, err := (Host{}).Terminal(context.Background(), TerminalSize{Cols: 5, Rows: 24}, nil, io.Discard, nil, "true"); err == nil {
		t.Fatal("a terminal of 5 columns is not valid")
	}
}
