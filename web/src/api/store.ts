// The state of the panel and the actions that change it.
//
// In live mode (mode.ts), each action calls the panel API through live.ts, and the state comes
// from the API. In mock mode, this file is an in-memory panel API with demonstration data. It
// follows the flow of a change in spec section 6.3: the API checks the input, writes the
// desired state, and puts a job in the queue. A simulated agent then runs the job step by step.

import { validateRecord, type RecordInput } from "../lib/dns";
import { ENGINE_NAME, jobActive } from "../lib/labels";
import * as live from "./live";
import { LIVE } from "./mode";
import { DAY, GB, MB, seed } from "./seed";
import type {
  AlertChannel,
  ApiToken,
  BackupPolicy,
  Branding,
  CronJob,
  Customer,
  Database,
  DatabaseEngine,
  DatabaseGrant,
  DatabasePrivileges,
  DnsRecord,
  Domain,
  DomainKind,
  FileNode,
  HostUpdatePolicy,
  Job,
  MailAlias,
  ManagedUser,
  Plan,
  ServerRole,
  State,
  SystemSettings,
  PanelCertificateMethod,
  TokenScope,
  UpdatePolicy,
  Website,
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
function emptyState(): State {
  const s = seed();
  return {
    ...s,
    session: { ...s.session, signedIn: false, user: { id: "", name: "", email: "", role: "owner" }, contextId: "" },
    contexts: [], websites: [], jobs: [], activity: [], notifications: [], servers: [], images: [], rollout: undefined,
    zones: [], mailboxes: [], aliases: [], databases: [], databaseUsers: [], files: {}, snapshots: [], plans: [], customers: [], users: [],
    tokens: [], passkeys: [], totpEnabled: false, audit: [], alertChannels: [], joinToken: "", joinCaSha256: undefined,
    panelCertificate: { domain: "", method: null, state: "pending", letsEncryptAvailable: false, serversWithoutCa: [] },
  };
}

let state: State = LIVE ? emptyState() : seed();
const listeners = new Set<Listener>();
let seq = 1000;

/** Time for each job step. Tests set it to 0. */
export const timing = { stepMs: 900, rolloutMs: 2500 };

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

export function resetState() {
  state = seed();
  listeners.forEach((l) => l());
}

const id = (prefix: string) => `${prefix}_${++seq}`;
const now = () => new Date().toISOString();
const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));

function audit(draft: State, action: string, target: string) {
  draft.audit.unshift({ id: id("aud"), at: now(), actor: draft.session.user.name, ip: "203.0.113.45", action, target });
}

function notify(draft: State, tone: State["notifications"][number]["tone"], text: string, href?: string) {
  draft.notifications.unshift({ id: id("ntf"), tone, text, at: now(), read: false, href });
}

function site(draft: State, websiteId: string): Website {
  const w = draft.websites.find((x) => x.id === websiteId);
  if (!w) throw new ApiError("The website does not exist.");
  return w;
}

interface JobSpec {
  title: string;
  ref?: string;
  steps: string[];
  websiteId?: string;
  serverId?: string;
  /** Changes the state when the job is complete. */
  apply?: (draft: State) => void;
  /** Changes the state when the job starts. */
  begin?: (draft: State) => void;
  activity?: string;
  failAt?: number;
  error?: string;
  onFail?: (draft: State) => void;
}

/** Puts a job in the queue and runs it. The promise resolves when the job is complete. */
function runJob(spec: JobSpec): Promise<Job> {
  const jobId = id("job");
  update((d) => {
    d.jobs.unshift({
      id: jobId,
      title: spec.title,
      ref: spec.ref,
      state: "running",
      steps: spec.steps,
      step: 0,
      createdAt: now(),
      websiteId: spec.websiteId,
      serverId: spec.serverId,
    });
    spec.begin?.(d);
  });

  return (async () => {
    for (let i = 0; i < spec.steps.length; i++) {
      await delay(timing.stepMs);
      if (spec.failAt === i && state.jobs.some((j) => j.id === jobId)) {
        update((d) => {
          const job = d.jobs.find((j) => j.id === jobId)!;
          job.state = "failed";
          job.error = spec.error ?? "The step failed.";
          job.finishedAt = now();
          spec.onFail?.(d);
          notify(d, "danger", `${spec.title} failed: ${job.error}`, spec.websiteId ? `/websites/${spec.websiteId}` : undefined);
        });
        return getState().jobs.find((j) => j.id === jobId)!;
      }
      // The state was reset, for example by a test. The job no longer exists.
      if (!state.jobs.some((j) => j.id === jobId)) return { id: jobId, title: spec.title, state: "failed", steps: spec.steps, step: i, createdAt: now() } as Job;
      update((d) => {
        const job = d.jobs.find((j) => j.id === jobId)!;
        job.step = i + 1;
        if (i === spec.steps.length - 1) {
          job.state = "succeeded";
          job.finishedAt = now();
          spec.apply?.(d);
          if (spec.activity) d.activity.unshift({ id: id("act"), text: spec.activity, ref: spec.ref, at: now() });
        }
      });
    }
    return getState().jobs.find((j) => j.id === jobId)!;
  })();
}

const touch = (w: Website, summary: string, ref?: string) => {
  w.lastChange = { at: now(), summary, ref };
};

// Live mode

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
  if (!LIVE || !live.hasSession()) return Promise.resolve();
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
  if (LIVE && state.session.signedIn) replace({ ...state, session: { ...state.session, loading: !state.session.user.id } });
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
  if (!LIVE) return;
  void live.showWebsite(state, websiteId).then((apply) => apply && replace(apply(state))).catch(() => {});
}

/** True when the panel reads the databases and snapshots only of the websites that the user opened. */
export const partialDetails = () => LIVE && state.websites.length > live.DETAIL_LIMIT;

/** For an area that the API of release 0.1 does not have yet. The UI hides these areas. */
function notInRelease(): never {
  throw new ApiError("The panel API of this release does not have this feature yet.");
}

const q = (s: string) => encodeURIComponent(s);
type JobAnswer = { job: live.ApiJob | null };

if (LIVE) {
  live.setUnauthorisedHandler(() => replace(emptyState()));
  if (live.hasSession()) {
    state = { ...state, session: { ...state.session, signedIn: true, loading: true } };
    void refresh();
  }
  // Read changes from agents and other users. The browser slows timers in hidden tabs.
  // Batch 1, finding 9: with many websites, a load takes more requests, so the panel loads less often.
  const poll = () => setTimeout(async () => {
    if (state.session.signedIn && document.visibilityState === "visible") await refresh();
    poll();
  }, state.websites.length > 500 ? 60000 : 15000);
  poll();
}

const hex = () => Math.random().toString(16).slice(2, 9).padEnd(7, "0");

// Session

export function signIn(method: "passkey" | "password", totp?: string, credentials?: { email: string; password: string }) {
  if (LIVE) {
    if (method === "passkey") notInRelease();
    return call(async () => {
      await live.signIn(credentials?.email ?? "", credentials?.password ?? "", totp ?? "");
      replace({ ...state, session: { ...state.session, signedIn: true, loading: true } });
      await refresh();
    });
  }
  if (method === "password" && !/^\d{6}$/.test(totp ?? "")) {
    throw new ApiError("Type the 6-digit code from your authenticator app.", { totp: "Type the 6-digit code." });
  }
  update((d) => {
    d.session.signedIn = true;
    audit(d, method === "passkey" ? "Signed in with passkey" : "Signed in with password and TOTP", d.session.user.email);
  });
}

export function signOut() {
  if (LIVE) {
    live.signOut();
    replace(emptyState());
    return;
  }
  update((d) => {
    d.session.signedIn = false;
  });
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
}

export function createWebsite(input: NewWebsite): Promise<{ websiteId: string; job: Promise<Job> }> | { websiteId: string; job: Promise<Job> } {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ website: live.ApiWebsite; job: live.ApiJob }>("POST", "/websites", {
        // CF-07: only an administrator selects the plan here. The panel uses the plan of the customer.
        name: input.name.trim().toLowerCase(), application: input.application, imageId: input.imageId, planId: state.session.contextId === "platform" || state.session.user.role === "administrator" ? input.planId : undefined,
        serverId: input.serverId || undefined, customerId: input.customerId && input.customerId !== "platform" ? input.customerId : undefined,
      });
      update((d) => {
        d.websites = [live.toWebsite(r.website), ...d.websites.filter((w) => w.id !== r.website.id)];
      });
      return { websiteId: r.website.id, job: track(r.job) };
    });
  }
  const name = input.name.trim().toLowerCase();
  const errors: Record<string, string> = {};
  if (!/^(?!-)[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(name)) errors.name = "Type a domain name, for example example.com.";
  if (state.websites.some((w) => w.domains.some((d) => d.name === name))) errors.name = "A website already uses this domain.";
  const image = state.images.find((i) => i.id === input.imageId);
  if (!image) errors.imageId = "Select a runtime.";
  const plan = state.plans.find((p) => p.id === input.planId);
  if (!plan) errors.planId = "Select a plan.";
  if (Object.keys(errors).length) throw new ApiError("Correct the errors and try again.", errors);

  const websiteId = id("web");
  const kind = image!.family === "frankenphp" ? "frankenphp" : image!.family === "apache-php-fpm" ? "apache-php-fpm" : image!.family === "static" || image!.family === "edge" ? "static" : "oci";
  const unix = `${name.split(".")[0].replace(/[^a-z0-9]/g, "").slice(0, 8)}_${hex().slice(0, 4)}`;
  const job = runJob({
    title: `Create website ${name}`,
    websiteId,
    steps: ["Create Unix user", "Pull and check image signature", "Start container in gVisor", "Configure edge proxy", "Get TLS certificate"],
    activity: `Created website ${name}`,
    begin: (d) => {
      d.websites.unshift({
        id: websiteId,
        name,
        application: input.application || "New website",
        customerId: input.customerId,
        serverId: input.serverId,
        unixUser: unix,
        createdAt: now(),
        status: "starting",
        runtime: { kind, imageId: image!.id, phpVersion: image!.phpVersion, workerMode: false, extensions: kind === "frankenphp" || kind === "apache-php-fpm" ? ["opcache", "intl", "gd", "zip"] : [], phpIni: kind === "oci" || kind === "static" ? {} : { memory_limit: "256M", upload_max_filesize: "64M", max_execution_time: "60" } },
        planId: plan!.id,
        limits: { ...plan!.limits },
        use: { cpuPercent: 0, memoryBytes: 0, diskBytes: 0, processes: 0, diskIoBytesPerSecond: 0 },
        domains: [{ name, kind: "primary", certificate: { state: "pending" }, dnsPointsHere: true, verification: verificationFor(d, input.customerId, name) }],
        releases: [],
        previews: [],
        cron: [],
        sshKeys: [],
        shellEnabled: false,
        lastChange: { at: now(), summary: "Created" },
        managedByTerraform: false,
      });
      d.files[websiteId] = [
        { path: "/public", name: "public", type: "dir", sizeBytes: 0, modifiedAt: now() },
        { path: "/public/index.html", name: "index.html", type: "file", sizeBytes: 120, modifiedAt: now(), content: `<!doctype html>\n<title>${name}</title>\n<h1>${name} is ready</h1>\n` },
        { path: "/logs", name: "logs", type: "dir", sizeBytes: 0, modifiedAt: now() },
      ];
      audit(d, "Created website", name);
    },
    apply: (d) => {
      const w = site(d, websiteId);
      w.status = "running";
      w.use = { cpuPercent: 1, memoryBytes: 42 * MB, diskBytes: 0.01 * GB, processes: 3, diskIoBytesPerSecond: 0 };
      w.domains[0].certificate = { state: "valid", expiresAt: new Date(Date.now() + 90 * DAY).toISOString() };
      notify(d, "success", `${name} is ready.`, `/websites/${websiteId}`);
    },
  });
  return { websiteId, job };
}

