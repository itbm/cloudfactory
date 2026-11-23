// Package execx runs host commands. It never uses a shell: each command is a program
// with a list of arguments. Tests use Fake instead of the host.
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
)

// Runner runs one command and returns its standard output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	// RunInput sends stdin to the command. Use it for secrets, so that they are not in the
	// process list.
	RunInput(ctx context.Context, stdin, name string, args ...string) (string, error)
	// Pipe sends the standard output of the first command to the standard input of the
	// second command, like "from | to" in a shell. Each command is a program and its arguments.
	Pipe(ctx context.Context, from, to []string) error
	// RunEnv is RunInput with more environment variables, for example the credentials of
	// restic. The values are not in the process list.
	RunEnv(ctx context.Context, env []string, stdin, name string, args ...string) (string, error)
	// Stream connects stdin and stdout of the command to a reader and a writer, so that large
	// data, for example a database dump, does not stay in memory (NFR-02). Both can be nil.
	Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error
	// StreamEnv is Stream with more environment variables, for example the credentials of
	// restic for a backup from stdin.
	//
	// When stdin gives an error, the command stops before its input closes. The command thus
	// does not see a normal end of a part of the input: restic does not make a snapshot of a
	// backup that is not complete.
	StreamEnv(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error
}

// Host runs commands on the host. Each command starts in the root directory, not in the
// working directory of the agent. Rootless Podman under runuser stops with "cannot chdir to
// /root" when the website user cannot open the working directory, for example when an
// administrator runs "cf-agent panel install" from /root.
type Host struct {
	// Env is added to the environment of each command, for example "DEBIAN_FRONTEND=noninteractive".
	Env []string
}

// Dir is the working directory of each command. Every Unix user can open it.
const Dir = "/"

// command makes a command that starts in Dir, with the extra environment of the host.
func (h Host) command(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = Dir
	cmd.Env = append(append(serviceEnviron(cmd.Environ()), h.Env...), env...)
	stopWithGroup(cmd)
	return cmd
}

// stopDelay is the time between the two signals of a command that the agent stops, and
// the time that the agent then waits for the output of the command.
const stopDelay = 5 * time.Second

// stopWithGroup makes the end of the context stop the command and each program that the
// command started. Without it, Go stops only the first program: runuser stopped, the
// command of the website user continued, and the agent waited for the output of that command
// with no limit (adversarial review, finding G5-02).
//
// The command gets a process group of its own. When the context ends, the group gets SIGTERM
// and, after stopDelay, SIGKILL. After one more stopDelay, the agent closes the output of the
// command and does not wait for a program that left the group.
func stopWithGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		time.AfterFunc(stopDelay, func() {
			// The group has the ID of the first program. The ID stays reserved while a
			// program of the group runs, so this signal cannot go to a new command.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
		return err
	}
	cmd.WaitDelay = 2 * stopDelay
}

// serviceEnviron removes the variables of a login session from environ. An administrator who
// runs "cf-agent panel install" in an SSH session as root has, for example,
// DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus. runuser keeps these variables, and
// rootless Podman as a different user then tries to use the D-Bus session of root. The agent
// service has none of these variables, so each command gets the same environment as under
// the service.
func serviceEnviron(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=") || strings.HasPrefix(kv, "XDG_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (h Host) Run(ctx context.Context, name string, args ...string) (string, error) {
	return h.RunInput(ctx, "", name, args...)
}

func (h Host) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return h.RunEnv(ctx, nil, stdin, name, args...)
}

func (h Host) RunEnv(ctx context.Context, env []string, stdin, name string, args ...string) (string, error) {
	cmd := h.command(ctx, env, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, stderr := newOutput(MaxOutput), newOutput(0)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	if stdout.Cut() {
		// The caller gets the start and the end of the output. A caller that reads a
		// structure from the output must not use a part of it as the full output.
		return stdout.String(), fmt.Errorf("%s: %w", name, ErrOutputLimit)
	}
	return stdout.String(), nil
}

// MaxOutput is the largest standard output of one command that the agent keeps in memory.
// A larger output uses Stream.
const MaxOutput = 8 << 20

// maxOutputEnd is the size of the end of the output that the agent keeps when it removes a
// part of the output. The standard error of a command keeps only its end.
const maxOutputEnd = 64 << 10

// ErrOutputLimit is the error of a command that ended with no error and wrote more than
// MaxOutput to its standard output.
var ErrOutputLimit = errors.New("the command wrote too much output, and the agent removed a part of it")

// output keeps the output of a command in memory, with a limit. The owner of a website
// controls the output of a cron command, a build command and the log of the website. With no
// limit, such a command used all the memory of the agent (adversarial review, finding G5-01).
//
// output keeps the first bytes, up to the limit, and the last maxOutputEnd bytes. It reads and
// discards the bytes between them, so the command continues and does not stop on a full pipe.
type output struct {
	start    []byte
	maxStart int
	end      []byte
	removed  int64
}

// newOutput gives an output that keeps limit bytes at most, and maxOutputEnd bytes when limit
// is smaller than that.
func newOutput(limit int) *output {
	return &output{maxStart: max(limit-maxOutputEnd, 0)}
}

func (o *output) Write(p []byte) (int, error) {
	n := len(p)
	if room := o.maxStart - len(o.start); room > 0 {
		take := min(room, len(p))
		o.start = append(o.start, p[:take]...)
		p = p[take:]
	}
	o.end = append(o.end, p...)
	// The end can grow to two times its size before the old half goes: one copy for each
	// maxOutputEnd bytes, not one for each write.
	if extra := len(o.end) - maxOutputEnd; extra > maxOutputEnd {
		o.removed += int64(extra)
		o.end = append(o.end[:0], o.end[extra:]...)
	}
	return n, nil
}

// Cut reports whether a part of the output was removed.
func (o *output) Cut() bool { return o.removed > 0 }

func (o *output) String() string {
	if !o.Cut() {
		return string(o.start) + string(o.end)
	}
	return string(o.start) + fmt.Sprintf("\n[cf-agent: removed %d bytes of output]\n", o.removed) + string(o.end)
}

func (h Host) Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	return h.StreamEnv(ctx, nil, stdin, stdout, name, args...)
}

