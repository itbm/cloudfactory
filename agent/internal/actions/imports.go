package actions

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/migrate"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The import of an account from a different hosting panel (docs/imports.md).
//
// The archive of an import comes from a customer, so the agent does not trust it. The worker
// keeps it in its own directory (ImportsDir) and reads it as a stream (package migrate). Root
// never opens it:
//
//   - The Unix user of the website extracts the files of a website from a new tar stream that
//     has only directories, regular files and symbolic links (migrate.WriteFiles).
//   - The database client in the container of the database server reads a dump, as the
//     temporary user of a restore that can use only the one database.
//   - The mail server gets each message through its JMAP API, in the account of the mailbox.

// Limits of an import.
const (
	// importReserve is the free disk space that a download leaves on the file system.
	importReserve = 2 << 30
	// importIdle is the longest time with no data while a download operates.
	importIdle = 2 * time.Minute
	// maxImportMessage is the largest message that an import copies.
	maxImportMessage = 50 << 20
	// importBatch is the number of messages in one Email/import call.
	importBatch = 50
)

// ImportsDir holds the archive of each import. The worker owns it.
func (d *Deps) ImportsDir() string { return filepath.Join(d.DataDir, "imports") }

func (d *Deps) importDir(id string) string { return filepath.Join(d.ImportsDir(), id) }

func (d *Deps) importArchive(id string) string { return filepath.Join(d.importDir(id), "archive") }

// importSum is the file with the SHA-256 hash and the size of the archive of an import.
func (d *Deps) importSum(id string) string { return filepath.Join(d.importDir(id), "sha256") }

// checkImportURL accepts an HTTPS URL with no user name and no password.
func checkImportURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(raw, " \t\r\n") {
		return errors.New("the URL of the archive must be an HTTPS URL with no user name and no password")
	}
	return nil
}

// FetchImport downloads the archive of an import, and with inspect reads the description of
// the account from it.
func (d *Deps) FetchImport(req *agentv1.FetchImportRequest) (jobs.Job, error) {
	id := req.GetImportId()
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(id), checkImportURL(req.GetUrl())); err != nil {
		return jobs.Job{}, err
	}
	if req.GetSha256() != "" {
		if err := validate.SHA256(req.GetSha256()); err != nil {
			return jobs.Job{}, err
		}
	}
	if req.GetMaxBytes() <= 0 {
		return jobs.Job{}, errors.New("the job has no size limit for the archive")
	}
	var sum string
	var size int64
	var account *migrate.Account
	steps := []jobs.Step{{Name: "Download archive", Do: func(ctx context.Context) (err error) {
		sum, size, err = d.fetchArchive(ctx, id, req.GetUrl(), req.GetSha256(), req.GetMaxBytes())
		return err
	}}}
	if req.GetInspect() {
		steps = append(steps, jobs.Step{Name: "Read archive", Do: func(ctx context.Context) error {
			acc, err := migrate.Inspect(ctx, &migrate.Archive{Path: d.importArchive(id)})
			if err != nil {
				return err
			}
			account = &acc
			return nil
		}})
	}
	return jobs.Job{ID: req.GetJobId(), Key: "import:" + id, Long: true, Steps: steps, Result: func() map[string]any {
		out := map[string]any{"sha256": sum, "bytes": size}
		if account != nil {
			out["account"] = account
		}
		return out
	}}, nil
}

