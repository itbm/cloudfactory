// Package panel sends reports from the agent to the panel API (/api/agent/v1/...).
package panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/pki"
)

// Client calls the agent routes of the panel.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// Retries is the number of times to try a report again when the panel is not available.
	Retries int
	Backoff time.Duration

	mu    sync.Mutex
	caPin string
}

// New gives a client. The agent sends its own certificate, so that the panel can check it.
//
// serverPin is the SHA-256 fingerprint of the certificate of the panel, or "". With a pin, the
// client accepts only that certificate and does not use the public certificate authorities.
// The agent on the panel server uses a pin, because it connects to the HTTPS port of the API
// on the service address, which has no public name. With no pin, the client checks the
// certificate of the panel domain in the usual way.
//
// caPin is the SHA-256 fingerprint of the root certificate of the panel CA, or "". With this
// pin, the client also accepts a certificate of the panel domain from the panel CA
// (pki.PinnedCA). The other servers use it when the panel has a certificate from its own CA.
// The panel gives the pin in the answer to each report. SetCAPin changes it.
func New(baseURL, token string, cert *tls.Certificate, serverPin, caPin string) *Client {
	c := &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Retries: 5, Backoff: 2 * time.Second, caPin: caPin}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cert != nil {
		tr.TLSClientConfig.Certificates = []tls.Certificate{*cert}
	}
	// The pins replace the checks of Go. Go calls VerifyConnection for each connection, also
	// for a resumed session.
	tr.TLSClientConfig.InsecureSkipVerify = true
	if serverPin != "" {
		// The pin replaces the checks of the name and of the certificate chain.
		tr.TLSClientConfig.VerifyConnection = pki.PinnedServer(serverPin)
	} else {
		host := ""
		if u, err := url.Parse(baseURL); err == nil {
			host = u.Hostname()
		}
		// PinnedCA checks the name and the chain, with the system roots and the panel CA. With
		// no pin, it checks only with the system roots, as Go does.
		tr.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			return pki.PinnedCA(c.CAPin(), host, nil)(cs)
		}
	}
	c.HTTP = &http.Client{Timeout: 30 * time.Second, Transport: tr}
	return c
}

// CAPin gives the fingerprint of the root certificate of the panel CA, or "".
func (c *Client) CAPin() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caPin
}

// SetCAPin changes the fingerprint of the root certificate of the panel CA. New connections use
// it. Connections that are open stay open.
func (c *Client) SetCAPin(fingerprint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caPin = fingerprint
}

