// Package gvisor installs runsc from the official gVisor release archive (FR-1206).
//
// Each release has one archive for each architecture, gvisor.tar.bz2, with runsc,
// containerd-shim-runsc-v1 and the directory gvisor-bin. runsc starts the Sentry from
// gvisor-bin, so gvisor-bin must stay next to runsc.
//
// Each version goes in its own directory. The link /usr/local/bin/runsc points to the
// current version, and the link runsc.previous points to the version before it, so that
// the agent can go back in one step.
package gvisor

import (
	"archive/tar"
	"compress/bzip2"
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

// Checksums are the SHA-512 checksums of gvisor.tar.bz2 for each "version/architecture" that
// this agent knows (CF-16). The checksum file next to the archive comes from the same server
// as the archive, so it does not prove who made the archive. A new gVisor version needs a new
// agent release, or the --sha512 option of "cf-agent gvisor install".
// The release workflow puts the same values in install/install.sh.
var Checksums = map[string]string{
	"20261019.0/x86_64":  "7cbc2aeeee809dcccfb40e612504a1b76f2c119790cee59e482f7b35428cccc4f71705e7e322442d07d1f7d29b5d9e72e31319fc3088d6dcbec9fc7c1f0d8881",
	"20261019.0/aarch64": "6b14b582cd3185169df54275eed8721d94c9b40e6cb1533d0d0c46573271c6d2d03db586ea1caac46a45e805797f00b115bead9fb93636a1ffa69f2430958977",
}

// Installer installs and switches gVisor versions.
type Installer struct {
	BaseURL string // ReleaseURL, or a test server
	Root    string // for example "/usr/local/lib/cloudfactory/gvisor"
	BinDir  string // for example "/usr/local/bin"
	HTTP    *http.Client
	// Checksums replaces the package Checksums when it is not nil.
	Checksums map[string]string
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

// Archive is the name of the release archive in each architecture directory.
const Archive = "gvisor.tar.bz2"

// checksumFile is the file in the version directory that records the checksum of the
// archive that the agent extracted.
const checksumFile = ".archive.sha512"

// Install downloads the archive of one version, checks its pinned SHA-512 checksum, extracts it
// and makes it the current version. The old current version becomes the previous version.
func (i Installer) Install(ctx context.Context, version, arch string) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("gvisor: the version %q is not valid", version)
	}
	if err := os.MkdirAll(i.Root, 0o755); err != nil {
		return err
	}
	dir := filepath.Join(i.Root, version)
	url := fmt.Sprintf("%s/%s/%s/%s", strings.TrimRight(i.BaseURL, "/"), version, arch, Archive)
	want, err := i.checksum(version, arch)
	if err != nil {
		return err
	}
	if got, err := os.ReadFile(filepath.Join(dir, checksumFile)); err != nil || strings.TrimSpace(string(got)) != want {
		if err := i.downloadAndExtract(ctx, url, want, dir); err != nil {
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

// Current gives the version of the current link, for example "20261019.0".
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

// downloadAndExtract downloads the archive to a temporary file, checks its checksum, and
// extracts it into a new directory. Then it puts the new directory in the place of dir.
func (i Installer) downloadAndExtract(ctx context.Context, url, want, dir string) error {
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
	archive, err := os.CreateTemp(i.Root, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	h := sha512.New()
	if _, err := io.Copy(io.MultiWriter(archive, h), resp.Body); err != nil {
		return fmt.Errorf("gvisor: download %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("gvisor: the SHA-512 checksum of %s is not correct", Archive)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp(i.Root, ".extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extract(archive, tmp); err != nil {
		return err
	}
	for _, name := range []string{"runsc", "containerd-shim-runsc-v1"} {
		if st, err := os.Stat(filepath.Join(tmp, name)); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("gvisor: %s has no %s", Archive, name)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, checksumFile), []byte(want+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	// The current version never uses dir here: a version with the same checksum is not
	// extracted again. A directory from a failed or different download is replaced.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// extract writes the regular files and directories of a bzip2 tar archive into dest. It
// refuses links, devices, absolute paths and paths that go out of dest.
func extract(r io.Reader, dest string) error {
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("gvisor: read %s: %w", Archive, err)
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("gvisor: %s has a bad path %q", Archive, hdr.Name)
		}
		path := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("gvisor: %s has an entry of a type that is not permitted: %q", Archive, hdr.Name)
		}
	}
}

// checksum gives the pinned checksum of a version and an architecture.
func (i Installer) checksum(version, arch string) (string, error) {
	sums := i.Checksums
	if sums == nil {
		sums = Checksums
	}
	sum := strings.ToLower(sums[version+"/"+arch])
	if !sha512RE.MatchString(sum) {
		return "", fmt.Errorf("gvisor: this agent has no SHA-512 checksum for version %s on %s; give it with --sha512", version, arch)
	}
	return sum, nil
}

var sha512RE = regexp.MustCompile(`^[0-9a-f]{128}$`)

func (i Installer) client() *http.Client {
	if i.HTTP != nil {
		return i.HTTP
	}
	return http.DefaultClient
}
