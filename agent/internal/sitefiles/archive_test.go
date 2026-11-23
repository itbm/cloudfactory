package sitefiles

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// site makes a small website: a directory with files, an empty directory and a link.
func site(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "public/css"), 0o750))
	must(t, os.Mkdir(filepath.Join(dir, "public/empty"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "public/index.php"), []byte("<?php echo 1;"), 0o640))
	must(t, os.WriteFile(filepath.Join(dir, "public/css/site.css"), []byte("body { margin: 0 }"), 0o604))
	must(t, os.WriteFile(filepath.Join(dir, "deploy.sh"), []byte("#!/bin/sh\necho ok\n"), 0o750))
	must(t, os.Symlink("index.php", filepath.Join(dir, "public/home.php")))
	return dir
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The archive and the extract give the same files, the same links and the same permissions,
// for each format.
func TestArchiveAndExtract(t *testing.T) {
	for _, name := range []string{"site.zip", "site.tar", "site.tar.gz", "site.TGZ"} {
		t.Run(name, func(t *testing.T) {
			dir := site(t)
			resp, err := Do(dir, Request{Op: OpArchive, Path: "/" + name, Paths: []string{"/public", "/deploy.sh"}})
			if err != nil {
				t.Fatal(err)
			}
			s := resp.Summary
			if resp.Entry.Name != name || resp.Entry.Mode != 0o640 || s.Files != 3 || s.Directories != 3 || s.Links != 1 || s.SizeBytes != resp.Entry.SizeBytes || s.SizeBytes == 0 {
				t.Fatalf("archive = %+v, %+v", resp.Entry, s)
			}
			resp, err = Do(dir, Request{Op: OpExtract, Path: "/" + name, To: "/out/copy"})
			if err != nil {
				t.Fatal(err)
			}
			s = resp.Summary
			if s.Files != 3 || s.Directories != 3 || s.Links != 1 || s.Kept != 0 || s.Skipped != 0 || s.SizeBytes != 49 {
				t.Fatalf("extract = %+v", s)
			}
			out := filepath.Join(dir, "out/copy")
			if content(t, filepath.Join(out, "public/index.php")) != "<?php echo 1;" || content(t, filepath.Join(out, "deploy.sh")) != "#!/bin/sh\necho ok\n" {
				t.Error("the content is not the same")
			}
			if target, err := os.Readlink(filepath.Join(out, "public/home.php")); err != nil || target != "index.php" {
				t.Errorf("link = %q, %v", target, err)
			}
			for p, want := range map[string]os.FileMode{"public": 0o750, "public/empty": 0o700, "public/index.php": 0o640, "public/css/site.css": 0o604, "deploy.sh": 0o750} {
				if got := mode(t, filepath.Join(out, p)); got != want {
					t.Errorf("mode of %s = %o; want %o", p, got, want)
				}
			}
			// The extract keeps the time of the last change.
			a, _ := os.Stat(filepath.Join(dir, "deploy.sh"))
			b, _ := os.Stat(filepath.Join(out, "deploy.sh"))
			if !a.ModTime().Truncate(2 * time.Second).Equal(b.ModTime().Truncate(2 * time.Second)) {
				t.Errorf("time = %v; want %v", b.ModTime(), a.ModTime())
			}
			assertNoTemporaryFiles(t, dir)
		})
	}
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".cf-") {
			t.Errorf("a temporary file stays: %s", p)
		}
		return nil
	})
}

