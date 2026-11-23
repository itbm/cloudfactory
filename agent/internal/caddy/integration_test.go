//go:build integration

package caddy

import (
	"context"
	"net/http"
	"os"
	"strings"
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

// TestRealCaddyCertificates checks the certificates from the panel with a real edge proxy:
// the edge proxy sends a certificate from the panel for its hosts, it still gets its own
// certificate for a different host in the same domain, and the panel route keeps its
// connection policy. Set CF_CADDY_ADMIN and CF_CADDY_HTTPS.
func TestRealCaddyCertificates(t *testing.T) {
	admin, address := os.Getenv("CF_CADDY_ADMIN"), os.Getenv("CF_CADDY_HTTPS")
	if admin == "" || address == "" {
		t.Skip("set CF_CADDY_ADMIN and CF_CADDY_HTTPS")
	}
	c := New(admin)
	ctx := context.Background()
	if err := c.EnsureBase(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := testCertificates(t)
	owner := Site{WebsiteID: "ICERTA", Port: 20003, Hosts: []string{"example.com", "www.example.com"}, Primary: "example.com"}
	other := Site{WebsiteID: "ICERTB", Port: 20004, Hosts: []string{"other.example.com"}, Primary: "other.example.com"}
	for _, s := range []Site{owner, other} {
		if err := c.Apply(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.ApplyPanel(ctx, "panel.localhost", PanelInternal, 19080, 19081); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCertificates(ctx, []Certificate{a, b}); err != nil {
		t.Fatal(err)
	}
	// A second call with the same list changes nothing.
	if err := c.SetCertificates(ctx, []Certificate{a, b}); err != nil {
		t.Fatal(err)
	}
	served := func(name string) string {
		chain, err := ServedCertificate(ctx, address, name)
		if err != nil || len(chain) == 0 {
			return ""
		}
		return LeafFingerprint(chain[0])
	}
	for _, h := range []string{"example.com", "www.example.com"} {
		if got := served(h); got != Fingerprint(a.CertificatePEM) {
			t.Errorf("%s: served %q, want the certificate from the panel", h, got)
		}
	}
	if got := served("shop.example.org"); got != Fingerprint(b.CertificatePEM) {
		t.Errorf("shop.example.org: served %q", got)
	}
	// The panel keeps its certificate from the panel CA.
	if got := served("panel.localhost"); got == "" || got == Fingerprint(a.CertificatePEM) || got == Fingerprint(b.CertificatePEM) {
		t.Errorf("panel.localhost: served %q", got)
	}
	// Caddy still manages the certificate of the other host in the same domain.
	status, body, err := c.do(ctx, http.MethodGet, automaticHTTPSPath, nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), `"ignore_loaded_certificates":true`) || strings.Contains(string(body), "other.example.com") {
		t.Errorf("automatic_https = %d %s %v", status, body, err)
	}
	loaded, err := c.Certificates(ctx)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("Certificates = %+v, %v", loaded, err)
	}
	t.Logf("loaded: %+v", loaded)

	if err := c.SetCertificates(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := served("shop.example.org"); got == Fingerprint(b.CertificatePEM) {
		t.Error("shop.example.org: the edge proxy still sends the certificate after its removal")
	}
	if loaded, err := c.Certificates(ctx); err != nil || len(loaded) != 0 {
		t.Errorf("Certificates after the removal = %+v, %v", loaded, err)
	}
	for _, s := range []Site{owner, other} {
		if err := c.Remove(ctx, s.WebsiteID); err != nil {
			t.Fatal(err)
		}
	}
}
