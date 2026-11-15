package caddy

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Traffic is the number of bytes that the edge proxy received from the visitors of one host
// (In) and sent to them (Out), from the start of the edge proxy. The values are the sizes of
// the requests and of the answers. They do not include TLS and TCP.
type Traffic struct {
	In  int64
	Out int64
}

// MetricsConfig is the metrics part of the HTTP app. With per_host, Caddy gives the metrics of
// each host that a route names. A request for a different host has the host "_other".
func MetricsConfig() map[string]any {
	return map[string]any{"per_host": true}
}

// EnsureMetrics turns on the metrics of each host. An edge proxy of an earlier release has no
// metrics. Caddy loads the configuration again only when it changes.
func (c *Client) EnsureMetrics(ctx context.Context) error {
	status, body, err := c.do(ctx, http.MethodGet, "/config/apps/http/metrics", nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK && strings.Contains(string(body), `"per_host":true`) {
		return nil
	}
	return c.expect(ctx, http.MethodPost, "/config/apps/http/metrics", MetricsConfig(), http.StatusOK)
}

// Traffic gives the traffic of each host from the metrics of the edge proxy. The keys are the
// host names in lower case.
func (c *Client) Traffic(ctx context.Context) (map[string]Traffic, error) {
	if err := c.EnsureMetrics(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Admin+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caddy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caddy: GET /metrics: status %d", resp.StatusCode)
	}
	return ParseTraffic(bufio.NewScanner(resp.Body))
}

// The metrics of Caddy that give the sizes. Each is a histogram: the line "_sum" has the
// total of the sizes.
const (
	requestSizeSum  = "caddy_http_request_size_bytes_sum{"
	responseSizeSum = "caddy_http_response_size_bytes_sum{"
)

// ParseTraffic reads the metrics in the Prometheus text format, and adds the sizes of each
// host. It ignores the lines of other metrics and lines that it cannot read.
func ParseTraffic(s *bufio.Scanner) (map[string]Traffic, error) {
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	out := map[string]Traffic{}
	for s.Scan() {
		line := s.Text()
		in := strings.HasPrefix(line, requestSizeSum)
		if !in && !strings.HasPrefix(line, responseSizeSum) {
			continue
		}
		end := strings.LastIndexByte(line, '}')
		if end < 0 {
			continue
		}
		host := label(line[:end], "host")
		if host == "" || host == "_other" {
			continue
		}
		value := strings.Fields(line[end+1:])
		if len(value) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(value[0], 64)
		if err != nil || v < 0 {
			continue
		}
		// A request to a different port has the port in its host.
		if h, _, ok := strings.Cut(host, ":"); ok && !strings.Contains(h, "[") {
			host = h
		}
		host = strings.ToLower(host)
		t := out[host]
		if in {
			t.In += int64(v)
		} else {
			t.Out += int64(v)
		}
		out[host] = t
	}
	return out, s.Err()
}

// label gives the value of a label in the labels of a metric line, or "". labels starts with
// "{".
func label(labels, name string) string {
	for _, sep := range []string{"{", ","} {
		if i := strings.Index(labels, sep+name+`="`); i >= 0 {
			rest := labels[i+len(sep)+len(name)+2:]
			if end := strings.IndexByte(rest, '"'); end >= 0 {
				return rest[:end]
			}
		}
	}
	return ""
}
