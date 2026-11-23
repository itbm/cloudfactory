// FR-103: passkeys in the browser. The panel API gives the options in the JSON form of
// WebAuthn (base64url for the binary values). These functions give them to the browser and
// return the answer of the browser in the same JSON form.

type Json = Record<string, unknown>;

/** The browser can make and use passkeys. */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && typeof window.PublicKeyCredential === "function" && !!navigator.credentials;
}

/** An error of the browser that the user can understand. */
export class PasskeyError extends Error {}

function fromBase64Url(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  const bytes = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
  return bytes.buffer;
}

function toBase64Url(value: ArrayBuffer | null): string | null {
  if (!value) return null;
  let text = "";
  for (const byte of new Uint8Array(value)) text += String.fromCharCode(byte);
  return btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

const descriptors = (list: unknown) =>
  ((list as { id: string; type: string; transports?: string[] }[] | undefined) ?? []).map((d) => ({ ...d, id: fromBase64Url(d.id) })) as PublicKeyCredentialDescriptor[];

/** The options for navigator.credentials.create(). Older browsers have no parseCreationOptionsFromJSON. */
function creationOptions(json: Json): PublicKeyCredentialCreationOptions {
  const parse = (PublicKeyCredential as unknown as { parseCreationOptionsFromJSON?: (j: Json) => PublicKeyCredentialCreationOptions }).parseCreationOptionsFromJSON;
  if (parse) return parse(json);
  const user = json.user as Json;
  return { ...json, challenge: fromBase64Url(json.challenge as string), user: { ...user, id: fromBase64Url(user.id as string) }, excludeCredentials: descriptors(json.excludeCredentials) } as unknown as PublicKeyCredentialCreationOptions;
}

function requestOptions(json: Json): PublicKeyCredentialRequestOptions {
  const parse = (PublicKeyCredential as unknown as { parseRequestOptionsFromJSON?: (j: Json) => PublicKeyCredentialRequestOptions }).parseRequestOptionsFromJSON;
  if (parse) return parse(json);
  return { ...json, challenge: fromBase64Url(json.challenge as string), allowCredentials: descriptors(json.allowCredentials) } as unknown as PublicKeyCredentialRequestOptions;
}

/** The JSON form of the answer. Older browsers have no toJSON(). */
function toJson(credential: PublicKeyCredential): Json {
  const own = (credential as unknown as { toJSON?: () => Json }).toJSON;
  if (own) return own.call(credential);
  const r = credential.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse;
  const response: Json = { clientDataJSON: toBase64Url(r.clientDataJSON) };
  if ("attestationObject" in r && r.attestationObject) {
    response.attestationObject = toBase64Url(r.attestationObject);
    response.transports = typeof r.getTransports === "function" ? r.getTransports() : [];
  } else {
    response.authenticatorData = toBase64Url(r.authenticatorData);
    response.signature = toBase64Url(r.signature);
    response.userHandle = toBase64Url(r.userHandle);
  }
  return {
    id: credential.id,
    rawId: toBase64Url(credential.rawId),
    type: credential.type,
    response,
    clientExtensionResults: credential.getClientExtensionResults(),
    authenticatorAttachment: credential.authenticatorAttachment ?? null,
  };
}

/** A text for each error of the browser. */
function explain(e: unknown, creating: boolean): PasskeyError {
  const name = (e as { name?: string })?.name;
  if (name === "NotAllowedError" || name === "AbortError") {
    return new PasskeyError(creating ? "The browser did not make the passkey. You cancelled it, or the time ran out. Try again." : "The browser did not use a passkey. You cancelled it, or the time ran out. Try again.");
  }
  if (name === "InvalidStateError") return new PasskeyError("This device already has a passkey for your user on this panel.");
  if (name === "SecurityError") return new PasskeyError("The browser refused the passkey for this domain. Open the panel at its own domain, with HTTPS.");
  if (name === "NotSupportedError") return new PasskeyError("This device cannot make a passkey of a type that the panel accepts.");
  return new PasskeyError((e as Error)?.message || "The browser could not use the passkey.");
}

/** Makes a passkey. It gives the answer of the browser for the panel API. */
export async function createPasskey(publicKey: Json): Promise<Json> {
  if (!passkeysSupported()) throw new PasskeyError("This browser cannot use passkeys.");
  try {
    const credential = await navigator.credentials.create({ publicKey: creationOptions(publicKey) });
    if (!credential) throw new PasskeyError("The browser did not make the passkey.");
    return toJson(credential as PublicKeyCredential);
  } catch (e) {
    throw e instanceof PasskeyError ? e : explain(e, true);
  }
}

/** Signs in with a passkey. The browser shows the passkeys of this domain. */
export async function getPasskey(publicKey: Json): Promise<Json> {
  if (!passkeysSupported()) throw new PasskeyError("This browser cannot use passkeys.");
  try {
    const credential = await navigator.credentials.get({ publicKey: requestOptions(publicKey) });
    if (!credential) throw new PasskeyError("The browser did not use a passkey.");
    return toJson(credential as PublicKeyCredential);
  } catch (e) {
    throw e instanceof PasskeyError ? e : explain(e, false);
  }
}
