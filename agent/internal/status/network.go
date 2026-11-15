package status

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// NetCounter gives the traffic of the network interfaces of the server since the last read.
// The zero value is ready to use.
type NetCounter struct {
	mu   sync.Mutex
	read bool
	in   uint64
	out  uint64
}

// virtualPrefixes are the names of the interfaces that the agent does not count: the
// interfaces of the containers, of the bridges and of the mesh. Their traffic also goes
// through a physical interface, or stays on the server.
var virtualPrefixes = []string{"lo", "veth", "podman", "cni", "docker", "br-", "virbr", "wg", "cf", "tap", "tun", "pasta", "dummy"}

// Since gives the bytes that the server received and sent since the last call. The first call
// gives 0. A counter that is smaller than at the last call (for example after a new
// interface) gives 0 for that call.
func (n *NetCounter) Since(procDir string) (in, out int64) {
	b, err := os.ReadFile(filepath.Join(procDir, "net", "dev"))
	if err != nil {
		return 0, 0
	}
	totalIn, totalOut := parseNetDev(string(b))
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.read && totalIn >= n.in && totalOut >= n.out {
		in, out = int64(totalIn-n.in), int64(totalOut-n.out)
	}
	n.read, n.in, n.out = true, totalIn, totalOut
	return in, out
}

// parseNetDev adds the received and the sent bytes of the physical interfaces in the format of
// /proc/net/dev.
func parseNetDev(s string) (in, out uint64) {
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if virtual(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		in += rx
		out += tx
	}
	return in, out
}

func virtual(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
