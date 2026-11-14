import { afterEach, describe, expect, it, vi } from "vitest";
import { DETAIL_LIMIT, LiveError, load, request, setUnauthorisedHandler, toDatabaseServer, toJob, toPanelCertificate, toRuntimeImage, toServer, toWebsite } from "./live";
import { emptyState } from "./store";
import { defaultImage } from "../pages/NewWebsitePage";

const answer = (status: number, body: unknown) =>
  vi.stubGlobal("fetch", vi.fn(async () => new Response(status === 204 ? null : JSON.stringify(body), { status })));

afterEach(() => vi.unstubAllGlobals());

describe("request", () => {
  it("sends the request to the API and gives the body", async () => {
    answer(200, { data: [1] });
    await expect(request("GET", "/websites")).resolves.toEqual({ data: [1] });
    expect(fetch).toHaveBeenCalledWith("/api/v1/websites", expect.objectContaining({ method: "GET" }));
  });

  it("gives each field error, and uses one error as the message", async () => {
    answer(422, { message: "x", errors: { email: ["The email or the password is not correct."] } });
    const error = await request("POST", "/auth/login", {}).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(LiveError);
    expect(error).toMatchObject({ message: "The email or the password is not correct.", fields: { email: "The email or the password is not correct." }, status: 422 });

    answer(422, { errors: { name: ["Bad name."], planId: ["Bad plan."] } });
    await expect(request("POST", "/websites", {})).rejects.toMatchObject({ message: "Correct the errors and try again.", fields: { name: "Bad name.", planId: "Bad plan." } });
  });

  it("gives the message of the API for other errors", async () => {
    answer(403, { message: "The token does not have the servers:read scope." });
    await expect(request("GET", "/servers")).rejects.toMatchObject({ message: "The token does not have the servers:read scope.", status: 403 });
  });

  it("says when the API does not answer", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("network"); }));
    await expect(request("GET", "/me")).rejects.toMatchObject({ message: expect.stringContaining("does not answer") });
  });

  it("does not sign out when there is no session", async () => {
    const handler = vi.fn();
    setUnauthorisedHandler(handler);
    answer(401, { message: "Unauthenticated." });
    await expect(request("GET", "/me")).rejects.toMatchObject({ status: 401 });
    expect(handler).not.toHaveBeenCalled();
  });
});

