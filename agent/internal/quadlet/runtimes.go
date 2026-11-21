package quadlet

import (
	"errors"
	"fmt"
	"slices"
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
// runtime does the rest. A runtime of the kind "app" can come from a pack (SetPackRuntimes),
// with no new agent code.
type RuntimeSpec struct {
	// Name is the name of the runtime in the requests of the panel, for example "node".
	Name string `json:"name"`
	// Label is the name for people, for example "Node.js".
	Label string `json:"label"`
	// Kind is the contract of the image.
	Kind RuntimeKind `json:"kind"`
	// FrankenPHPWorkerMode is true when the runtime can run in FrankenPHP worker mode.
	FrankenPHPWorkerMode bool `json:"-"`
	// Pack is the name and the version of the pack of a runtime that is not built in, for
	// example "golang 1.0.0".
	Pack string `json:"-"`
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
	// extraRuntimes are the runtimes of packs. They come after the built-in runtimes, and
	// cannot replace one.
	extraRuntimes []RuntimeSpec
)

// Runtimes gives the built-in runtimes, then the runtimes of packs.
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
// command: Node.js, Python, or the runtime of a pack.
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

// SetPackRuntimes replaces the runtimes of packs. A pack gives only the kind "app": the agent
// runs it as it runs Node.js and Python, with the same isolation, and a pack cannot change the
// unit in other ways. The package packs checks the manifests; this function checks again that
// no pack takes the name of a built-in runtime or of a different pack. An error changes nothing.
func SetPackRuntimes(list []RuntimeSpec) error {
	have := slices.Clone(builtinRuntimes)
	for _, r := range list {
		if r.Kind != KindApp {
			return fmt.Errorf("the runtime %q has the kind %q; a pack can give only the kind %q", r.Name, r.Kind, KindApp)
		}
		if r.Name == "" || r.Label == "" {
			return errors.New("a runtime of a pack needs a name and a label")
		}
		for _, h := range have {
			if h.Name == r.Name {
				return fmt.Errorf("the runtime %q is already a runtime", r.Name)
			}
		}
		have = append(have, r)
	}
	runtimesMu.Lock()
	extraRuntimes = slices.Clone(list)
	runtimesMu.Unlock()
	return nil
}
