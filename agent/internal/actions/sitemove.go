package actions

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// A website moves to a different server through the tunnel between the two servers, as a
// database server does (move.go). The panel sends ReceiveWebsite to the new server first, and
// then SendWebsite to the old server. Both jobs have the same token.
//
//  1. The new server makes the Unix user and the disk of the website. Its firewall lets the
//     mesh address of the old server connect to TransferPort on its own mesh address, for the
//     time of the move only.
//  2. The old server stops the website and closes its SFTP access. It connects from its mesh
//     address and sends the token, and then two parts in frames: the files of the website, and
//     the known SSH hosts of its deploys. tar makes each part as the website user.
//  3. The new server checks the token, and gives each part to tar, which writes the files as
//     the website user. It answers "ok" after the last frame.
//  4. The new server starts the website, and adds its edge route, its SFTP access and its cron
//     jobs.
//
// Only the root helpers connect, and only the website users read and write the files. Thus a
// link in the files of a website cannot make root read or change a different file (CF-05).
// The databases of the website stay in their database servers. The panel lets a website move
// only when the new server can connect to each of them.

const (
	opSiteSend    = "site.send"
	opSiteReceive = "site.receive"

	// siteHello is the first line from the server that sends a website, before the token.
	siteHello = "CFSITE1 "
	// maxFrame is the largest frame of the files of a website.
	maxFrame = 1 << 20
	// maxKnownHosts is the largest part with the known SSH hosts of the deploys.
	maxKnownHosts = 1 << 20
)

// siteTransferIn is the value of the operations that send and receive the files of a website.
type siteTransferIn struct {
	WebsiteID string
	User      string
	// Peer is the mesh address of the other server.
	Peer string
	// Token is the secret of this move: 64 hexadecimal digits.
	Token string
	// Timeout is the time in seconds in which the old server must connect. Only the server
	// that receives uses it.
	Timeout int64
}

// checkSiteTransfer checks the values of a move of a website from a request or from the worker.
func checkSiteTransfer(in siteTransferIn) error {
	if err := errors.Join(validate.ID(in.WebsiteID), validate.UnixUser(in.User)); err != nil {
		return err
	}
	if !tokenPattern.MatchString(in.Token) {
		return errors.New("the token of the move must be 64 hexadecimal digits in lower case")
	}
	a, err := netip.ParseAddr(in.Peer)
	if err != nil || !a.Is4() {
		return fmt.Errorf("the mesh address %q of the other server is not valid", in.Peer)
	}
	if in.Timeout < 0 || in.Timeout > 24*3600 {
		return errors.New("the time limit of the move must be 0 to 24 hours")
	}
	return nil
}

// SendWebsite stops a website, and sends its files to the server that receives it. When the
// files did not go to the new server, the website starts again on this server.
func (d *Deps) SendWebsite(req *agentv1.SendWebsiteRequest) (jobs.Job, error) {
	in := siteTransferIn{WebsiteID: req.GetWebsiteId(), User: req.GetUnixUser(), Peer: req.GetTo(), Token: req.GetToken()}
	if err := checkSiteTransfer(in); err != nil {
		return jobs.Job{}, err
	}
	if err := d.checkOwner(in.WebsiteID, in.User); err != nil {
		return jobs.Job{}, err
	}
	// start starts the website on this server again, after a move that failed. A website that
	// was stopped before the move stays stopped. The panel gives the SFTP access back.
	start := func(ctx context.Context) error {
		if req.GetStopped() {
			return nil
		}
		site, err := d.State.Get(in.WebsiteID)
		if err != nil || site == nil {
			return err
		}
		return d.power(ctx, site, "start")
	}
	// The files can take hours (Long).
	return jobs.Job{ID: req.GetJobId(), Key: in.WebsiteID, Long: true, Steps: []jobs.Step{
		{Name: "Stop website", Do: func(ctx context.Context) error {
			site, err := d.State.Get(in.WebsiteID)
			if err != nil {
				return err
			}
			if site == nil {
				return errors.New("this server does not have the website")
			}
			// A stopped website has a unit with no [Install] section, so it does not start
			// again at a start of this server while its files move. No cron job and no change
			// of the runtime starts it either.
			if err := d.power(ctx, site, "stop"); err != nil {
				return err
			}
			// No SFTP session changes the files while they move. setSftpKeys closes the open
			// sessions.
			return d.setSftpKeys(ctx, in.User, false, nil)
		}},
		{Name: "Send files", Do: func(ctx context.Context) error {
			err := d.sendSite(ctx, in)
			if err == nil {
				// The website stays stopped on this server. The panel removes it.
				return nil
			}
			if strings.Contains(err.Error(), sentNoAnswer) {
				// The new server possibly has all the files and starts the website. A start
				// here then gave two copies that operate (as for a database server, finding
				// G4-03). The panel knows the result of the new server: it removes this copy,
				// or it starts it again.
				return fmt.Errorf("%w. The website stays stopped on this server until the panel has the result of the new server", err)
			}
			// The website operates on this server again. The panel removes it from the new
			// server.
			return errors.Join(err, start(ctx))
		}},
	}}, nil
}

