// Package quadlet writes the Podman Quadlet units for websites and the edge proxy.
// systemd starts each container. Thus the websites continue to operate when the agent
// or the panel stops (NFR-04).
package quadlet

import (
	"fmt"
	"maps"
	"path"
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
	// DocumentRoot is the directory that the web server serves, for example "/app/public".
	DocumentRoot string
	// ContainerPort is the HTTP port in the container. 0 means 8080. Only a custom image sets it.
	ContainerPort int
	// Command replaces the start command of a custom image. Empty means the command of the image.
	Command []string
	// Stopped is true when the owner stopped the website. Then systemd does not start it at boot.
	Stopped bool
}

// Database host names in each website container. They point to the host, where the shared
// database containers publish their ports (DatabasePort).
const (
	MariaDBHost    = "mariadb.internal"
	PostgreSQLHost = "postgres.internal"
)

// ContainerName is the name of the Podman container.
func (w Website) ContainerName() string { return "cf-" + w.UnixUser + w.Suffix }

// UnitName is the file name of the container unit.
func (w Website) UnitName() string { return w.ContainerName() + ".container" }

// ServiceName is the systemd service that Quadlet makes from the unit.
func (w Website) ServiceName() string { return w.ContainerName() + ".service" }

// HostLoopback is the address in the edge and panel containers that pasta maps to 127.0.0.1
// on the host (audit batch 2, finding 3). host.containers.internal is the address of the
// host interface, so it cannot reach the ports that the website containers publish on
// 127.0.0.1. The address is link-local, so it is not on a real network.
const HostLoopback = "169.254.1.3"

// PastaNetwork is the Network value of the edge and panel containers. The option
// --map-host-loopback is in passt 2024_08_21 and later (Debian 13 and Ubuntu 26.04 have it).
const PastaNetwork = "pasta:--map-host-loopback=" + HostLoopback

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
	containerPort := 8080
	if w.Runtime == "custom" && w.ContainerPort > 0 {
		containerPort = w.ContainerPort
	}
	line("PublishPort", "127.0.0.1:"+strconv.Itoa(w.Port)+":"+strconv.Itoa(containerPort))
	// "host-gateway" is the address of the host. The database containers publish their
	// ports there. The host firewall does not let other computers connect to these ports.
	line("AddHost", MariaDBHost+":host-gateway")
	line("AddHost", PostgreSQLHost+":host-gateway")
	line("Volume", w.SiteDir+":/app:Z")
	if w.IniFile != "" && w.Runtime != "custom" {
		line("Volume", w.IniFile+":/usr/local/etc/php/conf.d/zz-cloudfactory.ini:ro,Z")
	}
	line("Label", "io.cloudfactory.website="+w.ID)
	line("Environment", "SERVER_NAME=:8080")
	root := w.DocumentRoot
	if root == "" {
		root = "/app/public"
	}
	if w.Runtime != "custom" {
		line("Environment", "CF_DOCUMENT_ROOT="+root)
	}
	if w.Runtime == "frankenphp" && w.WorkerMode {
		// The value has a space, so it is in quotes. systemd splits Environment at spaces.
		line("Environment", `"FRANKENPHP_CONFIG=worker `+path.Join(root, "index.php")+`"`)
	}
	// Spec section 6.4: no new privileges, and only the capabilities that the container needs.
	line("NoNewPrivileges", "true")
	line("DropCapability", "all")
	line("ReadOnly", "true")
	line("Tmpfs", "/tmp")
	line("UserNS", "keep-id")
	if w.Limits.Processes > 0 {
		line("PidsLimit", strconv.FormatInt(w.Limits.Processes, 10))
	}
	if args := podmanLimitArgs(w.Limits); args != "" {
		line("PodmanArgs", args)
	}

	if w.Runtime == "custom" && len(w.Command) > 0 {
		line("Exec", execLine(w.Command))
	}

	b.WriteString("\n[Service]\n")
	line("Restart", "always")
	line("TimeoutStartSec", "300")

	if !w.Stopped {
		b.WriteString("\n[Install]\n")
		line("WantedBy", "default.target")
	}
	return b.String()
}

