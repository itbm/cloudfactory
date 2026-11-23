package actions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/host"
)

const pathHash = "$2y$10$rwv0zN7amtbNiLIYD4lBl.AIDGf.p4y0hHxXqLDrkhORIHaBKMPuS"

func protection() *agentv1.WebsiteSecurity {
	return &agentv1.WebsiteSecurity{
		Firewall:           "block",
		FirewallExclusions: []int32{942100},
		FirewallSkipPaths:  []string{"/wp-admin/"},
		Allow:              []string{"203.0.113.7/24", "2001:db8::1"},
		Block:              []string{"198.51.100.7"},
		ProtectedPaths:     []*agentv1.ProtectedPath{{Path: "/staging/", Users: []*agentv1.PathUser{{Name: "client", PasswordHash: pathHash}}}},
		SignInLimit:        10,
		SignInPaths:        []string{"/wp-login.php"},
	}
}

// SetWebsiteSecurity changes the edge route and keeps the value with the website. The
// container does not restart.
func TestSetWebsiteSecurityChangesTheEdgeRoute(t *testing.T) {
	d, f, edge := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")})); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	if err := run(t)(d.SetWebsiteSecurity(&agentv1.SetWebsiteSecurityRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2", Security: protection()})); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "systemctl") {
			t.Errorf("the job changed the container: %s", c)
		}
	}
	got := edge.sites[websiteID].Security
	if got == nil || got.Firewall != caddy.FirewallBlock || got.SignInLimit != 10 || got.ProtectedPaths[0].Users[0].PasswordHash != pathHash {
		t.Fatalf("edge security = %+v", got)
	}
	// The edge proxy gets the masked ranges.
	if strings.Join(got.Allow, " ") != "203.0.113.0/24 2001:db8::1/128" || strings.Join(got.Block, " ") != "198.51.100.7/32" {
		t.Errorf("ranges = %v %v", got.Allow, got.Block)
	}
	site, _ := d.State.Get(websiteID)
	if site.Website.GetSecurity().GetFirewall() != "block" {
		t.Errorf("the state has no protection: %+v", site.Website.GetSecurity())
	}
	// A start of the agent gives the route with the protection again.
	edge.sites = map[string]caddy.Site{}
	if err := d.RestoreLocalNames(context.Background()); err != nil {
		t.Fatal(err)
	}
	if edge.sites[websiteID].Security == nil {
		t.Error("the route after a start has no protection")
	}
	// No protection: the route is the usual route.
	if err := run(t)(d.SetWebsiteSecurity(&agentv1.SetWebsiteSecurityRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2"})); err != nil {
		t.Fatal(err)
	}
	if edge.sites[websiteID].Security != nil {
		t.Errorf("security = %+v", edge.sites[websiteID].Security)
	}
}

func TestSetWebsiteSecurityChecksTheValues(t *testing.T) {
	d, _, _ := deps(t)
	bad := map[string]func(s *agentv1.WebsiteSecurity){
		"mode":         func(s *agentv1.WebsiteSecurity) { s.Firewall = "panic" },
		"rule":         func(s *agentv1.WebsiteSecurity) { s.FirewallExclusions = []int32{0} },
		"skip path":    func(s *agentv1.WebsiteSecurity) { s.FirewallSkipPaths = []string{"/a/../b"} },
		"address":      func(s *agentv1.WebsiteSecurity) { s.Allow = []string{"203.0.113.300"} },
		"zone":         func(s *agentv1.WebsiteSecurity) { s.Block = []string{"fe80::1%eth0"} },
		"path":         func(s *agentv1.WebsiteSecurity) { s.ProtectedPaths[0].Path = "staging" },
		"no user":      func(s *agentv1.WebsiteSecurity) { s.ProtectedPaths[0].Users = nil },
		"user":         func(s *agentv1.WebsiteSecurity) { s.ProtectedPaths[0].Users[0].Name = "a b" },
		"hash":         func(s *agentv1.WebsiteSecurity) { s.ProtectedPaths[0].Users[0].PasswordHash = "secret" },
		"same path":    func(s *agentv1.WebsiteSecurity) { s.ProtectedPaths = append(s.ProtectedPaths, s.ProtectedPaths[0]) },
		"limit":        func(s *agentv1.WebsiteSecurity) { s.SignInLimit = 2 },
		"sign-in path": func(s *agentv1.WebsiteSecurity) { s.SignInPaths = []string{"/wp-login.php*"} },
	}
	for name, change := range bad {
		s := protection()
		change(s)
		if _, err := d.SetWebsiteSecurity(&agentv1.SetWebsiteSecurityRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Security: s}); err == nil {
			t.Errorf("%s: the agent accepted %+v", name, s)
		}
		w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
		w.Security = s
		if CheckWebsite(w) == nil {
			t.Errorf("%s: CheckWebsite accepted %+v", name, s)
		}
	}
	if err := checkSecurity(protection()); err != nil {
		t.Errorf("a good value: %v", err)
	}
}

