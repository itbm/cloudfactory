import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { ApiError, setBackupPolicy, setServerStorageKey } from "../api/store";
import type { ServerStorageKey } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Field, FormError, Modal, PageHeader, Status, TableWrap } from "../components/ui";
import { BackupsPanel } from "../features/BackupsPanel";

export function BackupsPage() {
  const state = useStore();
  const isAdmin = state.session.user.role === "administrator";
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="Backups" description="Each night, the panel backs up files and databases to object storage on a different server. You can restore a full website, one database or one file." />
        <div className="stack">
          {isAdmin && <PolicyCard />}
          {isAdmin && <ServerKeysCard />}
          <BackupsPanel />
        </div>
      </main>
    </>
  );
}

type StorageType = "s3" | "b2" | "azure" | "gs";

/**
 * The types of object storage, with the restic environment variables of each one
 * (BackupController::CREDENTIAL_NAMES). The repository of restic is "type:location".
 */
export const STORAGE: Record<StorageType, { label: string; location: string; placeholder: string; hint: string; credentials: { name: string; label: string; secret?: boolean; optional?: boolean }[] }> = {
  s3: {
    label: "Amazon S3 or S3-compatible storage",
    location: "Endpoint, bucket and path",
    placeholder: "s3.amazonaws.com/bucket/path",
    hint: "The host name of the storage service, then the bucket and an optional path. For a service that is not Amazon S3, use the host name that the service gives you.",
    credentials: [{ name: "AWS_ACCESS_KEY_ID", label: "Access key ID" }, { name: "AWS_SECRET_ACCESS_KEY", label: "Secret access key", secret: true }, { name: "AWS_DEFAULT_REGION", label: "Region", optional: true }],
  },
  b2: {
    label: "Backblaze B2",
    location: "Bucket and path",
    placeholder: "bucket:path",
    hint: "The bucket, a colon and the path in the bucket.",
    credentials: [{ name: "B2_ACCOUNT_ID", label: "Account ID" }, { name: "B2_ACCOUNT_KEY", label: "Application key", secret: true }],
  },
  azure: {
    label: "Azure Blob Storage",
    location: "Container and path",
    placeholder: "container:/path",
    hint: "The container, a colon and the path in the container.",
    credentials: [{ name: "AZURE_ACCOUNT_NAME", label: "Account name" }, { name: "AZURE_ACCOUNT_KEY", label: "Account key", secret: true }],
  },
  gs: {
    label: "Google Cloud Storage",
    location: "Bucket and path",
    placeholder: "bucket:/path",
    hint: "The bucket, a colon and the path in the bucket.",
    credentials: [{ name: "GOOGLE_PROJECT_ID", label: "Project ID" }, { name: "GOOGLE_ACCESS_TOKEN", label: "Access token", secret: true }],
  },
};

/** Divides a restic repository into its type and its location. An unknown type gives S3. */
export function parseTarget(target: string): { type: StorageType; location: string } {
  const [type, ...rest] = target.trim().split(":");
  if (rest.length > 0 && type in STORAGE) return { type: type as StorageType, location: rest.join(":") };
  return { type: "s3", location: target.trim() };
}