// ReceiveWebsite makes a website that moves to this server, receives its files from the old
// server, and starts it.
func (d *Deps) ReceiveWebsite(req *agentv1.ReceiveWebsiteRequest) (jobs.Job, error) {
	w := req.GetWebsite()
	if err := CheckWebsite(w); err != nil {
		return jobs.Job{}, err
	}
	var errs []error
	for _, k := range req.GetSftpKeys() {
		errs = append(errs, validate.SSHKey(k))
	}
	if len(req.GetCronJobs()) > validate.MaxCronJobs {
		errs = append(errs, fmt.Errorf("a website can have %d cron jobs", validate.MaxCronJobs))
	}
	entries := make([]CronEntry, 0, len(req.GetCronJobs()))
	seen := map[string]bool{}
	for _, c := range req.GetCronJobs() {
		if err := checkCronJob(c); err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[c.GetId()] {
			errs = append(errs, errors.New("two cron jobs have the same ID"))
		}
		seen[c.GetId()] = true
		entries = append(entries, CronEntry{ID: c.GetId(), Schedule: c.GetSchedule(), Command: c.GetCommand(), Enabled: c.GetEnabled()})
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	timeout := int64(req.GetTimeoutSeconds())
	if timeout <= 0 {
		timeout = 900
	}
	user := w.GetUnixUser()
	in := siteTransferIn{WebsiteID: w.GetId(), User: user, Peer: req.GetFrom(), Token: req.GetToken(), Timeout: timeout}
	if err := checkSiteTransfer(in); err != nil {
		return jobs.Job{}, err
	}
	if err := d.checkOwner(w.GetId(), user); err != nil {
		return jobs.Job{}, err
	}
	// The customer of the website. The value was checked in CheckWebsite.
	instance, _ := instanceKey(w.GetDatabaseInstance())
	stopped := req.GetStopped()
	var port int
	var dnsHere bool
	return jobs.Job{ID: req.GetJobId(), Key: w.GetId(), Long: true, Steps: []jobs.Step{
		{Name: "Create Unix user", Do: func(ctx context.Context) error {
			// The website can use the database servers of its customer on this server.
			if _, err := d.useDatabases(instance, w.GetId()); err != nil {
				return err
			}
			// The disk of the website has the disk limit before the files arrive, as for a
			// new website (sitedisk.go).
			if err := d.ensureWebsiteUser(ctx, user, max(w.GetLimits().GetDiskBytes(), 0)); err != nil {
				return err
			}
			if err := d.applyDiskQuota(ctx, user, w.GetLimits()); err != nil {
				return err
			}
			// The state has the website as stopped while its files arrive: no cron job and no
			// other job starts it.
			_, err := d.State.Reserve(&Site{ID: w.GetId(), UnixUser: user, Website: w, Stopped: true})
			return err
		}},
		{Name: "Receive files", Do: func(ctx context.Context) error {
			return d.receiveSite(ctx, in)
		}},
		{Name: "Pull and check image signature", Do: func(ctx context.Context) error {
			return d.pullImage(ctx, w)
		}},
		{Name: "Start container in gVisor", Do: func(ctx context.Context) error {
			site := &Site{ID: w.GetId(), UnixUser: user, Website: w, Stopped: stopped}
			var err error
			if port, err = d.State.Reserve(site); err != nil {
				return err
			}
			// The firewall lets the website connect to the database servers of its customer
			// on this server, and to the members of its networks.
			if err := d.syncDatabaseAccess(ctx); err != nil {
				return err
			}
			if err := d.writeSiteFiles(ctx, w, ""); err != nil {
				return err
			}
			// A website that was stopped before the move gets its unit, and stays stopped.
			return d.restartWithUnit(ctx, site)
		}},
		{Name: "Configure edge proxy", Do: func(ctx context.Context) error {
			if err := d.Edge.EnsureBase(ctx); err != nil {
				return err
			}
			return d.Edge.Apply(ctx, d.edgeSite(&Site{ID: w.GetId(), Port: port, Stopped: stopped}, w))
		}},
		{Name: "Set SFTP access and cron jobs", Do: func(ctx context.Context) error {
			if err := d.setSftpKeys(ctx, user, req.GetSftpEnabled(), req.GetSftpKeys()); err != nil {
				return err
			}
			return d.State.SetCron(w.GetId(), entries)
		}},
		{Name: "Get TLS certificate", Do: func(ctx context.Context) error {
			// Caddy gets the certificate when the DNS of the domain points to this server.
			dnsHere = d.DNSPointsHere(ctx, primaryDomain(w))
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"port": port, "dnsPointsHere": dnsHere}
	}}, nil
}

// moveMarker is the file with the token of the move that wrote the files of a website user.
// A job that runs again does not receive the files again. Only root can read the directory.
func (d *Deps) moveMarker(user string) string {
	return filepath.Join(d.DataDir, "moves", user)
}

// sendSite connects to the server that receives the website, and sends its files. The website
// does not operate.
func (d *Deps) sendSite(ctx context.Context, in siteTransferIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opSiteSend, in, nil)
	}
	if err := checkSiteTransfer(in); err != nil {
		return err
	}
	if err := d.existingWebsiteUser(in.User); err != nil {
		return err
	}
	own, peer, err := waitPeer(ctx, func() (netip.Addr, netip.Addr, error) { return d.peerAddresses(in.Peer) })
	if err != nil {
		return err
	}
	conn, err := dialTransfer(ctx, own, peer)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if err := d.writeSiteTransfer(ctx, conn, in); err != nil {
		return fmt.Errorf("send the files of the website %s: %w", in.User, err)
	}
	// All the files went out. From here, the new server can have them all.
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.CloseWrite(); err != nil {
			return fmt.Errorf("this server %s of the website %s: %v", sentNoAnswer, in.User, err)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(transferAnswer))
	answer, err := io.ReadAll(io.LimitReader(conn, 512))
	if err != nil || len(answer) == 0 {
		return fmt.Errorf("this server %s of the website %s: %v", sentNoAnswer, in.User, err)
	}
	if string(answer) != transferOK {
		return fmt.Errorf("the server that receives the website %s did not accept the files: %q", in.User, strings.TrimSpace(string(answer)))
	}
	slog.Info("sent a website", "website", in.WebsiteID, "to", peer)
	return nil
}

