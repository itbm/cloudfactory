package actions

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// HTTPHealth requests the home page of a website on its port on quadlet.ServiceAddress, as the
// edge proxy does.
// It gives the HTTP status. An error means that the website did not answer.
func HTTPHealth(ctx context.Context, port int, hostname string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(quadlet.ServiceAddress, strconv.Itoa(port))+"/", nil)
	if err != nil {
		return 0, err
	}
	req.Host = hostname
	req.Header.Set("User-Agent", "cloudfactory-agent/health")
	// The edge proxy terminates TLS. Without this header, many applications redirect to https.
	req.Header.Set("X-Forwarded-Proto", "https")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// LocalDNS reports whether a domain resolves to an address of this server. It also
// accepts the public addresses in extra, for a server behind NAT.
func LocalDNS(extra ...string) func(ctx context.Context, domain string) bool {
	return func(ctx context.Context, domain string) bool {
		// RFC 6761: localhost and each name below it is this computer. Not all resolvers answer
		// for these names, so the agent does not ask.
		if name := strings.ToLower(strings.TrimSuffix(domain, ".")); name == "localhost" || strings.HasSuffix(name, ".localhost") {
			return true
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, domain)
		if err != nil {
			return false
		}
		local := map[string]bool{}
		for _, e := range extra {
			local[e] = true
		}
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					local[n.IP.String()] = true
				}
			}
		}
		for _, ip := range ips {
			if local[ip.IP.String()] {
				return true
			}
		}
		return false
	}
}
