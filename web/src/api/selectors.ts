// Values that the UI derives from the state.

import type { AttentionItem, RuntimeImage, SearchResult, Server, State, Website } from "./types";
import { daysUntil, longDate } from "../lib/format";

export const imageOf = (s: State, w: Website): RuntimeImage | undefined => s.images.find((i) => i.id === w.runtime.imageId);

export const tagOf = (s: State, w: Website) => w.imageTag ?? imageOf(s, w)?.running ?? "";

/** True when a newer signed image is ready for the website. */
export function updateReady(s: State, w: Website): boolean {
  const image = imageOf(s, w);
  // The rollout of an image updates its websites in stages, so the owner does not need to act.
  return !!image?.available && image.availableNote !== "rolling_out" && tagOf(s, w) !== image.available && w.status !== "stopped" && w.status !== "failed";
}

/** FR-306: warn 90 days before the security support for a PHP version stops. */
export function supportEnds(s: State, w: Website): string | undefined {
  const end = imageOf(s, w)?.supportEndsAt;
  return end && daysUntil(end) <= 90 ? end : undefined;
}

export function runtimeLabel(s: State, w: Website): string {
  const image = imageOf(s, w);
  switch (w.runtime.kind) {
    case "frankenphp":
      return `FrankenPHP${w.runtime.workerMode ? " worker" : ""} · PHP ${w.runtime.phpVersion}`;
    case "apache-php-fpm":
      return `Apache + PHP-FPM ${w.runtime.phpVersion}`;
    case "static":
      return "Caddy · static files";
    case "oci":
      return `${image?.name.replace(/\.js/, "") ?? "Own image"} · OCI image`;
  }
}

/**
 * True when the customer is in the selected account: the account itself, or a customer of the
 * account when the account is a reseller.
 */
export function inContext(s: State, customerId: string): boolean {
  const ctx = s.session.contextId;
  return ctx === "platform" || customerId === ctx || s.customers.some((c) => c.id === customerId && c.resellerId === ctx);
}

export function visibleWebsites(s: State): Website[] {
  return s.websites.filter((w) => inContext(s, w.customerId));
}

/** The customers of the selected account that can own a website. */
export function contextCustomers(s: State) {
  return s.customers.filter((c) => inContext(s, c.id));
}

export function attention(s: State): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const w of visibleWebsites(s)) {
    // The first problem of a website that the panel could not make is that it has no container.
    if (w.status === "failed") {
      items.push({ id: `failed-${w.id}`, tone: "warning", subject: w.name, text: "was not made. Look at the cause, and try again.", action: "Open website", href: `/websites/${w.id}` });
    }
  }
  for (const w of visibleWebsites(s)) {
    const end = supportEnds(s, w);
    if (end) {
      items.push({ id: `php-${w.id}`, tone: "warning", subject: w.name, text: `uses PHP ${w.runtime.phpVersion}. Security support ends on ${longDate(end)}.`, action: "Plan upgrade", href: `/websites/${w.id}/runtime` });
    }
  }
  for (const w of visibleWebsites(s)) {
    if (updateReady(s, w)) {
      items.push({ id: `upd-${w.id}`, tone: "info", subject: w.name, text: `has an image update ready for PHP ${w.runtime.phpVersion}.`, action: "Review update", href: `/websites/${w.id}` });
    }
  }
  for (const w of visibleWebsites(s)) {
    // The agent examines the DNS of a domain only when the website has a container.
    const bad = w.status === "failed" ? undefined : w.domains.find((d) => !d.dnsPointsHere);
    if (bad) items.push({ id: `dns-${w.id}-${bad.name}`, tone: "info", subject: bad.name, text: "does not point to this server yet.", action: "Check DNS", href: `/websites/${w.id}/domains` });
  }
  return items;
}

/** The use of the shown websites against their limits. A website's CPU use is a percentage of one vCPU. */
export function accountUsage(s: State) {
  const sites = visibleWebsites(s);
  const customer = s.customers.find((c) => c.id === s.session.contextId);
  const plan = s.plans.find((p) => p.id === customer?.planId);
  const sum = (f: (w: Website) => number) => sites.reduce((n, w) => n + f(w), 0);
  const vcpus = sum((w) => w.limits.vcpus);
  return {
    websites: { used: sites.length, limit: plan?.websites },
    cpuPercent: vcpus ? Math.round(sum((w) => w.use.cpuPercent) / vcpus) : 0,
    vcpus,
    memoryBytes: { used: sum((w) => w.use.memoryBytes), limit: sum((w) => w.limits.memoryBytes) },
    diskBytes: { used: sum((w) => w.use.diskBytes), limit: sum((w) => w.limits.diskBytes) },
  };
}

