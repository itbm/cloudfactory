import { useState } from "react";
import { useAction, useStore } from "../../api/hooks";
import { deployNow, rollbackRelease, setGitSource } from "../../api/store";
import type { GitSource, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, JobRow, Status } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { jobActive } from "../../lib/labels";

/** Deploys from Git (FR-1001 to FR-1010). */
export function DeploysTab({ website }: { website: Website }) {
  const state = useStore();
  const git = website.git;
  const [form, setForm] = useState<GitSource>(
    git ?? { repository: "", branch: "main", trigger: "webhook", scheduleMinutes: 5, buildCommands: ["composer install --no-dev"], deployKey: "", secrets: [] },
  );
  const [editing, setEditing] = useState(!git);
  const [secret, setSecret] = useState("");
  const save = useAction(async () => {
    setGitSource(website.id, form);
    setEditing(false);
  });
  const deploy = useAction(deployNow);
  const rollback = useAction(rollbackRelease);
  const jobs = state.jobs.filter((j) => j.websiteId === website.id && (j.title === "Deploy" || j.title === "Go back to release")).slice(0, 3);
  const running = jobs.some(jobActive);

  return (
    <div className="grid-2">
      <div className="stack">
        <Card
          title="Git repository"
          actions={git && !editing && (
            <div className="btn-group">
              <button type="button" className="btn" onClick={() => setEditing(true)}>Change</button>
              <button type="button" className="btn btn-primary" disabled={running} onClick={() => void deploy.run(website.id)}>Deploy now</button>
            </div>
          )}
        >
          <div className="card-body">
            <FormError error={deploy.error ?? rollback.error} />
            {!editing && git ? (
              <table className="kv">
                <tbody>
                  <tr><th scope="row">Repository</th><td><code>{git.repository}</code></td></tr>
                  <tr><th scope="row">Branch</th><td><code>{git.branch}</code></td></tr>
                  <tr><th scope="row">Trigger</th><td>{git.trigger === "webhook" ? "Webhook from the Git host, on each push" : `Check the branch every ${git.scheduleMinutes} minutes`}</td></tr>
                  <tr><th scope="row">Build</th><td>{git.buildCommands.map((c) => <div key={c}><code>{c}</code></div>)}<span className="cell-sub">Runs in a separate build container, not in the live container.</span></td></tr>
                  <tr><th scope="row">Deploy key</th><td><code style={{ wordBreak: "break-all" }}>{git.deployKey}</code><span className="cell-sub">Read-only. Add it to the repository on your Git host.</span></td></tr>
                  {git.trigger === "webhook" && <tr><th scope="row">Webhook URL</th><td><code style={{ wordBreak: "break-all" }}>https://panel.cloudfactory.io/hooks/git/{website.id}</code></td></tr>}
                </tbody>
              </table>
            ) : (
              <form noValidate onSubmit={(e) => { e.preventDefault(); void save.run(); }}>
                <FormError error={save.error} />
                <Field label="Repository URL" error={save.error?.fields.repository}>
                  {(p) => <input {...p} className="input mono" value={form.repository} onChange={(e) => setForm({ ...form, repository: e.target.value })} placeholder="git@github.com:acme/shop.git" spellCheck={false} />}
                </Field>
                <Field label="Branch" error={save.error?.fields.branch}>
                  {(p) => <input {...p} className="input mono" value={form.branch} onChange={(e) => setForm({ ...form, branch: e.target.value })} spellCheck={false} />}
                </Field>
                <fieldset className="radio-cards" style={{ marginTop: 14 }}>
                  <legend>Get changes</legend>
                  <label className="radio-card"><input type="radio" name="trigger" checked={form.trigger === "webhook"} onChange={() => setForm({ ...form, trigger: "webhook" })} /><span><strong>Webhook</strong><span>The Git host calls the panel on each push.</span></span></label>
                  <label className="radio-card"><input type="radio" name="trigger" checked={form.trigger === "schedule"} onChange={() => setForm({ ...form, trigger: "schedule" })} /><span><strong>Schedule</strong><span>The panel checks the branch every 5 minutes.</span></span></label>
                </fieldset>
                <div style={{ marginTop: 14 }}>
                  <Field label="Build commands" hint="One command on each line. For example composer install or npm ci.">
                    {(p) => <textarea {...p} className="textarea" rows={3} value={form.buildCommands.join("\n")} onChange={(e) => setForm({ ...form, buildCommands: e.target.value.split("\n") })} spellCheck={false} />}
                  </Field>
                </div>
                <div className="btn-group" style={{ marginTop: 14 }}>
                  <button type="submit" className="btn btn-primary">{git ? "Save" : "Connect repository"}</button>
                  {git && <button type="button" className="btn" onClick={() => { setForm(git); setEditing(false); }}>Cancel</button>}
                  {git && <ConfirmButton className="btn btn-danger" title="Disconnect the repository?" confirm="Disconnect" message={<p>The website keeps its live release. New pushes do not deploy.</p>} onConfirm={() => { setGitSource(website.id, undefined); setEditing(true); }}>Disconnect</ConfirmButton>}
                </div>
              </form>
            )}
          </div>
        </Card>
        {git && (
          <Card title="Deploy secrets">
            <div className="card-body">
              <p className="muted">The panel keeps secrets encrypted and gives them to the build as environment variables.</p>
              <ul className="tag-list" style={{ margin: "12px 0" }}>
                {git.secrets.map((s) => <li key={s}><span className="tag">{s}</span></li>)}
                {git.secrets.length === 0 && <li className="muted">No secrets.</li>}
              </ul>
              <form className="inline-form" onSubmit={(e) => {
                e.preventDefault();
                const n = secret.trim().toUpperCase().replace(/[^A-Z0-9_]/g, "_");
                if (n && !git.secrets.includes(n)) setGitSource(website.id, { ...git, secrets: [...git.secrets, n] });
                setSecret("");
              }}>
                <Field label="Name">
                  {(p) => <input {...p} className="input mono" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="STRIPE_SECRET_KEY" />}
                </Field>
                <Field label="Value">
                  {(p) => <input {...p} className="input" type="password" autoComplete="off" />}
                </Field>
                <div className="field"><span className="label" aria-hidden="true">&nbsp;</span><button type="submit" className="btn">Add secret</button></div>
              </form>
            </div>
          </Card>
        )}
      </div>
      <div className="stack">
        {jobs.length > 0 && (
          <Card title="Deploy jobs">
            <div className="card-body"><ul className="job-list">{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul></div>
          </Card>
        )}
        <Card title="Releases">
          <div className="card-body">
            <p className="muted small">Each deploy goes into a new release directory. The panel then changes the live release in one atomic step. It keeps the last 5 releases. If the health check fails, the panel goes back automatically, except after a database migration.</p>
          </div>
          <div className="table-wrap">
            <table>
              <thead><tr><th scope="col">Release</th><th scope="col">Result</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {website.releases.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <code>{r.ref}</code> {r.message}
                      <span className="cell-sub">{relativeTime(r.at)} · {r.durationSeconds} s{r.migration ? " · ran a database migration" : ""}</span>
                    </td>
                    <td>{r.live ? <Status tone="success">Live</Status> : r.state === "failed" ? <Status tone="danger">Failed</Status> : <Status tone="neutral">Kept</Status>}</td>
                    <td className="cell-actions" style={{ width: 150 }}>
                      {!r.live && r.state === "succeeded" && (
                        <ConfirmButton
                          className="btn btn-sm"
                          danger={false}
                          title={`Go back to ${r.ref}?`}
                          confirm="Go back"
                          message={r.migration ? <p>This release ran a database migration. The panel does not change the database. Make sure the code works with the current database.</p> : <p>The panel switches the live release in one step. There is no downtime.</p>}
                          onConfirm={() => void rollback.run(website.id, r.id)}
                        >
                          Go back
                        </ConfirmButton>
                      )}
                    </td>
                  </tr>
                ))}
                {website.releases.length === 0 && <tr><td colSpan={3} className="muted">No releases yet.</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      </div>
    </div>
  );
}
