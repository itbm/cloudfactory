// Package packs reads and checks packs: signed manifests from a publisher that add a website
// runtime, versions of a built-in PHP runtime, or a database engine to Cloudfactory with no new
// agent code. docs/packs.md is the contract.
//
// A pack has a manifest (pack.json) with the contract "cf-pack/1" or "cf-pack/2", and the
// images of the runtime or of the engine, each with a digest. The publisher signs the exact bytes of the manifest with an
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
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/itbm/cloudfactory/agent/internal/signature"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

const (
	// Contract is the first version of the manifest format. It has only the kind "runtime".
	Contract = "cf-pack/1"
	// Contract2 is the second version of the manifest format. It has the keys of Contract and
	// the kind "database". A runtime pack can use either version.
	Contract2 = "cf-pack/2"
	// RuntimeContract is the version of the contract between the agent and the image of a
	// runtime pack that adds a new runtime.
	RuntimeContract = "cf-runtime/1"
	// PHPContract is the version of the contract between the agent and the image of a PHP
	// pack. A PHP pack adds no runtime: it adds versions of PHP to a built-in PHP runtime
	// (Runtime.Family), and its images do what the images of that runtime do.
	PHPContract = "cf-php/1"
	// KindRuntime is a pack that adds a website runtime or versions of PHP. It is the only
	// kind of cf-pack/1.
	KindRuntime = "runtime"
	// KindDatabase is a pack that adds a database engine (cf-pack/2).
	KindDatabase = "database"
	// DatabaseContract is the version of the contract between the agent and the image of a
	// database pack: the image has the program /cf/manage (docs/packs.md).
	DatabaseContract = "cf-database/1"
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
	// Database is the engine of a database pack. A runtime pack has none.
	Database *Database `json:"database,omitempty"`
	Images   []Image   `json:"images"`
}

// Database is the engine of a database pack (cf-database/1).
type Database struct {
	Contract string `json:"contract"`
	// Port is the TCP port of the engine in its container, for example "3306". The websites
	// connect to a database server on this port at its member address.
	Port string `json:"port"`
	// DataDir is the data directory in the container, for example "/var/lib/mysql". The agent
	// mounts the data of the database server there.
	DataDir string `json:"dataDir"`
}

// PortNumber gives the port of the engine as a number. Parse checked it.
func (d Database) PortNumber() int {
	n, _ := strconv.Atoi(d.Port)
	return n
}

// IsDatabase reports whether the pack adds a database engine.
func (m Manifest) IsDatabase() bool { return m.Kind == KindDatabase && m.Database != nil }

// Image gives the image of a version of the pack.
func (m Manifest) Image(version string) (Image, bool) {
	for _, img := range m.Images {
		if img.Version == version {
			return img, true
		}
	}
	return Image{}, false
}

// Runtime is the runtime of a runtime pack. The runtime contract gives its keys: Type,
// DefaultCommand and DefaultInstall for cf-runtime/1, and Family for cf-php/1.
type Runtime struct {
	Contract string `json:"contract"`
	Type     string `json:"type,omitempty"`
	// DefaultCommand and DefaultInstall are the usual start command and install command of a
	// website. Only the panel uses them: the job of a website gives its own command.
	DefaultCommand string `json:"defaultCommand,omitempty"`
	DefaultInstall string `json:"defaultInstall,omitempty"`
	// Family is the built-in PHP runtime that a PHP pack adds versions to, for example
	// "apache-php-fpm".
	Family string `json:"family,omitempty"`
}

// runtimeKeys are the keys of the runtime object for each runtime contract.
var runtimeKeys = map[string][]string{
	RuntimeContract: {"contract", "type", "defaultCommand", "defaultInstall"},
	PHPContract:     {"contract", "family"},
}

