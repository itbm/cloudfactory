import { useState } from "react";
import { useAction, useStore } from "../api/hooks";
import { changePassword, createToken, revokeToken, setPanelCertificate, setTotp, startTotpSetup } from "../api/store";
import type { PanelCertificateMethod, TokenScope } from "../api/types";
import { Topbar } from "../components/Layout";
import { Card, ConfirmButton, Field, FormError, JobRow, Modal, PageHeader, Status, Tabs } from "../components/ui";
import { auditCsv } from "../lib/csv";
import { daysUntil, longDate, relativeTime, shortDate } from "../lib/format";
import { CopyField } from "./website/AccessTab";

// The API has no DNS or email scopes in release 0.1 (FR-104).
const SCOPES: TokenScope[] = ["websites:read", "websites:write", "databases:read", "databases:write", "backups:read", "backups:write", "servers:read", "servers:write", "images:read", "images:write", "customers:read", "customers:write", "users:read", "users:write", "audit:read", "tokens:read", "tokens:write"];

export function SettingsPage() {
  const state = useStore();
  const [tab, setTab] = useState("security");
  const isAdmin = state.session.user.role === "administrator";
  const tabs = [
    { id: "security", label: "Sign-in and security" },
    { id: "tokens", label: "API tokens" },
    { id: "audit", label: "Audit log" },
    ...(isAdmin ? [{ id: "certificate", label: "Panel certificate" }] : []),
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
        {tab === "certificate" && <PanelCertificateSettings />}
      </main>
    </>
  );
}

/** FR-103 and R-01: the password and TOTP. */
function Security() {
  const state = useStore();
  const [totpMode, setTotpMode] = useState<"on" | "off" | null>(null);
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
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
            "Ask for a code from an app on your phone each time you sign in. Then a stolen password alone does not give access to your account."
          </p>
          <div className="btn-group" style={{ marginTop: 14 }}>
            {state.totpEnabled
              ? <button type="button" className="btn btn-danger" onClick={() => setTotpMode("off")}>Turn off</button>
              : <button type="button" className="btn btn-primary" onClick={() => void openSetup()}>Set up</button>}
          </div>
        </div>
      </Card>
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
            <div style={{ marginTop: 14 }}>
              <Field label="6-digit code" hint="Open your authenticator app. Leave it empty if your account has no TOTP." error={create.error?.fields.totp}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={totp} onChange={(e) => setTotpCode(e.target.value)} />}</Field>
            </div>
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

const METHOD_LABEL: Record<PanelCertificateMethod, string> = { internal: "Panel CA", letsencrypt: "Let's Encrypt" };

/**
 * The TLS certificate of the panel domain. A new panel gets a certificate from its own CA. This
 * works for all domains, also localhost and panel.localhost. An administrator can change to
 * Let's Encrypt when the domain is public.
 */
