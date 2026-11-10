import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { addAlertChannel, addPasskey, changePassword, createToken, removeAlertChannel, removePasskey, revokeToken, setSystem, setTotp, startTotpSetup, testAlertChannel } from "../api/store";
import { features, LIVE } from "../api/mode";
import type { AlertChannel, TokenScope } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Field, FormError, Modal, PageHeader, Status, Tabs } from "../components/ui";
import { auditCsv } from "../lib/csv";
import { daysUntil, longDate, relativeTime, shortDate } from "../lib/format";
import { CopyField } from "./website/AccessTab";

// The live API has no DNS or email scopes in release 0.1 (FR-104).
const SCOPES: TokenScope[] = LIVE
  ? ["websites:read", "websites:write", "databases:read", "databases:write", "backups:read", "backups:write", "servers:read", "servers:write", "images:read", "images:write", "customers:read", "customers:write", "users:read", "users:write", "audit:read", "tokens:read", "tokens:write"]
  : ["websites:read", "websites:write", "dns:read", "dns:write", "email:read", "email:write", "databases:read", "databases:write", "backups:read", "backups:write", "servers:read", "servers:write"];
const EVENTS = ["Server health", "Failed update", "Failed deploy", "Failed backup", "Mailbox 90% full", "Certificate failed", "PHP support ends"];

export function SettingsPage() {
  const state = useStore();
  const [tab, setTab] = useState("security");
  const isAdmin = state.session.user.role === "administrator";
  const tabs = [
    { id: "security", label: "Sign-in and security" },
    { id: "tokens", label: "API tokens" },
    { id: "audit", label: "Audit log" },
    ...(features.alerts ? [{ id: "alerts", label: "Alerts" }] : []),
    ...(isAdmin && features.system ? [{ id: "system", label: "System" }] : []),
  ];
  return (
    <>
      <Topbar />
      <main id="main" className="content content-wide">
        <PageHeader title="Settings" />
        <Tabs label="Settings sections" current={tab} onSelect={setTab} items={tabs} />
        {tab === "security" && <Security />}
        {tab === "tokens" && <Tokens />}
        {tab === "audit" && <Audit />}
        {tab === "alerts" && <Alerts />}
        {tab === "system" && <System />}
      </main>
    </>
  );
}

