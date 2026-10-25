import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { setBackupPolicy } from "../api/store";
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

/** FR-902: the retention policy. The default keeps 7 daily, 4 weekly and 6 monthly snapshots. */
function PolicyCard() {
  const state = useStore();
  const p = state.backupPolicy;
  const [form, setForm] = useState({ daily: String(p.daily), weekly: String(p.weekly), monthly: String(p.monthly), time: p.time, target: p.target });
  const [saved, setSaved] = useState(false);
  const save = useAction(async () => {
    setBackupPolicy({ daily: Number(form.daily), weekly: Number(form.weekly), monthly: Number(form.monthly), time: form.time, target: form.target });
    setSaved(true);
  });
  const err = save.error?.fields ?? {};
  return (
    <Card title="Backup policy">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); setSaved(false); void save.run(); }}>
        <FormError error={save.error} />
        <div className="form-row">
          <Field label="Daily snapshots" error={err.daily}>{(f) => <input {...f} className="input" type="number" value={form.daily} onChange={(e) => setForm({ ...form, daily: e.target.value })} />}</Field>
          <Field label="Weekly snapshots" error={err.weekly}>{(f) => <input {...f} className="input" type="number" value={form.weekly} onChange={(e) => setForm({ ...form, weekly: e.target.value })} />}</Field>
          <Field label="Monthly snapshots" error={err.monthly}>{(f) => <input {...f} className="input" type="number" value={form.monthly} onChange={(e) => setForm({ ...form, monthly: e.target.value })} />}</Field>
          <Field label="Start time" error={err.time}>{(f) => <input {...f} className="input" type="time" value={form.time} onChange={(e) => setForm({ ...form, time: e.target.value })} />}</Field>
        </div>
        <div style={{ marginTop: 14 }}>
          <Field label="Object storage" hint="On a different server from the websites. restic encrypts each snapshot." error={err.target}>
            {(f) => <input {...f} className="input mono" value={form.target} onChange={(e) => setForm({ ...form, target: e.target.value })} spellCheck={false} />}
          </Field>
        </div>
        <div className="btn-group" style={{ marginTop: 14, alignItems: "center" }}>
          <button type="submit" className="btn btn-primary">Save policy</button>
          {saved && !save.error && <span className="text-success" role="status">Saved.</span>}
        </div>
      </form>
    </Card>
  );
}
