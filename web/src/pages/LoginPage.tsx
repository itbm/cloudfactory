import { KeyRound } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAction } from "../api/hooks";
import { features } from "../api/mode";
import { signIn } from "../api/store";
import { Logo } from "../components/Layout";
import { Field, FormError } from "../components/ui";

/**
 * Sign in with a passkey, or with a password (FR-103). The panel asks for a code from the
 * authenticator app only when the account has TOTP, and only after a correct password.
 */
export function LoginPage() {
  const navigate = useNavigate();
  const [email, setEmail] = useState(features.passkeys ? "alex@acme.example" : "");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [step, setStep] = useState<"start" | "totp">("start");
  const passkey = useAction(async () => signIn("passkey"));
  const pw = useAction(async (totp: string) => signIn("password", totp, { email, password }));
  const go = () => navigate("/websites");
  const busy = passkey.busy || pw.busy;

  return (
    <main className="login" id="main">
      <div className="card">
        <div className="brand">
          <span style={{ background: "#0f1b33", borderRadius: 8, padding: 4, display: "inline-flex" }}><Logo /></span>
          <span style={{ color: "var(--text)" }}>cloudfactory<span>.io</span></span>
        </div>
        <h1 style={{ fontSize: 22, marginBottom: 6 }}>Sign in</h1>
        <p className="muted" style={{ marginBottom: 18 }}>
          {step === "totp"
            ? <>The password is correct. Type the code from your authenticator app for <strong>{email}</strong>.</>
            : features.passkeys ? "Use a passkey, or your email and your password." : "Use your email and your password."}
        </p>
        <FormError error={passkey.error ?? pw.error} shown={step === "start" ? ["email", "password"] : ["totp"]} />
        {features.passkeys && step === "start" && <>
        <button type="button" className="btn btn-primary" style={{ width: "100%" }} disabled={busy} onClick={async () => { if ((await passkey.run()).ok) go(); }}>
          <KeyRound size={18} aria-hidden="true" /> Sign in with a passkey
        </button>
        <div className="divider">or</div>
        </>}
        {step === "start" ? (
          <form noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = await pw.run("");
            if (r.ok) go();
            // The password is correct and the account has TOTP: ask for the code. This is not an error.
            else if (r.error.fields.totp && !r.error.fields.email && !r.error.fields.password) {
              pw.clear();
              setCode("");
              setStep("totp");
            }
          }}>
            <Field label="Email" error={pw.error?.fields.email}>{(p) => <input {...p} className="input" type="email" autoComplete="username webauthn" value={email} onChange={(e) => setEmail(e.target.value)} />}</Field>
            <Field label="Password" error={pw.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <button type="submit" className={features.passkeys ? "btn" : "btn btn-primary"} style={{ width: "100%", marginTop: 16 }} disabled={busy}>{pw.busy ? "Signing in…" : "Sign in"}</button>
          </form>
        ) : (
          <form noValidate onSubmit={async (e) => {
            e.preventDefault();
            const r = await pw.run(code);
            if (r.ok) go();
            // An error that is not about the code (for example, too many attempts) shows on the first step.
            else if (!r.error.fields.totp) setStep("start");
          }}>
            <Field label="6-digit code" hint="Open your authenticator app." error={pw.error?.fields.totp}>
              {(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} autoFocus />}
            </Field>
            <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={busy}>{pw.busy ? "Signing in…" : "Sign in"}</button>
            <button type="button" className="btn-link" onClick={() => { pw.clear(); setStep("start"); }}>Back</button>
          </form>
        )}
      </div>
    </main>
  );
}
