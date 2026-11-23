import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { useAction } from "../api/hooks";
import { requestPasswordReset, resetPassword } from "../api/store";
import { BrandMark, useBrand } from "../components/Brand";
import { Field, FormError, Status } from "../components/ui";

/** The card of the pages before sign-in: the brand, a heading and the text under it. */
export function SignInCard({ title, intro, children }: { title: string; intro: React.ReactNode; children: React.ReactNode }) {
  const brand = useBrand();
  return (
    <main className="login" id="main">
      <div className="card">
        <div className="brand">
          <BrandMark brand={brand} tone="light" />
          <span className="visually-hidden">{brand.name}</span>
        </div>
        <h1 style={{ fontSize: 22, marginBottom: 6 }}>{title}</h1>
        <p className="muted" style={{ marginBottom: 18 }}>{intro}</p>
        {children}
      </div>
    </main>
  );
}

/**
 * A user who does not know the password asks for an email with a link. The answer is the same
 * for each email, so the page does not show which emails have a user.
 */
export function ForgotPasswordPage() {
  const location = useLocation();
  const [email, setEmail] = useState((location.state as { email?: string } | null)?.email ?? "");
  const [sent, setSent] = useState<string | null>(null);
  const send = useAction(requestPasswordReset);
  if (sent) {
    return (
      <SignInCard title="Look in your mailbox" intro={sent}>
        <p style={{ marginBottom: 12 }}>The email goes to <strong>{email}</strong>. If no email comes in a few minutes, make sure that the email is correct, and ask again.</p>
        <Link className="btn btn-primary" style={{ width: "100%" }} to="/login">Back to sign in</Link>
        <button type="button" className="btn-link" onClick={() => { send.clear(); setSent(null); }}>Use a different email</button>
      </SignInCard>
    );
  }
  return (
    <SignInCard title="Reset your password" intro="Type the email of your user. The panel sends a link to set a new password.">
      <form noValidate onSubmit={async (e) => {
        e.preventDefault();
        const r = await send.run(email);
        if (r.ok) setSent(r.value);
      }}>
        <FormError error={send.error} shown={["email"]} />
        <Field label="Email" error={send.error?.fields.email}>{(p) => <input {...p} className="input" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />}</Field>
        <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={send.busy}>{send.busy ? "Sending…" : "Send the link"}</button>
        <Link className="btn-link" to="/login">Back to sign in</Link>
      </form>
    </SignInCard>
  );
}

/**
 * The page of the link in the email. The email and the code are after "#" in the address, so
 * the browser does not send them to a server, and no access log has them.
 */
export function ResetPasswordPage() {
  const location = useLocation();
  const navigate = useNavigate();
  // The page reads the link one time. After the change, the address has no code.
  const [{ email, token }] = useState(() => {
    const params = new URLSearchParams(location.hash.replace(/^#/, ""));
    return { email: params.get("email") ?? "", token: params.get("token") ?? "" };
  });
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [done, setDone] = useState(false);
  const reset = useAction(resetPassword);
  if (!email || !token) {
    return (
      <SignInCard title="Set a new password" intro="This link is not complete. Open the link from the email again, or ask for a new email.">
        <Link className="btn btn-primary" style={{ width: "100%" }} to="/forgot-password">Ask for a new link</Link>
        <Link className="btn-link" to="/login">Back to sign in</Link>
      </SignInCard>
    );
  }
  // The code is not correct, or it is too old. A new password cannot help: the user asks for a new link.
  const tokenError = reset.error?.fields.token;
  if (tokenError) {
    return (
      <SignInCard title="This link does not work" intro={tokenError}>
        <Link className="btn btn-primary" style={{ width: "100%" }} to="/forgot-password" state={{ email }}>Ask for a new link</Link>
        <Link className="btn-link" to="/login">Back to sign in</Link>
      </SignInCard>
    );
  }
  if (done) {
    return (
      <SignInCard title="Your password is changed" intro={<>Sign in with the new password. All sessions and API tokens of <strong>{email}</strong> are stopped.</>}>
        <p role="status" style={{ marginBottom: 12 }}><Status tone="success">Changed</Status> If your user has an authenticator app, you still need its code.</p>
        <Link className="btn btn-primary" style={{ width: "100%" }} to="/login" state={{ email }}>Sign in</Link>
      </SignInCard>
    );
  }
  return (
    <SignInCard title="Set a new password" intro={<>Set a new password for <strong>{email}</strong>. A new password stops all sessions and deletes the API tokens of the user.</>}>
      <form noValidate onSubmit={async (e) => {
        e.preventDefault();
        if ((await reset.run(email, token, next, confirm)).ok) {
          // The code works one time only. Remove it from the address and the history.
          navigate(location.pathname, { replace: true });
          setDone(true);
        }
      }}>
        <FormError error={reset.error} shown={["password", "password_confirmation"]} />
        <input type="email" autoComplete="username" value={email} readOnly hidden />
        <Field label="New password" hint="Use 12 or more characters." error={reset.error?.fields.password}>{(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} autoFocus />}</Field>
        <Field label="Type the new password again" error={reset.error?.fields.password_confirmation}>{(p) => <input {...p} className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />}</Field>
        <button type="submit" className="btn btn-primary" style={{ width: "100%", marginTop: 16 }} disabled={reset.busy}>{reset.busy ? "Saving…" : "Set password"}</button>
        <Link className="btn-link" to="/login">Back to sign in</Link>
      </form>
    </SignInCard>
  );
}
