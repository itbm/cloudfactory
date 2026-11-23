package sitefiles

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The archives of the file manager. The program makes and extracts each archive itself, as
// the Unix user of the website, with the os.Root of the directory of the website:
//
//   - A new archive gets the files, the directories and the links. The program does not go
//     through a link. It writes a temporary file and then gives it the name of the archive,
//     so that a stopped job leaves no part of an archive.
//   - An extract writes all files in an os.Root of the destination directory. A name with
//     "..", or a link that goes out of the destination, thus cannot change a file out of that
//     directory. The program skips the names that are not safe, the devices and the FIFOs. It
//     does not use the owner of an entry, and it removes the bits setuid, setgid and sticky.
//   - The archive formats are the formats that the standard library of Go reads: zip, tar,
//     tar with gzip and tar with bzip2. The program makes zip, tar and tar with gzip.

// The limits of an extract. The disk limit of the website applies too.
const (
	// MaxExtractEntries is the largest number of entries in an archive that the file manager
	// extracts.
	MaxExtractEntries = 1_000_000
	// MaxExtractBytes is the largest total size of the files in an archive that the file
	// manager extracts. It stops a small archive with a very large content.
	MaxExtractBytes = 64 << 30
	// maxSkippedNames is the largest number of skipped names in a summary.
	maxSkippedNames = 20
	// maxLinkTarget is the largest target of a link in an archive.
	maxLinkTarget = 4095
)

// extractLimits are the limits that an extract uses. The tests make them smaller.
var extractLimits = struct {
	entries int
	bytes   int64
}{MaxExtractEntries, MaxExtractBytes}

// tooManyEntries and tooLarge are the errors of an archive that is too large.
func tooManyEntries() error {
	return fail(CodeTooLarge, "the archive has more than %d entries; use SFTP", extractLimits.entries)
}

func tooLarge() error {
	return fail(CodeTooLarge, "the files in the archive have more than %s; use SFTP", sizeText(extractLimits.bytes))
}

// sizeText gives a size in GiB, or in bytes when it is smaller than 1 GiB.
func sizeText(n int64) string {
	if n >= 1<<30 {
		return strconv.FormatInt(n>>30, 10) + " GiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// Summary is the result of an archive operation.
type Summary struct {
	Files       int `json:"files"`
	Directories int `json:"directories"`
	Links       int `json:"links"`
	// SizeBytes is the size of the new archive, or the size of the files that an extract
	// wrote.
	SizeBytes int64 `json:"sizeBytes"`
	// Kept is the number of files that an extract did not replace, because they exist.
	Kept int `json:"kept"`
	// Skipped is the number of entries that the operation did not use: names that are not
	// safe, devices, FIFOs and sockets, and names that are a directory in one place and a
	// file in the other. SkippedNames has the first of them.
	Skipped      int      `json:"skipped"`
	SkippedNames []string `json:"skippedNames,omitempty"`
}

func (s *Summary) skip(name string) {
	s.Skipped++
	if len(s.SkippedNames) < maxSkippedNames {
		s.SkippedNames = append(s.SkippedNames, printable(name))
	}
}

// printable gives a name for the summary: valid UTF-8 with no control character, and at most
// 200 bytes. The name comes from the archive, which the website user controls.
func printable(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(name, "?"))
	if len(name) > 200 {
		name = strings.ToValidUTF8(name[:200], "") + "…"
	}
	return name
}

// The formats of an archive.
type format int

const (
	formatZip format = iota + 1
	formatTar
	formatTarGzip
	formatTarBzip2
)

// formatOf gives the format of an archive from the end of its name.
func formatOf(name string) (format, bool) {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".zip"):
		return formatZip, true
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"):
		return formatTarGzip, true
	case strings.HasSuffix(n, ".tar.bz2"), strings.HasSuffix(n, ".tbz2"):
		return formatTarBzip2, true
	case strings.HasSuffix(n, ".tar"):
		return formatTar, true
	}
	return 0, false
}

