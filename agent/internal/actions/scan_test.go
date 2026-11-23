package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

const scanImage = "ghcr.io/itbm/scan:0.2.0"

// clamscanOutput is the output of clamscan 1.4.4 for a website with the EICAR test file.
const clamscanOutput = `/scan/public/wp-content/uploads/eicar.php: Eicar-Test-Signature FOUND

----------- SCAN SUMMARY -----------
Known viruses: 3627519
Engine version: 1.4.4
Scanned directories: 7
Scanned files: 12
Infected files: 1
Data scanned: 0.04 MB
Data read: 0.02 MB (ratio 2.00:1)
Time: 11.402 sec (0 m 11 s)
Start Date: 2026:11:22 20:14:03
End Date:   2026:11:22 20:14:15
`

// recordImages is a shared image store that records the images of the services.
type recordImages struct{ services []string }

func (r *recordImages) Init(context.Context) error           { return nil }
func (r *recordImages) Ensure(context.Context, string) error { return nil }
func (r *recordImages) EnsureService(_ context.Context, ref string) error {
	r.services = append(r.services, ref)
	return nil
}
func (r *recordImages) Prune([]string) error { return nil }

// scanDeps gives a worker with a website and the malware scanner. The update of the signatures
// makes the signature files.
func scanDeps(t *testing.T) (*Deps, *execx.Fake, *recordImages) {
	t.Helper()
	d, f, _ := deps(t)
	store := &recordImages{}
	home := filepath.Join(t.TempDir(), "cf-scan")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	both(d, func(d *Deps) {
		d.ScanImage = scanImage
		d.ScanHome = home
	})
	rootOf(d).Images = store
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")})); err != nil {
		t.Fatal(err)
	}
	f.Calls = nil
	return d, f, store
}

// freshclam is the rule of the fake runner for the update of the signatures.
func freshclam(d *Deps) execx.Rule {
	return execx.Rule{Prefix: "systemctl --user --machine=cf-scan@ start cf-scan-update.service", Do: func() error {
		for _, name := range []string{"main.cvd", "daily.cld", "bytecode.cvd"} {
			if err := os.WriteFile(filepath.Join(d.scanSignatureDir(), name), []byte("signatures"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}}
}

// scanCommand is the start of the scan command of the website of the tests.
const scanCommand = "runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 systemd-run --user "

func scanRequest() *agentv1.ScanWebsiteRequest {
	return &agentv1.ScanWebsiteRequest{JobId: jobID2, WebsiteId: websiteID, UnixUser: "acme_x7k2"}
}

// A scan updates the signatures as cf-scan, then runs clamscan as the website user in a
// container with no network, with the website and the signatures read-only. Malware gives
// the exit code 1, which is not an error of the job.
func TestScanWebsiteFindsMalwareWithTheRightsOfTheWebsite(t *testing.T) {
	d, f, store := scanDeps(t)
	f.Rules = append(f.Rules, freshclam(d), execx.Rule{Prefix: scanCommand, Output: clamscanOutput, Err: errors.New("exit status 1")})
	job, err := d.ScanWebsite(scanRequest())
	if err != nil {
		t.Fatal(err)
	}
	if job.Key != scanJobKey || !job.Long {
		t.Errorf("the scans of a server do not run one at a time: key %q, long %v", job.Key, job.Long)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	result := job.Result()
	findings := result["findings"].([]any)
	if result["scannedFiles"] != 12 || result["infectedFiles"] != 1 || result["truncated"] != false || len(findings) != 1 {
		t.Fatalf("result = %v", result)
	}
	if got := findings[0].(map[string]any); got["path"] != "/public/wp-content/uploads/eicar.php" || got["signature"] != "Eicar-Test-Signature" {
		t.Errorf("finding = %v", got)
	}
	if result["engine"] != "1.4.4" || result["signatures"] != 3627519 {
		t.Errorf("result = %v", result)
	}
	// The website users start the image from the shared store, with no pull.
	if !slices.Equal(store.services, []string{scanImage}) {
		t.Errorf("shared images = %v", store.services)
	}
	root := rootOf(d)
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{root.scanHome(), 0o711}, {root.scanSignatureDir(), 0o755}} {
		if fi, err := os.Stat(dir.path); err != nil || fi.Mode().Perm() != dir.mode {
			t.Errorf("%s: %v %v, want %v", dir.path, fi.Mode(), err, dir.mode)
		}
	}
	unitDir, _ := root.Host.UnitDir(host.ScanUser)
	unit, _ := os.ReadFile(filepath.Join(unitDir, scanUnit))
	for _, want := range []string{"Image=" + scanImage, "Exec=freshclam", "Volume=" + root.scanSignatureDir() + ":/var/lib/clamav:z", "ReadOnly=true", "Type=oneshot"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("the unit does not have %q:\n%s", want, unit)
		}
	}
	if strings.Contains(string(unit), "[Install]") {
		t.Errorf("the update starts at boot:\n%s", unit)
	}
	var scan string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, scanCommand) {
			scan = c
		}
	}
	site := d.Host.AppDir("acme_x7k2")
	for _, want := range []string{"MemoryMax=2147483648", "RuntimeMaxSec=3600", "--network=none", "--read-only", "--cap-drop=all", "--userns=keep-id",
		"--volume=" + site + ":/scan:ro,Z", "--volume=" + root.scanSignatureDir() + ":/var/lib/clamav:ro ", scanImage + " clamscan --recursive --infected", "/usr/local/bin/runsc"} {
		if !strings.Contains(scan, want) {
			t.Errorf("the scan does not have %q: %s", want, scan)
		}
	}
	if !f.Called("systemctl --user --machine=cf-scan@ daemon-reload") {
		t.Errorf("calls = %v", f.Calls)
	}

	// A second scan in the same hour uses the same signatures.
	f.Calls = nil
	if err := run(t)(d.ScanWebsite(scanRequest())); err != nil {
		t.Fatal(err)
	}
	if f.Called("systemctl --user --machine=cf-scan@ start cf-scan-update.service") || f.Called("systemctl --user --machine=cf-scan@ daemon-reload") {
		t.Errorf("the second scan updated the signatures again: %v", f.Calls)
	}
}

