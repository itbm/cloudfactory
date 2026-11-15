import { useState } from "react";
import { Link } from "react-router-dom";
import { useAction, useLoad, useStore } from "../api/hooks";
import {
  addPluginKey, installPlugin, restartPluginContainer, loadPlugins, pluginDeliveries, previewPlugin, removePlugin, removePluginKey, retryPluginDelivery,
  rotatePluginCredentials, setPluginDeveloperMode, setPluginEnabled, testPlugin, upgradePlugin, type PluginPreview,
} from "../api/store";
import type { Confirmation, Plugin, PluginActionTarget, PluginCredentials, PluginDelivery, PluginHook, PluginHookEvent, PluginKey } from "../api/types";
import { ConfirmIdentityFields, NO_CONFIRMATION } from "../components/ConfirmIdentity";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Empty, Field, FormError, Modal, PageHeader, RowMenu, Status, TableWrap, Toggle } from "../components/ui";
import { longDate, relativeTime } from "../lib/format";
import { ROLE_LABEL, pluginPagePath } from "../lib/roles";
import { CopyField } from "./website/AccessTab";

/** What each scope lets a plugin do, in plain words, for the review before an install. */
const SCOPE_TEXT: Record<string, string> = {
  "websites:read": "See all websites, their domains, files and logs.",
  "websites:write": "Create, change, start, stop and delete websites, and change their files.",
  "databases:read": "See the databases and the database servers.",
  "databases:write": "Create, change and delete databases and database users.",
  "dns:read": "See the DNS zones and their records.",
  "dns:write": "Change the DNS zones and their records.",
  "backups:read": "See the backups.",
  "backups:write": "Make and restore backups, and change the backup policy.",
  "servers:read": "See the servers and their state.",
  "servers:write": "Change, update and restart the servers.",
  "images:read": "See the runtime images and the rollouts.",
  "images:write": "Start, pause and roll back rollouts.",
  "customers:read": "See the customers and the plans.",
  "customers:write": "Create, change and suspend customers and plans.",
  "users:read": "See the users who can sign in.",
  "audit:read": "Read the audit log.",
};

const EVENT_TEXT: Record<string, string> = {
  "audit.recorded": "Each change in the audit log",
  "job.finished": "Each job that succeeds or fails",
  "plugin.test": "Test event",
  "website.created": "A new website",
  "website.changed": "A change of a website",
  "website.removed": "A removed website",
  "domain.added": "A new domain",
  "domain.verified": "A verified domain",
  "domain.removed": "A removed domain",
  "database.created": "A new database",
  "database.removed": "A removed database",
  "backup.started": "A backup",
  "restore.started": "A restore from a backup",
  "deploy.started": "A deploy",
  "customer.created": "A new customer",
  "customer.changed": "A change of a customer",
  "user.created": "A new user",
};

/** The before hooks in plain words: the plugin can refuse these changes. */
const HOOK_TEXT: Record<PluginHookEvent, string> = {
  "website.creating": "Make a website",
  "website.removing": "Delete a website",
  "domain.adding": "Add a domain",
  "database.creating": "Make a database",
  "customer.creating": "Add a customer",
};

const CONTAINER_TEXT = { starting: "Starting", running: "Running", stopped: "Stopped", failed: "Failed", removing: "Removing" } as const;
const CONTAINER_TONE = { starting: "warning", running: "success", stopped: "neutral", failed: "danger", removing: "warning" } as const;

const TARGET_TEXT: Record<PluginActionTarget, string> = { website: "websites", server: "servers", customer: "customers" };

function hookList(hooks: PluginHook[]): string {
  return hooks.map((h) => `${HOOK_TEXT[h.event] ?? h.event}${h.onError === "refuse" ? " (refused when the plugin does not answer)" : ""}`).join(", ");
}

const DELIVERY_TONE = { delivered: "success", pending: "warning", failed: "danger" } as const;
const DELIVERY_LABEL = { delivered: "Delivered", pending: "Waiting to try again", failed: "Failed" } as const;

/**
 * Plugins: programs that the administrator runs outside the panel. The panel never runs the
 * code of a plugin. A plugin uses the API with its own token, gets events with signed webhooks,
 * and can add pages that open in a frame. Refer to docs/plugins.md.
 */
