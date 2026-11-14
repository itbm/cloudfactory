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
  const results: SearchResult[] = [
    ...s.websites.map<SearchResult>((w) => ({ kind: "website", label: w.name, detail: w.application, href: `/websites/${w.id}` })),
    ...s.websites.flatMap((w) => w.domains.filter((d) => d.name !== w.name).map<SearchResult>((d) => ({ kind: "domain", label: d.name, detail: `Domain of ${w.name}`, href: `/websites/${w.id}/domains` }))),
    ...s.jobs.map<SearchResult>((j) => ({ kind: "job", label: j.ref ? `${j.title} ${j.ref}` : j.title, detail: `Job · ${j.state}`, href: j.websiteId ? `/websites/${j.websiteId}` : j.serverId ? `/servers/${j.serverId}` : "/jobs" })),
    ...s.servers.map<SearchResult>((sv) => ({ kind: "server", label: sv.name, detail: `Server · ${sv.location}`, href: `/servers/${sv.id}` })),
    ...s.images.map<SearchResult>((i) => ({ kind: "image", label: i.name, detail: `Runtime image · ${i.running}`, href: "/updates" })),
    ...s.customers.map<SearchResult>((c) => ({ kind: "customer", label: c.name, detail: `Customer · ${c.email}`, href: "/customers" })),
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
