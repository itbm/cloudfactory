package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/sitefiles"
	"github.com/itbm/cloudfactory/agent/internal/sitelogs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The file manager and the logs of the panel. These functions do not make a job: the panel
// waits for the answer. The archives are jobs, because an archive can take a long time. Each
// of them runs a command as the Unix user of the website, so the kernel limits it to the
// files of that website. The agent never opens a file of a website as root or as its own
// user. The access log of the edge proxy is a file of the worker, not of the website.

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

// ensureAccessLog makes the directory of the access log of a website. The worker keeps the
// entries of a website only when the directory is there.
func (d *Deps) ensureAccessLog(websiteID string) error {
	if d.AccessLogs == nil {
		return nil
	}
	return d.AccessLogs.Ensure(websiteID)
}

// SearchLogs searches the logs of a website: the access log of the edge proxy, which the
// worker keeps (sitelogs.Store), and the log file of its containers, which the agent reads as
// the website user. With no website, it searches the access log of the routes that are not
// for a website.
func (d *Deps) SearchLogs(ctx context.Context, req *agentv1.SearchLogsRequest) (*agentv1.LogSearchResult, error) {
	q, cursor, err := sitelogs.Parse(sitelogs.Request{
		Sources: req.GetSources(), Text: req.GetText(), Statuses: req.GetStatuses(), Methods: req.GetMethods(),
		Client: req.GetClient(), From: req.GetFrom(), To: req.GetTo(), Cursor: req.GetCursor(), Limit: int(req.GetLimit()), Summary: req.GetSummary(),
	})
	if err != nil {
		return nil, &FileError{Code: sitefiles.CodeInvalid, Text: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	logName := sitelogs.ServerLog
	var site *Site
	if req.GetWebsiteId() != "" {
		if site, err = d.knownSite(req.GetWebsiteId()); err != nil {
			return nil, err
		}
		logName = req.GetWebsiteId()
	} else {
		q.Sources = []string{sitelogs.SourceAccess}
	}
	var access, container *sitelogs.Group
	if slices.Contains(q.Sources, sitelogs.SourceAccess) && d.AccessLogs != nil {
		g, err := d.AccessLogs.Search(logName, &q, cursor.Access)
		if err != nil {
			return nil, err
		}
		access = &g
	}
	switch {
	case site == nil || !slices.Contains(q.Sources, sitelogs.SourceOutput) && !slices.Contains(q.Sources, sitelogs.SourceError):
	case cursor.Container != nil && cursor.Container.End && !q.Summary:
		// The earlier pages gave all lines of the containers.
		container = &sitelogs.Group{}
	default:
		in, err := sitelogs.ContainerRequest(&q, cursor.Container)
		if err != nil {
			return nil, err
		}
		out, err := d.Run.RunInput(ctx, string(in), "runuser", "-u", site.UnixUser, "--", d.agentBinary(), "log-search", d.logFile(site.UnixUser))
		if err != nil {
			return nil, fmt.Errorf("the search of the log did not start: %w", err)
		}
		g, err := sitelogs.ReadContainerResult([]byte(out))
		if err != nil {
			return nil, err
		}
		container = &g
	}
	entries, next, more := sitelogs.Merge(q.Limit, cursor, access, container)
	out := &agentv1.LogSearchResult{}
	for _, e := range entries {
		out.Entries = append(out.Entries, logEntry(e))
	}
	if more {
		out.Next = next.Encode()
	}
	if q.Summary {
		var sum *sitelogs.Summary
		for _, g := range []*sitelogs.Group{access, container} {
			if g != nil {
				sum = sitelogs.AddSummary(sum, g.Summary)
			}
		}
		out.Summary = logSummary(sum)
	}
	return out, nil
}

// logEntry gives an entry of a log as a message for the panel.
func logEntry(e sitelogs.Entry) *agentv1.LogEntry {
	out := &agentv1.LogEntry{At: e.At.UTC().Format(time.RFC3339Nano), Source: e.Source, Text: e.Text}
	if a := e.Access; a != nil {
		out.Access = &agentv1.AccessEntry{
			Client: a.Client, Method: a.Method, Host: a.Host, Uri: a.URI, Proto: a.Proto, Status: int32(a.Status),
			SizeBytes: a.Size, DurationSeconds: a.Duration, UserAgent: a.UserAgent, Referer: a.Referer,
		}
	}
	return out
}

// logSummary gives the summary of a search as a message for the panel.
func logSummary(s *sitelogs.Summary) *agentv1.LogSummary {
	out := &agentv1.LogSummary{}
	if s == nil {
		return out
	}
	out.Access, out.Output, out.Error, out.Statuses, out.SizeBytes = s.Access, s.Output, s.Error, s.Statuses, s.SizeBytes
	for _, c := range s.TopPaths {
		out.TopPaths = append(out.TopPaths, &agentv1.LogCount{Value: c.Value, Count: c.Count})
	}
	for _, c := range s.TopClients {
		out.TopClients = append(out.TopClients, &agentv1.LogCount{Value: c.Value, Count: c.Count})
	}
	if !s.Oldest.IsZero() {
		out.Oldest = s.Oldest.UTC().Format(time.RFC3339Nano)
	}
	return out
}
