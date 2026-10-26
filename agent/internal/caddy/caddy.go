// Package caddy sets the routes of the edge proxy through the Caddy admin API.
//
// Each website has one route with the ID "cf-<website ID>". The route serves static files
// directly and sends all other requests to the website container (FR-309). Redirect domains
// have a second route with the ID "cf-<website ID>-redirect" (FR-402).
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const serverName = "edge"

// Client calls the admin API of Caddy.
type Client struct {
	// Admin is the address of the admin API, for example "http://127.0.0.1:2019".
	Admin string
	HTTP  *http.Client
	// Upstream is the host name that Caddy uses to reach the website ports on the host.
	Upstream string
}

// New gives a client with a short timeout.
func New(admin string) *Client {
	return &Client{Admin: strings.TrimRight(admin, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}, Upstream: "host.containers.internal"}
}

// Site is the input for the routes of one website.
type Site struct {
	WebsiteID string
	UnixUser  string
	Port      int
	// Hosts are the primary domain and the aliases. Caddy gets a TLS certificate for each.
	Hosts []string
	// Redirects go to the primary domain with status 308.
	Redirects []string
	Primary   string
	// Static is false for a custom image, which serves all requests itself.
	Static bool
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
		"admin": map[string]any{"listen": "0.0.0.0:2019"},
		"logging": map[string]any{"logs": map[string]any{
			"access": map[string]any{"encoder": map[string]any{"format": "json"}, "include": []string{"http.log.access"}},
		}},
		"apps": map[string]any{"http": map[string]any{
			// The host firewall redirects 80 and 443 to these ports.
			"http_port":  8080,
			"https_port": 8443,
			"servers": map[string]any{serverName: map[string]any{
				"listen": []string{":8080", ":8443"},
				"routes": []any{},
				"logs":   map[string]any{"default_logger_name": "access"},
			}},
		}},
	}
}

// Apply sets the routes of a website. It is idempotent: it removes the old routes first.
func (c *Client) Apply(ctx context.Context, s Site) error {
	if err := c.Remove(ctx, s.WebsiteID); err != nil {
		return err
	}
	routes := "/config/apps/http/servers/" + serverName + "/routes"
	if len(s.Redirects) > 0 {
		if err := c.expect(ctx, http.MethodPost, routes, RedirectRoute(s), http.StatusOK); err != nil {
			return err
		}
	}
	return c.expect(ctx, http.MethodPost, routes, SiteRoute(s, c.Upstream), http.StatusOK)
}

// Remove deletes the routes of a website. A route that does not exist is not an error.
func (c *Client) Remove(ctx context.Context, websiteID string) error {
	for _, id := range []string{"cf-" + websiteID, "cf-" + websiteID + "-redirect"} {
		status, body, err := c.do(ctx, http.MethodDelete, "/id/"+id, nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK && status != http.StatusNotFound && !unknownID(body) {
			return fmt.Errorf("caddy: delete %s: status %d: %s", id, status, body)
		}
	}
	return nil
}

// Caddy answers 400 or 404 with "unknown object ID" for an ID that it does not have.
func unknownID(body []byte) bool { return bytes.Contains(body, []byte("unknown object ID")) }

// SiteRoute is the route of the primary domain and the aliases.
func SiteRoute(s Site, upstream string) map[string]any {
	proxy := map[string]any{
		"handler":   "reverse_proxy",
		"upstreams": []any{map[string]any{"dial": upstream + ":" + strconv.Itoa(s.Port)}},
	}
	var handle []any
	if s.Static {
		root := "/srv/sites/" + s.UnixUser + "/app/public"
		handle = []any{map[string]any{"handler": "subroute", "routes": []any{
			map[string]any{
				// Serve a file directly when it exists. PHP files, hidden files and
				// directories always go to the container.
				"match": []any{map[string]any{
					"file": map[string]any{"root": root, "try_files": []string{"{http.request.uri.path}"}},
					"not":  []any{map[string]any{"path": []string{"*.php", "*/", "/.*", "*/.*"}}},
				}},
				"handle": []any{map[string]any{"handler": "file_server", "root": root}},
			},
			map[string]any{"handle": []any{proxy}},
		}}}
	} else {
		handle = []any{proxy}
	}
	return map[string]any{
		"@id":      "cf-" + s.WebsiteID,
		"match":    []any{map[string]any{"host": s.Hosts}},
		"handle":   handle,
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
	if err := c.Remove(ctx, "panel"); err != nil {
		return err
	}
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
	return c.expect(ctx, http.MethodPost, "/config/apps/http/servers/"+serverName+"/routes", route, http.StatusOK)
}
