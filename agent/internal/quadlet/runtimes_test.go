package quadlet

import (
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

func TestPackRuntimes(t *testing.T) {
	t.Cleanup(func() { SetPackRuntimes(nil) })
	golang := RuntimeSpec{Name: "golang", Label: "Go", Kind: KindApp, Pack: "golang 1.0.0"}
	ruby := RuntimeSpec{Name: "ruby", Label: "Ruby", Kind: KindApp, Pack: "ruby 2.0.0"}
	if err := SetPackRuntimes([]RuntimeSpec{golang, ruby}); err != nil {
		t.Fatal(err)
	}
	if !IsApp("golang") || IsPHP("golang") || IsCustom("golang") {
		t.Error("golang is not an application runtime")
	}
	if n := len(Runtimes()); n != len(builtinRuntimes)+2 {
		t.Errorf("%d runtimes", n)
	}

	// A pack gives only the kind "app", and no pack takes the name of a different runtime. A
	// refused list changes nothing.
	for name, list := range map[string][]RuntimeSpec{
		"kind php":    {{Name: "php9", Label: "PHP 9", Kind: KindPHP}},
		"kind custom": {{Name: "mine", Label: "Mine", Kind: KindCustom}},
		"built in":    {{Name: "node", Label: "Node", Kind: KindApp}},
		"no label":    {{Name: "zig", Kind: KindApp}},
		"two times":   {golang, {Name: "golang", Label: "Go 2", Kind: KindApp}},
	} {
		if err := SetPackRuntimes(list); err == nil {
			t.Errorf("%s: the list is accepted", name)
		}
		if !IsApp("ruby") {
			t.Errorf("%s: the runtimes changed after a refused list", name)
		}
	}

	// No packs: only the built-in runtimes.
	if err := SetPackRuntimes(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := RuntimeOf("golang"); ok || len(Runtimes()) != len(builtinRuntimes) {
		t.Error("the runtimes of packs stay after an empty list")
	}
}
