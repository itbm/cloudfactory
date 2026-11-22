import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAction, useLoad, useStore } from "../api/hooks";
import {
  createImport, readImport, readImports, removeImport, startImport, uploadArchive,
  type Import, type ImportDetail, type ImportItem, type ImportItemKind, type ImportItemState, type ImportPlan, type ImportStatus,
} from "../api/imports";
import { ApiError } from "../api/store";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Empty, Field, FormError, Meter, Modal, PageHeader, Status, TableWrap, type Tone } from "../components/ui";
import { bytes, count, relativeTime } from "../lib/format";

const STATUS: Record<ImportStatus, [string, Tone]> = {
  uploading: ["Uploading", "info"],
  fetching: ["Reading the archive", "info"],
  ready: ["Ready to import", "warning"],
  starting: ["Starting", "info"],
  running: ["Importing", "info"],
  done: ["Complete", "success"],
  failed: ["Failed", "danger"],
};

const ITEM_STATE: Record<ImportItemState, [string, Tone]> = {
  waiting: ["Waiting", "neutral"],
  running: ["Running", "info"],
  done: ["Done", "success"],
  failed: ["Failed", "danger"],
  skipped: ["Skipped", "warning"],
};

const KIND: Record<ImportItemKind, string> = {
  website: "Website", domain: "Domain", database: "Database", databaseUsers: "Database users", cronJob: "Cron job", files: "Files",
  dump: "Database content", mailDomain: "Mail domain", mailbox: "Mailbox", mailAlias: "Address", messages: "Messages", dnsZone: "DNS zone",
};

/** The states in which the page asks the panel again every few seconds. */
const MOVING: ImportStatus[] = ["fetching", "starting", "running"];

function ImportStatusLabel({ item }: { item: Import }) {
  const [text, tone] = item.status === "done" && item.progress.failed > 0 ? ["Complete with problems", "warning" as Tone] : STATUS[item.status];
  return <Status tone={tone}>{text}</Status>;
}

const plural = (n: number, one: string, many = `${one}s`) => `${count(n)} ${n === 1 ? one : many}`;

/**
 * The import of an account from cPanel, DirectAdmin or Plesk (docs/imports.md). The web server reads
 * the backup archive, and the page shows the plan. The administrator selects the parts, and the
 * panel makes the websites, the databases, the mail and the DNS zones. Then the servers copy
 * the files, the dumps and the messages.
 */
export function ImportsPage() {
  const { id } = useParams();
  return id ? <ImportPage id={id} /> : <ImportList />;
}

