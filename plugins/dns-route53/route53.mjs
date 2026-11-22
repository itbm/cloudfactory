// The Route 53 side of the plugin: the four operations of the contract cf-dns-provider/1, with
// the API of Amazon Route 53 (2013-04-01). It has no dependencies: it signs each request with
// AWS Signature Version 4, and it reads the XML answers with a small parser. Refer to
// docs/plugins.md#dns-providers.
//
// The panel keeps the records. "sync" makes the record sets in the public hosted zone the same as
// the records of the panel, for the types that the panel manages. Route 53 applies each change
// batch in one transaction. These record sets stay: the types that the panel does not have, the
// SOA record, the NS records of the zone itself, alias records and record sets with a routing
// policy (a SetIdentifier).
import { createHash, createHmac, randomUUID } from "node:crypto";

/** The record types that the panel manages. The plugin does not change record sets of other types. */
export const TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "SRV", "CAA", "NS"];

const NS = "https://route53.amazonaws.com/doc/2013-04-01/";
/** The limits of one change batch. An UPSERT counts two times. */
const MAX_RECORDS = 1000;
const MAX_CHARS = 32000;
/** The name of the key-signing key that the plugin makes for DNSSEC. */
const KSK = "cloudfactory";

/** An error that the panel shows to the user. "status" is the HTTP status of the answer to the panel. */
export class ProviderError extends Error {
  constructor(message, status = 502, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

// --- AWS Signature Version 4 -------------------------------------------------------------------

/** URI encoding of RFC 3986, as SigV4 needs it. */
export const awsEncode = (s) => encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
const sha256 = (data) => createHash("sha256").update(data).digest("hex");
const hmac = (key, data) => createHmac("sha256", key).update(data).digest();

/**
 * The headers of a request with a SigV4 signature: x-amz-date and authorization. "headers" are
 * the other headers to sign. Each path segment is encoded a second time, as the services other
 * than S3 need.
 */
export function sign({ method, url, headers = {}, body = "", accessKeyId, secretAccessKey, region = "us-east-1", service = "route53", now = new Date() }) {
  const u = new URL(url);
  const amzDate = now.toISOString().replace(/[-:]/g, "").replace(/\.\d{3}/, "");
  const date = amzDate.slice(0, 8);
  const all = { host: u.host, "x-amz-date": amzDate };
  for (const [k, v] of Object.entries(headers)) all[k.toLowerCase()] = v;
  const names = Object.keys(all).sort();
  const canonicalHeaders = names.map((n) => `${n}:${String(all[n]).trim().replace(/\s+/g, " ")}\n`).join("");
  const signedHeaders = names.join(";");
  const path = u.pathname.split("/").map(awsEncode).join("/") || "/";
  const query = [...u.searchParams]
    .map(([k, v]) => [awsEncode(k), awsEncode(v)])
    .sort(([a, x], [b, y]) => (a < b ? -1 : a > b ? 1 : x < y ? -1 : x > y ? 1 : 0))
    .map(([k, v]) => `${k}=${v}`)
    .join("&");
  const canonical = [method, path, query, canonicalHeaders, signedHeaders, sha256(body)].join("\n");
  const scope = `${date}/${region}/${service}/aws4_request`;
  const toSign = ["AWS4-HMAC-SHA256", amzDate, scope, sha256(canonical)].join("\n");
  const key = hmac(hmac(hmac(hmac(`AWS4${secretAccessKey}`, date), region), service), "aws4_request");
  const signature = createHmac("sha256", key).update(toSign).digest("hex");
  const out = { ...headers, "x-amz-date": amzDate };
  out.authorization = `AWS4-HMAC-SHA256 Credential=${accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`;
  return out;
}

// --- XML ---------------------------------------------------------------------------------------

const ENTITIES = { lt: "<", gt: ">", amp: "&", quot: '"', apos: "'" };
const unescape = (s) =>
  s.replace(/&(#x[0-9a-f]+|#\d+|lt|gt|amp|quot|apos);/gi, (_, e) => (e[0] === "#" ? String.fromCodePoint(e[1].toLowerCase() === "x" ? parseInt(e.slice(2), 16) : Number(e.slice(1))) : ENTITIES[e.toLowerCase()]));
export const escapeXml = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/**
 * A small XML parser for the answers of the AWS API: elements and text. It ignores attributes,
 * comments and the declaration. Each node is { name, children, text }.
 */
export function parseXml(text) {
  const root = { name: "#root", children: [], text: "" };
  const stack = [root];
  const re = /<(\/?)([A-Za-z_][\w.:-]*)[^>]*?(\/?)>|<!--[\s\S]*?-->|<\?[\s\S]*?\?>|<!\[CDATA\[([\s\S]*?)\]\]>|([^<]+)/g;
  let m;
  while ((m = re.exec(text))) {
    const top = stack.at(-1);
    if (m[5] !== undefined) top.text += unescape(m[5]);
    else if (m[4] !== undefined) top.text += m[4];
    else if (m[2] && m[1]) {
      if (stack.length < 2 || top.name !== m[2].replace(/^.*:/, "")) throw new Error("The XML is not valid.");
      stack.pop();
    } else if (m[2]) {
      const node = { name: m[2].replace(/^.*:/, ""), children: [], text: "" };
      top.children.push(node);
      if (!m[3]) stack.push(node);
    }
  }
  if (stack.length !== 1) throw new Error("The XML is not complete.");
  return root.children[0] ?? null;
}

/** The first child of a node with the name, or null. */
export const kid = (node, name) => node?.children.find((c) => c.name === name) ?? null;
/** All the children of a node with the name. */
export const kids = (node, name) => node?.children.filter((c) => c.name === name) ?? [];
/** The text at a path of child names, or "". */
export const text = (node, ...path) => path.reduce((n, p) => kid(n, p), node)?.text.trim() ?? "";
/** The first node with the name anywhere in the tree, or null. */
const find = (node, name) => {
  if (!node) return null;
  if (node.name === name) return node;
  for (const c of node.children) {
    const f = find(c, name);
    if (f) return f;
  }
  return null;
};
/** A node as XML again, with no attributes. The plugin uses it to delete a record set exactly as Route 53 gave it. */
export const toXml = (n) => `<${n.name}>${n.children.length ? n.children.map(toXml).join("") : escapeXml(n.text)}</${n.name}>`;

// --- The API -----------------------------------------------------------------------------------

/** A client of the Route 53 API, with the access key of an IAM user. */
export class Route53 {
  constructor({ accessKeyId, secretAccessKey, base = "https://route53.amazonaws.com", timeout = 20000, fetch: fetcher = globalThis.fetch, now = () => new Date() }) {
    this.accessKeyId = accessKeyId;
    this.secretAccessKey = secretAccessKey;
    this.base = base.replace(/\/$/, "");
    this.timeout = timeout;
    this.fetch = fetcher;
    this.now = now;
  }

  /** The XML answer, or null for 404 when "missing" is true. "query" is a list of [key, value]. */
  async call(method, path, { query = [], body, missing = false } = {}) {
    const qs = query.map(([k, v]) => `${awsEncode(k)}=${awsEncode(v)}`).join("&");
    const url = `${this.base}/2013-04-01${path}${qs ? `?${qs}` : ""}`;
    const payload = body === undefined ? "" : `<?xml version="1.0" encoding="UTF-8"?>${body}`;
    const headers = sign({ method, url, headers: payload ? { "content-type": "text/xml" } : {}, body: payload, accessKeyId: this.accessKeyId, secretAccessKey: this.secretAccessKey, now: this.now() });
    let r;
    try {
      r = await this.fetch(url, { method, headers, body: payload || undefined, signal: AbortSignal.timeout(this.timeout) });
    } catch (error) {
      throw new ProviderError(`Route 53 did not answer: ${error.message}`);
    }
    if (missing && r.status === 404) return null;
    const raw = await r.text();
    let xml = null;
    try {
      xml = raw ? parseXml(raw) : null;
    } catch {
      // An answer that is not XML: the status gives the error.
    }
    if (!r.ok) {
      const code = text(find(xml, "Error"), "Code") || (xml?.name === "InvalidChangeBatch" ? "InvalidChangeBatch" : "");
      const message = find(xml, "Message")?.text.trim() || `HTTP status ${r.status}`;
      if (["SignatureDoesNotMatch", "InvalidClientTokenId", "UnrecognizedClientException", "IncompleteSignature", "InvalidSignatureException"].includes(code)) {
        throw new ProviderError("Route 53 refused the access key. Check the access key ID and the secret access key.", 422);
      }
      if (code === "AccessDenied" || code === "AccessDeniedException") {
        throw new ProviderError(`Route 53 refused the request: the IAM user does not have the permission. ${message.slice(0, 200)}`, 422, "AccessDenied");
      }
      const retry = r.status >= 500 || ["Throttling", "ThrottlingException", "PriorRequestNotComplete"].includes(code);
      throw new ProviderError(`Route 53 refused the request: ${message.slice(0, 240)}${code ? ` (${code})` : ""}`, retry ? 502 : 422, code);
    }
    return xml;
  }
}

// --- Records -----------------------------------------------------------------------------------

/** Route 53 writes some characters of a name as \ddd (octal), for example \052 for "*". */
export const decodeName = (s) => String(s).replace(/\\(\d{3})/g, (_, o) => String.fromCharCode(parseInt(o, 8))).toLowerCase();

/** The text of a TXT record in quotation marks, in parts of 255 characters. */
export function quoteTxt(t) {
  const parts = [];
  for (let i = 0; i < t.length; i += 255) parts.push(t.slice(i, i + 255));
  return (parts.length ? parts : [""]).map((p) => `"${p.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`).join(" ");
}

/** The text of a TXT record with no quotation marks. Parts are joined, and \ddd is a byte. */
export function unquoteTxt(content) {
  const s = String(content ?? "").trim();
  if (!s.startsWith('"')) return s;
  const bytes = [];
  const enc = new TextEncoder();
  for (let i = 0; i < s.length; i++) {
    if (s[i] !== '"') continue;
    for (i++; i < s.length && s[i] !== '"'; i++) {
      if (s[i] === "\\" && /^\d{3}$/.test(s.slice(i + 1, i + 4))) {
        bytes.push(parseInt(s.slice(i + 1, i + 4), 8) & 255);
        i += 3;
        continue;
      }
      if (s[i] === "\\" && i + 1 < s.length) i++;
      bytes.push(...enc.encode(s[i]));
    }
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}

/** A host name in the form of the panel. Route 53 makes each host name absolute, with or without the dot. */
const host = (v) => String(v ?? "").toLowerCase().replace(/\.$/, "");

/** The name of a record set in the form of the panel, or null when the name is not in the zone. */
export function relative(zone, fqdn) {
  const n = decodeName(fqdn).replace(/\.$/, "");
  if (n === zone) return "@";
  return n.endsWith(`.${zone}`) ? n.slice(0, -zone.length - 1) : null;
}

/** The name of a record set in the form of Route 53: absolute, with a dot at the end. */
export const absolute = (zone, name) => (name === "@" ? `${zone}.` : `${name}.${zone}.`).toLowerCase();

/** One value of a record set of Route 53 in the form of the panel. */
export function toPanel(name, type, ttl, value) {
  const base = { name, type, ttl, priority: null };
  const parts = String(value).trim().split(/\s+/);
  switch (type) {
    case "AAAA":
      return { ...base, value: String(value).toLowerCase() };
    case "CNAME":
    case "NS":
      return { ...base, value: host(value) };
    case "MX":
      return { ...base, value: host(parts[1]), priority: Number(parts[0]) };
    case "SRV":
      return { ...base, value: `${parts[1]} ${parts[2]} ${host(parts[3])}`, priority: Number(parts[0]) };
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

/** A record of the panel as one value of a record set of Route 53. */
export function toRoute53(r) {
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

/** A record set from the XML of Route 53. "foreign" is an alias or a set with a routing policy. */
export function readSet(zone, node) {
  const values = kids(kid(node, "ResourceRecords"), "ResourceRecord").map((r) => text(r, "Value"));
  const ttl = text(node, "TTL");
  return {
    node,
    fqdn: decodeName(text(node, "Name")),
    name: relative(zone, text(node, "Name")),
    type: text(node, "Type"),
    ttl: ttl === "" ? null : Number(ttl),
    values,
    alias: kid(node, "AliasTarget") ? text(node, "AliasTarget", "DNSName") : null,
    setIdentifier: text(node, "SetIdentifier") || null,
    get foreign() {
      return this.alias !== null || this.setIdentifier !== null;
    },
  };
}

/** True when the record set is the SOA record or the NS records of the zone itself. */
const reserved = (s) => s.type === "SOA" || (s.type === "NS" && s.name === "@");

/** The key of one value for the compare. */
const valueKey = (r) => `${r.type === "TXT" ? r.value : r.value.toLowerCase()} ${r.priority ?? ""}`;

/** The XML of a record set to make. */
const setXml = (s) =>
  `<ResourceRecordSet><Name>${escapeXml(s.name)}</Name><Type>${s.type}</Type><TTL>${s.ttl}</TTL><ResourceRecords>${s.records.map((v) => `<ResourceRecord><Value>${escapeXml(v)}</Value></ResourceRecord>`).join("")}</ResourceRecords></ResourceRecordSet>`;

/** A change of a change batch. "count" and "chars" are for the limits of a batch. */
const change = (action, xml, values) => ({ action, xml, count: Math.max(1, values.length), chars: values.reduce((n, v) => n + v.length, 0) });

/**
 * The changes that make Route 53 the same as the records of the panel: the deletes first, so
 * that a new CNAME record does not meet an old record of the same name. A record set has one
 * TTL: the shortest TTL of its records in the panel.
 */
export function plan(zone, desired, existing) {
  const want = new Map();
  for (const r of desired) {
    const id = `${absolute(zone, r.name)}/${r.type}`;
    const set = want.get(id) ?? { name: absolute(zone, r.name), type: r.type, ttl: r.ttl, records: [], keys: new Set() };
    set.ttl = Math.min(set.ttl, r.ttl);
    if (!set.keys.has(valueKey(r))) {
      set.keys.add(valueKey(r));
      set.records.push(toRoute53(r));
    }
    want.set(id, set);
  }
  const deletes = [];
  const upserts = [];
  const creates = [];
  const counts = { added: 0, changed: 0, removed: 0 };
  for (const s of existing.filter((e) => TYPES.includes(e.type) && e.name !== null && !reserved(e))) {
    const id = `${s.fqdn}/${s.type}`;
    const target = want.get(id);
    if (s.foreign) {
      if (target) {
        throw new ProviderError(`The record set ${s.name} ${s.type} in Route 53 is an alias or has a routing policy, and the panel does not change it. Remove it in Route 53, or remove the ${s.type} records of ${s.name} in the panel.`, 422);
      }
      continue;
    }
    if (!target) {
      deletes.push(change("DELETE", toXml(s.node), s.values));
      counts.removed += s.values.length;
      continue;
    }
    want.delete(id);
    const keys = new Set(s.values.map((v) => valueKey(toPanel(s.name, s.type, s.ttl, v))));
    const same = keys.size === target.keys.size && [...keys].every((k) => target.keys.has(k));
    if (!same || s.ttl !== target.ttl) {
      upserts.push(change("UPSERT", setXml(target), target.records));
      counts.changed++;
    }
  }
  for (const s of want.values()) {
    creates.push(change("CREATE", setXml(s), s.records));
    counts.added += s.records.length;
  }
  return { changes: [...deletes, ...upserts, ...creates], counts };
}

/** The changes in batches within the limits of Route 53. */
export function batches(changes, maxRecords = MAX_RECORDS, maxChars = MAX_CHARS) {
  const out = [];
  let current = [];
  let records = 0;
  let chars = 0;
  for (const c of changes) {
    const factor = c.action === "UPSERT" ? 2 : 1;
    if (current.length && (records + c.count * factor > maxRecords || chars + c.chars * factor > maxChars)) {
      out.push(current);
      current = [];
      records = 0;
      chars = 0;
    }
    current.push(c);
    records += c.count * factor;
    chars += c.chars * factor;
  }
  if (current.length) out.push(current);
  return out;
}

/** Sends the changes, one batch for each request. */
async function send(api, zoneId, changes, comment) {
  for (const batch of batches(changes)) {
    const body = `<ChangeResourceRecordSetsRequest xmlns="${NS}"><ChangeBatch><Comment>${escapeXml(comment)}</Comment><Changes>${batch.map((c) => `<Change><Action>${c.action}</Action>${c.xml}</Change>`).join("")}</Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`;
    await api.call("POST", `/hostedzone/${zoneId}/rrset/`, { body });
  }
}

/** All the record sets of the hosted zone, from all pages. */
async function listSets(api, zone, zoneId) {
  const out = [];
  let next = [];
  for (;;) {
    const xml = await api.call("GET", `/hostedzone/${zoneId}/rrset`, { query: [["maxitems", "300"], ...next] });
    out.push(...kids(kid(xml, "ResourceRecordSets"), "ResourceRecordSet").map((n) => readSet(zone, n)));
    if (text(xml, "IsTruncated") !== "true") return out;
    next = [["name", text(xml, "NextRecordName")], ["type", text(xml, "NextRecordType")]];
    if (text(xml, "NextRecordIdentifier")) next.push(["identifier", text(xml, "NextRecordIdentifier")]);
  }
}

/** The public hosted zone of the name, or null. */
async function findZone(api, name) {
  const xml = await api.call("GET", "/hostedzonesbyname", { query: [["dnsname", `${name}.`], ["maxitems", "10"]] });
  const zones = kids(kid(xml, "HostedZones"), "HostedZone").filter((z) => decodeName(text(z, "Name")) === `${name}.` && text(z, "Config", "PrivateZone") !== "true");
  if (zones.length > 1) throw new ProviderError(`Route 53 has ${zones.length} public hosted zones for ${name}. Remove the hosted zones that you do not use.`, 422);
  return zones.length ? text(zones[0], "Id").replace(/^\/hostedzone\//, "") : null;
}

/** The name servers of the hosted zone. */
const nameServers = (xml) => kids(kid(kid(xml, "DelegationSet"), "NameServers"), "NameServer").map((n) => host(n.text.trim()));

// --- DNSSEC ------------------------------------------------------------------------------------

/** The state of DNSSEC of the hosted zone: the signature state and the key-signing keys. */
async function dnssecOf(api, zoneId) {
  const xml = await api.call("GET", `/hostedzone/${zoneId}/dnssec`);
  return {
    serve: text(xml, "Status", "ServeSignature"),
    message: text(xml, "Status", "StatusMessage"),
    keys: (kid(xml, "KeySigningKeys")?.children ?? []).map((k) => ({
      name: text(k, "Name"),
      status: text(k, "Status"),
      message: text(k, "StatusMessage"),
      ds: text(k, "DSRecord"),
      keyTag: Number(text(k, "KeyTag")),
      algorithm: Number(text(k, "SigningAlgorithmType")),
      digestType: Number(text(k, "DigestAlgorithmType")),
      digest: text(k, "DigestValue").toUpperCase(),
    })),
  };
}

/** The DS records of the active keys, in the form of the contract. */
const dsOf = (d) =>
  d.keys
    .filter((k) => k.status === "ACTIVE")
    .map((k) => {
      if (k.digest && k.keyTag >= 0 && k.algorithm && k.digestType) return { keyTag: k.keyTag, algorithm: k.algorithm, digestType: k.digestType, digest: k.digest };
      const [keyTag, algorithm, digestType, digest] = k.ds.split(/\s+/);
      return { keyTag: Number(keyTag), algorithm: Number(algorithm), digestType: Number(digestType), digest: String(digest ?? "").toUpperCase() };
    })
    .filter((ds) => ds.digest);

/** Turns on DNSSEC signing with a key-signing key in the KMS key of the account. */
async function signZone(api, zoneId, kmsKeyArn, reference) {
  if (!kmsKeyArn) throw new ProviderError("Enter the ARN of a KMS key in the DNS account to turn on DNSSEC at Route 53.", 422);
  if (!/^arn:aws:kms:us-east-1:\d{12}:(key|alias)\/[\w/-]+$/.test(kmsKeyArn)) {
    throw new ProviderError("The KMS key must be in the region us-east-1, with an ARN such as arn:aws:kms:us-east-1:111122223333:key/....", 422);
  }
  let d = await dnssecOf(api, zoneId);
  const broken = d.keys.find((k) => ["ACTION_NEEDED", "INTERNAL_FAILURE"].includes(k.status));
  if (broken) throw new ProviderError(`Route 53 cannot use the key-signing key ${broken.name}: ${broken.message.slice(0, 200) || broken.status}. Check the policy of the KMS key.`, 422);
  if (!d.keys.some((k) => k.status === "ACTIVE")) {
    const own = d.keys.find((k) => k.name === KSK);
    if (own && own.status === "INACTIVE") await api.call("POST", `/keysigningkey/${zoneId}/${KSK}/activate`);
    else if (!own) {
      await api.call("POST", "/keysigningkey", {
        body: `<CreateKeySigningKeyRequest xmlns="${NS}"><CallerReference>${escapeXml(reference)}</CallerReference><HostedZoneId>${zoneId}</HostedZoneId><KeyManagementServiceArn>${escapeXml(kmsKeyArn)}</KeyManagementServiceArn><Name>${KSK}</Name><Status>ACTIVE</Status></CreateKeySigningKeyRequest>`,
      });
    }
  }
  if (d.serve !== "SIGNING") await api.call("POST", `/hostedzone/${zoneId}/enable-dnssec`);
  d = await dnssecOf(api, zoneId);
  if (d.serve === "ACTION_NEEDED" || d.serve === "INTERNAL_FAILURE") throw new ProviderError(`Route 53 cannot sign the zone: ${d.message.slice(0, 200) || d.serve}.`, 422);
  const ds = dsOf(d);
  return { state: d.serve === "SIGNING" && ds.length ? "active" : "pending", ds };
}

/**
 * Turns off DNSSEC signing, and then removes the key-signing key of the plugin. Route 53 changes
 * the state of a key some time after the request. Thus with "strict" false, a key that Route 53
 * cannot remove yet stays until the next sync.
 */
async function unsignZone(api, zoneId, strict) {
  let d;
  try {
    d = await dnssecOf(api, zoneId);
  } catch (error) {
    // An IAM user with no DNSSEC permissions cannot have turned DNSSEC on with the plugin.
    if (!strict && error.code === "AccessDenied") return { state: "off", ds: [] };
    throw error;
  }
  if (d.serve === "SIGNING") await api.call("POST", `/hostedzone/${zoneId}/disable-dnssec`);
  for (const k of d.keys.filter((key) => strict || key.name === KSK)) {
    try {
      if (k.status === "ACTIVE") await api.call("POST", `/keysigningkey/${zoneId}/${encodeURIComponent(k.name)}/deactivate`);
      await api.call("DELETE", `/keysigningkey/${zoneId}/${encodeURIComponent(k.name)}`);
    } catch (error) {
      if (strict) throw new ProviderError(`Route 53 is turning off DNSSEC for the zone, and cannot remove the key ${k.name} yet. Try again in some minutes. ${error.message}`.slice(0, 300), 502);
    }
  }
  return { state: "off", ds: [] };
}

// --- The contract ------------------------------------------------------------------------------

/**
 * Runs one operation of the contract. "request" is the body from the panel. The answer is the
 * body of the answer to the panel.
 */
export async function handle(request, { base, fetch: fetcher, now } = {}) {
  const { operation, credentials = {}, zone } = request;
  const accessKeyId = String(credentials.accessKeyId ?? "").trim();
  const secretAccessKey = String(credentials.secretAccessKey ?? "").trim();
  if (!accessKeyId || !secretAccessKey) throw new ProviderError("Enter the access key ID and the secret access key of an IAM user.", 422);
  const api = new Route53({ accessKeyId, secretAccessKey, base, fetch: fetcher, now });

  if (operation === "verify") {
    await api.call("GET", "/hostedzonesbyname", { query: [["maxitems", "1"]] });
    return { account: null };
  }

  const name = String(zone?.name ?? "").toLowerCase();
  if (!name) throw new ProviderError("The request has no zone.", 422);
  const reference = `cloudfactory-${request.id ?? randomUUID()}`.slice(0, 128);
  let zoneId = await findZone(api, name);

  if (operation === "records") {
    if (!zoneId) return { exists: false, records: [], nameServers: [] };
    const info = await api.call("GET", `/hostedzone/${zoneId}`);
    const records = [];
    for (const s of await listSets(api, name, zoneId)) {
      if (s.name === null || reserved(s)) continue;
      // The panel has no alias records and no routing policies. It shows them as skipped.
      if (s.alias !== null) records.push({ name: s.name, type: "ALIAS", ttl: null, value: `${s.type} ${host(s.alias)}`, priority: null });
      else if (s.setIdentifier !== null) records.push({ name: s.name, type: "POLICY", ttl: s.ttl, value: `${s.type} ${s.setIdentifier}: ${s.values.join(" ")}`, priority: null });
      else records.push(...s.values.map((v) => toPanel(s.name, s.type, s.ttl, v)));
    }
    return { exists: true, records, nameServers: nameServers(info) };
  }

  if (operation === "remove") {
    if (!zoneId) return {};
    await unsignZone(api, zoneId, true);
    // Route 53 removes only an empty hosted zone: the SOA record and the NS records of the zone.
    const sets = (await listSets(api, name, zoneId)).filter((s) => !(s.type === "SOA" || (s.type === "NS" && s.name === "@")));
    await send(api, zoneId, sets.map((s) => change("DELETE", toXml(s.node), s.values)), "Cloudfactory: remove the zone");
    await api.call("DELETE", `/hostedzone/${zoneId}`, { missing: true });
    return {};
  }

  if (operation !== "sync") throw new ProviderError(`The operation "${operation}" is not known.`, 400);
  if (!zoneId) {
    const made = await api.call("POST", "/hostedzone", {
      body: `<CreateHostedZoneRequest xmlns="${NS}"><Name>${escapeXml(`${name}.`)}</Name><CallerReference>${escapeXml(reference)}</CallerReference><HostedZoneConfig><Comment>Cloudfactory</Comment><PrivateZone>false</PrivateZone></HostedZoneConfig></CreateHostedZoneRequest>`,
    });
    zoneId = text(made, "HostedZone", "Id").replace(/^\/hostedzone\//, "");
  }
  const { changes, counts } = plan(name, (zone.records ?? []).filter((r) => TYPES.includes(r.type)), await listSets(api, name, zoneId));
  await send(api, zoneId, changes, `Cloudfactory: serial ${zone.serial ?? ""}`.trim());
  const dnssec = zone.dnssec === true ? await signZone(api, zoneId, String(credentials.kmsKeyArn ?? "").trim(), `${reference}-ksk`) : await unsignZone(api, zoneId, false);
  const info = await api.call("GET", `/hostedzone/${zoneId}`);
  return { nameServers: nameServers(info), active: true, dnssec, changes: counts };
}
