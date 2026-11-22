// The Cloudflare side of the plugin: the four operations of the contract cf-dns-provider/1.
// It has no dependencies. Refer to docs/plugins.md#dns-providers.
//
// The panel keeps the records. "sync" makes the records at Cloudflare the same as the records
// of the panel, for the types that the panel manages. Records of other types stay, and so do
// the NS records of the zone itself. A record that stays the same keeps its proxy setting.

/** The record types that the panel manages. The plugin does not change records of other types. */
export const TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"];

/** The TTL of Cloudflare for "automatic" (1). Cloudflare uses 300 seconds for it. */
const AUTO_TTL = 300;

/** The largest TTL that Cloudflare accepts. */
const MAX_TTL = 86400;

/** An error that the panel shows to the user. "status" is the HTTP status of the answer to the panel. */
export class ProviderError extends Error {
  constructor(message, status = 502) {
    super(message);
    this.status = status;
  }
}

/** A client of the Cloudflare API v4, with an API token. */
export class Cloudflare {
  constructor({ token, base = "https://api.cloudflare.com/client/v4", timeout = 20000, fetch: fetcher = globalThis.fetch } = {}) {
    this.token = token;
    this.base = base.replace(/\/$/, "");
    this.timeout = timeout;
    this.fetch = fetcher;
  }