// writeSiteTransfer writes the token and the two parts of a website: the files, and the known
// SSH hosts of its deploys. Each part is the output of tar as the website user, in frames, and
// a frame of length 0 ends it. tar does not follow a link.
func (d *Deps) writeSiteTransfer(ctx context.Context, conn io.Writer, in siteTransferIn) error {
	w := bufio.NewWriterSize(conn, 1<<20)
	if _, err := w.WriteString(siteHello + in.Token + "\n"); err != nil {
		return err
	}
	files := frameWriter{w: w}
	if err := d.Run.Stream(ctx, nil, files, "runuser", "-u", in.User, "--", "tar", "--create", "--file=-", "--directory="+d.Host.AppDir(in.User), "--", "."); err != nil {
		// No end frame: the new server does not accept a part of the files.
		return err
	}
	if err := files.end(); err != nil {
		return err
	}
	hosts := frameWriter{w: w}
	// The agent made the directory at the first deploy. root does not follow a link in the
	// home directory.
	dir := d.knownHosts(in.User)
	if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
		if err := d.Run.Stream(ctx, nil, hosts, "runuser", "-u", in.User, "--", "tar", "--create", "--file=-", "--directory="+filepath.Dir(dir), "--", filepath.Base(dir)); err != nil {
			return err
		}
	}
	if err := hosts.end(); err != nil {
		return err
	}
	return w.Flush()
}

// receiveSite lets the old server connect for the time of the move, and writes the files of
// the website that it sends. The website does not operate.
func (d *Deps) receiveSite(ctx context.Context, in siteTransferIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opSiteReceive, in, nil)
	}
	if err := checkSiteTransfer(in); err != nil {
		return err
	}
	if err := d.existingWebsiteUser(in.User); err != nil {
		return err
	}
	// The worker cannot select the files of a different website: the state has the website
	// with this Unix user (ReceiveWebsite records it first).
	site, err := d.State.Get(in.WebsiteID)
	if err != nil {
		return err
	}
	if site == nil || site.UnixUser != in.User || !site.Stopped {
		return errors.New("this server does not receive this website now")
	}
	marker := d.moveMarker(in.User)
	if receivedBefore(marker, in.Token) {
		// An earlier run of the job received the files.
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	own, peer, err := waitPeer(ctx, func() (netip.Addr, netip.Addr, error) { return d.peerAddresses(in.Peer) })
	if err != nil {
		return err
	}
	err = d.acceptTransfer(ctx, own, peer, in.Timeout, "the website "+in.User, marker, in.Token, func(conn net.Conn) (bool, error) {
		return d.receiveSiteFrom(ctx, conn, in)
	})
	if err == nil {
		slog.Info("received a website", "website", in.WebsiteID, "from", peer)
	}
	return err
}

