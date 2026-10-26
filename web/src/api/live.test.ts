import { afterEach, describe, expect, it, vi } from "vitest";
import { LiveError, request, setUnauthorisedHandler, toJob, toRuntimeImage, toWebsite } from "./live";
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
  it("maps a job", () => {
    const job = toJob({ id: "j1", title: "Create website", ref: null, state: "running", steps: ["a", "b"], step: 1, error: null, websiteId: "w1", serverId: null, createdAt: "2026-10-26T09:00:00Z", finishedAt: null });
    expect(job).toEqual({ id: "j1", title: "Create website", ref: undefined, state: "running", steps: ["a", "b"], step: 1, error: undefined, createdAt: "2026-10-26T09:00:00Z", finishedAt: undefined, websiteId: "w1", serverId: undefined });
  });

  it("maps a website, its runtime and its domains", () => {
    const w = toWebsite({
      id: "w1", name: "acme.example", application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: "acmeexam_1a2b", status: "running",
      runtime: { imageId: "i1", family: "frankenphp", image: "ghcr.io/itbm/frankenphp-php8.4:0.1.0", phpVersion: "8.4", workerMode: false, extensions: ["opcache"], phpIni: {}, previousImageId: null },
      limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: { cpuPercent: 12 },
      domains: [{ name: "acme.example", kind: "primary", dnsPointsHere: true, certificate: { state: "valid", expiresAt: null } }],
      stagingOf: null, managedByTerraform: false, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z",
    });
    expect(w.application).toBe("Website");
    expect(w.runtime).toMatchObject({ kind: "frankenphp", imageId: "i1", phpVersion: "8.4", previous: undefined });
    expect(w.use.cpuPercent).toBe(12);
    expect(w.use.memoryBytes).toBe(0);
    expect(w.imageTag).toBe("0.1.0");
    expect(w.domains[0]).toEqual({ name: "acme.example", kind: "primary", dnsPointsHere: true, certificate: { state: "valid", expiresAt: undefined } });
  });

  it("keeps the TXT record of a domain that is not verified (CF-03)", () => {
    const verification = { type: "TXT" as const, name: "_cloudfactory-challenge.new.example", value: "cf-abc" };
    const w = toWebsite({
      id: "w1", name: "acme.example", application: "", customerId: "c1", serverId: "s1", planId: "p1", unixUser: "acmeexam_1a2b", status: "running",
      runtime: { imageId: "i1", family: "frankenphp", image: "ghcr.io/itbm/frankenphp-php8.4:0.1.0", phpVersion: "8.4", workerMode: false, extensions: [], phpIni: {}, previousImageId: null },
      limits: { vcpus: 1, memoryBytes: 1, diskBytes: 1, diskIoBytesPerSecond: 1, processes: 100 }, usage: {},
      domains: [
        { name: "acme.example", kind: "primary", dnsPointsHere: true, certificate: { state: "valid", expiresAt: null }, verified: true, verification: null },
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
  const image = (id: string, family: "frankenphp" | "apache-php-fpm", phpVersion: "8.2" | "8.4" | "8.5", supportEndsAt?: string) =>
    toRuntimeImage({ id, name: id, family, tag: "0.1.0", phpVersion, custom: false, supportEndsAt: supportEndsAt ?? null });
  const images = [image("fp82", "frankenphp", "8.2", "2020-01-01"), image("fp84", "frankenphp", "8.4"), image("ap84", "apache-php-fpm", "8.4")];

  it("takes the newest supported PHP version of the family", () => {
    expect(defaultImage([...images, image("fp85", "frankenphp", "8.5", "2020-01-01")], "frankenphp")).toBe("fp84");
    expect(defaultImage(images, "apache-php-fpm")).toBe("ap84");
  });

  it("uses FrankenPHP when the family has no image", () => {
    expect(defaultImage(images, "node")).toBe("fp84");
    expect(defaultImage([], "node")).toBe("");
  });
});
