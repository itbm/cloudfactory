package execx

import (
	"bytes"
	"context"
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
