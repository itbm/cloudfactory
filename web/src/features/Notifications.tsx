import { useEffect, useRef, useState } from "react";
import { useAction, useLoad, useStore } from "../api/hooks";
import {
  previewTemplate, readDeliveries, readDelivery, readMailServer, readPreferences, readTemplates, removeMailServer, resetTemplate, retryDelivery,
  saveMailServer, saveTemplate, sendTestEmail, setPreferences,
} from "../api/notifications";
import type { DeliveryState, MailEncryption, MailServer, MailServerInput, NotificationDelivery, NotificationEvent, NotificationPreference, NotificationTemplate, TemplatePreview } from "../api/notifications";
import { Card, ConfirmButton, Empty, Field, FormError, Modal, Status, TableWrap, Toggle } from "../components/ui";
import type { Tone } from "../api/types";
import { longDate, relativeTime } from "../lib/format";

const STATE: Record<DeliveryState, { tone: Tone; label: string }> = {
  sent: { tone: "success", label: "Sent" },
  queued: { tone: "info", label: "In the queue" },
  failed: { tone: "danger", label: "Failed" },
  skipped: { tone: "neutral", label: "Not sent" },
};

/**
 * The email notifications of the signed-in user: the events that send an email, and the emails
 * that the panel sent. An administrator sees all emails, and a reseller the emails to its
 * customers too.
 */
export function NotificationSettings() {
  const state = useStore();
  const user = state.session.user;
  const prefs = useLoad(readPreferences, []);
  const save = useAction(setPreferences);
  const toggle = async (p: NotificationPreference, on: boolean) => {
    if (!prefs.data) return;
    const before = prefs.data;
    prefs.set(before.map((x) => (x.event === p.event ? { ...x, email: on } : x)));
    const r = await save.run({ [p.event]: on });
    prefs.set(r.ok ? r.value : before);
  };
  return (
    <div className="stack">
      <Card title="Email notifications">
        <div className="card-body" style={{ paddingBottom: 8 }}>
          <p className="muted">The panel sends these emails to <strong>{user.email}</strong>. Turn off the emails that you do not need. The security emails are always on.</p>
          <FormError error={prefs.error ?? save.error} />
        </div>
        <TableWrap>
          <table>
            <thead><tr><th scope="col">Event</th><th scope="col" style={{ width: 150 }}>Email</th></tr></thead>
            <tbody>
              {(prefs.data ?? []).map((p) => (
                <tr key={p.event}>
                  <td><strong>{p.label}</strong><span className="cell-sub">{p.description}</span></td>
                  <td>
                    <Toggle checked={p.email} disabled={p.required || save.busy} label={`Email for ${p.label}`} onChange={(on) => void toggle(p, on)}>
                      {p.required ? "Always on" : p.email ? "On" : "Off"}
                    </Toggle>
                  </td>
                </tr>
              ))}
              {prefs.loading && !prefs.data && <tr><td colSpan={2} className="muted">Loading…</td></tr>}
            </tbody>
          </table>
        </TableWrap>
      </Card>
      <SentEmails canRetry={user.role !== "owner"} everyone={user.role !== "owner"} />
    </div>
  );
}