// archiveWriter writes the entries of a new archive.
type archiveWriter interface {
	// add writes the header of one entry and gives the writer for its content. For a link,
	// the content is not necessary: target is the target of the link.
	add(name string, fi fs.FileInfo, size int64, target string) (io.Writer, error)
	Close() error
}

type tarWriter struct {
	tw *tar.Writer
	gz *gzip.Writer
}

func (w *tarWriter) add(name string, fi fs.FileInfo, size int64, target string) (io.Writer, error) {
	// The header has no owner: the user IDs of this server have no use on a different server.
	hdr := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime().Truncate(time.Second)}
	switch {
	case fi.IsDir():
		hdr.Typeflag, hdr.Name = tar.TypeDir, name+"/"
	case fi.Mode()&fs.ModeSymlink != 0:
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, target
	default:
		hdr.Typeflag, hdr.Size = tar.TypeReg, size
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	return w.tw, nil
}

func (w *tarWriter) Close() error {
	err := w.tw.Close()
	if w.gz != nil {
		err = errors.Join(err, w.gz.Close())
	}
	return err
}

type zipWriter struct{ zw *zip.Writer }

func (w *zipWriter) add(name string, fi fs.FileInfo, _ int64, target string) (io.Writer, error) {
	hdr := &zip.FileHeader{Name: name, Modified: fi.ModTime(), Method: zip.Deflate}
	switch {
	case fi.IsDir():
		hdr.Name, hdr.Method = name+"/", zip.Store
		hdr.SetMode(fs.ModeDir | fi.Mode().Perm())
	case fi.Mode()&fs.ModeSymlink != 0:
		// The content of a link entry is its target (the format of Info-ZIP).
		hdr.Method = zip.Store
		hdr.SetMode(fs.ModeSymlink | 0o777)
	default:
		hdr.SetMode(fi.Mode().Perm())
	}
	out, err := w.zw.CreateHeader(hdr)
	if err == nil && target != "" {
		_, err = io.WriteString(out, target)
	}
	return out, err
}

func (w *zipWriter) Close() error { return w.zw.Close() }

// makeArchive puts the files and the directories req.Paths in a new archive req.Path. Each
// path goes in the archive with its name, at the top level. The path "/" puts all files of
// the website at the top level.
func makeArchive(root *os.Root, req Request) (Response, error) {
	f, ok := formatOf(req.Path)
	if !ok || f == formatTarBzip2 || req.Path == "/" {
		return Response{}, fail(CodeInvalid, "the name of the archive must end with .zip, .tar.gz, .tgz or .tar")
	}
	switch {
	case len(req.Paths) == 0:
		return Response{}, fail(CodeInvalid, "select the files and the folders for the archive")
	case len(req.Paths) > validate.MaxArchivePaths:
		return Response{}, fail(CodeInvalid, "an archive can have at most %d selected paths; select a folder", validate.MaxArchivePaths)
	}
	names := map[string]bool{}
	for _, p := range req.Paths {
		if err := CheckPath(p); err != nil {
			return Response{}, err
		}
		if p == "/" && len(req.Paths) > 1 {
			return Response{}, fail(CodeInvalid, "the directory of the website contains all other paths; select it alone")
		}
		if names[path.Base(p)] {
			return Response{}, fail(CodeInvalid, "two of the selected paths have the name %q", printable(path.Base(p)))
		}
		names[path.Base(p)] = true
		if _, err := root.Lstat(local(p)); err != nil {
			return Response{}, pathError(p, err)
		}
	}
	to := local(req.Path)
	if _, err := root.Lstat(to); err == nil {
		return Response{}, fail(CodeExists, "a file or a directory with this name exists already")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Response{}, err
	}
	tmp := tempName(path.Dir(to), ".cf-archive-")
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Response{}, err
	}
	defer removeAtStop(root, tmp)()
	buffered := bufio.NewWriterSize(out, 1<<16)
	var w archiveWriter
	switch f {
	case formatZip:
		w = &zipWriter{zip.NewWriter(buffered)}
	case formatTar:
		w = &tarWriter{tw: tar.NewWriter(buffered)}
	case formatTarGzip:
		gz := gzip.NewWriter(buffered)
		w = &tarWriter{tw: tar.NewWriter(gz), gz: gz}
	}
	a := &archiver{root: root, w: w, temp: "/" + tmp}
	for _, p := range req.Paths {
		if err = a.add(p); err != nil {
			break
		}
	}
	if err == nil {
		err = w.Close()
	}
	if err == nil {
		err = buffered.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Chmod(tmp, 0o640)
	}
	if err == nil {
		err = renameNoReplace(root, tmp, to)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return Response{}, err
	}
	resp, err := entryOf(root, req.Path)
	if err != nil {
		return Response{}, err
	}
	a.sum.SizeBytes = resp.Entry.SizeBytes
	resp.Summary = &a.sum
	return resp, nil
}

