// Command cf-agent applies the desired state of the panel on one server (spec section 6.2).
//
// Usage:
//
//	cf-agent join --panel URL --token TOKEN [--name NAME] [--roles web,database] [--address HOST] [--sftp-port PORT]
//	cf-agent serve
//	cf-agent gvisor install|rollback
//	cf-agent panel install --domain DOMAIN
//	cf-agent rollback
//	cf-agent version
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/actions"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/config"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/gvisor"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/images"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/pki"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
	"github.com/itbm/cloudfactory/agent/internal/server"
	"github.com/itbm/cloudfactory/agent/internal/signature"
	"github.com/itbm/cloudfactory/agent/internal/status"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// version is set at build time with -ldflags "-X main.version=0.1.0".
var version = "0.0.0-dev"

const (
	binaryPath  = "/usr/local/bin/cf-agent"
	signingKey  = "/etc/cloudfactory/cosign.pub"
	resticKey   = "/etc/cloudfactory/restic.key"
	gvisorRoot  = "/usr/local/lib/cloudfactory/gvisor"
	gvisorBin   = "/usr/local/bin"
	usageString = `Usage:
  cf-agent join --panel URL --token TOKEN [--name NAME] [--roles web] [--address HOST] [--public-ip IP] [--sftp-port PORT]
  cf-agent serve
  cf-agent gvisor install [--sha512 SUM] [VERSION] | rollback
  cf-agent panel install --domain DOMAIN [--registry URL]
  cf-agent panel update
  cf-agent panel reset-password EMAIL [--remove-totp]
  cf-agent panel unlock EMAIL [--address IP]
  cf-agent rollback
  cf-agent version`
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usageString)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "join":
		err = join(ctx, os.Args[2:])
	case "serve":
		err = serve(ctx)
	case "gvisor":
		err = gvisorCmd(ctx, os.Args[2:])
	case "panel":
		err = panelCmd(ctx, os.Args[2:])
	case "rollback":
		err = selfupdate.Updater{Binary: binaryPath}.Rollback()
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, usageString)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cf-agent:", err)
		os.Exit(1)
	}
}

