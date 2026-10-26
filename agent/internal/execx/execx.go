// Package execx runs host commands. It never uses a shell: each command is a program
// with a list of arguments. Tests use Fake instead of the host.
package execx

import (
	"bytes"
	"context"
	"fmt"
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
}

// Host runs commands on the host.
type Host struct {
	// Env is added to the environment of each command, for example "DEBIAN_FRONTEND=noninteractive".
	Env []string
}

func (h Host) Run(ctx context.Context, name string, args ...string) (string, error) {
	return h.RunInput(ctx, "", name, args...)
}

func (h Host) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if len(h.Env) > 0 {
		cmd.Env = append(cmd.Environ(), h.Env...)
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

// Fake records commands and the stdin of each command. It returns the output or error of the first rule whose prefix agrees.
type Fake struct {
	mu     sync.Mutex
	Calls  []string
	Inputs []string
	Rules  []Rule
}

// Rule gives the result of each command that starts with Prefix.
type Rule struct {
	Prefix string
	Output string
	Err    error
}

func (f *Fake) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f.RunInput(ctx, "", name, args...)
}

func (f *Fake) RunInput(_ context.Context, stdin, name string, args ...string) (string, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, line)
	f.Inputs = append(f.Inputs, stdin)
	for _, r := range f.Rules {
		if strings.HasPrefix(line, r.Prefix) {
			return r.Output, r.Err
		}
	}
	return "", nil
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