/**
 * Makes a website again after its first job failed, for example because the server could not
 * pull the image. The agent keeps what the first job made.
 */
export function retryWebsite(websiteId: string) {
  if (LIVE) {
    return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/retry`)).then((r) => {
      update((d) => { site(d, websiteId).status = "starting"; });
      return track(r.job);
    });
  }
  const w = state.websites.find((x) => x.id === websiteId)!;
  return runJob({
    title: `Create website ${w.name}`,
    websiteId,
    steps: ["Create Unix user", "Pull and check image signature", "Start container in gVisor", "Configure edge proxy", "Get TLS certificate"],
    activity: `Created ${w.name}`,
    begin: (d) => {
      site(d, websiteId).status = "starting";
      audit(d, "Created website again", w.name);
    },
    apply: (d) => {
      site(d, websiteId).status = "running";
    },
  });
}

export function setPower(websiteId: string, action: "start" | "stop" | "restart") {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/power`, { action })).then((r) => track(r.job));
  const w = state.websites.find((x) => x.id === websiteId)!;
  const label = { start: "Start", stop: "Stop", restart: "Restart" }[action];
  return runJob({
    title: `${label} ${w.name}`,
    websiteId,
    steps: action === "restart" ? ["Start new container", "Health check", "Stop old container"] : action === "start" ? ["Start container in gVisor", "Health check"] : ["Stop container"],
    activity: `${label === "Stop" ? "Stopped" : label === "Start" ? "Started" : "Restarted"} ${w.name}`,
    begin: (d) => {
      const s = site(d, websiteId);
      if (action !== "restart") s.status = action === "start" ? "starting" : "stopping";
      audit(d, `${label}ed website`.replace("Stoped", "Stopped"), s.name);
    },
    apply: (d) => {
      const s = site(d, websiteId);
      s.status = action === "stop" ? "stopped" : "running";
      if (action === "stop") {
        s.use = { ...s.use, cpuPercent: 0, memoryBytes: 0, processes: 0, diskIoBytesPerSecond: 0 };
        s.domains.forEach((dm) => (dm.certificate = { state: "not_needed" }));
      } else if (s.use.memoryBytes === 0) {
        s.use = { ...s.use, cpuPercent: 1, memoryBytes: 64 * MB, processes: 4 };
        s.domains.forEach((dm) => (dm.certificate = { state: "valid", expiresAt: new Date(Date.now() + 90 * DAY).toISOString() }));
      }
      touch(s, `${label === "Stop" ? "Stopped" : label === "Start" ? "Started" : "Restarted"}`);
    },
  });
}

/** A database user that a staging copy made. The API shows the password once. */
export interface StagingCredential {
  engine: DatabaseEngine;
  user: string;
  password: string;
  databases: string[];
}

export function createStagingCopy(websiteId: string): Promise<{ websiteId: string; job: Promise<Job>; credentials?: StagingCredential[] }> | { websiteId: string; job: Promise<Job>; credentials?: StagingCredential[] } {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ website: live.ApiWebsite; job: live.ApiJob; databaseCredentials?: StagingCredential[] }>("POST", `/websites/${websiteId}/staging`);
      update((d) => {
        d.websites = [live.toWebsite(r.website), ...d.websites.filter((w) => w.id !== r.website.id)];
      });
      return { websiteId: r.website.id, job: track(r.job), credentials: r.databaseCredentials ?? [] };
    });
  }
  const src = state.websites.find((x) => x.id === websiteId)!;
  const name = `staging.${src.name.replace(/^www\./, "")}`.replace(/^staging\.staging\./, "staging.");
  if (state.websites.some((w) => w.name === name)) throw new ApiError(`${name} exists. Remove it first, or refresh it from the website.`);
  const copyId = id("web");
  const job = runJob({
    title: `Make staging copy of ${src.name}`,
    websiteId: copyId,
    steps: ["Copy files", "Copy databases", "Change URLs in the copy", "Start container in gVisor", "Get TLS certificate"],
    activity: `Made staging copy ${name}`,
    begin: (d) => {
      const copy = structuredClone(site(d, websiteId));
      copy.id = copyId;
      copy.name = name;
      copy.application = `Staging copy of ${src.name}`;
      copy.stagingOf = websiteId;
      // The copy is a new website, so it uses the database containers of its customer.
      copy.sharedDatabases = false;
      copy.status = "starting";
      copy.createdAt = now();
      copy.unixUser = `${copy.unixUser.split("_")[0].slice(0, 5)}stg_${hex().slice(0, 4)}`;
      copy.domains = [{ name, kind: "primary", certificate: { state: "pending" }, dnsPointsHere: true }];
      copy.previews = [];
      copy.git = undefined;
      copy.managedByTerraform = false;
      copy.lastChange = { at: now(), summary: `Cloned from ${src.name}` };
      d.websites.unshift(copy);
      d.files[copyId] = structuredClone(d.files[websiteId] ?? []);
      audit(d, "Made staging copy", `${src.name} → ${name}`);
    },
    apply: (d) => {
      const c = site(d, copyId);
      c.status = "running";
      c.domains[0].certificate = { state: "valid", expiresAt: new Date(Date.now() + 90 * DAY).toISOString() };
    },
  });
  return { websiteId: copyId, job };
}

export function deleteWebsite(websiteId: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("DELETE", `/websites/${websiteId}`)).then((r) => track(r.job));
  const w = state.websites.find((x) => x.id === websiteId)!;
  return runJob({
    title: `Remove website ${w.name}`,
    steps: ["Stop container", "Remove edge proxy route", "Remove Unix user"],
    activity: `Removed website ${w.name}`,
    begin: (d) => audit(d, "Removed website", w.name),
    apply: (d) => {
      d.websites = d.websites.filter((x) => x.id !== websiteId);
      d.databases = d.databases.filter((x) => x.websiteId !== websiteId);
      d.databaseUsers = d.databaseUsers.filter((x) => x.websiteId !== websiteId);
      delete d.files[websiteId];
    },
  });
}

export function setLimits(websiteId: string, limits: Website["limits"]) {
  if (LIVE) return call(() => live.request<Partial<JobAnswer>>("PATCH", `/websites/${websiteId}`, { limits })).then((r) => track(r.job));
  if (Object.values(limits).some((v) => !(v > 0))) throw new ApiError("Each limit must be more than 0.");
  update((d) => {
    const w = site(d, websiteId);
    w.limits = limits;
    audit(d, "Changed resource limits", w.name);
  });
  return runJob({ title: "Apply resource limits", websiteId, steps: ["Update cgroup limits"] });
}

export function setWebsitePlan(websiteId: string, planId: string) {
  if (LIVE) return call(() => live.request<Partial<JobAnswer>>("PATCH", `/websites/${websiteId}`, { planId })).then((r) => track(r.job));
  update((d) => {
    const w = site(d, websiteId);
    const plan = d.plans.find((p) => p.id === planId)!;
    w.planId = planId;
    w.limits = { ...plan.limits };
    w.runtime.extensions = w.runtime.extensions.filter((e) => plan.phpExtensions.includes(e));
    audit(d, "Changed plan", `${w.name} ${plan.name}`);
  });
  return runJob({ title: "Apply plan limits", websiteId, steps: ["Update cgroup limits"] });
}

// Domains

/**
 * CF-03: a domain needs a DNS TXT record before the edge proxy serves it. An administrator, or a
 * sub-domain of a verified domain of the same customer, does not need one.
 */
function verificationFor(d: State, customerId: string, domain: string): Domain["verification"] {
  if (d.session.contextId === "platform") return undefined;
  const verified = d.websites.filter((w) => w.customerId === customerId).flatMap((w) => w.domains).filter((x) => !x.verification);
  if (verified.some((x) => domain.endsWith(`.${x.name}`))) return undefined;
  const value = `cf-${Array.from(crypto.getRandomValues(new Uint8Array(40)), (b) => "abcdefghijklmnopqrstuvwxyz0123456789"[b % 36]).join("")}`;
  return { type: "TXT", name: `_cloudfactory-challenge.${domain}`, value };
}

export function verifyDomain(websiteId: string, name: string) {
  if (LIVE) {
    return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains/${q(name)}/verify`)).then(async (r) => {
      await refresh();
      if (r.job) await track(r.job);
    });
  }
  const dm = site(state, websiteId).domains.find((x) => x.name === name);
  if (!dm?.verification) return Promise.resolve();
  const want = dm.verification;
  const zone = state.zones.find((z) => want.name.endsWith(`.${z.name}`));
  const label = zone ? want.name.slice(0, -(zone.name.length + 1)) : "";
  if (!zone?.records.some((r) => r.type === "TXT" && r.name === label && r.value === want.value)) {
    throw new ApiError(`The TXT record ${want.name} does not have the value ${want.value}. DNS changes can take some minutes.`, { name: "The TXT record is not correct." });
  }
  return runJob({
    title: `Add domain ${name}`,
    websiteId,
    steps: ["Check TXT record", "Add edge proxy route", "Get TLS certificate"],
    activity: `Verified domain ${name}`,
    begin: (d) => {
      const x = site(d, websiteId).domains.find((y) => y.name === name)!;
      delete x.verification;
      audit(d, "Verified domain", `${site(d, websiteId).name} ${name}`);
    },
    apply: (d) => {
      const x = site(d, websiteId).domains.find((y) => y.name === name)!;
      if (x.dnsPointsHere) x.certificate = { state: "valid", expiresAt: new Date(Date.now() + 90 * DAY).toISOString() };
    },
  }).then(() => undefined);
}

export function addDomain(websiteId: string, name: string, kind: DomainKind) {
  if (LIVE) return call(() => live.request<Partial<JobAnswer>>("POST", `/websites/${websiteId}/domains`, { name: name.trim().toLowerCase(), kind })).then(async (r) => {
    await refresh();
    if (r.job) await track(r.job);
  });
  const domain = name.trim().toLowerCase();
  if (!/^(?!-)[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(domain)) throw new ApiError("Type a domain name.", { name: "Type a domain name, for example www.example.com." });
  if (state.websites.some((w) => w.domains.some((d) => d.name === domain))) throw new ApiError("A website already uses this domain.", { name: "A website already uses this domain." });
  const ours = state.zones.find((z) => domain === z.name || domain.endsWith(`.${z.name}`));
  const verification = verificationFor(state, site(state, websiteId).customerId, domain);
  if (verification) {
    update((d) => {
      const w = site(d, websiteId);
      w.domains.push({ name: domain, kind, certificate: { state: "pending" }, dnsPointsHere: !!ours, verification });
      audit(d, "Added domain", `${w.name} ${domain}`);
    });
    return Promise.resolve();
  }
  return runJob({
    title: `Add domain ${domain}`,
    websiteId,
    steps: ["Add edge proxy route", "Check DNS", "Get TLS certificate"],
    activity: `Added domain ${domain}`,
    begin: (d) => {
      const w = site(d, websiteId);
      w.domains.push({ name: domain, kind, certificate: { state: "pending" }, dnsPointsHere: !!ours });
      if (ours) {
        const z = d.zones.find((x) => x.name === ours.name)!;
        const label = domain === z.name ? "@" : domain.slice(0, -(z.name.length + 1));
        if (!z.records.some((r) => r.name === label)) z.records.push({ id: id("rec"), type: "A", name: label, value: "203.0.113.10", ttl: 3600, managed: "website" });
      }
      audit(d, "Added domain", `${w.name} ${domain}`);
    },
    apply: (d) => {
      const dm = site(d, websiteId).domains.find((x) => x.name === domain)!;
      if (dm.dnsPointsHere) dm.certificate = { state: "valid", expiresAt: new Date(Date.now() + 90 * DAY).toISOString() };
    },
  }).then(() => undefined);
}

export function removeDomain(websiteId: string, name: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("DELETE", `/websites/${websiteId}/domains/${q(name)}`)).then(async (r) => {
    await refresh();
    if (r.job) await track(r.job);
  });
  update((d) => {
    const w = site(d, websiteId);
    const dm = w.domains.find((x) => x.name === name);
    if (dm?.kind === "primary") throw new ApiError("Make a different domain primary first.");
    w.domains = w.domains.filter((x) => x.name !== name);
    audit(d, "Removed domain", `${w.name} ${name}`);
  });
}

export function makePrimary(websiteId: string, name: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains/${q(name)}/primary`)).then((r) => track(r.job));
  update((d) => {
    const w = site(d, websiteId);
    w.domains.forEach((x) => {
      if (x.kind === "primary") x.kind = "redirect";
      if (x.name === name) x.kind = "primary";
    });
    w.domains.sort((a, b) => (a.kind === "primary" ? -1 : b.kind === "primary" ? 1 : 0));
    w.name = name;
    audit(d, "Changed primary domain", name);
  });
}