// Event is the progress or the result of a job.
type Event struct {
	State  string         `json:"state"` // "running", "succeeded" or "failed"
	Step   int            `json:"step"`
	Error  string         `json:"error,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

// JobEvent reports the progress or the result of a job.
func (c *Client) JobEvent(ctx context.Context, jobID string, e Event) error {
	return c.post(ctx, "/api/agent/v1/jobs/"+jobID+"/events", e, nil)
}

// Report is the state of the host (FR-1118).
type Report struct {
	AgentVersion   string    `json:"agentVersion"`
	Kernel         string    `json:"kernel,omitempty"`
	PodmanVersion  string    `json:"podmanVersion,omitempty"`
	GVisorVersion  string    `json:"gvisorVersion,omitempty"`
	PendingUpdates []any     `json:"pendingUpdates"`
	RebootRequired bool      `json:"rebootRequired"`
	Services       []Service `json:"services"`
	Usage          *HostUse  `json:"usage,omitempty"`
	Websites       []Usage   `json:"websites"`
	// Domains are the DNS and certificate state of each domain on this server.
	Domains []DomainState `json:"domains"`
	// Databases are the sizes of the databases in the database servers of this server.
	Databases []DatabaseSize `json:"databases"`
	// DatabaseServers are the state and the resource use of each database server of a customer
	// on this server.
	DatabaseServers []DatabaseServerUse `json:"databaseServers"`
	// DatabaseEngines are the database engines that this agent can operate. An agent of an
	// earlier release does not send them: it has MariaDB and PostgreSQL only.
	DatabaseEngines []string `json:"databaseEngines,omitempty"`
	// Runtimes are the website runtimes that this agent can run: the built-in runtimes and
	// the runtimes of the packs.
	Runtimes []string `json:"runtimes,omitempty"`
	// Packs are the packs of the server (SetPacks). The panel sends its packs again to a
	// server that does not have them.
	Packs []PackState `json:"packs"`
	// Features are the optional features that are on, for example "valkey-backups".
	Features []string `json:"features,omitempty"`
	// DatabaseInstances is true when this agent gives each customer its own database servers
	// (DatabaseServer, Website.database_instance). An agent of an earlier release does not
	// send it.
	DatabaseInstances bool `json:"databaseInstances,omitempty"`
	// DNS is true when this agent can operate a DNS server (SetDnsZones). An agent of an
	// earlier release does not send it.
	DNS bool `json:"dns,omitempty"`
	// Addresses are the public IP addresses of this server. The panel uses them for the DNS
	// records of the websites on this server.
	Addresses []string `json:"addresses,omitempty"`
	// DNSZones are the DNS zones of this server, with the serial that the DNS server loaded.
	// The panel sends each zone that this server does not have, for example after the server
	// was offline or lost its data. An agent that cannot operate DNS sends null.
	DNSZones []DNSZoneState `json:"dnsZones"`
	// SftpPort is the port that the website owners use for SFTP on this server.
	SftpPort int `json:"sftpPort,omitempty"`
	// PanelCertificate is the certificate of the panel domain. Only the panel server sends it.
	PanelCertificate *PanelCertificate `json:"panelCertificate,omitempty"`
	// Mesh is the tunnel key of this server and the state of its tunnels to other servers. An
	// agent of an earlier release does not send it.
	Mesh *Mesh `json:"mesh,omitempty"`
	// PanelCASHA256 is the fingerprint of the root certificate of the panel CA that this agent
	// trusts. The panel changes to its own CA only when all other servers trust it.
	PanelCASHA256 string `json:"panelCaSha256,omitempty"`
	// Mail is the state of the mail server of this server. A server with no mail server does
	// not send it.
	Mail *MailReport `json:"mail,omitempty"`
	// CronRuns are the results of the cron jobs that ran since the last reports.
	CronRuns []CronRun `json:"cronRuns,omitempty"`
}

// PackState is the name and the version of one pack of the server.
type PackState struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

// MailReport is the state of the mail server and the size of each mailbox.
type MailReport struct {
	// State is "running" or "stopped".
	State     string       `json:"state"`
	Mailboxes []MailboxUse `json:"mailboxes"`
}

// MailboxUse is the size of the messages of one mailbox.
type MailboxUse struct {
	Address   string `json:"address"`
	UsedBytes int64  `json:"usedBytes"`
}

// CronRun is the result of one run of a cron job of a website.
type CronRun struct {
	WebsiteID string `json:"websiteId"`
	// ID is the ID of the cron job.
	ID string `json:"id"`
	// At is the start time (RFC 3339).
	At string `json:"at"`
	// ExitCode is the exit code of the command. -1 means that the command did not start.
	ExitCode int `json:"exitCode"`
	// Output is the end of the output of the command.
	Output string `json:"output"`
}

// Mesh is the state of the encrypted tunnels of one server (package mesh).
type Mesh struct {
	// PublicKey is the public tunnel key of this server. The private key stays on the server.
	PublicKey string `json:"publicKey"`
	// Version is the version of the mesh state that the server has. 0 means none.
	Version int64 `json:"version"`
	// Address is the mesh address of this server, or "" when it is not in the mesh.
	Address string   `json:"address,omitempty"`
	Tunnels []Tunnel `json:"tunnels"`
	// Networks is true: this agent can operate the members of the networks of the customers
	// (package mesh, network.go). An agent from before the networks does not send it.
	Networks bool `json:"networks"`
	// Websites is true: on this agent, a website can be a network member that answers on
	// port 80.
	Websites bool `json:"websites"`
	// Moves is true: this agent can send a database server or a website to a different
	// server through the tunnel, and receive one (SendDatabaseServer, ReceiveDatabaseServer,
	// SendWebsite and ReceiveWebsite).
	Moves bool `json:"moves"`
}

// Tunnel is the state of the tunnel to one other server.
type Tunnel struct {
	// Server is the ID of the other server in the panel.
	Server string `json:"server"`
	// HandshakeAt is the time of the last handshake in seconds since 1970. 0 means never.
	HandshakeAt int64 `json:"handshakeAt"`
	// Reachable is true when the other server answered an echo request through the tunnel.
	Reachable bool `json:"reachable"`
	// RoundTripMicros is the round-trip time of that request in microseconds.
	RoundTripMicros int64 `json:"roundTripMicros,omitempty"`
}

// PanelCertificate is the certificate method of the panel domain and the state of the
// certificate that the edge proxy serves.
type PanelCertificate struct {
	Domain string `json:"domain"`
	// Method is "internal" (the CA of the panel) or "letsencrypt".
	Method string `json:"method"`
	// State is "valid", "pending" (the edge proxy has no certificate of the method yet) or
	// "expired".
	State     string `json:"state"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	// CACertificate is the root certificate of the panel CA in PEM format. Browsers and other
	// servers trust the internal certificate when they trust this root.
	CACertificate string `json:"caCertificate,omitempty"`
}

// DNSZoneState is one DNS zone of a server in a report. Serial is 0 when the DNS server has
// the zone but did not load it.
type DNSZoneState struct {
	Name   string `json:"name"`
	Serial int64  `json:"serial"`
}