func (h Host) StreamEnv(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cmd := h.command(ctx, env, name, args...)
	if stdin != nil {
		cmd.Stdin = &stopOnError{r: stdin, cmd: cmd}
	}
	cmd.Stdout = stdout
	stderr := newOutput(0)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
}

// stopOnError is the input of a command. A read error kills the command before Go closes the
// input of the command.
type stopOnError struct {
	r   io.Reader
	cmd *exec.Cmd
}

func (s *stopOnError) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF && s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
	return n, err
}

func (h Host) Pipe(ctx context.Context, from, to []string) error {
	if len(from) == 0 || len(to) == 0 {
		return fmt.Errorf("execx: pipe needs two commands")
	}
	src := h.command(ctx, nil, from[0], from[1:]...)
	dst := h.command(ctx, nil, to[0], to[1:]...)
	out, err := src.StdoutPipe()
	if err != nil {
		return err
	}
	srcErr, dstErr := newOutput(0), newOutput(0)
	src.Stderr = srcErr
	dst.Stdin = out
	dst.Stderr = dstErr
	if err := src.Start(); err != nil {
		return err
	}
	if err := dst.Start(); err != nil {
		_ = src.Process.Kill()
		_ = src.Wait()
		return err
	}
	errDst := dst.Wait()
	errSrc := src.Wait()
	if errSrc != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(from, " "), errSrc, strings.TrimSpace(srcErr.String()))
	}
	if errDst != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(to, " "), errDst, strings.TrimSpace(dstErr.String()))
	}
	return nil
}

// Fake records commands and the stdin of each command. It returns the output or error of the first rule whose prefix agrees.
type Fake struct {
	mu     sync.Mutex
	Calls  []string
	Inputs []string
	// Envs has the extra environment of each command.
	Envs [][]string
	// Sizes has the sizes of each terminal (Terminal): the first size and each change.
	Sizes []TerminalSize
	Rules []Rule
	// MakeDirs makes the fake do the command "install -d": it makes each directory, with no
	// change of the owner. A test sets it when the code that it examines writes a file in a
	// directory that "install -d" makes on a server.
	MakeDirs bool
}

// Rule gives the result of each command that starts with Prefix.
type Rule struct {
	Prefix string
	// Input, when it is set, limits the rule to the commands with this text in their stdin.
	Input  string
	Output string
	Err    error
	// Do runs when the rule agrees, for example to make the files that a command makes.
	// Its error replaces Err.
	Do func() error
}

func (f *Fake) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f.RunInput(ctx, "", name, args...)
}

func (f *Fake) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return f.RunEnv(ctx, nil, stdin, name, args...)
}

func (f *Fake) RunEnv(_ context.Context, env []string, stdin, name string, args ...string) (string, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, line)
	f.Inputs = append(f.Inputs, stdin)
	f.Envs = append(f.Envs, env)
	for _, r := range f.Rules {
		if strings.HasPrefix(line, r.Prefix) && strings.Contains(stdin, r.Input) {
			if r.Do != nil {
				return r.Output, r.Do()
			}
			return r.Output, r.Err
		}
	}
	if f.MakeDirs && name == "install" && len(args) > 0 && args[0] == "-d" {
		return "", makeDirs(args[1:])
	}
	return "", nil
}

// makeDirs makes the directories of the arguments of "install -d". The options "-o", "-g"
// and "-m" have a value, which is not a directory.
func makeDirs(args []string) error {
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" || args[i] == "-g" || args[i] == "-m" {
			i++
			continue
		}
		if err := os.MkdirAll(args[i], 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Stream records the command and its stdin, and writes the output of the rule to stdout.
func (f *Fake) Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	return f.StreamEnv(ctx, nil, stdin, stdout, name, args...)
}

// StreamEnv is Stream with the extra environment of the command.
func (f *Fake) StreamEnv(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	in := ""
	if stdin != nil {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		in = string(b)
	}
	out, err := f.RunEnv(ctx, env, in, name, args...)
	if stdout != nil && out != "" {
		if _, werr := io.WriteString(stdout, out); werr != nil {
			return werr
		}
	}
	return err
}

// Pipe records the two commands as one line, "from | to".
func (f *Fake) Pipe(ctx context.Context, from, to []string) error {
	line := strings.Join(from, " ") + " | " + strings.Join(to, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, line)
	f.Inputs = append(f.Inputs, "")
	f.Envs = append(f.Envs, nil)
	for _, r := range f.Rules {
		if strings.HasPrefix(line, r.Prefix) {
			return r.Err
		}
	}
	return nil
}

// Called reports whether a command that starts with prefix ran.
func (f *Fake) Called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