/** FR-403: check again if the DNS of a domain points to the correct server. */
export function checkDns(websiteId: string, name: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains/${q(name)}/check`)).then((r) => track(r.job));
  return runJob({
    title: `Check DNS for ${name}`,
    websiteId,
    steps: ["Query public resolvers"],
    apply: (d) => {
      const dm = site(d, websiteId).domains.find((x) => x.name === name);
      // The sample data never points shop-old.example.org at the server.
      if (dm && name !== "shop-old.example.org") {
        dm.dnsPointsHere = true;
        dm.certificate = { state: "valid", expiresAt: new Date(Date.now() + 90 * DAY).toISOString() };
      }
    },
  });
}

// Runtime

export function changeRuntime(websiteId: string, imageId: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { imageId })).then((r) => track(r.job));
  const image = state.images.find((i) => i.id === imageId)!;
  const w = state.websites.find((x) => x.id === websiteId)!;
  const plan = state.plans.find((p) => p.id === w.planId);
  return runJob({
    title: `Change runtime to ${image.name}`,
    websiteId,
    ref: `${image.running}`,
    steps: ["Check image signature", "Start new container on a copy", "Health check", "Switch traffic", "Stop old container"],
    activity: `Changed ${w.name} to ${image.name}`,
    begin: (d) => audit(d, "Changed runtime", `${w.name} ${image.name}`),
    apply: (d) => {
      const s = site(d, websiteId);
      const old = d.images.find((i) => i.id === s.runtime.imageId);
      s.runtime.previous = { imageId: s.runtime.imageId, phpVersion: s.runtime.phpVersion, tag: old ? `${old.running}${old.phpVersion ? `-php${old.phpVersion}` : ""}` : "" };
      s.runtime.imageId = image.id;
      s.runtime.phpVersion = image.phpVersion;
      s.runtime.kind = image.family === "apache-php-fpm" ? "apache-php-fpm" : image.family === "frankenphp" ? "frankenphp" : s.runtime.kind;
      if (plan) s.runtime.extensions = s.runtime.extensions.filter((e) => plan.phpExtensions.includes(e));
      touch(s, "Runtime", image.name);
    },
  });
}

/** FR-303: go back to the previous image in one action. */
export function revertRuntime(websiteId: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/runtime/revert`)).then((r) => track(r.job));
  const w = state.websites.find((x) => x.id === websiteId)!;
  if (!w.runtime.previous) throw new ApiError("There is no previous image.");
  return changeRuntime(websiteId, w.runtime.previous.imageId);
}

export function setWorkerMode(websiteId: string, on: boolean) {
  if (LIVE) return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { workerMode: on })).then((r) => track(r.job));
  update((d) => {
    const w = site(d, websiteId);
    if (w.runtime.kind !== "frankenphp") throw new ApiError("Worker mode is only for FrankenPHP.");
    w.runtime.workerMode = on;
    audit(d, on ? "Set worker mode to on" : "Set worker mode to off", w.name);
  });
  return runJob({ title: `Set worker mode to ${on ? "on" : "off"}`, websiteId, steps: ["Start new container", "Health check", "Switch traffic"] });
}

/** Batch 1, finding 11: the directory in /app that the web server serves. */
export function setDocumentRoot(websiteId: string, documentRoot: string) {
  const root = documentRoot.trim().replace(/^\/+|\/+$/g, "") || ".";
  if (!/^(\.|[A-Za-z0-9_-][A-Za-z0-9._-]*(\/[A-Za-z0-9_-][A-Za-z0-9._-]*)*)$/.test(root) || /(^|\/)\.\.(\/|$)/.test(root)) {
    throw new ApiError("Type a directory in the files of the website.", { documentRoot: "Use letters, digits, dots, dashes and slashes, for example public or web. Do not use \"..\"." });
  }
  if (LIVE) return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { documentRoot: root })).then((r) => track(r.job));
  update((d) => {
    const w = site(d, websiteId);
    w.runtime.documentRoot = root;
    audit(d, "Changed document root", `${w.name} ${root}`);
  });
  return runJob({ title: "Change document root", websiteId, steps: ["Write the unit", "Restart the container", "Health check"] });
}

export function setPhpSettings(websiteId: string, extensions: string[], phpIni: Record<string, string>) {
  if (LIVE) return call(() => live.request<JobAnswer>("PUT", `/websites/${websiteId}/runtime`, { extensions, phpIni })).then((r) => track(r.job));
  const w = state.websites.find((x) => x.id === websiteId)!;
  const plan = state.plans.find((p) => p.id === w.planId)!;
  const notAllowed = extensions.filter((e) => !plan.phpExtensions.includes(e));
  if (notAllowed.length) throw new ApiError(`The ${plan.name} plan does not include: ${notAllowed.join(", ")}.`);
  const errors: Record<string, string> = {};
  for (const [k, v] of Object.entries(phpIni)) {
    if (!/^[a-z_.]+$/.test(k)) errors[k] = "Use a php.ini setting name.";
    if (k === "memory_limit" && !/^\d+[MG]$/.test(v)) errors[k] = "Use a value such as 256M or 1G.";
    if (k === "memory_limit" && /^\d+[MG]$/.test(v)) {
      const bytesValue = parseInt(v) * (v.endsWith("G") ? GB : MB);
      if (bytesValue > w.limits.memoryBytes) errors[k] = "The value is more than the memory limit of the website.";
    }
  }
  if (Object.keys(errors).length) throw new ApiError("Correct the php.ini values.", errors);
  update((d) => {
    const s = site(d, websiteId);
    s.runtime.extensions = extensions;
    s.runtime.phpIni = phpIni;
    audit(d, "Changed PHP settings", s.name);
  });
  return runJob({ title: "Apply PHP settings", websiteId, steps: ["Write configuration", "Reload PHP"] });
}

/** Test the image update on a copy first, then switch with no downtime. */
export function applyImageUpdate(websiteId: string) {
  if (LIVE) throw new ApiError("Image updates go to all websites in stages. Start the rollout on the Updates page.");
  const w = state.websites.find((x) => x.id === websiteId)!;
  const image = state.images.find((i) => i.id === w.runtime.imageId)!;
  return runJob({
    title: "Test update on a copy of this site",
    ref: image.available,
    websiteId,
    steps: ["Copy the files", "Copy the database", "Health check on the copy", "Switch with no downtime"],
    activity: `Updated ${w.name} to ${image.name} ${image.available ?? ""}`.trim(),
    begin: (d) => audit(d, "Started image update", `${w.name} ${image.available}`),
    apply: (d) => {
      const s = site(d, websiteId);
      s.runtime.previous = { imageId: s.runtime.imageId, phpVersion: s.runtime.phpVersion, tag: `${image.running}` };
      s.imageTag = image.available;
      s.updateScheduledAt = undefined;
      touch(s, "Image update", `${image.available}`);
    },
  });
}

export function scheduleImageUpdate(websiteId: string, at: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const w = site(d, websiteId);
    w.updateScheduledAt = at;
    audit(d, "Scheduled image update", `${w.name} at ${at}`);
  });
}

// Deploys (FR-1001 to FR-1010)

export function setGitSource(websiteId: string, git: Website["git"]) {
  if (LIVE) notInRelease();
  if (git && !/^(git@[\w.-]+:[\w./-]+|https:\/\/[\w.-]+\/[\w./-]+)$/.test(git.repository)) {
    throw new ApiError("Type a Git URL.", { repository: "Use an SSH or HTTPS Git URL." });
  }
  if (git && !git.branch.trim()) throw new ApiError("Type a branch.", { branch: "Type a branch, for example main." });
  update((d) => {
    const w = site(d, websiteId);
    w.git = git && {
      ...git,
      deployKey: w.git?.deployKey ?? `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI${hex()}${hex()}… ${w.unixUser} deploy`,
    };
    audit(d, git ? "Connected Git repository" : "Disconnected Git repository", w.name);
  });
}

export function deployNow(websiteId: string, opts: { fail?: boolean; migration?: boolean } = {}) {
  if (LIVE) notInRelease();
  const w = state.websites.find((x) => x.id === websiteId)!;
  if (!w.git) throw new ApiError("Connect a Git repository first.");
  const ref = hex();
  const started = Date.now();
  const steps = ["Fetch from Git", "Build in a separate container", "Write new release directory", "Switch live release", "Health check"];
  return runJob({
    title: "Deploy",
    ref,
    websiteId,
    steps,
    activity: `Deployed ${w.name}`,
    failAt: opts.fail ? 4 : undefined,
    error: opts.fail ? (opts.migration ? "The health check failed. The deploy ran a database migration, so the panel did not go back automatically." : "The health check failed. The panel went back to the previous release.") : undefined,
    begin: (d) => audit(d, "Deployed", `${w.name} ${ref}`),
    onFail: (d) => {
      const s = site(d, websiteId);
      s.releases.unshift({ id: id("rel"), ref, message: "Deploy from the panel", at: now(), state: "failed", durationSeconds: Math.round((Date.now() - started) / 1000), live: false, migration: !!opts.migration });
      s.releases = s.releases.slice(0, 5);
    },
    apply: (d) => {
      const s = site(d, websiteId);
      s.releases.forEach((r) => (r.live = false));
      s.releases.unshift({ id: id("rel"), ref, message: "Deploy from the panel", at: now(), state: "succeeded", durationSeconds: Math.max(1, Math.round((Date.now() - started) / 1000)), live: true, migration: !!opts.migration });
      // FR-1007: keep the last 5 releases.
      s.releases = s.releases.slice(0, 5);
      touch(s, "Deploy", `${s.git!.branch} · ${ref}`);
    },
  });
}

export function rollbackRelease(websiteId: string, releaseId: string) {
  if (LIVE) notInRelease();
  const w = state.websites.find((x) => x.id === websiteId)!;
  const rel = w.releases.find((r) => r.id === releaseId)!;
  return runJob({
    title: "Go back to release",
    ref: rel.ref,
    websiteId,
    steps: ["Switch live release", "Health check"],
    activity: `Went back to ${rel.ref} on ${w.name}`,
    begin: (d) => audit(d, "Went back to release", `${w.name} ${rel.ref}`),
    apply: (d) => {
      const s = site(d, websiteId);
      s.releases.forEach((r) => (r.live = r.id === releaseId));
      touch(s, "Rollback", rel.ref);
    },
  });
}

// Cron jobs

const CRON_FIELD = /^(\*|\d+|\d+-\d+)(\/\d+)?(,(\*|\d+|\d+-\d+)(\/\d+)?)*$/;

