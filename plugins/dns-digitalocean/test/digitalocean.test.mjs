// Tests of the DigitalOcean DNS plugin, with a fake DigitalOcean API. Run: node --test test/*.test.mjs
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import { handle, plan, ProviderError, toDigitalOcean } from "../digitalocean.mjs";
import { server } from "../server.mjs";

/** A fake of the parts of the DigitalOcean API v2 that the plugin uses. */
class FakeDigitalOcean {
  constructor() {
    this.domains = new Map();
    this.requests = [];
    this.next = 1;
    this.fetch = this.fetch.bind(this);
  }

  addDomain(name) {
    this.domains.set(name, [
      { id: this.next++, type: "SOA", name: "@", data: "1800", ttl: 1800 },
      ...["ns1", "ns2", "ns3"].map((n) => ({ id: this.next++, type: "NS", name: "@", data: `${n}.digitalocean.com`, ttl: 1800 })),
    ]);
  }

  addRecord(domain, record) {
    const r = { id: this.next++, priority: null, port: null, weight: null, flags: null, tag: null, ...record };
    this.domains.get(domain).push(r);
    return r;
  }

  async fetch(url, init = {}) {
    const u = new URL(url);
    const path = u.pathname.replace("/v2", "");
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (method !== "GET") this.requests.push(`${method} ${path}`);
    const ok = (value, status = 200) => new Response(status === 204 ? null : JSON.stringify(value), { status });
    const fail = (status, message) => new Response(JSON.stringify({ id: "error", message }), { status });
    if (init.headers.Authorization !== "Bearer good-token") return fail(401, "Unable to authenticate you");
    let m;
    if (path === "/account") return ok({ account: { email: "it@example.com", team: { name: "Example Ltd" } } });
    if (path === "/domains" && method === "POST") {
      this.addDomain(body.name);
      return ok({ domain: { name: body.name } }, 201);
    }
    if ((m = /^\/domains\/([^/]+)$/.exec(path))) {
      if (!this.domains.has(m[1])) return fail(404, "The resource you were accessing could not be found.");
      if (method === "DELETE") {
        this.domains.delete(m[1]);
        return ok(null, 204);
      }
      return ok({ domain: { name: m[1], ttl: 1800 } });
    }
    if ((m = /^\/domains\/([^/]+)\/records$/.exec(path))) {
      const list = this.domains.get(m[1]);
      if (method === "POST") return ok({ domain_record: this.addRecord(m[1], body) }, 201);
      // Three records on each page, so that the test reads more than one page.
      const page = Number(u.searchParams.get("page"));
      const last = page * 3 >= list.length;
      return ok({ domain_records: list.slice((page - 1) * 3, page * 3), links: last ? {} : { pages: { next: `${url}&next` } } });
    }
    if ((m = /^\/domains\/([^/]+)\/records\/(\d+)$/.exec(path))) {
      const list = this.domains.get(m[1]);
      const i = list.findIndex((r) => r.id === Number(m[2]));
      if (i < 0) return fail(404, "not found");
      if (method === "DELETE") {
        list.splice(i, 1);
        return ok(null, 204);
      }
      list[i] = { ...list[i], ...body };
      return ok({ domain_record: list[i] });
    }
    return fail(404, `No route ${method} ${path}`);
  }
}

const credentials = { apiToken: "good-token" };
const call = (api, operation, zone) => handle({ operation, credentials, zone }, { base: "https://api.test/v2", fetch: api.fetch });