// archiver walks the paths of a new archive.
type archiver struct {
	root *os.Root
	w    archiveWriter
	// temp is the temporary file of the archive, as a path in the website. It is not in the
	// archive.
	temp string
	sum  Summary
}

// add puts one selected path in the archive.
func (a *archiver) add(site string) error {
	if site == "/" {
		return a.entries(a.root, site, "")
	}
	dir, err := a.root.OpenRoot(local(path.Dir(site)))
	if err != nil {
		return pathError(site, err)
	}
	defer dir.Close()
	fi, err := dir.Lstat(path.Base(site))
	if err != nil {
		return pathError(site, err)
	}
	return a.entry(dir, fi, site, path.Base(site))
}

// entries puts all entries of a directory in the archive. dir is the os.Root of the
// directory, site its path in the website and prefix its name in the archive.
func (a *archiver) entries(dir *os.Root, site, prefix string) error {
	infos, err := readDir(dir)
	if err != nil {
		return pathError(site, err)
	}
	for _, fi := range infos {
		name := fi.Name()
		if prefix != "" {
			name = prefix + "/" + name
		}
		if err := a.entry(dir, fi, path.Join(site, fi.Name()), name); err != nil {
			return err
		}
	}
	return nil
}

// entry puts one file, directory or link of the directory dir in the archive with the name
// name. site is its path in the website.
func (a *archiver) entry(dir *os.Root, fi fs.FileInfo, site, name string) error {
	switch {
	case site == a.temp:
		return nil
	case fi.IsDir():
		if _, err := a.w.add(name, fi, 0, ""); err != nil {
			return err
		}
		a.sum.Directories++
		sub, err := dir.OpenRoot(fi.Name())
		if err != nil {
			return pathError(site, err)
		}
		defer sub.Close()
		return a.entries(sub, site, name)
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := dir.Readlink(fi.Name())
		if err != nil {
			return pathError(site, err)
		}
		if _, err := a.w.add(name, fi, 0, target); err != nil {
			return err
		}
		a.sum.Links++
		return nil
	case !fi.Mode().IsRegular():
		// A FIFO, a socket or a device.
		a.sum.skip(site)
		return nil
	}
	f, fi, err := openFile(dir, fi.Name())
	if err != nil {
		return pathError(site, err)
	}
	defer f.Close()
	out, err := a.w.add(name, fi, fi.Size(), "")
	if err != nil {
		return err
	}
	// The size in a tar header is fixed. A file that becomes larger gives the size of the
	// header, and a file that becomes smaller gets zeros at the end, as with GNU tar.
	n, err := io.CopyN(out, f, fi.Size())
	if errors.Is(err, io.EOF) {
		_, err = io.CopyN(out, zeros{}, fi.Size()-n)
	}
	if err != nil {
		return pathError(site, err)
	}
	a.sum.Files++
	return nil
}