// execLine quotes each argument for the Exec line of a unit. systemd splits the line at
// spaces and reads C-style escapes in double quotes. "%" and "$" are also special in units.
func execLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		a = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(a)
		q[i] = `"` + a + `"`
	}
	return strings.Join(q, " ")
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
	builtinOpcache := phpAtLeast(phpVersion, 8, 5)
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
	Image   string
	DataDir string
	Gvisor  Runtime
	// HTTPPort and HTTPSPort are the high ports that the container publishes. The host
	// firewall redirects 80 and 443 to them, because rootless containers cannot open
	// ports below 1024 (spec section 15, question 7).
	HTTPPort, HTTPSPort int
}

// EdgeAdminDir is the host directory of the admin socket of the edge proxy, in DataDir.
const EdgeAdminDir = "caddy-admin"

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
		// host-uds=create lets Caddy make its admin socket in the mounted directory (CF-13).
		"GlobalArgs=" + e.Gvisor.GlobalArgs() + " --runtime-flag=host-uds=create",
		// pasta maps HostLoopback to the host loopback address, so that Caddy can reach the
		// website ports on 127.0.0.1. Caddy sees the real client address (FR-1707).
		"Network=" + PastaNetwork,
		fmt.Sprintf("PublishPort=%d:8080", e.HTTPPort),
		fmt.Sprintf("PublishPort=%d:8443", e.HTTPSPort),
		fmt.Sprintf("PublishPort=%d:8443/udp", e.HTTPSPort),
		// CF-04: the edge proxy has no access to the files of the websites. Each website
		// container serves its own static files.
		"Volume=" + e.DataDir + "/caddy:/data:Z",
		// CF-13: the admin API is a Unix socket in this directory, not a TCP port.
		"Volume=" + e.DataDir + "/" + EdgeAdminDir + ":/run/caddy-admin:Z",
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

// Database is the input for the unit of a shared database container (FR-501). The user
// cf-db runs the containers. Each website connects with its own database user.
type Database struct {
	// Engine is "mariadb" or "postgresql".
	Engine string
	Image  string
	// DataDir holds the data files on the host.
	DataDir string
	// EnvFile holds the password of the superuser. Only cf-db can read it.
	EnvFile string
	Gvisor  Runtime
}

// DatabaseContainer is the name of the shared container of an engine.
func DatabaseContainer(engine string) string { return "cf-" + engine }

// DatabasePort is the host port of an engine. The website containers connect to it
// through the host names MariaDBHost and PostgreSQLHost.
func DatabasePort(engine string) int {
	if engine == "postgresql" {
		return 5432
	}
	return 3306
}

