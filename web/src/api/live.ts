// The client for the panel API (openapi/openapi.yaml). store.ts calls these functions. Each function sends one request, and then
// loads the changed data again. A change that makes a job gives a promise that resolves when
// the agent has finished the job.

import type {
  Account,
  ApiToken,
  AuditEntry,
  BackupPolicy,
  Brand,
  BrandStatus,
  CertificateOrderStage,
  CertificateSource,
  Customer,
  CustomerCertificate,
  Database,
  DatabaseGrant,
  DatabaseServer,
  MailAlias,
  MailDomain,
  Mailbox,
  DatabaseUser,
  DnsRecord,
  DnsAccount,
  DnsProvider,
  DnsZone,
  DnsZoneProvider,
  DomainVerification,
  Job,
  Limits,
  ManagedUser,
  Mesh,
  PanelCertificate,
  Plan,
  PluginAction,
  PluginPage,
  PrivateNetwork,
  ResellerBranding,
  Rollout,
  RuntimeImage,
  RuntimeKind,
  Server,
  SetupChecklist,
  Snapshot,
  State,
  UsageAlert,
  UsageAlertSettings,
  User,
  Website,
  MailCluster,
} from "./types";
import { jobFailure, openFailures } from "../lib/jobFailure";
import { degradedReason } from "./selectors";

