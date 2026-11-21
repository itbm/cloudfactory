// Package packs reads and checks packs: signed manifests from a publisher that add a website
// runtime to Cloudfactory with no new agent code. docs/packs.md is the contract.
//
// A pack has a manifest (pack.json) with the contract "cf-pack/1", and the images of the
// runtime, each with a digest. The publisher signs the exact bytes of the manifest with an
// ECDSA P-256 key ("cosign sign-blob"), and signs each image with the same key ("cosign
// sign"). The panel installs a pack only when a trusted key signed it. The agent checks the
// signature again, and lets Podman pull an image of a pack only with a signature of that key
// (Policy).
//
// The agent reads a manifest only as data. It never runs a program that a manifest names.
package packs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/itbm/cloudfactory/agent/internal/signature"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

const (
	// Contract is the version of the manifest format.
	Contract = "cf-pack/1"
	// RuntimeContract is the version of the contract between the agent and the image of a
	// runtime pack.
	RuntimeContract = "cf-runtime/1"
	// KindRuntime is the only kind of pack of the contract cf-pack/1. The kind "database" is
	// for a later release.
	KindRuntime = "runtime"
	// TypeApp is the only runtime type of cf-runtime/1: the image has /usr/local/bin/cf-app,
	// which runs the start command of the website. It is quadlet.KindApp.
	TypeApp = "app"

	// The files of an installed pack in its directory.
	ManifestFile  = "pack.json"
	SignatureFile = "pack.json.sig"
	KeyFile       = "publisher.pub"
)

