// Package selfupdate installs a new agent binary (FR-1812). The agent checks the signature
// before it uses the binary, and keeps the previous binary for rollback.
package selfupdate

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/signature"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Updater replaces the agent binary.
type Updater struct {
	// ReleaseURL is the base address, for example "https://github.com/itbm/cloudfactory/releases/download".
	ReleaseURL string
	// Binary is the path of the agent, for example "/usr/local/bin/cf-agent".
	Binary string
	Key    *ecdsa.PublicKey
	HTTP   *http.Client
	// Current is the version of the running agent, for example "0.1.0".
	Current string
}

// CheckVersion refuses a version that is older than the current version, unless the
// administrator asks for a downgrade on purpose (CF-16). A signed old version can have
// security problems that a newer version fixed.
func (u Updater) CheckVersion(version string, allowDowngrade bool) error {
	if err := validate.Version(version); err != nil {
		return err
	}
	if !allowDowngrade && u.Current != "" && Older(version, u.Current) {
		return fmt.Errorf("selfupdate: the version %s is older than the current version %s", strings.TrimPrefix(version, "v"), strings.TrimPrefix(u.Current, "v"))
	}
	return nil
}

// Older reports whether version a is older than version b. It compares the major, minor and
// patch numbers, and then a version with no suffix is newer than a version with a suffix
// (for example "0.2.0" is newer than "0.2.0-rc.1"). A version that it cannot read is older.
func Older(a, b string) bool {
	pa, sa, okA := parseVersion(a)
	pb, sb, okB := parseVersion(b)
	if !okA || !okB {
		return !okA && okB
	}
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	switch {
	case sa == sb:
		return false
	case sa == "":
		return false
	case sb == "":
		return true
	}
	return sa < sb
}

func parseVersion(v string) (nums [3]int, suffix string, ok bool) {
	v = strings.TrimPrefix(v, "v")
	v, suffix, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, suffix, true
}

// AssetName is the name of the binary in a release, for example "cf-agent_linux_arm64".
func AssetName(goarch string) string { return "cf-agent_linux_" + goarch }

// Previous is the path of the previous binary.
func (u Updater) Previous() string { return u.Binary + ".previous" }

// Release is a downloaded agent binary with its signed manifest.
type Release struct {
	Version     string
	Asset       string
	Binary      []byte
	Signature   []byte
	Manifest    []byte
	ManifestSig []byte
}

// Fetch downloads the binary of a version, its signature, and its signed manifest.
func (u Updater) Fetch(ctx context.Context, version, goarch string) (Release, error) {
	if err := validate.Version(version); err != nil {
		return Release{}, err
	}
	r := Release{Version: strings.TrimPrefix(version, "v"), Asset: AssetName(goarch)}
	base := fmt.Sprintf("%s/v%s/%s", strings.TrimRight(u.ReleaseURL, "/"), r.Version, r.Asset)
	var err error
	if r.Binary, err = u.get(ctx, base, 200<<20); err != nil {
		return Release{}, err
	}
	if r.Signature, err = u.get(ctx, base+".sig", 4096); err != nil {
		return Release{}, err
	}
	if r.Manifest, err = u.get(ctx, base+".manifest", 4096); err != nil {
		return Release{}, err
	}
	if r.ManifestSig, err = u.get(ctx, base+".manifest.sig", 4096); err != nil {
		return Release{}, err
	}
	return r, nil
}

// Verify checks the signature of the binary and of its manifest. R-13: the signature of the
// binary does not say which version the binary is. The manifest names the version, the asset
// and the SHA-256 hash of the binary, so a signed older binary cannot pass as a newer version.
func (u Updater) Verify(r Release) error {
	if err := signature.Verify(u.Key, r.Binary, string(r.Signature)); err != nil {
		return fmt.Errorf("selfupdate: binary: %w", err)
	}
	if err := signature.Verify(u.Key, r.Manifest, string(r.ManifestSig)); err != nil {
		return fmt.Errorf("selfupdate: manifest: %w", err)
	}
	m := ParseManifest(r.Manifest)
	sum := sha256.Sum256(r.Binary)
	switch {
	case m["version"] != r.Version:
		return fmt.Errorf("selfupdate: the manifest is for version %q, not %q", m["version"], r.Version)
	case m["asset"] != r.Asset:
		return fmt.Errorf("selfupdate: the manifest is for %q, not %q", m["asset"], r.Asset)
	case m["sha256"] != hex.EncodeToString(sum[:]):
		return errors.New("selfupdate: the binary does not agree with the manifest")
	}
	return nil
}

// ManifestRootHelper is the key of the manifest line "rootHelper=1". A release with this line
// has an agent with a root helper (second audit, the privilege boundary). The root helper
// installs only such a release for its worker.
const ManifestRootHelper = "rootHelper"

// Manifest gives the manifest of a release asset. The release workflow writes the same format.
func Manifest(version, asset string, bin []byte) string {
	sum := sha256.Sum256(bin)
	return fmt.Sprintf("version=%s\nasset=%s\nsha256=%s\n%s=1\n", strings.TrimPrefix(version, "v"), asset, hex.EncodeToString(sum[:]), ManifestRootHelper)
}

// ParseManifest reads the "key=value" lines of a release manifest. The release workflow
// writes it, for example:
//
//	version=0.2.0
//	asset=cf-agent_linux_amd64
//	sha256=9f86d0...
//	rootHelper=1
func ParseManifest(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			m[k] = v
		}
	}
	return m
}

// KeepPrevious copies the current binary to the previous binary.
func (u Updater) KeepPrevious() error {
	if err := copyFile(u.Binary, u.Previous()); err != nil {
		return fmt.Errorf("selfupdate: keep previous binary: %w", err)
	}
	return nil
}

// Replace puts a checked binary in place of the current binary in one step.
func (u Updater) Replace(bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(u.Binary), ".cf-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), u.Binary)
}

// Install downloads a version, checks its signature, keeps the current binary as the
// previous binary, and puts the new binary in its place. The caller then restarts the agent.
func (u Updater) Install(ctx context.Context, version, goarch string, allowDowngrade bool) error {
	if err := u.CheckVersion(version, allowDowngrade); err != nil {
		return err
	}
	r, err := u.Fetch(ctx, version, goarch)
	if err != nil {
		return err
	}
	if err := u.Verify(r); err != nil {
		return err
	}
	if err := u.KeepPrevious(); err != nil {
		return err
	}
	return u.Replace(r.Binary)
}

// Rollback puts the previous binary back. systemd runs "cf-agent rollback" when the
// new agent does not start.
func (u Updater) Rollback() error {
	if _, err := os.Stat(u.Previous()); err != nil {
		return fmt.Errorf("selfupdate: there is no previous binary")
	}
	tmp := u.Binary + ".rollback"
	if err := copyFile(u.Previous(), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, u.Binary)
}

func (u Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := u.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("selfupdate: download %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
