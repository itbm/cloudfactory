package safefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func open(t *testing.T) (*Dir, string) {
	t.Helper()
	base := t.TempDir()
	d, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, base
}

func TestWriteFileIsAtomicAndReportsChanges(t *testing.T) {
	d, base := open(t)
	if err := d.MkdirAll("a/b", 0o750, Keep, Keep); err != nil {
		t.Fatal(err)
	}
	changed, err := d.WriteFile("a/b/file", []byte("one"), 0o640, Keep, Keep)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	changed, err = d.WriteFile("a/b/file", []byte("one"), 0o640, Keep, Keep)
	if err != nil || changed {
		t.Fatalf("same content: changed=%v err=%v", changed, err)
	}
	changed, err = d.WriteFile("a/b/file", []byte("one"), 0o600, Keep, Keep)
	if err != nil || !changed {
		t.Fatalf("new mode: changed=%v err=%v", changed, err)
	}
	fi, err := os.Stat(filepath.Join(base, "a/b/file"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", fi.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Join(base, "a/b"))
	if len(entries) != 1 {
		t.Fatalf("a temporary file stayed: %v", entries)
	}
	b, err := d.ReadFile("a/b/file")
	if err != nil || string(b) != "one" {
		t.Fatalf("read = %q, %v", b, err)
	}
}

func TestPathsCannotLeaveTheDirectory(t *testing.T) {
	d, _ := open(t)
	for _, rel := range []string{"", "/etc/passwd", "../x", "a/../../x"} {
		if _, err := d.WriteFile(rel, []byte("x"), 0o600, Keep, Keep); err == nil {
			t.Errorf("WriteFile(%q) was not refused", rel)
		}
		if err := d.RemoveAll(rel); err == nil {
			t.Errorf("RemoveAll(%q) was not refused", rel)
		}
	}
}

// A different user makes a link in its directory. Root must not follow it.
func TestALinkInThePathIsRefused(t *testing.T) { linkIsRefused(t) }

// A kernel before Linux 5.6 has no openat2. The package then opens each part of a path.
func TestALinkInThePathIsRefusedWithNoOpenat2(t *testing.T) {
	noOpenat2 = true
	defer func() { noOpenat2 = false }()
	linkIsRefused(t)
}

func linkIsRefused(t *testing.T) {
	d, base := open(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(base, "file")); err != nil {
		t.Fatal(err)
	}

	if _, err := d.WriteFile("dir/secret", []byte("changed"), 0o600, Keep, Keep); err == nil {
		t.Error("a write through a link to a directory was not refused")
	}
	if _, err := d.ReadFile("dir/secret"); err == nil {
		t.Error("a read through a link to a directory was not refused")
	}
	if _, err := d.ReadFile("file"); err == nil {
		t.Error("a read of a link was not refused")
	}
	if err := d.MkdirAll("dir/new", 0o755, Keep, Keep); err == nil {
		t.Error("MkdirAll through a link was not refused")
	}
	if err := d.Chown("dir/secret", os.Getuid(), os.Getgid()); err == nil {
		t.Error("Chown through a link was not refused")
	}
	if err := d.SetDir("dir", 0o777, Keep, Keep); err == nil {
		t.Error("SetDir on a link was not refused")
	}
	if b, _ := os.ReadFile(secret); string(b) != "keep" {
		t.Fatalf("the file outside changed: %q", b)
	}
	if fi, _ := os.Stat(outside); fi.Mode().Perm() == 0o777 {
		t.Fatal("the directory outside got a new mode")
	}

	// A write to the name of a link replaces the link. The target stays.
	if _, err := d.WriteFile("file", []byte("new"), 0o600, Keep, Keep); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "keep" {
		t.Fatalf("the target of the link changed: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(base, "file")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("the link was not replaced: %v %v", fi, err)
	}

	// Lstat sees the link itself.
	fi, err := d.Lstat("dir")
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat(dir) = %v, %v", fi, err)
	}
}

func TestRemoveAllRemovesLinksNotTargets(t *testing.T) {
	d, base := open(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "tree/sub/deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "tree/sub/file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "tree/sub/link")); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(base, "tree")); !os.IsNotExist(err) {
		t.Fatalf("the tree is still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("RemoveAll removed a file outside the directory: %v", err)
	}
	if err := d.RemoveAll("tree"); err != nil {
		t.Fatalf("RemoveAll of a path that does not exist: %v", err)
	}
	if err := d.RemoveAll("no/such/path"); err != nil {
		t.Fatalf("RemoveAll of a path with no parent: %v", err)
	}
}

func TestMkdirAllKeepsExistingDirectories(t *testing.T) {
	d, base := open(t)
	if err := os.Mkdir(filepath.Join(base, "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.MkdirAll("old/new", 0o755, Keep, Keep); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(base, "old")); fi.Mode().Perm() != 0o700 {
		t.Fatalf("the mode of a directory that exists changed: %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(base, "old/new")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("the mode of the new directory = %v", fi.Mode())
	}
	if err := os.WriteFile(filepath.Join(base, "plain"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.MkdirAll("plain/x", 0o755, Keep, Keep); err == nil {
		t.Fatal("MkdirAll through a file was not refused")
	}
}

func TestSymlinkRenameAndSize(t *testing.T) {
	d, base := open(t)
	if err := d.Symlink("/etc/target", "link"); err != nil {
		t.Fatal(err)
	}
	if err := d.Symlink("/etc/other", "link"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(base, "link")); got != "/etc/other" {
		t.Fatalf("link = %q", got)
	}
	if err := d.MkdirAll("data/sub", 0o755, Keep, Keep); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFile("data/a", make([]byte, 100), 0o600, Keep, Keep); err != nil {
		t.Fatal(err)
	}
	if _, err := d.WriteFile("data/sub/b", make([]byte, 50), 0o600, Keep, Keep); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(base, "data/sub/link")); err != nil {
		t.Fatal(err)
	}
	if got := d.Size("data"); got != 150 {
		t.Fatalf("Size = %d, want 150", got)
	}
	if err := d.Rename("data", "moved"); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Exists("moved/sub/b"); err != nil || !ok {
		t.Fatalf("Exists after Rename = %v, %v", ok, err)
	}
	if ok, err := d.Exists("data"); err != nil || ok {
		t.Fatalf("the old name still exists: %v, %v", ok, err)
	}
	if err := d.Remove("moved/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lstat("moved/a"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat after Remove = %v", err)
	}
}
