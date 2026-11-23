package sitelogs

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

// Entry is one entry of a log.
type Entry struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	// Text is the line of a container. An entry of the access log has Access.
	Text   string  `json:"text,omitempty"`
	Access *Access `json:"access,omitempty"`
	// Pos is the place of the entry in its log, for the cursor.
	Pos Position `json:"pos"`
}

// Position is a place in a log. The next search gives the entries before it.
type Position struct {
	// File is the file of the access log. Inode is the file of the log of the containers. A
	// position with neither is the end of the log.
	File  string `json:"f,omitempty"`
	Inode uint64 `json:"i,omitempty"`
	// Offset is the first byte of the entry.
	Offset int64 `json:"o,omitempty"`
	// End is true when the search has given all entries of the log.
	End bool `json:"e,omitempty"`
}

// Group is the result of a search in one log: the access log, or the log of the containers.
type Group struct {
	// Entries are the newest entries first, at most the limit of the query.
	Entries []Entry `json:"entries,omitempty"`
	// More is true when the log has more entries that agree with the search.
	More bool `json:"more,omitempty"`
	// Summary is only for a query with Summary.
	Summary *Summary `json:"summary,omitempty"`
}

// collector collects the entries of a group.
type collector struct {
	q     *Query
	group Group
	stats *stats
}

func newCollector(q *Query) *collector {
	c := &collector{q: q}
	if q.Summary {
		c.stats = &stats{}
		c.group.Summary = &Summary{}
	}
	return c
}

// add gives an entry that agrees with the search. inRange is false for an entry that is not
// before the cursor: the summary counts it, but the result does not have it. add reports
// whether the search can stop.
func (c *collector) add(e Entry, inRange bool) (stop bool) {
	if c.stats != nil {
		c.stats.add(c.group.Summary, &e)
	}
	if !inRange {
		return false
	}
	if len(c.group.Entries) == c.q.Limit {
		c.group.More = true
		return c.stats == nil
	}
	c.group.Entries = append(c.group.Entries, e)
	return false
}

// result gives the group.
func (c *collector) result() Group {
	if c.stats != nil {
		c.stats.finish(c.group.Summary)
	}
	return c.group
}

// Cursor is the position of a search in each log.
type Cursor struct {
	Access    *Position `json:"a,omitempty"`
	Container *Position `json:"c,omitempty"`
}

// Encode gives the cursor as text for the panel.
func (c Cursor) Encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reads a cursor of Encode.
func DecodeCursor(s string) (Cursor, error) {
	var c Cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil {
		return Cursor{}, errors.New("the cursor is not valid: use the value next of an earlier search")
	}
	if c.Access != nil && c.Access.File != "" && !fileName.MatchString(c.Access.File) || c.Access != nil && c.Access.Offset < 0 || c.Container != nil && c.Container.Offset < 0 {
		return Cursor{}, errors.New("the cursor is not valid: use the value next of an earlier search")
	}
	return c, nil
}

// ended reports whether a position is the end of its log.
func ended(p *Position) bool { return p != nil && p.End }

// Merge puts the entries of the groups in one list, the newest entry first, and gives the
// cursor of the next search. cursor is the cursor of this search. A group that is nil was not
// part of the search. more is false when no log has more entries.
func Merge(limit int, cursor Cursor, access, container *Group) (entries []Entry, next Cursor, more bool) {
	type part struct {
		g   *Group
		pos **Position
		i   int
	}
	next = cursor
	var parts []*part
	for _, p := range []part{{g: access, pos: &next.Access}, {g: container, pos: &next.Container}} {
		if p.g == nil {
			*p.pos = &Position{End: true}
			continue
		}
		parts = append(parts, &p)
	}
	for len(entries) < limit {
		var best *part
		for _, p := range parts {
			if p.i < len(p.g.Entries) && (best == nil || p.g.Entries[p.i].At.After(best.g.Entries[best.i].At)) {
				best = p
			}
		}
		if best == nil {
			break
		}
		entries = append(entries, best.g.Entries[best.i])
		best.i++
	}
	for _, p := range parts {
		switch {
		case p.i == len(p.g.Entries) && !p.g.More:
			*p.pos = &Position{End: true}
		case p.i > 0:
			pos := p.g.Entries[p.i-1].Pos
			*p.pos = &pos
		}
	}
	return entries, next, !ended(next.Access) || !ended(next.Container)
}

