import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { ApiError, setBackupPolicy } from "../api/store";
import { Topbar } from "../components/Layout";
import { Card, Field, FormError, PageHeader } from "../components/ui";
import { BackupsPanel } from "../features/BackupsPanel";

export function BackupsPage() {
  const state = useStore();
  const isAdmin = state.session.user.role === "administrator";
  return (
    <>
      <Topbar />
      <main id="main" className="content">
        <PageHeader title="Backups" description="Each night, the panel backs up files, databases and mailboxes to object storage on a different server. You can restore a full website, one database, one mailbox or one file." />
        <div className="stack">
          {isAdmin && <PolicyCard />}
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
