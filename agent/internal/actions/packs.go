package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/packs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The packs of the server (docs/packs.md). The panel sends all its packs with SetPacks. The
// worker checks each pack, and the root helper checks each pack again before it saves the
// packs and changes the image policy of Podman. The worker then loads the runtimes of the
// packs. A pack never gives a program, a path or a command to the agent: only data that the
// agent checks.

const (
	// DefaultPacksDir holds one directory for each pack, with its manifest, its signature and
	// the key of its publisher. Root owns it. Each user can read it: the files are public.
	DefaultPacksDir = "/etc/cloudfactory/packs"
	// DefaultImagePolicy is the signature policy of Podman. The installer writes it. The
	// agent adds and removes only the rules of packs.
	DefaultImagePolicy = "/etc/containers/policy.json"
	// DefaultPackRegistries tells Podman where the signatures of the images of the packs are.
	DefaultPackRegistries = "/etc/containers/registries.d/cloudfactory-packs.yaml"
)

const opPacksSet = "packs.set"

// packIn is one pack from the worker for the root helper. The helper checks it again.
type packIn struct {
	Manifest, Signature, PublicKey string
}

type packsIn struct {
	Packs []packIn
}

func (d *Deps) packsDir() string {
	if d.PacksDir != "" {
		return d.PacksDir
	}
	return DefaultPacksDir
}

func (d *Deps) imagePolicy() string {
	if d.ImagePolicy != "" {
		return d.ImagePolicy
	}
	return DefaultImagePolicy
}

func (d *Deps) packRegistries() string {
	if d.PackRegistries != "" {
		return d.PackRegistries
	}
	return DefaultPackRegistries
}

// LoadPacks reads the packs of the server and loads their runtimes. The agent calls it at
// start. A pack that is not valid does not stop the agent: the agent writes it in its log,
// and the other packs load.
func (d *Deps) LoadPacks() error {
	good, bad, err := packs.Load(d.packsDir())
	for name, e := range bad {
		slog.Error("a pack of the server is not valid; its runtime is not available", "pack", name, "err", e)
	}
	if err != nil {
		return err
	}
	return d.usePacks(good)
}

// usePacks loads the runtimes and the database engines of the packs, and keeps the packs for
// the report.
func (d *Deps) usePacks(list []packs.Pack) error {
	if err := quadlet.SetPackRuntimes(runtimesOf(list)); err != nil {
		return err
	}
	if err := quadlet.SetPackEngines(packEngines(list)); err != nil {
		return err
	}
	d.packsMu.Lock()
	d.packs = slices.Clone(list)
	d.packsMu.Unlock()
	for _, p := range list {
		if p.Manifest.IsDatabase() {
			logPackEngine(p)
			continue
		}
		if p.Manifest.PHP() {
			slog.Info("PHP versions of a pack", "pack", p.Manifest.Slug, "version", p.Manifest.Version, "runtime", p.Manifest.Runtime.Family, "signed", p.Signed())
			continue
		}
		slog.Info("runtime of a pack", "pack", p.Manifest.Slug, "version", p.Manifest.Version, "signed", p.Signed())
	}
	return nil
}

// runtimesOf gives the runtimes of the packs. A PHP pack (cf-php/1) adds no runtime: its
// images are versions of a built-in PHP runtime, and the agent runs them as that runtime. A
// database pack adds an engine (packEngines), not a runtime.
func runtimesOf(list []packs.Pack) []quadlet.RuntimeSpec {
	out := make([]quadlet.RuntimeSpec, 0, len(list))
	for _, p := range list {
		if p.Manifest.PHP() || p.Manifest.IsDatabase() {
			continue
		}
		out = append(out, quadlet.RuntimeSpec{Name: p.Manifest.Slug, Label: p.Manifest.Name, Kind: quadlet.KindApp, Pack: p.Manifest.Slug + " " + p.Manifest.Version})
	}
	return out
}

// PackStates gives the name and the version of each pack of the server, for the report.
func (d *Deps) PackStates() []panel.PackState {
	d.packsMu.Lock()
	defer d.packsMu.Unlock()
	out := make([]panel.PackState, 0, len(d.packs))
	for _, p := range d.packs {
		out = append(out, panel.PackState{Slug: p.Manifest.Slug, Version: p.Manifest.Version})
	}
	return out
}

// checkPacks checks the packs of a request: each manifest and each signature, and the list
// together.
func checkPacks(in []packIn) ([]packs.Pack, error) {
	if len(in) > validate.MaxPacks {
		return nil, fmt.Errorf("a server can have %d packs, not %d", validate.MaxPacks, len(in))
	}
	list := make([]packs.Pack, 0, len(in))
	for _, p := range in {
		pack, err := packs.Check([]byte(p.Manifest), p.Signature, p.PublicKey)
		if err != nil {
			return nil, err
		}
		list = append(list, pack)
	}
	return list, packs.CheckSet(list)
}

