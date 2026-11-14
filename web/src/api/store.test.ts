// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { attention, contextCustomers, visibleWebsites } from "./selectors";
import { ApiError, backupNow, changePassword, createDatabase, createToken, createWebsite, getState, publicName, setContext, setDocumentRoot, setSftp, setTotp, splitCommand, start, startRollout } from "./store";
import { apiJob, panelApi, reset, signedIn, website } from "../test/panelApi";

afterEach(reset);

describe("the state from the panel API", () => {
  it("has no data before the sign-in", () => {
    expect(getState().session.signedIn).toBe(false);
    expect(getState().websites).toEqual([]);
  });

  it("loads the data of an administrator", async () => {
    await signedIn("administrator");
    const s = getState();
    expect(s.session).toMatchObject({ signedIn: true, user: { name: "Alex Admin", role: "administrator" }, contextId: "platform" });
    expect(s.websites).toHaveLength(10);
    expect(s.servers.map((x) => x.name)).toEqual(["web-1", "web-2", "web-3"]);
    expect(s.servers.find((x) => x.name === "web-3")).toMatchObject({ state: "degraded", agentUpdateAvailable: true, stateDetail: "Degraded: cf-sftp failed" });
    expect(s.images.find((i) => i.name === "FrankenPHP · PHP 8.4")).toMatchObject({ running: "0.1.9", available: "0.1.10" });
    expect(s.rollout).toMatchObject({ state: "done", rolledBack: 1 });
    expect(s.customers.map((c) => c.name).sort()).toEqual(["Acme Ltd", "Corner Bakery", "Harbour Books", "Lawson & Co", "Northwind Agency"]);
    // The databases and the snapshots of each website.
    const shop = website("shop.acme.example.com");
    expect(s.databases.filter((d) => d.websiteId === shop.id).map((d) => d.engine).sort()).toEqual(["mariadb", "valkey"]);
    expect(s.snapshots.some((x) => x.websiteId === shop.id && x.kind === "manual")).toBe(true);
  });

  it("gives a reseller its own websites and the websites of its customers, and no servers", async () => {
    await signedIn("reseller");
    const s = getState();
    expect(s.session.user.role).toBe("reseller");
    expect(visibleWebsites(s).map((w) => w.name).sort()).toEqual(["cornerbakery.example.org", "events.harbourbooks.example.net", "harbourbooks.example.net", "northwind.example.com"]);
    expect(contextCustomers(s).map((c) => c.name).sort()).toEqual(["Corner Bakery", "Harbour Books", "Northwind Agency"]);
    expect(s.servers).toEqual([]);
    // A reseller cannot list all images. It gets the images for a new website.
    expect(s.images.length).toBeGreaterThan(0);
    expect(s.images.every((i) => i.reference === "")).toBe(true);
  });

  it("gives an owner only its own websites and its own customer", async () => {
    await signedIn("owner");
    const s = getState();
    expect(visibleWebsites(s)).toHaveLength(5);
    expect(visibleWebsites(s).every((w) => w.name.endsWith("acme.example.com"))).toBe(true);
    expect(s.customers.map((c) => c.name)).toEqual(["Acme Ltd"]);
    expect(s.servers).toEqual([]);
    expect(s.rollout).toBeUndefined();
  });

  it("lets an administrator look at the account of one reseller", async () => {
    await signedIn("administrator");
    const northwind = getState().customers.find((c) => c.name === "Northwind Agency")!;
    setContext(northwind.id);
    expect(visibleWebsites(getState())).toHaveLength(4);
  });

  it("lists the website that the panel could not make first", async () => {
    await signedIn("reseller");
    expect(attention(getState())[0]).toMatchObject({ subject: "events.harbourbooks.example.net", href: `/websites/${website("events.harbourbooks.example.net").id}` });
  });

  it("uses the session of an earlier page load", async () => {
    await signedIn("owner");
    // The page loads again: the store has no data, and the browser has the token.
    const { resetState } = await import("./store");
    resetState();
    start();
    expect(getState().session).toMatchObject({ signedIn: true, loading: true });
    await expect.poll(() => getState().websites.length).toBe(5);
  });
});

