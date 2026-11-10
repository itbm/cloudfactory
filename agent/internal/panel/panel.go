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
	"strings"
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
}

// New gives a client. The agent sends its own certificate, so that the panel can check it.
//
// serverPin is the SHA-256 fingerprint of the certificate of the panel, or "". With a pin, the
// client accepts only that certificate and does not use the public certificate authorities.
// The agent on the panel server uses a pin, because it connects to the HTTPS port of the API
// on the service address, which has no public name. With no pin, the client checks the
// certificate of the panel domain in the usual way.
func New(baseURL, token string, cert *tls.Certificate, serverPin string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cert != nil {
		tr.TLSClientConfig.Certificates = []tls.Certificate{*cert}
	}
	if serverPin != "" {
		// The pin replaces the checks of the name and of the certificate chain. Go calls
		// VerifyConnection for each connection, also for a resumed session.
		tr.TLSClientConfig.InsecureSkipVerify = true
		tr.TLSClientConfig.VerifyConnection = pki.PinnedServer(serverPin)
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: tr}, Retries: 5, Backoff: 2 * time.Second}
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
	// Databases are the sizes of the databases in the shared database containers.
	Databases []DatabaseSize `json:"databases"`
	// SftpPort is the port that the website owners use for SFTP on this server.
	SftpPort int `json:"sftpPort,omitempty"`
}

// DomainState is what the agent sees for one domain (FR-402, FR-403).
type DomainState struct {
	Name          string `json:"name"`
	DNSPointsHere bool   `json:"dnsPointsHere"`
	// Certificate is "valid", "pending", "failed" or "expired".
	Certificate string `json:"certificate"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// DatabaseSize is the size of one database.
type DatabaseSize struct {
	Engine    string `json:"engine"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
}

// HostUse is the resource use of the host, in percent (FR-1301).
type HostUse struct {
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryPercent float64 `json:"memoryPercent"`
	DiskPercent   float64 `json:"diskPercent"`
}

type Service struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// Usage is the resource use of one website (FR-203).
type Usage struct {
	ID          string  `json:"id"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
	DiskBytes   int64   `json:"diskBytes"`
	Processes   int64   `json:"processes"`
}

// SendReport sends the state of the host.
func (c *Client) SendReport(ctx context.Context, r Report) error {
	return c.post(ctx, "/api/agent/v1/report", r, nil)
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
	if out != nil {
		return false, json.Unmarshal(data, out)
	}
	return false, nil
}
