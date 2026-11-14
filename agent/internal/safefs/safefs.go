// Package safefs does the file work of root below one directory, with no symbolic links.
//
// Second audit, finding F01 and the note about the privilege boundary: a root process that
// uses a path in a directory that a different user can change can be sent to a different
// file with a symbolic link. This package opens each path with openat2 and the flags
// RESOLVE_BENEATH and RESOLVE_NO_SYMLINKS. The kernel then refuses a path that goes out of
// the directory, and a path that has a symbolic link in one of its parts. Each change of the
// last part of a path uses the file descriptor of its parent directory, and does not follow
// a link.
package safefs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Keep is the value of uid or gid that does not change the owner or the group.
const Keep = -1

// Dir is an open directory. All paths of its methods are relative to it.
type Dir struct {
	base string
	fd   int
}

// Open opens the directory base. The path of base itself can have symbolic links: use a
// path that only root can change.
func Open(base string) (*Dir, error) {
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: base, Err: err}
	}
	return &Dir{base: base, fd: fd}, nil
}

// Close closes the directory.
func (d *Dir) Close() error { return unix.Close(d.fd) }

// Base gives the path of the directory.
func (d *Dir) Base() string { return d.base }

// clean checks a relative path. It refuses an absolute path and a path with "..".
func clean(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("safefs: %q is not a relative path", rel)
	}
	c := filepath.Clean(rel)
	for _, part := range strings.Split(c, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("safefs: %q goes out of the directory", rel)
		}
	}
	return c, nil
}

// noOpenat2 is true after the kernel said that it has no openat2 (Linux before 5.6).
var noOpenat2 bool

// openBeneath opens rel below the directory dirfd. No part of rel can be a symbolic link.
func openBeneath(dirfd int, rel string, flags int, mode uint32) (int, error) {
	flags |= unix.O_CLOEXEC | unix.O_NOFOLLOW
	if !noOpenat2 {
		how := unix.OpenHow{Flags: uint64(flags), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS}
		if flags&unix.O_CREAT != 0 {
			how.Mode = uint64(mode)
		}
		for {
			fd, err := unix.Openat2(dirfd, rel, &how)
			switch {
			case err == unix.EINTR || err == unix.EAGAIN:
				// EAGAIN: a rename in the path while the kernel read it. Try again.
				continue
			case err == unix.ENOSYS:
				noOpenat2 = true
			default:
				return fd, err
			}
			break
		}
	}
	// A kernel with no openat2: open each part with O_NOFOLLOW. An open of a link then fails.
	parts := strings.Split(rel, string(filepath.Separator))
	cur := dirfd
	for i, part := range parts {
		last := i == len(parts)-1
		f, m := unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(0)
		if last {
			f, m = flags, mode
		}
		fd, err := unix.Openat(cur, part, f, m)
		if cur != dirfd {
			unix.Close(cur)
		}
		if err != nil {
			return -1, err
		}
		cur = fd
	}
	return cur, nil
}

// parent opens the parent directory of rel, and gives the last part of rel.
func (d *Dir) parent(op, rel string) (fd int, name string, err error) {
	c, err := clean(rel)
	if err != nil {
		return -1, "", err
	}
	if c == "." {
		return -1, "", fmt.Errorf("safefs: %s needs a path below %s", op, d.base)
	}
	dir, name := filepath.Split(c)
	dir = strings.TrimSuffix(dir, string(filepath.Separator))
	if dir == "" {
		fd, err = unix.Dup(d.fd)
		if err == nil {
			unix.CloseOnExec(fd)
		}
	} else {
		fd, err = openBeneath(d.fd, dir, unix.O_PATH|unix.O_DIRECTORY, 0)
	}
	if err != nil {
		return -1, "", d.pathError(op, rel, err)
	}
	return fd, name, nil
}

func (d *Dir) pathError(op, rel string, err error) error {
	if err == unix.ELOOP || err == unix.EXDEV {
		// ELOOP: a part of the path is a symbolic link. EXDEV: the path goes out of the directory.
		return &fs.PathError{Op: op, Path: filepath.Join(d.base, rel), Err: fmt.Errorf("the path has a symbolic link or goes out of %s: %w", d.base, err)}
	}
	return &fs.PathError{Op: op, Path: filepath.Join(d.base, rel), Err: err}
}

// OpenFile opens a file. It does not follow a link at the last part of the path.
func (d *Dir) OpenFile(rel string, flag int, mode os.FileMode) (*os.File, error) {
	c, err := clean(rel)
	if err != nil {
		return nil, err
	}
	fd, err := openBeneath(d.fd, c, flag, uint32(mode.Perm()))
	if err != nil {
		return nil, d.pathError("open", rel, err)
	}
	return os.NewFile(uintptr(fd), filepath.Join(d.base, c)), nil
}