// Manifest is a pack manifest. The agent keeps only the keys of this struct. A key that starts
// with "x-" is for the publisher, and the agent ignores it. Each other key is an error, so that
// an agent never ignores a key of a later contract.
type Manifest struct {
	Contract    string  `json:"contract"`
	Kind        string  `json:"kind"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	Publisher   string  `json:"publisher"`
	Description string  `json:"description,omitempty"`
	Runtime     Runtime `json:"runtime"`
	Images      []Image `json:"images"`
}

// Runtime is the runtime of a runtime pack.
type Runtime struct {
	Contract string `json:"contract"`
	Type     string `json:"type"`
	// DefaultCommand and DefaultInstall are the usual start command and install command of a
	// website. Only the panel uses them: the job of a website gives its own command.
	DefaultCommand string `json:"defaultCommand"`
	DefaultInstall string `json:"defaultInstall,omitempty"`
}

// Image is one image of a runtime pack, for one version of the runtime.
type Image struct {
	// Version is the version of the runtime, for example "1.25". Customers select it.
	Version string `json:"version"`
	// Ref is the image with a tag, for example "ghcr.io/example/cf-golang:1.25.4".
	Ref string `json:"ref"`
	// Digest is the "sha256:" digest of the image. The signature of the manifest covers it,
	// so the agent pulls only this image.
	Digest string `json:"digest"`
	// SupportEndsAt is the last day of support of the version, for example "2026-08-31".
	SupportEndsAt string `json:"supportEndsAt,omitempty"`
}

// Repository gives the image with no tag, for example "ghcr.io/example/cf-golang".
func (i Image) Repository() string { return Repository(i.Ref) }

// Repository gives an image reference with no tag and no digest.
func Repository(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

// Pack is a pack that the agent checked: its manifest, the exact bytes that the publisher
// signed, the signature and the public key of the publisher. A pack with no signature and no
// key comes from the developer mode of the panel.
type Pack struct {
	Manifest  Manifest
	Raw       []byte
	Signature string
	PublicKey string
}

// Signed reports whether the publisher signed the pack.
func (p Pack) Signed() bool { return p.PublicKey != "" }

var dateRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// Parse checks a manifest and gives it. It refuses a key that it does not know, except a key
// that starts with "x-".
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > validate.MaxPackManifestBytes {
		return m, fmt.Errorf("the manifest has %d bytes; the limit is %d", len(raw), validate.MaxPackManifestBytes)
	}
	top, err := object(raw, "the manifest", "contract", "kind", "slug", "name", "version", "publisher", "description", "runtime", "images")
	if err != nil {
		return m, err
	}
	for _, f := range []struct {
		key string
		to  *string
	}{{"contract", &m.Contract}, {"kind", &m.Kind}, {"slug", &m.Slug}, {"name", &m.Name}, {"version", &m.Version}, {"publisher", &m.Publisher}, {"description", &m.Description}} {
		if v, ok := top[f.key]; ok {
			if err := json.Unmarshal(v, f.to); err != nil {
				return m, fmt.Errorf("%s: %w", f.key, errString)
			}
		}
	}
	if m.Contract != Contract {
		return m, fmt.Errorf("the contract %q is not known; this agent reads %q", m.Contract, Contract)
	}
	switch m.Kind {
	case KindRuntime:
	case "database":
		return m, errors.New("database engine packs are not available yet; this release installs runtime packs only")
	default:
		return m, fmt.Errorf("the kind %q is not known; use %q", m.Kind, KindRuntime)
	}
	errs := []error{validate.PackName(m.Slug), validate.PackVersion(m.Version),
		text("name", m.Name, 1, 60), text("publisher", m.Publisher, 1, 100), text("description", m.Description, 0, 500)}

	rt, ok := top["runtime"]
	if !ok {
		errs = append(errs, errors.New("a runtime pack needs the key runtime"))
	} else if fields, err := object(rt, "runtime", "contract", "type", "defaultCommand", "defaultInstall"); err != nil {
		errs = append(errs, err)
	} else {
		for _, f := range []struct {
			key string
			to  *string
		}{{"contract", &m.Runtime.Contract}, {"type", &m.Runtime.Type}, {"defaultCommand", &m.Runtime.DefaultCommand}, {"defaultInstall", &m.Runtime.DefaultInstall}} {
			if v, ok := fields[f.key]; ok {
				if err := json.Unmarshal(v, f.to); err != nil {
					errs = append(errs, fmt.Errorf("runtime.%s: %w", f.key, errString))
				}
			}
		}
		if m.Runtime.Contract != RuntimeContract {
			errs = append(errs, fmt.Errorf("the runtime contract %q is not known; this agent reads %q", m.Runtime.Contract, RuntimeContract))
		}
		if m.Runtime.Type != TypeApp {
			errs = append(errs, fmt.Errorf("the runtime type %q is not known; %s has only the type %q", m.Runtime.Type, RuntimeContract, TypeApp))
		}
		errs = append(errs, commandLine("runtime.defaultCommand", m.Runtime.DefaultCommand, true), commandLine("runtime.defaultInstall", m.Runtime.DefaultInstall, false))
	}

	var images []json.RawMessage
	if v, ok := top["images"]; !ok || json.Unmarshal(v, &images) != nil {
		errs = append(errs, errors.New("images must be a list"))
	} else if len(images) == 0 || len(images) > validate.MaxPackImages {
		errs = append(errs, fmt.Errorf("a pack has 1 to %d images, not %d", validate.MaxPackImages, len(images)))
	}
	seen := map[string]bool{}
	for n, raw := range images {
		what := fmt.Sprintf("images[%d]", n)
		fields, err := object(raw, what, "version", "ref", "digest", "supportEndsAt")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var img Image
		for _, f := range []struct {
			key string
			to  *string
		}{{"version", &img.Version}, {"ref", &img.Ref}, {"digest", &img.Digest}, {"supportEndsAt", &img.SupportEndsAt}} {
			if v, ok := fields[f.key]; ok {
				if err := json.Unmarshal(v, f.to); err != nil {
					errs = append(errs, fmt.Errorf("%s.%s: %w", what, f.key, errString))
				}
			}
		}
		errs = append(errs, validate.PackImageVersion(img.Version), validate.Image(img.Ref))
		if img.Digest == "" {
			errs = append(errs, fmt.Errorf("%s needs a digest", what))
		} else {
			errs = append(errs, validate.Digest(img.Digest))
		}
		if img.SupportEndsAt != "" {
			if _, err := time.Parse(time.DateOnly, img.SupportEndsAt); err != nil || !dateRE.MatchString(img.SupportEndsAt) {
				errs = append(errs, fmt.Errorf("%s.supportEndsAt must be a date such as 2026-08-31", what))
			}
		}
		if seen[img.Version] {
			errs = append(errs, fmt.Errorf("the version %q is in the pack two times", img.Version))
		}
		seen[img.Version] = true
		m.Images = append(m.Images, img)
	}
	return m, errors.Join(errs...)
}

var errString = errors.New("the value must be a string")

// object reads a JSON object and gives its values. A key that is not in known and that does
// not start with "x-" is an error.
func object(raw []byte, what string, known ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	if dec.More() {
		return nil, fmt.Errorf("%s has more than one JSON value", what)
	}
	var unknown []string
	for k := range fields {
		if !slices.Contains(known, k) && !strings.HasPrefix(k, "x-") {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, fmt.Errorf("%s has the key %q, which the contract %s does not have; the pack may need a later release of Cloudfactory", what, unknown[0], Contract)
	}
	return fields, nil
}

// text checks a text for people: its length in characters, and no control characters.
func text(key, s string, minimum, maximum int) error {
	n := len([]rune(s))
	if n < minimum || n > maximum {
		if minimum > 0 {
			return fmt.Errorf("%s needs 1 to %d characters", key, maximum)
		}
		return fmt.Errorf("%s can have up to %d characters", key, maximum)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s has a control character", key)
	}
	return nil
}

// commandLine checks a usual command of the manifest with the rule "commandLine".
func commandLine(key, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s is necessary", key)
		}
		return nil
	}
	if err := validate.CommandLine(s); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// Check checks a pack from the panel: the manifest, and the signature with the public key of
// the publisher. A pack with no signature and no key comes from the developer mode of the
// panel. It gets no image policy (Policy), so its images must pass the policy of the installer.
func Check(raw []byte, sig, publicKey string) (Pack, error) {
	m, err := Parse(raw)
	if err != nil {
		return Pack{}, err
	}
	p := Pack{Manifest: m, Raw: raw, Signature: strings.TrimSpace(sig), PublicKey: strings.TrimSpace(publicKey)}
	switch {
	case p.Signature == "" && p.PublicKey == "":
		return p, nil
	case p.Signature == "" || p.PublicKey == "":
		return Pack{}, fmt.Errorf("the pack %s has a signature with no key, or a key with no signature", m.Slug)
	}
	p.PublicKey += "\n"
	key, err := signature.ParsePublicKey([]byte(p.PublicKey))
	if err != nil {
		return Pack{}, fmt.Errorf("the key of the pack %s: %w", m.Slug, err)
	}
	if key.Curve.Params().Name != "P-256" {
		return Pack{}, fmt.Errorf("the key of the pack %s is not an ECDSA P-256 key", m.Slug)
	}
	if err := signature.Verify(key, raw, p.Signature); err != nil {
		return Pack{}, fmt.Errorf("the pack %s: %w", m.Slug, err)
	}
	return p, nil
}

// CheckSet checks the packs of one request together: each name and each image repository is
// in one pack only. Two packs with the same repository and different keys would make the
// image policy of one of them wrong.
func CheckSet(list []Pack) error {
	if len(list) > validate.MaxPacks {
		return fmt.Errorf("a server can have %d packs, not %d", validate.MaxPacks, len(list))
	}
	names := map[string]bool{}
	repos := map[string]string{}
	for _, p := range list {
		if names[p.Manifest.Slug] {
			return fmt.Errorf("the pack %s is in the request two times", p.Manifest.Slug)
		}
		names[p.Manifest.Slug] = true
		for _, img := range p.Manifest.Images {
			repo := img.Repository()
			if other, ok := repos[repo]; ok && other != p.Manifest.Slug {
				return fmt.Errorf("the image %s is in the packs %s and %s; an image repository can be in one pack only", repo, other, p.Manifest.Slug)
			}
			repos[repo] = p.Manifest.Slug
		}
	}
	return nil
}

// Load reads the packs in dir: one directory for each pack, with the files ManifestFile,
// SignatureFile and KeyFile. It checks each pack again. A pack that is not valid does not stop
// the others: Load gives it in bad, and the agent writes it in its log.
func Load(dir string) (good []Pack, bad map[string]error, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	bad = map[string]error{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := loadOne(filepath.Join(dir, e.Name()))
		if err == nil && p.Manifest.Slug != e.Name() {
			err = fmt.Errorf("the directory %s has the pack %s", e.Name(), p.Manifest.Slug)
		}
		if err != nil {
			bad[e.Name()] = err
			continue
		}
		good = append(good, p)
	}
	if err := CheckSet(good); err != nil {
		return nil, bad, err
	}
	return good, bad, nil
}

func loadOne(dir string) (Pack, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Pack{}, err
	}
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}
	sig, err := read(SignatureFile)
	if err != nil {
		return Pack{}, err
	}
	key, err := read(KeyFile)
	if err != nil {
		return Pack{}, err
	}
	return Check(raw, sig, key)
}
