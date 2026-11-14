import { useStore } from "../api/hooks";
import type { ApiError } from "../api/store";
import type { Confirmation } from "../api/types";
import { Field } from "./ui";

export const NO_CONFIRMATION: Confirmation = { password: "", totp: "" };

/**
 * Second audit, finding F09: a change to an administrator account needs the password of the
 * signed-in user and, when TOTP is on, a code. A stolen session alone cannot make, reset or
 * delete an administrator.
 */
export function ConfirmIdentityFields({ value, onChange, error }: { value: Confirmation; onChange: (value: Confirmation) => void; error: ApiError | null }) {
  const state = useStore();
  return (
    <fieldset className="form-section">
      <legend>Confirm that it is you</legend>
      <p className="hint">
        This changes who can sign in as an administrator. Type your password{state.totpEnabled ? " and a code from your authenticator app" : ""}. After 5 incorrect attempts, the panel refuses these changes for 15 minutes.
      </p>
      <Field label="Your password" error={error?.fields.password}>
        {(p) => <input {...p} className="input" type="password" autoComplete="current-password" value={value.password} onChange={(e) => onChange({ ...value, password: e.target.value })} />}
      </Field>
      {state.totpEnabled && (
        <Field label="6-digit code from the app" error={error?.fields.totp}>
          {(p) => <input {...p} className="input" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={value.totp} onChange={(e) => onChange({ ...value, totp: e.target.value })} />}
        </Field>
      )}
    </fieldset>
  );
}
