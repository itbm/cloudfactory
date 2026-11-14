package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/actions"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/config"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/gvisor"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/images"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/safefs"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
	"github.com/itbm/cloudfactory/agent/internal/server"
	"github.com/itbm/cloudfactory/agent/internal/signature"
	"github.com/itbm/cloudfactory/agent/internal/status"
)

// Second audit, the privilege boundary: the two processes of the agent service.
//
// systemd starts "cf-agent serve" as root. That process is the root helper. It does not
// connect to the panel, and it accepts no connection from the network. It starts the worker
// ("cf-agent work") as the user cf-agent, and does only the commands and the operations of
// its lists for the worker (packages gate and actions).
//
// The worker has the agent port, the connection to the panel, the jobs and the state. A
// fault in that code gives an attacker the rights of cf-agent, not the rights of root.
//
// The service has one unit, as before. When the worker stops, the root helper stops with
// the same result, and systemd starts the two again. Thus an update of the agent, a restart
// and "cf-agent rollback" operate as before.

// oneProcessEnv makes the agent operate as one root process, as before this release. It is
// for an administrator who must go around a fault of the two processes: add
// "Environment=CF_AGENT_ONE_PROCESS=1" to cf-agent.service.
const oneProcessEnv = "CF_AGENT_ONE_PROCESS"

// serve is the root helper of the agent service. It prepares the server, starts the worker
// and does the root work for it.
func serve(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New(`"cf-agent serve" must start as root`)
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return err
	}
	run := execx.Host{Env: []string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=a"}}
	updateGvisor(ctx, &cfg)
	root, err := newDeps(cfg, run)
	if err != nil {
		return err
	}
	root.SavePlatform = func(platform string) error {
		return changeConfig(func(c *config.Config) { c.GVisorPlatform = platform })
	}
	root.SavePanelCA = func(fingerprint string) error {
		return changeConfig(func(c *config.Config) { c.PanelCASHA256 = fingerprint })
	}
	// The disk limit of a website needs user quotas on the file system of the websites.
	root.QuotaMount = root.Host.QuotaMount(ctx, cfg.SitesDir)
	srv := &gate.Server{Run: run, Allow: root.Allow, Ops: root.RootOps(), Dial: root.RootDial}

	if os.Getenv(oneProcessEnv) == "1" {
		slog.Warn("the agent operates as one root process, because " + oneProcessEnv + " is set")
		return work(ctx, cfg, srv.Local())
	}
	uid, gid, err := workerUser(ctx, run)
	if err == nil {
		err = prepareWorker(cfg, uid, gid)
	}
	var l net.Listener
	if err == nil {
		l, err = listenGate(gate.DefaultSocket, gid)
	}
	if err != nil {
		// The websites of the server are more important than the boundary: the agent
		// operates, and the log and this error tell the administrator.
		slog.Error("the agent cannot start its worker as the user "+host.AgentUser+"; it operates as one root process", "err", err)
		return work(ctx, cfg, srv.Local())
	}
	defer l.Close()
	srv.UIDs = []uint32{uint32(uid)}
	go func() {
		if err := srv.Serve(l); err != nil && ctx.Err() == nil {
			slog.Error("the root helper stopped", "err", err)
		}
	}()
	return superviseWorker(ctx, uid, gid)
}

// changeConfig changes the settings file of the agent.
func changeConfig(change func(c *config.Config)) error {
	c, err := config.Load(config.DefaultPath)
	if err != nil {
		return err
	}
	change(&c)
	return config.Save(config.DefaultPath, c)
}

// updateGvisor installs the gVisor version of this agent version, when it is newer than the
// version of the server. Containers that start after the switch use it. Containers that
// operate keep the old version until they restart.
func updateGvisor(ctx context.Context, cfg *config.Config) {
	if !gvisor.Newer(gvisor.Version, cfg.GVisorVersion) {
		return
	}
	arch, err := gvisor.Arch(runtime.GOARCH)
	if err != nil {
		return
	}
	gv := gvisor.Installer{BaseURL: gvisor.ReleaseURL, Root: gvisorRoot, BinDir: gvisorBin}
	if err := gv.Install(ctx, gvisor.Version, arch); err != nil {
		slog.Error("update gVisor", "version", gvisor.Version, "err", err)
		return
	}
	cfg.GVisorVersion = gvisor.Version
	if err := changeConfig(func(c *config.Config) { c.GVisorVersion = gvisor.Version }); err != nil {
		slog.Error("save the gVisor version", "err", err)
	}
}

