// A panel API for the tests. It answers the read requests of the web panel with the answers
// of the real API for the sample data (fixtures/*.json). The command "php artisan
// demo:fixtures" in api/ writes the files, and an API test fails when they do not agree with
// the API. Thus the tests of the web panel cannot drift from the product.

import { vi } from "vitest";
import { getState, refresh, resetState, signIn, signOut } from "../api/store";
import type { Role, Website } from "../api/types";
import administrator from "./fixtures/administrator.json";
import owner from "./fixtures/owner.json";
import reseller from "./fixtures/reseller.json";

type Answer = { status: number; body: unknown };
type Handler = (body: unknown, path: string) => Answer | [number, unknown] | unknown;

const FIXTURES: Record<Role, Record<string, Answer>> = { administrator, reseller, owner } as never;

/** The email of the sample user of each role (api/app/Demo/Fixtures.php). */
export const EMAIL: Record<Role, string> = { administrator: "admin@example.com", reseller: "reseller@example.com", owner: "owner@example.com" };

export interface PanelApi {
  /** The requests that changed something, in order: "POST /websites". */
  readonly changes: { method: string; path: string; body: unknown }[];
  /**
   * Sets the answer to a request, for example on("POST /websites/{id}/snapshots", () => [202, { job }]).
   * The pattern can have "*" for one part of the path. A handler can give the body only (status 200).
   */
  on(pattern: string, handler: Handler): void;
  /** Changes the answer to a read request: the fixture of the role, changed by the function. */
  change<T = unknown>(path: string, change: (body: T) => void): void;
}

const matches = (pattern: string, key: string) =>
  new RegExp(`^${pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]+")}$`).test(key);

/** Puts the fake API in the place of fetch. The answers are the fixtures of the role. */
export function panelApi(role: Role): PanelApi {
  const answers = structuredClone(FIXTURES[role]);
  const handlers: [string, Handler][] = [];
  const changes: PanelApi["changes"] = [];

  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method ?? "GET";
    const path = url.replace(/^\/api\/v1/, "");
    const key = `${method} ${path}`;
    const body = typeof init.body === "string" ? JSON.parse(init.body) as unknown : undefined;
    // A text body is not JSON, for example the recording of a terminal session.
    const respond = (a: Answer) => new Response(a.status === 204 ? null : typeof a.body === "string" ? a.body : JSON.stringify(a.body), { status: a.status });

    const handler = [...handlers].reverse().find(([pattern]) => matches(pattern, key));
    if (method !== "GET") changes.push({ method, path, body });
    if (handler) {
      const result = handler[1](body, path);
      if (Array.isArray(result) && typeof result[0] === "number") return respond({ status: result[0], body: result[1] });
      return respond(result && typeof result === "object" && "status" in result && "body" in result ? result as Answer : { status: 200, body: result });
    }
    if (key === "POST /auth/login") return respond({ status: 200, body: { token: "1|test" } });
    if (key === "POST /auth/logout") return respond({ status: 204, body: null });
    // The brand of the sign-in page: the built-in brand, unless a test gives a different one.
    if (key === "GET /brand") return respond({ status: 200, body: { name: "Cloudfactory", colour: "#1d5fd6", logo: null, supportEmail: null, builtIn: true } });
    // The ways to sign in of the sign-in page: the panel has an SMTP server, passkeys work, and
    // there is no single sign-on provider.
    if (key === "GET /auth/methods") return respond({ status: 200, body: { passwordReset: true, passkeys: true, providers: [] } });
    if (answers[key]) return respond(answers[key]);
    // The usage graphs: the fixtures have no history. A test can give one with "on".
    if (method === "GET" && /^\/(websites|servers|database-servers)\/[^/]+\/usage\?range=/.test(path)) {
      return respond({ status: 200, body: usageHistory(path) });
    }
    // A test must give the answer to each change that it makes.
    return respond({ status: 500, body: { message: `The test has no answer for "${key}".` } });
  }));

  return {
    changes,
    on: (pattern, handler) => { handlers.push([pattern, handler]); },
    change: (path, change) => { change(answers[`GET ${path}`].body as never); },
  };
}

/** Signs in as the sample user of the role, and loads the state from the fixtures. */
export async function signedIn(role: Role): Promise<PanelApi> {
  const api = panelApi(role);
  await signIn(EMAIL[role], "password");
  return api;
}

/** Signs out and removes the fake API. Call it after each test. */
export function reset() {
  signOut();
  resetState();
  vi.unstubAllGlobals();
}

/** A website of the sample data, by its name. */
export function website(name: string): Website {
  const w = getState().websites.find((x) => x.name === name);
  if (!w) throw new Error(`The sample data has no website ${name}.`);
  return w;
}

/** A job as the API gives it, for the answer to a change. */
export const apiJob = (over: Record<string, unknown> = {}) => ({
  id: "job-1", title: "Job", ref: null, action: "Backup", state: "succeeded", steps: ["One", "Two"], step: 2, error: null, websiteId: null, serverId: null,
  createdAt: "2026-11-14T10:00:00+00:00", finishedAt: "2026-11-14T10:00:05+00:00", ...over,
});

export { refresh };

/** A usage history of 24 hours with two points with values, for the graphs. */
export function usageHistory(path = "/websites/x/usage?range=24h") {
  const now = Date.UTC(2026, 10, 15, 12, 0);
  const points = Array.from({ length: 288 }, (_, i) => {
    const at = new Date(now - (287 - i) * 300_000).toISOString().replace(".000Z", "Z");
    const has = i >= 286;
    return { at, cpuPercent: has ? 40 + i - 286 : null, cpuMaxPercent: has ? 60 : null, memoryBytes: has ? 512 * 1024 ** 2 : null, memoryMaxBytes: has ? 600 * 1024 ** 2 : null,
      diskBytes: has ? 3 * 1024 ** 3 : null, processes: has ? 12 : null, bytesIn: has ? 1000 : null, bytesOut: has ? 90000 : null,
      memoryPercent: has ? 50 : null, memoryMaxPercent: has ? 55 : null, diskPercent: has ? 30 : null };
  });
  return {
    range: "24h", stepMinutes: 5, from: points[0].at, to: points[287].at, points,
    ...(path.startsWith("/websites/") ? { limits: { vcpus: 2, memoryBytes: 2 * 1024 ** 3, diskBytes: 40 * 1024 ** 3, bandwidthBytes: 500 * 1024 ** 3 }, bandwidth: { month: "2026-11", bytes: 12 * 1024 ** 3, limitBytes: 500 * 1024 ** 3 } } : {}),
    alerts: [],
  };
}
