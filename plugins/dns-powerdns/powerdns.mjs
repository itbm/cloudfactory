// The PowerDNS side of the plugin: the four operations of the contract cf-dns-provider/1, with
// the HTTP API of the PowerDNS Authoritative Server (/api/v1). It has no dependencies. Refer to
// docs/plugins.md#dns-providers.
//
// The panel keeps the records. "sync" makes the record sets (RRsets) in PowerDNS the same as the
// records of the panel, for the types that the panel manages, in one PATCH request. PowerDNS
// applies the PATCH in one transaction. Record sets of other types stay, and so do the SOA record
// and the NS records of the zone itself.
//
// The user gives the address of the API. Thus the plugin does not connect to an address in a
// private network, unless the operator of the plugin permits the address in POWERDNS_ALLOW.
import { lookup as dnsLookup } from "node:dns";
import http from "node:http";
import https from "node:https";
import { BlockList, isIP } from "node:net";

/** The record types that the panel manages. The plugin does not change record sets of other types. */
export const TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"];

/** The largest answer from PowerDNS that the plugin reads. */
const MAX_ANSWER = 16 * 1024 * 1024;

/** An error that the panel shows to the user. "status" is the HTTP status of the answer to the panel. */
export class ProviderError extends Error {
  constructor(message, status = 502) {
    super(message);
    this.status = status;
  }
}

// The addresses that are not on the public internet: this host, private networks, link-local
// addresses (for example the metadata service of a cloud), multicast and reserved networks.
const PRIVATE = new BlockList();
for (const [net, prefix] of [
  ["0.0.0.0", 8], ["10.0.0.0", 8], ["100.64.0.0", 10], ["127.0.0.0", 8], ["169.254.0.0", 16],
  ["172.16.0.0", 12], ["192.0.0.0", 24], ["192.0.2.0", 24], ["192.168.0.0", 16], ["198.18.0.0", 15],
  ["198.51.100.0", 24], ["203.0.113.0", 24], ["224.0.0.0", 4], ["240.0.0.0", 4],
]) PRIVATE.addSubnet(net, prefix, "ipv4");
// IPv6. BlockList checks an IPv4-mapped address (::ffff:a.b.c.d) with the IPv4 networks. The
// NAT64 and 6to4 networks can also contain a private IPv4 address.
for (const [net, prefix] of [
  ["::", 96], ["64:ff9b::", 96], ["64:ff9b:1::", 48], ["100::", 64],
  ["2001:db8::", 32], ["2002::", 16], ["fc00::", 7], ["fe80::", 10], ["fec0::", 10], ["ff00::", 8],
]) PRIVATE.addSubnet(net, prefix, "ipv6");

const family = (address) => (isIP(address) === 6 ? "ipv6" : "ipv4");

/**
 * The hosts and networks that the operator permits, from a list with commas, for example
 * "pdns.internal, 10.0.0.0/8, 192.168.1.10". A host name in the list can have any address.
 */
export function allowList(text = "") {
  const names = new Set();
  const nets = new BlockList();
  for (const item of String(text).split(",").map((s) => s.trim().toLowerCase()).filter(Boolean)) {
    const [address, prefix] = item.split("/");
    const v = isIP(address);
    if (!v) {
      names.add(item.replace(/\.$/, ""));
      continue;
    }
    const bits = prefix === undefined ? (v === 4 ? 32 : 128) : Number(prefix);
    if (!Number.isInteger(bits) || bits < 0 || bits > (v === 4 ? 32 : 128)) throw new Error(`POWERDNS_ALLOW has a network that is not correct: ${item}`);
    nets.addSubnet(address, bits, family(address));
  }
  return { names, nets };
}

/** True when the plugin can connect to the address. */
export function permitted(address, allow) {
  return allow.nets.check(address, family(address)) || !PRIVATE.check(address, family(address));
}

const PRIVATE_MESSAGE = "The address of the PowerDNS API is in a private network. The plugin does not connect to it. The operator of the plugin can permit it in POWERDNS_ALLOW.";

/**
 * A lookup for node:http that refuses a host name with an address that the plugin does not
 * connect to. The connection uses the address that the lookup checked, so a second DNS answer
 * cannot change it.
 */
export function safeLookup(allow, resolve = dnsLookup) {
  return (hostname, options, callback) => {
    resolve(hostname, { ...options, all: true }, (error, addresses) => {
      if (error) return callback(error);
      const list = Array.isArray(addresses) ? addresses : [{ address: addresses, family: isIP(addresses) }];
      if (!allow.names.has(hostname.toLowerCase()) && (list.length === 0 || list.some((a) => !permitted(a.address, allow)))) {
        return callback(Object.assign(new Error(PRIVATE_MESSAGE), { code: "EPRIVATE" }));
      }
      if (options.all) return callback(null, list);
      callback(null, list[0].address, list[0].family);
    });
  };
}