function PanelCertificateSettings() {
  const state = useStore();
  const cert = state.panelCertificate;
  // The store changes the job in the job list while the agent works.
  const job = cert.job && (state.jobs.find((j) => j.id === cert.job!.id) ?? cert.job);
  const busy = job?.state === "running" || job?.state === "queued";
  const [method, setMethod] = useState<PanelCertificateMethod>(cert.method ?? "internal");
  const change = useAction(setPanelCertificate);
  const tone = cert.state === "valid" ? "success" : cert.state === "expired" ? "danger" : "warning";
  const stateLabel = cert.state === "valid" ? "Valid" : cert.state === "expired" ? "Expired" : "Waiting for the certificate";
  const blocked = method === "letsencrypt" && !cert.letsEncryptAvailable ? `Let's Encrypt gives certificates only for public domains. ${cert.domain} is not a public domain.`
    : method === "internal" && cert.serversWithoutCa.length > 0 ? `These servers do not trust the panel CA yet: ${cert.serversWithoutCa.join(", ")}. Update their agent, and wait one minute.`
      : null;
  return (
    <div className="stack">
      <Card title="Panel certificate">
        <div className="card-body">
          {cert.method === null ? (
            <p className="muted">The agent on the panel server has not reported the certificate yet. Update the agent on the panel server, and wait one minute.</p>
          ) : (
            <table className="kv">
              <tbody>
                <tr><th scope="row">Domain</th><td><code>{cert.domain}</code></td></tr>
                <tr><th scope="row">Method</th><td>{METHOD_LABEL[cert.method]}</td></tr>
                <tr><th scope="row">Certificate</th><td><Status tone={tone}>{stateLabel}</Status></td></tr>
                {cert.expiresAt && <tr><th scope="row">Expires</th><td>{longDate(cert.expiresAt)} <span className="muted">({relativeTime(cert.expiresAt)}). The edge proxy renews it automatically.</span></td></tr>}
                {cert.reportedAt && <tr><th scope="row">Last check</th><td>{relativeTime(cert.reportedAt)}</td></tr>}
              </tbody>
            </table>
          )}
          {job && (busy || job.state === "failed") && <ul className="job-list" style={{ marginTop: 16 }}><JobRow job={job} /></ul>}
        </div>
      </Card>

      {cert.caCertificate && (
        <Card title="Root certificate of the panel CA">
          <div className="card-body">
            <p>
              {cert.method === "internal"
                ? "Your browser shows a warning for the panel until you add this root certificate. Add it on each computer that opens the panel."
                : "The panel keeps its CA for a change back from Let's Encrypt. You need the root certificate only when the panel uses the panel CA."}
              {" "}The other servers trust the panel CA by its fingerprint. They do not need the file.
            </p>
            <div className="btn-group" style={{ marginTop: 12 }}>
              <a className="btn btn-primary" download="cloudfactory-panel-ca.crt" href={`data:application/x-x509-ca-cert;charset=utf-8,${encodeURIComponent(cert.caCertificate)}`}>Download root certificate</a>
            </div>
            {cert.caSha256 && <>
              <p className="muted small" style={{ marginTop: 14, marginBottom: 6 }}>SHA-256 fingerprint. Compare it with the fingerprint that your system shows before you trust the certificate.</p>
              <CopyField value={cert.caSha256} label="SHA-256 fingerprint of the root certificate" />
            </>}
            <details style={{ marginTop: 14 }}>
              <summary>How to add the root certificate</summary>
              <ul style={{ marginTop: 8 }}>
                <li><strong>macOS:</strong> open the file. Keychain Access adds it to the login keychain. Open the certificate, expand Trust and set &quot;When using this certificate&quot; to Always Trust.</li>
                <li><strong>Windows:</strong> open the file and select Install Certificate. Select Local Machine, then put the certificate in Trusted Root Certification Authorities.</li>
                <li><strong>Debian and Ubuntu:</strong> copy the file to <code>/usr/local/share/ca-certificates/</code> and run <code>sudo update-ca-certificates</code>.</li>
                <li><strong>Firefox:</strong> go to Settings, Privacy &amp; Security, View Certificates, Authorities. Select Import, then select &quot;Trust this CA to identify websites&quot;.</li>
              </ul>
            </details>
          </div>
        </Card>
      )}

      {cert.method !== null && (
        <Card title="Change the method">
          <div className="card-body" style={{ maxWidth: 760 }}>
            <FormError error={change.error} />
            <fieldset className="radio-cards">
              <legend>Certificate method</legend>
              <label className="radio-card">
                <input type="radio" name="panel-cert" checked={method === "internal"} onChange={() => setMethod("internal")} />
                <span><strong>Panel CA</strong><span>Works for all domains, also localhost and panel.localhost. Each browser must trust the root certificate.</span></span>
              </label>
              <label className={`radio-card${cert.letsEncryptAvailable ? "" : " radio-card-disabled"}`}>
                <input type="radio" name="panel-cert" checked={method === "letsencrypt"} disabled={!cert.letsEncryptAvailable} onChange={() => setMethod("letsencrypt")} />
                <span><strong>Let&apos;s Encrypt</strong><span>{cert.letsEncryptAvailable
                  ? "All browsers trust it. The DNS of the domain must point to the panel server, and ports 80 and 443 must be open."
                  : `Not available: ${cert.domain} is not a public domain.`}</span></span>
              </label>
            </fieldset>
            {blocked && method !== cert.method && <div className="alert alert-warning" style={{ marginTop: 12 }}><p>{blocked}</p></div>}
            <div className="btn-group" style={{ marginTop: 14 }}>
              <ConfirmButton
                className="btn btn-primary"
                danger={false}
                disabled={busy || method === cert.method || !!blocked}
                title={`Change to ${METHOD_LABEL[method]}?`}
                confirm={`Change to ${METHOD_LABEL[method]}`}
                message={method === "letsencrypt" ? <>
                  <p>The edge proxy asks Let&apos;s Encrypt for a certificate for <code>{cert.domain}</code>. Make sure that the DNS of the domain points to the panel server, and that ports 80 and 443 are open.</p>
                  <p>If the certificate does not come in two minutes, the panel keeps the current certificate.</p>
                </> : <>
                  <p>The panel CA signs the certificate of <code>{cert.domain}</code>. Browsers that do not trust the root certificate show a warning.</p>
                  <p><strong>Add the root certificate to your browser first.</strong> A browser that opened the panel with the Let&apos;s Encrypt certificate does not let you continue past the warning, because the panel tells browsers to use only HTTPS (HSTS).</p>
                </>}
                onConfirm={() => void change.run(method)}
              >
                {method === cert.method ? "This method is in use" : `Change to ${METHOD_LABEL[method]}`}
              </ConfirmButton>
            </div>
          </div>
        </Card>
      )}
    </div>
  );
}
