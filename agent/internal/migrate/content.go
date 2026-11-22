package migrate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// FileStats tells what WriteFiles wrote.
type FileStats struct {
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	Skipped int   `json:"skipped"`
	// Rewritten are the files that a rewrite changed.
	Rewritten []string `json:"rewritten,omitempty"`
}

// FileOptions change what WriteFiles writes.
type FileOptions struct {
	// Exclude are directories (relative to the directory of the files) that the stream does
	// not have, with their contents.
	Exclude []string
	// Rewrite changes the content of a file, by its path relative to the directory of the
	// files. The function gives the new content, and false to keep the file as it is.
	Rewrite map[string]func(old []byte) ([]byte, bool)
}

// maxRewrite is the largest file that a rewrite reads.
const maxRewrite = 1 << 20

// WriteFiles writes the members below dir to w as a new tar stream, with names relative to
// dir. A tar process of the Unix user of the website reads the stream, so the user writes each
// file. The stream has directories, regular files and symbolic links only. It does not have:
//
//   - the directories in opts.Exclude and their contents;
//   - hard links, devices and FIFOs;
//   - a member below a symbolic link of the stream, which a tar process can follow;
//   - the set-user-ID, set-group-ID and sticky bits.
func WriteFiles(ctx context.Context, a *Archive, dir string, opts FileOptions, w io.Writer) (FileStats, error) {
	var st FileStats
	tw := tar.NewWriter(w)
	links := map[string]bool{}
	underLink := func(rel string) bool {
		for p := rel; ; {
			i := strings.LastIndex(p, "/")
			if i < 0 {
				return false
			}
			p = p[:i]
			if links[p] {
				return true
			}
		}
	}
	walk := *a
	walk.Expand = ExpandFor(dir)
	found := false
	err := walk.Walk(ctx, func(e Entry, r io.Reader) error {
		if !Within(e.Name, dir) {
			return nil
		}
		found = true
		rel := Rel(e.Name, dir)
		if rel == "" {
			return nil
		}
		for _, x := range opts.Exclude {
			if Within(rel, x) {
				return nil
			}
		}
		if underLink(rel) {
			st.Skipped++
			return nil
		}
		h := &tar.Header{Name: rel, ModTime: e.ModTime, Format: tar.FormatPAX}
		switch e.Type {
		case TypeDir:
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, rel+"/", e.Mode&0o777|0o700
		case TypeFile:
			h.Typeflag, h.Size, h.Mode = tar.TypeReg, e.Size, e.Mode&0o777|0o600
		case TypeSymlink:
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, e.Link, 0o777
			links[rel] = true
		default:
			st.Skipped++
			return nil
		}
		if fn, ok := opts.Rewrite[rel]; ok && e.Type == TypeFile && e.Size <= maxRewrite {
			old, err := readSmall(r, maxRewrite)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name, err)
			}
			content := old
			if changed, ok := fn(old); ok {
				content = changed
				st.Rewritten = append(st.Rewritten, rel)
			}
			h.Size = int64(len(content))
			r = bytes.NewReader(content)
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if e.Type == TypeFile {
			n, err := io.Copy(tw, r)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name, err)
			}
			st.Files++
			st.Bytes += n
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	if !found {
		return st, fmt.Errorf("%w: %s", ErrNoMember, dir)
	}
	return st, tw.Close()
}

// ErrNoMember is the error of a member that the archive does not have.
var ErrNoMember = errors.New("the archive does not have the file")

// WithMember calls fn with the content of one file of the archive. A gzip, bzip2 or zstd
// file is given plain. A file that is a tar archive gives its first regular file, for example
// the dump in the "sqldump" archive of a Plesk backup.
func WithMember(ctx context.Context, a *Archive, name string, fn func(r io.Reader) error) error {
	walk := *a
	walk.Expand = ExpandFor(path.Dir(name))
	found := false
	err := walk.Walk(ctx, func(e Entry, r io.Reader) error {
		if e.Name != name || e.Type != TypeFile {
			return nil
		}
		found = true
		plain, done, err := Decompress(r)
		if err != nil {
			return err
		}
		defer done()
		br := bufio.NewReaderSize(plain, 4096)
		if head, _ := br.Peek(512); len(head) == 512 && bytes.Equal(head[257:262], []byte("ustar")) {
			return firstFile(br, fn)
		}
		if err := fn(br); err != nil {
			return err
		}
		return ErrStop
	})
	if err == nil && !found {
		return fmt.Errorf("%w: %s", ErrNoMember, name)
	}
	return err
}

// firstFile calls fn with the first regular file of a tar stream, given plain.
func firstFile(r io.Reader, fn func(r io.Reader) error) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return ErrNoMember
		}
		if err != nil {
			return fmt.Errorf("read the archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		plain, done, err := Decompress(tr)
		if err != nil {
			return err
		}
		defer done()
		if err := fn(plain); err != nil {
			return err
		}
		return ErrStop
	}
}