export function PluginsPage() {
  const [turn, setTurn] = useState(0);
  const data = useLoad(() => loadPlugins(), [turn]);
  const reload = () => setTurn((t) => t + 1);
  const [installing, setInstalling] = useState(false);
  const [credentials, setCredentials] = useState<PluginCredentials | null>(null);

  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader
          title="Plugins"
          description="A plugin is a program that you run on a different host. It uses the panel API with its own token, and only with the scopes that you approve. It cannot get root access to a server or connect to an agent."
          actions={<button type="button" className="btn btn-primary" onClick={() => setInstalling(true)}>Install plugin</button>}
        />
        <FormError error={data.error} />
        {data.data?.developerMode && (
          <div className="alert alert-warning" role="status">
            <p><strong>The developer mode is on.</strong> The panel installs plugins with no signature, and accepts addresses with HTTP. Turn it off on a production panel.</p>
          </div>
        )}
        <div className="stack">
          {data.loading && !data.data && <p className="muted">Loading the plugins…</p>}
          {data.data && data.data.plugins.length === 0 && (
            <Card title="Installed plugins">
              <div className="card-body">
                <Empty title="No plugins">To install a plugin, add the key of its publisher in "Trusted keys", then select <strong>Install plugin</strong>.</Empty>
              </div>
            </Card>
          )}
          {data.data?.plugins.map((p) => <PluginCard key={p.id} plugin={p} onChange={reload} onCredentials={setCredentials} />)}
          {data.data && <TrustedKeys keys={data.data.keys} onChange={reload} />}
          {data.data && <DeveloperMode on={data.data.developerMode} onChange={reload} />}
        </div>
        {installing && <InstallDialog onClose={() => setInstalling(false)} onInstalled={(c) => { setInstalling(false); setCredentials(c.token ? c : null); reload(); }} />}
        {credentials && <CredentialsDialog credentials={credentials} onClose={() => setCredentials(null)} />}
      </main>
    </>
  );
}

