import { useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { activateRelease, newWebhook, readDeploy, removeDeploySecret, removeGitSource, setDeploySecret, setGitSource, startDeploy } from "../../api/store";
import type { GitSourceInput } from "../../api/store";
import type { DeployTrigger, GitSource, Release, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, JobRow, Status, TableWrap } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { jobActive } from "../../lib/labels";
import { CopyField } from "./AccessTab";

const EMPTY: GitSourceInput = { repository: "", branch: "main", webDirectory: "public", trigger: "manual", checkMinutes: 5, buildCommands: [], migrationCommand: "" };
const DEPLOY_ACTIONS = ["Deploy", "ActivateRelease"];
/** The intervals of the branch check that the form offers, in minutes. */
const INTERVALS = [1, 5, 15, 30, 60];
/** The start of the error of a deploy whose health check failed after the switch. */
const WENT_BACK = "the health check failed after the switch";

const formOf = (s: GitSource): GitSourceInput => ({
  repository: s.repository,
  branch: s.branch,
  webDirectory: s.webDirectory,
  trigger: s.trigger,
  checkMinutes: s.checkMinutes,
  buildCommands: s.buildCommands,
  migrationCommand: s.migrationCommand ?? "",
});

const minutes = (n: number) => (n === 1 ? "minute" : `${n} minutes`);

const STARTED_BY: Record<DeployTrigger, string> = { manual: "started by hand", webhook: "started by a push", schedule: "started by a branch check" };

function ReleaseState({ release }: { release: Release }) {
  if (release.live) return <Status tone="success">Live</Status>;
  if (release.state === "building") return <Status tone="info">Deploying</Status>;
  if (release.state === "failed") return release.error?.startsWith(WENT_BACK) ? <Status tone="warning">Went back</Status> : <Status tone="danger">Failed</Status>;
  if (release.state === "removed") return <Status tone="neutral">Removed</Status>;
  return <Status tone="neutral">Kept</Status>;
}

/** How deploys start, for the summary of the source. */
function TriggerText({ source }: { source: GitSource }) {
  if (source.trigger === "webhook") {
    return <>On each push to the branch, with a signed webhook. You can also start a deploy here.{source.deployQueued && <span className="cell-sub">A push came during the current deploy. The panel deploys it next.</span>}</>;
  }
  if (source.trigger === "schedule") {
    return (
      <>
        The panel checks the branch every {minutes(source.checkMinutes)}, and deploys each new commit.
        {source.checkError ? (
          <span className="cell-sub text-danger">The last check failed{source.checkedAt ? ` ${relativeTime(source.checkedAt)}` : ""}: {source.checkError}</span>
        ) : (
          <span className="cell-sub">{source.checkedAt ? `Last check ${relativeTime(source.checkedAt)}.` : "The first check is in the next minute."}</span>
        )}
      </>
    );
  }
  return <>Only when you select Deploy now.</>;
}

/** Deploys from a Git repository. Each deploy makes a release, and the server keeps the last releases. */
export function DeploysTab({ website }: { website: Website }) {
  const state = useStore();
  const id = website.id;
  const jobs = state.jobs.filter((j) => j.websiteId === id && DEPLOY_ACTIONS.includes(j.action ?? "")).slice(0, 3);
  const running = jobs.some(jobActive);
  // A job that ends changes the releases, so the data loads again.
  const deploy = useLoad(() => readDeploy(id), [id, jobs.map((j) => `${j.id}:${j.state}`).join()]);
  const source = deploy.data?.source ?? null;
  const releases = deploy.data?.releases ?? [];
  const [form, setForm] = useState<GitSourceInput | null>(null);
  const [commands, setCommands] = useState("");
  const [secret, setSecret] = useState({ name: "", value: "" });

  const edit = (from: GitSourceInput) => {
    setForm(from);
    setCommands(from.buildCommands.join("\n"));
  };
  const save = useAction(async () => {
    if (!form) return;
    deploy.set(await setGitSource(id, { ...form, buildCommands: commands.split("\n") }));
    setForm(null);
  });
  const disconnect = useAction(async () => {
    await removeGitSource(id);
    setForm(null);
    deploy.reload();
  });
  const start = useAction((websiteId: string) => startDeploy(websiteId).then(deploy.reload));
  const activate = useAction((releaseId: string) => activateRelease(id, releaseId).then(deploy.reload));
  const rotate = useAction(async () => deploy.set(await newWebhook(id)));
  const addSecret = useAction(async () => {
    deploy.set(await setDeploySecret(id, secret.name, secret.value));
    setSecret({ name: "", value: "" });
  });
  const dropSecret = useAction(async (name: string) => deploy.set(await removeDeploySecret(id, name)));

  if (!deploy.data) {
    return deploy.error ? <FormError error={deploy.error} /> : <p className="muted" role="status">Loading…</p>;
  }
  if (website.runtime.kind === "oci" && !source) {
    return <p className="muted">A website with a custom image has its code in the image. To deploy a new version, change the image tag on the Runtime tab.</p>;
  }
  const editing = form ?? (source ? null : EMPTY);
  const field = (key: keyof GitSourceInput) => save.error?.fields[key];
  const trigger = (value: DeployTrigger, title: string, text: string) => (
    <label className="radio-card">
      <input type="radio" name="trigger" checked={editing!.trigger === value} onChange={() => setForm({ ...editing!, trigger: value })} />
      <span><strong>{title}</strong><span>{text}</span></span>
    </label>
  );
  const intervals = editing && !INTERVALS.includes(editing.checkMinutes) ? [...INTERVALS, editing.checkMinutes].sort((a, b) => a - b) : INTERVALS;
  const attention = releases.find((r) => r.live && r.healthError);

  return (
    <div className="grid-2">
      <div className="stack">
        <Card
          title="Git repository"
          actions={source && !editing && (
            <div className="btn-group">
              <button type="button" className="btn" onClick={() => edit(formOf(source))}>Change</button>
              <button type="button" className="btn btn-primary" disabled={running || start.busy || website.status === "failed"} onClick={() => void start.run(id)}>{running ? "Deploying…" : "Deploy now"}</button>
            </div>
          )}
        >
          <div className="card-body">
            <FormError error={start.error ?? activate.error ?? disconnect.error} />
            {source && !editing ? (
              <table className="kv">
                <tbody>
                  <tr><th scope="row">Repository</th><td><code style={{ wordBreak: "break-all" }}>{source.repository}</code></td></tr>
                  <tr><th scope="row">Branch</th><td><code>{source.branch}</code></td></tr>
                  <tr><th scope="row">Web directory</th><td><code>{source.webDirectory}</code><span className="cell-sub">The directory in the repository that the web server serves.</span></td></tr>
                  <tr><th scope="row">Start</th><td><TriggerText source={source} /></td></tr>
                  <tr>
                    <th scope="row">Build</th>
                    <td>
                      {source.buildCommands.length ? source.buildCommands.map((c, i) => <div key={i}><code>{c}</code></div>) : "No build commands."}
                      <span className="cell-sub">Runs in a separate build container, not in the live container.</span>
                    </td>
                  </tr>
                  <tr>
                    <th scope="row">Migration</th>
                    <td>
                      {source.migrationCommand ? <code>{source.migrationCommand}</code> : "No migration command."}
                      <span className="cell-sub">Runs after the build, before the switch. After a migration, the panel does not go back automatically. It sends you an email instead.</span>
                    </td>
                  </tr>
                  <tr>
                    <th scope="row">Health check</th>
                    <td>For 5 minutes after the switch.<span className="cell-sub">If the website gives server errors, it goes back to the previous release.</span></td>
                  </tr>
                </tbody>
              </table>
            ) : (
              <form noValidate onSubmit={(e) => { e.preventDefault(); void save.run(); }}>
                <FormError error={save.error && !Object.keys(save.error.fields).length ? save.error : null} />
                {!source && <p className="muted" style={{ marginBottom: 14 }}>Connect a Git repository, and the panel gets the code, builds it, and puts it live with no downtime.</p>}
                <Field label="Repository" hint="An SSH address or an HTTPS address. Use the SSH address for a private repository." error={field("repository")}>
                  {(p) => <input {...p} className="input mono" value={editing!.repository} onChange={(e) => setForm({ ...editing!, repository: e.target.value })} placeholder="git@github.com:acme/shop.git" spellCheck={false} autoCapitalize="none" />}
                </Field>
                <div className="inline-form" style={{ marginTop: 14 }}>
                  <Field label="Branch" error={field("branch")}>
                    {(p) => <input {...p} className="input mono" value={editing!.branch} onChange={(e) => setForm({ ...editing!, branch: e.target.value })} spellCheck={false} autoCapitalize="none" />}
                  </Field>
                  <Field label="Web directory" hint={<>For example <code>public</code>. Type <code>.</code> for the top directory.</>} error={field("webDirectory")}>
                    {(p) => <input {...p} className="input mono" value={editing!.webDirectory} onChange={(e) => setForm({ ...editing!, webDirectory: e.target.value })} spellCheck={false} autoCapitalize="none" />}
                  </Field>
                </div>
                <fieldset className="radio-cards" style={{ marginTop: 14 }}>
                  <legend>Start a deploy</legend>
                  {trigger("manual", "By hand", "Only when you select Deploy now.")}
                  {trigger("webhook", "Webhook", "The Git host calls the panel on each push to the branch.")}
                  {trigger("schedule", "Schedule", "The panel checks the branch for new commits. No setup on the Git host.")}
                </fieldset>
                {editing!.trigger === "schedule" && (
                  <div style={{ marginTop: 14 }}>
                    <Field label="Check the branch every" error={field("checkMinutes")}>
                      {(p) => (
                        <select {...p} className="select" value={editing!.checkMinutes} onChange={(e) => setForm({ ...editing!, checkMinutes: Number(e.target.value) })}>
                          {intervals.map((n) => <option key={n} value={n}>{minutes(n)}</option>)}
                        </select>
                      )}
                    </Field>
                  </div>
                )}
                <div style={{ marginTop: 14 }}>
                  <Field label="Build commands" hint="One command on each line, for example composer install --no-dev or npm ci. Up to 10 commands." error={save.error ? Object.entries(save.error.fields).find(([k]) => k.startsWith("buildCommands"))?.[1] : undefined}>
                    {(p) => <textarea {...p} className="textarea mono" rows={3} value={commands} onChange={(e) => setCommands(e.target.value)} spellCheck={false} />}
                  </Field>
                </div>
                <div style={{ marginTop: 14 }}>
                  <Field label="Migration command" hint={<>Optional. For example <code>php artisan migrate --force</code>. It runs after the build, before the switch. After a migration, the panel does not go back automatically when the health check fails.</>} error={field("migrationCommand")}>
                    {(p) => <input {...p} className="input mono" value={editing!.migrationCommand} onChange={(e) => setForm({ ...editing!, migrationCommand: e.target.value })} spellCheck={false} autoCapitalize="none" />}
                  </Field>
                </div>
                <div className="btn-group" style={{ marginTop: 14 }}>
                  <button type="submit" className="btn btn-primary" disabled={save.busy || !editing!.repository.trim()}>{source ? "Save" : "Connect repository"}</button>
                  {source && <button type="button" className="btn" onClick={() => { setForm(null); save.clear(); }}>Cancel</button>}
                  {source && (
                    <ConfirmButton className="btn btn-danger" title="Disconnect the repository?" confirm="Disconnect" disabled={running} message={<p>The website keeps its live release. The panel removes the deploy key and the secrets. A push does not start a deploy.</p>} onConfirm={() => void disconnect.run()}>
                      Disconnect
                    </ConfirmButton>
                  )}
                </div>
              </form>
            )}
          </div>
        </Card>
        {source && (
          <Card title="Access to the repository">
            <div className="card-body">
              <p className="muted" style={{ marginBottom: 10 }}>For a private repository, add this deploy key to the repository on your Git host. Give it read access only.</p>
              <CopyField value={source.deployKey} label="Deploy key" />
              {source.trigger === "webhook" && source.webhookUrl && source.webhookSecret && (
                <>
                  <p className="muted" style={{ margin: "16px 0 10px" }}>
                    Add a webhook for push events on your Git host, with this address and secret. Select the content type <code>application/json</code>. On GitLab, type the secret in <strong>Secret token</strong>.
                    The panel refuses a request with no correct signature.
                  </p>
                  <div className="label-like" aria-hidden="true">Address</div>
                  <CopyField value={source.webhookUrl} label="Webhook address" />
                  <div className="label-like" aria-hidden="true" style={{ marginTop: 12 }}>Secret</div>
                  <CopyField value={source.webhookSecret} label="Webhook secret" />
                  <FormError error={rotate.error} />
                  <div style={{ marginTop: 10 }}>
                    <ConfirmButton
                      className="btn btn-sm"
                      title="Make a new webhook address?"
                      confirm="Make new address"
                      disabled={rotate.busy}
                      message={<p>The old address and secret stop at once. Give the new address and secret to your Git host.</p>}
                      onConfirm={() => void rotate.run()}
                    >
                      New address and secret
                    </ConfirmButton>
                  </div>
                </>
              )}
            </div>
          </Card>
        )}
        {source && (
          <Card title="Build secrets">
            <div className="card-body">
              <p className="muted">The panel keeps each secret encrypted, and gives it to the build commands and the migration command as an environment variable. The panel does not show a value again.</p>
              <FormError error={dropSecret.error ?? (addSecret.error && !addSecret.error.fields.name && !addSecret.error.fields.value ? addSecret.error : null)} />
              <ul className="tag-list" style={{ margin: "12px 0" }}>
                {source.secrets.map((s) => (
                  <li key={s}>
                    <span className="tag"><code>{s}</code></span>{" "}
                    <button type="button" className="btn-link" disabled={dropSecret.busy} onClick={() => void dropSecret.run(s)}>Remove<span className="visually-hidden"> {s}</span></button>
                  </li>
                ))}
                {source.secrets.length === 0 && <li className="muted">No secrets.</li>}
              </ul>
              <form className="inline-form" noValidate onSubmit={(e) => { e.preventDefault(); void addSecret.run(); }}>
                <Field label="Name" error={addSecret.error?.fields.name}>
                  {(p) => <input {...p} className="input mono" value={secret.name} onChange={(e) => setSecret({ ...secret, name: e.target.value.toUpperCase() })} placeholder="NPM_TOKEN" spellCheck={false} autoCapitalize="characters" />}
                </Field>
                <Field label="Value" error={addSecret.error?.fields.value}>
                  {(p) => <input {...p} className="input" type="password" autoComplete="off" value={secret.value} onChange={(e) => setSecret({ ...secret, value: e.target.value })} />}
                </Field>
                <div className="field"><span className="label" aria-hidden="true">&nbsp;</span><button type="submit" className="btn" disabled={addSecret.busy || !secret.name.trim() || !secret.value}>Set secret</button></div>
              </form>
            </div>
          </Card>
        )}
      </div>
      <div className="stack">
        {attention && (
          <div className="alert alert-warning alert-stack" role="alert">
            <p><strong>The live release failed its health check after a migration.</strong></p>
            <p>The panel did not go back to the previous release, because the previous release possibly does not operate with the changed database. Examine the logs of the website, and push a correction. To go back, first restore the database from a backup.</p>
            <p className="cause">{attention.healthError}</p>
          </div>
        )}
        {jobs.length > 0 && (
          <Card title="Deploy jobs">
            <div className="card-body"><ul className="job-list">{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul></div>
          </Card>
        )}
        <Card title="Releases">
          <div className="card-body">
            <p className="muted small">
              Each deploy goes into a new release directory. The server starts the release on a copy of the website, does a health check, and then switches. Then it checks the website for 5 minutes.
              If a step or this check fails, the website goes back to the live release. The server keeps the live release and the last 4 releases before it, so that you can go back.
            </p>
          </div>
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Release</th><th scope="col">Result</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {releases.map((r) => (
                  <tr key={r.id}>
                    <td>
                      {r.commit ? <code>{r.commit.slice(0, 7)}</code> : <code>{r.branch}</code>} {r.message}
                      <span className="cell-sub">
                        {relativeTime(r.createdAt)}{r.durationSeconds !== null ? ` · ${r.durationSeconds} s` : ""} · {STARTED_BY[r.trigger] ?? STARTED_BY.manual}{r.migration ? " · ran the migration" : ""}
                      </span>
                      {r.state === "failed" && r.error && <span className="cell-sub text-danger">{r.error}</span>}
                      {r.healthError && <span className="cell-sub text-warning">{r.healthError} The website kept this release because of the migration.</span>}
                    </td>
                    <td>
                      <ReleaseState release={r} />
                      {r.healthError && <div style={{ marginTop: 4 }}><Status tone="warning">Check failed</Status></div>}
                    </td>
                    <td className="cell-actions" style={{ width: 130 }}>
                      {!r.live && r.state === "ready" && (
                        <ConfirmButton
                          className="btn btn-sm"
                          danger={false}
                          disabled={running || activate.busy || website.status === "failed"}
                          title={`Go back to ${r.commit ? r.commit.slice(0, 7) : "this release"}?`}
                          confirm="Go back"
                          message={<p>The server starts this release on a copy of the website, does a health check, and then switches. The panel does not change the databases. Make sure that the code of this release works with the current databases.</p>}
                          onConfirm={() => void activate.run(r.id)}
                        >
                          Go back<span className="visually-hidden"> to {r.commit ? r.commit.slice(0, 7) : r.id}</span>
                        </ConfirmButton>
                      )}
                    </td>
                  </tr>
                ))}
                {releases.length === 0 && <tr><td colSpan={3} className="muted">No releases yet.{source ? " Select Deploy now to make the first release." : ""}</td></tr>}
              </tbody>
            </table>
          </TableWrap>
        </Card>
      </div>
    </div>
  );
}
