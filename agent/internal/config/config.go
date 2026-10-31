// Package config reads the settings of the agent from /etc/cloudfactory/agent.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/itbm/cloudfactory/agent/internal/gvisor"
)

// DefaultPath is where "cf-agent join" writes the settings.
const DefaultPath = "/etc/cloudfactory/agent.json"

type Config struct {
	// PanelURL is the address of the panel, for example "https://panel.example.com".
	PanelURL string `json:"panelUrl"`
	// AgentToken authenticates the agent to the panel. The panel gives it at join.
	AgentToken string `json:"agentToken"`
	// ServerID is the ID of this server in the panel.
	ServerID string `json:"serverId"`
	// Listen is the address of the agent port, for example ":7443".
	Listen string `json:"listen"`
	// PanelCertificateSHA256 is the fingerprint of the client certificate of the panel.
	// The agent accepts calls only from a client with this certificate (FR-1706).
	PanelCertificateSHA256 string `json:"panelCertificateSha256"`
	// Registry is the registry of the Cloudfactory images. Set a mirror here (FR-1813).
	Registry string `json:"registry"`
	// GVisorPlatform is "systrap" (the default) or "kvm" on bare metal (FR-310).
	GVisorPlatform string `json:"gvisorPlatform"`
	// GVisorVersion is the gVisor release that the agent installs, for example "20261019.0".
	GVisorVersion string `json:"gvisorVersion"`
	// DataDir holds the state of the agent: certificates and finished jobs.
	DataDir string `json:"dataDir"`
	// SitesDir holds the files of each website, one directory for each Unix user.
	SitesDir string `json:"sitesDir"`
	// CaddyAdmin is the address of the Caddy admin API: a Unix socket (CF-13).
	CaddyAdmin string `json:"caddyAdmin"`
	// DatabaseClientHost is the host part of the MariaDB users that only the website containers
	// can use (CF-19). The default is the address that the database server sees for them,
	// quadlet.ServiceAddress.
	DatabaseClientHost string `json:"databaseClientHost"`
	// ReleaseURL is where the agent downloads new agent binaries.
	ReleaseURL string `json:"releaseUrl"`
	// ReportSeconds is the time between two host reports.
	ReportSeconds int `json:"reportSeconds"`
	// PublicAddresses are the public IP addresses of a server behind NAT, for example on a
	// cloud provider. The DNS check accepts them as addresses of this server.
	PublicAddresses []string `json:"publicAddresses,omitempty"`
}

// Defaults gives the values for the settings that the file does not set.
func Defaults() Config {
	return Config{
		Listen:         ":7443",
		Registry:       "ghcr.io/cloudfactory",
		GVisorPlatform: "systrap",
		GVisorVersion:  gvisor.Version,
		DataDir:        "/var/lib/cloudfactory",
		SitesDir:       "/srv/cloudfactory/sites",
		CaddyAdmin:     "unix:///var/lib/cloudfactory/caddy-admin/admin.sock",
		ReleaseURL:     "https://github.com/itbm/cloudfactory/releases/download",
		ReportSeconds:  60,
	}
}

// Load reads the file and puts the defaults in the empty settings.
func Load(path string) (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("read %s: %w", path, err)
	}
	return c, c.Check()
}

// Check makes sure that the settings that the agent needs are set.
func (c Config) Check() error {
	var errs []error
	if c.PanelURL == "" {
		errs = append(errs, errors.New("panelUrl is not set"))
	}
	if c.AgentToken == "" {
		errs = append(errs, errors.New("agentToken is not set; run cf-agent join"))
	}
	if c.GVisorPlatform != "systrap" && c.GVisorPlatform != "kvm" {
		errs = append(errs, fmt.Errorf("gvisorPlatform must be systrap or kvm, not %q", c.GVisorPlatform))
	}
	return errors.Join(errs...)
}

// Save writes the file. Only root can read it, because it holds the agent token.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CertFile is the certificate of the agent port.
func (c Config) CertFile() string { return filepath.Join(c.DataDir, "pki", "agent.crt") }

// KeyFile is the private key of the agent port.
func (c Config) KeyFile() string { return filepath.Join(c.DataDir, "pki", "agent.key") }