function PluginCard({ plugin: p, onChange, onCredentials }: { plugin: Plugin; onChange: () => void; onCredentials: (c: PluginCredentials | null) => void }) {
  const [showDeliveries, setShowDeliveries] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [upgrading, setUpgrading] = useState(false);
  const [removing, setRemoving] = useState(false);
  const toggle = useAction(async (on: boolean) => { await setPluginEnabled(p.id, on); onChange(); });
  const test = useAction(async () => { await testPlugin(p.id); setShowDeliveries(true); onChange(); });
  const remove = useAction(async () => { await removePlugin(p.id); onChange(); });
  const restart = useAction(async () => { await restartPluginContainer(p.id); onChange(); });
  const expires = p.tokenExpiresAt ? Math.floor((new Date(p.tokenExpiresAt).getTime() - Date.now()) / 86400000) : null;

  return (
    <Card className="plugin-card" title={`${p.name} ${p.version}`} actions={
      <div className="filters">
        <Toggle checked={p.enabled} label={p.enabled ? `Turn off ${p.name}` : `Turn on ${p.name}`} disabled={toggle.busy} onChange={(on) => void toggle.run(on)}>
          {p.enabled ? "On" : "Off"}
        </Toggle>
        <RowMenu label={`Actions for ${p.name}`} actions={[
          { label: "Send a test event", onSelect: () => void test.run(), disabled: !p.webhookUrl },
          { label: "Show deliveries", onSelect: () => setShowDeliveries(true) },
          { label: "Install a new version", onSelect: () => setUpgrading(true) },
          { label: "Make new credentials", onSelect: () => setRotating(true) },
          { label: "Remove plugin", danger: true, onSelect: () => setRemoving(true) },
        ]} />
      </div>
    }>
      <div className="card-body">
        <p style={{ marginTop: 0 }}>{p.description}</p>
        <FormError error={toggle.error ?? test.error ?? remove.error ?? restart.error} />
        <div className="two-col">
          <table className="kv">
            <tbody>
              <tr><th scope="row">Publisher</th><td>{p.publisher}</td></tr>
              <tr><th scope="row">Signature</th><td>{p.signedBy ? <Status tone="success">Signed by {p.signedBy.name}</Status> : <Status tone="warning">No signature (developer mode)</Status>}</td></tr>
              <tr><th scope="row">Address</th><td><code>{p.baseUrl}</code></td></tr>
              {p.hosting && (
                <tr>
                  <th scope="row">Runs on</th>
                  <td>
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
                      {p.hosting.serverName ?? "A removed server"}
                      <Status tone={CONTAINER_TONE[p.hosting.status]}>{CONTAINER_TEXT[p.hosting.status]}</Status>
                      {p.hosting.status === "failed" && p.enabled && <button type="button" className="btn btn-sm" disabled={restart.busy} onClick={() => void restart.run()}>Start again</button>}
                    </span>
                    {p.image && <span className="cell-sub">Container of <code>{p.image.ref}</code> in a gVisor sandbox</span>}
                  </td>
                </tr>
              )}
              <tr><th scope="row">Token</th><td>{p.tokenExpiresAt ? <>Expires {longDate(p.tokenExpiresAt)}{expires !== null && expires <= 30 && <span className="cell-sub note-warning">Make new credentials soon</span>}</> : "No token"}{p.tokenLastUsedAt && <span className="cell-sub">Last used {relativeTime(p.tokenLastUsedAt)}</span>}</td></tr>
              <tr>
                <th scope="row">Events</th>
                <td>
                  {p.events.length ? p.events.map((e) => EVENT_TEXT[e] ?? e).join(", ") : "None"}
                  {p.webhookUrl && <span className="cell-sub">
                    Last 24 hours: {p.deliveries.delivered} delivered{p.deliveries.pending ? `, ${p.deliveries.pending} waiting` : ""}{p.deliveries.failed ? `, ${p.deliveries.failed} failed` : ""}
                  </span>}
                </td>
              </tr>
              {p.pages.length > 0 && (
                <tr><th scope="row">Pages</th><td>{p.pages.map((pg, i) => <span key={pg.id}>{i > 0 && ", "}{p.enabled ? <Link to={pluginPagePath(p.id, pg.id)}>{pg.title}</Link> : pg.title} <span className="muted">({pg.roles.map((r) => ROLE_LABEL[r]).join(", ")})</span></span>)}</td></tr>
              )}
              {p.actions.length > 0 && (
                <tr><th scope="row">Actions</th><td>{p.actions.map((a, i) => <span key={a.id}>{i > 0 && ", "}{a.title} <span className="muted">(on {TARGET_TEXT[a.target]}, for {a.roles.map((role) => ROLE_LABEL[role]).join(", ")})</span></span>)}</td></tr>
              )}
              {p.hooks.length > 0 && <tr><th scope="row">Can refuse</th><td>{hookList(p.hooks)}</td></tr>}
              {p.commands.length > 0 && <tr><th scope="row">Commands</th><td>{p.commands.map((c) => <code key={c.name} style={{ marginRight: 6 }}>cf {c.name}</code>)}</td></tr>}
            </tbody>
          </table>
          <div>
            <h4 style={{ margin: "0 0 8px" }}>Scopes of the token</h4>
            <ScopeList scopes={p.scopes} />
          </div>
        </div>
        {p.deliveries.last?.state === "failed" && (
          <div className="alert alert-danger" role="status" style={{ marginTop: 12 }}>
            <p><strong>The last event did not arrive.</strong> {p.deliveries.last.error}</p>
          </div>
        )}
      </div>
      {showDeliveries && <Deliveries plugin={p} onClose={() => setShowDeliveries(false)} />}
      {rotating && <RotateDialog plugin={p} onClose={() => setRotating(false)} onDone={(c) => { setRotating(false); onCredentials(c.token ? c : null); onChange(); }} />}
      {removing && (
        <Modal title={`Remove ${p.name}?`} onClose={() => setRemoving(false)} footer={<><button type="button" className="btn" onClick={() => setRemoving(false)}>Cancel</button><button type="button" className="btn btn-danger-solid" disabled={remove.busy} onClick={() => { setRemoving(false); void remove.run(); }}>Remove plugin</button></>}>
          <p style={{ marginTop: 0 }}>The token of the plugin and its page tokens stop at once. The panel sends it no more events, and its pages leave the menu.</p>
        </Modal>
      )}
      {upgrading && <InstallDialog plugin={p} onClose={() => setUpgrading(false)} onInstalled={() => { setUpgrading(false); onChange(); }} />}
    </Card>
  );
}

