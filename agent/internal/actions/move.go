package actions

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/mesh"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
)

// A database server moves to a different server through the tunnel between the two servers.
// The panel sends ReceiveDatabaseServer to the new server first, and then SendDatabaseServer
// to the old server. Both jobs have the same token.
//
//  1. The new server makes the Unix user, the disk and the unit of the database server, and
//     does not start it. Its firewall lets the mesh address of the old server connect to
//     TransferPort on its own mesh address, for the time of the move only.
//  2. The old server stops the database server. It connects from its mesh address and sends
//     the token, and then one tar stream: the secret of the superuser, and the files of the
//     data directory.
//  3. The new server checks the token, writes the files with the owner of the database server,
//     and answers "ok". Then it starts the database server.
//
// Only the root helpers send and receive. The worker cannot connect to the mesh addresses,
// and it cannot read the data of a database server.

const (
	opDBSend    = "db.send"
	opDBReceive = "db.receive"

	// transferHello is the first line from the server that sends, before the token.
	transferHello = "CFMOVE1 "
	// transferOK is the answer of the server that receives, after it wrote all files.
	transferOK = "ok\n"
	// transferSecret is the name of the secret of the superuser in the tar stream. The files
	// of the data directory have names that start with transferData.
	transferSecret = "secret"
	transferData   = "data/"
	// receivedFile in the directory of a database server has the token of the move that
	// wrote its data. A job that runs again does not receive the data again.
	receivedFile = "received"
	// transferDial is the time in which the server that sends tries to connect. The new
	// server can start to listen after the old server stops the database server.
	transferDial = 2 * time.Minute
	// transferAnswer is the time in which the server that receives must write the last files
	// to the disk and answer.
	transferAnswer = 10 * time.Minute
	// transferIdle is the longest time with no data while the data arrives.
	transferIdle = 2 * time.Minute
	// maxSecret is the largest secret file that the server that receives accepts.
	maxSecret = 4096
)

// peerWait is the longest time that a move waits for the tunnel to the other server.
var peerWait = 2 * time.Minute

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// transferIn is the value of the operations that send and receive a database server.
type transferIn struct {
	Server dbServer
	// Peer is the mesh address of the other server.
	Peer string
	// Token is the secret of this move: 64 hexadecimal digits.
	Token string
	// Timeout is the time in seconds in which the old server must connect. Only the server
	// that receives uses it. The data itself has no time limit, but a connection that sends
	// nothing for transferIdle ends.
	Timeout int64
}

// SendDatabaseServer stops a database server, and sends its data to the server that receives
// it. When the transfer fails, the database server starts again on this server.
func (d *Deps) SendDatabaseServer(req *agentv1.SendDatabaseServerRequest) (jobs.Job, error) {
	key, err := serverKey(req.GetDatabaseServer())
	if err != nil {
		return jobs.Job{}, err
	}
	in := transferIn{Server: dbServer{ID: key, Engine: req.GetEngine()}, Peer: req.GetTo(), Token: req.GetToken()}
	if err := checkTransfer(in); err != nil {
		return jobs.Job{}, err
	}
	s := in.Server
	// start starts the database server on this server again, after a move that failed.
	start := func(ctx context.Context) error {
		if err := d.State.HoldDatabaseServer(key, false); err != nil {
			return err
		}
		return d.EnsureDatabaseService(ctx, s)
	}
	// The data can take hours (Long).
	return jobs.Job{ID: req.GetJobId(), Key: key, Long: true, Steps: []jobs.Step{
		{Name: "Stop database server", Do: func(ctx context.Context) error {
			if _, ok := d.existingServer(s.ID, s.Engine); !ok {
				return fmt.Errorf("this server does not have %s", s)
			}
			lock := d.instanceLock(key)
			lock.Lock()
			defer lock.Unlock()
			// The hold comes first: no other job starts the database server again while
			// its data goes to the new server.
			if err := d.State.HoldDatabaseServer(key, true); err != nil {
				return err
			}
			if err := d.stopServer(ctx, s); err != nil {
				return errors.Join(err, d.State.HoldDatabaseServer(key, false))
			}
			return nil
		}},
		{Name: "Send data", Do: func(ctx context.Context) error {
			lock := d.instanceLock(key)
			lock.Lock()
			err := d.sendServer(ctx, in)
			lock.Unlock()
			if err == nil {
				// The database server stays held on this server. The panel removes it.
				return nil
			}
			if strings.Contains(err.Error(), sentNoAnswer) {
				// The new server possibly has all the data and starts the database server.
				// A start on this server then gave two copies that operate, and the panel
				// removed this copy with its new data (adversarial review, finding G4-03).
				// The database server stays stopped. The panel knows the result of the new
				// server: it removes this copy, or it starts it again (SetDatabaseServer).
				return fmt.Errorf("%w. The database server stays stopped on this server until the panel has the result of the new server", err)
			}
			// The database server operates on this server again. The panel removes it from
			// the new server.
			return errors.Join(err, start(ctx))
		}},
	}}, nil
}