/** The emails that the panel sent, with a filter, and the text of each email. */
export function SentEmails({ canRetry, everyone }: { canRetry: boolean; everyone: boolean }) {
  const [filter, setFilter] = useState<DeliveryState | "">("");
  const list = useLoad(() => readDeliveries({ state: filter || undefined }), [filter]);
  const [open, setOpen] = useState<NotificationDelivery | null>(null);
  const retry = useAction(retryDelivery);
  const show = async (d: NotificationDelivery) => {
    setOpen(d);
    try {
      setOpen(await readDelivery(d.id));
    } catch {
      // The row has the subject. The text stays empty.
    }
  };
  const rows = list.data?.data ?? [];
  return (
    <Card title={everyone ? "Sent emails" : "Emails to your account"} actions={
      <div className="filters">
        <label className="visually-hidden" htmlFor="email-state">State</label>
        <select id="email-state" className="select" value={filter} onChange={(e) => setFilter(e.target.value as DeliveryState | "")}>
          <option value="">All states</option>
          <option value="sent">Sent</option>
          <option value="failed">Failed</option>
          <option value="skipped">Not sent</option>
          <option value="queued">In the queue</option>
        </select>
      </div>
    }>
      <FormError error={list.error ?? retry.error} />
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Time</th><th scope="col">Email</th><th scope="col">To</th><th scope="col">State</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {rows.map((d) => (
              <tr key={d.id}>
                <td className="nowrap">{relativeTime(d.createdAt)}<span className="cell-sub">{longDate(d.createdAt)}</span></td>
                <td><button type="button" className="row-link" onClick={() => void show(d)}><strong>{d.subject}</strong></button><span className="cell-sub">{d.label}{d.sentBy === "reseller" ? " · your SMTP server" : ""}</span></td>
                <td>{d.recipient}</td>
                <td><Status tone={STATE[d.state].tone}>{STATE[d.state].label}</Status>{d.error && <span className="cell-sub note-danger" style={{ maxWidth: 320 }}>{d.error}</span>}</td>
                <td className="cell-actions" style={{ width: 120 }}>
                  {canRetry && (d.state === "failed" || d.state === "skipped") && (
                    <button type="button" className="btn btn-sm" disabled={retry.busy} onClick={async () => { if ((await retry.run(d.id)).ok) list.reload(); }}>Send again</button>
                  )}
                </td>
              </tr>
            ))}
            {!list.loading && rows.length === 0 && <tr><td colSpan={5} className="muted">No emails.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {open && (
        <Modal wide title={open.subject} onClose={() => setOpen(null)} footer={<button type="button" className="btn btn-primary" onClick={() => setOpen(null)}>Close</button>}>
          <table className="kv" style={{ marginBottom: 14 }}>
            <tbody>
              <tr><th scope="row">To</th><td>{open.recipient}</td></tr>
              <tr><th scope="row">Event</th><td>{open.label}</td></tr>
              <tr><th scope="row">State</th><td><Status tone={STATE[open.state].tone}>{STATE[open.state].label}</Status>{open.attempts > 1 && <span className="muted"> after {open.attempts} tries</span>}</td></tr>
              {open.error && <tr><th scope="row">Error</th><td className="text-danger">{open.error}</td></tr>}
            </tbody>
          </table>
          <pre className="email-text">{open.body ?? "…"}</pre>
        </Modal>
      )}
    </Card>
  );
}

const PORTS: { port: number; encryption: MailEncryption; label: string }[] = [
  { port: 587, encryption: "starttls", label: "587 (submission, STARTTLS)" },
  { port: 465, encryption: "tls", label: "465 (TLS)" },
  { port: 2525, encryption: "starttls", label: "2525 (alternative)" },
  { port: 25, encryption: "none", label: "25 (relay)" },
];

function inputOf(s: MailServer | undefined): MailServerInput {
  return {
    enabled: s?.configured ? s.enabled : true,
    host: s?.host ?? "",
    port: s?.port ?? 587,
    encryption: s?.encryption ?? "starttls",
    username: s?.username ?? "",
    fromAddress: s?.fromAddress ?? "",
    fromName: s?.fromName ?? "",
    replyTo: s?.replyTo ?? "",
  };
}

/**
 * The SMTP server and the templates. An administrator sets them for the panel. A reseller sets
 * them for the emails to its customers.
 */
export function EmailDelivery() {
  const state = useStore();
  const admin = state.session.user.role === "administrator";
  return (
    <div className="stack">
      <SmtpServer admin={admin} />
      <Templates admin={admin} />
    </div>
  );
}

function SmtpServer({ admin }: { admin: boolean }) {
  const state = useStore();
  const server = useLoad(readMailServer, []);
  const [form, setForm] = useState<MailServerInput>(inputOf(undefined));
  const [password, setPassword] = useState<string | undefined>(undefined);
  const [to, setTo] = useState("");
  const [saved, setSaved] = useState(false);
  const [test, setTest] = useState<{ sent: boolean; to: string; error: string | null } | null>(null);
  const save = useAction(saveMailServer);
  const send = useAction(sendTestEmail);
  const loaded = useRef(false);
  useEffect(() => {
    if (server.data && !loaded.current) { loaded.current = true; setForm(inputOf(server.data)); }
  }, [server.data]);
  const s = server.data;
  const set = <K extends keyof MailServerInput>(k: K, v: MailServerInput[K]) => { setSaved(false); setForm((f) => ({ ...f, [k]: v })); };
  const status = !s?.configured ? <Status tone="warning">Not set up</Status>
    : !s.enabled ? <Status tone="neutral">Off</Status>
      : s.testError ? <Status tone="danger">Test failed</Status>
        : s.testedAt ? <Status tone="success">Works</Status>
          : <Status tone="warning">Not tested</Status>;
  const submit = async () => {
    const r = await save.run({ ...form, password });
    if (r.ok) { server.set(r.value); setPassword(undefined); setSaved(true); }
  };
  return (
    <Card title="SMTP server" actions={status}>
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <p className="muted" style={{ marginBottom: 12, maxWidth: 760 }}>
          {admin
            ? "The panel sends all email notifications through this server: to administrators, to resellers and to all customers. A reseller can set its own server for its customers."
            : s?.panelFallback
              ? "Your customers get emails with your name. With no server of your own, the emails go through the server of the panel. Set your own server to send them from your domain."
              : "Your customers get emails with your name through this server. Your provider has no SMTP server, so the panel sends no email to your customers until you set one."}
        </p>
        <FormError error={server.error ?? save.error} shown={["host", "port", "encryption", "username", "password", "fromAddress", "fromName", "replyTo"]} />
        {saved && <p role="status" style={{ marginBottom: 12 }}><Status tone="success">Saved</Status> Send a test email to make sure that the server accepts the emails of the panel.</p>}
        {s?.testError && <div className="alert alert-danger" style={{ marginBottom: 12 }}><p><strong>The last test email failed.</strong> {s.testError}</p></div>}
        {s?.testedAt && !s.testError && <p className="muted small" style={{ marginBottom: 12 }}>The last test email went out {relativeTime(s.testedAt)}.</p>}
        <div className="grid-2" style={{ gap: "0 20px", alignItems: "start" }}>
          <Field label="Host" hint="For example smtp.example.com." error={save.error?.fields.host}>{(p) => <input {...p} className="input" value={form.host} onChange={(e) => set("host", e.target.value)} placeholder="smtp.example.com" autoComplete="off" />}</Field>
          <div className="grid-2" style={{ gap: "0 12px" }}>
            <Field label="Port" error={save.error?.fields.port}>{(p) => (
              <select {...p} className="select" value={form.port} onChange={(e) => { const port = PORTS.find((x) => x.port === Number(e.target.value))!; setSaved(false); setForm((f) => ({ ...f, port: port.port, encryption: port.encryption })); }}>
                {PORTS.map((x) => <option key={x.port} value={x.port}>{x.label}</option>)}
              </select>
            )}</Field>
            <Field label="Encryption" error={save.error?.fields.encryption}>{(p) => (
              <select {...p} className="select" value={form.encryption} onChange={(e) => set("encryption", e.target.value as MailEncryption)}>
                <option value="starttls">STARTTLS</option>
                <option value="tls">TLS</option>
                <option value="none">None</option>
              </select>
            )}</Field>
          </div>
          <Field label="User name" hint="Leave it empty if the server needs no sign-in." error={save.error?.fields.username}>{(p) => <input {...p} className="input" value={form.username} onChange={(e) => set("username", e.target.value)} autoComplete="off" />}</Field>
          <Field label="Password" hint={s?.hasPassword && password === undefined ? "The panel keeps the saved password. Type a new one to change it." : undefined} error={save.error?.fields.password}>{(p) => (
            <input {...p} className="input" type="password" autoComplete="new-password" value={password ?? ""} placeholder={s?.hasPassword ? "••••••••••" : ""} onChange={(e) => { setSaved(false); setPassword(e.target.value); }} />
          )}</Field>
          <Field label="From address" hint="The address must be on a domain that the SMTP server can send for (SPF and DKIM)." error={save.error?.fields.fromAddress}>{(p) => <input {...p} className="input" type="email" value={form.fromAddress} onChange={(e) => set("fromAddress", e.target.value)} placeholder="noreply@example.com" />}</Field>
          <Field label="From name" hint={admin ? "For example the name of your company." : "Leave it empty to use the name of your brand."} error={save.error?.fields.fromName}>{(p) => <input {...p} className="input" value={form.fromName} onChange={(e) => set("fromName", e.target.value)} />}</Field>
          <Field label="Reply-to address" hint="Optional. Replies go to this address, for example your support desk." error={save.error?.fields.replyTo}>{(p) => <input {...p} className="input" type="email" value={form.replyTo} onChange={(e) => set("replyTo", e.target.value)} placeholder="support@example.com" />}</Field>
          <div className="field">
            <span className="label">Sending</span>
            <Toggle checked={form.enabled} label="Send emails through this server" onChange={(v) => set("enabled", v)}>{form.enabled ? "On" : "Off: the panel sends no email through this server"}</Toggle>
          </div>
        </div>
        <div className="btn-group" style={{ marginTop: 14 }}>
          <button type="submit" className="btn btn-primary" disabled={save.busy}>{save.busy ? "Saving…" : "Save"}</button>
          {s?.configured && (
            <ConfirmButton className="btn btn-danger" title="Remove the SMTP server?" confirm="Remove server" message={<p>{admin ? "The panel then sends no email notifications." : s.panelFallback ? "The emails to your customers then go through the server of the panel." : "The panel then sends no email to your customers."}</p>} onConfirm={async () => { await removeMailServer(); loaded.current = false; server.reload(); }}>
              Remove
            </ConfirmButton>
          )}
        </div>
      </form>
      {s?.configured && (
        <div className="card-body" style={{ borderTop: "1px solid var(--border)" }}>
          <h3 className="label" style={{ marginBottom: 8 }}>Test email</h3>
          <form className="inline-form" noValidate onSubmit={async (e) => {
            e.preventDefault();
            setTest(null);
            const r = await send.run(admin && to ? to : undefined);
            if (r.ok) { setTest(r.value); server.set(r.value.server); }
          }}>
            {admin
              ? <Field label="Send to" hint="Leave it empty to send to your own address.">{(p) => <input {...p} className="input" type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder={state.session.user.email} style={{ minWidth: 280 }} />}</Field>
              : <p className="muted" style={{ marginBottom: 8 }}>The panel sends the test email to {state.session.user.email}.</p>}
            <div className="btn-group"><button type="submit" className="btn" disabled={send.busy}>{send.busy ? "Sending…" : "Send test email"}</button></div>
          </form>
          <FormError error={send.error} />
          {test && (test.sent
            ? <p role="status" style={{ marginTop: 10 }}><Status tone="success">Sent</Status> The server accepted the email to {test.to}. Look in the inbox, and in the spam folder.</p>
            : <div className="alert alert-danger" role="alert" style={{ marginTop: 10 }}><p><strong>The server did not accept the email.</strong> {test.error}</p></div>)}
        </div>
      )}
    </Card>
  );
}

function Templates({ admin }: { admin: boolean }) {
  const list = useLoad(readTemplates, []);
  const [editing, setEditing] = useState<NotificationTemplate | null>(null);
  return (
    <Card title="Email templates">
      <div className="card-body" style={{ paddingBottom: 8 }}>
        <p className="muted" style={{ maxWidth: 760 }}>
          {admin
            ? "Change the subject and the text of each email. A reseller can change them again for its customers."
            : "Change the subject and the text of the emails to your customers. A template that you do not change is the template of your provider."}
        </p>
        <FormError error={list.error} />
      </div>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Email</th><th scope="col">Subject</th><th scope="col">Template</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {(list.data ?? []).map((t) => (
              <tr key={t.event}>
                <td><strong>{t.label}</strong><span className="cell-sub">{t.description}</span></td>
                <td className="mono small">{t.subject}</td>
                <td>{t.changed ? <Status tone="info">Changed</Status> : <Status tone="neutral">{admin ? "Default" : "From your provider"}</Status>}</td>
                <td className="cell-actions" style={{ width: 90 }}><button type="button" className="btn btn-sm" onClick={() => setEditing(t)}>Edit</button></td>
              </tr>
            ))}
            {list.loading && !list.data && <tr><td colSpan={4} className="muted">Loading…</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {editing && <TemplateEditor template={editing} admin={admin} onClose={() => setEditing(null)} onSaved={(t) => { list.set((list.data ?? []).map((x) => (x.event === t.event ? t : x))); setEditing(null); }} />}
    </Card>
  );
}

function TemplateEditor({ template, admin, onClose, onSaved }: { template: NotificationTemplate; admin: boolean; onClose: () => void; onSaved: (t: NotificationTemplate) => void }) {
  const [subject, setSubject] = useState(template.subject);
  const [body, setBody] = useState(template.body);
  const [preview, setPreview] = useState<TemplatePreview | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const save = useAction(saveTemplate);
  const reset = useAction(resetTemplate);
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const timer = setTimeout(() => {
      previewTemplate(template.event as NotificationEvent, subject, body).then((p) => { setPreview(p); setPreviewError(null); }, (e: Error) => setPreviewError(e.message));
    }, 300);
    return () => clearTimeout(timer);
  }, [template.event, subject, body]);
  const insert = (name: string) => {
    const el = bodyRef.current;
    const word = `{{${name}}}`;
    if (!el) { setBody(body + word); return; }
    const start = el.selectionStart ?? body.length;
    const end = el.selectionEnd ?? body.length;
    setBody(body.slice(0, start) + word + body.slice(end));
    requestAnimationFrame(() => { el.focus(); el.setSelectionRange(start + word.length, start + word.length); });
  };
  const changedFromOriginal = subject !== template.originalSubject || body !== template.originalBody;
  return (
    <Modal wide title={`Template: ${template.label}`} onClose={onClose} footer={<>
      {template.changed && (
        <ConfirmButton className="btn btn-danger" title="Reset this template?" confirm="Reset template" message={<p>{admin ? "The email goes back to the default subject and text." : "The email goes back to the template of your provider."}</p>} onConfirm={async () => { const r = await reset.run(template.event); if (r.ok) onSaved(r.value); }}>
          Reset
        </ConfirmButton>
      )}
      <span style={{ flex: 1 }} />
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="submit" form="template" className="btn btn-primary" disabled={save.busy}>Save template</button>
    </>}>
      <form id="template" noValidate onSubmit={async (e) => { e.preventDefault(); const r = await save.run(template.event, subject, body); if (r.ok) onSaved(r.value); }}>
        <FormError error={save.error ?? reset.error} shown={["subject", "body"]} />
        <div className="template-editor">
          <div>
            <Field label="Subject" error={save.error?.fields.subject}>{(p) => <input {...p} className="input mono" value={subject} onChange={(e) => setSubject(e.target.value)} />}</Field>
            <Field label="Text" error={save.error?.fields.body}>{(p) => <textarea {...p} ref={bodyRef} className="textarea mono" rows={14} value={body} onChange={(e) => setBody(e.target.value)} spellCheck={false} />}</Field>
            <p className="muted small" style={{ marginTop: 4 }}>Select a value to add it to the text. The panel changes each value to the data of the event.</p>
            <ul className="tag-list" style={{ marginTop: 6 }}>
              {template.variables.map((v) => <li key={v}><button type="button" className="tag tag-button" onClick={() => insert(v)}>{`{{${v}}}`}</button></li>)}
            </ul>
            {changedFromOriginal && <p className="small" style={{ marginTop: 10 }}><button type="button" className="btn-link" style={{ padding: 0, minHeight: 0 }} onClick={() => { setSubject(template.originalSubject); setBody(template.originalBody); }}>Use the {admin ? "default" : "template of your provider"} again</button></p>}
          </div>
          <div>
            <p className="label" style={{ marginBottom: 6 }}>Preview, with example values</p>
            {previewError ? <div className="alert alert-warning"><p>{previewError}</p></div> : preview ? (
              <div className="email-preview">
                <p className="email-preview-subject"><span className="muted">Subject:</span> {preview.subject}</p>
                <iframe title="Preview of the email" sandbox="" srcDoc={preview.html} />
              </div>
            ) : <Empty title="Loading the preview" />}
          </div>
        </div>
      </form>
    </Modal>
  );
}