// newDeps gives the settings of the actions. The worker and the root helper make them in
// the same way: the helper makes each path and each image name from its own copy, and does
// not use a value of the worker for them.
func newDeps(cfg config.Config, run execx.Runner) (*actions.Deps, error) {
	keyPEM, err := os.ReadFile(signingKey)
	if err != nil {
		return nil, fmt.Errorf("read the signing key: %w", err)
	}
	key, err := signature.ParsePublicKey(keyPEM)
	if err != nil {
		return nil, err
	}
	gv := gvisor.Installer{Root: gvisorRoot, BinDir: gvisorBin}
	h := host.New(run, cfg.SitesDir)
	// All website users share one read-only copy of each runtime image.
	h.StorageConfig = images.DefaultConfig
	d := &actions.Deps{
		Host: h, Run: run, Apt: hostupdate.New(run), Images: images.New(run, host.SitesGroup),
		Updater:            &selfupdate.Updater{ReleaseURL: cfg.ReleaseURL, Binary: binaryPath, Key: key, Current: version},
		Gvisor:             quadlet.Runtime{RunscPath: gv.Link(), Platform: cfg.GVisorPlatform},
		State:              actions.NewState(cfg.DataDir),
		Arch:               runtime.GOARCH,
		ResticPasswordFile: resticKey,
		DataDir:            cfg.DataDir,
		DatabaseClientHost: cfg.DatabaseClientHost,
		// The images of the services have the version of the agent. The release signs them.
		ServiceImage: func(name string) string { return cfg.Registry + "/" + name + ":" + version },
		EdgeImage:    cfg.Registry + "/edge:" + version,
		SftpImage:    cfg.Registry + "/sftp:" + version,
		SftpPort:     cfg.SftpPort,
	}
	// CF-13: the admin API of the edge proxy is a Unix socket.
	if path, ok := strings.CutPrefix(cfg.CaddyAdmin, "unix://"); ok {
		d.EdgeSocket = path
	}
	return d, nil
}

// workerUser gives the UID and the GID of the user of the worker. A server from an earlier
// release does not have the user: the agent makes it, as the installer of this release does.
func workerUser(ctx context.Context, run execx.Runner) (uid, gid int, err error) {
	u, err := user.Lookup(host.AgentUser)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		// The user has no home directory and no shell. It is in no other group.
		if _, err := run.Run(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", host.AgentUser); err != nil {
			return 0, 0, err
		}
		slog.Info("made the user of the worker of the agent", "user", host.AgentUser)
		u, err = user.Lookup(host.AgentUser)
	}
	if err != nil {
		return 0, 0, err
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, err
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, err
	}
	// The worker is never root, and never a website: the firewall and the root helper give
	// the UID range of the websites the rights of a website.
	if uid == 0 || gid == 0 || (uid >= host.WebsiteIDMin && uid <= host.WebsiteIDMax) {
		return 0, 0, fmt.Errorf("the user %s has the UID %d and the GID %d, which the worker cannot use", host.AgentUser, uid, gid)
	}
	return uid, gid, nil
}

// prepareWorker gives the worker the files that it needs, and no more. The worker reads the
// settings of the agent and the private key of the agent port, and it owns the records of
// the jobs. Root keeps all other files of the agent, for example the state and the backups.
//
// The paths do not change, so that the binary of an earlier release finds the files after
// "cf-agent rollback".
func prepareWorker(cfg config.Config, uid, gid int) error {
	// The group of the worker can read the settings and the key. Only root can write them,
	// and the worker cannot change their directories.
	for _, f := range []struct {
		path string
		mode os.FileMode
	}{
		{config.DefaultPath, 0o640},
		{filepath.Dir(cfg.KeyFile()), 0o750},
		{cfg.KeyFile(), 0o640},
		{cfg.CertFile(), 0o644},
	} {
		if fi, err := os.Lstat(f.path); err != nil {
			return err
		} else if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", f.path)
		}
		if err := os.Lchown(f.path, 0, gid); err != nil {
			return err
		}
		if err := os.Chmod(f.path, f.mode); err != nil {
			return err
		}
	}
	return ownJobs(cfg.DataDir, uid, gid)
}

