package termlog

import (
	"context"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// TestTheCommandsOfARealBash runs bash with readline in a pseudo-terminal, and checks the
// commands that Tracker finds in its output.
func TestTheCommandsOfARealBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this computer")
	}
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no pseudo-terminals on this computer")
	}
	var mu sync.Mutex
	var commands []string
	var screen strings.Builder
	tr := NewTracker(40, func(c string) { commands = append(commands, c) })
	in, keys := io.Pipe()
	out := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		tr.Output(p)
		screen.Write(p)
		return len(p), nil
	})
	press := func(k string) {
		mu.Lock()
		tr.Input([]byte(k))
		mu.Unlock()
		keys.Write([]byte(k))
		time.Sleep(15 * time.Millisecond)
	}
	typeText := func(s string) {
		for _, r := range s {
			press(string(r))
		}
	}
	waitPrompt := func(n int) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			got := strings.Count(screen.String(), "test$ ")
			mu.Unlock()
			if got >= n {
				return
			}
			if time.Now().After(deadline) {
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("no prompt %d in %q", n, screen.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		execx.Host{}.Terminal(context.Background(), execx.TerminalSize{Cols: 40, Rows: 20}, in, out, nil,
			"env", "PS1=test$ ", "HISTFILE=/dev/null", "INPUTRC=/dev/null", bash, "--norc", "--noprofile", "-i")
	}()
	waitPrompt(1)
	typeText("echo hello")
	press("\r")
	waitPrompt(2)
	typeText("ech")
	press("\x7f")
	typeText("ho x")
	press("\r")
	waitPrompt(3)
	// The previous command from the history.
	press("\x1b[A")
	press("\r")
	waitPrompt(4)
	long := "echo " + strings.Repeat("abcdefghij", 7)
	typeText(long)
	press("\r")
	waitPrompt(5)
	typeText("read -s -p 'pw: ' p")
	press("\r")
	time.Sleep(200 * time.Millisecond)
	typeText("secret")
	press("\r")
	waitPrompt(6)
	typeText("exit")
	press("\r")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("bash did not end")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"echo hello", "echo x", "echo x", long, "read -s -p 'pw: ' p", "exit"}
	if !slices.Equal(commands, want) {
		t.Fatalf("got %q, want %q\nscreen: %q", commands, want, screen.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
