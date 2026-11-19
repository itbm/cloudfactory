package quadlet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The table gives the same answers as the checks before the table.
func TestTheBuiltInRuntimes(t *testing.T) {
	for _, c := range []struct {
		name              string
		php, app, custom  bool
		frankenWorkerMode bool
	}{
		{"frankenphp", true, false, false, true},
		{"apache-php-fpm", true, false, false, false},
		{"node", false, true, false, false},
		{"python", false, true, false, false},
		{"custom", false, false, true, false},
		{"golang", false, false, false, false},
		{"", false, false, false, false},
	} {
		spec, _ := RuntimeOf(c.name)
		if IsPHP(c.name) != c.php || IsApp(c.name) != c.app || IsCustom(c.name) != c.custom || spec.FrankenPHPWorkerMode != c.frankenWorkerMode {
			t.Errorf("%q: php %v, app %v, custom %v, worker mode %v", c.name, IsPHP(c.name), IsApp(c.name), IsCustom(c.name), spec.FrankenPHPWorkerMode)
		}
	}
}

func writeManifests(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRuntimeManifests(t *testing.T) {
	t.Cleanup(func() { LoadRuntimeManifests("") })
	dir := writeManifests(t, map[string]string{
		"golang.json": `{"name": "golang", "label": "Go", "kind": "app"}`,
		"ruby.json":   `{"name": "ruby", "label": "Ruby", "kind": "app"}`,
		"notes.txt":   `not a manifest`,
	})
	loaded, err := LoadRuntimeManifests(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Name != "golang" || loaded[1].Name != "ruby" {
		t.Fatalf("loaded %+v", loaded)
	}
	if !IsApp("golang") || IsPHP("golang") || IsCustom("golang") {
		t.Error("golang is not an application runtime")
	}
	if n := len(Runtimes()); n != len(builtinRuntimes)+2 {
		t.Errorf("%d runtimes", n)
	}

	// A manifest that is not valid stops the load, and the runtimes stay as they were.
	for name, content := range map[string]string{
		"kind php":      `{"name": "php9", "label": "PHP 9", "kind": "php"}`,
		"kind custom":   `{"name": "mine", "label": "Mine", "kind": "custom"}`,
		"built in":      `{"name": "node", "label": "Node", "kind": "app"}`,
		"bad name":      `{"name": "Go Lang", "label": "Go", "kind": "app"}`,
		"no label":      `{"name": "golang", "kind": "app"}`,
		"unknown field": `{"name": "golang", "label": "Go", "kind": "app", "exec": "sh"}`,
		"two objects":   `{"name": "golang", "label": "Go", "kind": "app"} {}`,
		"not JSON":      `name=golang`,
	} {
		if _, err := LoadRuntimeManifests(writeManifests(t, map[string]string{"x.json": content})); err == nil {
			t.Errorf("%s: the manifest is accepted", name)
		}
		if !IsApp("ruby") {
			t.Errorf("%s: the runtimes changed after a refused load", name)
		}
	}
	if _, err := LoadRuntimeManifests(writeManifests(t, map[string]string{
		"a.json": `{"name": "golang", "label": "Go", "kind": "app"}`,
		"b.json": `{"name": "golang", "label": "Go 2", "kind": "app"}`,
	})); err == nil || !strings.Contains(err.Error(), "already a runtime") {
		t.Errorf("two manifests with one name: %v", err)
	}

	// No directory: only the built-in runtimes.
	if _, err := LoadRuntimeManifests(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := RuntimeOf("golang"); ok || len(Runtimes()) != len(builtinRuntimes) {
		t.Error("the runtimes from manifests stay after a load with no directory")
	}
}
