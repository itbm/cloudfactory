package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/sitefiles"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The file manager and the logs of the panel. These functions do not make a job: the panel
// waits for the answer. The archives are jobs, because an archive can take a long time. Each
// of them runs a command as the Unix user of the website, so the kernel limits it to the
// files of that website. The agent never opens a file of a website as root or as its own
// user.

// ErrUnknownWebsite is the error for a website that the agent does not have.
var ErrUnknownWebsite = errors.New("the agent does not know this website")

// FileError is an error of a file operation with a code of the package sitefiles.
type FileError struct {
	Code string
	Text string
}

func (e *FileError) Error() string { return e.Text }

// agentBinary is the program of the agent. The website user can run it: the file manager
// starts it as that user ("cf-agent site-files").
func (d *Deps) agentBinary() string {
	if d.AgentBinary != "" {
		return d.AgentBinary
	}
	return "/usr/local/bin/cf-agent"
}

// knownSite gives a website of the state.
func (d *Deps) knownSite(websiteID string) (*Site, error) {
	if err := validate.ID(websiteID); err != nil {
		return nil, &FileError{Code: sitefiles.CodeInvalid, Text: err.Error()}
	}
	site, err := d.State.Get(websiteID)
	if err != nil {
		return nil, err
	}
	if site == nil {
		return nil, ErrUnknownWebsite
	}
	return site, nil
}

// checkFileRequest checks the paths and the size of a file operation.
func checkFileRequest(req sitefiles.Request) error {
	paths := []string{req.Path}
	switch req.Op {
	case sitefiles.OpMove, sitefiles.OpExtract:
		paths = append(paths, req.To)
	case sitefiles.OpArchive:
		if len(req.Paths) == 0 || len(req.Paths) > validate.MaxArchivePaths {
			return &FileError{Code: sitefiles.CodeInvalid, Text: fmt.Sprintf("an archive needs 1 to %d paths", validate.MaxArchivePaths)}
		}
		paths = append(paths, req.Paths...)
	}
	for _, p := range paths {
		if err := sitefiles.CheckPath(p); err != nil {
			return &FileError{Code: sitefiles.CodeInvalid, Text: err.Error()}
		}
	}
	if len(req.Content) > sitefiles.MaxWrite {
		return &FileError{Code: sitefiles.CodeTooLarge, Text: "the file is too large for the file manager; use SFTP"}
	}
	return nil
}

