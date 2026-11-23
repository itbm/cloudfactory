import { ArrowUpRight } from "lucide-react";
import { useState } from "react";
import { useAction, useLoad, useStore } from "../../api/hooks";
import {
  checkWordPress,
  installWordPress,
  readWordPress,
  restoreWordPress,
  runWpCli,
  setWordPressUpdates,
  updateWordPress,
  type WordPressInstall,
  type WordPressUpdateInput,
} from "../../api/store";
import type { WordPressAdmin, WordPressCorePolicy, WordPressItem, WordPressSite, WpCliCommand, Website } from "../../api/types";
import { Card, ConfirmButton, Empty, Field, FormError, Live, Modal, Status, TableWrap, Toggle } from "../../components/ui";
import { longDate, relativeTime } from "../../lib/format";
import { jobActive } from "../../lib/labels";
import { CopyField } from "./AccessTab";

/** The languages of a new WordPress site. WordPress has more: the API takes each WordPress locale. */
export const LOCALES: [string, string][] = [
  ["en_GB", "English (UK)"], ["en_US", "English (US)"], ["de_DE", "Deutsch"], ["fr_FR", "Français"], ["es_ES", "Español"], ["it_IT", "Italiano"],
  ["nl_NL", "Nederlands"], ["pt_PT", "Português"], ["pt_BR", "Português do Brasil"], ["pl_PL", "Polski"], ["sv_SE", "Svenska"], ["da_DK", "Dansk"],
  ["nb_NO", "Norsk bokmål"], ["fi", "Suomi"], ["ja", "日本語"],
];

const CORE_POLICY: Record<WordPressCorePolicy, string> = {
  off: "Off",
  minor: "Security and maintenance releases",
  all: "All releases, also new major versions",
};

/** True when WordPress can run on the website: a public website with a PHP runtime. */
export const canHaveWordPress = (website: Website) =>
  website.kind === "public" && (website.runtime.kind === "frankenphp" || website.runtime.kind === "apache-php-fpm");

export const emptyInstall = (): WordPressInstall => ({ title: "", adminUser: "", adminEmail: "", locale: "en_GB" });

