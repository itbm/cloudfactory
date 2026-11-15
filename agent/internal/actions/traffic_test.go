package actions

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// trafficEdge is a fake edge proxy that gives the counters of each host.
type trafficEdge struct {
	*fakeEdge
	counters map[string]caddy.Traffic
	err      error
}

func (e *trafficEdge) Traffic(context.Context) (map[string]caddy.Traffic, error) {
	return e.counters, e.err
}

// The bandwidth of a website: the report has the traffic of all its domains since the last
// report.
func TestTheReportHasTheTrafficOfEachWebsite(t *testing.T) {
	d, _, edge := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.Domains = append(w.Domains, &agentv1.Domain{Name: "www.acme.example", Kind: "redirect"})
	run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w}))
	te := &trafficEdge{fakeEdge: edge, counters: map[string]caddy.Traffic{
		"acme.example": {In: 1000, Out: 50000}, "www.acme.example": {In: 10, Out: 20}, "other.example": {In: 5, Out: 5},
	}}
	d.Edge = te
	report := func() panel.Usage {
		t.Helper()
		var r panel.Report
		d.FillReport(context.Background(), &r)
		if len(r.Websites) != 1 {
			t.Fatalf("websites = %+v", r.Websites)
		}
		return r.Websites[0]
	}
	// The first report after a start of the agent only reads the counters.
	if u := report(); u.BytesIn != 0 || u.BytesOut != 0 {
		t.Errorf("first report = %+v, want no traffic", u)
	}
	te.counters = map[string]caddy.Traffic{"acme.example": {In: 1500, Out: 80000}, "www.acme.example": {In: 20, Out: 40}}
	if u := report(); u.BytesIn != 510 || u.BytesOut != 30020 {
		t.Errorf("second report = %+v, want 510 in and 30020 out", u)
	}
	// The edge proxy cannot answer: no traffic, and the next report gives the traffic of both
	// intervals.
	te.err = errors.New("no socket")
	if u := report(); u.BytesIn != 0 || u.BytesOut != 0 {
		t.Errorf("report with no metrics = %+v", u)
	}
	te.err = nil
	te.counters = map[string]caddy.Traffic{"acme.example": {In: 2000, Out: 90000}, "www.acme.example": {In: 20, Out: 40}}
	if u := report(); u.BytesIn != 500 || u.BytesOut != 10000 {
		t.Errorf("report after the error = %+v", u)
	}
	// The edge proxy started again, and its counters started at 0.
	te.counters = map[string]caddy.Traffic{"acme.example": {In: 100, Out: 700}}
	if u := report(); u.BytesIn != 100 || u.BytesOut != 700 {
		t.Errorf("report after a restart of the edge proxy = %+v", u)
	}
}
