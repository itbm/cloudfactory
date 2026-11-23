package sitelogs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Access is an entry of the access log of the edge proxy, as the agent keeps it.
type Access struct {
	At       time.Time `json:"at"`
	Client   string    `json:"client,omitempty"`
	Method   string    `json:"method,omitempty"`
	Host     string    `json:"host,omitempty"`
	URI      string    `json:"uri,omitempty"`
	Proto    string    `json:"proto,omitempty"`
	Status   int       `json:"status,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Duration float64   `json:"duration,omitempty"`
	// UserAgent and Referer are request headers.
	UserAgent string `json:"userAgent,omitempty"`
	Referer   string `json:"referer,omitempty"`
}

// The largest sizes of the fields of an entry of the access log, in bytes. The agent cuts a
// longer value.
const (
	maxClient    = 64
	maxMethod    = 16
	maxHost      = 255
	maxURI       = 2048
	maxProto     = 16
	maxUserAgent = 512
	maxReferer   = 1024
)

// haystack gives the fields of the entry in lower case, for the words of a search.
func (a *Access) haystack() string {
	return strings.ToLower(strings.Join([]string{a.Method, a.Host + a.URI, strconv.Itoa(a.Status), a.Client, a.Proto, a.UserAgent, a.Referer}, " "))
}

// path gives the path of the URI, with no query.
func (a *Access) path() string {
	p, _, _ := strings.Cut(a.URI, "?")
	return p
}

// caddyEntry is the part of an entry of the access log of Caddy that the agent reads.
type caddyEntry struct {
	TS       json.RawMessage `json:"ts"`
	Website  string          `json:"cf_website"`
	Status   int             `json:"status"`
	Size     int64           `json:"size"`
	Duration float64         `json:"duration"`
	Request  struct {
		RemoteIP string              `json:"remote_ip"`
		ClientIP string              `json:"client_ip"`
		Proto    string              `json:"proto"`
		Method   string              `json:"method"`
		Host     string              `json:"host"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
}

// WebsiteField is the field of an entry of the access log of Caddy with the ID of the
// website. The route of each website adds it (caddy.SiteRoute).
const WebsiteField = "cf_website"

// ServerLog is the name of the access log of the requests that are not for a website: the
// panel, the webmail and the host name of the mail server. A website ID has 26 characters,
// so no website has this name.
const ServerLog = "server"

// ParseCaddy reads an entry of the access log of Caddy. It gives the website of the entry, or
// ServerLog.
func ParseCaddy(line []byte, now time.Time) (string, Access, error) {
	var e caddyEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return "", Access{}, err
	}
	at := now
	var s string
	var f float64
	switch {
	case json.Unmarshal(e.TS, &s) == nil:
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			at = t
		}
	case json.Unmarshal(e.TS, &f) == nil && f > 0 && f < 1e11:
		sec, frac := math.Modf(f)
		at = time.Unix(int64(sec), int64(frac*1e9))
	}
	client := e.Request.ClientIP
	if client == "" {
		client = e.Request.RemoteIP
	}
	header := func(name string) string {
		if v := e.Request.Headers[name]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	a := Access{
		At:        at.UTC(),
		Client:    cut(Clean(client), maxClient),
		Method:    cut(Clean(e.Request.Method), maxMethod),
		Host:      cut(Clean(e.Request.Host), maxHost),
		URI:       cut(Clean(e.Request.URI), maxURI),
		Proto:     cut(Clean(e.Request.Proto), maxProto),
		Status:    e.Status,
		Size:      max(e.Size, 0),
		Duration:  max(e.Duration, 0),
		UserAgent: cut(Clean(header("User-Agent")), maxUserAgent),
		Referer:   cut(Clean(header("Referer")), maxReferer),
	}
	site := ServerLog
	if e.Website != "" && validate.ID(e.Website) == nil {
		site = e.Website
	}
	return site, a, nil
}

// Line gives the entry as one line of a file of the access log.
func (a Access) Line() []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(a)
	return b.Bytes()
}

// The limits of the access log.
const (
	// FileBytes is the size at which the agent starts a new file.
	FileBytes = 8 << 20
	// SiteBytes is the largest size of the files of one website.
	SiteBytes = 64 << 20
	// TotalBytes is the largest size of the access logs of all websites on a server.
	TotalBytes = 4 << 30
	// MaxAge is the time that the agent keeps an entry.
	MaxAge = 30 * 24 * time.Hour
)

