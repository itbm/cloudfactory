import { useState } from "react";
import { useAction, useLoad, useStore } from "../api/hooks";
import { backupsReady } from "../api/selectors";
import { backUpMailbox, listMailboxBackups, restoreMailbox } from "../api/store";
import type { Job, Mailbox, MailboxBackup, MailboxRestore } from "../api/types";
import { FormError, JobRow, Modal, TableWrap } from "../components/ui";
import { bytes, clockTime, count, longDate } from "../lib/format";

const plural = (n: number, one: string, many: string) => `${count(n)} ${n === 1 ? one : many}`;

/** The backup and the restore jobs of a mailbox (MailBackupController). */
const ofMailbox = (j: Job, mailbox: Mailbox) => (j.action === "BackupMailbox" || j.action === "RestoreMailbox") && j.title.endsWith(` ${mailbox.address}`);
const open = (j: Job) => j.state === "queued" || j.state === "running";

/** What the last restore of a backup did, and the messages that it did not add. */
export function restoreSummary(r: MailboxRestore): { text: string; warning: string | null } {
  const when = r.at ? `Restored on ${longDate(r.at)} at ${clockTime(r.at)}` : "Restored";
  const text = `${when}. ${plural(r.added, "message", "messages")} added, ${count(r.present)} already in the mailbox.`;
  const missing = [
    r.tooLarge ? `${plural(r.tooLarge, "message is", "messages are")} larger than the mail server accepts` : "",
    r.failed ? `the mail server refused ${plural(r.failed, "message", "messages")}` : "",
  ].filter(Boolean);
  return { text, warning: missing.length ? `Not restored: ${missing.join(", and ")}.` : null };
}

/**
 * The backups of a mailbox. The panel makes a backup of each mailbox each night. A restore
 * adds the messages of a backup that the mailbox does not have, in their folders.
 */
export function MailboxBackupsDialog({ mailbox, onClose }: { mailbox: Mailbox; onClose: () => void }) {
  const state = useStore();
  const load = useLoad(() => listMailboxBackups(mailbox.id), [mailbox.id]);
  const backup = useAction(backUpMailbox);
  const [restoring, setRestoring] = useState<MailboxBackup | null>(null);
  const jobs = state.jobs.filter((j) => ofMailbox(j, mailbox)).slice(0, 2);
  const busy = jobs.some(open);
  const ready = backupsReady(state);
  const admin = state.session.user.role === "administrator";
  // The list loads again when a job ends, so it shows the new backup or the restore result.
  const reloadAfter = (r: { ok: true; value: { job: Promise<Job> } } | { ok: false }) => {
    if (r.ok) void r.value.job.then(load.reload, load.reload);
  };

  if (restoring) {
    return <RestoreStep mailbox={mailbox} backup={restoring} onBack={() => setRestoring(null)} onClose={onClose} onStarted={(r) => { reloadAfter(r); setRestoring(null); }} />;
  }
  const backups = load.data ?? [];
  return (
    <Modal
      title={`Backups of ${mailbox.address}`}
      onClose={onClose}
      wide
      footer={<>
        <button type="button" className="btn" onClick={onClose}>Close</button>
        {ready && <button type="button" className="btn btn-primary" disabled={busy || backup.busy} onClick={async () => reloadAfter(await backup.run(mailbox.id))}>Back up now</button>}
      </>}
    >
      <FormError error={backup.error} />
      {jobs.length > 0 && <ul className="job-list" style={{ marginTop: 0 }}>{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul>}
      {load.error && <FormError error={load.error} />}
      {!load.data && !load.error && <p className="muted" role="status">The panel reads the backups…</p>}
      {load.data && (
        <TableWrap>
          <table>
            <thead><tr><th scope="col">Backup</th><th scope="col">Type</th><th scope="col">Messages</th><th scope="col">Size</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {backups.map((b) => {
                const last = b.lastRestore && restoreSummary(b.lastRestore);
                return (
                  <tr key={b.id}>
                    <td>
                      {longDate(b.createdAt)} <span className="cell-sub">{clockTime(b.createdAt)}</span>
                      {last && <span className="cell-sub">{last.text}</span>}
                      {last?.warning && <span className="cell-sub text-warning">{last.warning}</span>}
                    </td>
                    <td>{b.kind === "scheduled" ? "Nightly" : "Manual"}</td>
                    <td>{count(b.messages)}</td>
                    <td className="nowrap">{bytes(b.sizeBytes)}</td>
                    <td className="cell-actions" style={{ width: 110 }}>
                      <button type="button" className="btn btn-sm" disabled={busy} aria-label={`Restore the backup of ${longDate(b.createdAt)} ${clockTime(b.createdAt)}`} onClick={() => setRestoring(b)}>Restore…</button>
                    </td>
                  </tr>
                );
              })}
              {backups.length === 0 && <tr><td colSpan={5} className="muted">No backups yet. {ready ? "The panel makes the first backup tonight." : ""}</td></tr>}
            </tbody>
          </table>
        </TableWrap>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>
        {busy && <>A backup or a restore of this mailbox is in progress. You can start a different one when it is complete.{" "}</>}
        {ready
          ? <>The panel makes a backup of each mailbox each night, with the retention policy of the websites. It keeps the newest {state.backupPolicy.manual ?? 10} manual backups. {admin ? "Each backup is encrypted with restic and kept in the object storage of the backup policy." : "Backups are encrypted and kept in object storage, not on the mail server."}</>
          : admin ? "No object storage is set for backups, so the panel makes no backups. Set it in the backup policy on the Backups page." : "Your provider has not set up backups on this panel, so the panel makes no backups."}
      </p>
    </Modal>
  );
}

function RestoreStep({ mailbox, backup, onBack, onClose, onStarted }: {
  mailbox: Mailbox;
  backup: MailboxBackup;
  onBack: () => void;
  onClose: () => void;
  onStarted: (r: { ok: true; value: { job: Promise<Job> } }) => void;
}) {
  const action = useAction(restoreMailbox);
  const when = `${longDate(backup.createdAt)} ${clockTime(backup.createdAt)}`;
  return (
    <Modal
      title={`Restore ${mailbox.address} from ${when}?`}
      onClose={onClose}
      footer={<>
        <button type="button" className="btn" onClick={onBack}>Back</button>
        <button type="button" className="btn btn-primary" disabled={action.busy} onClick={async () => {
          const r = await action.run(mailbox.id, backup.id);
          if (r.ok) onStarted(r);
        }}>{action.busy ? "Restoring…" : "Restore messages"}</button>
      </>}
    >
      <FormError error={action.error} />
      <p style={{ marginTop: 0 }}>
        The backup has {plural(backup.messages, "message", "messages")}. The panel adds each message that the mailbox does not have now, in its folder.
        It makes again each folder that the user removed.
      </p>
      <p className="muted small">
        The restore does not change or remove a message that is in the mailbox now. A message that the user moved to a different folder stays there.
        You can restore the same backup again: the panel adds only the messages that are missing.
        Messages that are larger than the mail server accepts stay in the backup.
      </p>
    </Modal>
  );
}
