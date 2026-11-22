// Package caddy sets the routes of the edge proxy through the Caddy admin API.
//
// Each website has one route with the ID "cf-<website ID>". The route sends all requests to the
// website container, which also serves the static files (FR-309). It adds the website ID to
// the entry of each request in the access log, which Caddy sends to the agent. The edge proxy cannot read the
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
	c := &Client{Admin: strings.TrimRight(admin, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}, Upstream: quadlet.ServiceAddress}
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
	// Stopped is true for a website that is stopped, for example because its customer is
	// suspended. The edge proxy then answers with StoppedPage, not with an empty 502 answer.
	Stopped bool
	// Page is the text that the edge proxy gives for each request, with status 200. It is for
	// a host name that has no website but needs a certificate: the host name of the mail
	// server. The edge proxy gets the certificate, and the agent gives it to the mail server.
	Page string
	// Paths send some paths to a different port. The webmail is on the host name of the mail
	// server, and the JMAP paths go to the mail server: the browser then uses one origin.
	Paths []PathRoute
	// NoAuthorization is a pattern for the Authorization header, or "". The edge proxy refuses
	// a request whose header agrees with the pattern. The mail server uses it: its
	// administrator must not sign in from the internet.
	NoAuthorization string
	// Path is a path prefix such as "/01jbq3z5k8n6/", or "". With a prefix, the route has only
	// the paths that start with it, so that more websites can have the same host. The database
	// tool of each customer uses this (Adminer): all customers have one host.
	Path string
}

// PathRoute sends the requests for some paths to a port. A path that ends with "*" is a prefix.
// With port 0, the edge proxy refuses the requests with status 404. With methods, the route is
// only for requests with one of these methods.
type PathRoute struct {
	Paths   []string
	Methods []string
	Port    int
}

// StoppedPage is the page of a stopped website. It has status 503. It does not tell a visitor
// why the website is stopped.
const StoppedPage = `<!doctype html>
<html lang="en-GB">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Website not available</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">
<h1>This website is not available</h1>
<p>The website is stopped. If you are the owner of the website, sign in to your hosting panel or contact your hosting provider.</p>
</body>
</html>
`

// EnsureBase loads the base configuration when Caddy has no "edge" server. When Caddy has
// one, it sets the logging configuration of this agent version, if it is different.
func (c *Client) EnsureBase(ctx context.Context) error {
	status, _, err := c.do(ctx, http.MethodGet, "/config/apps/http/servers/"+serverName, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return c.expect(ctx, http.MethodPost, "/load", BaseConfig(), http.StatusOK)
	}
	status, body, err := c.do(ctx, http.MethodGet, "/config/logging", nil)
	if err != nil {
		return err
	}
	want, err := json.Marshal(LoggingConfig())
	if err != nil {
		return err
	}
	if status == http.StatusOK && sameJSON(body, want) {
		return nil
	}
	return c.expect(ctx, http.MethodPost, "/config/logging", LoggingConfig(), http.StatusOK)
}

// sameJSON reports whether two JSON documents have the same values.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

// LogAddress is the address in the edge container to which Caddy sends the access log: the
// receiver of the worker on the private service address (sitelogs.Receiver).
var LogAddress = "tcp/" + quadlet.ServiceAddress + ":" + strconv.Itoa(quadlet.EdgeLogPort)

// LoggingConfig is the logging configuration of the edge proxy. Caddy sends each entry of the
// access log as one line of JSON to the agent. When the agent does not answer, Caddy writes
// the entries to its standard error, and connects again after 10 seconds. The route of each
// website adds the field "cf_website" (SiteRoute), so the agent knows the website of each
// entry.
func LoggingConfig() map[string]any {
	return map[string]any{"logs": map[string]any{
		"access": map[string]any{
			"writer": map[string]any{"output": "net", "address": LogAddress, "dial_timeout": "2s", "soft_start": true},
			"encoder": map[string]any{
				"format":          "json",
				"time_format":     "rfc3339_nano",
				"duration_format": "seconds",
			},
			"include": []string{"http.log.access"},
		},
		// The access log has each request one time: not also in the default log.
		"default": map[string]any{"exclude": []string{"http.log.access"}},
	}}
}

// logField adds the ID of the website to the entry of each request of a route in the access
// log. The agent puts the entry in the access log of that website.
func logField(websiteID string) map[string]any {
	return map[string]any{"handler": "log_append", "key": "cf_website", "value": websiteID, "early": true}
}

