// The state of the panel and the actions that change it.
//
// Each action calls the panel API through live.ts, and the state comes from the API. The flow
// of a change is in spec section 6.3: the API checks the input, writes the desired state, and
// puts a job in the queue. The agent runs the job, and the panel reads the result.
//
// The panel has no sample data of its own. For development, the demonstration and the
// screenshots, the panel API has a demo seeder and a demo agent (docs/development.md).

import * as live from "./live";
import type {
  ApiToken,
  BackupPolicy,
  Confirmation,
  CronJob,
  Customer,
  DatabaseEngine,
  DatabaseLimits,
  DatabasePrivileges,
  DatabaseServer,
  DnsRecordType,
  Deploy,
  DomainKind,
  FileContent,
  FileEntry,
  FileList,
  HostUpdatePolicy,
  Job,
  LogLine,
  Mesh,
  Plan,
  ServerRole,
  State,
  PanelCertificateMethod,
  TokenScope,
  UpdatePolicy,
  Website,
  WebsiteKind,
} from "./types";

/** An error that the API returns for incorrect input. The UI shows it next to the field. */
export class ApiError extends Error {
  constructor(message: string, readonly fields: Record<string, string> = {}) {
    super(message);
    this.name = "ApiError";
  }
}

type Listener = () => void;

/** The state before the first load from the API: no data, and signed out. */
export function emptyState(): State {
  return {
    session: { signedIn: false, user: { id: "", name: "", email: "", role: "owner" }, contextId: "" },
    contexts: [], websites: [], jobs: [], activity: [], notifications: [], servers: [], images: [], rollout: undefined,
    databaseServers: [], dnsZones: [], nameServers: [], databases: [], databaseUsers: [], remoteDatabaseServers: {}, snapshots: [], plans: [], customers: [], users: [],
    backupPolicy: { daily: 7, weekly: 4, monthly: 6, time: "03:00", target: "", credentialNames: [] },
    tokens: [], totpEnabled: false, securityNotices: [], audit: [], joinToken: "", joinCaSha256: undefined,
    panelDomain: "",
    panelCertificate: { domain: "", method: null, state: "pending", letsEncryptAvailable: false, serversWithoutCa: [] },
  };
}

let state: State = emptyState();
const listeners = new Set<Listener>();

export const getState = () => state;

export function subscribe(listener: Listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function update(change: (draft: State) => void) {
  const draft = structuredClone(state);
  change(draft);
  state = draft;
  listeners.forEach((l) => l());
}

/** For tests: puts a state in the store, or the empty state. */
export function resetState(next: State = emptyState()) {
  state = next;
  listeners.forEach((l) => l());
}

const now = () => new Date().toISOString();

function replace(next: State) {
  state = next;
  listeners.forEach((l) => l());
}

/** Runs a request and gives an ApiError for the form when it fails. */
async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e) {
    if (e instanceof live.LiveError) throw new ApiError(e.message, e.fields);
    throw e;
  }
}

let loading: Promise<void> | null = null;

/** Loads the state from the API again. Calls at the same time share one load. */
export function refresh(): Promise<void> {
  if (!live.hasSession()) return Promise.resolve();
  loading ??= live.load(state).then(replace)
    .catch((e: unknown) => {
      // Keep the data that the panel has, and tell the user. Before, a failed first load
      // showed an empty panel with no message.
      if (!state.session.signedIn) return;
      const loadError = e instanceof Error && e.message ? e.message : "The panel API does not answer.";
      if (state.session.loading || state.session.loadError !== loadError) replace({ ...state, session: { ...state.session, loading: false, loadError } });
    })
    .finally(() => (loading = null));
  return loading;
}

/** Loads the state again after an error, for the "Try again" button. */
export function retryLoad(): Promise<void> {
  if (state.session.signedIn) replace({ ...state, session: { ...state.session, loading: !state.session.user.id } });
  return refresh();
}

/** Loads the state after a change. A load that started before the change does not count. */
async function reload(): Promise<void> {
  await loading;
  return refresh();
}

/** Follows a job that the API made. The promise resolves when the agent has finished it. */
function track(job: live.ApiJob | null | undefined): Promise<Job> {
  if (!job) {
    void refresh();
    return Promise.resolve({ id: "", title: "", state: "succeeded", steps: [], step: 0, createdAt: now() });
  }
  const first = live.toJob(job);
  update((d) => {
    d.jobs = [first, ...d.jobs.filter((j) => j.id !== first.id)];
  });
  void refresh();
  // The wait updates only the job. The panel loads all data again when the job ends.
  return live.waitForJob(job, (j) => update((d) => {
    d.jobs = d.jobs.map((x) => (x.id === j.id ? j : x));
  })).then(async (done) => {
    await refresh();
    return done;
  });
}

/** Batch 1, finding 9: with many websites, reads the databases and snapshots of a website when the user opens it. */
export function showWebsite(websiteId: string) {
  void live.showWebsite(state, websiteId).then((apply) => apply && replace(apply(state))).catch(() => {});
}

/** True when the panel reads the databases and snapshots only of the websites that the user opened. */
export const partialDetails = () => state.websites.length > live.DETAIL_LIMIT;

const q = (s: string) => encodeURIComponent(s);
type JobAnswer = { job: live.ApiJob | null };

live.setUnauthorisedHandler(() => replace(emptyState()));

/**
 * Starts the panel in the browser (main.tsx): it uses the session of an earlier page load, and
 * it reads the changes of agents and other users on a schedule.
 */
export function start() {
  if (live.hasSession()) {
    state = { ...state, session: { ...state.session, signedIn: true, loading: true } };
    void refresh();
  }
  // The browser slows timers in hidden tabs.
  // Batch 1, finding 9: with many websites, a load takes more requests, so the panel loads less often.
  const poll = () => setTimeout(async () => {
    if (state.session.signedIn && document.visibilityState === "visible") await refresh();
    poll();
  }, state.websites.length > 500 ? 60000 : 15000);
  poll();
}