/** The fields of a new WordPress site. The API gives the errors as "title" or as "wordpress.title". */
export function WordPressFields({ value, onChange, errors, prefix = "" }: {
  value: WordPressInstall;
  onChange: (v: WordPressInstall) => void;
  errors: Record<string, string>;
  prefix?: string;
}) {
  const set = (k: keyof WordPressInstall) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => onChange({ ...value, [k]: e.target.value });
  return (
    <>
      <Field label="Site title" error={errors[`${prefix}title`]}>
        {(p) => <input {...p} className="input" value={value.title} onChange={set("title")} placeholder="Acme Ltd" autoComplete="off" />}
      </Field>
      <div className="form-row" style={{ margin: "14px 0" }}>
        <Field label="Administrator user name" hint={<>3 to 60 letters, digits and <code>. _ @ -</code>. Do not use admin.</>} error={errors[`${prefix}adminUser`]}>
          {(p) => <input {...p} className="input" value={value.adminUser} onChange={set("adminUser")} autoComplete="off" autoCapitalize="none" spellCheck={false} />}
        </Field>
        <Field label="Administrator email" hint="WordPress sends the password resets to this address." error={errors[`${prefix}adminEmail`]}>
          {(p) => <input {...p} className="input" type="email" value={value.adminEmail} onChange={set("adminEmail")} autoComplete="off" />}
        </Field>
      </div>
      <Field label="Language" error={errors[`${prefix}locale`]}>
        {(p) => (
          <select {...p} className="select" value={value.locale} onChange={set("locale")}>
            {LOCALES.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
          </select>
        )}
      </Field>
    </>
  );
}

/** The sign-in of the first administrator. The API gives the password one time. */
export function AdminCredentials({ admin, onClose, closeLabel = "Done" }: { admin: WordPressAdmin; onClose: () => void; closeLabel?: string }) {
  return (
    <Modal title="WordPress is being installed" onClose={onClose} footer={<button type="button" className="btn btn-primary" onClick={onClose}>{closeLabel}</button>}>
      <p>The panel installs WordPress now. This takes about one minute. Then sign in at <a href={admin.loginUrl} target="_blank" rel="noreferrer">{admin.loginUrl}<span className="visually-hidden"> (opens in a new tab)</span></a>.</p>
      <p><strong>Copy the password now.</strong> The panel shows it only once. You can change it in WordPress.</p>
      <p className="small" style={{ marginTop: 12 }}>User name</p>
      <CopyField value={admin.user} label="Administrator user name" />
      <p className="small" style={{ marginTop: 12 }}>Password</p>
      <CopyField value={admin.password} label="Administrator password" />
    </Modal>
  );
}

/** FR-206: WordPress on the website. One-click install, updates with a restore point, and WP-CLI. */
export function WordPressTab({ website }: { website: Website }) {
  const state = useStore();
  const id = website.id;
  // A job of the website can change WordPress, so the tab loads again when a job ends.
  const active = state.jobs.filter((j) => j.websiteId === id && jobActive(j)).length;
  const load = useLoad(() => readWordPress(id), [id, active]);
  const [admin, setAdmin] = useState<WordPressAdmin | null>(null);

  if (!canHaveWordPress(website)) {
    return (
      <Empty title="WordPress is not available">
        <p>WordPress needs a public website with a PHP runtime. Change the runtime on the Runtime tab.</p>
      </Empty>
    );
  }
  if (!load.data) {
    return load.error ? <FormError error={load.error} /> : <p className="muted" aria-busy="true">Loading…</p>;
  }
  const { wordpress: site, commands, busy } = load.data;
  const running = website.status === "running";

  return (
    <div className="stack">
      {/* A new website or a new staging copy starts: the WordPress card shows the progress. */}
      {!running && website.status !== "starting" && <div className="alert alert-warning" role="status"><span>The website does not run. Start it before you change WordPress.</span></div>}
      {busy && site?.status !== "installing" && site?.status !== "checking" && site?.status !== "copying" && <div className="alert alert-info" role="status"><span>WordPress has a job on this website now. The page shows the result when it is complete.</span><Live /></div>}
      {!site || site.status === "failed" ? (
        <InstallCard website={website} site={site} disabled={!running || !!busy} onInstalled={(a) => { setAdmin(a); load.reload(); }} onChecked={load.reload} />
      ) : site.status === "installing" || site.status === "checking" || site.status === "copying" ? (
        <Card title="WordPress" actions={<Live />}>
          <div className="card-body">
            <p>{PROGRESS[site.status]}</p>
          </div>
        </Card>
      ) : (
        <ReadySite website={website} site={site} disabled={!running || !!busy} reload={load.reload} />
      )}
      {site?.status === "ready" && <WpCliCard website={website} commands={commands} disabled={!running} reload={load.reload} />}
      {admin && <AdminCredentials admin={admin} onClose={() => setAdmin(null)} />}
    </div>
  );
}

const PROGRESS = {
  installing: "The panel installs WordPress. This takes about one minute.",
  checking: "The panel looks for WordPress in the document root of the website.",
  copying: "The panel makes the WordPress copy. It connects WordPress to the database copy, changes the site address to the address of this website, and asks search engines not to index the copy.",
};

function InstallCard({ website, site, disabled, onInstalled, onChecked }: {
  website: Website;
  site: WordPressSite | null;
  disabled: boolean;
  onInstalled: (admin: WordPressAdmin) => void;
  onChecked: () => void;
}) {
  const [input, setInput] = useState(emptyInstall);
  const install = useAction(async () => {
    const r = await installWordPress(website.id, input);
    onInstalled(r.admin);
  });
  const find = useAction(() => checkWordPress(website.id).then(onChecked));
  const errors = install.error?.fields ?? {};
  return (
    <>
      {site?.status === "failed" && (
        <div className="alert alert-danger alert-stack" role="alert">
          <p><strong>The install of WordPress failed.</strong> The database and its user stay. Correct the cause, and try again.</p>
          {site.error && <p className="cause">{site.error}</p>}
        </div>
      )}
      <Card title="Install WordPress">
        <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void install.run(); }}>
          <p className="muted" style={{ marginBottom: 14 }}>
            The panel makes a MariaDB database and a user for WordPress, downloads the latest WordPress and installs it in the document root. The document root must be empty, or have only the default page of the panel.
          </p>
          <FormError error={install.error} shown={["title", "adminUser", "adminEmail", "locale"]} />
          <WordPressFields value={input} onChange={setInput} errors={errors} />
          <div className="btn-group" style={{ marginTop: 16 }}>
            <button type="submit" className="btn btn-primary" disabled={disabled || install.busy}>{install.busy ? "Starting…" : "Install WordPress"}</button>
          </div>
        </form>
      </Card>
      <Card title="WordPress is there already">
        <div className="card-body">
          <p className="muted">For a site that you uploaded with SFTP or imported: the panel looks for WordPress in the document root, and reads the versions and the updates.</p>
          <FormError error={find.error} />
          <div className="btn-group" style={{ marginTop: 14 }}>
            <button type="button" className="btn" disabled={disabled || find.busy} onClick={() => void find.run()}>{find.busy ? "Starting…" : "Find WordPress"}</button>
          </div>
        </div>
      </Card>
    </>
  );
}

