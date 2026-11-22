// Tests of the PowerDNS plugin, with a fake PowerDNS API. Run: node --test test/*.test.mjs
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { createServer } from "node:http";
import { test } from "node:test";
import { allowList, apiBase, handle, parseDs, plan, ProviderError, safeLookup, transport } from "../powerdns.mjs";
import { server } from "../server.mjs";

const DS = ["2371 13 1 8B9C33F2DD3D4C1B6E1C5C3A9D0F2E7A4B5C6D7E", "2371 13 2 a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"];

/** A fake of the parts of the API of the PowerDNS Authoritative Server that the plugin uses. */
class FakePowerDns {
  constructor({ daemon = "authoritative" } = {}) {
    this.daemon = daemon;
    this.zones = new Map();
    this.requests = [];
    this.fetch = this.fetch.bind(this);
  }

  addZone(name, { kind = "Native", nameservers = ["ns1.example.net.", "ns2.example.net."] } = {}) {
    const id = `${name}.`;
    this.zones.set(id, {
      id,
      name: id,
      kind,
      dnssec: false,
      api_rectify: false,
      rrsets: [
        { name: id, type: "SOA", ttl: 3600, records: [{ content: "ns1.example.net. hostmaster.example.net. 1 10800 3600 604800 3600", disabled: false }] },
        { name: id, type: "NS", ttl: 3600, records: nameservers.map((content) => ({ content, disabled: false })) },
      ],
    });
    return this.zones.get(id);
  }

  addSet(zone, set) {
    this.zones.get(`${zone}.`).rrsets.push({ ...set, records: set.records.map((r) => (typeof r === "string" ? { content: r, disabled: false } : r)) });
  }

  set(zone, name, type) {
    return this.zones.get(`${zone}.`).rrsets.find((s) => s.name === name && s.type === type);
  }

  async fetch(url, init = {}) {
    const u = new URL(url);
    const path = decodeURIComponent(u.pathname);
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (method !== "GET") this.requests.push({ method, path, body });
    const ok = (value, status = 200) => new Response(status === 204 ? null : JSON.stringify(value), { status });
    const fail = (status, error) => new Response(JSON.stringify({ error }), { status });
    if (init.headers["X-API-Key"] !== "good-key") return new Response("Unauthorized", { status: 401 });
    if (!path.startsWith("/api/v1/servers/localhost")) return fail(404, "Not Found");
    const rest = path.slice("/api/v1/servers/localhost".length);
    let m;
    if (rest === "") return ok({ type: "Server", id: "localhost", daemon_type: this.daemon, version: "4.9.2" });
    if (rest === "/zones" && method === "GET") {
      const want = u.searchParams.get("zone");
      return ok([...this.zones.values()].filter((z) => !want || z.name === want).map(({ rrsets, ...z }) => z));
    }
    if (rest === "/zones" && method === "POST") {
      if (this.zones.has(body.name)) return fail(409, "Conflict");
      const z = this.addZone(body.name.slice(0, -1), { kind: body.kind, nameservers: body.nameservers });
      return ok(z, 201);
    }
    if ((m = /^\/zones\/([^/]+)$/.exec(rest))) {
      const z = this.zones.get(m[1]);
      if (!z) return fail(404, "Could not find domain");
      if (method === "DELETE") {
        this.zones.delete(m[1]);
        return ok(null, 204);
      }
      if (method === "PUT") {
        z.dnssec = body.dnssec;
        if ("api_rectify" in body) z.api_rectify = body.api_rectify;
        return ok(null, 204);
      }
      if (method === "PATCH") {
        for (const s of body.rrsets) {
          const i = z.rrsets.findIndex((e) => e.name === s.name && e.type === s.type);
          if (s.changetype === "DELETE") {
            if (i >= 0) z.rrsets.splice(i, 1);
            continue;
          }
          if (!s.name.endsWith(z.name) || !Number.isInteger(s.ttl)) return fail(422, `RRset ${s.name} IN ${s.type}: not valid`);
          const set = { name: s.name, type: s.type, ttl: s.ttl, records: s.records };
          if (i >= 0) z.rrsets[i] = set;
          else z.rrsets.push(set);
        }
        return ok(null, 204);
      }
      return ok(z);
    }
    if ((m = /^\/zones\/([^/]+)\/cryptokeys$/.exec(rest))) {
      const z = this.zones.get(m[1]);
      return ok(z.dnssec ? [
        { type: "Cryptokey", id: 1, keytype: "csk", active: true, published: true, dnskey: "257 3 13 AbCd", ds: DS, algorithm: "ECDSAP256SHA256", bits: 256 },
        { type: "Cryptokey", id: 2, keytype: "zsk", active: false, published: true, dnskey: "256 3 13 EfGh", algorithm: "ECDSAP256SHA256", bits: 256 },
      ] : []);
    }
    return fail(404, `No route ${method} ${rest}`);
  }
}

