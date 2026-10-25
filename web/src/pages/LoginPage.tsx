import { KeyRound } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAction } from "../api/hooks";
import { signIn } from "../api/store";
import { Logo } from "../components/Layout";
import { Field, FormError } from "../components/ui";

/** Sign in with a passkey, or with a password and TOTP (FR-103). */
export function LoginPage() {
  const navigate = useNavigate();
  const [email, setEmail] = useState("alex@acme.example");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [step, setStep] = useState<"start" | "totp">("start");
  const passkey = useAction(async () => signIn("passkey"));
  const pw = useAction(async () => signIn("password", code));
  const go = () => navigate("/websites");

  return (
    <main className="login" id="main">
      <div className="card">
        <div className="brand">
          <span style={{ background: "#0f1b33", borderRadius: 8, padding: 4, display: "inline-flex" }}><Logo /></span>
          <span style={{ color: "var(--text)" }}>cloudfactory<span>.io</span></span>
        </div>
        <h1 style={{ fontSize: 22, marginBottom: 6 }}>Sign in</h1>
        <p className="muted" style={{ marginBottom: 18 }}>Use a passkey, or your password and a code from your authenticator app.</p>
        <FormError error={passkey.error ?? pw.error} />
        <button type="button" className="btn btn-primary" style={{ width: "100%" }} onClick={async () => { if ((await passkey.run()).ok) go(); }}>
          <KeyRound size={18} aria-hidden="true" /> Sign in with a passkey
        </button>
        <div className="divider">or</div>
        {step === "start" ? (
          <form noValidate onSubmit={(e) => { e.preventDefault(); setStep("totp"); }}>
            <Field label="Email">{(p) => <input {...p} className="input" type="email" autoComplete="username webauthn" value={email} onChange={(e) => setEmail(e.target.value)} />}</Field>
            <Field label="Password">{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <button type="submit" className="btn" style={{ width: "100%", marginTop: 16 }}>Continue</button>
          </form>
        ) : (
          <form noValidate onSubmit={async (e) => { e.preventDefault(); if ((await pw.run()).ok) go(); }}>
            <Field label="6-digit code" hint="Open your authenticator app." error={pw.error?.fields.totp}>
              {(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} autoFocus />}
            </Field>
            <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }}>Sign in</button>
            <button type="button" className="btn-link" onClick={() => setStep("start")}>Back</button>
          </form>
        )}
      </div>
    </main>
  );
}
