// Package migrate reads the backup archive of a different hosting panel, for example cPanel
// or DirectAdmin, and gives a description of the account in the format "cf-account/1"
// (Account). The panel makes a plan from the description. The import jobs of the agent then
// copy the content of the archive: the files of each website, the database dumps and the
// mail.
//
// The archive comes from a customer or from a different server, so the package does not trust
// it. It never extracts the archive: it reads the archive as a stream. A content job sends the
// files of one website to a tar process that runs as the Unix user of that website. The names
// are cleaned, and the package refuses each name that goes out of the archive.
package migrate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Types of an entry.
const (
	TypeFile    = 'f'
	TypeDir     = 'd'
	TypeSymlink = 'l'
	TypeLink    = 'h'
	TypeOther   = 'o'
)

// Entry is one member of an archive.
type Entry struct {
	// Name is the clean path in the archive, with no "./" or "/" at the start and no "/" at
	// the end. A member of a nested archive has the name of the nested archive, "/" and its
	// own name.
	Name    string
	Type    byte
	Size    int64
	Mode    int64
	ModTime time.Time
	// Link is the target of a symbolic link or of a hard link.
	Link string
}

// ErrStop ends a walk early with no error.
var ErrStop = errors.New("stop")

// ErrFormat is the error of an archive that the agent cannot read.
var ErrFormat = errors.New("the file is not a tar archive (plain, gzip, bzip2 or zstd)")

// tarSuffixes are the names of a member that can be a nested archive.
var tarSuffixes = []string{".tar", ".tar.gz", ".tgz", ".tar.zst", ".tzst", ".tar.bz2"}

// IsTarName reports whether a name ends as the name of a tar archive.
func IsTarName(name string) bool {
	for _, s := range tarSuffixes {
		if strings.HasSuffix(strings.ToLower(name), s) {
			return true
		}
	}
	return false
}

// Archive is a backup archive on the disk.
type Archive struct {
	Path string
	// Expand reports whether a member is a nested archive that the walk reads too. Nil means
	// no nested archives.
	Expand func(name string) bool
	// Lenient skips a nested archive that Expand selects and that is not a tar archive. With
	// no Lenient, such a member is an error.
	Lenient bool
}

// Walk calls fn for each member of the archive, in the order of the archive. r is the content
// of a regular file. It is valid only during the call. A nested archive that Expand selects is
// not given as a file: its members are given in its place.
func (a *Archive) Walk(ctx context.Context, fn func(e Entry, r io.Reader) error) error {
	f, err := os.Open(a.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	err = walkStream(ctx, f, "", a.Expand, a.Lenient, fn, 0)
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

// nestedFormatError is the error of a nested archive that is not a tar archive.
type nestedFormatError struct{ name string }

func (e *nestedFormatError) Error() string { return e.name + ": " + ErrFormat.Error() }
func (e *nestedFormatError) Unwrap() error { return ErrFormat }

// maxDepth is the largest number of archives in archives.
const maxDepth = 3

func walkStream(ctx context.Context, r io.Reader, prefix string, expand func(string) bool, lenient bool, fn func(Entry, io.Reader) error, depth int) error {
	plain, done, err := Decompress(r)
	if err != nil {
		return err
	}
	defer done()
	tr := tar.NewReader(plain)
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if first {
				return ErrFormat
			}
			return fmt.Errorf("read the archive: %w", err)
		}
		first = false
		name, ok := CleanName(h.Name)
		if !ok {
			continue
		}
		if prefix != "" {
			name = prefix + "/" + name
		}
		e := Entry{Name: name, Size: h.Size, Mode: h.Mode, ModTime: h.ModTime, Link: h.Linkname}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			e.Type = TypeFile
		case tar.TypeDir:
			e.Type, e.Size = TypeDir, 0
		case tar.TypeSymlink:
			e.Type, e.Size = TypeSymlink, 0
		case tar.TypeLink:
			e.Type, e.Size = TypeLink, 0
			if l, ok := CleanName(h.Linkname); ok {
				if prefix != "" {
					l = prefix + "/" + l
				}
				e.Link = l
			}
		default:
			e.Type, e.Size = TypeOther, 0
		}
		if e.Type == TypeFile && depth < maxDepth && expand != nil && IsTarName(name) && expand(name) {
			err := walkStream(ctx, tr, name, expand, lenient, fn, depth+1)
			var nested *nestedFormatError
			if errors.Is(err, ErrFormat) && !errors.As(err, &nested) {
				// The member itself is not a tar archive.
				if lenient {
					// The next header skips the rest of the member.
					continue
				}
				return &nestedFormatError{name: name}
			}
			if err != nil {
				return err
			}
			continue
		}
		if err := fn(e, tr); err != nil {
			return err
		}
	}
}

// Decompress gives the plain content of a stream that is plain, gzip, bzip2 or zstd. It finds
// the type from the first bytes, not from the name. done releases the decoder.
func Decompress(r io.Reader) (plain io.Reader, done func(), err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(4)
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, ErrFormat
		}
		return gz, func() { gz.Close() }, nil
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		// The window limit protects the memory of the agent from a stream that asks for a
		// large window.
		zr, err := zstd.NewReader(br, zstd.WithDecoderMaxWindow(1<<27), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, nil, ErrFormat
		}
		return zr, zr.Close, nil
	case bytes.HasPrefix(head, []byte("BZh")):
		return bzip2.NewReader(br), func() {}, nil
	}
	return br, func() {}, nil
}

// CleanName gives the clean form of the name of a member: no "./" or "/" at the start, no "/"
// at the end and no "." or ".." parts. ok is false for a name that goes out of the archive, the
// archive itself ("."), or a name with a NUL byte.
func CleanName(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	n := strings.TrimLeft(name, "/")
	for strings.HasPrefix(n, "./") {
		n = strings.TrimLeft(n[2:], "/")
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", false
		}
	}
	n = path.Clean("/" + n)[1:]
	if n == "" || n == "." {
		return "", false
	}
	return n, true
}

// Within reports whether name is dir or a path in dir. An empty dir is the root of the archive.
func Within(name, dir string) bool {
	return dir == "" || name == dir || strings.HasPrefix(name, dir+"/")
}

// Rel gives the path of name in dir, or "" for dir itself.
func Rel(name, dir string) string {
	if dir == "" {
		return name
	}
	if name == dir {
		return ""
	}
	return strings.TrimPrefix(name, dir+"/")
}

// ExpandFor gives the nested archives that a walk must read to find path: each part of path
// whose name is the name of a tar archive. For example "web/a.com/domain_data.tar.zst/public"
// needs "web/a.com/domain_data.tar.zst".
func ExpandFor(paths ...string) func(string) bool {
	nested := map[string]bool{}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		for i := range parts {
			if IsTarName(parts[i]) {
				nested[strings.Join(parts[:i+1], "/")] = true
			}
		}
	}
	if len(nested) == 0 {
		return nil
	}
	return func(name string) bool { return nested[name] }
}

// readSmall reads a file of up to max bytes. A larger file gives an error.
func readSmall(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("the file is larger than %d bytes", max)
	}
	return b, nil
}
