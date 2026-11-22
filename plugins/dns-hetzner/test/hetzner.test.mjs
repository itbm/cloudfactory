// Tests of the Hetzner DNS plugin, with a fake Hetzner Cloud API. Run: node --test test/*.test.mjs
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import { handle, plan, ProviderError, quoteTxt, toHetzner, toPanel, unquoteTxt } from "../hetzner.mjs";
import { server } from "../server.mjs";

const NS = ["hydrogen.ns.hetzner.com.", "oxygen.ns.hetzner.com.", "helium.ns.hetzner.de."];

/** A fake of the parts of the DNS API of the Hetzner Cloud API that the plugin uses. */
class FakeHetzner {
  constructor() {
    this.zones = new Map();
    this.actions = new Map();
    this.requests = [];
    this.log = [];
    this.next = 1;
    this.fetch = this.fetch.bind(this);
  }

  addZone(name, mode = "primary") {
    const zone = {
      id: this.next++,
      name,
      mode,
      ttl: 3600,
      status: "ok",
      authoritative_nameservers: { assigned: NS, delegated: [], delegation_status: "unknown" },
      rrsets: new Map(),
    };
    zone.rrsets.set("@/SOA", { name: "@", type: "SOA", ttl: null, records: [{ value: `hydrogen.ns.hetzner.com. dns.hetzner.com. 1 86400 10800 3600000 3600` }] });
    zone.rrsets.set("@/NS", { name: "@", type: "NS", ttl: null, records: NS.map((value) => ({ value })) });
    this.zones.set(name, zone);
    return zone;
  }

  addSet(zone, set) {
    this.zones.get(zone).rrsets.set(`${set.name}/${set.type}`, { ttl: null, ...set, records: set.records.map((value) => ({ value, comment: null })) });
  }

  /** An action that is "running". The next GET of the action gives "success". */
  action(command, resource = null) {
    const a = { id: this.next++, command, status: "running", error: null, resource };
    this.actions.set(a.id, a);
    const { resource: _, ...out } = a;
    return out;
  }

  /** True when a record set has a running action. Hetzner then refuses a new change ("locked"). */
  locked(resource) {
    return [...this.actions.values()].some((a) => a.resource === resource && a.status === "running");
  }

  zoneOut(z) {
    const { rrsets, ...rest } = z;
    return rest;
  }

