package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeCaddy keeps routes by their @id, like the admin API.
type fakeCaddy struct {
	mu       sync.Mutex
	loaded   bool
	routes   []map[string]any
	policies string
	// cas are the CAs of the PKI app. tls are the TLS automation policies; nil means that the
	// configuration has no TLS app.
	cas map[string]map[string]any
	tls []map[string]any
	// calls are the method and path of each request.
	calls []string
	// logging is the logging configuration, or nil when the configuration has none.
	logging map[string]any
	// config has the JSON of some paths of the configuration, as the admin API keeps it:
	// the TLS connection policies, the automatic HTTPS and the loaded certificates.
	config map[string]string
}

// configPaths are the paths of fakeCaddy.config.
var configPaths = map[string]bool{
	"/config/apps/http/servers/edge/tls_connection_policies": true,
	"/config/apps/http/servers/edge/automatic_https":         true,
	"/config/apps/tls/certificates/load_pem":                 true,
}

// object finds a route or a TLS automation policy by its @id.
func (f *fakeCaddy) object(id string) (list []map[string]any, index int) {
	for _, l := range [][]map[string]any{f.routes, f.tls} {
		for i, o := range l {
			if o["@id"] == id {
				return l, i
			}
		}
	}
	return nil, -1
}

func readJSON(r *http.Request, v any) {
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, v)
}