// When the server cannot get new signatures, the scan uses the signatures that it has. With no
// signatures, the scan fails.
func TestScanWebsiteNeedsSignatures(t *testing.T) {
	d, f, _ := scanDeps(t)
	f.Rules = append(f.Rules, execx.Rule{Prefix: "systemctl --user --machine=cf-scan@ start cf-scan-update.service", Err: errors.New("exit status 1")})
	err := run(t)(d.ScanWebsite(scanRequest()))
	if err == nil || !strings.Contains(err.Error(), "database.clamav.net") {
		t.Fatalf("err = %v", err)
	}
	if f.Called(scanCommand) {
		t.Error("the scan ran with no signatures")
	}
	root := rootOf(d)
	for _, name := range []string{"main.cvd", "daily.cvd"} {
		if err := os.WriteFile(filepath.Join(root.scanSignatureDir(), name), []byte("signatures"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.Rules = append(f.Rules, execx.Rule{Prefix: scanCommand, Output: strings.Replace(clamscanOutput, "Infected files: 1", "Infected files: 0", 1)})
	if err := run(t)(d.ScanWebsite(scanRequest())); err != nil {
		t.Fatal(err)
	}
	if !f.Called(scanCommand) {
		t.Errorf("calls = %v", f.Calls)
	}
}

// A scan that stops before its summary, for example at the memory limit, fails.
func TestScanWebsiteFailsWithNoSummary(t *testing.T) {
	d, f, _ := scanDeps(t)
	f.Rules = append(f.Rules, freshclam(d), execx.Rule{Prefix: scanCommand, Output: "LibClamAV Error: cli_malloc(): Can't allocate memory\n", Err: errors.New("signal: killed")})
	err := run(t)(d.ScanWebsite(scanRequest()))
	if err == nil || !strings.Contains(err.Error(), "the scan failed") || strings.Contains(err.Error(), "systemd-run") {
		t.Fatalf("err = %v", err)
	}
}

func TestScanWebsiteRefusesTheWebsiteOfADifferentUser(t *testing.T) {
	d, _, _ := scanDeps(t)
	for _, req := range []*agentv1.ScanWebsiteRequest{
		{JobId: jobID2, WebsiteId: websiteID, UnixUser: "bolt_k3m9"},
		{JobId: jobID2, WebsiteId: websiteID, UnixUser: "root"},
		{JobId: jobID2, WebsiteId: "../x", UnixUser: "acme_x7k2"},
	} {
		if _, err := d.ScanWebsite(req); err == nil {
			t.Errorf("%v: no error", req)
		}
	}
	both(d, func(d *Deps) { d.ScanImage = "" })
	if _, err := d.ScanWebsite(scanRequest()); err == nil {
		t.Error("an agent with no scanner accepted the scan")
	}
}

// The root helper does not follow a link that cf-scan put in place of the signature directory.
func TestPrepareScanRefusesALinkAsTheSignatureDirectory(t *testing.T) {
	d, _, _ := scanDeps(t)
	root := rootOf(d)
	target := t.TempDir()
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root.scanSignatureDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.prepareScan(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o700 {
		t.Error("the root helper changed the mode of the target of the link")
	}
}

func TestParseClamscan(t *testing.T) {
	r, err := parseClamscan(clamscanOutput)
	if err != nil {
		t.Fatal(err)
	}
	if r.ScannedFiles != 12 || r.InfectedFiles != 1 || r.Errors != 0 || r.Truncated || len(r.Findings) != 1 || r.Findings[0].Path != "/public/wp-content/uploads/eicar.php" {
		t.Errorf("result = %+v", r)
	}
	// Errors: clamscan could not read a file.
	r, err = parseClamscan("/scan/private.txt: Access denied. ERROR\n\n" + scanSummary + "\nScanned files: 3\nInfected files: 0\nTotal errors: 1\n")
	if err != nil || r.Errors != 1 || len(r.Findings) != 0 {
		t.Errorf("result = %+v, %v", r, err)
	}
	// The name of a file shows a summary. The findings before the real summary stay.
	r, err = parseClamscan("/scan/a.php: Php.Webshell FOUND\n/scan/b.php\n" + scanSummary + "\nInfected files: 0: Php.Webshell FOUND\n/scan/c.php: Win.Trojan FOUND\n\n" + scanSummary + "\nScanned files: 9\nInfected files: 3\n")
	if err != nil || r.InfectedFiles != 3 || len(r.Findings) != 2 || r.Findings[1].Path != "/c.php" || !r.Truncated {
		t.Errorf("result = %+v, %v", r, err)
	}
	// Only paths in the website directory.
	r, _ = parseClamscan("/etc/passwd: Fake FOUND\n/scanner/x: Fake FOUND\n/scan/../x: Fake FOUND\n\n" + scanSummary + "\nInfected files: 1\n")
	if len(r.Findings) != 1 || r.Findings[0].Path != "/x" {
		t.Errorf("findings = %+v", r.Findings)
	}
	// Control characters are not in the result.
	r, _ = parseClamscan("/scan/a\x1b[2Jb.php: Php.Shell\x07 FOUND\n\n" + scanSummary + "\nInfected files: 1\n")
	if len(r.Findings) != 1 || strings.ContainsAny(r.Findings[0].Path+r.Findings[0].Signature, "\x1b\x07") {
		t.Errorf("findings = %+v", r.Findings)
	}
	// A large number of findings.
	var many strings.Builder
	for range maxScanFindings + 5 {
		many.WriteString("/scan/x.php: Php.Shell FOUND\n")
	}
	r, _ = parseClamscan(many.String() + "\n" + scanSummary + "\nInfected files: 505\n")
	if len(r.Findings) != maxScanFindings || !r.Truncated {
		t.Errorf("findings %d, truncated %v", len(r.Findings), r.Truncated)
	}
	for _, out := range []string{"", "LibClamAV Error: cli_loaddbdir(): No supported database files found\n", "x" + scanSummary + "\n"} {
		if _, err := parseClamscan(out); err == nil {
			t.Errorf("%q: no error", out)
		}
	}
}

func TestTheScanUnitHasNoInstallSection(t *testing.T) {
	unit := quadlet.ScanUnit(quadlet.Scan{Image: scanImage, SignatureDir: "/var/lib/cf-scan/signatures"})
	if strings.Contains(unit, "[Install]") || !strings.Contains(unit, "Network="+quadlet.PastaNetwork) {
		t.Errorf("unit:\n%s", unit)
	}
}
