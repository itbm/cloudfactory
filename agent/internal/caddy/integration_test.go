//go:build integration

package caddy

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// Run against a real edge image:
//
//	mkdir -p /tmp/cf-caddy-admin
//	podman run -d -v /tmp/cf-caddy-admin:/run/caddy-admin -p 127.0.0.1:18080:8080 -p 127.0.0.1:18443:8443 ghcr.io/cloudfactory/edge:VERSION
//	CF_CADDY_ADMIN=unix:///tmp/cf-caddy-admin/admin.sock CF_CADDY_HTTP=http://127.0.0.1:18080 CF_CADDY_HTTPS=127.0.0.1:18443 go test -tags integration ./internal/caddy/
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
	site := Site{WebsiteID: "ITEST", Port: 20001, Hosts: []string{"acme.example"}, Redirects: []string{"www.acme.example"}, Primary: "acme.example"}
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

	if err := c.ApplyPanel(ctx, "panel.example.com", PanelInternal, 19080, 19081); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, site.WebsiteID); err != nil {
		t.Fatal(err)
	}
	testPanelCertificate(t, c)
}

// testPanelCertificate checks the certificate methods of the panel domain. Set CF_CADDY_HTTPS to
// the HTTPS address of the edge proxy, for example 127.0.0.1:18443.
func testPanelCertificate(t *testing.T, c *Client) {
	ctx := context.Background()
	if err := c.ApplyPanel(ctx, "panel.localhost", PanelInternal, 19080, 19081); err != nil {
		t.Fatal(err)
	}
	if m, err := c.PanelCertificate(ctx, "panel.localhost"); err != nil || m != PanelInternal {
		t.Fatalf("PanelCertificate = %q, %v", m, err)
	}
	root, err := c.PanelCARoot(ctx)
	if err != nil || root == "" {
		t.Fatalf("PanelCARoot = %q, %v", root, err)
	}
	address := os.Getenv("CF_CADDY_HTTPS")
	if address == "" {
		return
	}
	check := func(method string) error {
		chain, err := ServedCertificate(ctx, address, "panel.localhost")
		if err != nil {
			return err
		}
		return CheckPanelCertificate(chain, "panel.localhost", method, root, nil, time.Now())
	}
	var last error
	for range 20 {
		if last = check(PanelInternal); last == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("internal certificate: %v", last)
	}
	chain, _ := ServedCertificate(ctx, address, "panel.localhost")
	t.Logf("panel.localhost: %d certificates, issuer %q, expires %s", len(chain), chain[0].Issuer.CommonName, chain[0].NotAfter)
	// Caddy cannot get a certificate from Let's Encrypt for this name. It then serves no
	// certificate, and the agent goes back to the internal method.
	if err := c.SetPanelCertificate(ctx, "panel.localhost", PanelLetsEncrypt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := check(PanelLetsEncrypt); err == nil {
		t.Error("a certificate from Let's Encrypt for panel.localhost")
	} else {
		t.Logf("with Let's Encrypt: %v", err)
	}
	if err := c.SetPanelCertificate(ctx, "panel.localhost", PanelInternal); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if last = check(PanelInternal); last == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("internal certificate after the change back: %v", last)
	}
	testLocalWebsite(t, c, address, root)
}

// testLocalWebsite checks that a website on a local name gets a certificate from the panel
// CA, and not from the default CA of Caddy.
func testLocalWebsite(t *testing.T, c *Client, address, root string) {
	ctx := context.Background()
	site := Site{WebsiteID: "ILOCAL", Port: 20002, Hosts: []string{"shop.localhost", "shop.example.net"}, Redirects: []string{"www.shop.localhost"}, Primary: "shop.localhost"}
	for range 2 {
		if err := c.Apply(ctx, site); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"shop.localhost", "www.shop.localhost"} {
		var last error
		for range 20 {
			chain, err := ServedCertificate(ctx, address, name)
			if last = err; err == nil {
				if last = CheckPanelCertificate(chain, name, PanelInternal, root, nil, time.Now()); last == nil {
					t.Logf("%s: issuer %q, expires %s", name, chain[0].Issuer.CommonName, chain[0].NotAfter)
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if last != nil {
			t.Errorf("%s: %v", name, last)
		}
	}
	// The panel keeps its certificate.
	if chain, err := ServedCertificate(ctx, address, "panel.localhost"); err != nil || CheckPanelCertificate(chain, "panel.localhost", PanelInternal, root, nil, time.Now()) != nil {
		t.Errorf("panel.localhost after the website: %v", err)
	}
	if err := c.Remove(ctx, site.WebsiteID); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := c.do(ctx, http.MethodGet, "/id/"+localPolicyID(site.WebsiteID), nil); status == http.StatusOK {
		t.Error("the policy of the website stays after the removal")
	}
}
