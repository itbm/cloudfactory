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
	// calls are the method and path of each request.
	calls []string
}

func (f *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/config/apps/http/servers/edge"):
		if !f.loaded {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("{}"))
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		f.loaded = true
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
	case r.URL.Path == "/config/apps/http/servers/edge/tls_connection_policies":
		b, _ := io.ReadAll(r.Body)
		f.policies = string(b)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/id/"):
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		var route map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &route)
		for i, old := range f.routes {
			if old["@id"] == id {
				f.routes[i] = route
				return
			}
		}
		http.Error(w, `{"error":"unknown object ID '`+id+`'"}`, http.StatusNotFound)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/id/"):
		id := strings.TrimPrefix(r.URL.Path, "/id/")
		for i, route := range f.routes {
			if route["@id"] == id {
				f.routes = append(f.routes[:i], f.routes[i+1:]...)
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
	if len(fake.routes) != 1 || !strings.Contains(string(b), "169.254.1.3:20002") {
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

func TestPanelRouteSetsTheEdgeHeaders(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	if err := New(srv.URL).ApplyPanel(context.Background(), "panel.example.com", 19080, 19081); err != nil {
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
	if err := c.ApplyPanel(ctx, "panel.example.com", 19080, 19081); err != nil {
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