// ownJobs gives the records of the jobs to the worker.
//
// After the first start, the worker can write in the directory. Thus root does not change
// the owner of all that is in it again: it changes only a file that root made, for example
// while the binary of an earlier release operated, and never the target of a link.
func ownJobs(dataDir string, uid, gid int) error {
	data, err := safefs.Open(dataDir)
	if err != nil {
		return err
	}
	defer data.Close()
	const jobsDir = "jobs"
	fi, err := data.Lstat(jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return data.Mkdir(jobsDir, 0o700, uid, gid)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", filepath.Join(dataDir, jobsDir))
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
		// The first start with a worker: root made all the files.
		if err := data.ChownTree(jobsDir, uid, gid); err != nil {
			return err
		}
		return data.SetDir(jobsDir, 0o700, uid, gid)
	}
	records, err := safefs.Open(filepath.Join(dataDir, jobsDir))
	if err != nil {
		return err
	}
	defer records.Close()
	entries, err := records.ReadDir(".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		fi, err := records.Lstat(e.Name())
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		// A file with more than one name can be a file of root at a different path.
		if !ok || !fi.Mode().IsRegular() || st.Nlink != 1 || int(st.Uid) != 0 {
			continue
		}
		if err := records.Chown(e.Name(), uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// listenGate opens the socket of the root helper. Only root and the group of the worker can
// connect, and the helper checks the user of each connection again.
func listenGate(path string, gid int) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	// Only root can make a directory in /run, so root is its owner.
	if err := os.Chown(dir, -1, gid); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return nil, err
	}
	// A socket from an agent process that stopped.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(os.Chown(path, -1, gid), os.Chmod(path, 0o660)); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// superviseWorker starts the worker as its user and waits until it stops. The result of the
// worker is the result of the service: systemd starts the two processes again.
func superviseWorker(ctx context.Context, uid, gid int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "work")
	cmd.Dir = execx.Dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	// The worker has only its own group: Groups replaces the groups of root.
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{uint32(gid)}}}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the worker: %w", err)
	}
	slog.Info("the root helper started the worker", "user", host.AgentUser, "pid", cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		// The stop signal of systemd goes to the two processes. A signal for only this
		// process, for example from an administrator, goes to the worker here. The helper
		// waits: the jobs of the worker need it until they stop.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		err = <-done
	}
	if err != nil {
		return fmt.Errorf("the worker stopped: %w", err)
	}
	return nil
}

// workCmd is the worker of the agent service, as its own process.
func workCmd(ctx context.Context) error {
	if os.Geteuid() == 0 {
		return errors.New(`"cf-agent work" is a part of "cf-agent serve"; it does not start as root`)
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return err
	}
	return work(ctx, cfg, &gate.Client{Path: gate.DefaultSocket})
}