// PHP reports whether the pack adds versions of PHP to a built-in PHP runtime (cf-php/1). Such
// a pack adds no runtime to the agent.
func (m Manifest) PHP() bool { return m.Runtime.Contract == PHPContract }

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
	// The contract gives the keys of the manifest. cf-pack/2 adds the key database.
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) == nil && probe != nil {
		_ = json.Unmarshal(probe["contract"], &m.Contract)
	}
	keys := []string{"contract", "kind", "slug", "name", "version", "publisher", "description", "runtime", "images"}
	if m.Contract == Contract2 {
		keys = append(keys, "database")
	}
	top, err := object(raw, "the manifest", keys...)
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
	if m.Contract != Contract && m.Contract != Contract2 {
		return m, fmt.Errorf("the contract %q is not known; this agent reads %q and %q", m.Contract, Contract, Contract2)
	}
	switch {
	case m.Kind == KindRuntime:
	case m.Kind == KindDatabase && m.Contract == Contract:
		return m, fmt.Errorf("a database pack needs the contract %q", Contract2)
	case m.Kind == KindDatabase:
	default:
		return m, fmt.Errorf("the kind %q is not known; use %q or %q", m.Kind, KindRuntime, KindDatabase)
	}
	errs := []error{validate.PackName(m.Slug), validate.PackVersion(m.Version),
		text("name", m.Name, 1, 60), text("publisher", m.Publisher, 1, 100), text("description", m.Description, 0, 500)}

	if m.Kind == KindRuntime {
		if rt, ok := top["runtime"]; !ok {
			errs = append(errs, errors.New("a runtime pack needs the key runtime"))
		} else if err := parseRuntime(rt, &m.Runtime); err != nil {
			errs = append(errs, err)
		}
		if _, ok := top["database"]; ok {
			errs = append(errs, errors.New("a runtime pack cannot have the key database"))
		}
	} else {
		if _, ok := top["runtime"]; ok {
			errs = append(errs, errors.New("a database pack cannot have the key runtime"))
		}
		m.Database = &Database{}
		if db, ok := top["database"]; !ok {
			errs = append(errs, errors.New("a database pack needs the key database"))
		} else if err := parseDatabase(db, m.Database); err != nil {
			errs = append(errs, err)
		}
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
		if m.PHP() && img.Version != "" {
			// The agent writes the php.ini file of a website for this version of PHP.
			if err := validate.PHPVersion(img.Version); err != nil {
				errs = append(errs, fmt.Errorf("%s.version: %w", what, err))
			}
		}
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

// parseDatabase reads and checks the database object of a database pack (cf-database/1).
func parseDatabase(raw json.RawMessage, db *Database) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return errors.New("database is not a JSON object")
	}
	if v, ok := probe["contract"]; ok && json.Unmarshal(v, &db.Contract) != nil {
		return fmt.Errorf("database.contract: %w", errString)
	}
	if db.Contract != DatabaseContract {
		return fmt.Errorf("the database contract %q is not known; this agent reads %q", db.Contract, DatabaseContract)
	}
	fields, err := object(raw, "database", "contract", "port", "dataDir")
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range []struct {
		key string
		to  *string
	}{{"port", &db.Port}, {"dataDir", &db.DataDir}} {
		if v, ok := fields[f.key]; !ok {
			errs = append(errs, fmt.Errorf("database.%s is necessary", f.key))
		} else if err := json.Unmarshal(v, f.to); err != nil {
			errs = append(errs, fmt.Errorf("database.%s: %w", f.key, errString))
		}
	}
	if db.Port != "" {
		if err := validate.PackPort(db.Port); err != nil {
			errs = append(errs, fmt.Errorf("database.port: %w", err))
		} else if n := db.PortNumber(); n < 1024 || n > 65535 {
			errs = append(errs, errors.New("database.port: use a TCP port from 1024 to 65535"))
		}
	}
	if db.DataDir != "" {
		if err := validate.PackDataDir(db.DataDir); err != nil {
			errs = append(errs, fmt.Errorf("database.dataDir: %w", err))
		} else if first, _, _ := strings.Cut(db.DataDir[1:], "/"); validate.ReservedPackDir(first) {
			errs = append(errs, fmt.Errorf("database.dataDir: the directory /%s belongs to the container; use a different directory, for example /var/lib/data", first))
		}
	}
	return errors.Join(errs...)
}

// parseRuntime reads and checks the runtime object of a runtime pack. The runtime contract
// gives the keys of the object.
func parseRuntime(raw json.RawMessage, rt *Runtime) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		return errors.New("runtime is not a JSON object")
	}
	if v, ok := probe["contract"]; ok && json.Unmarshal(v, &rt.Contract) != nil {
		return fmt.Errorf("runtime.contract: %w", errString)
	}
	known, ok := runtimeKeys[rt.Contract]
	if !ok {
		return fmt.Errorf("the runtime contract %q is not known; this agent reads %q and %q", rt.Contract, RuntimeContract, PHPContract)
	}
	fields, err := object(raw, "runtime", known...)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range []struct {
		key string
		to  *string
	}{{"type", &rt.Type}, {"defaultCommand", &rt.DefaultCommand}, {"defaultInstall", &rt.DefaultInstall}, {"family", &rt.Family}} {
		if v, ok := fields[f.key]; ok {
			if err := json.Unmarshal(v, f.to); err != nil {
				errs = append(errs, fmt.Errorf("runtime.%s: %w", f.key, errString))
			}
		}
	}
	switch rt.Contract {
	case RuntimeContract:
		if rt.Type != TypeApp {
			errs = append(errs, fmt.Errorf("the runtime type %q is not known; %s has only the type %q", rt.Type, RuntimeContract, TypeApp))
		}
		errs = append(errs, commandLine("runtime.defaultCommand", rt.DefaultCommand, true), commandLine("runtime.defaultInstall", rt.DefaultInstall, false))
	case PHPContract:
		if err := validate.PHPRuntime(rt.Family); err != nil {
			errs = append(errs, fmt.Errorf("runtime.family: %w", err))
		}
	}
	return errors.Join(errs...)
}

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
		return nil, fmt.Errorf("%s has the key %q, which the contract does not have; the pack may need a later release of Cloudfactory", what, unknown[0])
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