// sentNoAnswer is in the error of a send that wrote all the data and got no answer.
const sentNoAnswer = "sent all the data, and the new server did not answer"

// ReceiveDatabaseServer makes a database server that moves to this server, receives its data
// from the old server, and starts it.
func (d *Deps) ReceiveDatabaseServer(req *agentv1.ReceiveDatabaseServerRequest) (jobs.Job, error) {
	s := req.GetDatabaseServer()
	if err := checkDatabaseServer(s); err != nil {
		return jobs.Job{}, err
	}
	key, _ := serverKey(s.GetId())
	customer, err := instanceKey(s.GetCustomer())
	if err != nil {
		return jobs.Job{}, err
	}
	timeout := int64(req.GetTimeoutSeconds())
	if timeout <= 0 {
		timeout = 900
	}
	srv := dbServer{ID: key, Engine: s.GetEngine()}
	in := transferIn{Server: srv, Peer: req.GetFrom(), Token: req.GetToken(), Timeout: timeout}
	if err := checkTransfer(in); err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{ID: req.GetJobId(), Key: key, Long: true, Steps: []jobs.Step{
		{Name: "Prepare database server", Do: func(ctx context.Context) error {
			lock := d.instanceLock(key)
			lock.Lock()
			defer lock.Unlock()
			if _, err := d.putServer(customer, key, recordOf(s), true); err != nil {
				return err
			}
			// The database server does not start before it has all its data: not for a
			// different job, and not after a restart of the agent or a receive that failed.
			if err := d.State.HoldDatabaseServer(key, true); err != nil {
				return err
			}
			// A job that runs again finds a database server that can operate. It must not
			// operate while the data arrives.
			if err := d.stopServer(ctx, srv); err != nil {
				return err
			}
			u, record, err := d.dbUnit(srv)
			if err != nil {
				return err
			}
			if err := d.syncDatabaseAccess(ctx); err != nil {
				return err
			}
			_, err = d.prepareServer(ctx, srv, u.Customer, u.Address, record)
			return err
		}},
		{Name: "Receive data", Do: func(ctx context.Context) error {
			lock := d.instanceLock(key)
			lock.Lock()
			defer lock.Unlock()
			return d.receiveServer(ctx, in)
		}},
		{Name: "Start database server", Do: func(ctx context.Context) error {
			// The first step wrote the unit. systemd makes the service from it now.
			if _, err := d.Host.Systemctl(ctx, dbUser(srv), "daemon-reload"); err != nil {
				return err
			}
			if err := d.State.HoldDatabaseServer(key, false); err != nil {
				return err
			}
			return d.EnsureDatabaseService(ctx, srv)
		}},
	}}, nil
}

