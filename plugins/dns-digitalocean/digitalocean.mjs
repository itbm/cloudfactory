// The DigitalOcean side of the plugin: the four operations of the contract cf-dns-provider/1.
// It has no dependencies. Refer to docs/plugins.md#dns-providers.
//
// The panel keeps the records. "sync" makes the records at DigitalOcean the same as the records
// of the panel, for the types that the panel manages. Records of other types stay, and so do the
// NS records of the domain itself. DigitalOcean has no DNSSEC.

/** The record types that the panel manages. The plugin does not change records of other types. */
export const TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"];

/** The name servers of DigitalOcean. Each domain has them. */
export const NAME_SERVERS = ["ns1.digitalocean.com", "ns2.digitalocean.com", "ns3.digitalocean.com"];

/** The number of requests that the plugin sends at the same time. The API accepts 250 requests in one minute. */
const PARALLEL = 4;

/** An error that the panel shows to the user. "status" is the HTTP status of the answer to the panel. */
export class ProviderError extends Error {
  constructor(message, status = 502) {
    super(message);
    this.status = status;
  }
}

/** A client of the DigitalOcean API v2, with a personal access token. */
export class DigitalOcean {
  constructor({ token, base = "https://api.digitalocean.com/v2", timeout = 20000, fetch: fetcher = globalThis.fetch } = {}) {
    this.token = token;
    this.base = base.replace(/\/$/, "");
    this.timeout = timeout;
    this.fetch = fetcher;
  }

