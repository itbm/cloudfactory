package actions

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/sitearchive"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Account export (docs/account-export.md). The panel makes the description of the account and
// the DNS zone files itself. The agent of each server sends the data that only the server has:
// the files of each website and a dump of each database. Each of them is one part of the
// export. The agent sends a part to the panel while it makes it, in pieces
// (panel.Client.UploadExportPart), so the server keeps no copy and needs no free disk space.
//
// The worker does the work with no new root work:
//
//   - The files: "cf-agent site-archive" runs as the Unix user of the website, as the file
//     manager does (package sitearchive). It writes a tar archive with gzip to its standard
//     output.
//   - The dumps: the dump command of a backup runs in the database container, as the Unix
//     user of the database server (policy: allowDatabasePodman). The root helper sends its
//     output to the worker. A MariaDB dump has no DEFINER clauses (withoutDefiners), so that
//     a different server can load it as a user that is not the superuser. The agent compresses
//     it with gzip. A PostgreSQL dump has the custom format of pg_dump, which is compressed.
//
// The export has no Valkey data: a Valkey database is a cache, and the panel does not send it.

// ExportUploader sends one part of an account export to the panel. It reads r to the end, and
// gives the number of bytes and their SHA-256 hash.
type ExportUploader interface {
	UploadExportPart(ctx context.Context, partID string, r io.Reader) (size int64, sum string, err error)
}

// The file names of the dumps in an export. The panel gives each part its path in the
// archive. The agent compresses only the MariaDB dump, which is SQL text.
func exportCompresses(engine string) bool { return engine == "mariadb" }

// ExportWebsite sends the parts of an account export of one website to the panel: the files of
// the website and the dumps of its databases on this server.
func (d *Deps) ExportWebsite(req *agentv1.ExportWebsiteRequest) (jobs.Job, error) {
	websiteID, unixUser, customer := req.GetWebsiteId(), req.GetUnixUser(), req.GetCustomer()
	if err := errors.Join(validate.ID(websiteID), validate.UnixUser(unixUser)); err != nil {
		return jobs.Job{}, err
	}
	only := req.GetDatabasesOnly()
	if only {
		if len(req.GetDatabases()) == 0 {
			return jobs.Job{}, errors.New("an export of the databases only needs a database")
		}
		if req.GetFilesPart() != "" {
			return jobs.Job{}, errors.New("an export of the databases only has no files")
		}
		if _, err := instanceKey(customer); err != nil {
			return jobs.Job{}, err
		}
	} else {
		// Adversarial review, finding X2-01: the Unix user of the request is the Unix user of
		// this website in the state of the agent.
		site, err := d.knownSite(websiteID)
		if err != nil {
			return jobs.Job{}, err
		}
		if site.UnixUser != unixUser {
			return jobs.Job{}, fmt.Errorf("the website %s does not have the Unix user %s", websiteID, unixUser)
		}
	}
	parts := map[string]bool{}
	addPart := func(id string) error {
		if err := validate.ID(id); err != nil {
			return fmt.Errorf("the export part: %w", err)
		}
		if parts[id] {
			return fmt.Errorf("the export part %s is in the job two times", id)
		}
		parts[id] = true
		return nil
	}
	if req.GetFilesPart() != "" {
		if err := addPart(req.GetFilesPart()); err != nil {
			return jobs.Job{}, err
		}
	}
	for _, db := range req.GetDatabases() {
		if err := sqlEngine(db.GetEngine()); err != nil {
			return jobs.Job{}, err
		}
		if err := validate.TenantDatabaseName(unixUser, db.GetName()); err != nil {
			return jobs.Job{}, err
		}
		if _, err := serverKey(db.GetDatabaseServer()); err != nil {
			return jobs.Job{}, err
		}
		if err := addPart(db.GetPart()); err != nil {
			return jobs.Job{}, err
		}
	}
	if len(parts) == 0 {
		return jobs.Job{}, errors.New("the export job has no part")
	}
	if d.Exports == nil {
		return jobs.Job{}, errors.New("this agent cannot send an account export")
	}
	sent := map[string]any{}
	record := func(part string, size int64, sum string) {
		sent[part] = map[string]any{"sizeBytes": size, "sha256": sum}
	}
	// The key of the website: an export, a backup and a restore of one website do not run at
	// the same time.
	return jobs.Job{ID: req.GetJobId(), Key: websiteID, Long: true, Steps: []jobs.Step{
		{Name: "Send database dumps", Do: func(ctx context.Context) error {
			for _, db := range req.GetDatabases() {
				size, sum, err := d.exportDump(ctx, websiteID, customer, db)
				if err != nil {
					return fmt.Errorf("database %s: %w", db.GetName(), err)
				}
				record(db.GetPart(), size, sum)
			}
			return nil
		}},
		{Name: "Send files", Do: func(ctx context.Context) error {
			if req.GetFilesPart() == "" {
				return nil
			}
			size, sum, err := d.exportFiles(ctx, websiteID, unixUser, req.GetFilesPart())
			if err != nil {
				return err
			}
			record(req.GetFilesPart(), size, sum)
			return nil
		}},
	}, Result: func() map[string]any {
		return map[string]any{"parts": sent}
	}}, nil
}

