// Package quadlet writes the Podman Quadlet units for websites and the edge proxy.
// systemd starts each container. Thus the websites continue to operate when the agent
// or the panel stops (NFR-04).
package quadlet

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Header is the first line of each file that the agent writes.
const Header = "# Managed by the Cloudfactory agent. The agent replaces changes to this file.\n"

// Runtime holds the gVisor settings. All containers use gVisor (FR-308). There is no
// setting for a different runtime.
type Runtime struct {
	// RunscPath is the path of the runsc binary, for example "/usr/local/bin/runsc".
	RunscPath string
	// Platform is "systrap" or "kvm" (FR-310).
	Platform string
}

// GlobalArgs are the Podman arguments that select gVisor. The network stack of gVisor
// ("sandbox") is always on (FR-1701). The agent never sets host networking (FR-1702).
func (r Runtime) GlobalArgs() string {
	return fmt.Sprintf("--runtime=%s --runtime-flag=platform=%s --runtime-flag=network=sandbox", r.RunscPath, r.Platform)
}

// Limits are the cgroup v2 limits of one website.
type Limits struct {
	VCPUs                float64
	MemoryBytes          int64
	Processes            int64
	DiskIOBytesPerSecond int64
	// BlockDevice is the disk for the I/O limit, for example "/dev/vda". Empty means no I/O limit.
	BlockDevice string
}

// Website is the input for the units of one website.
type Website struct {
	ID         string
	Name       string
	UnixUser   string
	Image      string // with "@sha256:..." when the image has a digest
	Port       int    // the host port on 127.0.0.1 that the edge proxy uses
	SiteDir    string // the files of the website on the host
	IniFile    string // the generated php.ini on the host
	Runtime    string // "frankenphp", "apache-php-fpm" or "custom"
	WorkerMode bool
	Limits     Limits
	Gvisor     Runtime
	// Suffix makes a second container name for a runtime change with no stop (FR-1108).
	Suffix string
}

// ContainerName is the name of the Podman container.
func (w Website) ContainerName() string { return "cf-" + w.UnixUser + w.Suffix }

// UnitName is the file name of the container unit.
func (w Website) UnitName() string { return w.ContainerName() + ".container" }

// ServiceName is the systemd service that Quadlet makes from the unit.
func (w Website) ServiceName() string { return w.ContainerName() + ".service" }

// NetworkName is the network of the hosting account (FR-1703).
func NetworkName(unixUser string) string { return "cf-" + unixUser }

// NetworkUnit gives the network unit of one hosting account. Each account has its own
// network, so a container cannot connect to the containers of a different account.
func NetworkUnit(unixUser string) string {
	return Header + strings.Join([]string{
		"[Network]",
		"NetworkName=" + NetworkName(unixUser),
		"Internal=false",
		"Label=io.cloudfactory.account=" + unixUser,
		"",
	}, "\n")
}

// ContainerUnit gives the container unit of a website.
func ContainerUnit(w Website) string {
	var b strings.Builder
	b.WriteString(Header)
	line := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }

	b.WriteString("[Unit]\n")
	line("Description", "Website "+w.Name)
	line("After", NetworkName(w.UnixUser)+"-network.service")
	line("Requires", NetworkName(w.UnixUser)+"-network.service")

	b.WriteString("\n[Container]\n")
	line("ContainerName", w.ContainerName())
	line("Image", w.Image)
	// The agent pulls and checks the image before it starts the unit (FR-1101).
	line("Pull", "never")
	line("GlobalArgs", w.Gvisor.GlobalArgs())
	line("Network", NetworkName(w.UnixUser)+".network")
	line("PublishPort", "127.0.0.1:"+strconv.Itoa(w.Port)+":8080")
	line("Volume", w.SiteDir+":/app:Z")
	if w.IniFile != "" && w.Runtime != "custom" {
		line("Volume", w.IniFile+":/usr/local/etc/php/conf.d/zz-cloudfactory.ini:ro,Z")
	}
	line("Label", "io.cloudfactory.website="+w.ID)
	line("Environment", "SERVER_NAME=:8080")
	if w.Runtime == "frankenphp" && w.WorkerMode {
		line("Environment", "FRANKENPHP_CONFIG=worker /app/public/index.php")
	}
	// Spec section 6.4: no new privileges, and only the capabilities that the container needs.
	line("NoNewPrivileges", "true")
	line("DropCapability", "all")
	if w.Runtime == "apache-php-fpm" {
		line("AddCapability", "CHOWN SETUID SETGID")
	}
	line("ReadOnly", "true")
	line("Tmpfs", "/tmp")
	line("UserNS", "keep-id")
	if w.Limits.Processes > 0 {
		line("PidsLimit", strconv.FormatInt(w.Limits.Processes, 10))
	}
	if args := podmanLimitArgs(w.Limits); args != "" {
		line("PodmanArgs", args)
	}

	b.WriteString("\n[Service]\n")
	line("Restart", "always")
	line("TimeoutStartSec", "300")

	b.WriteString("\n[Install]\n")
	line("WantedBy", "default.target")
	return b.String()
}

func podmanLimitArgs(l Limits) string {
	var args []string
	if l.VCPUs > 0 {
		args = append(args, "--cpus="+strconv.FormatFloat(l.VCPUs, 'f', -1, 64))
	}
	if l.MemoryBytes > 0 {
		m := strconv.FormatInt(l.MemoryBytes, 10)
		// The same value for swap means that the container cannot use swap.
		args = append(args, "--memory="+m, "--memory-swap="+m)
	}
	if l.DiskIOBytesPerSecond > 0 && l.BlockDevice != "" {
		v := l.BlockDevice + ":" + strconv.FormatInt(l.DiskIOBytesPerSecond, 10)
		args = append(args, "--device-read-bps="+v, "--device-write-bps="+v)
	}
	return strings.Join(args, " ")
}

