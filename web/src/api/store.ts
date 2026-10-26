// The state of the panel and the actions that change it.
//
// In live mode (mode.ts), each action calls the panel API through live.ts, and the state comes
// from the API. In mock mode, this file is an in-memory panel API with demonstration data. It
// follows the flow of a change in spec section 6.3: the API checks the input, writes the
// desired state, and puts a job in the queue. A simulated agent then runs the job step by step.

import { validateRecord, type RecordInput } from "../lib/dns";
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
  DatabaseEngine,
  DnsRecord,
  DomainKind,
  FileNode,
  HostUpdatePolicy,
  Job,
  MailAlias,
  Plan,
  ServerRole,
  State,
  SystemSettings,
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
    zones: [], mailboxes: [], aliases: [], databases: [], databaseUsers: [], files: {}, snapshots: [], plans: [], customers: [],
    tokens: [], passkeys: [], totpEnabled: false, audit: [], alertChannels: [], joinToken: "",
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
      if (spec.failAt === i) {
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
    .catch(() => {
      // Keep the data that the panel has. A failed first load shows an empty panel.
      if (state.session.loading) replace({ ...state, session: { ...state.session, loading: false } });
    })
    .finally(() => (loading = null));
  return loading;
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
  return live.waitForJob(job, () => void refresh()).then(async (done) => {
    await refresh();
    return done;
  });
}

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
  setInterval(() => {
    if (state.session.signedIn && document.visibilityState === "visible") void refresh();
  }, 15000);
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
        name: input.name.trim().toLowerCase(), application: input.application, imageId: input.imageId, planId: input.planId,
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
    steps: ["Create Unix user", "Create container network", "Pull and check image signature", "Start container in gVisor", "Configure edge proxy", "Get TLS certificate"],
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
        domains: [{ name, kind: "primary", certificate: { state: "pending" }, dnsPointsHere: true }],
        releases: [],
        previews: [],
        cron: [],
        sshKeys: [],
        sftpPasswordEnabled: false,
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

export function createStagingCopy(websiteId: string): Promise<{ websiteId: string; job: Promise<Job> }> | { websiteId: string; job: Promise<Job> } {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ website: live.ApiWebsite; job: live.ApiJob }>("POST", `/websites/${websiteId}/staging`);
      update((d) => {
        d.websites = [live.toWebsite(r.website), ...d.websites.filter((w) => w.id !== r.website.id)];
      });
      return { websiteId: r.website.id, job: track(r.job) };
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
    steps: ["Stop container", "Remove container and network", "Remove edge proxy route", "Remove Unix user"],
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

