import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, addDomain, changePassword, createDatabase, createDatabaseUser, createToken, getState, resetState, setDatabaseUser, setTotp, timing, verifyDomain } from "./store";

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
