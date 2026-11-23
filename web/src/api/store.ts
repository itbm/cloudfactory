// The state of the panel and the actions that change it.
//
// Each action calls the panel API through live.ts, and the state comes from the API. The flow
// of a change is in spec section 6.3: the API checks the input, writes the desired state, and
// puts a job in the queue. The agent runs the job, and the panel reads the result.
//
// The panel has no sample data of its own. For development, the demonstration and the
// screenshots, the panel API has a demo seeder and a demo agent (docs/development.md).

import { BUILT_IN_ENGINES } from "../lib/labels";
import * as live from "./live";
import type {
  ApiToken,
  BackupPolicy,
  BrandFields,
  Confirmation,
  CommandLogPage,
  CronJob,
  FirewallEvent,
  MalwareScan,
  MalwareScans,
  MalwareScanSchedule,
  Customer,
  CustomerCertificate,
  DatabaseEngine,
  DatabaseLimits,
  DatabasePrivileges,
  DatabaseServer,
  DnsProvider,
  DnsRecordType,
  Deploy,
  DeployTrigger,
  Preview,
  Previews,
  MailAlias,
  DomainKind,
  FileContent,
  FileEntry,
  FileList,
  HostUpdatePolicy,
  Job,
  LogLine,
  Mesh,
  Pack,
  PackManifest,
  PackSigner,
  Plan,
  Plugin,
  PluginCredentials,
  PluginDelivery,
  TrustedKey,
  PluginManifest,
  PluginActionResult,
  PluginPlacement,
  PluginPageSession,
  ResellerBranding,
  ServerRole,
  State,
  PanelCertificateMethod,
  TokenScope,
  UpdatePolicy,
  Website,
  TerminalOpening,
  TerminalSession,
  TerminalSessionPage,
  WebsiteCommand,
  WebsiteKind,
  WebsiteSecurity,
  WebsiteSecurityChange,
  UsageAlert,
  UsageAlertSettings,
  UsageHistory,
  UsageRange,
  UsageSubject,
  MailCluster,
  WordPressAdmin,
  WordPressAutoUpdates,
  WordPressSite,
  WordPressState,
  WpCliCommand,
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
    databaseServers: [], databaseEngines: BUILT_IN_ENGINES, dnsZones: [], certificates: [], nameServers: [], dnsAccounts: [], dnsProviders: [], networks: [], pluginPages: [], pluginActions: [], usageAlerts: [], alertSettings: { warningPercent: 80, criticalPercent: 95 }, mailDomains: [], mailClusters: [], mailboxes: [], mailAliases: [], databases: [], databaseUsers: [], remoteDatabaseServers: {}, snapshots: [], plans: [], customers: [], users: [],
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

/**
 * Adversarial review, finding W1-01: the number of the sign-in session. A sign-in, a sign-out
 * and an answer "not signed in" change it. A load that started in a different session does
 * not change the state: its data is of the user who signed out.
 */
let session = 0;

/** Loads the state from the API again. Calls at the same time share one load. */
export function refresh(): Promise<void> {
  if (!live.hasSession()) return Promise.resolve();
  const started = session;
  loading ??= live.load(state).then((next) => {
    if (started === session) replace(next);
  })
    .catch((e: unknown) => {
      // Keep the data that the panel has, and tell the user. Before, a failed first load
      // showed an empty panel with no message.
      if (!state.session.signedIn || started !== session) return;
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
  // The wait updates only the job. The panel loads all data again when the job ends. A sign-out
  // or a sign-in of a different user stops the wait: the job is of the user who signed out.
  const started = session;
  return live.waitForJob(job, (j) => update((d) => {
    d.jobs = d.jobs.map((x) => (x.id === j.id ? j : x));
  }), () => started === session).then(async (done) => {
    if (started === session) await refresh();
    return done;
  });
}

/** Batch 1, finding 9: with many websites, reads the databases and snapshots of a website when the user opens it. */
export function showWebsite(websiteId: string) {
  const started = session;
  void live.showWebsite(state, websiteId).then((apply) => apply && started === session && replace(apply(state))).catch(() => {});
}

/** True when the panel reads the databases and snapshots only of the websites that the user opened. */
export const partialDetails = () => state.websites.length > live.DETAIL_LIMIT;

const q = (s: string) => encodeURIComponent(s);
type JobAnswer = { job: live.ApiJob | null };

live.setUnauthorisedHandler(() => {
  session++;
  loading = null;
  replace(emptyState());
});

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
    session++;
    loading = null;
    replace({ ...state, session: { ...state.session, signedIn: true, loading: true } });
    await refresh();
  });
}

/** Asks for an email with a link to set a new password. */
export function requestPasswordReset(email: string) {
  return call(() => live.requestPasswordReset(email));
}

/** Sets a new password with the code from the link in the email. The user then signs in. */
export function resetPassword(email: string, token: string, password: string, confirmation: string) {
  return call(() => live.resetPassword(email, token, password, confirmation));
}

/** FR-103: signs in with a passkey, in place of the password and the TOTP code. */
export function signInWithPasskey() {
  return call(async () => {
    await live.signInWithPasskey();
    session++;
    loading = null;
    replace({ ...state, session: { ...state.session, signedIn: true, loading: true } });
    await refresh();
  });
}

export const listPasskeys = () => call(() => live.listPasskeys());
export const addPasskey = (name: string, password: string, totp: string) => call(() => live.addPasskey(name, password, totp));
export const renamePasskey = (id: string, name: string) => call(() => live.renamePasskey(id, name));
export const removePasskey = (id: string, password: string, totp: string) => call(() => live.removePasskey(id, password, totp));

/** Single sign-on: changes the ticket from the provider into a session, with a TOTP code when TOTP is on. */
export function completeSignOn(ticket: string, totp = "") {
  return call(async () => {
    await live.completeSignOn(ticket, totp);
    session++;
    loading = null;
    replace({ ...state, session: { ...state.session, signedIn: true, loading: true } });
    await refresh();
  });
}

export const listIdentities = () => call(() => live.listIdentities());
export const connectIdentity = (providerId: string, password: string, totp: string) => call(() => live.connectIdentity(providerId, password, totp));
export const disconnectIdentity = (providerId: string, password: string, totp: string) => call(() => live.disconnectIdentity(providerId, password, totp));

export function signOut() {
  live.signOut();
  session++;
  loading = null;
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
  /** The start command of a worker, or of a Node.js or Python website. */
  command?: string;
  /** The install command of a Node.js or Python website. Empty: no install step. */
  installCommand?: string;
  /** True for a Node.js or Python image. */
  app?: boolean;
  /** FR-206: install WordPress in the new website. */
  wordpress?: WordPressInstall;
}

/** The site and the first administrator of a new WordPress site. */
export interface WordPressInstall {
  title: string;
  adminUser: string;
  adminEmail: string;
  locale: string;
}

const wordpressBody = (w: WordPressInstall) => ({ title: w.title.trim(), adminUser: w.adminUser.trim(), adminEmail: w.adminEmail.trim(), locale: w.locale || undefined });

