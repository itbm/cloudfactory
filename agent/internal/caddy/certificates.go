package caddy

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// The certificates from the panel: a certificate that a customer uploaded, or a certificate
// that the panel got from an ACME CA with DNS validation, for example a wildcard certificate.
// The edge proxy does not get these certificates itself. The agent loads them, and each host
// that uses one gets it through a TLS connection policy:
//
//   - The TLS app has each certificate in "load_pem", with the tag "cf-cert-<ID>".
//   - The edge server has one connection policy for each certificate. The policy agrees with
//     the server names (SNI) of its hosts, and selects the certificate by its tag.
//   - The automatic HTTPS of the edge server skips the hosts of these certificates
//     ("skip_certificates"), so that Caddy does not get a certificate for them from Let's
//     Encrypt.
//   - Caddy gets a certificate for all other hosts, also when a loaded certificate covers
//     them ("ignore_loaded_certificates"). Without this, a wildcard certificate of one
//     customer would stop the certificates of the other hosts in the same domain.
const (
	certificatesPath       = "/config/apps/tls/certificates/load_pem"
	connectionPoliciesPath = "/config/apps/http/servers/" + serverName + "/tls_connection_policies"
	automaticHTTPSPath     = "/config/apps/http/servers/" + serverName + "/automatic_https"
	certificatePrefix      = "cf-cert-"
)

// Certificate is one certificate from the panel and the hosts on this server that use it.
type Certificate struct {
	ID    string
	Hosts []string
	// CertificatePEM is the chain: the certificate first, then the intermediate certificates.
	CertificatePEM string
	KeyPEM         string
}

// LoadedCertificate is a certificate from the panel that the edge proxy has.
type LoadedCertificate struct {
	ID string `json:"id"`
	// SHA256 is the fingerprint of the certificate (not of the chain), in lower-case hex.
	SHA256 string   `json:"sha256"`
	Hosts  []string `json:"hosts"`
}

// CertificateTag is the tag of a certificate from the panel in the TLS app.
func CertificateTag(id string) string { return certificatePrefix + id }