// ReadFile gives the content of a regular file.
func (d *Dir) ReadFile(rel string) ([]byte, error) {
	f, err := d.OpenFile(rel, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: f.Name(), Err: errors.New("not a regular file")}
	}
	return io.ReadAll(f)
}

// Lstat gives the properties of a path. It does not follow a link at the last part.
func (d *Dir) Lstat(rel string) (fs.FileInfo, error) {
	c, err := clean(rel)
	if err != nil {
		return nil, err
	}
	fd, err := openBeneath(d.fd, c, unix.O_PATH, 0)
	if err != nil {
		return nil, d.pathError("lstat", rel, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.base, c))
	defer f.Close()
	return f.Stat()
}

// Exists reports whether a path exists. A link at the last part exists.
func (d *Dir) Exists(rel string) (bool, error) {
	_, err := d.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// own sets the owner and the mode of an open file or directory.
func own(fd int, mode os.FileMode, uid, gid int) error {
	if uid != Keep || gid != Keep {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return err
		}
	}
	return unix.Fchmod(fd, uint32(mode.Perm()))
}

// MkdirAll makes a directory and its parents. A new directory gets the mode and the owner.
// A directory that exists keeps its mode and its owner. A part of the path that is not a
// directory is an error.
func (d *Dir) MkdirAll(rel string, mode os.FileMode, uid, gid int) error {
	c, err := clean(rel)
	if err != nil {
		return err
	}
	if c == "." {
		return nil
	}
	cur, err := unix.Dup(d.fd)
	if err != nil {
		return err
	}
	unix.CloseOnExec(cur)
	defer func() { unix.Close(cur) }()
	for _, part := range strings.Split(c, string(filepath.Separator)) {
		made := false
		if err := unix.Mkdirat(cur, part, uint32(mode.Perm())); err == nil {
			made = true
		} else if err != unix.EEXIST {
			return d.pathError("mkdir", rel, err)
		}
		fd, err := unix.Openat(cur, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return d.pathError("mkdir", rel, err)
		}
		unix.Close(cur)
		cur = fd
		if made {
			if err := own(cur, mode, uid, gid); err != nil {
				return d.pathError("mkdir", rel, err)
			}
		}
	}
	return nil
}

// Mkdir makes one directory with the mode and the owner. The parent must exist.
func (d *Dir) Mkdir(rel string, mode os.FileMode, uid, gid int) error {
	pfd, name, err := d.parent("mkdir", rel)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := unix.Mkdirat(pfd, name, uint32(mode.Perm())); err != nil {
		return d.pathError("mkdir", rel, err)
	}
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return d.pathError("mkdir", rel, err)
	}
	defer unix.Close(fd)
	if err := own(fd, mode, uid, gid); err != nil {
		return d.pathError("mkdir", rel, err)
	}
	return nil
}

// SetDir gives a directory that exists the mode and the owner. It refuses a path that is not
// a directory.
func (d *Dir) SetDir(rel string, mode os.FileMode, uid, gid int) error {
	c, err := clean(rel)
	if err != nil {
		return err
	}
	fd, err := openBeneath(d.fd, c, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return d.pathError("chmod", rel, err)
	}
	defer unix.Close(fd)
	if err := own(fd, mode, uid, gid); err != nil {
		return d.pathError("chmod", rel, err)
	}
	return nil
}

// WriteFile writes a file in one step: a new file with a random name gets the content, the
// mode and the owner, and then takes the name. It returns true when the file changed. The
// parent directory must exist.
func (d *Dir) WriteFile(rel string, data []byte, mode os.FileMode, uid, gid int) (bool, error) {
	pfd, name, err := d.parent("write", rel)
	if err != nil {
		return false, err
	}
	defer unix.Close(pfd)
	if same(pfd, name, data, mode, uid, gid) {
		return false, nil
	}
	var st unix.Stat_t
	if err := unix.Fstatat(pfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return false, d.pathError("write", rel, unix.EISDIR)
	}
	tmp := "." + name + "." + random()
	fd, err := unix.Openat(pfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return false, d.pathError("write", rel, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.base, rel))
	_, err = f.Write(data)
	if err == nil {
		err = own(fd, mode, uid, gid)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(pfd, tmp, pfd, name)
	}
	if err != nil {
		unix.Unlinkat(pfd, tmp, 0)
		return false, d.pathError("write", rel, err)
	}
	return true, nil
}

// same reports whether the file name in the directory pfd is a regular file with the
// content, the mode and the owner.
func same(pfd int, name string, data []byte, mode os.FileMode, uid, gid int) bool {
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int64(len(data)) != st.Size {
		return false
	}
	if os.FileMode(st.Mode).Perm() != mode.Perm() || (uid != Keep && int(st.Uid) != uid) || (gid != Keep && int(st.Gid) != gid) {
		return false
	}
	old, err := io.ReadAll(f)
	return err == nil && bytes.Equal(old, data)
}

