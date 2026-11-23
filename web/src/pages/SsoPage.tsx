import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useAction } from "../api/hooks";
import { completeSignOn } from "../api/store";
import { Field, FormError } from "../components/ui";
import { fragmentValue } from "../lib/sso";
import { SignInCard } from "./PasswordResetPages";

/**
 * The provider of a single sign-on sends the browser to this page, with a ticket in the
 * fragment of the address. The page changes the ticket into a session. When TOTP is on for the
 * user, the page asks for a code first.
 */
export function SsoPage() {
  const location = useLocation();
  const navigate = useNavigate();
  // The page reads the ticket one time, and then removes it from the address and the history.
  const [ticket] = useState(() => fragmentValue(location.hash, "ticket"));
  const [step, setStep] = useState<"start" | "totp" | "failed">(ticket ? "start" : "failed");
  const [code, setCode] = useState("");
  const complete = useAction(async (totp: string) => completeSignOn(ticket ?? "", totp));
  const started = useRef(false);

  useEffect(() => {
    if (location.hash) navigate({ pathname: location.pathname }, { replace: true });
  }, [location, navigate]);

  useEffect(() => {
    if (!ticket || started.current) return;
    started.current = true;
    void complete.run("").then((r) => {
      if (r.ok) navigate("/", { replace: true });
      // TOTP is on for the user. This is not an error.
      else if (r.error.fields.totp) { complete.clear(); setStep("totp"); }
      else setStep("failed");
    });
  }, [ticket, complete, navigate]);

  if (step === "totp") {
    return (
      <SignInCard title="Sign in" intro="The provider signed you in. Type the code from your authenticator app.">
        <form noValidate onSubmit={async (e) => {
          e.preventDefault();
          const r = await complete.run(code);
          if (r.ok) navigate("/", { replace: true });
          // The ticket stopped, for example after too many wrong codes.
          else if (!r.error.fields.totp) setStep("failed");
        }}>
          <FormError error={complete.error} shown={["totp"]} />
          <Field label="6-digit code" hint="Open your authenticator app." error={complete.error?.fields.totp}>
            {(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} autoFocus />}
          </Field>
          <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={complete.busy}>{complete.busy ? "Signing in…" : "Sign in"}</button>
          <Link className="btn-link" to="/login">Back to sign in</Link>
        </form>
      </SignInCard>
    );
  }
  if (step === "failed") {
    return (
      <SignInCard title="Sign in" intro="The sign-in with the provider did not work.">
        {complete.error ? <FormError error={complete.error} /> : <div className="alert alert-danger" role="alert"><p>The address has no sign-in. Start again from the sign-in page.</p></div>}
        <Link className="btn btn-primary" style={{ width: "100%" }} to="/login">Back to sign in</Link>
      </SignInCard>
    );
  }
  return (
    <SignInCard title="Sign in" intro="Signing in with the provider…">
      <p className="muted" role="status">Wait a moment.</p>
    </SignInCard>
  );
}