/** A fetch with node:http and node:https, with the lookup of safeLookup. It does not follow redirects. */
export function transport(allow, resolve) {
  const lookup = safeLookup(allow, resolve);
  return (url, init = {}) =>
    new Promise((done, fail) => {
      const u = new URL(url);
      const lib = u.protocol === "https:" ? https : http;
      const req = lib.request(u, { method: init.method ?? "GET", headers: init.headers, lookup, signal: init.signal }, (res) => {
        const chunks = [];
        let size = 0;
        res.on("data", (c) => {
          size += c.length;
          if (size > MAX_ANSWER) req.destroy(Object.assign(new Error("the answer is too large"), { code: "ETOOLARGE" }));
          else chunks.push(c);
        });
        res.on("end", () => {
          try {
            done(new Response([204, 205, 304].includes(res.statusCode) ? null : Buffer.concat(chunks), { status: res.statusCode }));
          } catch (error) {
            fail(error);
          }
        });
        res.on("error", fail);
      });
      req.on("error", fail);
      if (init.body !== undefined) req.write(init.body);
      req.end();
    });
}

/**
 * The base address of the API from the address that the user gives, for example
 * "https://pdns.example.net:8081" or "https://pdns.example.net/api/v1". The plugin uses HTTP with
 * no TLS only for a host that the operator permits, because the API key goes in each request.
 */
export function apiBase(text, allow) {
  let u;
  try {
    u = new URL(String(text ?? "").trim());
  } catch {
    throw new ProviderError("Enter the address of the PowerDNS API, for example https://pdns.example.net:8081.", 422);
  }
  if (!["http:", "https:"].includes(u.protocol) || u.username || u.password || u.search || u.hash) {
    throw new ProviderError("Enter the address of the PowerDNS API as https://HOST:PORT, with no user name, password or query.", 422);
  }
  const host = u.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  const named = allow.names.has(host);
  if (isIP(host) && !named && !permitted(host, allow)) throw new ProviderError(PRIVATE_MESSAGE, 422);
  const trusted = named || (isIP(host) && allow.nets.check(host, family(host)));
  if (u.protocol === "http:" && !trusted) throw new ProviderError("Use HTTPS for the PowerDNS API. The API key goes in each request.", 422);
  return `${u.origin}${u.pathname.replace(/\/+$/, "").replace(/\/api\/v1$/, "")}/api/v1`;
}

/** A client of the API of one server of PowerDNS. */
export class PowerDns {
  constructor({ base, key, server = "localhost", timeout = 20000, fetch: fetcher }) {
    this.base = base;
    this.key = key;
    this.server = server;
    this.timeout = timeout;
    this.fetch = fetcher;
  }

