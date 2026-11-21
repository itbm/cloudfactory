package packs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// PolicyEntry is one rule of the Podman signature policy for the images of a pack: Podman
// pulls an image of Scope only with a signature of the key in KeyPath.
type PolicyEntry struct {
	Scope   string
	KeyPath string
	Pack    string
}

// Entries gives the rules for the images of the signed packs. dir is the directory of the
// packs on the server: the key of each pack is the file KeyFile in its directory.
func Entries(list []Pack, dir string) []PolicyEntry {
	var out []PolicyEntry
	seen := map[string]bool{}
	for _, p := range list {
		if !p.Signed() {
			continue
		}
		for _, img := range p.Manifest.Images {
			repo := img.Repository()
			if seen[repo] {
				continue
			}
			seen[repo] = true
			out = append(out, PolicyEntry{Scope: repo, KeyPath: filepath.Join(dir, p.Manifest.Slug, KeyFile), Pack: p.Manifest.Slug})
		}
	}
	slices.SortFunc(out, func(a, b PolicyEntry) int { return strings.Compare(a.Scope, b.Scope) })
	return out
}

// Policy gives the new content of the Podman signature policy (/etc/containers/policy.json).
//
// The rules of the installer stay as they are. The rules of packs are the rules whose key is
// in dir: Policy removes them, then adds entries. A rule of the installer that asks for a
// signature, for example the rule of the release registry, covers each repository below it.
// Policy adds no rule for such a repository, so that a pack can never make the rule of the
// installer weaker. The image of such a pack then needs the signature that the installer
// asks for. Skipped gives these repositories.
//
// A rule of the installer that accepts each image (--allow-unsigned) is weaker than a rule of
// a pack, so a pack gets its own rule below it.
func Policy(current []byte, dir string, entries []PolicyEntry) (out []byte, skipped []string, err error) {
	var policy map[string]any
	if err := json.Unmarshal(current, &policy); err != nil || policy == nil {
		return nil, nil, errors.New("the image policy is not a JSON object")
	}
	transports, _ := policy["transports"].(map[string]any)
	if transports == nil {
		transports = map[string]any{}
		policy["transports"] = transports
	}
	docker, _ := transports["docker"].(map[string]any)
	if docker == nil {
		docker = map[string]any{}
		transports["docker"] = docker
	}
	for scope, reqs := range docker {
		if ofPack(reqs, dir) {
			delete(docker, scope)
		}
	}
	// The rules of the installer, after the rules of packs left.
	installer := map[string]any{}
	for scope, reqs := range docker {
		installer[scope] = reqs
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.KeyPath, filepath.Clean(dir)+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("the key of the pack %s is not in %s", e.Pack, dir)
		}
		if covered(installer, e.Scope) {
			skipped = append(skipped, e.Scope)
			continue
		}
		docker[e.Scope] = []any{map[string]any{
			"type":           "sigstoreSigned",
			"keyPath":        e.KeyPath,
			"signedIdentity": map[string]any{"type": "matchRepository"},
		}}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(policy); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), skipped, nil
}

// ofPack reports whether the requirements of a scope are a rule of a pack: a signature with a
// key in dir.
func ofPack(reqs any, dir string) bool {
	list, _ := reqs.([]any)
	for _, r := range list {
		m, _ := r.(map[string]any)
		if path, _ := m["keyPath"].(string); path != "" && strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// covered reports whether a rule of the installer covers the repository and asks for more than
// "accept each image". A rule with the same scope always covers it, so that Policy never
// replaces a rule of the installer.
func covered(installer map[string]any, repo string) bool {
	host, _, _ := strings.Cut(repo, "/")
	for scope, reqs := range installer {
		if scope == repo {
			return true
		}
		match := strings.HasPrefix(repo, scope+"/") ||
			// A wildcard scope "*.example.com" is for each host name below example.com.
			(strings.HasPrefix(scope, "*.") && strings.HasSuffix(host, scope[1:]))
		if match && !acceptsAll(reqs) {
			return true
		}
	}
	return false
}

func acceptsAll(reqs any) bool {
	list, _ := reqs.([]any)
	if len(list) == 0 {
		return false
	}
	for _, r := range list {
		m, _ := r.(map[string]any)
		if m["type"] != "insecureAcceptAnything" {
			return false
		}
	}
	return true
}

// Registries gives the content of the file of the agent in /etc/containers/registries.d. It
// tells Podman to look for the signatures of each image of a pack next to the image, where
// "cosign sign" puts them. It gives "" when no pack has a rule.
func Registries(entries []PolicyEntry, skipped []string) string {
	var b strings.Builder
	for _, e := range entries {
		if slices.Contains(skipped, e.Scope) {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("# Managed by the Cloudfactory agent. The agent replaces changes to this file.\n")
			b.WriteString("# Podman finds the signatures of the images of the packs next to each image.\n")
			b.WriteString("docker:\n")
		}
		fmt.Fprintf(&b, "  %q:\n    use-sigstore-attachments: true\n", e.Scope)
	}
	return b.String()
}
