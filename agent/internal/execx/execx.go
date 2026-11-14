// Package execx runs host commands. It never uses a shell: each command is a program
// with a list of arguments. Tests use Fake instead of the host.
package execx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
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
	return cmd
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}

func (h Host) Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cmd := h.command(ctx, nil, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
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
	var srcErr, dstErr bytes.Buffer
	src.Stderr = &srcErr
	dst.Stdin = out
	dst.Stderr = &dstErr
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
	Envs  [][]string
	Rules []Rule
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
	return "", nil
}

// Stream records the command and its stdin, and writes the output of the rule to stdout.
func (f *Fake) Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	in := ""
	if stdin != nil {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		in = string(b)
	}
	out, err := f.RunEnv(ctx, nil, in, name, args...)
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