function ImportList() {
  const data = useLoad(() => readImports(), []);
  const [adding, setAdding] = useState(false);
  const navigate = useNavigate();

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Imports"
          description="Move an account from cPanel, DirectAdmin or Plesk to this panel: its websites with their files and databases, its mail with the messages, and its DNS zones."
          actions={<button type="button" className="btn btn-primary" onClick={() => setAdding(true)}>Import account</button>}
        />
        <FormError error={data.error} />
        {data.loading && !data.data && <p className="muted">Loading the imports…</p>}
        {data.data && data.data.length === 0 && (
          <Card title="Imports">
            <div className="card-body">
              <Empty title="No imports">Make a full backup of the account on the old panel. In cPanel, use <strong>Backup</strong>, then <strong>Download a Full Account Backup</strong>. In DirectAdmin, use <strong>Create/Restore Backups</strong>. In Plesk, make a backup of the subscription in <strong>Backup Manager</strong>, then download it. Then select <strong>Import account</strong>.</Empty>
            </div>
          </Card>
        )}
        {data.data && data.data.length > 0 && (
          <Card title="Imports">
            <TableWrap>
              <table>
                <thead><tr><th scope="col">Archive</th><th scope="col">Customer</th><th scope="col">Server</th><th scope="col">Status</th><th scope="col">Parts</th><th scope="col">Started</th></tr></thead>
                <tbody>
                  {data.data.map((i) => (
                    <tr key={i.id}>
                      <td>
                        <Link to={`/imports/${i.id}`}><strong>{i.account ?? i.fileName ?? "Archive"}</strong></Link>
                        <span className="cell-sub">{[i.panelName, i.fileName].filter(Boolean).join(" · ")}</span>
                      </td>
                      <td>{i.customerName}</td>
                      <td>{i.serverName}</td>
                      <td><ImportStatusLabel item={i} /></td>
                      <td>{i.progress.total > 0 ? `${i.progress.done} of ${i.progress.total} done` : i.counts ? plural(i.counts.websites, "website") : "—"}</td>
                      <td>{relativeTime(i.createdAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          </Card>
        )}
        {adding && <NewImportDialog onClose={() => setAdding(false)} onCreated={(id) => { setAdding(false); navigate(`/imports/${id}`); }} />}
      </main>
    </>
  );
}

function NewImportDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const state = useStore();
  const customers = state.customers.filter((c) => !c.suspended);
  const servers = state.servers.filter((s) => s.roles.includes("web"));
  const [customerId, setCustomerId] = useState(customers[0]?.id ?? "");
  const [serverId, setServerId] = useState(servers[0]?.id ?? "");
  const [source, setSource] = useState<"upload" | "url">("upload");
  const [file, setFile] = useState<File | null>(null);
  const [url, setUrl] = useState("");
  const [sent, setSent] = useState<number | null>(null);
  const closed = useRef(false);
  useEffect(() => () => { closed.current = true; }, []);

  const create = useAction(async () => {
    if (source === "upload" && !file) throw new ApiError("Select the backup archive.", { fileName: "Select the backup archive." });
    const made = await createImport(source === "url" ? { customerId, serverId, url } : { customerId, serverId, fileName: file!.name, sizeBytes: file!.size });
    if (source === "upload") {
      setSent(0);
      await uploadArchive(made.id, file!, 0, setSent, () => closed.current);
    }
    if (!closed.current) onCreated(made.id);
  });
  const err = create.error?.fields ?? {};
  const uploading = sent !== null && file !== null;

  return (
    <Modal
      title="Import an account"
      onClose={onClose}
      footer={<>
        <button type="button" className="btn" onClick={onClose}>{uploading ? "Stop the upload" : "Cancel"}</button>
        <button type="button" className="btn btn-primary" disabled={create.busy || !customerId || !serverId} onClick={() => void create.run()}>{source === "upload" ? "Upload and read" : "Read the archive"}</button>
      </>}
    >
      <FormError error={create.error} shown={["customerId", "serverId", "url", "fileName", "sizeBytes"]} />
      <Field label="Customer" hint="The customer gets the websites, the mail domains and the DNS zones of the account." error={err.customerId}>
        {(p) => <select {...p} className="select" value={customerId} onChange={(e) => setCustomerId(e.target.value)}>{customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select>}
      </Field>
      <Field label="Web server" hint="The server of the new websites. It downloads and reads the archive." error={err.serverId}>
        {(p) => <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>{servers.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select>}
      </Field>
      <fieldset className="radio-cards" style={{ marginBottom: 14 }}>
        <legend>Backup archive</legend>
        <label className="radio-card">
          <input type="radio" name="source" checked={source === "upload"} onChange={() => setSource("upload")} />
          <span><strong>Upload a file</strong><span>The browser sends the file to the panel in parts.</span></span>
        </label>
        <label className="radio-card">
          <input type="radio" name="source" checked={source === "url"} onChange={() => setSource("url")} />
          <span><strong>Download from a URL</strong><span>Better for a large archive, for example a link to object storage.</span></span>
        </label>
      </fieldset>
      {source === "upload" ? (
        <Field label="File" hint="A cPanel backup (cpmove-USER.tar.gz or backup-…_USER.tar.gz), a DirectAdmin backup (user.ADMIN.USER.tar.gz or .tar.zst) or a Plesk backup (backup_….tar)." error={err.fileName ?? err.sizeBytes}>
          {(p) => <input {...p} type="file" accept=".gz,.tgz,.zst,.bz2,.tar" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />}
        </Field>
      ) : (
        <Field label="URL" hint="An HTTPS address with no user name and no password. Each server that needs the archive downloads it from there." error={err.url}>
          {(p) => <input {...p} className="input" type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" autoCapitalize="none" spellCheck={false} />}
        </Field>
      )}
      {uploading && (
        <div role="status">
          <Meter value={file.size ? sent / file.size : 1} label="Upload progress" />
          <p className="muted small">{bytes(sent)} of {bytes(file.size)} sent. Keep this window open until the upload is complete.</p>
        </div>
      )}
    </Modal>
  );
}

function ImportPage({ id }: { id: string }) {
  const data = useLoad(() => readImport(id), [id]);
  const navigate = useNavigate();
  const status = data.data?.status;
  useEffect(() => {
    if (!status || !MOVING.includes(status)) return;
    const t = setInterval(() => { if (document.visibilityState === "visible") data.reload(); }, 3000);
    return () => clearInterval(t);
  }, [status, data.reload]);
  const remove = useAction(async () => { await removeImport(id); navigate("/imports"); });
  const i = data.data;

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <p style={{ margin: "0 0 8px" }}><Link to="/imports">All imports</Link></p>
        {data.loading && !i && <p className="muted">Loading the import…</p>}
        <FormError error={(!i && data.error) || remove.error} />
        {i && (
          <>
            <PageHeader
              title={i.account ?? i.fileName ?? "Import"}
              description={<><ImportStatusLabel item={i} /> {`${i.panelName ? `${i.panelName} account` : "Account"} for ${i.customerName ?? "the customer"} on ${i.serverName ?? "the server"}.`}</>}
              actions={!["starting", "running"].includes(i.status) && (
                <ConfirmButton title="Remove the import?" confirm="Remove import" onConfirm={() => void remove.run()}
                  message="The panel and the servers remove the archive. The websites, the mail and the DNS zones of the import stay.">Remove import</ConfirmButton>
              )}
            />
            <Summary item={i} />
            {i.status === "uploading" && <ContinueUpload item={i} onDone={data.reload} />}
            {i.status === "fetching" && <p className="muted" role="status">The server {i.serverName} downloads the archive and reads it. This can take some minutes for a large archive.</p>}
            {i.status === "failed" && (
              <div className="alert alert-danger alert-stack" role="alert">
                <p><strong>The import failed.</strong></p>
                {i.error && <p className="cause">{i.error}</p>}
                <p>Check that the file is a full account backup of cPanel, DirectAdmin or Plesk, then start a new import.</p>
              </div>
            )}
            {i.status === "ready" && i.plan && <PlanForm item={i} plan={i.plan} onStarted={data.set} />}
            {i.status !== "ready" && <Warnings item={i} />}
            {i.items.length > 0 && <Items item={i} />}
          </>
        )}
      </main>
    </>
  );
}

/** The parts of the account that the import does not copy. */
function Warnings({ item: i }: { item: ImportDetail }) {
  if (i.warnings.length === 0) return null;
  return (
    <Card title="Not in the import">
      <div className="card-body">
        <ul style={{ margin: 0, paddingLeft: 18 }}>{i.warnings.map((w, n) => <li key={n}>{w.message}</li>)}</ul>
      </div>
    </Card>
  );
}

function Summary({ item: i }: { item: ImportDetail }) {
  return (
    <Card>
      <div className="card-body">
        <table className="kv">
          <tbody>
            <tr><th scope="row">Archive</th><td>{i.fileName ?? "From a URL"}<span className="cell-sub">{bytes(i.sizeBytes)}{i.source === "url" ? ", downloaded from a URL" : ", uploaded to the panel"}</span></td></tr>
            {i.customer && (i.customer.name || i.customer.email) && <tr><th scope="row">In the backup</th><td>{[i.customer.name, i.customer.email].filter(Boolean).join(", ")}</td></tr>}
            {i.counts && <tr><th scope="row">Account</th><td>{[plural(i.counts.websites, "website"), plural(i.counts.databases, "database"), plural(i.counts.mailboxes, "mailbox", "mailboxes"), plural(i.counts.dnsZones, "DNS zone")].join(", ")}</td></tr>}
            <tr><th scope="row">Created</th><td>{relativeTime(i.createdAt)}{i.finishedAt && <span className="cell-sub">Ended {relativeTime(i.finishedAt)}</span>}</td></tr>
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/** After a reload of the page, the browser does not have the file. The user selects it again. */
function ContinueUpload({ item: i, onDone }: { item: ImportDetail; onDone: () => void }) {
  const [sent, setSent] = useState(i.receivedBytes);
  const upload = useAction(async (file: File) => {
    if (file.size !== i.sizeBytes) throw new ApiError(`Select the file ${i.fileName}. It has ${bytes(i.sizeBytes)}.`);
    await uploadArchive(i.id, file, i.receivedBytes, setSent);
    onDone();
  });
  return (
    <Card title="Upload">
      <div className="card-body">
        <FormError error={upload.error} />
        <Meter value={i.sizeBytes ? sent / i.sizeBytes : 0} label="Upload progress" />
        <p className="muted small">{bytes(sent)} of {bytes(i.sizeBytes)} sent.</p>
        {!upload.busy && (
          <Field label="Select the same file to continue" hint={`The upload continues at ${bytes(i.receivedBytes)}.`}>
            {(p) => <input {...p} type="file" onChange={(e) => { const f = e.target.files?.[0]; if (f) void upload.run(f); }} />}
          </Field>
        )}
      </div>
    </Card>
  );
}

function PlanForm({ item: i, plan, onStarted }: { item: ImportDetail; plan: ImportPlan; onStarted: (d: ImportDetail) => void }) {
  const [websites, setWebsites] = useState(() => new Set(plan.websites.filter((w) => !w.conflict).map((w) => w.name)));
  const [images, setImages] = useState<Record<string, string>>(() => Object.fromEntries(plan.websites.map((w) => [w.name, w.imageId ?? ""])));
  const [mail, setMail] = useState(() => new Set(plan.mailDomains.filter((m) => !m.conflict).map((m) => m.name)));
  const [zones, setZones] = useState(() => new Set(plan.dnsZones.filter((z) => !z.conflict).map((z) => z.name)));
  const [rewrite, setRewrite] = useState(true);
  const toggle = (set: Set<string>, change: (s: Set<string>) => void, name: string, on: boolean) => {
    const next = new Set(set);
    if (on) next.add(name); else next.delete(name);
    change(next);
  };
  const start = useAction(async () => onStarted(await startImport(i.id, {
    websites: plan.websites.filter((w) => websites.has(w.name)).map((w) => ({ name: w.name, imageId: images[w.name] || null })),
    mailDomains: [...mail], dnsZones: [...zones], rewriteWordPress: rewrite,
  })));
  const selected = websites.size + mail.size + zones.size;
  const chosenMail = plan.mailDomains.filter((m) => mail.has(m.name));
  const needs = { mailboxes: chosenMail.reduce((n, m) => n + m.mailboxes, 0), bytes: chosenMail.reduce((n, m) => n + m.quotaBytes, 0) };
  const tooSmall = plan.mailSpace && (needs.mailboxes > plan.mailSpace.mailboxes || needs.bytes > plan.mailSpace.bytes);
  const wordpress = plan.websites.some((w) => w.wordpress && websites.has(w.name));
  const summary = useMemo(() => [
    websites.size && plural(websites.size, "website"), mail.size && plural(mail.size, "mail domain"), zones.size && plural(zones.size, "DNS zone"),
  ].filter(Boolean).join(", "), [websites, mail, zones]);

  return (
    <>
      {plan.websites.length > 0 && (
        <Card title="Websites">
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Website</th><th scope="col">Image</th><th scope="col">Files</th><th scope="col">Databases</th><th scope="col">Notes</th></tr></thead>
              <tbody>
                {plan.websites.map((w) => (
                  <tr key={w.name}>
                    <td>
                      <label className="check">
                        <input type="checkbox" checked={websites.has(w.name)} disabled={!!w.conflict} onChange={(e) => toggle(websites, setWebsites, w.name, e.target.checked)} />
                        <strong>{w.name}</strong>
                      </label>
                      {w.domains.length > 1 && <span className="cell-sub">{w.domains.filter((d) => d.name !== w.name).map((d) => `${d.name}${d.kind === "redirect" ? " (redirect)" : ""}`).join(", ")}</span>}
                    </td>
                    <td>
                      <select className="select" aria-label={`Image for ${w.name}`} value={images[w.name]} disabled={!websites.has(w.name)} onChange={(e) => setImages({ ...images, [w.name]: e.target.value })}>
                        {plan.images.map((img) => <option key={img.id} value={img.id}>{img.name}</option>)}
                      </select>
                      <span className="cell-sub">{w.phpVersion ? `PHP ${w.phpVersion} on the old panel` : "No PHP version in the backup"}</span>
                    </td>
                    <td>{w.files ? bytes(w.diskBytes) : "No files"}</td>
                    <td>{w.databases.length === 0 ? "None" : w.databases.map((d) => <span key={d.name} className="cell-sub mono">{d.name} · {bytes(d.bytes)}</span>)}</td>
                    <td>
                      {w.conflict && <Status tone="danger">{w.conflict}</Status>}
                      {w.wordpress && <span className="cell-sub">WordPress</span>}
                      {w.cronJobs > 0 && <span className="cell-sub">{plural(w.cronJobs, "cron job")}, off until you turn {w.cronJobs === 1 ? "it" : "them"} on</span>}
                      {w.databaseUsers > w.databases.length && <span className="cell-sub">Each database gets one user with a new password</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </Card>
      )}
      {plan.mailDomains.length > 0 && (
        <Card title="Email">
          {tooSmall && plan.mailSpace && (
            <div className="card-body" style={{ paddingBottom: 0 }}>
              <div className="alert alert-warning" role="status">
                <p>The mailboxes need {plural(needs.mailboxes, "mailbox", "mailboxes")} and {bytes(needs.bytes)}. The plan of {i.customerName} has {plural(plan.mailSpace.mailboxes, "mailbox", "mailboxes")} and {bytes(plan.mailSpace.bytes)} free. The mailboxes that do not fit fail. Give the customer a larger plan first.</p>
              </div>
            </div>
          )}
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Mail domain</th><th scope="col">Mailboxes</th><th scope="col">Addresses</th><th scope="col">Messages</th><th scope="col">Notes</th></tr></thead>
              <tbody>
                {plan.mailDomains.map((m) => (
                  <tr key={m.name}>
                    <td><label className="check"><input type="checkbox" checked={mail.has(m.name)} disabled={!!m.conflict} onChange={(e) => toggle(mail, setMail, m.name, e.target.checked)} /><strong>{m.name}</strong></label></td>
                    <td>{count(m.mailboxes)}<span className="cell-sub">{bytes(m.quotaBytes)} in all</span></td>
                    <td>{count(m.aliases)}</td>
                    <td>{count(m.messages)}<span className="cell-sub">{bytes(m.bytes)}</span></td>
                    <td>
                      {m.conflict && <Status tone="danger">{m.conflict}</Status>}
                      {m.newPasswords > 0 && <span className="cell-sub">{m.newPasswords === 1 ? "1 mailbox gets" : `${m.newPasswords} mailboxes get`} a new password</span>}
                      {m.newPasswords < m.mailboxes && <span className="cell-sub">The other mailboxes keep their passwords</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </Card>
      )}
      {plan.dnsZones.length > 0 && (
        <Card title="DNS zones">
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Zone</th><th scope="col">Notes</th></tr></thead>
              <tbody>
                {plan.dnsZones.map((z) => (
                  <tr key={z.name}>
                    <td><label className="check"><input type="checkbox" checked={zones.has(z.name)} disabled={!!z.conflict} onChange={(e) => toggle(zones, setZones, z.name, e.target.checked)} /><strong>{z.name}</strong></label></td>
                    <td>{z.conflict ? <Status tone="danger">{z.conflict}</Status> : z.exists ? "The customer has this zone. The import adds the records that it does not have." : "A new zone. The websites keep the addresses of the new server."}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </Card>
      )}
      <Warnings item={i} />
      <Card>
        <div className="card-body">
          <FormError error={start.error} />
          {wordpress && <label className="check"><input type="checkbox" checked={rewrite} onChange={(e) => setRewrite(e.target.checked)} />Put the settings of the new databases in wp-config.php</label>}
          <p className="muted">{selected === 0 ? "Select one or more parts." : `The import makes ${summary} for ${i.customerName} on ${i.serverName}. The websites stay on the old panel: change the DNS when you are ready.`}</p>
          <div className="btn-group" style={{ marginTop: 12 }}>
            <button type="button" className="btn btn-primary" disabled={start.busy || selected === 0} onClick={() => void start.run()}>Start import</button>
          </div>
        </div>
      </Card>
    </>
  );
}

function Items({ item: i }: { item: ImportDetail }) {
  const ended = i.progress.done + i.progress.failed;
  return (
    <Card title="Parts" actions={i.progress.total > 0 && <span className="muted small">{ended} of {i.progress.total} ended</span>}>
      <div className="card-body" style={{ paddingBottom: 0 }}>
        <Meter value={i.progress.total ? ended / i.progress.total : 0} label="Import progress" tone={i.progress.failed > 0 ? "warning" : undefined} />
      </div>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Part</th><th scope="col">Name</th><th scope="col">State</th><th scope="col">Result</th></tr></thead>
          <tbody>{i.items.map((item) => <ItemRow key={item.id} item={item} />)}</tbody>
        </table>
      </TableWrap>
    </Card>
  );
}

function ItemRow({ item }: { item: ImportItem }) {
  const [text, tone] = ITEM_STATE[item.state];
  return (
    <tr>
      <td>{KIND[item.kind] ?? item.kind}</td>
      <td><span className="mono">{item.name}</span></td>
      <td><Status tone={tone}>{text}</Status></td>
      <td>{item.error ?? item.detail ?? ""}</td>
    </tr>
  );
}
