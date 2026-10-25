import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { backupNow, restore, type RestoreScope } from "../api/store";
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
  const jobs = state.jobs.filter((j) => (j.title.startsWith("Back up") || j.title.startsWith("Restore")) && (!websiteId || j.websiteId === websiteId)).slice(0, 4);
  const siteName = (id: string) => state.websites.find((w) => w.id === id)?.name ?? "Removed website";

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
            {shownSite !== "all" && <button type="button" className="btn btn-primary" onClick={() => void backupNow(shownSite)}>Back up now</button>}
          </div>
        }
      >
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
        <p className="card-body muted small" style={{ paddingTop: 12 }}>
          Snapshots are encrypted with restic and kept in object storage on a different server: <code>{state.backupPolicy.target}</code>.
        </p>
      </Card>
      {restoring && <RestoreDialog snapshot={restoring} onClose={() => setRestoring(null)} />}
    </div>
  );
}

function RestoreDialog({ snapshot, onClose }: { snapshot: Snapshot; onClose: () => void }) {
  const [kind, setKind] = useState<RestoreScope["kind"]>("website");
  const [item, setItem] = useState("");
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
              const result = await action.run(snapshot.id, scope());
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
          ["website", "Full website", "Files, databases and mailboxes."],
          ["database", "One database", `${snapshot.databases.length} in this snapshot.`],
          ["mailbox", "One mailbox", `${snapshot.mailboxes.length} in this snapshot.`],
          ["file", "One file or folder", "Type the path."],
        ] as const).map(([k, label, hint]) => (
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
      <p className="muted small">{kind === "website" ? "The panel restores to a copy first and does a health check. Then it switches with no downtime." : "The panel replaces the current version. It makes a snapshot of the current version first."}</p>
    </Modal>
  );
}