func (f *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	switch {
	case configPaths[r.URL.Path]:
		// The answers of the admin API: GET gives null for a value that is not there, PUT
		// refuses a value that is there, and PATCH and DELETE refuse a value that is not there.
		if f.config == nil {
			f.config = map[string]string{}
		}
		v, ok := f.config[r.URL.Path]
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && !ok:
			w.Write([]byte("null"))
		case r.Method == http.MethodGet:
			w.Write([]byte(v))
		case r.Method == http.MethodPut && ok:
			http.Error(w, `{"error":"key already exists"}`, http.StatusConflict)
		case (r.Method == http.MethodPatch || r.Method == http.MethodDelete) && !ok:
			http.Error(w, `{"error":"key does not exist"}`, http.StatusNotFound)
		case r.Method == http.MethodDelete:
			delete(f.config, r.URL.Path)
		default:
			f.config[r.URL.Path] = string(b)
			if strings.HasSuffix(r.URL.Path, "tls_connection_policies") {
				f.policies = string(b)
			}
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/config/apps/http/servers/edge"):
		if !f.loaded {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("{}"))
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		var cfg map[string]any
		readJSON(r, &cfg)
		f.logging, _ = cfg["logging"].(map[string]any)
		f.loaded = true
	case r.Method == http.MethodGet && r.URL.Path == "/config/logging":
		json.NewEncoder(w).Encode(f.logging)
	case r.Method == http.MethodPost && r.URL.Path == "/config/logging":
		readJSON(r, &f.logging)
	case r.Method == http.MethodPost && r.URL.Path == "/config/apps/http/servers/edge/routes":
		var route map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &route)
		f.routes = append(f.routes, route)
	case r.Method == http.MethodPut && r.URL.Path == "/config/apps/http/servers/edge/routes/0":
		var route map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &route)
		f.routes = append([]map[string]any{route}, f.routes...)
	case (r.Method == http.MethodPatch || r.Method == http.MethodGet) && strings.Count(r.URL.Path, "/") == 3 && strings.HasPrefix(r.URL.Path, "/id/"):
		// A field of an object, for example /id/cf-panel/match.
		id, field, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/id/"), "/")
		list, i := f.object(id)
		if i < 0 {
			http.Error(w, `{"error":"unknown object ID '`+id+`'"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(list[i][field])
			return
		}
		var value any
		readJSON(r, &value)
		list[i][field] = value
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/id/"):
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		var object map[string]any
		readJSON(r, &object)
		if list, i := f.object(id); i >= 0 {
			list[i] = object
			return
		}
		http.Error(w, `{"error":"unknown object ID '`+id+`'"}`, http.StatusNotFound)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/id/"):
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		if list, i := f.object(id); i >= 0 {
			json.NewEncoder(w).Encode(list[i])
			return
		}
		http.Error(w, `{"error":"unknown object ID '`+id+`'"}`, http.StatusNotFound)
	case strings.HasPrefix(r.URL.Path, "/config/apps/pki/certificate_authorities/"):
		id := strings.TrimPrefix(r.URL.Path, "/config/apps/pki/certificate_authorities/")
		_, exists := f.cas[id]
		switch {
		case r.Method == http.MethodPut && exists:
			http.Error(w, `{"error":"key already exists: `+id+`"}`, http.StatusConflict)
		case r.Method == http.MethodPatch && !exists:
			http.Error(w, `{"error":"key does not exist: `+id+`"}`, http.StatusNotFound)
		default:
			var ca map[string]any
			readJSON(r, &ca)
			if f.cas == nil {
				f.cas = map[string]map[string]any{}
			}
			f.cas[id] = ca
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/pki/ca/"):
		if _, ok := f.cas[strings.TrimPrefix(r.URL.Path, "/pki/ca/")]; !ok {
			http.Error(w, `{"error":"no certificate authority configured with id"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": PanelCA, "root_certificate": "-----BEGIN CERTIFICATE-----\nROOT\n-----END CERTIFICATE-----\n"})
	case r.URL.Path == "/config/apps/tls/automation/policies":
		switch {
		case r.Method == http.MethodGet && f.tls == nil:
			http.Error(w, `{"error":"invalid traversal path at: config/apps/tls/automation"}`, http.StatusBadRequest)
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(f.tls)
		case r.Method == http.MethodPut && f.tls != nil:
			http.Error(w, `{"error":"key already exists: policies"}`, http.StatusConflict)
		case r.Method == http.MethodPut:
			readJSON(r, &f.tls)
		case r.Method == http.MethodPost && f.tls != nil:
			// A POST on a list adds the value at the end.
			var policy map[string]any
			readJSON(r, &policy)
			f.tls = append(f.tls, policy)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	case r.Method == http.MethodPut && r.URL.Path == "/config/apps/tls/automation/policies/0":
		var policy map[string]any
		readJSON(r, &policy)
		f.tls = append([]map[string]any{policy}, f.tls...)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/id/"):
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		for i, route := range f.routes {
			if route["@id"] == id {
				f.routes = append(f.routes[:i], f.routes[i+1:]...)
				return
			}
		}
		for i, policy := range f.tls {
			if policy["@id"] == id {
				f.tls = append(f.tls[:i], f.tls[i+1:]...)
				return
			}
		}
		http.Error(w, `{"error":"unknown object ID '`+id+`'"}`, http.StatusBadRequest)
	default:
		http.Error(w, "unexpected", http.StatusTeapot)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	fake := &fakeCaddy{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	if err := c.EnsureBase(ctx); err != nil || !fake.loaded {
		t.Fatalf("EnsureBase: %v, loaded=%v", err, fake.loaded)
	}
	site := Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"acme.example"}, Redirects: []string{"www.acme.example"}, Primary: "acme.example"}
	for range 3 {
		if err := c.Apply(ctx, site); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(fake.routes))
	}
	if err := c.Remove(ctx, "W1"); err != nil || len(fake.routes) != 0 {
		t.Fatalf("Remove: %v, routes=%d", err, len(fake.routes))
	}
}

// Audit batch 2, finding 7: a change of the routes replaces each route in one request. The
// website has a route at all times.
func TestApplyReplacesARouteWithNoGap(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	site := Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"acme.example"}, Primary: "acme.example"}
	if err := c.Apply(ctx, site); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	site.Port = 20002
	if err := c.Apply(ctx, site); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		if call == "DELETE /id/cf-W1" || strings.HasPrefix(call, "POST") {
			t.Errorf("the route was removed and added again: %v", fake.calls)
		}
	}
	b, _ := json.Marshal(fake.routes)
	if len(fake.routes) != 1 || !strings.Contains(string(b), "169.254.77.1:20002") {
		t.Errorf("routes = %s", b)
	}
	// A redirect domain that the website no longer has loses its route.
	site.Redirects = []string{"www.acme.example"}
	if err := c.Apply(ctx, site); err != nil || len(fake.routes) != 2 {
		t.Fatalf("Apply: %v, routes=%d", err, len(fake.routes))
	}
	site.Redirects = nil
	if err := c.Apply(ctx, site); err != nil || len(fake.routes) != 1 {
		t.Fatalf("Apply: %v, routes=%d", err, len(fake.routes))
	}
}

// CF-04: the edge proxy does not serve files from the disk. The website container serves them.
func TestSiteRouteSendsAllRequestsToTheContainer(t *testing.T) {
	b, _ := json.Marshal(SiteRoute(Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"acme.example"}}, "host.containers.internal"))
	s := string(b)
	for _, want := range []string{`"@id":"cf-W1"`, `"dial":"host.containers.internal:20001"`, `"host":["acme.example"]`} {
		if !strings.Contains(s, want) {
			t.Errorf("route does not contain %s: %s", want, s)
		}
	}
	for _, bad := range []string{"file_server", "/srv/sites", `"file"`} {
		if strings.Contains(s, bad) {
			t.Errorf("route contains %s: %s", bad, s)
		}
	}
	b, _ = json.Marshal(RedirectRoute(Site{WebsiteID: "W1", Redirects: []string{"www.acme.example"}, Primary: "acme.example"}))
	if !strings.Contains(string(b), `"Location":["https://acme.example{http.request.uri}"]`) || !strings.Contains(string(b), `"status_code":308`) {
		t.Errorf("redirect route: %s", b)
	}
}

