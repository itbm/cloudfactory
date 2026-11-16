package caddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// The TLS automation policies of the panel domains of the resellers (white label). Each
// reseller can have its own panel domain, for example panel.agency.example. The edge proxy
// sends a request for one of these domains to the panel, as for the panel domain, and gets a
// certificate for each domain:
//
//   - A public name gets its certificate from Let's Encrypt.
//   - A name that is not public, for example panel.agency.test, gets its certificate from
//     PanelCA, as a website domain that is not public.
const (
	brandPolicyID      = "cf-panel-brands-tls"
	brandLocalPolicyID = "cf-panel-brands-local-tls"
)

// MaxBrandDomains is the largest number of reseller panel domains.
const MaxBrandDomains = 1000

// BrandPolicies gives the TLS automation policies of the reseller panel domains. A list with
// no names gives no policy.
func BrandPolicies(brands []string) []map[string]any {
	var public, local []string
	for _, b := range brands {
		if PublicName(b) {
			public = append(public, b)
		} else {
			local = append(local, b)
		}
	}
	var out []map[string]any
	if len(public) > 0 {
		out = append(out, map[string]any{"@id": brandPolicyID, "subjects": public, "issuers": []any{map[string]any{"module": "acme"}}})
	}
	if len(local) > 0 {
		out = append(out, map[string]any{"@id": brandLocalPolicyID, "subjects": local, "issuers": []any{
			map[string]any{"module": "internal", "ca": PanelCA, "lifetime": panelLifetime, "sign_with_root": true},
		}})
	}
	return out
}

// BrandMethod gives the certificate method of a reseller panel domain.
func BrandMethod(domain string) string {
	if PublicName(domain) {
		return PanelLetsEncrypt
	}
	return PanelInternal
}

// PanelHosts gives the names of the panel route: the panel domain first, then the reseller
// panel domains in order, with no repeated name.
func PanelHosts(domain string, brands []string) []string {
	hosts := []string{domain}
	for _, b := range brands {
		b = strings.ToLower(b)
		if !slices.Contains(hosts, b) {
			hosts = append(hosts, b)
		}
	}
	return hosts
}

// SetPanelBrands sets the reseller panel domains. The panel route then agrees with the panel
// domain and with each reseller panel domain, and each reseller panel domain has a TLS
// policy. A domain that is not in the list loses its route and its policy.
//
// The policies come first, before the route: with the route first, Caddy would start an ACME
// order for a name that is not public.
func (c *Client) SetPanelBrands(ctx context.Context, domain string, brands []string) error {
	hosts := PanelHosts(domain, brands)
	brands = hosts[1:]
	if err := c.setPanelCA(ctx); err != nil {
		return err
	}
	for _, id := range []string{brandPolicyID, brandLocalPolicyID} {
		if err := c.removeID(ctx, id); err != nil {
			return err
		}
	}
	const path = "/config/apps/tls/automation/policies"
	for _, policy := range BrandPolicies(brands) {
		policies, err := c.tlsPolicies(ctx)
		if err != nil {
			return err
		}
		if len(policies) == 0 {
			if err := c.putOrPatch(ctx, path, []any{policy}); err != nil {
				return err
			}
			continue
		}
		// Each policy has its own subjects, so the sequence of the policies is not important.
		// A POST on a list adds the value at the end.
		if err := c.expect(ctx, http.MethodPost, path, policy, http.StatusOK); err != nil {
			return err
		}
	}
	status, body, err := c.do(ctx, http.MethodPatch, "/id/cf-panel/match", []any{hostMatch(hosts)})
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status == http.StatusNotFound || unknownID(body) {
		return fmt.Errorf("the edge proxy has no panel route. Start the panel first (cf-agent panel install)")
	}
	return fmt.Errorf("caddy: patch cf-panel match: status %d: %s", status, body)
}

// PanelBrands gives the reseller panel domains: the subjects of the TLS policies of the
// reseller panel domains. It gives no names when the edge proxy has no such policy.
func (c *Client) PanelBrands(ctx context.Context) ([]string, error) {
	var brands []string
	for _, id := range []string{brandPolicyID, brandLocalPolicyID} {
		status, body, err := c.do(ctx, http.MethodGet, "/id/"+id+"/subjects", nil)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound || unknownID(body) {
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("caddy: get %s: status %d: %s", id, status, body)
		}
		var subjects []string
		if err := json.Unmarshal(body, &subjects); err != nil {
			return nil, fmt.Errorf("caddy: read %s: %w", id, err)
		}
		brands = append(brands, subjects...)
	}
	return brands, nil
}

// removeID removes the object with the ID. An ID that is not in the configuration is not an
// error.
func (c *Client) removeID(ctx context.Context, id string) error {
	status, body, err := c.do(ctx, http.MethodDelete, "/id/"+id, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNotFound || unknownID(body) {
		return nil
	}
	return fmt.Errorf("caddy: delete %s: status %d: %s", id, status, body)
}
