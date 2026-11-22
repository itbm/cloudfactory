import { useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { activateRelease, readDeploy, removeDeploySecret, removeGitSource, setDeploySecret, setGitSource, startDeploy } from "../../api/store";
import type { GitSourceInput } from "../../api/store";
import type { GitSource, Release, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, JobRow, Status, TableWrap } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { jobActive } from "../../lib/labels";
import { CopyField } from "./AccessTab";
import { PreviewOfNotice, PreviewsCard } from "./PreviewsCard";

const EMPTY: GitSourceInput = { repository: "", branch: "main", webDirectory: "public", trigger: "manual", buildCommands: [] };
const DEPLOY_ACTIONS = ["Deploy", "ActivateRelease"];

const formOf = (s: GitSource): GitSourceInput => ({ repository: s.repository, branch: s.branch, webDirectory: s.webDirectory, trigger: s.trigger, buildCommands: s.buildCommands });

function ReleaseState({ release }: { release: Release }) {
  if (release.live) return <Status tone="success">Live</Status>;
  if (release.state === "building") return <Status tone="info">Deploying</Status>;
  if (release.state === "failed") return <Status tone="danger">Failed</Status>;
  if (release.state === "removed") return <Status tone="neutral">Removed</Status>;
  return <Status tone="neutral">Kept</Status>;
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
  // A preview takes its deploy settings from the website of the pull request.
  const preview = Boolean(website.previewOf);

  return (
    <div className="stack">
      <div className="grid-2">
        <div className="stack">
          <PreviewOfNotice website={website} />
          <Card
            title="Git repository"
            actions={source && !editing && (
              <div className="btn-group">
                {!preview && <button type="button" className="btn" onClick={() => edit(formOf(source))}>Change</button>}
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
                    <tr><th scope="row">Start</th><td>{source.trigger === "webhook" ? "On each push to the branch, with a webhook. You can also start a deploy here." : "Only when you select Deploy now."}</td></tr>
                    <tr>
                      <th scope="row">Build</th>
                      <td>
                        {source.buildCommands.length ? source.buildCommands.map((c, i) => <div key={i}><code>{c}</code></div>) : "No build commands."}
                        <span className="cell-sub">Runs in a separate build container, not in the live container.</span>
                      </td>
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
                    <label className="radio-card"><input type="radio" name="trigger" checked={editing!.trigger === "manual"} onChange={() => setForm({ ...editing!, trigger: "manual" })} /><span><strong>By hand</strong><span>Only when you select Deploy now.</span></span></label>
                    <label className="radio-card"><input type="radio" name="trigger" checked={editing!.trigger === "webhook"} onChange={() => setForm({ ...editing!, trigger: "webhook" })} /><span><strong>Webhook</strong><span>The Git host calls the panel on each push to the branch.</span></span></label>
                  </fieldset>
                  <div style={{ marginTop: 14 }}>
                    <Field label="Build commands" hint="One command on each line, for example composer install --no-dev or npm ci. Up to 10 commands." error={save.error ? Object.entries(save.error.fields).find(([k]) => k.startsWith("buildCommands"))?.[1] : undefined}>
                      {(p) => <textarea {...p} className="textarea mono" rows={3} value={commands} onChange={(e) => setCommands(e.target.value)} spellCheck={false} />}
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
                {source.trigger === "webhook" && (
                  <>
                    <p className="muted" style={{ margin: "16px 0 10px" }}>Add this address as a webhook for push events on your Git host. Keep it secret: each call to it starts a deploy.</p>
                    <CopyField value={source.webhookUrl} label="Webhook address" />
                  </>
                )}
              </div>
            </Card>
          )}
          {source && !preview && (
            <Card title="Build secrets">
              <div className="card-body">
                <p className="muted">The panel keeps each secret encrypted, and gives it to the build commands as an environment variable. The panel does not show a value again.</p>
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
          {jobs.length > 0 && (
            <Card title="Deploy jobs">
              <div className="card-body"><ul className="job-list">{jobs.map((j) => <JobRow key={j.id} job={j} />)}</ul></div>
            </Card>
          )}
          <Card title="Releases">
            <div className="card-body">
              <p className="muted small">Each deploy goes into a new release directory. The server starts the release on a copy of the website, does a health check, and then switches. If a step fails, the live release continues. The server keeps the live release and the last 4 releases before it, so that you can go back.</p>
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
                          {relativeTime(r.createdAt)}{r.durationSeconds !== null ? ` · ${r.durationSeconds} s` : ""} · {r.trigger === "webhook" ? "started by a push" : "started by hand"}
                        </span>
                        {r.state === "failed" && r.error && <span className="cell-sub text-danger">{r.error}</span>}
                      </td>
                      <td><ReleaseState release={r} /></td>
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
      {/* The table of the previews needs the full width. */}
      {source && !preview && <PreviewsCard website={website} source={source} />}
    </div>
  );
}
