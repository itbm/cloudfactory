package sitefiles

import (
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestChangeOfMode(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "public/css"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "public/index.php"), []byte("<?php"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "public/css/site.css"), []byte("body{}"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "deploy.sh"), []byte("#!/bin/sh"), 0o600))

	resp, err := Do(dir, Request{Op: OpChmod, Path: "/deploy.sh", Mode: "750"})
	if err != nil || resp.Entry.Mode != 0o750 || resp.Changed != 1 || mode(t, filepath.Join(dir, "deploy.sh")) != 0o750 {
		t.Fatalf("chmod = %+v, %v", resp, err)
	}
	// A recursive change: one mode for the directories and one for the files.
	resp, err = Do(dir, Request{Op: OpChmod, Path: "/public", Mode: "0755", Recursive: true, FileMode: "0644"})
	if err != nil || resp.Changed != 4 || resp.Entry.Mode != 0o755 {
		t.Fatalf("recursive chmod = %+v, %v", resp, err)
	}
	for p, want := range map[string]os.FileMode{"public": 0o755, "public/css": 0o755, "public/index.php": 0o644, "public/css/site.css": 0o644, "deploy.sh": 0o750} {
		if got := mode(t, filepath.Join(dir, p)); got != want {
			t.Errorf("mode of %s = %o; want %o", p, got, want)
		}
	}
	// The directory of the website.
	if _, err := Do(dir, Request{Op: OpChmod, Path: "/", Mode: "0700"}); err != nil || mode(t, dir) != 0o700 {
		t.Fatalf("chmod of / = %v, %o", err, mode(t, dir))
	}

	for _, c := range []struct {
		name string
		req  Request
		code string
	}{
		{"setuid", Request{Path: "/deploy.sh", Mode: "4755"}, CodeInvalid},
		{"not octal", Request{Path: "/deploy.sh", Mode: "rwxr-xr-x"}, CodeInvalid},
		{"no mode", Request{Path: "/deploy.sh"}, CodeInvalid},
		{"directory with no search permission", Request{Path: "/public", Mode: "0644"}, CodeInvalid},
		{"recursive with a directory mode that locks the owner out", Request{Path: "/public", Mode: "0300", Recursive: true, FileMode: "0644"}, CodeInvalid},
		{"recursive with no file mode", Request{Path: "/public", Mode: "0755", Recursive: true}, CodeInvalid},
		{"recursive on a file", Request{Path: "/deploy.sh", Mode: "0755", Recursive: true, FileMode: "0644"}, CodeInvalid},
		{"no file", Request{Path: "/none", Mode: "0644"}, CodeNotFound},
	} {
		c.req.Op = OpChmod
		if _, err := Do(dir, c.req); code(err) != c.code {
			t.Errorf("%s: error = %v; want the code %q", c.name, err, c.code)
		}
	}
	if got := mode(t, filepath.Join(dir, "public")); got != 0o755 {
		t.Errorf("a refused change changed the mode to %o", got)
	}
}

// A change of mode does not change a link or go through a link, also in a recursive change.
func TestChangeOfModeAndLinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	must(t, os.MkdirAll(filepath.Join(dir, "public"), 0o750))
	secret := filepath.Join(base, "secret")
	must(t, os.WriteFile(secret, []byte("other website"), 0o600))
	must(t, os.Mkdir(filepath.Join(base, "other"), 0o700))
	must(t, os.Symlink(secret, filepath.Join(dir, "public/link")))
	must(t, os.Symlink(filepath.Join(base, "other"), filepath.Join(dir, "public/dir-link")))

	if _, err := Do(dir, Request{Op: OpChmod, Path: "/public/link", Mode: "0666"}); code(err) != CodeInvalid {
		t.Errorf("chmod of a link = %v", err)
	}
	if _, err := Do(dir, Request{Op: OpChmod, Path: "/public/dir-link", Mode: "0777"}); code(err) != CodeInvalid {
		t.Errorf("chmod of a link to a directory = %v", err)
	}
	resp, err := Do(dir, Request{Op: OpChmod, Path: "/public", Mode: "0777", Recursive: true, FileMode: "0666"})
	if err != nil || resp.Changed != 1 {
		t.Fatalf("recursive chmod = %+v, %v", resp, err)
	}
	if mode(t, secret) != 0o600 || mode(t, filepath.Join(base, "other")) != 0o700 {
		t.Errorf("the change went through a link: %o, %o", mode(t, secret), mode(t, filepath.Join(base, "other")))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