// fetchArchive downloads the archive, or keeps an archive of an earlier run with the same hash.
func (d *Deps) fetchArchive(ctx context.Context, id, source, want string, maxBytes int64) (string, int64, error) {
	if sum, size, err := d.archiveSum(id); err == nil && (want == "" || sum == want) {
		return sum, size, nil
	}
	dir := d.importDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	part := filepath.Join(dir, "archive.part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(part)
	defer f.Close()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	h := sha256.New()
	w := &archiveWriter{w: io.MultiWriter(f, h), max: maxBytes, dir: dir, idle: time.AfterFunc(importIdle, func() {
		cancel(errors.New("the download of the archive sent no data for 2 minutes"))
	})}
	defer w.idle.Stop()
	if source == "" {
		if d.PanelDownload == nil {
			return "", 0, errors.New("the agent cannot download from the panel")
		}
		_, err = d.PanelDownload(ctx, "/api/agent/v1/imports/"+id+"/archive", w)
	} else {
		err = d.downloadURL(ctx, source, w)
	}
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		err = cause
	}
	if err != nil {
		return "", 0, err
	}
	if w.n == 0 {
		return "", 0, errors.New("the archive is empty")
	}
	if err := f.Sync(); err != nil {
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if want != "" && sum != want {
		return "", 0, errors.New("the archive does not agree with the hash of its first download; it changed after the inspection")
	}
	if err := os.Rename(part, d.importArchive(id)); err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(d.importSum(id), []byte(sum+" "+strconv.FormatInt(w.n, 10)+"\n"), 0o600); err != nil {
		return "", 0, err
	}
	return sum, w.n, nil
}

// archiveSum gives the hash and the size of the archive of an import on this server.
func (d *Deps) archiveSum(id string) (string, int64, error) {
	b, err := os.ReadFile(d.importSum(id))
	if err != nil {
		return "", 0, err
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || validate.SHA256(f[0]) != nil {
		return "", 0, errors.New("the hash file of the archive is not valid")
	}
	size, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return "", 0, err
	}
	fi, err := os.Stat(d.importArchive(id))
	if err != nil {
		return "", 0, err
	}
	if fi.Size() != size {
		return "", 0, errors.New("the archive does not have the size of its download")
	}
	return f[0], size, nil
}

// archive opens the archive of an import that FetchImport downloaded to this server.
func (d *Deps) archive(id string) (*migrate.Archive, error) {
	if _, _, err := d.archiveSum(id); err != nil {
		return nil, fmt.Errorf("this server does not have the archive of the import; download it again: %w", err)
	}
	return &migrate.Archive{Path: d.importArchive(id)}, nil
}

// archiveWriter writes a download. It stops at the size limit and when the disk is almost
// full, and it resets the idle timer.
type archiveWriter struct {
	w       io.Writer
	n       int64
	max     int64
	dir     string
	checked int64
	idle    *time.Timer
}

func (w *archiveWriter) Write(p []byte) (int, error) {
	if w.n+int64(len(p)) > w.max {
		return 0, fmt.Errorf("the archive is larger than the limit of %d bytes", w.max)
	}
	if w.n-w.checked >= 64<<20 || w.checked == 0 {
		w.checked = max(w.n, 1)
		var st unix.Statfs_t
		if err := unix.Statfs(w.dir, &st); err == nil && int64(st.Bavail)*int64(st.Bsize) < importReserve {
			return 0, errors.New("the server has too little free disk space for the archive")
		}
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	w.idle.Reset(importIdle)
	return n, err
}

// downloadURL downloads an archive from a public HTTPS server.
func (d *Deps) downloadURL(ctx context.Context, raw string, w io.Writer) error {
	client := d.ImportHTTP
	if client == nil {
		client = publicHTTP()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download the archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download the archive: the server answered with the status %d", resp.StatusCode)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("download the archive: %w", err)
	}
	return nil
}

// blockedRanges are address ranges that are not on the internet, in addition to the private,
// loopback and link-local ranges.
var blockedRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// publicAddress reports whether an import can download from an address. A URL from a
// customer must not reach the services of the server or of its private network.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range blockedRanges {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

var (
	publicOnce   sync.Once
	publicClient *http.Client
)

// publicHTTP gives the HTTP client of the downloads from a URL. It connects only to public
// addresses, also after a redirect, and it does not use a proxy: a proxy would connect to
// an address that the agent does not check.
func publicHTTP() *http.Client {
	publicOnce.Do(func() {
		dialer := &net.Dialer{Timeout: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicAddress(ip) {
				return fmt.Errorf("the address %s is not a public address", host)
			}
			return nil
		}}
		publicClient = &http.Client{
			Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 30 * time.Second,
				ResponseHeaderTimeout: 2 * time.Minute, ForceAttemptHTTP2: true},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("the URL of the archive has too many redirects")
				}
				return checkImportURL(req.URL.String())
			},
		}
	})
	return publicClient
}

