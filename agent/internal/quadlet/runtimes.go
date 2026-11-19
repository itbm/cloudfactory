package quadlet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// RuntimeKind is the contract between the agent and the image of a runtime.
type RuntimeKind string

const (
	// KindPHP is a PHP runtime. The image reads CF_DOCUMENT_ROOT, and the agent mounts a
	// php.ini file.
	KindPHP RuntimeKind = "php"
	// KindApp is an application runtime. The image has /usr/local/bin/cf-app, which starts
	// the start command of the customer in CF_APP_DIR. The application listens on PORT.
	KindApp RuntimeKind = "app"
	// KindCustom is the image of the customer or of a plugin, with its own port and command.
	KindCustom RuntimeKind = "custom"
)

// RuntimeSpec is what the agent needs to know about one website runtime. The image of the
// runtime does the rest. A runtime of the kind "app" can come from a manifest file
// (LoadRuntimeManifests), with no new agent code.
type RuntimeSpec struct {
	// Name is the name of the runtime in the requests of the panel, for example "node".
	Name string `json:"name"`
	// Label is the name for people, for example "Node.js".
	Label string `json:"label"`
	// Kind is the contract of the image.
	Kind RuntimeKind `json:"kind"`
	// FrankenPHPWorkerMode is true when the runtime can run in FrankenPHP worker mode.
	FrankenPHPWorkerMode bool `json:"-"`
	// Manifest is the manifest file of a runtime that is not built in.
	Manifest string `json:"-"`
}

// PHP reports whether the runtime is a PHP runtime with a php.ini file.
func (r RuntimeSpec) PHP() bool { return r.Kind == KindPHP }

// App reports whether the runtime runs the start command of the customer through cf-app.
func (r RuntimeSpec) App() bool { return r.Kind == KindApp }

// Custom reports whether the runtime is an image of the customer or of a plugin.
func (r RuntimeSpec) Custom() bool { return r.Kind == KindCustom }

// builtinRuntimes are the runtimes of this agent version.
var builtinRuntimes = []RuntimeSpec{
	{Name: "frankenphp", Label: "FrankenPHP", Kind: KindPHP, FrankenPHPWorkerMode: true},
	{Name: "apache-php-fpm", Label: "Apache + PHP-FPM", Kind: KindPHP},
	{Name: "node", Label: "Node.js", Kind: KindApp},
	{Name: "python", Label: "Python", Kind: KindApp},
	{Name: "custom", Label: "Custom image", Kind: KindCustom},
}

var (
	runtimesMu sync.RWMutex
	// extraRuntimes are the runtimes from manifest files. They come after the built-in
	// runtimes, and cannot replace one.
	extraRuntimes []RuntimeSpec
)

// Runtimes gives the built-in runtimes, then the runtimes from manifest files.
func Runtimes() []RuntimeSpec {
	runtimesMu.RLock()
	defer runtimesMu.RUnlock()
	return slices.Concat(builtinRuntimes, extraRuntimes)
}

// RuntimeOf gives the RuntimeSpec of a runtime.
func RuntimeOf(name string) (RuntimeSpec, bool) {
	for _, r := range Runtimes() {
		if r.Name == name {
			return r, true
		}
	}
	return RuntimeSpec{}, false
}

// IsApp reports whether the runtime runs an application of the customer with its start
// command: Node.js, Python, or a runtime of the kind "app" from a manifest.
func IsApp(runtime string) bool {
	r, _ := RuntimeOf(runtime)
	return r.App()
}

// IsPHP reports whether the runtime is a PHP runtime with a php.ini file.
func IsPHP(runtime string) bool {
	r, _ := RuntimeOf(runtime)
	return r.PHP()
}

// IsCustom reports whether the runtime is an image of the customer or of a plugin.
func IsCustom(runtime string) bool {
	r, _ := RuntimeOf(runtime)
	return r.Custom()
}

var runtimeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

// checkRuntimeManifest checks a runtime from a manifest file. Only the kind "app" is
// possible: the agent runs it as it runs Node.js and Python, with the same isolation. A
// manifest cannot change the unit in other ways.
func checkRuntimeManifest(r RuntimeSpec, have []RuntimeSpec) error {
	if !runtimeNameRE.MatchString(r.Name) {
		return fmt.Errorf("the runtime name %q is not valid", r.Name)
	}
	if r.Label == "" || len(r.Label) > 60 || strings.ContainsAny(r.Label, "\n\r\x00") {
		return fmt.Errorf("the runtime %q needs a label of 1 to 60 characters", r.Name)
	}
	if r.Kind != KindApp {
		return fmt.Errorf("the runtime %q has the kind %q; a manifest can give only the kind %q", r.Name, r.Kind, KindApp)
	}
	for _, h := range have {
		if h.Name == r.Name {
			return fmt.Errorf("the runtime %q is already a runtime", r.Name)
		}
	}
	return nil
}

// LoadRuntimeManifests reads the runtime manifests (*.json) in dir, and replaces the runtimes
// from manifest files with them. An empty dir removes them. The agent reads the directory
// only when its settings name it (config.RuntimeManifests), so a server with no setting has
// only the built-in runtimes.
//
// A manifest is one JSON object, for example:
//
//	{"name": "golang", "label": "Go", "kind": "app"}
//
// A manifest that is not valid stops the load, and no runtime from a manifest changes.
func LoadRuntimeManifests(dir string) ([]RuntimeSpec, error) {
	var loaded []RuntimeSpec
	if dir != "" {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			return nil, err
		}
		slices.Sort(files)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			var r RuntimeSpec
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if dec.More() {
				return nil, fmt.Errorf("%s: %w", f, errors.New("the file has more than one object"))
			}
			if err := checkRuntimeManifest(r, slices.Concat(builtinRuntimes, loaded)); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			r.Manifest = f
			loaded = append(loaded, r)
		}
	}
	runtimesMu.Lock()
	extraRuntimes = loaded
	runtimesMu.Unlock()
	return loaded, nil
}
