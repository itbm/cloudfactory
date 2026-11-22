package caddy

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// pemPair gives a certificate and its key in PEM format.
func pemPair(t *testing.T, cert *x509.Certificate, key any) (string, string) {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func testCertificates(t *testing.T) (Certificate, Certificate) {
	ca := newTestCA(t, "Customer CA")
	wild, wildKey := ca.leaf(t, "*.example.com", "example.com")
	shop, shopKey := ca.leaf(t, "shop.example.org")
	a := Certificate{ID: "01jbq3z5k8n6t4w2x9y7r5p3c1", Hosts: []string{"www.example.com", "example.com"}}
	a.CertificatePEM, a.KeyPEM = pemPair(t, wild, wildKey)
	a.CertificatePEM += ca.pem()
	b := Certificate{ID: "01jbq3z5k8n6t4w2x9y7r5p3c2", Hosts: []string{"shop.example.org"}}
	b.CertificatePEM, b.KeyPEM = pemPair(t, shop, shopKey)
	return a, b
}

func TestCertificatesFromThePanelAreLoadedSelectedAndSkipped(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	a, b := testCertificates(t)

	// An administrator loaded a certificate with no tag of the panel. It stays.
	fake.config = map[string]string{certificatesPath: `[{"certificate":"OTHER","key":"OTHERKEY"}]`}
	if err := c.SetCertificates(ctx, []Certificate{a, b}); err != nil {
		t.Fatal(err)
	}
	var entries []certificateEntry
	json.Unmarshal([]byte(fake.config[certificatesPath]), &entries)
	if len(entries) != 3 || entries[0].Certificate != "OTHER" || entries[1].id() != a.ID || entries[2].id() != b.ID || entries[1].Key != a.KeyPEM {
		t.Fatalf("load_pem = %s", fake.config[certificatesPath])
	}
	var policies []map[string]any
	json.Unmarshal([]byte(fake.config[connectionPoliciesPath]), &policies)
	if len(policies) != 3 || policies[0]["@id"] != "cf-cert-"+a.ID || len(policies[2]) != 0 {
		t.Fatalf("policies = %s", fake.config[connectionPoliciesPath])
	}
	if got := fake.config[connectionPoliciesPath]; !strings.Contains(got, `"sni":["example.com","www.example.com"]`) || !strings.Contains(got, `"any_tag":["cf-cert-`+a.ID+`"]`) {
		t.Errorf("policies = %s", got)
	}
	if got := fake.config[automaticHTTPSPath]; got != `{"ignore_loaded_certificates":true,"skip_certificates":["example.com","shop.example.org","www.example.com"]}` {
		t.Errorf("automatic_https = %s", got)
	}

	loaded, err := c.Certificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []LoadedCertificate{
		{ID: a.ID, SHA256: Fingerprint(a.CertificatePEM), Hosts: []string{"example.com", "www.example.com"}},
		{ID: b.ID, SHA256: Fingerprint(b.CertificatePEM), Hosts: []string{"shop.example.org"}},
	}
	if !slices.EqualFunc(loaded, want, func(x, y LoadedCertificate) bool {
		return x.ID == y.ID && x.SHA256 == y.SHA256 && slices.Equal(x.Hosts, y.Hosts)
	}) || len(want[0].SHA256) != 64 {
		t.Errorf("Certificates = %+v", loaded)
	}

	// The panel route comes first, and the policies of the certificates stay.
	if err := c.ApplyPanel(ctx, "panel.example.net", PanelInternal, 19080, 19081); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(fake.config[connectionPoliciesPath]), &policies)
	if len(policies) != 4 || policies[0]["@id"] != panelConnectionID || policies[1]["@id"] != "cf-cert-"+a.ID || len(policies[3]) != 0 {
		t.Fatalf("policies after ApplyPanel = %s", fake.config[connectionPoliciesPath])
	}

	// The second certificate goes. The panel policy stays.
	if err := c.SetCertificates(ctx, []Certificate{a}); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(fake.config[connectionPoliciesPath]), &policies)
	if len(policies) != 3 || policies[0]["@id"] != panelConnectionID || policies[1]["@id"] != "cf-cert-"+a.ID {
		t.Fatalf("policies = %s", fake.config[connectionPoliciesPath])
	}
	entries = nil
	json.Unmarshal([]byte(fake.config[certificatesPath]), &entries)
	if len(entries) != 2 || entries[1].id() != a.ID {
		t.Fatalf("load_pem = %s", fake.config[certificatesPath])
	}

	// No certificates: only the certificate of the administrator stays.
	if err := c.SetCertificates(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := fake.config[certificatesPath]; got != `[{"certificate":"OTHER","key":"OTHERKEY"}]` {
		t.Errorf("load_pem = %s", got)
	}
	if got := fake.config[automaticHTTPSPath]; got != `{"ignore_loaded_certificates":true}` {
		t.Errorf("automatic_https = %s", got)
	}
	if loaded, _ := c.Certificates(ctx); len(loaded) != 0 {
		t.Errorf("Certificates = %+v", loaded)
	}
}

func TestNoCertificatesRemovesTheList(t *testing.T) {
	fake := &fakeCaddy{loaded: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(srv.URL)
	a, _ := testCertificates(t)
	if err := c.SetCertificates(context.Background(), []Certificate{a}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCertificates(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.config[certificatesPath]; ok {
		t.Errorf("load_pem = %s", fake.config[certificatesPath])
	}
	// Each change sends only what changed: a second call with the same list loads nothing.
	fake.calls = nil
	if err := c.SetCertificates(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "load_pem") && !strings.HasPrefix(call, "GET") {
			t.Errorf("call %s", call)
		}
	}
}

func TestTheBaseConfigurationDoesNotSkipHostsForLoadedCertificates(t *testing.T) {
	b, _ := json.Marshal(BaseConfig())
	if !strings.Contains(string(b), `"automatic_https":{"ignore_loaded_certificates":true}`) {
		t.Errorf("base = %s", b)
	}
}