// Fingerprint gives the SHA-256 fingerprint of the first certificate in a PEM chain, or "".
func Fingerprint(chainPEM string) string {
	block, _ := pem.Decode([]byte(chainPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

// LeafFingerprint gives the SHA-256 fingerprint of a parsed certificate.
func LeafFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// certificateEntry is one entry of "load_pem".
type certificateEntry struct {
	Certificate string   `json:"certificate"`
	Key         string   `json:"key"`
	Tags        []string `json:"tags,omitempty"`
}

func (e certificateEntry) id() string {
	for _, t := range e.Tags {
		if id, ok := strings.CutPrefix(t, certificatePrefix); ok {
			return id
		}
	}
	return ""
}

// CertificatePolicy is the TLS connection policy of a certificate from the panel.
func CertificatePolicy(c Certificate) map[string]any {
	hosts := slices.Clone(c.Hosts)
	slices.Sort(hosts)
	return map[string]any{
		"@id":                   CertificateTag(c.ID),
		"match":                 map[string]any{"sni": hosts},
		"certificate_selection": map[string]any{"any_tag": []string{CertificateTag(c.ID)}},
	}
}

// isCertificatePolicy reports whether a connection policy is the policy of a certificate from
// the panel.
func isCertificatePolicy(p map[string]any) bool {
	id, _ := p["@id"].(string)
	return strings.HasPrefix(id, certificatePrefix)
}

// PanelConnectionPolicies gives the TLS connection policies of the edge server with a new
// panel policy. The policies of the certificates from the panel stay.
func PanelConnectionPolicies(current []map[string]any, panel map[string]any) []map[string]any {
	out := []map[string]any{panel}
	for _, p := range current {
		if isCertificatePolicy(p) {
			out = append(out, p)
		}
	}
	return append(out, map[string]any{})
}

// MergeConnectionPolicies gives the TLS connection policies of the edge server: first the
// other policies (the panel policy), then the policies of the certificates from the panel,
// then one policy with no match. Caddy uses the first policy that agrees with a connection,
// and it refuses a connection that agrees with no policy.
func MergeConnectionPolicies(current []map[string]any, certificates []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, p := range current {
		if len(p) == 0 || isCertificatePolicy(p) {
			continue
		}
		out = append(out, p)
	}
	out = append(out, certificates...)
	return append(out, map[string]any{})
}

// connectionPolicies reads the TLS connection policies of the edge server. A list that is
// not there is an empty list.
func (c *Client) connectionPolicies(ctx context.Context) ([]map[string]any, error) {
	status, body, err := c.do(ctx, http.MethodGet, connectionPoliciesPath, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusOK:
		var policies []map[string]any
		if err := json.Unmarshal(body, &policies); err != nil {
			return nil, fmt.Errorf("caddy: read the TLS connection policies: %w", err)
		}
		return policies, nil
	case status == http.StatusNotFound, status == http.StatusBadRequest && strings.Contains(string(body), "invalid traversal path"):
		return nil, nil
	}
	return nil, fmt.Errorf("caddy: read the TLS connection policies: status %d: %s", status, body)
}

// setConnectionPolicies replaces the TLS connection policies of the edge server.
func (c *Client) setConnectionPolicies(ctx context.Context, policies []map[string]any) error {
	return c.putOrPatch(ctx, connectionPoliciesPath, policies)
}

// loadedEntries reads the certificates of "load_pem".
func (c *Client) loadedEntries(ctx context.Context) ([]certificateEntry, error) {
	status, body, err := c.do(ctx, http.MethodGet, certificatesPath, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusOK:
		var entries []certificateEntry
		if string(body) == "null\n" || string(body) == "null" {
			return nil, nil
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, fmt.Errorf("caddy: read the certificates: %w", err)
		}
		return entries, nil
	case status == http.StatusNotFound, status == http.StatusBadRequest && strings.Contains(string(body), "invalid traversal path"):
		return nil, nil
	}
	return nil, fmt.Errorf("caddy: read the certificates: status %d: %s", status, body)
}

// setEntries replaces the certificates of "load_pem". The entries that are not from the panel
// stay.
func (c *Client) setEntries(ctx context.Context, entries []certificateEntry) error {
	if len(entries) == 0 {
		return c.deletePath(ctx, certificatesPath)
	}
	return c.putOrPatch(ctx, certificatesPath, entries)
}

// deletePath removes a value. A value that is not there is not an error.
func (c *Client) deletePath(ctx context.Context, path string) error {
	status, body, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNotFound || strings.Contains(string(body), "invalid traversal path") {
		return nil
	}
	return fmt.Errorf("caddy: delete %s: status %d: %s", path, status, body)
}

// SetCertificates sets the certificates from the panel. The list is complete: a certificate
// from the panel that is not in the list goes, and its hosts get a certificate from Caddy again.
//
// The steps keep a certificate for each host at each time when possible:
//  1. Load the new certificates, and keep the old ones.
//  2. Set the connection policies: each host selects its new certificate.
//  3. Set the hosts that Caddy does not get a certificate for.
//  4. Remove the old certificates.
func (c *Client) SetCertificates(ctx context.Context, certs []Certificate) error {
	current, err := c.loadedEntries(ctx)
	if err != nil {
		return err
	}
	var others, union, wanted []certificateEntry
	ids := map[string]bool{}
	for _, cert := range certs {
		ids[cert.ID] = true
		wanted = append(wanted, certificateEntry{Certificate: cert.CertificatePEM, Key: cert.KeyPEM, Tags: []string{CertificateTag(cert.ID)}})
	}
	for _, e := range current {
		switch id := e.id(); {
		case id == "":
			// Not from the panel, for example from an administrator. It stays.
			others = append(others, e)
		case !ids[id]:
			union = append(union, e)
		}
	}
	union = append(append(slices.Clone(others), union...), wanted...)
	wanted = append(others, wanted...)
	onEdge := current
	if !sameEntries(current, union) {
		if err := c.setEntries(ctx, union); err != nil {
			return err
		}
		onEdge = union
	}

	policies, err := c.connectionPolicies(ctx)
	if err != nil {
		return err
	}
	var certPolicies []map[string]any
	var skip []string
	for _, cert := range certs {
		certPolicies = append(certPolicies, CertificatePolicy(cert))
		skip = append(skip, cert.Hosts...)
	}
	slices.Sort(skip)
	skip = slices.Compact(skip)
	if err := c.setConnectionPolicies(ctx, MergeConnectionPolicies(policies, certPolicies)); err != nil {
		return err
	}
	if err := c.putOrPatch(ctx, automaticHTTPSPath, AutomaticHTTPS(skip)); err != nil {
		return err
	}
	if !sameEntries(onEdge, wanted) {
		return c.setEntries(ctx, wanted)
	}
	return nil
}

// AutomaticHTTPS is the automatic HTTPS configuration of the edge server. skip are the hosts
// that use a certificate from the panel.
func AutomaticHTTPS(skip []string) map[string]any {
	a := map[string]any{"ignore_loaded_certificates": true}
	if len(skip) > 0 {
		a["skip_certificates"] = skip
	}
	return a
}

func sameEntries(a, b []certificateEntry) bool {
	return slices.EqualFunc(a, b, func(x, y certificateEntry) bool {
		return x.Certificate == y.Certificate && x.Key == y.Key && slices.Equal(x.Tags, y.Tags)
	})
}

// Certificates gives the certificates from the panel that the edge proxy has, with the hosts
// that use each one, in the order of their IDs.
func (c *Client) Certificates(ctx context.Context) ([]LoadedCertificate, error) {
	entries, err := c.loadedEntries(ctx)
	if err != nil {
		return nil, err
	}
	policies, err := c.connectionPolicies(ctx)
	if err != nil {
		return nil, err
	}
	hosts := map[string][]string{}
	for _, p := range policies {
		if !isCertificatePolicy(p) {
			continue
		}
		id := strings.TrimPrefix(p["@id"].(string), certificatePrefix)
		match, _ := p["match"].(map[string]any)
		sni, _ := match["sni"].([]any)
		for _, h := range sni {
			if s, ok := h.(string); ok {
				hosts[id] = append(hosts[id], s)
			}
		}
	}
	out := []LoadedCertificate{}
	for _, e := range entries {
		id := e.id()
		if id == "" {
			continue
		}
		h := hosts[id]
		if h == nil {
			h = []string{}
		}
		slices.Sort(h)
		out = append(out, LoadedCertificate{ID: id, SHA256: Fingerprint(e.Certificate), Hosts: h})
	}
	slices.SortFunc(out, func(a, b LoadedCertificate) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}