/** The error of a request. store.ts makes an ApiError from it. */
export class LiveError extends Error {
  constructor(message: string, readonly fields: Record<string, string> = {}, readonly status = 0, /** The answer of the API. Some answers have more fields than the errors. */ readonly body?: unknown) {
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
  return send<T>(method, path, body === undefined ? undefined : JSON.stringify(body), body === undefined ? undefined : "application/json");
}

/** Sends bytes, for example a part of the archive of an import (application/octet-stream). */
export async function sendBytes<T>(method: string, path: string, bytes: Blob): Promise<T> {
  return send<T>(method, path, bytes, "application/octet-stream");
}

async function send<T>(method: string, path: string, body: BodyInit | undefined, contentType: string | undefined): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (contentType) headers["Content-Type"] = contentType;
  if (token) headers.Authorization = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(BASE + path, { method, headers, body });
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
  throw new LiveError(message, fields, res.status, data);
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

interface ApiMe {
  id: number; name: string; email: string; role: User["role"]; customerId: string | null; totpEnabled: boolean; mustChangePassword?: boolean;
  totpSetupRequired?: boolean; securityNotices?: { action: string; target: string; actor: string; at: string | null }[];
  brand?: Brand;
}
interface ApiUser { id: number; name: string; email: string; role: User["role"]; customerId: string | null; totpEnabled: boolean; mustChangePassword: boolean; createdAt: string | null }
interface ApiJob { id: string; title: string; ref: string | null; action?: string | null; state: Job["state"]; steps: string[]; step: number; error: string | null; result?: Job["result"] | null; websiteId: string | null; serverId: string | null; databaseServerId?: string | null; dnsZoneId?: string | null; createdAt: string; finishedAt: string | null }
interface ApiWebsite {
  id: string; name: string; application: string; customerId: string; serverId: string; planId: string; unixUser: string; networkHost?: string | null; networkPort?: number | null; kind?: Website["kind"]; command?: string | null; installCommand?: string | null; environment?: string[]; status: Website["status"];
  runtime: { imageId: string; family: string; image: string; phpVersion: string | null; version?: string | null; workerMode: boolean; extensions: string[]; phpIni: Record<string, string>; previousImageId: string | null; documentRoot?: string; digest?: string | null; signatureVerified?: boolean };
  sftp?: { enabled: boolean; host: string | null; port: number; username: string; authorizedKeys: string[] };
  limits: Limits; diskLimitApplied?: boolean | null; usage: Partial<Website["use"]> | null; bandwidthBytes?: number;
  domains?: { name: string; kind: Website["domains"][number]["kind"]; dnsPointsHere: boolean; certificate: { state: Website["domains"][number]["certificate"]["state"]; expiresAt: string | null; source?: CertificateSource; certificateId?: string | null }; verified?: boolean; verification?: DomainVerification | null }[];
  stagingOf: string | null; managedByTerraform: boolean; createdAt: string; updatedAt: string;
}
interface ApiServer {
  id: string; name: string; location: string; roles: Server["roles"]; state: Server["state"]; agentVersion: string | null; os: string | null; kernel: string | null; arch: string | null;
  podmanVersion: string | null; gvisorVersion: string | null; gvisorPlatform: Server["gvisorPlatform"]; bareMetal: boolean; updatePolicy: Server["updatePolicy"]; maintenanceWindow: string;
  pendingUpdates: Server["pendingUpdates"]; lastUpdateAt: string | null; rebootRequired: boolean; agentUpdateAvailable?: boolean; agentAddress?: string | null; connectionError?: string | null; panel?: boolean; databaseEngines?: Database["engine"][]; databaseInstances?: boolean; services: Server["services"]; sftpPort?: number | null; dnsName?: string | null; dns?: boolean; addresses?: string[]; mail?: Server["mail"] | null;
  usage: { cpuPercent?: number; memoryPercent?: number; diskPercent?: number } | null;
}
interface ApiImage {
  id: string; name: string; family: RuntimeImage["family"]; reference: string; tag: string; digest: string | null; availableTag: string | null; phpVersion: RuntimeImage["phpVersion"] | null; version?: string | null;
  supportEndsAt: string | null; policy: RuntimeImage["policy"]; custom: boolean; signatureVerified: boolean; websites: number | null; checkedAt?: string | null; checkError?: string | null;
  containerPort?: number; command?: string[] | null; pack?: RuntimeImage["pack"] | null; defaults?: RuntimeImage["defaults"] | null;
}
interface ApiRuntimeImage {
  id: string; name: string; family: RuntimeImage["family"]; tag: string; phpVersion?: RuntimeImage["phpVersion"] | null; version?: string | null; custom: boolean; supportEndsAt: string | null;
  defaults?: RuntimeImage["defaults"] | null; pack?: RuntimeImage["pack"] | null;
}
interface ApiRollout {
  id: string; imageId: string; title: string; fromTag: string; toTag: string; severity: Rollout["severity"]; state: Rollout["state"]; stages: Rollout["stages"];
  websites: number; healthy: number; rolledBack: number; running: number; rollbacks: Rollout["rollbacks"]; startedAt: string; finishedAt?: string | null;
}
interface ApiCustomer { id: string; name: string; email: string; role: Customer["role"]; planId: string; resellerId: string | null; suspended: boolean; createdAt: string; branding?: ResellerBranding | [] | null; brandStatus?: BrandStatus | null }
interface ApiToken_ { id: number; name: string; scopes: string[]; createdAt: string; expiresAt: string | null; lastUsedAt: string | null }
interface ApiAudit { id: number; at: string; actor: string; ip: string | null; action: string; target: string }
interface ApiPanelCertificate {
  domain: string; method: PanelCertificate["method"]; state: PanelCertificate["state"]; expiresAt: string | null; caCertificate: string | null; caSha256: string | null;
  reportedAt: string | null; letsEncryptAvailable: boolean; serversWithoutCa: string[]; job: ApiJob | null;
}
interface ApiDatabaseServer extends Omit<DatabaseServer, "usage" | "createdAt"> { usage: DatabaseServer["usage"] | null; createdAt: string | null }
interface ApiDnsRecord extends Omit<DnsRecord, "priority" | "managed"> { priority: number | null; managed: DnsRecord["managed"] | null }
interface ApiDnsZoneProvider extends Omit<DnsZoneProvider, "error" | "syncedAt" | "changes" | "imported" | "handover"> {
  error: string | null; syncedAt: string | null; changes: DnsZoneProvider["changes"] | null; imported: DnsZoneProvider["imported"] | null; handover: { endsAt: string | null } | null;
}
interface ApiDnsZone extends Omit<DnsZone, "delegation" | "dnssec" | "records" | "createdAt" | "provider"> {
  delegation: { state: DnsZone["delegation"]["state"]; nameServers: string[]; checkedAt: string | null };
  // A panel API of an earlier release has no state and no provider.
  dnssec: { enabled: boolean; state?: DnsZone["dnssec"]["state"]; ds: DnsZone["dnssec"]["ds"] | null; dnskey: string | null };
  provider?: ApiDnsZoneProvider | null;
  createdAt: string | null;
  records?: ApiDnsRecord[];
}
interface ApiSnapshot { id: string; kind: "scheduled" | "manual"; sizeBytes: number; databases: string[]; part?: "website" | "databases"; serverId?: string; createdAt: string }

// Mapping from the API shapes to the state of the UI.

// A family that the panel does not have built in is a runtime from a manifest of the agent. The
// family of an own image is "custom".
const BUILT_IN_FAMILIES = ["frankenphp", "apache-php-fpm", "node", "python", "custom", "static", "edge", "mail", "dns", "database"];
const runtimeKind = (family: string): RuntimeKind =>
  family === "frankenphp" || family === "apache-php-fpm" || family === "node" || family === "python" ? family
    : family === "static" || family === "edge" ? "static" : BUILT_IN_FAMILIES.includes(family) ? "oci" : "app";

const tagOfReference = (ref: string) => {
  const at = ref.lastIndexOf(":");
  return at > ref.lastIndexOf("/") ? ref.slice(at + 1) : "";
};

export function toJob(j: ApiJob): Job {
  return {
    id: j.id, title: j.title, ref: j.ref ?? undefined, action: j.action ?? undefined, state: j.state, steps: j.steps ?? [], step: j.step ?? 0, error: j.error ?? undefined,
    createdAt: j.createdAt, finishedAt: j.finishedAt ?? undefined, websiteId: j.websiteId ?? undefined, serverId: j.serverId ?? undefined,
    databaseServerId: j.databaseServerId ?? undefined, dnsZoneId: j.dnsZoneId ?? undefined, result: j.result ?? undefined,
  };
}

interface ApiAccount {
  customer: Account["customer"] | null;
  provider: { name: string; email: string } | null;
  plan: Plan | null;
  usage: { websites: number; databases: number; diskBytes: number; customers: number | null; dnsZones?: number; networks?: number; bandwidthBytes?: number } | null;
  backups?: { available: boolean };
  users: ApiUser[];
}

export function toAccount(a: ApiAccount): Account {
  const usage = a.usage ?? { websites: 0, databases: 0, diskBytes: 0, customers: null };
  return {
    customer: a.customer!, provider: a.provider ?? undefined, plan: a.plan ?? undefined,
    usage: { websites: usage.websites, databases: usage.databases, diskBytes: usage.diskBytes, customers: usage.customers ?? undefined, dnsZones: usage.dnsZones ?? 0, networks: usage.networks ?? 0, bandwidthBytes: usage.bandwidthBytes ?? 0 },
    backupsAvailable: a.backups?.available ?? true,
    users: a.users.map(toUser),
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
    // An API from before the networks sends no name. The name comes from the Unix user.
    networkHost: w.kind === "worker" ? null : w.networkHost ?? `${w.unixUser.replaceAll("_", "-")}.internal`, networkPort: w.kind === "worker" ? null : w.networkPort ?? 80,
    kind: w.kind ?? "public", command: w.command ?? null, installCommand: w.installCommand ?? null, environment: w.environment ?? [],
    createdAt: w.createdAt, status: w.status, planId: w.planId, limits: w.limits, diskLimitApplied: w.diskLimitApplied ?? undefined,
    runtime: {
      kind: runtimeKind(w.runtime.family), imageId: w.runtime.imageId, phpVersion: (w.runtime.phpVersion ?? undefined) as Website["runtime"]["phpVersion"],
      version: w.runtime.version ?? undefined,
      workerMode: w.runtime.workerMode, extensions: w.runtime.extensions ?? [], phpIni: w.runtime.phpIni ?? {},
      previous: w.runtime.previousImageId ? { imageId: w.runtime.previousImageId, tag: "" } : undefined,
      documentRoot: w.runtime.documentRoot ?? (w.runtime.family === "node" || w.runtime.family === "python" || runtimeKind(w.runtime.family) === "app" ? "." : "public"),
      digest: w.runtime.digest ?? undefined, signatureVerified: w.runtime.signatureVerified ?? undefined,
    },
    use: { cpuPercent: 0, memoryBytes: 0, diskBytes: 0, processes: 0, diskIoBytesPerSecond: 0, ...(w.usage ?? {}) },
    bandwidthBytes: w.bandwidthBytes ?? 0,
    domains: (w.domains ?? []).map((d) => ({
      name: d.name, kind: d.kind, dnsPointsHere: d.dnsPointsHere,
      certificate: { state: d.certificate.state, expiresAt: d.certificate.expiresAt ?? undefined, source: d.certificate.source ?? "automatic", certificateId: d.certificate.certificateId ?? undefined },
      ...(d.verified === false && d.verification ? { verification: d.verification } : {}),
    })),
    sftp: w.sftp ? { ...w.sftp, host: w.sftp.host ?? location.host.replace(/:\d+$/, ""), authorizedKeys: w.sftp.authorizedKeys ?? [] } : undefined,
    lastChange: { at: w.updatedAt, summary: "" },
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
    databaseInstances: s.databaseInstances ?? false,
    dnsName: s.dnsName ?? undefined, dns: s.dns ?? false, addresses: s.addresses ?? [],
    mail: s.mail ?? undefined,
    stateDetail: s.state === "degraded" ? (s.connectionError ? "Degraded: the panel cannot connect" : degradedReason(s.services ?? [])) : undefined,
  };
}

export function toImage(i: ApiImage, rollouts: ApiRollout[]): RuntimeImage {
  const rolling = rollouts.some((r) => r.imageId === i.id && (r.state === "running" || r.state === "paused"));
  return {
    id: i.id, name: i.name, family: i.family, reference: i.reference, running: i.tag, available: i.availableTag ?? undefined,
    availableNote: i.availableTag && rolling ? "rolling_out" : undefined, phpVersion: i.phpVersion ?? undefined, version: i.version ?? undefined, supportEndsAt: i.supportEndsAt ?? undefined,
    websites: String(i.websites ?? 0), policy: i.policy, checkError: i.checkError ?? undefined,
    custom: i.custom, signatureVerified: i.signatureVerified, digest: i.digest ?? "", containerPort: i.containerPort, command: i.command ?? undefined,
    defaults: i.defaults ?? undefined, pack: i.pack ?? undefined,
  };
}

export function toRuntimeImage(i: ApiRuntimeImage): RuntimeImage {
  return {
    id: i.id, name: i.name, family: i.family, reference: "", running: i.tag, phpVersion: i.phpVersion ?? undefined, version: i.version ?? undefined,
    defaults: i.defaults ?? undefined, supportEndsAt: i.supportEndsAt ?? undefined,
    websites: "", policy: "manual", custom: i.custom, signatureVerified: !i.custom, digest: "", pack: i.pack ?? undefined,
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

interface ApiMailDomain extends Omit<MailDomain, "dnsCheckedAt"> { dnsCheckedAt: string | null }
interface ApiMailbox extends Omit<Mailbox, "usedBytes"> { usedBytes: number | null }
export const toMailDomain = (d: ApiMailDomain): MailDomain => ({ ...d, records: d.records ?? [], dnsCheckedAt: d.dnsCheckedAt ?? undefined });
export const toMailbox = (m: ApiMailbox): Mailbox => ({ ...m, usedBytes: m.usedBytes ?? 0 });
export const toDatabaseServer = (s: ApiDatabaseServer): DatabaseServer => ({ ...s, usage: s.usage ?? undefined, createdAt: s.createdAt ?? "" });

export const toDnsRecord = (r: ApiDnsRecord): DnsRecord => ({ id: r.id, name: r.name, type: r.type, value: r.value, ttl: r.ttl, priority: r.priority ?? undefined, managed: r.managed ?? undefined });

export interface ApiNetwork extends PrivateNetwork { createdAt?: string | null }

export const toNetwork = (n: ApiNetwork): PrivateNetwork => ({ id: n.id, customerId: n.customerId, name: n.name, isDefault: n.isDefault, websiteIds: n.websiteIds ?? [], databaseServerIds: n.databaseServerIds ?? [] });

interface ApiDnsAccount extends Omit<DnsAccount, "customerId" | "account" | "checkedAt" | "createdAt" | "credentials"> {
  customerId: string | null; account: string | null; checkedAt: string | null; createdAt: string | null;
  // PHP gives an empty object as an empty list.
  credentials: Record<string, string | null> | [];
}

export const toDnsAccount = (a: ApiDnsAccount): DnsAccount => ({
  ...a,
  customerId: a.customerId ?? undefined,
  account: a.account ?? undefined,
  checkedAt: a.checkedAt ?? undefined,
  createdAt: a.createdAt ?? "",
  credentials: Array.isArray(a.credentials) ? {} : a.credentials,
});

const toDnsZoneProvider = (p: ApiDnsZoneProvider): DnsZoneProvider => ({
  ...p,
  error: p.error ?? undefined,
  syncedAt: p.syncedAt ?? undefined,
  changes: p.changes ?? undefined,
  imported: p.imported ?? undefined,
  handover: p.handover ? { endsAt: p.handover.endsAt ?? undefined } : undefined,
});

export const toDnsZone = (z: ApiDnsZone): DnsZone => ({
  ...z,
  delegation: { state: z.delegation.state, nameServers: z.delegation.nameServers ?? [], checkedAt: z.delegation.checkedAt ?? undefined },
  dnssec: { enabled: z.dnssec.enabled, state: z.dnssec.state ?? (z.dnssec.enabled ? "active" : "off"), ds: z.dnssec.ds ?? undefined, dnskey: z.dnssec.dnskey ?? undefined },
  provider: z.provider ? toDnsZoneProvider(z.provider) : undefined,
  createdAt: z.createdAt ?? "",
  records: z.records?.map(toDnsRecord),
});

/** The zones that the user opened. Each load reads their records again. */
const shownZones = new Set<string>();

interface ApiCertificate {
  id: string; customerId: string; source: CustomerCertificate["source"]; names: string[]; state: CustomerCertificate["state"];
  issuer: string | null; fingerprint: string | null; notBefore: string | null; expiresAt: string | null; trusted: boolean | null; renewAt: string | null;
  order: { stage: CertificateOrderStage; startedAt: string | null; records: { name: string; value: string }[] } | null;
  error: string | null; nextTryAt: string | null;
  usedBy: { domain: string; websiteId: string | null; websiteName: string | null; serverName: string }[];
  certificatePem?: string | null;
  createdAt: string;
}

export const toCertificate = (c: ApiCertificate): CustomerCertificate => ({
  id: c.id, customerId: c.customerId, source: c.source, names: c.names, state: c.state,
  issuer: c.issuer ?? undefined, fingerprint: c.fingerprint ?? undefined, notBefore: c.notBefore ?? undefined, expiresAt: c.expiresAt ?? undefined,
  trusted: c.trusted ?? undefined, renewAt: c.renewAt ?? undefined,
  order: c.order ? { stage: c.order.stage, startedAt: c.order.startedAt ?? undefined, records: c.order.records ?? [] } : undefined,
  error: c.error ?? undefined, nextTryAt: c.nextTryAt ?? undefined,
  usedBy: (c.usedBy ?? []).map((u) => ({ domain: u.domain, websiteId: u.websiteId ?? undefined, websiteName: u.websiteName ?? undefined, serverName: u.serverName })),
  certificatePem: c.certificatePem ?? undefined,
  createdAt: c.createdAt,
});

/** Reads one certificate with its chain. */
export async function certificate(id: string): Promise<CustomerCertificate> {
  return toCertificate(await request<ApiCertificate>("GET", `/certificates/${encodeURIComponent(id)}`));
}

/** Reads one zone with its records. The next loads also read the records of the zone. */
export async function dnsZone(id: string): Promise<DnsZone> {
  shownZones.add(id);
  return toDnsZone(await request<ApiDnsZone>("GET", `/dns-zones/${encodeURIComponent(id)}`));
}

const toCustomer = (c: ApiCustomer): Customer => ({
  id: c.id, name: c.name, email: c.email, planId: c.planId, role: c.role, resellerId: c.resellerId ?? undefined, createdAt: c.createdAt, suspended: c.suspended,
  // PHP gives an empty brand as an empty list.
  branding: c.branding && !Array.isArray(c.branding) ? c.branding : undefined,
  brandStatus: c.brandStatus ?? undefined,
});

export const toUser = (u: ApiUser): ManagedUser => ({
  id: String(u.id), name: u.name, email: u.email, role: u.role, customerId: u.customerId ?? undefined, totpEnabled: u.totpEnabled, mustChangePassword: u.mustChangePassword, createdAt: u.createdAt ?? "",
});

const toToken = (t: ApiToken_): ApiToken => ({
  id: String(t.id), name: t.name, scopes: t.scopes as ApiToken["scopes"], createdAt: t.createdAt, expiresAt: t.expiresAt ?? "", lastUsedAt: t.lastUsedAt ?? undefined, prefix: `${t.id}|`,
});

const toAudit = (a: ApiAudit): AuditEntry => ({ id: String(a.id), at: a.at, actor: a.actor, ip: a.ip ?? "", action: a.action, target: a.target });

// Loading

/** White label: the brand of the sign-in page, for the domain that the browser opened. */
export async function signInBrand(): Promise<Brand> {
  return request<Brand>("GET", "/brand");
}

/** Loads the full state for the signed-in user from the API. */
export async function load(previous: State): Promise<State> {
  const me = await request<ApiMe>("GET", "/me");
  const user: User = { id: String(me.id), name: me.name, email: me.email, role: me.role };
  // Audit finding B5: with a temporary password, the API refuses each other request. The
  // panel shows only the form for a new password.
  if (me.mustChangePassword) {
    return { ...previous, brand: me.brand ?? previous.brand, session: { signedIn: true, mustChangePassword: true, user, contextId: previous.session.contextId }, totpEnabled: me.totpEnabled };
  }
  // Second audit, finding F09: an administrator or a reseller must set up TOTP first. The API
  // refuses each other request until then.
  if (me.totpSetupRequired) {
    return { ...previous, brand: me.brand ?? previous.brand, session: { signedIn: true, totpSetupRequired: true, user, contextId: previous.session.contextId }, totpEnabled: false };
  }
  const admin = me.role === "administrator";
  const optional = <T>(p: Promise<T>, fallback: T) => p.catch(() => fallback);

  const [websites, jobs, customers, plans, tokens, policy, servers, images, rollouts, audit, users, certificate, setup, account, databaseServers, dns, dnsAccounts, mesh, networks, alerts, mailDomains, mailboxes, mailAliases, mailClusters, pluginPages, pluginActions, certificates] = await Promise.all([
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
    admin ? optional(request<SetupChecklist>("GET", "/setup"), undefined) : Promise.resolve(undefined),
    admin ? Promise.resolve(null) : optional(request<ApiAccount>("GET", "/account"), null),
    // The database servers of the customers that the user can see.
    optional(request<{ data: ApiDatabaseServer[] }>("GET", "/database-servers").then((r) => r.data), []),
    // The DNS zones, and the name servers that a customer gives to the registrar of a domain.
    optional(request<{ data: ApiDnsZone[]; nameServers?: string[] }>("GET", "/dns-zones"), { data: [], nameServers: [] }),
    // The accounts at external DNS providers, and the providers of the plugins.
    optional(request<{ data: ApiDnsAccount[]; providers: DnsProvider[] }>("GET", "/dns-accounts"), { data: [], providers: [] }),
    // A panel API of an earlier release has no mesh.
    admin ? optional(request<Mesh>("GET", "/mesh"), undefined) : Promise.resolve(undefined),
    // A panel API of an earlier release has no private networks.
    optional(request<{ data: ApiNetwork[] }>("GET", "/networks").then((r) => r.data.map(toNetwork)), []),
    // A panel API of an earlier release has no usage alerts.
    optional(request<{ data: UsageAlert[]; settings: UsageAlertSettings }>("GET", "/usage-alerts"), { data: [], settings: previous.alertSettings }),
    // Email. A panel of an earlier release, or a token with no mail scope, gives no lists.
    optional(request<{ data: ApiMailDomain[] }>("GET", "/mail/domains").then((r) => r.data), []),
    optional(request<{ data: ApiMailbox[] }>("GET", "/mail/mailboxes").then((r) => r.data), []),
    optional(request<{ data: MailAlias[] }>("GET", "/mail/aliases").then((r) => r.data), []),
    admin ? optional(request<{ data: MailCluster[] }>("GET", "/mail-clusters").then((r) => r.data), []) : Promise.resolve([] as MailCluster[]),
    // The pages of the plugins for the menu. A panel API of an earlier release has no plugins.
    optional(request<{ data: PluginPage[] }>("GET", "/plugin-pages").then((r) => r.data), []),
    optional(request<{ data: PluginAction[] }>("GET", "/plugin-actions").then((r) => r.data), []),
    // The certificates of the customers. A token with no websites scope gives no list.
    optional(request<{ data: ApiCertificate[] }>("GET", "/certificates").then((r) => r.data), []),
  ]);
  // The list has no records. The panel reads the records of the zones that the user opened.
  const zoneDetails = new Map<string, DnsZone>();
  await Promise.all(dns.data.filter((z) => shownZones.has(z.id)).map(async (z) => {
    const detail = await optional(dnsZone(z.id), null);
    if (detail) zoneDetails.set(z.id, detail);
  }));
  // A customer or a reseller cannot list all images. They get the images for a new website.
  const runtimes = admin ? images.map((i) => toImage(i, rollouts))
    : (await optional(request<{ data: ApiRuntimeImage[] }>("GET", "/runtime-images").then((r) => r.data), [])).map(toRuntimeImage);

  // Databases and snapshots belong to each website.
  const all_ = websites.length <= DETAIL_LIMIT;
  const detailed = websites.filter((w) => all_ || shown.has(w.id));
  const perSite = await Promise.all(detailed.map((w) => websiteDetails(w)));
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
  // Audit item U2: a failure that a later job resolved needs no notification.
  const failed = openFailures(mappedJobs).slice(0, 10);
  const done = mappedJobs.filter((j) => j.state === "succeeded").slice(0, 10);
  const current = rollouts.find((r) => r.state === "running" || r.state === "paused") ?? rollouts[0];

  return {
    ...previous,
    session: { signedIn: true, user, contextId },
    brand: me.brand ?? previous.brand,
    contexts,
    websites: websites.map(toWebsite),
    jobs: mappedJobs,
    activity: done.map((j) => ({ id: `act_${j.id}`, text: j.title, ref: j.ref, at: j.finishedAt ?? j.createdAt })),
    notifications: failed.map((j) => {
      const known = previous.notifications.find((n) => n.id === `ntf_${j.id}`);
      return { id: `ntf_${j.id}`, tone: "danger" as const, text: `${j.title}${j.ref ? ` ${j.ref}` : ""} failed. ${jobFailure(j, me.role).text} Reference ${jobFailure(j, me.role).reference}.`, at: j.finishedAt ?? j.createdAt, read: known?.read ?? false, href: j.websiteId ? `/websites/${j.websiteId}` : j.dnsZoneId ? `/dns/${j.dnsZoneId}` : "/jobs" };
    }),
    servers: servers.map(toServer),
    images: runtimes,
    rollout: current ? toRollout(current, servers.length) : undefined,
    databaseServers: databaseServers.map(toDatabaseServer),
    dnsZones: dns.data.map((z) => zoneDetails.get(z.id) ?? { ...toDnsZone(z), records: previous.dnsZones.find((x) => x.id === z.id && x.serial === z.serial)?.records }),
    nameServers: dns.nameServers ?? [],
    dnsAccounts: dnsAccounts.data.map(toDnsAccount),
    dnsProviders: dnsAccounts.providers,
    // The chain stays from the page of the certificate, while the certificate is the same.
    certificates: certificates.map((c) => {
      const next = toCertificate(c);
      const before = previous.certificates.find((x) => x.id === c.id && x.fingerprint === next.fingerprint);
      return before?.certificatePem ? { ...next, certificatePem: before.certificatePem } : next;
    }),
    mesh,
    networks,
    pluginPages,
    pluginActions,
    usageAlerts: alerts.data,
    alertSettings: alerts.settings,
    mailDomains: mailDomains.map(toMailDomain),
    mailboxes: mailboxes.map(toMailbox),
    mailAliases,
    mailClusters,
    databases: [...kept(previous.databases), ...perSite.flatMap((s) => s.databases)],
    databaseUsers: [...kept(previous.databaseUsers), ...perSite.flatMap((s) => s.users)],
    remoteDatabaseServers: { ...previous.remoteDatabaseServers, ...Object.fromEntries(perSite.map((s, i) => [detailed[i].id, s.remote])) },
    snapshots: [...kept(previous.snapshots), ...perSite.flatMap((s) => s.snapshots)].sort((a, b) => b.at.localeCompare(a.at)),
    backupPolicy: policy,
    plans,
    customers: customers.map(toCustomer),
    users: users.map(toUser),
    tokens: tokens.map(toToken),
    totpEnabled: me.totpEnabled,
    securityNotices: (me.securityNotices ?? []).map((n) => ({ ...n, at: n.at ?? "" })),
    audit: audit.map(toAudit),
    panelDomain: location.host,
    panelCertificate: certificate ? toPanelCertificate(certificate) : previous.panelCertificate,
    setup,
    account: account?.customer ? toAccount(account) : undefined,
  };
}

/** Reads the databases, database users and snapshots of one website. */
async function websiteDetails(w: { id: string; serverId: string }) {
  const optional = <T>(p: Promise<T>, fallback: T) => p.catch(() => fallback);
  const [db, snaps] = await Promise.all([
    optional(request<{ databases: { id: string; engine: Database["engine"]; name: string; sizeBytes: number; number?: number | null; keys?: number | null; databaseServerId?: string | null; host?: string; port?: number }[]; users: { id: string; engine: Database["engine"]; name: string; grants: DatabaseGrant[]; access: DatabaseUser["access"]; databaseServerId?: string | null }[]; servers?: { id: string; remote?: boolean }[] }>("GET", `/websites/${w.id}/databases`), { databases: [], users: [] }),
    optional(request<{ data: ApiSnapshot[] }>("GET", `/websites/${w.id}/snapshots`).then((r) => r.data), []),
  ]);
  return {
    // The database servers on a different server that the website can use.
    remote: (db.servers ?? []).filter((x) => x.remote).map((x) => x.id),
    databases: db.databases.map((d): Database => ({ id: d.id, name: d.name, engine: d.engine, websiteId: w.id, sizeBytes: d.sizeBytes ?? 0, serverId: w.serverId, number: d.number ?? undefined, keys: d.keys ?? undefined, databaseServerId: d.databaseServerId ?? undefined, host: d.host, port: d.port })),
    users: db.users.map((u): DatabaseUser => ({ id: u.id, name: u.name, engine: u.engine, websiteId: w.id, databaseServerId: u.databaseServerId ?? undefined, grants: u.grants ?? [], access: u.access })),
    snapshots: snaps.map((s): Snapshot => ({ id: s.id, at: s.createdAt, kind: s.kind === "manual" ? "manual" : "nightly", sizeBytes: s.sizeBytes ?? 0, websiteId: w.id, databases: s.databases ?? [], part: s.part ?? "website", serverId: s.serverId })),
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
    remoteDatabaseServers: { ...s.remoteDatabaseServers, [websiteId]: d.remote },
    snapshots: [...s.snapshots.filter((x) => x.websiteId !== websiteId), ...d.snapshots].sort((a, b) => b.at.localeCompare(a.at)),
  });
}

// Session

const DEVICE_KEY = "cloudfactory.device";

/**
 * A random ID of this browser. The panel sends an email to the user after a sign-in from a
 * browser that it does not know. The ID stays after a sign-out.
 */
export function deviceId(): string | undefined {
  try {
    let id = localStorage.getItem(DEVICE_KEY);
    if (!id || !/^[A-Za-z0-9_-]{16,100}$/.test(id)) {
      id = crypto.randomUUID();
      localStorage.setItem(DEVICE_KEY, id);
    }
    return id;
  } catch {
    // The browser keeps no data. The panel then knows the browser by its user agent.
    return undefined;
  }
}

export async function signIn(email: string, password: string, totp: string): Promise<void> {
  const res = await request<{ token: string }>("POST", "/auth/login", { email, password, totp: totp || undefined, deviceId: deviceId() });
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

export type { ApiCertificate, ApiDatabaseServer, ApiDnsAccount, ApiDnsRecord, ApiDnsZone, ApiMailDomain, ApiMailbox, ApiJob, ApiPanelCertificate, ApiUser, ApiWebsite };
