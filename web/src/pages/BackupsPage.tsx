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

/** The restic environment variables of each object storage (BackupController::CREDENTIAL_NAMES). */
const CREDENTIALS: Record<string, { name: string; label: string; secret?: boolean; optional?: boolean }[]> = {
  s3: [{ name: "AWS_ACCESS_KEY_ID", label: "Access key ID" }, { name: "AWS_SECRET_ACCESS_KEY", label: "Secret access key", secret: true }, { name: "AWS_DEFAULT_REGION", label: "Region", optional: true }],
  b2: [{ name: "B2_ACCOUNT_ID", label: "Account ID" }, { name: "B2_ACCOUNT_KEY", label: "Application key", secret: true }],
  azure: [{ name: "AZURE_ACCOUNT_NAME", label: "Account name" }, { name: "AZURE_ACCOUNT_KEY", label: "Account key", secret: true }],
  gs: [{ name: "GOOGLE_PROJECT_ID", label: "Project ID" }, { name: "GOOGLE_ACCESS_TOKEN", label: "Access token", secret: true }],
};

/** FR-902: the retention policy. The default keeps 7 daily, 4 weekly and 6 monthly snapshots. */
function PolicyCard() {
  const state = useStore();
  const p = state.backupPolicy;
  const [form, setForm] = useState({ daily: String(p.daily), weekly: String(p.weekly), monthly: String(p.monthly), time: p.time, target: p.target });
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState(false);
  const provider = form.target.split(":")[0];
  const fields = CREDENTIALS[provider] ?? [];
  const set = new Set(p.credentialNames ?? []);
  const save = useAction(async (remove?: boolean) => {
    const typed = Object.fromEntries(fields.filter((f) => secrets[f.name]?.trim()).map((f) => [f.name, secrets[f.name].trim()]));
    // The API replaces all credentials, so the user types each one again to change them.
    const missing = fields.filter((f) => !f.optional && !typed[f.name]);
    if (!remove && Object.keys(typed).length && missing.length) {
      throw new ApiError("Type each credential of the object storage.", Object.fromEntries(missing.map((f) => [`credentials.${f.name}`, `Type the ${f.label.toLowerCase()}.`])));
    }
    // Batch 2, finding 10: wait for the API, so that an error shows and "Saved" does not.
    await setBackupPolicy({
      daily: Number(form.daily), weekly: Number(form.weekly), monthly: Number(form.monthly), time: form.time, target: form.target.trim(),
      ...(remove ? { credentials: {} } : Object.keys(typed).length ? { credentials: typed } : {}),
    });
    setSecrets({});
    setSaved(true);
  });
  const err = save.error?.fields ?? {};
  return (
    <Card title="Backup policy">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); setSaved(false); void save.run(); }}>
        <FormError error={save.error} />
        <div className="form-row">
          <Field label="Daily snapshots" error={err.daily}>{(f) => <input {...f} className="input" type="number" min={0} value={form.daily} onChange={(e) => setForm({ ...form, daily: e.target.value })} />}</Field>
          <Field label="Weekly snapshots" error={err.weekly}>{(f) => <input {...f} className="input" type="number" min={0} value={form.weekly} onChange={(e) => setForm({ ...form, weekly: e.target.value })} />}</Field>
          <Field label="Monthly snapshots" error={err.monthly}>{(f) => <input {...f} className="input" type="number" min={0} value={form.monthly} onChange={(e) => setForm({ ...form, monthly: e.target.value })} />}</Field>
          <Field label="Start time (UTC)" error={err.time}>{(f) => <input {...f} className="input" type="time" value={form.time} onChange={(e) => setForm({ ...form, time: e.target.value })} />}</Field>
        </div>
        <div style={{ marginTop: 14 }}>
          <Field label="Object storage" hint={<>A restic repository on a different server from the websites, for example <code>s3:s3.amazonaws.com/bucket/path</code> or <code>b2:bucket:path</code>. restic encrypts each snapshot.</>} error={err.target}>
            {(f) => <input {...f} className="input mono" value={form.target} onChange={(e) => setForm({ ...form, target: e.target.value })} spellCheck={false} placeholder="s3:s3.amazonaws.com/bucket/path" />}
          </Field>
        </div>
        {fields.length > 0 && (
          <fieldset style={{ marginTop: 14 }}>
            <legend>Credentials of the object storage</legend>
            <p className="hint">
              {set.size ? <>Set: {[...set].map((n) => <code key={n} style={{ marginRight: 6 }}>{n}</code>)}. Leave the fields empty to keep them. To change them, type all of them again.</> : "No credentials are set. The panel keeps them encrypted and does not show them again."}
            </p>
            <div className="form-row">
              {fields.map((c) => (
                <Field key={c.name} label={`${c.label}${c.optional ? " (optional)" : ""}`} error={err[`credentials.${c.name}`]}>
                  {(f) => <input {...f} className="input mono" type={c.secret ? "password" : "text"} autoComplete="off" spellCheck={false} value={secrets[c.name] ?? ""} placeholder={set.has(c.name) ? "Set" : ""} onChange={(e) => setSecrets({ ...secrets, [c.name]: e.target.value })} />}
                </Field>
              ))}
            </div>
          </fieldset>
        )}
        <div className="btn-group" style={{ marginTop: 14, alignItems: "center" }}>
          <button type="submit" className="btn btn-primary" disabled={save.busy}>Save policy</button>
          {set.size > 0 && <button type="button" className="btn" disabled={save.busy} onClick={() => { setSaved(false); void save.run(true); }}>Remove credentials</button>}
          {saved && !save.error && <span className="text-success" role="status">Saved.</span>}
        </div>
      </form>
    </Card>
  );
}