export function addCron(websiteId: string, schedule: string, command: string) {
  if (LIVE) notInRelease();
  const fields = schedule.trim().split(/\s+/);
  const errors: Record<string, string> = {};
  if (fields.length !== 5 || !fields.every((f) => CRON_FIELD.test(f))) errors.schedule = "Use five fields, for example */5 * * * *.";
  if (!command.trim()) errors.command = "Type a command.";
  if (Object.keys(errors).length) throw new ApiError("Correct the cron job.", errors);
  update((d) => {
    const w = site(d, websiteId);
    w.cron.push({ id: id("cron"), schedule: fields.join(" "), command: command.trim(), enabled: true });
    audit(d, "Added cron job", `${w.name} ${command.trim()}`);
  });
}

export function setCron(websiteId: string, cronId: string, change: Partial<CronJob>) {
  if (LIVE) notInRelease();
  update((d) => {
    const c = site(d, websiteId).cron.find((x) => x.id === cronId)!;
    Object.assign(c, change);
  });
}

export function removeCron(websiteId: string, cronId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const w = site(d, websiteId);
    w.cron = w.cron.filter((x) => x.id !== cronId);
    audit(d, "Removed cron job", w.name);
  });
}

export function runCron(websiteId: string, cronId: string) {
  if (LIVE) notInRelease();
  const c = state.websites.find((x) => x.id === websiteId)!.cron.find((x) => x.id === cronId)!;
  return runJob({
    title: "Run cron job",
    ref: c.command,
    websiteId,
    steps: ["Run in the website container"],
    apply: (d) => {
      const cc = site(d, websiteId).cron.find((x) => x.id === cronId)!;
      cc.lastRunAt = now();
      cc.lastResult = "succeeded";
    },
  });
}

// SFTP and SSH

export function addSshKey(websiteId: string, name: string, key: string) {
  if (LIVE) notInRelease();
  if (!/^(ssh-(ed25519|rsa)|ecdsa-sha2-nistp\d+|sk-ssh-ed25519@openssh\.com) [A-Za-z0-9+/=]{20,}/.test(key.trim())) {
    throw new ApiError("This is not an SSH public key.", { key: "Paste a public key that starts with ssh-ed25519 or ssh-rsa." });
  }
  update((d) => {
    const w = site(d, websiteId);
    w.sshKeys.push({ id: id("key"), name: name.trim() || "SSH key", fingerprint: `SHA256:${hex().slice(0, 4)}…${hex().slice(0, 3)}`, addedAt: now() });
    audit(d, "Added SSH key", `${w.name} ${name}`);
  });
}

export function removeSshKey(websiteId: string, keyId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const w = site(d, websiteId);
    w.sshKeys = w.sshKeys.filter((k) => k.id !== keyId);
    audit(d, "Removed SSH key", w.name);
  });
}

export function setAccess(websiteId: string, change: { shellEnabled?: boolean }) {
  if (LIVE) notInRelease();
  update((d) => {
    const w = site(d, websiteId);
    Object.assign(w, change);
    audit(d, "Changed SFTP and SSH access", w.name);
  });
}

/**
 * Spec section 10: the SFTP access of a website (live mode). The owner signs in as the Unix
 * user of the website with an SSH key. SFTP accepts no passwords.
 */