describe("changes", () => {
  it("sends a new website to the API and follows its job", async () => {
    const api = await signedIn("owner");
    const image = getState().images[0];
    const made = { ...structuredClone(apiWebsite(api, "acme.example.com")), id: "new-website", name: "blog.acme.example.com", status: "starting" };
    api.on("POST /websites", () => [202, { website: made, job: apiJob({ id: "job-new", title: "Create website blog.acme.example.com", action: "CreateWebsite", websiteId: "new-website" }) }]);

    const r = await createWebsite({ name: " Blog.Acme.example.com ", application: "WordPress", imageId: image.id, planId: "plan-of-a-different-customer", serverId: "", customerId: "" });
    // CF-07: only an administrator selects the plan. The name is in lower case.
    expect(api.changes[1]).toEqual({ method: "POST", path: "/websites", body: { name: "blog.acme.example.com", application: "WordPress", imageId: image.id } });
    expect(r.websiteId).toBe("new-website");
    expect(getState().websites[0]).toMatchObject({ id: "new-website", status: "starting" });
    expect(await r.job).toMatchObject({ id: "job-new", state: "succeeded" });
  });

  it("shows the message of the API when a change is refused", async () => {
    const api = await signedIn("owner");
    api.on("POST /websites/*/snapshots", () => [409, { message: "The backups are not set up. Ask your provider." }]);
    await expect(backupNow(website("acme.example.com").id)).rejects.toMatchObject({ name: "ApiError", message: "The backups are not set up. Ask your provider." });
  });

  it("shows the error of each field", async () => {
    const api = await signedIn("owner");
    api.on("POST /websites/*/databases", () => [422, { message: "x", errors: { name: ["Use 1 to 32 lower-case letters, digits and underscores."] } }]);
    await expect(createDatabase(website("acme.example.com").id, "mariadb", "Bad Name")).rejects.toMatchObject({ fields: { name: "Use 1 to 32 lower-case letters, digits and underscores." } });
  });

  it("always makes a Valkey database with its user", async () => {
    const api = await signedIn("owner");
    api.on("POST /websites/*/databases", () => [202, { password: "secret-password", job: apiJob({ action: "CreateDatabase" }) }]);
    await expect(createDatabase(website("acme.example.com").id, "valkey", "sessions", false)).resolves.toBe("secret-password");
    expect(api.changes[1].body).toEqual({ engine: "valkey", name: "sessions", createUser: true });
  });

  it("loads the data again after a rollout starts", async () => {
    const api = await signedIn("administrator");
    const image = getState().images.find((i) => i.available)!;
    api.on("POST /rollouts", () => [201, {}]);
    api.change<{ data: { state: string }[] }>("/rollouts", (body) => { body.data[0].state = "running"; });
    await startRollout(image.id);
    expect(api.changes[1]).toMatchObject({ path: "/rollouts", body: { imageId: image.id } });
    // The progress of a rollout comes from the API only. The browser does not move it forward.
    expect(getState().rollout?.state).toBe("running");
  });

  it("signs out when the API does not accept the session", async () => {
    const api = await signedIn("owner");
    api.on("POST /websites/*/snapshots", () => [401, { message: "Unauthenticated." }]);
    await expect(backupNow(website("acme.example.com").id)).rejects.toBeInstanceOf(ApiError);
    expect(getState().session.signedIn).toBe(false);
    expect(getState().websites).toEqual([]);
  });
});

