// Package actions turns each typed request from the panel into a job with steps.
// The step names agree with the steps that the panel shows (api/app/Enums/AgentAction.php).
package actions

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/images"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
)

// Edge is the part of the Caddy client that the actions use.
type Edge interface {
	EnsureBase(ctx context.Context) error
	Apply(ctx context.Context, s caddy.Site) error
	Remove(ctx context.Context, websiteID string) error
	// PanelCertificate gives the certificate method of the panel domain, or "" with no panel.
	PanelCertificate(ctx context.Context, domain string) (string, error)
	SetPanelCertificate(ctx context.Context, domain, method string) error
	// PanelCARoot gives the root certificate of the CA of the panel in PEM format.
	PanelCARoot(ctx context.Context) (string, error)
}

// Deps are the parts of the host that the actions change.
type Deps struct {
	Host    *host.Host
	Run     execx.Runner
	Edge    Edge
	Apt     hostupdate.Apt
	Updater *selfupdate.Updater
	Gvisor  quadlet.Runtime
	State   *State
	// Images is the shared read-only store of the runtime images. Nil means that each website
	// user pulls its own copy, as for a custom image.
	Images *images.Store
	// Health requests the home page of a website port and gives the HTTP status.
	Health func(ctx context.Context, port int, hostname string) (int, error)
	// DNSPointsHere reports whether a domain resolves to this server.
	DNSPointsHere func(ctx context.Context, domain string) bool
	// Reboot reboots the host. Restart restarts the agent.
	Reboot  func(ctx context.Context) error
	Restart func()
	// Stopping is closed when the agent gets a stop signal, for example at a shutdown. A nil
	// channel never closes.
	Stopping <-chan struct{}
	// RebootWait is the maximum time between the reboot command and the stop signal. The
	// default is 10 minutes.
	RebootWait time.Duration
	// DomainWait is the time that a status report waits for a check of the domains. DomainLimit
	// is the time limit for one check of all domains. The defaults are 5 seconds and 2 minutes.
	DomainWait  time.Duration
	DomainLimit time.Duration
	Arch        string
	// BlockDevice is the disk for the I/O limit of each website.
	BlockDevice string
	// QuotaMount is the mount point of SitesDir when user quotas are on there. Empty means that
	// the agent cannot apply the disk limit of a website.
	QuotaMount string
	// ReadyTimeout is the maximum time for a new container to answer.
	ReadyTimeout time.Duration
	// MaxWatch is the maximum time that the agent watches a website after a runtime change.
	MaxWatch time.Duration
	// ResticPasswordFile holds the encryption key of the backups.
	ResticPasswordFile string
	// DataDir holds the state of the agent. Only root can read it. The agent keeps database
	// dumps and restores there, not in the home directory of a website (CF-05, CF-06).
	DataDir string
	// SecretsDir holds short-lived files with passwords for the database clients. The
	// default is "/run/cloudfactory/secrets".
	SecretsDir string
	// DatabaseClientHost is the host part of a MariaDB user that can connect from the website
	// containers only, for example "10.89.%" (CF-19). The default is quadlet.ServiceAddress.
	DatabaseClientHost string
	// ServiceImage gives the image of a service, for example ServiceImage("mariadb").
	ServiceImage func(name string) string
	// DBHome is the home directory of cf-db. The default is "/var/lib/cf-db".
	DBHome string
	// ValkeyMemoryMB is the memory limit of the shared Valkey container for its data, in MB.
	// 0 means one eighth of the memory of the server, from 64 MB to 1024 MB.
	ValkeyMemoryMB int
	// ValkeyDial opens a connection to the shared Valkey container. Nil means a connection to
	// its port on quadlet.ServiceAddress. Tests replace it.
	ValkeyDial func(ctx context.Context) (net.Conn, error)
	// SftpKeysDir holds the SSH keys of each website user. The default is "/etc/cloudfactory/sftp".
	SftpKeysDir string
	// SftpUsersDir holds the user list of the SFTP container. The default is
	// "/etc/cloudfactory/sftp-users".
	SftpUsersDir string
	// SftpImage is the image of the SFTP container of this agent version.
	SftpImage string
	// SftpPort is the public SFTP port of the server. 0 means quadlet.DefaultSftpPort.
	SftpPort int
	// SftpReady checks that the SFTP container answers on a port. Nil means an SSH connection
	// to quadlet.ServiceAddress. Tests replace it.
	SftpReady func(ctx context.Context, port int) error
	// SystemUnitDir holds the Quadlet units that root runs. The default is "/etc/containers/systemd".
	SystemUnitDir string
	// FirewallFile is the nftables file of the installer. The default is
	// "/etc/nftables.d-cloudfactory.nft".
	FirewallFile string
	// HostSftpConfig is the SSH configuration of the host from release 0.1. The default is
	// "/etc/ssh/sshd_config.d/50-cloudfactory-sftp.conf".
	HostSftpConfig string

	// SavePlatform keeps the gVisor platform in the settings of the agent (ConfigureHost).
	SavePlatform func(platform string) error
	// EdgeImage is the image of the edge proxy of this agent version.
	EdgeImage string

	// CgroupRoot is "/sys/fs/cgroup". Tests use a different directory.
	CgroupRoot string
	// Certificate gives the expiry time of the certificate of a domain. Tests replace it.
	Certificate func(ctx context.Context, domain string) (time.Time, error)
	// ServedCertificate gives the certificate chain that the edge proxy serves for a domain.
	// Nil means a TLS connection to the edge proxy. Tests replace it.
	ServedCertificate func(ctx context.Context, domain string) ([]*x509.Certificate, error)
	// PublicRoots are the public certificate authorities for the check of a certificate from
	// Let's Encrypt. Nil means the system roots. Tests replace them.
	PublicRoots *x509.CertPool
	// PanelCertificateWait is the time that the agent waits for a new certificate of the
	// panel, and PanelCertificatePoll is the time between two checks. The defaults are
	// 2 minutes and 2 seconds.
	PanelCertificateWait time.Duration
	PanelCertificatePoll time.Duration

	dbMu sync.Mutex
	// valkeyMu makes the search for a free Valkey database number and its user one step.
	valkeyMu sync.Mutex
	sftpMu   sync.Mutex
	dbReady  map[string]bool
	// backupSlots limits the backups that operate at the same time (BackupSlots).
	backupSlots chan struct{}
	rtMu        sync.RWMutex
	obs         observer
}

