package caddy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

const securedID = "01JBQ3Z5K8N6ABCDEFGHJKMNPQ"

func securedSite() Site {
	return Site{WebsiteID: securedID, Port: 20001, Hosts: []string{"shop.example"}, Primary: "shop.example", Security: &Security{
		Firewall:           FirewallBlock,
		FirewallExclusions: []int{942100, 920450, 942100},
		FirewallSkipPaths:  []string{"/wp-admin/"},
		Allow:              []string{"203.0.113.0/24"},
		Block:              []string{"198.51.100.7/32"},
		ProtectedPaths:     []ProtectedPath{{Path: "/staging/", Users: []PathUser{{Name: "client", PasswordHash: "$2y$10$rwv0zN7amtbNiLIYD4lBl.AIDGf.p4y0hHxXqLDrkhORIHaBKMPuS"}}}},
		SignInLimit:        10,
		SignInPaths:        []string{"/wp-login.php", "/xmlrpc.php"},
	}}
}

// subroutes gives the routes in the subroute of a website route, as JSON values.
func subroutes(t *testing.T, route map[string]any) []map[string]any {
	t.Helper()
	b, _ := json.Marshal(route)
	var r struct {
		Handle []struct {
			Handler string           `json:"handler"`
			Routes  []map[string]any `json:"routes"`
		} `json:"handle"`
	}
	json.Unmarshal(b, &r)
	// The route first adds the website to the access log (logField), also for a request that
	// the protection blocks.
	if len(r.Handle) != 2 || r.Handle[0].Handler != "log_append" || r.Handle[1].Handler != "subroute" {
		t.Fatalf("the route has no subroute after the log field: %s", b)
	}
	return r.Handle[1].Routes
}

func handlerOf(route map[string]any, i int) map[string]any {
	return route["handle"].([]any)[i].(map[string]any)
}