function site(websiteId: string): Website {
  const w = state.websites.find((x) => x.id === websiteId);
  if (!w) throw new ApiError("The website does not exist.");
  return w;
}

// Session

export function signIn(email: string, password: string, totp = "") {
  return call(async () => {
    await live.signIn(email, password, totp);
    replace({ ...state, session: { ...state.session, signedIn: true, loading: true } });
    await refresh();
  });
}

export function signOut() {
  live.signOut();
  replace(emptyState());
}

export function setContext(contextId: string) {
  update((d) => {
    d.session.contextId = contextId;
  });
}

export function markNotificationsRead() {
  update((d) => d.notifications.forEach((n) => (n.read = true)));
}

// Websites

export interface NewWebsite {
  name: string;
  application: string;
  imageId: string;
  planId: string;
  serverId: string;
  customerId: string;
  kind?: WebsiteKind;
  /** The start command of a worker. */
  command?: string;
}

export function createWebsite(input: NewWebsite): Promise<{ websiteId: string; job: Promise<Job> }> | { websiteId: string; job: Promise<Job> } {
  return call(async () => {
    const r = await live.request<{ website: live.ApiWebsite; job: live.ApiJob }>("POST", "/websites", {
      // CF-07: only an administrator selects the plan here. The panel uses the plan of the customer.
      // A private website and a worker have no domain: the name is a short name.
      kind: input.kind && input.kind !== "public" ? input.kind : undefined, command: input.kind === "worker" ? input.command?.trim() : undefined,
      name: input.name.trim().toLowerCase(), application: input.application, imageId: input.imageId, planId: state.session.contextId === "platform" || state.session.user.role === "administrator" ? input.planId : undefined,
      serverId: input.serverId || undefined, customerId: input.customerId && input.customerId !== "platform" ? input.customerId : undefined,
    });
    update((d) => {
      d.websites = [live.toWebsite(r.website), ...d.websites.filter((w) => w.id !== r.website.id)];
    });
    return { websiteId: r.website.id, job: track(r.job) };
  });
}

/**
 * Makes a website again after its first job failed, for example because the server could not
 * pull the image. The agent keeps what the first job made.
 */
export function retryWebsite(websiteId: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/retry`)).then((r) => {
    update((d) => {
      const w = d.websites.find((x) => x.id === websiteId);
      if (w) w.status = "starting";
    });
    return track(r.job);
  });
}

export function setPower(websiteId: string, action: "start" | "stop" | "restart") {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/power`, { action })).then((r) => track(r.job));
}

/** A database user that a staging copy made. The API shows the password once. */
export interface StagingCredential {
  engine: DatabaseEngine;
  user: string;
  password: string;
  databases: string[];
  /** The host name and the port of the database server of the user. */
  host?: string;
  port?: number;
  databaseServerId?: string | null;
}

export function createStagingCopy(websiteId: string): Promise<{ websiteId: string; job: Promise<Job>; credentials?: StagingCredential[] }> | { websiteId: string; job: Promise<Job>; credentials?: StagingCredential[] } {
  return call(async () => {
    const r = await live.request<{ website: live.ApiWebsite; job: live.ApiJob; databaseCredentials?: StagingCredential[] }>("POST", `/websites/${websiteId}/staging`);
    update((d) => {
      d.websites = [live.toWebsite(r.website), ...d.websites.filter((w) => w.id !== r.website.id)];
    });
    return { websiteId: r.website.id, job: track(r.job), credentials: r.databaseCredentials ?? [] };
  });
}

export function deleteWebsite(websiteId: string) {
  return call(() => live.request<JobAnswer>("DELETE", `/websites/${websiteId}`)).then((r) => track(r.job));
}

export function setLimits(websiteId: string, limits: Website["limits"]) {
  return call(() => live.request<Partial<JobAnswer>>("PATCH", `/websites/${websiteId}`, { limits })).then((r) => track(r.job));
}

export function setWebsitePlan(websiteId: string, planId: string) {
  return call(() => live.request<Partial<JobAnswer>>("PATCH", `/websites/${websiteId}`, { planId })).then((r) => track(r.job));
}

// Domains

export function verifyDomain(websiteId: string, name: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains/${q(name)}/verify`)).then(async (r) => {
    await refresh();
    if (r.job) await track(r.job);
  });
}

export function addDomain(websiteId: string, name: string, kind: DomainKind) {
  return call(() => live.request<Partial<JobAnswer>>("POST", `/websites/${websiteId}/domains`, { name: name.trim().toLowerCase(), kind })).then(async (r) => {
    await refresh();
    if (r.job) await track(r.job);
  });
}

export function removeDomain(websiteId: string, name: string) {
  return call(() => live.request<JobAnswer>("DELETE", `/websites/${websiteId}/domains/${q(name)}`)).then(async (r) => {
    await refresh();
    if (r.job) await track(r.job);
  });
}

export function makePrimary(websiteId: string, name: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains/${q(name)}/primary`)).then((r) => track(r.job));
}

/** FR-403: check again if the DNS of a domain points to the correct server. */
export function checkDns(websiteId: string, name: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains/${q(name)}/check`)).then((r) => track(r.job));
}

// Runtime

export function changeRuntime(websiteId: string, imageId: string) {
  return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { imageId })).then((r) => track(r.job));
}

/** FR-303: go back to the previous image in one action. */
export function revertRuntime(websiteId: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/runtime/revert`)).then((r) => track(r.job));
}

/** The start command of a worker. The panel starts a new container with the command. */
export function setCommand(websiteId: string, command: string) {
  return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { command: command.trim() })).then((r) => track(r.job));
}

export function setWorkerMode(websiteId: string, on: boolean) {
  return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { workerMode: on })).then((r) => track(r.job));
}