  async fetch(url, init = {}) {
    const u = new URL(url);
    const path = decodeURIComponent(u.pathname.replace("/v1", ""));
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(init.body) : undefined;
    if (method !== "GET") this.requests.push(`${method} ${path}${body?.ttl && !body.records ? ` ttl=${body.ttl}` : ""}`);
    if (method !== "GET") this.log.push(method);
    const ok = (value, status = 200) => new Response(JSON.stringify(value), { status });
    const fail = (status, code, message) => new Response(JSON.stringify({ error: { code, message } }), { status });
    if (init.headers.Authorization !== "Bearer good-token") return fail(401, "unauthorized", "unable to authenticate");
    const byId = (id) => [...this.zones.values()].find((z) => z.id === Number(id));
    let m;
    if ((m = /^\/actions\/(\d+)$/.exec(path))) {
      this.log.push("wait");
      const a = this.actions.get(Number(m[1]));
      a.status = a.command === "fail" ? "error" : "success";
      if (a.status === "error") a.error = { code: "action_failed", message: "the action failed" };
      return ok({ action: a });
    }
    if (path === "/zones" && method === "GET") {
      const name = u.searchParams.get("name");
      const list = [...this.zones.values()].filter((z) => !name || z.name === name).map((z) => this.zoneOut(z));
      return ok({ zones: list, meta: { pagination: { page: 1, next_page: null } } });
    }
    if (path === "/zones" && method === "POST") {
      if (this.zones.has(body.name)) return fail(409, "uniqueness_error", "zone exists");
      const z = this.addZone(body.name, body.mode);
      return ok({ zone: this.zoneOut(z), action: this.action("create_zone") }, 201);
    }
    if ((m = /^\/zones\/(\d+)$/.exec(path))) {
      const z = byId(m[1]);
      if (!z) return fail(404, "not_found", "zone not found");
      if (method === "DELETE") {
        this.zones.delete(z.name);
        return ok({ action: this.action("delete_zone") });
      }
      return ok({ zone: this.zoneOut(z) });
    }
    if ((m = /^\/zones\/(\d+)\/rrsets$/.exec(path))) {
      const z = byId(m[1]);
      if (method === "POST") {
        const id = `${body.name}/${body.type}`;
        if (z.rrsets.has(id)) return fail(409, "uniqueness_error", "rrset exists");
        z.rrsets.set(id, { name: body.name, type: body.type, ttl: body.ttl ?? null, records: body.records });
        return ok({ rrset: { id, ...z.rrsets.get(id) }, action: this.action("create_rrset", `${z.id}/${id}`) }, 201);
      }
      // Two record sets on each page, so that the test reads more than one page.
      const page = Number(u.searchParams.get("page"));
      const all = [...z.rrsets.entries()].map(([id, s]) => ({ id, ...s }));
      const last = page * 2 >= all.length;
      return ok({ rrsets: all.slice((page - 1) * 2, page * 2), meta: { pagination: { page, next_page: last ? null : page + 1 } } });
    }
    if ((m = /^\/zones\/(\d+)\/rrsets\/([^/]+)\/([A-Z]+)(\/actions\/(\w+))?$/.exec(path))) {
      const z = byId(m[1]);
      const id = `${m[2]}/${m[3]}`;
      const set = z.rrsets.get(id);
      if (!set) return fail(404, "not_found", "rrset not found");
      if (this.locked(`${z.id}/${id}`)) return fail(423, "locked", "the item is locked");
      if (method === "DELETE") {
        z.rrsets.delete(id);
        return ok({ action: this.action("delete_rrset", `${z.id}/${id}`) });
      }
      if (m[5] === "set_records") set.records = body.records;
      if (m[5] === "change_ttl") set.ttl = body.ttl;
      return ok({ action: this.action(m[5], `${z.id}/${id}`) }, 201);
    }
    return fail(404, "not_found", `No route ${method} ${path}`);
  }
}

const credentials = { apiToken: "good-token" };
const options = (api) => ({ base: "https://api.test/v1", fetch: api.fetch, pause: 0 });
const call = (api, operation, zone) => handle({ operation, credentials, zone }, options(api));

