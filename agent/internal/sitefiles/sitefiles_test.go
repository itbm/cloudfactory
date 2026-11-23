package sitefiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func code(err error) string {
	if err == nil {
		return ""
	}
	return codeOf(err)
}

func TestWriteReadListMoveDelete(t *testing.T) {
	dir := t.TempDir()
	if _, err := Do(dir, Request{Op: OpMkdir, Path: "/public"}); err != nil {
		t.Fatal(err)
	}
	resp, err := Do(dir, Request{Op: OpWrite, Path: "/public/index.php", Content: []byte("<?php echo 1;")})
	if err != nil || resp.Entry.Name != "index.php" || resp.Entry.Type != "file" || resp.Entry.SizeBytes != 13 || resp.Entry.Mode != 0o640 {
		t.Fatalf("write = %+v, %v", resp.Entry, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err = Do(dir, Request{Op: OpList, Path: "/"})
	if err != nil || len(resp.Entries) != 2 || resp.Entries[0].Name != "public" || resp.Entries[0].Type != "directory" || resp.Entries[1].Name != "a.txt" {
		t.Fatalf("list = %+v, %v", resp.Entries, err)
	}
	// A write of a file that exists keeps its permission bits, and leaves no temporary file.
	if _, err := Do(dir, Request{Op: OpWrite, Path: "/a.txt", Content: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "a.txt"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", fi.Mode().Perm())
	}
	resp, err = Do(dir, Request{Op: OpRead, Path: "/a.txt"})
	if err != nil || string(resp.Content) != "new" {
		t.Fatalf("read = %q, %v", resp.Content, err)
	}
	if _, err := Do(dir, Request{Op: OpWrite, Path: "/a.txt", CreateOnly: true}); code(err) != CodeExists {
		t.Errorf("create only = %v", err)
	}
	if _, err := Do(dir, Request{Op: OpMove, Path: "/a.txt", To: "/public/index.php"}); code(err) != CodeExists {
		t.Errorf("move to a path that exists = %v", err)
	}
	if _, err := Do(dir, Request{Op: OpMove, Path: "/public", To: "/public/sub"}); code(err) != CodeInvalid {
		t.Errorf("move into itself = %v", err)
	}
	if resp, err := Do(dir, Request{Op: OpMove, Path: "/a.txt", To: "/public/b.txt"}); err != nil || resp.Entry.Name != "b.txt" {
		t.Fatalf("move = %+v, %v", resp.Entry, err)
	}
	if _, err := Do(dir, Request{Op: OpDelete, Path: "/public"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(dir, Request{Op: OpDelete, Path: "/public"}); code(err) != CodeNotFound {
		t.Errorf("delete of no file = %v", err)
	}
	if _, err := Do(dir, Request{Op: OpDelete, Path: "/"}); code(err) != CodeInvalid {
		t.Errorf("delete of the directory of the website = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("the directory is not empty: %v", entries)
	}
}

func TestPathsStayInTheDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret")
	if err := os.WriteFile(secret, []byte("other website"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/../secret", "/a/../../secret", "/./a", "//a", "/a/", "a", "", "/a\x00b", "/a\nb"} {
		if _, err := Do(dir, Request{Op: OpRead, Path: p}); code(err) != CodeInvalid {
			t.Errorf("read %q = %v", p, err)
		}
	}
	// A link that goes out of the directory. The list shows it, but no operation follows it.
	if err := os.Symlink(secret, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}
	if resp, err := Do(dir, Request{Op: OpRead, Path: "/link"}); err == nil {
		t.Errorf("read through a link gave %q", resp.Content)
	}
	if _, err := Do(dir, Request{Op: OpRead, Path: "/up/secret"}); err == nil {
		t.Error("read through a linked directory gave no error")
	}
	if _, err := Do(dir, Request{Op: OpWrite, Path: "/up/new", Content: []byte("x")}); err == nil {
		t.Error("write through a linked directory gave no error")
	}
	if _, err := Do(dir, Request{Op: OpWrite, Path: "/link", Content: []byte("x")}); code(err) != CodeInvalid {
		t.Errorf("write on a link = %v", err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "other website" {
		t.Errorf("the file out of the directory changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(base, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a file is out of the directory")
	}
	resp, err := Do(dir, Request{Op: OpList, Path: "/"})
	if err != nil || len(resp.Entries) != 2 || resp.Entries[0].Type != "link" || resp.Entries[0].Target != secret {
		t.Errorf("list = %+v, %v", resp.Entries, err)
	}
	// The removal of a link removes the link only.
	if _, err := Do(dir, Request{Op: OpDelete, Path: "/up"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("the removal of a link removed its target: %v", err)
	}
}

func TestLimits(t *testing.T) {
	dir := t.TempDir()
	if _, err := Do(dir, Request{Op: OpWrite, Path: "/big", Content: make([]byte, MaxWrite+1)}); code(err) != CodeTooLarge {
		t.Errorf("large write = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big"), make([]byte, MaxRead+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(dir, Request{Op: OpRead, Path: "/big"}); code(err) != CodeTooLarge {
		t.Errorf("large read = %v", err)
	}
	if _, err := Do(dir, Request{Op: OpRead, Path: "/"}); code(err) != CodeInvalid {
		t.Errorf("read of a directory = %v", err)
	}
	if _, err := Do(dir, Request{Op: "chown", Path: "/big"}); code(err) != CodeInvalid {
		t.Errorf("unknown operation = %v", err)
	}
}

func TestMainAnswers(t *testing.T) {
	dir := t.TempDir()
	in, _ := json.Marshal(Request{Op: OpWrite, Path: "/a.txt", Content: []byte("hello")})
	var out bytes.Buffer
	if err := Main(dir, bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Entry == nil || resp.Entry.Name != "a.txt" || resp.Code != "" {
		t.Fatalf("answer = %s, %v", out.String(), err)
	}
	out.Reset()
	if err := Main(dir, strings.NewReader(`{"op":"read","path":"/none"}`), &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Code != CodeNotFound || strings.Contains(resp.Error, dir) {
		t.Fatalf("answer = %s, %v", out.String(), err)
	}
	out.Reset()
	if err := Main(dir, strings.NewReader(`not json`), &out); err != nil || !strings.Contains(out.String(), CodeInvalid) {
		t.Fatalf("answer = %s, %v", out.String(), err)
	}
}

func TestIsText(t *testing.T) {
	if !IsText([]byte("héllo\n")) || IsText([]byte{0xff, 0xfe}) || IsText([]byte("a\x00b")) {
		t.Error("IsText is not correct")
	}
}

// The read of a FIFO fails at once. It did not return before (adversarial review, findings
// A2-03 and G5-03).
func TestReadOfAFifoDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(dir+"/p", 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 2)
	for _, op := range []string{OpRead, OpList} {
		go func() {
			_, err := Do(dir, Request{Op: op, Path: "/p"})
			done <- err
		}()
	}
	for range 2 {
		select {
		case err := <-done:
			if code(err) != CodeInvalid {
				t.Errorf("error = %v; want the code %q", err, CodeInvalid)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the operation waits for a writer of the FIFO")
		}
	}
}