// work is the worker of the agent: the agent port, the reports to the panel and the jobs. g
// is its connection to the root helper.
func work(ctx context.Context, cfg config.Config, g gate.Gate) error {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		return err
	}
	deps, err := newDeps(cfg, g)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	deps.Health = deps.HTTPHealth
	deps.DNSPointsHere = actions.LocalDNS(cfg.PublicAddresses...)
	deps.Reboot = func(ctx context.Context) error {
		_, err := g.Run(ctx, "systemctl", "reboot")
		return err
	}
	// systemd starts the agent again (Restart=always in cf-agent.service).
	deps.Restart = cancel
	// The stop signal of systemd, for example at a shutdown, closes this channel.
	deps.Stopping = ctx.Done()
	deps.MaxWatch = 5 * time.Minute
	deps.Edge = edgeClient(cfg, deps)
	deps.UseGate(g)

	apt := deps.Apt
	if err := apt.Hold(ctx); err != nil {
		slog.Warn("hold container packages", "err", err)
	}
	gv := gvisor.Installer{Root: gvisorRoot, BinDir: gvisorBin}
	reporter := panel.New(cfg.PanelURL, cfg.AgentToken, &cert, cfg.PanelServerCertificateSHA256, cfg.PanelCASHA256)
	dialLocal(reporter, deps)

	h := deps.Host
	// FR-202: the disk I/O limit needs the disk of the websites, and the disk limit needs
	// user quotas on its file system.
	if dev, err := h.BlockDevice(ctx, cfg.SitesDir); err == nil {
		deps.BlockDevice = dev
	} else {
		slog.Warn("no disk I/O limits", "err", err)
	}
	if deps.QuotaMount = h.QuotaMount(ctx, cfg.SitesDir); deps.QuotaMount == "" {
		slog.Warn("no disk limits: user quotas are off on the file system of the websites", "dir", cfg.SitesDir)
	}
	// The edge proxy operates in gVisor like all other containers. Its image is signed (FR-1101).
	if err := deps.EnsureEdge(ctx, deps.EdgeImage, cfg.DataDir); err != nil {
		slog.Error("start edge proxy", "err", err)
	}
	if err := deps.RestoreLocalNames(ctx); err != nil {
		slog.Error("set the certificates of the local names", "err", err)
	}
	if err := deps.ShareImages(ctx); err != nil {
		slog.Error("start the shared image store", "err", err)
	}
	// The database containers get the images of this agent version. A server can have the
	// containers of many customers, so the agent does not wait for them: a job that needs a
	// container waits for that container only.
	go func() {
		if err := deps.RefreshDatabaseServices(ctx); err != nil {
			slog.Error("start database containers", "err", err)
		}
	}()
	// The SFTP container of the websites. On a server from release 0.1, this also moves SFTP
	// from the SSH service of the host to the container.
	if err := deps.EnsureSftp(ctx); err != nil {
		slog.Error("start the SFTP container", "err", err)
	}
	runner := &jobs.Runner{Dir: filepath.Join(cfg.DataDir, "jobs"), Reporter: reporter,
		Timeout: time.Duration(cfg.JobTimeoutMinutes) * time.Minute, LongTimeout: time.Duration(cfg.LongJobTimeoutMinutes) * time.Minute}
	if job, ok := deps.ResumeReboot(); ok {
		runner.Resume(job)
	}
	collector := status.Collector{Run: g, Apt: apt, AgentVersion: version, GVisorVersion: gv.Current, Services: []string{"cf-agent", "ssh", "cf-sftp"}, UserServices: []string{host.EdgeUser + ":cf-edge.service"}, DiskPath: cfg.SitesDir, Extra: deps.FillReport}

	go reportLoop(ctx, reporter, collector, time.Duration(cfg.ReportSeconds)*time.Second, cfg.PanelServerCertificateSHA256 == "", deps.SavePanelCAPin)
	go func() {
		for range time.Tick(24 * time.Hour) {
			runner.Prune(time.Now().Add(-30 * 24 * time.Hour))
			deps.PruneImages()
		}
	}()

	slog.Info("cf-agent started", "version", version, "listen", cfg.Listen, "uid", os.Geteuid())
	err = server.Serve(ctx, cfg.Listen, server.Handler(&server.Service{Deps: deps, Runner: runner, Status: collector}), server.TLSConfig(cert, cfg.PanelCertificateSHA256))
	runner.Wait()
	return err
}

// edgeClient gives the client of the admin API of the edge proxy. Only the edge user and
// root can open the directory of the admin socket (CF-13), so the worker connects through
// the root helper.
func edgeClient(cfg config.Config, deps *actions.Deps) *caddy.Client {
	edge := caddy.New(cfg.CaddyAdmin)
	if deps.EdgeSocket != "" {
		edge.HTTP.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return deps.DialEdge(ctx)
		}}
	}
	return edge
}

// dialLocal makes the client of the panel connect through the root helper when the panel is
// on this server. The agent on the panel server reports to a port of the private service
// address, and the firewall lets only root and the edge proxy connect to that address.
func dialLocal(c *panel.Client, deps *actions.Deps) {
	tr, ok := c.HTTP.Transport.(*http.Transport)
	if !ok {
		return
	}
	direct := tr.DialContext
	if direct == nil {
		direct = (&net.Dialer{}).DialContext
	}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if h, _, err := net.SplitHostPort(address); err == nil && h == quadlet.ServiceAddress {
			return deps.DialLocal(ctx, address)
		}
		return direct(ctx, network, address)
	}
}