// Message is one message of a Maildir.
type Message struct {
	// Folder is the path of the folder, with one element for each level. An empty path is
	// the inbox.
	Folder []string
	// Name is the path of the message in the Maildir. It is the key of the message in the
	// record of the messages that a job copied.
	Name string
	// Keywords are the JMAP keywords of the flags, for example "$seen".
	Keywords []string
	Received time.Time
	Size     int64
}

// maildirFlags are the flags of a message in its file name, after ":2,".
var maildirFlags = map[rune]string{'S': "$seen", 'F': "$flagged", 'R': "$answered", 'D': "$draft", 'P': "$forwarded"}

// WalkMaildir calls fn for each message of the Maildir dir. A message with the flag "T"
// (marked for removal) is not given.
func WalkMaildir(ctx context.Context, a *Archive, dir string, fn func(m Message, r io.Reader) error) error {
	walk := *a
	walk.Expand = ExpandFor(dir)
	return walk.Walk(ctx, func(e Entry, r io.Reader) error {
		if e.Type != TypeFile || !Within(e.Name, dir) {
			return nil
		}
		rel := Rel(e.Name, dir)
		folder, ok := MessageOf(rel)
		if !ok {
			return nil
		}
		base := rel[strings.LastIndex(rel, "/")+1:]
		m := Message{Name: rel, Received: e.ModTime, Size: e.Size}
		if folder != "" {
			for _, part := range strings.Split(folder, ".") {
				m.Folder = append(m.Folder, DecodeFolder(part))
			}
			// Some servers keep the folders below "INBOX".
			if len(m.Folder) > 1 && strings.EqualFold(m.Folder[0], "INBOX") {
				m.Folder = m.Folder[1:]
			}
		}
		if i := strings.LastIndexAny(base, ":!"); i >= 0 && strings.HasPrefix(base[i+1:], "2,") {
			for _, f := range base[i+3:] {
				if f == 'T' {
					return nil
				}
				if k, ok := maildirFlags[f]; ok {
					m.Keywords = append(m.Keywords, k)
				}
			}
		}
		if strings.HasPrefix(rel, "new/") || strings.Contains(rel, "/new/") {
			m.Keywords = removeKeyword(m.Keywords, "$seen")
		}
		if secs, _, ok := strings.Cut(base, "."); ok {
			if n, err := strconv.ParseInt(secs, 10, 64); err == nil && n > 0 && n < 1<<33 {
				m.Received = time.Unix(n, 0)
			}
		}
		return fn(m, r)
	})
}

func removeKeyword(k []string, name string) []string {
	out := k[:0]
	for _, v := range k {
		if v != name {
			out = append(out, v)
		}
	}
	return out
}

// DecodeFolder decodes the name of an IMAP folder in modified UTF-7 (RFC 3501, section
// 5.1.3), for example "Entw&APw-rfe" is "Entwürfe". A name that is not valid stays as it is.
func DecodeFolder(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			b.WriteByte(s[i])
			continue
		}
		j := strings.IndexByte(s[i:], '-')
		if j < 0 {
			return s
		}
		enc := s[i+1 : i+j]
		i += j
		if enc == "" {
			b.WriteByte('&')
			continue
		}
		raw, ok := decodeBase64Modified(enc)
		if !ok || len(raw)%2 != 0 {
			return s
		}
		units := make([]uint16, len(raw)/2)
		for k := range units {
			units[k] = uint16(raw[2*k])<<8 | uint16(raw[2*k+1])
		}
		b.WriteString(string(utf16.Decode(units)))
	}
	return b.String()
}

const modifiedBase64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,"

func decodeBase64Modified(s string) ([]byte, bool) {
	var out []byte
	var acc, bits uint
	for _, c := range []byte(s) {
		v := strings.IndexByte(modifiedBase64, c)
		if v < 0 {
			return nil, false
		}
		acc = acc<<6 | uint(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out, true
}

// wpDefine agrees with a database setting of wp-config.php.
var wpDefine = regexp.MustCompile(`define\s*\(\s*['"](DB_NAME|DB_USER|DB_PASSWORD|DB_HOST)['"]\s*,\s*(?:'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*")\s*\)`)

// RewriteWordPress gives wp-config.php with new database settings. It changes only the four
// definitions DB_NAME, DB_USER, DB_PASSWORD and DB_HOST. ok is false when the file does not
// have all four.
func RewriteWordPress(config []byte, values map[string]string) ([]byte, bool) {
	found := map[string]bool{}
	out := wpDefine.ReplaceAllFunc(config, func(m []byte) []byte {
		key := string(wpDefine.FindSubmatch(m)[1])
		v, ok := values[key]
		if !ok {
			return m
		}
		found[key] = true
		return []byte("define( '" + key + "', '" + phpSingleQuoted(v) + "' )")
	})
	return out, len(found) == 4
}

func phpSingleQuoted(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}