export function search(s: State, query: string): SearchResult[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const role = s.session.user.role;
  const results: SearchResult[] = [
    ...s.websites.map<SearchResult>((w) => ({ kind: "website", label: w.name, detail: w.application, href: `/websites/${w.id}` })),
    ...s.websites.flatMap((w) => w.domains.filter((d) => d.name !== w.name).map<SearchResult>((d) => ({ kind: "domain", label: d.name, detail: `Domain of ${w.name}`, href: `/websites/${w.id}/domains` }))),
    ...s.jobs.map<SearchResult>((j) => ({ kind: "job", label: j.ref ? `${j.title} ${j.ref}` : j.title, detail: `Job · ${j.state}`, href: j.websiteId ? `/websites/${j.websiteId}` : j.serverId ? `/servers/${j.serverId}` : "/jobs" })),
    // Each console finds only what its pages show (lib/roles.ts).
    ...(role === "administrator" ? s.servers.map<SearchResult>((sv) => ({ kind: "server", label: sv.name, detail: `Server · ${sv.location}`, href: `/servers/${sv.id}` })) : []),
    ...(role === "administrator" ? s.images.map<SearchResult>((i) => ({ kind: "image", label: i.name, detail: `Runtime image · ${i.running}`, href: "/updates" })) : []),
    ...(role !== "owner" ? s.customers.map<SearchResult>((c) => ({ kind: "customer", label: c.name, detail: `Customer · ${c.email}`, href: "/customers" })) : []),
  ];
  return results.filter((r) => r.label.toLowerCase().includes(q) || r.detail.toLowerCase().includes(q)).slice(0, 14);
}

/** The necessary services of a server that do not run. These are the cause of the state "degraded". */
export function failedServices(services: Server["services"]): Server["services"] {
  return services.filter((x) => x.state !== "running" && !x.optional);
}

/** A short text for the status of a degraded server, for example "Degraded: cf-sftp failed". */
export function degradedReason(services: Server["services"]): string | undefined {
  const failed = failedServices(services);
  if (!failed.length) return undefined;
  return `Degraded: ${failed.map((x) => `${x.name} ${x.state}`).join(", ")}`;
}

/**
 * What an administrator must look at on the platform: servers, updates, images and jobs. The
 * most important items come first.
 */
export function platformAttention(s: State): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const sv of s.servers) {
    const href = `/servers/${sv.id}`;
    const failed = failedServices(sv.services);
    if (sv.state === "offline") items.push({ id: `off-${sv.id}`, tone: "warning", subject: sv.name, text: "is offline. Its websites can be down, and its jobs fail.", action: "Open server", href });
    else if (sv.connectionError) items.push({ id: `con-${sv.id}`, tone: "warning", subject: sv.name, text: "sends reports, but the panel cannot connect to its agent. Each job for this server fails.", action: "Open server", href });
    else if (failed.length) items.push({ id: `svc-${sv.id}`, tone: "warning", subject: sv.name, text: `is degraded: ${failed.map((x) => `${x.name} ${x.state}`).join(", ")}.`, action: "Open server", href });
  }
  for (const w of s.websites) {
    if (w.status === "failed") items.push({ id: `failed-${w.id}`, tone: "warning", subject: w.name, text: "was not made. Look at the cause, and try again.", action: "Open website", href: `/websites/${w.id}` });
  }
  for (const sv of s.servers) {
    const href = `/servers/${sv.id}`;
    const security = sv.pendingUpdates.filter((u) => u.security).length;
    if (sv.rebootRequired) items.push({ id: `boot-${sv.id}`, tone: "warning", subject: sv.name, text: "must restart to complete its updates.", action: "Open server", href });
    if (security) items.push({ id: `sec-${sv.id}`, tone: "warning", subject: sv.name, text: `has ${security} security ${security === 1 ? "update" : "updates"} to install.`, action: "Review updates", href });
    else if (sv.pendingUpdates.length) items.push({ id: `upd-${sv.id}`, tone: "info", subject: sv.name, text: `has ${sv.pendingUpdates.length} ${sv.pendingUpdates.length === 1 ? "update" : "updates"} to install.`, action: "Review updates", href });
    if (sv.agentUpdateAvailable) items.push({ id: `agent-${sv.id}`, tone: "info", subject: sv.name, text: `has agent ${sv.agentVersion}. A newer agent is ready.`, action: "Open server", href });
  }
  for (const i of s.images) {
    if (i.checkError) items.push({ id: `chk-${i.id}`, tone: "warning", subject: i.name, text: "could not be checked for updates. The panel does not know if a newer image exists.", action: "Open images", href: "/updates" });
    else if (i.available && i.availableNote !== "rolling_out") items.push({ id: `img-${i.id}`, tone: "info", subject: i.name, text: `has the update ${i.available} ready to roll out.`, action: "Review update", href: "/updates" });
    if (i.supportEndsAt && daysUntil(i.supportEndsAt) <= 90) items.push({ id: `eol-${i.id}`, tone: "warning", subject: i.name, text: `loses security support on ${longDate(i.supportEndsAt)}. Move its websites to a newer PHP version.`, action: "Open images", href: "/updates" });
  }
  return items;
}

/**
 * True when the panel can make backups. An administrator sees the object storage. A customer
 * sees only whether the panel has one (GET /account).
 */
export function backupsReady(s: State): boolean {
  if (s.session.user.role === "administrator") return !!s.backupPolicy.target;
  return s.account?.backupsAvailable ?? true;
}
