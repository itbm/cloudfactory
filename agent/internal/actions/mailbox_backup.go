package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Mailbox backups. A snapshot of a mailbox has one file, mailboxBackupFile: the tar stream of
// stalwart.ExportMailbox. The worker reads the messages from the management API of the mail
// server and sends the stream to restic on stdin. A restore reads the stream from restic on
// stdout and adds the messages through the same API. The messages are thus never on the disk
// of the server, and restic reads and writes no path.

// mailboxBackupFile is the name of the file in a snapshot of a mailbox.
const mailboxBackupFile = "mailbox.tar"

// mailboxBackupKey is the key of the backup and restore jobs of one mailbox: they do not run
// at the same time. They do not wait for the other mail jobs, which have mailKey.
func mailboxBackupKey(mailboxID string) string { return "mailbox-backup-" + mailboxID }

// errResticStopped closes the stream of an export when restic stops first.
var errResticStopped = errors.New("restic stopped before the end of the export")

// BackupMailbox makes a snapshot of all folders and messages of one mailbox, in the restic
// repository of this server. A scheduled backup also applies the retention policy (FR-902).
func (d *Deps) BackupMailbox(req *agentv1.BackupMailboxRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetMailboxId()), checkMailAddress(req.GetDomain(), req.GetLocalPart())); err != nil {
		return jobs.Job{}, err
	}
	kind := req.GetKind()
	if kind != "manual" && kind != "scheduled" {
		return jobs.Job{}, fmt.Errorf("the backup kind %q is not valid", kind)
	}
	if k := req.GetRetention(); k.GetLast() < 0 || k.GetDaily() < 0 || k.GetWeekly() < 0 || k.GetMonthly() < 0 {
		return jobs.Job{}, errors.New("a retention value must not be negative")
	}
	r, err := newRepo(req.GetTarget(), req.GetCredentials())
	if err != nil {
		return jobs.Job{}, err
	}
	var snapshotID string
	var size float64
	var export stalwart.Export
	forgotten := []string{}
	return jobs.Job{ID: req.GetJobId(), Key: mailboxBackupKey(req.GetMailboxId()), Long: true, Steps: []jobs.Step{
		{Name: "Export and upload the messages", Do: func(ctx context.Context) error {
			c, err := d.mailClient(ctx)
			if err != nil {
				return err
			}
			release, err := d.backupSlot(ctx)
			if err != nil {
				return err
			}
			defer release()
			if err := d.ensureRepo(ctx, r); err != nil {
				return err
			}
			out := &tail{max: 64 << 10}
			err = d.streamBackup(ctx, r, out, func(w io.Writer) error {
				var err error
				export, err = c.ExportMailbox(ctx, req.GetDomain(), req.GetLocalPart(), w)
				return err
			}, "backup", "--stdin", "--stdin-filename", mailboxBackupFile, "--tag", "mailbox="+req.GetMailboxId(), "--tag", "kind="+kind)
			if err != nil {
				return err
			}
			snapshotID, size = parseResticSummary(out.String())
			if snapshotID == "" {
				return errors.New("restic did not give a snapshot ID")
			}
			return nil
		}},
		{Name: "Apply retention", Do: func(ctx context.Context) error {
			if req.GetRetention() == nil {
				return nil
			}
			ids, err := d.forget(ctx, r, "mailbox="+req.GetMailboxId()+",kind="+kind, req.GetRetention())
			forgotten = ids
			return err
		}},
	}, Result: func() map[string]any {
		return map[string]any{"snapshotId": snapshotID, "sizeBytes": int64(size), "messages": export.Messages, "forgotten": forgotten}
	}}, nil
}

// streamBackup runs restic with the output of export on its stdin. When the export fails,
// restic stops with no snapshot (execx.Runner.StreamEnv). The error is the error of the side
// that failed first.
func (d *Deps) streamBackup(ctx context.Context, r repo, stdout io.Writer, export func(w io.Writer) error, args ...string) error {
	var first firstError
	pr, pw := io.Pipe()
	exported := make(chan struct{})
	go func() {
		defer close(exported)
		err := export(pw)
		first.set(err)
		pw.CloseWithError(err)
	}()
	first.set(d.resticStream(ctx, r, pr, stdout, args...))
	pr.CloseWithError(errResticStopped)
	<-exported
	return first.get()
}

// RestoreMailbox adds the messages of a snapshot that the mailbox does not have (stalwart.ImportMailbox).
func (d *Deps) RestoreMailbox(req *agentv1.RestoreMailboxRequest) (jobs.Job, error) {
	if err := errors.Join(validate.ID(req.GetMailboxId()), checkMailAddress(req.GetDomain(), req.GetLocalPart())); err != nil {
		return jobs.Job{}, err
	}
	if !snapshotID.MatchString(req.GetSnapshotId()) {
		return jobs.Job{}, errors.New("the snapshot ID is not valid")
	}
	r, err := newRepo(req.GetTarget(), req.GetCredentials())
	if err != nil {
		return jobs.Job{}, err
	}
	var result stalwart.Import
	return jobs.Job{ID: req.GetJobId(), Key: mailboxBackupKey(req.GetMailboxId()), Long: true, Steps: []jobs.Step{
		{Name: "Download the backup and add the messages", Do: func(ctx context.Context) error {
			c, err := d.mailClient(ctx)
			if err != nil {
				return err
			}
			release, err := d.backupSlot(ctx)
			if err != nil {
				return err
			}
			defer release()
			var first firstError
			pr, pw := io.Pipe()
			imported := make(chan struct{})
			go func() {
				defer close(imported)
				var err error
				result, err = c.ImportMailbox(ctx, req.GetDomain(), req.GetLocalPart(), pr)
				if err == nil {
					// The end of the tar stream: restic then ends normally.
					_, _ = io.Copy(io.Discard, pr)
				}
				first.set(err)
				pr.CloseWithError(errImportStopped)
			}()
			first.set(d.resticStream(ctx, r, nil, pw, "dump", req.GetSnapshotId(), "/"+mailboxBackupFile))
			pw.Close()
			<-imported
			return first.get()
		}},
	}, Result: func() map[string]any {
		return map[string]any{"added": result.Added, "present": result.Present, "tooLarge": result.TooLarge, "failed": result.Failed, "firstError": result.FirstError}
	}}, nil
}

// errImportStopped closes the stream of a restore when the import stops first.
var errImportStopped = errors.New("the import stopped before the end of the backup")

// firstError keeps the first error of the two sides of a stream. The error of the other side
// is a result of it.
type firstError struct {
	mu  sync.Mutex
	err error
}

func (f *firstError) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil {
		f.err = err
	}
}

func (f *firstError) get() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// tail keeps the last bytes of an output, for example the summary of restic after its status
// lines.
type tail struct {
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }
