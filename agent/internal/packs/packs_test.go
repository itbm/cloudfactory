package packs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// example is the manifest of the example pack in the repository.
func example(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../plugins/packs/golang/pack.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// change gives the example manifest with a change to its JSON object.
func change(t *testing.T, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(example(t), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type publisher struct {
	key *ecdsa.PrivateKey
	pem string
}

func newPublisher(t *testing.T) publisher {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return publisher{key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}

// sign signs as "cosign sign-blob" does: ECDSA over the SHA-256 of the bytes, in base64.
func (p publisher) sign(t *testing.T, b []byte) string {
	t.Helper()
	sum := sha256.Sum256(b)
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestTheExamplePackFollowsTheContract(t *testing.T) {
	m, err := Parse(example(t))
	if err != nil {
		t.Fatal(err)
	}
	if m.Contract != Contract || m.Kind != KindRuntime || m.Slug != "golang" || m.Runtime.Contract != RuntimeContract || m.Runtime.Type != TypeApp {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Images) != 2 || m.Images[0].Version != "1.27" || m.Images[0].Repository() != "ghcr.io/example/cf-golang" {
		t.Errorf("images = %+v", m.Images)
	}
}

// phpPack gives the manifest of a first-party PHP pack in the repository.
func phpPack(t *testing.T, slug string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../plugins/packs/" + slug + "/pack.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestThePHPPacksFollowTheContract(t *testing.T) {
	for slug, family := range map[string]string{"php-legacy-apache": "apache-php-fpm", "php-legacy-frankenphp": "frankenphp"} {
		m, err := Parse(phpPack(t, slug))
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if !m.PHP() || m.Slug != slug || m.Runtime.Family != family || m.Runtime.DefaultCommand != "" {
			t.Errorf("%s: manifest = %+v", slug, m)
		}
		if len(m.Images) != 2 || m.Images[0].Version != "8.3" || m.Images[1].Version != "8.2" || m.Images[1].SupportEndsAt != "2026-12-31" {
			t.Errorf("%s: images = %+v", slug, m.Images)
		}
	}
	m, _ := Parse(example(t))
	if m.PHP() {
		t.Error("the Go pack is a PHP pack")
	}
}

func TestAPHPPackThatIsNotValidIsRefused(t *testing.T) {
	php := func(fn func(rt, img map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(phpPack(t, "php-legacy-apache"), &m); err != nil {
			t.Fatal(err)
		}
		fn(m["runtime"].(map[string]any), img(m))
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, c := range map[string]struct {
		fn   func(rt, img map[string]any)
		want string
	}{
		"a runtime that is not PHP": {func(rt, _ map[string]any) { rt["family"] = "node" }, "not a built-in PHP runtime"},
		"the name of the pack":      {func(rt, _ map[string]any) { rt["family"] = "php-legacy-apache" }, "not a built-in PHP runtime"},
		"no family":                 {func(rt, _ map[string]any) { delete(rt, "family") }, "not a built-in PHP runtime"},
		"a start command":           {func(rt, _ map[string]any) { rt["defaultCommand"] = "php artisan serve" }, `key "defaultCommand"`},
		"a type":                    {func(rt, _ map[string]any) { rt["type"] = "php" }, `key "type"`},
		"a version that is not PHP": {func(_, i map[string]any) { i["version"] = "8.3-fpm" }, "PHP version"},
		"a version with a patch":    {func(_, i map[string]any) { i["version"] = "8.3.14" }, "PHP version"},
		"a family that is a number": {func(rt, _ map[string]any) { rt["family"] = 8 }, "must be a string"},
	} {
		_, err := Parse(php(c.fn))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	// A runtime of cf-runtime/1 cannot have the key of cf-php/1.
	if _, err := Parse(change(t, func(m map[string]any) { m["runtime"].(map[string]any)["family"] = "frankenphp" })); err == nil || !strings.Contains(err.Error(), `key "family"`) {
		t.Errorf("a family in cf-runtime/1: %v", err)
	}
}

func TestTheDatabasePackFollowsTheContract(t *testing.T) {
	m, err := Parse(phpPack(t, "mysql"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsDatabase() || m.Contract != Contract2 || m.Slug != "mysql" || m.PHP() {
		t.Errorf("manifest = %+v", m)
	}
	if m.Database.Contract != DatabaseContract || m.Database.Port != "3306" || m.Database.PortNumber() != 3306 || m.Database.DataDir != "/var/lib/mysql" {
		t.Errorf("database = %+v", m.Database)
	}
	if img, ok := m.Image("8.4"); !ok || img.Repository() != "ghcr.io/example/cf-mysql" {
		t.Errorf("image 8.4 = %+v %v", img, ok)
	}
	if _, ok := m.Image("9.0"); ok {
		t.Error("the pack has the version 9.0")
	}
	// A runtime pack can use cf-pack/2.
	rt, err := Parse(change(t, func(m map[string]any) { m["contract"] = Contract2 }))
	if err != nil || rt.IsDatabase() || rt.Database != nil {
		t.Errorf("a runtime pack of cf-pack/2: %+v %v", rt, err)
	}
}

func TestADatabasePackThatIsNotValidIsRefused(t *testing.T) {
	db := func(fn func(m, db map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(phpPack(t, "mysql"), &m); err != nil {
			t.Fatal(err)
		}
		fn(m, m["database"].(map[string]any))
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, c := range map[string]struct {
		fn   func(m, db map[string]any)
		want string
	}{
		"cf-pack/1":                  {func(m, _ map[string]any) { m["contract"] = Contract }, `key "database"`},
		"a runtime":                  {func(m, _ map[string]any) { m["runtime"] = map[string]any{"contract": "cf-runtime/1"} }, "cannot have the key runtime"},
		"no database":                {func(m, _ map[string]any) { delete(m, "database") }, "needs the key database"},
		"a database in a runtime":    {func(m, _ map[string]any) { m["kind"] = KindRuntime }, "needs the key runtime"},
		"a later contract":           {func(_, d map[string]any) { d["contract"] = "cf-database/9" }, `database contract "cf-database/9" is not known`},
		"an unknown key":             {func(_, d map[string]any) { d["user"] = "999" }, `key "user"`},
		"no port":                    {func(_, d map[string]any) { delete(d, "port") }, "database.port is necessary"},
		"a port that is a number":    {func(_, d map[string]any) { d["port"] = 3306 }, "must be a string"},
		"a port below 1024":          {func(_, d map[string]any) { d["port"] = "80" }, "database.port"},
		"a port above 65535":         {func(_, d map[string]any) { d["port"] = "70000" }, "1024 to 65535"},
		"no data directory":          {func(_, d map[string]any) { delete(d, "dataDir") }, "database.dataDir is necessary"},
		"a relative data directory":  {func(_, d map[string]any) { d["dataDir"] = "var/lib/mysql" }, "database.dataDir"},
		"a data directory in /tmp":   {func(_, d map[string]any) { d["dataDir"] = "/tmp/mysql" }, "/tmp belongs to the container"},
		"a data directory in /cf":    {func(_, d map[string]any) { d["dataDir"] = "/cf" }, "/cf belongs to the container"},
		"a data directory with ..":   {func(_, d map[string]any) { d["dataDir"] = "/var/../etc" }, "database.dataDir"},
		"the name of a built-in":     {func(m, _ map[string]any) { m["slug"] = "postgresql" }, "built-in runtime"},
		"an engine version with ':'": {func(m, _ map[string]any) { img(m)["version"] = "8.4:latest" }, "version of a pack image"},
	} {
		_, err := Parse(db(c.fn))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
}

func TestAManifestThatIsNotValidIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		fn   func(m map[string]any)
		want string
	}{
		"a later contract":      {func(m map[string]any) { m["contract"] = "cf-pack/9" }, `contract "cf-pack/9" is not known`},
		"a database pack":       {func(m map[string]any) { m["kind"] = "database" }, `needs the contract "cf-pack/2"`},
		"an unknown kind":       {func(m map[string]any) { m["kind"] = "theme" }, `kind "theme"`},
		"an unknown key":        {func(m map[string]any) { m["exec"] = "sh -c id" }, `key "exec"`},
		"an unknown image key":  {func(m map[string]any) { img(m)["pull"] = "always" }, `key "pull"`},
		"an unknown run key":    {func(m map[string]any) { m["runtime"].(map[string]any)["privileged"] = true }, `key "privileged"`},
		"a built-in name":       {func(m map[string]any) { m["slug"] = "node" }, "built-in runtime"},
		"a bad name":            {func(m map[string]any) { m["slug"] = "Go Lang" }, "pack name"},
		"a bad version":         {func(m map[string]any) { m["version"] = "1.0" }, "pack version"},
		"no name":               {func(m map[string]any) { m["name"] = "" }, "name needs"},
		"a control character":   {func(m map[string]any) { m["publisher"] = "Evil\u001b[2J" }, "control character"},
		"a later runtime":       {func(m map[string]any) { m["runtime"].(map[string]any)["contract"] = "cf-runtime/9" }, "runtime contract"},
		"a PHP runtime":         {func(m map[string]any) { m["runtime"].(map[string]any)["type"] = "php" }, "runtime type"},
		"no start command":      {func(m map[string]any) { delete(m["runtime"].(map[string]any), "defaultCommand") }, "defaultCommand is necessary"},
		"a bad start command":   {func(m map[string]any) { m["runtime"].(map[string]any)["defaultCommand"] = "./server\nrm -rf /" }, "defaultCommand"},
		"no runtime":            {func(m map[string]any) { delete(m, "runtime") }, "needs the key runtime"},
		"no images":             {func(m map[string]any) { m["images"] = []any{} }, "1 to 10 images"},
		"images is not a list":  {func(m map[string]any) { m["images"] = "ghcr.io/example/cf-golang:1.27" }, "images must be a list"},
		"an image with no tag":  {func(m map[string]any) { img(m)["ref"] = "ghcr.io/example/cf-golang" }, "image"},
		"a short image name":    {func(m map[string]any) { img(m)["ref"] = "golang:1.27" }, "image"},
		"an image no digest":    {func(m map[string]any) { delete(img(m), "digest") }, "needs a digest"},
		"a bad digest":          {func(m map[string]any) { img(m)["digest"] = "sha256:abc" }, "digest"},
		"a bad date":            {func(m map[string]any) { img(m)["supportEndsAt"] = "next year" }, "supportEndsAt"},
		"one version two times": {func(m map[string]any) { img(m)["version"] = "1.26" }, "two times"},
		"a number for a string": {func(m map[string]any) { m["version"] = 1 }, "must be a string"},
	} {
		_, err := Parse(change(t, c.fn))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	if _, err := Parse([]byte(`{"contract": "cf-pack/1"} {}`)); err == nil {
		t.Error("two JSON values are accepted")
	}
	if _, err := Parse([]byte(strings.Repeat(" ", 40000))); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a large manifest: %v", err)
	}
	// The keys of the publisher are permitted.
	if _, err := Parse(change(t, func(m map[string]any) { m["x-homepage"] = "https://example.com"; img(m)["x-built"] = "2026-11-01" })); err != nil {
		t.Errorf("x- keys: %v", err)
	}
}

func img(m map[string]any) map[string]any { return m["images"].([]any)[0].(map[string]any) }

func TestTheSignatureCoversTheExactBytes(t *testing.T) {
	pub := newPublisher(t)
	raw := example(t)
	sig := pub.sign(t, raw)
	p, err := Check(raw, sig, pub.pem)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Signed() || p.Manifest.Slug != "golang" {
		t.Errorf("pack = %+v", p)
	}
	// One more space is a different manifest.
	if _, err := Check(append([]byte(" "), raw...), sig, pub.pem); err == nil {
		t.Error("a changed manifest has a good signature")
	}
	if _, err := Check(raw, sig, newPublisher(t).pem); err == nil {
		t.Error("the key of a different publisher is accepted")
	}
	if _, err := Check(raw, sig, ""); err == nil {
		t.Error("a signature with no key is accepted")
	}
	if _, err := Check(raw, "", pub.pem); err == nil {
		t.Error("a key with no signature is accepted")
	}
	// The developer mode: no signature and no key.
	if p, err := Check(raw, "", ""); err != nil || p.Signed() {
		t.Errorf("a pack with no signature: %v", err)
	}
}

func TestARepositoryIsInOnePackOnly(t *testing.T) {
	a, _ := Check(example(t), "", "")
	b, _ := Check(change(t, func(m map[string]any) { m["slug"] = "golang-tip" }), "", "")
	if err := CheckSet([]Pack{a, b}); err == nil || !strings.Contains(err.Error(), "one pack only") {
		t.Errorf("one repository in two packs: %v", err)
	}
	if err := CheckSet([]Pack{a, a}); err == nil || !strings.Contains(err.Error(), "two times") {
		t.Errorf("one pack two times: %v", err)
	}
}

// installerPolicy is the policy that install/install.sh writes, with --allow-unsigned docker.io.
const installerPolicy = `{
  "default": [{ "type": "reject" }],
  "transports": {
    "docker": {
      "docker.io": [{ "type": "insecureAcceptAnything" }],
      "ghcr.io/itbm": [{ "type": "sigstoreSigned", "keyPath": "/etc/cloudfactory/cosign.pub", "signedIdentity": { "type": "matchRepository" } }]
    },
    "containers-storage": { "": [{ "type": "insecureAcceptAnything" }] }
  }
}`

func TestThePolicyKeepsTheRulesOfTheInstaller(t *testing.T) {
	dir := "/etc/cloudfactory/packs"
	entries := []PolicyEntry{
		{Scope: "ghcr.io/example/cf-golang", KeyPath: dir + "/golang/publisher.pub", Pack: "golang"},
		// The release registry asks for the release key: a pack cannot make it weaker.
		{Scope: "ghcr.io/itbm/cf-ruby", KeyPath: dir + "/ruby/publisher.pub", Pack: "ruby"},
		// A registry that accepts each image gets a stricter rule for the pack.
		{Scope: "docker.io/example/bun", KeyPath: dir + "/bun/publisher.pub", Pack: "bun"},
	}
	out, skipped, err := Policy([]byte(installerPolicy), dir, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "ghcr.io/itbm/cf-ruby" {
		t.Errorf("skipped = %v", skipped)
	}
	var p struct {
		Default    []map[string]any                       `json:"default"`
		Transports map[string]map[string][]map[string]any `json:"transports"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	docker := p.Transports["docker"]
	if p.Default[0]["type"] != "reject" || docker["ghcr.io/itbm"][0]["keyPath"] != "/etc/cloudfactory/cosign.pub" || docker["docker.io"][0]["type"] != "insecureAcceptAnything" {
		t.Errorf("the rules of the installer changed: %s", out)
	}
	if len(p.Transports["containers-storage"]) != 1 {
		t.Errorf("the local storage rule changed: %s", out)
	}
	for _, scope := range []string{"ghcr.io/example/cf-golang", "docker.io/example/bun"} {
		r := docker[scope]
		if len(r) != 1 || r[0]["type"] != "sigstoreSigned" || !strings.HasPrefix(r[0]["keyPath"].(string), dir+"/") {
			t.Errorf("rule of %s = %v", scope, r)
		}
	}
	if _, ok := docker["ghcr.io/itbm/cf-ruby"]; ok {
		t.Error("a pack has a rule below the release registry")
	}

	// Again with no packs: only the rules of the installer stay.
	again, _, err := Policy(out, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(again), dir) {
		t.Errorf("rules of packs stay: %s", again)
	}
	first, _, _ := Policy([]byte(installerPolicy), dir, nil)
	if string(again) != string(first) {
		t.Errorf("the policy with no packs depends on the packs before:\n%s\n%s", again, first)
	}

	// A key outside the directory of the packs is refused.
	if _, _, err := Policy([]byte(installerPolicy), dir, []PolicyEntry{{Scope: "ghcr.io/x/y", KeyPath: "/tmp/evil.pub"}}); err == nil {
		t.Error("a key outside the directory of the packs is accepted")
	}
	if _, _, err := Policy([]byte("not json"), dir, nil); err == nil {
		t.Error("a policy that is not JSON is accepted")
	}

	reg := Registries(entries, skipped)
	if !strings.Contains(reg, "\"ghcr.io/example/cf-golang\":\n    use-sigstore-attachments: true") || strings.Contains(reg, "cf-ruby") {
		t.Errorf("registries =\n%s", reg)
	}
	if Registries(nil, nil) != "" {
		t.Error("the registries file of no packs is not empty")
	}
}

func TestLoadSkipsAPackThatIsNotValid(t *testing.T) {
	dir := t.TempDir()
	pub := newPublisher(t)
	raw := example(t)
	write := func(slug, name, content string) {
		if err := os.MkdirAll(filepath.Join(dir, slug), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, slug, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("golang", ManifestFile, string(raw))
	write("golang", SignatureFile, pub.sign(t, raw))
	write("golang", KeyFile, pub.pem)
	// A manifest that somebody changed after the signature.
	write("ruby", ManifestFile, strings.Replace(string(change(t, func(m map[string]any) { m["slug"] = "ruby" })), "Go", "Ruby", 1))
	write("ruby", SignatureFile, pub.sign(t, raw))
	write("ruby", KeyFile, pub.pem)

	good, bad, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(good) != 1 || good[0].Manifest.Slug != "golang" || !good[0].Signed() {
		t.Errorf("good = %+v", good)
	}
	if bad["ruby"] == nil {
		t.Errorf("bad = %v", bad)
	}
	if good, _, err := Load(filepath.Join(dir, "none")); err != nil || good != nil {
		t.Errorf("no directory: %v %v", good, err)
	}
}

// The cases of schema/pack-vectors.json. The panel has the same test (PackTest), so that the
// panel and the agent accept the same manifests.
func TestTheSharedManifestCases(t *testing.T) {
	b, err := os.ReadFile("../../../schema/pack-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name   string         `json:"name"`
			Valid  bool           `json:"valid"`
			Set    map[string]any `json:"set"`
			Delete []string       `json:"delete"`
			Raw    *string        `json:"raw"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) < 40 {
		t.Fatalf("%d cases", len(file.Cases))
	}
	for _, c := range file.Cases {
		raw := example(t)
		if c.Raw != nil {
			raw = []byte(*c.Raw)
		} else if len(c.Set) > 0 || len(c.Delete) > 0 {
			raw = change(t, func(m map[string]any) {
				for path, v := range c.Set {
					setPath(t, m, path, v, false)
				}
				for _, path := range c.Delete {
					setPath(t, m, path, nil, true)
				}
			})
		}
		_, err := Parse(raw)
		if c.Valid && err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
		if !c.Valid && err == nil {
			t.Errorf("%s: the manifest is accepted", c.Name)
		}
	}
}

// setPath sets or removes a value at a path such as "images.0.ref".
func setPath(t *testing.T, m map[string]any, path string, v any, remove bool) {
	t.Helper()
	keys := strings.Split(path, ".")
	var at any = m
	for _, k := range keys[:len(keys)-1] {
		switch node := at.(type) {
		case map[string]any:
			at = node[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil {
				t.Fatalf("path %s: %v", path, err)
			}
			at = node[i]
		}
	}
	last := keys[len(keys)-1]
	switch node := at.(type) {
	case map[string]any:
		if remove {
			delete(node, last)
		} else {
			node[last] = v
		}
	case []any:
		i, err := strconv.Atoi(last)
		if err != nil || remove {
			t.Fatalf("path %s: cannot change", path)
		}
		node[i] = v
	default:
		t.Fatalf("path %s: no object", path)
	}
}
