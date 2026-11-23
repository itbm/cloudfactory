package caddy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
)

func panelHosts(t *testing.T, fake *fakeCaddy) []string {
	t.Helper()
	list, i := fake.object("cf-panel")
	if i < 0 {
		t.Fatal("no panel route")
	}
	b, _ := json.Marshal(list[i]["match"])
	var match []struct {
		Host []string `json:"host"`
	}
	json.Unmarshal(b, &match)
	return match[0].Host
}

func policySubjects(fake *fakeCaddy, id string) []string {
	list, i := fake.object(id)
	if i < 0 {
		return nil
	}
	b, _ := json.Marshal(list[i]["subjects"])
	var subjects []string
	json.Unmarshal(b, &subjects)
	return subjects
}

// A reseller panel domain goes to the panel. A public name gets a certificate from Let's
// Encrypt, and a name that is not public gets one from the panel CA. A new install of the
// panel keeps the reseller panel domains.
func TestResellerPanelDomains(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.SetPanelBrands(ctx, "panel.example.com", []string{"panel.agency.example"}); err == nil {
		t.Fatal("brands with no panel route")
	}
	if err := c.ApplyPanel(ctx, "panel.example.com", PanelLetsEncrypt, 8080, 8081, 8082); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPanelBrands(ctx, "panel.example.com", []string{"Panel.Agency.com", "panel.agency.test", "panel.example.com", "panel.agency.com"}); err != nil {
		t.Fatal(err)
	}
	if got := panelHosts(t, fake); !slices.Equal(got, []string{"panel.example.com", "panel.agency.com", "panel.agency.test"}) {
		t.Errorf("hosts = %v", got)
	}
	if got := policySubjects(fake, brandPolicyID); !slices.Equal(got, []string{"panel.agency.com"}) {
		t.Errorf("Let's Encrypt subjects = %v", got)
	}
	if got := policySubjects(fake, brandLocalPolicyID); !slices.Equal(got, []string{"panel.agency.test"}) {
		t.Errorf("panel CA subjects = %v", got)
	}
	if fake.tls[0]["@id"] != panelPolicyID {
		t.Errorf("the panel policy is not first: %v", fake.tls[0]["@id"])
	}

	// A new install of the panel keeps the reseller panel domains in the route.
	if err := c.ApplyPanel(ctx, "panel.example.com", PanelLetsEncrypt, 8080, 8081, 8082); err != nil {
		t.Fatal(err)
	}
	if got := panelHosts(t, fake); len(got) != 3 {
		t.Errorf("hosts after a new install = %v", got)
	}

	// The full list replaces the list before it.
	if err := c.SetPanelBrands(ctx, "panel.example.com", nil); err != nil {
		t.Fatal(err)
	}
	if got := panelHosts(t, fake); !slices.Equal(got, []string{"panel.example.com"}) {
		t.Errorf("hosts = %v", got)
	}
	if _, i := fake.object(brandPolicyID); i >= 0 {
		t.Error("the policy of the removed domains stays")
	}
	if _, i := fake.object(brandLocalPolicyID); i >= 0 {
		t.Error("the local policy of the removed domains stays")
	}
}
