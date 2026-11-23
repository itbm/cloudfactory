package sitelogs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const site = "01m7zwcajpcbqz8v9kkvqnc65a"

// caddyLine is an entry of the access log of Caddy 2.11 with the field of the route.
func caddyLine(at time.Time, method, uri string, status int, client string) []byte {
	return []byte(fmt.Sprintf(`{"level":"info","ts":%q,"logger":"http.log.access.access","msg":"handled request",`+
		`"request":{"remote_ip":"10.0.0.1","remote_port":"49670","client_ip":%q,"proto":"HTTP/2.0","method":%q,"host":"acme.example","uri":%q,`+
		`"headers":{"User-Agent":["Mozilla/5.0 (Test)"],"Referer":["https://search.example/"],"Cookie":["REDACTED"]}},`+
		`"bytes_read":0,"user_id":"","duration":0.0125,"size":1500,"status":%d,"resp_headers":{"Content-Type":["text/html"]},"cf_website":%q}`,
		at.Format(time.RFC3339Nano), client, method, uri, status, site))
}

func TestParseCaddyKeepsTheFieldsOfTheRequest(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 0, 0, 123456789, time.UTC)
	got, a, err := ParseCaddy(caddyLine(at, "GET", "/products?id=42", 200, "203.0.113.7"), time.Now())
	if err != nil || got != site {
		t.Fatalf("site = %q, %v", got, err)
	}
	want := Access{At: at, Client: "203.0.113.7", Method: "GET", Host: "acme.example", URI: "/products?id=42", Proto: "HTTP/2.0", Status: 200, Size: 1500, Duration: 0.0125, UserAgent: "Mozilla/5.0 (Test)", Referer: "https://search.example/"}
	if a != want {
		t.Errorf("entry = %+v\nwant %+v", a, want)
	}
	// The line of the file has no cookie and no other header.
	if line := string(a.Line()); strings.Contains(line, "Cookie") || strings.Contains(line, "REDACTED") || !strings.HasPrefix(line, `{"at":"2026-11-14T10:00:00.123456789Z",`) || !strings.HasSuffix(line, "}\n") {
		t.Errorf("line = %s", line)
	}

	// A time in seconds, no client address, a request from a route of no website, and control
	// characters from a visitor.
	got, a, err = ParseCaddy([]byte(`{"ts":1794650400.5,"request":{"remote_ip":"198.51.100.2","method":"GET","host":"panel.example","uri":"/\u001b[2Jx","headers":{"User-Agent":["a\u0007b"]}},"status":404,"cf_website":"mail"}`), time.Now())
	if err != nil || got != ServerLog {
		t.Fatalf("site = %q, %v", got, err)
	}
	if !a.At.Equal(time.Unix(1794650400, 5e8)) || a.Client != "198.51.100.2" || a.URI != "/�[2Jx" || a.UserAgent != "a�b" {
		t.Errorf("entry = %+v", a)
	}
	// A website ID that is not valid cannot name a directory.
	if got, _, _ = ParseCaddy([]byte(`{"cf_website":"../../etc"}`), time.Now()); got != ServerLog {
		t.Errorf("site = %q", got)
	}
	if _, _, err := ParseCaddy([]byte(`not json`), time.Now()); err == nil {
		t.Error("no error for a line that is not JSON")
	}
	// A long value is cut.
	_, a, _ = ParseCaddy([]byte(`{"request":{"uri":"/`+strings.Repeat("a", 5000)+`"}}`), time.Now())
	if len(a.URI) > maxURI+len("…") {
		t.Errorf("URI has %d bytes", len(a.URI))
	}
}