// A new website and a change of the runtime use the protection of the request.
func TestCreateWebsiteAppliesTheProtection(t *testing.T) {
	d, _, edge := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.Security = protection()
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
		t.Fatal(err)
	}
	if edge.sites[websiteID].Security.Firewall != caddy.FirewallBlock {
		t.Errorf("security = %+v", edge.sites[websiteID].Security)
	}
}

// The root helper reads the firewall log of the edge proxy only when it is a regular file of
// the edge user, with no symbolic link.
func TestFirewallEventsReadTheLogOfTheEdgeProxy(t *testing.T) {
	d, _, _ := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")})); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// No log yet: no events.
	if events, err := d.FirewallEvents(ctx, websiteID, 0); err != nil || len(events) != 0 {
		t.Fatalf("events = %v, %v", events, err)
	}
	logs := filepath.Join(rootOf(d).DataDir, "caddy", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"level":"error","ts":1795382819.128,"logger":"http.handlers.waf","msg":"[client \"203.0.113.9\"] Coraza: Warning. XSS Attack Detected via libinjection [file \"x\"] [line \"1\"] [id \"941100\"] [rev \"\"] [msg \"XSS Attack Detected via libinjection\"] [data \"Matched Data: XSS\"] [severity \"critical\"] [hostname \"\"] [uri \"/?q=x\"] [unique_id \"` + websiteID + `-a1\"]"}` + "\n"
	if err := os.WriteFile(filepath.Join(logs, "firewall.log"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	// The file is not a file of the edge user.
	if _, err := d.FirewallEvents(ctx, websiteID, 0); err == nil {
		t.Error("the helper read a file of a different user")
	}
	both(d, func(d *Deps) {
		d.Host.LookupUID = func(name string) (int, error) {
			if name == host.EdgeUser {
				return os.Getuid(), nil
			}
			return testUID(name)
		}
	})
	events, err := d.FirewallEvents(ctx, websiteID, 0)
	if err != nil || len(events) != 1 || events[0].GetClientIp() != "203.0.113.9" || events[0].GetRules()[0].GetId() != 941100 || events[0].GetAt() != "2026-11-22T21:26:59Z" {
		t.Fatalf("events = %v, %v", events, err)
	}
	// A symbolic link in place of the log.
	os.Remove(filepath.Join(logs, "firewall.log"))
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte(line), 0o600)
	os.Symlink(secret, filepath.Join(logs, "firewall.log"))
	if _, err := d.FirewallEvents(ctx, websiteID, 0); err == nil {
		t.Error("the helper followed a symbolic link")
	}
	// The worker gives the ID of a website that the agent does not know.
	if _, err := d.FirewallEvents(ctx, "01JBQ3Z5K8N6T4W2X9Y7R5P3ZZ", 0); err == nil {
		t.Error("no error for an unknown website")
	}
}
