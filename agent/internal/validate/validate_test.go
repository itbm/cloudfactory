package validate

import "testing"

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
	for _, good := range []string{"https://panel.example.com", "https://panel.example.com:8443/", "http://127.0.0.1:8080", "http://localhost", "http://[::1]:8080"} {
		if err := PanelURL(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{"http://panel.example.com", "http://10.0.0.5", "ftp://panel.example.com", "panel.example.com", "https://", "https://user:pw@panel.example.com"} {
		if err := PanelURL(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
