package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/sitelogs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The malware scan of a website (docs/architecture.md#malware-scan).
//
// The Unix user cf-scan updates the ClamAV signatures of the server with freshclam, in a
// container of the scanner image (quadlet.ScanUnit). The website users can read the
// signatures, but cannot change them. The scan runs clamscan as the Unix user of the website,
// in a gVisor container with no network, with the directory of the website and the signatures
// mounted read-only. Thus the scanner cannot change the website, and a file of the website
// that attacks ClamAV gets only the rights of its own website.

const (
	opScanPrepare = "scan.prepare"

	scanUnit    = "cf-scan-update.container"
	scanService = "cf-scan-update.service"

	// scanJobKey makes the scans of a server run one at a time: each scan keeps all
	// signatures in memory.
	scanJobKey = "malware-scan"
	// scanMemory is the memory limit of a scan. ClamAV 1.4 uses approximately 1 GB for the
	// signatures.
	scanMemory = 2 << 30
	// scanTimeLimit is the longest time of one scan.
	scanTimeLimit = time.Hour
	// scanMaxFileSize and scanMaxScanSize are the limits of clamscan for one file and for
	// the content of one archive. ClamAV does not scan the part of a file after the limit.
	scanMaxFileSize = "25M"
	scanMaxScanSize = "100M"
	// maxScanFindings is the largest number of files in the result of a scan.
	maxScanFindings = 500
	// signatureUpdateGap is the shortest time between two updates of the signatures.
	// freshclam must not ask the ClamAV mirrors more often.
	signatureUpdateGap = time.Hour
	// scanMount is the directory of the website in the scan container.
	scanMount = "/scan"
)

// signatureUpdates has the time of the last update of each signature directory.
var signatureUpdates = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

func (d *Deps) scanHome() string {
	if d.ScanHome != "" {
		return d.ScanHome
	}
	return "/var/lib/cf-scan"
}

// scanSignatureDir holds the signatures. cf-scan owns it, and all users can read it.
func (d *Deps) scanSignatureDir() string { return filepath.Join(d.scanHome(), "signatures") }

func (d *Deps) scanRootOps() map[string]gate.Handler {
	return map[string]gate.Handler{
		opScanPrepare: gate.Op(func(ctx context.Context, _ gate.None) (bool, error) {
			return d.prepareScan(ctx)
		}),
	}
}

// prepareScan makes the Unix user of the scanner, the directory of the signatures and the
// unit of the update. It puts the image of the scanner in the shared image store, so that each
// website user can start it with no pull. It gives true when the unit changed.
func (d *Deps) prepareScan(ctx context.Context) (changed bool, err error) {
	if d.Gate != nil {
		err = d.Gate.Call(ctx, opScanPrepare, gate.None{}, &changed)
		return changed, err
	}
	if d.ScanImage == "" {
		return false, errors.New("the root helper has no image of the malware scanner")
	}
	if d.Images == nil {
		return false, errors.New("this server has no shared image store")
	}
	home := d.scanHome()
	if err := d.Host.EnsureServiceUser(ctx, host.ScanUser, home); err != nil {
		return false, err
	}
	// The website users go through the home directory to the signatures. They cannot list
	// the home directory.
	if err := os.Chmod(home, 0o711); err != nil {
		return false, err
	}
	uid, gid, err := d.Host.FileOwner(host.ScanUser)
	if err != nil {
		return false, err
	}
	dir := d.scanSignatureDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	// cf-scan owns the home directory. Root does not follow a link that cf-scan made.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return false, err
	}
	if err := os.Lchown(dir, uid, gid); err != nil {
		return false, err
	}
	if err := d.Images.EnsureService(ctx, d.ScanImage); err != nil {
		return false, err
	}
	unit := quadlet.ScanUnit(quadlet.Scan{Image: d.ScanImage, Gvisor: d.runtime(), SignatureDir: d.scanSignatureDir()})
	return d.Host.WriteUnit(host.ScanUser, scanUnit, unit)
}

