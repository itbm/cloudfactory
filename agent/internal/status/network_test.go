package status

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const netDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9000000     100    0    0    0     0          0         0  9000000     100    0    0    0     0       0          0
  eth0: %d    2000    0    0    0     0          0         0 %d    1000    0    0    0     0       0          0
veth12ab: 777     1    0    0    0     0          0         0 777     1    0    0    0     0       0          0
cf-mesh: 555     1    0    0    0     0          0         0 555     1    0    0    0     0       0          0
`

func writeNetDev(t *testing.T, dir string, in, out int) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "net", "dev"), []byte(fmt.Sprintf(netDev, in, out)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNetCounterCountsOnlyPhysicalInterfaces(t *testing.T) {
	dir := t.TempDir()
	var n NetCounter
	writeNetDev(t, dir, 1000, 2000)
	if in, out := n.Since(dir); in != 0 || out != 0 {
		t.Errorf("first read = %d, %d, want 0, 0", in, out)
	}
	writeNetDev(t, dir, 1500, 2600)
	if in, out := n.Since(dir); in != 500 || out != 600 {
		t.Errorf("second read = %d, %d, want 500, 600", in, out)
	}
	// The counters started again, for example after a reboot.
	writeNetDev(t, dir, 10, 10)
	if in, out := n.Since(dir); in != 0 || out != 0 {
		t.Errorf("read after a reset = %d, %d, want 0, 0", in, out)
	}
	if in, out := n.Since(filepath.Join(dir, "none")); in != 0 || out != 0 {
		t.Errorf("read with no file = %d, %d", in, out)
	}
}
