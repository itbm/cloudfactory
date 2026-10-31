import { useState } from "react";
import { useAction, useStore } from "../../api/hooks";
import { imageOf, supportEnds } from "../../api/selectors";
import { changeRuntime, revertRuntime, setDocumentRoot, setPhpSettings, setWorkerMode } from "../../api/store";
import type { Website } from "../../api/types";
import { Card, Field, FormError, Toggle } from "../../components/ui";
import { longDate } from "../../lib/format";

export function RuntimeTab({ website }: { website: Website }) {
  const state = useStore();
  const current = imageOf(state, website);
  const isPhp = website.runtime.kind === "frankenphp" || website.runtime.kind === "apache-php-fpm";
  const choices = state.images.filter((i) => (isPhp ? i.family === "frankenphp" || i.family === "apache-php-fpm" : i.family === current?.family));
  const [imageId, setImageId] = useState(website.runtime.imageId);
  const plan = state.plans.find((p) => p.id === website.planId);
  const [extensions, setExtensions] = useState(website.runtime.extensions);
  const [ini, setIni] = useState(Object.entries(website.runtime.phpIni).map(([k, v]) => `${k} = ${v}`).join("\n"));
  const change = useAction(changeRuntime);
  const revert = useAction(revertRuntime);
  const worker = useAction(setWorkerMode);
  const [root, setRoot] = useState(website.runtime.documentRoot ?? "public");
  const docRoot = useAction(setDocumentRoot);
  const settings = useAction(async () => {
    const parsed: Record<string, string> = {};
    for (const line of ini.split("\n").map((l) => l.trim()).filter((l) => l && !l.startsWith(";"))) {
      const [k, ...rest] = line.split("=");
      parsed[k.trim()] = rest.join("=").trim();
    }
    return setPhpSettings(website.id, extensions, parsed);
  });
  const prev = website.runtime.previous && state.images.find((i) => i.id === website.runtime.previous!.imageId);
  const busy = state.jobs.some((j) => j.websiteId === website.id && j.state === "running" && j.title.startsWith("Change runtime"));
  const end = supportEnds(state, website);

  return (
    <div className="grid-2">
      <div className="stack">
        <Card title={isPhp ? "PHP version" : "Image"}>
          <form className="card-body" onSubmit={(e) => { e.preventDefault(); void change.run(website.id, imageId); }}>
            <FormError error={change.error ?? revert.error} />
            {end && <div className="alert alert-warning"><p>Security support for PHP {website.runtime.phpVersion} ends on {longDate(end)}. Change to a newer version.</p></div>}
            <fieldset className="radio-cards" style={{ marginTop: 12 }}>
              <legend className="visually-hidden">Runtime image</legend>
              {choices.map((i) => (
                <label key={i.id} className="radio-card">
                  <input type="radio" name="image" checked={imageId === i.id} onChange={() => setImageId(i.id)} />
                  <span>
                    <strong>{i.name}</strong>
                    <span>
                      {i.id === website.runtime.imageId ? "Current · " : ""}{i.running}
                      {i.supportEndsAt && <span className="note-warning" style={{ display: "block" }}>Support ends {longDate(i.supportEndsAt)}</span>}
                    </span>
                  </span>
                </label>
              ))}
            </fieldset>
            <p className="hint" style={{ marginTop: 10 }}>The panel starts the new image on a copy, does a health check, and then switches. It keeps the previous image so that you can go back in one action.</p>
            <div className="btn-group" style={{ marginTop: 14 }}>
              <button type="submit" className="btn btn-primary" disabled={imageId === website.runtime.imageId || busy}>{busy ? "Changing…" : "Change runtime"}</button>
              {prev && <button type="button" className="btn" disabled={busy} onClick={() => void revert.run(website.id)}>Go back to {prev.name}</button>}
            </div>
          </form>
        </Card>
        {website.runtime.kind !== "oci" && (
          <Card title="Document root">
            <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void docRoot.run(website.id, root); }}>
              <FormError error={docRoot.error} />
              <Field label="Directory that the web server serves" hint={<>A directory in the files of the website, for example <code>public</code> for Laravel or <code>web</code>. Type <code>.</code> for the top directory, for example for WordPress.</>} error={docRoot.error?.fields.documentRoot}>
                {(p) => <input {...p} className="input mono" value={root} onChange={(e) => setRoot(e.target.value)} spellCheck={false} autoCapitalize="none" />}
              </Field>
              <button type="submit" className="btn" style={{ marginTop: 12 }} disabled={docRoot.busy || busy || root.trim() === (website.runtime.documentRoot ?? "public")}>Save document root</button>
            </form>
          </Card>
        )}
        {website.runtime.kind === "frankenphp" && (
          <Card title="Worker mode">
            <div className="card-body">
              <FormError error={worker.error} />
              <Toggle checked={website.runtime.workerMode} label="Worker mode" onChange={(v) => void worker.run(website.id, v)}>
                {website.runtime.workerMode ? "On" : "Off"}. Use only for apps built for it, such as Laravel Octane or Symfony Runtime.
              </Toggle>
              <p className="hint" style={{ marginTop: 10 }}>Worker mode keeps the app in memory between requests. It is off by default.</p>
            </div>
          </Card>
        )}
      </div>
      {isPhp && (
        <Card title="Extensions and php.ini">
          <form className="card-body" onSubmit={(e) => { e.preventDefault(); void settings.run(); }}>
            <FormError error={settings.error} />
            <fieldset className="checkbox-list">
              <legend>Extensions in the {plan?.name ?? "website"} plan</legend>
              {(plan?.phpExtensions ?? website.runtime.extensions).map((ext) => (
                <label className="check" key={ext}>
                  <input type="checkbox" checked={extensions.includes(ext)} onChange={(e) => setExtensions(e.target.checked ? [...extensions, ext] : extensions.filter((x) => x !== ext))} />
                  <code>{ext}</code>
                </label>
              ))}
            </fieldset>
            <div style={{ marginTop: 14 }}>
              <Field label="php.ini values" hint="One setting on each line, for example memory_limit = 256M." error={settings.error ? Object.values(settings.error.fields)[0] : undefined}>
                {(p) => <textarea {...p} className="textarea" value={ini} onChange={(e) => setIni(e.target.value)} rows={6} spellCheck={false} />}
              </Field>
            </div>
            <button type="submit" className="btn btn-primary" style={{ marginTop: 14 }}>Save PHP settings</button>
          </form>
        </Card>
      )}
    </div>
  );
}
