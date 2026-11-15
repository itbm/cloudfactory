import { useState } from "react";
import { useAction, useStore } from "../../api/hooks";
import { imageOf, supportEnds } from "../../api/selectors";
import { changeRuntime, installDependencies, revertRuntime, setCommand, setDocumentRoot, setEnvironment, setInstallCommand, setPhpSettings, setWorkerMode } from "../../api/store";
import type { Website } from "../../api/types";
import { Card, Field, FormError, Toggle } from "../../components/ui";
import { longDate } from "../../lib/format";
import { jobActive } from "../../lib/labels";

export function RuntimeTab({ website }: { website: Website }) {
  const state = useStore();
  const current = imageOf(state, website);
  const isPhp = website.runtime.kind === "frankenphp" || website.runtime.kind === "apache-php-fpm";
  // A Node.js or Python website runs its start command.
  const isApp = website.runtime.kind === "node" || website.runtime.kind === "python";
  const language = website.runtime.kind === "node" ? "Node.js" : "Python";
  const choices = state.images.filter((i) => (isPhp ? i.family === "frankenphp" || i.family === "apache-php-fpm" : i.family === current?.family));
  const [imageId, setImageId] = useState(website.runtime.imageId);
  const plan = state.plans.find((p) => p.id === website.planId);
  const [extensions, setExtensions] = useState(website.runtime.extensions);
  const [ini, setIni] = useState(Object.entries(website.runtime.phpIni).map(([k, v]) => `${k} = ${v}`).join("\n"));
  const change = useAction(changeRuntime);
  const revert = useAction(revertRuntime);
  const worker = useAction(setWorkerMode);
  const [command, setCommandText] = useState(website.command ?? "");
  const start = useAction(setCommand);
  const [install, setInstallText] = useState(website.installCommand ?? "");
  const saveInstall = useAction(setInstallCommand);
  const runInstall = useAction(installDependencies);
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
  const busy = state.jobs.some((j) => j.websiteId === website.id && jobActive(j) && (j.title.startsWith("Change runtime") || j.title.startsWith("Install dependencies")));
  const end = supportEnds(state, website);

  return (
    <div className="grid-2">
      <div className="stack">
        <Card title={isPhp ? "PHP version" : isApp ? `${language} version` : "Image"}>
          <form className="card-body" onSubmit={(e) => { e.preventDefault(); void change.run(website.id, imageId); }}>
            <FormError error={change.error ?? revert.error} />
            {end && <div className="alert alert-warning"><p>Security support for {isApp ? `${language} ${website.runtime.version ?? ""}`.trim() : `PHP ${website.runtime.phpVersion}`} ends on {longDate(end)}. Change to a newer version.</p></div>}
            <fieldset className="radio-cards" style={{ marginTop: 12 }}>
              <legend className="visually-hidden">Runtime image</legend>
              {choices.map((i) => (
                <label key={i.id} className="radio-card">
                  <input type="radio" name="image" checked={imageId === i.id} onChange={() => setImageId(i.id)} />
                  <span>
                    <strong>{i.name}</strong>
                    <span>
                      {i.id === website.runtime.imageId ? "Current · " : ""}{isApp ? `Image ${i.running}` : i.running}
                      {i.supportEndsAt && <span className="note-warning" style={{ display: "block" }}>Support ends {longDate(i.supportEndsAt)}</span>}
                    </span>
                  </span>
                </label>
              ))}
            </fieldset>
            <p className="hint" style={{ marginTop: 10 }}>{isApp && website.installCommand ? "The panel runs the install command with the new version first. " : ""}The panel starts the new image on a copy, does a health check, and then switches. It records the previous image so that you can go back in one action.</p>
            <div className="btn-group" style={{ marginTop: 14 }}>
              <button type="submit" className="btn btn-primary" disabled={imageId === website.runtime.imageId || busy}>{busy ? "Changing…" : "Change runtime"}</button>
              {prev && <button type="button" className="btn" disabled={busy} onClick={() => void revert.run(website.id)}>Go back to {prev.name}</button>}
            </div>
          </form>
        </Card>
        {(website.kind === "worker" || isApp) && (
          <Card title="Start command">
            <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void start.run(website.id, command); }}>
              <FormError error={start.error} />
              <Field label={website.kind === "worker" ? "Command that the worker runs" : "Command that starts the app"} hint={<>One command line. It runs in the container, in the gVisor sandbox, in the application directory. {website.kind === "worker" ? "" : <>The app must listen on the port in the variable <code>PORT</code> (8080). </>}When the command stops, the panel starts it again.</>} error={start.error?.fields.command}>
                {(p) => <input {...p} className="input mono" value={command} onChange={(e) => setCommandText(e.target.value)} spellCheck={false} autoCapitalize="none" />}
              </Field>
              <button type="submit" className="btn" style={{ marginTop: 12 }} disabled={start.busy || busy || !command.trim() || command.trim() === (website.command ?? "")}>Save start command</button>
            </form>
          </Card>
        )}
        {isApp && (
          <Card title="Dependencies">
            <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void saveInstall.run(website.id, install); }}>
              <FormError error={saveInstall.error ?? runInstall.error} />
              <Field label="Install command" hint={<>Runs in a build container with the files of the website, for example <code>{website.runtime.kind === "node" ? "npm ci --omit=dev" : "pip install -r requirements.txt"}</code>. Each Git deploy runs it before the build commands. Leave it empty for no install step.</>} error={saveInstall.error?.fields.installCommand ?? runInstall.error?.fields.installCommand}>
                {(p) => <input {...p} className="input mono" value={install} onChange={(e) => setInstallText(e.target.value)} spellCheck={false} autoCapitalize="none" />}
              </Field>
              <p className="hint" style={{ marginTop: 10 }}>After you upload new files with SFTP, select <strong>Install dependencies</strong>. The panel runs the install command, starts the app on a copy, does a health check, and then switches. If the command fails, the website does not change.</p>
              <div className="btn-group" style={{ marginTop: 12 }}>
                <button type="submit" className="btn" disabled={saveInstall.busy || busy || install.trim() === (website.installCommand ?? "")}>Save install command</button>
                <button type="button" className="btn btn-primary" disabled={runInstall.busy || busy || !website.installCommand || install.trim() !== (website.installCommand ?? "")} onClick={() => void runInstall.run(website.id)}>{busy ? "Installing…" : "Install dependencies"}</button>
              </div>
            </form>
          </Card>
        )}
        {website.runtime.kind !== "oci" && (website.kind !== "worker" || isApp) && (
          <Card title={isApp ? "Application directory" : "Document root"}>
            <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void docRoot.run(website.id, root); }}>
              <FormError error={docRoot.error} />
              <Field label={isApp ? "Directory in which the commands run" : "Directory that the web server serves"} hint={isApp ? <>A directory in the files of the website, with the <code>{website.runtime.kind === "node" ? "package.json" : "requirements.txt"}</code> file. Type <code>.</code> for the top directory.</> : <>A directory in the files of the website, for example <code>public</code> for Laravel or <code>web</code>. Type <code>.</code> for the top directory, for example for WordPress.</>} error={docRoot.error?.fields.documentRoot}>
                {(p) => <input {...p} className="input mono" value={root} onChange={(e) => setRoot(e.target.value)} spellCheck={false} autoCapitalize="none" />}
              </Field>
              <button type="submit" className="btn" style={{ marginTop: 12 }} disabled={docRoot.busy || busy || root.trim() === (website.runtime.documentRoot ?? "public")}>{isApp ? "Save application directory" : "Save document root"}</button>
            </form>
          </Card>
        )}
        {website.runtime.kind === "frankenphp" && website.kind !== "worker" && (
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
      <div className="stack">
      {website.runtime.kind !== "static" && <EnvironmentCard website={website} busy={busy} />}
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
    </div>
  );
}