export function createWebsite(input: NewWebsite): Promise<{ websiteId: string; job: Promise<Job>; wordpress?: WordPressAdmin }> {
  return call(async () => {
    const r = await live.request<{ website: live.ApiWebsite; job: live.ApiJob; wordpress?: { admin: WordPressAdmin } }>("POST", "/websites", {
      // CF-07: only an administrator selects the plan here. The panel uses the plan of the customer.
      // A private website and a worker have no domain: the name is a short name.
      kind: input.kind && input.kind !== "public" ? input.kind : undefined,
      command: input.kind === "worker" || input.app ? input.command?.trim() || undefined : undefined,
      installCommand: input.app && input.installCommand !== undefined ? input.installCommand.trim() : undefined,
      name: input.name.trim().toLowerCase(), application: input.application, imageId: input.imageId, planId: state.session.contextId === "platform" || state.session.user.role === "administrator" ? input.planId : undefined,
      serverId: input.serverId || undefined, customerId: input.customerId && input.customerId !== "platform" ? input.customerId : undefined,
      wordpress: input.wordpress ? wordpressBody(input.wordpress) : undefined,
    });
    update((d) => {
      d.websites = [live.toWebsite(r.website), ...d.websites.filter((w) => w.id !== r.website.id)];
    });
    return { websiteId: r.website.id, job: track(r.job), wordpress: r.wordpress?.admin };
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

/** The start command of a worker, or of a Node.js or Python website. The panel starts a new container with the command. */
export function setCommand(websiteId: string, command: string) {
  return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { command: command.trim() })).then((r) => track(r.job));
}

/** The install command of a Node.js or Python website. Empty: no install step. */
export function setInstallCommand(websiteId: string, installCommand: string) {
  return call(() => live.request<Partial<JobAnswer>>("PUT", `/websites/${websiteId}/runtime`, { installCommand: installCommand.trim() })).then(async (r) => {
    await refresh();
    if (r.job) await track(r.job);
  });
}

/** Runs the install command in a build container, then starts the website again with no stop. */
export function installDependencies(websiteId: string) {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/runtime/install`)).then((r) => track(r.job));
}

/**
 * Sets and removes environment variables. A value of null removes the variable. The panel
 * starts a new container with the variables, with no stop.
 */
export function setEnvironment(websiteId: string, variables: Record<string, string | null>) {
  return call(() => live.request<{ website: live.ApiWebsite; job: live.ApiJob | null }>("PATCH", `/websites/${websiteId}/environment`, { variables })).then(async (r) => {
    update((d) => {
      d.websites = d.websites.map((w) => (w.id === websiteId ? live.toWebsite(r.website) : w));
    });
    if (r.job) await track(r.job);
  });
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
  // Adversarial review, finding W2-01: the API removes the white space at the start and at the
  // end of each text value. Thus the text of a file goes as base64, and the file stays as the
  // user wrote it, with its last new line.
  const body = options.encoding === "base64" ? { path, content, ...options } : { path, content: toBase64(content), ...options, encoding: "base64" as const };
  return call(() => live.request<{ file: FileEntry }>("PUT", `${filesOf(websiteId)}/content`, body)).then((r) => r.file);
}

function toBase64(text: string): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(text)) binary += String.fromCharCode(byte);
  return btoa(binary);
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

/** The usage history of a website, a server or a database server, for the graphs. */
export function readUsage(subject: UsageSubject, id: string, range: UsageRange): Promise<UsageHistory> {
  // The ID can come from the address of the page (finding W1-13).
  const base = subject === "website" ? `/websites/${q(id)}` : subject === "server" ? `/servers/${q(id)}` : `/database-servers/${q(id)}`;
  return call(() => live.request<UsageHistory>("GET", `${base}/usage?range=${range}`));
}

/** The usage alerts, also the closed ones with state "all". */
export function listUsageAlerts(state: "open" | "resolved" | "all" = "open"): Promise<UsageAlert[]> {
  return call(() => live.request<{ data: UsageAlert[] }>("GET", `/usage-alerts?state=${state}`)).then((r) => r.data);
}

/** An administrator changes the thresholds of the usage alerts. */
export async function setAlertSettings(settings: UsageAlertSettings): Promise<void> {
  await call(() => live.request("PUT", "/usage-alerts/settings", settings));
  await refresh();
}

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

/** FR-206: the WordPress site of a website, or null, and the last WP-CLI commands. */
export function readWordPress(websiteId: string): Promise<WordPressState> {
  return call(() => live.request<WordPressState>("GET", `/websites/${websiteId}/wordpress`));
}

/** Installs WordPress on an empty website. The API gives the password of the administrator one time. */
export function installWordPress(websiteId: string, input: WordPressInstall): Promise<{ admin: WordPressAdmin; job: Promise<Job> }> {
  return call(() => live.request<{ wordpress: WordPressSite; admin: WordPressAdmin; job: live.ApiJob }>("POST", `/websites/${websiteId}/wordpress`, wordpressBody(input)))
    .then((r) => ({ admin: r.admin, job: track(r.job) }));
}

/** Reads the versions and the updates. On a website with no WordPress record, the agent looks for WordPress. */
export function checkWordPress(websiteId: string): Promise<Job> {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/wordpress/check`)).then((r) => track(r.job));
}

export interface WordPressUpdateInput {
  core?: "minor" | "latest" | null;
  plugins?: string[];
  themes?: string[];
  /** Each available update. */
  all?: boolean;
}

/** Updates WordPress, plugins and themes, with a restore point and a health check. */
export function updateWordPress(websiteId: string, input: WordPressUpdateInput): Promise<Job> {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/wordpress/updates`, input)).then((r) => track(r.job));
}

/** Puts back the restore point of the last update: the files with no uploads, and the database. */
export function restoreWordPress(websiteId: string): Promise<Job> {
  return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/wordpress/restore`)).then((r) => track(r.job));
}

export function setWordPressUpdates(websiteId: string, autoUpdates: Partial<WordPressAutoUpdates>): Promise<WordPressSite> {
  return call(() => live.request<{ wordpress: WordPressSite }>("PATCH", `/websites/${websiteId}/wordpress`, { autoUpdates })).then((r) => r.wordpress);
}

/**
 * Splits a WP-CLI command line into arguments, as a shell does: quotes keep spaces, and a
 * backslash keeps the next character. The panel sends each argument to WP-CLI with no shell.
 * The word "wp" at the start is optional.
 */
export function wpCliArguments(line: string): string[] {
  const args: string[] = [];
  let current = "";
  let quote: string | null = null;
  let has = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote) {
      if (c === quote) quote = null;
      else if (c === "\\" && quote === '"' && i + 1 < line.length) current += line[++i];
      else current += c;
    } else if (c === "'" || c === '"') {
      quote = c;
      has = true;
    } else if (c === "\\" && i + 1 < line.length) {
      current += line[++i];
      has = true;
    } else if (/\s/.test(c)) {
      if (has || current) args.push(current);
      current = "";
      has = false;
    } else {
      current += c;
    }
  }
  if (quote) throw new ApiError("A quote has no end.", { args: "A quote has no end." });
  if (has || current) args.push(current);
  return args[0] === "wp" ? args.slice(1) : args;
}