// ImportFiles copies the files of one website from the archive.
func (d *Deps) ImportFiles(req *agentv1.ImportFilesRequest) (jobs.Job, error) {
	id, unixUser := req.GetImportId(), req.GetUnixUser()
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(id), validate.ID(req.GetWebsiteId()), validate.UnixUser(unixUser)); err != nil {
		return jobs.Job{}, err
	}
	if err := d.checkOwner(req.GetWebsiteId(), unixUser); err != nil {
		return jobs.Job{}, err
	}
	dir, ok := migrate.CleanName(req.GetPath())
	if !ok || dir != req.GetPath() {
		return jobs.Job{}, errors.New("the path in the archive is not valid")
	}
	var exclude []string
	for _, x := range req.GetExclude() {
		clean, ok := migrate.CleanName(x)
		if !ok || clean != x {
			return jobs.Job{}, fmt.Errorf("the excluded path %q is not valid", x)
		}
		exclude = append(exclude, clean)
	}
	opts := migrate.FileOptions{Exclude: exclude}
	if cfg := req.GetWordpressConfig(); cfg != "" {
		clean, ok := migrate.CleanName(cfg)
		if !ok || clean != cfg {
			return jobs.Job{}, errors.New("the path of the WordPress configuration is not valid")
		}
		values := req.GetWordpressValues()
		for _, k := range []string{"DB_NAME", "DB_USER", "DB_PASSWORD", "DB_HOST"} {
			if v := values[k]; v == "" || len(v) > 200 || strings.ContainsAny(v, "\x00\r\n") {
				return jobs.Job{}, fmt.Errorf("the WordPress setting %s is not valid", k)
			}
		}
		opts.Rewrite = map[string]func([]byte) ([]byte, bool){clean: func(old []byte) ([]byte, bool) {
			return migrate.RewriteWordPress(old, values)
		}}
	}
	var stats migrate.FileStats
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Long: true, Steps: []jobs.Step{
		{Name: "Check archive", Do: func(ctx context.Context) error {
			site, err := d.State.Get(req.GetWebsiteId())
			if err != nil {
				return err
			}
			if site == nil {
				return errors.New("this server does not have the website")
			}
			_, err = d.archive(id)
			return err
		}},
		{Name: "Copy files", Do: func(ctx context.Context) error {
			a, err := d.archive(id)
			if err != nil {
				return err
			}
			app := d.Host.AppDir(unixUser)
			if req.GetReplace() {
				if err := d.emptyAsUser(ctx, unixUser, app); err != nil {
					return err
				}
			}
			stats, err = d.streamFiles(ctx, a, dir, opts, unixUser, app)
			return err
		}},
	}, Result: func() map[string]any {
		return map[string]any{"files": stats.Files, "bytes": stats.Bytes, "skipped": stats.Skipped, "rewritten": nonNilStrings(stats.Rewritten)}
	}}, nil
}

// streamFiles sends the files of dir in the archive to a tar process of the website user,
// which extracts them into app.
func (d *Deps) streamFiles(ctx context.Context, a *migrate.Archive, dir string, opts migrate.FileOptions, unixUser, app string) (migrate.FileStats, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	var stats migrate.FileStats
	done := make(chan error, 1)
	go func() {
		var err error
		stats, err = migrate.WriteFiles(ctx, a, dir, opts, pw)
		pw.CloseWithError(err)
		done <- err
	}()
	err := d.Run.Stream(ctx, pr, nil, "runuser", "-u", unixUser, "--", "tar", "--extract", "--file=-", "--directory="+app, "--no-same-owner", "--preserve-permissions")
	// The writer stops when the tar process stopped before the end of the stream.
	pr.CloseWithError(errTarStopped)
	if werr := <-done; werr != nil && !errors.Is(werr, errTarStopped) {
		return stats, werr
	}
	if err != nil {
		return stats, fmt.Errorf("extract the files: %w", err)
	}
	return stats, nil
}

var errTarStopped = errors.New("the tar process stopped")

// ImportDatabase loads a dump of the archive into a database.
func (d *Deps) ImportDatabase(req *agentv1.ImportDatabaseRequest) (jobs.Job, error) {
	id, unixUser, engine, name := req.GetImportId(), req.GetUnixUser(), req.GetEngine(), req.GetName()
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(id), validate.ID(req.GetWebsiteId()), validate.UnixUser(unixUser), validate.TenantDatabaseName(unixUser, name)); err != nil {
		return jobs.Job{}, err
	}
	if engine != "mariadb" {
		return jobs.Job{}, fmt.Errorf("the import cannot load a dump into a %s database", engine)
	}
	if _, err := serverKey(req.GetDatabaseServer()); err != nil {
		return jobs.Job{}, err
	}
	member, ok := migrate.CleanName(req.GetPath())
	if !ok || member != req.GetPath() {
		return jobs.Job{}, errors.New("the path of the dump in the archive is not valid")
	}
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Long: true, Steps: []jobs.Step{
		{Name: "Check archive", Do: func(ctx context.Context) error {
			_, err := d.archive(id)
			return err
		}},
		{Name: "Load dump", Do: func(ctx context.Context) error {
			a, err := d.archive(id)
			if err != nil {
				return err
			}
			srv, err := d.serverFor(req.GetWebsiteId(), req.GetCustomer(), req.GetDatabaseServer(), engine)
			if err != nil {
				return err
			}
			drv, err := sqlOf(engine)
			if err != nil {
				return err
			}
			return d.restoreDump(ctx, srv, name, func(ctx context.Context, client []string) error {
				return migrate.WithMember(ctx, a, member, func(r io.Reader) error {
					dump := importDump(r)
					if f := drv.dumpFilter(); f != nil {
						dump = f(dump)
					}
					return d.Run.Stream(ctx, dump, nil, client[0], client[1:]...)
				})
			})
		}},
	}}, nil
}

