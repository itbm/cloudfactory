package caddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The protection of a website in the edge proxy (docs/architecture.md#protection-of-a-website).
//
// The route of a website with protection is a subroute. The edge proxy applies the parts in
// this order, before the request goes to the website container:
//
//  1. The IP address rules: the block list, then the allow list. Status 403.
//  2. The sign-in limit: POST requests to the sign-in paths from one IP address. Status 429.
//  3. The password-protected paths: HTTP basic authentication. Status 401.
//  4. The firewall: Coraza with the OWASP Core Rule Set (CRS). Status 403 in the mode "block".
//
// The firewall of each website is a "waf" handler. The handler keeps one compiled rule set for
// each different text of directives, and the websites with the same text use the same rule set
// (about 3 MB of memory each). Thus the directives have no value of one website: the mode and
// the rules that the website turns off are the only differences. The ID of the website is in
// the transaction ID of each request (FirewallTxHeader), so the agent can find the events of a
// website in the firewall log.

// Firewall modes.
const (
	FirewallOff    = "off"
	FirewallDetect = "detect"
	FirewallBlock  = "block"
)

// FirewallTxHeader is the request header that gives the transaction ID to the firewall. The
// edge proxy sets it for each request, also when a client sends one, and removes it before the
// request goes to the website.
const FirewallTxHeader = "X-Cf-Firewall-Id"

// FirewallLog is the firewall log in the edge container. The data directory of the edge proxy
// is at /data in the container.
const FirewallLog = "/data/logs/firewall.log"

// firewallLogger is the name of the logger of the firewall handler in Caddy.
const firewallLogger = "http.handlers.waf"

// Security is the protection of one website.
type Security struct {
	// Firewall is FirewallOff, FirewallDetect or FirewallBlock. "" is FirewallOff.
	Firewall string
	// FirewallExclusions are the IDs of the firewall rules that do not apply.
	FirewallExclusions []int
	// FirewallSkipPaths are paths that the firewall does not examine.
	FirewallSkipPaths []string
	// Allow and Block are IP addresses and ranges in CIDR form.
	Allow, Block []string
	// ProtectedPaths ask for a user name and a password.
	ProtectedPaths []ProtectedPath
	// SignInLimit is the largest number of POST requests in one minute from one IP address to
	// SignInPaths. 0 is no limit.
	SignInLimit int
	SignInPaths []string
}

// ProtectedPath is a path with HTTP basic authentication.
type ProtectedPath struct {
	Path  string
	Users []PathUser
}

// PathUser is a user of a protected path. PasswordHash is a bcrypt hash.
type PathUser struct {
	Name         string
	PasswordHash string
}

// active reports whether the protection changes the route.
func (s *Security) active() bool {
	if s == nil {
		return false
	}
	return s.firewallOn() || len(s.Allow) > 0 || len(s.Block) > 0 || len(s.ProtectedPaths) > 0 ||
		(s.SignInLimit > 0 && len(s.SignInPaths) > 0)
}

func (s *Security) firewallOn() bool {
	return s.Firewall == FirewallDetect || s.Firewall == FirewallBlock
}

// AccessDeniedPage is the page for an IP address that the rules of a website refuse.
const AccessDeniedPage = `<!doctype html>
<html lang="en-GB">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Access denied</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">
<h1>Access denied</h1>
<p>This website does not accept requests from your IP address.</p>
</body>
</html>
`

// pathPatterns gives the patterns of the path matcher of Caddy for the paths of the protection.
// A path that ends with "/" is the directory and all paths in it, also the path with no "/" at
// the end. "/" is all paths. The path matcher of Caddy ignores case, and compares the decoded
// and cleaned path of a request, so "/ADMIN/", "//admin/" and "/x/../admin/" agree with "/admin/".
func pathPatterns(paths []string) []string {
	out := make([]string, 0, len(paths)*2)
	for _, p := range paths {
		switch {
		case p == "/":
			out = append(out, "*")
		case strings.HasSuffix(p, "/"):
			out = append(out, strings.TrimSuffix(p, "/"), p+"*")
		default:
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// FirewallDirectives gives the directives of the firewall for a mode and the rules that a
// website turns off. The text is the same for all websites with the same values, so they use
// one compiled rule set.
func FirewallDirectives(mode string, exclusions []int) string {
	engine := "On"
	if mode == FirewallDetect {
		engine = "DetectionOnly"
	}
	lines := []string{
		"Include @coraza.conf-recommended",
		"SecRuleEngine " + engine,
		// The firewall log of Caddy has each rule that agrees with a request. The audit log of
		// Coraza would have the same events a second time.
		"SecAuditEngine Off",
		// The firewall does not keep the answers of the website: a stream, for example server-sent
		// events, goes to the visitor at once. The rules for data leaks do not apply.
		"SecResponseBodyAccess Off",
		// The firewall examines the first 1 MiB of a request body and sends the full body to the
		// website. Large uploads stay possible.
		"SecRequestBodyLimit 1048576",
		"SecRequestBodyLimitAction ProcessPartial",
		"Include @crs-setup.conf.example",
		"Include @owasp_crs/*.conf",
	}
	ids := slices.Clone(exclusions)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > 0 {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		lines = append(lines, "SecRuleRemoveById "+strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

// securityRoutes gives the routes of the protection of a website. They come before the route
// to the website container in the subroute of the website.
func securityRoutes(websiteID string, s *Security) []any {
	var routes []any
	deny := func(match map[string]any) map[string]any {
		return map[string]any{
			"match": []any{match},
			"handle": []any{map[string]any{
				"handler":     "static_response",
				"status_code": 403,
				"headers": map[string]any{
					"Content-Type":  []string{"text/html; charset=utf-8"},
					"Cache-Control": []string{"no-store"},
				},
				"body": AccessDeniedPage,
			}},
			"terminal": true,
		}
	}
	if len(s.Block) > 0 {
		routes = append(routes, deny(map[string]any{"remote_ip": map[string]any{"ranges": s.Block}}))
	}
	if len(s.Allow) > 0 {
		routes = append(routes, deny(map[string]any{"not": []any{map[string]any{"remote_ip": map[string]any{"ranges": s.Allow}}}}))
	}
	if s.SignInLimit > 0 && len(s.SignInPaths) > 0 {
		routes = append(routes, map[string]any{
			"match": []any{map[string]any{"method": []string{"POST"}, "path": pathPatterns(s.SignInPaths)}},
			"handle": []any{map[string]any{
				"handler": "rate_limit",
				// The rate limit module keeps the counters of a zone by its name, for all routes.
				// Thus each website has its own name.
				"rate_limits": map[string]any{"cf-" + websiteID: map[string]any{
					"key":        "{http.request.remote.host}",
					"window":     "1m",
					"max_events": s.SignInLimit,
				}},
			}},
		})
	}
	for _, p := range s.ProtectedPaths {
		accounts := make([]any, 0, len(p.Users))
		for _, u := range p.Users {
			accounts = append(accounts, map[string]any{"username": u.Name, "password": u.PasswordHash})
		}
		routes = append(routes, map[string]any{
			"match": []any{map[string]any{"path": pathPatterns([]string{p.Path})}},
			"handle": []any{map[string]any{
				"handler": "authentication",
				"providers": map[string]any{"http_basic": map[string]any{
					"accounts": accounts,
					"hash":     map[string]any{"algorithm": "bcrypt"},
					// The cache keeps the result of a correct password, so that each request of a
					// signed-in visitor does not compute the bcrypt hash again.
					"hash_cache": map[string]any{},
					"realm":      "Protected area",
				}},
			}},
		})
	}
	if s.firewallOn() {
		route := map[string]any{
			"handle": []any{
				map[string]any{"handler": "headers", "request": map[string]any{"set": map[string]any{
					FirewallTxHeader: []string{websiteID + "-{http.request.uuid}"},
				}}},
				map[string]any{
					"handler":          "waf",
					"load_owasp_crs":   true,
					"tx_id_req_header": FirewallTxHeader,
					"directives":       FirewallDirectives(s.Firewall, s.FirewallExclusions),
				},
			},
		}
		if len(s.FirewallSkipPaths) > 0 {
			route["match"] = []any{map[string]any{"not": []any{map[string]any{"path": pathPatterns(s.FirewallSkipPaths)}}}}
		}
		routes = append(routes, route)
	}
	return routes
}

// firewallLogs gives the loggers of the firewall in the base configuration: the firewall log
// in the data directory, and the default log with no firewall entries. The log has the events
// of all websites. Caddy keeps one older file.
func firewallLogs() map[string]any {
	return map[string]any{
		"firewall": map[string]any{
			"writer":  map[string]any{"output": "file", "filename": FirewallLog, "roll_size_mb": 20, "roll_keep": 1},
			"encoder": map[string]any{"format": "json"},
			"include": []string{firewallLogger},
		},
	}
}

// FirewallEvent is one request that the firewall blocked or detected.
type FirewallEvent struct {
	At       time.Time
	ClientIP string
	Host     string
	URI      string
	// Action is "blocked" or "detected".
	Action string
	Score  int
	Rules  []FirewallRule
}

// FirewallRule is one rule that agreed with a request.
type FirewallRule struct {
	ID      int
	Message string
	Data    string
}

// The rules of the CRS that give the result of a request, not a find. Their messages give the
// score.
var scoreRules = map[int]bool{949110: true, 949111: true, 980170: true}

var (
	fieldRE  = regexp.MustCompile(`\[(id|msg|data|uri|unique_id) "((?:[^"\\]|\\.)*)"\]`)
	clientRE = regexp.MustCompile(`^\[client "([^"]*)"\]`)
	scoreRE  = regexp.MustCompile(`Total Score: ([0-9]+)`)
)

// maxText is the largest length of a text in an event.
const maxText = 300

func cut(s string) string {
	if len(s) <= maxText {
		return s
	}
	s = s[:maxText]
	// Do not cut in a UTF-8 sequence.
	for len(s) > 0 && s[len(s)-1]&0xC0 == 0x80 {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] >= 0xC0 {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// unquote reads a value in a field of a Coraza message. Coraza writes the value with %q.
func unquote(s string) string {
	if v, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return v
	}
	return s
}

// FirewallEvents reads a firewall log and gives the events of one website, the newest first,
// at most limit. The transaction ID of each request starts with the ID of the website and "-".
func FirewallEvents(r io.Reader, websiteID string, limit int) []FirewallEvent {
	prefix := websiteID + "-"
	events := map[string]*FirewallEvent{}
	var order []string
	get := func(id string) *FirewallEvent {
		e := events[id]
		if e == nil {
			e = &FirewallEvent{Action: "detected"}
			events[id] = e
			order = append(order, id)
		}
		return e
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(prefix)) {
			continue
		}
		var entry struct {
			TS       float64 `json:"ts"`
			Msg      string  `json:"msg"`
			Hostname string  `json:"hostname"`
			URI      string  `json:"uri"`
			ClientIP string  `json:"client_ip"`
			UniqueID string  `json:"unique_id"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		at := time.Unix(0, int64(entry.TS*float64(time.Second))).UTC()
		// The handler writes this line when it blocks a request. It has the host.
		if entry.UniqueID != "" {
			if !strings.HasPrefix(entry.UniqueID, prefix) {
				continue
			}
			e := get(entry.UniqueID)
			e.Action = "blocked"
			e.Host, e.ClientIP = cut(entry.Hostname), entry.ClientIP
			if e.URI == "" {
				e.URI = cut(entry.URI)
			}
			if e.At.IsZero() {
				e.At = at
			}
			continue
		}
		fields := map[string]string{}
		for _, m := range fieldRE.FindAllStringSubmatch(entry.Msg, -1) {
			if _, ok := fields[m[1]]; !ok {
				fields[m[1]] = unquote(m[2])
			}
		}
		id := fields["unique_id"]
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		e := get(id)
		if e.At.IsZero() {
			e.At = at
		}
		if m := clientRE.FindStringSubmatch(entry.Msg); m != nil && e.ClientIP == "" {
			e.ClientIP = m[1]
		}
		if e.URI == "" {
			e.URI = cut(fields["uri"])
		}
		rule, _ := strconv.Atoi(fields["id"])
		if scoreRules[rule] {
			if m := scoreRE.FindStringSubmatch(fields["msg"]); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n > e.Score {
					e.Score = n
				}
			}
			if strings.Contains(entry.Msg, "Coraza: Access denied") {
				e.Action = "blocked"
			}
			continue
		}
		if rule == 0 {
			continue
		}
		e.Rules = append(e.Rules, FirewallRule{ID: rule, Message: cut(fields["msg"]), Data: cut(fields["data"])})
	}
	out := make([]FirewallEvent, 0, len(order))
	for _, id := range order {
		out = append(out, *events[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
