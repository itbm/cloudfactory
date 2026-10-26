package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
}

func (f *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	case r.URL.Path == "/config/apps/http/servers/edge/tls_connection_policies":
		b, _ := io.ReadAll(r.Body)
		f.policies = string(b)
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
	site := Site{WebsiteID: "W1", UnixUser: "acme_x7k2", Port: 20001, Hosts: []string{"acme.example"}, Redirects: []string{"www.acme.example"}, Primary: "acme.example", Static: true}
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

func TestSiteRouteServesStaticFilesButNotPHP(t *testing.T) {
	b, _ := json.Marshal(SiteRoute(Site{WebsiteID: "W1", UnixUser: "acme_x7k2", Port: 20001, Hosts: []string{"acme.example"}, Static: true}, "host.containers.internal"))
	s := string(b)
	for _, want := range []string{`"@id":"cf-W1"`, `"root":"/srv/sites/acme_x7k2/app/public"`, `"*.php"`, `"dial":"host.containers.internal:20001"`} {
		if !strings.Contains(s, want) {
			t.Errorf("route does not contain %s: %s", want, s)
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
	for _, want := range []string{`"X-Client-Cert-Sha256":["{http.request.tls.client.fingerprint}"]`, `"X-Cloudfactory-Edge":["1"]`, `"/api/*"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("route does not contain %s: %s", want, b)
		}
	}
	if !strings.Contains(fake.policies, `"mode":"request"`) {
		t.Errorf("policies = %s", fake.policies)
	}
}
