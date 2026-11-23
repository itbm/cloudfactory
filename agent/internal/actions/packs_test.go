package actions

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/packs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// installerPolicy is the image policy that the installer writes, with one registry that the
// administrator gave with --allow-unsigned.
const installerPolicy = `{
  "default": [{ "type": "reject" }],
  "transports": {
    "docker": {
      "docker.io": [{ "type": "insecureAcceptAnything" }],
      "ghcr.io/itbm": [{ "type": "sigstoreSigned", "keyPath": "/etc/cloudfactory/cosign.pub", "signedIdentity": { "type": "matchRepository" } }]
    },
    "containers-storage": { "": [{ "type": "insecureAcceptAnything" }] }
  }
}
`

// examplePack gives the manifest of the example pack in the repository.
func examplePack(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../plugins/packs/golang/pack.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// signedPack gives the example pack with a signature of a new publisher key, as the panel
// sends it.
func signedPack(t *testing.T, raw []byte) *agentv1.Pack {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return &agentv1.Pack{
		Manifest:  string(raw),
		Signature: base64.StdEncoding.EncodeToString(sig),
		PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}
}

// renamed gives the example manifest with another slug and other image repositories.
func renamed(t *testing.T, slug string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(examplePack(t), &m); err != nil {
		t.Fatal(err)
	}
	m["slug"] = slug
	for _, img := range m["images"].([]any) {
		i := img.(map[string]any)
		i["ref"] = strings.Replace(i["ref"].(string), "cf-golang", "cf-"+slug, 1)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func withPolicy(t *testing.T, d *Deps) {
	t.Helper()
	if err := os.WriteFile(d.imagePolicy(), []byte(installerPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = quadlet.SetPackRuntimes(nil) })
}

func TestSetPacksSavesThePacksAndThePolicy(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	pack := signedPack(t, examplePack(t))
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{pack}})); err != nil {
		t.Fatal(err)
	}

	// The files of the pack, as the panel sent them.
	dir := filepath.Join(d.packsDir(), "golang")
	for name, want := range map[string]string{packs.ManifestFile: pack.Manifest, packs.SignatureFile: pack.Signature + "\n", packs.KeyFile: pack.PublicKey} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}

	// The policy keeps the rules of the installer and has a rule for each image of the pack.
	var policy struct {
		Default    []map[string]any `json:"default"`
		Transports struct {
			Docker map[string][]map[string]any `json:"docker"`
		} `json:"transports"`
	}
	b, err := os.ReadFile(d.imagePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Default) != 1 || policy.Default[0]["type"] != "reject" {
		t.Errorf("default = %v", policy.Default)
	}
	if r := policy.Transports.Docker["ghcr.io/itbm"]; len(r) != 1 || r[0]["keyPath"] != "/etc/cloudfactory/cosign.pub" {
		t.Errorf("the rule of the release registry changed: %v", r)
	}
	if r := policy.Transports.Docker["docker.io"]; len(r) != 1 || r[0]["type"] != "insecureAcceptAnything" {
		t.Errorf("the rule of --allow-unsigned changed: %v", r)
	}
	rule := policy.Transports.Docker["ghcr.io/example/cf-golang"]
	if len(rule) != 1 || rule[0]["type"] != "sigstoreSigned" || rule[0]["keyPath"] != filepath.Join(dir, packs.KeyFile) {
		t.Errorf("the rule of the pack = %v", rule)
	}
	registries, err := os.ReadFile(d.packRegistries())
	if err != nil || !strings.Contains(string(registries), "\"ghcr.io/example/cf-golang\":\n    use-sigstore-attachments: true") {
		t.Errorf("registries = %q, %v", registries, err)
	}

	// The worker uses the runtime of the pack, and reports the pack.
	if spec, ok := quadlet.RuntimeOf("golang"); !ok || spec.Pack != "golang 1.0.0" {
		t.Errorf("runtime = %+v, %v", spec, ok)
	}
	if got := d.PackStates(); len(got) != 1 || got[0].Slug != "golang" || got[0].Version != "1.0.0" {
		t.Errorf("packs = %+v", got)
	}

	// The agent loads the same packs when it starts again.
	again, _, _ := deps(t)
	again.PacksDir = d.PacksDir
	if err := again.LoadPacks(); err != nil {
		t.Fatal(err)
	}
	if got := again.PackStates(); len(got) != 1 || got[0].Slug != "golang" {
		t.Errorf("packs after a start = %+v", got)
	}
}

func TestSetPacksRemovesAPackThatIsNotInTheRequest(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	golang, other := signedPack(t, examplePack(t)), signedPack(t, renamed(t, "zig"))
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{golang, other}})); err != nil {
		t.Fatal(err)
	}
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID2, Packs: []*agentv1.Pack{golang}})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.packsDir(), "zig")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory of the removed pack is there: %v", err)
	}
	if _, ok := quadlet.RuntimeOf("zig"); ok {
		t.Error("the runtime of the removed pack is available")
	}
	b, _ := os.ReadFile(d.imagePolicy())
	if strings.Contains(string(b), "cf-zig") {
		t.Errorf("the policy has a rule of the removed pack:\n%s", b)
	}

	// With no packs, the registries file goes, and the policy is the policy of the installer.
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.packRegistries()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the registries file is there: %v", err)
	}
	var got, want any
	b, _ = os.ReadFile(d.imagePolicy())
	_ = json.Unmarshal(b, &got)
	_ = json.Unmarshal([]byte(installerPolicy), &want)
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("policy = %s, want %s", gb, wb)
	}
}

