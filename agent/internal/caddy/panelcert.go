package caddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// The certificate methods of the panel domain.
const (
	// PanelInternal: the edge proxy signs the certificate of the panel with its own
	// certificate authority (PanelCA). This method works for all names, for example localhost,
	// panel.localhost or a name in a private network. A browser trusts the certificate only
	// after the administrator adds the CA certificate to it. A new panel uses this method.
	PanelInternal = "internal"
	// PanelLetsEncrypt: the edge proxy gets the certificate of the panel from Let's Encrypt with
	// ACME, as for the websites. The DNS of the domain must point to the server, and ports 80
	// and 443 must be open.
	PanelLetsEncrypt = "letsencrypt"
)

// PanelCA is the ID of the certificate authority of the panel in the PKI app of Caddy. Caddy
// keeps its root certificate and key in /data/caddy/pki/authorities/cloudfactory.
const PanelCA = "cloudfactory"

// PanelCAName is the name of the CA. Caddy puts it in the common name of the root certificate.
const PanelCAName = "Cloudfactory panel CA"

// panelLifetime is the validity of a certificate from PanelCA. A browser that has an exception
// for the certificate shows the warning again after each new certificate, so the certificate
// is valid for 90 days, not 12 hours (the default of Caddy). Caddy renews it after 60 days.
const panelLifetime = "2160h"

// panelPolicyID is the @id of the TLS automation policy of the panel domain.
const panelPolicyID = "cf-panel-tls"

// PanelMethod checks a certificate method.
func PanelMethod(method string) error {
	if method != PanelInternal && method != PanelLetsEncrypt {
		return fmt.Errorf("the certificate method must be %s or %s, not %q", PanelInternal, PanelLetsEncrypt, method)
	}
	return nil
}

// PublicName reports whether Let's Encrypt can give a certificate for a domain. Caddy uses the
// same list for its own CA (certmagic.SubjectIsInternal). The other names are the top-level
// domains that RFC 2606 and RFC 6761 keep for tests and examples. Let's Encrypt can also refuse
// a different name, for example with a top-level domain that does not exist.
func PublicName(domain string) bool {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	if !strings.Contains(d, ".") || net.ParseIP(d) != nil {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa", ".test", ".example", ".invalid"} {
		if strings.HasSuffix(d, suffix) {
			return false
		}
	}
	return true
}

// PanelCAConfig is the configuration of PanelCA. Caddy makes the root certificate once, and
// keeps it for 10 years. Caddy does not add it to the trust store of the container.
func PanelCAConfig() map[string]any {
	return map[string]any{"name": PanelCAName, "install_trust": false}
}

// PanelPolicy is the TLS automation policy of the panel domain.
//
// With PanelInternal, the root of PanelCA signs the certificate (sign_with_root). Thus the edge
// proxy sends the root certificate with the certificate of the panel, and an agent on a
// different server can check the root by its fingerprint (cf-agent join --panel-ca-sha256).
func PanelPolicy(domain, method string) map[string]any {
	issuer := map[string]any{"module": "acme"}
	if method == PanelInternal {
		issuer = map[string]any{"module": "internal", "ca": PanelCA, "lifetime": panelLifetime, "sign_with_root": true}
	}
	return map[string]any{"@id": panelPolicyID, "subjects": []string{domain}, "issuers": []any{issuer}}
}

// LocalNames gives the names of a website that Let's Encrypt cannot give a certificate for,
// for example test.localhost. The edge proxy signs their certificates with PanelCA.
func LocalNames(s Site) []string {
	names := []string{}
	for _, name := range append(append([]string{}, s.Hosts...), s.Redirects...) {
		if !PublicName(name) {
			names = append(names, name)
		}
	}
	return names
}

func localPolicyID(websiteID string) string { return "cf-" + websiteID + "-tls" }

// LocalPolicy is the TLS automation policy of the local names of one website.
//
// With no policy, Caddy signs a certificate for a local name with its default CA ("local"),
// which is a different CA from PanelCA, and the certificate is valid for 12 hours. Thus a
// browser that trusts the panel CA did not trust a website on a local name, and the panel
// showed that the certificate waits. With this policy, the certificate is from PanelCA, as
// for a panel on a local name.
func LocalPolicy(websiteID string, names []string) map[string]any {
	issuer := map[string]any{"module": "internal", "ca": PanelCA, "lifetime": panelLifetime, "sign_with_root": true}
	return map[string]any{"@id": localPolicyID(websiteID), "subjects": names, "issuers": []any{issuer}}
}