/** Batch 1, finding 11: the directory in /app that the web server serves. */
export function setDocumentRoot(websiteId: string, documentRoot: string) {
  const root = documentRoot.trim().replace(/^\/+|\/+$/g, "") || ".";
  if (!/^(\.|[A-Za-z0-9_-][A-Za-z0-9._-]*(\/[A-Za-z0-9_-][A-Za-z0-9._-]*)*)$/.test(root) || /(^|\/)\.\.(\/|$)/.test(root)) {
    throw new ApiError("Type a directory in the files of the website.", { documentRoot: "Use letters, digits, dots, dashes and slashes, for example public or web. Do not use \"..\"." });
  }
  return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { documentRoot: root })).then((r) => track(r.job));
}

export function setPhpSettings(websiteId: string, extensions: string[], phpIni: Record<string, string>) {
  return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { extensions, phpIni })).then((r) => track(r.job));
}

// Files, logs, cron jobs and deploys
//
// The panel reads these when the user opens their page, and does not keep them in the state.
// The agent does each file operation as the Unix user of the website, in the website directory.

const filesOf = (websiteId: string) => `/websites/${websiteId}/files`;

export function listFiles(websiteId: string, path: string): Promise<FileList> {
  return call(() => live.request<FileList>("GET", `${filesOf(websiteId)}?path=${q(path)}`));
}

export function readFile(websiteId: string, path: string): Promise<FileContent> {
  return call(() => live.request<FileContent>("GET", `${filesOf(websiteId)}/content?path=${q(path)}`));
}

/** Writes a file. With createOnly, the API refuses a path that exists. */
export function writeFile(websiteId: string, path: string, content: string, options: { encoding?: "utf8" | "base64"; createOnly?: boolean } = {}): Promise<FileEntry> {
  return call(() => live.request<{ file: FileEntry }>("PUT", `${filesOf(websiteId)}/content`, { path, content, ...options })).then((r) => r.file);
}

export function makeDirectory(websiteId: string, path: string): Promise<FileEntry> {
  return call(() => live.request<{ file: FileEntry }>("POST", `${filesOf(websiteId)}/directories`, { path })).then((r) => r.file);
}

export function moveFile(websiteId: string, from: string, to: string): Promise<FileEntry> {
  return call(() => live.request<{ file: FileEntry }>("POST", `${filesOf(websiteId)}/move`, { from, to })).then((r) => r.file);
}

/** Removes a file, or a directory with all that is in it. */
export function deleteFile(websiteId: string, path: string): Promise<void> {
  return call(() => live.request<void>("DELETE", `${filesOf(websiteId)}?path=${q(path)}`));
}

/** The largest file that the panel sends in one request (FileController::MAX_WRITE). */
export const MAX_UPLOAD_BYTES = 5 * 1024 * 1024;

/** The last lines of the output of the website container. */
export function readLogs(websiteId: string, stream: "all" | "access" | "error" = "all", lines = 200): Promise<LogLine[]> {
  return call(() => live.request<{ lines: LogLine[] }>("GET", `/websites/${websiteId}/logs?lines=${lines}${stream === "all" ? "" : `&stream=${stream}`}`)).then((r) => r.lines);
}

export function listCronJobs(websiteId: string): Promise<CronJob[]> {
  return call(() => live.request<{ data: CronJob[] }>("GET", `/websites/${websiteId}/cron-jobs`)).then((r) => r.data);
}

type CronAnswer = { cronJob: CronJob; job: live.ApiJob | null };

export function addCronJob(websiteId: string, schedule: string, command: string): Promise<CronJob> {
  return call(() => live.request<CronAnswer>("POST", `/websites/${websiteId}/cron-jobs`, { schedule: schedule.trim().replace(/\s+/g, " "), command: command.trim() })).then((r) => {
    void track(r.job);
    return r.cronJob;
  });
}

export function setCronJob(websiteId: string, cronJobId: string, change: { schedule?: string; command?: string; enabled?: boolean }): Promise<CronJob> {
  return call(() => live.request<CronAnswer>("PATCH", `/websites/${websiteId}/cron-jobs/${cronJobId}`, change)).then((r) => {
    void track(r.job);
    return r.cronJob;
  });
}

export function removeCronJob(websiteId: string, cronJobId: string): Promise<void> {
  return call(() => live.request<JobAnswer>("DELETE", `/websites/${websiteId}/cron-jobs/${cronJobId}`)).then((r) => void track(r.job));
}

/** Runs the command of a cron job now. The promise resolves when the command has finished. */
export function runCronJob(websiteId: string, cronJobId: string): Promise<Job> {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/cron-jobs/${cronJobId}/run`)).then((r) => track(r.job));
}

export function readDeploy(websiteId: string): Promise<Deploy> {
  return call(() => live.request<Deploy>("GET", `/websites/${websiteId}/deploy`));
}

export interface GitSourceInput {
  repository: string;
  branch: string;
  webDirectory: string;
  trigger: "manual" | "webhook";
  buildCommands: string[];
}

/** Connects a Git repository to the website, or changes the connection. */
export function setGitSource(websiteId: string, input: GitSourceInput): Promise<Deploy> {
  const body = { ...input, repository: input.repository.trim(), branch: input.branch.trim(), webDirectory: input.webDirectory.trim() || ".", buildCommands: input.buildCommands.map((c) => c.trim()).filter(Boolean) };
  return call(() => live.request<Deploy>("PUT", `/websites/${websiteId}/deploy`, body));
}

export function removeGitSource(websiteId: string): Promise<void> {
  return call(() => live.request<void>("DELETE", `/websites/${websiteId}/deploy`));
}

export function setDeploySecret(websiteId: string, name: string, value: string): Promise<Deploy> {
  return call(() => live.request<Deploy>("PUT", `/websites/${websiteId}/deploy/secrets/${q(name.trim())}`, { value }));
}

export function removeDeploySecret(websiteId: string, name: string): Promise<Deploy> {
  return call(() => live.request<Deploy>("DELETE", `/websites/${websiteId}/deploy/secrets/${q(name)}`));
}

/** Starts a deploy of the branch. The job promise resolves when the release is live or has failed. */
export function startDeploy(websiteId: string): Promise<{ job: Promise<Job> }> {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/deploy/releases`)).then((r) => ({ job: track(r.job) }));
}