func random() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Remove removes a file, a link or an empty directory. A link is removed, not its target.
func (d *Dir) Remove(rel string) error {
	pfd, name, err := d.parent("remove", rel)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	err = unix.Unlinkat(pfd, name, 0)
	if err == unix.EISDIR || err == unix.EPERM {
		err = unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR)
	}
	if err != nil {
		return d.pathError("remove", rel, err)
	}
	return nil
}

// RemoveAll removes a path and all that is in it. It removes a link, not its target, and it
// never leaves the directory tree. A path that does not exist is not an error.
func (d *Dir) RemoveAll(rel string) error {
	pfd, name, err := d.parent("remove", rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := removeAllAt(pfd, name); err != nil {
		return d.pathError("remove", rel, err)
	}
	return nil
}

func removeAllAt(pfd int, name string) error {
	err := unix.Unlinkat(pfd, name, 0)
	if err == nil || err == unix.ENOENT {
		return nil
	}
	if err != unix.EISDIR && err != unix.EPERM {
		return err
	}
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	for {
		names, rerr := f.Readdirnames(256)
		for _, n := range names {
			if err := removeAllAt(fd, n); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	err = unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR)
	if err == unix.ENOENT {
		return nil
	}
	return err
}

// Rename gives a path a new name in one step. Both paths are below the directory.
func (d *Dir) Rename(from, to string) error {
	ffd, fname, err := d.parent("rename", from)
	if err != nil {
		return err
	}
	defer unix.Close(ffd)
	tfd, tname, err := d.parent("rename", to)
	if err != nil {
		return err
	}
	defer unix.Close(tfd)
	if err := unix.Renameat(ffd, fname, tfd, tname); err != nil {
		return d.pathError("rename", from, err)
	}
	return nil
}

// Symlink makes rel a symbolic link to target, in one step. It replaces a file or a link
// with that name. The target is text: this package never opens it.
func (d *Dir) Symlink(target, rel string) error {
	pfd, name, err := d.parent("symlink", rel)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	buf := make([]byte, 4096)
	if n, err := unix.Readlinkat(pfd, name, buf); err == nil && string(buf[:n]) == target {
		return nil
	}
	tmp := "." + name + "." + random()
	if err := unix.Symlinkat(target, pfd, tmp); err != nil {
		return d.pathError("symlink", rel, err)
	}
	if err := unix.Renameat(pfd, tmp, pfd, name); err != nil {
		unix.Unlinkat(pfd, tmp, 0)
		return d.pathError("symlink", rel, err)
	}
	return nil
}

// Chown changes the owner of a path. It changes a link, not its target.
func (d *Dir) Chown(rel string, uid, gid int) error {
	pfd, name, err := d.parent("chown", rel)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := unix.Fchownat(pfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return d.pathError("chown", rel, err)
	}
	return nil
}

// ChownTree changes the owner of a path and of all that is in it. It changes a link, not its
// target, and it never leaves the directory tree.
func (d *Dir) ChownTree(rel string, uid, gid int) error {
	pfd, name, err := d.parent("chown", rel)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := chownTreeAt(pfd, name, uid, gid); err != nil {
		return d.pathError("chown", rel, err)
	}
	return nil
}

func chownTreeAt(pfd int, name string, uid, gid int) error {
	if err := unix.Fchownat(pfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	fd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOTDIR || err == unix.ELOOP {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	for {
		names, rerr := f.Readdirnames(256)
		for _, n := range names {
			if err := chownTreeAt(fd, n, uid, gid); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// ReadDir gives the entries of a directory, sorted by name.
func (d *Dir) ReadDir(rel string) ([]fs.DirEntry, error) {
	c, err := clean(rel)
	if err != nil {
		return nil, err
	}
	fd, err := openBeneath(d.fd, c, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, d.pathError("readdir", rel, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.base, c))
	defer f.Close()
	return f.ReadDir(-1)
}

// Size gives the size of the regular files in a path and in all that is in it. It does not
// follow links. A part that it cannot read counts as 0.
func (d *Dir) Size(rel string) int64 {
	c, err := clean(rel)
	if err != nil {
		return 0
	}
	fd, err := openBeneath(d.fd, c, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return 0
	}
	f := os.NewFile(uintptr(fd), c)
	defer f.Close()
	return sizeOf(f)
}

func sizeOf(dir *os.File) int64 {
	var total int64
	for {
		names, rerr := dir.Readdirnames(256)
		for _, n := range names {
			var st unix.Stat_t
			if unix.Fstatat(int(dir.Fd()), n, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				total += st.Size
			case unix.S_IFDIR:
				fd, err := unix.Openat(int(dir.Fd()), n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					continue
				}
				sub := os.NewFile(uintptr(fd), n)
				total += sizeOf(sub)
				sub.Close()
			}
		}
		if rerr != nil {
			return total
		}
	}
}