const updatesOf = (items: WordPressItem[]) => items.filter((i) => i.update);

function ReadySite({ website, site, disabled, reload }: { website: Website; site: WordPressSite; disabled: boolean; reload: () => void }) {
  const [plugins, setPlugins] = useState<string[]>([]);
  const [themes, setThemes] = useState<string[]>([]);
  const check = useAction(() => checkWordPress(website.id).then(reload));
  const update = useAction(async (input: WordPressUpdateInput) => {
    await updateWordPress(website.id, input);
    setPlugins([]);
    setThemes([]);
    reload();
  });
  const restore = useAction(() => restoreWordPress(website.id).then(reload));
  const count = updatesOf(site.plugins).length + updatesOf(site.themes).length + (site.coreMinor || site.coreLatest ? 1 : 0);
  const last = site.lastUpdate;
  return (
    <>
      <Card
        title="WordPress"
        actions={site.adminUrl && <a href={site.adminUrl} target="_blank" rel="noreferrer">Open WordPress admin <ArrowUpRight size={14} aria-hidden="true" /><span className="visually-hidden">(opens in a new tab)</span></a>}
      >
        <div className="card-body">
          <table className="kv">
            <tbody>
              <tr>
                <th scope="row">Version</th>
                <td>
                  WordPress {site.version}{" "}
                  {count ? <span className="badge badge-warning">{count === 1 ? "1 update" : `${count} updates`}</span> : <span className="badge badge-success">Up to date</span>}
                </td>
              </tr>
              {site.siteUrl && <tr><th scope="row">Site address</th><td><a href={site.siteUrl} target="_blank" rel="noreferrer">{site.siteUrl}</a></td></tr>}
              {site.adminUser && <tr><th scope="row">Administrator</th><td><code>{site.adminUser}</code></td></tr>}
              <tr><th scope="row">Last check</th><td>{site.checkedAt ? relativeTime(site.checkedAt) : "Not checked yet"}</td></tr>
            </tbody>
          </table>
          <FormError error={check.error ?? update.error} />
          <div className="btn-group" style={{ marginTop: 14 }}>
            <button type="button" className="btn btn-primary" disabled={disabled || update.busy || count === 0} onClick={() => void update.run({ all: true })}>Update all</button>
            <button type="button" className="btn" disabled={disabled || check.busy} onClick={() => void check.run()}>{check.busy ? "Starting…" : "Check for updates"}</button>
          </div>
          <p className="muted small" style={{ marginTop: 10 }}>Before each update, the agent saves a restore point. If an update or the health check fails, the agent puts back the restore point.</p>
          {website.stagingOf && <p className="muted small">This is a staging copy: search engines do not index it. Its automatic updates are off at the start.</p>}
        </div>
      </Card>

      {site.error && site.error !== last?.error && <div className="alert alert-danger" role="alert"><span>{site.error}</span></div>}
      {last && <LastUpdate update={last} />}

      {(site.coreMinor || site.coreLatest) && (
        <Card title="WordPress updates">
          <TableWrap>
            <table>
              <thead><tr><th scope="col">Release</th><th scope="col">Version</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
              <tbody>
                {site.coreMinor && (
                  <tr>
                    <td>Security and maintenance release</td>
                    <td>{site.version} to <strong>{site.coreMinor}</strong></td>
                    <td className="cell-actions"><button type="button" className="btn btn-sm" disabled={disabled || update.busy} onClick={() => void update.run({ core: "minor" })}>Update<span className="visually-hidden"> WordPress to {site.coreMinor}</span></button></td>
                  </tr>
                )}
                {site.coreLatest && (
                  <tr>
                    <td>New major version<span className="cell-sub">Test your plugins and themes on a staging copy first.</span></td>
                    <td>{site.version} to <strong>{site.coreLatest}</strong></td>
                    <td className="cell-actions"><button type="button" className="btn btn-sm" disabled={disabled || update.busy} onClick={() => void update.run({ core: "latest" })}>Update<span className="visually-hidden"> WordPress to {site.coreLatest}</span></button></td>
                  </tr>
                )}
              </tbody>
            </table>
          </TableWrap>
        </Card>
      )}

      <ItemsCard title="Plugins" kind="plugin" items={site.plugins} selected={plugins} setSelected={setPlugins} />
      <ItemsCard title="Themes" kind="theme" items={site.themes} selected={themes} setSelected={setThemes} />
      {(plugins.length > 0 || themes.length > 0) && (
        <div className="alert alert-info" role="status">
          <span>{plugins.length + themes.length} selected: {[...plugins, ...themes].join(", ")}</span>
          <div className="btn-group">
            <button type="button" className="btn" onClick={() => { setPlugins([]); setThemes([]); }}>Clear</button>
            <button type="button" className="btn btn-primary" disabled={disabled || update.busy} onClick={() => void update.run({ plugins, themes })}>Update selected</button>
          </div>
        </div>
      )}

      <div className="grid-2">
        <Card title="Restore point">
          <div className="card-body">
            {site.restorePoint ? (
              <>
                <p>Saved {relativeTime(site.restorePoint.at)} ({longDate(site.restorePoint.at)}), before an update{site.restorePoint.version ? <> of WordPress {site.restorePoint.version}</> : null}.</p>
                <p className="muted small" style={{ marginTop: 8 }}>It has the files with no uploads{site.restorePoint.database ? ", and the database" : ". The database of this site is not on this server, so it is not in the restore point"}.</p>
                <FormError error={restore.error} />
                <div className="btn-group" style={{ marginTop: 14 }}>
                  <ConfirmButton
                    className="btn btn-danger"
                    disabled={disabled || restore.busy}
                    title="Roll back the last update?"
                    confirm="Roll back"
                    onConfirm={() => restore.run()}
                    message={<>
                      <p>The files of WordPress, the plugins and the themes go back to the restore point of {longDate(site.restorePoint.at)}. The uploads stay.</p>
                      {site.restorePoint.database && <p><strong>The database also goes back.</strong> The posts, comments, orders and users since then go.</p>}
                    </>}
                  >
                    Roll back
                  </ConfirmButton>
                </div>
              </>
            ) : <p className="muted">No restore point yet. The panel saves one before each update.</p>}
          </div>
        </Card>
        <AutoUpdatesCard website={website} site={site} reload={reload} />
      </div>
    </>
  );
}