export function setSftp(websiteId: string, change: { enabled: boolean; authorizedKeys: string[] }): Promise<{ job: Promise<Job> }> {
  const keys = change.authorizedKeys.map((k) => k.trim().replace(/\s+/g, " ")).filter(Boolean);
  const bad = keys.findIndex((k) => !SSH_KEY.test(k));
  if (bad >= 0) return Promise.reject(new ApiError("This is not an SSH public key.", { key: "Paste a public key that starts with ssh-ed25519, ssh-rsa or ecdsa-sha2." }));
  if (new Set(keys).size !== keys.length) return Promise.reject(new ApiError("This key is in the list.", { key: "This key is in the list." }));
  if (change.enabled && keys.length === 0) return Promise.reject(new ApiError("Add an SSH public key to turn on SFTP.", { key: "Add an SSH public key to turn on SFTP." }));
  if (!LIVE) {
    update((d) => audit(d, "Changed SFTP access", site(d, websiteId).name));
    return Promise.resolve({ job: runJob({ title: "Set SFTP access", websiteId, steps: ["Write the keys", "Lock the password"] }) });
  }
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
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/snapshots`)).then((r) => track(r.job));
  const w = state.websites.find((x) => x.id === websiteId)!;
  return runJob({
    title: `Back up ${w.name}`,
    websiteId,
    steps: ["Snapshot files", "Dump databases", "Copy mailboxes", "Upload to object storage"],
    activity: `Backed up ${w.name}`,
    apply: (d) => {
      const s = site(d, websiteId);
      d.snapshots.unshift({
        id: id("snap"),
        at: now(),
        kind: "manual",
        sizeBytes: Math.round(s.use.diskBytes * 0.22),
        websiteId,
        databases: d.databases.filter((x) => x.websiteId === websiteId).map((x) => x.name),
        mailboxes: d.mailboxes.filter((m) => s.domains.some((dm) => m.address.endsWith(`@${dm.name}`))).map((m) => m.address),
      });
      audit(d, "Backed up website", s.name);
    },
  });
}

export type RestoreScope = { kind: "website" } | { kind: "database"; name: string } | { kind: "mailbox"; name: string } | { kind: "file"; path: string };

/**
 * Audit finding R8: removeNewFiles is for the restore of a full website. The panel then also
 * removes the files that are not in the backup, for example the files that an attacker added.
 */
export function restore(snapshotId: string, scope: RestoreScope, removeNewFiles = false) {
  const extra = removeNewFiles && scope.kind === "website";
  if (LIVE) {
    const snapshot = state.snapshots.find((x) => x.id === snapshotId);
    if (!snapshot) throw new ApiError("The snapshot does not exist.");
    const item = scope.kind === "website" ? undefined : scope.kind === "file" ? scope.path : scope.name;
    return call(() => live.request<JobAnswer>("POST", `/websites/${snapshot.websiteId}/snapshots/${snapshotId}/restore`, { scope: scope.kind, item, ...(extra ? { removeNewFiles: true } : {}) })).then((r) => track(r.job));
  }
  const snap = state.snapshots.find((s) => s.id === snapshotId)!;
  const w = state.websites.find((x) => x.id === snap.websiteId)!;
  const what = scope.kind === "website" ? "full website" : scope.kind === "file" ? scope.path : scope.name;
  return runJob({
    title: `Restore ${what}`,
    websiteId: w.id,
    steps: scope.kind === "website" ? ["Download snapshot", "Restore files", "Restore databases", "Restore mailboxes", "Health check"] : ["Download snapshot", "Restore"],
    activity: `Restored ${what} on ${w.name}`,
    begin: (d) => audit(d, "Restored from backup", `${w.name} ${what}${extra ? ", new files removed" : ""}`),
  });
}

export function setBackupPolicy(policy: BackupPolicy) {
  if (LIVE) return call(() => live.request<BackupPolicy>("PUT", "/backup-policy", policy)).then(() => refresh());
  const errors: Record<string, string> = {};
  (["daily", "weekly", "monthly"] as const).forEach((k) => {
    if (!Number.isInteger(policy[k]) || policy[k] < 0 || policy[k] > 365) errors[k] = "Use a number from 0 to 365.";
  });
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(policy.time)) errors.time = "Use the format HH:MM.";
  if (!/^(s3|b2|azure|gs):\S+$/.test(policy.target)) errors.target = "Use a restic repository, for example s3:s3.amazonaws.com/bucket/path.";
  if (Object.keys(errors).length) throw new ApiError("Correct the backup policy.", errors);
  update((d) => {
    const { credentials, ...rest } = policy;
    d.backupPolicy = { ...rest, credentialNames: credentials ? Object.keys(credentials) : d.backupPolicy.credentialNames };
    audit(d, "Changed backup policy", `${policy.daily} daily, ${policy.weekly} weekly, ${policy.monthly} monthly`);
  });
}

// DNS

export function addZone(name: string) {
  if (LIVE) notInRelease();
  const zone = name.trim().toLowerCase();
  if (!/^(?!-)[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(zone)) throw new ApiError("Type a domain name.", { name: "Type a domain name, for example example.com." });
  if (state.zones.some((z) => z.name === zone)) throw new ApiError("This zone exists.", { name: "This zone exists." });
  update((d) => {
    d.zones.push({
      name: zone,
      customerId: d.session.contextId,
      nameservers: ["dns-01.cloudfactory.io", "dns-02.cloudfactory.io"],
      records: [
        { id: id("rec"), type: "NS", name: "@", value: "dns-01.cloudfactory.io.", ttl: 86400, managed: "ns" },
        { id: id("rec"), type: "NS", name: "@", value: "dns-02.cloudfactory.io.", ttl: 86400, managed: "ns" },
      ],
      managedByTerraform: false,
    });
    audit(d, "Added DNS zone", zone);
  });
  return runJob({ title: `Add DNS zone ${zone}`, steps: ["Write zone to dns-01", "Write zone to dns-02", "Check serial on both servers"] });
}

export function saveRecord(zoneName: string, input: RecordInput, recordId?: string) {
  if (LIVE) notInRelease();
  const zone = state.zones.find((z) => z.name === zoneName)!;
  const errors = validateRecord(input, zone.records, recordId);
  if (Object.keys(errors).length) throw new ApiError("Correct the record and try again.", errors as Record<string, string>);
  update((d) => {
    const z = d.zones.find((x) => x.name === zoneName)!;
    const record: DnsRecord = { id: recordId ?? id("rec"), type: input.type, name: input.name.trim() || "@", value: input.value.trim(), ttl: input.ttl, priority: input.type === "MX" || input.type === "SRV" ? input.priority : undefined };
    if (recordId) z.records = z.records.map((r) => (r.id === recordId ? record : r));
    else z.records.push(record);
    audit(d, recordId ? "Changed DNS record" : "Added DNS record", `${zoneName} ${record.type} ${record.name}`);
  });
  return runJob({ title: `Update DNS zone ${zoneName}`, steps: ["Write to dns-01", "Write to dns-02"] });
}

export function deleteRecord(zoneName: string, recordId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const z = d.zones.find((x) => x.name === zoneName)!;
    const r = z.records.find((x) => x.id === recordId)!;
    z.records = z.records.filter((x) => x.id !== recordId);
    audit(d, "Removed DNS record", `${zoneName} ${r.type} ${r.name}`);
  });
}

/** FR-602: make the SPF, DKIM and DMARC records for a zone. */
export function addMailRecords(zoneName: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const z = d.zones.find((x) => x.name === zoneName)!;
    z.records = z.records.filter((r) => r.managed !== "mail");
    z.records.push(
      { id: id("rec"), type: "MX", name: "@", value: "mail-01.cloudfactory.io.", ttl: 3600, priority: 10, managed: "mail" },
      { id: id("rec"), type: "TXT", name: "@", value: "v=spf1 mx include:_spf.cloudfactory.io -all", ttl: 3600, managed: "mail" },
      { id: id("rec"), type: "TXT", name: "cf2026._domainkey", value: `v=DKIM1; k=ed25519; p=${btoa(hex() + hex() + hex()).slice(0, 43)}=`, ttl: 3600, managed: "mail" },
      { id: id("rec"), type: "TXT", name: "_dmarc", value: `v=DMARC1; p=quarantine; rua=mailto:dmarc@${zoneName}`, ttl: 3600, managed: "mail" },
    );
    audit(d, "Added mail DNS records", zoneName);
  });
}

// Email

export function addMailbox(address: string, quotaGb: number) {
  if (LIVE) notInRelease();
  const a = address.trim().toLowerCase();
  const errors: Record<string, string> = {};
  if (!/^[a-z0-9._%+-]+@[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(a)) errors.address = "Type an email address, for example hello@example.com.";
  if (state.mailboxes.some((m) => m.address === a) || state.aliases.some((x) => x.source === a)) errors.address = "This address exists.";
  if (!(quotaGb > 0 && quotaGb <= 100)) errors.quota = "Use a size from 1 to 100 GB.";
  if (Object.keys(errors).length) throw new ApiError("Correct the mailbox.", errors);
  update((d) => {
    d.mailboxes.push({ id: id("mbx"), address: a, quotaBytes: quotaGb * GB, usedBytes: 0 });
    audit(d, "Created mailbox", a);
    const domain = a.split("@")[1];
    const z = d.zones.find((x) => x.name === domain);
    if (z && !z.records.some((r) => r.managed === "mail")) notify(d, "info", `Add the mail DNS records for ${domain} so that mail is delivered.`, `/dns/${domain}`);
  });
}

export function removeMailbox(mailboxId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const m = d.mailboxes.find((x) => x.id === mailboxId)!;
    d.mailboxes = d.mailboxes.filter((x) => x.id !== mailboxId);
    audit(d, "Removed mailbox", m.address);
  });
}

export function setMailboxQuota(mailboxId: string, quotaGb: number) {
  if (LIVE) notInRelease();
  update((d) => {
    const m = d.mailboxes.find((x) => x.id === mailboxId)!;
    if (quotaGb * GB < m.usedBytes) throw new ApiError("The size is smaller than the mail in the mailbox.");
    m.quotaBytes = quotaGb * GB;
    audit(d, "Changed mailbox size", `${m.address} ${quotaGb} GB`);
  });
}

export function addAlias(kind: MailAlias["kind"], source: string, destinations: string[]) {
  if (LIVE) notInRelease();
  const s = source.trim().toLowerCase();
  const dest = destinations.map((x) => x.trim().toLowerCase()).filter(Boolean);
  const errors: Record<string, string> = {};
  const email = /^[a-z0-9._%+-]+@[a-z0-9-]+(\.[a-z0-9-]+)+$/;
  if (!email.test(s)) errors.source = "Type an email address.";
  if (state.mailboxes.some((m) => m.address === s) || state.aliases.some((a) => a.source === s)) errors.source = "This address exists.";
  if (!dest.length || !dest.every((x) => email.test(x))) errors.destinations = "Type one or more email addresses, with a comma between them.";
  if (kind === "alias" && dest.some((x) => !state.mailboxes.some((m) => m.address === x))) errors.destinations = "An alias must go to a mailbox on this panel. Use a forwarder for other addresses.";
  if (Object.keys(errors).length) throw new ApiError("Correct the address.", errors);
  update((d) => {
    d.aliases.push({ id: id("als"), kind, source: s, destinations: dest });
    audit(d, kind === "alias" ? "Created alias" : "Created forwarder", s);
  });
}

export function removeAlias(aliasId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const a = d.aliases.find((x) => x.id === aliasId)!;
    d.aliases = d.aliases.filter((x) => x.id !== aliasId);
    audit(d, "Removed address", a.source);
  });
}

// Databases (FR-501, FR-502)

/** CF-01: each database and database user name starts with the Unix user of the website. */
export function databasePrefix(websiteId: string) {
  return `${site(state, websiteId).unixUser}_`;
}

const DB_SUFFIX = /^[a-z0-9][a-z0-9_]{0,31}$/;

function databaseName(websiteId: string, name: string) {
  const prefix = databasePrefix(websiteId);
  const suffix = name.trim().startsWith(prefix) ? name.trim().slice(prefix.length) : name.trim();
  if (!DB_SUFFIX.test(suffix)) throw new ApiError("Correct the name.", { name: "Use 1 to 32 lower-case letters, digits and underscores." });
  return prefix + suffix;
}

const PASSWORD_CHARACTERS = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
const newPassword = () => Array.from(crypto.getRandomValues(new Uint8Array(24)), (b) => PASSWORD_CHARACTERS[b % PASSWORD_CHARACTERS.length]).join("");

export type GrantInput = { databaseId: string; privileges: DatabasePrivileges };

/** The number of Valkey databases of one customer on a server. Database 0 is for no website. */
const VALKEY_DATABASES = 1024;

/** The message for a Valkey database of a website with the shared database containers of an earlier release. */
export const sharedDatabasesMessage = (server: string) =>
  `This website uses the shared database containers of an earlier release, which have no Valkey. An administrator must move the databases of the server ${server} to the containers of each customer first.`;

/**
 * Makes a database. With createUser, it also makes a user with readwrite access, and gives its password.
 * A Valkey database always has a user with the same name: the user and the database number are the database.
 */
export function createDatabase(websiteId: string, engine: DatabaseEngine, name: string, createUser = true): Promise<string | null> {
  if (engine === "valkey") createUser = true;
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ password: string | null; job: live.ApiJob }>("POST", `/websites/${websiteId}/databases`, { engine, name: name.trim(), createUser });
      void track(r.job).then(() => refresh());
      await refresh();
      return r.password;
    });
  }
  const n = databaseName(websiteId, name);
  if (state.databases.some((x) => x.name === n && x.engine === engine)) throw new ApiError("This database exists.", { name: "This database exists." });
  if (createUser && state.databaseUsers.some((x) => x.name === n && x.engine === engine)) throw new ApiError("A database user with this name exists.", { name: "A database user with this name exists. Make the database with no user." });
  const w = state.websites.find((x) => x.id === websiteId)!;
  const server = state.servers.find((x) => x.id === w.serverId);
  if (engine === "valkey" && server?.databaseEngines && !server.databaseEngines.includes("valkey")) {
    throw new ApiError("Correct the engine.", { engine: `The agent on the server ${server.name} did not report that it can operate Valkey. Update the agent of the server, then try again.` });
  }
  if (engine === "valkey" && w.sharedDatabases) {
    throw new ApiError("Correct the engine.", { engine: sharedDatabasesMessage(server?.name ?? "of the website") });
  }
  const dbId = id("db");
  void runJob({
    title: `Create ${ENGINE_NAME[engine]} database ${n}`,
    websiteId,
    steps: engine === "valkey" ? ["Create database and user"] : createUser ? ["Create database", "Create user", "Allow the website container only"] : ["Create database"],
    activity: `Created database ${n} for ${w.name}`,
    begin: (d) => audit(d, "Created database", `${w.name} ${n}`),
    apply: (d) => {
      const database: Database = { id: dbId, name: n, engine, websiteId, sizeBytes: 0, serverId: w.serverId };
      if (engine === "valkey") {
        // Each customer has its own Valkey container on a server. The server gives the smallest
        // number that no database of the customer has there.
        const sites = new Set(d.websites.filter((x) => x.customerId === w.customerId && x.serverId === w.serverId).map((x) => x.id));
        const used = new Set(d.databases.filter((x) => x.engine === "valkey" && sites.has(x.websiteId)).map((x) => x.number));
        database.number = Array.from({ length: VALKEY_DATABASES - 1 }, (_, i) => i + 1).find((i) => !used.has(i));
        database.keys = 0;
      }
      d.databases.push(database);
      if (createUser) d.databaseUsers.push({ id: id("dbu"), name: n, engine, websiteId, grants: [{ databaseId: dbId, database: n, privileges: "readwrite" }], access: "website" });
    },
  });
  return Promise.resolve(createUser ? newPassword() : null);
}

export function deleteDatabase(databaseId: string) {
  if (LIVE) {
    const db = state.databases.find((x) => x.id === databaseId);
    if (!db) throw new ApiError("The database does not exist.");
    return call(() => live.request<JobAnswer>("DELETE", `/websites/${db.websiteId}/databases/${databaseId}`)).then((r) => track(r.job)).then(() => refresh());
  }
  update((d) => {
    const db = d.databases.find((x) => x.id === databaseId)!;
    d.databases = d.databases.filter((x) => x.id !== databaseId);
    d.databaseUsers.forEach((u) => (u.grants = u.grants.filter((g) => g.databaseId !== databaseId)));
    // The user of a Valkey database goes with its database.
    if (db.engine === "valkey") d.databaseUsers = d.databaseUsers.filter((u) => !(u.engine === "valkey" && u.name === db.name));
    audit(d, "Removed database", db.name);
  });
}

function checkGrants(websiteId: string, engine: DatabaseEngine, grants: GrantInput[]) {
  return grants.map((g, i): DatabaseGrant => {
    const db = state.databases.find((x) => x.id === g.databaseId && x.websiteId === websiteId && x.engine === engine);
    if (!db) throw new ApiError("Correct the databases.", { [`grants.${i}.databaseId`]: "Select a database of this website with the same engine." });
    return { databaseId: db.id, database: db.name, privileges: g.privileges };
  }).sort((a, b) => a.database.localeCompare(b.database));
}

/** Makes a database user with a permission for each database, and gives its password. */
export function createDatabaseUser(websiteId: string, engine: DatabaseEngine, name: string, grants: GrantInput[]): Promise<string> {
  if (engine === "valkey") throw new ApiError("Correct the engine.", { engine: "A Valkey database has one user, with the same name. Make a Valkey database." });
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ password: string; job: live.ApiJob }>("POST", `/websites/${websiteId}/database-users`, { engine, name: name.trim(), grants });
      void track(r.job);
      await refresh();
      return r.password;
    });
  }
  const n = databaseName(websiteId, name);
  if (state.databaseUsers.some((x) => x.name === n && x.engine === engine)) throw new ApiError("This user exists.", { name: "This user exists." });
  const checked = checkGrants(websiteId, engine, grants);
  update((d) => {
    d.databaseUsers.push({ id: id("dbu"), name: n, engine, websiteId, grants: checked, access: "website" });
    audit(d, "Created database user", n);
  });
  return Promise.resolve(newPassword());
}

/** Changes the permissions of a user, where it can connect from, or its password. Gives the new password. */
export function setDatabaseUser(userId: string, change: { grants?: GrantInput[]; access?: "website" | "anywhere"; resetPassword?: boolean }): Promise<string | null> {
  const user = state.databaseUsers.find((x) => x.id === userId);
  if (!user) throw new ApiError("The database user does not exist.");
  if (user.engine === "valkey" && (change.grants || change.access)) throw new ApiError("A Valkey user has access to its own database only, from the container of the website only. You can only make a new password.");
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ password: string | null; job: live.ApiJob }>("PATCH", `/websites/${user.websiteId}/database-users/${userId}`, change);
      void track(r.job);
      await refresh();
      return r.password;
    });
  }
  const grants = change.grants && checkGrants(user.websiteId, user.engine, change.grants);
  update((d) => {
    const u = d.databaseUsers.find((x) => x.id === userId)!;
    if (grants) u.grants = grants;
    if (change.access) u.access = change.access;
    audit(d, "Changed database user", u.name);
  });
  return Promise.resolve(change.resetPassword ? newPassword() : null);
}

export function deleteDatabaseUser(userId: string) {
  const user = state.databaseUsers.find((x) => x.id === userId);
  if (!user) throw new ApiError("The database user does not exist.");
  if (user.engine === "valkey") throw new ApiError("The user of a Valkey database goes with its database. Remove the database.");
  if (LIVE) return call(() => live.request<JobAnswer>("DELETE", `/websites/${user.websiteId}/database-users/${userId}`)).then((r) => track(r.job)).then(() => refresh());
  update((d) => {
    d.databaseUsers = d.databaseUsers.filter((x) => x.id !== userId);
    audit(d, "Removed database user", user.name);
  });
}

// Files (FR-803)

const parentOf = (path: string) => path.slice(0, path.lastIndexOf("/")) || "";

export function createNode(websiteId: string, dir: string, name: string, type: FileNode["type"], content = "") {
  if (LIVE) notInRelease();
  const n = name.trim();
  if (!n || n.includes("/") || n === "." || n === "..") throw new ApiError("Type a name with no slash.", { name: "Type a name with no slash." });
  const path = `${dir}/${n}`.replace(/^\/\//, "/");
  if ((state.files[websiteId] ?? []).some((f) => f.path === path)) throw new ApiError("This name exists in the folder.", { name: "This name exists in the folder." });
  update((d) => {
    (d.files[websiteId] ??= []).push({ path, name: n, type, sizeBytes: new Blob([content]).size, modifiedAt: now(), content: type === "file" ? content : undefined });
    audit(d, type === "dir" ? "Created folder" : "Uploaded file", `${site(d, websiteId).name} ${path}`);
  });
}

export function saveFile(websiteId: string, path: string, content: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const f = d.files[websiteId].find((x) => x.path === path)!;
    f.content = content;
    f.sizeBytes = new Blob([content]).size;
    f.modifiedAt = now();
    audit(d, "Changed file", `${site(d, websiteId).name} ${path}`);
  });
}

export function renameNode(websiteId: string, path: string, name: string) {
  if (LIVE) notInRelease();
  const n = name.trim();
  if (!n || n.includes("/")) throw new ApiError("Type a name with no slash.");
  const target = `${parentOf(path)}/${n}`;
  if (state.files[websiteId].some((f) => f.path === target)) throw new ApiError("This name exists in the folder.");
  update((d) => {
    d.files[websiteId].forEach((f) => {
      if (f.path === path) {
        f.path = target;
        f.name = n;
      } else if (f.path.startsWith(`${path}/`)) {
        f.path = target + f.path.slice(path.length);
      }
    });
    audit(d, "Renamed file", `${path} → ${target}`);
  });
}

export function deleteNode(websiteId: string, path: string) {
  if (LIVE) notInRelease();
  update((d) => {
    d.files[websiteId] = d.files[websiteId].filter((f) => f.path !== path && !f.path.startsWith(`${path}/`));
    audit(d, "Removed file", `${site(d, websiteId).name} ${path}`);
  });
}

// Images and updates (FR-1101 to FR-1110)

/** FR-307: a custom image. The port and the start command are optional. */
export function addImage(name: string, reference: string, tag: string, policy: UpdatePolicy, run: { containerPort?: string; command?: string } = {}) {
  const port = run.containerPort?.trim() ? Number(run.containerPort) : null;
  const command = run.command?.trim() ? splitCommand(run.command) : null;
  if (LIVE) return call(() => live.request("POST", "/images", { name: name.trim(), reference: reference.trim(), tag: tag.trim(), policy, containerPort: port, command })).then(() => refresh()).then(() => undefined);
  const errors: Record<string, string> = {};
  if (port !== null && !(Number.isInteger(port) && port >= 1 && port <= 65535)) errors.containerPort = "Use a port from 1 to 65535.";
  if (!name.trim()) errors.name = "Type a name.";
  if (!/^([a-z0-9.-]+(:\d+)?\/)?[a-z0-9._/-]+$/.test(reference.trim())) errors.reference = "Type an image reference, for example docker.io/library/node.";
  if (!/^[\w][\w.-]{0,127}$/.test(tag.trim())) errors.tag = "Type a tag, for example 24-alpine.";
  if (Object.keys(errors).length) throw new ApiError("Correct the image.", errors);
  const imageId = id("img");
  return runJob({
    title: `Add image ${name}`,
    steps: ["Pull image", "Check signature", "Scan for known vulnerabilities"],
    activity: `Added image ${name}`,
    begin: (d) => audit(d, "Added image", `${reference}:${tag}`),
    apply: (d) => {
      d.images.push({ id: imageId, name: name.trim(), family: "custom", reference: reference.trim(), running: tag.trim(), websites: "0", policy, custom: true, signatureVerified: false, digest: `sha256:${hex()}…${hex().slice(0, 4)}`, containerPort: port ?? 8080, command: command ?? undefined });
    },
  });
}

export function setImagePolicy(imageId: string, policy: UpdatePolicy) {
  if (LIVE) return call(() => live.request("PATCH", `/images/${imageId}`, { policy })).then(() => refresh());
  update((d) => {
    const i = d.images.find((x) => x.id === imageId)!;
    i.policy = policy;
    audit(d, "Changed update policy", `${i.name}: ${policy}`);
  });
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
  if (!LIVE) return;
  return call(() => live.request("POST", `/images/${imageId}/check`)).then(() => refresh());
}

export function startRollout(imageId: string) {
  if (LIVE) return call(() => live.request("POST", "/rollouts", { imageId })).then(() => refresh());
  if (state.rollout && (state.rollout.state === "running" || state.rollout.state === "paused")) {
    throw new ApiError("A rollout is in progress. Wait until it is complete, or roll it back.");
  }
  const image = state.images.find((i) => i.id === imageId)!;
  if (!image.available) throw new ApiError("This image is up to date.");
  const total = Math.max(1, parseInt(image.websites) || 1);
  update((d) => {
    d.rollout = {
      id: id("rol"),
      imageId,
      title: `${image.name.split(" · ")[0]} ${image.available} with ${image.name.split(" · ")[1] ?? image.name}`,
      fromTag: image.running,
      toTag: image.available!,
      severity: "security",
      state: "running",
      startedAt: now(),
      window: d.servers[0]?.maintenanceWindow ?? "",
      websites: total,
      servers: 2,
      stages: [
        { name: "Canary", websites: 1, state: "in_progress", note: "1 website, watched for 30 minutes" },
        { name: "10%", websites: Math.max(1, Math.round(total * 0.1)), state: "waiting" },
        { name: "50%", websites: Math.max(1, Math.round(total * 0.5)), state: "waiting" },
        { name: "Everyone", websites: total, state: "waiting" },
      ],
      healthy: 0,
      rolledBack: 0,
      rollbacks: [],
      checks: [
        { label: "Signed with the Cloudfactory release key", passed: true },
        { label: "Scan: 0 critical, 0 high", passed: true },
        { label: "SBOM attached", passed: true },
        { label: "Health check: HTTP 200 and error rate under 1% for 5 minutes", passed: null },
      ],
    };
    const img = d.images.find((i) => i.id === imageId)!;
    img.availableNote = "rolling_out";
    audit(d, "Started rollout", `${image.name} ${image.available}`);
  });
}

export function pauseRollout(paused: boolean) {
  if (LIVE) {
    const r = state.rollout;
    if (!r) throw new ApiError("No rollout is running.");
    return call(() => live.request("POST", `/rollouts/${r.id}/${paused ? "pause" : "resume"}`)).then(() => refresh());
  }
  update((d) => {
    if (!d.rollout) return;
    d.rollout.state = paused ? "paused" : "running";
    audit(d, paused ? "Paused rollout" : "Resumed rollout", d.rollout.title);
  });
}

export function rollBackAll() {
  if (LIVE) {
    const r = state.rollout;
    if (!r) throw new ApiError("No rollout is running.");
    return call(() => live.request("POST", `/rollouts/${r.id}/roll-back`)).then(() => refresh());
  }
  const r = state.rollout!;
  update((d) => {
    d.rollout!.state = "rolled_back";
    audit(d, "Rolled back rollout", r.title);
  });
  return runJob({
    title: `Roll back ${r.title}`,
    steps: ["Switch websites to the previous image", "Health check", "Remove new containers"],
    activity: `Rolled back ${r.title}`,
    apply: (d) => {
      const img = d.images.find((i) => i.id === r.imageId);
      if (img) img.availableNote = undefined;
      d.rollout!.healthy = 0;
      d.rollout!.stages.forEach((s) => (s.state = "waiting"));
      notify(d, "info", `All websites are back on ${r.fromTag}.`, "/updates");
    },
  });
}

/**
 * The simulated agents move the rollout forward while it runs. In live mode the real agents
 * do this, and the panel API gives the progress. Before, this function also ran in live
 * mode: the card showed progress that was not real, and then the page had an error.
 */
function rolloutTick() {
  if (LIVE) return;
  const r = state.rollout;
  if (!r || r.state !== "running") return;
  update((d) => {
    const ro = d.rollout!;
    const stage = ro.stages.find((s) => s.state === "in_progress");
    if (!stage) return;
    const done = ro.healthy + ro.rolledBack;
    if (done < stage.websites) {
      ro.healthy += Math.min(2, stage.websites - done);
      const srv = d.servers.find((s) => s.id === "srv_web02");
      if (srv && srv.state === "updating") {
        const m = /(\d+) of (\d+)/.exec(srv.stateDetail ?? "");
        if (m) srv.stateDetail = `Updating ${Math.min(+m[2], +m[1] + 1)} of ${m[2]} containers`;
      }
      return;
    }
    stage.state = "done";
    const next = ro.stages.find((s) => s.state === "waiting");
    if (next) next.state = "in_progress";
    else {
      ro.state = "done";
      ro.checks[3].passed = true;
      const img = d.images.find((i) => i.id === ro.imageId);
      if (img && img.available) {
        img.running = img.available;
        img.available = undefined;
        img.availableNote = undefined;
      }
      const srv = d.servers.find((s) => s.id === "srv_web02");
      if (srv && srv.state === "updating") {
        srv.state = "healthy";
        srv.stateDetail = undefined;
      }
      d.activity.unshift({ id: id("act"), text: `Rollout complete: ${ro.title}`, at: now() });
      notify(d, "success", `Rollout complete: ${ro.title}.`, "/updates");
    }
  });
}

if (!LIVE && typeof window !== "undefined" && !import.meta.env?.VITEST) {
  setInterval(rolloutTick, timing.rolloutMs);
}

export const _rolloutTick = rolloutTick;

// Servers (FR-1112 to FR-1118, FR-1201)

export function addServer(name: string, location: string, roles: ServerRole[]): Promise<{ serverId: string; job?: Promise<Job> }> | { serverId: string; job?: Promise<Job> } {
  if (LIVE) {
    // The server joins when the install script runs on it. The panel then shows it.
    return refresh().then(() => ({ serverId: "" }));
  }
  const n = name.trim().toLowerCase();
  const errors: Record<string, string> = {};
  if (!/^[a-z][a-z0-9-]{1,30}$/.test(n)) errors.name = "Use lower-case letters, digits and hyphens.";
  if (state.servers.some((s) => s.name === n)) errors.name = "A server with this name exists.";
  if (!roles.length) errors.roles = "Select one or more roles.";
  if (Object.keys(errors).length) throw new ApiError("Correct the server.", errors);
  const serverId = id("srv");
  const job = runJob({
    title: `Add server ${n}`,
    serverId,
    steps: ["Wait for the agent to connect", "Exchange mTLS certificates", "Check operating system", "Install gVisor and check SHA-512", "Hold Podman packages", "Set nftables firewall"],
    activity: `Added server ${n}`,
    begin: (d) => {
      d.servers.push({
        id: serverId, name: n, location: location.trim() || "Unknown", roles, agentVersion: "0.9.4", cpuPercent: 0, memoryPercent: 0, diskPercent: 0,
        state: "joining", stateDetail: "Waiting for the agent", os: "Debian 13 (trixie)", kernel: "6.12.48-1", arch: "x86-64",
        podmanVersion: "5.4.2", gvisorVersion: "release-20261013.0", gvisorPlatform: "systrap", bareMetal: false, updatePolicy: "security",
        maintenanceWindow: "02:00 to 05:00 (Europe/London)", pendingUpdates: [], lastUpdateAt: now(), rebootRequired: false, sftpPort: 2200,
        services: [{ name: "cloudfactory-agent", state: "running" }, { name: "podman", state: "running" }, { name: "nftables", state: "running" }],
      });
      audit(d, "Added server", n);
    },
    apply: (d) => {
      const s = d.servers.find((x) => x.id === serverId)!;
      s.state = "healthy";
      s.stateDetail = undefined;
      s.cpuPercent = 1;
      s.memoryPercent = 8;
      s.diskPercent = 4;
    },
  });
  return { serverId, job };
}

export function setServer(serverId: string, change: { updatePolicy?: HostUpdatePolicy; maintenanceWindow?: string; gvisorPlatform?: "systrap" | "kvm"; roles?: ServerRole[] }) {
  // A new gVisor platform makes a job that writes the units again (ConfigureHost).
  if (LIVE) return call(() => live.request<JobAnswer>("PATCH", `/servers/${serverId}`, change)).then((r) => (r?.job ? track(r.job).then(() => undefined) : refresh()));
  const srv = state.servers.find((s) => s.id === serverId)!;
  if (change.gvisorPlatform === "kvm" && !srv.bareMetal) throw new ApiError("The KVM platform needs a bare-metal server.");
  update((d) => {
    const s = d.servers.find((x) => x.id === serverId)!;
    Object.assign(s, change);
    audit(d, "Changed server settings", s.name);
  });
}

/** FR-1113: the policy "tools" updates only the held container tools. */
export function installUpdates(serverId: string, policy?: "security" | "all" | "tools") {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/install-updates`, policy ? { policy } : undefined)).then((r) => track(r.job));
  const srv = state.servers.find((s) => s.id === serverId)!;
  if (policy === "tools") {
    return runJob({
      title: `Update container tools on ${srv.name}`,
      serverId,
      steps: ["Install the held packages", "Hold them again", "Restart the website containers"],
      activity: `Updated container tools on ${srv.name}`,
      apply: (d) => {
        const s = d.servers.find((x) => x.id === serverId)!;
        s.pendingUpdates = s.pendingUpdates.filter((p) => !p.containerTool);
      },
    });
  }
  // The same rule as the panel API: with no policy in the request, a server with the policy "all"
  // installs all packages, and each other server installs only the security updates. The held
  // container tools stay (FR-1113).
  const all = (policy ?? (srv.updatePolicy === "all" ? "all" : "security")) === "all";
  const installs = (p: { security: boolean; containerTool?: boolean }) => !p.containerTool && (all || p.security);
  const packages = srv.pendingUpdates.filter(installs);
  const kernel = packages.some((p) => p.name.startsWith("linux-image"));
  return runJob({
    title: `Install updates on ${srv.name}`,
    serverId,
    steps: ["Download packages", "Install packages", "Restart changed services only"],
    activity: `Installed ${packages.length} ${packages.length === 1 ? "update" : "updates"} on ${srv.name}`,
    begin: (d) => audit(d, "Installed host updates", srv.name),
    apply: (d) => {
      const s = d.servers.find((x) => x.id === serverId)!;
      s.pendingUpdates = s.pendingUpdates.filter((p) => !installs(p));
      s.lastUpdateAt = now();
      if (kernel) s.rebootRequired = true;
    },
  });
}