// exportDump sends the dump of one database to the panel.
func (d *Deps) exportDump(ctx context.Context, websiteID, customer string, db *agentv1.ExportDatabase) (int64, string, error) {
	srv, err := d.serverFor(websiteID, customer, db.GetDatabaseServer(), db.GetEngine())
	if err != nil {
		return 0, "", err
	}
	argv, err := d.dumpCommand(ctx, srv, db.GetName())
	if err != nil {
		return 0, "", err
	}
	drv, err := sqlOf(srv.Engine)
	if err != nil {
		return 0, "", err
	}
	release, err := d.backupSlot(ctx)
	if err != nil {
		return 0, "", err
	}
	defer release()
	return d.sendPart(ctx, db.GetPart(), func(ctx context.Context, w io.Writer) error {
		if !exportCompresses(srv.Engine) {
			return d.Run.Stream(ctx, nil, w, argv[0], argv[1:]...)
		}
		// The dump goes through the filter of a restore, then through gzip.
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(d.Run.Stream(ctx, nil, pw, argv[0], argv[1:]...)) }()
		var dump io.Reader = pr
		if filter := drv.dumpFilter(); filter != nil {
			dump = filter(pr)
		}
		gz := gzip.NewWriter(w)
		_, err := io.Copy(gz, dump)
		// The dump command stops when the copy stops early.
		pr.CloseWithError(errExportStopped)
		if err != nil {
			return err
		}
		return gz.Close()
	})
}

// exportFiles sends the archive of the files of a website to the panel. The archive does not
// have a file that is larger than the disk limit of the website, as a backup does not.
func (d *Deps) exportFiles(ctx context.Context, websiteID, unixUser, part string) (int64, string, error) {
	if _, err := d.appExists(ctx, unixUser); err != nil {
		return 0, "", err
	}
	var maxBytes int64
	if site, err := d.State.Get(websiteID); err == nil && site != nil {
		maxBytes = max(site.Website.GetLimits().GetDiskBytes(), 0)
	}
	release, err := d.backupSlot(ctx)
	if err != nil {
		return 0, "", err
	}
	defer release()
	argv := []string{"runuser", "-u", unixUser, "--", d.agentBinary(), sitearchive.Command, d.Host.AppDir(unixUser), strconv.FormatInt(maxBytes, 10)}
	return d.sendPart(ctx, part, func(ctx context.Context, w io.Writer) error {
		return d.Run.Stream(ctx, nil, w, argv[0], argv[1:]...)
	})
}

// errExportStopped stops the command of a part when the upload of the part stops.
var errExportStopped = errors.New("the upload of the export part stopped")

// sendPart runs produce, which writes a part, and sends its output to the panel at the same time.
// When the upload fails, the command stops. When the command fails, the upload stops, and the
// panel does not get a complete part: the result of the job has only the parts that are
// complete.
func (d *Deps) sendPart(ctx context.Context, part string, produce func(ctx context.Context, w io.Writer) error) (int64, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	made := make(chan error, 1)
	go func() {
		err := produce(ctx, pw)
		pw.CloseWithError(err)
		made <- err
	}()
	size, sum, uploadErr := d.Exports.UploadExportPart(ctx, part, pr)
	if uploadErr != nil {
		cancel()
		pr.CloseWithError(errExportStopped)
	}
	produceErr := <-made
	switch {
	case produceErr != nil && (uploadErr == nil || errors.Is(uploadErr, produceErr)):
		// The command failed first. The upload stopped because of it.
		return 0, "", produceErr
	case uploadErr != nil:
		return 0, "", uploadErr
	}
	return size, sum, nil
}
