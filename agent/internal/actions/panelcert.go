package actions

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The time limits of a change of the certificate method of the panel.
const (
	// panelCertificateWait is the time that the agent waits for the new certificate. Let's
	// Encrypt usually gives a certificate in less than 30 seconds.
	panelCertificateWait = 2 * time.Minute
	// panelCertificatePoll is the time between two checks of the certificate.
	panelCertificatePoll = 2 * time.Second
)

// PanelEnvFile gives the settings file of the panel. Only the panel server has it.
func PanelEnvFile(dataDir string) string { return filepath.Join(dataDir, "panel", "panel.env") }

// SetPanelCertificate changes the certificate method of the panel domain: the CA of the panel
// or Let's Encrypt. The agent reads the domain from the settings of the panel, so the request
// cannot get a certificate for a different domain.
//
// Caddy stops serving the old certificate when the method changes. If the new certificate does
// not come in time, the agent goes back to the previous method, so that the panel stays
// available.
func (d *Deps) SetPanelCertificate(req *agentv1.SetPanelCertificateRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetJobId()); err != nil {
		return jobs.Job{}, err
	}
	method := req.GetMethod()
	if err := caddy.PanelMethod(method); err != nil {
		return jobs.Job{}, err
	}
	domain, err := d.panelDomain(context.Background())
	if err != nil {
		return jobs.Job{}, errors.New("this server has no panel")
	}
	if method == caddy.PanelLetsEncrypt && !caddy.PublicName(domain) {
		return jobs.Job{}, fmt.Errorf("Let's Encrypt gives certificates only for public domains, and %s is not a public domain", domain)
	}
	var previous string
	var state *panel.PanelCertificate
	return jobs.Job{ID: req.GetJobId(), Key: "panel-certificate", Steps: []jobs.Step{
		{Name: "Change certificate method", Do: func(ctx context.Context) error {
			p, err := d.Edge.PanelCertificate(ctx, domain)
			if err != nil {
				return err
			}
			previous = p
			return d.Edge.SetPanelCertificate(ctx, domain, method)
		}},
		{Name: "Get TLS certificate", Do: func(ctx context.Context) error {
			err := d.waitPanelCertificate(ctx, domain, method)
			if err == nil {
				state = d.PanelCertificateState(ctx, domain)
				return nil
			}
			if previous == "" || previous == method {
				return err
			}
			if back := d.Edge.SetPanelCertificate(context.WithoutCancel(ctx), domain, previous); back != nil {
				return fmt.Errorf("%w. The agent could not go back to the previous method: %v", err, back)
			}
			return fmt.Errorf("%w. The panel uses its previous certificate again (%s)", err, PanelMethodName(previous))
		}},
	}, Result: func() map[string]any {
		return map[string]any{"panelCertificate": state}
	}}, nil
}

// PanelMethodName gives the name of a certificate method for a message.
func PanelMethodName(method string) string {
	if method == caddy.PanelLetsEncrypt {
		return "Let's Encrypt"
	}
	return "the panel CA"
}

// waitPanelCertificate waits until the edge proxy serves a certificate of the method for the
// domain.
func (d *Deps) waitPanelCertificate(ctx context.Context, domain, method string) error {
	wait := orDefault(d.PanelCertificateWait, panelCertificateWait)
	deadline := time.Now().Add(wait)
	for {
		err := d.checkPanelCertificate(ctx, domain, method)
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			if method == caddy.PanelLetsEncrypt {
				return fmt.Errorf("Let's Encrypt did not give a certificate for %s in %s (%v). Make sure that the DNS of %s points to this server, and that ports 80 and 443 of this server are open to the internet", domain, wait, err, domain)
			}
			return fmt.Errorf("the edge proxy did not serve a certificate of the panel CA for %s in %s: %w", domain, wait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(orDefault(d.PanelCertificatePoll, panelCertificatePoll)):
		}
	}
}

// checkPanelCertificate checks the certificate that the edge proxy serves for the panel domain.
func (d *Deps) checkPanelCertificate(ctx context.Context, domain, method string) error {
	chain, err := d.servedCertificate(ctx, domain)
	if err != nil {
		return err
	}
	root, err := d.Edge.PanelCARoot(ctx)
	if err != nil {
		return err
	}
	return caddy.CheckPanelCertificate(chain, domain, method, root, d.PublicRoots, time.Now())
}

func (d *Deps) servedCertificate(ctx context.Context, domain string) ([]*x509.Certificate, error) {
	if d.ServedCertificate != nil {
		return d.ServedCertificate(ctx, domain)
	}
	return caddy.ServedCertificateVia(ctx, d.DialLocal, edgeHTTPS, domain)
}

// PanelCertificateState gives the certificate method of the panel and the state of the
// certificate that the edge proxy serves. It is nil when the edge proxy has no panel route.
func (d *Deps) PanelCertificateState(ctx context.Context, domain string) *panel.PanelCertificate {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	method, err := d.Edge.PanelCertificate(ctx, domain)
	if err != nil || method == "" {
		return nil
	}
	st := &panel.PanelCertificate{Domain: domain, Method: method, State: "pending"}
	st.CACertificate, _ = d.Edge.PanelCARoot(ctx)
	chain, err := d.servedCertificate(ctx, domain)
	if err != nil || len(chain) == 0 {
		return st
	}
	now := time.Now()
	st.ExpiresAt = chain[0].NotAfter.UTC().Format(time.RFC3339)
	switch {
	case now.After(chain[0].NotAfter):
		st.State = "expired"
	case caddy.CheckPanelCertificate(chain, domain, method, st.CACertificate, d.PublicRoots, now) == nil:
		st.State = "valid"
	default:
		// The edge proxy still serves a certificate of the other method.
		st.ExpiresAt = ""
	}
	return st
}