// zeros is a reader of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// extract extracts the archive req.Path into the directory req.To. The program makes the
// directory when it does not exist. A file that exists stays, unless req.Overwrite is true.
func extract(root *os.Root, req Request) (Response, error) {
	if err := CheckPath(req.To); err != nil {
		return Response{}, err
	}
	f, ok := formatOf(req.Path)
	if !ok {
		return Response{}, fail(CodeInvalid, "the file manager extracts .zip, .tar, .tar.gz, .tgz, .tar.bz2 and .tbz2 archives")
	}
	src, fi, err := openFile(root, local(req.Path))
	if err != nil {
		return Response{}, err
	}
	defer src.Close()
	if fi, err := root.Stat(local(req.To)); err == nil && !fi.IsDir() {
		return Response{}, fail(CodeInvalid, "the destination is a file; select a folder")
	}
	if req.To != "/" {
		if err := root.MkdirAll(local(req.To), 0o750); err != nil {
			return Response{}, pathError(req.To, err)
		}
	}
	dest, err := root.OpenRoot(local(req.To))
	if err != nil {
		return Response{}, pathError(req.To, err)
	}
	defer dest.Close()
	x := &extractor{dest: dest, to: req.To, overwrite: req.Overwrite}
	if f == formatZip {
		err = x.zip(src, fi.Size())
	} else {
		var r io.Reader = bufio.NewReaderSize(src, 1<<16)
		switch f {
		case formatTarGzip:
			gz, gerr := gzip.NewReader(r)
			if gerr != nil {
				return Response{}, fail(CodeInvalid, "the archive is not a valid gzip file")
			}
			defer gz.Close()
			r = gz
		case formatTarBzip2:
			r = bzip2.NewReader(r)
		}
		err = x.tar(r)
	}
	x.sum.SizeBytes = x.written
	if err != nil {
		return Response{}, err
	}
	return Response{Summary: &x.sum}, nil
}

// extractor writes the entries of an archive in the destination directory.
type extractor struct {
	dest      *os.Root
	to        string
	overwrite bool
	entries   int
	written   int64
	sum       Summary
}

// cleanName gives the name in the destination for a name in an archive: with no "/" at the
// start or at the end, and with no "./" at the start, as GNU tar does. It gives "" for the
// entry of the destination itself ("./"), and false for a name that is not safe, for example
// a name with "..".
func cleanName(name string) (string, bool) {
	name = strings.TrimRight(name, "/")
	for strings.HasPrefix(name, "./") || strings.HasPrefix(name, "/") {
		name = strings.TrimPrefix(strings.TrimPrefix(name, "."), "/")
	}
	if name == "" || name == "." {
		return "", true
	}
	if CheckPath("/"+name) != nil {
		return "", false
	}
	return name, true
}

// next counts one entry of the archive.
func (x *extractor) next() error {
	x.entries++
	if x.entries > extractLimits.entries {
		return tooManyEntries()
	}
	return nil
}

// site gives the path in the website of a name in the destination.
func (x *extractor) site(name string) string { return path.Join(x.to, name) }

// problem handles an error of the destination for one entry. The entry is skipped when the
// error is about that entry only: for example a file where the archive has a directory, a
// link that goes out of the destination, or a directory that the website user cannot change.
// A full disk or an unknown error stops the extract.
func (x *extractor) problem(name string, err error) error {
	switch codeOf(err) {
	case CodeExists, CodeInvalid, CodeDenied, CodeNotFound:
		x.sum.skip(x.site(name))
		return nil
	}
	return pathError(x.site(name), err)
}

