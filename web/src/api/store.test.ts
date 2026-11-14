import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, addCustomer, addDomain, addUser, changePassword, createDatabase, createDatabaseUser, createToken, deleteDatabase, deleteDatabaseUser, deleteUser, getState, moveDatabases, publicName, resetState, resetUserPassword, setContext, setDatabaseUser, setDocumentRoot, setPanelCertificate, setSftp, setTotp, splitCommand, timing, verifyDomain } from "./store";
import { contextCustomers, visibleWebsites } from "./selectors";

beforeEach(() => {
  resetState();
  timing.stepMs = 0;
});

const shop = () => getState().websites.find((w) => w.id === "web_shop")!;

describe("databases (CF-01)", () => {
  it("starts each name with the Unix user of the website", async () => {
    await createDatabase("web_shop", "mariadb", "orders");
    await new Promise((r) => setTimeout(r, 10));
    expect(getState().databases.map((d) => d.name)).toContain(`${shop().unixUser}_orders`);
    expect(() => createDatabase("web_shop", "mariadb", "Orders!")).toThrow(ApiError);
  });

  it("gives one user a different permission on each database", async () => {
    await createDatabaseUser("web_shop", "mariadb", "deploy", [
      { databaseId: "db_1", privileges: "readonly" },
      { databaseId: "db_5", privileges: "readwrite" },
    ]);
    const user = getState().databaseUsers.find((u) => u.name === "shop_7f2a_deploy")!;
    expect(user.grants).toEqual([
      { databaseId: "db_5", database: "shop_7f2a_staging", privileges: "readwrite" },
      { databaseId: "db_1", database: "shop_7f2a_wp", privileges: "readonly" },
    ].sort((a, b) => a.database.localeCompare(b.database)));

    await setDatabaseUser(user.id, { grants: [{ databaseId: "db_1", privileges: "readwrite" }] });
    expect(getState().databaseUsers.find((u) => u.id === user.id)!.grants).toEqual([{ databaseId: "db_1", database: "shop_7f2a_wp", privileges: "readwrite" }]);
  });

  it("refuses a database of a different website or engine", () => {
    expect(() => createDatabaseUser("web_shop", "mariadb", "x", [{ databaseId: "db_3", privileges: "readonly" }])).toThrow(ApiError);
    expect(() => createDatabaseUser("web_shop", "mariadb", "y", [{ databaseId: "db_2", privileges: "readonly" }])).toThrow(ApiError);
  });

  it("gives a Valkey database a number and a user with the same name", async () => {
    // The demo data has one Valkey database with the number 1 on the server of the shop.
    expect(await createDatabase("web_shop", "valkey", "sessions", false)).toMatch(/^[a-zA-Z2-9]{24}$/);
    await new Promise((r) => setTimeout(r, 10));
    const name = `${shop().unixUser}_sessions`;
    expect(getState().databases.find((d) => d.name === name)).toMatchObject({ engine: "valkey", number: 2, keys: 0 });
    const user = getState().databaseUsers.find((u) => u.name === name)!;
    expect(user).toMatchObject({ engine: "valkey", access: "website", grants: [{ database: name, privileges: "readwrite" }] });

    // The user can only get a new password. It goes with its database.
    expect(() => setDatabaseUser(user.id, { access: "anywhere" })).toThrow(ApiError);
    expect(() => setDatabaseUser(user.id, { grants: [] })).toThrow(ApiError);
    expect(() => deleteDatabaseUser(user.id)).toThrow(ApiError);
    expect(() => createDatabaseUser("web_shop", "valkey", "more", [])).toThrow(ApiError);
    expect(await setDatabaseUser(user.id, { resetPassword: true })).toMatch(/^[a-zA-Z2-9]{24}$/);

    deleteDatabase(getState().databases.find((d) => d.name === name)!.id);
    expect(getState().databaseUsers.some((u) => u.name === name)).toBe(false);
  });

  it("refuses a Valkey database on a server with an agent that cannot operate Valkey", () => {
    getState().servers.find((s) => s.id === shop().serverId)!.databaseEngines = ["mariadb", "postgresql"];
    expect(() => createDatabase("web_shop", "valkey", "cache2")).toThrow(/Correct the engine/);
  });
});