// DatabaseUnit gives the container unit of a shared database.
//
// The superuser can connect only through the socket in the container, with "podman exec".
// The agent uses it. Each connection on the published port needs a password: MariaDB has no
// root user for TCP, and PostgreSQL uses scram-sha-256 for all TCP connections.
func DatabaseUnit(d Database) string {
	name := DatabaseContainer(d.Engine)
	port := strconv.Itoa(DatabasePort(d.Engine))
	lines := []string{
		"[Unit]",
		"Description=Cloudfactory " + map[string]string{"mariadb": "MariaDB", "postgresql": "PostgreSQL"}[d.Engine] + " service",
		"",
		"[Container]",
		"ContainerName=" + name,
		"Image=" + d.Image,
		"Pull=never",
		"GlobalArgs=" + d.Gvisor.GlobalArgs(),
		"PublishPort=" + port + ":" + port,
		"Label=io.cloudfactory.service=" + d.Engine,
		"NoNewPrivileges=true",
		"DropCapability=all",
		// The entry point of the image sets the owner of the data files, and then changes to
		// the database user.
		"AddCapability=CHOWN DAC_OVERRIDE FOWNER SETUID SETGID",
		"ReadOnly=true",
		"Tmpfs=/tmp",
	}
	if d.Engine == "mariadb" {
		lines = append(lines,
			"Volume="+d.DataDir+":/var/lib/mysql:Z",
			"Tmpfs=/run/mysqld",
			// Audit batch 2, finding 4: the image does not let root use the socket with no
			// password. The env file gives the root password to the server at the first start,
			// and to the client (MYSQL_PWD) in each "podman exec". No root user can use TCP.
			"EnvironmentFile="+d.EnvFile,
			"Environment=MARIADB_ROOT_HOST=localhost",
			"Environment=MARIADB_AUTO_UPGRADE=1",
		)
	} else {
		lines = append(lines,
			"Volume="+d.DataDir+":/var/lib/postgresql/data:Z",
			"Environment=PGDATA=/var/lib/postgresql/data/pgdata",
			"Tmpfs=/var/run/postgresql",
			"ShmSize=256m",
			"EnvironmentFile="+d.EnvFile,
			`Environment="POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 --auth-local=trust"`,
		)
	}
	lines = append(lines,
		"",
		"[Service]",
		"Restart=always",
		"TimeoutStartSec=900",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	)
	return Header + strings.Join(lines, "\n")
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

// CosignPublicKey is the release public key on each server. The install script writes it.
const CosignPublicKey = "/etc/cloudfactory/cosign.pub"

// PanelUnits gives the units of the panel: the API, the queue worker, the scheduler and
// the web UI. The scheduler checks the registry for new image tags (FR-1110).
// The API container publishes its port only on 127.0.0.1. Thus only the edge proxy and the
// agent on the panel server can connect to it.
// The API keeps its data in SQLite in DataDir, for an installation on one server.
func PanelUnits(p Panel) map[string]string {
	api := p.Registry + "/panel-api:" + p.Version
	common := []string{
		"Pull=never",
		"GlobalArgs=" + p.Gvisor.GlobalArgs(),
		// pasta maps HostLoopback to the host, so that the API can reach the agent port.
		"Network=" + PastaNetwork,
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
		// The panel checks the signatures of new image tags with the release key.
		"Environment=CF_COSIGN_PUBLIC_KEY=/run/cloudfactory/cosign.pub",
		"Volume=" + CosignPublicKey + ":/run/cloudfactory/cosign.pub:ro",
	}
	return map[string]string{
		"cf-panel-api.container": unit("Cloudfactory panel API",
			append([]string{"ContainerName=cf-panel-api", "Image=" + api, fmt.Sprintf("PublishPort=127.0.0.1:%d:8080", p.APIPort)}, env...)...),
		"cf-panel-queue.container": unit("Cloudfactory panel queue worker",
			append([]string{"ContainerName=cf-panel-queue", "Image=" + api, "Exec=php artisan queue:work --tries=5"}, env...)...),
		"cf-panel-scheduler.container": unit("Cloudfactory panel scheduler",
			append([]string{"ContainerName=cf-panel-scheduler", "Image=" + api, "Exec=php artisan schedule:work"}, env...)...),
		"cf-panel-web.container": unit("Cloudfactory panel web UI",
			"ContainerName=cf-panel-web", "Image="+p.Registry+"/panel-web:"+p.Version, fmt.Sprintf("PublishPort=127.0.0.1:%d:8080", p.WebPort), "ReadOnly=true", "Tmpfs=/tmp"),
	}
}

// phpAtLeast compares a PHP version such as "8.10" as numbers, not as text (audit batch 3).
func phpAtLeast(version string, major, minor int) bool {
	a, b, _ := strings.Cut(version, ".")
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(strings.SplitN(b, ".", 2)[0])
	if err1 != nil || err2 != nil {
		return false
	}
	return x > major || (x == major && y >= minor)
}