/** Runs one WP-CLI command. The promise gives the command when its job ends. */
export function runWpCli(websiteId: string, line: string): Promise<{ command: WpCliCommand; job: Promise<Job> }> {
  return call(async () => {
    const args = wpCliArguments(line);
    const r = await live.request<{ command: WpCliCommand; job: live.ApiJob }>("POST", `/websites/${websiteId}/wordpress/cli`, { args });
    return { command: r.command, job: track(r.job) };
  });
}

// The protection of a website in the edge proxy

type SecurityAnswer = { security: WebsiteSecurity; job: live.ApiJob | null };

export function readSecurity(websiteId: string): Promise<WebsiteSecurity> {
  return call(() => live.request<{ security: WebsiteSecurity }>("GET", `/websites/${websiteId}/security`)).then((r) => r.security);
}

/** Changes the protection. The panel saves the change at once, and a job applies it to the edge proxy. */
export function setSecurity(websiteId: string, change: WebsiteSecurityChange): Promise<WebsiteSecurity> {
  return call(() => live.request<SecurityAnswer>("PATCH", `/websites/${websiteId}/security`, change)).then((r) => {
    if (r.job) void track(r.job);
    return r.security;
  });
}

/** The last requests that the firewall blocked or detected, the newest first. */
export function listFirewallEvents(websiteId: string): Promise<FirewallEvent[]> {
  return call(() => live.request<{ events: FirewallEvent[] }>("GET", `/websites/${websiteId}/security/events`)).then((r) => r.events);
}

/**
 * A page of the command log of a website. The search finds the commands with the text. With a
 * session, only the commands of that session of the web terminal.
 */
export function listCommands(websiteId: string, page = 1, search = "", sessionId = ""): Promise<CommandLogPage> {
  const query = [page > 1 ? `page=${page}` : "", search.trim() ? `q=${q(search.trim())}` : "", sessionId ? `sessionId=${q(sessionId)}&perPage=200` : ""].filter(Boolean).join("&");
  return call(() => live.request<CommandLogPage>("GET", `/websites/${websiteId}/commands${query ? `?${query}` : ""}`));
}

/** One entry of the command log, with the end of the output. */
export function readCommand(websiteId: string, commandId: string): Promise<WebsiteCommand> {
  return call(() => live.request<{ command: WebsiteCommand }>("GET", `/websites/${websiteId}/commands/${commandId}`)).then((r) => r.command);
}

/**
 * Runs one command line in the container of the website. The promise resolves with the entry
 * of the command log when the command has ended.
 */
export async function runCommand(websiteId: string, command: string, timeoutSeconds?: number): Promise<WebsiteCommand> {
  const r = await call(() => live.request<{ command: WebsiteCommand; job: live.ApiJob | null }>("POST", `/websites/${websiteId}/commands`, {
    command: command.trim(), ...(timeoutSeconds ? { timeoutSeconds } : {}),
  }));
  await track(r.job);
  return readCommand(websiteId, r.command.id);
}

// The malware scans of a website

export function readMalwareScans(websiteId: string): Promise<MalwareScans> {
  return call(() => live.request<MalwareScans>("GET", `/websites/${websiteId}/malware-scans`));
}

/** Starts a scan now. The job updates the signatures of the server, then scans the files. */
export function startMalwareScan(websiteId: string): Promise<MalwareScan> {
  return call(() => live.request<{ scan: MalwareScan; job: live.ApiJob }>("POST", `/websites/${websiteId}/malware-scans`)).then((r) => {
    void track(r.job);
    return r.scan;
  });
}

export function setMalwareScanSchedule(websiteId: string, schedule: MalwareScanSchedule): Promise<MalwareScans> {
  return call(() => live.request<MalwareScans>("PATCH", `/websites/${websiteId}/malware-scans`, { schedule }));
}

/** The sessions of the web terminal of a website, the newest first. */
export function listTerminalSessions(websiteId: string, page = 1): Promise<TerminalSessionPage> {
  return call(() => live.request<TerminalSessionPage>("GET", `/websites/${websiteId}/terminal-sessions${page > 1 ? `?page=${page}` : ""}`));
}

export function readTerminalSession(websiteId: string, sessionId: string): Promise<TerminalSession> {
  return call(() => live.request<{ session: TerminalSession }>("GET", `/websites/${websiteId}/terminal-sessions/${sessionId}`)).then((r) => r.session);
}

/**
 * Opens the web terminal of a website. The server starts the shell when the browser connects to
 * the WebSocket of the answer. The job of the server does not block: the WebSocket tells when the
 * shell is ready.
 */
export function openTerminal(websiteId: string, cols: number, rows: number): Promise<TerminalOpening> {
  return call(() => live.request<TerminalOpening & { job: live.ApiJob | null }>("POST", `/websites/${websiteId}/terminal-sessions`, { cols, rows })).then((r) => {
    // The job shows in the list of jobs. It fails when the terminal does not open.
    void track(r.job).catch(() => {});
    return { session: r.session, path: r.path, ticket: r.ticket };
  });
}

/** The recording of a session of the web terminal, in the asciicast v2 format. */
export function readTerminalRecording(websiteId: string, sessionId: string): Promise<string> {
  return call(() => live.requestText(`/websites/${websiteId}/terminal-sessions/${sessionId}/recording`));
}

export function readDeploy(websiteId: string): Promise<Deploy> {
  return call(() => live.request<Deploy>("GET", `/websites/${websiteId}/deploy`));
}

export interface GitSourceInput {
  repository: string;
  branch: string;
  webDirectory: string;
  trigger: DeployTrigger;
  checkMinutes: number;
  buildCommands: string[];
  migrationCommand: string;
}

/** Connects a Git repository to the website, or changes the connection. */
export function setGitSource(websiteId: string, input: GitSourceInput): Promise<Deploy> {
  const body = {
    ...input,
    repository: input.repository.trim(),
    branch: input.branch.trim(),
    webDirectory: input.webDirectory.trim() || ".",
    buildCommands: input.buildCommands.map((c) => c.trim()).filter(Boolean),
    migrationCommand: input.migrationCommand.trim() || null,
  };
  return call(() => live.request<Deploy>("PUT", `/websites/${websiteId}/deploy`, body));
}