func TestParseChecksTheSearch(t *testing.T) {
	q, c, err := Parse(Request{Text: `500 "PHP Fatal" -bot`, Statuses: []string{"5xx", "404"}, Methods: []string{"POST"}, Client: "203.0.113.0/24", From: "2026-11-14T09:00:00Z", To: "2026-11-14T10:00:00+01:00"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.Sources, Sources) || !slices.Equal(q.Include, []string{"500", "php fatal"}) || !slices.Equal(q.Exclude, []string{"bot"}) || q.Limit != DefaultLimit {
		t.Errorf("query = %+v", q)
	}
	if q.Client.String() != "203.0.113.0/24" || !q.To.Equal(time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)) || c.Access != nil {
		t.Errorf("query = %+v, cursor = %+v", q, c)
	}
	for _, r := range []Request{
		{Sources: []string{"journal"}},
		{Text: "a\nb"},
		{Text: strings.Repeat("a", 201)},
		{Statuses: []string{"600"}},
		{Statuses: slices.Repeat([]string{"404"}, 11)},
		{Methods: []string{"get"}},
		{Client: "example.com"},
		{Client: "fe80::1%eth0"},
		{From: "yesterday"},
		{Limit: 1001},
		{Limit: -1},
		{Cursor: "a b"},
		{Cursor: "bm90IGpzb24"},
		{Cursor: Cursor{Access: &Position{File: "../../x"}}.Encode()},
	} {
		if _, _, err := Parse(r); err == nil {
			t.Errorf("no error for %+v", r)
		}
	}
	// One address is a network with one address.
	if p, err := ParseClient("::ffff:203.0.113.7"); err != nil || p.String() != "203.0.113.7/32" {
		t.Errorf("client = %v, %v", p, err)
	}
}

func TestParseTerms(t *testing.T) {
	for _, tc := range []struct {
		in            string
		include, excl []string
	}{
		{"", nil, nil},
		{"  /Checkout   POST ", []string{"/checkout", "post"}, nil},
		{`"Fatal error" -"health check" -bot`, []string{"fatal error"}, []string{"health check", "bot"}},
		{`"open quote`, []string{"open quote"}, nil},
		{`- -`, []string{"-", "-"}, nil},
	} {
		in, ex := ParseTerms(tc.in)
		if !slices.Equal(in, tc.include) || !slices.Equal(ex, tc.excl) {
			t.Errorf("ParseTerms(%q) = %q, %q", tc.in, in, ex)
		}
	}
}

// newStore gives a store with a clock that the test sets.
func newStore(t *testing.T, now *time.Time) *Store {
	t.Helper()
	s := &Store{Dir: filepath.Join(t.TempDir(), "logs"), Now: func() time.Time { return *now }}
	if err := s.Ensure(site); err != nil {
		t.Fatal(err)
	}
	return s
}

// write adds requests to the access log, one each second from start.
func write(t *testing.T, s *Store, now *time.Time, n int, fn func(i int) Access) {
	t.Helper()
	var b bytes.Buffer
	for i := range n {
		a := fn(i)
		*now = a.At
		b.Write(a.Line())
		if err := s.Append(site, b.Bytes()); err != nil {
			t.Fatal(err)
		}
		b.Reset()
	}
}

