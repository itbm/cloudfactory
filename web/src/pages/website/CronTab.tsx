import { useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { addCronJob, listCronJobs, removeCronJob, runCronJob, setCronJob } from "../../api/store";
import type { CronJob, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, Modal, Status, TableWrap, Toggle } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { jobActive } from "../../lib/labels";

const PRESETS = [
  ["*/5 * * * *", "Every 5 minutes"],
  ["0 * * * *", "Every hour"],
  ["0 3 * * *", "Each day at 03:00"],
  ["0 4 * * 1", "Each Monday at 04:00"],
];

/** The largest number of cron jobs of a website (InputRules::MAX_CRON_JOBS). */
const MAX_CRON_JOBS = 20;

/** Cron jobs: commands that the server runs in the container of the website on a schedule. */
export function CronTab({ website }: { website: Website }) {
  const state = useStore();
  const id = website.id;
  // A finished job can change the last result, so the list loads again when a job of the website ends.
  const active = state.jobs.filter((j) => j.websiteId === id && jobActive(j)).length;
  const list = useLoad(() => listCronJobs(id), [id, active]);
  const jobs = list.data ?? [];
  const [schedule, setSchedule] = useState("*/5 * * * *");
  const [command, setCommand] = useState("");
  const [output, setOutput] = useState<CronJob | null>(null);
  const add = useAction(async () => {
    await addCronJob(id, schedule, command);
    setCommand("");
    list.reload();
  });
  const change = useAction((cronJobId: string, enabled: boolean) => setCronJob(id, cronJobId, { enabled }).then(list.reload));
  const run = useAction((cronJobId: string) => runCronJob(id, cronJobId).then(list.reload));
  const remove = useAction((cronJobId: string) => removeCronJob(id, cronJobId).then(list.reload));

  return (
    <div className="stack">
      <Card title="Cron jobs">
        {(list.error ?? change.error ?? run.error ?? remove.error) && <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={list.error ?? change.error ?? run.error ?? remove.error} /></div>}
        <TableWrap>
          <table aria-busy={list.loading}>
            <thead><tr><th scope="col">Schedule</th><th scope="col">Command</th><th scope="col">Last run</th><th scope="col">On</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {jobs.map((c) => (
                <tr key={c.id}>
                  <td><code>{c.schedule}</code></td>
                  <td><code style={{ wordBreak: "break-all" }}>{c.command}</code></td>
                  <td>
                    {c.lastRunAt ? (
                      <>
                        <Status tone={c.lastResult === "failed" ? "danger" : "success"}>{c.lastResult === "failed" ? `Failed${c.lastExitCode === null ? "" : ` (exit code ${c.lastExitCode})`}` : "Succeeded"}</Status>
                        <span className="cell-sub">
                          {relativeTime(c.lastRunAt)}
                          {c.lastOutput ? <> · <button type="button" className="btn-link" onClick={() => setOutput(c)}>Show output<span className="visually-hidden"> of {c.command}</span></button></> : null}
                        </span>
                      </>
                    ) : <span className="muted">Not run yet</span>}
                  </td>
                  <td><Toggle checked={c.enabled} disabled={change.busy} label={`Cron job ${c.command}`} onChange={(v) => void change.run(c.id, v)} /></td>
                  <td>
                    <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                      <button type="button" className="btn btn-sm" disabled={run.busy || website.status !== "running"} onClick={() => void run.run(c.id)}>{run.busy ? "Running…" : "Run now"}<span className="visually-hidden"> {c.command}</span></button>
                      <ConfirmButton className="btn btn-sm btn-danger" title="Remove this cron job?" confirm="Remove" message={<p>The server stops running <code>{c.command}</code>.</p>} onConfirm={() => void remove.run(c.id)}>Remove<span className="visually-hidden"> {c.command}</span></ConfirmButton>
                    </div>
                  </td>
                </tr>
              ))}
              {jobs.length === 0 && <tr><td colSpan={5} className="muted">{list.loading ? "Loading…" : "No cron jobs."}</td></tr>}
            </tbody>
          </table>
        </TableWrap>
        <p className="card-body muted small" style={{ paddingTop: 12 }}>
          The server runs each command in the container of the website, in the directory <code>/app</code>, as the Unix user <code>{website.unixUser}</code>. The command has the limits of the website. Times are in UTC. The server stops a command that runs for more than 1 hour.
        </p>
      </Card>
      <Card title="Add a cron job">
        <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void add.run(); }}>
          <FormError error={add.error && !add.error.fields.schedule && !add.error.fields.command ? add.error : null} />
          <div className="inline-form">
            <Field label="Schedule" hint="Minute, hour, day of month, month, day of week." error={add.error?.fields.schedule}>
              {(p) => <input {...p} className="input mono" value={schedule} onChange={(e) => setSchedule(e.target.value)} list="cron-presets" spellCheck={false} />}
            </Field>
            <datalist id="cron-presets">{PRESETS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</datalist>
            <Field label="Command" error={add.error?.fields.command}>
              {(p) => <input {...p} className="input mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="php artisan schedule:run" spellCheck={false} autoCapitalize="none" />}
            </Field>
            <div className="field"><span className="label" aria-hidden="true">&nbsp;</span><button type="submit" className="btn btn-primary" disabled={add.busy || !command.trim() || jobs.length >= MAX_CRON_JOBS}>Add</button></div>
          </div>
          {jobs.length >= MAX_CRON_JOBS && <p className="muted small" style={{ marginTop: 10 }}>A website can have {MAX_CRON_JOBS} cron jobs.</p>}
        </form>
      </Card>
      {output && (
        <Modal wide title="Output of the last run" onClose={() => setOutput(null)} footer={<button type="button" className="btn" onClick={() => setOutput(null)}>Close</button>}>
          <p className="muted small" style={{ marginBottom: 10 }}><code>{output.command}</code></p>
          <pre className="log-view" tabIndex={0} aria-label="Output">{output.lastOutput}</pre>
        </Modal>
      )}
    </div>
  );
}