// join makes the certificate of the agent and adds the server to the panel (FR-1201).
func join(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	panelURL := fs.String("panel", "", "the address of the panel, for example https://panel.example.com")
	token := fs.String("token", "", "the join token from the panel")
	hostname, _ := os.Hostname()
	name := fs.String("name", strings.Split(hostname, ".")[0], "the name of this server in the panel")
	roles := fs.String("roles", "web", "the roles of this server, separated by commas")
	address := fs.String("address", hostname, "the host name or IP address that the panel uses to reach this server")
	registry := fs.String("registry", config.Defaults().Registry, "the registry of the images, for example a mirror")
	configPath := fs.String("config", config.DefaultPath, "the settings file")
	public := fs.String("public-ip", "", "the public IP addresses of this server behind NAT, separated by commas")
	sftpPort := fs.Int("sftp-port", quadlet.DefaultSftpPort, "the public port of the SFTP service of the websites")
	panelPin := fs.String("panel-certificate-sha256", "", "the SHA-256 fingerprint of the certificate of the panel; only for the server of the panel, which uses the HTTPS port of the API")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *panelURL == "" || *token == "" {
		return errors.New("join needs --panel and --token")
	}
	if err := validate.PanelURL(*panelURL); err != nil {
		return err
	}
	*panelPin = strings.ToLower(strings.TrimSpace(*panelPin))
	if *panelPin != "" {
		if err := pki.CheckFingerprint(*panelPin); err != nil {
			return fmt.Errorf("--panel-certificate-sha256: %w", err)
		}
		if !strings.HasPrefix(*panelURL, "https://") {
			return errors.New("--panel-certificate-sha256 needs a panel address that starts with https://")
		}
	}
	if _, err := os.Stat(*configPath); err == nil {
		return fmt.Errorf("%s exists; this server has already joined a panel", *configPath)
	}

	if err := validate.SftpPort(*sftpPort); err != nil {
		return err
	}
	cfg := config.Defaults()
	cfg.SftpPort = *sftpPort
	cfg.PanelURL = strings.TrimRight(*panelURL, "/")
	cfg.PanelServerCertificateSHA256 = *panelPin
	cfg.Registry = strings.TrimRight(*registry, "/")
	// Batch 1, finding 10: behind NAT the public address is not on an interface. The DNS check
	// accepts the address that the panel uses.
	if ip := net.ParseIP(*address); ip != nil && !ip.IsLoopback() {
		cfg.PublicAddresses = []string{ip.String()}
	}
	for _, a := range strings.Split(*public, ",") {
		if ip := net.ParseIP(strings.TrimSpace(a)); ip != nil && !slices.Contains(cfg.PublicAddresses, ip.String()) {
			cfg.PublicAddresses = append(cfg.PublicAddresses, ip.String())
		}
	}
	// The panel on the same server reaches the agent through the host loopback address that
	// pasta maps in its container.
	hosts := []string{*address, "127.0.0.1", quadlet.HostLoopback}
	if hostname != *address {
		hosts = append(hosts, hostname)
	}
	if err := pki.Generate(cfg.CertFile(), cfg.KeyFile(), hosts, 10*365*24*time.Hour); err != nil {
		return fmt.Errorf("make certificate: %w", err)
	}
	fp, certPEM, err := pki.FileFingerprint(cfg.CertFile())
	if err != nil {
		return err
	}
	osID, osName := readOS()
	run := execx.Host{}
	podman, _ := run.Run(ctx, "podman", "version", "--format", "{{.Client.Version}}")
	gv := gvisor.Installer{Root: gvisorRoot, BinDir: gvisorBin}
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")

	// The agent sends its new certificate in the TLS connection. The panel then knows that the
	// agent has the private key of the certificate in the request.
	cert, err := tls.LoadX509KeyPair(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		return err
	}
	resp, err := panel.New(cfg.PanelURL, "", &cert, cfg.PanelServerCertificateSHA256).Join(ctx, panel.JoinRequest{
		Token: *token, Name: *name, AgentURL: "https://" + *address + cfg.Listen,
		CertificateSHA256: fp, CertificatePEM: string(certPEM), AgentVersion: version,
		OS: osID, OSName: osName, Kernel: strings.TrimSpace(string(kernel)), Arch: archName(),
		BareMetal: bareMetal(ctx, run), PodmanVersion: strings.TrimSpace(podman), GVisorVersion: gv.Current(),
		Roles: strings.Split(*roles, ","),
	})
	if err != nil {
		return err
	}
	cfg.ServerID, cfg.AgentToken, cfg.PanelCertificateSHA256 = resp.ServerID, resp.AgentToken, resp.PanelCertificateSHA256
	if err := config.Save(*configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("This server joined the panel as %s (%s).\n", *name, resp.ServerID)
	return nil
}

func serve(ctx context.Context) error {
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile(), cfg.KeyFile())
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(signingKey)
	if err != nil {
		return fmt.Errorf("read the signing key: %w", err)
	}
	key, err := signature.ParsePublicKey(keyPEM)
	if err != nil {
		return err
	}

	run := execx.Host{Env: []string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=a"}}
	apt := hostupdate.New(run)
	if err := apt.Hold(ctx); err != nil {
		slog.Warn("hold container packages", "err", err)
	}
	gv := gvisor.Installer{BaseURL: gvisor.ReleaseURL, Root: gvisorRoot, BinDir: gvisorBin}
	reporter := panel.New(cfg.PanelURL, cfg.AgentToken, &cert, cfg.PanelServerCertificateSHA256)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	h := host.New(run, cfg.SitesDir)
	// All website users share one read-only copy of each runtime image.
	h.StorageConfig = images.DefaultConfig
	deps := &actions.Deps{
		Host: h, Run: run, Edge: caddy.New(cfg.CaddyAdmin), Apt: apt, Images: images.New(run, host.SitesGroup),
		Updater: &selfupdate.Updater{ReleaseURL: cfg.ReleaseURL, Binary: binaryPath, Key: key, Current: version},
		Gvisor:  quadlet.Runtime{RunscPath: gv.Link(), Platform: cfg.GVisorPlatform},
		State:   actions.NewState(cfg.DataDir), Health: actions.HTTPHealth, DNSPointsHere: actions.LocalDNS(cfg.PublicAddresses...),
		Reboot: func(ctx context.Context) error {
			_, err := run.Run(ctx, "systemctl", "reboot")
			return err
		},
		// systemd starts the agent again (Restart=always in cf-agent.service).
		Restart: cancel,
		// The stop signal of systemd, for example at a shutdown, closes this channel.
		Stopping:           ctx.Done(),
		Arch:               runtime.GOARCH,
		MaxWatch:           5 * time.Minute,
		ResticPasswordFile: resticKey,
		DataDir:            cfg.DataDir,
		DatabaseClientHost: cfg.DatabaseClientHost,
		// The images of the services have the version of the agent. The release signs them.
		ServiceImage: func(name string) string { return cfg.Registry + "/" + name + ":" + version },
		EdgeImage:    cfg.Registry + "/edge:" + version,
		SftpImage:    cfg.Registry + "/sftp:" + version,
		SftpPort:     cfg.SftpPort,
		SavePlatform: func(platform string) error {
			c, err := config.Load(config.DefaultPath)
			if err != nil {
				return err
			}
			c.GVisorPlatform = platform
			return config.Save(config.DefaultPath, c)
		},
	}
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
	// An agent update brings the gVisor version of the new agent. Containers that start after
	// the switch use it. Containers that operate keep the old version until they restart.
	if gvisor.Newer(gvisor.Version, cfg.GVisorVersion) {
		if arch, err := gvisor.Arch(runtime.GOARCH); err == nil {
			if err := gv.Install(ctx, gvisor.Version, arch); err != nil {
				slog.Error("update gVisor", "version", gvisor.Version, "err", err)
			} else if c, err := config.Load(config.DefaultPath); err == nil {
				c.GVisorVersion = gvisor.Version
				if err := config.Save(config.DefaultPath, c); err != nil {
					slog.Error("save the gVisor version", "err", err)
				}
			}
		}
	}
	// The edge proxy operates in gVisor like all other containers. Its image is signed (FR-1101).
	if err := deps.EnsureEdge(ctx, deps.EdgeImage, cfg.DataDir); err != nil {
		slog.Error("start edge proxy", "err", err)
	}
	if err := deps.ShareImages(ctx); err != nil {
		slog.Error("start the shared image store", "err", err)
	}
	// The database containers get the images of this agent version.
	if err := deps.RefreshDatabaseServices(ctx); err != nil {
		slog.Error("start database containers", "err", err)
	}
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
	collector := status.Collector{Run: run, Apt: apt, AgentVersion: version, GVisorVersion: gv.Current, Services: []string{"cf-agent", "nftables", "ssh", "cf-sftp"}, UserServices: []string{host.EdgeUser + ":cf-edge.service"}, DiskPath: cfg.SitesDir, Extra: deps.FillReport}

	go reportLoop(ctx, reporter, collector, time.Duration(cfg.ReportSeconds)*time.Second)
	go func() {
		for range time.Tick(24 * time.Hour) {
			runner.Prune(time.Now().Add(-30 * 24 * time.Hour))
			deps.PruneImages()
		}
	}()

	slog.Info("cf-agent started", "version", version, "listen", cfg.Listen)
	err = server.Serve(ctx, cfg.Listen, server.Handler(&server.Service{Deps: deps, Runner: runner, Status: collector}), server.TLSConfig(cert, cfg.PanelCertificateSHA256))
	runner.Wait()
	return err
}

func reportLoop(ctx context.Context, c *panel.Client, s status.Collector, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	for {
		if err := c.SendReport(ctx, s.Report(ctx)); err != nil && ctx.Err() == nil {
			slog.Warn("report to panel failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// gvisorCmd installs the gVisor version from the settings, or goes back to the previous version.
func gvisorCmd(ctx context.Context, args []string) error {
	cfg := config.Defaults()
	if c, err := config.Load(config.DefaultPath); err == nil {
		cfg = c
	}
	gv := gvisor.Installer{BaseURL: gvisor.ReleaseURL, Root: gvisorRoot, BinDir: gvisorBin}
	if len(args) == 1 && args[0] == "rollback" {
		return gv.Rollback()
	}
	if len(args) < 1 || args[0] != "install" {
		return errors.New("usage: cf-agent gvisor install [--sha512 SUM] [VERSION] | rollback")
	}
	fs := flag.NewFlagSet("gvisor install", flag.ContinueOnError)
	sum := fs.String("sha512", "", "the SHA-512 checksum of gvisor.tar.bz2, for a version that this agent does not know")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	v := cfg.GVisorVersion
	if fs.NArg() > 0 {
		v = fs.Arg(0)
	}
	arch, err := gvisor.Arch(runtime.GOARCH)
	if err != nil {
		return err
	}
	if *sum != "" {
		gv.Checksums = map[string]string{v + "/" + arch: *sum}
	}
	if err := gv.Install(ctx, v, arch); err != nil {
		return err
	}
	fmt.Println("gVisor", gv.Current(), "is installed at", gv.Link())
	return nil
}

func readOS() (id, name string) {
	b, _ := os.ReadFile("/etc/os-release")
	vals := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			vals[k] = strings.Trim(v, `"`)
		}
	}
	return vals["ID"] + "-" + vals["VERSION_ID"], vals["PRETTY_NAME"]
}

func archName() string {
	if runtime.GOARCH == "amd64" {
		return "x86-64"
	}
	return runtime.GOARCH
}

// bareMetal reports whether the server is not a virtual machine. Only bare metal can use
// the gVisor KVM platform (FR-310).
func bareMetal(ctx context.Context, run execx.Runner) bool {
	out, err := run.Run(ctx, "systemd-detect-virt")
	return err != nil && strings.TrimSpace(out) == "none"
}

// panelCmd pulls the panel images and starts them in gVisor (FR-1808).
const panelUsage = `usage:
  cf-agent panel install --domain DOMAIN [--registry URL]   Install the panel, or start it again.
  cf-agent panel update                                     Change the panel to the version of this agent.
  cf-agent panel reset-password EMAIL [--remove-totp]       Give a user a new temporary password.
  cf-agent panel unlock EMAIL [--address IP]                Remove the sign-in lock of an account.`

// panelCmd has the commands for the panel server. Only root can run them.
func panelCmd(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New(panelUsage)
	}
	switch args[0] {
	case "install", "update":
	case "reset-password", "unlock":
		return panelAccount(ctx, args[0], args[1:])
	default:
		return errors.New(panelUsage)
	}
	fs := flag.NewFlagSet("panel", flag.ContinueOnError)
	domain := fs.String("domain", "", "the domain of the panel, for example panel.example.com")
	registry := fs.String("registry", config.Defaults().Registry, "the registry of the panel images")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "update" {
		// Audit finding S7: after an update of the agent, this command changes the panel
		// containers to the images of the same version. The domain and the registry are the
		// values of the installation.
		saved, err := config.Load(config.DefaultPath)
		if err != nil {
			return fmt.Errorf("read the settings of the agent: %w", err)
		}
		*registry = saved.Registry
		if *domain, err = actions.PanelDomain(filepath.Join(saved.DataDir, "panel", "panel.env")); err != nil {
			return fmt.Errorf("this server has no panel: %w", err)
		}
	}
	if err := validate.Domain(*domain); err != nil {
		return err
	}
	cfg := config.Defaults()
	run := execx.Host{}
	h := host.New(run, cfg.SitesDir)
	gv := gvisor.Installer{Root: gvisorRoot, BinDir: gvisorBin}
	p := quadlet.Panel{Registry: *registry, Version: version, Domain: *domain, DataDir: filepath.Join(cfg.DataDir, "panel"), Gvisor: quadlet.Runtime{RunscPath: gv.Link(), Platform: cfg.GVisorPlatform},
		APIPort: quadlet.PanelAPIPort, WebPort: quadlet.PanelWebPort, AgentPort: quadlet.PanelAgentPort, DatabasePort: quadlet.PanelDatabasePort}
	edge := caddy.New(cfg.CaddyAdmin)
	deps := &actions.Deps{Host: h, Run: run, Edge: edge, Gvisor: p.Gvisor}
	if err := deps.InstallPanel(ctx, p); err != nil {
		return fmt.Errorf("start the panel: %w", err)
	}
	// The agent service starts after the join, so this command starts the edge proxy itself.
	// The edge proxy sends /api to the API and all other paths to the web UI.
	if err := deps.EnsureEdge(ctx, *registry+"/edge:"+version, cfg.DataDir); err != nil {
		return fmt.Errorf("start the edge proxy: %w", err)
	}
	if err := edge.ApplyPanel(ctx, *domain, p.APIPort, p.WebPort); err != nil {
		return err
	}
	fmt.Printf("The panel operates at https://%s with version %s\n", *domain, version)
	return nil
}

// panelAccount runs an account command of the panel in the API container.
func panelAccount(ctx context.Context, command string, args []string) error {
	fs := flag.NewFlagSet("panel "+command, flag.ContinueOnError)
	removeTotp := fs.Bool("remove-totp", false, "also remove TOTP, for a user who lost the device")
	address := fs.String("address", "", "also remove the lock for this client address")
	// The email address can come before or after the options.
	var email string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		email, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if email == "" && fs.NArg() == 1 {
		email = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return errors.New(panelUsage)
	}
	if email == "" {
		return errors.New(panelUsage)
	}
	cfg := config.Defaults()
	deps := &actions.Deps{Host: host.New(execx.Host{}, cfg.SitesDir), Run: execx.Host{}}
	var out string
	var err error
	if command == "reset-password" {
		out, err = deps.ResetPanelPassword(ctx, email, *removeTotp)
	} else {
		out, err = deps.UnlockPanelSignIn(ctx, email, *address)
	}
	fmt.Print(out)
	return err
}