/** FR-103 and R-01: the password, passkeys and TOTP. */
function Security() {
  const state = useStore();
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [totpMode, setTotpMode] = useState<"on" | "off" | null>(null);
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const remove = useAction(removePasskey);
  const totp = useAction(setTotp);
  const start = useAction(startTotpSetup);
  const closeTotp = () => { setTotpMode(null); setSetup(null); setCode(""); setPassword(""); totp.clear(); };
  const openSetup = async () => {
    const r = await start.run();
    if (r.ok) { setSetup(r.value); setTotpMode("on"); }
  };
  return (
    <div className="grid-2">
      <PasswordCard />
      <Card title="Authenticator app (TOTP)">
        <div className="card-body">
          <FormError error={start.error ?? (totpMode ? null : totp.error)} />
          <p>{state.totpEnabled ? <Status tone="success">On</Status> : <Status tone="warning">Off</Status>}</p>
          <p className="muted" style={{ marginTop: 8 }}>
            {features.passkeys
              ? "Use a time-based one-time password with your password when a passkey is not available."
              : "Ask for a code from an app on your phone each time you sign in. Then a stolen password alone does not give access to your account."}
          </p>
          <div className="btn-group" style={{ marginTop: 14 }}>
            {state.totpEnabled
              ? <button type="button" className="btn btn-danger" onClick={() => setTotpMode("off")}>Turn off</button>
              : <button type="button" className="btn btn-primary" onClick={() => void openSetup()}>Set up</button>}
          </div>
        </div>
      </Card>
      {features.passkeys && (
      <Card title="Passkeys" actions={<button type="button" className="btn btn-primary" onClick={() => setAdding(true)}>Add passkey</button>}>
        <div className="card-body" style={{ paddingBottom: 0 }}><FormError error={remove.error} /></div>
        <div className="table-wrap">
          <table>
            <thead><tr><th scope="col">Name</th><th scope="col">Added</th><th scope="col">Last used</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {state.passkeys.map((p) => (
                <tr key={p.id}>
                  <td>{p.name}</td>
                  <td>{shortDate(p.addedAt)}</td>
                  <td>{p.lastUsedAt ? relativeTime(p.lastUsedAt) : "Never"}</td>
                  <td className="cell-actions" style={{ width: 110 }}>
                    <ConfirmButton className="btn btn-sm btn-danger" title={`Remove ${p.name}?`} confirm="Remove" message={<p>You cannot sign in with this passkey after you remove it.</p>} onConfirm={() => void remove.run(p.id)}>Remove</ConfirmButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      )}
      {adding && (
        <Modal title="Add passkey" onClose={() => setAdding(false)} footer={<><button type="button" className="btn" onClick={() => setAdding(false)}>Cancel</button><button type="button" className="btn btn-primary" onClick={() => { addPasskey(name); setAdding(false); setName(""); }}>Continue</button></>}>
          <p>Your browser asks you to confirm with Touch ID, Windows Hello or a security key.</p>
          <Field label="Name for this passkey">{(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Work laptop" />}</Field>
        </Modal>
      )}
      {totpMode && (
        <Modal
          title={totpMode === "on" ? "Set up an authenticator app" : "Turn off the authenticator app"}
          onClose={closeTotp}
          footer={<><button type="button" className="btn" onClick={closeTotp}>Cancel</button><button type="submit" form="totp" className={totpMode === "on" ? "btn btn-primary" : "btn btn-danger"}>{totpMode === "on" ? "Turn on" : "Turn off"}</button></>}
        >
          <form id="totp" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await totp.run(totpMode === "on", code, password)).ok) closeTotp(); }}>
            <FormError error={totp.error} shown={["password", "code"]} />
            {totpMode === "on" && setup && <>
              <p>Add this key to your authenticator app, for example 1Password, Google Authenticator or Microsoft Authenticator. On a phone, you can <a href={setup.uri}>open the key in the app</a>.</p>
              <CopyField value={setup.secret.replace(/(.{4})/g, "$1 ").trim()} label="TOTP key" />
              <p className="muted" style={{ marginTop: 8, marginBottom: 12 }}>The key is valid for 10 minutes. Keep a copy in a safe place, in case you lose your phone.</p>
            </>}
            {totpMode === "off" && <p style={{ marginBottom: 12 }}>You then sign in with your password only. Type your password and a code from the app.</p>}
            <Field label="Password" error={totp.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <Field label="6-digit code from the app" error={totp.error?.fields.code}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
          </form>
        </Modal>
      )}
    </div>
  );
}

/** R-01: change the password with the current password and, when TOTP is on, a code. */
function PasswordCard() {
  const state = useStore();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [code, setCode] = useState("");
  const [done, setDone] = useState(false);
  const change = useAction(changePassword);
  return (
    <Card title="Password">
      <form className="card-body" noValidate onSubmit={async (e) => {
        e.preventDefault();
        setDone(false);
        if ((await change.run(current, next, confirm, code)).ok) { setCurrent(""); setNext(""); setConfirm(""); setCode(""); setDone(true); }
      }}>
        <FormError error={change.error} shown={["currentPassword", "password", "password_confirmation", "totp"]} />
        <p className="muted" style={{ marginBottom: 12 }}>A password change signs out your other sessions and deletes your API tokens. Make new tokens after the change.</p>
        {done && <p role="status" style={{ marginBottom: 12 }}><Status tone="success">Changed</Status> Your other sessions are signed out, and your API tokens are deleted.</p>}
        <Field label="Current password" error={change.error?.fields.currentPassword}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />}</Field>
        <Field label="New password" hint="Use 12 or more characters." error={change.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />}</Field>
        <Field label="Type the new password again" error={change.error?.fields.password_confirmation}>{(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />}</Field>
        {state.totpEnabled && <Field label="6-digit code from the app" error={change.error?.fields.totp}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />}</Field>}
        <div className="btn-group" style={{ marginTop: 14 }}><button type="submit" className="btn btn-primary">Change password</button></div>
      </form>
    </Card>
  );
}

/** FR-104: API tokens with scopes and an expiry date. */
function Tokens() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<TokenScope[]>(["websites:read"]);
  const [days, setDays] = useState("90");
  const [password, setPassword] = useState("");
  const [totp, setTotpCode] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useAction(createToken);
  return (
    <Card title="API tokens" actions={<button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>New token</button>}>
      <div className="card-body" style={{ paddingBottom: 12 }}><p className="muted">The web UI, the CLI, the Terraform provider and your scripts use the same public API. Give each token only the scopes that it needs.</p></div>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Name</th><th scope="col">Scopes</th><th scope="col">Expires</th><th scope="col">Last used</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.tokens.map((t) => {
              const left = daysUntil(t.expiresAt);
              return (
                <tr key={t.id}>
                  <td><strong>{t.name}</strong><span className="cell-sub mono">{t.prefix}…</span></td>
                  <td><ul className="tag-list">{t.scopes.map((s) => <li key={s}><span className="tag">{s}</span></li>)}</ul></td>
                  <td>{shortDate(t.expiresAt)}{left <= 14 && <span className="cell-sub note-warning">{left < 1 ? "Expires today" : left === 1 ? "Expires in 1 day" : `Expires in ${left} days`}</span>}</td>
                  <td>{t.lastUsedAt ? relativeTime(t.lastUsedAt) : "Never"}</td>
                  <td className="cell-actions" style={{ width: 110 }}>
                    <ConfirmButton className="btn btn-sm btn-danger" title={`Revoke ${t.name}?`} confirm="Revoke token" message={<p>Scripts and tools that use this token stop working now.</p>} onConfirm={() => revokeToken(t.id)}>Revoke</ConfirmButton>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {open && (
        <Modal title="New API token" onClose={() => { setOpen(false); setPassword(""); setTotpCode(""); create.clear(); }} footer={<><button type="button" className="btn" onClick={() => { setOpen(false); setPassword(""); setTotpCode(""); }}>Cancel</button><button type="submit" form="tok" className="btn btn-primary">Create token</button></>}>
          <form id="tok" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = await create.run(name, scopes, Number(days), password, totp);
            setPassword("");
            setTotpCode("");
            if (r.ok) { setOpen(false); setSecret(r.value.secret); setName(""); }
          }}>
            <FormError error={create.error} />
            <Field label="Name" error={create.error?.fields.name}>{(p) => <input {...p} className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Terraform (staging)" />}</Field>
            <Field label="Expires after (days)" error={create.error?.fields.days}>{(p) => <input {...p} className="input" type="number" min={1} max={365} value={days} onChange={(e) => setDays(e.target.value)} />}</Field>
            <fieldset className="checkbox-list" style={{ marginTop: 14 }}>
              <legend>Scopes</legend>
              {SCOPES.map((s) => <label key={s} className="check"><input type="checkbox" checked={scopes.includes(s)} onChange={(e) => setScopes(e.target.checked ? [...scopes, s] : scopes.filter((x) => x !== s))} /><code>{s}</code></label>)}
            </fieldset>
            {create.error?.fields.scopes && <p className="error text-danger">{create.error.fields.scopes}</p>}
            {/* CF-02: a stolen session cannot make a token without the password. */}
            <div style={{ marginTop: 14 }}>
              <Field label="Your password" hint="Type your password again to make a token." error={create.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            </div>
            {(state.totpEnabled || LIVE) && (
              <div style={{ marginTop: 14 }}>
              <Field label="6-digit code" hint={LIVE ? "Open your authenticator app. Leave it empty if your account has no TOTP." : "Open your authenticator app."} error={create.error?.fields.totp}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={totp} onChange={(e) => setTotpCode(e.target.value)} />}</Field>
              </div>
            )}
          </form>
        </Modal>
      )}
      {secret && (
        <Modal title="Copy the token" onClose={() => setSecret(null)} footer={<button type="button" className="btn btn-primary" onClick={() => setSecret(null)}>I copied the token</button>}>
          <p>The panel shows the token one time only. Keep it in a secret store.</p>
          <CopyField value={secret} label="API token" />
        </Modal>
      )}
    </Card>
  );
}

/** FR-105: who made each change, when, and from which IP address. */
function Audit() {
  const state = useStore();
  const [q, setQ] = useState("");
  const rows = state.audit.filter((a) => !q || `${a.actor} ${a.action} ${a.target} ${a.ip}`.toLowerCase().includes(q.toLowerCase()));
  return (
    <Card title="Audit log" actions={
      <div className="filters">
        <label className="visually-hidden" htmlFor="audit-q">Filter</label>
        <input id="audit-q" className="input" style={{ width: 240 }} placeholder="Filter by person, change or IP" value={q} onChange={(e) => setQ(e.target.value)} />
        <a className="btn" download="audit-log.csv" href={`data:text/csv;charset=utf-8,${encodeURIComponent(auditCsv(state.audit))}`}>Export CSV</a>
      </div>
    }>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Time</th><th scope="col">Person or token</th><th scope="col">IP address</th><th scope="col">Change</th><th scope="col">Target</th></tr></thead>
          <tbody>
            {rows.map((a) => (
              <tr key={a.id}>
                <td className="nowrap">{longDate(a.at)}<span className="cell-sub">{relativeTime(a.at)}</span></td>
                <td>{a.actor}</td>
                <td><code>{a.ip}</code></td>
                <td>{a.action}</td>
                <td>{a.target}</td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={5} className="muted">No changes match the filter.</td></tr>}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/** FR-1303: alerts by email and by webhook. */
function Alerts() {
  const state = useStore();
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<AlertChannel["kind"]>("email");
  const [target, setTarget] = useState("");
  const [events, setEvents] = useState<string[]>(["Server health", "Failed update"]);
  const add = useAction(addAlertChannel);
  return (
    <Card title="Alerts" actions={<button type="button" className="btn btn-primary" onClick={() => setOpen(true)}>Add alert</button>}>
      <div className="table-wrap">
        <table>
          <thead><tr><th scope="col">Send to</th><th scope="col">Events</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {state.alertChannels.map((c) => (
              <tr key={c.id}>
                <td><strong>{c.kind === "email" ? "Email" : "Webhook"}</strong><span className="cell-sub mono">{c.target}</span></td>
                <td><ul className="tag-list">{c.events.map((e) => <li key={e}><span className="tag" style={{ fontFamily: "inherit" }}>{e}</span></li>)}</ul></td>
                <td>
                  <div className="btn-group" style={{ justifyContent: "flex-end" }}>
                    <button type="button" className="btn btn-sm" onClick={() => void testAlertChannel(c.id)}>Send test</button>
                    <ConfirmButton className="btn btn-sm btn-danger" title="Remove alert?" confirm="Remove" message={<p>{c.target} stops getting alerts.</p>} onConfirm={() => removeAlertChannel(c.id)}>Remove</ConfirmButton>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open && (
        <Modal title="Add alert" onClose={() => { setOpen(false); add.clear(); }} footer={<><button type="button" className="btn" onClick={() => setOpen(false)}>Cancel</button><button type="submit" form="alc" className="btn btn-primary">Add alert</button></>}>
          <form id="alc" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await add.run(kind, target, events)).ok) { setOpen(false); setTarget(""); } }}>
            <FormError error={add.error} />
            <fieldset className="radio-cards">
              <legend>Send by</legend>
              <label className="radio-card"><input type="radio" checked={kind === "email"} onChange={() => setKind("email")} /><span><strong>Email</strong></span></label>
              <label className="radio-card"><input type="radio" checked={kind === "webhook"} onChange={() => setKind("webhook")} /><span><strong>Webhook</strong><span>A JSON POST, signed with HMAC-SHA256.</span></span></label>
            </fieldset>
            <Field label={kind === "email" ? "Email address" : "HTTPS URL"} error={add.error?.fields.target}>{(p) => <input {...p} className="input" value={target} onChange={(e) => setTarget(e.target.value)} />}</Field>
            <fieldset className="checkbox-list" style={{ marginTop: 14 }}>
              <legend>Events</legend>
              {EVENTS.map((ev) => <label key={ev} className="check"><input type="checkbox" checked={events.includes(ev)} onChange={(e) => setEvents(e.target.checked ? [...events, ev] : events.filter((x) => x !== ev))} />{ev}</label>)}
            </fieldset>
          </form>
        </Modal>
      )}
    </Card>
  );
}

/** FR-1813: a different registry URL, for example a mirror. */
function System() {
  const state = useStore();
  const [f, setF] = useState(state.system);
  const [saved, setSaved] = useState(false);
  const save = useAction(async () => { setSystem(f); setSaved(true); });
  return (
    <Card title="System">
      <form className="card-body" noValidate onSubmit={(e) => { e.preventDefault(); setSaved(false); void save.run(); }} style={{ maxWidth: 640 }}>
        <FormError error={save.error} />
        <Field label="Panel domain">{(p) => <input {...p} className="input mono" value={f.panelDomain} onChange={(e) => setF({ ...f, panelDomain: e.target.value })} />}</Field>
        <Field label="Image registry" hint="The agents pull signed images from this registry.">{(p) => <input {...p} className="input mono" value={f.registry} readOnly />}</Field>
        <Field label="Registry mirror" hint="Optional. The agents try the mirror first, then the registry. Signatures are still checked." error={save.error?.fields.registryMirror}>
          {(p) => <input {...p} className="input mono" value={f.registryMirror} onChange={(e) => setF({ ...f, registryMirror: e.target.value })} placeholder="registry.example.com/cloudfactory" />}
        </Field>
        <Field label="Time zone">{(p) => <select {...p} className="select" value={f.timezone} onChange={(e) => setF({ ...f, timezone: e.target.value })}>{["Europe/London", "Europe/Amsterdam", "Europe/Berlin", "UTC", "America/New_York"].map((t) => <option key={t}>{t}</option>)}</select>}</Field>
        <div className="btn-group" style={{ marginTop: 14, alignItems: "center" }}>
          <button type="submit" className="btn btn-primary">Save</button>
          {saved && !save.error && <span className="text-success" role="status">Saved.</span>}
        </div>
      </form>
    </Card>
  );
}