// applyLocalPolicy sets or removes the policy for the local names of a website.
func (c *Client) applyLocalPolicy(ctx context.Context, s Site) error {
	id := localPolicyID(s.WebsiteID)
	names := LocalNames(s)
	if len(names) == 0 {
		return c.remove(ctx, id)
	}
	if err := c.setPanelCA(ctx); err != nil {
		return err
	}
	policy := LocalPolicy(s.WebsiteID, names)
	status, body, err := c.do(ctx, http.MethodPatch, "/id/"+id, policy)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound && !unknownID(body) {
		return fmt.Errorf("caddy: patch %s: status %d: %s", id, status, body)
	}
	const path = "/config/apps/tls/automation/policies"
	status, body, err = c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var policies []any
	if status == http.StatusOK {
		_ = json.Unmarshal(body, &policies)
	}
	if len(policies) == 0 {
		// PUT makes the objects in the path that do not exist (apps/tls/automation).
		return c.putOrPatch(ctx, path, []any{policy})
	}
	// A POST on a list adds the value at the end. Each policy has its own subjects, so the
	// sequence of the website policies is not important.
	return c.expect(ctx, http.MethodPost, path, policy, http.StatusOK)
}

// SetPanelCertificate sets the certificate method of the panel domain. It keeps the routes and
// the policies of the websites.
func (c *Client) SetPanelCertificate(ctx context.Context, domain, method string) error {
	if err := PanelMethod(method); err != nil {
		return err
	}
	// The CA stays in the configuration with Let's Encrypt too, so that a change back to the
	// internal method uses the same root certificate.
	if err := c.setPanelCA(ctx); err != nil {
		return err
	}
	policy := PanelPolicy(domain, method)
	status, body, err := c.do(ctx, http.MethodPatch, "/id/"+panelPolicyID, policy)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound && !unknownID(body) {
		return fmt.Errorf("caddy: patch %s: status %d: %s", panelPolicyID, status, body)
	}
	// The first policy that agrees with a name applies to it. Thus the policy of the panel is
	// the first policy, before a policy for all names.
	const path = "/config/apps/tls/automation/policies"
	status, body, err = c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var policies []any
	if status == http.StatusOK {
		_ = json.Unmarshal(body, &policies)
	}
	if len(policies) == 0 {
		// PUT makes the objects in the path that do not exist (apps/tls/automation).
		return c.putOrPatch(ctx, path, []any{policy})
	}
	// A PUT on an index of a list puts the value at that index.
	return c.expect(ctx, http.MethodPut, path+"/0", policy, http.StatusOK)
}

// setPanelCA adds PanelCA to the PKI app, or changes its configuration.
func (c *Client) setPanelCA(ctx context.Context) error {
	return c.putOrPatch(ctx, "/config/apps/pki/certificate_authorities/"+PanelCA, PanelCAConfig())
}

// putOrPatch sets a value. PUT makes a value that does not exist, and PATCH replaces a value
// that exists.
func (c *Client) putOrPatch(ctx context.Context, path string, value any) error {
	status, body, err := c.do(ctx, http.MethodPut, path, value)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return c.expect(ctx, http.MethodPatch, path, value, http.StatusOK)
	}
	return fmt.Errorf("caddy: put %s: status %d: %s", path, status, body)
}