// checkTransfer checks the values of a move from a request or from the worker.
func checkTransfer(in transferIn) error {
	if err := checkServer(in.Server); err != nil {
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

// transferAddresses gives the mesh address of this server and of the other server. The other
// server must be a peer in the mesh state that the helper applied, so the worker cannot make
// the helper connect to a different address.
func (d *Deps) transferAddresses(in transferIn) (own, peer netip.Addr, err error) {
	if err := checkTransfer(in); err != nil {
		return own, peer, err
	}
	st, ok, err := d.savedMesh()
	if err != nil {
		return own, peer, err
	}
	if !ok || st.Off() {
		return own, peer, fmt.Errorf("this server is not in the mesh, so the other server is %w", errNoPeer)
	}
	if own, err = netip.ParseAddr(st.Address); err != nil {
		return own, peer, err
	}
	for _, p := range st.Peers {
		if p.Address == in.Peer {
			peer, err = netip.ParseAddr(p.Address)
			return own, peer, err
		}
	}
	return own, peer, fmt.Errorf("the server with the mesh address %s is %w of this server", in.Peer, errNoPeer)
}

// errNoPeer is the error of a move to a server that is not a peer in the mesh state.
var errNoPeer = errors.New("not a peer")

// waitTransferAddresses is transferAddresses. The panel makes the tunnel between the two
// servers when the move starts, so the mesh state can get the other server some seconds
// after the job starts.
func (d *Deps) waitTransferAddresses(ctx context.Context, in transferIn) (own, peer netip.Addr, err error) {
	ctx, cancel := context.WithTimeout(ctx, peerWait)
	defer cancel()
	for {
		own, peer, err = d.transferAddresses(in)
		if !errors.Is(err, errNoPeer) {
			return own, peer, err
		}
		select {
		case <-ctx.Done():
			return own, peer, err
		case <-time.After(time.Second):
		}
	}
}

// secretPath gives the file with the secret of the superuser of a database server.
func (d *Deps) secretPath(s dbServer) string {
	if s.Engine == "valkey" {
		return d.valkeySecretFile(s)
	}
	return filepath.Join(d.dbDir(s), s.Engine+".env")
}

// sendServer connects to the server that receives the database server, and sends its secret
// and its data. The database server does not operate.
func (d *Deps) sendServer(ctx context.Context, in transferIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDBSend, in, nil)
	}
	own, peer, err := d.waitTransferAddresses(ctx, in)
	if err != nil {
		return err
	}
	s := in.Server
	secret, err := os.ReadFile(d.secretPath(s))
	if err != nil {
		return fmt.Errorf("read the secret of %s: %w", s, err)
	}
	// Only a database server with a disk image has a disk to mount. A database server with a
	// user quota or with no disk limit has a directory. Before, the mount of an image that
	// did not exist failed each such move (adversarial review, finding G4-01).
	if _, err := os.Lstat(d.diskImage(s)); err == nil {
		if err := d.mountDisk(ctx, s); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	disk, err := safefs.Open(d.diskDir(s))
	if err != nil {
		return err
	}
	defer disk.Close()

	conn, err := dialTransfer(ctx, own, peer)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	files, bytes, err := writeTransfer(conn, in.Token, secret, disk)
	if err != nil {
		return fmt.Errorf("send the data of %s: %w", s, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.CloseWrite(); err != nil {
			return err
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(transferAnswer))
	answer, err := io.ReadAll(io.LimitReader(conn, 64))
	if err != nil || len(answer) == 0 {
		return fmt.Errorf("this server %s of %s: %v", sentNoAnswer, s, err)
	}
	if string(answer) != transferOK {
		return fmt.Errorf("the server that receives %s did not accept the data: %q", s, strings.TrimSpace(string(answer)))
	}
	slog.Info("sent a database server", "server", s.ID, "to", peer, "files", files, "bytes", bytes)
	return nil
}

// writeTransfer writes the token, the secret and the data directory of a database server.
func writeTransfer(conn io.Writer, token string, secret []byte, disk *safefs.Dir) (files, bytes int64, err error) {
	w := bufio.NewWriterSize(conn, 1<<20)
	if _, err := w.WriteString(transferHello + token + "\n"); err != nil {
		return 0, 0, err
	}
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: transferSecret, Mode: 0o600, Size: int64(len(secret))}); err != nil {
		return 0, 0, err
	}
	if _, err := tw.Write(secret); err != nil {
		return 0, 0, err
	}
	if files, bytes, err = sendTree(disk, tw, "data"); err != nil {
		return files, bytes, err
	}
	if err := tw.Close(); err != nil {
		return files, bytes, err
	}
	return files, bytes, w.Flush()
}

// dialTransfer connects from the mesh address of this server to the server that receives. It
// tries again until the other server listens, or until transferDial ends.
func dialTransfer(ctx context.Context, own, peer netip.Addr) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, transferDial)
	defer cancel()
	dialer := net.Dialer{LocalAddr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(own, 0)), Timeout: 10 * time.Second}
	to := netip.AddrPortFrom(peer, mesh.TransferPort).String()
	for {
		conn, err := dialer.DialContext(ctx, "tcp", to)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to %s: %w", to, err)
		case <-time.After(2 * time.Second):
		}
	}
}