describe("checks before a request", () => {
  it("needs the password to make a token (CF-02)", async () => {
    const api = await signedIn("owner");
    api.on("POST /tokens", (body) => [201, { id: 9, token: "9|secret", scopes: (body as { scopes: string[] }).scopes, expiresAt: "2026-12-14T10:00:00+00:00" }]);
    await expect(createToken("ci", ["websites:read"], 30, "password", " 123456 ")).resolves.toMatchObject({ secret: "9|secret", token: { name: "ci", scopes: ["websites:read"] } });
    expect(api.changes[1].body).toEqual({ name: "ci", scopes: ["websites:read"], expiresInDays: 30, password: "password", totp: "123456" });
  });

  it("checks a new password before it sends it (R-01)", () => {
    panelApi("owner");
    const fields = (fn: () => unknown) => { try { fn(); } catch (e) { return (e as ApiError).fields; } return {}; };
    expect(fields(() => changePassword("", "short", "other", ""))).toMatchObject({ currentPassword: expect.any(String), password: expect.any(String), password_confirmation: expect.any(String) });
    expect(fields(() => changePassword("old-password-1", "old-password-1", "old-password-1", "123456"))).toMatchObject({ password: expect.any(String) });
  });

  it("needs the password and a code to change TOTP (R-01)", () => {
    expect(() => setTotp(false, "123456", "")).toThrow(ApiError);
    expect(() => setTotp(false, "12", "secret")).toThrow(ApiError);
  });

  it("splits a start command into arguments with no shell", () => {
    expect(splitCommand(`node server.js --title "My app" '--x=a b'`)).toEqual(["node", "server.js", "--title", "My app", "--x=a b"]);
    expect(splitCommand("  ")).toEqual([]);
  });

  it("refuses a document root outside the website (batch 1, finding 11)", async () => {
    const api = await signedIn("owner");
    const id = website("acme.example.com").id;
    expect(() => setDocumentRoot(id, "../etc")).toThrow(ApiError);
    expect(() => setDocumentRoot(id, "public/../..")).toThrow(ApiError);
    api.on("PUT /websites/*/runtime", () => [202, { job: apiJob({ action: "SetRuntime" }) }]);
    await setDocumentRoot(id, "/web/");
    expect(api.changes[1].body).toEqual({ documentRoot: "web" });
  });

  it("accepts only OpenSSH public keys for SFTP", async () => {
    const api = await signedIn("owner");
    const id = website("acme.example.com").id;
    await expect(setSftp(id, { enabled: true, authorizedKeys: ["not a key"] })).rejects.toMatchObject({ fields: { key: expect.any(String) } });
    const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk2b3hZQ2dQa1dGa1lQa1dGa1lQa1dGa1lQa1dGa1lQ me@laptop";
    await expect(setSftp(id, { enabled: true, authorizedKeys: [key, key] })).rejects.toBeInstanceOf(ApiError);
    // SFTP accepts keys only, so it cannot be on with no key.
    await expect(setSftp(id, { enabled: true, authorizedKeys: [] })).rejects.toMatchObject({ fields: { key: expect.any(String) } });
    // The panel sent none of these changes to the API.
    expect(api.changes).toHaveLength(1);

    api.on("PUT /websites/*/sftp", (body) => [202, { job: apiJob({ action: "SetSftp" }), sftp: { ...(body as object), host: null, port: 2222, username: "acmeexam" } }]);
    await setSftp(id, { enabled: true, authorizedKeys: [`  ${key}  `] });
    expect(api.changes[1].body).toEqual({ enabled: true, authorizedKeys: [key] });
    // The API gives no host name: the panel keeps the host that it has.
    expect(website("acme.example.com").sftp).toMatchObject({ enabled: true, host: "web-1.panel.example", authorizedKeys: [key] });
  });

  it("knows the public names, as the agent does", () => {
    expect(publicName("panel.example.com")).toBe(true);
    for (const name of ["panel.localhost", "localhost", "panel.internal", "192.0.2.10", "panel.test", "[::1]"]) expect(publicName(name)).toBe(false);
  });
});

/** The website of the fixtures as the API gives it. */
function apiWebsite(api: Awaited<ReturnType<typeof signedIn>>, name: string) {
  let found: Record<string, unknown> | undefined;
  api.change<{ data: Record<string, unknown>[] }>("/websites?perPage=100&page=1", (body) => { found = body.data.find((w) => w.name === name); });
  return found!;
}