// The edge proxy applies the IP rules, the sign-in limit, the protected paths and the firewall
// in this order, and then sends the request to the website.
func TestSecurityRoutesHaveTheOrderOfTheProtection(t *testing.T) {
	routes := subroutes(t, SiteRoute(securedSite(), "169.254.77.1"))
	var handlers []string
	for _, r := range routes {
		handlers = append(handlers, handlerOf(r, len(r["handle"].([]any))-1)["handler"].(string))
	}
	want := []string{"static_response", "static_response", "rate_limit", "authentication", "waf", "reverse_proxy"}
	if !reflect.DeepEqual(handlers, want) {
		t.Fatalf("handlers = %v, want %v", handlers, want)
	}
	b, _ := json.Marshal(routes)
	text := string(b)
	for _, want := range []string{
		// The block list, then the allow list with "not".
		`"match":[{"remote_ip":{"ranges":["198.51.100.7/32"]}}]`,
		`"match":[{"not":[{"remote_ip":{"ranges":["203.0.113.0/24"]}}]}]`,
		// Each website has its own counters.
		`"rate_limits":{"cf-` + securedID + `":{"key":"{http.request.remote.host}","max_events":10,"window":"1m"}}`,
		`"match":[{"method":["POST"],"path":["/wp-login.php","/xmlrpc.php"]}]`,
		// A directory is the path with no "/" and all paths in it.
		`"match":[{"path":["/staging","/staging/*"]}]`,
		`"password":"$2y$10$rwv0zN7amtbNiLIYD4lBl.AIDGf.p4y0hHxXqLDrkhORIHaBKMPuS","username":"client"`,
		`"match":[{"not":[{"path":["/wp-admin","/wp-admin/*"]}]}]`,
		// The transaction ID starts with the ID of the website. The website does not get it.
		`"X-Cf-Firewall-Id":["` + securedID + `-{http.request.uuid}"]`,
		`"tx_id_req_header":"X-Cf-Firewall-Id"`,
		`"headers":{"request":{"delete":["X-Cf-Firewall-Id"]}}`,
		`SecRuleRemoveById 920450 942100"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the routes do not have %s:\n%s", want, text)
		}
	}
}

// A website whose protection is off has the usual route.
func TestNoProtectionKeepsTheUsualRoute(t *testing.T) {
	plain := Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"shop.example"}, Primary: "shop.example"}
	off := plain
	off.Security = &Security{Firewall: FirewallOff, SignInPaths: []string{"/wp-login.php"}}
	if !reflect.DeepEqual(SiteRoute(plain, "u"), SiteRoute(off, "u")) {
		t.Errorf("the route changed:\n%v\n%v", SiteRoute(plain, "u"), SiteRoute(off, "u"))
	}
}

// A stopped website shows its page to all visitors.
func TestStoppedWebsiteHasNoProtection(t *testing.T) {
	s := securedSite()
	s.Stopped = true
	b, _ := json.Marshal(SiteRoute(s, "u"))
	if strings.Contains(string(b), "waf") || !strings.Contains(string(b), `"status_code":503`) {
		t.Errorf("route = %s", b)
	}
}

// The websites with the same mode and the same rules use one compiled rule set, so the text
// does not depend on the order of the rules.
func TestFirewallDirectives(t *testing.T) {
	a, b := FirewallDirectives(FirewallBlock, []int{942100, 920450}), FirewallDirectives(FirewallBlock, []int{920450, 942100, 920450})
	if a != b {
		t.Errorf("the directives differ:\n%s\n%s", a, b)
	}
	if !strings.Contains(a, "SecRuleEngine On\n") || !strings.Contains(FirewallDirectives(FirewallDetect, nil), "SecRuleEngine DetectionOnly\n") {
		t.Error("the mode is not in the directives")
	}
	if strings.Contains(FirewallDirectives(FirewallBlock, nil), "SecRuleRemoveById") {
		t.Error("no rule is off")
	}
	for _, want := range []string{"SecRequestBodyLimitAction ProcessPartial", "SecResponseBodyAccess Off", "Include @owasp_crs/*.conf"} {
		if !strings.Contains(a, want) {
			t.Errorf("the directives do not have %q", want)
		}
	}
}

func TestPathPatterns(t *testing.T) {
	got := pathPatterns([]string{"/", "/admin/", "/wp-login.php", "/admin/"})
	want := []string{"*", "/admin", "/admin/*", "/wp-login.php"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("patterns = %v, want %v", got, want)
	}
}

// testdata/firewall.log is a log of a real edge proxy with two websites. The first one blocks.
// The second one only detects. Each request of the second one has a different ID.
func TestFirewallEvents(t *testing.T) {
	read := func(id string, limit int) []FirewallEvent {
		f, err := os.Open("testdata/firewall.log")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		return FirewallEvents(f, id, limit)
	}
	events := read(securedID, 10)
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	// The newest first: a scanner, then an XSS attack.
	scan, xss := events[0], events[1]
	if scan.Action != "blocked" || scan.Host != "shop.localhost" || scan.ClientIP != "127.0.0.1" || len(scan.Rules) != 1 || scan.Rules[0].ID != 913100 {
		t.Errorf("scanner event = %+v", scan)
	}
	if xss.Action != "blocked" || xss.Score != 20 || xss.URI != "/search?q=%3Cscript%3Ealert(1)%3C/script%3E" || len(xss.Rules) != 4 {
		t.Errorf("XSS event = %+v", xss)
	}
	if r := xss.Rules[0]; r.ID != 941100 || r.Message != "XSS Attack Detected via libinjection" || !strings.Contains(r.Data, "<script>alert(1)</script>") {
		t.Errorf("rule = %+v", r)
	}
	if !xss.At.Before(scan.At) || xss.At.IsZero() {
		t.Errorf("times: %v, %v", xss.At, scan.At)
	}

	detected := read("01JBQ3Z5K8N6ABCDEFGHJKMNPR", 10)
	if len(detected) != 1 || detected[0].Action != "detected" || detected[0].Score != 5 || detected[0].Rules[0].ID != 942100 {
		t.Errorf("detected = %+v", detected)
	}
	if got := read(securedID, 1); len(got) != 1 || got[0].Rules[0].ID != 913100 {
		t.Errorf("limit 1 = %+v", got)
	}
	// A website ID that is the start of a different ID gets no events of that website.
	if got := read("01JBQ3Z5K8N6ABCDEFGHJKMNP", 10); len(got) != 0 {
		t.Errorf("events of a prefix = %+v", got)
	}
}

func TestCutKeepsUTF8(t *testing.T) {
	s := strings.Repeat("a", maxText-1) + "é"
	if got := cut(s); got != strings.Repeat("a", maxText-1)+"…" {
		t.Errorf("cut = %q", got)
	}
}

// An edge proxy of an earlier agent has no firewall log. EnsureBase adds it.
func TestEnsureBaseAddsTheFirewallLog(t *testing.T) {
	fake := &fakeCaddy{loaded: true, logging: map[string]any{"logs": map[string]any{"access": map[string]any{}}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	if err := c.EnsureBase(context.Background()); err != nil {
		t.Fatal(err)
	}
	logs := fake.logging["logs"].(map[string]any)
	if logs["firewall"] == nil || !strings.Contains(jsonText(logs["default"]), firewallLogger) {
		t.Errorf("logging = %v", fake.logging)
	}
	calls := len(fake.calls)
	if err := c.EnsureBase(context.Background()); err != nil || fake.calls[len(fake.calls)-1] != "GET /config/logging" || len(fake.calls) != calls+2 {
		t.Errorf("second EnsureBase: %v, calls %v", err, fake.calls[calls:])
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
