// Command cf-agent applies the desired state of the panel on one server (spec section 6.2).
//
// Usage:
//
//	cf-agent join --panel URL --token TOKEN [--name NAME] [--roles web,database] [--address HOST]
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
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
  cf-agent join --panel URL --token TOKEN [--name NAME] [--roles web] [--address HOST]
  cf-agent serve
  cf-agent gvisor install|rollback
  cf-agent panel install --domain DOMAIN
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *panelURL == "" || *token == "" {
		return errors.New("join needs --panel and --token")
	}
	if _, err := os.Stat(*configPath); err == nil {
		return fmt.Errorf("%s exists; this server has already joined a panel", *configPath)
	}

	cfg := config.Defaults()
	cfg.PanelURL = strings.TrimRight(*panelURL, "/")
	cfg.Registry = strings.TrimRight(*registry, "/")
	// The panel on the same server reaches the agent through the gateway of its container.
	hosts := []string{*address, "127.0.0.1", "host.containers.internal"}
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

	resp, err := panel.New(cfg.PanelURL, "", nil).Join(ctx, panel.JoinRequest{
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
	reporter := panel.New(cfg.PanelURL, cfg.AgentToken, &cert)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	deps := &actions.Deps{
		Host: host.New(run, cfg.SitesDir), Run: run, Edge: caddy.New(cfg.CaddyAdmin), Apt: apt,
		Updater: &selfupdate.Updater{ReleaseURL: cfg.ReleaseURL, Binary: binaryPath, Key: key},
		Gvisor:  quadlet.Runtime{RunscPath: gv.Link(), Platform: cfg.GVisorPlatform},
		State:   actions.NewState(cfg.DataDir), Health: actions.HTTPHealth, DNSPointsHere: actions.LocalDNS(),
		Reboot: func(ctx context.Context) error {
			_, err := run.Run(ctx, "systemctl", "reboot")
			return err
		},
		// systemd starts the agent again (Restart=always in cf-agent.service).
		Restart:            cancel,
		Arch:               runtime.GOARCH,
		MaxWatch:           5 * time.Minute,
		ResticPasswordFile: resticKey,
	}
	// The edge proxy operates in gVisor like all other containers. Its image is signed (FR-1101).
	if err := deps.EnsureEdge(ctx, cfg.Registry+"/edge:"+version, cfg.DataDir); err != nil {
		slog.Error("start edge proxy", "err", err)
	}
	runner := &jobs.Runner{Dir: filepath.Join(cfg.DataDir, "jobs"), Reporter: reporter}
	if job, ok := deps.ResumeReboot(); ok {
		runner.Resume(job)
	}
	collector := status.Collector{Run: run, Apt: apt, AgentVersion: version, GVisorVersion: gv.Current, Services: []string{"cf-agent", "nftables"}, UserServices: []string{host.EdgeUser + ":cf-edge.service"}}

	go reportLoop(ctx, reporter, collector, time.Duration(cfg.ReportSeconds)*time.Second)
	go func() {
		for range time.Tick(24 * time.Hour) {
			runner.Prune(time.Now().Add(-30 * 24 * time.Hour))
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
		return errors.New("usage: cf-agent gvisor install [VERSION] | rollback")
	}
	v := cfg.GVisorVersion
	if len(args) > 1 {
		v = args[1]
	}
	arch, err := gvisor.Arch(runtime.GOARCH)
	if err != nil {
		return err
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

// panelUser is the Unix user of the panel containers.
const panelUser = "cf-panel"

// panelCmd pulls the panel images and starts them in gVisor (FR-1808).
func panelCmd(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "install" {
		return errors.New("usage: cf-agent panel install --domain DOMAIN")
	}
	fs := flag.NewFlagSet("panel", flag.ContinueOnError)
	domain := fs.String("domain", "", "the domain of the panel, for example panel.example.com")
	registry := fs.String("registry", config.Defaults().Registry, "the registry of the panel images")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := validate.Domain(*domain); err != nil {
		return err
	}
	cfg := config.Defaults()
	run := execx.Host{}
	h := host.New(run, cfg.SitesDir)
	gv := gvisor.Installer{Root: gvisorRoot, BinDir: gvisorBin}
	p := quadlet.Panel{Registry: *registry, Version: version, Domain: *domain, DataDir: filepath.Join(cfg.DataDir, "panel"), Gvisor: quadlet.Runtime{RunscPath: gv.Link(), Platform: cfg.GVisorPlatform}, APIPort: 19080, WebPort: 19081}
	for _, image := range []string{p.Registry + "/panel-api:" + p.Version, p.Registry + "/panel-web:" + p.Version} {
		if _, err := h.Podman(ctx, panelUser, "pull", "--quiet", image); err != nil {
			return err
		}
	}
	for name, content := range quadlet.PanelUnits(p) {
		if _, err := h.WriteUnit(panelUser, name, content); err != nil {
			return err
		}
	}
	if _, err := h.Systemctl(ctx, panelUser, "daemon-reload"); err != nil {
		return err
	}
	for _, svc := range []string{"cf-panel-api.service", "cf-panel-queue.service", "cf-panel-scheduler.service", "cf-panel-web.service"} {
		if _, err := h.Systemctl(ctx, panelUser, "start", svc); err != nil {
			return err
		}
	}
	// The edge proxy sends /api to the API and all other paths to the web UI.
	edge := caddy.New(cfg.CaddyAdmin)
	if err := edge.EnsureBase(ctx); err != nil {
		return err
	}
	if err := edge.ApplyPanel(ctx, *domain, p.APIPort, p.WebPort); err != nil {
		return err
	}
	fmt.Printf("The panel operates at https://%s\n", *domain)
	return nil
}