const records = [
  { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
  { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
  { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
  { name: "@", type: "TXT", ttl: 3600, value: "v=spf1 -all", priority: null },
  { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
  { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
];

test("verify checks the token, and gives the name of the team", async () => {
  const api = new FakeDigitalOcean();
  assert.deepEqual(await call(api, "verify", null), { account: "Example Ltd" });
  await assert.rejects(handle({ operation: "verify", credentials: { apiToken: "bad" } }, { base: "https://api.test/v2", fetch: api.fetch }),
    (e) => e instanceof ProviderError && e.status === 422 && /refused the API token/.test(e.message));
  await assert.rejects(handle({ operation: "verify", credentials: {} }, { fetch: api.fetch }), /Enter the API token/);
});

test("records gives the records in the form of the panel, with no SOA and no NS records of the domain", async () => {
  const api = new FakeDigitalOcean();
  assert.deepEqual(await call(api, "records", { name: "example.com" }), { exists: false, records: [], nameServers: [] });
  api.addDomain("example.com");
  api.addRecord("example.com", { type: "A", name: "@", data: "192.0.2.1", ttl: 300 });
  api.addRecord("example.com", { type: "CNAME", name: "www", data: "@", ttl: 3600 });
  api.addRecord("example.com", { type: "MX", name: "@", data: "Mail.Example.net", priority: 10, ttl: 3600 });
  api.addRecord("example.com", { type: "SRV", name: "_sip._tcp", data: "sip.example.com", priority: 10, weight: 5, port: 5060, ttl: 3600 });
  api.addRecord("example.com", { type: "CAA", name: "@", data: "letsencrypt.org", flags: 0, tag: "issue", ttl: 3600 });
  const out = await call(api, "records", { name: "example.com" });
  assert.deepEqual(out.nameServers, ["ns1.digitalocean.com", "ns2.digitalocean.com", "ns3.digitalocean.com"]);
  assert.deepEqual(out.records, [
    { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
    { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
    { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
    { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
    { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
  ]);
});

test("sync makes the domain, and then makes the records the same as the records of the panel", async () => {
  const api = new FakeDigitalOcean();
  const first = await call(api, "sync", { name: "example.com", serial: 1, dnssec: false, records });
  assert.deepEqual(first, { nameServers: ["ns1.digitalocean.com", "ns2.digitalocean.com", "ns3.digitalocean.com"], active: true, dnssec: { state: "off", ds: [] }, changes: { added: 6, changed: 0, removed: 0 } });
  const list = api.domains.get("example.com");
  assert.deepEqual(list.find((r) => r.type === "MX"), { id: list.find((r) => r.type === "MX").id, type: "MX", name: "@", ttl: 3600, data: "mail.example.net.", priority: 10, port: null, weight: null, flags: null, tag: null });
  assert.deepEqual(list.find((r) => r.type === "CAA").tag, "issue");

  // The same records again: no change.
  api.requests = [];
  const again = await call(api, "sync", { name: "example.com", serial: 2, dnssec: false, records });
  assert.deepEqual(again.changes, { added: 0, changed: 0, removed: 0 });
  assert.deepEqual(api.requests, []);

  // Someone adds records at DigitalOcean. The panel changes the address of @, removes the
  // CNAME record and changes the TTL of the TXT record.
  api.addRecord("example.com", { type: "TXT", name: "old", data: "left over", ttl: 300 });
  api.addRecord("example.com", { type: "PTR", name: "1", data: "host.example.com.", ttl: 300 });
  const changed = records.filter((r) => r.type !== "CNAME").map((r) => (r.type === "A" ? { ...r, value: "192.0.2.2" } : r.type === "TXT" ? { ...r, ttl: 600 } : r));
  const third = await call(api, "sync", { name: "example.com", serial: 3, dnssec: false, records: changed });
  assert.deepEqual(third.changes, { added: 0, changed: 2, removed: 2 });
  const after = api.domains.get("example.com");
  assert.equal(after.find((r) => r.type === "A").data, "192.0.2.2");
  assert.equal(after.find((r) => r.type === "TXT").ttl, 600);
  assert.equal(after.some((r) => r.type === "CNAME" || r.name === "old"), false);
  // The plugin does not change the types that the panel does not have, or the NS records of the domain.
  assert.equal(after.filter((r) => r.type === "PTR" || r.type === "NS" || r.type === "SOA").length, 5);
});

test("a slow sync stops in time", async () => {
  const api = new FakeDigitalOcean();
  await assert.rejects(handle({ operation: "sync", credentials, zone: { name: "example.com", records } }, { base: "https://api.test/v2", fetch: api.fetch, budget: -1 }),
    (e) => e.status === 502 && /did not take all the changes in time/.test(e.message));
});

test("remove removes the domain, and a domain that is not there is not an error", async () => {
  const api = new FakeDigitalOcean();
  api.addDomain("example.com");
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
  assert.equal(api.domains.size, 0);
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
});

test("a CAA, SRV and MX record get the fields of DigitalOcean", () => {
  assert.deepEqual(toDigitalOcean({ name: "@", type: "CAA", ttl: 300, value: '128 issuewild "ca.example"', priority: null }),
    { type: "CAA", name: "@", ttl: 300, data: "ca.example", priority: null, port: null, weight: null, flags: 128, tag: "issuewild" });
  assert.deepEqual(toDigitalOcean({ name: "_sip._tcp", type: "SRV", ttl: 300, value: "5 5060 sip.example.com", priority: 10 }),
    { type: "SRV", name: "_sip._tcp", ttl: 300, data: "sip.example.com.", priority: 10, port: 5060, weight: 5, flags: null, tag: null });
  assert.deepEqual(plan("example.com", [{ name: "www", type: "A", ttl: 600, value: "192.0.2.1", priority: null }], [{ id: 7, type: "A", name: "www", data: "192.0.2.1", ttl: 300 }]).updates.map((u) => [u.id, u.ttl]), [[7, 600]]);
});

test("the server refuses a request with no correct signature, and answers a signed request", async () => {
  const api = new FakeDigitalOcean();
  const app = server({ secret: "whsec_test", base: "https://api.test/v2", fetch: api.fetch }).listen(0);
  await new Promise((resolve) => app.once("listening", resolve));
  const url = `http://127.0.0.1:${app.address().port}/dns/digitalocean`;
  const send = (payload, key = "whsec_test") => {
    const body = JSON.stringify(payload);
    const t = Math.floor(Date.now() / 1000);
    const v1 = createHmac("sha256", key).update(`${t}.${body}`).digest("hex");
    return fetch(url, { method: "POST", body, headers: { "Content-Type": "application/json", "Cloudfactory-Signature": `t=${t},v1=${v1}` } });
  };
  try {
    const request = { id: "01a", contract: "cf-dns-provider/1", provider: "digitalocean", operation: "verify", credentials };
    assert.equal((await send(request, "wrong")).status, 401);
    const r = await send(request);
    assert.equal(r.status, 200);
    assert.deepEqual(await r.json(), { account: "Example Ltd" });
    assert.equal((await send(request)).status, 409);
    const bad = await send({ ...request, id: "01b", credentials: { apiToken: "bad" } });
    assert.equal(bad.status, 422);
    assert.match((await bad.json()).error, /DigitalOcean refused/);
  } finally {
    app.close();
  }
});