/**
 * The environment variables of a website. The panel keeps the values encrypted and never shows
 * them again: the card lists the names only. A change starts a new container with no stop.
 */
function EnvironmentCard({ website, busy }: { website: Website; busy: boolean }) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const save = useAction(setEnvironment);
  const remove = useAction(setEnvironment);
  const exists = website.environment.includes(name.trim());
  return (
    <Card title="Environment variables">
      <div className="card-body">
        <FormError error={save.error ?? remove.error} />
        <p className="hint">The app gets these variables when it starts, and the install command and the build commands get them too. The panel keeps the values encrypted and does not show them again. The panel sets <code>PORT</code>, <code>HOST</code> and <code>HOME</code>.</p>
        {website.environment.length > 0 ? (
          <ul style={{ marginTop: 12, listStyle: "none", padding: 0 }} aria-label="Variables">
            {website.environment.map((n) => (
              <li key={n} style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, padding: "6px 0", borderBottom: "1px solid var(--border)" }}>
                <span><code>{n}</code> <span className="muted">= ••••••</span></span>
                <button type="button" className="btn btn-sm" disabled={remove.busy || busy} onClick={() => void remove.run(website.id, { [n]: null })} aria-label={`Remove ${n}`}>Remove</button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted" style={{ marginTop: 12 }}>No variables.</p>
        )}
        <form noValidate style={{ marginTop: 14 }} onSubmit={async (e) => {
          e.preventDefault();
          if ((await save.run(website.id, { [name.trim()]: value })).ok) {
            setName("");
            setValue("");
          }
        }}>
          <div className="form-row">
            <Field label="Name" error={save.error ? Object.entries(save.error.fields).find(([k]) => k.startsWith("variables"))?.[1] : undefined}>
              {(p) => <input {...p} className="input mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="DATABASE_URL" spellCheck={false} autoCapitalize="none" autoComplete="off" />}
            </Field>
            <Field label="Value">
              {(p) => <input {...p} className="input mono" value={value} onChange={(e) => setValue(e.target.value)} spellCheck={false} autoCapitalize="none" autoComplete="off" />}
            </Field>
          </div>
          <button type="submit" className="btn" style={{ marginTop: 12 }} disabled={save.busy || busy || !name.trim()}>{exists ? "Replace value" : "Add variable"}</button>
        </form>
      </div>
    </Card>
  );
}
