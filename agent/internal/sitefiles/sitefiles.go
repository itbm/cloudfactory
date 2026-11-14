// Package sitefiles does the file work of the file manager of the panel in the directory of
// one website.
//
// The agent starts this code as the Unix user of the website ("cf-agent site-files"), not as
// root and not as the worker of the agent. Thus two limits apply to each request:
//
//   - The kernel gives the process only the rights of the website user.
//   - Each path opens in an os.Root of the directory of the website. A path with ".." or a
//     symbolic link that goes out of that directory gives an error.
//
// A request is one JSON value on the standard input. The answer is one JSON value on the
// standard output.
package sitefiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// The operations of a request.
const (
	OpList   = "list"
	OpRead   = "read"
	OpWrite  = "write"
	OpMkdir  = "mkdir"
	OpMove   = "move"
	OpDelete = "delete"
)

// The codes of an error. The agent gives each code its own status to the panel.
const (
	CodeNotFound = "not_found"
	CodeExists   = "exists"
	CodeInvalid  = "invalid"
	CodeTooLarge = "too_large"
	CodeDenied   = "denied"
	CodeFull     = "full"
	CodeFailed   = "failed"
)

// Limits of one request.
const (
	// MaxEntries is the largest number of entries in the list of a directory.
	MaxEntries = 2000
	// MaxRead is the largest file that the file manager reads. Larger files are for SFTP.
	MaxRead = 2 << 20
	// MaxWrite is the largest file that the file manager writes.
	MaxWrite = 5 << 20
)

// Request is one operation on a path. Path and To start with "/", which is the directory of
// the website.
type Request struct {
	Op   string `json:"op"`
	Path string `json:"path"`
	// To is the new path of a move.
	To string `json:"to,omitempty"`
	// Content is the content of a file to write.
	Content []byte `json:"content,omitempty"`
	// CreateOnly makes a write fail when the file exists.
	CreateOnly bool `json:"createOnly,omitempty"`
}

// Entry is one file or directory.
type Entry struct {
	Name string `json:"name"`
	// Type is "file", "directory", "link" or "other".
	Type      string    `json:"type"`
	SizeBytes int64     `json:"sizeBytes"`
	Modified  time.Time `json:"modified"`
	// Mode has the permission bits, for example 0640.
	Mode uint32 `json:"mode"`
	// Target is the target of a symbolic link.
	Target string `json:"target,omitempty"`
}

