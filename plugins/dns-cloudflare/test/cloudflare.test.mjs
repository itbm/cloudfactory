// Tests of the Cloudflare DNS plugin, with a fake Cloudflare API. Run: node --test test/
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import { handle, plan, ProviderError, quoteTxt, unquoteTxt } from "../cloudflare.mjs";
import { server } from "../server.mjs";

/** A fake of the parts of the Cloudflare API v4 that the plugin uses. */
class FakeCloudflare {
  constructor() {
    this.zones = [];
    this.records = new Map();
    this.dnssec = new Map();
    this.batches = [];
    this.next = 1;
    this.fetch = this.fetch.bind(this);
  }

  addZone(name, { status = "active", account = "acc1" } = {}) {
    const zone = { id: `z${this.next++}`, name, status, account: { id: account }, name_servers: ["ada.ns.cloudflare.com", "bob.ns.cloudflare.com"] };
    this.zones.push(zone);
    this.records.set(zone.id, []);
    this.dnssec.set(zone.id, { status: "disabled" });
    return zone;
  }

  addRecord(zone, record) {
    const r = { id: `r${this.next++}`, proxied: false, ...record };
    this.records.get(zone.id).push(r);
    return r;
  }

  async fetch(url, init = {}) {
    const u = new URL(url);
    const path = u.pathname.replace("/client/v4", "");
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : undefined;
    const ok = (result, extra = {}) => new Response(JSON.stringify({ success: true, errors: [], result, ...extra }), { status: 200 });
    const fail = (status, message, code = 1000) => new Response(JSON.stringify({ success: false, errors: [{ code, message }] }), { status });
    if (init.headers.Authorization !== "Bearer good-token") return fail(401, "Invalid API Token", 1000);
    let m;
    if (path === "/user/tokens/verify") return ok({ id: "t1", status: "active" });
    if ((m = /^\/accounts\/([^/]+)$/.exec(path))) return ok({ id: m[1], name: "Example Ltd" });
    if (path === "/zones" && method === "GET") {
      const account = u.searchParams.get("account.id");
      return ok(this.zones.filter((z) => z.name === u.searchParams.get("name") && (!account || z.account.id === account)));
    }
    if (path === "/zones" && method === "POST") return ok(this.addZone(body.name, { status: "pending", account: body.account.id }));
    if ((m = /^\/zones\/([^/]+)$/.exec(path)) && method === "DELETE") {
      this.zones = this.zones.filter((z) => z.id !== m[1]);
      return ok({ id: m[1] });
    }
    if ((m = /^\/zones\/([^/]+)\/dns_records$/.exec(path))) {
      const all = this.records.get(m[1]);
      const page = Number(u.searchParams.get("page"));
      // Two records on each page, so that the test reads more than one page.
      return ok(all.slice((page - 1) * 2, page * 2), { result_info: { page, per_page: 2, total_pages: Math.max(1, Math.ceil(all.length / 2)) } });
    }
    if ((m = /^\/zones\/([^/]+)\/dns_records\/batch$/.exec(path))) {
      this.batches.push(body);
      let list = this.records.get(m[1]);
      for (const d of body.deletes ?? []) list = list.filter((r) => r.id !== d.id);
      for (const p of body.patches ?? []) list = list.map((r) => (r.id === p.id ? { ...r, ...p } : r));
      for (const p of body.posts ?? []) {
        const content = p.data && p.type === "SRV" ? `${p.data.weight} ${p.data.port} ${p.data.target}` : p.data ? `${p.data.flags} ${p.data.tag} "${p.data.value}"` : p.content;
        list.push({ id: `r${this.next++}`, proxied: false, ...p, content, priority: p.data?.priority ?? p.priority });
      }
      this.records.set(m[1], list);
      return ok({});
    }
    if ((m = /^\/zones\/([^/]+)\/dnssec$/.exec(path))) {
      if (method === "PATCH") {
        this.dnssec.set(m[1], body.status === "active" ? { status: "pending", key_tag: 2371, algorithm: "13", digest_type: "2", digest: "AB12CD" } : { status: "pending-disabled" });
      }
      return ok(this.dnssec.get(m[1]));
    }
    return fail(404, `No route ${method} ${path}`, 7003);
  }
}

const credentials = { apiToken: "good-token", accountId: "acc1" };
const call = (cf, operation, zone, extra = {}) => handle({ operation, credentials, zone, ...extra }, { base: "https://api.test/client/v4", fetch: cf.fetch, batchSize: 2 });

