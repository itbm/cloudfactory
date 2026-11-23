// Single sign-on: the panel API sends the browser back to the web panel with a value in the
// fragment of the address, for example "/login#sso-error=no-account". The browser does not send
// the fragment to a server. The page removes the fragment after it reads it.

/** A text for each code of the panel API (SingleSignOn::finish). */
const ERRORS: Record<string, string> = {
  expired: "The sign-in took too long, or it started in a different browser. Try again.",
  cancelled: "The provider did not sign you in. You cancelled it, or the provider refused it.",
  failed: "The panel could not check the answer of the provider. Try again. If the error stays, tell your administrator.",
  "no-account": "No user of the panel has this account of the provider. Sign in with your password, then connect the account in Settings.",
  taken: "This account of the provider is connected to a different user of the panel.",
  disabled: "An administrator turned off this provider.",
  unavailable: "The panel cannot connect to the provider now. Try again later.",
};

/** The browser leaves the web panel for an address of the panel API. The tests replace it. */
export const browser = { go: (url: string) => window.location.assign(url) };

/** A value from the fragment of an address ("#ticket=…"), or null. */
export function fragmentValue(hash: string, name: string): string | null {
  return new URLSearchParams(hash.replace(/^#/, "")).get(name);
}

/** The text for an error code, or null when there is no code. */
export function signOnError(code: string | null): string | null {
  if (code === null) return null;
  return ERRORS[code] ?? "The sign-in with the provider did not work. Try again.";
}