// An archive of "/" has all files of the website at the top level, but not the archive.
func TestArchiveOfTheWebsite(t *testing.T) {
	dir := site(t)
	if _, err := Do(dir, Request{Op: OpArchive, Path: "/public/all.zip", Paths: []string{"/"}}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(filepath.Join(dir, "public/all.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Mode()&fs.ModeSymlink != 0 && f.Name != "public/home.php" {
			t.Errorf("link %s", f.Name)
		}
	}
	want := []string{"deploy.sh", "public/", "public/css/", "public/css/site.css", "public/empty/", "public/home.php", "public/index.php"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %q; want %q", names, want)
	}
}

func TestArchiveRefusals(t *testing.T) {
	dir := site(t)
	for _, c := range []struct {
		name string
		req  Request
		code string
	}{
		{"exists", Request{Path: "/deploy.sh", Paths: []string{"/public"}}, CodeInvalid},
		{"archive that exists", Request{Path: "/public/index.php.zip", Paths: []string{"/public"}}, ""},
		{"unknown format", Request{Path: "/site.rar", Paths: []string{"/public"}}, CodeInvalid},
		{"bzip2 is only for an extract", Request{Path: "/site.tar.bz2", Paths: []string{"/public"}}, CodeInvalid},
		{"no paths", Request{Path: "/site.zip"}, CodeInvalid},
		{"same name two times", Request{Path: "/site.zip", Paths: []string{"/public/css", "/public/empty/css"}}, CodeInvalid},
		{"the website and a path in it", Request{Path: "/site.zip", Paths: []string{"/", "/public"}}, CodeInvalid},
		{"a path that does not exist", Request{Path: "/site.zip", Paths: []string{"/none"}}, CodeNotFound},
		{"a path that is not valid", Request{Path: "/site.zip", Paths: []string{"/../x"}}, CodeInvalid},
		{"a directory that does not exist", Request{Path: "/none/site.zip", Paths: []string{"/public"}}, CodeNotFound},
		{"too many paths", Request{Path: "/site.zip", Paths: slices.Repeat([]string{"/public"}, 2001)}, CodeInvalid},
	} {
		c.req.Op = OpArchive
		if _, err := Do(dir, c.req); code(err) != c.code {
			t.Errorf("%s: error = %v; want the code %q", c.name, err, c.code)
		}
	}
	if _, err := Do(dir, Request{Op: OpArchive, Path: "/public/index.php.zip", Paths: []string{"/deploy.sh"}}); code(err) != CodeExists {
		t.Errorf("archive with the name of a file that exists = %v", err)
	}
	assertNoTemporaryFiles(t, dir)
}

// An archive does not go through a link: it has the link.
func TestArchiveDoesNotFollowLinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	must(t, os.Mkdir(dir, 0o750))
	must(t, os.WriteFile(filepath.Join(base, "secret"), []byte("other website"), 0o600))
	must(t, os.Symlink(filepath.Join(base, "secret"), filepath.Join(dir, "link")))
	must(t, os.Symlink(base, filepath.Join(dir, "up")))
	if _, err := Do(dir, Request{Op: OpArchive, Path: "/a.tar", Paths: []string{"/"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.tar"))
	if bytes.Contains(b, []byte("other website")) {
		t.Error("the archive has the content of a file out of the website")
	}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeSymlink || hdr.Uid != 0 || hdr.Uname != "" {
			t.Errorf("entry %s: type %c, owner %d %q", hdr.Name, hdr.Typeflag, hdr.Uid, hdr.Uname)
		}
	}
}

// entry is one entry of an archive for a test.
type entry struct {
	name   string
	kind   byte
	mode   int64
	body   string
	target string
}

func tarOf(t *testing.T, gz bool, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var g *gzip.Writer
	if gz {
		g = gzip.NewWriter(&buf)
		w = g
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: mode, Linkname: e.target, ModTime: time.Unix(1700000000, 0)}
		if e.kind == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		must(t, tw.WriteHeader(hdr))
		if e.kind == tar.TypeReg {
			_, err := io.WriteString(tw, e.body)
			must(t, err)
		}
	}
	must(t, tw.Close())
	if g != nil {
		must(t, g.Close())
	}
	return buf.Bytes()
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch e.kind {
		case tar.TypeDir:
			hdr.SetMode(fs.ModeDir | 0o755)
		case tar.TypeSymlink:
			hdr.SetMode(fs.ModeSymlink | 0o777)
			e.body = e.target
		default:
			hdr.SetMode(fs.FileMode(e.mode) | 0o644)
		}
		w, err := zw.CreateHeader(hdr)
		must(t, err)
		_, err = io.WriteString(w, e.body)
		must(t, err)
	}
	must(t, zw.Close())
	return buf.Bytes()
}

