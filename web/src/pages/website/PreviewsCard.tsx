import { ArrowUpRight } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { useAction, useLoad, useStore } from "../../api/hooks";
import { makePreview, readPreviews, removePreview, setPreviewSettings } from "../../api/store";
import type { PreviewSettingsInput } from "../../api/store";
import type { GitSource, Preview, PreviewState, Website } from "../../api/types";
import { Card, ConfirmButton, Field, FormError, Status, TableWrap } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { relativeTime } from "../../lib/format";
import { CopyField } from "./AccessTab";

const STATE: Record<PreviewState, { tone: Tone; label: string }> = {
  creating: { tone: "info", label: "Making the website" },
  deploying: { tone: "info", label: "Deploying" },
  live: { tone: "success", label: "Live" },
  failed: { tone: "danger", label: "Failed" },
  removing: { tone: "neutral", label: "Removing" },
};

function External({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noreferrer">
      {children} <ArrowUpRight size={14} aria-hidden="true" />
      <span className="visually-hidden"> (opens in a new tab)</span>
    </a>
  );
}

/**
 * The previews of the pull requests of a website that deploys from Git. Each preview is a
 * website of the plan, with the name pr-<number>.<domain>. The webhook of the website makes,
 * deploys and removes the previews.
 */
export function PreviewsCard({ website, source }: { website: Website; source: GitSource }) {
  const state = useStore();
  const id = website.id;
  // A job of a preview website changes the list, so the data loads again.
  const sites = state.websites.filter((w) => w.previewOf?.websiteId === id).map((w) => w.id);
  const jobs = state.jobs.filter((j) => j.websiteId && sites.includes(j.websiteId)).map((j) => `${j.id}:${j.state}`).join();
  const data = useLoad(() => readPreviews(id), [id, sites.join(), jobs]);
  const [form, setForm] = useState<PreviewSettingsInput | null>(null);
  const [manual, setManual] = useState<{ number: string; branch: string } | null>(null);
  const save = useAction(async (input: PreviewSettingsInput) => {
    data.set(await setPreviewSettings(id, input));
    setForm(null);
  });
  const make = useAction(async (number: number, branch: string) => {
    await makePreview(id, { number, branch });
    setManual(null);
    data.reload();
  });
  const remove = useAction((number: number) => removePreview(id, number).then(data.reload));

  if (!data.data) {
    return <Card title="Previews of pull requests"><div className="card-body">{data.error ? <FormError error={data.error} /> : <p className="muted" role="status">Loading…</p>}</div></Card>;
  }
  const { settings, suggestedDomain, maxLimit, previews } = data.data;
  if (!settings || website.kind !== "public") return null;
  const domain = settings.domain ?? suggestedDomain ?? "";
  const editing = form;
  const field = (key: string) => save.error?.fields[key];
  const edit = (enabled: boolean) => {
    save.clear();
    setForm({ enabled, domain, limit: settings.limit, copyEnvironment: settings.copyEnvironment });
  };

  return (
    <Card
      title="Previews of pull requests"
      actions={!editing && (settings.enabled
        ? <button type="button" className="btn" onClick={() => edit(true)}>Change</button>
        : <button type="button" className="btn btn-primary" onClick={() => edit(true)}>Turn on previews</button>)}
    >
      <div className="card-body">
        <FormError error={remove.error ?? (save.error && !Object.keys(save.error.fields).some((k) => ["domain", "limit"].includes(k)) ? save.error : null)} />
        {editing ? (
          <form noValidate onSubmit={(e) => { e.preventDefault(); void save.run(editing); }}>
            <p className="muted" style={{ marginBottom: 14 }}>
              The panel makes a preview website for each pull request to <code>{source.branch}</code>, deploys it again for each new commit, and removes it when the pull request closes.
              Each preview is a website of your plan, on the server of this website.
            </p>
            <Field label="Domain of the previews" hint={<>Each preview gets the name <code>pr-NUMBER.{editing.domain || "DOMAIN"}</code>. Use a sub-domain of a verified domain of this website, for example <code>{suggestedDomain}</code>.</>} error={field("domain")}>
              {(p) => <input {...p} className="input mono" value={editing.domain} onChange={(e) => setForm({ ...editing, domain: e.target.value.toLowerCase() })} placeholder={suggestedDomain ?? ""} spellCheck={false} autoCapitalize="none" />}
            </Field>
            <div style={{ marginTop: 14 }}>
              <Field label="Previews at the same time" hint={`From 1 to ${maxLimit}. A new pull request gets no preview when this number of previews exists.`} error={field("limit")}>
                {(p) => <input {...p} className="input" type="number" min={1} max={maxLimit} style={{ maxWidth: 120 }} value={editing.limit} onChange={(e) => setForm({ ...editing, limit: Number(e.target.value) })} />}
              </Field>
            </div>
            <label className="check" style={{ marginTop: 14 }}>
              <input type="checkbox" checked={editing.copyEnvironment} onChange={(e) => setForm({ ...editing, copyEnvironment: e.target.checked })} />
              Give the environment variables and the build secrets of this website to the previews
            </label>
            <p className="hint">Turn this on only when the variables do not give access to live data, for example to the live database. The code of a pull request can read them.</p>
            <div className="btn-group" style={{ marginTop: 14 }}>
              <button type="submit" className="btn btn-primary" disabled={save.busy || !editing.domain.trim()}>{settings.enabled ? "Save" : "Turn on previews"}</button>
              <button type="button" className="btn" onClick={() => { setForm(null); save.clear(); }}>Cancel</button>
              {settings.enabled && (
                <ConfirmButton
                  className="btn btn-danger"
                  title="Turn off the previews?"
                  confirm="Turn off"
                  message={<p>The panel removes {previews.length === 1 ? "the preview website" : `the ${previews.length} preview websites`} with their files. A pull request does not make a preview.</p>}
                  onConfirm={() => void save.run({ ...editing, enabled: false })}
                >
                  Turn off
                </ConfirmButton>
              )}
            </div>
          </form>
        ) : settings.enabled ? (
          <>
            <table className="kv">
              <tbody>
                <tr><th scope="row">Names</th><td><code>pr-NUMBER.{settings.domain}</code><span className="cell-sub">Add a DNS record <code>*.{settings.domain}</code> that points to the server of this website.</span></td></tr>
                <tr><th scope="row">Limit</th><td>{settings.limit === 1 ? "1 preview" : `${settings.limit} previews`} at the same time</td></tr>
                <tr><th scope="row">Variables</th><td>{settings.copyEnvironment ? "The previews get the environment variables and the build secrets of this website." : "The previews do not get the environment variables and the build secrets of this website."}</td></tr>
              </tbody>
            </table>
            <p className="muted small" style={{ margin: "14px 0 10px" }}>
              On your Git host, send the pull request events (merge request events on GitLab) to the webhook address. The panel ignores a pull request from a fork.
            </p>
            {source.webhookUrl && <CopyField value={source.webhookUrl} label="Webhook address" />}
          </>
        ) : (
          <p className="muted">Get a preview website for each pull request to <code>{source.branch}</code>. The panel makes the preview when the pull request opens, deploys each new commit, and removes the preview when the pull request closes.</p>
        )}
      </div>
      {settings.enabled && (
        <>
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Pull request</th><th scope="col">Preview</th><th scope="col">State</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {previews.map((p) => <PreviewRow key={p.number} preview={p} busy={remove.busy} onRemove={() => void remove.run(p.number)} />)}
                {previews.length === 0 && <tr><td colSpan={4} className="muted">No previews. Open a pull request to <code>{source.branch}</code>.</td></tr>}
              </tbody>
            </table>
          </TableWrap>
          <div className="card-body">
            {manual ? (
              <form className="inline-form" noValidate onSubmit={(e) => { e.preventDefault(); void make.run(Number(manual.number), manual.branch); }}>
                <Field label="Pull request" error={make.error?.fields.number}>
                  {(p) => <input {...p} className="input" inputMode="numeric" style={{ maxWidth: 110 }} value={manual.number} onChange={(e) => setManual({ ...manual, number: e.target.value.replace(/\D/g, "") })} placeholder="12" />}
                </Field>
                <Field label="Branch" error={make.error?.fields.branch}>
                  {(p) => <input {...p} className="input mono" value={manual.branch} onChange={(e) => setManual({ ...manual, branch: e.target.value })} placeholder="feature/new-menu" spellCheck={false} autoCapitalize="none" />}
                </Field>
                <div className="field"><span className="label" aria-hidden="true">&nbsp;</span>
                  <div className="btn-group">
                    <button type="submit" className="btn btn-primary" disabled={make.busy || !manual.number || !manual.branch.trim()}>Make preview</button>
                    <button type="button" className="btn" onClick={() => { setManual(null); make.clear(); }}>Cancel</button>
                  </div>
                </div>
              </form>
            ) : (
              <button type="button" className="btn-link" onClick={() => setManual({ number: "", branch: "" })}>Make a preview by hand</button>
            )}
            {make.error && !make.error.fields.number && !make.error.fields.branch && <FormError error={make.error} />}
          </div>
        </>
      )}
    </Card>
  );
}