// importDump removes the statements of a dump from a different server that select or make
// a database: "CREATE DATABASE" and "USE". The temporary user of the restore can use only the
// new database, which has a different name.
func importDump(dump io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(dump, 64<<10)
		lineStart := true
		for {
			part, err := br.ReadSlice('\n')
			if len(part) > 0 {
				if lineStart && databaseStatement(part) {
					part = append([]byte("-- "), part...)
				}
				lineStart = part[len(part)-1] == '\n'
				if _, werr := pw.Write(part); werr != nil {
					return
				}
			}
			if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}

// databaseStatement reports whether a line of a dump is a statement that selects or makes a
// database, also in a comment with a version ("/*!40000 ...").
func databaseStatement(line []byte) bool {
	s := strings.TrimSpace(string(line))
	if strings.HasPrefix(s, "/*!") {
		if i := strings.IndexByte(s, ' '); i > 0 {
			s = strings.TrimSpace(s[i:])
		}
	}
	upper := strings.ToUpper(s)
	return strings.HasPrefix(upper, "USE ") || strings.HasPrefix(upper, "USE`") || strings.HasPrefix(upper, "CREATE DATABASE ") || strings.HasPrefix(upper, "CREATE SCHEMA ")
}

// ImportMail copies the messages of Maildir folders into mailboxes.
func (d *Deps) ImportMail(req *agentv1.ImportMailRequest) (jobs.Job, error) {
	id := req.GetImportId()
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(id)); err != nil {
		return jobs.Job{}, err
	}
	boxes := req.GetMailboxes()
	if len(boxes) == 0 || len(boxes) > 2000 {
		return jobs.Job{}, errors.New("the job must have 1 to 2000 mailboxes")
	}
	for _, b := range boxes {
		if err := checkMailAddress(b.GetDomain(), b.GetLocalPart()); err != nil {
			return jobs.Job{}, err
		}
		if p, ok := migrate.CleanName(b.GetPath()); !ok || p != b.GetPath() {
			return jobs.Job{}, errors.New("the path of a Maildir in the archive is not valid")
		}
	}
	copied := 0
	failed := map[string]int{}
	return jobs.Job{ID: req.GetJobId(), Key: "import:" + id, Long: true, Steps: []jobs.Step{
		{Name: "Check archive", Do: func(ctx context.Context) error {
			_, err := d.archive(id)
			return err
		}},
		{Name: "Copy messages", Do: func(ctx context.Context) error {
			a, err := d.archive(id)
			if err != nil {
				return err
			}
			c, err := d.mailClient(ctx)
			if err != nil {
				return err
			}
			mc := &mailCopy{d: d, c: c, a: a, importID: id}
			for _, b := range boxes {
				n, bad, err := mc.mailbox(ctx, b)
				copied += n
				if bad > 0 {
					failed[b.GetLocalPart()+"@"+b.GetDomain()] = bad
				}
				if err != nil {
					return fmt.Errorf("%s@%s: %w", b.GetLocalPart(), b.GetDomain(), err)
				}
			}
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"messages": copied, "failed": failed}
	}}, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// DeleteImport removes the archive of an import from this server.
func (d *Deps) DeleteImport(req *agentv1.DeleteImportRequest) (jobs.Job, error) {
	id := req.GetImportId()
	if err := errors.Join(validate.ID(req.GetJobId()), validate.ID(id)); err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{ID: req.GetJobId(), Key: "import:" + id, Steps: []jobs.Step{{Name: "Remove archive", Do: func(ctx context.Context) error {
		return os.RemoveAll(d.importDir(id))
	}}}}, nil
}