describe("mappers", () => {
  it("maps the panel certificate", () => {
    const cert = toPanelCertificate({
      domain: "panel.localhost", method: "internal", state: "valid", expiresAt: "2027-02-09T10:00:00Z", caCertificate: "-----BEGIN CERTIFICATE-----", caSha256: "ab".repeat(32),
      reportedAt: null, letsEncryptAvailable: false, serversWithoutCa: ["web-2"], job: null,
    });
    expect(cert).toEqual({
      domain: "panel.localhost", method: "internal", state: "valid", expiresAt: "2027-02-09T10:00:00Z", caCertificate: "-----BEGIN CERTIFICATE-----", caSha256: "ab".repeat(32),
      reportedAt: undefined, letsEncryptAvailable: false, serversWithoutCa: ["web-2"], job: undefined,
    });
  });

  it("maps a job", () => {
    const job = toJob({ id: "j1", title: "Create website", ref: null, state: "running", steps: ["a", "b"], step: 1, error: null, websiteId: "w1", serverId: null, createdAt: "2026-10-26T09:00:00Z", finishedAt: null });
    expect(job).toEqual({ id: "j1", title: "Create website", ref: undefined, state: "running", steps: ["a", "b"], step: 1, error: undefined, createdAt: "2026-10-26T09:00:00Z", finishedAt: undefined, websiteId: "w1", serverId: undefined });
  });

  it("maps a website, its runtime and its domains", () => {
    const w = toWebsite({
      id: "w1", name: "acme.example.com", application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: "acmeexam_1a2b", status: "running",
      runtime: { imageId: "i1", family: "frankenphp", image: "ghcr.io/itbm/frankenphp-php8.4:0.1.0", phpVersion: "8.4", workerMode: false, extensions: ["opcache"], phpIni: {}, previousImageId: null },
      limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: { cpuPercent: 12 },
      domains: [{ name: "acme.example.com", kind: "primary", dnsPointsHere: true, certificate: { state: "valid", expiresAt: null } }],
      stagingOf: null, managedByTerraform: false, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z",
    });
    expect(w.application).toBe("Website");
    expect(w.runtime).toMatchObject({ kind: "frankenphp", imageId: "i1", phpVersion: "8.4", previous: undefined });
    expect(w.use.cpuPercent).toBe(12);
    expect(w.use.memoryBytes).toBe(0);
    expect(w.imageTag).toBe("0.1.0");
    expect(w.domains[0]).toEqual({ name: "acme.example.com", kind: "primary", dnsPointsHere: true, certificate: { state: "valid", expiresAt: undefined } });
    // A panel of an earlier release does not send the field.
    expect(w.sharedDatabases).toBe(false);
  });

  it("maps the database containers of a website and of a server", () => {
    const website = { id: "w1", name: "acme.example.com", application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: "acmeexam_1a2b", status: "running" as const,
      runtime: { imageId: "i1", family: "frankenphp", image: "ghcr.io/itbm/frankenphp-php8.4:0.1.0", phpVersion: "8.4", workerMode: false, extensions: [], phpIni: {}, previousImageId: null },
      limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: {},
      stagingOf: null, managedByTerraform: false, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z" };
    expect(toWebsite({ ...website, sharedDatabases: true }).sharedDatabases).toBe(true);
    expect(toWebsite({ ...website, sharedDatabases: false }).sharedDatabases).toBe(false);

    const server = { id: "s1", name: "web-1", location: "", roles: ["web" as const], state: "healthy" as const, agentVersion: "0.2.0", os: null, kernel: null, arch: null,
      podmanVersion: null, gvisorVersion: null, gvisorPlatform: "systrap" as const, bareMetal: false, updatePolicy: "security" as const, maintenanceWindow: "02:00-05:00",
      pendingUpdates: [], lastUpdateAt: null, rebootRequired: false, services: [], usage: null };
    expect(toServer({ ...server, databaseInstances: true, sharedDatabaseWebsites: 3 })).toMatchObject({ databaseInstances: true, sharedDatabaseWebsites: 3 });
    // An agent of an earlier release: the panel gives no number of websites.
    expect(toServer({ ...server, databaseInstances: false, sharedDatabaseWebsites: null })).toMatchObject({ databaseInstances: false, sharedDatabaseWebsites: undefined });
    expect(toServer(server).databaseInstances).toBe(false);
  });

  it("maps a database server, and the database server of a job", () => {
    const api = { id: "ds1", name: "mariadb-1", engine: "mariadb" as const, customerId: "c1", serverId: "s1", planId: "p1", status: "starting" as const, host: "mariadb.internal", port: 3306,
      limits: { vcpus: 1, memoryBytes: 536870912, diskBytes: 5368709120 }, usage: null, databases: 0, maxDatabases: 10, createdAt: null };
    // A new database server has no report of the server yet.
    expect(toDatabaseServer(api)).toEqual({ ...api, usage: undefined, createdAt: "" });
    const usage = { memoryBytes: 1024, diskBytes: 2048, diskSizeBytes: 4096 };
    expect(toDatabaseServer({ ...api, status: "running", usage, createdAt: "2026-11-14T10:00:00+00:00" })).toMatchObject({ status: "running", usage, createdAt: "2026-11-14T10:00:00+00:00" });
    const job = { id: "j1", title: "Create database server mariadb-1", ref: null, state: "running" as const, steps: ["Start database server"], step: 0, error: null, websiteId: null, serverId: "s1", createdAt: "2026-11-14T10:00:00Z", finishedAt: null };
    expect(toJob({ ...job, databaseServerId: "ds1" }).databaseServerId).toBe("ds1");
    expect(toJob({ ...job, databaseServerId: null }).databaseServerId).toBeUndefined();
    // A panel of an earlier release does not send the field.
    expect(toJob(job).databaseServerId).toBeUndefined();
  });

  it("keeps the TXT record of a domain that is not verified (CF-03)", () => {
    const verification = { type: "TXT" as const, name: "_cloudfactory-challenge.new.example", value: "cf-abc" };
    const w = toWebsite({
      id: "w1", name: "acme.example.com", application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: "acmeexam_1a2b", status: "running",
      runtime: { imageId: "i1", family: "frankenphp", image: "ghcr.io/itbm/frankenphp-php8.4:0.1.0", phpVersion: "8.4", workerMode: false, extensions: [], phpIni: {}, previousImageId: null },
      limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: {},
      domains: [
        { name: "acme.example.com", kind: "primary", dnsPointsHere: true, certificate: { state: "valid", expiresAt: null }, verified: true, verification: null },
        { name: "new.example", kind: "alias", dnsPointsHere: false, certificate: { state: "pending", expiresAt: null }, verified: false, verification },
      ],
      stagingOf: null, managedByTerraform: false, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z",
    });
    expect(w.domains[0].verification).toBeUndefined();
    expect(w.domains[1].verification).toEqual(verification);
  });

  it("maps an image for a new website", () => {
    expect(toRuntimeImage({ id: "i1", name: "FrankenPHP · PHP 8.4", family: "frankenphp", tag: "0.1.0", custom: false, supportEndsAt: null }))
      .toMatchObject({ id: "i1", running: "0.1.0", custom: false, signatureVerified: true, supportEndsAt: undefined });
  });
});

describe("defaultImage", () => {
  const image = (id: string, family: "frankenphp" | "apache-php-fpm", phpVersion: "8.4" | "8.5", supportEndsAt?: string) =>
    toRuntimeImage({ id, name: id, family, tag: "0.1.0", phpVersion, custom: false, supportEndsAt: supportEndsAt ?? null });
  const images = [image("fp84", "frankenphp", "8.4"), image("ap84", "apache-php-fpm", "8.4")];

  it("takes the newest supported PHP version of the family", () => {
    expect(defaultImage([...images, image("fp85", "frankenphp", "8.5")], "frankenphp")).toBe("fp85");
    expect(defaultImage([...images, image("fp85", "frankenphp", "8.5", "2020-01-01")], "frankenphp")).toBe("fp84");
    expect(defaultImage(images, "apache-php-fpm")).toBe("ap84");
  });

  it("uses FrankenPHP when the family has no image", () => {
    expect(defaultImage(images, "node")).toBe("fp84");
    expect(defaultImage([], "node")).toBe("");
  });
});

describe("load (batch 1, finding 9)", () => {
  const site = (n: number) => ({
    id: `w${n}`, name: `site${n}.example`, application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: `site${n}_1a2b`, status: "running",
    runtime: { imageId: "i1", family: "frankenphp", image: "x:1", phpVersion: "8.4", workerMode: false, extensions: [], phpIni: {}, previousImageId: null },
    limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: {}, domains: [],
    sftp: { enabled: true, host: "web-01.example", port: 22, username: `site${n}_1a2b`, authorizedKeys: [] },
    stagingOf: null, managedByTerraform: false, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z",
  });

  it("reads all pages, and reads the databases of each website only when there are few", async () => {
    const pages = 60;
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      const path = url.replace("/api/v1", "");
      const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200 });
      if (path === "/me") return json({ id: 1, name: "Owner", email: "o@example.com", role: "owner", customerId: "c1", totpEnabled: true });
      const page = /^\/websites\?perPage=100&page=(\d+)$/.exec(path);
      if (page) return json({ data: [site(+page[1])], meta: { last_page: pages } });
      if (path.startsWith("/backup-policy")) return json({ daily: 7, weekly: 4, monthly: 6, time: "03:00", target: "s3:x/y" });
      return json({ data: [] });
    }));
    const state = await load(emptyState());
    expect(pages).toBeGreaterThan(50);
    expect(state.websites).toHaveLength(pages);
    expect(state.websites[0].sftp).toMatchObject({ enabled: true, host: "web-01.example" });
    expect(pages).toBeGreaterThan(DETAIL_LIMIT);
    expect(calls.some((c) => c.includes("/databases") || c.includes("/snapshots"))).toBe(false);
  });
});

