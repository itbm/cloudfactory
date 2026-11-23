// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { getState } from "../api/store";
import { browser } from "../lib/sso";
import type { IdentityProvider } from "../api/live";
import { fixture, panelApi, reset, signedIn } from "../test/panelApi";

// Single sign-on with OpenID Connect and SAML. The browser goes to the provider and comes back
// to the panel API, which sends it to the web panel with a value after "#".
const [GOOGLE, ENTRA] = fixture<{ data: IdentityProvider[] }>("administrator", "GET /identity-providers").body.data;
const METHODS = { passwordReset: true, passkeys: false, providers: [{ id: GOOGLE.id, name: "Google Workspace" }] };

afterEach(() => {
  cleanup();
  reset();
  vi.restoreAllMocks();
});

describe("single sign-on", () => {
  it("shows a button for each provider on the sign-in page, and the reason when a sign-in did not work", async () => {
    const api = panelApi("owner");
    api.on("GET /auth/methods", () => METHODS);
    render(<MemoryRouter initialEntries={["/login#sso-error=no-account"]}><App /></MemoryRouter>);

    // The browser goes to the panel API, which sends it to the provider.
    const link = await screen.findByRole("link", { name: "Sign in with Google Workspace" });
    expect(link.getAttribute("href")).toBe(`/api/v1/auth/sso/${GOOGLE.id}`);
    expect(screen.getByRole("alert").textContent).toBe("No user of the panel has this account of the provider. Sign in with your password, then connect the account in Settings.");
  });

  it("changes the ticket into a session", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/sso/complete", () => ({ token: "3|sso", expiresAt: null }));
    render(<MemoryRouter initialEntries={["/sso#ticket=t1"]}><App /></MemoryRouter>);

    await waitFor(() => expect(getState().session.signedIn).toBe(true));
    expect(api.changes).toMatchObject([{ method: "POST", path: "/auth/sso/complete", body: { ticket: "t1" } }]);
    expect(api.changes[0].body).not.toHaveProperty("totp");
  });

  it("asks for the TOTP code when TOTP is on for the user", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/sso/complete", (body) => (body as { totp?: string }).totp === "123456"
      ? { token: "3|sso", expiresAt: null }
      : [422, { message: "Type the code from your authenticator app.", errors: { totp: ["Type the code from your authenticator app."] }, totpRequired: true }]);
    render(<MemoryRouter initialEntries={["/sso#ticket=t1"]}><App /></MemoryRouter>);

    fireEvent.change(await screen.findByLabelText("6-digit code"), { target: { value: "123456" } });
    // The first answer is not an error for the user.
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(getState().session.signedIn).toBe(true));
    expect(api.changes.map((c) => c.body)).toEqual([{ ticket: "t1", deviceId: expect.any(String) }, { ticket: "t1", totp: "123456", deviceId: expect.any(String) }]);
  });

  it("shows why the ticket does not work", async () => {
    const api = panelApi("owner");
    api.on("POST /auth/sso/complete", () => [422, { message: "The sign-in is too old. Sign in again.", errors: { ticket: ["The sign-in is too old. Sign in again."] } }]);
    render(<MemoryRouter initialEntries={["/sso#ticket=old"]}><App /></MemoryRouter>);
    expect(await screen.findByText("The sign-in is too old. Sign in again.")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Back to sign in" })).toBeTruthy();
    expect(getState().session.signedIn).toBe(false);
    cleanup();

    // An address with no ticket does not call the API.
    const other = panelApi("owner");
    render(<MemoryRouter initialEntries={["/sso"]}><App /></MemoryRouter>);
    expect(await screen.findByText("The address has no sign-in. Start again from the sign-in page.")).toBeTruthy();
    expect(other.changes).toEqual([]);
  });

  it("connects an account in Settings after the password, and the browser goes to the provider", async () => {
    const api = await signedIn("reseller");
    const go = vi.spyOn(browser, "go").mockImplementation(() => {});
    api.on("POST /me/identities/*", () => ({ url: `/api/v1/auth/sso/${GOOGLE.id}?connect=c1` }));
    render(<MemoryRouter initialEntries={["/settings"]}><App /></MemoryRouter>);

    const card = (await screen.findByRole("heading", { name: "Single sign-on" })).closest(".card") as HTMLElement;
    expect((await within(card).findByRole("list", { name: "Single sign-on providers" })).textContent).toContain("Not connected");
    fireEvent.click(within(card).getByRole("button", { name: "Connect Google Workspace" }));
    const dialog = await screen.findByRole("dialog", { name: "Connect Google Workspace" });
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(go).toHaveBeenCalledWith(`/api/v1/auth/sso/${GOOGLE.id}?connect=c1`));
    // The first change is the sign-in of the test.
    expect(api.changes.slice(1)).toEqual([{ method: "POST", path: `/me/identities/${GOOGLE.id}`, body: { password: "password" } }]);
  });

  it("shows the result of a connection, and disconnects an account after the password", async () => {
    const api = await signedIn("owner");
    api.on("DELETE /me/identities/*", () => [204, null]);
    render(<MemoryRouter initialEntries={[`/settings#sso-connected=${GOOGLE.id}`]}><App /></MemoryRouter>);

    expect((await screen.findByRole("status")).textContent).toContain("You can now sign in with Google Workspace.");
    const list = screen.getByRole("list", { name: "Single sign-on providers" });
    expect(list.textContent).toContain("owner@example.com");
    fireEvent.click(within(list).getByRole("button", { name: "Disconnect Google Workspace" }));
    const dialog = await screen.findByRole("dialog", { name: "Disconnect Google Workspace" });
    fireEvent.change(within(dialog).getByLabelText("Password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(api.changes.slice(1)).toEqual([{ method: "DELETE", path: `/me/identities/${GOOGLE.id}`, body: { password: "password" } }]);
    cleanup();

    // The provider sent the browser back with an error.
    await signedIn("owner");
    render(<MemoryRouter initialEntries={["/settings#sso-error=taken"]}><App /></MemoryRouter>);
    expect(await screen.findByText("This account of the provider is connected to a different user of the panel.")).toBeTruthy();
  });

  it("shows no card in Settings when there is no provider", async () => {
    const api = await signedIn("owner");
    api.change<{ data: unknown[] }>("/me/identities", (body) => { body.data = []; });
    render(<MemoryRouter initialEntries={["/settings"]}><App /></MemoryRouter>);
    await screen.findByRole("heading", { name: "Passkeys" });
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Single sign-on" })).toBeNull());
  });

  it("lets an administrator add a SAML provider with the values for the provider", async () => {
    const api = await signedIn("administrator");
    api.on("POST /identity-providers", () => [201, { ...ENTRA, id: "p3", name: "Okta" }]);
    render(<MemoryRouter initialEntries={["/settings/sso"]}><App /></MemoryRouter>);

    const table = await screen.findByRole("table");
    await within(table).findByText("Google Workspace");
    expect(table.textContent).toContain("SAML 2.0");
    expect(table.textContent).toContain("example.com");

    fireEvent.click(screen.getByRole("button", { name: "Add provider" }));
    const dialog = await screen.findByRole("dialog", { name: "Add a provider" });
    // OpenID Connect is the first choice. The redirect URI is the same for all the providers.
    expect(within(dialog).getByLabelText("Redirect URI").textContent).toBe("http://localhost/api/v1/auth/sso/oidc/callback");
    fireEvent.click(within(dialog).getByRole("radio", { name: /SAML 2.0/ }));
    expect(within(dialog).getByLabelText("Entity ID").textContent).toBe("http://localhost/api/v1/auth/sso/saml/metadata");
    expect(within(dialog).getByLabelText("ACS URL").textContent).toBe("http://localhost/api/v1/auth/sso/saml/acs");
    expect(within(dialog).queryByLabelText("Client ID")).toBeNull();

    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Okta" } });
    fireEvent.change(within(dialog).getByLabelText("Entity ID of the provider"), { target: { value: "http://www.okta.com/exk1" } });
    fireEvent.change(within(dialog).getByLabelText("Sign-in URL"), { target: { value: "https://example.okta.com/app/sso/saml" } });
    fireEvent.change(within(dialog).getByLabelText("Signing certificate"), { target: { value: "MIIC" } });
    fireEvent.click(within(dialog).getByLabelText("Connect users by email"));
    fireEvent.change(within(dialog).getByLabelText("Email domains"), { target: { value: "example.com, example.org" } });
    fireEvent.change(within(dialog).getByLabelText("Your password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add provider" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // Only the fields of SAML go to the API.
    expect(api.changes.slice(1)).toEqual([{ method: "POST", path: "/identity-providers", body: {
      name: "Okta", protocol: "saml", enabled: true, entityId: "http://www.okta.com/exk1", ssoUrl: "https://example.okta.com/app/sso/saml", certificate: "MIIC",
      linkByEmail: true, emailDomains: ["example.com", "example.org"], password: "password",
    } }]);
  });

  it("keeps the client secret when an administrator changes a provider, and removes a provider after the password", async () => {
    const api = await signedIn("administrator");
    api.on("PUT /identity-providers/*", () => ({ ...GOOGLE, name: "Google" }));
    api.on("DELETE /identity-providers/*", () => [204, null]);
    api.on("POST /identity-providers", () => [422, { message: "The panel could not read the discovery document.", errors: { issuer: ["The panel could not read the discovery document."] } }]);
    render(<MemoryRouter initialEntries={["/settings/sso"]}><App /></MemoryRouter>);

    fireEvent.click(await screen.findByRole("button", { name: "Edit Google Workspace" }));
    let dialog = await screen.findByRole("dialog", { name: "Edit Google Workspace" });
    // The protocol of a provider does not change.
    expect(within(dialog).queryByRole("radio")).toBeNull();
    expect((within(dialog).getByLabelText("Client secret") as HTMLInputElement).value).toBe("");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Google" } });
    fireEvent.change(within(dialog).getByLabelText("Your password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Remove Microsoft Entra ID" }));
    dialog = await screen.findByRole("dialog", { name: "Remove the provider" });
    fireEvent.change(within(dialog).getByLabelText("Your password"), { target: { value: "password" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    expect(api.changes.slice(1)).toEqual([
      { method: "PUT", path: `/identity-providers/${GOOGLE.id}`, body: { name: "Google", enabled: true, linkByEmail: true, emailDomains: ["example.com"], issuer: "https://accounts.google.com", clientId: GOOGLE.clientId, password: "password" } },
      { method: "DELETE", path: `/identity-providers/${ENTRA.id}`, body: { password: "password" } },
    ]);

    // An error of the discovery document shows next to the issuer.
    fireEvent.click(screen.getByRole("button", { name: "Add provider" }));
    dialog = await screen.findByRole("dialog", { name: "Add a provider" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add provider" }));
    expect(await within(dialog).findByText("The panel could not read the discovery document.")).toBeTruthy();
  });
});
