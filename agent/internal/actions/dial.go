package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// Second audit, the privilege boundary: the local connections of the worker.
//
// The firewall of the installer lets only root and the edge proxy connect to the private
// service address, and only root and the websites connect to the database network. The
// admin socket of the edge proxy is in a directory that only the edge user and root can
// open. The worker of the agent is not root. It opens each of these connections through
// the root helper, and the helper connects only to the sockets and the addresses of RootDial.
// Thus the firewall needs no rule for the user of the worker.

const (
	// dialEdge is the name of the admin socket of the edge proxy.
	dialEdge = "edge"
	// dialTCP starts the name of a TCP address: "tcp:<address>:<port>".
	dialTCP = "tcp:"

	// The ports of the edge proxy on the host (quadlet.Edge).
	edgeHTTPPort  = 8080
	edgeHTTPSPort = 8443
)

// DialLocal opens a TCP connection to an address of this server that the firewall keeps for
// root: a port of the private service address, or a database server of a customer.
func (d *Deps) DialLocal(ctx context.Context, address string) (net.Conn, error) {
	if d.Gate != nil {
		return d.Gate.Dial(ctx, dialTCP+address)
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return dialer.DialContext(ctx, "tcp", address)
}

// DialEdge opens a connection to the admin socket of the edge proxy.
func (d *Deps) DialEdge(ctx context.Context) (net.Conn, error) {
	if d.Gate != nil {
		return d.Gate.Dial(ctx, dialEdge)
	}
	if d.EdgeSocket == "" {
		return nil, errors.New("the admin API of the edge proxy is not a Unix socket")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", d.EdgeSocket)
}

// RootDial is the list of the connections that the root helper opens for the worker
// (gate.Server.Dial). d is the Deps of the helper.
func (d *Deps) RootDial(ctx context.Context, name string) (net.Conn, error) {
	if name == dialEdge {
		return d.DialEdge(ctx)
	}
	address, ok := strings.CutPrefix(name, dialTCP)
	if !ok {
		return nil, errors.New("the name is not in the list")
	}
	if err := d.allowAddress(address); err != nil {
		return nil, err
	}
	return d.DialLocal(ctx, address)
}

// allowAddress checks a TCP address from the worker. The worker can connect to:
//
//   - the port of a website, the HTTPS port of the edge proxy, the port of the SFTP
//     container and the agent port of the panel, on the private service address;
//   - a Valkey database server on the address of its customer. The agent manages the users
//     of Valkey through a connection. It manages MariaDB and PostgreSQL with a client in the
//     container.
//
// It cannot connect to the database of the panel, or to an address that is not on this server.
func (d *Deps) allowAddress(address string) error {
	host, p, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(p)
	if err != nil || strconv.Itoa(port) != p {
		return fmt.Errorf("%q is not a port", p)
	}
	if host == quadlet.ServiceAddress {
		website := d.State != nil && port >= d.State.FirstPort && port <= d.State.LastPort
		if website || port == edgeHTTPSPort || port == d.sftpPort() || port == quadlet.PanelAgentPort {
			return nil
		}
		return fmt.Errorf("the worker cannot connect to port %d of the service address", port)
	}
	if _, err := quadlet.DatabaseNumber(host); err != nil {
		return fmt.Errorf("%q is not an address that the worker can connect to", host)
	}
	return quadlet.CheckDatabasePort("valkey", port)
}