// PHPIni gives the php.ini file for a website: the extensions and the values of the plan.
// The caller must check the keys and values with the validate package first.
// From PHP 8.5, OPcache is always part of PHP, so the file does not load it.
func PHPIni(phpVersion string, extensions []string, values map[string]string) string {
	var b strings.Builder
	b.WriteString("; Managed by the Cloudfactory agent. Change these values in the panel.\n")
	builtinOpcache := phpVersion >= "8.5"
	for _, ext := range slices.Sorted(slices.Values(extensions)) {
		if ext == "opcache" {
			if !builtinOpcache {
				b.WriteString("zend_extension=opcache\n")
			}
			continue
		}
		fmt.Fprintf(&b, "extension=%s\n", ext)
	}
	for _, k := range slices.Sorted(maps.Keys(values)) {
		fmt.Fprintf(&b, "%s = \"%s\"\n", k, values[k])
	}
	return b.String()
}

// Edge is the input for the unit of the edge proxy.
type Edge struct {
	Image    string
	SitesDir string
	DataDir  string
	Gvisor   Runtime
	// HTTPPort and HTTPSPort are the high ports that the container publishes. The host
	// firewall redirects 80 and 443 to them, because rootless containers cannot open
	// ports below 1024 (spec section 15, question 7).
	HTTPPort, HTTPSPort, AdminPort int
}

// EdgeUnit gives the container unit of the edge proxy (Caddy).
func EdgeUnit(e Edge) string {
	return Header + strings.Join([]string{
		"[Unit]",
		"Description=Cloudfactory edge proxy",
		"",
		"[Container]",
		"ContainerName=cf-edge",
		"Image=" + e.Image,
		"Pull=never",
		"GlobalArgs=" + e.Gvisor.GlobalArgs(),
		// pasta maps the gateway address to the host, so that Caddy can reach the website
		// ports on 127.0.0.1. Caddy sees the real client address (FR-1707).
		"Network=pasta:--map-gw",
		fmt.Sprintf("PublishPort=%d:8080", e.HTTPPort),
		fmt.Sprintf("PublishPort=%d:8443", e.HTTPSPort),
		fmt.Sprintf("PublishPort=%d:8443/udp", e.HTTPSPort),
		fmt.Sprintf("PublishPort=127.0.0.1:%d:2019", e.AdminPort),
		// Read-only access to the files of each website (spec section 6.4).
		"Volume=" + e.SitesDir + ":/srv/sites:ro,z",
		"Volume=" + e.DataDir + "/caddy:/data:Z",
		// The user cf-edge is in the group of each website user. Keep these groups in the container.
		"GroupAdd=keep-groups",
		"Environment=CADDY_ADMIN=0.0.0.0:2019",
		"NoNewPrivileges=true",
		"DropCapability=all",
		"ReadOnly=true",
		"Tmpfs=/tmp",
		"",
		"[Service]",
		"Restart=always",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}, "\n")
}

// Panel is the input for the units of the panel (FR-1808).
type Panel struct {
	Registry string
	Version  string
	Domain   string
	DataDir  string
	Gvisor   Runtime
	// APIPort and WebPort are the host ports on 127.0.0.1 for the edge proxy.
	APIPort, WebPort int
}

// PanelUnits gives the units of the panel: the API, the queue worker and the web UI.
// The API container publishes its port only on 127.0.0.1. Thus only the edge proxy and the
// agent on the panel server can connect to it.
// The API keeps its data in SQLite in DataDir, for an installation on one server.
func PanelUnits(p Panel) map[string]string {
	api := p.Registry + "/panel-api:" + p.Version
	common := []string{
		"Pull=never",
		"GlobalArgs=" + p.Gvisor.GlobalArgs(),
		// pasta maps the gateway address to the host, so that the API can reach the agent port.
		"Network=pasta:--map-gw",
		"NoNewPrivileges=true",
		"DropCapability=all",
	}
	unit := func(desc string, lines ...string) string {
		body := append([]string{"[Unit]", "Description=" + desc, "", "[Container]"}, lines...)
		body = append(body, common...)
		body = append(body, "", "[Service]", "Restart=always", "", "[Install]", "WantedBy=default.target", "")
		return Header + strings.Join(body, "\n")
	}
	env := []string{
		"Environment=APP_ENV=production",
		"Environment=DB_CONNECTION=sqlite",
		"Environment=DB_DATABASE=/data/panel.sqlite",
		"Environment=QUEUE_CONNECTION=database",
		"Environment=CF_PANEL_DOMAIN=" + p.Domain,
		"Environment=CF_REGISTRY=" + p.Registry,
		"EnvironmentFile=" + p.DataDir + "/panel.env",
		"Volume=" + p.DataDir + ":/data:Z",
	}
	return map[string]string{
		"cf-panel-api.container": unit("Cloudfactory panel API",
			append([]string{"ContainerName=cf-panel-api", "Image=" + api, fmt.Sprintf("PublishPort=127.0.0.1:%d:8080", p.APIPort)}, env...)...),
		"cf-panel-queue.container": unit("Cloudfactory panel queue worker",
			append([]string{"ContainerName=cf-panel-queue", "Image=" + api, "Exec=php artisan queue:work --tries=5"}, env...)...),
		"cf-panel-web.container": unit("Cloudfactory panel web UI",
			"ContainerName=cf-panel-web", "Image="+p.Registry+"/panel-web:"+p.Version, fmt.Sprintf("PublishPort=127.0.0.1:%d:8080", p.WebPort), "ReadOnly=true", "Tmpfs=/tmp"),
	}
}