// Response is the answer to a request.
type Response struct {
	Entries   []Entry `json:"entries,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Entry     *Entry  `json:"entry,omitempty"`
	Content   []byte  `json:"content,omitempty"`
	// Code and Error are set when the operation failed.
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// Error is an error with a code.
type Error struct {
	Code string
	Text string
}

func (e *Error) Error() string { return e.Text }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Text: fmt.Sprintf(format, args...)}
}

// CheckPath checks a path of a request: it starts with "/", and it has no empty part, no "."
// part, no ".." part, no control character and no "/" at the end. The rule "filePath" of
// schema/input-rules.json is the same rule.
func CheckPath(p string) error {
	if p == "/" {
		return nil
	}
	if len(p) > 1024 || !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return fail(CodeInvalid, "the path is not valid")
	}
	for _, part := range strings.Split(p[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return fail(CodeInvalid, "the path is not valid")
		}
		for _, c := range part {
			if c < 0x20 || c == 0x7f {
				return fail(CodeInvalid, "the path is not valid")
			}
		}
	}
	return nil
}

// local gives the path for os.Root: "." for "/", and "a/b" for "/a/b".
func local(p string) string {
	if p == "/" {
		return "."
	}
	return p[1:]
}

// Main reads one request from in, does it in the directory dir and writes the answer to out.
// It gives an error only when it cannot write the answer.
func Main(dir string, in io.Reader, out io.Writer) error {
	var req Request
	resp := Response{}
	if err := json.NewDecoder(io.LimitReader(in, 2*MaxWrite+1<<16)).Decode(&req); err != nil {
		resp.Code, resp.Error = CodeInvalid, "the request is not valid"
	} else if r, err := Do(dir, req); err != nil {
		resp.Code, resp.Error = codeOf(err), message(err)
	} else {
		resp = r
	}
	return json.NewEncoder(out).Encode(resp)
}

// codeOf gives the code of an error of the file system.
func codeOf(err error) string {
	var e *Error
	switch {
	case errors.As(err, &e):
		return e.Code
	case errors.Is(err, fs.ErrNotExist):
		return CodeNotFound
	case errors.Is(err, fs.ErrExist), errors.Is(err, syscall.ENOTEMPTY):
		return CodeExists
	case errors.Is(err, fs.ErrPermission):
		return CodeDenied
	case errors.Is(err, syscall.EDQUOT), errors.Is(err, syscall.ENOSPC):
		return CodeFull
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EISDIR), errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.EXDEV):
		return CodeInvalid
	}
	// os.Root gives an error with no type for a path that goes out of the directory.
	if strings.Contains(err.Error(), "path escapes from parent") {
		return CodeDenied
	}
	return CodeFailed
}

// message gives a text for the user. It has no path of the host: the paths of the errors of
// os.Root are the paths in the directory of the website.
func message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Text
	}
	switch codeOf(err) {
	case CodeNotFound:
		return "the file or the directory does not exist"
	case CodeExists:
		return "a file or a directory with this name exists already"
	case CodeDenied:
		return "the website user cannot open this path, or the path goes out of the directory of the website"
	case CodeFull:
		return "the website has no free disk space"
	case CodeInvalid:
		return "the path is not a file or a directory of the correct type"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	return "the operation failed"
}

// Do does one request in the directory dir.
func Do(dir string, req Request) (Response, error) {
	if err := CheckPath(req.Path); err != nil {
		return Response{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Response{}, fail(CodeFailed, "the directory of the website is not available")
	}
	defer root.Close()
	switch req.Op {
	case OpList:
		return list(root, req.Path)
	case OpRead:
		return read(root, req.Path)
	case OpWrite:
		return write(root, req)
	case OpMkdir:
		if req.Path == "/" {
			return Response{}, fail(CodeExists, "the directory exists already")
		}
		if err := root.Mkdir(local(req.Path), 0o750); err != nil {
			return Response{}, err
		}
		return entryOf(root, req.Path)
	case OpMove:
		return move(root, req)
	case OpDelete:
		if req.Path == "/" {
			return Response{}, fail(CodeInvalid, "the directory of the website cannot be removed")
		}
		if _, err := root.Lstat(local(req.Path)); err != nil {
			return Response{}, err
		}
		return Response{}, root.RemoveAll(local(req.Path))
	}
	return Response{}, fail(CodeInvalid, "the operation %q is not valid", req.Op)
}

func toEntry(root *os.Root, dir string, fi fs.FileInfo) Entry {
	e := Entry{Name: fi.Name(), Type: "other", SizeBytes: fi.Size(), Modified: fi.ModTime().UTC().Truncate(time.Second), Mode: uint32(fi.Mode().Perm())}
	switch {
	case fi.Mode().IsRegular():
		e.Type = "file"
	case fi.IsDir():
		e.Type, e.SizeBytes = "directory", 0
	case fi.Mode()&fs.ModeSymlink != 0:
		e.Type, e.SizeBytes = "link", 0
		if target, err := root.Readlink(path.Join(local(dir), fi.Name())); err == nil {
			e.Target = target
		}
	}
	return e
}

func entryOf(root *os.Root, p string) (Response, error) {
	fi, err := root.Lstat(local(p))
	if err != nil {
		return Response{}, err
	}
	e := toEntry(root, path.Dir(p), fi)
	if p == "/" {
		e.Name = "/"
	}
	return Response{Entry: &e}, nil
}

// list gives the entries of a directory: the directories first, then the files, each by name.
func list(root *os.Root, p string) (Response, error) {
	f, err := root.Open(local(p))
	if err != nil {
		return Response{}, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return Response{}, err
	} else if !fi.IsDir() {
		return Response{}, fail(CodeInvalid, "the path is not a directory")
	}
	infos, err := f.Readdir(MaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return Response{}, err
	}
	resp := Response{Truncated: len(infos) > MaxEntries, Entries: []Entry{}}
	if resp.Truncated {
		infos = infos[:MaxEntries]
	}
	for _, fi := range infos {
		resp.Entries = append(resp.Entries, toEntry(root, p, fi))
	}
	sort.Slice(resp.Entries, func(i, j int) bool {
		a, b := resp.Entries[i], resp.Entries[j]
		if (a.Type == "directory") != (b.Type == "directory") {
			return a.Type == "directory"
		}
		return a.Name < b.Name
	})
	return resp, nil
}

func read(root *os.Root, p string) (Response, error) {
	f, err := root.Open(local(p))
	if err != nil {
		return Response{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Response{}, err
	}
	if !fi.Mode().IsRegular() {
		return Response{}, fail(CodeInvalid, "the path is not a file")
	}
	if fi.Size() > MaxRead {
		return Response{}, fail(CodeTooLarge, "the file has more than %d MB; use SFTP to download it", MaxRead>>20)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxRead+1))
	if err != nil {
		return Response{}, err
	}
	if len(b) > MaxRead {
		return Response{}, fail(CodeTooLarge, "the file has more than %d MB; use SFTP to download it", MaxRead>>20)
	}
	e := toEntry(root, path.Dir(p), fi)
	e.Name = path.Base(p)
	return Response{Entry: &e, Content: b}, nil
}

// write writes a file in one step: it writes a temporary file in the same directory and then
// gives it the name of the file. A file that exists keeps its permission bits.
func write(root *os.Root, req Request) (Response, error) {
	if req.Path == "/" {
		return Response{}, fail(CodeInvalid, "the path is not a file")
	}
	if len(req.Content) > MaxWrite {
		return Response{}, fail(CodeTooLarge, "the file has more than %d MB; use SFTP to upload it", MaxWrite>>20)
	}
	name := local(req.Path)
	mode := fs.FileMode(0o640)
	if fi, err := root.Lstat(name); err == nil {
		switch {
		case req.CreateOnly:
			return Response{}, fail(CodeExists, "a file or a directory with this name exists already")
		case !fi.Mode().IsRegular():
			return Response{}, fail(CodeInvalid, "the path is not a file")
		}
		mode = fi.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Response{}, err
	}
	tmp := path.Join(path.Dir(name), ".cf-upload-"+fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Response{}, err
	}
	_, err = f.Write(req.Content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Chmod(tmp, mode)
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return Response{}, err
	}
	return entryOf(root, req.Path)
}

// move gives a file or a directory a new path. It does not replace a path that exists.
func move(root *os.Root, req Request) (Response, error) {
	if err := CheckPath(req.To); err != nil {
		return Response{}, err
	}
	if req.Path == "/" || req.To == "/" {
		return Response{}, fail(CodeInvalid, "the directory of the website cannot be moved")
	}
	if req.To == req.Path || strings.HasPrefix(req.To, req.Path+"/") {
		return Response{}, fail(CodeInvalid, "a directory cannot be moved into itself")
	}
	if _, err := root.Lstat(local(req.Path)); err != nil {
		return Response{}, err
	}
	if _, err := root.Lstat(local(req.To)); err == nil {
		return Response{}, fail(CodeExists, "a file or a directory with this name exists already")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Response{}, err
	}
	if err := root.Rename(local(req.Path), local(req.To)); err != nil {
		return Response{}, err
	}
	return entryOf(root, req.To)
}

// IsText reports whether the content of a file is text: valid UTF-8 with no NUL byte.
func IsText(b []byte) bool {
	return utf8.Valid(b) && !strings.ContainsRune(string(b), 0)
}
