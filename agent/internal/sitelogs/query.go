// Package sitelogs keeps and searches the logs of the websites.
//
// A website has two logs:
//
//   - The access log of the edge proxy. Caddy sends each entry as JSON to the agent
//     (Receiver), and the agent keeps the entries of each website in its own directory (Store).
//   - The log of its containers: the standard output and the standard error. Podman writes it
//     to a file in the home directory of the website user, in the "k8s-file" format. The agent
//     reads it as the website user (SearchContainer).
//
// A search goes from the newest entry to the oldest entry. It gives a cursor, and the next
// search with the cursor gives the older entries.
package sitelogs

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The sources of the logs.
const (
	// SourceAccess is the access log of the edge proxy.
	SourceAccess = "access"
	// SourceOutput is the standard output of the containers of a website.
	SourceOutput = "output"
	// SourceError is the standard error of the containers of a website.
	SourceError = "error"
)

// Sources are all sources, in the sequence of the panel.
var Sources = []string{SourceAccess, SourceOutput, SourceError}

// DefaultLimit is the number of entries of a search that does not give a limit.
const DefaultLimit = 100

// maxTerms is the largest number of words in the text of a search.
const maxTerms = 20

// Request is a search as the panel sends it.
type Request struct {
	Sources  []string
	Text     string
	Statuses []string
	Methods  []string
	Client   string
	From, To string
	Cursor   string
	Limit    int
	Summary  bool
}

// Query is a search that the agent checked. The worker sends it as JSON to the search of the
// log of the containers (Main).
type Query struct {
	Sources []string `json:"sources"`
	// Include and Exclude are the words of the text, in lower case.
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// Statuses are status codes ("404") or classes ("5xx").
	Statuses []string     `json:"statuses,omitempty"`
	Methods  []string     `json:"methods,omitempty"`
	Client   netip.Prefix `json:"client,omitzero"`
	From     time.Time    `json:"from,omitzero"`
	To       time.Time    `json:"to,omitzero"`
	Limit    int          `json:"limit"`
	Summary  bool         `json:"summary,omitempty"`
}

// Parse checks a search and gives its query and its cursor.
func Parse(r Request) (Query, Cursor, error) {
	q := Query{Limit: r.Limit, Summary: r.Summary}
	var errs []error
	for _, s := range r.Sources {
		if !slices.Contains(Sources, s) {
			errs = append(errs, fmt.Errorf("%q is not a source of the logs", s))
		} else if !slices.Contains(q.Sources, s) {
			q.Sources = append(q.Sources, s)
		}
	}
	if len(q.Sources) == 0 {
		q.Sources = slices.Clone(Sources)
	}
	if r.Text != "" {
		if err := validate.LogSearch(r.Text); err != nil {
			errs = append(errs, err)
		} else {
			q.Include, q.Exclude = ParseTerms(r.Text)
		}
	}
	if len(r.Statuses) > validate.MaxLogFilters || len(r.Methods) > validate.MaxLogFilters {
		errs = append(errs, fmt.Errorf("a search has at most %d status codes and %d methods", validate.MaxLogFilters, validate.MaxLogFilters))
	}
	for _, s := range r.Statuses {
		errs = append(errs, validate.HTTPStatus(s))
		q.Statuses = append(q.Statuses, s)
	}
	for _, m := range r.Methods {
		errs = append(errs, validate.HTTPMethod(m))
		q.Methods = append(q.Methods, m)
	}
	if r.Client != "" {
		p, err := ParseClient(r.Client)
		errs = append(errs, err)
		q.Client = p
	}
	var err error
	if q.From, err = parseTime(r.From); err != nil {
		errs = append(errs, fmt.Errorf("from: %w", err))
	}
	if q.To, err = parseTime(r.To); err != nil {
		errs = append(errs, fmt.Errorf("to: %w", err))
	}
	if q.Limit < 0 || q.Limit > validate.MaxLogEntries {
		errs = append(errs, fmt.Errorf("the limit must be from 1 to %d", validate.MaxLogEntries))
	}
	if q.Limit == 0 {
		q.Limit = DefaultLimit
	}
	var c Cursor
	if r.Cursor != "" {
		if err := validate.LogCursor(r.Cursor); err != nil {
			errs = append(errs, err)
		} else if c, err = DecodeCursor(r.Cursor); err != nil {
			errs = append(errs, err)
		}
	}
	return q, c, errors.Join(errs...)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a time in RFC 3339", s)
	}
	return t.UTC(), nil
}