// DomainState is what the agent sees for one domain (FR-402, FR-403).
type DomainState struct {
	Name          string `json:"name"`
	DNSPointsHere bool   `json:"dnsPointsHere"`
	// Certificate is "valid", "pending", "failed" or "expired".
	Certificate string `json:"certificate"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// DatabaseSize is the size of one database. A Valkey database has no size in bytes: it has its
// database number and its number of keys.
type DatabaseSize struct {
	Engine    string `json:"engine"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	Number    int    `json:"number,omitempty"`
	Keys      *int64 `json:"keys,omitempty"`
}

// DatabaseServerUse is the state and the resource use of one database server of a customer.
type DatabaseServerUse struct {
	// ID is the ID of the database server in lower case.
	ID string `json:"id"`
	// State is "running" or "stopped".
	State       string `json:"state"`
	MemoryBytes int64  `json:"memoryBytes"`
	// DiskBytes is the disk space that the database server uses. DiskSizeBytes is the size of
	// its disk, or 0 when the server does not apply a disk limit to it.
	DiskBytes     int64 `json:"diskBytes"`
	DiskSizeBytes int64 `json:"diskSizeBytes"`
}

// HostUse is the resource use of the host, in percent (FR-1301).
type HostUse struct {
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryPercent float64 `json:"memoryPercent"`
	DiskPercent   float64 `json:"diskPercent"`
	// NetworkInBytes and NetworkOutBytes are the bytes that the network interfaces of the
	// server received and sent since the last report. The agent does not count the loopback
	// interface and the virtual interfaces of the containers and of the mesh.
	NetworkInBytes  int64 `json:"networkInBytes"`
	NetworkOutBytes int64 `json:"networkOutBytes"`
}

type Service struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Detail tells the administrator why the service does not run, and what to do.
	Detail string `json:"detail,omitempty"`
	// Optional is true for a part that a server can operate without. The panel does not show
	// the server as degraded when an optional part is off.
	Optional bool `json:"optional,omitempty"`
}

// Usage is the resource use of one website (FR-203).
type Usage struct {
	ID          string  `json:"id"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
	DiskBytes   int64   `json:"diskBytes"`
	Processes   int64   `json:"processes"`
	// BytesIn and BytesOut are the traffic of the domains of the website through the edge
	// proxy since the last report: the sizes of the requests and of the answers.
	BytesIn  int64 `json:"bytesIn"`
	BytesOut int64 `json:"bytesOut"`
}

// ReportResponse is the answer of the panel to a report.
type ReportResponse struct {
	// PanelCASHA256 is the fingerprint of the root certificate of the panel CA, or "" when the
	// panel does not know it yet.
	PanelCASHA256 string `json:"panelCaSha256"`
}

// SendReport sends the state of the host.
func (c *Client) SendReport(ctx context.Context, r Report) (ReportResponse, error) {
	var resp ReportResponse
	err := c.post(ctx, "/api/agent/v1/report", r, &resp)
	return resp, err
}

// JoinRequest is the request of "cf-agent join" (FR-1201).
type JoinRequest struct {
	Token             string   `json:"token"`
	Name              string   `json:"name"`
	AgentURL          string   `json:"agentUrl"`
	CertificateSHA256 string   `json:"certificateSha256"`
	CertificatePEM    string   `json:"certificatePem"`
	AgentVersion      string   `json:"agentVersion"`
	OS                string   `json:"os"`
	OSName            string   `json:"osName"`
	Kernel            string   `json:"kernel"`
	Arch              string   `json:"arch"`
	BareMetal         bool     `json:"bareMetal"`
	PodmanVersion     string   `json:"podmanVersion,omitempty"`
	GVisorVersion     string   `json:"gvisorVersion,omitempty"`
	Roles             []string `json:"roles"`
}

type JoinResponse struct {
	ServerID               string `json:"serverId"`
	AgentToken             string `json:"agentToken"`
	PanelCertificateSHA256 string `json:"panelCertificateSha256"`
	// PanelCASHA256 is the fingerprint of the root certificate of the panel CA, or "".
	PanelCASHA256 string `json:"panelCaSha256"`
}

// Join adds this server to the panel. The join token works once, so Join does not try again.
func (c *Client) Join(ctx context.Context, r JoinRequest) (JoinResponse, error) {
	var resp JoinResponse
	c.Retries = 0
	err := c.post(ctx, "/api/agent/v1/join", r, &resp)
	return resp, err
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.Backoff * time.Duration(1<<(attempt-1))):
			}
		}
		retry, err := c.once(ctx, path, b, out)
		if err == nil {
			return nil
		}
		last = err
		if !retry {
			return err
		}
	}
	return last
}

func (c *Client) once(ctx context.Context, path string, body []byte, out any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return true, fmt.Errorf("panel: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return true, fmt.Errorf("panel: %s: status %d", path, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &msg)
		return false, fmt.Errorf("panel: %s: status %d: %s", path, resp.StatusCode, msg.Message)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return false, json.Unmarshal(data, out)
	}
	return false, nil
}
