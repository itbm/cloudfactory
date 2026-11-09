// Package images keeps one read-only copy of each runtime image on the server. All website
// users share it, so a server with 100 websites keeps one copy of each image, not 100, and a
// new website starts without a pull (NFR-03).
//
// Each image has its own small Podman store in Root, named by the SHA-256 of the image
// reference. Only root writes a store. The agent pulls the image into a temporary store, so
// that Podman's signature policy checks it (FR-1101). Then the agent lets the website users
// read the metadata of the store and moves the store to its final name. After that, nothing
// writes the store again. Podman writes its metadata files with mode 0600 each time that it
// changes them, so a store that Podman changes while the websites read it would stop the
// websites for a short time. A store that never changes does not have this problem.
//
// Each website user reads the stores through Config, which lists them as additional image
// stores. A website user keeps only its own writable container layer. Podman uses
// fuse-overlayfs for the website users, because the containers use --userns=keep-id. With the
// overlay driver of the kernel, Podman must copy each image to change the file owners for
// keep-id, and it cannot copy a store that it cannot write.
package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
)

const (
	// DefaultRoot holds one store for each image. Root owns it, and only the group of the
	// website users can go into it (mode 0750).
	DefaultRoot = "/var/lib/cloudfactory-images"
	// DefaultConfig is the Podman storage configuration of the website users. The
	// ~/.config/containers/storage.conf of each website user is a link to it.
	DefaultConfig = "/etc/cloudfactory/website-storage.conf"
	// DefaultRunRoot holds the temporary run directories of the pulls.
	DefaultRunRoot = "/run/cloudfactory-images"
	// FuseOverlayfs is the mount program of the website users. The installer installs it.
	FuseOverlayfs = "/usr/bin/fuse-overlayfs"
	// KeepRecent is how long the agent keeps an image that a job asked for, also when no
	// website uses it yet. A job records the image of a website only after its health checks.
	KeepRecent = time.Hour
)

var (
	digestRE    = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
	storeNameRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Shareable reports whether an image reference can go in the shared store. Only a reference
// with a digest can: the content of a tag can change.
func Shareable(ref string) bool { return digestRE.MatchString(ref) }

// StoreName is the directory name of the store of an image reference.
func StoreName(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(sum[:])
}

// Store is the shared image store of a server.
type Store struct {
	Run     execx.Runner
	Root    string
	Config  string
	RunRoot string
	// Group can go into Root. It is the group of all website users.
	Group string
	// MountProgram is FuseOverlayfs. Tests can change it.
	MountProgram string
	// Now gives the time. Tests replace it.
	Now func() time.Time

	// mu protects recent and Config. pullMu lets one pull operate at a time.
	mu     sync.Mutex
	pullMu sync.Mutex
	recent map[string]time.Time
}

// New gives a Store with the standard paths.
func New(run execx.Runner, group string) *Store {
	return &Store{Run: run, Root: DefaultRoot, Config: DefaultConfig, RunRoot: DefaultRunRoot, Group: group, MountProgram: FuseOverlayfs}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) path(name string) string { return filepath.Join(s.Root, name) }

// Init makes Root, removes the pulls that did not finish, and writes Config. The agent calls
// it at start, before a website starts.
func (s *Store) Init(ctx context.Context) error {
	if err := os.MkdirAll(s.Root, 0o750); err != nil {
		return err
	}
	if _, err := s.Run.Run(ctx, "chown", "root:"+s.Group, s.Root); err != nil {
		return err
	}
	if err := os.Chmod(s.Root, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	s.pullMu.Lock()
	defer s.pullMu.Unlock()
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pull-") {
			if err := os.RemoveAll(s.path(e.Name())); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	listed, err := s.listed()
	if err != nil {
		return err
	}
	return s.writeConfig(listed)
}

// Ensure makes sure that the shared store has the image. It pulls the image only when the
// store does not have it. Podman checks the signature of the image when it pulls it.
func (s *Store) Ensure(ctx context.Context, ref string) error {
	if !Shareable(ref) {
		return fmt.Errorf("images: %q has no digest", ref)
	}
	name := StoreName(ref)
	s.mu.Lock()
	if s.recent == nil {
		s.recent = map[string]time.Time{}
	}
	s.recent[name] = s.now()
	s.mu.Unlock()

	s.pullMu.Lock()
	defer s.pullMu.Unlock()
	if !isDir(s.path(name)) {
		if err := s.pull(ctx, ref, name); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	listed, err := s.listed()
	if err != nil {
		return err
	}
	if slices.Contains(listed, name) {
		return nil
	}
	return s.writeConfig(append(listed, name))
}

func (s *Store) pull(ctx context.Context, ref, name string) error {
	tmp := s.path(".pull-" + name)
	run := filepath.Join(s.RunRoot, name)
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(run)
	conf := filepath.Join(run, "storage.conf")
	// Only the options of this file apply, not the ones in /etc/containers/storage.conf.
	if err := os.WriteFile(conf, []byte(fmt.Sprintf("[storage]\ndriver = \"overlay\"\ngraphroot = %q\nrunroot = %q\n", tmp, filepath.Join(run, "run"))), 0o600); err != nil {
		return err
	}
	// The pull runs as root with /etc/containers/policy.json, which refuses an image with no
	// valid signature (FR-1101).
	if _, err := s.Run.RunEnv(ctx, []string{"CONTAINERS_STORAGE_CONF=" + conf}, "", "podman", "pull", "--quiet", ref); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := ShareMetadata(tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	// The final name appears in one step, so a store with that name is always complete.
	return os.Rename(tmp, s.path(name))
}

// ShareMetadata lets the website users read the metadata of a store: the lists of images and
// layers, and the directories that lead to the layers. It does not change the files of the
// layers themselves, which keep the owners and modes of the image. It does not open the
// directories of Podman's own state (libpod, volumes, overlay-containers) or the tar-split
// files, which the website users do not need.
func ShareMetadata(store string) error {
	var errs []error
	chmod := func(path string, mode os.FileMode, dir bool) {
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err != nil {
			errs = append(errs, err)
			return
		}
		// Never follow a link (R-16). The links in overlay/l stay as they are.
		if fi.IsDir() != dir || fi.Mode()&fs.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("images: %s is not a regular %s", path, map[bool]string{true: "directory", false: "file"}[dir]))
			return
		}
		errs = append(errs, os.Chmod(path, mode))
	}
	glob := func(pattern string) []string {
		m, err := filepath.Glob(filepath.Join(store, pattern))
		errs = append(errs, err)
		return m
	}
	for _, d := range []string{"", "overlay", "overlay/l", "overlay-images", "overlay-layers"} {
		chmod(filepath.Join(store, d), 0o755, true)
	}
	for _, f := range glob("overlay-images/*.json") {
		chmod(f, 0o644, false)
	}
	for _, f := range glob("overlay-layers/*.json") {
		chmod(f, 0o644, false)
	}
	for _, d := range glob("overlay-images/*") {
		if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
			chmod(d, 0o755, true)
			entries, err := os.ReadDir(d)
			errs = append(errs, err)
			for _, e := range entries {
				chmod(filepath.Join(d, e.Name()), 0o644, false)
			}
		}
	}
	for _, d := range glob("overlay/*") {
		if base := filepath.Base(d); base == "l" || strings.HasPrefix(base, ".") {
			continue
		}
		if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
			chmod(d, 0o755, true)
			chmod(filepath.Join(d, "link"), 0o644, false)
			chmod(filepath.Join(d, "lower"), 0o644, false)
		}
	}
	return errors.Join(errs...)
}