function LastUpdate({ update }: { update: NonNullable<WordPressSite["lastUpdate"]> }) {
  const what = [update.core ? "WordPress" : null, ...update.plugins, ...update.themes].filter(Boolean).join(", ");
  const how = update.automatic ? "The automatic update" : "The update";
  if (update.state === "succeeded") {
    return (
      <div className="alert alert-info" role="status">
        <span>{how} of {relativeTime(update.at)} succeeded: {what}{update.from && update.to && update.from !== update.to ? `. WordPress ${update.from} to ${update.to}` : ""}.</span>
      </div>
    );
  }
  return (
    <div className={`alert alert-stack ${update.state === "rolledBack" ? "alert-warning" : "alert-danger"}`} role="alert">
      <p><strong>{how} of {relativeTime(update.at)} failed: {what}.</strong> {update.state === "rolledBack" ? "The agent put back the restore point, so the site is as it was before the update." : "Check the site now."}</p>
      {update.error && <p className="cause">{update.error}</p>}
    </div>
  );
}

function ItemsCard({ title, kind, items, selected, setSelected }: {
  title: string;
  kind: "plugin" | "theme";
  items: WordPressItem[];
  selected: string[];
  setSelected: (v: string[]) => void;
}) {
  const toggle = (name: string, on: boolean) => setSelected(on ? [...selected, name] : selected.filter((n) => n !== name));
  return (
    <Card title={title}>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th scope="col" style={{ width: 44 }}><span className="visually-hidden">Select</span></th>
              <th scope="col">Name</th>
              <th scope="col">Status</th>
              <th scope="col">Version</th>
              <th scope="col">Update</th>
            </tr>
          </thead>
          <tbody>
            {items.map((i) => (
              <tr key={i.name}>
                <td>{i.update && <input type="checkbox" style={{ width: 18, height: 18, accentColor: "var(--primary)" }} checked={selected.includes(i.name)} onChange={(e) => toggle(i.name, e.target.checked)} aria-label={`Select the update of ${i.title}`} />}</td>
                <td><strong style={{ fontWeight: 500 }}>{i.title}</strong><span className="cell-sub"><code>{i.name}</code></span></td>
                <td>{i.status === "active" || i.status === "must-use" ? <Status tone="success">{i.status === "must-use" ? "Must use" : "Active"}</Status> : <span className="muted">{i.status === "inactive" ? "Inactive" : i.status}</span>}</td>
                <td>{i.version}</td>
                <td>{i.update ? <span className="badge badge-warning">{i.update}</span> : <span className="muted">None</span>}</td>
              </tr>
            ))}
            {items.length === 0 && <tr><td colSpan={5} className="muted">No {kind}s.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
    </Card>
  );
}

function AutoUpdatesCard({ website, site, reload }: { website: Website; site: WordPressSite; reload: () => void }) {
  const change = useAction((autoUpdates: Partial<WordPressSite["autoUpdates"]>) => setWordPressUpdates(website.id, autoUpdates).then(reload));
  const a = site.autoUpdates;
  return (
    <Card title="Automatic updates">
      <div className="card-body">
        <FormError error={change.error} />
        <Field label="WordPress">
          {(p) => (
            <select {...p} className="select" value={a.core} disabled={change.busy} onChange={(e) => void change.run({ core: e.target.value as WordPressCorePolicy })}>
              {(Object.keys(CORE_POLICY) as WordPressCorePolicy[]).map((k) => <option key={k} value={k}>{CORE_POLICY[k]}</option>)}
            </select>
          )}
        </Field>
        <div className="row" style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginTop: 12 }}>
          <span>Plugins</span>
          <Toggle checked={a.plugins} disabled={change.busy} label="Update the plugins automatically" onChange={(v) => void change.run({ plugins: v })} />
        </div>
        <div className="row" style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginTop: 8 }}>
          <span>Themes</span>
          <Toggle checked={a.themes} disabled={change.busy} label="Update the themes automatically" onChange={(v) => void change.run({ themes: v })} />
        </div>
        <p className="muted small" style={{ marginTop: 12 }}>
          Each day, in the maintenance window of the server, the panel checks the site. It updates what you select here, with a restore point and a health check. WordPress does not update itself.
        </p>
      </div>
    </Card>
  );
}