const credentials = { apiUrl: "https://pdns.example.net:8081", apiKey: "good-key" };
const call = (api, operation, zone, more = {}) => handle({ operation, credentials: { ...credentials, ...more }, zone }, { fetch: api.fetch });

const records = [
  { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
  { name: "@", type: "A", ttl: 300, value: "192.0.2.3", priority: null },
  { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
  { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
  { name: "@", type: "TXT", ttl: 3600, value: "v=spf1 -all", priority: null },
  { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
  { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
];

test("verify checks the address, the key and the server", async () => {
  const api = new FakePowerDns();
  assert.deepEqual(await call(api, "verify", null), { account: "pdns.example.net:8081, server localhost" });
  await assert.rejects(call(api, "verify", null, { apiKey: "bad" }), (e) => e instanceof ProviderError && e.status === 422 && /refused the API key/.test(e.message));
  await assert.rejects(call(api, "verify", null, { serverId: "other" }), /has no server "other"/);
  await assert.rejects(call(new FakePowerDns({ daemon: "recursor" }), "verify", null), /not the API of a PowerDNS Authoritative Server/);
  await assert.rejects(call(api, "verify", null, { apiKey: "" }), /Enter the API key/);
});

test("the address of the API must be HTTPS and not in a private network", () => {
  const none = allowList("");
  assert.equal(apiBase("https://pdns.example.net:8081", none), "https://pdns.example.net:8081/api/v1");
  assert.equal(apiBase("https://pdns.example.net/proxy/api/v1/", none), "https://pdns.example.net/proxy/api/v1");
  for (const bad of ["http://pdns.example.net:8081", "https://127.0.0.1:8081", "https://[::1]/", "https://169.254.169.254/", "https://10.1.2.3/", "https://[::ffff:127.0.0.1]/", "https://[fd00::1]/", "ftp://pdns.example.net", "https://user:pw@pdns.example.net", "not a url"]) {
    assert.throws(() => apiBase(bad, none), (e) => e instanceof ProviderError && e.status === 422, bad);
  }
  // The operator can permit a host or a network in POWERDNS_ALLOW. Then HTTP is also possible.
  const allow = allowList("pdns.internal, 10.0.0.0/8");
  assert.equal(apiBase("http://pdns.internal:8081", allow), "http://pdns.internal:8081/api/v1");
  assert.equal(apiBase("http://10.1.2.3:8081", allow), "http://10.1.2.3:8081/api/v1");
  assert.throws(() => apiBase("https://192.168.1.1", allow), /private network/);
  assert.throws(() => allowList("10.0.0.0/33"), /not correct/);
});

test("the lookup refuses a host name with a private address", async () => {
  const resolver = (table) => (host, options, callback) => callback(null, (table[host] ?? []).map((address) => ({ address, family: address.includes(":") ? 6 : 4 })));
  const resolve = resolver({ "pdns.example.net": ["198.41.0.4"], "evil.example.net": ["198.41.0.4", "10.0.0.5"], "pdns.internal": ["10.0.0.7"], "v6.example.net": ["::1"] });
  const look = (allow, host, options = {}) => new Promise((done) => safeLookup(allowList(allow), resolve)(host, options, (...args) => done(args)));
  assert.deepEqual(await look("", "pdns.example.net"), [null, "198.41.0.4", 4]);
  assert.deepEqual(await look("", "pdns.example.net", { all: true }), [null, [{ address: "198.41.0.4", family: 4 }]]);
  assert.equal((await look("", "evil.example.net"))[0].code, "EPRIVATE");
  assert.equal((await look("", "v6.example.net"))[0].code, "EPRIVATE");
  assert.equal((await look("", "pdns.internal"))[0].code, "EPRIVATE");
  assert.deepEqual(await look("pdns.internal", "pdns.internal"), [null, "10.0.0.7", 4]);
  assert.deepEqual(await look("10.0.0.0/8", "pdns.internal"), [null, "10.0.0.7", 4]);

  // The transport uses the lookup: a name that points to this host does not get the request.
  const fetch = transport(allowList(""), resolver({ "pdns.example.net": ["127.0.0.1"] }));
  await assert.rejects(handle({ operation: "verify", credentials }, { fetch }), (e) => e.status === 422 && /private network/.test(e.message));
});

test("the transport sends the request to a permitted address and reads the answer", async () => {
  const fake = new FakePowerDns();
  const local = createServer(async (req, res) => {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const r = await fake.fetch(`http://pdns${req.url}`, { method: req.method, headers: { "X-API-Key": req.headers["x-api-key"] }, body: chunks.length ? Buffer.concat(chunks).toString() : undefined });
    res.writeHead(r.status, { "Content-Type": "application/json" });
    res.end(await r.text());
  }).listen(0, "127.0.0.1");
  await new Promise((resolve) => local.once("listening", resolve));
  try {
    const apiUrl = `http://127.0.0.1:${local.address().port}`;
    const out = await handle({ operation: "verify", credentials: { ...credentials, apiUrl } }, { allow: allowList("127.0.0.1") });
    assert.deepEqual(out, { account: `127.0.0.1:${local.address().port}, server localhost` });
    await assert.rejects(handle({ operation: "verify", credentials: { ...credentials, apiUrl } }), /private network/);
    // A zone that PowerDNS does not have, with a 404 from the transport.
    assert.deepEqual(await handle({ operation: "remove", credentials: { ...credentials, apiUrl }, zone: { name: "example.com" } }, { allow: allowList("127.0.0.1") }), {});
  } finally {
    local.close();
  }
});

test("records gives the records in the form of the panel, with no SOA, no NS records of the zone and no disabled records", async () => {
  const api = new FakePowerDns();
  assert.deepEqual(await call(api, "records", { name: "example.com" }), { exists: false, records: [], nameServers: [] });
  api.addZone("example.com");
  api.addSet("example.com", { name: "example.com.", type: "A", ttl: 300, records: ["192.0.2.1", { content: "192.0.2.9", disabled: true }] });
  api.addSet("example.com", { name: "www.example.com.", type: "CNAME", ttl: 3600, records: ["example.com."] });
  api.addSet("example.com", { name: "example.com.", type: "MX", ttl: 3600, records: ["10 Mail.Example.net."] });
  api.addSet("example.com", { name: "example.com.", type: "TXT", ttl: 3600, records: ['"v=spf1 " "-all"'] });
  api.addSet("example.com", { name: "_sip._tcp.example.com.", type: "SRV", ttl: 3600, records: ["10 5 5060 sip.example.com."] });
  api.addSet("example.com", { name: "sub.example.com.", type: "NS", ttl: 3600, records: ["ns1.other.example."] });
  const out = await call(api, "records", { name: "example.com" });
  assert.deepEqual(out.nameServers, ["ns1.example.net", "ns2.example.net"]);
  assert.deepEqual(out.records, [
    { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
    { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
    { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
    { name: "@", type: "TXT", ttl: 3600, value: "v=spf1 -all", priority: null },
    { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
    { name: "sub", type: "NS", ttl: 3600, value: "ns1.other.example", priority: null },
  ]);
});

test("sync adds the zone with the name servers of the account, and then sends only the changes in one PATCH", async () => {
  const api = new FakePowerDns();
  await assert.rejects(call(api, "sync", { name: "example.com", dnssec: false, records }), /Enter the name servers for new zones/);
  await assert.rejects(call(api, "sync", { name: "example.com", dnssec: false, records }, { nameServers: "ns1.example.net, -bad-" }), /not a correct host name/);
  const first = await call(api, "sync", { name: "example.com", serial: 1, dnssec: false, records }, { nameServers: "ns1.example.net, NS2.example.net." });
  assert.deepEqual(first, { nameServers: ["ns1.example.net", "ns2.example.net"], active: true, dnssec: { state: "off", ds: [] }, changes: { added: 7, changed: 0, removed: 0 } });
  assert.deepEqual(api.requests[0].body, { name: "example.com.", kind: "Native", nameservers: ["ns1.example.net.", "ns2.example.net."], soa_edit_api: "DEFAULT" });
  assert.deepEqual(api.set("example.com", "example.com.", "A"), { name: "example.com.", type: "A", ttl: 300, records: [{ content: "192.0.2.1", disabled: false }, { content: "192.0.2.3", disabled: false }] });
  assert.deepEqual(api.set("example.com", "_sip._tcp.example.com.", "SRV").records, [{ content: "10 5 5060 sip.example.com.", disabled: false }]);
  assert.deepEqual(api.set("example.com", "example.com.", "TXT").records, [{ content: '"v=spf1 -all"', disabled: false }]);

  // The same records again: no PATCH.
  api.requests = [];
  const again = await call(api, "sync", { name: "example.com", serial: 2, dnssec: false, records });
  assert.deepEqual(again.changes, { added: 0, changed: 0, removed: 0 });
  assert.deepEqual(api.requests, []);

  // Someone adds record sets in PowerDNS. The panel changes one address of @, removes the CNAME
  // record and changes the TTL of the TXT record.
  api.addSet("example.com", { name: "old.example.com.", type: "TXT", ttl: 300, records: ['"left over"'] });
  api.addSet("example.com", { name: "1.example.com.", type: "PTR", ttl: 300, records: ["host.example.com."] });
  const changed = records.filter((r) => r.type !== "CNAME").map((r) => (r.value === "192.0.2.3" ? { ...r, value: "192.0.2.2" } : r.type === "TXT" ? { ...r, ttl: 600 } : r));
  const third = await call(api, "sync", { name: "example.com", serial: 3, dnssec: false, records: changed });
  assert.deepEqual(third.changes, { added: 0, changed: 2, removed: 2 });
  assert.equal(api.requests.length, 1);
  assert.deepEqual(api.requests[0].body.rrsets.map((s) => `${s.changetype} ${s.name} ${s.type}`), [
    "DELETE www.example.com. CNAME",
    "DELETE old.example.com. TXT",
    "REPLACE example.com. A",
    "REPLACE example.com. TXT",
  ]);
  assert.equal(api.set("example.com", "example.com.", "TXT").ttl, 600);
  // The plugin does not change the types that the panel does not have, or the NS records of the zone.
  assert.ok(api.set("example.com", "1.example.com.", "PTR"));
  assert.equal(api.set("example.com", "example.com.", "NS").records.length, 2);
});

test("sync turns DNSSEC on and off, and gives the SHA-256 DS record of the active key", async () => {
  const api = new FakePowerDns();
  api.addZone("example.com");
  const on = await call(api, "sync", { name: "example.com", dnssec: true, records });
  assert.deepEqual(on.dnssec, { state: "active", ds: [{ keyTag: 2371, algorithm: 13, digestType: 2, digest: "A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F90" }] });
  assert.deepEqual(api.requests.at(-1), { method: "PUT", path: "/api/v1/servers/localhost/zones/example.com.", body: { dnssec: true, api_rectify: true } });
  api.requests = [];
  await call(api, "sync", { name: "example.com", dnssec: true, records });
  assert.deepEqual(api.requests, []);
  const off = await call(api, "sync", { name: "example.com", dnssec: false, records });
  assert.deepEqual(off.dnssec, { state: "off", ds: [] });
  assert.equal(api.zones.get("example.com.").dnssec, false);
  assert.deepEqual(parseDs("1 8 1 abcd"), { keyTag: 1, algorithm: 8, digestType: 1, digest: "ABCD" });
});

test("a secondary zone in PowerDNS is an error, and remove removes the zone", async () => {
  const api = new FakePowerDns();
  api.addZone("example.org", { kind: "Slave" });
  await assert.rejects(call(api, "sync", { name: "example.org", records }), (e) => e.status === 422 && /secondary zone/.test(e.message));
  api.addZone("example.com");
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
  assert.equal(api.zones.has("example.com."), false);
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
});

test("a record set has the shortest TTL of its records, and names out of the zone stay", () => {
  const p = plan("example.com", [
    { name: "www", type: "A", ttl: 600, value: "192.0.2.1", priority: null },
    { name: "www", type: "A", ttl: 300, value: "192.0.2.2", priority: null },
  ], [{ name: "www.example.com.", type: "A", ttl: 3600, records: [{ content: "192.0.2.1", disabled: false }, { content: "192.0.2.2", disabled: false }] }, { name: "example.org.", type: "A", ttl: 60, records: [] }]);
  assert.deepEqual(p.rrsets, [{ name: "www.example.com.", type: "A", ttl: 300, changetype: "REPLACE", records: [{ content: "192.0.2.1", disabled: false }, { content: "192.0.2.2", disabled: false }] }]);
});

test("the server refuses a request with no correct signature, and answers a signed request", async () => {
  const api = new FakePowerDns();
  const app = server({ secret: "whsec_test", fetch: api.fetch }).listen(0);
  await new Promise((resolve) => app.once("listening", resolve));
  const url = `http://127.0.0.1:${app.address().port}/dns/powerdns`;
  const send = (payload, key = "whsec_test") => {
    const body = JSON.stringify(payload);
    const t = Math.floor(Date.now() / 1000);
    const v1 = createHmac("sha256", key).update(`${t}.${body}`).digest("hex");
    return fetch(url, { method: "POST", body, headers: { "Content-Type": "application/json", "Cloudfactory-Signature": `t=${t},v1=${v1}` } });
  };
  try {
    const request = { id: "01a", contract: "cf-dns-provider/1", provider: "powerdns", operation: "verify", credentials };
    assert.equal((await send(request, "wrong")).status, 401);
    const r = await send(request);
    assert.equal(r.status, 200);
    assert.deepEqual(await r.json(), { account: "pdns.example.net:8081, server localhost" });
    assert.equal((await send(request)).status, 409);
    const bad = await send({ ...request, id: "01b", credentials: { ...credentials, apiUrl: "https://127.0.0.1" } });
    assert.equal(bad.status, 422);
    assert.match((await bad.json()).error, /private network/);
  } finally {
    app.close();
  }
});