// An extract writes only in the destination directory. It skips the names with "..", the
// links out of the destination and the devices, and removes setuid, setgid and sticky.
func TestExtractStaysInTheDestination(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	must(t, os.MkdirAll(filepath.Join(dir, "public"), 0o750))
	must(t, os.WriteFile(filepath.Join(dir, "public/index.php"), []byte("live"), 0o640))
	archive := tarOf(t, true,
		entry{name: "./", kind: tar.TypeDir, mode: 0o755},
		entry{name: "../public/index.php", kind: tar.TypeReg, body: "attack"},
		entry{name: "a/../../public/index.php", kind: tar.TypeReg, body: "attack"},
		entry{name: "/abs.txt", kind: tar.TypeReg, body: "abs"},
		entry{name: "./ok/file.txt", kind: tar.TypeReg, body: "ok", mode: 0o4777},
		entry{name: "up", kind: tar.TypeSymlink, target: "../public"},
		entry{name: "up/index.php", kind: tar.TypeReg, body: "attack"},
		entry{name: "root", kind: tar.TypeSymlink, target: "/"},
		entry{name: "root/etc/x", kind: tar.TypeReg, body: "attack"},
		entry{name: "hard", kind: tar.TypeLink, target: "../public/index.php"},
		entry{name: "hard-ok", kind: tar.TypeLink, target: "ok/file.txt"},
		entry{name: "fifo", kind: tar.TypeFifo},
		entry{name: "dev", kind: tar.TypeChar},
		entry{name: "ok/sticky", kind: tar.TypeDir, mode: 0o1777},
	)
	must(t, os.WriteFile(filepath.Join(dir, "x.tar.gz"), archive, 0o640))
	resp, err := Do(dir, Request{Op: OpExtract, Path: "/x.tar.gz", To: "/out"})
	if err != nil {
		t.Fatal(err)
	}
	s := resp.Summary
	if content(t, filepath.Join(dir, "public/index.php")) != "live" {
		t.Fatal("the extract changed a file out of the destination")
	}
	if s.Files != 3 || s.Links != 2 || s.Directories != 1 || s.Skipped != 7 || len(s.SkippedNames) != 7 {
		t.Errorf("summary = %+v", s)
	}
	if content(t, filepath.Join(dir, "out/abs.txt")) != "abs" || content(t, filepath.Join(dir, "out/ok/file.txt")) != "ok" || content(t, filepath.Join(dir, "out/hard-ok")) != "ok" {
		t.Error("the safe entries are not in the destination")
	}
	if m := mode(t, filepath.Join(dir, "out/ok/file.txt")); m != 0o755 {
		t.Errorf("mode = %o; want 0755", m)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "out/ok/sticky")); fi.Mode()&(fs.ModeSticky|fs.ModeSetuid) != 0 || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode of the directory = %v", fi.Mode())
	}
	if _, err := os.Lstat(filepath.Join(base, "etc")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the extract wrote out of the website")
	}
	for _, name := range []string{"/out/fifo", "/out/dev"} {
		if !slices.Contains(s.SkippedNames, name) {
			t.Errorf("the skipped names %q do not have %s", s.SkippedNames, name)
		}
	}
	assertNoTemporaryFiles(t, dir)
}