// ParseClient reads the address of a client or a network, for example "203.0.113.7" or
// "2001:db8::/32".
func ParseClient(s string) (netip.Prefix, error) {
	if len(s) > 64 {
		return netip.Prefix{}, errors.New("the client is not an address or a network")
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("%q is not a network, for example 203.0.113.0/24", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// ParseTerms reads the words of a search. Words in double quotation marks are one word. A
// word that starts with "-" goes to exclude. The words are in lower case.
func ParseTerms(s string) (include, exclude []string) {
	s = strings.ToLower(s)
	for len(s) > 0 && len(include)+len(exclude) < maxTerms {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			break
		}
		not := false
		if len(s) > 1 && s[0] == '-' && s[1] != ' ' {
			not, s = true, s[1:]
		}
		var word string
		if s[0] == '"' {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				word, s = s[1:], ""
			} else {
				word, s = s[1:end+1], s[end+2:]
			}
		} else {
			end := strings.IndexByte(s, ' ')
			if end < 0 {
				end = len(s)
			}
			word, s = s[:end], s[end:]
		}
		if word == "" {
			continue
		}
		if not {
			exclude = append(exclude, word)
		} else {
			include = append(include, word)
		}
	}
	return include, exclude
}

// wants reports whether the search is for a source.
func (q *Query) wants(source string) bool { return slices.Contains(q.Sources, source) }

// accessOnly reports whether the search has a filter that only the access log has. An entry
// of a container then never agrees with it.
func (q *Query) accessOnly() bool {
	return len(q.Statuses) > 0 || len(q.Methods) > 0 || q.Client.IsValid()
}

// after reports whether t is at the time "to" or later: the search does not give it.
func (q *Query) after(t time.Time) bool { return !q.To.IsZero() && !t.Before(q.To) }

// before reports whether t is before the time "from". The search stops there, because the
// entries are in the sequence of their times.
func (q *Query) before(t time.Time) bool { return !q.From.IsZero() && t.Before(q.From) }

// matchText reports whether a text in lower case has the words of the search.
func (q *Query) matchText(lower string) bool {
	for _, w := range q.Include {
		if !strings.Contains(lower, w) {
			return false
		}
	}
	for _, w := range q.Exclude {
		if strings.Contains(lower, w) {
			return false
		}
	}
	return true
}

// matchStatus reports whether a status agrees with the status filter.
func (q *Query) matchStatus(status int) bool {
	if len(q.Statuses) == 0 {
		return true
	}
	code := fmt.Sprintf("%03d", status)
	for _, s := range q.Statuses {
		if s == code || strings.HasSuffix(s, "xx") && s[0] == code[0] {
			return true
		}
	}
	return false
}

// matchAccess reports whether an entry of the access log agrees with the search.
func (q *Query) matchAccess(a *Access) bool {
	if !q.matchStatus(a.Status) {
		return false
	}
	if len(q.Methods) > 0 && !slices.Contains(q.Methods, a.Method) {
		return false
	}
	if q.Client.IsValid() {
		addr, err := netip.ParseAddr(a.Client)
		if err != nil || !q.Client.Contains(addr.Unmap()) {
			return false
		}
	}
	if len(q.Include) == 0 && len(q.Exclude) == 0 {
		return true
	}
	return q.matchText(a.haystack())
}

// Clean gives a text from a website or a visitor with no control character and no invalid
// UTF-8. A person with more rights reads it, also in a terminal, where a control character
// can change earlier lines (adversarial review, finding G5-09). Tabs and line ends stay.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || r == 0x2028 || r == 0x2029:
			return '�'
		}
		return r
	}, s)
}

// cut gives the first n bytes of s, with no part of a character at the end.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
