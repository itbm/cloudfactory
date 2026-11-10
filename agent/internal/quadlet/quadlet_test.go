package quadlet

import (
	"strings"
	"testing"
)

func website() Website {
	return Website{
		ID: "01JBQ3Z5K8N6T4W2X9Y7R5P3M1", Name: "acme.example", UnixUser: "acme_x7k2",
		Image: "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1@sha256:abc", Port: 20001,
		SiteDir: "/srv/cloudfactory/sites/acme_x7k2/app", IniFile: "/srv/cloudfactory/sites/acme_x7k2/php.ini",
		Runtime: "frankenphp", Limits: Limits{VCPUs: 1.5, MemoryBytes: 1 << 30, Processes: 100},
		Gvisor: Runtime{RunscPath: "/usr/local/bin/runsc", Platform: "systrap"},
	}
}

func TestContainerUnitUsesGvisorAndNoHostNetwork(t *testing.T) {
	u := ContainerUnit(website())
	for _, want := range []string{
		"GlobalArgs=--runtime=/usr/local/bin/runsc --runtime-flag=platform=systrap --runtime-flag=network=sandbox",
		"Network=pasta",
		"PublishPort=169.254.77.1:20001:8080",
		"AddHost=mariadb.internal:169.254.77.1",
		"NoNewPrivileges=true",
		"DropCapability=all",
		"PidsLimit=100",
		"PodmanArgs=--cpus=1.5 --memory=1073741824 --memory-swap=1073741824",
		"Pull=never",
	} {
		if !strings.Contains(u, want+"\n") {
			t.Errorf("unit does not contain %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "Network=host") || strings.Contains(u, "AddCapability") || strings.Contains(u, "FRANKENPHP_CONFIG") {
		t.Errorf("unexpected setting:\n%s", u)
	}
}

func TestWorkerModeAndApache(t *testing.T) {
	w := website()
	w.WorkerMode = true
	if !strings.Contains(ContainerUnit(w), "Environment=\"FRANKENPHP_CONFIG=worker /app/public/index.php\"\n") {
		t.Error("worker mode is not on")
	}
	w.Runtime = "apache-php-fpm"
	u := ContainerUnit(w)
	// Audit batch 2: Apache and PHP-FPM run as the website user and need no capabilities.
	if strings.Contains(u, "FRANKENPHP_CONFIG") || strings.Contains(u, "AddCapability") {
		t.Errorf("apache unit:\n%s", u)
	}
}

func TestDiskLimitNeedsADevice(t *testing.T) {
	w := website()
	w.Limits.DiskIOBytesPerSecond = 1000
	if strings.Contains(ContainerUnit(w), "device-write-bps") {
		t.Error("I/O limit with no device")
	}
	w.Limits.BlockDevice = "/dev/vda"
	if !strings.Contains(ContainerUnit(w), "--device-write-bps=/dev/vda:1000") {
		t.Error("no I/O limit")
	}
}

func TestPHPIni(t *testing.T) {
	if strings.Contains(PHPIni("8.5", []string{"opcache"}, nil), "opcache") {
		t.Error("PHP 8.5 loads OPcache itself")
	}
	got := PHPIni("8.4", []string{"redis", "opcache"}, map[string]string{"memory_limit": "256M", "date.timezone": "Europe/London"})
	want := "; Managed by the Cloudfactory agent. Change these values in the panel.\n" +
		"zend_extension=opcache\nextension=redis\n" +
		"date.timezone = \"Europe/London\"\nmemory_limit = \"256M\"\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// CF-04 and CF-13: the edge proxy cannot read the files of the websites, and its admin API is a
// Unix socket, not a port.
func TestEdgeUnitHasNoSitesAndNoAdminPort(t *testing.T) {
	u := EdgeUnit(Edge{Image: "ghcr.io/cloudfactory/edge:2.10", DataDir: "/var/lib/cloudfactory", Gvisor: Runtime{"/usr/local/bin/runsc", "kvm"}, HTTPPort: 8080, HTTPSPort: 8443})
	for _, want := range []string{"Volume=/var/lib/cloudfactory/caddy-admin:/run/caddy-admin:Z", "--runtime-flag=host-uds=create", "--runtime-flag=platform=kvm"} {
		if !strings.Contains(u, want) {
			t.Errorf("edge unit does not contain %q", want)
		}
	}
	for _, bad := range []string{"/srv/sites", "keep-groups", "2019", "CADDY_ADMIN"} {
		if strings.Contains(u, bad) {
			t.Errorf("edge unit contains %q:\n%s", bad, u)
		}
	}
}

func TestPanelUnits(t *testing.T) {
	units := PanelUnits(Panel{Registry: "ghcr.io/cloudfactory", Version: "0.1.0", Domain: "panel.example.com", DataDir: "/var/lib/cloudfactory/panel", Gvisor: Runtime{"/usr/local/bin/runsc", "systrap"},
		APIPort: PanelAPIPort, WebPort: PanelWebPort, AgentPort: PanelAgentPort, DatabasePort: PanelDatabasePort})
	if len(units) != 5 {
		t.Fatalf("units = %d", len(units))
	}
	if !strings.Contains(units["cf-panel-queue.container"], "Exec=php artisan queue:work") || !strings.Contains(units["cf-panel-api.container"], "--runtime=/usr/local/bin/runsc") ||
		!strings.Contains(units["cf-panel-scheduler.container"], "Exec=php artisan schedule:work") ||
		!strings.Contains(units["cf-panel-scheduler.container"], "Volume=/etc/cloudfactory/cosign.pub:/run/cloudfactory/cosign.pub:ro") {
		t.Error(units)
	}
	// The edge proxy uses the HTTP port of the API. The agent on the panel server uses the
	// HTTPS port. The two ports are only on the service address.
	for _, want := range []string{"PublishPort=169.254.77.1:19080:8080\n", "PublishPort=169.254.77.1:19443:8443\n"} {
		if !strings.Contains(units["cf-panel-api.container"], want) {
			t.Errorf("the API unit does not contain %q:\n%s", want, units["cf-panel-api.container"])
		}
	}
	for _, name := range []string{"cf-panel-queue.container", "cf-panel-scheduler.container"} {
		if strings.Contains(units[name], "PublishPort") {
			t.Errorf("%s publishes a port", name)
		}
	}
}

// Audit findings B4 and R4: gVisor keeps a file lock in one sandbox, so three containers cannot
// share one SQLite file. The panel uses PostgreSQL in its own container.
func TestThePanelUsesPostgreSQL(t *testing.T) {
	units := PanelUnits(Panel{Registry: "ghcr.io/cloudfactory", Version: "0.1.0", Domain: "panel.example.com", DataDir: "/var/lib/cloudfactory/panel", Gvisor: Runtime{"/usr/local/bin/runsc", "systrap"},
		APIPort: PanelAPIPort, WebPort: PanelWebPort, AgentPort: PanelAgentPort, DatabasePort: PanelDatabasePort})
	db := units["cf-panel-db.container"]
	for _, want := range []string{
		"ContainerName=cf-panel-db\n",
		"Image=ghcr.io/cloudfactory/postgresql:0.1.0\n",
		"Pull=never\n",
		"--runtime=/usr/local/bin/runsc",
		// Only on the service address, and not on the port of the databases of the websites.
		"PublishPort=169.254.77.1:19432:5432\n",
		"Volume=/var/lib/cloudfactory/panel/db:/var/lib/postgresql/data:Z\n",
		"EnvironmentFile=/var/lib/cloudfactory/panel/db.env\n",
		"--auth-host=scram-sha-256",
		"DropCapability=all\n",
		"NoNewPrivileges=true\n",
		"Restart=always\n",
	} {
		if !strings.Contains(db, want) {
			t.Errorf("the database unit does not contain %q:\n%s", want, db)
		}
	}
	for _, name := range []string{"cf-panel-api.container", "cf-panel-queue.container", "cf-panel-scheduler.container"} {
		u := units[name]
		for _, want := range []string{
			"Environment=DB_CONNECTION=pgsql\n",
			"Environment=DB_HOST=169.254.77.1\n",
			"Environment=DB_PORT=19432\n",
			"Environment=DB_DATABASE=cloudfactory\n",
			"Environment=DB_USERNAME=cloudfactory\n",
			"EnvironmentFile=/var/lib/cloudfactory/panel/db.env\n",
			"After=cf-panel-db.service",
			"Wants=cf-panel-db.service",
			"RestartSec=5\n",
		} {
			if !strings.Contains(u, want) {
				t.Errorf("%s does not contain %q:\n%s", name, want, u)
			}
		}
		if strings.Contains(u, "sqlite") {
			t.Errorf("%s uses SQLite:\n%s", name, u)
		}
	}
	// The API makes the tables. The queue worker and the scheduler start after it.
	for _, name := range []string{"cf-panel-queue.container", "cf-panel-scheduler.container"} {
		if !strings.Contains(units[name], "After=cf-panel-db.service cf-panel-api.service\n") {
			t.Errorf("%s does not start after the API:\n%s", name, units[name])
		}
	}
	if strings.Contains(units["cf-panel-web.container"], "DB_") || strings.Contains(units["cf-panel-web.container"], "db.env") {
		t.Errorf("the web UI has the database settings:\n%s", units["cf-panel-web.container"])
	}
}

// Audit batch 2, finding 3: the edge and the panel reach the ports on the host loopback address
// through an address that pasta maps.
func TestEdgeAndPanelMapTheHostLoopback(t *testing.T) {
	units := PanelUnits(Panel{Registry: "r", Version: "1"})
	// The database of the panel does not connect to the host.
	if db := units[PanelDatabaseContainer+".container"]; !strings.Contains(db, "Network=pasta\n") {
		t.Errorf("database:\n%s", db)
	}
	delete(units, PanelDatabaseContainer+".container")
	units["edge"] = EdgeUnit(Edge{Image: "e", HTTPPort: 8080, HTTPSPort: 8443})
	for name, u := range units {
		if !strings.Contains(u, "Network=pasta:--map-host-loopback=169.254.1.3\n") || strings.Contains(u, "--map-gw") {
			t.Errorf("%s:\n%s", name, u)
		}
	}
}

func TestPHPVersionsCompareAsNumbers(t *testing.T) {
	for v, want := range map[string]bool{"8.4": false, "8.5": true, "8.10": true, "9.0": true, "10.1": true, "7.4": false, "x": false} {
		if got := phpAtLeast(v, 8, 5); got != want {
			t.Errorf("phpAtLeast(%q) = %v", v, got)
		}
	}
}

// Spec section 10: one SFTP container in gVisor serves all websites. Root runs it, so that each
// session can change to the Unix user of its website, with only the capabilities that sshd needs.
func TestSftpUnit(t *testing.T) {
	u := SftpUnit(Sftp{
		Image: "ghcr.io/cloudfactory/sftp:0.2.0", Port: 2222, SitesDir: "/srv/cloudfactory/sites",
		KeysDir: "/etc/cloudfactory/sftp", UsersDir: "/etc/cloudfactory/sftp-users", HostKeysDir: "/var/lib/cloudfactory/sftp-host-keys",
		Gvisor: Runtime{RunscPath: "/usr/local/bin/runsc", Platform: "systrap"},
	})
	for _, want := range []string{
		"ContainerName=cf-sftp",
		"Pull=never",
		"GlobalArgs=--runtime=/usr/local/bin/runsc --runtime-flag=platform=systrap --runtime-flag=network=sandbox",
		"Network=pasta",
		"PublishPort=2222:2200",
		"Volume=/srv/cloudfactory/sites:/srv/cloudfactory/sites",
		"Volume=/etc/cloudfactory/sftp:/etc/cloudfactory/sftp:ro",
		"Volume=/etc/cloudfactory/sftp-users:/var/lib/extrausers:ro",
		"Volume=/var/lib/cloudfactory/sftp-host-keys:/etc/ssh/keys",
		"NoNewPrivileges=true",
		"DropCapability=all",
		"AddCapability=SYS_CHROOT SETUID SETGID KILL",
		"ReadOnly=true",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want+"\n") {
			t.Errorf("unit does not contain %q:\n%s", want, u)
		}
	}
	// The home directories hold the Podman storage of each user. The container must not see them,
	// and a new SELinux label on the website files would stop the website containers.
	if strings.Contains(u, "homes") || strings.Contains(u, ":Z") || strings.Contains(u, "Network=host") || strings.Contains(u, "DAC_OVERRIDE") {
		t.Errorf("unexpected setting:\n%s", u)
	}
	if !strings.Contains(SftpUnit(Sftp{Image: "x"}), "PublishPort=2200:2200\n") {
		t.Error("the default SFTP port is not 2200")
	}
}
