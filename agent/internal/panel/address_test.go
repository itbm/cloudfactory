package panel

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestDefaultAddress(t *testing.T) {
	hosts := map[string][]string{
		"web2":             {"127.0.1.1"},
		"web3":             {"203.0.113.30"},
		"web4.localdomain": {"203.0.113.40"},
		"web2.example.com": {"203.0.113.20"},
		"both.example.com": {"127.0.1.1", "2001:db8::20"},
		"link.example.com": {"169.254.1.3", "::1"},
	}
	lookup := func(_ context.Context, host string) ([]string, error) {
		if a, ok := hosts[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	var asked string
	outbound := func(ip string) func(string) (net.IP, error) {
		return func(hostPort string) (net.IP, error) {
			asked = hostPort
			if ip == "" {
				return nil, errors.New("network is unreachable")
			}
			return net.ParseIP(ip), nil
		}
	}
	cases := []struct {
		name, hostname, panel, outbound, want, wantAsked string
	}{
		// A new server: /etc/hosts gives 127.0.1.1 for the host name. No other computer finds it.
		{"host name only in /etc/hosts", "web2", "https://panel.example.com", "198.51.100.7", "198.51.100.7", "panel.example.com:443"},
		{"host name not known", "srv", "https://panel.example.com:8443", "198.51.100.7", "198.51.100.7", "panel.example.com:8443"},
		// cloud-init can write the public address of the server for its short name. The panel does not have this record.
		{"short name with a public address in /etc/hosts", "web3", "https://panel.example.com", "203.0.113.30", "203.0.113.30", "panel.example.com:443"},
		{"name in localdomain", "web4.localdomain", "https://panel.example.com", "203.0.113.40", "203.0.113.40", "panel.example.com:443"},
		{"full name that the server does not find", "web9.example.com", "https://panel.example.com", "198.51.100.7", "198.51.100.7", "panel.example.com:443"},
		{"host name in the DNS", "web2.example.com", "https://panel.example.com", "198.51.100.7", "web2.example.com", ""},
		{"host name with a loopback and a public address", "both.example.com", "https://panel.example.com", "198.51.100.7", "both.example.com", ""},
		{"host name with only addresses of the computer itself", "link.example.com", "https://panel.example.com", "2001:db8::7", "2001:db8::7", "panel.example.com:443"},
		{"an IPv6 address of the panel", "web2", "https://[2001:db8::1]", "2001:db8::7", "2001:db8::7", "[2001:db8::1]:443"},
		{"no route to the panel", "web2", "https://panel.example.com", "", "web2", "panel.example.com:443"},
		{"the route is the loopback interface", "web2", "https://panel.example.com", "127.0.0.1", "web2", "panel.example.com:443"},
	}
	for _, c := range cases {
		asked = ""
		got := AddressSource{Lookup: lookup, Outbound: outbound(c.outbound)}.Default(context.Background(), c.hostname, c.panel)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if asked != c.wantAsked {
			t.Errorf("%s: asked for the route to %q, want %q", c.name, asked, c.wantAsked)
		}
	}
}

func TestAgentURL(t *testing.T) {
	cases := map[[2]string]string{
		{"web2.example.com", ":7443"}: "https://web2.example.com:7443",
		{"203.0.113.20", ":7443"}:     "https://203.0.113.20:7443",
		{"2001:db8::20", ":7443"}:     "https://[2001:db8::20]:7443",
		{"web2", "0.0.0.0:7443"}:      "https://web2:7443",
		{"169.254.1.3", "[::]:7443"}:  "https://169.254.1.3:7443",
	}
	for in, want := range cases {
		if got := AgentURL(in[0], in[1]); got != want {
			t.Errorf("AgentURL(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