// prepare makes the parent directories of an entry and checks the path of the entry. It
// gives false when the entry must not be written: the path exists and the extract keeps it,
// or the path is a directory. The caller then returns err.
func (x *extractor) prepare(name string) (write bool, err error) {
	if dir := path.Dir(name); dir != "." {
		if err := x.dest.MkdirAll(dir, 0o750); err != nil {
			return false, x.problem(name, err)
		}
	}
	fi, err := x.dest.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, x.problem(name, err)
	case fi.IsDir():
		x.sum.skip(x.site(name))
		return false, nil
	case !x.overwrite:
		x.sum.Kept++
		return false, nil
	}
	return true, nil
}

// place gives the temporary entry tmp the name name. With overwrite, it replaces a file or
// a link that exists. Without overwrite, it does not replace an entry that appeared after
// the check of prepare.
func (x *extractor) place(tmp, name string) error {
	var err error
	if x.overwrite {
		err = x.dest.Rename(tmp, name)
	} else {
		err = renameNoReplace(x.dest, tmp, name)
		if err != nil && codeOf(err) == CodeExists {
			_ = x.dest.Remove(tmp)
			x.sum.Kept++
			return nil
		}
	}
	if err != nil {
		_ = x.dest.Remove(tmp)
		return x.problem(name, err)
	}
	return nil
}

// filePerm gives the permission bits of an extracted file: the bits of the archive with no
// write permission for the group and the others (the umask 022), and with read and write
// permission for the owner.
func filePerm(mode fs.FileMode) fs.FileMode { return mode.Perm()&^0o022 | 0o600 }

// dirPerm gives the permission bits of an extracted directory, with all permissions for the
// owner, so that the extract can write in it.
func dirPerm(mode fs.FileMode) fs.FileMode { return mode.Perm()&^0o022 | 0o700 }