describe("database containers of each customer", () => {
  const site = (id: string) => getState().websites.find((w) => w.id === id)!;

  it("gives the Valkey numbers of each customer on a server", async () => {
    // The shop has the Valkey database number 1 on its server. A website of a different
    // customer on the same server has its own Valkey container, so its first number is 1 too.
    const other = getState().websites.find((w) => w.serverId === shop().serverId && w.customerId !== shop().customerId && !w.sharedDatabases)!;
    await createDatabase(other.id, "valkey", "cache");
    await createDatabase("web_shop", "valkey", "sessions");
    await new Promise((r) => setTimeout(r, 10));
    expect(getState().databases.find((d) => d.name === `${other.unixUser}_cache`)).toMatchObject({ number: 1 });
    expect(getState().databases.find((d) => d.name === `${shop().unixUser}_sessions`)).toMatchObject({ number: 2 });
  });

  it("moves the databases of the websites of an earlier release to the containers of each customer", async () => {
    // The demo data has two websites of an earlier release on web-02.
    const shared = () => getState().websites.filter((w) => w.sharedDatabases);
    expect(shared().map((w) => w.serverId)).toEqual(["srv_web02", "srv_web02"]);
    const first = shared()[0].id;
    expect(() => createDatabase(first, "valkey", "cache")).toThrow(/Correct the engine/);
    site(shared()[1].id).status = "starting";

    await moveDatabases("srv_web02");

    // A website that the panel makes now is not on the server yet, so it does not move.
    expect(shared().map((w) => w.status)).toEqual(["starting"]);
    const job = getState().jobs.find((j) => j.title === `Move the databases of ${site(first).name}`)!;
    expect(job).toMatchObject({ state: "succeeded", serverId: "srv_web02", steps: ["Start database containers", "Stop website", "Copy databases", "Copy users", "Start website", "Remove old copies"] });
    expect(getState().jobs.filter((j) => j.title.startsWith("Move the databases of "))).toHaveLength(1);
    // The website has the Valkey container of its customer now.
    await createDatabase(first, "valkey", "cache");
  });
});

describe("domains (CF-03)", () => {
  it("does not serve a new domain before the TXT record is correct", async () => {
    await addDomain("web_shop", "brand-new.example", "alias");
    const domain = shop().domains.find((d) => d.name === "brand-new.example")!;
    expect(domain.verification).toMatchObject({ type: "TXT", name: "_cloudfactory-challenge.brand-new.example" });
    expect(domain.verification!.value).toMatch(/^cf-[a-z0-9]{40}$/);
    expect(() => verifyDomain("web_shop", "brand-new.example")).toThrow(/does not have the value/);
  });

  it("does not need a record for a sub-domain of a verified domain", async () => {
    await addDomain("web_shop", "eu.shop.example.net", "alias");
    expect(shop().domains.find((d) => d.name === "eu.shop.example.net")!.verification).toBeUndefined();
  });
});

describe("tokens (CF-02)", () => {
  it("needs the password and the TOTP code", () => {
    expect(() => createToken("ci", ["websites:read"], 30, "")).toThrow(ApiError);
    expect(() => createToken("ci", ["websites:read"], 30, "secret", "")).toThrow(ApiError);
    expect(createToken("ci", ["websites:read"], 30, "secret", "123456")).toMatchObject({ token: { name: "ci" } });
  });
});