/** FR-1116: reboot only one server of each role at a time. */
export function rebootServer(serverId: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/reboot`)).then((r) => track(r.job));
  const srv = state.servers.find((s) => s.id === serverId)!;
  const busy = state.servers.find((s) => s.id !== serverId && s.state === "rebooting" && s.roles.some((r) => srv.roles.includes(r)));
  if (busy) throw new ApiError(`${busy.name} has the same role and is rebooting. Wait until it is healthy.`);
  return runJob({
    title: `Reboot ${srv.name}`,
    serverId,
    steps: ["Drain the edge proxy", "Stop containers", "Reboot", "Start containers", "Check all services"],
    activity: `Rebooted ${srv.name}`,
    begin: (d) => {
      const s = d.servers.find((x) => x.id === serverId)!;
      s.state = "rebooting";
      s.stateDetail = "Rebooting";
      audit(d, "Rebooted server", srv.name);
    },
    apply: (d) => {
      const s = d.servers.find((x) => x.id === serverId)!;
      s.state = "healthy";
      s.stateDetail = undefined;
      s.rebootRequired = false;
      if (s.os.startsWith("Ubuntu")) s.kernel = "6.17.0-15-generic";
    },
  });
}

/** The websites of a server that use the shared database containers of an earlier release. */
export const sharedDatabaseWebsites = (d: State, serverId: string) => d.websites.filter((w) => w.serverId === serverId && w.sharedDatabases && w.status !== "failed");

/**
 * Gives each customer its own database containers on a server from an earlier release. The
 * panel starts one job for each website that uses the shared containers. The job copies the
 * databases and the database users of the website to the containers of its customer.
 */
export function moveDatabases(serverId: string) {
  if (LIVE) {
    return call(() => live.request<{ jobs: live.ApiJob[] }>("POST", `/servers/${serverId}/move-databases`))
      .then((r) => Promise.all(r.jobs.map((job) => track(job))))
      .then(() => refresh());
  }
  const server = state.servers.find((s) => s.id === serverId)!;
  const moving = new Set(state.jobs.filter((j) => jobActive(j) && j.title.startsWith("Move the databases of ")).map((j) => j.websiteId));
  const sites = sharedDatabaseWebsites(state, serverId).filter((w) => w.status !== "starting" && !moving.has(w.id));
  if (sites.length) update((d) => audit(d, "Moved databases to the containers of each customer", server.name));
  return Promise.all(sites.map((w) => runJob({
    title: `Move the databases of ${w.name}`,
    websiteId: w.id,
    serverId,
    steps: ["Start database containers", "Stop website", "Copy databases", "Copy users", "Start website", "Remove old copies"],
    activity: `Moved the databases of ${w.name} to the containers of its customer`,
    apply: (d) => {
      site(d, w.id).sharedDatabases = false;
    },
  }))).then(() => undefined);
}

/** The panel connects to the agent of the server again and records the result. */
export function checkConnection(serverId: string) {
  if (LIVE) {
    return call(() => live.request<Parameters<typeof live.toServer>[0]>("POST", `/servers/${serverId}/check-connection`)).then((r) => update((d) => {
      const i = d.servers.findIndex((s) => s.id === serverId);
      if (i >= 0) d.servers[i] = live.toServer(r);
    }));
  }
  update((d) => {
    const s = d.servers.find((x) => x.id === serverId)!;
    s.connectionError = undefined;
    if (s.state === "degraded") s.state = "healthy";
    s.stateDetail = undefined;
  });
}

/** Removes a server with no websites from the panel, for example a server that joined with a wrong address. */
export function removeServer(serverId: string) {
  if (LIVE) return call(() => live.request("DELETE", `/servers/${serverId}`)).then(() => refresh()).then(() => undefined);
  const srv = state.servers.find((s) => s.id === serverId)!;
  const count = state.websites.filter((w) => w.serverId === serverId).length;
  if (count > 0) throw new ApiError(`This server has ${count} ${count === 1 ? "website" : "websites"}. Delete the websites first.`);
  update((d) => {
    d.servers = d.servers.filter((s) => s.id !== serverId);
    d.jobs = d.jobs.filter((j) => j.serverId !== serverId);
    audit(d, "Removed server", srv.name);
  });
}

export function updateAgent(serverId: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/update-agent`)).then((r) => track(r.job));
  const srv = state.servers.find((s) => s.id === serverId)!;
  return runJob({
    title: `Update agent on ${srv.name}`,
    ref: "0.9.4",
    serverId,
    steps: ["Download agent binary", "Check signature", "Keep previous binary for rollback", "Restart agent"],
    activity: `Updated agent on ${srv.name} to 0.9.4`,
    apply: (d) => {
      const s = d.servers.find((x) => x.id === serverId)!;
      s.agentVersion = "0.9.4";
      s.state = "healthy";
    },
  });
}