func TestExtractKeepsOrReplacesFiles(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "app")
	must(t, os.MkdirAll(filepath.Join(dir, "public/wp-content"), 0o750))
	must(t, os.WriteFile(filepath.Join(dir, "public/index.php"), []byte("old"), 0o600))
	must(t, os.WriteFile(filepath.Join(base, "secret"), []byte("other website"), 0o600))
	must(t, os.Symlink(filepath.Join(base, "secret"), filepath.Join(dir, "public/config.php")))
	archive := zipOf(t,
		entry{name: "public/", kind: tar.TypeDir},
		entry{name: "public/index.php", kind: tar.TypeReg, body: "new"},
		entry{name: "public/config.php", kind: tar.TypeReg, body: "new"},
		entry{name: "public/new.php", kind: tar.TypeReg, body: "new"},
		entry{name: "public/wp-content", kind: tar.TypeReg, body: "a file where a directory is"},
		entry{name: `public\win\file.txt`, kind: tar.TypeReg, body: "windows"},
	)
	must(t, os.WriteFile(filepath.Join(dir, "update.zip"), archive, 0o640))

	resp, err := Do(dir, Request{Op: OpExtract, Path: "/update.zip", To: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if s := resp.Summary; s.Files != 2 || s.Kept != 2 || s.Skipped != 1 || s.SkippedNames[0] != "/public/wp-content" {
		t.Errorf("summary = %+v", s)
	}
	if content(t, filepath.Join(dir, "public/index.php")) != "old" || content(t, filepath.Join(dir, "public/new.php")) != "new" || content(t, filepath.Join(dir, "public/win/file.txt")) != "windows" {
		t.Error("the files are not correct after the extract that keeps files")
	}

	resp, err = Do(dir, Request{Op: OpExtract, Path: "/update.zip", To: "/", Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := resp.Summary; s.Files != 4 || s.Kept != 0 || s.Skipped != 1 {
		t.Errorf("summary = %+v", s)
	}
	if content(t, filepath.Join(dir, "public/index.php")) != "new" {
		t.Error("the extract did not replace the file")
	}
	// The extract replaces a link with a file. It does not write through the link.
	if fi, _ := os.Lstat(filepath.Join(dir, "public/config.php")); !fi.Mode().IsRegular() || content(t, filepath.Join(base, "secret")) != "other website" {
		t.Error("the extract wrote through a link")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "public/wp-content")); !fi.IsDir() {
		t.Error("the extract replaced a directory")
	}
	assertNoTemporaryFiles(t, dir)
}

func TestExtractRefusals(t *testing.T) {
	dir := t.TempDir()
	good := tarOf(t, true, entry{name: "a.txt", kind: tar.TypeReg, body: strings.Repeat("a", 4096)})
	files := map[string][]byte{
		"not.zip":       []byte("not a zip file"),
		"not.tar.gz":    []byte("not gzip"),
		"short.tar.gz":  good[:len(good)/2],
		"site.rar":      []byte("rar"),
		"protected.zip": nil,
		"file":          []byte("x"),
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "a.txt", Flags: 0x1})
	_, _ = w.Write([]byte("encrypted"))
	must(t, zw.Close())
	files["protected.zip"] = buf.Bytes()
	for name, b := range files {
		must(t, os.WriteFile(filepath.Join(dir, name), b, 0o640))
	}
	for _, c := range []struct {
		path, to, code string
	}{
		{"/not.zip", "/", CodeInvalid},
		{"/not.tar.gz", "/", CodeInvalid},
		{"/short.tar.gz", "/", CodeInvalid},
		{"/site.rar", "/", CodeInvalid},
		{"/protected.zip", "/", CodeInvalid},
		{"/none.zip", "/", CodeNotFound},
		{"/not.zip", "/file", CodeInvalid},
		{"/not.zip", "/../x", CodeInvalid},
	} {
		if _, err := Do(dir, Request{Op: OpExtract, Path: c.path, To: c.to}); code(err) != c.code {
			t.Errorf("extract %s to %s = %v; want the code %q", c.path, c.to, err, c.code)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a protected entry was extracted")
	}
	assertNoTemporaryFiles(t, dir)
}

// An archive with a small size and a large content stops at the limit.
func TestExtractLimits(t *testing.T) {
	saved := extractLimits
	t.Cleanup(func() { extractLimits = saved })
	extractLimits.bytes, extractLimits.entries = 1000, 3
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "big.tar.gz"), tarOf(t, true, entry{name: "zeros", kind: tar.TypeReg, body: strings.Repeat("\x00", 5000)}), 0o640))
	must(t, os.WriteFile(filepath.Join(dir, "big.zip"), zipOf(t, entry{name: "zeros", kind: tar.TypeReg, body: strings.Repeat("\x00", 5000)}), 0o640))
	many := []entry{}
	for _, n := range []string{"a", "b", "c", "d"} {
		many = append(many, entry{name: n, kind: tar.TypeReg, body: n})
	}
	must(t, os.WriteFile(filepath.Join(dir, "many.tar"), tarOf(t, false, many...), 0o640))
	must(t, os.WriteFile(filepath.Join(dir, "many.zip"), zipOf(t, many...), 0o640))
	for _, name := range []string{"/big.tar.gz", "/big.zip", "/many.tar", "/many.zip"} {
		if _, err := Do(dir, Request{Op: OpExtract, Path: name, To: "/out"}); code(err) != CodeTooLarge {
			t.Errorf("extract %s = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out/zeros")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a part of a file that is too large stays")
	}
	assertNoTemporaryFiles(t, dir)
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt": "a.txt", "./a.txt": "a.txt", "/a.txt": "a.txt", "dir/": "dir", "././a": "a", ".hidden": ".hidden",
		"./": "", ".": "", "/": "",
		"..": "!", "../a": "!", "a/../b": "!", "a//b": "!", "a/./b": "!", "a\x00b": "!", "a\nb": "!",
	} {
		got, ok := cleanName(in)
		if !ok {
			got = "!"
		}
		if got != want {
			t.Errorf("cleanName(%q) = %q; want %q", in, got, want)
		}
	}
}

// A file system error of the destination for one entry skips the entry. A full disk stops
// the extract.
func TestExtractProblems(t *testing.T) {
	x := &extractor{to: "/out"}
	if err := x.problem("a", &os.PathError{Op: "mkdir", Path: "a", Err: syscall.ENOTDIR}); err != nil || x.sum.Skipped != 1 || x.sum.SkippedNames[0] != "/out/a" {
		t.Errorf("problem = %v, %+v", err, x.sum)
	}
	if err := x.problem("b", &os.PathError{Op: "write", Path: "b", Err: syscall.EDQUOT}); code(err) != CodeFull || !strings.HasPrefix(err.Error(), "/out/b: ") {
		t.Errorf("problem = %v", err)
	}
	for range maxSkippedNames + 5 {
		x.sum.skip("\x1b[2Jname")
	}
	if len(x.sum.SkippedNames) != maxSkippedNames || x.sum.Skipped != maxSkippedNames+6 || x.sum.SkippedNames[1] != "?[2Jname" {
		t.Errorf("summary = %+v", x.sum)
	}
}
