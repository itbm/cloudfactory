import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAction, useStore } from "../api/hooks";
import { setTotp, signOut, startTotpSetup } from "../api/store";
import { Logo } from "../components/Layout";
import { Field, FormError } from "../components/ui";
import { CopyField } from "./website/AccessTab";

/**
 * Second audit, finding F09: an administrator or a reseller sets up TOTP before the panel
 * shows other pages. The API refuses each other request of the session until TOTP is on.
 */
export function SetTotpPage() {
  const state = useStore();
  const navigate = useNavigate();
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const start = useAction(startTotpSetup);
  const turnOn = useAction(setTotp);
  // Each request for a key makes the key before it invalid. Ask one time only, also when
  // React runs the effect two times in development.
  const asked = useRef(false);
  const newKey = async () => {
    const r = await start.run();
    if (r.ok) setSetup(r.value);
  };
  useEffect(() => {
    if (asked.current) return;
    asked.current = true;
    void newKey();
  }, []);
  const role = state.session.user.role === "administrator" ? "administrator" : "reseller";
  return (
    <main className="login" id="main">
      <div className="card">
        <div className="brand">
          <span style={{ background: "#0f1b33", borderRadius: 8, padding: 4, display: "inline-flex" }}><Logo /></span>
          <span style={{ color: "var(--text)" }}>cloudfactory<span>.io</span></span>
        </div>
        <h1 style={{ fontSize: 22, marginBottom: 6 }}>Set up an authenticator app</h1>
        <p className="muted" style={{ marginBottom: 18 }}>
          You are signed in as <strong>{state.session.user.email}</strong>. Each {role} must sign in with a password and a code from an authenticator app. Then a stolen password alone does not give access to the websites of your customers.
        </p>
        <form noValidate onSubmit={(e) => { e.preventDefault(); void turnOn.run(true, code, password); }}>
          <FormError error={start.error ?? turnOn.error} shown={["password", "code"]} />
          {setup && (
            <>
              <p>1. Add this key to your authenticator app, for example 1Password, Google Authenticator or Microsoft Authenticator. On a phone, you can <a href={setup.uri}>open the key in the app</a>.</p>
              <CopyField value={setup.secret.replace(/(.{4})/g, "$1 ").trim()} label="TOTP key" />
              <p className="muted small" style={{ marginTop: 8, marginBottom: 14 }}>
                The key is valid for 10 minutes. Keep a copy in a safe place, in case you lose your phone. <button type="button" className="btn-link" style={{ display: "inline", width: "auto", minHeight: 0, margin: 0, padding: 0 }} disabled={start.busy} onClick={() => void newKey()}>Get a new key</button>
              </p>
              <p style={{ marginBottom: 10 }}>2. Type your password and the code that the app shows.</p>
            </>
          )}
          {!setup && !start.busy && <button type="button" className="btn" style={{ marginBottom: 14 }} onClick={() => void newKey()}>Get a new key</button>}
          <Field label="Password" error={turnOn.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
          <Field label="6-digit code from the app" error={turnOn.error?.fields.code}>{(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />}</Field>
          <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={turnOn.busy || !setup}>Turn on and continue</button>
          <button type="button" className="btn-link" onClick={() => { signOut(); navigate("/login"); }}>Sign out</button>
        </form>
      </div>
    </main>
  );
}
