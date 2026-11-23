// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import owner from "../test/fixtures/owner.json";
import { panelApi, reset, signedIn } from "../test/panelApi";

// FR-103: the browser part of WebAuthn. jsdom has no passkeys, so each test gives the answers of
// the browser. The JSON is the JSON of PublicKeyCredential.toJSON().
const browser = { create: vi.fn(), get: vi.fn() };
const credential = (json: Record<string, unknown>) => ({ toJSON: () => json });
const CREATED = { id: "bmV3", rawId: "bmV3", type: "public-key", response: { clientDataJSON: "e30", attestationObject: "oA", transports: ["internal"] }, clientExtensionResults: {} };
const SIGNED = { id: "a2V5", rawId: "a2V5", type: "public-key", response: { clientDataJSON: "e30", authenticatorData: "AA", signature: "AA", userHandle: "dXNlcg" }, clientExtensionResults: {} };
const [IPHONE, YUBIKEY] = owner["GET /me/passkeys"].body.data;
const PUBLIC_KEY = { challenge: "Y2hhbGxlbmdl", rpId: "localhost", userVerification: "required", timeout: 300000 };

beforeEach(() => {
  browser.create.mockReset();
  browser.get.mockReset();
  vi.stubGlobal("PublicKeyCredential", function PublicKeyCredential() {});
  Object.defineProperty(navigator, "credentials", { value: browser, configurable: true });
});

afterEach(() => {
  cleanup();
  reset();
  Reflect.deleteProperty(navigator, "credentials");
});