/** Makes a release that the server keeps the live release again. */
export function activateRelease(websiteId: string, releaseId: string): Promise<{ job: Promise<Job> }> {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/deploy/releases/${releaseId}/activate`)).then((r) => ({ job: track(r.job) }));
}

// SFTP

/**
 * Spec section 10: the SFTP access of a website. The owner signs in as the Unix
 * user of the website with an SSH key. SFTP accepts no passwords.
 */
export function setSftp(websiteId: string, change: { enabled: boolean; authorizedKeys: string[] }): Promise<{ job: Promise<Job> }> {
  const keys = change.authorizedKeys.map((k) => k.trim().replace(/\s+/g, " ")).filter(Boolean);
  const bad = keys.findIndex((k) => !SSH_KEY.test(k));
  if (bad >= 0) return Promise.reject(new ApiError("This is not an SSH public key.", { key: "Paste a public key that starts with ssh-ed25519, ssh-rsa or ecdsa-sha2." }));
  if (new Set(keys).size !== keys.length) return Promise.reject(new ApiError("This key is in the list.", { key: "This key is in the list." }));
  if (change.enabled && keys.length === 0) return Promise.reject(new ApiError("Add an SSH public key to turn on SFTP.", { key: "Add an SSH public key to turn on SFTP." }));
  return call(async () => {
    const r = await live.request<{ job: live.ApiJob; sftp: Website["sftp"] }>("PUT", `/websites/${websiteId}/sftp`, { ...change, authorizedKeys: keys });
    update((d) => {
      const w = d.websites.find((x) => x.id === websiteId);
      if (w && r.sftp) w.sftp = { ...r.sftp, host: r.sftp.host ?? w.sftp?.host ?? "" };
    });
    return { job: track(r.job) };
  });
}

/** An OpenSSH public key (SftpController::SSH_KEY). */
const SSH_KEY = /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [ -~]{0,200})?$/;

// Backups (FR-901 to FR-903)

export function backupNow(websiteId: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/snapshots`)).then((r) => track(r.job));
}

export type RestoreScope = { kind: "website" } | { kind: "database"; name: string } | { kind: "file"; path: string };

/**
 * Audit finding R8: removeNewFiles is for the restore of a full website. The panel then also
 * removes the files that are not in the backup, for example the files that an attacker added.
 */
export function restore(snapshotId: string, scope: RestoreScope, removeNewFiles = false) {
  const extra = removeNewFiles && scope.kind === "website";
  const snapshot = state.snapshots.find((x) => x.id === snapshotId);
  if (!snapshot) throw new ApiError("The snapshot does not exist.");
  const item = scope.kind === "website" ? undefined : scope.kind === "file" ? scope.path : scope.name;
  return call(() => live.request<JobAnswer>("POST", `/websites/${snapshot.websiteId}/snapshots/${snapshotId}/restore`, { scope: scope.kind, item, ...(extra ? { removeNewFiles: true } : {}) })).then((r) => track(r.job));
}

export function setBackupPolicy(policy: BackupPolicy) {
  return call(() => live.request<BackupPolicy>("PUT", "/backup-policy", policy)).then(() => refresh());
}

/**
 * Second audit, finding F08: the object storage key of one server. The panel sends it to
 * that server only. Without credentials, the server uses the shared key again.
 */
export function setServerStorageKey(serverId: string, credentials: Record<string, string> | null) {
  const path = `/backup-policy/servers/${serverId}/credentials`;
  return call(() => (credentials ? live.request<BackupPolicy>("PUT", path, { credentials }) : live.request<BackupPolicy>("DELETE", path))).then(() => refresh());
}

// Databases (FR-501, FR-502)

/** CF-01: each database and database user name starts with the Unix user of the website. */
export function databasePrefix(websiteId: string) {
  return `${site(websiteId).unixUser}_`;
}

export type GrantInput = { databaseId: string; privileges: DatabasePrivileges };

// Database servers
//
// A customer makes database servers as it makes websites. Each one is a container with one
// engine and its own limits. The plan of the customer gives the number of its database
// servers. The plan of a database server gives its limits and the number of its databases.

/**
 * The database servers that a website can use: those of its customer on its server, then those
 * on a different server that are in a network with the website. The API gives the second group.
 */
export function databaseServersFor(s: State, website: Pick<Website, "id" | "customerId" | "serverId"> | undefined, engine?: DatabaseEngine): DatabaseServer[] {
  if (!website) return [];
  const remote = s.remoteDatabaseServers[website.id] ?? [];
  const usable = s.databaseServers.filter((x) => x.customerId === website.customerId && x.status !== "removing" && (!engine || x.engine === engine));
  return [
    ...usable.filter((x) => x.serverId === website.serverId).sort((a, b) => a.port - b.port),
    ...usable.filter((x) => x.serverId !== website.serverId && remote.includes(x.id)).sort((a, b) => a.name.localeCompare(b.name)),
  ];
}

function putDatabaseServer(server: live.ApiDatabaseServer) {
  update((d) => {
    const next = live.toDatabaseServer(server);
    d.databaseServers = d.databaseServers.some((x) => x.id === next.id) ? d.databaseServers.map((x) => (x.id === next.id ? next : x)) : [...d.databaseServers, next];
  });
}

export interface DatabaseServerInput {
  customerId: string;
  engine: DatabaseEngine;
  /** With no name, the panel gives a name, for example "mariadb-2". */
  name?: string;
  /** With no plan, the database server gets the plan of the customer. */
  planId?: string;
  /** With no server, the panel uses the server with the most websites of the customer. */
  serverId?: string;
  /** Only an administrator sets limits. The other users select a plan. */
  limits?: DatabaseLimits;
}

/** Makes a database server. The promise resolves when the panel has put the job in the queue. */
export function createDatabaseServer(input: DatabaseServerInput): Promise<void> {
  return call(async () => {
    const body = { engine: input.engine, customerId: input.customerId, name: input.name?.trim() || undefined, planId: input.planId || undefined, serverId: input.serverId || undefined, limits: input.limits };
    const r = await live.request<{ databaseServer: live.ApiDatabaseServer; job: live.ApiJob }>("POST", "/database-servers", body);
    putDatabaseServer(r.databaseServer);
    void track(r.job).then(() => refresh());
  });
}