function ScopeList({ scopes }: { scopes: string[] }) {
  if (scopes.length === 0) return <p className="muted">No scopes. The plugin cannot read or change data.</p>;
  return (
    <ul className="scope-list">
      {scopes.map((s) => <li key={s}><code>{s}</code><span>{SCOPE_TEXT[s] ?? s}</span></li>)}
    </ul>
  );
}

function Deliveries({ plugin, onClose }: { plugin: Plugin; onClose: () => void }) {
  const [turn, setTurn] = useState(0);
  const list = useLoad(() => pluginDeliveries(plugin.id), [plugin.id, turn]);
  const retry = useAction(async (d: PluginDelivery) => { await retryPluginDelivery(plugin.id, d.id); setTurn((t) => t + 1); });
  return (
    <Modal wide title={`Deliveries of ${plugin.name}`} onClose={onClose} footer={<><button type="button" className="btn" onClick={() => setTurn((t) => t + 1)}>Refresh</button><button type="button" className="btn btn-primary" onClick={onClose}>Close</button></>}>
      <p className="muted" style={{ marginTop: 0 }}>The last 50 events to <code>{plugin.webhookUrl}</code>. The panel tries a failed delivery again after 1 minute, 5 minutes, 30 minutes, 2 hours and 6 hours.</p>
      <FormError error={list.error ?? retry.error} />
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Time</th><th scope="col">Event</th><th scope="col">Result</th><th scope="col">Attempts</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {(list.data ?? []).map((d) => (
              <tr key={d.id}>
                <td className="nowrap">{relativeTime(d.createdAt)}<span className="cell-sub mono">{d.id.slice(-8)}</span></td>
                <td>{EVENT_TEXT[d.event] ?? d.event}<span className="cell-sub">{summary(d)}</span></td>
                <td><Status tone={DELIVERY_TONE[d.state]}>{DELIVERY_LABEL[d.state]}{d.responseStatus ? ` · HTTP ${d.responseStatus}` : ""}</Status>{d.error && <span className="cell-sub">{d.error}</span>}</td>
                <td>{d.attempts}</td>
                <td className="cell-actions">{d.state !== "pending" && <button type="button" className="btn btn-sm" disabled={retry.busy} onClick={() => void retry.run(d)}>Send again</button>}</td>
              </tr>
            ))}
            {list.data?.length === 0 && <tr><td colSpan={5} className="muted">No events yet.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
    </Modal>
  );
}

function summary(d: PluginDelivery): string {
  const data = d.payload.data as Record<string, string | undefined>;
  if (d.event === "audit.recorded") return [data.action, data.target].filter(Boolean).join(": ");
  if (d.event === "job.finished") return `${data.title ?? ""} (${data.state ?? ""})`;
  return data.message ?? "";
}

/** Install a plugin, or a new version of an installed plugin. */
function InstallDialog({ plugin, onClose, onInstalled }: { plugin?: Plugin; onClose: () => void; onInstalled: (c: PluginCredentials) => void }) {
  const [manifest, setManifest] = useState("");
  const [signature, setSignature] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [hosted, setHosted] = useState(true);
  const [serverId, setServerId] = useState("");
  const [host, setHost] = useState("");
  const servers = useStore().servers.filter((s) => s.panel || s.roles.includes("web"));
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const [preview, setPreview] = useState<PluginPreview | null>(null);
  const check = useAction(async () => setPreview(await previewPlugin(manifest, signature)));
  const install = useAction(async () => {
    if (plugin) {
      const updated = await upgradePlugin(plugin.id, manifest, signature, confirmation);
      onInstalled({ plugin: updated, token: "", webhookSecret: "" });
    } else {
      const placement = preview?.manifest.image && hosted
        ? { hosted: true as const, serverId: serverId || null, host }
        : { hosted: false as const, baseUrl };
      onInstalled(await installPlugin(manifest, signature, placement, confirmation));
    }
  });
  const error = install.error ?? check.error;
  const m = preview?.manifest;
  const added = plugin && m ? m.scopes.filter((s) => !plugin.scopes.includes(s)) : [];

  return (
    <Modal wide title={plugin ? `Install a new version of ${plugin.name}` : "Install plugin"} onClose={onClose} footer={
      <>
        <button type="button" className="btn" onClick={preview ? () => setPreview(null) : onClose}>{preview ? "Back" : "Cancel"}</button>
        {preview
          ? <button type="submit" form="plugin-install" className="btn btn-primary" disabled={install.busy}>{plugin ? "Install version" : "Install plugin"}</button>
          : <button type="submit" form="plugin-check" className="btn btn-primary" disabled={check.busy || !manifest.trim()}>Check the manifest</button>}
      </>
    }>
      {!preview && (
        <form id="plugin-check" noValidate onSubmit={(e) => { e.preventDefault(); void check.run(); }}>
          <FormError error={error} shown={["manifest", "signature"]} />
          <p className="muted" style={{ marginTop: 0 }}>The publisher of the plugin gives you the file <code>plugin.json</code> and its signature. The panel reads the file only as data. It does not run code from it.</p>
          <Field label="Manifest (plugin.json)" error={error?.fields.manifest}>
            {(p) => <textarea {...p} className="input manifest-input" spellCheck={false} value={manifest} onChange={(e) => setManifest(e.target.value)} placeholder='{"schema": 1, "slug": "uptime", …}' />}
          </Field>
          <Field label="Signature" hint="The text of plugin.json.sig, for example from cosign sign-blob." error={error?.fields.signature}>
            {(p) => <textarea {...p} className="input manifest-input" style={{ minHeight: 64 }} spellCheck={false} value={signature} onChange={(e) => setSignature(e.target.value)} />}
          </Field>
        </form>
      )}
      {preview && m && (
        <form id="plugin-install" noValidate onSubmit={(e) => { e.preventDefault(); void install.run(); }}>
          <FormError error={error} shown={["baseUrl", "serverId", "host", "password", "totp"]} />
          <div className="plugin-head">
            <div>
              <h3>{m.name} {m.version}</h3>
              <div className="plugin-meta"><span>By {m.publisher}</span><span><code>{m.slug}</code></span></div>
            </div>
            {preview.signedBy
              ? <Status tone="success">Signed by {preview.signedBy.name}</Status>
              : <Status tone={preview.developerMode ? "warning" : "danger"}>{preview.developerMode ? "No trusted signature. The developer mode accepts it." : "No trusted signature. The panel refuses it."}</Status>}
          </div>
          {m.description && <p>{m.description}</p>}
          {preview.installed && !plugin && <div className="alert alert-warning" role="status"><p>This plugin is installed. To change it, use <strong>Install a new version</strong> on its card.</p></div>}
          <h4>The token of the plugin can</h4>
          <ScopeList scopes={m.scopes} />
          {added.length > 0 && <p className="note-warning">This version asks for more access: {added.join(", ")}.</p>}
          <h4>Events</h4>
          <p>{m.events.length ? m.events.map((e) => EVENT_TEXT[e] ?? e).join(", ") : "None"}{m.webhook && <> to <code>{m.webhook}</code></>}</p>
          {m.pages.length > 0 && (
            <>
              <h4>Pages</h4>
              <p>{m.pages.map((pg) => `${pg.title} (${pg.roles.map((r) => ROLE_LABEL[r]).join(", ")})`).join("; ")}. A page opens in a frame. It gets a token of the user who opens it, with these scopes, for 15 minutes.</p>
            </>
          )}
          {m.actions.length > 0 && (
            <>
              <h4>Actions</h4>
              <p>{m.actions.map((a) => `${a.title} on ${TARGET_TEXT[a.target]} (${a.roles.map((r) => ROLE_LABEL[r]).join(", ")})`).join("; ")}. When a user selects an action, the plugin gets a token of that user, with these scopes, for 15 minutes.</p>
            </>
          )}
          {m.hooks.length > 0 && (
            <>
              <h4>Changes that the plugin can refuse</h4>
              <p>{hookList(m.hooks)}. The panel asks the plugin before each of these changes, and waits up to 5 seconds.</p>
            </>
          )}
          {m.image && (
            <>
              <h4>Image</h4>
              <p><code>{m.image.ref}</code> <span className="muted">({m.image.digest.slice(0, 19)}…)</span>. The signature covers the digest, so the panel runs only this image.</p>
            </>
          )}
          {!plugin && m.image && (
            <fieldset className="radio-cards" style={{ marginBottom: 14 }}>
              <legend>Where the plugin runs</legend>
              <label className="radio-card">
                <input type="radio" name="plugin-place" checked={hosted} onChange={() => setHosted(true)} />
                <span><strong>In a container of the panel</strong><span>A gVisor sandbox with its own Unix user, 1 vCPU and 512 MB of memory. No root access, no files of a customer and no database server.</span></span>
              </label>
              <label className="radio-card">
                <input type="radio" name="plugin-place" checked={!hosted} onChange={() => setHosted(false)} />
                <span><strong>On my own host</strong><span>You run the plugin and give it its token and webhook secret.</span></span>
              </label>
            </fieldset>
          )}
          {!plugin && m.image && hosted && (
            <div className="form-row">
              <Field label="Server" error={error?.fields.serverId}>
                {(p) => (
                  <select {...p} className="select" value={serverId} onChange={(e) => setServerId(e.target.value)}>
                    <option value="">The panel server</option>
                    {servers.filter((s) => !s.panel).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                  </select>
                )}
              </Field>
              <Field label="Host name" hint={`Empty for ${m.slug}.plugins and the panel domain. Its DNS must point to the server.`} error={error?.fields.host}>
                {(p) => <input {...p} className="input" value={host} onChange={(e) => setHost(e.target.value)} placeholder={`${m.slug}.plugins.example.com`} />}
              </Field>
            </div>
          )}
          {!plugin && (!m.image || !hosted) && (
            <Field label="Address of the plugin" hint="Where you run the plugin, for example https://uptime.example.net. Use HTTPS and a host name that is not the panel." error={error?.fields.baseUrl}>
              {(p) => <input {...p} className="input" inputMode="url" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://uptime.example.net" />}
            </Field>
          )}
          <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={install.error} reason="A plugin gets a token with the scopes above." />
        </form>
      )}
    </Modal>
  );
}

function CredentialsDialog({ credentials, onClose }: { credentials: PluginCredentials; onClose: () => void }) {
  const { token, webhookSecret } = credentials;
  if (!token || !webhookSecret) return null;
  return (
    <Modal title={`Credentials of ${credentials.plugin.name}`} onClose={onClose} footer={<button type="button" className="btn btn-primary" onClick={onClose}>I copied the credentials</button>}>
      <p style={{ marginTop: 0 }}>Give these values to the plugin, for example as environment variables. The panel shows them one time only.</p>
      <Field label="API token (CF_TOKEN)">{() => <CopyField value={token} label="API token" />}</Field>
      <Field label="Webhook secret (CF_WEBHOOK_SECRET)" hint="The plugin uses it to check the signature of each event.">{() => <CopyField value={webhookSecret} label="Webhook secret" />}</Field>
    </Modal>
  );
}

function RotateDialog({ plugin, onClose, onDone }: { plugin: Plugin; onClose: () => void; onDone: (c: PluginCredentials) => void }) {
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const rotate = useAction(async () => onDone(await rotatePluginCredentials(plugin.id, confirmation)));
  return (
    <Modal title={`New credentials for ${plugin.name}`} onClose={onClose} footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" form="plugin-rotate" className="btn btn-primary" disabled={rotate.busy}>Make new credentials</button></>}>
      <form id="plugin-rotate" noValidate onSubmit={(e) => { e.preventDefault(); void rotate.run(); }}>
        <FormError error={rotate.error} shown={["password", "totp"]} />
        <p style={{ marginTop: 0 }}>{plugin.hosting
          ? "The panel makes a new token and a new webhook secret, and starts a new container with them. The old ones stop at once."
          : "The panel makes a new token and a new webhook secret. The old ones stop at once, so give the new values to the plugin immediately."}</p>
        <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={rotate.error} reason="New credentials give access to the panel." />
      </form>
    </Modal>
  );
}

function TrustedKeys({ keys, onChange }: { keys: PluginKey[]; onChange: () => void }) {
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const add = useAction(async () => {
    await addPluginKey(name, publicKey, confirmation);
    setAdding(false);
    setName("");
    setPublicKey("");
    setConfirmation(NO_CONFIRMATION);
    onChange();
  });
  const remove = useAction(async (id: string) => { await removePluginKey(id); onChange(); });
  return (
    <Card title="Trusted keys" actions={<button type="button" className="btn" onClick={() => setAdding(true)}>Add key</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}>
        <p className="muted" style={{ marginTop: 0 }}>The panel installs a plugin only when one of these keys signed its manifest. Add only the keys of publishers that you trust.</p>
        <FormError error={remove.error} />
      </div>
      <TableWrap>
        <table>
          <thead><tr><th scope="col">Publisher</th><th scope="col">Fingerprint (SHA-256)</th><th scope="col">Added</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {keys.map((k) => (
              <tr key={k.id}>
                <td><strong>{k.name}</strong></td>
                <td><code>{k.fingerprint.match(/.{1,4}/g)?.slice(0, 8).join(" ")} …</code></td>
                <td>{relativeTime(k.createdAt)}</td>
                <td className="cell-actions" style={{ width: 110 }}>
                  <ConfirmButton className="btn btn-sm btn-danger" title={`Stop trusting ${k.name}?`} confirm="Remove key" message={<p>The installed plugins stay. A new install or a new version needs a different trusted key.</p>} onConfirm={() => void remove.run(k.id)}>Remove</ConfirmButton>
                </td>
              </tr>
            ))}
            {keys.length === 0 && <tr><td colSpan={4} className="muted">No trusted keys.</td></tr>}
          </tbody>
        </table>
      </TableWrap>
      {adding && (
        <Modal title="Add a trusted key" onClose={() => setAdding(false)} footer={<><button type="button" className="btn" onClick={() => setAdding(false)}>Cancel</button><button type="submit" form="plugin-key" className="btn btn-primary" disabled={add.busy}>Add key</button></>}>
          <form id="plugin-key" noValidate onSubmit={(e) => { e.preventDefault(); void add.run(); }}>
            <FormError error={add.error} shown={["name", "publicKey", "password", "totp"]} />
            <Field label="Publisher" error={add.error?.fields.name}>{(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Example Plugins Ltd" />}</Field>
            <Field label="Public key" hint="An ECDSA P-256 key in PEM format, for example the file cosign.pub of the publisher." error={add.error?.fields.publicKey}>
              {(p) => <textarea {...p} className="input manifest-input" spellCheck={false} value={publicKey} onChange={(e) => setPublicKey(e.target.value)} placeholder="-----BEGIN PUBLIC KEY-----" />}
            </Field>
            <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={add.error} reason="A trusted key lets its owner sign plugins for this panel." />
          </form>
        </Modal>
      )}
    </Card>
  );
}

function DeveloperMode({ on, onChange }: { on: boolean; onChange: () => void }) {
  const [asking, setAsking] = useState(false);
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const change = useAction(async (value: boolean) => {
    await setPluginDeveloperMode(value, confirmation);
    setAsking(false);
    setConfirmation(NO_CONFIRMATION);
    onChange();
  });
  return (
    <Card title="Developer mode">
      <div className="card-body">
        <FormError error={asking ? null : change.error} />
        <Toggle checked={on} label="Developer mode" disabled={change.busy} onChange={(value) => (value ? setAsking(true) : void change.run(false))}>
          Accept plugins with no signature, addresses with HTTP, and webhooks on this computer. Use it only to develop a plugin.
        </Toggle>
      </div>
      {asking && (
        <Modal title="Turn on the developer mode" onClose={() => setAsking(false)} footer={<><button type="button" className="btn" onClick={() => setAsking(false)}>Cancel</button><button type="submit" form="plugin-dev" className="btn btn-danger-solid" disabled={change.busy}>Turn on</button></>}>
          <form id="plugin-dev" noValidate onSubmit={(e) => { e.preventDefault(); void change.run(true); }}>
            <FormError error={change.error} shown={["password", "totp"]} />
            <p style={{ marginTop: 0 }}>With the developer mode, an administrator can install a plugin that nobody signed. Do not turn it on for a production panel.</p>
            <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={change.error} reason="The developer mode turns off the signature check." />
          </form>
        </Modal>
      )}
    </Card>
  );
}