describe("sign-in settings (R-01)", () => {
  it("checks a new password before it sends it", () => {
    const fields = (fn: () => unknown) => { try { fn(); } catch (e) { return (e as ApiError).fields; } return {}; };
    expect(fields(() => changePassword("", "short", "other", ""))).toMatchObject({ currentPassword: expect.any(String), password: expect.any(String), password_confirmation: expect.any(String) });
    expect(fields(() => changePassword("old-password-1", "old-password-1", "old-password-1", "123456"))).toMatchObject({ password: expect.any(String) });
    expect(() => changePassword("old-password-1", "a-new-long-password", "a-new-long-password", "123456")).not.toThrow();
    expect(getState().audit[0]).toMatchObject({ action: "Changed password" });
  });

  it("needs the password and a code to change TOTP", () => {
    expect(() => setTotp(false, "123456", "")).toThrow(ApiError);
    expect(() => setTotp(false, "12", "secret")).toThrow(ApiError);
  });
});

describe("reseller accounts (batch 3, issue 2)", () => {
  it("shows the websites of the reseller and of its customers", () => {
    setContext("cus_acme");
    const s = getState();
    const owners = new Set(visibleWebsites(s).map((w) => w.customerId));
    const customers = s.customers.filter((c) => c.resellerId === "cus_acme").map((c) => c.id);
    expect(customers.length).toBeGreaterThan(0);
    for (const c of customers) expect(contextCustomers(s).map((x) => x.id)).toContain(c);
    expect([...owners].every((c) => c === "cus_acme" || customers.includes(c))).toBe(true);
    expect(visibleWebsites(s).some((w) => w.customerId !== "cus_acme")).toBe(true);
  });

  it("shows only the websites of an owner", () => {
    setContext("cus_bakery");
    expect(visibleWebsites(getState()).every((w) => w.customerId === "cus_bakery")).toBe(true);
    expect(contextCustomers(getState()).map((c) => c.id)).toEqual(["cus_bakery"]);
  });
});

describe("runtime settings", () => {
  it("splits a start command into arguments with no shell", () => {
    expect(splitCommand(`node server.js --title "My app" '--x=a b'`)).toEqual(["node", "server.js", "--title", "My app", "--x=a b"]);
    expect(splitCommand("  ")).toEqual([]);
  });

  it("refuses a document root outside the website (batch 1, finding 11)", () => {
    expect(() => setDocumentRoot("web_shop", "../etc")).toThrow(ApiError);
    expect(() => setDocumentRoot("web_shop", "public/../..")).toThrow(ApiError);
    expect(() => setDocumentRoot("web_shop", "web")).not.toThrow();
    expect(shop().runtime.documentRoot).toBe("web");
  });
});

describe("SFTP", () => {
  it("accepts only OpenSSH public keys", async () => {
    await expect(setSftp("web_shop", { enabled: true, authorizedKeys: ["not a key"] })).rejects.toMatchObject({ fields: { key: expect.any(String) } });
    const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk2b3hZQ2dQa1dGa1lQa1dGa1lQa1dGa1lQa1dGa1lQ me@laptop";
    await expect(setSftp("web_shop", { enabled: true, authorizedKeys: [key, key] })).rejects.toBeInstanceOf(ApiError);
    // SFTP accepts keys only, so it cannot be on with no key.
    await expect(setSftp("web_shop", { enabled: true, authorizedKeys: [] })).rejects.toMatchObject({ fields: { key: expect.any(String) } });
    await expect(setSftp("web_shop", { enabled: false, authorizedKeys: [] })).resolves.toMatchObject({ job: expect.any(Promise) });
    await expect(setSftp("web_shop", { enabled: true, authorizedKeys: [key] })).resolves.not.toHaveProperty("password");
  });
});