// receiveSiteFrom reads the token and the two parts of a website from one connection. ok is
// false when the connection does not have the token of the move.
func (d *Deps) receiveSiteFrom(ctx context.Context, conn net.Conn, in siteTransferIn) (ok bool, err error) {
	idle := &idleReader{conn: conn, timeout: 30 * time.Second}
	r := bufio.NewReaderSize(idle, 1<<20)
	if !readHello(r, siteHello, in.Token) {
		return false, nil
	}
	// The files can take hours. A connection that sends nothing for transferIdle ends.
	idle.timeout = transferIdle
	return true, d.writeSiteParts(ctx, r, in.User)
}

// writeSiteParts writes the two parts of a website from r, as the website user.
func (d *Deps) writeSiteParts(ctx context.Context, r io.Reader, user string) error {
	app := d.Host.AppDir(user)
	// The directory is empty before the files arrive, also after a move that stopped. The
	// website user removes the files.
	if err := d.emptyAsUser(ctx, user, app); err != nil {
		return err
	}
	files := &frameReader{r: r}
	err := d.Run.Stream(ctx, files, nil, "runuser", "-u", user, "--", "tar", "--extract", "--file=-", "--directory="+app, "--no-same-owner", "--preserve-permissions")
	if err != nil {
		return fmt.Errorf("write the files of the website: %w", err)
	}
	// tar stops at the end of the archive. The part must also have its last frame: the old
	// server sends it only when its tar read all the files.
	if _, err := io.Copy(io.Discard, files); err != nil {
		return fmt.Errorf("receive the files of the website: %w", err)
	}
	hosts, err := io.ReadAll(io.LimitReader(&frameReader{r: r}, maxKnownHosts+1))
	if err != nil {
		return fmt.Errorf("receive the known SSH hosts of the website: %w", err)
	}
	if len(hosts) > maxKnownHosts {
		return errors.New("the known SSH hosts of the website are too large")
	}
	if len(hosts) > 0 {
		// tar extracts only the directory of the known hosts.
		dir := d.knownHosts(user)
		if err := d.Run.Stream(ctx, bytes.NewReader(hosts), nil, "runuser", "-u", user, "--", "tar", "--extract", "--file=-", "--directory="+filepath.Dir(dir), "--no-same-owner", "--preserve-permissions", "--", filepath.Base(dir)); err != nil {
			return fmt.Errorf("write the known SSH hosts of the website: %w", err)
		}
	}
	// The old server removes its copy after this answer, so the files are on the disk first.
	unix.Sync()
	return nil
}

// readHello reads the first line of a transfer, and reports whether it has the token.
func readHello(r *bufio.Reader, hello, token string) bool {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return false
	}
	got, found := strings.CutPrefix(strings.TrimSuffix(string(line), "\n"), hello)
	return found && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// frameWriter writes the data of each Write in frames of at most maxFrame bytes: 4 bytes with
// the length, big endian, and then the data. end writes a frame of length 0.
type frameWriter struct{ w io.Writer }

func (f frameWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := min(len(p), maxFrame)
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], uint32(c))
		if _, err := f.w.Write(h[:]); err != nil {
			return n, err
		}
		if _, err := f.w.Write(p[:c]); err != nil {
			return n, err
		}
		n += c
		p = p[c:]
	}
	return n, nil
}

func (f frameWriter) end() error {
	_, err := f.w.Write([]byte{0, 0, 0, 0})
	return err
}

// frameReader reads the frames of one part. It gives io.EOF only after the frame of length 0.
// A stream that ends before that frame gives io.ErrUnexpectedEOF, so a part that the old
// server did not complete is never complete here.
type frameReader struct {
	r    io.Reader
	left uint32
	done bool
}

func (f *frameReader) Read(p []byte) (int, error) {
	for f.left == 0 {
		if f.done {
			return 0, io.EOF
		}
		var h [4]byte
		if _, err := io.ReadFull(f.r, h[:]); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		n := binary.BigEndian.Uint32(h[:])
		if n == 0 {
			f.done = true
			return 0, io.EOF
		}
		if n > maxFrame {
			return 0, fmt.Errorf("a frame of %d bytes is larger than %d bytes", n, maxFrame)
		}
		f.left = n
	}
	if len(p) == 0 {
		return 0, nil
	}
	if uint32(len(p)) > f.left {
		p = p[:f.left]
	}
	n, err := f.r.Read(p)
	f.left -= uint32(n)
	if err == io.EOF {
		err = nil
		if f.left > 0 && n == 0 {
			err = io.ErrUnexpectedEOF
		}
	}
	return n, err
}