// A route with a path prefix has only the paths of the prefix on its host.
func TestSiteRouteWithAPath(t *testing.T) {
	b, _ := json.Marshal(SiteRoute(Site{WebsiteID: "T1", Port: 20002, Hosts: []string{"adminer.example.com"}, Path: "/t1/"}, "169.254.77.1"))
	if !strings.Contains(string(b), `"match":[{`+plainHostJSON+`"host":["adminer.example.com"],"path":["/t1/*"]}]`) {
		t.Errorf("route: %s", b)
	}
	b, _ = json.Marshal(SiteRoute(Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"acme.example"}}, "169.254.77.1"))
	if strings.Contains(string(b), `"path"`) {
		t.Errorf("a route with no path prefix has a path: %s", b)
	}
}

func TestPanelRouteSetsTheEdgeHeaders(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	if err := New(srv.URL).ApplyPanel(context.Background(), "panel.example.com", PanelInternal, 19080, 19081, 19082); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(fake.routes)
	for _, want := range []string{`"X-Client-Cert-Sha256":["{http.request.tls.client.fingerprint}"]`, `"X-Cloudfactory-Edge":["1"]`, `"/api/*"`, `"Strict-Transport-Security":["max-age=31536000"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("route does not contain %s: %s", want, b)
		}
	}
	if !strings.Contains(fake.policies, `"mode":"request"`) {
		t.Errorf("policies = %s", fake.policies)
	}
	// The web terminal goes to the relay, before the route of the API.
	relay := strings.Index(string(b), `{"handle":[{"handler":"reverse_proxy","stream_close_delay":"12h","upstreams":[{"dial":"169.254.77.1:19082"}]}],"match":[{"path":["/api/terminal/*"]}]}`)
	if relay < 0 || relay > strings.Index(string(b), `"/api/*"`) {
		t.Errorf("no route for the relay before the API: %s", b)
	}
}

// CF-03: a website with no verified domain gets no route. A route with no host matches all hosts.
func TestAWebsiteWithNoDomainHasNoRoute(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	if err := New(srv.URL).Apply(context.Background(), Site{WebsiteID: "W1", Port: 20001}); err != nil {
		t.Fatal(err)
	}
	if len(fake.routes) != 0 {
		t.Errorf("routes = %v", fake.routes)
	}
}

// CF-03: the panel route is first, so that a website route for the same domain cannot come before it.
func TestThePanelRouteIsFirst(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	if err := c.Apply(ctx, Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"panel.example.com"}, Primary: "panel.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyPanel(ctx, "panel.example.com", PanelInternal, 19080, 19081, 19082); err != nil {
		t.Fatal(err)
	}
	if len(fake.routes) != 2 || fake.routes[0]["@id"] != "cf-panel" {
		t.Errorf("routes = %v", fake.routes)
	}
}

// CF-13: the admin API is a Unix socket.
func TestTheClientUsesTheAdminSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "caddy")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "admin.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no Unix sockets:", err)
	}
	fake := &fakeCaddy{}
	srv := &http.Server{Handler: fake}
	go srv.Serve(l)
	defer srv.Close()

	if err := New("unix://" + sock).EnsureBase(context.Background()); err != nil || !fake.loaded {
		t.Fatalf("EnsureBase: %v, loaded=%v", err, fake.loaded)
	}
	b, _ := json.Marshal(BaseConfig())
	if !strings.Contains(string(b), `"admin":{"listen":"unix//run/caddy-admin/admin.sock"}`) {
		t.Errorf("base config: %s", b)
	}
}

// Audit item U10: the route of a stopped website answers with a page and status 503. It keeps
// the domains, so that the edge proxy keeps their certificates.
func TestSiteRouteOfAStoppedWebsite(t *testing.T) {
	route := SiteRoute(Site{WebsiteID: "w1", Port: 20001, Hosts: []string{"acme.example"}, Stopped: true}, "169.254.77.1")
	b, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"@id":"cf-w1"`, `"host":["acme.example"]`, `"handler":"static_response"`, `"status_code":503`, `text/html; charset=utf-8`, `This website is not available`} {
		if !strings.Contains(got, want) {
			t.Errorf("the route has no %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "reverse_proxy") {
		t.Errorf("the route of a stopped website sends requests to the container:\n%s", got)
	}
}

func TestSiteRouteSendsSomePathsToADifferentPort(t *testing.T) {
	route := SiteRoute(Site{WebsiteID: "mail", Port: 19027, Hosts: []string{"mail.example.net"}, Paths: []PathRoute{{Paths: []string{"/jmap", "/jmap/*"}, Port: 19025}, {Paths: []string{"/admin/*/css"}, Methods: []string{"GET"}, Port: 19027}, {Paths: []string{"/admin"}}}}, "169.254.77.1")
	b, _ := json.Marshal(route)
	want := `{"@id":"cf-mail","handle":[{"early":true,"handler":"log_append","key":"cf_website","value":"mail"},{"handler":"subroute","routes":[` +
		`{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"169.254.77.1:19025"}]}],"match":[{"path":["/jmap","/jmap/*"]}],"terminal":true},` +
		`{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"169.254.77.1:19027"}]}],"match":[{"method":["GET"],"path":["/admin/*/css"]}],"terminal":true},` +
		`{"handle":[{"handler":"static_response","status_code":404}],"match":[{"path":["/admin"]}],"terminal":true},` +
		`{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"169.254.77.1:19027"}]}]}]}],"match":[{` + plainHostJSON + `"host":["mail.example.net"]}],"terminal":true}`
	if string(b) != want {
		t.Errorf("route:\n%s\nwant:\n%s", b, want)
	}
}

// plainHostJSON is the matcher for the Host header in the JSON of a route (hostMatch).
const plainHostJSON = `"header_regexp":{"Host":{"pattern":"^([^:\\[\\]]+|\\[[^\\]]+\\])(:(443|8443))?$"}},`

// Adversarial review, finding G6-02: a route accepts a Host header with no port or with the
// HTTPS port only.
func TestRoutesAcceptOnlyAPlainHost(t *testing.T) {
	re := regexp.MustCompile(plainHost)
	for host, want := range map[string]bool{
		"example.com": true, "example.com:443": true, "example.com:8443": true, "[2001:db8::1]": true, "[2001:db8::1]:443": true,
		"example.com:1": false, "example.com:abc": false, "example.com:x:443": false, "example.com:": false, "": false,
	} {
		if got := re.MatchString(host); got != want {
			t.Errorf("%q: %v, want %v", host, got, want)
		}
	}
}

// Each route of a website adds the website ID to the entries of its requests in the access
// log, so the agent knows the website of each entry. The field is the first handler: it is
// also in the entry of a request that a later handler refuses.
func TestEachRouteOfAWebsiteNamesTheWebsiteInTheAccessLog(t *testing.T) {
	for _, route := range []map[string]any{
		SiteRoute(Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"acme.example"}}, "169.254.77.1"),
		SiteRoute(Site{WebsiteID: "W1", Port: 20001, Hosts: []string{"acme.example"}, Stopped: true}, "169.254.77.1"),
		RedirectRoute(Site{WebsiteID: "W1", Redirects: []string{"www.acme.example"}, Primary: "acme.example"}),
	} {
		b, _ := json.Marshal(route["handle"].([]any)[0])
		if string(b) != `{"early":true,"handler":"log_append","key":"cf_website","value":"W1"}` {
			t.Errorf("first handler of %v = %s", route["@id"], b)
		}
	}
}