func TestSetPacksKeepsThePackOfARuntimeInUse(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{signedPack(t, examplePack(t))}})); err != nil {
		t.Fatal(err)
	}
	w := website("ghcr.io/example/cf-golang:1.27@sha256:" + strings.Repeat("0", 60) + "0127")
	w.Runtime = "golang"
	if err := d.State.Put(&Site{ID: websiteID, UnixUser: "acme_x7k2", Port: 20000, Website: w}); err != nil {
		t.Fatal(err)
	}
	err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID2}))
	if err == nil || !strings.Contains(err.Error(), "uses the runtime golang") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.packsDir(), "golang", packs.ManifestFile)); err != nil {
		t.Errorf("the pack went: %v", err)
	}
}

func TestSetPacksRefusesAPackThatIsNotValid(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	bad := signedPack(t, examplePack(t))
	bad.Manifest = strings.Replace(bad.Manifest, `"Go"`, `"Go!"`, 1)
	err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{bad}}))
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(d.packsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the agent wrote packs: %v", err)
	}
}

// The root helper checks each pack again: a worker that skips its own check cannot save a
// pack with no valid signature or with a key outside the directory of the packs.
func TestTheRootHelperChecksThePacksAgain(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	bad := signedPack(t, examplePack(t))
	bad.Signature = signedPack(t, examplePack(t)).Signature
	err := d.savePacks(t.Context(), []packIn{{Manifest: bad.Manifest, Signature: bad.Signature, PublicKey: bad.PublicKey}})
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("err = %v", err)
	}
	slug := signedPack(t, renamed(t, "golang"))
	slug.Manifest = strings.Replace(slug.Manifest, `"slug":"golang"`, `"slug":"../etc"`, 1)
	if err := d.savePacks(t.Context(), []packIn{{Manifest: slug.Manifest}}); err == nil {
		t.Fatal("the root helper saved a pack with a bad slug")
	}
	if _, err := os.Stat(d.packsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the root helper wrote packs: %v", err)
	}
}

// A pack with no signature is for the development of a pack. The policy has no rule for its
// images, so Podman pulls them only from a registry that the administrator allows.
func TestAnUnsignedPackHasNoRule(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	pack := &agentv1.Pack{Manifest: string(examplePack(t))}
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{pack}})); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(d.imagePolicy())
	if strings.Contains(string(b), "cf-golang") {
		t.Errorf("the policy has a rule for an unsigned pack:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(d.packsDir(), "golang", packs.KeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the key file is there: %v", err)
	}
	if _, ok := quadlet.RuntimeOf("golang"); !ok {
		t.Error("the runtime of the pack is not available")
	}
}

// phpPack gives the manifest of a first-party PHP pack in the repository.
func phpPack(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../plugins/packs/php-legacy-apache/pack.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A PHP pack adds versions of a built-in PHP runtime: the agent adds no runtime, and its
// images get a rule in the image policy as the images of each other pack.
func TestAPHPPackAddsNoRuntime(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	before := len(quadlet.Runtimes())
	// The image is not below the release registry here, so it gets a rule of its own.
	raw := strings.ReplaceAll(string(phpPack(t)), "ghcr.io/itbm/", "registry.example.com/php/")
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{signedPack(t, []byte(raw))}})); err != nil {
		t.Fatal(err)
	}
	if got := len(quadlet.Runtimes()); got != before {
		t.Errorf("%d runtimes, want %d", got, before)
	}
	if _, ok := quadlet.RuntimeOf("php-legacy-apache"); ok {
		t.Error("the PHP pack is a runtime")
	}
	if got := d.PackStates(); len(got) != 1 || got[0].Slug != "php-legacy-apache" {
		t.Errorf("packs = %+v", got)
	}
	b, _ := os.ReadFile(d.imagePolicy())
	if !strings.Contains(string(b), `"registry.example.com/php/apache-php8.2"`) || !strings.Contains(string(b), `"registry.example.com/php/apache-php8.3"`) {
		t.Errorf("the policy has no rule for the images of the PHP pack:\n%s", b)
	}
}

// A website of a built-in PHP runtime with an image of a PHP pack keeps the pack.
func TestSetPacksKeepsThePHPPackOfAWebsite(t *testing.T) {
	d, _, _ := deps(t)
	withPolicy(t, d)
	if err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID, Packs: []*agentv1.Pack{signedPack(t, phpPack(t))}})); err != nil {
		t.Fatal(err)
	}
	w := website("ghcr.io/itbm/apache-php8.2:0.2.0@sha256:" + strings.Repeat("0", 62) + "82")
	w.Runtime = "apache-php-fpm"
	w.PhpVersion = "8.2"
	if err := d.State.Put(&Site{ID: websiteID, UnixUser: "acme_x7k2", Port: 20000, Website: w}); err != nil {
		t.Fatal(err)
	}
	err := run(t)(d.SetPacks(&agentv1.SetPacksRequest{JobId: jobID2}))
	if err == nil || !strings.Contains(err.Error(), "uses the image ghcr.io/itbm/apache-php8.2 of the pack php-legacy-apache") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.packsDir(), "php-legacy-apache", packs.ManifestFile)); err != nil {
		t.Errorf("the pack went: %v", err)
	}
}