// BaseConfig is the configuration of a new edge proxy. It sends the access log to the agent,
// and it counts the traffic of each host.
func BaseConfig() map[string]any {
	return map[string]any{
		// CF-13: a Unix socket, not a TCP port. Only the edge user and root can open it.
		"admin":   map[string]any{"listen": "unix/" + AdminSocket},
		"logging": LoggingConfig(),
		"apps": map[string]any{"http": map[string]any{
			// The host firewall redirects 80 and 443 to these ports. The "edge" server listens
			// for HTTPS. Caddy adds a server on the HTTP port that redirects to HTTPS and
			// answers ACME HTTP challenges.
			"http_port":  8080,
			"https_port": 8443,
			// The traffic of each host, for the bandwidth of each website (Traffic).
			"metrics": MetricsConfig(),
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
	// The certificate policy comes before the routes. With the routes first, Caddy would
	// start to get a certificate for a local name from its default CA.
	if err := c.applyLocalPolicy(ctx, s); err != nil {
		return err
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

// Remove deletes the routes and the certificate policy of a website. A route that does not
// exist is not an error.
func (c *Client) Remove(ctx context.Context, websiteID string) error {
	for _, id := range []string{"cf-" + websiteID, "cf-" + websiteID + "-redirect", localPolicyID(websiteID)} {
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

// SiteRoute is the route of the primary domain and the aliases. The route of a stopped website
// keeps the domains, so that the edge proxy keeps their certificates.
func SiteRoute(s Site, upstream string) map[string]any {
	proxy := func(port int) map[string]any {
		return map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": []any{map[string]any{"dial": upstream + ":" + strconv.Itoa(port)}},
		}
	}
	handler := proxy(s.Port)
	if len(s.Paths) > 0 {
		routes := make([]any, 0, len(s.Paths)+2)
		if s.NoAuthorization != "" {
			routes = append(routes, map[string]any{
				"match":    []any{map[string]any{"header_regexp": map[string]any{"Authorization": map[string]any{"pattern": s.NoAuthorization}}}},
				"handle":   []any{map[string]any{"handler": "static_response", "status_code": 403}},
				"terminal": true,
			})
		}
		for _, p := range s.Paths {
			handle := proxy(p.Port)
			if p.Port == 0 {
				handle = map[string]any{"handler": "static_response", "status_code": 404}
			}
			match := map[string]any{"path": p.Paths}
			if len(p.Methods) > 0 {
				match["method"] = p.Methods
			}
			routes = append(routes, map[string]any{"match": []any{match}, "handle": []any{handle}, "terminal": true})
		}
		routes = append(routes, map[string]any{"handle": []any{handler}})
		handler = map[string]any{"handler": "subroute", "routes": routes}
	}
	if s.Stopped {
		handler = map[string]any{
			"handler":     "static_response",
			"status_code": 503,
			"headers": map[string]any{
				"Content-Type":  []string{"text/html; charset=utf-8"},
				"Cache-Control": []string{"no-store"},
			},
			"body": StoppedPage,
		}
	}
	if s.Page != "" {
		handler = map[string]any{
			"handler":     "static_response",
			"status_code": 200,
			"headers":     map[string]any{"Content-Type": []string{"text/plain; charset=utf-8"}},
			"body":        s.Page,
		}
	}
	match := hostMatch(s.Hosts)
	if s.Path != "" {
		match["path"] = []string{s.Path + "*"}
	}
	return map[string]any{
		"@id":      "cf-" + s.WebsiteID,
		"match":    []any{match},
		"handle":   []any{logField(s.WebsiteID), handler},
		"terminal": true,
	}
}

// plainHost is the pattern of a Host header that the routes accept: a name or an address, with
// no port or with the HTTPS port.
const plainHost = `^([^:\[\]]+|\[[^\]]+\])(:(443|8443))?$`

// hostMatch gives the matcher of a route for its hosts. The host matcher of Caddy ignores the
// port of the Host header, and the metrics of Caddy keep one set of counters for each Host
// header of a request that a route accepts. Thus a visitor could make a new set of counters
// with each request ("name:1", "name:2", ...) until the edge proxy has no memory left
// (adversarial review, finding G6-02). With this matcher, no route accepts such a request, and
// Caddy counts nothing for it.
func hostMatch(hosts []string) map[string]any {
	return map[string]any{
		"host":          hosts,
		"header_regexp": map[string]any{"Host": map[string]any{"pattern": plainHost}},
	}
}

// RedirectRoute sends the redirect domains to the primary domain.
func RedirectRoute(s Site) map[string]any {
	return map[string]any{
		"@id":   "cf-" + s.WebsiteID + "-redirect",
		"match": []any{hostMatch(s.Redirects)},
		"handle": []any{logField(s.WebsiteID), map[string]any{
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
// go to the web UI. method is the certificate method of the panel domain: PanelInternal or
// PanelLetsEncrypt.
func (c *Client) ApplyPanel(ctx context.Context, domain, method string, apiPort, webPort int) error {
	// Set the certificate method before the route. With the route first, Caddy would start an
	// ACME order for the domain with the default issuer.
	if err := c.SetPanelCertificate(ctx, domain, method); err != nil {
		return err
	}
	// The reseller panel domains stay in the route (SetPanelBrands).
	brands, err := c.PanelBrands(ctx)
	if err != nil {
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
		"match": []any{hostMatch(PanelHosts(domain, brands))},
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
