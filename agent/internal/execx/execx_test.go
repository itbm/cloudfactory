package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The commands do not start in the working directory of the agent. A website user cannot
// open /root, and rootless Podman stops when it cannot open its working directory.
func TestHostRunsEachCommandInTheRootDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	h := Host{}

	out, err := h.Run(ctx, "pwd")
	if err != nil || strings.TrimSpace(out) != Dir {
		t.Fatalf("Run: got %q, %v; want %q", out, err, Dir)
	}
	out, err = h.RunEnv(ctx, []string{"A=1"}, "x", "pwd")
	if err != nil || strings.TrimSpace(out) != Dir {
		t.Fatalf("RunEnv: got %q, %v; want %q", out, err, Dir)
	}
	var buf bytes.Buffer
	if err := h.Stream(ctx, nil, &buf, "pwd"); err != nil || strings.TrimSpace(buf.String()) != Dir {
		t.Fatalf("Stream: got %q, %v; want %q", buf.String(), err, Dir)
	}
	file := t.TempDir() + "/pwd"
	if err := h.Pipe(ctx, []string{"pwd"}, []string{"sh", "-c", "cat >" + file + "; pwd >>" + file}); err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	got, _ := os.ReadFile(file)
	if string(got) != Dir+"\n"+Dir+"\n" {
		t.Fatalf("Pipe: got %q; want both commands in %q", got, Dir)
	}
}

// The variables of the login session of root do not go to the commands. runuser keeps them,
// and rootless Podman as a service user then uses the D-Bus session of root.
func TestHostRemovesTheVariablesOfTheLoginSession(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/0/bus")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/0")
	t.Setenv("CF_TEST_KEEP", "yes")
	h := Host{Env: []string{"CF_TEST_HOST=1"}}

	out, err := h.RunEnv(context.Background(), []string{"XDG_RUNTIME_DIR=/run/user/1001"}, "", "env")
	if err != nil {
		t.Fatal(err)
	}
	vars := strings.Split(strings.TrimSpace(out), "\n")
	for _, kv := range vars {
		if strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=") || kv == "XDG_RUNTIME_DIR=/run/user/0" {
			t.Errorf("the command got %s", kv)
		}
	}
	for _, want := range []string{"CF_TEST_KEEP=yes", "CF_TEST_HOST=1", "XDG_RUNTIME_DIR=/run/user/1001"} {
		found := false
		for _, kv := range vars {
			found = found || kv == want
		}
		if !found {
			t.Errorf("the command did not get %s", want)
		}
	}
}

// Adversarial review, finding G5-01: a command of a website, for example a cron command, can
// write output with no end. The agent keeps a fixed quantity of it.
func TestHostKeepsALimitedQuantityOfOutput(t *testing.T) {
	ctx := context.Background()
	// 40 MiB of standard output, and then a last line.
	out, err := Host{}.Run(ctx, "sh", "-c", "head -c 41943040 /dev/zero | tr '\\0' 'a'; echo; echo cf-exit:0")
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("error = %v, want ErrOutputLimit", err)
	}
	if len(out) > MaxOutput+maxOutputEnd+100 {
		t.Fatalf("the agent kept %d bytes of output", len(out))
	}
	if !strings.HasPrefix(out, "aaaa") || !strings.HasSuffix(out, "cf-exit:0\n") || !strings.Contains(out, "[cf-agent: removed ") {
		t.Fatalf("the output does not have its start, its end and the note: %q … %q", out[:20], out[len(out)-40:])
	}

	// 40 MiB of standard error of a command that fails: the error has the end only.
	_, err = Host{}.Run(ctx, "sh", "-c", "head -c 41943040 /dev/zero | tr '\\0' 'a' >&2; echo the end >&2; exit 3")
	if err == nil || !strings.HasSuffix(err.Error(), "the end") || len(err.Error()) > 2200 {
		t.Fatalf("error = %.80q (%d bytes)", err, len(fmt.Sprint(err)))
	}

	// A small output stays as it is.
	out, err = Host{}.Run(ctx, "sh", "-c", "echo one; echo two")
	if err != nil || out != "one\ntwo\n" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestOutputKeepsItsStartAndItsEnd(t *testing.T) {
	o := newOutput(maxOutputEnd + 10)
	for i := 0; i < 1000; i++ {
		_, _ = o.Write([]byte(strings.Repeat("x", 1000)))
	}
	_, _ = o.Write([]byte("END"))
	s := o.String()
	if !o.Cut() || !strings.HasPrefix(s, "xxxxxxxxxx\n[cf-agent: removed ") || !strings.HasSuffix(s, "END") {
		t.Fatalf("output = %.60q", s)
	}
	if len(o.start) != 10 || len(o.end) > 2*maxOutputEnd {
		t.Fatalf("start = %d bytes, end = %d bytes", len(o.start), len(o.end))
	}
	if got := int64(10) + o.removed + int64(len(o.end)); got != 1000*1000+3 {
		t.Fatalf("the parts have %d bytes", got)
	}
}
