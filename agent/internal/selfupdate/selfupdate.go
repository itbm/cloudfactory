// Package selfupdate installs a new agent binary (FR-1812). The agent checks the signature
// before it uses the binary, and keeps the previous binary for rollback.
package selfupdate

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
}

// AssetName is the name of the binary in a release, for example "cf-agent_linux_arm64".
func AssetName(goarch string) string { return "cf-agent_linux_" + goarch }

// Previous is the path of the previous binary.
func (u Updater) Previous() string { return u.Binary + ".previous" }

// Fetch downloads the binary and the signature of a version.
func (u Updater) Fetch(ctx context.Context, version, goarch string) (bin, sig []byte, err error) {
	if err := validate.Version(version); err != nil {
		return nil, nil, err
	}
	tag := "v" + strings.TrimPrefix(version, "v")
	base := fmt.Sprintf("%s/%s/%s", strings.TrimRight(u.ReleaseURL, "/"), tag, AssetName(goarch))
	if bin, err = u.get(ctx, base, 200<<20); err != nil {
		return nil, nil, err
	}
	if sig, err = u.get(ctx, base+".sig", 4096); err != nil {
		return nil, nil, err
	}
	return bin, sig, nil
}

// Verify checks the signature of a binary.
func (u Updater) Verify(bin, sig []byte) error {
	if err := signature.Verify(u.Key, bin, string(sig)); err != nil {
		return fmt.Errorf("selfupdate: %w", err)
	}
	return nil
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
func (u Updater) Install(ctx context.Context, version, goarch string) error {
	bin, sig, err := u.Fetch(ctx, version, goarch)
	if err != nil {
		return err
	}
	if err := u.Verify(bin, sig); err != nil {
		return err
	}
	if err := u.KeepPrevious(); err != nil {
		return err
	}
	return u.Replace(bin)
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
