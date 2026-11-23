import { LogIn } from "lucide-react";
import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useAction, useLoad, useStore } from "../api/hooks";
import type { IdentityProvider, IdentityProviderInput, UserIdentity } from "../api/live";
import { addIdentityProvider, changeIdentityProvider, connectIdentity, disconnectIdentity, listIdentities, listIdentityProviders, removeIdentityProvider } from "../api/store";
import type { Confirmation } from "../api/types";
import { ConfirmIdentityFields, NO_CONFIRMATION } from "../components/ConfirmIdentity";
import { Card, Field, FormError, Modal, Status, TableWrap } from "../components/ui";
import { longDate, relativeTime, shortDate } from "../lib/format";
import { browser, fragmentValue, signOnError } from "../lib/sso";
import { CopyField } from "../pages/website/AccessTab";

/**
 * Single sign-on in the settings of the user: the providers that an administrator added, and
 * the accounts that the user connected. To connect or disconnect an account, the user gives the
 * password and, when TOTP is on, a code. The panel shows the card only when there are providers.
 */
export function SignOnCard() {
  const state = useStore();
  const location = useLocation();
  const navigate = useNavigate();
  const list = useLoad(listIdentities, []);
  // The provider sends the browser back to "/settings#sso-connected=<id>" or "#sso-error=<code>".
  const [back] = useState(() => ({ connected: fragmentValue(location.hash, "sso-connected"), error: signOnError(fragmentValue(location.hash, "sso-error")) }));
  const [mode, setMode] = useState<{ kind: "connect" | "disconnect"; identity: UserIdentity } | null>(null);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const connect = useAction(connectIdentity);
  const disconnect = useAction(disconnectIdentity);
  const action = mode?.kind === "connect" ? connect : disconnect;
  const close = () => { setMode(null); setPassword(""); setCode(""); connect.clear(); disconnect.clear(); };
  useEffect(() => {
    if (location.hash) navigate({ pathname: location.pathname }, { replace: true });
  }, [location, navigate]);

  const identities = list.data ?? [];
  if (!list.error && identities.length === 0 && !back.error) return null;
  const connected = identities.find((i) => i.providerId === back.connected);
  return (
    <Card title="Single sign-on">
      <div className="card-body">
        <FormError error={list.error} />
        {back.error && <div className="alert alert-danger" role="alert"><p>{back.error}</p></div>}
        {connected && <p role="status" style={{ marginBottom: 12 }}><Status tone="success">Connected</Status> You can now sign in with {connected.name}.</p>}
        <p className="muted">Sign in with the account of your organisation. The provider checks your password and the other steps of its sign-in.</p>
        {identities.length > 0 && (
          <ul className="passkey-list" aria-label="Single sign-on providers">
            {identities.map((i) => (
              <li key={i.providerId}>
                <LogIn size={20} aria-hidden="true" className="passkey-icon" />
                <div className="passkey-text">
                  <strong>{i.name}</strong>
                  <span className="cell-sub">
                    {i.connected
                      ? <>{i.email ?? "Connected"} · Connected {shortDate(i.connectedAt ?? "")} · {i.lastUsedAt ? `Used ${relativeTime(i.lastUsedAt)}` : "Not used yet"}</>
                      : "Not connected"}
                  </span>
                </div>
                <div className="btn-group">
                  {i.connected
                    ? <button type="button" className="btn btn-sm btn-danger" onClick={() => setMode({ kind: "disconnect", identity: i })} aria-label={`Disconnect ${i.name}`}>Disconnect</button>
                    : <button type="button" className="btn btn-sm" onClick={() => setMode({ kind: "connect", identity: i })} aria-label={`Connect ${i.name}`}>Connect</button>}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
      {mode && (
        <Modal
          title={mode.kind === "connect" ? `Connect ${mode.identity.name}` : `Disconnect ${mode.identity.name}`}
          onClose={close}
          footer={<>
            <button type="button" className="btn" onClick={close}>Cancel</button>
            <button type="submit" form="sso-identity" className={mode.kind === "connect" ? "btn btn-primary" : "btn btn-danger"} disabled={action.busy}>
              {mode.kind === "connect" ? "Continue" : "Disconnect"}
            </button>
          </>}
        >
          <form id="sso-identity" noValidate onSubmit={async (e) => {
            e.preventDefault();
            if (mode.kind === "connect") {
              const r = await connect.run(mode.identity.providerId, password, code);
              // The browser goes to the provider, and the provider sends it back to Settings.
              if (r.ok) browser.go(r.value);
            } else if ((await disconnect.run(mode.identity.providerId, password, code)).ok) {
              close();
              list.reload();
            }
          }}>
            <FormError error={action.error} shown={["password", "totp"]} />
            {mode.kind === "connect"
              ? <p style={{ marginBottom: 12 }}>Type your password{state.totpEnabled ? " and a code from the app" : ""}. The browser then goes to {mode.identity.name}. Sign in there with your account.</p>
              : <p style={{ marginBottom: 12 }}>You can no longer sign in with {mode.identity.name}. You can connect the account again later. Type your password{state.totpEnabled ? " and a code from the app" : ""}.</p>}
            <Field label="Password" error={action.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            {state.totpEnabled && <Field label="6-digit code from the app" error={action.error?.fields.totp}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />}</Field>}
          </form>
        </Modal>
      )}
    </Card>
  );
}

const PROTOCOLS = { oidc: "OpenID Connect", saml: "SAML 2.0" } as const;

type Draft = {
  name: string; protocol: "oidc" | "saml"; enabled: boolean;
  issuer: string; clientId: string; clientSecret: string;
  entityId: string; ssoUrl: string; certificate: string;
  linkByEmail: boolean; emailDomains: string;
};

const EMPTY: Draft = { name: "", protocol: "oidc", enabled: true, issuer: "", clientId: "", clientSecret: "", entityId: "", ssoUrl: "", certificate: "", linkByEmail: false, emailDomains: "" };

const draftOf = (p: IdentityProvider): Draft => ({
  name: p.name, protocol: p.protocol, enabled: p.enabled,
  issuer: p.issuer ?? "", clientId: p.clientId ?? "", clientSecret: "",
  entityId: p.entityId ?? "", ssoUrl: p.ssoUrl ?? "", certificate: p.certificate ?? "",
  linkByEmail: p.linkByEmail, emailDomains: p.emailDomains.join(", "),
});

/** The fields of the protocol only. The API refuses the fields of the other protocol. */
function inputOf(d: Draft, adding: boolean): IdentityProviderInput {
  const common = { name: d.name.trim(), enabled: d.enabled, linkByEmail: d.linkByEmail, emailDomains: d.emailDomains.split(/[\s,]+/).filter(Boolean), ...(adding ? { protocol: d.protocol } : {}) };
  return d.protocol === "oidc"
    ? { ...common, issuer: d.issuer.trim(), clientId: d.clientId.trim(), ...(d.clientSecret ? { clientSecret: d.clientSecret } : {}) }
    : { ...common, entityId: d.entityId.trim(), ssoUrl: d.ssoUrl.trim(), certificate: d.certificate.trim() };
}

/**
 * The administrator adds the OpenID Connect and SAML providers. The panel signs in a user with a
 * provider when the user connected the account, or, when the provider can connect users by
 * email, when the provider gives a verified email of an accepted domain.
 */
export function IdentityProviders() {
  const list = useLoad(listIdentityProviders, []);
  const [mode, setMode] = useState<{ kind: "add" } | { kind: "edit" | "remove"; provider: IdentityProvider } | null>(null);
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [confirmation, setConfirmation] = useState<Confirmation>(NO_CONFIRMATION);
  const add = useAction(addIdentityProvider);
  const change = useAction(changeIdentityProvider);
  const remove = useAction(removeIdentityProvider);
  const action = mode?.kind === "add" ? add : mode?.kind === "edit" ? change : remove;
  const close = () => { setMode(null); setDraft(EMPTY); setConfirmation(NO_CONFIRMATION); add.clear(); change.clear(); remove.clear(); };
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const providers = list.data?.providers ?? [];
  const sp = list.data?.serviceProvider;

  return (
    <>
      <Card title="Single sign-on providers" actions={<button type="button" className="btn btn-primary" onClick={() => { setDraft(EMPTY); setMode({ kind: "add" }); }}>Add provider</button>}>
        <div className="card-body" style={{ paddingBottom: 12 }}>
          <p className="muted" style={{ marginTop: 0 }}>
            Users can sign in with the account of an organisation, for example Microsoft Entra ID, Google Workspace or Okta. The provider checks the password and the other steps of its sign-in. When TOTP is on for a user, the panel also asks for a code.
          </p>
          <FormError error={list.error} />
        </div>
        <TableWrap>
          <table>
            <thead><tr><th scope="col">Provider</th><th scope="col">Status</th><th scope="col">Connect by email</th><th scope="col">Users</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {providers.map((p) => (
                <tr key={p.id}>
                  <td><strong>{p.name}</strong><span className="cell-sub">{PROTOCOLS[p.protocol]} · {p.protocol === "oidc" ? p.issuer : p.entityId}</span></td>
                  <td>
                    {p.enabled ? <Status tone="success">On</Status> : <Status tone="neutral">Off</Status>}
                    {p.certificateExpiresAt && <span className="cell-sub">Certificate expires {longDate(p.certificateExpiresAt)}</span>}
                  </td>
                  <td>{p.linkByEmail ? p.emailDomains.join(", ") : "No"}</td>
                  <td>{p.users}</td>
                  <td className="cell-actions" style={{ width: 170 }}>
                    <div className="btn-group">
                      <button type="button" className="btn btn-sm" aria-label={`Edit ${p.name}`} onClick={() => { setDraft(draftOf(p)); setMode({ kind: "edit", provider: p }); }}>Edit</button>
                      <button type="button" className="btn btn-sm btn-danger" aria-label={`Remove ${p.name}`} onClick={() => setMode({ kind: "remove", provider: p })}>Remove</button>
                    </div>
                  </td>
                </tr>
              ))}
              {!list.loading && providers.length === 0 && <tr><td colSpan={5} className="muted">No providers.</td></tr>}
            </tbody>
          </table>
        </TableWrap>
      </Card>
      {mode?.kind === "remove" && (
        <Modal title="Remove the provider" onClose={close} footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="sso-provider" className="btn btn-danger" disabled={remove.busy}>Remove</button></>}>
          <form id="sso-provider" noValidate onSubmit={async (e) => { e.preventDefault(); if ((await remove.run(mode.provider.id, confirmation)).ok) { close(); list.reload(); } }}>
            <FormError error={remove.error} shown={["password", "totp"]} />
            <p style={{ marginBottom: 12 }}>Users can no longer sign in with <strong>{mode.provider.name}</strong>. The panel also removes the {mode.provider.users === 1 ? "connected account" : `${mode.provider.users} connected accounts`}. The users can still sign in with a password or a passkey.</p>
            <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={remove.error} reason="This changes how users sign in." />
          </form>
        </Modal>
      )}
      {(mode?.kind === "add" || mode?.kind === "edit") && (
        <Modal wide title={mode.kind === "add" ? "Add a provider" : `Edit ${mode.provider.name}`} onClose={close}
          footer={<><button type="button" className="btn" onClick={close}>Cancel</button><button type="submit" form="sso-provider" className="btn btn-primary" disabled={action.busy}>{mode.kind === "add" ? "Add provider" : "Save"}</button></>}>
          <form id="sso-provider" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = mode.kind === "add" ? await add.run(inputOf(draft, true), confirmation) : await change.run(mode.provider.id, inputOf(draft, false), confirmation);
            if (r.ok) { close(); list.reload(); }
          }}>
            <FormError error={action.error} shown={["name", "issuer", "clientId", "clientSecret", "entityId", "ssoUrl", "certificate", "emailDomains", "password", "totp"]} />
            {mode.kind === "add" && (
              <fieldset className="radio-cards">
                <legend>Protocol</legend>
                {([
                  ["oidc", "For example Google Workspace, Microsoft Entra ID, Okta, Keycloak or Authentik."],
                  ["saml", "For example Microsoft Entra ID, Okta, AD FS or Shibboleth."],
                ] as const).map(([p, hint]) => (
                  <label key={p} className="radio-card">
                    <input type="radio" name="protocol" checked={draft.protocol === p} onChange={() => set({ protocol: p })} />
                    <span><strong>{PROTOCOLS[p]}</strong><span>{hint}</span></span>
                  </label>
                ))}
              </fieldset>
            )}
            {sp && (
              <fieldset className="form-section">
                <legend>Give these values to the provider</legend>
                {draft.protocol === "oidc" ? <>
                  <p className="hint">Make an application of the type "web" at the provider, with this redirect URI. Then copy the client ID and the client secret of the application to this form.</p>
                  <p className="sp-label">Redirect URI</p>
                  <CopyField value={sp.redirectUri} label="Redirect URI" />
                </> : <>
                  <p className="hint">Make an application at the provider with these values, or give the provider the metadata at the address of the entity ID. Set the NameID format to "persistent" or "emailAddress".</p>
                  <p className="sp-label">Entity ID (also the metadata address)</p>
                  <CopyField value={sp.entityId} label="Entity ID" />
                  <p className="sp-label">Assertion consumer service (ACS) URL</p>
                  <CopyField value={sp.acsUrl} label="ACS URL" />
                </>}
              </fieldset>
            )}
            <fieldset className="form-section">
              <legend>The provider</legend>
              <Field label="Name" hint={`The sign-in page shows "Sign in with ${draft.name.trim() || "<name>"}".`} error={action.error?.fields.name}>{(p) => <input {...p} className="input" maxLength={100} value={draft.name} onChange={(e) => set({ name: e.target.value })} placeholder="Microsoft Entra ID" />}</Field>
              {draft.protocol === "oidc" ? <>
                <Field label="Issuer" hint="The panel reads the discovery document at the issuer, in /.well-known/openid-configuration." error={action.error?.fields.issuer}>{(p) => <input {...p} className="input" type="url" value={draft.issuer} onChange={(e) => set({ issuer: e.target.value })} placeholder="https://login.example.com" />}</Field>
                <Field label="Client ID" error={action.error?.fields.clientId}>{(p) => <input {...p} className="input" value={draft.clientId} onChange={(e) => set({ clientId: e.target.value })} />}</Field>
                <Field label="Client secret" hint={mode.kind === "edit" ? "Leave this empty to keep the current secret." : "The panel keeps the secret encrypted, and does not show it again."} error={action.error?.fields.clientSecret}>{(p) => <input {...p} className="input" type="password" autoComplete="off" value={draft.clientSecret} onChange={(e) => set({ clientSecret: e.target.value })} />}</Field>
              </> : <>
                <Field label="Entity ID of the provider" hint="The provider also calls this the issuer or the identifier." error={action.error?.fields.entityId}>{(p) => <input {...p} className="input" value={draft.entityId} onChange={(e) => set({ entityId: e.target.value })} placeholder="https://sts.windows.net/…/" />}</Field>
                <Field label="Sign-in URL" hint="The single sign-on service of the provider, for the HTTP-Redirect binding." error={action.error?.fields.ssoUrl}>{(p) => <input {...p} className="input" type="url" value={draft.ssoUrl} onChange={(e) => set({ ssoUrl: e.target.value })} placeholder="https://login.example.com/saml2" />}</Field>
                <Field label="Signing certificate" hint="The X.509 certificate of the provider, in PEM format or as base64 text." error={action.error?.fields.certificate}>{(p) => <textarea {...p} className="input manifest-input" spellCheck={false} value={draft.certificate} onChange={(e) => set({ certificate: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" />}</Field>
              </>}
              <label className="check" style={{ marginTop: 12 }}><input type="checkbox" checked={draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} /> Show the provider on the sign-in page</label>
            </fieldset>
            <fieldset className="form-section">
              <legend>Users</legend>
              <p className="hint">A user connects the account in Settings. You can also let the panel connect the user with the same email at the first sign-in. Use this only when the provider controls the email domains, because a person who controls an email at the provider can then sign in as the user with that email.</p>
              <label className="check"><input type="checkbox" checked={draft.linkByEmail} onChange={(e) => set({ linkByEmail: e.target.checked })} /> Connect users by email</label>
              {draft.linkByEmail && <Field label="Email domains" hint="Separate the domains with commas, for example example.com, example.org." error={action.error?.fields.emailDomains ?? Object.entries(action.error?.fields ?? {}).find(([k]) => k.startsWith("emailDomains."))?.[1]}>{(p) => <input {...p} className="input" value={draft.emailDomains} onChange={(e) => set({ emailDomains: e.target.value })} />}</Field>}
            </fieldset>
            <ConfirmIdentityFields value={confirmation} onChange={setConfirmation} error={action.error} reason="This changes how users sign in." />
          </form>
        </Modal>
      )}
    </>
  );
}