// fileName is the name of a file of the access log: the time in UTC at which the agent
// started the file. The names sort in the sequence of the times.
var fileName = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z\.log$`)

const fileLayout = "20060102T150405.000000000Z"

func fileStart(name string) time.Time {
	t, _ := time.Parse(fileLayout, strings.TrimSuffix(name, ".log"))
	return t
}

// ErrUnknownLog is the error for a website that has no directory of its access log. The
// agent removed the website, or the website is not on this server.
var ErrUnknownLog = errors.New("this website has no access log on this server")

// Store keeps the access log of each website in a directory: <Dir>/<website ID>/. Each file
// has the entries of one day in UTC, and at most FileBytes. Only the worker of the agent can
// read the files.
type Store struct {
	Dir string
	// The limits. 0 means FileBytes, SiteBytes, TotalBytes and MaxAge.
	FileBytes, SiteBytes, TotalBytes int64
	MaxAge                           time.Duration
	Now                              func() time.Time

	mu      sync.Mutex
	current map[string]*current
}

// current is the file that the store writes for a website.
type current struct {
	name  string
	start time.Time
	size  int64
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) limits() (file, site, total int64, age time.Duration) {
	return cmpOr(s.FileBytes, FileBytes), cmpOr(s.SiteBytes, SiteBytes), cmpOr(s.TotalBytes, TotalBytes), cmpOr(s.MaxAge, MaxAge)
}

func cmpOr[T int64 | time.Duration](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}

func (s *Store) dir(site string) (string, error) {
	if site != ServerLog {
		if err := validate.ID(site); err != nil {
			return "", err
		}
	}
	return filepath.Join(s.Dir, site), nil
}

// Ensure makes the directory of the access log of a website. The store writes the entries
// of a website only when its directory is there.
func (s *Store) Ensure(site string) error {
	dir, err := s.dir(site)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// Remove deletes the access log of a website.
func (s *Store) Remove(site string) error {
	dir, err := s.dir(site)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.current, site)
	return os.RemoveAll(dir)
}

// files gives the names of the files of a website, the oldest first.
func (s *Store) files(site string) ([]string, error) {
	dir, err := s.dir(site)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrUnknownLog
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && fileName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// Append adds complete lines to the access log of a website. It starts a new file each day
// and at FileBytes, and then removes the old files of the website.
func (s *Store) Append(site string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if site == ServerLog {
		if err := s.Ensure(ServerLog); err != nil {
			return err
		}
	}
	dir, err := s.dir(site)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		s.current = map[string]*current{}
	}
	fileMax, _, _, _ := s.limits()
	now := s.now()
	cur := s.current[site]
	if cur == nil {
		names, err := s.files(site)
		if err != nil {
			return err
		}
		cur = &current{}
		if len(names) > 0 {
			cur.name = names[len(names)-1]
			cur.start = fileStart(cur.name)
			if fi, err := os.Stat(filepath.Join(dir, cur.name)); err == nil {
				cur.size = fi.Size()
			}
		}
	}
	day := func(t time.Time) time.Time { return t.Truncate(24 * time.Hour) }
	rotated := false
	if cur.name == "" || cur.size > 0 && cur.size+int64(len(data)) > fileMax || day(now).After(day(cur.start)) {
		// A clock that goes back must not give a name before the name of the last file.
		start := now
		if !cur.start.IsZero() && !start.After(cur.start) {
			start = cur.start.Add(time.Nanosecond)
		}
		cur = &current{name: start.Format(fileLayout) + ".log", start: start}
		rotated = true
	}
	f, err := os.OpenFile(filepath.Join(dir, cur.name), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		delete(s.current, site)
		return ErrUnknownLog
	}
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	cur.size += int64(n)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	s.current[site] = cur
	if rotated {
		if _, perr := s.pruneSite(site); err == nil {
			err = perr
		}
	}
	return err
}

// pruneSite removes the files of a website that are too old, and the oldest files while the
// files are larger than SiteBytes. It gives the files that stay. The caller holds s.mu.
func (s *Store) pruneSite(site string) ([]fileInfo, error) {
	names, err := s.files(site)
	if err != nil {
		return nil, err
	}
	dir, _ := s.dir(site)
	_, siteMax, _, age := s.limits()
	// Each file has the entries of one day. Thus a file of a day before the limit has only
	// old entries.
	cutoff := s.now().Add(-age).Truncate(24 * time.Hour)
	var kept []fileInfo
	var total int64
	var errs []error
	for _, name := range names {
		if fileStart(name).Before(cutoff) {
			errs = append(errs, s.removeFile(site, dir, name))
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		kept = append(kept, fileInfo{site: site, name: name, size: fi.Size()})
		total += fi.Size()
	}
	for len(kept) > 1 && total > siteMax {
		errs = append(errs, s.removeFile(site, dir, kept[0].name))
		total -= kept[0].size
		kept = kept[1:]
	}
	return kept, errors.Join(errs...)
}

type fileInfo struct {
	site, name string
	size       int64
}

// removeFile removes a file of a website. The caller holds s.mu.
func (s *Store) removeFile(site, dir, name string) error {
	if cur := s.current[site]; cur != nil && cur.name == name {
		delete(s.current, site)
	}
	err := os.Remove(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Prune removes the old files of all websites, and the oldest files of all websites while
// the files are larger than TotalBytes. The agent does it each hour.
func (s *Store) Prune() error {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, totalMax, _ := s.limits()
	var all []fileInfo
	var total int64
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || (e.Name() != ServerLog && validate.ID(e.Name()) != nil) {
			continue
		}
		kept, err := s.pruneSite(e.Name())
		errs = append(errs, err)
		for _, f := range kept {
			all = append(all, f)
			total += f.size
		}
	}
	slices.SortFunc(all, func(a, b fileInfo) int { return strings.Compare(a.name, b.name) })
	for len(all) > 0 && total > totalMax {
		dir, _ := s.dir(all[0].site)
		errs = append(errs, s.removeFile(all[0].site, dir, all[0].name))
		total -= all[0].size
		all = all[1:]
	}
	return errors.Join(errs...)
}

// maxRead is the largest part of a file that a search reads at one time.
const maxRead = 16 << 20

// Search searches the access log of a website. pos is the position of the cursor, or nil
// for the newest entries.
func (s *Store) Search(site string, q *Query, pos *Position) (Group, error) {
	c := newCollector(q)
	if ended(pos) && !q.Summary {
		return c.result(), nil
	}
	names, err := s.files(site)
	if errors.Is(err, ErrUnknownLog) {
		return c.result(), nil
	}
	if err != nil {
		return Group{}, err
	}
	dir, _ := s.dir(site)
	var lower []byte
	stop := false
	for i := len(names) - 1; i >= 0 && !stop; i-- {
		name := names[i]
		// The entries of a file are before the start of the next file.
		if i+1 < len(names) && q.before(fileStart(names[i+1])) {
			break
		}
		if !q.To.IsZero() && !fileStart(name).Before(q.To) {
			continue
		}
		// A file after the cursor: only the summary needs it.
		limit := int64(-1)
		switch {
		case ended(pos):
			limit = 0
		case pos != nil && pos.File != "":
			if name > pos.File {
				limit = 0
			} else if name == pos.File {
				limit = pos.Offset
			}
		}
		if limit == 0 && !q.Summary {
			continue
		}
		b, base, err := readTail(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Group{}, err
		}
		lines(b, func(line []byte, offset int) bool {
			at := base + int64(offset)
			inRange := limit < 0 || at < limit
			if !inRange && !q.Summary {
				return true
			}
			// The time is the first field of a line (Access.Line). The search stops at the
			// first entry before "from", and does not read an entry after "to".
			if t, ok := lineTime(line); ok {
				if q.before(t) {
					stop = true
					return false
				}
				if q.after(t) {
					return true
				}
			}
			var ascii bool
			if lower, ascii = lowerASCII(lower, line); ascii && !q.mayMatch(lower) {
				return true
			}
			var a Access
			if json.Unmarshal(line, &a) != nil {
				return true
			}
			if q.before(a.At) {
				stop = true
				return false
			}
			if q.after(a.At) || !q.matchAccess(&a) {
				return true
			}
			if c.add(Entry{At: a.At, Source: SourceAccess, Access: &a, Pos: Position{File: name, Offset: at}}, inRange) {
				stop = true
				return false
			}
			return true
		})
	}
	g := c.result()
	if g.Summary != nil && len(names) > 0 {
		g.Summary.Oldest = s.oldest(filepath.Join(dir, names[0]))
	}
	return g, nil
}

// lineTime reads the time at the start of a line of the access log, with no full read of the
// line.
func lineTime(line []byte) (time.Time, bool) {
	rest, ok := bytes.CutPrefix(line, []byte(`{"at":"`))
	if !ok {
		return time.Time{}, false
	}
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, string(rest[:end]))
	return t, err == nil
}

// oldest gives the time of the first entry of a file.
func (s *Store) oldest(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	b := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, b)
	line, _, _ := bytes.Cut(b[:n], []byte("\n"))
	var a Access
	if json.Unmarshal(line, &a) != nil {
		return fileStart(filepath.Base(path))
	}
	return a.At
}

// readTail reads a file, or its last maxRead bytes. It gives the offset of the first byte
// that it read. When it does not read from the start, it drops the first line, which can be
// incomplete.
func readTail(path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := fi.Size()
	base := max(size-maxRead, 0)
	b := make([]byte, size-base)
	n, err := f.ReadAt(b, base)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	b = b[:n]
	if base > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return nil, base, nil
		}
		b, base = b[i+1:], base+int64(i+1)
	}
	return b, base, nil
}