// sendTree writes a directory and all that is in it to the tar stream: directories, regular
// files and symbolic links. It never follows a link.
func sendTree(disk *safefs.Dir, tw *tar.Writer, rel string) (files, bytes int64, err error) {
	fi, err := disk.Lstat(rel)
	if err != nil {
		return 0, 0, err
	}
	name := transferData + strings.TrimPrefix(strings.TrimPrefix(rel, "data"), "/")
	switch {
	case fi.IsDir():
		if rel != "data" {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime()}); err != nil {
				return 0, 0, err
			}
		}
		entries, err := disk.ReadDir(rel)
		if err != nil {
			return 0, 0, err
		}
		for _, e := range entries {
			f, b, err := sendTree(disk, tw, path.Join(rel, e.Name()))
			files, bytes = files+f, bytes+b
			if err != nil {
				return files, bytes, err
			}
		}
		return files, bytes, nil
	case fi.Mode().IsRegular():
		f, err := disk.OpenFile(rel, unix.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			return 0, 0, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return 0, 0, err
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(st.Mode().Perm()), Size: st.Size(), ModTime: st.ModTime()}); err != nil {
			return 0, 0, err
		}
		n, err := io.CopyN(tw, f, st.Size())
		return 1, n, err
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := readLink(disk, rel)
		if err != nil {
			return 0, 0, err
		}
		return 1, 0, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777, ModTime: fi.ModTime()})
	default:
		// A socket or a pipe of a database server that does not operate has no data.
		return 0, 0, nil
	}
}

// readLink gives the target of a symbolic link in disk.
func readLink(disk *safefs.Dir, rel string) (string, error) {
	f, err := disk.OpenFile(path.Dir(rel), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(int(f.Fd()), path.Base(rel), buf)
	if err != nil {
		return "", &fs.PathError{Op: "readlink", Path: rel, Err: err}
	}
	return string(buf[:n]), nil
}

// receiveServer lets the old server connect for the time of the move, and writes the data of
// the database server that it sends. The database server does not operate.
func (d *Deps) receiveServer(ctx context.Context, in transferIn) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opDBReceive, in, nil)
	}
	own, peer, err := d.waitTransferAddresses(ctx, in)
	if err != nil {
		return err
	}
	s := in.Server
	customer, record, _, err := d.State.DatabaseServer(s.ID)
	if err != nil {
		return err
	}
	if customer == "" || record.Engine != s.Engine {
		return fmt.Errorf("this server does not have %s", s)
	}
	marker := filepath.Join(d.dbDir(s), receivedFile)
	if b, err := os.ReadFile(marker); err == nil && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(b))), []byte(in.Token)) == 1 {
		// An earlier run of the job received the data.
		return nil
	}
	uid, gid, err := d.Host.FileOwner(dbUser(s))
	if err != nil {
		return err
	}

	deadline := time.Now().Add(time.Duration(in.Timeout) * time.Second)
	element := fmt.Sprintf("{ %s . %s . %d timeout %ds }", peer, own, mesh.TransferPort, in.Timeout)
	if _, err := d.Run.Run(ctx, "nft", "add", "element", "inet", "cloudfactory", mesh.SetTransfers, element); err != nil {
		return fmt.Errorf("open the firewall for the move: %w", err)
	}
	defer func() {
		_, _ = d.Run.Run(context.WithoutCancel(ctx), "nft", "delete", "element", "inet", "cloudfactory", mesh.SetTransfers, fmt.Sprintf("{ %s . %s . %d }", peer, own, mesh.TransferPort))
	}()

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(own, mesh.TransferPort).String())
	if err != nil {
		return err
	}
	defer l.Close()
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	if tl, ok := l.(*net.TCPListener); ok {
		_ = tl.SetDeadline(deadline)
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("the old server did not send %s: %w", s, err)
		}
		remote, _ := netip.ParseAddrPort(conn.RemoteAddr().String())
		if remote.Addr().Unmap() != peer {
			conn.Close()
			continue
		}
		// The end of the job also ends a transfer that runs (finding G4-05).
		stopConn := context.AfterFunc(ctx, func() { conn.Close() })
		ok, err := d.receiveFrom(conn, in, uid, gid, record.DiskBytes)
		stopConn()
		if !ok {
			// A connection with a wrong token does not stop the move.
			conn.Close()
			continue
		}
		if err == nil {
			_, err = host.WriteFile(marker, in.Token+"\n", 0o600)
		}
		if err == nil {
			// The old server starts its copy again when it does not get this answer in an
			// error case, so this server does not start its copy when the answer did not
			// go out.
			if _, werr := io.WriteString(conn, transferOK); werr != nil {
				_ = os.Remove(marker)
				err = fmt.Errorf("the answer to the old server did not go out: %w", werr)
			}
		} else {
			_, _ = io.WriteString(conn, "error: "+err.Error()+"\n")
		}
		conn.Close()
		if err == nil {
			slog.Info("received a database server", "server", s.ID, "from", peer)
		}
		return err
	}
}