// SetPacks sets the packs of the server: all packs that the panel has installed. A pack that
// the server has and that is not in the request goes, except when a website of the server
// uses its runtime.
func (d *Deps) SetPacks(req *agentv1.SetPacksRequest) (jobs.Job, error) {
	if err := validate.ID(req.GetJobId()); err != nil {
		return jobs.Job{}, err
	}
	if len(req.GetPacks()) > validate.MaxPacks {
		return jobs.Job{}, fmt.Errorf("a server can have %d packs, not %d", validate.MaxPacks, len(req.GetPacks()))
	}
	in := make([]packIn, 0, len(req.GetPacks()))
	for _, p := range req.GetPacks() {
		in = append(in, packIn{Manifest: p.GetManifest(), Signature: p.GetSignature(), PublicKey: p.GetPublicKey()})
	}
	var list []packs.Pack
	return jobs.Job{ID: req.GetJobId(), Key: "packs", Steps: []jobs.Step{
		{Name: "Check the packs", Do: func(ctx context.Context) error {
			var err error
			if list, err = checkPacks(in); err != nil {
				return err
			}
			return d.checkPackUse(list)
		}},
		{Name: "Save the packs and the image policy", Do: func(ctx context.Context) error {
			return d.savePacks(ctx, in)
		}},
		{Name: "Load the runtimes and the database engines", Do: func(ctx context.Context) error {
			return d.usePacks(list)
		}},
	}, Result: func() map[string]any {
		// The panel puts a website of a pack only on a server with its runtime. With the
		// runtimes in the result, the panel does not wait for the next report.
		names := []string{}
		for _, rt := range quadlet.Runtimes() {
			names = append(names, rt.Name)
		}
		return map[string]any{"packs": d.PackStates(), "runtimes": names, "databaseEngines": engines()}
	}}, nil
}

// checkPackUse refuses to remove a pack that a website of the server uses: the runtime of
// the pack, or an image of the pack (a PHP pack has no runtime of its own). It also refuses to
// remove a database pack that a database server of the server uses, or a version of it. The
// agent could not start the website or the database server again. The panel refuses to
// remove such a pack too.
func (d *Deps) checkPackUse(list []packs.Pack) error {
	keep := map[string]bool{}
	// versions gives the versions of each database pack of the list.
	versions := map[string][]string{}
	for _, p := range list {
		keep[p.Manifest.Slug] = true
		if p.Manifest.IsDatabase() {
			for _, img := range p.Manifest.Images {
				versions[p.Manifest.Slug] = append(versions[p.Manifest.Slug], img.Version)
			}
		}
	}
	if d.State != nil {
		instances, err := d.State.DatabaseInstances()
		if err != nil {
			return err
		}
		for _, inst := range instances {
			for id, srv := range inst.Servers {
				if !isManaged(srv.Engine) {
					continue
				}
				if !keep[srv.Engine] {
					return fmt.Errorf("the database server %s uses the engine %s, so its pack cannot go", id, srv.Engine)
				}
				if !slices.Contains(versions[srv.Engine], srv.Version) {
					return fmt.Errorf("the database server %s uses the version %s of the pack %s, so the version cannot go", id, srv.Version, srv.Engine)
				}
			}
		}
	}
	d.packsMu.Lock()
	var gone []string
	// repos gives the pack of each image repository of the packs that go.
	repos := map[string]string{}
	for _, p := range d.packs {
		if keep[p.Manifest.Slug] {
			continue
		}
		gone = append(gone, p.Manifest.Slug)
		for _, img := range p.Manifest.Images {
			repos[img.Repository()] = p.Manifest.Slug
		}
	}
	d.packsMu.Unlock()
	if len(gone) == 0 || d.State == nil {
		return nil
	}
	sites, err := d.State.All()
	if err != nil {
		return err
	}
	for _, s := range sites {
		if s.Website == nil {
			continue
		}
		if slices.Contains(gone, s.Website.GetRuntime()) {
			return fmt.Errorf("the website %s uses the runtime %s, so its pack cannot go", s.ID, s.Website.GetRuntime())
		}
		if pack, ok := repos[packs.Repository(s.Website.GetImage())]; ok {
			return fmt.Errorf("the website %s uses the image %s of the pack %s, so the pack cannot go", s.ID, packs.Repository(s.Website.GetImage()), pack)
		}
	}
	return nil
}

// savePacks saves the packs and changes the image policy. Only root can do it: the root
// helper checks each pack again.
func (d *Deps) savePacks(ctx context.Context, in []packIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opPacksSet, packsIn{Packs: in}, nil)
	}
	list, err := checkPacks(in)
	if err != nil {
		return err
	}
	if err := d.writePacks(list); err != nil {
		return err
	}
	// The root helper makes the units of the database servers of pack engines, with the
	// images of the packs that it checked.
	return d.usePacks(list)
}

// writePacks writes the files of each pack, then the image policy, then removes the packs
// that are not in the list. The policy never names a key file that is not there.
func (d *Deps) writePacks(list []packs.Pack) error {
	d.packsMu.Lock()
	defer d.packsMu.Unlock()
	dir := d.packsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, p := range list {
		slug := p.Manifest.Slug
		keep[slug] = true
		pd := filepath.Join(dir, slug)
		if err := os.MkdirAll(pd, 0o755); err != nil {
			return err
		}
		if _, err := host.WriteFile(filepath.Join(pd, packs.ManifestFile), string(p.Raw), 0o644); err != nil {
			return err
		}
		for name, content := range map[string]string{packs.SignatureFile: p.Signature + "\n", packs.KeyFile: p.PublicKey} {
			path := filepath.Join(pd, name)
			if !p.Signed() {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				continue
			}
			if _, err := host.WriteFile(path, content, 0o644); err != nil {
				return err
			}
		}
	}

	current, err := os.ReadFile(d.imagePolicy())
	if err != nil {
		return fmt.Errorf("read the image policy of Podman: %w", err)
	}
	entries := packs.Entries(list, dir)
	policy, skipped, err := packs.Policy(current, dir, entries)
	if err != nil {
		return err
	}
	for _, repo := range skipped {
		slog.Info("a rule of the installer covers the image of a pack; the image needs the signature of that rule", "image", repo)
	}
	if _, err := host.WriteFile(d.imagePolicy(), string(policy), 0o644); err != nil {
		return err
	}
	if registries := packs.Registries(entries, skipped); registries == "" {
		if err := os.Remove(d.packRegistries()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if _, err := host.WriteFile(d.packRegistries(), registries, 0o644); err != nil {
		return err
	}

	entriesOnDisk, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entriesOnDisk {
		if keep[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