// Prune removes the images that no website uses. inUse has the image reference of each
// website on the server. The agent keeps an image that a job asked for in the last
// KeepRecent, because the job has not recorded it yet.
//
// A store goes in two steps. The first prune removes it from Config. The next prune removes
// its files. So a Podman command of a website user that read Config before the change still
// finds the store.
func (s *Store) Prune(inUse []string) error {
	keep := map[string]bool{}
	for _, ref := range inUse {
		if Shareable(ref) {
			keep[StoreName(ref)] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, t := range s.recent {
		if s.now().Sub(t) < KeepRecent {
			keep[name] = true
		} else {
			delete(s.recent, name)
		}
	}
	listed, err := s.listed()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	var list, remove []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case !e.IsDir() || !storeNameRE.MatchString(name):
		case keep[name]:
			// This also lists again a store that a pull made before the agent stopped.
			list = append(list, name)
		case slices.Contains(listed, name):
			slog.Info("stop sharing an unused image", "store", s.path(name))
		default:
			remove = append(remove, name)
		}
	}
	if err := s.writeConfig(list); err != nil {
		return err
	}
	var errs []error
	for _, name := range remove {
		slog.Info("remove an unused image", "store", s.path(name))
		errs = append(errs, os.RemoveAll(s.path(name)))
	}
	return errors.Join(errs...)
}

var configStoreRE = regexp.MustCompile(`"([^"]+)"`)

// listed gives the names of the stores in Config that exist. The caller holds mu.
func (s *Store) listed() ([]string, error) {
	b, err := os.ReadFile(s.Config)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	inList := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "additionalimagestores"):
			inList = true
		case inList && line == "]":
			inList = false
		case inList:
			for _, m := range configStoreRE.FindAllStringSubmatch(line, -1) {
				dir, name := filepath.Split(m[1])
				if filepath.Clean(dir) == filepath.Clean(s.Root) && storeNameRE.MatchString(name) && isDir(s.path(name)) {
					names = append(names, name)
				}
			}
		}
	}
	return names, nil
}

// writeConfig writes Config with the stores in names. The caller holds mu.
func (s *Store) writeConfig(names []string) error {
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	var b strings.Builder
	b.WriteString("# Written by cf-agent. Do not change it: the agent writes it again.\n")
	b.WriteString("# The Podman storage of each website user. ~/.config/containers/storage.conf of\n")
	b.WriteString("# each website user is a link to this file.\n")
	b.WriteString("[storage]\ndriver = \"overlay\"\n\n[storage.options]\n")
	b.WriteString("# The shared read-only images. Only root can write them.\n")
	b.WriteString("additionalimagestores = [\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %q,\n", s.path(n))
	}
	b.WriteString("]\n\n[storage.options.overlay]\n")
	b.WriteString("# fuse-overlayfs changes the file owners for --userns=keep-id without a copy of the image.\n")
	fmt.Fprintf(&b, "mount_program = %q\n", s.MountProgram)
	_, err := host.WriteFile(s.Config, b.String(), 0o644)
	return err
}

func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}