// PanelCertificate gives the certificate method of the panel domain, or "" when the edge proxy
// has no panel route.
//
// A panel from before the certificate methods has a route and no policy. Caddy then uses its
// default: Let's Encrypt for a public name, and its own local CA for an internal name such as
// panel.localhost. The function gives the method that agrees with that default.
func (c *Client) PanelCertificate(ctx context.Context, domain string) (string, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/id/"+panelPolicyID, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK {
		var policy struct {
			Issuers []struct {
				Module string `json:"module"`
			} `json:"issuers"`
		}
		if err := json.Unmarshal(body, &policy); err != nil {
			return "", fmt.Errorf("caddy: read %s: %w", panelPolicyID, err)
		}
		if len(policy.Issuers) > 0 && policy.Issuers[0].Module == "internal" {
			return PanelInternal, nil
		}
		return PanelLetsEncrypt, nil
	}
	if status != http.StatusNotFound && !unknownID(body) {
		return "", fmt.Errorf("caddy: get %s: status %d: %s", panelPolicyID, status, body)
	}
	status, body, err = c.do(ctx, http.MethodGet, "/id/cf-panel", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		if status == http.StatusNotFound || unknownID(body) {
			return "", nil
		}
		return "", fmt.Errorf("caddy: get cf-panel: status %d: %s", status, body)
	}
	if PublicName(domain) {
		return PanelLetsEncrypt, nil
	}
	return PanelInternal, nil
}

// PanelCARoot gives the root certificate of PanelCA in PEM format, or "" when Caddy does not
// have the CA.
func (c *Client) PanelCARoot(ctx context.Context) (string, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/pki/ca/"+PanelCA, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		// Caddy answers 404 for a CA that it does not have.
		if status == http.StatusNotFound {
			return "", nil
		}
		return "", fmt.Errorf("caddy: get the CA %s: status %d: %s", PanelCA, status, body)
	}
	var ca struct {
		Root string `json:"root_certificate"`
	}
	if err := json.Unmarshal(body, &ca); err != nil {
		return "", fmt.Errorf("caddy: read the CA %s: %w", PanelCA, err)
	}
	return ca.Root, nil
}

// ServedCertificate gives the certificate chain that the edge proxy at address sends for a
// domain, as a browser gets it. It does not check the chain.
func ServedCertificate(ctx context.Context, address, domain string) ([]*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect to the edge proxy: %w", err)
	}
	defer raw.Close()
	// The caller checks the chain with CheckPanelCertificate.
	conn := tls.Client(raw, &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
	if err := conn.HandshakeContext(ctx); err != nil {
		// Caddy ends the handshake when it has no certificate for the name.
		return nil, fmt.Errorf("the edge proxy has no certificate for %s yet (%v)", domain, err)
	}
	return conn.ConnectionState().PeerCertificates, nil
}

// CheckPanelCertificate checks the certificate chain that the edge proxy sends for the panel
// domain. With PanelInternal, the root certificate rootPEM of PanelCA must sign the
// certificate. With PanelLetsEncrypt, a public CA in roots must sign it, and PanelCA must not.
// A nil roots means the system roots.
func CheckPanelCertificate(chain []*x509.Certificate, domain, method, rootPEM string, roots *x509.CertPool, now time.Time) error {
	if len(chain) == 0 {
		return fmt.Errorf("the edge proxy sent no certificate for %s", domain)
	}
	leaf := chain[0]
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		intermediates.AddCert(c)
	}
	var root *x509.Certificate
	if rootPEM != "" {
		block, _ := pem.Decode([]byte(rootPEM))
		if block == nil {
			return errors.New("the root certificate of the panel CA is not in PEM format")
		}
		var err error
		if root, err = x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("read the root certificate of the panel CA: %w", err)
		}
	}
	opts := x509.VerifyOptions{DNSName: domain, Intermediates: intermediates, Roots: roots, CurrentTime: now}
	switch method {
	case PanelInternal:
		if root == nil {
			return errors.New("the edge proxy has no panel CA")
		}
		opts.Roots = x509.NewCertPool()
		opts.Roots.AddCert(root)
		if _, err := leaf.Verify(opts); err != nil {
			return fmt.Errorf("the certificate for %s is not from the panel CA: %w", domain, err)
		}
	case PanelLetsEncrypt:
		// An administrator can add the panel CA to the trust store of the server. Thus the
		// check of the system roots alone does not show that the certificate is public.
		if root != nil && leaf.CheckSignatureFrom(root) == nil {
			return fmt.Errorf("the edge proxy has no certificate from Let's Encrypt for %s yet", domain)
		}
		if _, err := leaf.Verify(opts); err != nil {
			return fmt.Errorf("the certificate for %s is not from a public certificate authority: %w", domain, err)
		}
	default:
		return PanelMethod(method)
	}
	return nil
}
