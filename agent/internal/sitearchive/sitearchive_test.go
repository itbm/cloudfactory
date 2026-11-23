package sitearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// entries reads an archive and gives each entry as "<type> <name> <mode> <content or target>".
func entries(t *testing.T, archive []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		switch h.Typeflag {
		case tar.TypeDir:
			out = append(out, "dir "+h.Name+" "+modeText(h.Mode))
		case tar.TypeSymlink:
			out = append(out, "link "+h.Name+" -> "+h.Linkname)
		case tar.TypeReg:
			out = append(out, "file "+h.Name+" "+modeText(h.Mode)+" "+string(body))
		default:
			out = append(out, "other "+h.Name)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s has an owner in the archive", h.Name)
		}
	}
	sort.Strings(out)
	return out
}

func modeText(m int64) string { return fmt.Sprintf("%04o", m) }

func TestArchiveHasFilesDirectoriesAndLinks(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "public", "css"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "public", "index.php"), []byte("<?php echo 1;"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "public", "css", "a.css"), []byte("a{}"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("KEY=1"), 0o640))
	must(t, os.Chmod(filepath.Join(dir, ".env"), 0o6640))
	must(t, os.Symlink("public/index.php", filepath.Join(dir, "current")))
	// A link out of the directory stays a link. The archive does not have the file.
	must(t, os.Symlink("/etc/passwd", filepath.Join(dir, "passwd")))
	must(t, syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600))

	var b bytes.Buffer
	sum, err := Main(dir, 0, &b)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, b.Bytes())
	want := []string{
		"dir public/ 0755",
		"dir public/css/ 0755",
		// No set-user-ID or set-group-ID bit.
		"file .env 0640 KEY=1",
		"file public/css/a.css 0600 a{}",
		"file public/index.php 0644 <?php echo 1;",
		"link current -> public/index.php",
		"link passwd -> /etc/passwd",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the archive has\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if sum.Files != 3 || sum.Bytes != int64(len("<?php echo 1;")+len("a{}")+len("KEY=1")) || sum.Skipped != 1 {
		t.Errorf("summary %+v", sum)
	}
}

// Adversarial review, finding G4-04: a sparse file can be much larger than the disk limit.
func TestArchiveSkipsFilesLargerThanTheLimit(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "small"), []byte("12345"), 0o644))
	f, err := os.Create(filepath.Join(dir, "sparse"))
	must(t, err)
	must(t, f.Truncate(1<<40))
	must(t, f.Close())

	var b bytes.Buffer
	sum, err := Main(dir, 1024, &b)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries(t, b.Bytes()); len(got) != 1 || got[0] != "file small 0644 12345" {
		t.Fatalf("the archive has %v", got)
	}
	if sum.Skipped != 1 {
		t.Errorf("summary %+v", sum)
	}
}

func TestArchiveOfAnEmptyDirectory(t *testing.T) {
	var b bytes.Buffer
	if _, err := Main(t.TempDir(), 0, &b); err != nil {
		t.Fatal(err)
	}
	if got := entries(t, b.Bytes()); len(got) != 0 {
		t.Fatalf("the archive has %v", got)
	}
}

func TestArchiveOfAMissingDirectoryFails(t *testing.T) {
	if _, err := Main(filepath.Join(t.TempDir(), "gone"), 0, io.Discard); err == nil {
		t.Fatal("no error")
	}
}

func TestArgs(t *testing.T) {
	if dir, max, err := Args([]string{"/srv/x", "1024"}); err != nil || dir != "/srv/x" || max != 1024 {
		t.Fatalf("Args gave %q %d %v", dir, max, err)
	}
	for _, bad := range [][]string{{"/srv/x"}, {"/srv/x", "-1"}, {"/srv/x", "ten"}, {"/srv/x", "1", "2"}} {
		if _, _, err := Args(bad); err == nil {
			t.Errorf("Args(%q) gave no error", bad)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