// haveSignatures reports whether the server has the main and the daily signatures.
func (d *Deps) haveSignatures() bool {
	for _, name := range []string{"main", "daily"} {
		found := false
		for _, ext := range []string{".cvd", ".cld"} {
			if info, err := os.Stat(filepath.Join(d.scanSignatureDir(), name+ext)); err == nil && info.Mode().IsRegular() {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// updateSignatures prepares the scanner and updates the signatures, at most one time in
// signatureUpdateGap. When the update fails and the server has signatures, the scan uses them.
func (d *Deps) updateSignatures(ctx context.Context) error {
	changed, err := d.prepareScan(ctx)
	if err != nil {
		return err
	}
	if _, err := d.Host.Podman(ctx, host.ScanUser, "image", "exists", d.ScanImage); err != nil {
		if _, err := d.Host.Podman(ctx, host.ScanUser, "pull", "--quiet", d.ScanImage); err != nil {
			return err
		}
	}
	if changed {
		if _, err := d.Host.Systemctl(ctx, host.ScanUser, "daemon-reload"); err != nil {
			return err
		}
	}
	dir := d.scanSignatureDir()
	signatureUpdates.Lock()
	last := signatureUpdates.at[dir]
	signatureUpdates.Unlock()
	if !last.IsZero() && time.Since(last) < signatureUpdateGap && d.haveSignatures() {
		return nil
	}
	// The unit is of the type oneshot: "start" ends when freshclam ends.
	_, err = d.Host.Systemctl(ctx, host.ScanUser, "start", scanService)
	if err == nil {
		signatureUpdates.Lock()
		signatureUpdates.at[dir] = time.Now()
		signatureUpdates.Unlock()
	}
	if err != nil && d.haveSignatures() {
		slog.Warn("the update of the malware signatures failed; the scan uses the signatures of the server", "err", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("the server cannot get the malware signatures from database.clamav.net: %w", err)
	}
	if !d.haveSignatures() {
		return errors.New("freshclam ended, but the server has no malware signatures")
	}
	return nil
}

// ScanFinding is a file in which ClamAV found malware.
type ScanFinding struct {
	// Path is the path in the website directory, as in the file manager: "/" is the
	// directory of the website.
	Path      string `json:"path"`
	Signature string `json:"signature"`
}

// ScanResult is the result of a scan.
type ScanResult struct {
	ScannedFiles  int           `json:"scannedFiles"`
	InfectedFiles int           `json:"infectedFiles"`
	Errors        int           `json:"errors"`
	Signatures    int           `json:"signatures"`
	Engine        string        `json:"engine"`
	Findings      []ScanFinding `json:"findings"`
	// Truncated is true when the scan found more files than the result has.
	Truncated bool `json:"truncated"`
}

// scanSummary is the line before the summary in the output of clamscan.
const scanSummary = "----------- SCAN SUMMARY -----------"

// parseClamscan reads the output of "clamscan --infected --stdout": a line for each infected
// file, then the summary. Output with no summary is an error.
//
// The name of a file can have a line break, so the name of an infected file can show a line
// of a summary. Thus only the last summary line starts the summary: no file name comes after
// it. When the summary has more infected files than the lines, the result is truncated.
func parseClamscan(out string) (ScanResult, error) {
	r := ScanResult{Findings: []ScanFinding{}}
	i := strings.LastIndex(out, scanSummary+"\n")
	if i < 0 || (i > 0 && out[i-1] != '\n') {
		return r, errors.New("clamscan gave no summary")
	}
	files, summary := out[:i], out[i+len(scanSummary)+1:]
	for line := range strings.SplitSeq(summary, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		n, _ := strconv.Atoi(value)
		switch key {
		case "Scanned files":
			r.ScannedFiles = n
		case "Infected files":
			r.InfectedFiles = n
		case "Total errors":
			r.Errors = n
		case "Known viruses":
			r.Signatures = n
		case "Engine version":
			r.Engine = sitelogs.Clean(value)
		}
	}
	for line := range strings.SplitSeq(files, "\n") {
		rest, ok := strings.CutSuffix(line, " FOUND")
		if !ok {
			continue
		}
		j := strings.LastIndex(rest, ": ")
		if j < 0 {
			continue
		}
		file, signature := rest[:j], rest[j+2:]
		if file != scanMount && !strings.HasPrefix(file, scanMount+"/") {
			continue
		}
		if len(r.Findings) >= maxScanFindings {
			break
		}
		r.Findings = append(r.Findings, ScanFinding{Path: sitelogs.Clean(path.Clean("/" + strings.TrimPrefix(file, scanMount))), Signature: sitelogs.Clean(signature)})
	}
	r.Truncated = r.InfectedFiles > len(r.Findings)
	return r, nil
}

// scanFiles runs clamscan as the Unix user of the website, in a gVisor container with no
// network. The directory of the website and the signatures are read-only.
func (d *Deps) scanFiles(ctx context.Context, w *agentv1.Website) (ScanResult, error) {
	user := w.GetUnixUser()
	uid, err := d.Host.LookupUID(user)
	if err != nil {
		return ScanResult{}, err
	}
	u := d.unit(w, 0, "")
	// systemd stops the unit, and thus the container, at the time limit. The limits are the
	// limits of the scan, not of the website: ClamAV needs the memory for the signatures.
	argv := []string{"env", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid), "systemd-run", "--user", "--wait", "--collect", "--pipe", "--quiet",
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(scanTimeLimit.Seconds())), "-p", "KillMode=control-group", "-p", "TimeoutStopSec=15",
		"-p", "MemoryMax=" + strconv.Itoa(scanMemory), "-p", "CPUQuota=100%", "--", "podman"}
	argv = append(argv, strings.Fields(u.Gvisor.GlobalArgs())...)
	argv = append(argv, "run", "--rm", "--pull=never", "--network=none", "--userns=keep-id", "--cap-drop=all",
		"--security-opt=no-new-privileges", "--read-only", "--tmpfs=/tmp",
		"--volume="+u.SiteDir+":"+scanMount+":ro,Z", "--volume="+d.scanSignatureDir()+":"+quadlet.ScanSignatures+":ro",
		d.ScanImage, "clamscan", "--recursive", "--infected", "--stdout", "--tempdir=/tmp",
		"--max-filesize="+scanMaxFileSize, "--max-scansize="+scanMaxScanSize, "--database="+quadlet.ScanSignatures, scanMount)
	ctx, cancel := context.WithTimeout(ctx, scanTimeLimit+time.Minute)
	defer cancel()
	// clamscan ends with the exit code 1 when it finds malware, and 2 when it could not read
	// a file. The summary has the result in both cases.
	out, err := d.asUser(ctx, user, "", argv...)
	result, parseErr := parseClamscan(out)
	if parseErr != nil {
		if err != nil {
			return result, commandError("the scan failed", err)
		}
		return result, parseErr
	}
	return result, nil
}

// ScanWebsite updates the signatures and scans the files of a website.
func (d *Deps) ScanWebsite(req *agentv1.ScanWebsiteRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetWebsiteId()), validate.UnixUser(req.GetUnixUser())); err != nil {
		return jobs.Job{}, err
	}
	if err := d.checkOwner(req.GetWebsiteId(), req.GetUnixUser()); err != nil {
		return jobs.Job{}, err
	}
	if d.ScanImage == "" {
		return jobs.Job{}, errors.New("this agent has no malware scanner")
	}
	var result ScanResult
	return jobs.Job{ID: req.GetJobId(), Key: scanJobKey, Long: true, Steps: []jobs.Step{
		{Name: "Update the malware signatures", Do: d.updateSignatures},
		{Name: "Scan the files", Do: func(ctx context.Context) error {
			site, err := d.knownSite(req.GetWebsiteId())
			if err != nil {
				return err
			}
			result, err = d.scanFiles(ctx, site.Website)
			return err
		}},
	}, Result: func() map[string]any {
		findings := make([]any, len(result.Findings))
		for i, f := range result.Findings {
			findings[i] = map[string]any{"path": f.Path, "signature": f.Signature}
		}
		return map[string]any{
			"scannedFiles": result.ScannedFiles, "infectedFiles": result.InfectedFiles, "errors": result.Errors,
			"signatures": result.Signatures, "engine": result.Engine, "findings": findings, "truncated": result.Truncated,
		}
	}}, nil
}
