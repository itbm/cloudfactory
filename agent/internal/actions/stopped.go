package actions

import (
	"context"
	"log/slog"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
)

// edgeSite gives the edge routes of a site that the state knows, with the domains of w. A
// stopped website gets a page with status 503 in place of the route to its container, which
// does not operate (audit item U10). Before, a visitor of a stopped website, for example of a
// suspended customer, got an empty answer with status 502.
func (d *Deps) edgeSite(s *Site, w *agentv1.Website) caddy.Site {
	out := d.site(w, s.Port)
	out.Stopped = s.Stopped
	return out
}

// applyStoppedPage changes the edge route of a website after a start or a stop. A website
// that starts must get the route to its container, so an error stops the job. For a website
// that stops, the page is only a help for its visitors: an error is in the log, and the
// website stays stopped.
func (d *Deps) applyStoppedPage(ctx context.Context, s *Site) error {
	err := d.Edge.Apply(ctx, d.edgeSite(s, s.Website))
	if err != nil && s.Stopped {
		slog.Warn("no page for the stopped website", "website", s.ID, "err", err)
		return nil
	}
	return err
}
