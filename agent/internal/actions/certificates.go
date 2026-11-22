package actions

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The certificates from the panel (docs/certificates.md): a certificate that a customer
// uploaded, or a certificate that the panel got from an ACME CA with DNS validation, for
// example a wildcard certificate. The panel sends all certificates for the server with
// SetCertificates. The edge proxy then serves each certificate for its hosts, and does not
// get a certificate for these hosts itself (caddy.SetCertificates). The worker does this work
// through the admin socket of the edge proxy. The root helper has no operation for it.

// checkedCertificate is a certificate from the panel that the agent checked.
type checkedCertificate struct {
	caddy.Certificate
	sha256 string
}

// SetCertificates sets the certificates from the panel on the edge proxy of the server.
func (d *Deps) SetCertificates(req *agentv1.SetCertificatesRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetJobId()); err != nil {
		return jobs.Job{}, err
	}
	certs, err := CheckCertificates(req.GetCertificates())
	if err != nil {
		return jobs.Job{}, err
	}
	var loaded []caddy.LoadedCertificate
	return jobs.Job{ID: req.GetJobId(), Key: "certificates", Steps: []jobs.Step{
		{Name: "Load certificates", Do: func(ctx context.Context) error {
			list := make([]caddy.Certificate, len(certs))
			for i, c := range certs {
				list[i] = c.Certificate
			}
			return d.Edge.SetCertificates(ctx, list)
		}},
		{Name: "Check certificates", Do: func(ctx context.Context) error {
			if err := d.checkServedCertificates(ctx, certs); err != nil {
				return err
			}
			loaded, _ = d.Edge.Certificates(ctx)
			return nil
		}},
	}, Result: func() map[string]any {
		if loaded == nil {
			loaded = []caddy.LoadedCertificate{}
		}
		return map[string]any{"certificates": loaded}
	}}, nil
}

// CheckCertificates checks the certificates of a SetCertificates request: the IDs, the hosts,
// the chain and the key. Each host must be in one certificate only, and the certificate must
// be valid for the host.
func CheckCertificates(in []*agentv1.EdgeCertificate) ([]checkedCertificate, error) {
	if len(in) > validate.MaxCertificatesPerServer {
		return nil, fmt.Errorf("a server can have at most %d certificates from the panel", validate.MaxCertificatesPerServer)
	}
	ids := map[string]bool{}
	hosts := map[string]string{}
	out := make([]checkedCertificate, 0, len(in))
	for _, c := range in {
		id := c.GetId()
		if err := validate.ID(id); err != nil {
			return nil, fmt.Errorf("certificate ID: %w", err)
		}
		if ids[id] {
			return nil, fmt.Errorf("the certificate %s is in the list two times", id)
		}
		ids[id] = true
		if len(c.GetHosts()) == 0 {
			return nil, fmt.Errorf("the certificate %s has no hosts", id)
		}
		if len(c.GetHosts()) > validate.MaxCertificateHosts {
			return nil, fmt.Errorf("the certificate %s can have at most %d hosts", id, validate.MaxCertificateHosts)
		}
		if len(c.GetCertificatePem()) > validate.MaxCertificateBytes || len(c.GetPrivateKeyPem()) > validate.MaxCertificateBytes {
			return nil, fmt.Errorf("the certificate %s is too large", id)
		}
		pair, err := tls.X509KeyPair([]byte(c.GetCertificatePem()), []byte(c.GetPrivateKeyPem()))
		if err != nil {
			return nil, fmt.Errorf("the certificate %s: %w", id, err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("the certificate %s: %w", id, err)
		}
		var list []string
		for _, h := range c.GetHosts() {
			h = strings.ToLower(h)
			if err := validate.Domain(h); err != nil {
				return nil, fmt.Errorf("the certificate %s: host %q: %w", id, h, err)
			}
			if other, ok := hosts[h]; ok {
				return nil, fmt.Errorf("the host %s is in the certificates %s and %s", h, other, id)
			}
			if err := leaf.VerifyHostname(h); err != nil {
				return nil, fmt.Errorf("the certificate %s is not valid for %s", id, h)
			}
			hosts[h] = id
			list = append(list, h)
		}
		slices.Sort(list)
		out = append(out, checkedCertificate{
			Certificate: caddy.Certificate{ID: id, Hosts: list, CertificatePEM: c.GetCertificatePem(), KeyPEM: c.GetPrivateKeyPem()},
			sha256:      caddy.LeafFingerprint(leaf),
		})
	}
	return out, nil
}

// checkServedCertificates connects to the edge proxy with the name of each host, as a browser
// does, and checks that the edge proxy sends the certificate from the panel.
func (d *Deps) checkServedCertificates(ctx context.Context, certs []checkedCertificate) error {
	var errs []error
	for _, c := range certs {
		for _, h := range c.Hosts {
			chain, err := d.servedCertificate(ctx, h)
			switch {
			case err != nil:
				errs = append(errs, err)
			case len(chain) == 0 || caddy.LeafFingerprint(chain[0]) != c.sha256:
				errs = append(errs, fmt.Errorf("the edge proxy does not send the certificate %s for %s", c.ID, h))
			}
		}
	}
	return errors.Join(errs...)
}

// certificateStates gives the certificates from the panel that the edge proxy has, for the
// status report. It is nil when the agent cannot read them: the panel then does not compare.
func (d *Deps) certificateStates(ctx context.Context) []panel.CertificateState {
	if d.Edge == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	loaded, err := d.Edge.Certificates(ctx)
	if err != nil {
		return nil
	}
	out := make([]panel.CertificateState, 0, len(loaded))
	for _, l := range loaded {
		out = append(out, panel.CertificateState{ID: l.ID, SHA256: l.SHA256, Hosts: l.Hosts})
	}
	return out
}

// panelCertificateHosts gives the fingerprint of the certificate from the panel of each host
// that uses one. The check of the domains uses it.
func (d *Deps) panelCertificateHosts(ctx context.Context) map[string]string {
	if d.Edge == nil {
		return nil
	}
	loaded, err := d.Edge.Certificates(ctx)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, l := range loaded {
		for _, h := range l.Hosts {
			out[h] = l.SHA256
		}
	}
	return out
}

// panelCertificate gives the expiry time of the certificate from the panel that the edge
// proxy serves for a host. The agent does not check the chain with the system roots: the
// customer can use a certificate from a private CA. It checks that the edge proxy sends the
// certificate from the panel.
func (d *Deps) panelCertificate(ctx context.Context, host, sha256 string) (time.Time, error) {
	chain, err := d.servedCertificate(ctx, host)
	if err != nil || len(chain) == 0 {
		return time.Time{}, errNoCertificate
	}
	if caddy.LeafFingerprint(chain[0]) != sha256 {
		return time.Time{}, errors.New("the edge proxy sends a different certificate")
	}
	return chain[0].NotAfter, nil
}