describe("users (audit finding B5)", () => {
  it("makes the first user of a new customer with a temporary password", () => {
    const access = addCustomer({ name: "Oak Lane", email: "owner@oaklane.example", planId: "plan_starter", role: "owner" }, true) as { email: string; temporaryPassword: string };
    expect(access.email).toBe("owner@oaklane.example");
    expect(access.temporaryPassword).toMatch(/^[A-Za-z0-9]{20}$/);
    const customer = getState().customers.find((c) => c.name === "Oak Lane")!;
    expect(getState().users.find((u) => u.email === "owner@oaklane.example")).toMatchObject({ role: "owner", customerId: customer.id, mustChangePassword: true, totpEnabled: false });
    // With no user, the customer cannot sign in, and the panel gives no password.
    expect(addCustomer({ name: "Elm Road", email: "owner@elmroad.example", planId: "plan_starter", role: "owner" })).toBeNull();
    expect(getState().users.some((u) => u.email === "owner@elmroad.example")).toBe(false);
  });

  it("gives a new user the role of the customer", () => {
    expect(addUser({ name: "Kim", email: "kim@acme.example", customerId: "cus_acme" })).toMatchObject({ email: "kim@acme.example", temporaryPassword: expect.stringMatching(/^.{20}$/) });
    expect(getState().users.find((u) => u.email === "kim@acme.example")).toMatchObject({ role: "reseller", customerId: "cus_acme" });
    addUser({ name: "Root", email: "root@platform.example", customerId: null });
    expect(getState().users.find((u) => u.email === "root@platform.example")).toMatchObject({ role: "administrator", customerId: undefined });
    expect(() => addUser({ name: "Kim", email: "kim@acme.example", customerId: "cus_acme" })).toThrow(/exists already/);
    expect(() => addUser({ name: "", email: "bad", customerId: "cus_none" })).toThrow(ApiError);
  });

  it("resets a password, and can remove TOTP", () => {
    expect(getState().users.find((u) => u.id === "usr_4")).toMatchObject({ totpEnabled: true, mustChangePassword: false });
    resetUserPassword("usr_4");
    expect(getState().users.find((u) => u.id === "usr_4")).toMatchObject({ totpEnabled: true, mustChangePassword: true });
    resetUserPassword("usr_4", true);
    expect(getState().users.find((u) => u.id === "usr_4")).toMatchObject({ totpEnabled: false, mustChangePassword: true });
    expect(getState().audit[0]).toMatchObject({ action: "Reset password and removed TOTP" });
  });

  it("does not let a user reset or delete itself", () => {
    expect(() => resetUserPassword("usr_1")).toThrow(/your own password/);
    expect(() => deleteUser("usr_1")).toThrow(/your own user/);
    deleteUser("usr_5");
    expect(getState().users.some((u) => u.id === "usr_5")).toBe(false);
  });
});

describe("panel certificate", () => {
  it("changes the method with a job", async () => {
    expect(getState().panelCertificate.method).toBe("internal");
    const job = await setPanelCertificate("letsencrypt");
    expect(job).toMatchObject({ state: "succeeded", title: "Change the panel certificate to Let's Encrypt", steps: ["Change certificate method", "Get TLS certificate"] });
    expect(getState().panelCertificate).toMatchObject({ method: "letsencrypt", state: "valid" });
    expect(getState().audit[0]).toMatchObject({ action: "Changed the panel certificate method", target: "letsencrypt" });
  });

  it("refuses Let's Encrypt for a domain that is not public", () => {
    const state = getState();
    state.panelCertificate.domain = "panel.localhost";
    state.panelCertificate.letsEncryptAvailable = false;
    expect(() => setPanelCertificate("letsencrypt")).toThrow("panel.localhost is not a public domain");
  });

  it("uses the panel CA only when all servers trust it", () => {
    getState().panelCertificate.serversWithoutCa = ["web-02"];
    expect(() => setPanelCertificate("internal")).toThrow("These servers do not trust the panel CA yet: web-02.");
  });

  it("knows the public names, as the agent does", () => {
    for (const name of ["panel.example.com", "Panel.Example.Com.", "cloudfactory.io"]) expect(publicName(name)).toBe(true);
    for (const name of ["localhost", "panel.localhost", "panel.local", "panel.internal", "panel.home.arpa", "panel.test", "panel.example", "panel", "10.0.0.1", "::1"]) expect(publicName(name)).toBe(false);
  });
});
