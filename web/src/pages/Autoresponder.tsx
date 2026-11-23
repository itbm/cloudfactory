import { useState } from "react";
import { useAction, useLoad } from "../api/hooks";
import { getAutoresponder, setAutoresponder } from "../api/store";
import type { Autoresponder, Mailbox } from "../api/types";
import { Field, FormError, Modal } from "../components/ui";
import { longDate } from "../lib/format";

/** The mail server accepts a text of less than 2048 bytes. The panel permits 2000. */
const MAX_TEXT_BYTES = 2000;

const bytesOf = (s: string) => new TextEncoder().encode(s).length;

/** The day of a time in the time zone of the browser, as the value of a date input. */
export function localDay(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The start (00:00:00) or the end (23:59:59) of a day in the time zone of the browser, in UTC. */
export function dayBoundary(day: string, end: boolean): string | null {
  if (!day) return null;
  const [y, m, d] = day.split("-").map(Number);
  const t = end ? new Date(y, m - 1, d, 23, 59, 59) : new Date(y, m - 1, d, 0, 0, 0);
  return t.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** What the mail server does now with the autoresponder, or null when it is off. */
export function autoresponderState(a: Pick<Autoresponder, "enabled" | "startsAt" | "endsAt">, now = Date.now()): string | null {
  if (!a.enabled) return null;
  if (a.endsAt && Date.parse(a.endsAt) < now) return `The last day was ${longDate(a.endsAt)}. The mail server does not answer.`;
  if (a.startsAt && Date.parse(a.startsAt) > now) return `The mail server starts to answer on ${longDate(a.startsAt)}.`;
  return a.endsAt ? `The mail server answers new messages until the end of ${longDate(a.endsAt)}.` : "The mail server answers new messages.";
}

/**
 * The autoresponder of a mailbox. The panel reads it from the mail server each time, because
 * the user can also change it in the webmail.
 */
export function AutoresponderDialog({ mailbox, onClose }: { mailbox: Mailbox; onClose: () => void }) {
  const load = useLoad(() => getAutoresponder(mailbox.id), [mailbox.id]);
  const save = useAction(setAutoresponder);
  return (
    <Modal
      title={`Autoresponder of ${mailbox.address}`}
      onClose={onClose}
      wide
      footer={load.data
        ? <><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="autoresponder" className="btn btn-primary" disabled={save.busy}>Save autoresponder</button></>
        : <button type="button" className="btn" onClick={onClose}>Close</button>}
    >
      {load.data
        ? <AutoresponderForm mailbox={mailbox} current={load.data} save={save} onSaved={onClose} />
        : load.error ? <FormError error={load.error} /> : <p className="muted" role="status">The panel reads the autoresponder from the mail server…</p>}
    </Modal>
  );
}

function AutoresponderForm({ mailbox, current, save, onSaved }: { mailbox: Mailbox; current: Autoresponder; save: ReturnType<typeof useAction<Parameters<typeof setAutoresponder>, Autoresponder>>; onSaved: () => void }) {
  const [enabled, setEnabled] = useState(current.enabled);
  const [subject, setSubject] = useState(current.subject ?? "");
  const [text, setText] = useState(current.text);
  const firstDay = localDay(current.startsAt);
  const lastDay = localDay(current.endsAt);
  const [start, setStart] = useState(firstDay);
  const [end, setEnd] = useState(lastDay);
  const fields = save.error?.fields ?? {};
  const used = bytesOf(text);
  const state = autoresponderState(current);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <form id="autoresponder" noValidate onSubmit={async (e) => {
      e.preventDefault();
      if (used > MAX_TEXT_BYTES) return;
      const r = await save.run(mailbox.id, {
        enabled,
        subject: subject.trim() === "" ? null : subject.trim(),
        text,
        // A day that the user did not change keeps its time, for example a time from the webmail.
        startsAt: start === firstDay ? current.startsAt : dayBoundary(start, false),
        endsAt: end === lastDay ? current.endsAt : dayBoundary(end, true),
      });
      if (r.ok) onSaved();
    }}>
      <FormError error={save.error} shown={["enabled", "subject", "text", "startsAt", "endsAt"]} />
      {state && <p className="muted" role="status" style={{ marginTop: 0 }}>{state}</p>}
      <label className="check" style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} style={{ marginTop: 3, flex: "none" }} />
        <span>
          <strong>Answer new messages automatically</strong>
          <span className="muted small" style={{ display: "block" }}>The mail server answers each sender one time in 30 days. It does not answer mailing lists or automatic messages. The user of the mailbox can also change it in the webmail.</span>
        </span>
      </label>
      {enabled && current.filterScript && (
        <div className="alert alert-warning" role="note" style={{ marginTop: 12 }}>
          <p>
            This mailbox has its own filter rules (“{current.filterScript}”). The mail server uses one set of rules for each mailbox, so the filter rules stop while the autoresponder is on.
            After you turn off the autoresponder, turn on the filter rules again in the webmail.
          </p>
        </div>
      )}
      <div className="form-grid" style={{ marginTop: 14 }}>
        <div style={{ gridColumn: "1 / -1" }}>
          <Field label="Subject" hint="Leave empty to use “Auto:” and the subject of the message." error={fields.subject}>
            {(p) => <input {...p} className="input" value={subject} maxLength={200} onChange={(e) => setSubject(e.target.value)} placeholder="Out of office" />}
          </Field>
        </div>
        <div style={{ gridColumn: "1 / -1" }}>
          <Field label="Message" hint={`${used} of ${MAX_TEXT_BYTES} characters. A letter with an accent counts as two.`} error={fields.text ?? (used > MAX_TEXT_BYTES ? "The message is too long." : undefined)}>
            {(p) => <textarea {...p} className="input" rows={7} value={text} onChange={(e) => setText(e.target.value)} placeholder="I am away until Monday, with no access to my email." />}
          </Field>
        </div>
        <Field label="First day" hint={`Optional. Days are in your time zone (${zone}).`} error={fields.startsAt}>
          {(p) => <input {...p} className="input" type="date" value={start} max={end || undefined} onChange={(e) => setStart(e.target.value)} />}
        </Field>
        <Field label="Last day" hint="Optional. Empty: the mail server answers until you turn off the autoresponder." error={fields.endsAt}>
          {(p) => <input {...p} className="input" type="date" value={end} min={start || undefined} onChange={(e) => setEnd(e.target.value)} />}
        </Field>
      </div>
    </form>
  );
}