// runtime gives the gVisor settings. ConfigureHost can change the platform.
func (d *Deps) runtime() quadlet.Runtime {
	d.rtMu.RLock()
	defer d.rtMu.RUnlock()
	return d.Gvisor
}

// Site is what the agent knows about one website.
type Site struct {
	ID       string            `json:"id"`
	UnixUser string            `json:"unixUser"`
	Port     int               `json:"port"`
	Suffix   string            `json:"suffix"`
	Website  *agentv1.Website  `json:"-"`
	Raw      json.RawMessage   `json:"website"`
	Extra    map[string]string `json:"extra,omitempty"`
	// Stopped is true when the owner stopped the website. Then a change of the runtime does not
	// start it.
	Stopped bool `json:"stopped,omitempty"`
	// NextPort is the port of the new container while a change of the runtime is in work, or 0.
	// The state keeps it, so that no other website gets the same port in that time.
	NextPort int `json:"nextPort,omitempty"`
}

// State keeps the sites and their ports in a file, so that the agent knows them after a restart.
type State struct {
	Path string
	mu   sync.Mutex
	// FirstPort and LastPort are the host ports for the website containers.
	FirstPort, LastPort int
}

type stateFile struct {
	Sites map[string]*Site `json:"sites"`
}

func (s *State) read() (stateFile, error) {
	f := stateFile{Sites: map[string]*Site{}}
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	for _, site := range f.Sites {
		site.Website = &agentv1.Website{}
		if len(site.Raw) > 0 {
			if err := unmarshalWebsite(site.Raw, site.Website); err != nil {
				return f, err
			}
		}
	}
	return f, nil
}

func (s *State) write(f stateFile) error {
	for _, site := range f.Sites {
		raw, err := marshalWebsite(site.Website)
		if err != nil {
			return err
		}
		site.Raw = raw
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	_, err = host.WriteFile(s.Path, string(b), 0o600)
	return err
}

// Get gives a copy of a site, or nil.
func (s *State) Get(id string) (*Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	return f.Sites[id], nil
}

// All gives all sites.
func (s *State) All() ([]*Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]*Site, 0, len(f.Sites))
	for _, site := range f.Sites {
		out = append(out, site)
	}
	return out, nil
}

// Put records a site.
func (s *State) Put(site *Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	f.Sites[site.ID] = site
	return s.write(f)
}

// Delete removes a site.
func (s *State) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	delete(f.Sites, id)
	return s.write(f)
}

// freePort gives a port that no site uses and that no change of a runtime has taken. The
// caller holds the lock and records the port before it releases the lock.
func (s *State) freePort(f stateFile) (int, error) {
	used := map[int]bool{}
	for _, site := range f.Sites {
		used[site.Port] = true
		if site.NextPort != 0 {
			used[site.NextPort] = true
		}
	}
	for p := s.FirstPort; p <= s.LastPort; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("actions: no free port from %d to %d", s.FirstPort, s.LastPort)
}

// Reserve records a site and gives its port. A site that the state knows keeps its port. A new
// site gets a free port. The search and the record are one step with one lock. The runner
// starts the jobs of different websites at the same time, and with two steps two websites
// could get the same port (audit finding B6).
func (s *State) Reserve(site *Site) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return 0, err
	}
	if old := f.Sites[site.ID]; old != nil {
		site.Port = old.Port
	} else if site.Port, err = s.freePort(f); err != nil {
		return 0, err
	}
	f.Sites[site.ID] = site
	return site.Port, s.write(f)
}

// ReserveNext takes a second port for a site and records it in the same step. A change of the
// runtime starts the new container on this port while the old container continues. The port
// stays taken until the change is complete (Put) or stops (ReleaseNext). A port from a change
// that did not end, for example because the agent stopped, is used again.
func (s *State) ReserveNext(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return 0, err
	}
	site := f.Sites[id]
	if site == nil {
		return 0, fmt.Errorf("actions: the state does not have the website %s", id)
	}
	if site.NextPort != 0 {
		return site.NextPort, nil
	}
	if site.NextPort, err = s.freePort(f); err != nil {
		return 0, err
	}
	return site.NextPort, s.write(f)
}

// ReleaseNext gives the second port of a site back after a change of the runtime that failed.
func (s *State) ReleaseNext(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	site := f.Sites[id]
	if site == nil || site.NextPort == 0 {
		return nil
	}
	site.NextPort = 0
	return s.write(f)
}

// NewState gives a State in the data directory.
func NewState(dataDir string) *State {
	return &State{Path: filepath.Join(dataDir, "sites.json"), FirstPort: 20000, LastPort: 29999}
}
