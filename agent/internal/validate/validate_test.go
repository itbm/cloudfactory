package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/rulesgen"
)

// vector is one example of schema/input-vectors.json: a string, or {"repeat": [...]} with
// strings and [string, number] pairs.
type vector string

func (v *vector) UnmarshalJSON(b []byte) error {
	var plain string
	if json.Unmarshal(b, &plain) == nil {
		*v = vector(plain)
		return nil
	}
	var parts struct{ Repeat []any }
	if err := json.Unmarshal(b, &parts); err != nil {
		return err
	}
	var out strings.Builder
	for _, part := range parts.Repeat {
		switch p := part.(type) {
		case string:
			out.WriteString(p)
		case []any:
			out.WriteString(strings.Repeat(p[0].(string), int(p[1].(float64))))
		default:
			return fmt.Errorf("the example part %v is not a string or a pair", part)
		}
	}
	*v = vector(out.String())
	return nil
}

// Audit findings R5 and F04: the agent and the panel accept the same values. The two have their
// rules from schema/input-rules.json, and the two run the examples in schema/input-vectors.json
// (the panel: api/tests/Unit/InputRulesTest.php).
func TestTheAgentAndThePanelUseTheSameExamples(t *testing.T) {
	b, err := os.ReadFile("../../../schema/input-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Rules map[string]struct{ Valid, Invalid []vector }
	}
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	for name := range vectors.Rules {
		if rules[name] == nil {
			t.Errorf("the examples have the rule %q, which does not exist", name)
		}
	}
	for name, r := range rules {
		examples := vectors.Rules[name]
		if len(examples.Valid) == 0 || len(examples.Invalid) == 0 {
			t.Errorf("the rule %q must have valid and invalid examples", name)
		}
		for _, v := range examples.Valid {
			if err := r.check(string(v)); err != nil {
				t.Errorf("%s: the agent refuses the valid example %.80q", name, v)
			}
		}
		for _, v := range examples.Invalid {
			if r.check(string(v)) == nil {
				t.Errorf("%s: the agent accepts the invalid example %.80q", name, v)
			}
		}
	}
}

// The generated files are current: the generator gives the same text for the rules file. CI
// also runs the generator and compares the files.
func TestTheGeneratedRulesAreCurrent(t *testing.T) {
	schema, err := os.ReadFile("../../../schema/input-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	goCode, phpCode, err := rulesgen.Generate(schema)
	if err != nil {
		t.Fatal(err)
	}
	for current, want := range map[string][]byte{"rules_gen.go": goCode, "../../../api/app/Validation/InputRules.php": phpCode} {
		if got, err := os.ReadFile(current); err != nil || string(want) != string(got) {
			t.Errorf("%s is not current. Run \"go run ./cmd/cf-rules\" in the directory agent.", current)
		}
	}
}

// An error does not repeat a long value such as an SSH key.
func TestTheErrorOfAKeyDoesNotRepeatTheKey(t *testing.T) {
	if err := SSHKey("ssh-dss AAAA"); err == nil || strings.Contains(err.Error(), "AAAA") {
		t.Errorf("SSHKey: %v", err)
	}
	if err := Extension("Bad"); err == nil || err.Error() != `the PHP extension "Bad" is not valid` {
		t.Errorf("Extension: %v", err)
	}
}

func TestCommand(t *testing.T) {
	if err := Command([]string{"node", "server.js", "--port=8080"}); err != nil {
		t.Error(err)
	}
	if err := Command(nil); err != nil {
		t.Error(err)
	}
	for _, bad := range [][]string{{""}, {"a\nb"}, {strings.Repeat("a", 1025)}, make([]string, MaxCommandArguments+1)} {
		if Command(bad) == nil {
			t.Errorf("Command accepts %.40q", bad)
		}
	}
}

func TestTenantDatabaseName(t *testing.T) {
	if err := TenantDatabaseName("acme_x7k2", "acme_x7k2_shop"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"mysql", "acme_x7k2_", "other_ab12_shop", "pg_acme_x7k2_shop", "acme_x7k2_Shop"} {
		if TenantDatabaseName("acme_x7k2", bad) == nil {
			t.Errorf("TenantDatabaseName accepts %q", bad)
		}
	}
	// A reserved name is refused for each Unix user (CF-01).
	if slices.Contains(reservedDatabaseNames, "postgres") == false || TenantDatabaseName("pg_x7k2", "pg_x7k2_shop") == nil {
		t.Error("a reserved database name is accepted")
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
	for _, p := range []int{0, -1, 65536, 22, 80, 443, 7443, 8080, 8443, 3306, 5432, 6379, 19080, 19081, 19432, 19443, 20000, 29999} {
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