export function addDomain(websiteId: string, name: string, kind: DomainKind) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/domains`, { name: name.trim().toLowerCase(), kind })).then((r) => track(r.job));
  const domain = name.trim().toLowerCase();
  if (!/^(?!-)[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(domain)) throw new ApiError("Type a domain name.", { name: "Type a domain name, for example www.example.com." });
  if (state.websites.some((w) => w.domains.some((d) => d.name === domain))) throw new ApiError("A website already uses this domain.", { name: "A website already uses this domain." });
  const ours = state.zones.find((z) => domain === z.name || domain.endsWith(`.${z.name}`));
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
  });
}

export function removeDomain(websiteId: string, name: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("DELETE", `/websites/${websiteId}/domains/${q(name)}`)).then((r) => track(r.job));
  update((d) => {
    const w = site(d, websiteId);
    const dm = w.domains.find((x) => x.name === name);
    if (dm?.kind === "primary") throw new ApiError("Make a different domain primary first.");
    w.domains = w.domains.filter((x) => x.name !== name);
    audit(d, "Removed domain", `${w.name} ${name}`);
  });
}

export function makePrimary(websiteId: string, name: string) {
  if (LIVE) notInRelease();
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
  if (LIVE) return refresh();
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

export function setAccess(websiteId: string, change: { shellEnabled?: boolean; sftpPasswordEnabled?: boolean }) {
  if (LIVE) notInRelease();
  update((d) => {
    const w = site(d, websiteId);
    Object.assign(w, change);
    audit(d, "Changed SFTP and SSH access", w.name);
  });
}

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

export function restore(snapshotId: string, scope: RestoreScope) {
  if (LIVE) {
    const snapshot = state.snapshots.find((x) => x.id === snapshotId);
    if (!snapshot) throw new ApiError("The snapshot does not exist.");
    const item = scope.kind === "website" ? undefined : scope.kind === "file" ? scope.path : scope.name;
    return call(() => live.request<JobAnswer>("POST", `/websites/${snapshot.websiteId}/snapshots/${snapshotId}/restore`, { scope: scope.kind, item })).then((r) => track(r.job));
  }
  const snap = state.snapshots.find((s) => s.id === snapshotId)!;
  const w = state.websites.find((x) => x.id === snap.websiteId)!;
  const what = scope.kind === "website" ? "full website" : scope.kind === "file" ? scope.path : scope.name;
  return runJob({
    title: `Restore ${what}`,
    websiteId: w.id,
    steps: scope.kind === "website" ? ["Download snapshot", "Restore files", "Restore databases", "Restore mailboxes", "Health check"] : ["Download snapshot", "Restore"],
    activity: `Restored ${what} on ${w.name}`,
    begin: (d) => audit(d, "Restored from backup", `${w.name} ${what}`),
  });
}

export function setBackupPolicy(policy: BackupPolicy) {
  if (LIVE) return call(() => live.request<BackupPolicy>("PUT", "/backup-policy", policy)).then(() => refresh());
  const errors: Record<string, string> = {};
  (["daily", "weekly", "monthly"] as const).forEach((k) => {
    if (!Number.isInteger(policy[k]) || policy[k] < 0 || policy[k] > 365) errors[k] = "Use a number from 0 to 365.";
  });
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(policy.time)) errors.time = "Use the format HH:MM.";
  if (!/^(s3|b2|azure|gs):\/\/\S+$/.test(policy.target)) errors.target = "Use an object storage URL, for example s3://bucket/path.";
  if (Object.keys(errors).length) throw new ApiError("Correct the backup policy.", errors);
  update((d) => {
    d.backupPolicy = policy;
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

const DB_NAME = /^[a-z][a-z0-9_]{1,62}$/;

export function createDatabase(websiteId: string, engine: DatabaseEngine, name: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/websites/${websiteId}/databases`, { engine, name: name.trim() })).then((r) => track(r.job));
  const n = name.trim();
  if (!DB_NAME.test(n)) throw new ApiError("Correct the name.", { name: "Start with a letter. Use lower-case letters, digits and underscores." });
  if (state.databases.some((x) => x.name === n && x.engine === engine)) throw new ApiError("This database exists.", { name: "This database exists." });
  const w = state.websites.find((x) => x.id === websiteId)!;
  return runJob({
    title: `Create ${engine === "mariadb" ? "MariaDB" : "PostgreSQL"} database ${n}`,
    websiteId,
    steps: ["Create database", "Create user", "Allow the website container only"],
    activity: `Created database ${n} for ${w.name}`,
    begin: (d) => audit(d, "Created database", `${w.name} ${n}`),
    apply: (d) => {
      d.databases.push({ id: id("db"), name: n, engine, websiteId, sizeBytes: 0, serverId: w.serverId });
      d.databaseUsers.push({ id: id("dbu"), name: n, engine, websiteId, databases: [n], access: "website" });
    },
  });
}

export function deleteDatabase(databaseId: string) {
  if (LIVE) {
    const db = state.databases.find((x) => x.id === databaseId);
    if (!db) throw new ApiError("The database does not exist.");
    return call(() => live.request<JobAnswer>("DELETE", `/websites/${db.websiteId}/databases/${databaseId}`)).then((r) => track(r.job));
  }
  update((d) => {
    const db = d.databases.find((x) => x.id === databaseId)!;
    d.databases = d.databases.filter((x) => x.id !== databaseId);
    d.databaseUsers.forEach((u) => (u.databases = u.databases.filter((n) => n !== db.name)));
    audit(d, "Removed database", db.name);
  });
}

export function createDatabaseUser(websiteId: string, engine: DatabaseEngine, name: string, databases: string[]): string {
  if (LIVE) notInRelease();
  const n = name.trim();
  if (!DB_NAME.test(n)) throw new ApiError("Correct the name.", { name: "Start with a letter. Use lower-case letters, digits and underscores." });
  if (state.databaseUsers.some((x) => x.name === n && x.engine === engine)) throw new ApiError("This user exists.", { name: "This user exists." });
  const password = Array.from(crypto.getRandomValues(new Uint8Array(18)), (b) => "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"[b % 56]).join("");
  update((d) => {
    d.databaseUsers.push({ id: id("dbu"), name: n, engine, websiteId, databases, access: "website" });
    audit(d, "Created database user", n);
  });
  return password;
}

export function setDatabaseUser(userId: string, change: { databases?: string[]; access?: "website" | "anywhere" }) {
  if (LIVE) notInRelease();
  update((d) => {
    const u = d.databaseUsers.find((x) => x.id === userId)!;
    Object.assign(u, change);
    audit(d, "Changed database user", u.name);
  });
}

