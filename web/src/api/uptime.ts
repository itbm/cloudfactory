// Uptime checks: the panel requests an address of a website on a schedule, and sends an email
// when the website is down and when it is up again. The pages load this data when they open,
// so it is not in the state of the store.

import * as live from "./live";
import { ApiError } from "./store";

/** "pending": no result yet. "paused": the check is off, or the website does not run. */
export type UptimeState = "pending" | "up" | "down" | "paused";

export interface UptimeResult {
  ok: boolean;
  status: number | null;
  responseMs: number | null;
  error: string | null;
}

export interface UptimeIncident {
  id: string;
  startedAt: string;
  resolvedAt: string | null;
  durationSeconds: number;
  error: string;
  /** True when the check was paused while it was down. */
  endedByPause: boolean;
}

/** An incident in the list of all incidents, with its check and website. */
export interface UptimeIncidentRow extends UptimeIncident {
  checkId: string;
  websiteId: string;
  websiteName: string;
  url: string;
}

export interface UptimeCheck {
  id: string;
  websiteId: string;
  websiteName: string;
  domain: string;
  scheme: "https" | "http";
  path: string;
  url: string;
  intervalSeconds: number;
  timeoutSeconds: number;
  /** Null: each status from 200 to 399. */
  expectedStatus: number | null;
  keyword: string | null;
  failuresBeforeAlert: number;
  enabled: boolean;
  state: UptimeState;
  pausedReason: string | null;
  since: string | null;
  failures: number;
  lastCheckedAt: string | null;
  lastResult: UptimeResult | null;
  nextCheckAt: string | null;
  /** The percent of the time that the website was up, in the last 24 hours, 7 days and 30 days. */
  uptime: { day: number | null; week: number | null; month: number | null };
  incident: UptimeIncident | null;
  createdAt: string;
}

/** The results of 30 minutes. */
export interface UptimeBucket {
  at: string;
  checks: number;
  failures: number;
  responseMs: number | null;
  maxResponseMs: number | null;
}

export interface UptimeDay {
  date: string;
  /** Null: the check did not run on this day. */
  uptime: number | null;
  downSeconds: number;
}

export interface UptimeCheckDetail extends UptimeCheck {
  history: UptimeBucket[];
  days: UptimeDay[];
  incidents: UptimeIncident[];
}

export interface UptimeCheckInput {
  domain: string;
  scheme: "https" | "http";
  path: string;
  intervalSeconds: number;
  timeoutSeconds: number;
  expectedStatus: number | null;
  keyword: string | null;
  failuresBeforeAlert: number;
  enabled: boolean;
}

export type UptimeSummary = Record<UptimeState | "total", number>;

/** The limits of the API (App\Services\Uptime\Monitor). */
export const UPTIME_LIMITS = {
  perWebsite: 5,
  intervals: [60, 120, 300, 600, 900, 1800, 3600],
  minTimeout: 2,
  maxTimeout: 30,
  maxFailures: 5,
} as const;

async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e) {
    if (e instanceof live.LiveError) throw new ApiError(e.message, e.fields);
    throw e;
  }
}

const base = (websiteId: string) => `/websites/${encodeURIComponent(websiteId)}/uptime-checks`;

export const listUptimeChecks = (websiteId: string) =>
  call(() => live.request<{ data: UptimeCheck[] }>("GET", base(websiteId))).then((r) => r.data);

export const readUptimeCheck = (websiteId: string, checkId: string) =>
  call(() => live.request<UptimeCheckDetail>("GET", `${base(websiteId)}/${encodeURIComponent(checkId)}`));

export const addUptimeCheck = (websiteId: string, input: UptimeCheckInput) =>
  call(() => live.request<{ uptimeCheck: UptimeCheck }>("POST", base(websiteId), input)).then((r) => r.uptimeCheck);

export const setUptimeCheck = (websiteId: string, checkId: string, input: Partial<UptimeCheckInput>) =>
  call(() => live.request<{ uptimeCheck: UptimeCheck }>("PATCH", `${base(websiteId)}/${encodeURIComponent(checkId)}`, input)).then((r) => r.uptimeCheck);

export const removeUptimeCheck = (websiteId: string, checkId: string) =>
  call(() => live.request<void>("DELETE", `${base(websiteId)}/${encodeURIComponent(checkId)}`));

export const runUptimeCheck = (websiteId: string, checkId: string) =>
  call(() => live.request<{ uptimeCheck: UptimeCheck; result: UptimeResult }>("POST", `${base(websiteId)}/${encodeURIComponent(checkId)}/run`));

/** All checks that the user can see, the checks that are down first, and the number in each state. */
export async function listAllUptimeChecks(): Promise<{ checks: UptimeCheck[]; summary: UptimeSummary }> {
  const checks: UptimeCheck[] = [];
  let summary: UptimeSummary = { total: 0, up: 0, down: 0, pending: 0, paused: 0 };
  for (let page = 1; ; page++) {
    const r = await call(() => live.request<{ data: UptimeCheck[]; meta: { last_page: number }; summary: UptimeSummary }>("GET", `/uptime-checks?perPage=100&page=${page}`));
    checks.push(...r.data);
    summary = r.summary;
    if (page >= r.meta.last_page) break;
  }
  return { checks, summary };
}

export const listUptimeIncidents = () =>
  call(() => live.request<{ data: UptimeIncidentRow[] }>("GET", "/uptime-incidents")).then((r) => r.data);

/** A length of time for people, for example "1 hour 5 minutes" (Monitor::duration). */
export function duration(seconds: number): string {
  if (seconds < 60) return "less than 1 minute";
  let minutes = Math.floor(seconds / 60);
  const parts: string[] = [];
  for (const [unit, size] of [["day", 1440], ["hour", 60], ["minute", 1]] as const) {
    const n = Math.floor(minutes / size);
    minutes -= n * size;
    if (n > 0) parts.push(`${n} ${unit}${n === 1 ? "" : "s"}`);
  }
  return parts.slice(0, 2).join(" ");
}

/** A percent of uptime: "99.95%", or "No data". */
export const uptimeText = (v: number | null) => (v === null ? "No data" : `${v.toFixed(v === 100 ? 0 : 2)}%`);

/** An interval for people: "1 minute", "5 minutes", "1 hour". */
export const intervalText = (seconds: number) =>
  seconds >= 3600 ? `${seconds / 3600} hour${seconds === 3600 ? "" : "s"}` : `${seconds / 60} minute${seconds === 60 ? "" : "s"}`;
