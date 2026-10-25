// Values that the UI derives from the state.

import type { AttentionItem, RuntimeImage, SearchResult, State, Website } from "./types";
import { daysUntil, longDate } from "../lib/format";

export const imageOf = (s: State, w: Website): RuntimeImage | undefined => s.images.find((i) => i.id === w.runtime.imageId);

export const tagOf = (s: State, w: Website) => w.imageTag ?? imageOf(s, w)?.running ?? "";

/** True when a newer signed image is ready for the website. */
export function updateReady(s: State, w: Website): boolean {
  const image = imageOf(s, w);
  // The rollout of an image updates its websites in stages, so the owner does not need to act.
  return !!image?.available && image.availableNote !== "rolling_out" && tagOf(s, w) !== image.available && w.status !== "stopped";
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

export function visibleWebsites(s: State): Website[] {
  if (s.session.contextId === "platform") return s.websites;
  return s.websites.filter((w) => w.customerId === s.session.contextId);
}

export function attention(s: State): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const w of visibleWebsites(s)) {
    const end = supportEnds(s, w);
    if (end) {
      items.push({ id: `php-${w.id}`, tone: "warning", subject: w.name, text: `uses PHP ${w.runtime.phpVersion}. Security support ends on ${longDate(end)}.`, action: "Plan upgrade", href: `/websites/${w.id}/runtime` });
    }
  }
  for (const w of visibleWebsites(s)) {
    if (updateReady(s, w)) {
      items.push({ id: `upd-${w.id}`, tone: "info", subject: w.name, text: `has a security update ready for PHP ${w.runtime.phpVersion}.`, action: "Review update", href: `/websites/${w.id}` });
    }
  }
  for (const w of visibleWebsites(s)) {
    const bad = w.domains.find((d) => !d.dnsPointsHere);
    if (bad) items.push({ id: `dns-${w.id}-${bad.name}`, tone: "info", subject: bad.name, text: "does not point to this server yet.", action: "Check DNS", href: `/websites/${w.id}/domains` });
  }
  for (const m of s.mailboxes) {
    const pct = Math.round((m.usedBytes / m.quotaBytes) * 100);
    // FR-603: warn when a mailbox is 90% full.
    if (pct >= 90) items.push({ id: `mbx-${m.id}`, tone: "info", subject: m.address, text: `is ${pct}% full.`, action: "Manage mailbox", href: "/email" });
  }
  return items;
}

export function accountUsage(s: State) {
  const sites = visibleWebsites(s);
  const customer = s.customers.find((c) => c.id === s.session.contextId);
  const plan = s.plans.find((p) => p.id === customer?.planId);
  return {
    websites: { used: sites.length, limit: plan?.websites ?? sites.length },
    cpuPercent: 23,
    vcpus: 8,
    memoryBytes: { used: 9.4 * 1024 ** 3, limit: 16 * 1024 ** 3 },
    diskBytes: { used: 118 * 1024 ** 3, limit: 400 * 1024 ** 3 },
  };
}

export function search(s: State, query: string): SearchResult[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const results: SearchResult[] = [
    ...s.websites.map<SearchResult>((w) => ({ kind: "website", label: w.name, detail: w.application, href: `/websites/${w.id}` })),
    ...s.websites.flatMap((w) => w.domains.filter((d) => d.name !== w.name).map<SearchResult>((d) => ({ kind: "domain", label: d.name, detail: `Domain of ${w.name}`, href: `/websites/${w.id}/domains` }))),
    ...s.zones.map<SearchResult>((z) => ({ kind: "zone", label: z.name, detail: `DNS zone · ${z.records.length} records`, href: `/dns/${z.name}` })),
    ...s.mailboxes.map<SearchResult>((m) => ({ kind: "mailbox", label: m.address, detail: `Mailbox · ${Math.round((m.usedBytes / m.quotaBytes) * 100)}% full`, href: "/email" })),
    ...s.jobs.map<SearchResult>((j) => ({ kind: "job", label: j.ref ? `${j.title} ${j.ref}` : j.title, detail: `Job · ${j.state}`, href: j.websiteId ? `/websites/${j.websiteId}` : j.serverId ? `/servers/${j.serverId}` : "/jobs" })),
    ...s.servers.map<SearchResult>((sv) => ({ kind: "server", label: sv.name, detail: `Server · ${sv.location}`, href: `/servers/${sv.id}` })),
    ...s.images.map<SearchResult>((i) => ({ kind: "image", label: i.name, detail: `Runtime image · ${i.running}`, href: "/updates" })),
    ...s.customers.map<SearchResult>((c) => ({ kind: "customer", label: c.name, detail: `Customer · ${c.email}`, href: "/customers" })),
  ];
  return results.filter((r) => r.label.toLowerCase().includes(q) || r.detail.toLowerCase().includes(q)).slice(0, 14);
}