// Files does one file operation in the directory of a website, as the Unix user of the
// website.
func (d *Deps) Files(ctx context.Context, websiteID string, req sitefiles.Request) (sitefiles.Response, error) {
	site, err := d.knownSite(websiteID)
	if err != nil {
		return sitefiles.Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return d.siteFiles(ctx, site, req)
}

// siteFiles starts "cf-agent site-files" as the website user with one request. An archive
// gets a lower CPU priority ("nice"), so that the websites on the server stay fast.
func (d *Deps) siteFiles(ctx context.Context, site *Site, req sitefiles.Request) (sitefiles.Response, error) {
	var resp sitefiles.Response
	if err := checkFileRequest(req); err != nil {
		return resp, err
	}
	in, err := json.Marshal(req)
	if err != nil {
		return resp, err
	}
	command := []string{"runuser", "-u", site.UnixUser, "--"}
	if req.Op == sitefiles.OpArchive || req.Op == sitefiles.OpExtract {
		command = append(command, "nice", "-n", "10")
	}
	command = append(command, d.agentBinary(), "site-files", d.Host.AppDir(site.UnixUser))
	out, err := d.Run.RunInput(ctx, string(in), command[0], command[1:]...)
	if err != nil {
		return resp, fmt.Errorf("the file manager did not start: %w", err)
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return resp, errors.New("the file manager gave no answer")
	}
	if resp.Code != "" {
		return resp, &FileError{Code: resp.Code, Text: resp.Error}
	}
	return resp, nil
}

// MakeArchive puts files and directories of a website in a new archive, as a job.
func (d *Deps) MakeArchive(req *agentv1.MakeArchiveRequest) (jobs.Job, error) {
	return d.archiveJob(req.GetJobId(), req.GetWebsiteId(), "Make the archive",
		sitefiles.Request{Op: sitefiles.OpArchive, Path: req.GetPath(), Paths: req.GetPaths()})
}

// ExtractArchive extracts an archive of a website into a directory of the website, as a job.
func (d *Deps) ExtractArchive(req *agentv1.ExtractArchiveRequest) (jobs.Job, error) {
	return d.archiveJob(req.GetJobId(), req.GetWebsiteId(), "Extract the archive",
		sitefiles.Request{Op: sitefiles.OpExtract, Path: req.GetPath(), To: req.GetTo(), Overwrite: req.GetOverwrite()})
}

// archiveJob makes the job of an archive operation. The job has the long time limit. Its key
// is not the website: an archive does not stop the other jobs of the website, but two
// archives of one website do not run at the same time.
func (d *Deps) archiveJob(jobID, websiteID, step string, req sitefiles.Request) (jobs.Job, error) {
	if err := errors.Join(validate.ID(websiteID), checkFileRequest(req)); err != nil {
		return jobs.Job{}, err
	}
	var summary *sitefiles.Summary
	return jobs.Job{ID: jobID, Key: "files:" + websiteID, Long: true, Steps: []jobs.Step{
		{Name: step, Do: func(ctx context.Context) error {
			site, err := d.knownSite(websiteID)
			if err != nil {
				return err
			}
			resp, err := d.siteFiles(ctx, site, req)
			if err != nil {
				return err
			}
			summary = resp.Summary
			return nil
		}},
	}, Result: func() map[string]any {
		if summary == nil {
			return nil
		}
		return map[string]any{
			"files": summary.Files, "directories": summary.Directories, "links": summary.Links, "sizeBytes": summary.SizeBytes,
			"kept": summary.Kept, "skipped": summary.Skipped, "skippedNames": summary.SkippedNames,
		}
	}}, nil
}

// FileEntry gives an entry of the file manager as a message for the panel.
func FileEntry(e sitefiles.Entry) *agentv1.FileEntry {
	return &agentv1.FileEntry{
		Name: e.Name, Type: e.Type, SizeBytes: e.SizeBytes, Target: e.Target,
		Modified: e.Modified.UTC().Format(time.RFC3339), Mode: fmt.Sprintf("%04o", e.Mode),
	}
}

// logFile is the file that has the output of the containers of a website. Podman writes it
// (quadlet.Website.LogFile), and the website user owns it.
func (d *Deps) logFile(unixUser string) string {
	return filepath.Join(d.Host.Home(unixUser), "website.log")
}

// The limits of GetLogs.
const (
	defaultLogLines = 200
	maxLogLines     = 2000
	maxLogLineBytes = 4000
)

// Logs gives the last lines of the output of the containers of a website. The standard
// output has the access log, and the standard error has the errors.
func (d *Deps) Logs(ctx context.Context, websiteID string, lines int) ([]*agentv1.LogLine, error) {
	site, err := d.knownSite(websiteID)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = defaultLogLines
	}
	lines = min(lines, maxLogLines)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := d.Run.Run(ctx, "runuser", "-u", site.UnixUser, "--", "tail", "-n", strconv.Itoa(lines), d.logFile(site.UnixUser))
	if err != nil {
		// A website that did not start after this release has no log file yet.
		return nil, nil
	}
	return parseLogs(out), nil
}

// parseLogs reads lines in the "k8s-file" format of Podman:
// "<time> <stdout|stderr> <F|P> <text>". It ignores a line with a different format.
func parseLogs(out string) []*agentv1.LogLine {
	var lines []*agentv1.LogLine
	for _, raw := range strings.Split(out, "\n") {
		parts := strings.SplitN(raw, " ", 4)
		if len(parts) < 3 {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			continue
		}
		stream := "access"
		switch parts[1] {
		case "stdout":
		case "stderr":
			stream = "error"
		default:
			continue
		}
		text := ""
		if len(parts) == 4 {
			text = cleanText(parts[3])
		}
		if len(text) > maxLogLineBytes {
			text = strings.ToValidUTF8(text[:maxLogLineBytes], "") + "…"
		}
		lines = append(lines, &agentv1.LogLine{At: at.UTC().Format(time.RFC3339Nano), Stream: stream, Text: text})
	}
	return lines
}

// cleanText gives a text of a website with no control character and no invalid UTF-8. The
// owner of a website controls the log lines, the output of a cron job, the output of a build
// and the subject of a commit. A person with more rights reads them, also in a terminal,
// where a control character can change earlier lines (adversarial review, finding G5-09).
// Tabs and line ends stay.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || r == 0x2028 || r == 0x2029:
			return '�'
		}
		return r
	}, s)
}
