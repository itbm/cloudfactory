import { describe, expect, it } from "vitest";
import { attention, degradedReason, failedServices, updateReady } from "../api/selectors";
import { getState, loopbackName } from "../api/store";
import { reset, signedIn, website as site } from "../test/panelApi";
import type { Rollout } from "../api/types";
import { toServer } from "../api/live";
import { certificateText, certificateUntil, jobActive } from "../lib/labels";
import { parseTarget } from "./BackupsPage";
import { joinCommand } from "./ServersPage";
import { showRollout } from "./UpdatesPage";

const rollout = (over: Partial<Rollout>): Rollout => ({
  id: "r1", imageId: "i1", title: "FrankenPHP · PHP 8.5 0.1.9", fromTag: "0.1.7", toTag: "0.1.9", severity: "feature", state: "done",
  startedAt: "2026-11-01T10:00:00Z", finishedAt: "2026-11-01T10:05:00Z", window: "", websites: 0, servers: 1, stages: [], healthy: 0, rolledBack: 0, rollbacks: [], checks: [],
  ...over,
});

describe("the rollout card", () => {
  const end = new Date("2026-11-01T10:05:00Z").getTime();

  it("always shows a rollout in progress", () => {
    expect(showRollout(rollout({ state: "running", finishedAt: undefined }), "r1", end + 30 * 864e5)).toBe(true);
    expect(showRollout(rollout({ state: "paused", finishedAt: undefined }), "", end + 30 * 864e5)).toBe(true);
  });

  it("shows a rollout that ended for one day only", () => {
    expect(showRollout(rollout({}), "", end + 3600e3)).toBe(true);
    expect(showRollout(rollout({}), "", end + 25 * 3600e3)).toBe(false);
    expect(showRollout(rollout({ state: "rolled_back" }), "", end + 25 * 3600e3)).toBe(false);
  });

  it("hides a rollout that the user closed", () => {
    expect(showRollout(rollout({}), "r1", end + 60e3)).toBe(false);
    expect(showRollout(rollout({}), "r0", end + 60e3)).toBe(true);
    expect(showRollout(undefined, "", end)).toBe(false);
  });
});

describe("the object storage of the backups", () => {
  it("divides a restic repository into the type and the location", () => {
    expect(parseTarget("s3:s3.amazonaws.com/bucket/path")).toEqual({ type: "s3", location: "s3.amazonaws.com/bucket/path" });
    expect(parseTarget("s3:https://minio.example.com:9000/bucket")).toEqual({ type: "s3", location: "https://minio.example.com:9000/bucket" });
    expect(parseTarget("b2:bucket:path")).toEqual({ type: "b2", location: "bucket:path" });
    expect(parseTarget("azure:container:/path")).toEqual({ type: "azure", location: "container:/path" });
    expect(parseTarget("gs:bucket:/")).toEqual({ type: "gs", location: "bucket:/" });
  });

  it("starts with S3 when no storage is set", () => {
    expect(parseTarget("")).toEqual({ type: "s3", location: "" });
  });
});

describe("a degraded server", () => {
  const services = [
    { name: "cf-agent", state: "running" as const },
    { name: "cf-sftp", state: "failed" as const },
    { name: "firewall", state: "stopped" as const, detail: "The table is not loaded." },
    { name: "disk-quotas", state: "stopped" as const, optional: true },
  ];

  it("gives the necessary services that do not run as the reason", () => {
    expect(failedServices(services).map((s) => s.name)).toEqual(["cf-sftp", "firewall"]);
    expect(degradedReason(services)).toBe("Degraded: cf-sftp failed, firewall stopped");
  });

  it("does not count an optional part", () => {
    expect(degradedReason([services[0], services[3]])).toBeUndefined();
  });
});

describe("the certificate text of a domain", () => {
  it("does not tell the user to wait for DNS when the DNS is correct", () => {
    expect(certificateUntil({ certificate: { state: "pending" }, dnsPointsHere: true })).toBe("the edge proxy gets the certificate");
    expect(certificateUntil({ certificate: { state: "pending" }, dnsPointsHere: false })).toBe("certificate waits for DNS");
    // A website that the panel could not make: the cause is not the DNS.
    expect(certificateUntil({ certificate: { state: "pending" }, dnsPointsHere: false }, true)).toBe("no certificate yet");
    expect(certificateText({ certificate: { state: "pending" }, dnsPointsHere: true })).toBe("Getting the certificate");
    expect(certificateText({ certificate: { state: "pending" }, dnsPointsHere: false })).toBe("Waiting for DNS");
    expect(certificateUntil({ certificate: { state: "failed" }, dnsPointsHere: true })).toBe("the edge proxy could not get a certificate");
  });
});

