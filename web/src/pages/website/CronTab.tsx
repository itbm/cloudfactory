import { useState } from "react";
import { useAction } from "../../api/hooks";
import { addCron, removeCron, runCron, setCron } from "../../api/store";
import type { Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, Status, Toggle } from "../../components/ui";
import { relativeTime } from "../../lib/format";

const PRESETS = [
  ["*/5 * * * *", "Every 5 minutes"],
  ["0 * * * *", "Every hour"],
  ["0 3 * * *", "Each day at 03:00"],
  ["0 4 * * 1", "Each Monday at 04:00"],
];

export function CronTab({ website }: { website: Website }) {
  const [schedule, setSchedule] = useState("*/5 * * * *");
  const [command, setCommand] = useState("");
  const add = useAction(async () => {
    addCron(website.id, schedule, command);
    setCommand("");
  });

  return (
    <div className="stack">
      <Card title="Cron jobs">
        <div className="table-wrap">
          <table>
            <thead><tr><th scope="col">Schedule</th><th scope="col">Command</th><th scope="col">Last run</th><th scope="col">On</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {website.cron.map((c) => (
                <tr key={c.id}>
                  <td><code>{c.schedule}</code></td>
                  <td><code>{c.command}</code></td>
                  <td>
                    {c.lastRunAt ? (
                      <>
                        <Status tone={c.lastResult === "failed" ? "danger" : "success"}>{c.lastResult === "failed" ? "Failed" : "Succeeded"}</Status>
                        <span className="cell-sub">{relativeTime(c.lastRunAt)}</span>
                      </>
                    ) : <span className="muted">Not run yet</span>}
                  </td>
                  <td><Toggle checked={c.enabled} label={`Cron job ${c.command} on`} onChange={(v) => setCron(website.id, c.id, { enabled: v })} /></td>
                  <td>
                    <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                      <button type="button" className="btn btn-sm" onClick={() => void runCron(website.id, c.id)}>Run now</button>
                      <ConfirmButton className="btn btn-sm btn-danger" title="Remove cron job?" confirm="Remove" message={<p><code>{c.command}</code> stops running.</p>} onConfirm={() => removeCron(website.id, c.id)}>Remove</ConfirmButton>
                    </div>
                  </td>
                </tr>
              ))}
              {website.cron.length === 0 && <tr><td colSpan={5} className="muted">No cron jobs.</td></tr>}
            </tbody>
          </table>
        </div>
        <p className="card-body muted small" style={{ paddingTop: 12 }}>Cron jobs run inside the website container, as the Unix user {website.unixUser}, in the limits of the plan.</p>
      </Card>
      <Card title="Add cron job">
        <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void add.run(); }}>
          <FormError error={add.error} />
          <div className="inline-form">
            <Field label="Schedule" hint="Minute, hour, day of month, month, day of week." error={add.error?.fields.schedule}>
              {(p) => <input {...p} className="input mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} list="cron-presets" spellCheck={false} />}
            </Field>
            <datalist id="cron-presets">{PRESETS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</datalist>
            <Field label="Command" error={add.error?.fields.command}>
              {(p) => <input {...p} className="input mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="php artisan schedule:run" spellCheck={false} />}
            </Field>
            <div className="field"><span className="label" aria-hidden="true">&nbsp;</span><button type="submit" className="btn btn-primary">Add</button></div>
          </div>
        </form>
      </Card>
    </div>
  );
}
