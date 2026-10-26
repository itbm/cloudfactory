//go:build integration

package caddy

import (
	"context"
	"net/http"
	"os"
	"testing"
)

// Run against a real edge image:
//
//	podman run -d -p 127.0.0.1:12019:2019 -p 127.0.0.1:18080:8080 ghcr.io/cloudfactory/edge:VERSION
//	CF_CADDY_ADMIN=http://127.0.0.1:12019 CF_CADDY_HTTP=http://127.0.0.1:18080 go test -tags integration ./internal/caddy/
func TestRealCaddy(t *testing.T) {
	admin, web := os.Getenv("CF_CADDY_ADMIN"), os.Getenv("CF_CADDY_HTTP")
	if admin == "" || web == "" {
		t.Skip("set CF_CADDY_ADMIN and CF_CADDY_HTTP")
	}
	c := New(admin)
	ctx := context.Background()
	if err := c.EnsureBase(ctx); err != nil {
		t.Fatal(err)
	}
	site := Site{WebsiteID: "ITEST", UnixUser: "acme_x7k2", Port: 20001, Hosts: []string{"acme.example"}, Redirects: []string{"www.acme.example"}, Primary: "acme.example", Static: true}
	// Remove before Apply checks the answer of Caddy for an ID that does not exist.
	if err := c.Remove(ctx, site.WebsiteID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := c.Apply(ctx, site); err != nil {
			t.Fatal(err)
		}
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, web+"/a/b?c=1", nil)
	req.Host = "www.acme.example"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Caddy redirects HTTP to HTTPS first for a domain with automatic HTTPS.
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("status = %d, Location = %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	t.Logf("www.acme.example → %d %s", resp.StatusCode, resp.Header.Get("Location"))

	if err := c.ApplyPanel(ctx, "panel.example.com", 19080, 19081); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, site.WebsiteID); err != nil {
		t.Fatal(err)
	}
}