describe("load with a temporary password (audit finding B5)", () => {
  it("reads only the user, because the API refuses each other request", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      if (url === "/api/v1/me") return new Response(JSON.stringify({ id: 7, name: "New", email: "new@example.com", role: "owner", customerId: "c1", totpEnabled: false, mustChangePassword: true }), { status: 200 });
      return new Response(JSON.stringify({ message: "Set a new password first. Your password is temporary.", code: "password_change_required" }), { status: 403 });
    }));
    const state = await load({ ...emptyState(), websites: [], users: [] });
    expect(calls).toEqual(["/api/v1/me"]);
    expect(state.session).toMatchObject({ signedIn: true, mustChangePassword: true, user: { id: "7", email: "new@example.com" } });
    expect(state.totpEnabled).toBe(false);
  });

  it("reads only the user when an administrator must set up TOTP (second audit, finding F09)", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      if (url === "/api/v1/me") return new Response(JSON.stringify({ id: 3, name: "Admin", email: "a@example.com", role: "administrator", customerId: null, totpEnabled: false, mustChangePassword: false, totpSetupRequired: true, securityNotices: [] }), { status: 200 });
      return new Response(JSON.stringify({ message: "Set up TOTP first.", code: "totp_setup_required" }), { status: 403 });
    }));
    const state = await load(emptyState());
    expect(calls).toEqual(["/api/v1/me"]);
    expect(state.session).toMatchObject({ signedIn: true, totpSetupRequired: true, user: { id: "3", role: "administrator" } });
    expect(state.session.mustChangePassword).toBeUndefined();
    expect(state.totpEnabled).toBe(false);
  });

  it("reads the users and the kind of each snapshot after the password change", async () => {
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const path = url.replace("/api/v1", "");
      const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200 });
      if (path === "/me") return json({ id: 1, name: "Admin", email: "a@example.com", role: "administrator", customerId: null, totpEnabled: true, mustChangePassword: false, totpSetupRequired: false, securityNotices: [{ action: "Created administrator", target: "new@example.com", actor: "Other Admin", at: "2026-11-13T10:00:00+00:00" }] });
      if (path === "/users") return json({ data: [{ id: 2, name: "Owner", email: "o@example.com", role: "owner", customerId: "c1", totpEnabled: false, mustChangePassword: true, createdAt: "2026-11-10T09:00:00+00:00" }, { id: 1, name: "Admin", email: "a@example.com", role: "administrator", customerId: null, totpEnabled: true, mustChangePassword: false, createdAt: null }] });
      if (path.startsWith("/websites?")) return json({ data: [{ id: "w1", name: "a.example", application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: "a_1a2b", status: "running", runtime: { imageId: "i1", family: "frankenphp", image: "x:1", phpVersion: "8.4", workerMode: false, extensions: [], phpIni: {}, previousImageId: null }, limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: {}, domains: [], stagingOf: null, managedByTerraform: false, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z" }], meta: { last_page: 1 } });
      if (path === "/websites/w1/snapshots") return json({ data: [{ id: "s1", kind: "scheduled", sizeBytes: 5, databases: [], mailboxes: [], createdAt: "2026-11-10T03:00:00Z" }, { id: "s2", kind: "manual", sizeBytes: 5, databases: [], mailboxes: [], createdAt: "2026-11-09T12:00:00Z" }] });
      if (path === "/websites/w1/databases") return json({ databases: [{ id: "d1", engine: "valkey", name: "a_1a2b_cache", sizeBytes: 0, number: 3, keys: 12, databaseServerId: "ds2", host: "valkey.internal", port: 6380 }, { id: "d2", engine: "valkey", name: "a_1a2b_new", sizeBytes: 0, number: null, keys: null, databaseServerId: "ds2", host: "valkey.internal", port: 6380 }, { id: "d3", engine: "mariadb", name: "a_1a2b_wp", sizeBytes: 4096, number: null, keys: null, databaseServerId: null, host: "mariadb.internal", port: 3306 }], users: [{ id: "u1", engine: "mariadb", name: "a_1a2b_wp", access: "website", databaseServerId: null, grants: [] }, { id: "u2", engine: "valkey", name: "a_1a2b_cache", access: "website", databaseServerId: "ds2", grants: [] }] });
      if (path === "/database-servers") return json({ data: [{ id: "ds2", name: "valkey-2", engine: "valkey", customerId: "c1", serverId: "s1", planId: "p1", status: "running", host: "valkey.internal", port: 6380, limits: { vcpus: 1, memoryBytes: 536870912, diskBytes: 1073741824 }, usage: { memoryBytes: 1000, diskBytes: 2000, diskSizeBytes: 3000 }, databases: 2, maxDatabases: 10, createdAt: "2026-11-12T08:00:00+00:00" }] });
      if (path === "/plans") return json({ data: [{ id: "p1", name: "Starter", limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, websites: 1, mailboxes: 0, databases: 10, phpExtensions: [], databaseServers: 3, databaseLimits: { vcpus: 1, memoryBytes: 536870912, diskBytes: 1073741824 } }] });
      if (path.startsWith("/backup-policy")) return json({ daily: 7, weekly: 4, monthly: 6, time: "03:00", target: "s3:x/y", manual: 10 });
      return json({ data: [] });
    }));
    const state = await load(emptyState());
    expect(state.session.mustChangePassword).toBeUndefined();
    expect(state.session.totpSetupRequired).toBeUndefined();
    expect(state.securityNotices).toEqual([{ action: "Created administrator", target: "new@example.com", actor: "Other Admin", at: "2026-11-13T10:00:00+00:00" }]);
    expect(state.users).toEqual([
      { id: "2", name: "Owner", email: "o@example.com", role: "owner", customerId: "c1", totpEnabled: false, mustChangePassword: true, createdAt: "2026-11-10T09:00:00+00:00" },
      { id: "1", name: "Admin", email: "a@example.com", role: "administrator", customerId: undefined, totpEnabled: true, mustChangePassword: false, createdAt: "" },
    ]);
    // The API names a nightly backup "scheduled".
    expect(state.snapshots.map((s) => [s.id, s.kind])).toEqual([["s1", "nightly"], ["s2", "manual"]]);
    expect(state.backupPolicy.manual).toBe(10);
    // Valkey: the number is not known until the agent made the database.
    expect(state.databases.map((d) => [d.name, d.number, d.keys, d.sizeBytes])).toEqual([["a_1a2b_cache", 3, 12, 0], ["a_1a2b_new", undefined, undefined, 0], ["a_1a2b_wp", undefined, undefined, 4096]]);
    // The database servers of the customers, and the database server of each database and user.
    // A database in the shared database containers of an earlier release has none.
    expect(state.databaseServers).toMatchObject([{ id: "ds2", name: "valkey-2", port: 6380, databases: 2, maxDatabases: 10, usage: { diskSizeBytes: 3000 } }]);
    expect(state.databases.map((d) => [d.databaseServerId, d.host, d.port])).toEqual([["ds2", "valkey.internal", 6380], ["ds2", "valkey.internal", 6380], [undefined, "mariadb.internal", 3306]]);
    expect(state.databaseUsers.map((u) => [u.name, u.databaseServerId])).toEqual([["a_1a2b_wp", undefined], ["a_1a2b_cache", "ds2"]]);
    expect(state.plans[0]).toMatchObject({ databases: 10, databaseServers: 3, databaseLimits: { memoryBytes: 536870912 } });
  });
});