// receiveFrom reads the token and the tar stream from one connection. ok is false when the
// connection does not have the token of the move.
func (d *Deps) receiveFrom(conn net.Conn, in transferIn, uid, gid int, limit int64) (ok bool, err error) {
	idle := &idleReader{conn: conn, timeout: 30 * time.Second}
	r := bufio.NewReaderSize(idle, 1<<20)
	line, err := r.ReadSlice('\n')
	if err != nil {
		return false, err
	}
	token, found := strings.CutPrefix(strings.TrimSuffix(string(line), "\n"), transferHello)
	if !found || subtle.ConstantTimeCompare([]byte(token), []byte(in.Token)) != 1 {
		return false, nil
	}
	// The data can take hours. A connection that sends nothing for transferIdle ends.
	idle.timeout = transferIdle
	s := in.Server
	disk, err := safefs.Open(d.diskDir(s))
	if err != nil {
		return true, err
	}
	defer disk.Close()
	// The data directory is empty when the data arrive, also after a move that stopped.
	if err := disk.RemoveAll("data"); err != nil {
		return true, err
	}
	if err := disk.Mkdir("data", 0o700, uid, gid); err != nil {
		return true, err
	}
	secret, err := receiveTree(disk, tar.NewReader(r), uid, gid, limit)
	if err != nil {
		return true, fmt.Errorf("receive the data of %s: %w", s, err)
	}
	if err := checkSecret(s.Engine, secret); err != nil {
		return true, err
	}
	if err := d.writeSecret(s, secret); err != nil {
		return true, err
	}
	unix.Sync()
	return true, nil
}

// idleReader ends a read when the connection sends nothing for the time of timeout.
type idleReader struct {
	conn    net.Conn
	timeout time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(r.timeout))
	return r.conn.Read(p)
}