func (x *extractor) directory(name string, mode fs.FileMode) error {
	fi, err := x.dest.Lstat(name)
	switch {
	case err == nil && !fi.IsDir():
		x.sum.skip(x.site(name))
		return nil
	case err == nil:
		// A directory that exists keeps its permissions, unless the extract replaces files.
		if x.overwrite {
			if err := x.dest.Chmod(name, dirPerm(mode)); err != nil {
				return x.problem(name, err)
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return x.problem(name, err)
	default:
		if err := x.dest.MkdirAll(name, 0o750); err != nil {
			return x.problem(name, err)
		}
		if err := x.dest.Chmod(name, dirPerm(mode)); err != nil {
			return x.problem(name, err)
		}
	}
	x.sum.Directories++
	return nil
}

// file writes one file. modified is the time of the last change in the archive.
func (x *extractor) file(name string, mode fs.FileMode, modified time.Time, r io.Reader) error {
	if write, err := x.prepare(name); !write {
		return err
	}
	tmp := tempName(path.Dir(name), ".cf-extract-")
	out, err := x.dest.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return x.problem(name, err)
	}
	defer removeAtStop(x.dest, tmp)()
	n, err := io.Copy(out, &budget{r: r, left: extractLimits.bytes - x.written})
	x.written += n
	if err == nil {
		err = out.Chmod(filePerm(mode))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && !modified.IsZero() {
		err = x.dest.Chtimes(tmp, modified, modified)
	}
	if err != nil {
		_ = x.dest.Remove(tmp)
		return pathError(x.site(name), err)
	}
	if err := x.place(tmp, name); err != nil {
		return err
	}
	x.sum.Files++
	return nil
}

func (x *extractor) symlink(name, target string) error {
	if target == "" || len(target) > maxLinkTarget || strings.ContainsRune(target, 0) {
		x.sum.skip(x.site(name))
		return nil
	}
	if write, err := x.prepare(name); !write {
		return err
	}
	tmp := tempName(path.Dir(name), ".cf-extract-")
	if err := x.dest.Symlink(target, tmp); err != nil {
		return x.problem(name, err)
	}
	if err := x.place(tmp, name); err != nil {
		return err
	}
	x.sum.Links++
	return nil
}

// hardlink makes a hard link to a file that the archive has before it. The two names are in
// the destination.
func (x *extractor) hardlink(name, oldname string) error {
	old, ok := cleanName(oldname)
	if !ok || old == "" {
		x.sum.skip(x.site(name))
		return nil
	}
	if write, err := x.prepare(name); !write {
		return err
	}
	tmp := tempName(path.Dir(name), ".cf-extract-")
	if err := x.dest.Link(old, tmp); err != nil {
		return x.problem(name, err)
	}
	if err := x.place(tmp, name); err != nil {
		return err
	}
	// A rename between two names of the same file changes nothing, so the temporary name can
	// stay when the name was a link to the same file before.
	_ = x.dest.Remove(tmp)
	x.sum.Files++
	return nil
}

func (x *extractor) zip(src *os.File, size int64) error {
	zr, err := zip.NewReader(src, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return fail(CodeInvalid, "the archive is not a valid zip file")
	}
	// The sizes in the central directory come first: an archive that is too large fails before
	// a change. The sizes can be false, so the writes count the bytes too.
	if len(zr.File) > extractLimits.entries {
		return tooManyEntries()
	}
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
	}
	if total > uint64(extractLimits.bytes) {
		return tooLarge()
	}
	for _, f := range zr.File {
		if err := x.next(); err != nil {
			return err
		}
		// Some programs on Windows write "\" between the parts of a name.
		name, ok := cleanName(strings.ReplaceAll(f.Name, `\`, "/"))
		if !ok {
			x.sum.skip(f.Name)
			continue
		}
		if name == "" {
			continue
		}
		if f.Flags&0x1 != 0 {
			return fail(CodeInvalid, "the archive is protected with a password; the file manager cannot extract it")
		}
		mode := f.Mode()
		switch {
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			err = x.directory(name, mode)
		case mode&fs.ModeSymlink != 0:
			var target []byte
			target, err = readZip(f, maxLinkTarget+1)
			if err == nil {
				err = x.symlink(name, string(target))
			}
		case mode.IsRegular():
			err = x.zipFile(name, f)
		default:
			x.sum.skip(x.site(name))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *extractor) zipFile(name string, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return damaged(err)
	}
	defer rc.Close()
	return x.file(name, f.Mode(), f.Modified, archiveReader{rc})
}

// readZip reads the content of a small entry, for example the target of a link.
func readZip(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, damaged(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return nil, damaged(err)
	}
	return b, nil
}

func (x *extractor) tar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			return damaged(err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if err := x.next(); err != nil {
			return err
		}
		name, ok := cleanName(hdr.Name)
		if !ok {
			x.sum.skip(hdr.Name)
			continue
		}
		if name == "" {
			continue
		}
		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = x.directory(name, mode)
		case tar.TypeReg:
			err = x.file(name, mode, hdr.ModTime, archiveReader{tr})
		case tar.TypeSymlink:
			err = x.symlink(name, hdr.Linkname)
		case tar.TypeLink:
			err = x.hardlink(name, hdr.Linkname)
		default:
			// A device, a FIFO or a different type.
			x.sum.skip(x.site(name))
		}
		if err != nil {
			return err
		}
	}
}

// damaged gives the error of an archive that the program cannot read.
func damaged(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	switch {
	case errors.Is(err, zip.ErrAlgorithm):
		return fail(CodeInvalid, "the archive uses a compression method that the file manager cannot read")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fail(CodeInvalid, "the archive is not complete")
	}
	return fail(CodeInvalid, "the archive is damaged: %s", printable(err.Error()))
}

// archiveReader gives each read error of an archive as the error of a damaged archive, so
// that it is different from an error of the destination.
type archiveReader struct{ r io.Reader }

func (a archiveReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = damaged(err)
	}
	return n, err
}

// budget stops an extract that writes more than the limit of bytes.
type budget struct {
	r    io.Reader
	left int64
}

func (b *budget) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One more byte shows if the content continues.
		var one [1]byte
		if n, _ := b.r.Read(one[:]); n > 0 {
			return 0, tooLarge()
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}
