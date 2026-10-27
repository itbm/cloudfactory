// Package caddy sets the routes of the edge proxy through the Caddy admin API.
//
// Each website has one route with the ID "cf-<website ID>". The route sends all requests to the
// website container, which also serves the static files (FR-309). The edge proxy cannot read the
// files of the websites, so a link in one website cannot show the files of a different website
// (CF-04). Redirect domains have a second route with the ID "cf-<website ID>-redirect" (FR-402).
//
// The admin API listens on a Unix socket that only the edge user and root can open (CF-13).
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const serverName = "edge"

// AdminSocket is the path of the admin socket in the edge container. The host directory of the
// socket is mounted at /run/caddy-admin.
const AdminSocket = "/run/caddy-admin/admin.sock"

// Client calls the admin API of Caddy.
type Client struct {
	// Admin is the base address of the admin API, for example "http://caddy" for a Unix socket
	// or "http://127.0.0.1:2019" in a test.
	Admin string
	HTTP  *http.Client
	// Upstream is the host name that Caddy uses to reach the website ports on the host.
	Upstream string
}

// New gives a client with a short timeout. The address is "unix:///path/to/admin.sock" or an
// HTTP address.
func New(admin string) *Client {
	c := &Client{Admin: strings.TrimRight(admin, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}, Upstream: quadlet.HostLoopback}
	if path, ok := strings.CutPrefix(admin, "unix://"); ok {
		c.Admin = "http://caddy"
		c.HTTP.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
	}
	return c
}

// Site is the input for the routes of one website.
type Site struct {
	WebsiteID string
	Port      int
	// Hosts are the primary domain and the aliases. Caddy gets a TLS certificate for each.
	Hosts []string
	// Redirects go to the primary domain with status 308.
	Redirects []string
	Primary   string
}

// EnsureBase loads the base configuration when Caddy has no "edge" server.
func (c *Client) EnsureBase(ctx context.Context) error {
	status, _, err := c.do(ctx, http.MethodGet, "/config/apps/http/servers/"+serverName, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	return c.expect(ctx, http.MethodPost, "/load", BaseConfig(), http.StatusOK)
}

// BaseConfig is the configuration of a new edge proxy. It logs requests as JSON.
func BaseConfig() map[string]any {
	return map[string]any{
		// CF-13: a Unix socket, not a TCP port. Only the edge user and root can open it.
		"admin": map[string]any{"listen": "unix/" + AdminSocket},
		"logging": map[string]any{"logs": map[string]any{
			"access": map[string]any{"encoder": map[string]any{"format": "json"}, "include": []string{"http.log.access"}},
		}},
		"apps": map[string]any{"http": map[string]any{
			// The host firewall redirects 80 and 443 to these ports. The "edge" server listens
			// for HTTPS. Caddy adds a server on the HTTP port that redirects to HTTPS and
			// answers ACME HTTP challenges.
			"http_port":  8080,
			"https_port": 8443,
			"servers": map[string]any{serverName: map[string]any{
				"listen": []string{":8443"},
				"routes": []any{},
				"logs":   map[string]any{"default_logger_name": "access"},
			}},
		}},
	}
}

// Apply sets the routes of a website. It is idempotent. It replaces a route that exists in one
// request, so that the website has a route at all times (FR-1108, audit batch 2, finding 7).
// A website with no verified domain has no route. A route with no host would match all hosts.
func (c *Client) Apply(ctx context.Context, s Site) error {
	if len(s.Hosts) == 0 {
		return c.Remove(ctx, s.WebsiteID)
	}
	redirect := "cf-" + s.WebsiteID + "-redirect"
	if len(s.Redirects) > 0 {
		if err := c.put(ctx, redirect, RedirectRoute(s)); err != nil {
			return err
		}
	} else if err := c.remove(ctx, redirect); err != nil {
		return err
	}
	return c.put(ctx, "cf-"+s.WebsiteID, SiteRoute(s, c.Upstream))
}

// put replaces the route with an ID, or adds it to the edge server when it does not exist.
func (c *Client) put(ctx context.Context, id string, route map[string]any) error {
	status, body, err := c.do(ctx, http.MethodPatch, "/id/"+id, route)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusNotFound || unknownID(body):
		return c.expect(ctx, http.MethodPost, "/config/apps/http/servers/"+serverName+"/routes", route, http.StatusOK)
	}
	return fmt.Errorf("caddy: patch %s: status %d: %s", id, status, body)
}

