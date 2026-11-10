import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { changePassword, signOut } from "../api/store";
import { Logo } from "../components/Layout";
import { Field, FormError } from "../components/ui";

/**
 * Audit finding B5: a user with a temporary password sets a new password before the panel
 * shows other pages. The API refuses each other request until the password is changed.
 */
export function SetPasswordPage() {
  const state = useStore();
  const navigate = useNavigate();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [code, setCode] = useState("");
  const change = useAction(changePassword);
  return (
    <main className="login" id="main">
      <div className="card">
        <div className="brand">
          <span style={{ background: "#0f1b33", borderRadius: 8, padding: 4, display: "inline-flex" }}><Logo /></span>
          <span style={{ color: "var(--text)" }}>cloudfactory<span>.io</span></span>
        </div>
        <h1 style={{ fontSize: 22, marginBottom: 6 }}>Set a new password</h1>
        <p className="muted" style={{ marginBottom: 18 }}>
          Your password is temporary. Set a new password to use the panel. You are signed in as <strong>{state.session.user.email}</strong>.
        </p>
        <form noValidate onSubmit={(e) => { e.preventDefault(); void change.run(current, next, confirm, code); }}>
          <FormError error={change.error} shown={["currentPassword", "password", "password_confirmation", "totp"]} />
          <Field label="Temporary password" error={change.error?.fields.currentPassword}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} autoFocus />}</Field>
          <Field label="New password" hint="Use 12 or more characters." error={change.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />}</Field>
          <Field label="Type the new password again" error={change.error?.fields.password_confirmation}>{(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />}</Field>
          {state.totpEnabled && <Field label="6-digit code from the app" error={change.error?.fields.totp}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />}</Field>}
          <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={change.busy}>Set password</button>
          <button type="button" className="btn-link" onClick={() => { signOut(); navigate("/login"); }}>Sign out</button>
        </form>
      </div>
    </main>
  );
}