  /** The body of the answer, or null for 404 when "missing" is true. */
  async call(method, path, body, { missing = false } = {}) {
    let r;
    try {
      r = await this.fetch(`${this.base}/servers/${encodeURIComponent(this.server)}${path}`, {
        method,
        headers: { "X-API-Key": this.key, "Content-Type": "application/json", Accept: "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(this.timeout),
      });
    } catch (error) {
      if (error.code === "EPRIVATE") throw new ProviderError(PRIVATE_MESSAGE, 422);
      // Only the code of the error: the message can contain an address of the network of the plugin.
      throw new ProviderError(`PowerDNS did not answer (${error.code ?? error.name}). Make sure that the plugin can connect to the address of the API.`);
    }
    if (missing && r.status === 404) return null;
    const text = await r.text();
    let data = {};
    try {
      data = text ? JSON.parse(text) : {};
    } catch {
      // An answer that is not JSON is not from the API of PowerDNS. The plugin does not show it.
    }
    if (!r.ok) {
      if (r.status === 401 || r.status === 403) throw new ProviderError("PowerDNS refused the API key. Use the value of api-key in the configuration of PowerDNS.", 422);
      const message = typeof data?.error === "string" ? data.error.slice(0, 200) : `HTTP status ${r.status}`;
      throw new ProviderError(`PowerDNS refused the request: ${message}`, r.status >= 400 && r.status < 500 ? 422 : 502);
    }
    return data;
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
const host = (zone, v) => {
  const s = String(v ?? "").toLowerCase();
  return s.endsWith(".") ? s.slice(0, -1) : `${s}.${zone}`;
};

/** The name of a record set in the form of the panel, or null when the name is not in the zone. */
export function relative(zone, fqdn) {
  const n = String(fqdn).toLowerCase().replace(/\.$/, "");
  if (n === zone) return "@";
  return n.endsWith(`.${zone}`) ? n.slice(0, -zone.length - 1) : null;
}

/** The name of a record set in the form of PowerDNS: absolute, with a dot at the end. */
export const absolute = (zone, name) => (name === "@" ? `${zone}.` : `${name}.${zone}.`);

/** One record of PowerDNS in the form of the panel. */
export function toPanel(zone, set, content) {
  const name = relative(zone, set.name);
  if (name === null) return null;
  const base = { name, type: set.type, ttl: set.ttl, priority: null };
  const parts = String(content).trim().split(/\s+/);
  switch (set.type) {
    case "AAAA":
      return { ...base, value: String(content).toLowerCase() };
    case "CNAME":
    case "NS":
      return { ...base, value: host(zone, content) };
    case "MX":
      return { ...base, value: host(zone, parts[1]), priority: Number(parts[0]) };
    case "SRV":
      return { ...base, value: `${parts[1]} ${parts[2]} ${host(zone, parts[3])}`, priority: Number(parts[0]) };
    case "TXT":
      return { ...base, value: unquoteTxt(content) };
    case "CAA": {
      const m = /^(\d+)\s+(\S+)\s+"?(.*?)"?$/.exec(String(content).trim());
      return { ...base, value: m ? `${m[1]} ${m[2].toLowerCase()} "${m[3]}"` : String(content) };
    }
    default:
      // A, and the types that the panel does not have (the panel shows them as skipped).
      return { ...base, value: String(content) };
  }
}

/** A record of the panel as the content of a record of PowerDNS. */
export function toPowerDns(r) {
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

/** The key of one value for the compare. */
const valueKey = (r) => `${r.type === "TXT" ? r.value : r.value.toLowerCase()} ${r.priority ?? ""}`;

/** True when the record set is the SOA record or the NS records of the zone itself. */
const reserved = (zone, set) => set.type === "SOA" || (set.type === "NS" && relative(zone, set.name) === "@");

/** The name servers of the zone: its NS records at @. */
const nameServers = (zone, full) =>
  (full.rrsets ?? []).filter((s) => s.type === "NS" && relative(zone, s.name) === "@").flatMap((s) => s.records.filter((r) => !r.disabled).map((r) => host(zone, r.content)));

/**
 * The record sets of one PATCH that makes PowerDNS the same as the records of the panel. A
 * record set has one TTL: the shortest TTL of its records in the panel. The deletes come first,
 * so that a new CNAME record does not meet an old record of the same name.
 */
export function plan(zone, desired, existing) {
  const want = new Map();
  for (const r of desired) {
    const id = `${absolute(zone, r.name).toLowerCase()}/${r.type}`;
    const set = want.get(id) ?? { name: absolute(zone, r.name), type: r.type, ttl: r.ttl, records: [], keys: new Set() };
    set.ttl = Math.min(set.ttl, r.ttl);
    if (!set.keys.has(valueKey(r))) {
      set.keys.add(valueKey(r));
      set.records.push(toPowerDns(r));
    }
    want.set(id, set);
  }
  const deletes = [];
  const replaces = [];
  let added = 0;
  let changed = 0;
  let removed = 0;
  for (const set of existing.filter((s) => TYPES.includes(s.type) && !reserved(zone, s) && relative(zone, s.name) !== null)) {
    const id = `${set.name.toLowerCase()}/${set.type}`;
    const target = want.get(id);
    if (!target) {
      deletes.push({ name: set.name, type: set.type, changetype: "DELETE" });
      removed += set.records.length;
      continue;
    }
    want.delete(id);
    const keys = new Set(set.records.map((r) => valueKey(toPanel(zone, set, r.content))));
    const same = !set.records.some((r) => r.disabled) && keys.size === target.keys.size && [...keys].every((k) => target.keys.has(k));
    if (!same || set.ttl !== target.ttl) {
      replaces.push({ name: target.name, type: target.type, ttl: target.ttl, changetype: "REPLACE", records: target.records.map((content) => ({ content, disabled: false })) });
      changed++;
    }
  }
  for (const set of want.values()) {
    replaces.push({ name: set.name, type: set.type, ttl: set.ttl, changetype: "REPLACE", records: set.records.map((content) => ({ content, disabled: false })) });
    added += set.records.length;
  }
  return { rrsets: [...deletes, ...replaces], changes: { added, changed, removed } };
}

/** A DS record of PowerDNS ("<key tag> <algorithm> <digest type> <digest>") in the form of the contract. */
export function parseDs(text) {
  const [keyTag, algorithm, digestType, digest] = String(text).trim().split(/\s+/);
  return { keyTag: Number(keyTag), algorithm: Number(algorithm), digestType: Number(digestType), digest: String(digest ?? "").toUpperCase() };
}

/** The DS records of the active KSK and CSK keys: SHA-256 when PowerDNS has it. */
async function dsRecords(api, zonePath) {
  const keys = await api.call("GET", `${zonePath}/cryptokeys`);
  const out = [];
  for (const key of Array.isArray(keys) ? keys : []) {
    if (!key.active || !["ksk", "csk"].includes(key.keytype)) continue;
    const ds = Array.isArray(key.ds) ? key.ds : (await api.call("GET", `${zonePath}/cryptokeys/${key.id}`)).ds ?? [];
    const list = ds.map(parseDs).filter((d) => d.digest);
    const best = list.find((d) => d.digestType === 2) ?? list[0];
    if (best) out.push(best);
  }
  return out;
}

/** The name servers for new zones, from a list with commas or spaces. */
function newNameServers(text) {
  const list = String(text ?? "").split(/[\s,]+/).map((s) => s.toLowerCase().replace(/\.$/, "")).filter(Boolean);
  for (const n of list) {
    if (!/^(?=.{1,253}$)[a-z0-9_]([a-z0-9-_]{0,61}[a-z0-9])?(\.[a-z0-9_]([a-z0-9-_]{0,61}[a-z0-9])?)+$/.test(n)) {
      throw new ProviderError(`The name server ${n.slice(0, 60)} is not a correct host name.`, 422);
    }
  }
  return list;
}

/**
 * Runs one operation of the contract. "request" is the body from the panel. The answer is the
 * body of the answer to the panel. "allow" comes from allowList. "fetch" is for tests.
 */
export async function handle(request, { allow = allowList(), fetch: fetcher, kind = "Native" } = {}) {
  const { operation, credentials = {}, zone } = request;
  const key = String(credentials.apiKey ?? "").trim();
  if (!key) throw new ProviderError("Enter the API key of PowerDNS.", 422);
  const base = apiBase(credentials.apiUrl, allow);
  const server = String(credentials.serverId ?? "").trim() || "localhost";
  const api = new PowerDns({ base, key, server, fetch: fetcher ?? transport(allow) });

  if (operation === "verify") {
    const info = await api.call("GET", "", undefined, { missing: true });
    if (info === null) throw new ProviderError(`PowerDNS has no server "${server.slice(0, 40)}". The server ID is usually localhost.`, 422);
    if (info.daemon_type !== "authoritative") throw new ProviderError("The address is not the API of a PowerDNS Authoritative Server.", 422);
    return { account: `${new URL(base).host}, server ${String(info.id ?? server).slice(0, 40)}` };
  }

  const name = String(zone?.name ?? "").toLowerCase();
  if (!name) throw new ProviderError("The request has no zone.", 422);
  const list = await api.call("GET", `/zones?zone=${encodeURIComponent(`${name}.`)}`);
  const found = (Array.isArray(list) ? list : []).find((z) => String(z.name).toLowerCase() === `${name}.`) ?? null;
  if (found && ["Slave", "Consumer"].includes(found.kind)) {
    throw new ProviderError(`The zone ${name} in PowerDNS is a secondary zone. Remove it in PowerDNS, or use a different provider.`, 422);
  }
  const zonePath = (z) => `/zones/${encodeURIComponent(z.id)}`;

  if (operation === "records") {
    if (!found) return { exists: false, records: [], nameServers: [] };
    const full = await api.call("GET", zonePath(found));
    const records = (full.rrsets ?? [])
      .filter((s) => !reserved(name, s))
      .flatMap((s) => s.records.filter((r) => !r.disabled).map((r) => toPanel(name, s, r.content)))
      .filter(Boolean);
    return { exists: true, records, nameServers: nameServers(name, full) };
  }

  if (operation === "remove") {
    if (found) await api.call("DELETE", zonePath(found), undefined, { missing: true });
    return {};
  }

  if (operation !== "sync") throw new ProviderError(`The operation "${operation}" is not known.`, 400);
  let z = found;
  if (!z) {
    const servers = newNameServers(credentials.nameServers);
    if (servers.length === 0) throw new ProviderError(`The zone ${name} is not in PowerDNS. Enter the name servers for new zones in the DNS account, or add the zone in PowerDNS.`, 422);
    z = await api.call("POST", "/zones", { name: `${name}.`, kind, nameservers: servers.map((n) => `${n}.`), soa_edit_api: "DEFAULT" });
  }
  const full = await api.call("GET", zonePath(z));
  const { rrsets, changes } = plan(name, (zone.records ?? []).filter((r) => TYPES.includes(r.type)), full.rrsets ?? []);
  if (rrsets.length > 0) await api.call("PATCH", zonePath(z), { rrsets });

  const signed = zone.dnssec === true;
  if (signed !== (full.dnssec === true)) await api.call("PUT", zonePath(z), signed ? { dnssec: true, api_rectify: true } : { dnssec: false });
  const ds = signed ? await dsRecords(api, zonePath(z)) : [];
  return {
    nameServers: nameServers(name, full),
    active: true,
    dnssec: { state: signed ? (ds.length ? "active" : "pending") : "off", ds },
    changes,
  };
}
