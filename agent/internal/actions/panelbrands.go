package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// BrandCertificate is the state of the TLS certificate of one reseller panel domain.
type BrandCertificate struct {
	Domain string `json:"domain"`
	// Method is "letsencrypt" for a public name, else "internal" (the CA of the panel).
	Method string `json:"method"`
	// State is "valid", "pending" (no certificate yet) or "expired".
	State     string `json:"state"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	// Error tells why the certificate is not valid.
	Error string `json:"error,omitempty"`
}

// SetPanelBrands sets the panel domains of the resellers on the panel server (white label).
// The panel route then agrees with each of them, and the edge proxy gets a certificate for
// each of them. The job waits some time for the certificates. A domain with no certificate
// does not make the job fail: Caddy tries again later, and the result shows the state of
// each domain.
func (d *Deps) SetPanelBrands(req *agentv1.SetPanelBrandsRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetJobId()); err != nil {
		return jobs.Job{}, err
	}
	if len(req.GetDomains()) > caddy.MaxBrandDomains {
		return jobs.Job{}, fmt.Errorf("the panel can have at most %d reseller panel domains", caddy.MaxBrandDomains)
	}
	brands := make([]string, 0, len(req.GetDomains()))
	for _, b := range req.GetDomains() {
		b = strings.ToLower(b)
		if err := validate.Domain(b); err != nil {
			return jobs.Job{}, fmt.Errorf("reseller panel domain %q: %w", b, err)
		}
		brands = append(brands, b)
	}
	domain, err := d.panelDomain(context.Background())
	if err != nil {
		return jobs.Job{}, errors.New("this server has no panel")
	}
	brands = caddy.PanelHosts(domain, brands)[1:]
	var states []BrandCertificate
	return jobs.Job{ID: req.GetJobId(), Key: "panel-brands", Steps: []jobs.Step{
		{Name: "Set panel domains", Do: func(ctx context.Context) error {
			return d.Edge.SetPanelBrands(ctx, domain, brands)
		}},
		{Name: "Get TLS certificates", Do: func(ctx context.Context) error {
			states = d.waitBrandCertificates(ctx, brands)
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"panelBrands": states}
	}}, nil
}

// waitBrandCertificates waits until the edge proxy serves a valid certificate for each
// domain, or until the time limit. It gives the state of each domain.
func (d *Deps) waitBrandCertificates(ctx context.Context, brands []string) []BrandCertificate {
	deadline := time.Now().Add(orDefault(d.PanelCertificateWait, panelCertificateWait))
	states := make([]BrandCertificate, len(brands))
	for {
		open := false
		for i, b := range brands {
			if states[i].State == "valid" {
				continue
			}
			states[i] = d.brandCertificate(ctx, b)
			open = open || states[i].State != "valid"
		}
		if !open || !time.Now().Before(deadline) {
			return states
		}
		select {
		case <-ctx.Done():
			return states
		case <-time.After(orDefault(d.PanelCertificatePoll, panelCertificatePoll)):
		}
	}
}

// brandCertificate gives the state of the certificate that the edge proxy serves for a
// reseller panel domain.
func (d *Deps) brandCertificate(ctx context.Context, domain string) BrandCertificate {
	st := BrandCertificate{Domain: domain, Method: caddy.BrandMethod(domain), State: "pending"}
	chain, err := d.servedCertificate(ctx, domain)
	if err != nil || len(chain) == 0 {
		if st.Method == caddy.PanelLetsEncrypt {
			st.Error = fmt.Sprintf("Let's Encrypt has not given a certificate yet. Make sure that the DNS of %s points to the panel server, and that ports 80 and 443 are open to the internet.", domain)
		} else {
			st.Error = "The edge proxy has not made a certificate yet."
		}
		return st
	}
	root, _ := d.Edge.PanelCARoot(ctx)
	now := time.Now()
	st.ExpiresAt = chain[0].NotAfter.UTC().Format(time.RFC3339)
	switch err := caddy.CheckPanelCertificate(chain, domain, st.Method, root, d.PublicRoots, now); {
	case now.After(chain[0].NotAfter):
		st.State = "expired"
	case err == nil:
		st.State = "valid"
	default:
		st.ExpiresAt = ""
		st.Error = err.Error()
	}
	return st
}
