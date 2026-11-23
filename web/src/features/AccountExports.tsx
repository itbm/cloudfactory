import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { downloadExport, removeExport, startExport } from "../api/store";
import type { AccountExport } from "../api/types";
import { Card, ConfirmButton, FormError, Modal, Status, TableWrap } from "../components/ui";
import { bytes, clockTime, longDate, relativeTime, shortDate } from "../lib/format";

/**
 * Account exports (docs/account-export.md): an archive of an account that the user downloads.
 * The archive has the description of the account (account.json), the files of the websites,
 * a dump of each MariaDB and PostgreSQL database, and the DNS zones. Another Cloudfactory panel
 * can import it.
 */
export function AccountExportsPanel({ customerId, customerName, inCard = true }: { customerId: string; customerName: string; inCard?: boolean }) {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const download = useAction(downloadExport);
  const remove = useAction(removeExport);
  const exports = state.exports.filter((e) => e.customerId === customerId);
  const running = exports.some((e) => e.state === "running");
  const start = (
    <button type="button" className="btn btn-primary" disabled={running} onClick={() => setOpen(true)} title={running ? "An export of this account is in progress." : undefined}>
      Export account
    </button>
  );
  const body = (
    <>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted">
          Download an archive of {customerName}: the websites with their files and settings, the databases, the DNS zones and the email addresses.
          You can keep the archive as a copy, or import it into another Cloudfactory panel. The panel keeps each archive for 7 days.
        </p>
      </div>
      {(download.error || remove.error) && <div className="card-body"><FormError error={download.error ?? remove.error} /></div>}
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Started</th><th scope="col">Contents</th><th scope="col">Status</th><th scope="col">Size</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {exports.map((e) => (
              <tr key={e.id}>
                <td style={{ whiteSpace: "nowrap" }}>{shortDate(e.createdAt)} {clockTime(e.createdAt)}{e.createdBy && <span className="cell-sub">{e.createdBy}</span>}</td>
                <td className="small muted">{contents(e)}</td>
                <td><ExportState export={e} /></td>
                <td style={{ whiteSpace: "nowrap" }}>{e.sizeBytes !== null ? bytes(e.sizeBytes) : e.state === "running" ? bytes(e.receivedBytes) : "–"}</td>
                <td className="cell-actions" style={{ width: "1%", whiteSpace: "nowrap" }}>
                  <div className="btn-group" style={{ justifyContent: "flex-end", flexWrap: "nowrap" }}>
                    {e.state === "ready" && <button type="button" className="btn btn-sm btn-primary" disabled={download.busy} onClick={() => void download.run(e.id)}>Download</button>}
                    <ConfirmButton
                      className="btn btn-sm"
                      title={e.state === "running" ? "Stop this export?" : "Remove this export?"}
                      confirm={e.state === "running" ? "Stop export" : "Remove"}
                      message={<p>{e.state === "running" ? "The servers stop the export, and the panel removes the parts that it has." : "The panel removes the archive. The websites, the databases and the other data of the account do not change."}</p>}
                      onConfirm={() => remove.run(e.id)}
                    >
                      {e.state === "running" ? "Stop" : "Remove"}
                    </ConfirmButton>
                  </div>
                </td>
              </tr>
            ))}
            {exports.length === 0 && <tr><td colSpan={5} className="muted">No exports. Select Export account to make an archive.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      <p className="card-body muted small" style={{ paddingTop: 12 }}>
        The archive has the environment variables of the websites, which often have passwords and keys. Keep it safe. It does not have the email messages, the passwords of the mailboxes or the data of Valkey databases.
      </p>
      {open && <StartExportDialog customerId={customerId} customerName={customerName} onClose={() => setOpen(false)} />}
    </>
  );
  if (!inCard) {
    return (
      <>
        <div className="card-body" style={{ display: "flex", justifyContent: "flex-end", paddingBottom: 0 }}>{start}</div>
        {body}
      </>
    );
  }
  return <Card title="Account export" actions={start}>{body}</Card>;
}

/** "Settings, files and databases". The settings are in each archive. */
function contents(e: AccountExport): string {
  const items = ["Settings", ...(e.files ? ["files"] : []), ...(e.databases ? ["databases"] : [])];
  return items.length === 1 ? "Settings only" : `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

function ExportState({ export: e }: { export: AccountExport }) {
  if (e.state === "running") {
    return (
      <>
        <Status tone="info">In progress</Status>
        <span className="cell-sub">{e.parts.total > 0 ? `${e.parts.complete} of ${e.parts.total} parts complete` : "The panel makes the archive."}</span>
      </>
    );
  }
  if (e.state === "failed") {
    return (
      <>
        <Status tone="danger">Failed</Status>
        {e.error && <span className="cell-sub" style={{ overflowWrap: "anywhere" }}>{e.error}</span>}
      </>
    );
  }
  return (
    <>
      <Status tone="success">Ready</Status>
      {e.expiresAt && <span className="cell-sub" style={{ whiteSpace: "nowrap" }} title={`Available until ${longDate(e.expiresAt)}`}>Removed {relativeTime(e.expiresAt)}</span>}
    </>
  );
}

function StartExportDialog({ customerId, customerName, onClose }: { customerId: string; customerName: string; onClose: () => void }) {
  const state = useStore();
  const [files, setFiles] = useState(true);
  const [databases, setDatabases] = useState(true);
  const start = useAction(startExport);
  const own = state.account?.customer.id === customerId;
  const websites = state.websites.filter((w) => w.customerId === customerId);
  const diskBytes = websites.reduce((sum, w) => sum + (w.use?.diskBytes ?? 0), 0);
  return (
    <Modal
      title={`Export ${customerName}`}
      onClose={onClose}
      footer={<>
        <button type="button" className="btn" onClick={onClose}>Cancel</button>
        <button type="button" className="btn btn-primary" disabled={start.busy} onClick={async () => {
          const r = await start.run({ customerId: own ? undefined : customerId, files, databases });
          if (r.ok) onClose();
        }}>Start export</button>
      </>}
    >
      <FormError error={start.error} />
      <p>The archive always has the settings of the account: the websites, the domains, the environment variables, the cron jobs, the database users, the DNS zones and the email addresses.</p>
      <fieldset style={{ marginTop: 14, border: 0, padding: 0 }}>
        <legend className="small muted">Also include</legend>
        <label className="check"><input type="checkbox" checked={files} onChange={(e) => setFiles(e.target.checked)} />The files of the websites{diskBytes > 0 ? ` (approximately ${bytes(diskBytes)} before compression)` : ""}</label>
        <label className="check"><input type="checkbox" checked={databases} onChange={(e) => setDatabases(e.target.checked)} />A dump of each MariaDB and PostgreSQL database</label>
      </fieldset>
      <p className="muted small" style={{ marginTop: 12 }}>
        The servers send the files and the dumps to the panel. A large account can take some minutes. The websites stay online during the export. You can close this page: the list shows when the archive is ready.
      </p>
    </Modal>
  );
}