export function newJoinToken() {
  if (LIVE) {
    return call(() => live.request<{ token: string; panelCaSha256?: string | null; installUrl?: string }>("POST", "/servers/join-tokens")).then((r) => update((d) => {
      d.joinToken = r.token;
      d.joinCaSha256 = r.panelCaSha256 ?? undefined;
      d.joinInstallUrl = r.installUrl;
    }));
  }
  update((d) => {
    d.joinToken = `cfj_${hex()}${hex()}`;
    audit(d, "Made new server join token", "");
  });
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

const METHOD_NAME: Record<PanelCertificateMethod, string> = { internal: "the panel CA", letsencrypt: "Let's Encrypt" };

/**
 * The agent on the panel server changes the certificate method of the panel domain. If the new
 * certificate does not come in time, the agent goes back to the previous method.
 */
export function setPanelCertificate(method: PanelCertificateMethod) {
  if (LIVE) {
    return call(() => live.request<live.ApiPanelCertificate>("PUT", "/panel-certificate", { method })).then((r) => {
      update((d) => { d.panelCertificate = live.toPanelCertificate(r); });
      return track(r.job);
    });
  }
  const cert = state.panelCertificate;
  if (method === "letsencrypt" && !publicName(cert.domain)) {
    throw new ApiError(`Let's Encrypt gives certificates only for public domains. ${cert.domain} is not a public domain.`, { method: "Use the panel CA for this domain." });
  }
  if (method === "internal" && cert.serversWithoutCa.length > 0) {
    throw new ApiError(`These servers do not trust the panel CA yet: ${cert.serversWithoutCa.join(", ")}. Update their agent, and wait one minute.`);
  }
  if (cert.job && (cert.job.state === "running" || cert.job.state === "queued")) {
    throw new ApiError("A change of the panel certificate is in progress. Wait until it is complete.");
  }
  const panel = state.servers.find((s) => s.roles.includes("web")) ?? state.servers[0];
  const job = runJob({
    title: `Change the panel certificate to ${METHOD_NAME[method]}`,
    ref: method,
    serverId: panel?.id,
    steps: ["Change certificate method", "Get TLS certificate"],
    activity: `Changed the panel certificate to ${METHOD_NAME[method]}`,
    begin: (d) => audit(d, "Changed the panel certificate method", method),
    apply: (d) => {
      d.panelCertificate = {
        ...d.panelCertificate, method, state: "valid", reportedAt: now(),
        // Both methods give certificates for 90 days.
        expiresAt: new Date(Date.now() + 90 * DAY).toISOString(),
      };
    },
  });
  update((d) => { d.panelCertificate.job = d.jobs[0]; });
  return job;
}

// Customers, plans and branding (FR-102, FR-106, FR-202)

/** A new user or a new password. The panel shows the temporary password one time only. */
export interface NewAccess {
  email: string;
  temporaryPassword: string;
}

const EMAIL = /^[^@\s]+@[^@\s]+\.[^@\s]+$/;

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
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ id: string; user?: { email: string; temporaryPassword: string } }>("POST", "/customers", { name: input.name.trim(), email: input.email.trim(), planId: input.planId, role: input.role, ...(createUser ? { createUser: true } : {}) });
      await reload();
      return { customerId: r.id, access: r.user ? { email: r.user.email, temporaryPassword: r.user.temporaryPassword } : null };
    });
  }
  const errors: Record<string, string> = {};
  if (!input.name.trim()) errors.name = "Type a name.";
  if (!EMAIL.test(input.email)) errors.email = "Type an email address.";
  else if (createUser && state.users.some((u) => u.email === input.email.trim())) errors.email = "A user with this email exists already.";
  if (Object.keys(errors).length) throw new ApiError("Correct the customer.", errors);
  const customerId = id("cus");
  const access = createUser ? { email: input.email.trim(), temporaryPassword: newPassword().slice(0, 20) } : null;
  update((d) => {
    d.customers.push({ id: customerId, ...input, name: input.name.trim(), email: input.email.trim(), resellerId: d.session.contextId === "platform" ? undefined : d.session.contextId, createdAt: now(), suspended: false });
    audit(d, "Created customer", input.name);
    if (access) {
      d.users.push({ id: id("usr"), name: input.name.trim(), email: access.email, role: input.role, customerId, totpEnabled: false, mustChangePassword: true, createdAt: now() });
      audit(d, "Created user", access.email);
    }
  });
  return { customerId, access };
}