  async call(method, path, body) {
    let r;
    try {
      r = await this.fetch(`${this.base}${path}`, {
        method,
        headers: { Authorization: `Bearer ${this.token}`, "Content-Type": "application/json", Accept: "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(this.timeout),
      });
    } catch (error) {
      throw new ProviderError(`Cloudflare did not answer: ${error.message}`);
    }
    const data = await r.json().catch(() => ({}));
    if (!r.ok || data.success === false) {
      const first = (data.errors ?? [])[0];
      const text = first ? `${first.message}${first.code ? ` (code ${first.code})` : ""}` : `HTTP status ${r.status}`;
      // A 4xx answer of Cloudflare is a problem of the token or of the request: the panel shows it.
      throw new ProviderError(`Cloudflare refused the request: ${text}`, r.status >= 400 && r.status < 500 ? 422 : 502);
    }
    return data;
  }

  /** All the pages of a list. */
  async all(path) {
    const out = [];
    for (let page = 1; ; page++) {
      const sep = path.includes("?") ? "&" : "?";
      const data = await this.call("GET", `${path}${sep}page=${page}&per_page=1000`);
      out.push(...(data.result ?? []));
      const pages = data.result_info?.total_pages ?? 1;
      if (page >= pages || (data.result ?? []).length === 0) return out;
    }
  }
}

/** The zone at Cloudflare, or null. */
async function findZone(cf, name, accountId) {
  const query = `/zones?name=${encodeURIComponent(name)}${accountId ? `&account.id=${encodeURIComponent(accountId)}` : ""}`;
  const data = await cf.call("GET", query);
  return (data.result ?? []).find((z) => z.name === name) ?? null;
}

/** The full name of a record. */
const absolute = (zone, name) => (name === "@" ? zone : `${name}.${zone}`);

/** The name of a record in the zone: "@" or, for example, "www". */
function relative(zone, name) {
  const n = name.toLowerCase().replace(/\.$/, "");
  return n === zone ? "@" : n.endsWith(`.${zone}`) ? n.slice(0, -zone.length - 1) : n;
}

/** The text of a TXT record in quotation marks, in parts of 255 characters, as Cloudflare wants it. */
export function quoteTxt(text) {
  const parts = [];
  for (let i = 0; i < text.length; i += 255) parts.push(text.slice(i, i + 255));
  return parts.map((p) => `"${p.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`).join(" ");
}

/** The text of a TXT record of Cloudflare, with no quotation marks. Parts are joined. */
export function unquoteTxt(content) {
  const s = String(content ?? "").trim();
  if (!s.startsWith('"')) return s;
  let out = "";
  for (let i = 0; i < s.length; i++) {
    if (s[i] !== '"') continue;
    for (i++; i < s.length && s[i] !== '"'; i++) {
      if (s[i] === "\\" && i + 1 < s.length) i++;
      out += s[i];
    }
  }
  return out;
}

/** A record of Cloudflare in the form of the panel, or null for a record that the panel does not take. */
export function toPanel(zone, r) {
  const name = relative(zone, r.name);
  const ttl = r.ttl === 1 ? AUTO_TTL : r.ttl;
  const host = (v) => String(v ?? "").toLowerCase().replace(/\.$/, "");
  switch (r.type) {
    case "A":
      return { name, type: r.type, ttl, value: r.content, priority: null };
    case "AAAA":
      return { name, type: r.type, ttl, value: String(r.content).toLowerCase(), priority: null };
    case "CNAME":
    case "NS":
      return { name, type: r.type, ttl, value: host(r.content), priority: null };
    case "MX":
      return { name, type: r.type, ttl, value: host(r.content), priority: r.priority ?? 0 };
    case "TXT":
      return { name, type: r.type, ttl, value: unquoteTxt(r.content), priority: null };
    case "SRV": {
      const d = r.data ?? {};
      if (d.target === undefined) {
        // An older answer has only "content": "weight port target".
        const [weight, port, target] = String(r.content ?? "").split(/\s+/);
        return { name, type: r.type, ttl, value: `${weight} ${port} ${host(target)}`, priority: r.priority ?? 0 };
      }
      return { name, type: r.type, ttl, value: `${d.weight} ${d.port} ${host(d.target)}`, priority: d.priority ?? r.priority ?? 0 };
    }
    case "CAA": {
      const d = r.data;
      return { name, type: r.type, ttl, value: d ? `${d.flags} ${String(d.tag).toLowerCase()} "${d.value}"` : String(r.content), priority: null };
    }
    default:
      // The panel does not have this type. The panel shows it as a skipped record.
      return { name, type: r.type, ttl, value: String(r.content ?? ""), priority: null };
  }
}

/** A record of the panel in the form of the Cloudflare API, with no "proxied" and no "id". */
export function toCloudflare(zone, r) {
  const out = { type: r.type, name: absolute(zone, r.name), ttl: Math.min(MAX_TTL, Math.max(60, r.ttl)) };
  switch (r.type) {
    case "MX":
      return { ...out, content: r.value, priority: r.priority };
    case "TXT":
      return { ...out, content: quoteTxt(r.value) };
    case "SRV": {
      const [weight, port, target] = r.value.split(" ");
      return { ...out, data: { priority: r.priority, weight: Number(weight), port: Number(port), target } };
    }
    case "CAA": {
      const m = /^(\d+) (\S+) "(.*)"$/.exec(r.value);
      return { ...out, data: { flags: Number(m?.[1] ?? 0), tag: m?.[2] ?? "issue", value: m?.[3] ?? "" } };
    }
    default:
      return { ...out, content: r.value };
  }
}

/** The key of a record for the compare: the type, the name, the value and the priority. */
const key = (r) => `${r.type} ${r.name} ${r.type === "TXT" ? r.value : r.value.toLowerCase()} ${r.priority ?? ""}`;

/**
 * The changes that make the records of Cloudflare the same as the records of the panel. A
 * record of the panel with no record of the same key at Cloudflare changes a record of the
 * same name and type, when there is one, so that the record keeps its proxy setting and its
 * comment. The other records of Cloudflare go.
 */
export function plan(zone, desired, existing) {
  const managed = existing.filter((r) => TYPES.includes(r.type) && !(r.type === "NS" && relative(zone, r.name) === "@"));
  const have = new Map();
  for (const r of managed) {
    const k = key(toPanel(zone, r));
    if (!have.has(k)) have.set(k, []);
    have.get(k).push(r);
  }
  const posts = [];
  const patches = [];
  const unmatched = [];
  for (const want of desired) {
    const list = have.get(key(want));
    const found = list?.shift();
    if (!found) {
      unmatched.push(want);
      continue;
    }
    // A proxied record has the automatic TTL. Its TTL does not change.
    const ttl = Math.min(MAX_TTL, Math.max(60, want.ttl));
    if (!found.proxied && found.ttl !== ttl) patches.push({ id: found.id, ttl });
  }
  const left = [...have.values()].flat();
  for (const want of unmatched) {
    const i = left.findIndex((r) => r.type === want.type && relative(zone, r.name) === want.name);
    if (i >= 0) {
      const [old] = left.splice(i, 1);
      const body = toCloudflare(zone, want);
      // A proxied record keeps the proxy, and the automatic TTL.
      patches.push(old.proxied ? { id: old.id, ...body, ttl: 1, proxied: true } : { id: old.id, ...body });
    } else {
      posts.push(toCloudflare(zone, want));
    }
  }
  const deletes = left.map((r) => ({ id: r.id }));
  return { deletes, patches, posts };
}

/** Sends the changes with the batch endpoint, in parts of "size" changes. Each part is one transaction at Cloudflare. */
async function apply(cf, zoneId, changes, size) {
  const queue = [...changes.deletes.map((d) => ["deletes", d]), ...changes.patches.map((p) => ["patches", p]), ...changes.posts.map((p) => ["posts", p])];
  for (let i = 0; i < queue.length; i += size) {
    const batch = {};
    for (const [kind, item] of queue.slice(i, i + size)) (batch[kind] ??= []).push(item);
    await cf.call("POST", `/zones/${zoneId}/dns_records/batch`, batch);
  }
}

/** The state of DNSSEC at Cloudflare, in the form of the contract. */
function dnssecState(d) {
  const state = d.status === "active" ? "active" : d.status === "pending" ? "pending" : "off";
  const ds = state !== "off" && d.digest ? [{ keyTag: Number(d.key_tag), algorithm: Number(d.algorithm), digestType: Number(d.digest_type), digest: String(d.digest) }] : [];
  return { state, ds };
}

/**
 * Runs one operation of the contract. "request" is the body from the panel. The answer is the
 * body of the answer to the panel.
 */
export async function handle(request, { base, batchSize = 200, fetch: fetcher } = {}) {
  const { operation, credentials = {}, zone } = request;
  const token = String(credentials.apiToken ?? "").trim();
  const accountId = String(credentials.accountId ?? "").trim();
  if (!token) throw new ProviderError("Enter the API token of Cloudflare.", 422);
  const cf = new Cloudflare({ token, base, fetch: fetcher });

  if (operation === "verify") {
    // An account token has its own endpoint to check it.
    const user = await cf.call("GET", "/user/tokens/verify").catch(() => null);
    if (!user && accountId) await cf.call("GET", `/accounts/${encodeURIComponent(accountId)}/tokens/verify`);
    else if (!user) throw new ProviderError("Cloudflare refused the API token. Make a token with the permissions Zone: Read and DNS: Edit.", 422);
    if (!accountId) return { account: null };
    const account = await cf.call("GET", `/accounts/${encodeURIComponent(accountId)}`);
    return { account: account.result?.name ?? null };
  }

  const name = String(zone?.name ?? "").toLowerCase();
  if (!name) throw new ProviderError("The request has no zone.", 422);
  const found = await findZone(cf, name, accountId);

  if (operation === "records") {
    if (!found) return { exists: false, records: [], nameServers: [] };
    const records = await cf.all(`/zones/${found.id}/dns_records`);
    return {
      exists: true,
      records: records.filter((r) => !(r.type === "NS" && relative(name, r.name) === "@")).map((r) => toPanel(name, r)),
      nameServers: found.name_servers ?? [],
    };
  }

  if (operation === "remove") {
    if (found) await cf.call("DELETE", `/zones/${found.id}`);
    return {};
  }

  if (operation !== "sync") throw new ProviderError(`The operation "${operation}" is not known.`, 400);
  let z = found;
  if (!z) {
    if (!accountId) throw new ProviderError(`The zone ${name} is not in the Cloudflare account. Add the zone at Cloudflare, or give the account ID so that the plugin can add it.`, 422);
    z = (await cf.call("POST", "/zones", { name, account: { id: accountId }, type: "full" })).result;
  }
  const existing = await cf.all(`/zones/${z.id}/dns_records`);
  const changes = plan(name, (zone.records ?? []).filter((r) => TYPES.includes(r.type)), existing);
  await apply(cf, z.id, changes, batchSize);

  let dnssec = (await cf.call("GET", `/zones/${z.id}/dnssec`)).result ?? {};
  const on = dnssec.status === "active" || dnssec.status === "pending";
  if (zone.dnssec && !on) dnssec = (await cf.call("PATCH", `/zones/${z.id}/dnssec`, { status: "active" })).result ?? {};
  if (!zone.dnssec && on) dnssec = (await cf.call("PATCH", `/zones/${z.id}/dnssec`, { status: "disabled" })).result ?? {};

  return {
    nameServers: z.name_servers ?? [],
    active: z.status === "active",
    dnssec: dnssecState(dnssec),
    changes: { added: changes.posts.length, changed: changes.patches.length, removed: changes.deletes.length },
  };
}
