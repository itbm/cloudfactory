// Package gvisor installs runsc from the official gVisor release binaries (FR-1206).
//
// Each version goes in its own directory. The link /usr/local/bin/runsc points to the
// current version, and the link runsc.previous points to the version before it, so that
// the agent can go back in one step.
package gvisor

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ReleaseURL is the base address of the gVisor release binaries.
const ReleaseURL = "https://storage.googleapis.com/gvisor/releases/release"

var versionRE = regexp.MustCompile(`^[0-9]{8}(\.[0-9]+)?$`)

// Installer installs and switches gVisor versions.
type Installer struct {
	BaseURL string // ReleaseURL, or a test server
	Root    string // for example "/usr/local/lib/cloudfactory/gvisor"
	BinDir  string // for example "/usr/local/bin"
	HTTP    *http.Client
}

// Arch gives the gVisor name of a Go architecture.
func Arch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "aarch64", nil
	}
	return "", fmt.Errorf("gvisor: the architecture %s is not supported", goarch)
}

// Link is the path of the runsc link that Podman uses.
func (i Installer) Link() string { return filepath.Join(i.BinDir, "runsc") }

func (i Installer) previousLink() string { return filepath.Join(i.BinDir, "runsc.previous") }

// Install downloads one version, checks the SHA-512 checksum of each binary, and makes it
// the current version. The old current version becomes the previous version.
func (i Installer) Install(ctx context.Context, version, arch string) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("gvisor: the version %q is not valid", version)
	}
	dir := filepath.Join(i.Root, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"runsc", "containerd-shim-runsc-v1"} {
		url := fmt.Sprintf("%s/%s/%s/%s", strings.TrimRight(i.BaseURL, "/"), version, arch, name)
		if err := i.download(ctx, url, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return i.switchTo(filepath.Join(dir, "runsc"))
}

// Rollback makes the previous version the current version again.
func (i Installer) Rollback() error {
	prev, err := os.Readlink(i.previousLink())
	if err != nil {
		return errors.New("gvisor: there is no previous version")
	}
	return i.switchTo(prev)
}

// Current gives the version of the current link, for example "20261020.0".
func (i Installer) Current() string {
	target, err := os.Readlink(i.Link())
	if err != nil {
		return ""
	}
	return filepath.Base(filepath.Dir(target))
}

func (i Installer) switchTo(target string) error {
	if old, err := os.Readlink(i.Link()); err == nil && old != target {
		if err := replaceLink(old, i.previousLink()); err != nil {
			return err
		}
	}
	return replaceLink(target, i.Link())
}

// replaceLink changes a link in one step, so that Podman never sees a missing runsc.
func replaceLink(target, link string) error {
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func (i Installer) download(ctx context.Context, url, dest string) error {
	want, err := i.fetchChecksum(ctx, url+".sha512")
	if err != nil {
		return err
	}
	if sum, err := fileSHA512(dest); err == nil && sum == want {
		return nil // This version is already installed.
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := i.client().Do(req)
	if err != nil {
		return fmt.Errorf("gvisor: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gvisor: download %s: status %d", url, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha512.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("gvisor: the SHA-512 checksum of %s is not correct", filepath.Base(dest))
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func (i Installer) fetchChecksum(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := i.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("gvisor: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gvisor: download %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	// The file has the format "<checksum>  <name>".
	fields := strings.Fields(string(b))
	if len(fields) == 0 || len(fields[0]) != 128 {
		return "", fmt.Errorf("gvisor: %s is not a SHA-512 checksum file", url)
	}
	return strings.ToLower(fields[0]), nil
}

func (i Installer) client() *http.Client {
	if i.HTTP != nil {
		return i.HTTP
	}
	return http.DefaultClient
}

func fileSHA512(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