function PreviewRow({ preview: p, busy, onRemove }: { preview: Preview; busy: boolean; onRemove: () => void }) {
  const state = STATE[p.state];
  const title = p.title || `Pull request #${p.number}`;
  return (
    <tr>
      <td>
        <strong>#{p.number}</strong> {p.url ? <External href={p.url}>{title}</External> : title}
        <span className="cell-sub"><code>{p.branch}</code></span>
      </td>
      <td>
        {p.websiteId && p.name ? (
          <>
            <Link to={`/websites/${p.websiteId}`}>{p.name}</Link>
            {p.state === "live" && <span className="cell-sub"><External href={`https://${p.name}`}>Visit preview</External></span>}
          </>
        ) : <span className="muted">No website</span>}
      </td>
      <td>
        <Status tone={state.tone}>{state.label}</Status>
        {p.release && <span className="cell-sub">{p.release.commit ? <code>{p.release.commit.slice(0, 7)}</code> : null} {relativeTime(p.release.createdAt)}</span>}
        {p.error && <span className="cell-sub text-danger">{p.error}</span>}
        {!p.error && p.release?.state === "failed" && p.release.error && <span className="cell-sub text-danger">{p.release.error}</span>}
      </td>
      <td className="cell-actions" style={{ width: 110 }}>
        {p.state !== "removing" && (
          <ConfirmButton
            className="btn btn-sm"
            title={`Remove the preview of #${p.number}?`}
            confirm="Remove"
            disabled={busy}
            message={<p>The panel removes the preview website with its files. The next commit in the pull request makes a new preview.</p>}
            onConfirm={onRemove}
          >
            Remove<span className="visually-hidden"> the preview of #{p.number}</span>
          </ConfirmButton>
        )}
      </td>
    </tr>
  );
}

/** On a preview website: the pull request, and where its deploy settings come from. */
export function PreviewOfNotice({ website }: { website: Website }) {
  const state = useStore();
  const of = website.previewOf;
  if (!of) return null;
  const base = state.websites.find((w) => w.id === of.websiteId);
  return (
    <div className="alert alert-info alert-stack" role="note">
      <p>
        <strong>This website is the preview of pull request #{of.number}</strong>{of.title ? <>: {of.url ? <External href={of.url}>{of.title}</External> : of.title}</> : null}.
        It deploys the branch <code>{of.branch}</code>.
      </p>
      <p>
        The panel takes the deploy settings from {base ? <Link to={`/websites/${base.id}/deploys`}>{base.name}</Link> : "the website of the pull request"}, deploys each new commit, and removes this website when the pull request closes.
      </p>
    </div>
  );
}