/** Makes a new webhook address and secret. The old address stops at once. */
export function newWebhook(websiteId: string): Promise<Deploy> {
  return call(() => live.request<Deploy>("POST", `/websites/${websiteId}/deploy/webhook`));
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

// The preview websites of pull requests

export function readPreviews(websiteId: string): Promise<Previews> {
  return call(() => live.request<Previews>("GET", `/websites/${websiteId}/previews`));
}

export interface PreviewSettingsInput {
  enabled: boolean;
  domain: string;
  limit: number;
  copyEnvironment: boolean;
}

/** Turns the previews on or off, or changes their settings. The panel removes the previews when they go off. */
export async function setPreviewSettings(websiteId: string, input: PreviewSettingsInput): Promise<Previews> {
  const body = { ...input, domain: input.domain.trim() || null };
  const answer = await call(() => live.request<Previews>("PUT", `/websites/${websiteId}/previews`, body));
  if (!input.enabled) void reload();
  return answer;
}

export interface PreviewAnswer {
  status: "started" | "queued" | "waiting" | "removing";
  message: string;
  preview: Preview;
}

/** Makes the preview of a pull request, or deploys it again. */
export async function makePreview(websiteId: string, input: { number: number; branch: string; title?: string }): Promise<PreviewAnswer> {
  const answer = await call(() => live.request<PreviewAnswer>("POST", `/websites/${websiteId}/previews`, { ...input, branch: input.branch.trim() }));
  // A new preview is a new website.
  void reload();
  return answer;
}

/** Removes the preview of a pull request. The job promise resolves when the preview website is gone. */
export function removePreview(websiteId: string, number: number): Promise<{ job: Promise<Job> }> {
  return call(() => live.request<{ job: live.ApiJob | null }>("DELETE", `/websites/${websiteId}/previews/${number}`)).then((r) => ({ job: track(r.job) }));
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
  /** The version of a pack engine. With no version, the database server gets the newest version of the pack. */
  version?: string;
}

/** Makes a database server. The promise resolves when the panel has put the job in the queue. */
export function createDatabaseServer(input: DatabaseServerInput): Promise<void> {
  return call(async () => {
    const body = { engine: input.engine, customerId: input.customerId, name: input.name?.trim() || undefined, planId: input.planId || undefined, serverId: input.serverId || undefined, limits: input.limits, version: input.version || undefined };
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

/**
 * Moves a database server to a different server. Only an administrator. The data goes through
 * the tunnel between the two servers, and the database server keeps its name and its address.
 */
export function moveDatabaseServer(id: string, serverId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<{ databaseServer: live.ApiDatabaseServer; jobs: live.ApiJob[] }>("POST", `/database-servers/${id}/move`, { serverId });
    putDatabaseServer(r.databaseServer);
    void Promise.all(r.jobs.map((j) => track(j))).then(() => refresh());
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

// Email

/** The DNS record that shows who controls a domain. The panel asks for it when a different customer can own the domain. */
export interface MailVerification { type: string; name: string; value: string }

/** The error of a new mail domain that needs a DNS record first. */
export class MailVerificationError extends ApiError {
  constructor(message: string, readonly verification: MailVerification) {
    super(message, {});
  }
}

/** Adds a mail domain. The job makes the DKIM keys, and then the domain has its DNS records. */
export function createMailDomain(input: { name: string; customerId?: string; serverId?: string }): Promise<void> {
  return call(async () => {
    try {
      const r = await live.request<{ mailDomain: live.ApiMailDomain; job: live.ApiJob }>("POST", "/mail/domains", { name: input.name.trim().toLowerCase(), customerId: input.customerId || undefined, serverId: input.serverId || undefined });
      update((d) => { d.mailDomains = [...d.mailDomains.filter((x) => x.id !== r.mailDomain.id), live.toMailDomain(r.mailDomain)]; });
      void track(r.job).then(() => refresh());
    } catch (e) {
      const verification = e instanceof live.LiveError ? (e.body as { verification?: MailVerification } | undefined)?.verification : undefined;
      if (verification) throw new MailVerificationError((e as Error).message, verification);
      throw e;
    }
  });
}

/** Reads the DNS records of a mail domain now. */
export function checkMailDomain(domainId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<live.ApiMailDomain>("POST", `/mail/domains/${domainId}/check`);
    update((d) => { d.mailDomains = d.mailDomains.map((x) => (x.id === domainId ? live.toMailDomain(r) : x)); });
  });
}

/** Adds a mail domain again after its first job failed. */
export function retryMailDomain(domainId: string): Promise<void> {
  return call(async () => {
    const r = await live.request<{ mailDomain: live.ApiMailDomain; job: live.ApiJob }>("POST", `/mail/domains/${domainId}/retry`);
    update((d) => { d.mailDomains = d.mailDomains.map((x) => (x.id === domainId ? live.toMailDomain(r.mailDomain) : x)); });
    void track(r.job).then(() => refresh());
  });
}

/** Marks a row "removing" until the job is complete. */
function removing(list: "mailDomains" | "mailboxes" | "mailAliases", id: string, job: live.ApiJob | null) {
  update((d) => {
    const x = d[list].find((y) => y.id === id);
    if (x) x.status = "removing";
  });
  void track(job).then(() => refresh());
}

/** Removes a mail domain that has no mailboxes and no addresses. */
export function deleteMailDomain(domainId: string): Promise<void> {
  return call(async () => removing("mailDomains", domainId, (await live.request<JobAnswer>("DELETE", `/mail/domains/${domainId}`)).job));
}

/** Makes a mailbox. The answer has the password: the panel shows it one time. */
export function createMailbox(input: { domainId: string; localPart: string; quotaBytes: number; encrypt?: boolean }): Promise<string> {
  return call(async () => {
    const r = await live.request<{ mailbox: live.ApiMailbox; password: string; job: live.ApiJob }>("POST", "/mail/mailboxes", { ...input, localPart: input.localPart.trim().toLowerCase() });
    update((d) => { d.mailboxes = [...d.mailboxes, live.toMailbox(r.mailbox)]; });
    void track(r.job).then(() => refresh());
    return r.password;
  });
}

/** Changes the size of a mailbox, or gives it a new password. The answer is the new password, or "". */
export function setMailbox(mailboxId: string, change: { quotaBytes?: number; newPassword?: boolean; currentPassword?: string }): Promise<string> {
  return call(async () => {
    const r = await live.request<{ mailbox: live.ApiMailbox; password: string | null; job: live.ApiJob | null }>("PATCH", `/mail/mailboxes/${mailboxId}`, change);
    update((d) => { d.mailboxes = d.mailboxes.map((x) => (x.id === mailboxId ? live.toMailbox(r.mailbox) : x)); });
    if (r.job) void track(r.job).then(() => refresh());
    return r.password ?? "";
  });
}

/**
 * Turns on the encryption of a mailbox with an OpenPGP public key, or turns it off with null.
 * The mail server reads the key. The promise fails when the mail server refuses the key.
 */
export function setMailboxEncryption(mailboxId: string, publicKey: string | null): Promise<void> {
  return call(async () => {
    const r = await live.request<{ mailbox: live.ApiMailbox; job: live.ApiJob | null }>("PATCH", `/mail/mailboxes/${mailboxId}`, { publicKey });
    update((d) => { d.mailboxes = d.mailboxes.map((x) => (x.id === mailboxId ? live.toMailbox(r.mailbox) : x)); });
    if (!r.job) return;
    const done = await track(r.job);
    if (done.state === "failed") {
      const reason = done.error?.replace(/^.*the mail server refused the public key: /, "") ?? "";
      throw new ApiError(reason ? `The mail server refused the key. ${reason}` : "The mail server could not change the encryption. Try again.");
    }
  });
}

/**
 * Turns on the encryption of a mailbox with keys that the panel makes. The mailbox gets a new
 * password, and the private key gets the same password. The answer is the new password.
 */
export function encryptMailbox(mailboxId: string): Promise<string> {
  return call(async () => {
    const r = await live.request<{ mailbox: live.ApiMailbox; password: string | null; job: live.ApiJob | null }>("PATCH", `/mail/mailboxes/${mailboxId}`, { encrypt: true, newPassword: true });
    if (r.job && (await track(r.job)).state === "failed") {
      throw new ApiError("The mail server could not turn on the encryption. Try again.");
    }
    await refresh();
    return r.password ?? "";
  });
}

/** Gets the private keys that the panel made for a mailbox. A key needs the password of the mailbox. */
export function mailboxPrivateKey(mailboxId: string): Promise<string> {
  return call(async () => (await live.request<{ privateKey: string }>("GET", `/mail/mailboxes/${mailboxId}/private-key`)).privateKey);
}

/** Removes a mailbox and its messages. */
export function deleteMailbox(mailboxId: string): Promise<void> {
  return call(async () => removing("mailboxes", mailboxId, (await live.request<JobAnswer>("DELETE", `/mail/mailboxes/${mailboxId}`)).job));
}

/** Makes an alias or a forwarder. */
export function createMailAlias(input: { domainId: string; localPart: string; kind: MailAlias["kind"]; destinations: string[] }): Promise<void> {
  return call(async () => {
    const r = await live.request<{ mailAlias: MailAlias; job: live.ApiJob }>("POST", "/mail/aliases", { ...input, localPart: input.localPart.trim().toLowerCase() });
    update((d) => { d.mailAliases = [...d.mailAliases, r.mailAlias]; });
    void track(r.job).then(() => refresh());
  });
}

export function deleteMailAlias(aliasId: string): Promise<void> {
  return call(async () => removing("mailAliases", aliasId, (await live.request<JobAnswer>("DELETE", `/mail/aliases/${aliasId}`)).job));
}

/** Turns on email for a server, or changes the host name of its mail server. */
/** A member of a new mail cluster. The endpoint is the public address of the server. */
export interface NewClusterMember { serverId: string; hostname: string; endpoint: string; region: number }

/** Makes a mail cluster. The panel makes it in steps, and the page shows each step. */
export function createMailCluster(name: string, hostname: string, members: NewClusterMember[]) {
  return call(() => live.request<MailCluster>("POST", "/mail-clusters", {
    name: name.trim().toLowerCase(), hostname: hostname.trim().toLowerCase(),
    members: members.map((m) => ({ serverId: m.serverId, hostname: m.hostname.trim().toLowerCase(), endpoint: m.endpoint.trim(), region: m.region })),
  })).then(async (c) => { await refresh(); return c; });
}

/** Starts the step of a mail cluster that failed again. */
export function retryMailCluster(clusterId: string) {
  return call(() => live.request<MailCluster>("POST", `/mail-clusters/${clusterId}/retry`)).then(() => refresh());
}

export function setServerMail(serverId: string, hostname: string) {
  return call(() => live.request<JobAnswer>("PUT", `/servers/${serverId}/mail`, { hostname: hostname.trim().toLowerCase() })).then((r) => track(r.job).then(() => refresh()));
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

/** The state of a sign-in to Adminer (GET /v1/adminer-logins/{id}). */
export interface AdminerLogin {
  id: string;
  status: "starting" | "ready" | "failed" | "expired";
  url: string;
  databaseName: string;
  username: string;
  expiresAt: string;
}

/** The fields of the sign-in form of Adminer, with the password of the temporary database user. */
export interface AdminerForm {
  driver: string;
  server: string;
  username: string;
  password: string;
  db: string;
}

/** The time between two checks of a sign-in, and the longest wait for a new Adminer container. */
export const ADMINER_POLL_MS = 1500;
const ADMINER_WAIT_MS = 4 * 60 * 1000;

/**
 * Opens a database in Adminer, with single sign-on. The panel makes a temporary database user
 * for the database, and starts the Adminer of the customer if necessary. When the two are
 * ready, the sign-in form goes to Adminer in the window "target". The user types no password,
 * and the password is never on the screen.
 */
export async function openInAdminer(databaseId: string, target: Window, privileges: DatabasePrivileges = "readwrite"): Promise<void> {
  const db = state.databases.find((x) => x.id === databaseId);
  if (!db) throw new ApiError("The database does not exist.");
  const r = await call(() => live.request<{ login: AdminerLogin; form: AdminerForm; jobs: live.ApiJob[] }>("POST", `/websites/${db.websiteId}/databases/${databaseId}/adminer`, { privileges }));
  for (const job of r.jobs) void track(job).catch(() => undefined);
  let login = r.login;
  const until = Date.now() + ADMINER_WAIT_MS;
  while (login.status === "starting") {
    if (Date.now() > until) throw new ApiError("Adminer did not start in time. Look at the job on the Jobs page, and try again.");
    await new Promise((done) => setTimeout(done, ADMINER_POLL_MS));
    login = (await call(() => live.request<{ login: AdminerLogin }>("GET", `/adminer-logins/${login.id}`))).login;
  }
  if (login.status !== "ready") throw new ApiError(login.status === "expired" ? "The sign-in expired. Open the database again." : "The panel could not start Adminer, or could not make the temporary database user. Look at the job on the Jobs page.");
  postAdminerForm(target, login.url, r.form);
}

/**
 * Sends the sign-in form to Adminer in a window of the panel. The form is a POST request, so
 * the password is not in an address or in the history of the browser.
 */
export function postAdminerForm(target: Window, url: string, form: AdminerForm) {
  const doc = target.document;
  const el = doc.createElement("form");
  el.method = "post";
  el.action = url;
  for (const [key, value] of Object.entries(form)) {
    const input = doc.createElement("input");
    input.type = "hidden";
    input.name = `auth[${key}]`;
    input.value = value;
    el.appendChild(input);
  }
  doc.body.appendChild(el);
  el.submit();
}

/**
 * FR-307: adds a custom image. The API checks the reference, the tag and the port. With
 * `runtime`, the image is for a runtime from a manifest of the agent, for example Go: its
 * websites run their start command, and customers can select it.
 */
/** Adds an own OCI image. A new runtime comes from a pack (installPack), not from an image. */
export function addImage(name: string, reference: string, tag: string, policy: UpdatePolicy, run: { containerPort?: string; command?: string } = {}) {
  const body = {
    name: name.trim(), reference: reference.trim(), tag: tag.trim(), policy,
    containerPort: run.containerPort?.trim() ? Number(run.containerPort) : null, command: run.command?.trim() ? splitCommand(run.command) : null,
  };
  return call(() => live.request("POST", "/images", body)).then(() => refresh()).then(() => undefined);
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

// Private networks

/** The members of a network: the IDs of its websites and of its database servers. */
export interface NetworkMembers { websiteIds: string[]; databaseServerIds: string[] }

const networkName = (name: string) => name.trim().toLowerCase();

/** Makes a named network for a customer. The plan of the customer gives the number of them. */
export function createNetwork(name: string, customerId: string, members: NetworkMembers): Promise<void> {
  return call(async () => {
    await live.request("POST", "/networks", { name: networkName(name), customerId: customerId || undefined, ...members });
    await refresh();
  });
}

/** Changes the members of a network and, for a named network, its name. */
export function saveNetwork(networkId: string, name: string | undefined, members: NetworkMembers): Promise<void> {
  return call(async () => {
    await live.request("PATCH", `/networks/${networkId}`, { name: name === undefined ? undefined : networkName(name), ...members });
    await refresh();
  });
}

/** Removes a named network. Its members stay. */
export function deleteNetwork(networkId: string): Promise<void> {
  return call(async () => {
    await live.request("DELETE", `/networks/${networkId}`);
    await refresh();
  });
}

// Certificates of the customers (docs/certificates.md)

function putCertificate(next: CustomerCertificate) {
  update((d) => {
    const before = d.certificates.find((x) => x.id === next.id);
    const keep = before?.certificatePem && before.fingerprint === next.fingerprint && !next.certificatePem ? { certificatePem: before.certificatePem } : {};
    d.certificates = before ? d.certificates.map((x) => (x.id === next.id ? { ...next, ...keep } : x)) : [...d.certificates, next];
  });
}

/** The certificates that the panel follows now: the timer of each one. */
const following = new Map<string, ReturnType<typeof setTimeout>>();

/**
 * Follows an order: the panel reads the certificate again every few seconds until the order
 * ends, for up to one hour. Then it loads all data again, because the servers get the new
 * certificate.
 */
function followCertificate(id: string, started = Date.now()) {
  clearTimeout(following.get(id));
  following.set(id, setTimeout(() => {
    following.delete(id);
    void live.certificate(id).then((c) => {
      putCertificate(c);
      if (c.order && Date.now() - started < 3_600_000) followCertificate(id, started);
      else void refresh();
    }).catch(() => undefined);
  }, 3000));
}

/** Reads a certificate with its chain. The page of the certificate calls it when it opens. */
export async function showCertificate(id: string): Promise<void> {
  try {
    const c = await live.certificate(id);
    putCertificate(c);
    if (c.order && !following.has(id)) followCertificate(id);
  } catch {
    // The certificate is removed, or the API does not answer. The page shows what the panel has.
  }
}

/** Uploads a certificate chain and its private key in PEM format. The promise gives the ID. */
export function uploadCertificate(customerId: string, certificate: string, privateKey: string): Promise<string> {
  return call(async () => {
    const c = live.toCertificate(await live.request<live.ApiCertificate>("POST", "/certificates", { source: "upload", customerId: customerId || undefined, certificate, privateKey }));
    putCertificate(c);
    void refresh();
    return c.id;
  });
}

/** Asks the panel to get a certificate for the domain with DNS validation. The promise gives the ID. */
export function requestCertificate(customerId: string, domain: string, wildcard: boolean): Promise<string> {
  return call(async () => {
    const c = live.toCertificate(await live.request<live.ApiCertificate>("POST", "/certificates", { source: "acme", customerId: customerId || undefined, domain: domain.trim().toLowerCase().replace(/\.$/, ""), wildcard }));
    putCertificate(c);
    followCertificate(c.id);
    return c.id;
  });
}

/** Replaces the chain and the key of an uploaded certificate. */
export function replaceCertificate(id: string, certificate: string, privateKey: string): Promise<void> {
  return call(async () => {
    putCertificate(live.toCertificate(await live.request<live.ApiCertificate>("PUT", `/certificates/${id}`, { certificate, privateKey })));
    void showCertificate(id);
    void refresh();
  });
}

/** Starts a new order for a certificate with DNS validation now. */
export function renewCertificate(id: string): Promise<void> {
  return call(async () => {
    putCertificate(live.toCertificate(await live.request<live.ApiCertificate>("POST", `/certificates/${id}/renew`)));
    followCertificate(id);
  });
}

/** Removes a certificate. Its domains get a certificate from the edge proxy again. */
export function deleteCertificate(id: string): Promise<void> {
  return call(async () => {
    await live.request("DELETE", `/certificates/${id}`);
    clearTimeout(following.get(id));
    following.delete(id);
    update((d) => {
      d.certificates = d.certificates.filter((x) => x.id !== id);
    });
    void refresh();
  });
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
  void Promise.all(jobs.map((j) => track(j))).then(() => showDnsZone(zoneId)).then(() => followProvider(zoneId)).catch(() => undefined);
}

/** The time between two reads of a zone that waits for its external DNS provider. */
export const PROVIDER_POLL_MS = 2000;

/**
 * A zone at an external DNS provider has no job of an agent. A queue worker of the panel sends
 * it to the plugin. The panel reads the zone again until the provider has the last change,
 * for up to 30 seconds.
 */
async function followProvider(zoneId: string) {
  for (let i = 0; i < 15; i++) {
    const zone = state.dnsZones.find((x) => x.id === zoneId);
    if (!zone?.provider || zone.provider.state !== "pending" || zone.status === "removing") return;
    await new Promise((resolve) => setTimeout(resolve, PROVIDER_POLL_MS));
    await showDnsZone(zoneId);
  }
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

/**
 * Makes a zone for a domain of a customer. With a DNS account, the zone goes to its external
 * provider, and first gets the records that the provider has now ("importRecords"). The
 * promise gives the ID of the zone.
 */
export function createDnsZone(name: string, customerId?: string, accountId?: string, importRecords = true): Promise<string> {
  return call(async () => {
    const r = await live.request<{ zone: live.ApiDnsZone } & DnsJobs>("POST", "/dns-zones", {
      name: name.trim().toLowerCase().replace(/\.$/, ""), customerId: customerId || undefined, ...(accountId ? { accountId, importRecords } : {}),
    });
    putDnsZone(r.zone);
    trackZone(r.zone.id, r.jobs);
    patchAccountZones(accountId, 1);
    return r.zone.id;
  });
}

/** Moves a zone to the provider of a DNS account, or back to the DNS servers of the panel (null). */
export function moveDnsZone(zoneId: string, accountId: string | null, importRecords = true): Promise<void> {
  return call(async () => {
    const before = state.dnsZones.find((z) => z.id === zoneId)?.provider?.accountId;
    const r = await live.request<{ zone: live.ApiDnsZone } & DnsJobs>("POST", `/dns-zones/${zoneId}/move`, { accountId, importRecords });
    putDnsZone(r.zone);
    trackZone(zoneId, r.jobs);
    patchAccountZones(before, -1);
    patchAccountZones(accountId ?? undefined, 1);
  });
}

/** The count of zones of an account changes with the zones. */
function patchAccountZones(accountId: string | undefined, change: number) {
  if (!accountId) return;
  update((d) => {
    const a = d.dnsAccounts.find((x) => x.id === accountId);
    if (a) a.zones = Math.max(0, a.zones + change);
  });
}

/**
 * Removes a zone and all its records from each DNS server. A zone at an external provider goes
 * from the provider too, unless "keepAtProvider".
 */
export function deleteDnsZone(zoneId: string, keepAtProvider = false): Promise<void> {
  return call(async () => {
    const accountId = state.dnsZones.find((z) => z.id === zoneId)?.provider?.accountId;
    const r = await live.request<DnsJobs>("DELETE", `/dns-zones/${zoneId}${keepAtProvider ? "?keepAtProvider=1" : ""}`);
    patchAccountZones(accountId, -1);
    update((d) => {
      const z = d.dnsZones.find((x) => x.id === zoneId);
      if (z) z.status = "removing";
    });
    void Promise.all(r.jobs.map((j) => track(j))).then(() => refresh()).catch(() => undefined);
    if (r.jobs.length === 0) void refresh();
  });
}

/** Reads the DNS accounts and the DNS providers again. */
async function loadDnsAccounts() {
  const r = await live.request<{ data: live.ApiDnsAccount[]; providers: DnsProvider[] }>("GET", "/dns-accounts");
  update((d) => {
    d.dnsAccounts = r.data.map(live.toDnsAccount);
    d.dnsProviders = r.providers;
  });
}

export interface DnsAccountInput {
  name: string;
  /** For a new account only. */
  pluginId?: string;
  provider?: string;
  /** A value for each field of the provider. An empty secret keeps its value. */
  credentials?: Record<string, string>;
  /** For a new account only. Null: an account of the panel (only an administrator). */
  customerId?: string | null;
}

/** Adds an account at an external DNS provider, or changes one. The plugin checks the credentials first. */
export function saveDnsAccount(input: DnsAccountInput, accountId?: string): Promise<void> {
  return call(async () => {
    if (accountId) {
      await live.request<live.ApiDnsAccount>("PATCH", `/dns-accounts/${accountId}`, { name: input.name.trim(), ...(input.credentials ? { credentials: input.credentials } : {}) });
    } else {
      await live.request<live.ApiDnsAccount>("POST", "/dns-accounts", {
        name: input.name.trim(), pluginId: input.pluginId, provider: input.provider, credentials: input.credentials ?? {}, customerId: input.customerId,
      });
    }
    await loadDnsAccounts();
  });
}

/** Removes an account that no zone uses. */
export function deleteDnsAccount(accountId: string): Promise<void> {
  return call(async () => {
    await live.request<void>("DELETE", `/dns-accounts/${accountId}`);
    update((d) => {
      d.dnsAccounts = d.dnsAccounts.filter((a) => a.id !== accountId);
    });
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

/** The result of an import of a zone file, or of its preview. */
export interface DnsImportResult {
  dryRun: boolean;
  /** The records that the panel added, or would add. */
  added: { line: number; name: string; type: DnsRecordType; value: string; ttl: number; priority?: number; note?: string }[];
  /** The number of records of the file that the zone has already. */
  existing: number;
  /** The lines that the panel does not import, with the reason. */
  skipped: { line: number; text: string; code: string; reason: string }[];
}

/** Adds the records of a zone file to a zone. With dryRun, the panel only tells what it would add. */
export function importDnsZone(zoneId: string, zoneFile: string, dryRun: boolean): Promise<DnsImportResult> {
  return call(async () => {
    type Added = Omit<DnsImportResult["added"][number], "priority" | "note"> & { priority: number | null; note: string | null };
    const r = await live.request<{ dryRun: boolean; added: Added[]; existing: number; skipped: DnsImportResult["skipped"]; zone: live.ApiDnsZone } & DnsJobs>("POST", `/dns-zones/${zoneId}/import`, { zoneFile, dryRun });
    if (!dryRun) {
      putDnsZone(r.zone);
      trackZone(zoneId, r.jobs);
    }
    return { dryRun: r.dryRun, existing: r.existing, skipped: r.skipped, added: r.added.map((a) => ({ ...a, priority: a.priority ?? undefined, note: a.note ?? undefined })) };
  });
}

/** The records of a zone as a zone file (RFC 1035). */
export function exportDnsZone(zoneId: string): Promise<string> {
  return call(async () => (await live.request<{ name: string; zoneFile: string }>("GET", `/dns-zones/${zoneId}/export`)).zoneFile);
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

// White label

/** The default brand that the administrator sets. Only an administrator can read it. */
export function defaultBrand(): Promise<BrandFields> {
  return call(() => live.request<BrandFields & Record<string, unknown>>("GET", "/brand/default"));
}

/** Changes the default brand. A null value removes the field. */
export function saveDefaultBrand(change: { [K in keyof BrandFields]?: BrandFields[K] | null }) {
  return call(() => live.request<BrandFields>("PUT", "/brand/default", change)).then(async (r) => {
    await refresh();
    return r;
  });
}

/**
 * Changes the brand of a reseller. A null value removes the field. A new panel domain, name
 * server host name or mail host name needs a proof of control: the error message gives the
 * TXT record.
 */
export function saveResellerBrand(customerId: string, change: { [K in keyof ResellerBranding]?: ResellerBranding[K] | null }) {
  return call(() => live.request("PATCH", `/customers/${customerId}`, { branding: change })).then(() => refresh());
}

export function savePlan(plan: Plan) {
  const exists = state.plans.some((p) => p.id === plan.id);
  const body = { name: plan.name.trim(), websites: plan.websites, mailboxes: plan.mailboxes, mailStorageBytes: plan.mailStorageBytes, databases: plan.databases, phpExtensions: plan.phpExtensions, limits: plan.limits, databaseServers: plan.databaseServers, databaseLimits: plan.databaseLimits, dnsZones: plan.dnsZones, networks: plan.networks };
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

// Plugins (docs/plugins.md). Only an administrator manages them. Each role opens the pages.

export interface PluginAdmin { plugins: Plugin[]; keys: TrustedKey[]; developerMode: boolean }

/** The plugins, the trusted keys and the developer mode, for the Plugins page. */
export async function loadPlugins(): Promise<PluginAdmin> {
  return call(async () => {
    const [plugins, keys, settings] = await Promise.all([
      live.request<{ data: Plugin[] }>("GET", "/plugins"),
      live.request<{ data: TrustedKey[] }>("GET", "/trusted-keys"),
      live.request<{ developerMode: boolean }>("GET", "/plugin-settings"),
    ]);
    return { plugins: plugins.data, keys: keys.data, developerMode: settings.developerMode };
  });
}

export function pluginDeliveries(pluginId: string): Promise<PluginDelivery[]> {
  return call(() => live.request<{ data: PluginDelivery[] }>("GET", `/plugins/${pluginId}/deliveries`).then((r) => r.data));
}

export interface PluginPreview { manifest: PluginManifest; signedBy: TrustedKey | null; installed: string | null; developerMode: boolean }

/** Checks a manifest and gives what it asks for. It changes nothing. */
export function previewPlugin(manifest: string, signature: string): Promise<PluginPreview> {
  return call(() => live.request<PluginPreview>("POST", "/plugins/preview", { manifest, signature: signature.trim() || null }));
}

export function installPlugin(manifest: string, signature: string, placement: PluginPlacement, confirm: Confirmation): Promise<PluginCredentials> {
  const where = placement.hosted
    ? { hosted: true, serverId: placement.serverId, host: placement.host.trim() || null }
    : { baseUrl: placement.baseUrl.trim() };
  return call(async () => {
    const r = await live.request<PluginCredentials>("POST", "/plugins", { manifest, signature: signature.trim() || null, ...where, ...confirmation(confirm) });
    await refresh();
    return r;
  });
}

export function upgradePlugin(pluginId: string, manifest: string, signature: string, confirm: Confirmation): Promise<Plugin> {
  return call(async () => {
    const r = await live.request<Plugin>("PUT", `/plugins/${pluginId}`, { manifest, signature: signature.trim() || null, ...confirmation(confirm) });
    await refresh();
    return r;
  });
}

export function setPluginEnabled(pluginId: string, enabled: boolean): Promise<Plugin> {
  return call(async () => {
    const r = await live.request<Plugin>("PATCH", `/plugins/${pluginId}`, { enabled });
    await refresh();
    return r;
  });
}

export function setPluginAddress(pluginId: string, baseUrl: string, confirm: Confirmation): Promise<Plugin> {
  return call(async () => {
    const r = await live.request<Plugin>("PATCH", `/plugins/${pluginId}`, { baseUrl: baseUrl.trim(), ...confirmation(confirm) });
    await refresh();
    return r;
  });
}

export function removePlugin(pluginId: string): Promise<void> {
  return call(async () => {
    await live.request("DELETE", `/plugins/${pluginId}`);
    await refresh();
  });
}

export function rotatePluginCredentials(pluginId: string, confirm: Confirmation): Promise<PluginCredentials> {
  return call(() => live.request<PluginCredentials>("POST", `/plugins/${pluginId}/credentials`, confirmation(confirm)));
}

/** Makes the container of a plugin that the panel runs again, for example after a failure. */
export function restartPluginContainer(pluginId: string): Promise<Plugin> {
  return call(() => live.request<Plugin>("POST", `/plugins/${pluginId}/container`));
}

export function testPlugin(pluginId: string): Promise<PluginDelivery> {
  return call(() => live.request<PluginDelivery>("POST", `/plugins/${pluginId}/test`));
}

export function retryPluginDelivery(pluginId: string, deliveryId: string): Promise<PluginDelivery> {
  return call(() => live.request<PluginDelivery>("POST", `/plugins/${pluginId}/deliveries/${deliveryId}/retry`));
}

// Single sign-on providers (administrator)

export const listIdentityProviders = () => call(() => live.listIdentityProviders());

export function addIdentityProvider(input: live.IdentityProviderInput, confirm: Confirmation): Promise<live.IdentityProvider> {
  return call(() => live.request<live.IdentityProvider>("POST", "/identity-providers", { ...input, ...confirmation(confirm) }));
}

export function changeIdentityProvider(id: string, input: live.IdentityProviderInput, confirm: Confirmation): Promise<live.IdentityProvider> {
  return call(() => live.request<live.IdentityProvider>("PUT", `/identity-providers/${encodeURIComponent(id)}`, { ...input, ...confirmation(confirm) }));
}

export function removeIdentityProvider(id: string, confirm: Confirmation): Promise<void> {
  return call(() => live.request("DELETE", `/identity-providers/${encodeURIComponent(id)}`, confirmation(confirm)).then(() => undefined));
}

/** Trusts the public key of a publisher, for plugins and packs. */
export function addTrustedKey(name: string, publicKey: string, confirm: Confirmation): Promise<TrustedKey> {
  return call(() => live.request<TrustedKey>("POST", "/trusted-keys", { name: name.trim(), publicKey: publicKey.trim(), ...confirmation(confirm) }));
}

export function removeTrustedKey(keyId: string): Promise<void> {
  return call(() => live.request("DELETE", `/trusted-keys/${keyId}`).then(() => undefined));
}

export function setPluginDeveloperMode(on: boolean, confirm: Confirmation): Promise<boolean> {
  return call(() => live.request<{ developerMode: boolean }>("PUT", "/plugin-settings", { developerMode: on, ...(on ? confirmation(confirm) : {}) }).then((r) => r.developerMode));
}

/** A short token for a plugin page, for the signed-in user. */
/** Runs an action of a plugin on a website, a server or a customer. */
export function runPluginAction(pluginId: string, actionId: string, targetId: string): Promise<PluginActionResult> {
  return call(() => live.request<PluginActionResult>("POST", `/plugin-actions/${pluginId}/${actionId}`, { targetId }));
}

export function pluginPageSession(pluginId: string, pageId: string): Promise<PluginPageSession> {
  return call(() => live.request<PluginPageSession>("POST", `/plugin-pages/${pluginId}/${pageId}/session`));
}


// Packs (docs/packs.md). Only an administrator manages them.

export interface PackAdmin { packs: Pack[]; keys: TrustedKey[]; developerMode: boolean }

/** The packs, the trusted keys and the developer mode, for the Packs page. */
export async function loadPacks(): Promise<PackAdmin> {
  return call(async () => {
    const [packs, keys, settings] = await Promise.all([
      live.request<{ data: Pack[] }>("GET", "/packs"),
      live.request<{ data: TrustedKey[] }>("GET", "/trusted-keys"),
      live.request<{ developerMode: boolean }>("GET", "/pack-settings"),
    ]);
    return { packs: packs.data, keys: keys.data, developerMode: settings.developerMode };
  });
}

export interface PackPreview { manifest: PackManifest; signedBy: PackSigner | null; installed: { id: string; version: string } | null; developerMode: boolean }

/**
 * Checks a manifest and gives what it adds. It changes nothing. The panel keeps the exact text
 * of the manifest, because the signature covers each byte: the store does not trim it.
 */
export function previewPack(manifest: string, signature: string): Promise<PackPreview> {
  return call(() => live.request<PackPreview>("POST", "/packs/preview", { manifest, signature: signature.trim() || null }));
}

export function installPack(manifest: string, signature: string, confirm: Confirmation): Promise<Pack> {
  return call(async () => {
    const r = await live.request<Pack>("POST", "/packs", { manifest, signature: signature.trim() || null, ...confirmation(confirm) });
    await refresh();
    return r;
  });
}

export function updatePack(packId: string, manifest: string, signature: string, confirm: Confirmation): Promise<Pack> {
  return call(async () => {
    const r = await live.request<Pack>("PUT", `/packs/${packId}`, { manifest, signature: signature.trim() || null, ...confirmation(confirm) });
    await refresh();
    return r;
  });
}

export function removePack(packId: string): Promise<void> {
  return call(async () => {
    await live.request("DELETE", `/packs/${packId}`);
    await refresh();
  });
}

/** Sends all packs to each web server again, for example after a failed job. */
export function syncPacks(): Promise<number> {
  return call(async () => {
    const r = await live.request<{ servers: number }>("POST", "/packs/sync");
    await refresh();
    return r.servers;
  });
}

export function setPackDeveloperMode(on: boolean, confirm: Confirmation): Promise<boolean> {
  return call(() => live.request<{ developerMode: boolean }>("PUT", "/pack-settings", { developerMode: on, ...(on ? confirmation(confirm) : {}) }).then((r) => r.developerMode));
}
