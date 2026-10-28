import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, addDomain, changePassword, createDatabase, createDatabaseUser, createToken, getState, resetState, setContext, setDatabaseUser, setDocumentRoot, setSftp, setTotp, splitCommand, timing, verifyDomain } from "./store";
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
