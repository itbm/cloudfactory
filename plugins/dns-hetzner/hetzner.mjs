// The Hetzner side of the plugin: the four operations of the contract cf-dns-provider/1, with
// the DNS API of the Hetzner Cloud API (api.hetzner.cloud/v1/zones). It has no dependencies.
// Refer to docs/plugins.md#dns-providers.
//
// The panel keeps the records. "sync" makes the record sets (RRSets) at Hetzner the same as the
// records of the panel, for the types that the panel manages. Record sets of other types stay,
// and so do the SOA record and the NS records of the zone itself. The Hetzner API has no DNSSEC.

/** The record types that the panel manages. The plugin does not change record sets of other types. */
export const TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"];

/** The longest time of a sync, in milliseconds. The panel waits 60 seconds for the answer. */
const BUDGET = 45000;

/** An error that the panel shows to the user. "status" is the HTTP status of the answer to the panel. */
export class ProviderError extends Error {
  constructor(message, status = 502, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

/** A client of the Hetzner Cloud API, with an API token of a project. */
export class Hetzner {
  constructor({ token, base = "https://api.hetzner.cloud/v1", timeout = 20000, fetch: fetcher = globalThis.fetch, pause = 500, budget = BUDGET } = {}) {
    this.token = token;
    this.base = base.replace(/\/$/, "");
    this.timeout = timeout;
    this.fetch = fetcher;
    this.pause = pause;
    this.deadline = Date.now() + budget;
    this.pending = [];
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
      throw new ProviderError(`Hetzner did not answer: ${error.message}`);
    }
    if (missing && r.status === 404) return null;
    const text = await r.text();
    const data = text ? JSON.parse(text) : {};
    if (!r.ok) {
      const e = data.error ?? {};
      // A 4xx answer is a problem of the token or of the request: the panel shows it.
      const retry = r.status >= 500 || r.status === 429 || e.code === "locked";
      throw new ProviderError(`Hetzner refused the request: ${e.message ?? `HTTP status ${r.status}`}${e.code ? ` (${e.code})` : ""}`, retry ? 502 : 422, e.code ?? "");
    }
    return data;
  }

  /** All the entries of a list, from all pages. */
  async all(path, field) {
    const out = [];
    for (let page = 1; ; page++) {
      const sep = path.includes("?") ? "&" : "?";
      const data = await this.call("GET", `${path}${sep}page=${page}&per_page=50`);
      out.push(...(data[field] ?? []));
      if (!data.meta?.pagination?.next_page) return out;
    }
  }

  /** Waits until an action is complete. An action with an error is an error. */
  async wait(action) {
    let a = action;
    while (a && a.status === "running") {
      this.inTime();
      await new Promise((resolve) => setTimeout(resolve, this.pause));
      a = (await this.call("GET", `/actions/${a.id}`)).action;
    }
    if (a?.status === "error") throw new ProviderError(`Hetzner could not complete the action ${a.command}: ${a.error?.message ?? "no reason"}`, 422);
  }

  /** Waits for all the actions that "change" started. */
  async settle() {
    const list = this.pending;
    this.pending = [];
    for (const a of list) await this.wait(a);
  }

  /**
   * Sends one change, and keeps its action to wait for later: Hetzner runs the actions at the
   * same time. When a change meets a running action ("locked"), the plugin waits for the actions
   * and sends the change again.
   */
  async change(method, path, body, options) {
    this.inTime();
    let answer;
    try {
      answer = await this.call(method, path, body, options);
    } catch (error) {
      if (error.code !== "locked" || this.pending.length === 0) throw error;
      await this.settle();
      answer = await this.call(method, path, body, options);
    }
    if (answer?.action) this.pending.push(answer.action);
  }

  /** A sync that takes too long stops. The next sync continues from the records at Hetzner. */
  inTime() {
    if (Date.now() > this.deadline) throw new ProviderError("Hetzner did not take all the changes in time. The panel sends the zone again later, and the plugin continues with the changes that are left.");
  }
}

/** The text of a TXT record in quotation marks, in parts of 255 characters. */
export function quoteTxt(text) {
  const parts = [];
  for (let i = 0; i < text.length; i += 255) parts.push(text.slice(i, i + 255));
  return (parts.length ? parts : [""]).map((p) => `"${p.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`).join(" ");
}

/** The text of a TXT record with no quotation marks. Parts are joined. */
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

/** A host name in the form of the panel: absolute, lower case, and no dot at the end. */
function host(zone, v) {
  const s = String(v ?? "").toLowerCase();
  if (s === "@") return zone;
  return s.endsWith(".") ? s.slice(0, -1) : `${s}.${zone}`;
}

/** One value of a record set of Hetzner in the form of the panel. */
export function toPanel(zone, set, value) {
  const name = set.name || "@";
  const base = { name, type: set.type, ttl: set.ttl, priority: null };
  const parts = String(value).trim().split(/\s+/);
  switch (set.type) {
    case "AAAA":
      return { ...base, value: String(value).toLowerCase() };
    case "CNAME":
    case "NS":
      return { ...base, value: host(zone, value) };
    case "MX":
      return { ...base, value: host(zone, parts[1]), priority: Number(parts[0]) };
    case "SRV":
      return { ...base, value: `${parts[1]} ${parts[2]} ${host(zone, parts[3])}`, priority: Number(parts[0]) };
    case "TXT":
      return { ...base, value: unquoteTxt(value) };
    case "CAA": {
      const m = /^(\d+)\s+(\S+)\s+"?(.*?)"?$/.exec(String(value).trim());
      return { ...base, value: m ? `${m[1]} ${m[2].toLowerCase()} "${m[3]}"` : String(value) };
    }
    default:
      // A, and the types that the panel does not have (the panel shows them as skipped).
      return { ...base, value: String(value) };
  }
}

/** A record of the panel as one value of a record set of Hetzner. */
export function toHetzner(r) {
  switch (r.type) {
    case "CNAME":
    case "NS":
      return `${r.value}.`;
    case "MX":
      return `${r.priority} ${r.value}.`;
    case "SRV": {
      const [weight, port, target] = r.value.split(" ");
      return `${r.priority} ${weight} ${port} ${target}.`;
    }
    case "TXT":
      return quoteTxt(r.value);
    default:
      return r.value;
  }
}

/** The name servers of Hetzner for the zone, with no dot at the end. */
const assigned = (z) => (z.authoritative_nameservers?.assigned ?? []).map((n) => String(n).replace(/\.$/, ""));

/** The key of one value for the compare. */
const valueKey = (r) => `${r.type === "TXT" ? r.value : r.value.toLowerCase()} ${r.priority ?? ""}`;

/** True when the record set is the SOA record or the NS records of the zone itself. */
const reserved = (set) => set.type === "SOA" || (set.type === "NS" && (set.name === "@" || set.name === ""));

/**
 * The changes that make the record sets of Hetzner the same as the records of the panel. A
 * record set has one TTL: the shortest TTL of its records in the panel.
 */
export function plan(zone, desired, existing, defaultTtl = 3600) {
  const want = new Map();
  for (const r of desired) {
    const id = `${r.name}/${r.type}`;
    const set = want.get(id) ?? { name: r.name, type: r.type, ttl: r.ttl, records: [], keys: new Set() };
    set.ttl = Math.min(set.ttl, r.ttl);
    if (!set.keys.has(valueKey(r))) {
      set.keys.add(valueKey(r));
      set.records.push(toHetzner(r));
    }
    want.set(id, set);
  }
  const creates = [];
  const updates = [];
  const deletes = [];
  for (const set of existing.filter((s) => TYPES.includes(s.type) && !reserved(s))) {
    const id = `${set.name}/${set.type}`;
    const target = want.get(id);
    if (!target) {
      deletes.push({ name: set.name, type: set.type, count: set.records.length });
      continue;
    }
    want.delete(id);
    const ttl = set.ttl ?? defaultTtl;
    const keys = new Set(set.records.map((v) => valueKey(toPanel(zone, set, v.value))));
    const same = keys.size === target.keys.size && [...keys].every((k) => target.keys.has(k));
    if (!same || ttl !== target.ttl) updates.push({ name: set.name, type: set.type, records: same ? null : target.records, ttl: ttl === target.ttl ? null : target.ttl });
  }
  for (const set of want.values()) creates.push({ name: set.name, type: set.type, ttl: set.ttl, records: set.records });
  return { creates, updates, deletes };
}

/**
 * Runs one operation of the contract. "request" is the body from the panel. The answer is the
 * body of the answer to the panel.
 */
export async function handle(request, { base, fetch: fetcher, pause, budget } = {}) {
  const { operation, credentials = {}, zone } = request;
  const token = String(credentials.apiToken ?? "").trim();
  if (!token) throw new ProviderError("Enter the API token of the Hetzner project.", 422);
  const api = new Hetzner({ token, base, fetch: fetcher, pause, budget });

  if (operation === "verify") {
    await api.call("GET", "/zones?per_page=1").catch((e) => {
      throw new ProviderError(e.status === 422 ? "Hetzner refused the API token. Make a token with the permission Read & Write in the project of your DNS zones." : e.message, e.status);
    });
    return { account: null };
  }

  const name = String(zone?.name ?? "").toLowerCase();
  if (!name) throw new ProviderError("The request has no zone.", 422);
  const found = (await api.call("GET", `/zones?name=${encodeURIComponent(name)}`)).zones?.find((z) => z.name === name) ?? null;
  if (found && found.mode !== "primary") throw new ProviderError(`The zone ${name} at Hetzner is a secondary zone. Remove it at Hetzner, or use a different provider.`, 422);
  const zonePath = (z) => `/zones/${z.id}`;

  if (operation === "records") {
    if (!found) return { exists: false, records: [], nameServers: [] };
    const sets = await api.all(`${zonePath(found)}/rrsets`, "rrsets");
    return {
      exists: true,
      records: sets.filter((s) => !reserved(s)).flatMap((s) => s.records.map((v) => toPanel(name, { ...s, ttl: s.ttl ?? found.ttl }, v.value))),
      nameServers: assigned(found),
    };
  }

  if (operation === "remove") {
    if (found) await api.wait((await api.call("DELETE", zonePath(found), undefined, { missing: true }))?.action);
    return {};
  }

  if (operation !== "sync") throw new ProviderError(`The operation "${operation}" is not known.`, 400);
  let z = found;
  if (!z) {
    const made = await api.call("POST", "/zones", { name, mode: "primary" });
    await api.wait(made.action);
    z = made.zone;
  }
  const existing = await api.all(`${zonePath(z)}/rrsets`, "rrsets");
  const changes = plan(name, (zone.records ?? []).filter((r) => TYPES.includes(r.type)), existing, z.ttl);
  const rrset = (s) => `${zonePath(z)}/rrsets/${encodeURIComponent(s.name)}/${s.type}`;
  // The deletes first, so that a new CNAME record does not meet an old record of the same name.
  for (const s of changes.deletes) await api.change("DELETE", rrset(s), undefined, { missing: true });
  await api.settle();
  for (const s of changes.updates) {
    if (s.records) await api.change("POST", `${rrset(s)}/actions/set_records`, { records: s.records.map((value) => ({ value })) });
    if (s.ttl !== null) await api.change("POST", `${rrset(s)}/actions/change_ttl`, { ttl: s.ttl });
  }
  for (const s of changes.creates) await api.change("POST", `${zonePath(z)}/rrsets`, { name: s.name, type: s.type, ttl: s.ttl, records: s.records.map((value) => ({ value })) });
  await api.settle();

  const now = (await api.call("GET", zonePath(z))).zone ?? z;
  return {
    nameServers: assigned(now),
    active: now.authoritative_nameservers?.delegation_status === "valid",
    dnssec: { state: "off", ds: [] },
    changes: {
      added: changes.creates.reduce((n, s) => n + s.records.length, 0),
      changed: changes.updates.length,
      removed: changes.deletes.reduce((n, s) => n + s.count, 0),
    },
  };
}