// Remove deletes the routes of a website. A route that does not exist is not an error.
func (c *Client) Remove(ctx context.Context, websiteID string) error {
	for _, id := range []string{"cf-" + websiteID, "cf-" + websiteID + "-redirect"} {
		if err := c.remove(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) remove(ctx context.Context, id string) error {
	status, body, err := c.do(ctx, http.MethodDelete, "/id/"+id, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNotFound && !unknownID(body) {
		return fmt.Errorf("caddy: delete %s: status %d: %s", id, status, body)
	}
	return nil
}

// Caddy answers 400 or 404 with "unknown object ID" for an ID that it does not have.
func unknownID(body []byte) bool { return bytes.Contains(body, []byte("unknown object ID")) }

// SiteRoute is the route of the primary domain and the aliases.
func SiteRoute(s Site, upstream string) map[string]any {
	return map[string]any{
		"@id":   "cf-" + s.WebsiteID,
		"match": []any{map[string]any{"host": s.Hosts}},
		"handle": []any{map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": []any{map[string]any{"dial": upstream + ":" + strconv.Itoa(s.Port)}},
		}},
		"terminal": true,
	}
}

// RedirectRoute sends the redirect domains to the primary domain.
func RedirectRoute(s Site) map[string]any {
	return map[string]any{
		"@id":   "cf-" + s.WebsiteID + "-redirect",
		"match": []any{map[string]any{"host": s.Redirects}},
		"handle": []any{map[string]any{
			"handler":     "static_response",
			"status_code": 308,
			"headers":     map[string]any{"Location": []string{"https://" + s.Primary + "{http.request.uri}"}},
		}},
		"terminal": true,
	}
}

func (c *Client) expect(ctx context.Context, method, path string, body any, want int) error {
	status, resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status != want {
		return fmt.Errorf("caddy: %s %s: status %d: %s", method, path, status, resp)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Admin+path, r)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("caddy: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// ApplyPanel sets the route of the panel: /api/* goes to the API, and all other paths
// go to the web UI.
func (c *Client) ApplyPanel(ctx context.Context, domain string, apiPort, webPort int) error {
	dial := func(port int) []any { return []any{map[string]any{"dial": c.Upstream + ":" + strconv.Itoa(port)}} }
	api := map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": dial(apiPort),
		// The API knows that the request came through the edge proxy, and sees the fingerprint
		// of the client certificate. These values replace the values that a client sends.
		"headers": map[string]any{"request": map[string]any{"set": map[string]any{
			"X-Cloudfactory-Edge":  []string{"1"},
			"X-Client-Cert-Sha256": []string{"{http.request.tls.client.fingerprint}"},
		}}},
	}
	route := map[string]any{
		"@id":   "cf-panel",
		"match": []any{map[string]any{"host": []string{domain}}},
		"handle": []any{map[string]any{"handler": "subroute", "routes": []any{
			// R-17: browsers use only HTTPS for the panel domain after the first visit.
			map[string]any{"handle": []any{map[string]any{"handler": "headers", "response": map[string]any{"set": map[string]any{
				"Strict-Transport-Security": []string{"max-age=31536000"},
			}}}}},
			map[string]any{"match": []any{map[string]any{"path": []string{"/api/*", "/up"}}}, "handle": []any{api}},
			map[string]any{"handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": dial(webPort)}}},
		}}},
		"terminal": true,
	}
	// Ask for a client certificate on the panel domain, but do not require it. Agents send one.
	policies := []any{
		map[string]any{"match": map[string]any{"sni": []string{domain}}, "client_authentication": map[string]any{"mode": "request"}},
		map[string]any{},
	}
	if err := c.expect(ctx, http.MethodPatch, "/config/apps/http/servers/"+serverName+"/tls_connection_policies", policies, http.StatusOK); err != nil {
		if err := c.expect(ctx, http.MethodPut, "/config/apps/http/servers/"+serverName+"/tls_connection_policies", policies, http.StatusOK); err != nil {
			return err
		}
	}
	// A route that exists is replaced in place, with no gap. It is already the first route.
	status, body, err := c.do(ctx, http.MethodPatch, "/id/cf-panel", route)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound && !unknownID(body) {
		return fmt.Errorf("caddy: patch cf-panel: status %d: %s", status, body)
	}
	// CF-03: the panel route is always the first route, so that no website route for the
	// same domain can come before it. A PUT on an index of a list puts the value at that index.
	return c.expect(ctx, http.MethodPut, "/config/apps/http/servers/"+serverName+"/routes/0", route, http.StatusOK)
}