// The edge proxy sends its access log to the agent on the private service address, as JSON
// with times in RFC 3339. A new edge proxy that has no agent yet still starts.
func TestTheAccessLogGoesToTheAgent(t *testing.T) {
	b, _ := json.Marshal(BaseConfig()["logging"])
	for _, want := range []string{
		`"writer":{"address":"tcp/169.254.77.1:19514","dial_timeout":"2s","output":"net","soft_start":true}`,
		`"time_format":"rfc3339_nano"`, `"include":["http.log.access"]`, `"default":{"exclude":["http.log.access","http.handlers.waf"]}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("logging has no %s:\n%s", want, b)
		}
	}
	// The image of the edge proxy starts with the same logging configuration.
	var image map[string]any
	raw, err := os.ReadFile("../../../images/edge/base.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &image); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(LoggingConfig())
	got, _ := json.Marshal(image["logging"])
	if !sameJSON(got, want) {
		t.Errorf("images/edge/base.json has the logging\n%s\nwant\n%s", got, want)
	}
}

// An edge proxy that operates gets the logging configuration of this agent version one time.
func TestEnsureBaseSetsTheLoggingOfThisVersion(t *testing.T) {
	fake := &fakeCaddy{loaded: true, logging: map[string]any{"logs": map[string]any{"access": map[string]any{"encoder": map[string]any{"format": "json"}}}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	for range 2 {
		if err := c.EnsureBase(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := json.Marshal(fake.logging)
	want, _ := json.Marshal(LoggingConfig())
	if !sameJSON(got, want) {
		t.Errorf("logging = %s", got)
	}
	posts := 0
	for _, call := range fake.calls {
		if call == "POST /config/logging" {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("the agent set the logging %d times: %v", posts, fake.calls)
	}
}
