// The client for the real panel API (openapi/openapi.yaml). store.ts calls these functions
// when the panel runs in live mode (see mode.ts). Each function sends one request, and then
// loads the changed data again. A change that makes a job gives a promise that resolves when
// the agent has finished the job.

import type {
  ApiToken,
  AuditEntry,
  BackupPolicy,
  Customer,
  Database,
  DatabaseGrant,
  DatabaseUser,
  DomainVerification,
  Job,
  Limits,
  ManagedUser,
  PanelCertificate,
  Plan,
  Rollout,
  RuntimeImage,
  RuntimeKind,
  Server,
  Snapshot,
  State,
  User,
  Website,
} from "./types";
import { degradedReason } from "./selectors";

/** The error of a request. store.ts makes an ApiError from it. */
export class LiveError extends Error {
  constructor(message: string, readonly fields: Record<string, string> = {}, readonly status = 0) {
    super(message);
    this.name = "LiveError";
  }
}

const TOKEN_KEY = "cloudfactory.token";
const BASE = "/api/v1";

function readToken(): string | null {
  try {
    return sessionStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function writeToken(token: string | null) {
  try {
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
    else sessionStorage.removeItem(TOKEN_KEY);
  } catch {
    // The browser does not keep the session. The user signs in again after a reload.
  }
}

let token = readToken();
export const hasSession = () => token !== null;

/** Called when the session token is no longer valid. store.ts signs the user out. */
let onUnauthorised: () => void = () => {};
export function setUnauthorisedHandler(fn: () => void) {
  onUnauthorised = fn;
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers.Authorization = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new LiveError("The panel API does not answer. Check your connection and try again.");
  }
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (res.ok) return data as T;
  if (res.status === 401 && token) {
    token = null;
    writeToken(null);
    onUnauthorised();
  }
  const fields: Record<string, string> = {};
  for (const [key, messages] of Object.entries((data as { errors?: Record<string, string[]> }).errors ?? {})) {
    fields[key] = messages[0];
  }
  // With one error, the message says what to correct. Some forms do not show each field.
  const messages = Object.values(fields);
  const message = res.status === 422 ? (messages.length === 1 ? messages[0] : "Correct the errors and try again.") : (data as { message?: string }).message || `The panel API gave status ${res.status}.`;
  throw new LiveError(message, fields, res.status);
}

/** Reads all pages of a paginated list. Batch 1, finding 9: it reads each page, with no limit. */
async function all<T>(path: string): Promise<T[]> {
  const out: T[] = [];
  for (let page = 1; ; page++) {
    const sep = path.includes("?") ? "&" : "?";
    const res = await request<{ data: T[]; meta?: { last_page?: number; lastPage?: number } }>("GET", `${path}${sep}perPage=100&page=${page}`);
    out.push(...res.data);
    const last = res.meta?.last_page ?? res.meta?.lastPage ?? 1;
    if (page >= last || res.data.length === 0) return out;
  }
}

/**
 * Batch 1, finding 9: the databases and snapshots of each website need 2 requests. With up to
 * this number of websites, each load reads them for all websites. With more, the panel reads
 * them only for the websites that the user opened (showWebsite).
 */
export const DETAIL_LIMIT = 50;
const shown = new Set<string>();

// Shapes of the API (openapi/openapi.yaml, components/schemas).

interface ApiMe { id: number; name: string; email: string; role: User["role"]; customerId: string | null; totpEnabled: boolean; mustChangePassword?: boolean }
interface ApiUser { id: number; name: string; email: string; role: User["role"]; customerId: string | null; totpEnabled: boolean; mustChangePassword: boolean; createdAt: string | null }
interface ApiJob { id: string; title: string; ref: string | null; state: Job["state"]; steps: string[]; step: number; error: string | null; websiteId: string | null; serverId: string | null; createdAt: string; finishedAt: string | null }
interface ApiWebsite {
  id: string; name: string; application: string; customerId: string; serverId: string; planId: string; unixUser: string; status: Website["status"];
  runtime: { imageId: string; family: string; image: string; phpVersion: string | null; workerMode: boolean; extensions: string[]; phpIni: Record<string, string>; previousImageId: string | null; documentRoot?: string };
  sftp?: { enabled: boolean; host: string | null; port: number; username: string; authorizedKeys: string[] };
  limits: Limits; usage: Partial<Website["use"]> | null;
  domains?: { name: string; kind: Website["domains"][number]["kind"]; dnsPointsHere: boolean; certificate: { state: Website["domains"][number]["certificate"]["state"]; expiresAt: string | null }; verified?: boolean; verification?: DomainVerification | null }[];
  stagingOf: string | null; managedByTerraform: boolean; createdAt: string; updatedAt: string;
}
interface ApiServer {
  id: string; name: string; location: string; roles: Server["roles"]; state: Server["state"]; agentVersion: string | null; os: string | null; kernel: string | null; arch: string | null;
  podmanVersion: string | null; gvisorVersion: string | null; gvisorPlatform: Server["gvisorPlatform"]; bareMetal: boolean; updatePolicy: Server["updatePolicy"]; maintenanceWindow: string;
  pendingUpdates: Server["pendingUpdates"]; lastUpdateAt: string | null; rebootRequired: boolean; agentUpdateAvailable?: boolean; agentAddress?: string | null; connectionError?: string | null; panel?: boolean; databaseEngines?: Database["engine"][]; services: Server["services"]; sftpPort?: number | null;
  usage: { cpuPercent?: number; memoryPercent?: number; diskPercent?: number } | null;
}
interface ApiImage {
  id: string; name: string; family: RuntimeImage["family"]; reference: string; tag: string; digest: string | null; availableTag: string | null; phpVersion: RuntimeImage["phpVersion"] | null;
  supportEndsAt: string | null; policy: RuntimeImage["policy"]; custom: boolean; signatureVerified: boolean; websites: number | null; checkedAt?: string | null; checkError?: string | null;
  containerPort?: number; command?: string[] | null;
}
interface ApiRuntimeImage { id: string; name: string; family: RuntimeImage["family"]; tag: string; phpVersion?: RuntimeImage["phpVersion"] | null; custom: boolean; supportEndsAt: string | null }
interface ApiRollout {
  id: string; imageId: string; title: string; fromTag: string; toTag: string; severity: Rollout["severity"]; state: Rollout["state"]; stages: Rollout["stages"];
  websites: number; healthy: number; rolledBack: number; running: number; rollbacks: Rollout["rollbacks"]; startedAt: string; finishedAt?: string | null;
}
interface ApiCustomer { id: string; name: string; email: string; role: Customer["role"]; planId: string; resellerId: string | null; suspended: boolean; createdAt: string }
interface ApiToken_ { id: number; name: string; scopes: string[]; createdAt: string; expiresAt: string | null; lastUsedAt: string | null }
interface ApiAudit { id: number; at: string; actor: string; ip: string | null; action: string; target: string }
interface ApiPanelCertificate {
  domain: string; method: PanelCertificate["method"]; state: PanelCertificate["state"]; expiresAt: string | null; caCertificate: string | null; caSha256: string | null;
  reportedAt: string | null; letsEncryptAvailable: boolean; serversWithoutCa: string[]; job: ApiJob | null;
}
interface ApiSnapshot { id: string; kind: "scheduled" | "manual"; sizeBytes: number; databases: string[]; mailboxes: string[]; createdAt: string }

// Mapping from the API shapes to the state of the UI.

const runtimeKind = (family: string): RuntimeKind =>
  family === "frankenphp" || family === "apache-php-fpm" ? family : family === "static" || family === "edge" ? "static" : "oci";

const tagOfReference = (ref: string) => {
  const at = ref.lastIndexOf(":");
  return at > ref.lastIndexOf("/") ? ref.slice(at + 1) : "";
};

export function toJob(j: ApiJob): Job {
  return {
    id: j.id, title: j.title, ref: j.ref ?? undefined, state: j.state, steps: j.steps ?? [], step: j.step ?? 0, error: j.error ?? undefined,
    createdAt: j.createdAt, finishedAt: j.finishedAt ?? undefined, websiteId: j.websiteId ?? undefined, serverId: j.serverId ?? undefined,
  };
}

export function toPanelCertificate(c: ApiPanelCertificate): PanelCertificate {
  return {
    domain: c.domain, method: c.method, state: c.state, expiresAt: c.expiresAt ?? undefined, caCertificate: c.caCertificate ?? undefined, caSha256: c.caSha256 ?? undefined,
    reportedAt: c.reportedAt ?? undefined, letsEncryptAvailable: c.letsEncryptAvailable, serversWithoutCa: c.serversWithoutCa ?? [], job: c.job ? toJob(c.job) : undefined,
  };
}

export function toWebsite(w: ApiWebsite): Website {
  return {
    id: w.id, name: w.name, application: w.application || "Website", customerId: w.customerId, serverId: w.serverId, unixUser: w.unixUser,
    createdAt: w.createdAt, status: w.status, planId: w.planId, limits: w.limits,
    runtime: {
      kind: runtimeKind(w.runtime.family), imageId: w.runtime.imageId, phpVersion: (w.runtime.phpVersion ?? undefined) as Website["runtime"]["phpVersion"],
      workerMode: w.runtime.workerMode, extensions: w.runtime.extensions ?? [], phpIni: w.runtime.phpIni ?? {},
      previous: w.runtime.previousImageId ? { imageId: w.runtime.previousImageId, tag: "" } : undefined,
      documentRoot: w.runtime.documentRoot ?? "public",
    },
    use: { cpuPercent: 0, memoryBytes: 0, diskBytes: 0, processes: 0, diskIoBytesPerSecond: 0, ...(w.usage ?? {}) },
    domains: (w.domains ?? []).map((d) => ({
      name: d.name, kind: d.kind, dnsPointsHere: d.dnsPointsHere, certificate: { state: d.certificate.state, expiresAt: d.certificate.expiresAt ?? undefined },
      ...(d.verified === false && d.verification ? { verification: d.verification } : {}),
    })),
    releases: [], previews: [], cron: [], sshKeys: [], shellEnabled: false,
    sftp: w.sftp ? { ...w.sftp, host: w.sftp.host ?? location.host.replace(/:\d+$/, ""), authorizedKeys: w.sftp.authorizedKeys ?? [] } : undefined,
    lastChange: { at: w.updatedAt, summary: "Last change" },
    stagingOf: w.stagingOf ?? undefined, managedByTerraform: w.managedByTerraform, imageTag: tagOfReference(w.runtime.image) || undefined, imageReference: w.runtime.image || undefined,
  };
}

export function toServer(s: ApiServer): Server {
  return {
    id: s.id, name: s.name, location: s.location ?? "", roles: s.roles, agentVersion: s.agentVersion ?? "", state: s.state,
    cpuPercent: s.usage?.cpuPercent ?? 0, memoryPercent: s.usage?.memoryPercent ?? 0, diskPercent: s.usage?.diskPercent ?? 0,
    os: s.os ?? "", kernel: s.kernel ?? "", arch: s.arch === "aarch64" || s.arch === "arm64" ? "arm64" : "x86-64",
    podmanVersion: s.podmanVersion ?? "", gvisorVersion: s.gvisorVersion ?? "", gvisorPlatform: s.gvisorPlatform, bareMetal: s.bareMetal,
    updatePolicy: s.updatePolicy, maintenanceWindow: s.maintenanceWindow, pendingUpdates: s.pendingUpdates ?? [], lastUpdateAt: s.lastUpdateAt ?? "",
    rebootRequired: s.rebootRequired, services: s.services ?? [], sftpPort: s.sftpPort ?? 22,
    // A panel of an earlier release does not send the field. Then the state tells the same thing.
    agentUpdateAvailable: s.agentUpdateAvailable ?? s.state === "agent_update",
    agentAddress: s.agentAddress ?? undefined, connectionError: s.connectionError ?? undefined, panel: s.panel ?? false,
    databaseEngines: s.databaseEngines ?? undefined,
    stateDetail: s.state === "degraded" ? (s.connectionError ? "Degraded: the panel cannot connect" : degradedReason(s.services ?? [])) : undefined,
  };
}

export function toImage(i: ApiImage, rollouts: ApiRollout[]): RuntimeImage {
  const rolling = rollouts.some((r) => r.imageId === i.id && (r.state === "running" || r.state === "paused"));
  return {
    id: i.id, name: i.name, family: i.family, reference: i.reference, running: i.tag, available: i.availableTag ?? undefined,
    availableNote: i.availableTag && rolling ? "rolling_out" : undefined, phpVersion: i.phpVersion ?? undefined, supportEndsAt: i.supportEndsAt ?? undefined,
    websites: String(i.websites ?? 0), policy: i.policy, checkError: i.checkError ?? undefined,
    custom: i.custom, signatureVerified: i.signatureVerified, digest: i.digest ?? "", containerPort: i.containerPort, command: i.command ?? undefined,
  };
}

export function toRuntimeImage(i: ApiRuntimeImage): RuntimeImage {
  return {
    id: i.id, name: i.name, family: i.family, reference: "", running: i.tag, phpVersion: i.phpVersion ?? undefined, supportEndsAt: i.supportEndsAt ?? undefined,
    websites: "", policy: "manual", custom: i.custom, signatureVerified: !i.custom, digest: "",
  };
}

export function toRollout(r: ApiRollout, servers: number): Rollout {
  return {
    id: r.id, imageId: r.imageId, title: r.title, fromTag: r.fromTag, toTag: r.toTag, severity: r.severity, state: r.state, startedAt: r.startedAt, finishedAt: r.finishedAt ?? undefined,
    window: "", websites: r.websites, servers, stages: r.stages, healthy: r.healthy, rolledBack: r.rolledBack, rollbacks: r.rollbacks,
    checks: [
      { label: "Signature verified", passed: true },
      { label: "Health checks pass", passed: r.rolledBack === 0 ? (r.state === "done" ? true : null) : false },
    ],
  };
}

const toCustomer = (c: ApiCustomer): Customer => ({ id: c.id, name: c.name, email: c.email, planId: c.planId, role: c.role, resellerId: c.resellerId ?? undefined, createdAt: c.createdAt, suspended: c.suspended });

export const toUser = (u: ApiUser): ManagedUser => ({
  id: String(u.id), name: u.name, email: u.email, role: u.role, customerId: u.customerId ?? undefined, totpEnabled: u.totpEnabled, mustChangePassword: u.mustChangePassword, createdAt: u.createdAt ?? "",
});

const toToken = (t: ApiToken_): ApiToken => ({
  id: String(t.id), name: t.name, scopes: t.scopes as ApiToken["scopes"], createdAt: t.createdAt, expiresAt: t.expiresAt ?? "", lastUsedAt: t.lastUsedAt ?? undefined, prefix: `${t.id}|`,
});

const toAudit = (a: ApiAudit): AuditEntry => ({ id: String(a.id), at: a.at, actor: a.actor, ip: a.ip ?? "", action: a.action, target: a.target });

// Loading

/** Loads the full state for the signed-in user from the API. */
export async function load(previous: State): Promise<State> {
  const me = await request<ApiMe>("GET", "/me");
  const user: User = { id: String(me.id), name: me.name, email: me.email, role: me.role };
  // Audit finding B5: with a temporary password, the API refuses each other request. The
  // panel shows only the form for a new password.
  if (me.mustChangePassword) {
    return { ...previous, session: { signedIn: true, mustChangePassword: true, user, contextId: previous.session.contextId }, totpEnabled: me.totpEnabled };
  }
  const admin = me.role === "administrator";
  const optional = <T>(p: Promise<T>, fallback: T) => p.catch(() => fallback);

  const [websites, jobs, customers, plans, tokens, policy, servers, images, rollouts, audit, users, certificate] = await Promise.all([
    all<ApiWebsite>("/websites"),
    request<{ data: ApiJob[] }>("GET", "/jobs?perPage=50").then((r) => r.data),
    optional(request<{ data: ApiCustomer[] }>("GET", "/customers").then((r) => r.data), []),
    optional(request<{ data: Plan[] }>("GET", "/plans").then((r) => r.data), []),
    optional(request<{ data: ApiToken_[] }>("GET", "/tokens").then((r) => r.data), []),
    optional(request<BackupPolicy>("GET", "/backup-policy"), previous.backupPolicy),
    admin ? request<{ data: ApiServer[] }>("GET", "/servers").then((r) => r.data) : Promise.resolve([] as ApiServer[]),
    admin ? request<{ data: ApiImage[] }>("GET", "/images").then((r) => r.data) : Promise.resolve([] as ApiImage[]),
    admin ? request<{ data: ApiRollout[] }>("GET", "/rollouts").then((r) => r.data) : Promise.resolve([] as ApiRollout[]),
    optional(request<{ data: ApiAudit[] }>("GET", "/audit?perPage=100").then((r) => r.data), []),
    optional(request<{ data: ApiUser[] }>("GET", "/users").then((r) => r.data), []),
    admin ? optional(request<ApiPanelCertificate>("GET", "/panel-certificate"), null) : Promise.resolve(null),
  ]);
  // A customer or a reseller cannot list all images. They get the images for a new website.
  const runtimes = admin ? images.map((i) => toImage(i, rollouts))
    : (await optional(request<{ data: ApiRuntimeImage[] }>("GET", "/runtime-images").then((r) => r.data), [])).map(toRuntimeImage);

  // Databases and snapshots belong to each website.
  const all_ = websites.length <= DETAIL_LIMIT;
  const perSite = await Promise.all(websites.filter((w) => all_ || shown.has(w.id)).map((w) => websiteDetails(w)));
  // Keep the data of the other websites from the previous load.
  const fresh = new Set(all_ ? websites.map((w) => w.id) : [...shown]);
  const exists = new Set(websites.map((w) => w.id));
  const kept = <T extends { websiteId: string }>(rows: T[]) => all_ ? [] : rows.filter((r) => exists.has(r.websiteId) && !fresh.has(r.websiteId));

  const own = customers.find((c) => c.id === me.customerId);
  const contexts: State["contexts"] = admin
    ? [{ id: "platform", name: "All customers", role: "administrator" }, ...customers.filter((c) => c.role === "reseller").map((c) => ({ id: c.id, name: c.name, role: "reseller" as const }))]
    : own ? [{ id: own.id, name: own.name, role: own.role }] : [];
  const contextIds = new Set(contexts.map((c) => c.id));
  const contextId = contextIds.has(previous.session.contextId) ? previous.session.contextId : contexts[0]?.id ?? "platform";

  const mappedJobs = jobs.map(toJob);
  const failed = mappedJobs.filter((j) => j.state === "failed").slice(0, 10);
  const done = mappedJobs.filter((j) => j.state === "succeeded").slice(0, 10);
  const current = rollouts.find((r) => r.state === "running" || r.state === "paused") ?? rollouts[0];

  return {
    ...previous,
    session: { signedIn: true, user, contextId },
    contexts,
    websites: websites.map(toWebsite),
    jobs: mappedJobs,
    activity: done.map((j) => ({ id: `act_${j.id}`, text: j.title, ref: j.ref, at: j.finishedAt ?? j.createdAt })),
    notifications: failed.map((j) => {
      const known = previous.notifications.find((n) => n.id === `ntf_${j.id}`);
      return { id: `ntf_${j.id}`, tone: "danger" as const, text: `${j.title} failed${j.error ? `: ${j.error}` : "."}`, at: j.finishedAt ?? j.createdAt, read: known?.read ?? false, href: j.websiteId ? `/websites/${j.websiteId}` : "/jobs" };
    }),
    servers: servers.map(toServer),
    images: runtimes,
    rollout: current ? toRollout(current, servers.length) : undefined,
    zones: [], mailboxes: [], aliases: [], files: {},
    databases: [...kept(previous.databases), ...perSite.flatMap((s) => s.databases)],
    databaseUsers: [...kept(previous.databaseUsers), ...perSite.flatMap((s) => s.users)],
    snapshots: [...kept(previous.snapshots), ...perSite.flatMap((s) => s.snapshots)].sort((a, b) => b.at.localeCompare(a.at)),
    backupPolicy: policy,
    plans,
    customers: customers.map(toCustomer),
    users: users.map(toUser),
    tokens: tokens.map(toToken),
    passkeys: [],
    totpEnabled: me.totpEnabled,
    audit: audit.map(toAudit),
    alertChannels: [],
    branding: { ...previous.branding, panelDomain: location.host },
    system: { ...previous.system, panelDomain: location.host },
    panelCertificate: certificate ? toPanelCertificate(certificate) : previous.panelCertificate,
  };
}

/** Reads the databases, database users and snapshots of one website. */
async function websiteDetails(w: { id: string; serverId: string }) {
  const optional = <T>(p: Promise<T>, fallback: T) => p.catch(() => fallback);
  const [db, snaps] = await Promise.all([
    optional(request<{ databases: { id: string; engine: Database["engine"]; name: string; sizeBytes: number; number?: number | null; keys?: number | null }[]; users: { id: string; engine: Database["engine"]; name: string; grants: DatabaseGrant[]; access: DatabaseUser["access"] }[] }>("GET", `/websites/${w.id}/databases`), { databases: [], users: [] }),
    optional(request<{ data: ApiSnapshot[] }>("GET", `/websites/${w.id}/snapshots`).then((r) => r.data), []),
  ]);
  return {
    databases: db.databases.map((d): Database => ({ id: d.id, name: d.name, engine: d.engine, websiteId: w.id, sizeBytes: d.sizeBytes ?? 0, serverId: w.serverId, number: d.number ?? undefined, keys: d.keys ?? undefined })),
    users: db.users.map((u): DatabaseUser => ({ id: u.id, name: u.name, engine: u.engine, websiteId: w.id, grants: u.grants ?? [], access: u.access })),
    snapshots: snaps.map((s): Snapshot => ({ id: s.id, at: s.createdAt, kind: s.kind === "manual" ? "manual" : "nightly", sizeBytes: s.sizeBytes ?? 0, websiteId: w.id, databases: s.databases ?? [], mailboxes: s.mailboxes ?? [] })),
  };
}

/**
 * The user opened a website. With many websites, the panel now also reads the databases and
 * snapshots of this website at each load. The function gives the new state, or null when the
 * last load already read them.
 */
export async function showWebsite(previous: State, websiteId: string): Promise<((s: State) => State) | null> {
  const w = previous.websites.find((x) => x.id === websiteId);
  if (!w || previous.websites.length <= DETAIL_LIMIT || shown.has(websiteId)) return null;
  shown.add(websiteId);
  const d = await websiteDetails(w);
  return (s) => ({
    ...s,
    databases: [...s.databases.filter((x) => x.websiteId !== websiteId), ...d.databases],
    databaseUsers: [...s.databaseUsers.filter((x) => x.websiteId !== websiteId), ...d.users],
    snapshots: [...s.snapshots.filter((x) => x.websiteId !== websiteId), ...d.snapshots].sort((a, b) => b.at.localeCompare(a.at)),
  });
}

// Session

export async function signIn(email: string, password: string, totp: string): Promise<void> {
  const res = await request<{ token: string }>("POST", "/auth/login", { email, password, totp: totp || undefined });
  token = res.token;
  writeToken(token);
}

/**
 * Audit finding S8: the API deletes the session token, so a copy of the token does not work
 * after the sign-out. The panel does not wait for the answer.
 */
export function signOut() {
  if (token) {
    void fetch(`${BASE}/auth/logout`, { method: "POST", headers: { Accept: "application/json", Authorization: `Bearer ${token}` }, keepalive: true }).catch(() => {});
  }
  token = null;
  writeToken(null);
}

// Jobs

/**
 * Waits until the agent finishes the job. It reads only the job from the API, each 1.5 seconds
 * for the first minute and then each 5 seconds. Batch 1, finding 9: it does not load all data
 * again while it waits.
 */
export async function waitForJob(job: ApiJob | Job, onChange: (job: Job) => void): Promise<Job> {
  let current = toJob(job as ApiJob);
  const started = Date.now();
  // A job can run for up to 3 hours (CheckJobsAndServers). Then the API marks it failed.
  while ((current.state === "queued" || current.state === "running") && Date.now() - started < 3.5 * 3600_000) {
    await new Promise((r) => setTimeout(r, Date.now() - started < 60_000 ? 1500 : 5000));
    try {
      current = toJob((await request<{ data: ApiJob }>("GET", `/jobs/${current.id}`)).data);
    } catch (e) {
      // A short loss of the connection does not stop the wait.
      if (e instanceof LiveError && e.status !== 0) throw e;
      continue;
    }
    onChange(current);
  }
  return current;
}

export type { ApiJob, ApiPanelCertificate, ApiUser, ApiWebsite };
