package validate

import (
	"encoding/json"
	"os"
	"testing"
)

// Audit finding R5: the agent and the panel accept the same names. The two test their rules
// with one list of examples (the panel: AuditFixesTest).
func TestTheAgentAndThePanelUseTheSameExamplesForNames(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/names.json")
	if err != nil {
		t.Fatal(err)
	}
	var names struct {
		Domains, Images struct{ Valid, Invalid []string }
	}
	if err := json.Unmarshal(b, &names); err != nil {
		t.Fatal(err)
	}
	if len(names.Domains.Valid) == 0 || len(names.Domains.Invalid) == 0 || len(names.Images.Valid) == 0 || len(names.Images.Invalid) == 0 {
		t.Fatalf("the list has no examples: %+v", names)
	}
	for _, d := range names.Domains.Valid {
		if err := Domain(d); err != nil {
			t.Errorf("the agent refuses the domain %q, which the panel accepts", d)
		}
	}
	for _, d := range names.Domains.Invalid {
		if Domain(d) == nil {
			t.Errorf("the agent accepts the domain %q, which the panel refuses", d)
		}
	}
	// The list has references with no tag. The agent gets each reference with a tag.
	for _, i := range names.Images.Valid {
		if err := Image(i + ":8.4.0-1"); err != nil {
			t.Errorf("the agent refuses the image %q, which the panel accepts", i)
		}
	}
	for _, i := range names.Images.Invalid {
		if Image(i+":8.4.0-1") == nil {
			t.Errorf("the agent accepts the image %q, which the panel refuses", i)
		}
	}
}

func TestValues(t *testing.T) {
	good := []struct {
		f func(string) error
		v string
	}{
		{UnixUser, "acme_x7k2"},
		{Domain, "shop.acme.example"},
		{ID, "01JBQ3Z5K8N6T4W2X9Y7R5P3M1"},
		{DatabaseName, "shop_live"},
		{Image, "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"},
		{Image, "registry.example.com:5000/team/app:v2"},
		{Digest, ""},
		{Digest, "sha256:" + "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"},
		{IniKey, "opcache.memory_consumption"},
		{IniValue, "256M"},
		{Version, "0.1.0"},
		{RestorePath, "/public/index.php"},
	}
	for _, c := range good {
		if err := c.f(c.v); err != nil {
			t.Errorf("%q: %v", c.v, err)
		}
	}

	bad := []struct {
		f func(string) error
		v string
	}{
		{UnixUser, "root"},
		{UnixUser, "../etc_abcd"},
		{Domain, "Acme.example"},
		{Domain, "acme.example;rm -rf /"},
		{Domain, "localhost"},
		{ID, "../../etc/passwd"},
		{DatabaseName, "mysql; DROP"},
		{Image, "ghcr.io/cloudfactory/app"},
		{Image, "--privileged"},
		{Digest, "sha256:xyz"},
		{IniKey, "disable functions"},
		{IniValue, "1\nextension=evil.so"},
		{Version, "latest"},
		{RestorePath, "/../etc/passwd"},
		{RestorePath, "public/index.php"},
		{RestorePath, "/a/./b"},
	}
	for _, c := range bad {
		if err := c.f(c.v); err == nil {
			t.Errorf("%q: want an error", c.v)
		}
	}
}

// R-15: the join command refuses a plain HTTP panel address, except on the same server.
func TestPanelURL(t *testing.T) {
	for _, good := range []string{"https://panel.example.com", "https://panel.example.com:8443/", "http://127.0.0.1:8080", "http://localhost", "http://[::1]:8080",
		// The address that the install script gives to the agent on the panel server.
		"https://169.254.77.1:19443"} {
		if err := PanelURL(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{"http://panel.example.com", "http://10.0.0.5", "http://169.254.77.1:19080", "ftp://panel.example.com", "panel.example.com", "https://", "https://user:pw@panel.example.com"} {
		if err := PanelURL(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestSftpPort(t *testing.T) {
	for _, p := range []int{2200, 2222, 1022, 65535} {
		if err := SftpPort(p); err != nil {
			t.Errorf("SftpPort(%d): %v", p, err)
		}
	}
	for _, p := range []int{0, -1, 65536, 22, 80, 443, 7443, 8080, 8443, 3306, 5432, 19080, 19081, 19432, 19443, 20000, 29999} {
		if err := SftpPort(p); err == nil {
			t.Errorf("SftpPort(%d) is accepted", p)
		}
	}
}

// The panel can use "localhost", for a browser on the same server. A website domain cannot.
func TestPanelDomain(t *testing.T) {
	for _, ok := range []string{"localhost", "panel.localhost", "panel.example.com"} {
		if err := PanelDomain(ok); err != nil {
			t.Errorf("PanelDomain(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "panel", "LOCALHOST", "localhost.", "127.0.0.1", "panel.example.com/x"} {
		if err := PanelDomain(bad); err == nil {
			t.Errorf("PanelDomain(%q) accepted", bad)
		}
	}
	if err := Domain("localhost"); err == nil {
		t.Error(`Domain("localhost") accepted`)
	}
}