func uris(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		if e.Access != nil {
			out = append(out, e.Access.URI)
		} else {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestTheStoreSearchesTheNewestEntriesFirst(t *testing.T) {
	start := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	now := start
	s := newStore(t, &now)
	write(t, s, &now, 10, func(i int) Access {
		a := Access{At: start.Add(time.Duration(i) * time.Second), Client: "203.0.113.1", Method: "GET", URI: fmt.Sprintf("/p%d", i), Status: 200, Size: 100}
		if i%3 == 0 {
			a.Status, a.Method, a.Client = 500, "POST", "198.51.100.9"
		}
		return a
	})
	q, cur, _ := Parse(Request{Sources: []string{SourceAccess}, Limit: 4})
	g, err := s.Search(site, &q, cur.Access)
	if err != nil || !slices.Equal(uris(g.Entries), []string{"/p9", "/p8", "/p7", "/p6"}) || !g.More {
		t.Fatalf("page 1 = %v, more %v, %v", uris(g.Entries), g.More, err)
	}
	// The cursor gives the older entries, and the last page has no more.
	pages := [][]string{{"/p5", "/p4", "/p3", "/p2"}, {"/p1", "/p0"}}
	pos := g.Entries[len(g.Entries)-1].Pos
	for i, want := range pages {
		g, err = s.Search(site, &q, &pos)
		if err != nil || !slices.Equal(uris(g.Entries), want) || g.More != (i == 0) {
			t.Fatalf("page %d = %v, more %v, %v", i+2, uris(g.Entries), g.More, err)
		}
		pos = g.Entries[len(g.Entries)-1].Pos
	}
	// The filters.
	for _, tc := range []struct {
		r    Request
		want []string
	}{
		{Request{Statuses: []string{"5xx"}}, []string{"/p9", "/p6", "/p3", "/p0"}},
		{Request{Statuses: []string{"200"}, Methods: []string{"GET"}}, []string{"/p8", "/p7", "/p5", "/p4", "/p2", "/p1"}},
		{Request{Client: "198.51.100.0/24"}, []string{"/p9", "/p6", "/p3", "/p0"}},
		{Request{Text: "/P1"}, []string{"/p1"}},
		{Request{Text: "post -/p9"}, []string{"/p6", "/p3", "/p0"}},
		{Request{From: start.Add(7 * time.Second).Format(time.RFC3339), To: start.Add(9 * time.Second).Format(time.RFC3339)}, []string{"/p8", "/p7"}},
	} {
		tc.r.Sources = []string{SourceAccess}
		q, _, err := Parse(tc.r)
		if err != nil {
			t.Fatal(err)
		}
		g, err := s.Search(site, &q, nil)
		if err != nil || !slices.Equal(uris(g.Entries), tc.want) || g.More {
			t.Errorf("%+v = %v, more %v, %v", tc.r, uris(g.Entries), g.More, err)
		}
	}
}

func TestTheSummaryCountsAllEntriesThatAgree(t *testing.T) {
	start := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	now := start
	s := newStore(t, &now)
	write(t, s, &now, 12, func(i int) Access {
		status := []int{200, 200, 404, 500}[i%4]
		return Access{At: start.Add(time.Duration(i) * time.Second), Client: fmt.Sprintf("203.0.113.%d", i%3), URI: fmt.Sprintf("/a?page=%d", i%2), Status: status, Size: 10}
	})
	q, cur, _ := Parse(Request{Sources: []string{SourceAccess}, Limit: 2, Summary: true})
	g, err := s.Search(site, &q, cur.Access)
	if err != nil || len(g.Entries) != 2 || !g.More || g.Summary == nil {
		t.Fatalf("group = %+v, %v", g, err)
	}
	sum := g.Summary
	if sum.Access != 12 || sum.SizeBytes != 120 || sum.Statuses["2xx"] != 6 || sum.Statuses["4xx"] != 3 || sum.Statuses["5xx"] != 3 || !sum.Oldest.Equal(start) {
		t.Errorf("summary = %+v", sum)
	}
	if len(sum.TopPaths) != 1 || sum.TopPaths[0] != (Count{"/a", 12}) || len(sum.TopClients) != 3 || sum.TopClients[0].Count != 4 {
		t.Errorf("top = %+v %+v", sum.TopPaths, sum.TopClients)
	}
	// With a cursor, the summary counts the same entries.
	pos := g.Entries[1].Pos
	g, _ = s.Search(site, &q, &pos)
	if g.Summary.Access != 12 || uris(g.Entries)[0] != "/a?page=1" || !g.Entries[0].At.Equal(start.Add(9*time.Second)) {
		t.Errorf("page 2 = %v, summary %+v", uris(g.Entries), g.Summary)
	}
}

func TestTheStoreStartsANewFileEachDayAndAtItsSizeLimit(t *testing.T) {
	start := time.Date(2026, 11, 14, 23, 59, 58, 0, time.UTC)
	now := start
	s := newStore(t, &now)
	s.FileBytes = 600
	line := func(i int) Access {
		return Access{At: start.Add(time.Duration(i) * time.Second), URI: fmt.Sprintf("/%03d", i), Status: 200}
	}
	write(t, s, &now, 2, line)
	write(t, s, &now, 1, func(int) Access { return line(2) })
	names, _ := s.files(site)
	if len(names) != 2 || !strings.HasPrefix(names[0], "20261114T235958") || !strings.HasPrefix(names[1], "20261115T000000") {
		t.Fatalf("files = %v", names)
	}
	// Each line has about 70 bytes: a file has at most 600 bytes.
	write(t, s, &now, 30, func(i int) Access { return line(i + 3) })
	names, _ = s.files(site)
	if len(names) < 5 {
		t.Fatalf("files = %v", names)
	}
	for _, n := range names {
		fi, _ := os.Stat(filepath.Join(s.Dir, site, n))
		if fi.Size() > 600 {
			t.Errorf("%s has %d bytes", n, fi.Size())
		}
	}
	// A search goes through all files.
	q, _, _ := Parse(Request{Sources: []string{SourceAccess}, Limit: 1000})
	g, _ := s.Search(site, &q, nil)
	if len(g.Entries) != 33 || g.Entries[0].Access.URI != "/032" || g.Entries[32].Access.URI != "/000" {
		t.Errorf("entries = %d: %v", len(g.Entries), uris(g.Entries))
	}
	// A clock that goes back does not put a new file before the last one.
	now = start.Add(-time.Hour)
	s.FileBytes = 1
	if err := s.Append(site, line(99).Line()); err != nil {
		t.Fatal(err)
	}
	after, _ := s.files(site)
	if after[len(after)-1] <= names[len(names)-1] {
		t.Errorf("new file %s is before %s", after[len(after)-1], names[len(names)-1])
	}
}

func TestPruneRemovesOldAndLargeLogs(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := start
	s := newStore(t, &now)
	// One entry each day for 40 days.
	write(t, s, &now, 40, func(i int) Access { return Access{At: start.AddDate(0, 0, i), URI: "/", Status: 200} })
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	names, _ := s.files(site)
	if len(names) != 31 || !strings.HasPrefix(names[0], "20261010") {
		t.Errorf("after 40 days: %d files from %v", len(names), names[0])
	}
	// The size limit of a website keeps the newest files.
	s.SiteBytes = 3 * int64(len(Access{At: start, URI: "/", Status: 200}.Line()))
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	if names, _ = s.files(site); len(names) != 3 || !strings.HasPrefix(names[2], "20261109") {
		t.Errorf("size limit: %v", names)
	}
	// The total limit removes the oldest files of all websites.
	other := "01m7zwcakhwxpa8qmpsqz4p0gk"
	if err := s.Ensure(other); err != nil {
		t.Fatal(err)
	}
	now = start.AddDate(0, 0, 40)
	if err := s.Append(other, Access{At: now, URI: "/", Status: 200}.Line()); err != nil {
		t.Fatal(err)
	}
	s.TotalBytes = 2 * int64(len(Access{At: start, URI: "/", Status: 200}.Line()))
	if err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.files(site)
	theirs, _ := s.files(other)
	if len(mine) != 1 || len(theirs) != 1 {
		t.Errorf("total limit: %v %v", mine, theirs)
	}
	// Remove deletes the log, and the store does not make it again.
	if err := s.Remove(site); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(site, Access{At: now}.Line()); err != ErrUnknownLog {
		t.Errorf("append after remove = %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, site)); !os.IsNotExist(err) {
		t.Errorf("directory: %v", err)
	}
	q, _, _ := Parse(Request{})
	if g, err := s.Search(site, &q, nil); err != nil || len(g.Entries) != 0 {
		t.Errorf("search after remove = %+v, %v", g, err)
	}
}

// k8s writes lines in the "k8s-file" format of Podman.
func k8s(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "website.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSearchContainerReadsTheOutputAndTheErrors(t *testing.T) {
	path := k8s(t,
		`2026-11-14T10:00:00.123456789Z stdout F {"status":200,"uri":"/"}`,
		`2026-11-14T10:00:01.5+00:00 stderr F PHP Warning: Undefined variable $x`,
		`not a log line`,
		`2026-11-14T10:00:02Z stdout P`,
		`2026-11-14T10:00:03Z stderr F PHP Fatal error: `+"\x1b[31m"+`stock`,
	)
	q, _, _ := Parse(Request{Limit: 2})
	g, err := SearchContainer(path, &q, nil)
	if err != nil || !slices.Equal(uris(g.Entries), []string{"PHP Fatal error: �[31mstock", ""}) || !g.More {
		t.Fatalf("page 1 = %q, more %v, %v", uris(g.Entries), g.More, err)
	}
	if g.Entries[0].Source != SourceError || g.Entries[1].Source != SourceOutput || g.Entries[0].Pos.Inode == 0 {
		t.Errorf("entries = %+v", g.Entries)
	}
	pos := g.Entries[1].Pos
	g, _ = SearchContainer(path, &q, &pos)
	if !slices.Equal(uris(g.Entries), []string{"PHP Warning: Undefined variable $x", `{"status":200,"uri":"/"}`}) || g.More {
		t.Errorf("page 2 = %q, more %v", uris(g.Entries), g.More)
	}
	if !g.Entries[1].At.Equal(time.Date(2026, 11, 14, 10, 0, 0, 123456789, time.UTC)) {
		t.Errorf("time = %v", g.Entries[1].At)
	}
	// The source, the words and the summary.
	q, _, _ = Parse(Request{Sources: []string{SourceError}, Text: "php -fatal", Summary: true})
	g, _ = SearchContainer(path, &q, nil)
	if !slices.Equal(uris(g.Entries), []string{"PHP Warning: Undefined variable $x"}) || g.Summary.Error != 1 || g.Summary.Output != 0 || !g.Summary.Oldest.Equal(time.Date(2026, 11, 14, 10, 0, 0, 123456789, time.UTC)) {
		t.Errorf("errors = %q, summary %+v", uris(g.Entries), g.Summary)
	}
	// A filter of the access log gives no lines of the containers.
	q, _, _ = Parse(Request{Statuses: []string{"500"}})
	if g, _ = SearchContainer(path, &q, nil); len(g.Entries) != 0 {
		t.Errorf("status filter = %q", uris(g.Entries))
	}
	// Podman started the file again: the cursor of the earlier file has no more lines.
	pos.Inode++
	q, _, _ = Parse(Request{})
	if g, _ = SearchContainer(path, &q, &pos); len(g.Entries) != 0 || g.More {
		t.Errorf("new file = %q", uris(g.Entries))
	}
	// A website that did not start has no file.
	if g, err = SearchContainer(filepath.Join(t.TempDir(), "none.log"), &q, nil); err != nil || len(g.Entries) != 0 {
		t.Errorf("no file = %+v, %v", g, err)
	}
}

func TestMainAnswersWithJSON(t *testing.T) {
	path := k8s(t, `2026-11-14T10:00:00Z stdout F hello`)
	q, _, _ := Parse(Request{})
	in, _ := ContainerRequest(&q, nil)
	var out bytes.Buffer
	if err := Main(path, bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	g, err := ReadContainerResult(out.Bytes())
	if err != nil || len(g.Entries) != 1 || g.Entries[0].Text != "hello" {
		t.Errorf("result = %+v, %v (%s)", g, err, out.String())
	}
	out.Reset()
	if err := Main(path, strings.NewReader("{"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContainerResult(out.Bytes()); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Errorf("bad input = %v", err)
	}
}

func TestMergePutsTheLogsInOneList(t *testing.T) {
	at := func(s int) time.Time { return time.Date(2026, 11, 14, 10, 0, s, 0, time.UTC) }
	const f = "20261114T100000.000000000Z.log"
	access := &Group{Entries: []Entry{
		{At: at(9), Source: SourceAccess, Pos: Position{File: f, Offset: 90}},
		{At: at(5), Source: SourceAccess, Pos: Position{File: f, Offset: 50}},
	}, More: true}
	container := &Group{Entries: []Entry{
		{At: at(8), Source: SourceError, Pos: Position{Inode: 7, Offset: 80}},
		{At: at(2), Source: SourceOutput, Pos: Position{Inode: 7, Offset: 20}},
	}}
	entries, next, more := Merge(3, Cursor{}, access, container)
	var secs []int
	for _, e := range entries {
		secs = append(secs, e.At.Second())
	}
	if !slices.Equal(secs, []int{9, 8, 5}) || !more {
		t.Fatalf("entries = %v, more %v", secs, more)
	}
	if *next.Access != (Position{File: f, Offset: 50}) || *next.Container != (Position{Inode: 7, Offset: 80}) {
		t.Errorf("next = %+v %+v", next.Access, next.Container)
	}
	// The cursor is text for the panel.
	c, err := DecodeCursor(next.Encode())
	if err != nil || *c.Access != *next.Access || *c.Container != *next.Container {
		t.Errorf("cursor = %+v, %v", c, err)
	}
	// A log that gave all its entries ends. A log that was not part of the search ends too.
	_, next, more = Merge(10, Cursor{}, &Group{Entries: access.Entries}, nil)
	if !next.Access.End || !next.Container.End || more {
		t.Errorf("next = %+v %+v, more %v", next.Access, next.Container, more)
	}
	// A log that gave no entry of the page keeps its position.
	keep := &Position{File: "g", Offset: 5}
	_, next, _ = Merge(1, Cursor{Container: keep}, &Group{Entries: []Entry{{At: at(30), Pos: Position{File: f}}}, More: true}, &Group{Entries: container.Entries})
	if next.Container != keep {
		t.Errorf("container = %+v", next.Container)
	}
}

func TestTheReceiverWritesTheEntriesOfEachWebsite(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	s := newStore(t, &now)
	ctx, cancel := context.WithCancel(context.Background())
	r := &Receiver{Addr: "127.0.0.1:0", Store: s, Flush: 10 * time.Millisecond}
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	<-r.Listening()
	conn, err := net.Dial("tcp", r.Address())
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.Write(caddyLine(now, "GET", "/first", 200, "203.0.113.7"))
	b.WriteString("\n")
	// A line that is too long goes, and the next line stays.
	b.WriteString(`{"request":{"uri":"/` + strings.Repeat("x", maxCaddyLine) + `"},"cf_website":"` + site + `"}` + "\n")
	b.WriteString("not json\n")
	b.WriteString(`{"ts":"2026-11-14T10:00:01Z","request":{"host":"panel.example","uri":"/api/v1/me"},"status":200}` + "\n")
	b.WriteString(`{"ts":"2026-11-14T10:00:01Z","request":{"uri":"/gone"},"status":200,"cf_website":"01m7zwcakhwxpa8qmpsqz4p0gk"}` + "\n")
	b.Write(caddyLine(now.Add(time.Second), "POST", "/second", 201, "203.0.113.8"))
	b.WriteString("\n")
	// An entry with no line end is not complete.
	b.WriteString(`{"cf_website":"` + site + `","request":{"uri":"/partial"}}`)
	if _, err := conn.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	q, _, _ := Parse(Request{Sources: []string{SourceAccess}})
	var g Group
	for range 200 {
		if g, _ = s.Search(site, &q, nil); len(g.Entries) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Equal(uris(g.Entries), []string{"/second", "/first"}) {
		t.Errorf("website = %v", uris(g.Entries))
	}
	var sg Group
	for range 200 {
		if sg, _ = s.Search(ServerLog, &q, nil); len(sg.Entries) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Equal(uris(sg.Entries), []string{"/api/v1/me"}) {
		t.Errorf("server = %v", uris(sg.Entries))
	}
	// A website with no directory gets no log.
	if _, err := os.Stat(filepath.Join(s.Dir, "01m7zwcakhwxpa8qmpsqz4p0gk")); !os.IsNotExist(err) {
		t.Errorf("unknown website: %v", err)
	}
	cancel()
	<-done
}

func TestTheReceiverDropsEntriesWhenTheQueueIsFull(t *testing.T) {
	r := &Receiver{Store: &Store{Dir: t.TempDir()}}
	line := caddyLine(time.Now(), "GET", "/", 200, "203.0.113.7")
	for range queueLines + 5 {
		r.Receive(line)
	}
	if r.Dropped() != 5 {
		t.Errorf("dropped = %d", r.Dropped())
	}
}

func TestEntriesHaveTheJSONOfTheSubprocess(t *testing.T) {
	e := Entry{At: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), Source: SourceError, Text: "x", Pos: Position{Inode: 3, Offset: 9}}
	b, _ := json.Marshal(e)
	if string(b) != `{"at":"2026-11-14T10:00:00Z","source":"error","text":"x","pos":{"i":3,"o":9}}` {
		t.Errorf("json = %s", b)
	}
}
