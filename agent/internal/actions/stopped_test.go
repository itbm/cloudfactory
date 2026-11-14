package actions

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
)

// failingEdge is an edge proxy that does not answer.
type failingEdge struct{ *fakeEdge }

func (failingEdge) Apply(context.Context, caddy.Site) error { return errors.New("no edge proxy") }

// Audit item U10: a stopped website, for example of a suspended customer, shows a page with
// status 503. A website that starts again gets the route to its container.
func TestAStoppedWebsiteShowsAPage(t *testing.T) {
	d, _, edge := deps(t)
	ctx := context.Background()
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")}))
	if edge.sites[websiteID].Stopped {
		t.Fatal("a new website shows the page of a stopped website")
	}
	power := func(action string) error {
		t.Helper()
		return run(t)(d.SetWebsitePower(&agentv1.SetWebsitePowerRequest{JobId: jobID2, WebsiteId: websiteID, Power: action}))
	}
	if err := power("stop"); err != nil {
		t.Fatal(err)
	}
	if s := edge.sites[websiteID]; !s.Stopped || len(s.Hosts) != 1 {
		t.Errorf("the stopped website has the route %+v", s)
	}

	// A change of the domains keeps the page.
	if err := run(t)(d.AddDomain(&agentv1.DomainRequest{JobId: jobID, WebsiteId: websiteID, Domain: "www.acme.example", Kind: "alias"})); err != nil {
		t.Fatal(err)
	}
	if s := edge.sites[websiteID]; !s.Stopped || len(s.Hosts) != 2 {
		t.Errorf("after a new domain, the stopped website has the route %+v", s)
	}

	// The agent sets the page again when it starts, for a website that an earlier version stopped.
	delete(edge.sites, websiteID)
	if err := d.RestoreLocalNames(ctx); err != nil {
		t.Fatal(err)
	}
	if !edge.sites[websiteID].Stopped {
		t.Error("the agent did not set the page of the stopped website when it started")
	}

	if err := power("start"); err != nil {
		t.Fatal(err)
	}
	if edge.sites[websiteID].Stopped {
		t.Error("the website operates, but the edge proxy shows the page of a stopped website")
	}

	// With no edge proxy, a stop completes: the website is stopped. A start fails, because the
	// visitors would continue to get the page.
	d.Edge = failingEdge{edge}
	if err := power("stop"); err != nil {
		t.Errorf("a stop with no edge proxy failed: %v", err)
	}
	if site, _ := d.State.Get(websiteID); site == nil || !site.Stopped {
		t.Error("the state does not record the stop")
	}
	if err := power("start"); err == nil {
		t.Error("a start with no edge proxy reported success")
	}
}
