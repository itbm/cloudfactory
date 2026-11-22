package sitelogs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// MaxLineBytes is the largest size of a line of a container in a result. The agent cuts a
// longer line.
const MaxLineBytes = 4000

// containerLine reads a line in the "k8s-file" format of Podman:
// "<time> <stdout|stderr> <F|P> <text>". ok is false for a line with a different format.
func containerLine(raw []byte) (at time.Time, source string, text []byte, ok bool) {
	parts := bytes.SplitN(raw, []byte(" "), 4)
	if len(parts) < 3 {
		return at, "", nil, false
	}
	at, err := time.Parse(time.RFC3339Nano, string(parts[0]))
	if err != nil {
		return at, "", nil, false
	}
	switch string(parts[1]) {
	case "stdout":
		source = SourceOutput
	case "stderr":
		source = SourceError
	default:
		return at, "", nil, false
	}
	if len(parts) == 4 {
		text = parts[3]
	}
	return at.UTC(), source, text, true
}

// SearchContainer searches the log file of the containers of a website. The website user
// runs it (Main), because only that user can read the file. pos is the position of the
// cursor, or nil for the newest lines.
func SearchContainer(path string, q *Query, pos *Position) (Group, error) {
	c := newCollector(q)
	if ended(pos) && !q.Summary || !q.wants(SourceOutput) && !q.wants(SourceError) || q.accessOnly() {
		return c.result(), nil
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		// A website that did not start yet has no log file.
		return c.result(), nil
	}
	if err != nil {
		return Group{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Group{}, err
	}
	var inode uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		inode = st.Ino
	}
	// limit is the end of the part of the file before the cursor: -1 for the full file.
	limit := int64(-1)
	switch {
	case ended(pos):
		limit = 0
	case pos != nil && pos.Inode != 0:
		limit = pos.Offset
		// Podman starts the file again at its size limit. A cursor of the earlier file has
		// no more lines.
		if pos.Inode != inode || pos.Offset > fi.Size() {
			limit = 0
		}
	}
	if limit == 0 && !q.Summary {
		return c.result(), nil
	}
	size := fi.Size()
	base := max(size-maxRead, 0)
	b := make([]byte, size-base)
	n, err := f.ReadAt(b, base)
	if err != nil && !errors.Is(err, io.EOF) {
		return Group{}, err
	}
	b = b[:n]
	if base > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			b = nil
		} else {
			b, base = b[i+1:], base+int64(i+1)
		}
	}
	var lower []byte
	lines(b, func(raw []byte, offset int) bool {
		at := base + int64(offset)
		inRange := limit < 0 || at < limit
		if !inRange && !q.Summary {
			return true
		}
		t, source, text, ok := containerLine(raw)
		if !ok {
			return true
		}
		if q.before(t) {
			return false
		}
		if q.after(t) || !q.wants(source) {
			return true
		}
		if len(q.Include) > 0 || len(q.Exclude) > 0 {
			var ascii bool
			if lower, ascii = lowerASCII(lower, text); ascii && !q.mayMatch(lower) {
				return true
			}
			if !q.matchText(strings.ToLower(Clean(string(text)))) {
				return true
			}
		}
		e := Entry{At: t, Source: source, Text: cut(Clean(string(text)), MaxLineBytes), Pos: Position{Inode: inode, Offset: at}}
		return !c.add(e, inRange)
	})
	g := c.result()
	if g.Summary != nil {
		first, _, _ := bytes.Cut(b, []byte("\n"))
		if t, _, _, ok := containerLine(first); ok {
			g.Summary.Oldest = t
		}
	}
	return g, nil
}

// containerRequest is the input of Main.
type containerRequest struct {
	Query    Query     `json:"query"`
	Position *Position `json:"position,omitempty"`
}

// containerResponse is the output of Main.
type containerResponse struct {
	Group
	Error string `json:"error,omitempty"`
}

// ContainerRequest gives the input of Main for a search.
func ContainerRequest(q *Query, pos *Position) ([]byte, error) {
	return json.Marshal(containerRequest{Query: *q, Position: pos})
}

// ReadContainerResult reads the output of Main.
func ReadContainerResult(out []byte) (Group, error) {
	var r containerResponse
	if err := json.Unmarshal(out, &r); err != nil {
		return Group{}, errors.New("the search of the log gave no answer")
	}
	if r.Error != "" {
		return Group{}, errors.New(r.Error)
	}
	return r.Group, nil
}

// Main is the command "cf-agent log-search <file>". The worker starts it as the Unix user of
// a website, with a search as JSON on the standard input. It writes the result as JSON.
func Main(path string, in io.Reader, out io.Writer) error {
	var req containerRequest
	var resp containerResponse
	if err := json.NewDecoder(io.LimitReader(in, 64<<10)).Decode(&req); err != nil {
		resp.Error = "the search is not valid"
	} else {
		if req.Query.Limit <= 0 || req.Query.Limit > validate.MaxLogEntries {
			req.Query.Limit = DefaultLimit
		}
		g, err := SearchContainer(path, &req.Query, req.Position)
		if err != nil {
			resp.Error = "the log file cannot be read"
		}
		resp.Group = g
	}
	// The worker reads an error from the answer, so the command does not fail for it.
	return json.NewEncoder(out).Encode(resp)
}
