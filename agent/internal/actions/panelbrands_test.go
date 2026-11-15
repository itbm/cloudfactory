package actions

import (
	"context"
	"crypto/x509"
	"os"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
)

// The edge proxy gets the reseller panel domains. A domain with a certificate is valid. A
// domain with no certificate does not make the job fail: the result tells why.
func TestSetPanelBrands(t *testing.T) {
	d, edge := panelServer(t, "panel.example.com", true)
	acmeRoot, acmeKey := certificateFor(t, "Public CA", nil, nil)
	d.PublicRoots = x509.NewCertPool()
	d.PublicRoots.AddCert(acmeRoot)
	good, _ := certificateFor(t, "panel.agency.com", acmeRoot, acmeKey)
	d.ServedCertificate = func(_ context.Context, domain string) ([]*x509.Certificate, error) {
		if domain == "panel.agency.com" {
			return []*x509.Certificate{good}, nil
		}
		return nil, os.ErrDeadlineExceeded
	}
	job, err := d.SetPanelBrands(&agentv1.SetPanelBrandsRequest{JobId: jobID, Domains: []string{"Panel.Agency.com", "panel.other.com", "panel.example.com"}})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(edge.brands, []string{"panel.agency.com", "panel.other.com"}) {
		t.Errorf("brands = %v", edge.brands)
	}
	states, _ := job.Result()["panelBrands"].([]BrandCertificate)
	if len(states) != 2 || states[0].State != "valid" || states[0].Method != "letsencrypt" || states[0].ExpiresAt == "" {
		t.Fatalf("states = %+v", states)
	}
	if states[1].State != "pending" || !strings.Contains(states[1].Error, "DNS of panel.other.com points to the panel server") {
		t.Errorf("state of the domain with no certificate = %+v", states[1])
	}
}

func TestThePanelBrandsRequestIsChecked(t *testing.T) {
	d, _, _ := deps(t)
	if _, err := d.SetPanelBrands(&agentv1.SetPanelBrandsRequest{JobId: jobID}); err == nil || !strings.Contains(err.Error(), "no panel") {
		t.Errorf("a server with no panel: %v", err)
	}
	d, _ = panelServer(t, "panel.example.com", true)
	for _, bad := range []string{"panel agency.com", "*.agency.com", "localhost", "-x.com"} {
		if _, err := d.SetPanelBrands(&agentv1.SetPanelBrandsRequest{JobId: jobID, Domains: []string{bad}}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := d.SetPanelBrands(&agentv1.SetPanelBrandsRequest{JobId: "1"}); err == nil {
		t.Error("a bad job ID was accepted")
	}
}
