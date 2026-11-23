import { KeyRound } from "lucide-react";
import { useState } from "react";
import { useAction, useLoad, useStore } from "../api/hooks";
import type { Passkey } from "../api/live";
import { addPasskey, listPasskeys, removePasskey, renamePasskey } from "../api/store";
import { Card, Field, FormError, Modal, Status } from "../components/ui";
import { relativeTime, shortDate } from "../lib/format";
import { passkeysSupported } from "../lib/webauthn";

/** A name for a new passkey from the browser, for example "Firefox on Linux". The user can change it. */
function deviceName(): string {
  const agent = navigator.userAgent;
  const browser = /Edg\//.test(agent) ? "Edge" : /Firefox\//.test(agent) ? "Firefox" : /Chrome\//.test(agent) ? "Chrome" : /Safari\//.test(agent) ? "Safari" : "Browser";
  const system = /iPhone|iPad/.test(agent) ? "iOS" : /Android/.test(agent) ? "Android" : /Mac OS X/.test(agent) ? "macOS" : /Windows/.test(agent) ? "Windows" : /Linux/.test(agent) ? "Linux" : "";
  return system ? `${browser} on ${system}` : browser;
}

/**
 * FR-103: the passkeys of the user. A passkey signs in with no password and no TOTP code. To
 * add or remove one, the user gives the password and, when TOTP is on, a code.
 */
export function PasskeysCard() {
  const state = useStore();
  const list = useLoad(listPasskeys, []);
  const [mode, setMode] = useState<{ kind: "add" } | { kind: "rename" | "remove"; passkey: Passkey } | null>(null);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const add = useAction(addPasskey);
  const rename = useAction(renamePasskey);
  const remove = useAction(removePasskey);
  const supported = passkeysSupported();
  const close = () => { setMode(null); setName(""); setPassword(""); setCode(""); add.clear(); rename.clear(); remove.clear(); };
  const passkeys = list.data ?? [];
  const action = mode?.kind === "add" ? add : mode?.kind === "rename" ? rename : remove;

  return (
    <Card title="Passkeys">
      <div className="card-body">
        <FormError error={list.error} />
        <p className="muted">
          Sign in with your fingerprint, your face or the PIN of your device, in place of your password and the code. A passkey works only on the domain where you add it, so a fake site cannot use it.
        </p>
        {passkeys.length > 0 && (
          <ul className="passkey-list" aria-label="Your passkeys">
            {passkeys.map((p) => (
              <li key={p.id}>
                <KeyRound size={20} aria-hidden="true" className="passkey-icon" />
                <div className="passkey-text">
                  <strong>{p.name}</strong>
                  <span className="cell-sub">
                    {p.domain} · Added {shortDate(p.createdAt)} · {p.lastUsedAt ? `Used ${relativeTime(p.lastUsedAt)}` : "Not used yet"}
                    {p.synced && <> · Synced</>}
                  </span>
                </div>
                <div className="btn-group">
                  <button type="button" className="btn btn-sm" onClick={() => { setName(p.name); setMode({ kind: "rename", passkey: p }); }} aria-label={`Rename ${p.name}`}>Rename</button>
                  <button type="button" className="btn btn-sm btn-danger" onClick={() => setMode({ kind: "remove", passkey: p })} aria-label={`Remove ${p.name}`}>Remove</button>
                </div>
              </li>
            ))}
          </ul>
        )}
        {!list.loading && passkeys.length === 0 && <p style={{ marginTop: 10 }}><Status tone="neutral">None</Status></p>}
        {!supported && <p className="note-danger" style={{ marginTop: 10 }}>This browser cannot make passkeys.</p>}
        <div className="btn-group" style={{ marginTop: 14 }}>
          <button type="button" className="btn btn-primary" disabled={!supported} onClick={() => { setName(deviceName()); setMode({ kind: "add" }); }}>Add a passkey</button>
        </div>
      </div>
      {mode && (
        <Modal
          title={mode.kind === "add" ? "Add a passkey" : mode.kind === "rename" ? "Rename the passkey" : "Remove the passkey"}
          onClose={close}
          footer={<>
            <button type="button" className="btn" onClick={close}>Cancel</button>
            <button type="submit" form="passkey" className={mode.kind === "remove" ? "btn btn-danger" : "btn btn-primary"} disabled={action.busy}>
              {mode.kind === "add" ? (add.busy ? "Waiting for the device…" : "Add passkey") : mode.kind === "rename" ? "Save" : "Remove"}
            </button>
          </>}
        >
          <form id="passkey" noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = mode.kind === "add" ? await add.run(name, password, code)
              : mode.kind === "rename" ? await rename.run(mode.passkey.id, name)
              : await remove.run(mode.passkey.id, password, code);
            if (r.ok) { close(); list.reload(); }
          }}>
            <FormError error={action.error} shown={["name", "password", "totp"]} />
            {mode.kind === "add" && <p style={{ marginBottom: 12 }}>Give the passkey a name, then type your password{state.totpEnabled ? " and a code from the app" : ""}. Your browser then asks you to make the passkey, for example with your fingerprint.</p>}
            {mode.kind === "remove" && <p style={{ marginBottom: 12 }}>You can no longer sign in with <strong>{mode.passkey.name}</strong>. Also remove it from your device or your password manager. Type your password{state.totpEnabled ? " and a code from the app" : ""}.</p>}
            {mode.kind !== "remove" && <Field label="Name" hint="For example the device or the password manager." error={action.error?.fields.name}>{(p) => <input {...p} className="input" maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}</Field>}
            {mode.kind !== "rename" && <>
              <Field label="Password" error={action.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
              {state.totpEnabled && <Field label="6-digit code from the app" error={action.error?.fields.totp}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />}</Field>}
            </>}
          </form>
        </Modal>
      )}
    </Card>
  );
}
