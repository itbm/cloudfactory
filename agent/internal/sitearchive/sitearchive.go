// Package sitearchive writes the files of the directory of one website as a tar archive with
// gzip compression. An account export uses it (docs/account-export.md).
//
// The agent starts this code as the Unix user of the website ("cf-agent site-archive"), not as
// root and not as the worker of the agent. Thus two limits apply, as for the file manager
// (package sitefiles):
//
//   - The kernel gives the process only the rights of the website user.
//   - Each path opens in an os.Root of the directory of the website. A symbolic link that goes
//     out of that directory gives an error.
//
// The archive has regular files, directories and symbolic links. A symbolic link stays a link:
// the archive has its target, not the file that it points to. The archive does not have
// sockets, named pipes and device files, and it does not have a file that is larger than the
// limit. The website can change its files during the export, so a file that changes or goes
// during the export is in the archive as it was, or is not in the archive.
package sitearchive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"syscall"
	"time"
)

// Command is the name of the command of the agent ("cf-agent site-archive DIR MAX-BYTES").
const Command = "site-archive"

// Summary is the result of an archive.
type Summary struct {
	// Files is the number of regular files in the archive. Bytes is the sum of their sizes.
	Files int64
	Bytes int64
	// Skipped is the number of entries that are not in the archive: special files, files
	// that are larger than the limit, and files that the user cannot read.
	Skipped int64
}

// Main writes the archive of dir to w. maxBytes is the largest file in the archive. A larger
// file is not in the archive, because a sparse file can be much larger than the disk limit of
// the website (adversarial review, finding G4-04). 0 means no limit.
func Main(dir string, maxBytes int64, w io.Writer) (Summary, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Summary{}, err
	}
	defer root.Close()
	return Write(root, maxBytes, w)
}

// Args reads the arguments of the command: the directory and the largest file in bytes.
func Args(args []string) (dir string, maxBytes int64, err error) {
	if len(args) != 2 {
		return "", 0, fmt.Errorf("usage: cf-agent %s DIR MAX-BYTES", Command)
	}
	maxBytes, err = strconv.ParseInt(args[1], 10, 64)
	if err != nil || maxBytes < 0 {
		return "", 0, fmt.Errorf("the largest file %q is not a number of bytes", args[1])
	}
	return args[0], maxBytes, nil
}

// Write writes the archive of the directory of root to w.
func Write(root *os.Root, maxBytes int64, w io.Writer) (Summary, error) {
	var sum Summary
	gz, err := gzip.NewWriterLevel(w, gzip.DefaultCompression)
	if err != nil {
		return sum, err
	}
	tw := tar.NewWriter(gz)
	buf := make([]byte, 256<<10)
	walk := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that the user cannot read, or that went during the export.
			if name == "." {
				return err
			}
			sum.Skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if name == "." {
			return nil
		}
		info, err := root.Lstat(name)
		if err != nil {
			sum.Skipped++
			return nil
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return tw.WriteHeader(header(name, tar.TypeDir, info, 0))
		case mode&fs.ModeSymlink != 0:
			target, err := root.Readlink(name)
			if err != nil {
				sum.Skipped++
				return nil
			}
			h := header(name, tar.TypeSymlink, info, 0)
			h.Linkname = target
			return tw.WriteHeader(h)
		case mode.IsRegular():
			if maxBytes > 0 && info.Size() > maxBytes {
				sum.Skipped++
				return nil
			}
			n, ok, err := writeFile(tw, root, name, maxBytes, buf)
			if err != nil {
				return err
			}
			if !ok {
				sum.Skipped++
				return nil
			}
			sum.Files++
			sum.Bytes += n
			return nil
		default:
			// A socket, a named pipe or a device file.
			sum.Skipped++
			return nil
		}
	})
	if walk != nil {
		return sum, walk
	}
	if err := tw.Close(); err != nil {
		return sum, err
	}
	return sum, gz.Close()
}

// writeFile writes one regular file to the archive. ok is false when the file is not in the
// archive: the user cannot open it, or it is not a regular file now. An error is an error of
// the archive, which stops the export.
//
// The file opens with O_NONBLOCK, so that a named pipe that the website puts in the place of
// a file does not stop the export. The size in the header is the size at the open. When the
// file becomes shorter during the export, the archive gets zeros at the end of the file.
// When it becomes longer, the archive gets only the first bytes.
func writeFile(tw *tar.Writer, root *os.Root, name string, maxBytes int64, buf []byte) (n int64, ok bool, err error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, false, nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || (maxBytes > 0 && info.Size() > maxBytes) {
		return 0, false, nil
	}
	size := info.Size()
	if err := tw.WriteHeader(header(name, tar.TypeReg, info, size)); err != nil {
		return 0, false, err
	}
	copied, err := io.CopyBuffer(tw, io.LimitReader(onlyFile{f}, size), buf)
	if err != nil {
		var fileErr *fileError
		if !errors.As(err, &fileErr) {
			return 0, false, err
		}
	}
	// The header has the size, so the archive must have that number of bytes.
	if copied < size {
		if _, err := io.CopyBuffer(tw, io.LimitReader(zeros{}, size-copied), buf); err != nil {
			return 0, false, err
		}
	}
	return size, true, nil
}

// header gives the header of an entry. The owner is not in the archive: the website user
// owns all files of a website. The archive has the permission bits only, with no set-user-ID,
// set-group-ID or sticky bit.
func header(name string, kind byte, info fs.FileInfo, size int64) *tar.Header {
	name = path.Clean(name)
	if kind == tar.TypeDir {
		name += "/"
	}
	return &tar.Header{
		Typeflag: kind,
		Name:     name,
		Mode:     int64(info.Mode().Perm()),
		Size:     size,
		ModTime:  info.ModTime().UTC().Truncate(time.Second),
		Format:   tar.FormatPAX,
	}
}

// fileError is an error of a read of a file of the website.
type fileError struct{ err error }

func (e *fileError) Error() string { return e.err.Error() }

// onlyFile marks the errors of a read of the file, so that writeFile can tell them from the
// errors of the archive.
type onlyFile struct{ r io.Reader }

func (o onlyFile) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	if err != nil && err != io.EOF {
		err = &fileError{err}
	}
	return n, err
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
