// Package actions turns each typed request from the panel into a job with steps.
// The step names agree with the steps that the panel shows (api/app/Enums/AgentAction.php).
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
)

// Edge is the part of the Caddy client that the actions use.
type Edge interface {
	EnsureBase(ctx context.Context) error
	Apply(ctx context.Context, s caddy.Site) error
	Remove(ctx context.Context, websiteID string) error
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
	// Health checks a website port. It returns an error when the website does not answer.
	Health func(ctx context.Context, port int, hostname string) error
	// DNSPointsHere reports whether a domain resolves to this server.
	DNSPointsHere func(ctx context.Context, domain string) bool
	// Reboot reboots the host. Restart restarts the agent.
	Reboot  func(ctx context.Context) error
	Restart func()
	Arch    string
	// BlockDevice is the disk for the I/O limit of each website.
	BlockDevice string
	// ReadyTimeout is the maximum time for a new container to answer.
	ReadyTimeout time.Duration
	// MaxWatch is the maximum time that the agent watches a website after a runtime change.
	MaxWatch time.Duration
	// ResticPasswordFile holds the encryption key of the backups.
	ResticPasswordFile string
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

// FreePort gives a port that no site uses. A runtime change uses two ports for a short time.
func (s *State) FreePort(also ...int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, site := range f.Sites {
		used[site.Port] = true
	}
	for _, p := range also {
		used[p] = true
	}
	for p := s.FirstPort; p <= s.LastPort; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("actions: no free port from %d to %d", s.FirstPort, s.LastPort)
}

// NewState gives a State in the data directory.
func NewState(dataDir string) *State {
	return &State{Path: filepath.Join(dataDir, "sites.json"), FirstPort: 20000, LastPort: 29999}
}