test("a TXT text gets quotation marks and parts of 255 characters, and comes back the same", () => {
  const text = `v=DKIM1; k=rsa; p=${"A".repeat(290)} "quoted" \\ end`;
  const quoted = quoteTxt(text);
  assert.match(quoted, /^"v=DKIM1[^"]*" "/);
  assert.equal(unquoteTxt(quoted), text);
  assert.equal(unquoteTxt("plain text"), "plain text");
});

test("verify checks the token, and gives the name of the account", async () => {
  const cf = new FakeCloudflare();
  assert.deepEqual(await call(cf, "verify", null), { account: "Example Ltd" });
  await assert.rejects(handle({ operation: "verify", credentials: { apiToken: "bad" } }, { fetch: cf.fetch }), (e) => e instanceof ProviderError && e.status === 422);
  await assert.rejects(handle({ operation: "verify", credentials: {} }, { fetch: cf.fetch }), /Enter the API token/);
});

test("records gives the records in the form of the panel, with no NS records of the zone itself", async () => {
  const cf = new FakeCloudflare();
  assert.deepEqual(await call(cf, "records", { name: "example.com" }), { exists: false, records: [], nameServers: [] });
  const zone = cf.addZone("example.com");
  cf.addRecord(zone, { type: "NS", name: "example.com", content: "ada.ns.cloudflare.com", ttl: 86400 });
  cf.addRecord(zone, { type: "A", name: "example.com", content: "192.0.2.1", ttl: 1, proxied: true });
  cf.addRecord(zone, { type: "MX", name: "example.com", content: "Mail.Example.net.", ttl: 300, priority: 10 });
  cf.addRecord(zone, { type: "TXT", name: "example.com", content: '"v=spf1 " "-all"', ttl: 300 });
  cf.addRecord(zone, { type: "SRV", name: "_sip._tcp.example.com", content: "5 5060 sip.example.com", ttl: 300, data: { priority: 10, weight: 5, port: 5060, target: "sip.example.com" } });
  cf.addRecord(zone, { type: "CAA", name: "example.com", content: '0 issue "letsencrypt.org"', ttl: 300, data: { flags: 0, tag: "issue", value: "letsencrypt.org" } });
  cf.addRecord(zone, { type: "HTTPS", name: "www.example.com", content: '1 . alpn="h2"', ttl: 300 });
  const out = await call(cf, "records", { name: "example.com" });
  assert.equal(out.exists, true);
  assert.deepEqual(out.nameServers, ["ada.ns.cloudflare.com", "bob.ns.cloudflare.com"]);
  assert.deepEqual(out.records, [
    { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
    { name: "@", type: "MX", ttl: 300, value: "mail.example.net", priority: 10 },
    { name: "@", type: "TXT", ttl: 300, value: "v=spf1 -all", priority: null },
    { name: "_sip._tcp", type: "SRV", ttl: 300, value: "5 5060 sip.example.com", priority: 10 },
    { name: "@", type: "CAA", ttl: 300, value: '0 issue "letsencrypt.org"', priority: null },
    // The panel does not have this type. It shows it as a skipped record.
    { name: "www", type: "HTTPS", ttl: 300, value: '1 . alpn="h2"', priority: null },
  ]);
});

test("sync makes the zone, and then makes the records the same as the records of the panel", async () => {
  const cf = new FakeCloudflare();
  const records = [
    { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
    { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
    { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
    { name: "@", type: "TXT", ttl: 604800, value: "v=spf1 -all", priority: null },
    { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
    { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
  ];
  const first = await call(cf, "sync", { name: "example.com", serial: 1, dnssec: false, records });
  assert.deepEqual(first, { nameServers: ["ada.ns.cloudflare.com", "bob.ns.cloudflare.com"], active: false, dnssec: { state: "off", ds: [] }, changes: { added: 6, changed: 0, removed: 0 } });
  const zone = cf.zones[0];
  // Parts of 2 changes for the test: three batches.
  assert.equal(cf.batches.length, 3);
  const txt = cf.records.get(zone.id).find((r) => r.type === "TXT");
  // The largest TTL of Cloudflare is one day.
  assert.equal(txt.ttl, 86400);
  assert.equal(txt.content, '"v=spf1 -all"');

  // The same records again: no change.
  cf.batches = [];
  const again = await call(cf, "sync", { name: "example.com", serial: 2, dnssec: false, records });
  assert.deepEqual(again.changes, { added: 0, changed: 0, removed: 0 });
  assert.equal(cf.batches.length, 0);

  // At Cloudflare, someone turns on the proxy for @, and adds records. The panel changes the
  // address of @ and removes the CNAME record.
  const list = cf.records.get(zone.id);
  Object.assign(list.find((r) => r.type === "A"), { proxied: true, ttl: 1 });
  cf.addRecord(zone, { type: "TXT", name: "old.example.com", content: '"left over"', ttl: 300 });
  cf.addRecord(zone, { type: "HTTPS", name: "example.com", content: "1 .", ttl: 300 });
  cf.addRecord(zone, { type: "NS", name: "example.com", content: "ada.ns.cloudflare.com", ttl: 86400 });
  const changed = records.filter((r) => r.type !== "CNAME").map((r) => (r.type === "A" ? { ...r, value: "192.0.2.2" } : r));
  const third = await call(cf, "sync", { name: "example.com", serial: 3, dnssec: false, records: changed });
  assert.deepEqual(third.changes, { added: 0, changed: 1, removed: 2 });
  const after = cf.records.get(zone.id);
  const a = after.find((r) => r.type === "A");
  // The record keeps its proxy and the automatic TTL.
  assert.deepEqual([a.content, a.proxied, a.ttl], ["192.0.2.2", true, 1]);
  assert.equal(after.some((r) => r.type === "CNAME" || r.name === "old.example.com"), false);
  // The plugin does not change the types that the panel does not have, or the NS records of the zone.
  assert.equal(after.filter((r) => r.type === "HTTPS" || r.type === "NS").length, 2);
});

test("sync with no account ID cannot make a zone", async () => {
  const cf = new FakeCloudflare();
  await assert.rejects(handle({ operation: "sync", credentials: { apiToken: "good-token" }, zone: { name: "example.com", records: [] } }, { base: "https://api.test/client/v4", fetch: cf.fetch }),
    (e) => e instanceof ProviderError && e.status === 422 && /give the account ID/.test(e.message));
});

test("sync turns DNSSEC on and off, and gives the DS record", async () => {
  const cf = new FakeCloudflare();
  cf.addZone("example.com");
  const on = await call(cf, "sync", { name: "example.com", dnssec: true, records: [] });
  assert.deepEqual(on.dnssec, { state: "pending", ds: [{ keyTag: 2371, algorithm: 13, digestType: 2, digest: "AB12CD" }] });
  const off = await call(cf, "sync", { name: "example.com", dnssec: false, records: [] });
  assert.equal(off.dnssec.state, "off");
});

test("remove removes the zone at Cloudflare, and a zone that is not there is not an error", async () => {
  const cf = new FakeCloudflare();
  cf.addZone("example.com");
  assert.deepEqual(await call(cf, "remove", { name: "example.com" }), {});
  assert.equal(cf.zones.length, 0);
  assert.deepEqual(await call(cf, "remove", { name: "example.com" }), {});
});

test("plan keeps a record that has the same value, and patches only its TTL", () => {
  const existing = [{ id: "r1", type: "A", name: "www.example.com", content: "192.0.2.1", ttl: 300, proxied: false }];
  assert.deepEqual(plan("example.com", [{ name: "www", type: "A", ttl: 3600, value: "192.0.2.1", priority: null }], existing), { deletes: [], patches: [{ id: "r1", ttl: 3600 }], posts: [] });
});

test("the server refuses a request with no correct signature, and answers a signed request", async () => {
  const cf = new FakeCloudflare();
  const app = server({ secret: "whsec_test", base: "https://api.test/client/v4", fetch: cf.fetch }).listen(0);
  await new Promise((resolve) => app.once("listening", resolve));
  const url = `http://127.0.0.1:${app.address().port}/dns/cloudflare`;
  const send = (payload, key = "whsec_test") => {
    const body = JSON.stringify(payload);
    const t = Math.floor(Date.now() / 1000);
    const v1 = createHmac("sha256", key).update(`${t}.${body}`).digest("hex");
    return fetch(url, { method: "POST", body, headers: { "Content-Type": "application/json", "Cloudfactory-Signature": `t=${t},v1=${v1}` } });
  };
  try {
    const request = { id: "01a", contract: "cf-dns-provider/1", provider: "cloudflare", operation: "verify", credentials };
    assert.equal((await send(request, "wrong")).status, 401);
    const r = await send(request);
    assert.equal(r.status, 200);
    assert.deepEqual(await r.json(), { account: "Example Ltd" });
    assert.equal((await send(request)).status, 409);
    assert.equal((await send({ ...request, id: "01b", contract: "cf-dns-provider/2" })).status, 400);
    const bad = await send({ ...request, id: "01c", credentials: { apiToken: "bad" } });
    assert.equal(bad.status, 422);
    assert.match((await bad.json()).error, /Cloudflare refused/);
  } finally {
    app.close();
  }
});