/**
 * Changes the name, the plan or the limits of a database server. A new plan gives its limits.
 * New limits restart the database server.
 */
export function setDatabaseServer(serverId: string, change: { name?: string; planId?: string; limits?: DatabaseLimits }): Promise<void> {
  return call(async () => {
    const r = await live.request<{ databaseServer: live.ApiDatabaseServer; job: live.ApiJob | null }>("PATCH", `/database-servers/${serverId}`, change);
    putDatabaseServer(r.databaseServer);
    if (r.job) void track(r.job).then(() => refresh());
  });
}

/** Makes a database server again after its first job failed. */
export function retryDatabaseServer(serverId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<{ databaseServer: live.ApiDatabaseServer; job: live.ApiJob }>("POST", `/database-servers/${serverId}/retry`);
    putDatabaseServer(r.databaseServer);
    void track(r.job).then(() => refresh());
  });
}

/** Removes a database server that has no databases and no users. The server removes its data. */
export function deleteDatabaseServer(serverId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<JobAnswer>("DELETE", `/database-servers/${serverId}`);
    update((d) => {
      const x = d.databaseServers.find((y) => y.id === serverId);
      if (x) x.status = "removing";
    });
    void track(r.job).then(() => refresh());
  });
}

/**
 * Makes a database. With createUser, it also makes a user with readwrite access, and gives its password.
 * A Valkey database always has a user with the same name: the user and the database number are the database.
 * With no database server, the panel selects one, or makes the first one of the engine.
 */
export function createDatabase(websiteId: string, engine: DatabaseEngine, name: string, createUser = true, databaseServerId?: string): Promise<string | null> {
  if (engine === "valkey") createUser = true;
  return call(async () => {
    const r = await live.request<{ password: string | null; job: live.ApiJob }>("POST", `/websites/${websiteId}/databases`, { engine, name: name.trim(), createUser, databaseServerId: databaseServerId || undefined });
    void track(r.job).then(() => refresh());
    await refresh();
    return r.password;
  });
}

export function deleteDatabase(databaseId: string) {
  const db = state.databases.find((x) => x.id === databaseId);
  if (!db) throw new ApiError("The database does not exist.");
  return call(() => live.request<JobAnswer>("DELETE", `/websites/${db.websiteId}/databases/${databaseId}`)).then((r) => track(r.job)).then(() => refresh());
}

/**
 * Makes a database user with a permission for each database, and gives its password. A user
 * is in one database server: the database server of its databases, or the one of the request.
 */
export function createDatabaseUser(websiteId: string, engine: DatabaseEngine, name: string, grants: GrantInput[], databaseServerId?: string): Promise<string> {
  if (engine === "valkey") throw new ApiError("Correct the engine.", { engine: "A Valkey database has one user, with the same name. Make a Valkey database." });
  return call(async () => {
    const r = await live.request<{ password: string; job: live.ApiJob }>("POST", `/websites/${websiteId}/database-users`, { engine, name: name.trim(), grants, databaseServerId: databaseServerId || undefined });
    void track(r.job);
    await refresh();
    return r.password;
  });
}

/** Changes the permissions of a user, where it can connect from, or its password. Gives the new password. */
export function setDatabaseUser(userId: string, change: { grants?: GrantInput[]; access?: "website" | "anywhere"; resetPassword?: boolean }): Promise<string | null> {
  const user = state.databaseUsers.find((x) => x.id === userId);
  if (!user) throw new ApiError("The database user does not exist.");
  if (user.engine === "valkey" && (change.grants || change.access)) throw new ApiError("A Valkey user has access to its own database only, from the container of the website only. You can only make a new password.");
  return call(async () => {
    const r = await live.request<{ password: string | null; job: live.ApiJob }>("PATCH", `/websites/${user.websiteId}/database-users/${userId}`, change);
    void track(r.job);
    await refresh();
    return r.password;
  });
}

export function deleteDatabaseUser(userId: string) {
  const user = state.databaseUsers.find((x) => x.id === userId);
  if (!user) throw new ApiError("The database user does not exist.");
  if (user.engine === "valkey") throw new ApiError("The user of a Valkey database goes with its database. Remove the database.");
  return call(() => live.request<JobAnswer>("DELETE", `/websites/${user.websiteId}/database-users/${userId}`)).then((r) => track(r.job)).then(() => refresh());
}

/** FR-307: adds a custom image. The API checks the reference, the tag and the port. */
export function addImage(name: string, reference: string, tag: string, policy: UpdatePolicy, run: { containerPort?: string; command?: string } = {}) {
  const port = run.containerPort?.trim() ? Number(run.containerPort) : null;
  const command = run.command?.trim() ? splitCommand(run.command) : null;
  return call(() => live.request("POST", "/images", { name: name.trim(), reference: reference.trim(), tag: tag.trim(), policy, containerPort: port, command })).then(() => refresh()).then(() => undefined);
}

export function setImagePolicy(imageId: string, policy: UpdatePolicy) {
  return call(() => live.request("PATCH", `/images/${imageId}`, { policy })).then(() => refresh());
}

/**
 * Splits a start command into its arguments. Single and double quotes keep spaces in an
 * argument. The panel does not use a shell.
 */
export function splitCommand(text: string): string[] {
  const out: string[] = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  for (let m = re.exec(text); m; m = re.exec(text)) out.push(m[1] ?? m[2] ?? m[3]);
  return out;
}

/** FR-1110: checks the registry for a new signed version of the image now. */
export function checkImage(imageId: string) {
  return call(() => live.request("POST", `/images/${imageId}/check`)).then(() => refresh());
}

export function startRollout(imageId: string) {
  return call(() => live.request("POST", "/rollouts", { imageId })).then(() => refresh());
}

export function pauseRollout(paused: boolean) {
  const r = state.rollout;
  if (!r) throw new ApiError("No rollout is running.");
  return call(() => live.request("POST", `/rollouts/${r.id}/${paused ? "pause" : "resume"}`)).then(() => refresh());
}