describe("passkeys", () => {
  it("signs in with a passkey, with no password and no code", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/passkey/options", () => ({ ceremony: "c1", publicKey: PUBLIC_KEY }));
    api.on("POST /auth/passkey", () => ({ token: "2|passkey", expiresAt: null }));
    browser.get.mockResolvedValue(credential(SIGNED));
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);

    fireEvent.click(await screen.findByRole("button", { name: "Sign in with a passkey" }));
    await waitFor(() => expect(getState().session.signedIn).toBe(true));
    // The browser gets the challenge as bytes, and the panel gets the answer of the browser.
    const options = browser.get.mock.calls[0][0].publicKey;
    expect(new TextDecoder().decode(options.challenge)).toBe("challenge");
    expect(options.userVerification).toBe("required");
    expect(api.changes.map((c) => `${c.method} ${c.path}`)).toEqual(["POST /auth/passkey/options", "POST /auth/passkey"]);
    expect(api.changes[1].body).toMatchObject({ ceremony: "c1", credential: SIGNED });
  });

  it("shows why the browser did not use a passkey", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/passkey/options", () => ({ ceremony: "c1", publicKey: PUBLIC_KEY }));
    browser.get.mockRejectedValue(Object.assign(new Error("The operation was cancelled."), { name: "NotAllowedError" }));
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);

    fireEvent.click(await screen.findByRole("button", { name: "Sign in with a passkey" }));
    expect(await screen.findByText("The browser did not use a passkey. You cancelled it, or the time ran out. Try again.")).toBeTruthy();
    expect(api.changes.map((c) => c.path)).toEqual(["/auth/passkey/options"]);
    expect(getState().session.signedIn).toBe(false);
  });

  it("shows no passkey button when passkeys do not work on the domain or in the browser", async () => {
    const api = panelApi("owner");
    api.on("GET /auth/methods", () => ({ passwordReset: true, passkeys: false, providers: [] }));
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);
    await screen.findByRole("link", { name: "Forgot your password?" });
    expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
    cleanup();

    panelApi("owner");
    vi.stubGlobal("PublicKeyCredential", undefined);
    render(<MemoryRouter initialEntries={["/login"]}><App /></MemoryRouter>);
    await screen.findByRole("link", { name: "Forgot your password?" });
    expect(screen.queryByRole("button", { name: "Sign in with a passkey" })).toBeNull();
  });

  it("adds a passkey after the password, and lists the passkeys", async () => {
    const api = await signedIn("owner");
    render(<MemoryRouter initialEntries={["/settings"]}><App /></MemoryRouter>);
    const card = (await screen.findByRole("heading", { name: "Passkeys" })).closest(".card") as HTMLElement;
    const list = await within(card).findByRole("list", { name: "Your passkeys" });
    expect(within(list).getAllByRole("listitem").map((li) => li.querySelector("strong")?.textContent)).toEqual(["iPhone", "YubiKey 5C"]);
    expect(list.textContent).toContain("Not used yet");
    expect(list.textContent).toContain("Synced");

    api.on("POST /me/passkeys/options", () => ({ ceremony: "c2", publicKey: { rp: { name: "Cloudfactory", id: "localhost" }, user: { name: "owner@example.com", id: "dXNlcg", displayName: "Olu Owner" }, challenge: "Y2hhbGxlbmdl", pubKeyCredParams: [{ type: "public-key", alg: -7 }], excludeCredentials: [{ type: "public-key", id: "a2V5" }] } }));
    api.on("POST /me/passkeys", () => [201, { id: "p3", name: "Work laptop", domain: "localhost", synced: false, createdAt: "2026-11-14T10:00:00+00:00", lastUsedAt: null }]);
    browser.create.mockResolvedValue(credential(CREATED));
    fireEvent.click(within(card).getByRole("button", { name: "Add a passkey" }));
    const dialog = await screen.findByRole("dialog", { name: "Add a passkey" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Work laptop" } });
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "password" } });
    // TOTP is off for this user, so the panel does not ask for a code.
    expect(within(dialog).queryByLabelText("6-digit code from the app")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Add passkey" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // The first change is the sign-in of the test.
    expect(api.changes.slice(1)).toEqual([
      { method: "POST", path: "/me/passkeys/options", body: { password: "password" } },
      { method: "POST", path: "/me/passkeys", body: { ceremony: "c2", name: "Work laptop", credential: CREATED } },
    ]);
    // The browser gets the IDs as bytes, so that it does not make a second passkey on a device.
    const options = browser.create.mock.calls[0][0].publicKey;
    expect(new TextDecoder().decode(options.user.id)).toBe("user");
    expect(new TextDecoder().decode(options.excludeCredentials[0].id)).toBe("key");
  });

  it("shows a wrong password next to the field, and does not open the browser", async () => {
    const api = await signedIn("owner");
    api.on("POST /me/passkeys/options", () => [422, { message: "The password is not correct.", errors: { password: ["The password is not correct."] } }]);
    render(<MemoryRouter initialEntries={["/settings"]}><App /></MemoryRouter>);
    fireEvent.click(await screen.findByRole("button", { name: "Add a passkey" }));
    const dialog = await screen.findByRole("dialog", { name: "Add a passkey" });
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "wrong" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add passkey" }));
    expect(await within(dialog).findByText("The password is not correct.")).toBeTruthy();
    expect(browser.create).not.toHaveBeenCalled();
  });

  it("renames a passkey, and removes a passkey after the password", async () => {
    const api = await signedIn("owner");
    api.on("PATCH /me/passkeys/*", () => ({ ...IPHONE, name: "Olu's iPhone" }));
    api.on("DELETE /me/passkeys/*", () => [204, null]);
    render(<MemoryRouter initialEntries={["/settings"]}><App /></MemoryRouter>);

    fireEvent.click(await screen.findByRole("button", { name: "Rename iPhone" }));
    let dialog = await screen.findByRole("dialog", { name: "Rename the passkey" });
    // To rename a passkey, the user does not give the password.
    expect(within(dialog).queryByLabelText("Password")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Olu's iPhone" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Remove YubiKey 5C" }));
    dialog = await screen.findByRole("dialog", { name: "Remove the passkey" });
    expect(dialog.textContent).toContain("You can no longer sign in with YubiKey 5C.");
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    // The first change is the sign-in of the test.
    expect(api.changes.slice(1)).toEqual([
      { method: "PATCH", path: `/me/passkeys/${IPHONE.id}`, body: { name: "Olu's iPhone" } },
      { method: "DELETE", path: `/me/passkeys/${YUBIKEY.id}`, body: { password: "password" } },
    ]);
  });
});