function WpCliCard({ website, commands, disabled, reload }: { website: Website; commands: WpCliCommand[]; disabled: boolean; reload: () => void }) {
  const [line, setLine] = useState("");
  const [shown, setShown] = useState<WpCliCommand | null>(null);
  const run = useAction(async () => {
    const r = await runWpCli(website.id, line);
    setLine("");
    reload();
    await r.job;
  });
  const last = commands[0];
  return (
    <Card title="WP-CLI">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); void run.run().then(reload); }}>
        <Field
          label="Command"
          hint={<>One WP-CLI command, for example <code>plugin install query-monitor --activate</code>. It runs in the container of the website as the Unix user <code>{website.unixUser}</code>, with no shell. The audit log keeps the command with no passwords.</>}
          error={run.error?.fields.args}
        >
          {(p) => (
            <div className="inline-form">
              <div className="input-prefix" style={{ flex: 1 }}>
                <span aria-hidden="true">wp</span>
                <input {...p} className="input mono" value={line} onChange={(e) => setLine(e.target.value)} placeholder="plugin list" autoComplete="off" autoCapitalize="none" spellCheck={false} />
              </div>
              <button type="submit" className="btn btn-primary" disabled={disabled || run.busy || !line.trim()}>{run.busy ? "Running…" : "Run"}</button>
            </div>
          )}
        </Field>
        <FormError error={run.error && !run.error.fields.args ? run.error : null} />
        {last && last.status !== "running" && last.output !== null && (
          <>
            <p className="small" style={{ marginTop: 14 }}><code>{last.command}</code> · {last.status === "failed" ? "failed" : `exit code ${last.exitCode}`}</p>
            <pre className="log-view" tabIndex={0} aria-label="Output of the last command">{last.output || "No output."}</pre>
          </>
        )}
      </form>
      {commands.length > 0 && (
        <TableWrap>
          <table>
            <thead><tr><th scope="col">Command</th><th scope="col">User</th><th scope="col">Ran</th><th scope="col">Result</th></tr></thead>
            <tbody>
              {commands.map((c) => (
                <tr key={c.id}>
                  <td><code style={{ wordBreak: "break-all" }}>{c.command}</code></td>
                  <td>{c.user ?? <span className="muted">The panel</span>}</td>
                  <td>{relativeTime(c.ranAt ?? c.createdAt ?? new Date().toISOString())}</td>
                  <td>
                    {c.status === "running" ? <Live /> : <Status tone={c.status === "failed" || c.exitCode ? "danger" : "success"}>{c.status === "failed" ? "Failed" : `Exit code ${c.exitCode}`}</Status>}
                    {c.output ? <span className="cell-sub"><button type="button" className="btn-link" onClick={() => setShown(c)}>Show output<span className="visually-hidden"> of {c.command}</span></button></span> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {shown && (
        <Modal wide title="Output of the command" onClose={() => setShown(null)} footer={<button type="button" className="btn" onClick={() => setShown(null)}>Close</button>}>
          <p className="muted small" style={{ marginBottom: 10 }}><code>{shown.command}</code></p>
          <pre className="log-view" tabIndex={0} aria-label="Output">{shown.output}</pre>
        </Modal>
      )}
    </Card>
  );
}
