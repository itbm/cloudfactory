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