/** FR-902: the retention policy. The default keeps 7 daily, 4 weekly and 6 monthly snapshots. */
function PolicyCard() {
  const state = useStore();
  const p = state.backupPolicy;
  const [form, setForm] = useState({ daily: String(p.daily), weekly: String(p.weekly), monthly: String(p.monthly), time: p.time, ...parseTarget(p.target) });
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState(false);
  const storage = STORAGE[form.type];
  const fields = storage.credentials;
  const target = form.location.trim() ? `${form.type}:${form.location.trim()}` : "";
  const set = new Set(p.credentialNames ?? []);
  // The credentials in the panel are for a different type of storage: the user must type new ones.
  const otherType = set.size > 0 && !fields.some((f) => set.has(f.name));
  const save = useAction(async (remove?: boolean) => {
    const typed = Object.fromEntries(fields.filter((f) => secrets[f.name]?.trim()).map((f) => [f.name, secrets[f.name].trim()]));
    // The API replaces all credentials, so the user types each one again to change them.
    const missing = fields.filter((f) => !f.optional && !typed[f.name]);
    if (!remove && Object.keys(typed).length && missing.length) {
      throw new ApiError("Type each credential of the object storage.", Object.fromEntries(missing.map((f) => [`credentials.${f.name}`, `Type the ${f.label.toLowerCase()}.`])));
    }
    // Batch 2, finding 10: wait for the API, so that an error shows and "Saved" does not.
    await setBackupPolicy({
      daily: Number(form.daily), weekly: Number(form.weekly), monthly: Number(form.monthly), time: form.time, target,
      ...(remove ? { credentials: {} } : Object.keys(typed).length ? { credentials: typed } : {}),
    });
    setSecrets({});
    setSaved(true);
  });
  const err = save.error?.fields ?? {};
  // A full repository in the location field (for example from the documentation of restic) sets the type also.
  const setLocation = (value: string) => {
    const pasted = /^(s3|b2|azure|gs):/.test(value.trim()) ? parseTarget(value) : null;
    setForm({ ...form, ...(pasted ?? { location: value }) });
  };
  return (
    <Card title="Backup policy">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); setSaved(false); void save.run(); }}>
        <FormError error={save.error} />
        {!p.target && <div className="alert alert-warning" role="note"><p>No object storage is set, so the panel makes no backups. Select the storage, type its location and its credentials, and save the policy.</p></div>}
        <div className="form-row" style={{ marginTop: p.target ? 0 : 14 }}>
          <Field label="Daily snapshots" error={err.daily}>{(f) => <input {...f} className="input" type="number" min={0} value={form.daily} onChange={(e) => setForm({ ...form, daily: e.target.value })} />}</Field>
          <Field label="Weekly snapshots" error={err.weekly}>{(f) => <input {...f} className="input" type="number" min={0} value={form.weekly} onChange={(e) => setForm({ ...form, weekly: e.target.value })} />}</Field>
          <Field label="Monthly snapshots" error={err.monthly}>{(f) => <input {...f} className="input" type="number" min={0} value={form.monthly} onChange={(e) => setForm({ ...form, monthly: e.target.value })} />}</Field>
          <Field label="Start time (UTC)" error={err.time}>{(f) => <input {...f} className="input" type="time" value={form.time} onChange={(e) => setForm({ ...form, time: e.target.value })} />}</Field>
        </div>
        <fieldset className="form-section">
          <legend>Object storage</legend>
          <p className="hint">The backups go to object storage on a different server from the websites. restic encrypts each snapshot before it leaves the server.</p>
          <div className="form-row">
            <Field label="Storage type">
              {(f) => (
                <select {...f} className="select" value={form.type} onChange={(e) => { setForm({ ...form, type: e.target.value as StorageType }); setSecrets({}); }}>
                  {Object.entries(STORAGE).map(([id, s]) => <option key={id} value={id}>{s.label}</option>)}
                </select>
              )}
            </Field>
            <Field label={storage.location} hint={storage.hint} error={err.target}>
              {(f) => <input {...f} className="input mono" value={form.location} onChange={(e) => setLocation(e.target.value)} spellCheck={false} placeholder={storage.placeholder} />}
            </Field>
          </div>
          {target && <p className="hint">restic repository: <code>{target}</code></p>}
        </fieldset>
        <fieldset className="form-section">
          <legend>Credentials of the object storage</legend>
          <p className="hint">
            {otherType ? "The credentials in the panel are for a different type of storage. Type the credentials of this storage."
              : set.size ? <>Set: {[...set].map((n) => <code key={n} style={{ marginRight: 6 }}>{n}</code>)}. Leave the fields empty to keep them. To change them, type all of them again.</>
                : "No credentials are set. The panel keeps them encrypted and does not show them again."}
          </p>
          <div className="form-row">
            {fields.map((c) => (
              <Field key={c.name} label={`${c.label}${c.optional ? " (optional)" : ""}`} error={err[`credentials.${c.name}`]}>
                {(f) => <input {...f} className="input mono" type={c.secret ? "password" : "text"} autoComplete="off" spellCheck={false} value={secrets[c.name] ?? ""} placeholder={set.has(c.name) ? "Set" : ""} onChange={(e) => setSecrets({ ...secrets, [c.name]: e.target.value })} />}
              </Field>
            ))}
          </div>
        </fieldset>
        <div className="btn-group" style={{ marginTop: 14, alignItems: "center" }}>
          <button type="submit" className="btn btn-primary" disabled={save.busy}>Save policy</button>
          {set.size > 0 && <button type="button" className="btn" disabled={save.busy} onClick={() => { setSaved(false); void save.run(true); }}>Remove credentials</button>}
          {saved && !save.error && <span className="text-success" role="status">Saved.</span>}
        </div>
      </form>
    </Card>
  );
}

