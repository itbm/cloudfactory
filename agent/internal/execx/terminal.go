package execx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// TerminalSize is the size of a terminal, in characters.
type TerminalSize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Valid reports whether the size is in the limits of a terminal of the panel.
func (s TerminalSize) Valid() bool {
	return s.Cols >= 10 && s.Cols <= 1000 && s.Rows >= 2 && s.Rows <= 500
}

// TerminalRunner runs a command in a new pseudo-terminal, for the terminal of a website.
type TerminalRunner interface {
	// Terminal starts the command in a new pseudo-terminal of the size. The keys come from
	// input, and output receives what the terminal shows. A size from resize changes the size
	// of the terminal. Terminal gives the exit code of the command when the command ends. When
	// the context ends, Terminal stops the command.
	Terminal(ctx context.Context, size TerminalSize, input io.Reader, output io.Writer, resize <-chan TerminalSize, name string, args ...string) (int, error)
}

// openPTY opens a new pseudo-terminal: the master side for the agent, and the slave side for
// the command.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	var n int
	err = control(master, func(fd int) error {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return err
		}
		var err error
		n, err = unix.IoctlGetInt(fd, unix.TIOCGPTN)
		return err
	})
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open a pseudo-terminal: %w", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// control runs fn with the descriptor of the file. It does not change the file to blocking
// mode, as File.Fd does, so a Close stops a Read in a different goroutine.
func control(f *os.File, fn func(fd int) error) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := raw.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

func setSize(master *os.File, size TerminalSize) error {
	return control(master, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: size.Rows, Col: size.Cols})
	})
}

// Terminal runs the command in a new pseudo-terminal. The command is the leader of a new
// session, and the terminal is its controlling terminal. When the context ends, the session
// gets SIGHUP and, after stopDelay, SIGKILL.
func (h Host) Terminal(ctx context.Context, size TerminalSize, input io.Reader, output io.Writer, resize <-chan TerminalSize, name string, args ...string) (int, error) {
	if !size.Valid() {
		return -1, fmt.Errorf("execx: the terminal size %dx%d is not valid", size.Cols, size.Rows)
	}
	master, slave, err := openPTY()
	if err != nil {
		return -1, err
	}
	defer master.Close()
	if err := setSize(master, size); err != nil {
		slave.Close()
		return -1, err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = Dir
	cmd.Env = append(append(serviceEnviron(cmd.Environ()), h.Env...), "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	err = cmd.Start()
	slave.Close()
	if err != nil {
		return -1, err
	}
	pid := cmd.Process.Pid
	stop := context.AfterFunc(ctx, func() {
		_ = syscall.Kill(-pid, syscall.SIGHUP)
		time.AfterFunc(stopDelay, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	})
	defer stop()

	// The output ends when each program of the session closed the terminal: then a read
	// gives EIO.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, _ = io.Copy(output, master)
	}()
	go func() {
		if input != nil {
			_, _ = io.Copy(master, input)
		}
	}()
	resized := make(chan struct{})
	defer close(resized)
	go func() {
		for {
			select {
			case s, ok := <-resize:
				if !ok {
					return
				}
				if s.Valid() {
					_ = setSize(master, s)
				}
			case <-resized:
				return
			}
		}
	}()
	err = cmd.Wait()
	// A program that left the session can keep the terminal open. Do not wait for it.
	select {
	case <-outDone:
	case <-time.After(2 * time.Second):
	}
	if ctx.Err() != nil {
		return exitCode(cmd.ProcessState), ctx.Err()
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return -1, err
	}
	return exitCode(cmd.ProcessState), nil
}

// exitCode gives the exit code of a command, or 128 and the number of the signal that
// stopped it, as a shell does.
func exitCode(s *os.ProcessState) int {
	if s == nil {
		return -1
	}
	if ws, ok := s.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return s.ExitCode()
}

// Terminal records the command. It writes the output of the rule, and then shows each key
// that it receives, as a terminal with echo does, until the input ends or the context ends.
// The exit code is 0, or 1 when the rule has an error.
// TerminalSizes gives the sizes of the terminals: the first size of each terminal and each new
// size.
func (f *Fake) TerminalSizes() []TerminalSize {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TerminalSize(nil), f.Sizes...)
}

func (f *Fake) Terminal(ctx context.Context, size TerminalSize, input io.Reader, output io.Writer, resize <-chan TerminalSize, name string, args ...string) (int, error) {
	out, err := f.RunEnv(ctx, nil, "", name, args...)
	f.mu.Lock()
	f.Sizes = append(f.Sizes, size)
	f.mu.Unlock()
	if out != "" {
		if _, werr := io.WriteString(output, out); werr != nil {
			return -1, werr
		}
	}
	if err != nil {
		return 1, nil
	}
	var mu sync.Mutex
	go func() {
		for s := range resize {
			mu.Lock()
			f.mu.Lock()
			f.Sizes = append(f.Sizes, s)
			f.mu.Unlock()
			mu.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if input == nil {
			return
		}
		buf := make([]byte, 1024)
		for {
			n, rerr := input.Read(buf)
			if n > 0 {
				text := string(buf[:n])
				// "exit" and Enter end the fake shell.
				if strings.Contains(text, "exit\r") {
					return
				}
				// The fake shell echoes the keys. After each Enter, it shows the output of the
				// rule again, as a prompt.
				if _, werr := io.WriteString(output, strings.ReplaceAll(text, "\r", "\r\n"+out)); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case <-done:
		return 0, nil
	case <-ctx.Done():
		return 129, ctx.Err()
	}
}