// Summary is the count of all entries that agree with a search, also the entries that are
// not in the page of the result.
type Summary struct {
	Access int64 `json:"access,omitempty"`
	Output int64 `json:"output,omitempty"`
	Error  int64 `json:"error,omitempty"`
	// Statuses has the number of requests of each class of status codes, for example "5xx".
	Statuses   map[string]int64 `json:"statuses,omitempty"`
	TopPaths   []Count          `json:"topPaths,omitempty"`
	TopClients []Count          `json:"topClients,omitempty"`
	SizeBytes  int64            `json:"sizeBytes,omitempty"`
	// Oldest is the time of the oldest entry of the log, also an entry that does not agree
	// with the search.
	Oldest time.Time `json:"oldest,omitzero"`
}

// Count is a value and its number of entries.
type Count struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// The limits of the summary.
const (
	topCount = 5
	// maxKeys is the largest number of different paths and clients that a summary counts. A
	// search with more counts only the first ones, so the memory of the agent stays small.
	maxKeys = 20000
)

// stats counts the paths and the clients of the access log.
type stats struct {
	paths, clients map[string]int64
}

func (s *stats) add(sum *Summary, e *Entry) {
	switch e.Source {
	case SourceOutput:
		sum.Output++
	case SourceError:
		sum.Error++
	case SourceAccess:
		sum.Access++
		a := e.Access
		if sum.Statuses == nil {
			sum.Statuses = map[string]int64{}
			s.paths, s.clients = map[string]int64{}, map[string]int64{}
		}
		if a.Status >= 100 && a.Status <= 599 {
			sum.Statuses[string(rune('0'+a.Status/100))+"xx"]++
		}
		sum.SizeBytes += a.Size
		countKey(s.paths, a.path())
		countKey(s.clients, a.Client)
	}
}

func countKey(m map[string]int64, k string) {
	if k == "" {
		return
	}
	if _, ok := m[k]; ok || len(m) < maxKeys {
		m[k]++
	}
}

func (s *stats) finish(sum *Summary) {
	sum.TopPaths, sum.TopClients = top(s.paths), top(s.clients)
}

// top gives the values with the most entries, the most first.
func top(m map[string]int64) []Count {
	keys := slices.SortedFunc(maps.Keys(m), func(a, b string) int {
		return cmp.Or(cmp.Compare(m[b], m[a]), cmp.Compare(a, b))
	})
	out := []Count{}
	for _, k := range keys[:min(topCount, len(keys))] {
		out = append(out, Count{Value: k, Count: m[k]})
	}
	return out
}

// AddSummary adds the counts of two summaries. Only the access log has the top paths and the
// top clients.
func AddSummary(a, b *Summary) *Summary {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	out := *a
	out.Access += b.Access
	out.Output += b.Output
	out.Error += b.Error
	out.SizeBytes += b.SizeBytes
	if len(b.Statuses) > 0 {
		out.Statuses = maps.Clone(a.Statuses)
		if out.Statuses == nil {
			out.Statuses = map[string]int64{}
		}
		for k, v := range b.Statuses {
			out.Statuses[k] += v
		}
	}
	if len(out.TopPaths) == 0 {
		out.TopPaths, out.TopClients = b.TopPaths, b.TopClients
	}
	if out.Oldest.IsZero() || !b.Oldest.IsZero() && b.Oldest.Before(out.Oldest) {
		out.Oldest = b.Oldest
	}
	return &out
}

// lines calls fn for each complete line of b, the last line first, with the offset of its
// first byte. A last line with no line end is not complete: the writer of the log can be
// in the middle of it. fn returns false to stop.
func lines(b []byte, fn func(line []byte, offset int) bool) {
	end := bytes.LastIndexByte(b, '\n')
	for end >= 0 {
		start := bytes.LastIndexByte(b[:end], '\n') + 1
		if !fn(b[start:end], start) {
			return
		}
		end = start - 1
	}
}

// lowerASCII puts the bytes of b in lower case into dst, and reports whether b has only ASCII
// characters. For a line with other characters, the fast check (mayMatch) does not apply: in
// Unicode, a character that is not ASCII can have an ASCII letter as its lower case.
func lowerASCII(dst, b []byte) ([]byte, bool) {
	dst = dst[:0]
	ascii := true
	for _, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		} else if c >= 0x80 {
			ascii = false
		}
		dst = append(dst, c)
	}
	return dst, ascii
}

// mayMatch is a fast check of a raw line in lower case, with only ASCII characters, before the
// agent reads it. It is false only when a word of the search is not in the line: the full
// check then is false also.
func (q *Query) mayMatch(lower []byte) bool {
	for _, w := range q.Include {
		if isASCIIPlain(w) && !bytes.Contains(lower, []byte(w)) {
			return false
		}
	}
	return true
}

// isASCIIPlain reports whether a word has only ASCII characters that JSON does not escape.
func isASCIIPlain(w string) bool {
	for i := 0; i < len(w); i++ {
		if c := w[i]; c >= 0x80 || c == '"' || c == '\\' || c < 0x20 {
			return false
		}
	}
	return true
}