describe("a website that the panel could not make", () => {
  it("shows as the first item that needs attention, with no DNS or update item", async () => {
    await signedIn("administrator");
    const state = structuredClone(getState());
    const website = state.websites.find((w) => w.id === site("shop.acme.example").id)!;
    website.domains[0].dnsPointsHere = false;
    const image = state.images.find((i) => i.id === website.runtime.imageId)!;
    image.available = "9.9.9";
    image.availableNote = undefined;
    expect(updateReady(state, website)).toBe(true);
    expect(attention(state).some((i) => i.id === `dns-${website.id}-${website.domains[0].name}`)).toBe(true);

    website.status = "failed";
    const items = attention(state).filter((i) => i.subject === website.name || i.subject === website.domains[0].name);
    expect(items.map((i) => i.id)).toContain(`failed-${website.id}`);
    expect(items.some((i) => i.id.startsWith("dns-") || i.id.startsWith("upd-"))).toBe(false);
    expect(updateReady(state, website)).toBe(false);
    reset();
  });
});

describe("a job that waits for the server", () => {
  it("counts as work in progress, the same as a job that runs", () => {
    // The agent does one job at a time. A second job stays "queued" until the first job ends.
    expect(jobActive({ state: "queued" })).toBe(true);
    expect(jobActive({ state: "running" })).toBe(true);
    expect(jobActive({ state: "succeeded" })).toBe(false);
    expect(jobActive({ state: "failed" })).toBe(false);
  });
});

describe("a server from the panel API", () => {
  const api = (over: object) => toServer({
    id: "s1", name: "web-01", location: "", roles: ["web"], state: "healthy", agentVersion: "0.1.9", os: null, kernel: null, arch: null,
    podmanVersion: null, gvisorVersion: null, gvisorPlatform: "systrap", bareMetal: false, updatePolicy: "security", maintenanceWindow: "02:00-05:00",
    pendingUpdates: [], lastUpdateAt: null, rebootRequired: false, services: [], usage: null, ...over,
  });

  it("has no date of the last update before the first update", () => {
    // The page shows "Never" for the empty value. Before, it showed "just now".
    expect(api({}).lastUpdateAt).toBe("");
  });

  it("tells when the panel has a newer agent", () => {
    expect(api({ agentUpdateAvailable: false }).agentUpdateAvailable).toBe(false);
    expect(api({ state: "degraded", agentUpdateAvailable: true }).agentUpdateAvailable).toBe(true);
    // A panel of an earlier release does not send the field.
    expect(api({ state: "agent_update" }).agentUpdateAvailable).toBe(true);
    expect(api({}).agentUpdateAvailable).toBe(false);
  });

  it("tells when the panel cannot connect to the agent", () => {
    const server = api({ state: "degraded", connectionError: "Could not resolve host: web2", agentAddress: "web2:7443", services: [{ name: "cf-agent", state: "running" }] });
    expect(server.connectionError).toBe("Could not resolve host: web2");
    expect(server.agentAddress).toBe("web2:7443");
    expect(server.stateDetail).toBe("Degraded: the panel cannot connect");
    expect(api({}).connectionError).toBeUndefined();
    expect(api({ panel: true }).panel).toBe(true);
  });
});

describe("the command that adds a server", () => {
  const base = { token: "cfj_abc", panelHost: "panel.example.com", name: "web-02", roles: ["web", "database"] };

  it("uses the installation script of the release of the panel", () => {
    // Before, the address was "releases/latest", which GitHub does not have while each release is a pre-release.
    const url = "https://github.com/itbm/cloudfactory/releases/download/v0.1.9/install.sh";
    expect(joinCommand({ ...base, installUrl: url, caSha256: "ab12" }))
      .toBe(`curl -fsSL ${url} | sh -s -- --join cfj_abc --panel https://panel.example.com --panel-ca-sha256 ab12 --name web-02 --roles web,database`);
  });

  it("has no fingerprint and no name when the panel gives none", () => {
    expect(joinCommand({ ...base, name: "", roles: [] })).toMatch(/--join cfj_abc --panel https:\/\/panel\.example\.com --roles web$/);
  });

  it("knows a panel name that a different server cannot reach", () => {
    for (const host of ["localhost", "panel.localhost", "PANEL.LOCALHOST.", "127.0.0.1", "127.0.1.1", "[::1]"]) expect(loopbackName(host)).toBe(true);
    for (const host of ["panel.example.com", "panel.internal", "10.0.0.5", "localhost.example.com", "1270.0.0.1"]) expect(loopbackName(host)).toBe(false);
  });
});