const records = [
  { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
  { name: "@", type: "A", ttl: 300, value: "192.0.2.3", priority: null },
  { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
  { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
  { name: "@", type: "TXT", ttl: 3600, value: 'v=spf1 include:"x" -all', priority: null },
  { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
  { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
  { name: "sub", type: "NS", ttl: 3600, value: "ns1.example.net", priority: null },
];

test("verify checks the token", async () => {
  const api = new FakeHetzner();
  assert.deepEqual(await call(api, "verify", null), { account: null });
  await assert.rejects(handle({ operation: "verify", credentials: { apiToken: "bad" } }, options(api)),
    (e) => e instanceof ProviderError && e.status === 422 && /refused the API token/.test(e.message));
  await assert.rejects(handle({ operation: "verify", credentials: {} }, options(api)), /Enter the API token/);
});

test("records gives the record sets in the form of the panel, with no SOA and no NS records of the zone", async () => {
  const api = new FakeHetzner();
  assert.deepEqual(await call(api, "records", { name: "example.com" }), { exists: false, records: [], nameServers: [] });
  api.addZone("example.com");
  api.addSet("example.com", { name: "@", type: "A", ttl: 300, records: ["192.0.2.1", "192.0.2.3"] });
  api.addSet("example.com", { name: "www", type: "CNAME", records: ["@"] });
  api.addSet("example.com", { name: "@", type: "MX", records: ["10 Mail.Example.net."] });
  api.addSet("example.com", { name: "@", type: "TXT", records: ['"v=spf1 " "-all"'] });
  api.addSet("example.com", { name: "_sip._tcp", type: "SRV", records: ["10 5 5060 sip"] });
  api.addSet("example.com", { name: "@", type: "CAA", records: ['0 issue "letsencrypt.org"'] });
  const out = await call(api, "records", { name: "Example.com" });
  assert.deepEqual(out.nameServers, ["hydrogen.ns.hetzner.com", "oxygen.ns.hetzner.com", "helium.ns.hetzner.de"]);
  assert.deepEqual(out.records, [
    { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
    { name: "@", type: "A", ttl: 300, value: "192.0.2.3", priority: null },
    { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
    { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
    { name: "@", type: "TXT", ttl: 3600, value: "v=spf1 -all", priority: null },
    { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
    { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
  ]);
});

test("a secondary zone at Hetzner is an error", async () => {
  const api = new FakeHetzner();
  api.addZone("example.com", "secondary");
  await assert.rejects(call(api, "sync", { name: "example.com", records }), (e) => e.status === 422 && /secondary zone/.test(e.message));
});

test("sync makes the zone, and then makes the record sets the same as the records of the panel", async () => {
  const api = new FakeHetzner();
  const first = await call(api, "sync", { name: "example.com", serial: 1, dnssec: false, records });
  assert.deepEqual(first, {
    nameServers: ["hydrogen.ns.hetzner.com", "oxygen.ns.hetzner.com", "helium.ns.hetzner.de"],
    active: false,
    dnssec: { state: "off", ds: [] },
    changes: { added: 8, changed: 0, removed: 0 },
  });
  const sets = api.zones.get("example.com").rrsets;
  assert.deepEqual(sets.get("@/A"), { name: "@", type: "A", ttl: 300, records: [{ value: "192.0.2.1" }, { value: "192.0.2.3" }] });
  assert.deepEqual(sets.get("@/MX").records, [{ value: "10 mail.example.net." }]);
  assert.deepEqual(sets.get("@/TXT").records, [{ value: '"v=spf1 include:\\"x\\" -all"' }]);
  assert.deepEqual(sets.get("_sip._tcp/SRV").records, [{ value: "10 5 5060 sip.example.com." }]);
  // The NS records of the zone itself stay the records of Hetzner.
  assert.equal(sets.get("@/NS").records.length, 3);

  // The same records again: no change.
  api.requests = [];
  const again = await call(api, "sync", { name: "example.com", serial: 2, dnssec: false, records });
  assert.deepEqual(again.changes, { added: 0, changed: 0, removed: 0 });
  assert.deepEqual(api.requests, []);

  // Someone adds record sets at Hetzner. The panel changes one address of @, removes the CNAME
  // record and changes the TTL of the TXT record. The registrar now has the name servers of Hetzner.
  api.addSet("example.com", { name: "old", type: "TXT", records: ['"left over"'] });
  api.addSet("example.com", { name: "1", type: "PTR", records: ["host.example.com."] });
  api.zones.get("example.com").authoritative_nameservers.delegation_status = "valid";
  api.log = [];
  const changed = records.filter((r) => r.type !== "CNAME").map((r) => (r.value === "192.0.2.3" ? { ...r, value: "192.0.2.2" } : r.type === "TXT" ? { ...r, ttl: 600 } : r));
  const third = await call(api, "sync", { name: "example.com", serial: 3, dnssec: false, records: changed });
  assert.equal(third.active, true);
  assert.deepEqual(third.changes, { added: 0, changed: 2, removed: 2 });
  assert.deepEqual(api.requests.slice(-4), [
    "DELETE /zones/1/rrsets/www/CNAME",
    "DELETE /zones/1/rrsets/old/TXT",
    "POST /zones/1/rrsets/@/A/actions/set_records",
    "POST /zones/1/rrsets/@/TXT/actions/change_ttl ttl=600",
  ]);
  // The plugin waits for the actions after the deletes and at the end, not after each change.
  assert.deepEqual(api.log, ["DELETE", "DELETE", "wait", "wait", "POST", "POST", "wait", "wait"]);
  assert.deepEqual(sets.get("@/A").records, [{ value: "192.0.2.1" }, { value: "192.0.2.2" }]);
  assert.equal(sets.get("@/TXT").ttl, 600);
  assert.equal(sets.has("www/CNAME") || sets.has("old/TXT"), false);
  // The plugin does not change the types that the panel does not have.
  assert.equal(sets.has("1/PTR"), true);
});

test("a change to a record set with a running action waits for the actions, and a slow sync stops in time", async () => {
  const api = new FakeHetzner();
  api.addZone("example.com");
  api.addSet("example.com", { name: "@", type: "A", ttl: 600, records: ["192.0.2.1"] });
  // A new value and a new TTL: set_records, and then change_ttl of the same record set.
  const out = await call(api, "sync", { name: "example.com", records: [{ name: "@", type: "A", ttl: 300, value: "192.0.2.2", priority: null }] });
  assert.deepEqual(out.changes, { added: 0, changed: 1, removed: 0 });
  assert.deepEqual(api.zones.get("example.com").rrsets.get("@/A"), { name: "@", type: "A", ttl: 300, records: [{ value: "192.0.2.2" }] });

  await assert.rejects(handle({ operation: "sync", credentials, zone: { name: "example.org", records } }, { ...options(api), budget: -1 }), (e) => e.status === 502 && /did not take all the changes in time/.test(e.message));
});

test("sync gives an error when an action of Hetzner fails", async () => {
  const api = new FakeHetzner();
  api.addZone("example.com");
  const real = api.action.bind(api);
  api.action = (command) => real(command === "create_rrset" ? "fail" : command);
  await assert.rejects(call(api, "sync", { name: "example.com", records: records.slice(0, 1) }), (e) => e.status === 422 && /could not complete the action fail: the action failed/.test(e.message));
});

test("remove removes the zone, and a zone that is not there is not an error", async () => {
  const api = new FakeHetzner();
  api.addZone("example.com");
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
  assert.equal(api.zones.size, 0);
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
});

test("the values of records change between the forms of the panel and of Hetzner", () => {
  assert.equal(quoteTxt("a".repeat(300)), `"${"a".repeat(255)}" "${"a".repeat(45)}"`);
  assert.equal(unquoteTxt('"say \\"hi\\"" " there"'), 'say "hi" there');
  assert.equal(unquoteTxt("no quotes"), "no quotes");
  assert.equal(toHetzner({ type: "CNAME", value: "example.com" }), "example.com.");
  assert.deepEqual(toPanel("example.com", { name: "@", type: "CAA", ttl: 300 }, "128 IssueWild ca.example"), { name: "@", type: "CAA", ttl: 300, priority: null, value: '128 issuewild "ca.example"' });
  assert.deepEqual(toPanel("example.com", { name: "mail", type: "AAAA", ttl: 300 }, "2001:DB8::1").value, "2001:db8::1");
  // A record set has one TTL: the shortest TTL of its records in the panel.
  const p = plan("example.com", [
    { name: "www", type: "A", ttl: 600, value: "192.0.2.1", priority: null },
    { name: "www", type: "A", ttl: 300, value: "192.0.2.2", priority: null },
  ], [{ name: "www", type: "A", ttl: null, records: [{ value: "192.0.2.1" }, { value: "192.0.2.2" }] }], 3600);
  assert.deepEqual(p, { creates: [], updates: [{ name: "www", type: "A", records: null, ttl: 300 }], deletes: [] });
});

test("the server refuses a request with no correct signature, and answers a signed request", async () => {
  const api = new FakeHetzner();
  const app = server({ secret: "whsec_test", ...options(api) }).listen(0);
  await new Promise((resolve) => app.once("listening", resolve));
  const url = `http://127.0.0.1:${app.address().port}/dns/hetzner`;
  const send = (payload, key = "whsec_test") => {
    const body = JSON.stringify(payload);
    const t = Math.floor(Date.now() / 1000);
    const v1 = createHmac("sha256", key).update(`${t}.${body}`).digest("hex");
    return fetch(url, { method: "POST", body, headers: { "Content-Type": "application/json", "Cloudfactory-Signature": `t=${t},v1=${v1}` } });
  };
  try {
    const request = { id: "01a", contract: "cf-dns-provider/1", provider: "hetzner", operation: "verify", credentials };
    assert.equal((await send(request, "wrong")).status, 401);
    const r = await send(request);
    assert.equal(r.status, 200);
    assert.deepEqual(await r.json(), { account: null });
    assert.equal((await send(request)).status, 409);
    const bad = await send({ ...request, id: "01b", credentials: { apiToken: "bad" } });
    assert.equal(bad.status, 422);
    assert.match((await bad.json()).error, /Hetzner refused/);
  } finally {
    app.close();
  }
});
