import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { LIVE } from "../api/mode";
import { backupNow, partialDetails, restore, showWebsite, type RestoreScope } from "../api/store";
import type { Snapshot } from "../api/types";
import { Card, Field, FormError, JobRow, Modal } from "../components/ui";
import { bytes, clockTime, longDate } from "../lib/format";

/** Snapshots and restore (FR-901, FR-903). With websiteId, it shows one website only. */
export function BackupsPanel({ websiteId }: { websiteId?: string }) {
  const state = useStore();
  const [site, setSite] = useState(websiteId ?? "all");
  const shownSite = websiteId ?? site;
  const snaps = state.snapshots.filter((s) => shownSite === "all" || s.websiteId === shownSite).slice(0, websiteId ? 20 : 30);
  const [restoring, setRestoring] = useState<Snapshot | null>(null);
  const backup = useAction(backupNow);
  const jobs = state.jobs.filter((j) => (j.title.startsWith("Back up") || j.title.startsWith("Restore")) && (!websiteId || j.websiteId === websiteId)).slice(0, 4);
  const siteName = (id: string) => state.websites.find((w) => w.id === id)?.name ?? "Removed website";
  useEffect(() => {
    if (shownSite !== "all") showWebsite(shownSite);
  }, [shownSite]);

  return (
    <div className="stack">
      {jobs.length > 0 && (
        <Card title="Backup jobs">
          <div className="card-body"><ul className="job-list">{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul></div>
        </Card>
      )}
      <Card
        title="Snapshots"
        actions={
          <div className="btn-group">
            {!websiteId && (
              <>
                <label className="visually-hidden" htmlFor="snap-site">Website</label>
                <select id="snap-site" className="select" value={site} onChange={(e) => setSite(e.target.value)}>
                  <option value="all">All websites</option>
                  {state.websites.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
                </select>
              </>
            )}
            {shownSite !== "all" && <button type="button" className="btn btn-primary" disabled={backup.busy} onClick={() => void backup.run(shownSite)}>Back up now</button>}
          </div>
        }
      >
        {backup.error && <div className="card-body"><FormError error={backup.error} /></div>}
        <div className="table-wrap">
          <table>
            <thead><tr><th scope="col">Snapshot</th>{!websiteId && <th scope="col">Website</th>}<th scope="col">Type</th><th scope="col">Size</th><th scope="col">Contents</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {snaps.map((s) => (
                <tr key={s.id}>
                  <td>{longDate(s.at)} <span className="cell-sub">{clockTime(s.at)}</span></td>
                  {!websiteId && <td><Link to={`/websites/${s.websiteId}/backups`}>{siteName(s.websiteId)}</Link></td>}
                  <td>{s.kind === "nightly" ? "Nightly" : "Manual"}</td>
                  <td>{bytes(s.sizeBytes)}</td>
                  <td className="small muted">Files{s.databases.length ? `, ${s.databases.length} database${s.databases.length > 1 ? "s" : ""}` : ""}{s.mailboxes.length ? `, ${s.mailboxes.length} mailbox${s.mailboxes.length > 1 ? "es" : ""}` : ""}</td>
                  <td className="cell-actions" style={{ width: 120 }}><button type="button" className="btn btn-sm" onClick={() => setRestoring(s)}>Restore…</button></td>
                </tr>
              ))}
              {snaps.length === 0 && <tr><td colSpan={6} className="muted">No snapshots.</td></tr>}
            </tbody>
          </table>
        </div>
        {shownSite === "all" && partialDetails() && <p className="card-body muted small">There are many websites, so this list shows only the websites that you opened. Select a website to see its snapshots.</p>}
        <p className="card-body muted small" style={{ paddingTop: 12 }}>
          {/* Audit finding R8: the retention policy does not remove manual backups. */}
          The panel keeps the newest {state.backupPolicy.manual ?? 10} manual backups of each website. The retention policy applies to the nightly backups only.{" "}
          {state.backupPolicy.target ? <>Snapshots are encrypted with restic and kept in object storage on a different server: <code>{state.backupPolicy.target}</code>.</> : "No object storage is set for backups. An administrator sets it in the backup policy."}
        </p>
      </Card>
      {restoring && <RestoreDialog snapshot={restoring} onClose={() => setRestoring(null)} />}
    </div>
  );
}

/** What the agent of this release does for each restore (agent/internal/actions/data.go). */
const RESTORE_NOTE: Record<RestoreScope["kind"], string> = {
  website: "The panel puts the files from the backup into the website, and then starts the website again. It does not restore the databases: restore each database separately. Make a backup first if you want to keep the current version.",
  database: "The panel replaces the database with the dump from the backup. Make a backup first if you want to keep the current version.",
  mailbox: "",
  file: "The panel replaces the file or the folder with the version from the backup. Make a backup first if you want to keep the current version.",
};

function RestoreDialog({ snapshot, onClose }: { snapshot: Snapshot; onClose: () => void }) {
  const [kind, setKind] = useState<RestoreScope["kind"]>("website");
  const [item, setItem] = useState("");
  const [removeNewFiles, setRemoveNewFiles] = useState(false);
  const action = useAction(restore);
  const scope = (): RestoreScope => (kind === "website" ? { kind } : kind === "file" ? { kind, path: item } : { kind, name: item });
  const options = kind === "database" ? snapshot.databases : kind === "mailbox" ? snapshot.mailboxes : [];

  return (
    <Modal
      title={`Restore from ${longDate(snapshot.at)} ${clockTime(snapshot.at)}`}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={kind !== "website" && !item}
            onClick={async () => {
              const result = await action.run(snapshot.id, scope(), kind === "website" && removeNewFiles);
              if (result.ok) onClose();
            }}
          >
            Restore
          </button>
        </>
      }
    >
      <FormError error={action.error} />
      <fieldset className="radio-cards">
        <legend>What to restore</legend>
        {([
          ["website", "Full website", LIVE ? "All files of the website." : "Files, databases and mailboxes."],
          ["database", "One database", `${snapshot.databases.length} in this snapshot.`],
          ["mailbox", "One mailbox", `${snapshot.mailboxes.length} in this snapshot.`],
          ["file", "One file or folder", "Type the path."],
        ] as const).filter(([k]) => !LIVE || k !== "mailbox").map(([k, label, hint]) => (
          <label key={k} className="radio-card">
            <input type="radio" name="scope" checked={kind === k} disabled={(k === "database" && !snapshot.databases.length) || (k === "mailbox" && !snapshot.mailboxes.length)} onChange={() => { setKind(k); setItem(""); }} />
            <span><strong>{label}</strong><span>{hint}</span></span>
          </label>
        ))}
      </fieldset>
      {(kind === "database" || kind === "mailbox") && (
        <Field label={kind === "database" ? "Database" : "Mailbox"}>
          {(p) => (
            <select {...p} className="select" value={item} onChange={(e) => setItem(e.target.value)}>
              <option value="">Select…</option>
              {options.map((o) => <option key={o}>{o}</option>)}
            </select>
          )}
        </Field>
      )}
      {kind === "file" && (
        <Field label="Path" hint="For example /public/wp-config.php">
          {(p) => <input {...p} className="input" value={item} onChange={(e) => setItem(e.target.value)} spellCheck={false} />}
        </Field>
      )}
      {kind === "website" && (
        <>
          {/* Audit finding R8: without this option, a file that an attacker added stays after the restore. */}
          <label className="check" style={{ marginTop: 12 }}><input type="checkbox" checked={removeNewFiles} onChange={(e) => setRemoveNewFiles(e.target.checked)} />Remove files that are not in the backup</label>
          <p className="muted small">{removeNewFiles ? "The panel deletes each file of the website that the backup does not have. This cannot be undone. Use it after an attack." : "Files that were added after the backup stay."}</p>
        </>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>{LIVE ? RESTORE_NOTE[kind] : kind === "website" ? "The panel restores to a copy first and does a health check. Then it switches with no downtime." : "The panel replaces the current version. It makes a snapshot of the current version first."}</p>
    </Modal>
  );
}