// Users (audit finding B5)

/**
 * The rules of the API (UserController::refuse) for the mock data. customerId is undefined
 * for an administrator.
 */
function checkUserChange(customerId: string | undefined) {
  const role = state.session.user.role;
  if (role === "owner") throw new ApiError("Website owners cannot change users.");
  if (role === "reseller" && state.customers.find((c) => c.id === customerId)?.resellerId !== state.session.contextId) {
    throw new ApiError("A reseller can change only the users of its own customers.");
  }
}

/** Makes a user for a customer. With no customer (null), the user is an administrator. */
export function addUser(input: { name: string; email: string; customerId: string | null }): Promise<NewAccess> | NewAccess {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<live.ApiUser & { temporaryPassword: string }>("POST", "/users", { name: input.name.trim(), email: input.email.trim(), customerId: input.customerId });
      await reload();
      return { email: r.email, temporaryPassword: r.temporaryPassword };
    });
  }
  const errors: Record<string, string> = {};
  const customer = state.customers.find((c) => c.id === input.customerId);
  if (!input.name.trim()) errors.name = "Type a name.";
  if (!EMAIL.test(input.email)) errors.email = "Type an email address.";
  else if (state.users.some((u) => u.email === input.email.trim())) errors.email = "A user with this email exists already.";
  if (input.customerId !== null && !customer) errors.customerId = "The customer does not exist.";
  // As in live mode: with one error, the message says what to correct.
  const messages = Object.values(errors);
  if (messages.length) throw new ApiError(messages.length === 1 ? messages[0] : "Correct the errors and try again.", errors);
  checkUserChange(customer?.id);
  const access = { email: input.email.trim(), temporaryPassword: newPassword().slice(0, 20) };
  update((d) => {
    d.users.push({ id: id("usr"), name: input.name.trim(), email: access.email, role: customer ? customer.role : "administrator", customerId: customer?.id, totpEnabled: false, mustChangePassword: true, createdAt: now() });
    audit(d, "Created user", access.email);
  });
  return access;
}

function managedUser(userId: string): ManagedUser {
  const u = state.users.find((x) => x.id === userId);
  if (!u) throw new ApiError("The user does not exist.");
  return u;
}

/**
 * Gives the user a new temporary password and signs the user out. With removeTotp, the user
 * can sign in with the password only, for example after the loss of a telephone.
 */
export function resetUserPassword(userId: string, removeTotp = false): Promise<NewAccess> | NewAccess {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<live.ApiUser & { temporaryPassword: string }>("POST", `/users/${userId}/reset-password`, removeTotp ? { removeTotp: true } : {});
      await reload();
      return { email: r.email, temporaryPassword: r.temporaryPassword };
    });
  }
  const u = managedUser(userId);
  if (u.id === state.session.user.id) throw new ApiError("Change your own password in the settings.");
  checkUserChange(u.customerId);
  const access = { email: u.email, temporaryPassword: newPassword().slice(0, 20) };
  update((d) => {
    const x = d.users.find((y) => y.id === userId)!;
    x.mustChangePassword = true;
    if (removeTotp) x.totpEnabled = false;
    audit(d, removeTotp ? "Reset password and removed TOTP" : "Reset password", u.email);
  });
  return access;
}

export function deleteUser(userId: string) {
  if (LIVE) return call(() => live.request("DELETE", `/users/${userId}`)).then(() => reload());
  const u = managedUser(userId);
  if (u.id === state.session.user.id) throw new ApiError("You cannot delete your own user.");
  checkUserChange(u.customerId);
  update((d) => {
    d.users = d.users.filter((x) => x.id !== userId);
    audit(d, "Deleted user", u.email);
  });
}

export function setCustomer(customerId: string, change: Partial<Pick<Customer, "planId" | "suspended">>) {
  if (LIVE) return call(() => live.request("PATCH", `/customers/${customerId}`, change)).then(() => refresh());
  update((d) => {
    const c = d.customers.find((x) => x.id === customerId)!;
    Object.assign(c, change);
    audit(d, change.suspended === undefined ? "Changed plan" : change.suspended ? "Suspended customer" : "Unsuspended customer", c.name);
  });
}

export function savePlan(plan: Plan) {
  if (LIVE) {
    const exists = state.plans.some((p) => p.id === plan.id);
    const body = { name: plan.name.trim(), websites: plan.websites, mailboxes: plan.mailboxes, databases: plan.databases, phpExtensions: plan.phpExtensions, limits: plan.limits };
    return call(() => live.request(exists ? "PATCH" : "POST", exists ? `/plans/${plan.id}` : "/plans", body)).then(() => refresh());
  }
  const errors: Record<string, string> = {};
  if (!plan.name.trim()) errors.name = "Type a name.";
  if (Object.values(plan.limits).some((v) => !(v > 0))) errors.limits = "Each limit must be more than 0.";
  if (Object.keys(errors).length) throw new ApiError("Correct the plan.", errors);
  update((d) => {
    const i = d.plans.findIndex((p) => p.id === plan.id);
    if (i >= 0) d.plans[i] = plan;
    else d.plans.push({ ...plan, id: id("plan") });
    audit(d, i >= 0 ? "Changed plan" : "Created plan", plan.name);
  });
}

export function setBranding(branding: Branding) {
  if (LIVE) notInRelease();
  const errors: Record<string, string> = {};
  if (!/^#[0-9a-f]{6}$/i.test(branding.colour)) errors.colour = "Use a hex colour, for example #1d5fd6.";
  if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(branding.panelDomain)) errors.panelDomain = "Type a domain name.";
  if (Object.keys(errors).length) throw new ApiError("Correct the branding.", errors);
  update((d) => {
    d.branding = branding;
    audit(d, "Changed branding", branding.name);
  });
}

// Settings (FR-103, FR-104, FR-1303, FR-1813)

/** CF-02: a new token needs the password of the user and, if set, a TOTP code. */
export function createToken(name: string, scopes: TokenScope[], days: number, password: string, totp = ""): Promise<{ token: ApiToken; secret: string }> | { token: ApiToken; secret: string } {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ id: number; token: string; scopes: TokenScope[]; expiresAt: string }>("POST", "/tokens", { name: name.trim(), scopes, expiresInDays: days, password, totp: totp.trim() || undefined });
      await refresh();
      const token: ApiToken = { id: String(r.id), name: name.trim(), scopes: r.scopes, createdAt: now(), expiresAt: r.expiresAt, prefix: `${r.id}|` };
      return { token, secret: r.token };
    });
  }
  const errors: Record<string, string> = {};
  if (!name.trim()) errors.name = "Type a name.";
  if (!scopes.length) errors.scopes = "Select one or more scopes.";
  if (!(days >= 1 && days <= 365)) errors.days = "Use from 1 to 365 days.";
  if (!password) errors.password = "Type your password.";
  if (state.totpEnabled && !/^\d{6}$/.test(totp.trim())) errors.totp = "Type the 6-digit code from your authenticator app.";
  if (Object.keys(errors).length) throw new ApiError("Correct the token.", errors);
  const prefix = `cf_live_${hex().slice(0, 4)}`;
  const secret = `${prefix}${hex()}${hex()}${hex()}`;
  const token: ApiToken = { id: id("tok"), name: name.trim(), scopes, createdAt: now(), expiresAt: new Date(Date.now() + days * DAY).toISOString(), prefix };
  update((d) => {
    d.tokens.push(token);
    audit(d, "Created API token", token.name);
  });
  return { token, secret };
}

export function revokeToken(tokenId: string) {
  if (LIVE) return call(() => live.request("DELETE", `/tokens/${tokenId}`)).then(() => refresh());
  update((d) => {
    const t = d.tokens.find((x) => x.id === tokenId)!;
    d.tokens = d.tokens.filter((x) => x.id !== tokenId);
    audit(d, "Revoked API token", t.name);
  });
}

export function addPasskey(name: string) {
  if (LIVE) notInRelease();
  update((d) => {
    d.passkeys.push({ id: id("pk"), name: name.trim() || "Passkey", addedAt: now() });
    audit(d, "Added passkey", name);
  });
}

export function removePasskey(passkeyId: string) {
  if (LIVE) notInRelease();
  if (state.passkeys.length === 1 && !state.totpEnabled) throw new ApiError("Keep one passkey, or set up TOTP first.");
  update((d) => {
    d.passkeys = d.passkeys.filter((p) => p.id !== passkeyId);
    audit(d, "Removed passkey", "");
  });
}

/** R-01: the key for a new authenticator app. TOTP is on only after setTotp(true, …). */
export function startTotpSetup(): Promise<{ secret: string; uri: string }> | { secret: string; uri: string } {
  if (LIVE) return call(() => live.request<{ secret: string; uri: string }>("POST", "/me/totp/setup"));
  const secret = "JBSWY3DPEHPK3PXP";
  return { secret, uri: `otpauth://totp/Cloudfactory:${encodeURIComponent(state.session.user.email)}?secret=${secret}&issuer=Cloudfactory` };
}

/** R-01: turns TOTP on or off. Each change needs the password and a code from the app. */
export function setTotp(enabled: boolean, code: string, password: string) {
  const errors: Record<string, string> = {};
  if (!password) errors.password = "Type your password.";
  if (!/^\d{6}$/.test(code.trim())) errors.code = "Type the 6-digit code from the app.";
  if (Object.keys(errors).length) throw new ApiError(Object.values(errors)[0], errors);
  if (LIVE) {
    return call(async () => {
      await live.request("POST", enabled ? "/me/totp/enable" : "/me/totp/disable", { password, code: code.trim() });
      await refresh();
    });
  }
  if (!enabled && state.passkeys.length === 0) throw new ApiError("Add a passkey before you remove TOTP.");
  update((d) => {
    d.totpEnabled = enabled;
    audit(d, enabled ? "Set up TOTP" : "Removed TOTP", d.session.user.email);
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
  if (LIVE) {
    return call(async () => {
      await live.request("PUT", "/me/password", { currentPassword: current, password: next, password_confirmation: confirm, totp: totp.trim() || undefined });
      await reload();
    });
  }
  update((d) => {
    d.session.mustChangePassword = false;
    d.tokens = [];
    audit(d, "Changed password", d.session.user.email);
  });
}

export function addAlertChannel(kind: AlertChannel["kind"], target: string, events: string[]) {
  if (LIVE) notInRelease();
  const errors: Record<string, string> = {};
  if (kind === "email" && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(target)) errors.target = "Type an email address.";
  if (kind === "webhook" && !/^https:\/\/\S+$/.test(target)) errors.target = "Type an HTTPS URL.";
  if (!events.length) errors.events = "Select one or more events.";
  if (Object.keys(errors).length) throw new ApiError("Correct the alert.", errors);
  update((d) => {
    d.alertChannels.push({ id: id("alc"), kind, target: target.trim(), events });
    audit(d, "Added alert", target);
  });
}

export function removeAlertChannel(channelId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    d.alertChannels = d.alertChannels.filter((c) => c.id !== channelId);
    audit(d, "Removed alert", "");
  });
}

export function testAlertChannel(channelId: string) {
  if (LIVE) notInRelease();
  const c = state.alertChannels.find((x) => x.id === channelId)!;
  return runJob({ title: `Send test alert to ${c.target}`, steps: ["Send"] });
}

export function setSystem(system: SystemSettings) {
  if (LIVE) notInRelease();
  if (system.registryMirror && !/^[a-z0-9.-]+(:\d+)?(\/[\w./-]+)?$/.test(system.registryMirror)) {
    throw new ApiError("Correct the mirror.", { registryMirror: "Type a registry host, for example registry.example.com/cloudfactory." });
  }
  update((d) => {
    d.system = system;
    audit(d, "Changed system settings", "");
  });
}