// receiveTree writes the files of a tar stream in the data directory, with the owner of the
// database server. It gives the secret of the superuser. limit is the disk limit of the
// database server, or 0 for no limit: root does not have the quota of the user.
func receiveTree(disk *safefs.Dir, tr *tar.Reader, uid, gid int, limit int64) (secret []byte, err error) {
	var total, entries int64
	// A database server with no disk limit can use the free space of the file system, less a
	// reserve for the other database servers. root writes the files, and a user quota does
	// not hold root (adversarial review, finding G4-06).
	if limit <= 0 {
		var st unix.Statfs_t
		if err := unix.Statfs(disk.Base(), &st); err == nil {
			limit = max(int64(st.Bavail)*st.Bsize-receiveReserve, 1)
		}
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Each entry counts: a directory, a link and an empty file use an inode and a block.
		entries++
		total += 4096
		if entries > maxReceiveEntries {
			return nil, fmt.Errorf("the data has more than %d files", maxReceiveEntries)
		}
		if limit > 0 && total > limit {
			return nil, fmt.Errorf("the data is larger than the space of %d bytes for this database server", limit)
		}
		if h.Name == transferSecret && h.Typeflag == tar.TypeReg {
			if h.Size > maxSecret {
				return nil, errors.New("the secret is too large")
			}
			if secret, err = io.ReadAll(io.LimitReader(tr, maxSecret)); err != nil {
				return nil, err
			}
			continue
		}
		rel, ok := strings.CutPrefix(strings.TrimSuffix(h.Name, "/"), transferData)
		if !ok || rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("the name %q in the data is not valid", h.Name)
		}
		rel = path.Join("data", rel)
		mode := os.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := disk.MkdirAll(rel, mode, uid, gid); err != nil {
				return nil, err
			}
			if err := disk.SetDir(rel, mode, uid, gid); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			total += h.Size
			if limit > 0 && total > limit {
				return nil, fmt.Errorf("the data is larger than the disk limit of %d bytes", limit)
			}
			if err := receiveFile(disk, tr, rel, h.Size, mode, uid, gid); err != nil {
				return nil, err
			}
		case tar.TypeSymlink:
			if err := disk.Symlink(h.Linkname, rel); err != nil {
				return nil, err
			}
			if err := disk.Chown(rel, uid, gid); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("the entry %q in the data has a type that is not permitted", h.Name)
		}
	}
	if secret == nil {
		return nil, errors.New("the data has no secret")
	}
	return secret, nil
}

const (
	// maxReceiveEntries is the largest number of files, directories and links of a database
	// server that moves.
	maxReceiveEntries = 5_000_000
	// receiveReserve is the free space that a move of a database server with no disk limit
	// leaves on the file system.
	receiveReserve = 1 << 30
)

// receiveFile writes one regular file that does not exist.
func receiveFile(disk *safefs.Dir, r io.Reader, rel string, size int64, mode os.FileMode, uid, gid int) error {
	f, err := disk.OpenFile(rel, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if _, err := io.CopyN(f, r, size); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return f.Close()
}

// checkSecret checks the secret of the superuser from the old server. The entry point of
// the image reads the environment file, so it can have only the names that the agent writes.
func checkSecret(engine string, secret []byte) error {
	text := string(secret)
	if engine == "valkey" {
		if !regexp.MustCompile(`^[0-9a-f]{32,128}\n?$`).MatchString(text) {
			return errors.New("the secret of the Valkey administrator is not valid")
		}
		return nil
	}
	names := map[string]bool{"POSTGRES_PASSWORD": engine == "postgresql", "MARIADB_ROOT_PASSWORD": engine == "mariadb", "MYSQL_PWD": engine == "mariadb"}
	value := regexp.MustCompile(`^[0-9a-f]{16,256}$`)
	for i, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		name, v, ok := strings.Cut(l, "=")
		if !ok || !names[name] || !value.MatchString(v) {
			return fmt.Errorf("line %d of the secret of the %s superuser is not valid", i+1, engine)
		}
	}
	return nil
}

// writeSecret replaces the secret of the superuser with the secret from the old server. The
// secret agrees with the data.
func (d *Deps) writeSecret(s dbServer, secret []byte) error {
	p := d.secretPath(s)
	if s.Engine == "valkey" {
		_, err := host.WriteFile(p, string(secret), 0o600)
		return err
	}
	// As writeSuperuserSecret: root owns the file, and the group of the user can read it.
	if _, err := host.WriteFile(p, string(secret), 0o600); err != nil {
		return err
	}
	_, gid, err := d.Host.FileOwner(dbUser(s))
	if err != nil {
		return err
	}
	if err := os.Lchown(p, 0, gid); err != nil {
		return err
	}
	return os.Chmod(p, 0o640)
}