export function rollBackAll() {
  const r = state.rollout;
  if (!r) throw new ApiError("No rollout is running.");
  return call(() => live.request("POST", `/rollouts/${r.id}/roll-back`)).then(() => refresh());
}

// DNS hosting

type DnsJobs = { jobs: live.ApiJob[] };

function putDnsZone(zone: live.ApiDnsZone) {
  update((d) => {
    const next = live.toDnsZone(zone);
    d.dnsZones = d.dnsZones.some((x) => x.id === next.id) ? d.dnsZones.map((x) => (x.id === next.id ? next : x)) : [...d.dnsZones, next].sort((a, b) => a.name.localeCompare(b.name));
  });
}

/** Follows the jobs of a change of a zone: one job for each DNS server. Then it reads the zone again. */
function trackZone(zoneId: string, jobs: live.ApiJob[]) {
  update((d) => {
    const z = d.dnsZones.find((x) => x.id === zoneId);
    if (z && jobs.length && z.status !== "removing") z.status = "updating";
  });
  void Promise.all(jobs.map((j) => track(j))).then(() => showDnsZone(zoneId)).catch(() => undefined);
}

/** Reads the records of a zone. The zone page calls it when it opens. */
export async function showDnsZone(zoneId: string): Promise<void> {
  try {
    const zone = await live.dnsZone(zoneId);
    update((d) => {
      d.dnsZones = d.dnsZones.some((x) => x.id === zone.id) ? d.dnsZones.map((x) => (x.id === zone.id ? zone : x)) : [...d.dnsZones, zone];
    });
  } catch {
    // The zone is removed, or the API does not answer. The page shows what the panel has.
  }
}

/** Makes a zone for a domain of a customer. The promise gives the ID of the zone. */
export function createDnsZone(name: string, customerId?: string): Promise<string> {
  return call(async () => {
    const r = await live.request<{ zone: live.ApiDnsZone } & DnsJobs>("POST", "/dns-zones", { name: name.trim().toLowerCase().replace(/\.$/, ""), customerId: customerId || undefined });
    putDnsZone(r.zone);
    trackZone(r.zone.id, r.jobs);
    return r.zone.id;
  });
}

/** Removes a zone and all its records from each DNS server. */
export function deleteDnsZone(zoneId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<DnsJobs>("DELETE", `/dns-zones/${zoneId}`);
    update((d) => {
      const z = d.dnsZones.find((x) => x.id === zoneId);
      if (z) z.status = "removing";
    });
    void Promise.all(r.jobs.map((j) => track(j))).then(() => refresh()).catch(() => undefined);
    if (r.jobs.length === 0) void refresh();
  });
}

export interface DnsRecordInput {
  type: DnsRecordType;
  name: string;
  value: string;
  ttl: number;
  /** Only for an MX or an SRV record. */
  priority?: number;
}

/** Adds a record, or changes the record with the ID. The type of a record does not change. */
export function saveDnsRecord(zoneId: string, input: DnsRecordInput, recordId?: string): Promise<void> {
  return call(async () => {
    const body = { type: recordId ? undefined : input.type, name: input.name.trim() || "@", value: input.value.trim(), ttl: input.ttl, priority: input.priority };
    const r = await live.request<{ record: live.ApiDnsRecord } & DnsJobs>(recordId ? "PATCH" : "POST", `/dns-zones/${zoneId}/records${recordId ? `/${recordId}` : ""}`, body);
    update((d) => {
      const z = d.dnsZones.find((x) => x.id === zoneId);
      if (!z?.records) return;
      const next = live.toDnsRecord(r.record);
      z.records = recordId ? z.records.map((x) => (x.id === recordId ? next : x)) : [...z.records, next];
      z.recordCount = z.records.length;
    });
    trackZone(zoneId, r.jobs);
  });
}

export function deleteDnsRecord(zoneId: string, recordId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<DnsJobs>("DELETE", `/dns-zones/${zoneId}/records/${recordId}`);
    update((d) => {
      const z = d.dnsZones.find((x) => x.id === zoneId);
      if (!z?.records) return;
      z.records = z.records.filter((x) => x.id !== recordId);
      z.recordCount = z.records.length;
    });
    trackZone(zoneId, r.jobs);
  });
}

/** Turns DNSSEC on or off. With DNSSEC on, the zone then has the DS record for the registrar. */
export function setDnssec(zoneId: string, enabled: boolean): Promise<void> {
  return call(async () => {
    const r = await live.request<{ zone: live.ApiDnsZone } & DnsJobs>("PUT", `/dns-zones/${zoneId}/dnssec`, { enabled });
    putDnsZone(r.zone);
    trackZone(zoneId, r.jobs);
  });
}

/** Asks the public DNS again for the name servers of the domain. */
export function checkDnsZone(zoneId: string): Promise<void> {
  return call(async () => {
    putDnsZone(await live.request<live.ApiDnsZone>("POST", `/dns-zones/${zoneId}/check`));
  });
}

/** Sends the zone to each DNS server again, for example after a job failed. */
export function syncDnsZone(zoneId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<{ zone: live.ApiDnsZone } & DnsJobs>("POST", `/dns-zones/${zoneId}/sync`);
    putDnsZone(r.zone);
    trackZone(zoneId, r.jobs);
  });
}

// Servers (FR-1112 to FR-1118, FR-1201)

export function setServer(serverId: string, change: { updatePolicy?: HostUpdatePolicy; maintenanceWindow?: string; gvisorPlatform?: "systrap" | "kvm"; roles?: ServerRole[]; dnsName?: string | null }) {
  // A new gVisor platform makes a job that writes the units again (ConfigureHost).
  return call(() => live.request<JobAnswer>("PATCH", `/servers/${serverId}`, change)).then((r) => (r?.job ? track(r.job).then(() => undefined) : refresh()));
}