  /** The body of the answer, or null for 404 when "missing" is true. */
  async call(method, path, body, { missing = false } = {}) {
    let r;
    try {
      r = await this.fetch(`${this.base}${path}`, {
        method,
        headers: { Authorization: `Bearer ${this.token}`, "Content-Type": "application/json", Accept: "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(this.timeout),
      });
    } catch (error) {
      throw new ProviderError(`DigitalOcean did not answer: ${error.message}`);
    }
    if (missing && r.status === 404) return null;
    const text = await r.text();
    const data = text ? JSON.parse(text) : {};
    if (!r.ok) {
      // A 4xx answer is a problem of the token or of the request: the panel shows it.
      throw new ProviderError(`DigitalOcean refused the request: ${data.message ?? `HTTP status ${r.status}`}`, r.status >= 400 && r.status < 500 ? 422 : 502);
    }
    return data;
  }

  /** All the records of a domain, from all pages. */
  async records(domain) {
    const out = [];
    for (let page = 1; ; page++) {
      const data = await this.call("GET", `/domains/${encodeURIComponent(domain)}/records?page=${page}&per_page=200`);
      const list = data.domain_records ?? [];
      out.push(...list);
      if (!data.links?.pages?.next || list.length === 0) return out;
    }
  }
}

/** The longest time of a sync, in milliseconds. The panel waits 60 seconds for the answer. */
const BUDGET = 45000;

/**
 * Runs the functions with at most "size" at the same time. After "deadline", the sync stops:
 * the next sync continues from the records at DigitalOcean.
 */
async function pool(tasks, deadline, size = PARALLEL) {
  let next = 0;
  const worker = async () => {
    while (next < tasks.length) {
      if (Date.now() > deadline) throw new ProviderError("DigitalOcean did not take all the changes in time. The panel sends the zone again later, and the plugin continues with the changes that are left.");
      await tasks[next++]();
    }
  };
  await Promise.all(Array.from({ length: Math.min(size, tasks.length) }, worker));
}

/** A host name of DigitalOcean in the form of the panel: "@" is the domain, and no dot at the end. */
const host = (zone, v) => {
  const s = String(v ?? "").toLowerCase();
  return s === "@" ? zone : s.replace(/\.$/, "");
};

/** A record of DigitalOcean in the form of the panel. */
export function toPanel(zone, r) {
  const name = String(r.name ?? "@").toLowerCase() || "@";
  const base = { name, type: r.type, ttl: r.ttl, priority: null };
  switch (r.type) {
    case "AAAA":
      return { ...base, value: String(r.data).toLowerCase() };
    case "CNAME":
    case "NS":
      return { ...base, value: host(zone, r.data) };
    case "MX":
      return { ...base, value: host(zone, r.data), priority: r.priority ?? 0 };
    case "SRV":
      return { ...base, value: `${r.weight ?? 0} ${r.port ?? 0} ${host(zone, r.data)}`, priority: r.priority ?? 0 };
    case "CAA":
      return { ...base, value: `${r.flags ?? 0} ${String(r.tag ?? "issue").toLowerCase()} "${r.data}"` };
    default:
      // A, TXT, and the types that the panel does not have (the panel shows them as skipped).
      return { ...base, value: String(r.data ?? "") };
  }
}

/** A record of the panel in the form of the DigitalOcean API. */
export function toDigitalOcean(r) {
  const out = { type: r.type, name: r.name, ttl: r.ttl, data: r.value, priority: null, port: null, weight: null, flags: null, tag: null };
  switch (r.type) {
    case "CNAME":
    case "NS":
      return { ...out, data: `${r.value}.` };
    case "MX":
      return { ...out, data: `${r.value}.`, priority: r.priority };
    case "SRV": {
      const [weight, port, target] = r.value.split(" ");
      return { ...out, data: `${target}.`, priority: r.priority, weight: Number(weight), port: Number(port) };
    }
    case "CAA": {
      const m = /^(\d+) (\S+) "(.*)"$/.exec(r.value);
      return { ...out, data: m?.[3] ?? "", flags: Number(m?.[1] ?? 0), tag: m?.[2] ?? "issue" };
    }
    default:
      return out;
  }
}

/** The key of a record for the compare: the type, the name, the value and the priority. */
const key = (r) => `${r.type} ${r.name} ${r.type === "TXT" ? r.value : r.value.toLowerCase()} ${r.priority ?? ""}`;

/**
 * The changes that make the records of DigitalOcean the same as the records of the panel. A
 * record of the panel with no record of the same key changes a record of the same name and type
 * when there is one. The other records of DigitalOcean go.
 */
export function plan(zone, desired, existing) {
  const managed = existing.filter((r) => TYPES.includes(r.type) && !(r.type === "NS" && (r.name === "@" || r.name === "")));
  const have = new Map();
  for (const r of managed) {
    const k = key(toPanel(zone, r));
    if (!have.has(k)) have.set(k, []);
    have.get(k).push(r);
  }
  const creates = [];
  const updates = [];
  const unmatched = [];
  for (const want of desired) {
    const found = have.get(key(want))?.shift();
    if (!found) unmatched.push(want);
    else if (found.ttl !== want.ttl) updates.push({ id: found.id, ...toDigitalOcean(want) });
  }
  const left = [...have.values()].flat();
  for (const want of unmatched) {
    const i = left.findIndex((r) => r.type === want.type && toPanel(zone, r).name === want.name);
    if (i >= 0) updates.push({ id: left.splice(i, 1)[0].id, ...toDigitalOcean(want) });
    else creates.push(toDigitalOcean(want));
  }
  return { deletes: left.map((r) => ({ id: r.id })), updates, creates };
}

/**
 * Runs one operation of the contract. "request" is the body from the panel. The answer is the
 * body of the answer to the panel.
 */
export async function handle(request, { base, fetch: fetcher, budget = BUDGET } = {}) {
  const { operation, credentials = {}, zone } = request;
  const token = String(credentials.apiToken ?? "").trim();
  if (!token) throw new ProviderError("Enter the API token of DigitalOcean.", 422);
  const api = new DigitalOcean({ token, base, fetch: fetcher });
  const deadline = Date.now() + budget;

  if (operation === "verify") {
    const data = await api.call("GET", "/account").catch((e) => {
      throw new ProviderError(e.status === 422 ? "DigitalOcean refused the API token. Make a token with the scopes domain:read, domain:create, domain:update and domain:delete." : e.message, e.status);
    });
    return { account: data.account?.team?.name ?? data.account?.email ?? null };
  }

  const name = String(zone?.name ?? "").toLowerCase();
  if (!name) throw new ProviderError("The request has no zone.", 422);
  const path = `/domains/${encodeURIComponent(name)}`;
  const found = await api.call("GET", path, undefined, { missing: true });

  if (operation === "records") {
    if (!found) return { exists: false, records: [], nameServers: [] };
    const records = await api.records(name);
    return {
      exists: true,
      records: records.filter((r) => r.type !== "SOA" && !(r.type === "NS" && r.name === "@")).map((r) => toPanel(name, r)),
      nameServers: NAME_SERVERS,
    };
  }

  if (operation === "remove") {
    if (found) await api.call("DELETE", path, undefined, { missing: true });
    return {};
  }

  if (operation !== "sync") throw new ProviderError(`The operation "${operation}" is not known.`, 400);
  if (!found) await api.call("POST", "/domains", { name });
  const existing = await api.records(name);
  const changes = plan(name, (zone.records ?? []).filter((r) => TYPES.includes(r.type)), existing);
  // The deletes first, so that a new CNAME record does not meet an old record of the same name.
  await pool(changes.deletes.map((d) => () => api.call("DELETE", `${path}/records/${d.id}`, undefined, { missing: true })), deadline);
  await pool(changes.updates.map(({ id, ...body }) => () => api.call("PUT", `${path}/records/${id}`, body)), deadline);
  await pool(changes.creates.map((body) => () => api.call("POST", `${path}/records`, body)), deadline);
  const ns = existing.filter((r) => r.type === "NS" && r.name === "@").map((r) => host(name, r.data));
  return {
    nameServers: ns.length > 0 ? ns : NAME_SERVERS,
    // DigitalOcean answers for each domain of the account.
    active: true,
    dnssec: { state: "off", ds: [] },
    changes: { added: changes.creates.length, changed: changes.updates.length, removed: changes.deletes.length },
  };
}
