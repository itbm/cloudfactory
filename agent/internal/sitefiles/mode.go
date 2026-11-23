package sitefiles

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The change of the permission bits of a file or a directory. The program runs as the Unix
// user of the website, so the kernel lets it change only the files of that user. The bits
// setuid, setgid and sticky are not permitted.

// ownerDirectory is the permission that the owner of a directory must keep: read and search.
// Without it, the file manager, SFTP and the website cannot open the directory.
const ownerDirectory = 0o500

// ParseMode reads the permission bits of a change of mode: 3 octal digits, with a 0 before
// them or not, for example "0644" or "755" (the rule "fileMode" of schema/input-rules.json).
func ParseMode(s string) (fs.FileMode, error) {
	if validate.FileMode(s) != nil {
		return 0, fail(CodeInvalid, "the permissions %q are not valid; use 3 octal digits, for example 644 or 0755", s)
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fail(CodeInvalid, "the permissions %q are not valid; use 3 octal digits, for example 644 or 0755", s)
	}
	return fs.FileMode(n), nil
}

func checkDirectoryMode(mode fs.FileMode) error {
	if mode&ownerDirectory != ownerDirectory {
		return fail(CodeInvalid, "the owner must keep the read and the search permission of a directory (for example 0755 or 0700)")
	}
	return nil
}

// chmod changes the permission bits of a file or a directory. A recursive change also
// changes each directory (req.Mode) and each file (req.FileMode) in the directory.
func chmod(root *os.Root, req Request) (Response, error) {
	mode, err := ParseMode(req.Mode)
	if err != nil {
		return Response{}, err
	}
	name := local(req.Path)
	fi, err := root.Lstat(name)
	if err != nil {
		return Response{}, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return Response{}, fail(CodeInvalid, "a link has no permissions of its own; change the permissions of its target")
	case fi.IsDir():
		if err := checkDirectoryMode(mode); err != nil {
			return Response{}, err
		}
	case !fi.Mode().IsRegular():
		return Response{}, fail(CodeInvalid, "the path is not a file or a directory")
	case req.Recursive:
		return Response{}, fail(CodeInvalid, "the path is a file; only a directory has files to change")
	}
	changed := 1
	if req.Recursive {
		fileMode, err := ParseMode(req.FileMode)
		if err != nil {
			return Response{}, err
		}
		changed, err = chmodTree(root, name, req.Path, mode, fileMode)
		if err != nil {
			return Response{}, err
		}
	} else if err := root.Chmod(name, mode); err != nil {
		return Response{}, err
	}
	resp, err := entryOf(root, req.Path)
	resp.Changed = changed
	return resp, err
}

// chmodTree changes the directory name in root and all that is in it: dirMode for each
// directory and fileMode for each file. It does not change a link and does not go through a
// link. Each directory opens as an os.Root of its own, so that each change has a name with
// one part. site is the path of the directory in the website, for the errors.
//
// The mode of a directory changes before its entries. dirMode lets the owner read and search
// the directory, so the change of the entries is possible after it.
func chmodTree(root *os.Root, name, site string, dirMode, fileMode fs.FileMode) (int, error) {
	if err := root.Chmod(name, dirMode); err != nil {
		return 0, pathError(site, err)
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return 1, pathError(site, err)
	}
	defer dir.Close()
	infos, err := readDir(dir)
	if err != nil {
		return 1, pathError(site, err)
	}
	changed := 1
	for _, fi := range infos {
		child := path.Join(site, fi.Name())
		switch {
		case fi.IsDir():
			n, err := chmodTree(dir, fi.Name(), child, dirMode, fileMode)
			changed += n
			if err != nil {
				return changed, err
			}
		case fi.Mode().IsRegular():
			if err := dir.Chmod(fi.Name(), fileMode); err != nil {
				return changed, pathError(child, err)
			}
			changed++
		}
	}
	return changed, nil
}

// readDir gives all entries of the directory of root, sorted by name. It closes the
// directory before it returns, so that a deep tree does not keep a descriptor open for each
// level.
func readDir(root *os.Root) ([]fs.FileInfo, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	return infos, nil
}

// pathError gives an error with the path in the website, for an operation on many paths.
func pathError(site string, err error) error {
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return &Error{Code: codeOf(err), Text: site + ": " + message(err)}
}