/** FR-1113: the policy "tools" updates only the held container tools. */
export function installUpdates(serverId: string, policy?: "security" | "all" | "tools") {
  return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/install-updates`, policy ? { policy } : undefined)).then((r) => track(r.job));
}

/** FR-1116: reboot only one server of each role at a time. */
export function rebootServer(serverId: string) {
  return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/reboot`)).then((r) => track(r.job));
}

/** The panel connects to the agent of the server again and records the result. */
export function checkConnection(serverId: string) {
  return call(() => live.request<Parameters<typeof live.toServer>[0]>("POST", `/servers/${serverId}/check-connection`)).then((r) => update((d) => {
    const i = d.servers.findIndex((s) => s.id === serverId);
    if (i >= 0) d.servers[i] = live.toServer(r);
  }));
}

/** Removes a server with no websites from the panel, for example a server that joined with a wrong address. */
export function removeServer(serverId: string) {
  return call(() => live.request("DELETE", `/servers/${serverId}`)).then(() => refresh()).then(() => undefined);
}

export function updateAgent(serverId: string) {
  return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/update-agent`)).then((r) => track(r.job));
}

export function newJoinToken() {
  return call(() => live.request<{ token: string; panelCaSha256?: string | null; installUrl?: string }>("POST", "/servers/join-tokens")).then((r) => update((d) => {
    d.joinToken = r.token;
    d.joinCaSha256 = r.panelCaSha256 ?? undefined;
    d.joinInstallUrl = r.installUrl;
  }));
}

/**
 * A name or an address that only the computer itself can reach, for example panel.localhost.
 * A different server cannot connect to a panel with such a name.
 */
export function loopbackName(host: string) {
  const h = host.toLowerCase().replace(/\.$/, "").replace(/^\[|\]$/g, "");
  return h === "localhost" || h.endsWith(".localhost") || /^127(\.\d+){3}$/.test(h) || h === "::1";
}

/** Public names only: the same rule as the agent (caddy.PublicName) and the API. */
export function publicName(domain: string) {
  const d = domain.toLowerCase().replace(/\.$/, "");
  if (!d.includes(".") || /^[\d.]+$/.test(d) || d.includes(":")) return false;
  return ![".localhost", ".local", ".internal", ".home.arpa", ".test", ".example", ".invalid"].some((s) => d.endsWith(s));
}

/**
 * The agent on the panel server changes the certificate method of the panel domain. If the new
 * certificate does not come in time, the agent goes back to the previous method.
 */
export function setPanelCertificate(method: PanelCertificateMethod) {
  return call(() => live.request<live.ApiPanelCertificate>("PUT", "/panel-certificate", { method })).then((r) => {
    update((d) => { d.panelCertificate = live.toPanelCertificate(r); });
    return track(r.job);
  });
}

// The mesh: the encrypted tunnels between the servers
//
// Each change gives the jobs that send the new state to the servers. The page shows the
// result of each job, and the servers report the state of each tunnel each minute.

type MeshJobs = { jobs?: live.ApiJob[] };

/** Waits for the jobs of a change to the mesh, then reads the mesh again. */
const trackAll = (jobs: live.ApiJob[] = []) => (jobs.length > 0 ? Promise.all(jobs.map((job) => track(job))).then(() => undefined) : refresh());

/** Changes the address range or the UDP port of the mesh. Possible only while the mesh has no tunnel. */
export function setMesh(change: { range?: string; port?: number }) {
  return call(() => live.request<Mesh>("PATCH", "/mesh", change)).then((mesh) => update((d) => { d.mesh = mesh; }));
}

/**
 * Makes a tunnel between two servers. The promise is complete when the API accepts the
 * request, so that a dialog can show an error of the API. The jobs continue after that.
 */
export function makeTunnel(serverA: string, serverB: string) {
  return call(() => live.request<MeshJobs>("POST", "/mesh/tunnels", { servers: [serverA, serverB] })).then((r) => { void trackAll(r.jobs); });
}

/** Sends the mesh state to the two servers of a tunnel again. Each server then tests the tunnel. */
export function repairTunnel(tunnelId: string) {
  return call(() => live.request<MeshJobs>("POST", `/mesh/tunnels/${tunnelId}/repair`)).then((r) => trackAll(r.jobs));
}

export function removeTunnel(tunnelId: string) {
  return call(() => live.request<MeshJobs>("DELETE", `/mesh/tunnels/${tunnelId}`)).then((r) => trackAll(r.jobs));
}

/**
 * Sets the public IP address for the tunnel traffic of a server. Null: the panel finds the
 * address. The promise is complete when the API accepts the address.
 */
export function setTunnelAddress(serverId: string, tunnelAddress: string | null) {
  return call(() => live.request<Mesh & MeshJobs>("PATCH", `/mesh/servers/${serverId}`, { tunnelAddress })).then(({ jobs, ...mesh }) => {
    update((d) => { d.mesh = mesh; });
    void trackAll(jobs);
  });
}

/**
 * Makes a new tunnel key on a server. The private key does not leave the server. The tunnels
 * of the server do not operate until its peers have the new public key.
 */
export function rotateTunnelKey(serverId: string) {
  return call(() => live.request<JobAnswer>("POST", `/mesh/servers/${serverId}/rotate-key`)).then((r) => track(r.job));
}

// Customers, plans and branding (FR-102, FR-106, FR-202)

/** A new user or a new password. The panel shows the temporary password one time only. */
export interface NewAccess {
  email: string;
  temporaryPassword: string;
}

/**
 * Audit finding B5: with createUser, the panel also makes the first user of the customer. The
 * user has the email of the customer and a temporary password. The panel sends no email.
 */
export function addCustomer(input: Pick<Customer, "name" | "email" | "planId" | "role">, createUser = false): Promise<NewAccess | null> | NewAccess | null {
  const made = makeCustomer(input, createUser);
  return made instanceof Promise ? made.then((m) => m.access) : made.access;
}

/**
 * Makes a customer and gives its ID. The New website page uses it to make the customer and
 * the website in one step.
 */
export function makeCustomer(input: Pick<Customer, "name" | "email" | "planId" | "role">, createUser = false): Promise<{ customerId: string; access: NewAccess | null }> | { customerId: string; access: NewAccess | null } {
  return call(async () => {
    const r = await live.request<{ id: string; user?: { email: string; temporaryPassword: string } }>("POST", "/customers", { name: input.name.trim(), email: input.email.trim(), planId: input.planId, role: input.role, ...(createUser ? { createUser: true } : {}) });
    await reload();
    return { customerId: r.id, access: r.user ? { email: r.user.email, temporaryPassword: r.user.temporaryPassword } : null };
  });
}

// Users (audit finding B5)

/**
 * Second audit, finding F09: a change to an administrator needs the password of the signed-in
 * user and, when TOTP is on, a code. These are the fields that the API gets.
 */
function confirmation(confirm: Confirmation | undefined): { password?: string; totp?: string } {
  return confirm ? { password: confirm.password, totp: confirm.totp.trim() || undefined } : {};
}

/**
 * Makes a user for a customer. With no customer (null), the user is an administrator, and the
 * signed-in user must confirm with the password and a TOTP code.
 */
export function addUser(input: { name: string; email: string; customerId: string | null }, confirm?: Confirmation): Promise<NewAccess> | NewAccess {
  return call(async () => {
    const r = await live.request<live.ApiUser & { temporaryPassword: string }>("POST", "/users", { name: input.name.trim(), email: input.email.trim(), customerId: input.customerId, ...(input.customerId === null ? confirmation(confirm) : {}) });
    await reload();
    return { email: r.email, temporaryPassword: r.temporaryPassword };
  });
}

/**
 * Gives the user a new temporary password and signs the user out. With removeTotp, the user
 * can sign in with the password only, for example after the loss of a telephone.
 */
export function resetUserPassword(userId: string, removeTotp = false, confirm?: Confirmation): Promise<NewAccess> | NewAccess {
  return call(async () => {
    const r = await live.request<live.ApiUser & { temporaryPassword: string }>("POST", `/users/${userId}/reset-password`, { ...(removeTotp ? { removeTotp: true } : {}), ...confirmation(confirm) });
    await reload();
    return { email: r.email, temporaryPassword: r.temporaryPassword };
  });
}

/** With an administrator, the signed-in user must confirm with the password and a TOTP code. */
export function deleteUser(userId: string, confirm?: Confirmation) {
  return call(() => live.request("DELETE", `/users/${userId}`, confirm ? confirmation(confirm) : undefined)).then(() => reload());
}

export function setCustomer(customerId: string, change: Partial<Pick<Customer, "planId" | "suspended">>) {
  return call(() => live.request("PATCH", `/customers/${customerId}`, change)).then(() => refresh());
}

export function savePlan(plan: Plan) {
  const exists = state.plans.some((p) => p.id === plan.id);
  const body = { name: plan.name.trim(), websites: plan.websites, mailboxes: plan.mailboxes, databases: plan.databases, phpExtensions: plan.phpExtensions, limits: plan.limits, databaseServers: plan.databaseServers, databaseLimits: plan.databaseLimits, dnsZones: plan.dnsZones };
  return call(() => live.request(exists ? "PATCH" : "POST", exists ? `/plans/${plan.id}` : "/plans", body)).then(() => refresh());
}

// Settings (FR-103, FR-104, FR-1303, FR-1813)

/** CF-02: a new token needs the password of the user and, if set, a TOTP code. */
export function createToken(name: string, scopes: TokenScope[], days: number, password: string, totp = ""): Promise<{ token: ApiToken; secret: string }> | { token: ApiToken; secret: string } {
  return call(async () => {
    const r = await live.request<{ id: number; token: string; scopes: TokenScope[]; expiresAt: string }>("POST", "/tokens", { name: name.trim(), scopes, expiresInDays: days, password, totp: totp.trim() || undefined });
    await refresh();
    const token: ApiToken = { id: String(r.id), name: name.trim(), scopes: r.scopes, createdAt: now(), expiresAt: r.expiresAt, prefix: `${r.id}|` };
    return { token, secret: r.token };
  });
}

export function revokeToken(tokenId: string) {
  return call(() => live.request("DELETE", `/tokens/${tokenId}`)).then(() => refresh());
}

/** R-01: the key for a new authenticator app. TOTP is on only after setTotp(true, …). */
export function startTotpSetup(): Promise<{ secret: string; uri: string }> | { secret: string; uri: string } {
  return call(() => live.request<{ secret: string; uri: string }>("POST", "/me/totp/setup"));
}

/** R-01: turns TOTP on or off. Each change needs the password and a code from the app. */
export function setTotp(enabled: boolean, code: string, password: string) {
  const errors: Record<string, string> = {};
  if (!password) errors.password = "Type your password.";
  if (!/^\d{6}$/.test(code.trim())) errors.code = "Type the 6-digit code from the app.";
  if (Object.keys(errors).length) throw new ApiError(Object.values(errors)[0], errors);
  return call(async () => {
    await live.request("POST", enabled ? "/me/totp/enable" : "/me/totp/disable", { password, code: code.trim() });
    await refresh();
  });
}

/**
 * R-01: changes the password. The panel then signs out the other sessions of the user and
 * deletes the API tokens of the user (audit finding S8). It also ends the state of a temporary
 * password (audit finding B5).
 */
export function changePassword(current: string, next: string, confirm: string, totp = "") {
  const errors: Record<string, string> = {};
  if (!current) errors.currentPassword = "Type your current password.";
  if (next.length < 12) errors.password = "Use 12 or more characters.";
  else if (next === current) errors.password = "Use a password that is different from the current password.";
  if (next !== confirm) errors.password_confirmation = "The passwords are not the same.";
  if (state.totpEnabled && !/^\d{6}$/.test(totp.trim())) errors.totp = "Type the 6-digit code from the app.";
  if (Object.keys(errors).length) throw new ApiError(Object.values(errors)[0], errors);
  return call(async () => {
    await live.request("PUT", "/me/password", { currentPassword: current, password: next, password_confirmation: confirm, totp: totp.trim() || undefined });
    await reload();
  });
}
