package panel

import (
	"context"
	"net"
	"net/url"
	"strings"
)

// AddressSource finds the address that the panel uses to connect to this server, when the
// administrator gives none with --address.
//
// The host name is a good address only when a different computer can find it: it is a full
// name such as web2.example.com, and it has an address that is not for the computer itself. On
// most new servers, the host name is a short name such as web2, and /etc/hosts gives 127.0.1.1
// for it. Before, such a server joined with its host name: the panel showed it as healthy, but
// each job for it failed. Thus the default is now the IP address that this server uses for its
// connection to the panel, when the host name is not sufficient.
type AddressSource struct {
	// Lookup gives the IP addresses of a host name. Nil uses the resolver of the system.
	Lookup func(ctx context.Context, host string) ([]string, error)
	// Outbound gives the local IP address of a connection to the host and the port of the
	// panel. Nil asks the kernel for the route, and sends no data.
	Outbound func(hostPort string) (net.IP, error)
}

// Default gives the host name when a different computer can use it, and the IP address of the
// connection to the panel in other cases. If the two are not available, it gives the host name.
func (a AddressSource) Default(ctx context.Context, hostname, panelURL string) string {
	lookup := a.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	if fullName(hostname) {
		if addrs, err := lookup(ctx, hostname); err == nil && reachable(addrs) {
			return hostname
		}
	}
	outbound := a.Outbound
	if outbound == nil {
		outbound = outboundIP
	}
	if u, err := url.Parse(panelURL); err == nil && u.Hostname() != "" {
		port := u.Port()
		if port == "" {
			port = "443"
		}
		if ip, err := outbound(net.JoinHostPort(u.Hostname(), port)); err == nil && usable(ip) {
			return ip.String()
		}
	}
	return hostname
}

// fullName is true for a name with a domain, such as web2.example.com. A short name such as
// web2 can be in /etc/hosts of this server with a public address, but the panel cannot find it.
// The names in "localhost" and "localdomain" are only for the computer itself.
func fullName(hostname string) bool {
	name := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if !strings.Contains(name, ".") {
		return false
	}
	for _, own := range []string{".localhost", ".localdomain"} {
		if strings.HasSuffix(name, own) {
			return false
		}
	}
	return true
}

// reachable is true when one address or more is not for the computer itself.
func reachable(addrs []string) bool {
	for _, a := range addrs {
		if usable(net.ParseIP(a)) {
			return true
		}
	}
	return false
}

func usable(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast()
}

// outboundIP gives the local address of the route to the panel. A UDP socket that connects
// sends no packet.
func outboundIP(hostPort string) (net.IP, error) {
	conn, err := net.Dial("udp", hostPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP, nil
}

// AgentURL gives the address of the agent port for the panel, for example
// https://web2.example.com:7443 or https://[2001:db8::1]:7443. listen is the listen address of
// the agent, for example ":7443".
func AgentURL(address, listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = strings.TrimPrefix(listen, ":")
	}
	return "https://" + net.JoinHostPort(address, port)
}
