import { KeyRound, LogIn } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useAction } from "../api/hooks";
import { signInMethods, signOnUrl, type SignInMethods } from "../api/live";
import { signIn, signInWithPasskey } from "../api/store";
import { BrandMark, useBrand } from "../components/Brand";
import { Field, FormError } from "../components/ui";
import { fragmentValue, signOnError } from "../lib/sso";
import { passkeysSupported } from "../lib/webauthn";

/**
 * Sign in with an email and a password (FR-103). The panel asks for a code from the
 * authenticator app only when the account has TOTP, and only after a correct password. A
 * passkey and the single sign-on providers are the other ways to sign in.
 */
export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const brand = useBrand();
  const methods = useSignInMethods();
  // After a new password, the page of the link sends the email.
  const [email, setEmail] = useState((location.state as { email?: string } | null)?.email ?? "");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [step, setStep] = useState<"start" | "totp">("start");
  const pw = useAction(async (totp: string) => signIn(email, password, totp));
  const passkey = useAction(signInWithPasskey);
  // Each role has its own first page (lib/roles.ts).
  const go = () => navigate("/");
  const busy = pw.busy || passkey.busy;
  // FR-103: a passkey works only on a panel domain, and only in a browser that has WebAuthn.
  const passkeys = methods?.passkeys && passkeysSupported();
  const providers = methods?.providers ?? [];
  // A sign-in with a provider that did not work comes back with a code in the fragment.
  const [ssoError, setSsoError] = useState(() => signOnError(fragmentValue(location.hash, "sso-error")));
  useEffect(() => {
    if (location.hash) navigate({ pathname: location.pathname, search: location.search }, { replace: true, state: location.state });
  }, [location, navigate]);

  return (
    <main className="login" id="main">
      <div className="card">
        <div className="brand">
          <BrandMark brand={brand} tone="light" />
          <span className="visually-hidden">{brand.name}</span>
        </div>
        <h1 style={{ fontSize: 22, marginBottom: 6 }}>Sign in</h1>
        <p className="muted" style={{ marginBottom: 18 }}>
          {step === "totp"
            ? <>The password is correct. Type the code from your authenticator app for <strong>{email}</strong>.</>
            : "Use your email and your password."}
        </p>
        {ssoError && <div className="alert alert-danger" role="alert"><p>{ssoError}</p></div>}
        <FormError error={pw.error} shown={step === "start" ? ["email", "password"] : ["totp"]} />
        {step === "start" ? (
          <form noValidate onSubmit={async (e) => {
            e.preventDefault();
            setSsoError(null);
            const r = await pw.run("");
            if (r.ok) go();
            // The password is correct and the account has TOTP: ask for the code. This is not an error.
            else if (r.error.fields.totp && !r.error.fields.email && !r.error.fields.password) {
              pw.clear();
              setCode("");
              setStep("totp");
            }
          }}>
            <Field label="Email" error={pw.error?.fields.email}>{(p) => <input {...p} className="input" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />}</Field>
            <Field label="Password" error={pw.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={busy}>{pw.busy ? "Signing in…" : "Sign in"}</button>
            {methods?.passwordReset && <div className="sign-in-links"><Link className="btn-link" to="/forgot-password" state={{ email }}>Forgot your password?</Link></div>}
            {(passkeys || providers.length > 0) && <>
              <div className="divider">or</div>
              <FormError error={passkey.error} />
              <div className="sign-in-options">
                {passkeys && (
                  <button type="button" className="btn" disabled={busy} onClick={async () => { pw.clear(); setSsoError(null); if ((await passkey.run()).ok) go(); }}>
                    <KeyRound size={18} aria-hidden="true" />{passkey.busy ? "Waiting for the passkey…" : "Sign in with a passkey"}
                  </button>
                )}
                {/* The browser goes to the provider, and the provider sends it back to the page /sso. */}
                {providers.map((p) => <a key={p.id} className="btn" href={signOnUrl(p.id)}><LogIn size={18} aria-hidden="true" />Sign in with {p.name}</a>)}
              </div>
            </>}
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

/** The ways to sign in for the domain of the page. Until the API answers, the page shows only the password. */
function useSignInMethods(): SignInMethods | null {
  const [methods, setMethods] = useState<SignInMethods | null>(null);
  useEffect(() => {
    let current = true;
    signInMethods().then((m) => { if (current) setMethods(m); }).catch(() => {});
    return () => { current = false; };
  }, []);
  return methods;
}
