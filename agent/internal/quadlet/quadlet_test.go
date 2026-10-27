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
		"Network=cf-acme_x7k2.network",
		"PublishPort=127.0.0.1:20001:8080",
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
	units := PanelUnits(Panel{Registry: "ghcr.io/cloudfactory", Version: "0.1.0", Domain: "panel.example.com", DataDir: "/var/lib/cloudfactory/panel", Gvisor: Runtime{"/usr/local/bin/runsc", "systrap"}, APIPort: 19080, WebPort: 19081})
	if len(units) != 4 {
		t.Fatalf("units = %d", len(units))
	}
	if !strings.Contains(units["cf-panel-queue.container"], "Exec=php artisan queue:work") || !strings.Contains(units["cf-panel-api.container"], "--runtime=/usr/local/bin/runsc") ||
		!strings.Contains(units["cf-panel-scheduler.container"], "Exec=php artisan schedule:work") ||
		!strings.Contains(units["cf-panel-scheduler.container"], "Volume=/etc/cloudfactory/cosign.pub:/run/cloudfactory/cosign.pub:ro") {
		t.Error(units)
	}
}

// Audit batch 2, finding 3: the edge and the panel reach the ports on the host loopback address
// through an address that pasta maps.
func TestEdgeAndPanelMapTheHostLoopback(t *testing.T) {
	units := PanelUnits(Panel{Registry: "r", Version: "1"})
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