/** The restic repository with no storage type and no host: the bucket and the path that a key must have access to. */
export function storagePath(repository: string): string {
  const { type, location } = parseTarget(repository);
  // S3 has "host/bucket/path", with an optional "https://" before the host.
  return type === "s3" ? location.replace(/^https?:\/\//, "").split("/").slice(1).join("/") : location;
}

/**
 * Second audit, finding F08: each server can have its own key for the object storage. The
 * storage provider limits the key to the path of the server, and the panel sends it to that
 * server only. This card shows the servers that still use the shared key.
 */
function ServerKeysCard() {
  const p = useStore().backupPolicy;
  const [editing, setEditing] = useState<ServerStorageKey | null>(null);
  const remove = useAction((serverId: string) => setServerStorageKey(serverId, null));
  const servers = p.servers ?? [];
  if (!p.target || servers.length === 0) return null;
  const fields = STORAGE[parseTarget(p.target).type].credentials;
  const hasShared = (p.credentialNames ?? []).length > 0;
  const own = (s: ServerStorageKey) => s.credentialNames.length > 0;
  // A key from before a change of the storage type cannot open the storage of the new type.
  const otherType = (s: ServerStorageKey) => own(s) && !fields.some((f) => s.credentialNames.includes(f.name));
  const shared = servers.filter((s) => !own(s));
  return (
    <Card title="Storage key of each server">
      <div className="card-body">
        <FormError error={remove.error} />
        <p className="hint" style={{ marginTop: 0 }}>
          At the storage provider, make one key for each server. Give the key access to the path of that server only. The panel sends each key to its server only.
          A person who has root access to one server can then not delete or change the backups of the other servers.
        </p>
        {shared.length > 0 && servers.length > 1 && hasShared && (
          <div className="alert alert-warning" role="note" style={{ marginTop: 12 }}>
            <p>
              <strong>{shared.length === servers.length ? "All servers use" : shared.length === 1 ? "1 server uses" : `${shared.length} servers use`} the shared key.</strong>{" "}
              A person who has root access to one of {shared.length === 1 ? "the servers with the shared key" : "these servers"} can delete or change the backups of all servers. Set a key for each server.
            </p>
          </div>
        )}
      </div>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Server</th><th scope="col">Path of the server</th><th scope="col">Key</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {servers.map((s) => (
              <tr key={s.serverId}>
                <td><span className="cell-title mono">{s.server}</span></td>
                <td><code style={{ overflowWrap: "anywhere" }}>{s.repository ? storagePath(s.repository) : "–"}</code></td>
                <td>
                  {otherType(s) ? <Status tone="warning">Own key, for a different storage type</Status>
                    : own(s) ? <Status tone="success">Own key</Status>
                      : hasShared ? <Status tone={servers.length > 1 ? "warning" : "neutral"}>Shared key</Status>
                        : <Status tone="danger">No key</Status>}
                </td>
                <td style={{ textAlign: "right" }}>
                  <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                    <button type="button" className="btn btn-sm" onClick={() => setEditing(s)}>{own(s) ? "Change key" : "Set key"}<span className="visually-hidden"> of {s.server}</span></button>
                    {own(s) && (
                      <ConfirmButton
                        className="btn btn-sm"
                        title={`Remove the key of ${s.server}?`}
                        message={<p>{hasShared ? `The server ${s.server} then uses the shared key, which has access to the backups of all servers.` : `No shared key is set, so the backups of the websites on ${s.server} fail until you set a key.`} Delete the key at the storage provider also.</p>}
                        confirm="Remove key"
                        onConfirm={() => void remove.run(s.serverId)}
                        disabled={remove.busy}
                      >
                        Remove key<span className="visually-hidden"> of {s.server}</span>
                      </ConfirmButton>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {editing && <ServerKeyDialog server={editing} onClose={() => setEditing(null)} />}
    </Card>
  );
}

function ServerKeyDialog({ server, onClose }: { server: ServerStorageKey; onClose: () => void }) {
  const p = useStore().backupPolicy;
  const fields = STORAGE[parseTarget(p.target).type].credentials;
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const save = useAction(async () => {
    const typed = Object.fromEntries(fields.filter((f) => secrets[f.name]?.trim()).map((f) => [f.name, secrets[f.name].trim()]));
    const missing = fields.filter((f) => !f.optional && !typed[f.name]);
    if (missing.length) {
      throw new ApiError("Type each credential of the key.", Object.fromEntries(missing.map((f) => [`credentials.${f.name}`, `Type the ${f.label.toLowerCase()}.`])));
    }
    await setServerStorageKey(server.serverId, typed);
  });
  const err = save.error?.fields ?? {};
  return (
    <Modal
      title={`Storage key of ${server.server}`}
      onClose={onClose}
      wide
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="server-key" className="btn btn-primary" disabled={save.busy}>Save key</button></>}
    >
      <form id="server-key" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await save.run()).ok) onClose(); }}>
        <FormError error={save.error} shown={fields.map((f) => `credentials.${f.name}`)} />
        <p>Make a key at the storage provider that can read, write, list and delete only below this path:</p>
        <p style={{ marginTop: 8 }}><code style={{ overflowWrap: "anywhere" }}>{server.repository ? storagePath(server.repository) : ""}</code></p>
        <p className="hint">
          The panel keeps the key encrypted, does not show it again, and sends it to {server.server} only.
          {server.credentialNames.length > 0 && " The new key replaces the key that the server has."}
        </p>
        <div className="form-row" style={{ marginTop: 14 }}>
          {fields.map((c) => (
            <Field key={c.name} label={`${c.label}${c.optional ? " (optional)" : ""}`} error={err[`credentials.${c.name}`]}>
              {(f) => <input {...f} className="input mono" type={c.secret ? "password" : "text"} autoComplete="off" spellCheck={false} value={secrets[c.name] ?? ""} onChange={(e) => setSecrets({ ...secrets, [c.name]: e.target.value })} />}
            </Field>
          ))}
        </div>
      </form>
    </Modal>
  );
}