export function deleteDatabaseUser(userId: string) {
  if (LIVE) notInRelease();
  update((d) => {
    const u = d.databaseUsers.find((x) => x.id === userId)!;
    d.databaseUsers = d.databaseUsers.filter((x) => x.id !== userId);
    audit(d, "Removed database user", u.name);
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

export function addImage(name: string, reference: string, tag: string, policy: UpdatePolicy) {
  if (LIVE) return call(() => live.request("POST", "/images", { name: name.trim(), reference: reference.trim(), tag: tag.trim(), policy })).then(() => refresh()).then(() => undefined);
  const errors: Record<string, string> = {};
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
      d.images.push({ id: imageId, name: name.trim(), family: "custom", reference: reference.trim(), running: tag.trim(), websites: "0", policy, custom: true, signatureVerified: false, digest: `sha256:${hex()}…${hex().slice(0, 4)}` });
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

/** The simulated agents move the rollout forward while it runs. */
function rolloutTick() {
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

if (typeof window !== "undefined" && !import.meta.env?.VITEST) {
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
        maintenanceWindow: "02:00 to 05:00 (Europe/London)", pendingUpdates: [], lastUpdateAt: now(), rebootRequired: false,
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
  if (LIVE) return call(() => live.request("PATCH", `/servers/${serverId}`, change)).then(() => refresh());
  const srv = state.servers.find((s) => s.id === serverId)!;
  if (change.gvisorPlatform === "kvm" && !srv.bareMetal) throw new ApiError("The KVM platform needs a bare-metal server.");
  update((d) => {
    const s = d.servers.find((x) => x.id === serverId)!;
    Object.assign(s, change);
    audit(d, "Changed server settings", s.name);
  });
}

export function installUpdates(serverId: string) {
  if (LIVE) return call(() => live.request<JobAnswer>("POST", `/servers/${serverId}/install-updates`)).then((r) => track(r.job));
  const srv = state.servers.find((s) => s.id === serverId)!;
  const kernel = srv.pendingUpdates.some((p) => p.name.startsWith("linux-image"));
  return runJob({
    title: `Install updates on ${srv.name}`,
    serverId,
    steps: ["Download packages", "Install packages", "Restart changed services only"],
    activity: `Installed ${srv.pendingUpdates.length} updates on ${srv.name}`,
    begin: (d) => audit(d, "Installed host updates", srv.name),
    apply: (d) => {
      const s = d.servers.find((x) => x.id === serverId)!;
      s.pendingUpdates = [];
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
    return call(() => live.request<{ token: string }>("POST", "/servers/join-tokens")).then((r) => update((d) => {
      d.joinToken = r.token;
    }));
  }
  update((d) => {
    d.joinToken = `cfj_${hex()}${hex()}`;
    audit(d, "Made new server join token", "");
  });
}

// Customers, plans and branding (FR-102, FR-106, FR-202)

export function addCustomer(input: Pick<Customer, "name" | "email" | "planId" | "role">) {
  if (LIVE) return call(() => live.request("POST", "/customers", { name: input.name.trim(), email: input.email.trim(), planId: input.planId, role: input.role })).then(() => refresh());
  const errors: Record<string, string> = {};
  if (!input.name.trim()) errors.name = "Type a name.";
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(input.email)) errors.email = "Type an email address.";
  if (Object.keys(errors).length) throw new ApiError("Correct the customer.", errors);
  update((d) => {
    d.customers.push({ id: id("cus"), ...input, name: input.name.trim(), resellerId: d.session.contextId === "platform" ? undefined : d.session.contextId, createdAt: now(), suspended: false });
    audit(d, "Created customer", input.name);
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

export function createToken(name: string, scopes: TokenScope[], days: number): Promise<{ token: ApiToken; secret: string }> | { token: ApiToken; secret: string } {
  if (LIVE) {
    return call(async () => {
      const r = await live.request<{ id: number; token: string; scopes: TokenScope[]; expiresAt: string }>("POST", "/tokens", { name: name.trim(), scopes, expiresInDays: days });
      await refresh();
      const token: ApiToken = { id: String(r.id), name: name.trim(), scopes: r.scopes, createdAt: now(), expiresAt: r.expiresAt, prefix: `${r.id}|` };
      return { token, secret: r.token };
    });
  }
  const errors: Record<string, string> = {};
  if (!name.trim()) errors.name = "Type a name.";
  if (!scopes.length) errors.scopes = "Select one or more scopes.";
  if (!(days >= 1 && days <= 365)) errors.days = "Use from 1 to 365 days.";
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

export function setTotp(enabled: boolean, code?: string) {
  if (LIVE) notInRelease();
  if (enabled && !/^\d{6}$/.test(code ?? "")) throw new ApiError("Type the 6-digit code.", { code: "Type the 6-digit code from the app." });
  if (!enabled && state.passkeys.length === 0) throw new ApiError("Add a passkey before you remove TOTP.");
  update((d) => {
    d.totpEnabled = enabled;
    audit(d, enabled ? "Set up TOTP" : "Removed TOTP", d.session.user.email);
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

