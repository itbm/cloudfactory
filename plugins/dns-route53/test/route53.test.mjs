// Tests of the Route 53 DNS plugin, with a fake Route 53 API. Run: node --test test/*.test.mjs
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import { batches, decodeName, handle, kid, kids, parseXml, ProviderError, sign, text, toXml, unquoteTxt } from "../route53.mjs";
import { server } from "../server.mjs";

const KEY = { accessKeyId: "AKIAGOOD", secretAccessKey: "secret/key+1" };
const NS = ["ns-1.awsdns-01.org", "ns-2.awsdns-02.co.uk", "ns-3.awsdns-03.com", "ns-4.awsdns-04.net"];
const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const el = (name, inner) => `<${name}>${inner}</${name}>`;

/** A fake of the parts of the Route 53 API that the plugin uses. It checks each signature. */
class FakeRoute53 {
  constructor() {
    this.zones = new Map();
    this.next = 1;
    this.requests = [];
    this.batches = [];
    this.fetch = this.fetch.bind(this);
  }

  addZone(name, { private: isPrivate = false } = {}) {
    const id = `Z${String(this.next++).padStart(4, "0")}`;
    const zone = {
      id,
      name: `${name}.`,
      private: isPrivate,
      dnssec: "NOT_SIGNING",
      keys: [],
      sets: [
        { name: `${name}.`, type: "SOA", ttl: 900, values: [`${NS[0]}. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400`] },
        { name: `${name}.`, type: "NS", ttl: 172800, values: NS.map((n) => `${n}.`) },
      ],
    };
    this.zones.set(id, zone);
    return zone;
  }

  zoneByName(name) {
    return [...this.zones.values()].find((z) => z.name === `${name}.` && !z.private);
  }

  setXml(s) {
    return el("ResourceRecordSet", [
      el("Name", esc(s.name)),
      el("Type", s.type),
      s.setIdentifier ? el("SetIdentifier", esc(s.setIdentifier)) + el("Weight", "10") : "",
      s.alias ? el("AliasTarget", el("HostedZoneId", "Z2FDTNDATAQYW2") + el("DNSName", esc(s.alias)) + el("EvaluateTargetHealth", "false")) : "",
      s.alias ? "" : el("TTL", s.ttl) + el("ResourceRecords", s.values.map((v) => el("ResourceRecord", el("Value", esc(v)))).join("")),
    ].join(""));
  }

  readSet(node) {
    const alias = kid(node, "AliasTarget");
    return {
      name: text(node, "Name").toLowerCase(),
      type: text(node, "Type"),
      ttl: alias ? null : Number(text(node, "TTL")),
      values: kids(kid(node, "ResourceRecords"), "ResourceRecord").map((r) => text(r, "Value")),
      alias: alias ? text(alias, "DNSName") : null,
      setIdentifier: text(node, "SetIdentifier") || null,
    };
  }

  zoneXml(z) {
    return el("HostedZone", el("Id", `/hostedzone/${z.id}`) + el("Name", z.name) + el("CallerReference", "ref") + el("Config", el("PrivateZone", String(z.private))) + el("ResourceRecordSetCount", z.sets.length));
  }

  delegation() {
    return el("DelegationSet", el("NameServers", NS.map((n) => el("NameServer", n)).join("")));
  }

  async fetch(url, init = {}) {
    const u = new URL(url);
    const method = init.method ?? "GET";
    const body = init.body ?? "";
    const ok = (xml, status = 200) => new Response(`<?xml version="1.0"?>\n${xml}`, { status });
    const fail = (status, code, message) => new Response(`<?xml version="1.0"?><ErrorResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Error><Type>Sender</Type><Code>${code}</Code><Message>${esc(message)}</Message></Error><RequestId>r1</RequestId></ErrorResponse>`, { status });
    const batchFail = (message) => new Response(`<?xml version="1.0"?><InvalidChangeBatch xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Messages><Message>${esc(message)}</Message></Messages><RequestId>r2</RequestId></InvalidChangeBatch>`, { status: 400 });

    // The signature: the fake signs the same request again, with the same time.
    const date = init.headers["x-amz-date"];
    const t = new Date(`${date.slice(0, 4)}-${date.slice(4, 6)}-${date.slice(6, 8)}T${date.slice(9, 11)}:${date.slice(11, 13)}:${date.slice(13, 15)}Z`);
    if (!init.headers.authorization.startsWith(`AWS4-HMAC-SHA256 Credential=${KEY.accessKeyId}/`)) return fail(403, "InvalidClientTokenId", "The security token included in the request is invalid.");
    const again = sign({ method, url, headers: body ? { "content-type": init.headers["content-type"] } : {}, body, ...KEY, now: t });
    if (again.authorization !== init.headers.authorization) return fail(403, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.");

    const path = u.pathname.replace("/2013-04-01", "");
    if (method !== "GET") this.requests.push(`${method} ${path}`);
    const xml = body ? parseXml(body) : null;
    let m;
    if (path === "/hostedzonesbyname") {
      const from = u.searchParams.get("dnsname") ?? "";
      const list = [...this.zones.values()].filter((z) => z.name >= from).sort((a, b) => (a.name < b.name ? -1 : 1));
      return ok(el("ListHostedZonesByNameResponse", el("HostedZones", list.map((z) => this.zoneXml(z)).join("")) + el("IsTruncated", "false") + el("MaxItems", u.searchParams.get("maxitems"))));
    }
    if (path === "/hostedzone" && method === "POST") {
      const z = this.addZone(text(xml, "Name").replace(/\.$/, ""));
      return ok(el("CreateHostedZoneResponse", this.zoneXml(z) + el("ChangeInfo", el("Id", "/change/C1") + el("Status", "PENDING")) + this.delegation()), 201);
    }
    if ((m = /^\/hostedzone\/(\w+)$/.exec(path))) {
      const z = this.zones.get(m[1]);
      if (!z) return fail(404, "NoSuchHostedZone", "No hosted zone found");
      if (method === "DELETE") {
        if (z.sets.length > 2) return fail(400, "HostedZoneNotEmpty", "The hosted zone contains resource records that are not SOA or NS records.");
        if (z.keys.length) return fail(400, "KeySigningKeyInUse", "The hosted zone has key-signing keys.");
        this.zones.delete(z.id);
        return ok(el("DeleteHostedZoneResponse", el("ChangeInfo", el("Status", "PENDING"))));
      }
      return ok(el("GetHostedZoneResponse", this.zoneXml(z) + this.delegation()));
    }
    if ((m = /^\/hostedzone\/(\w+)\/rrset\/?$/.exec(path))) {
      const z = this.zones.get(m[1]);
      if (method === "GET") {
        // Two record sets on each page, so that the plugin reads more than one page.
        let start = 0;
        if (u.searchParams.get("name")) start = z.sets.findIndex((s) => s.name === u.searchParams.get("name") && s.type === u.searchParams.get("type"));
        const page = z.sets.slice(start, start + 2);
        const after = z.sets[start + 2];
        return ok(el("ListResourceRecordSetsResponse", el("ResourceRecordSets", page.map((s) => this.setXml(s)).join("")) + el("IsTruncated", String(!!after)) + (after ? el("NextRecordName", esc(after.name)) + el("NextRecordType", after.type) : "") + el("MaxItems", "300")));
      }
      const changes = kids(kid(kid(xml, "ChangeBatch"), "Changes"), "Change");
      const count = changes.reduce((n, c) => n + Math.max(1, kids(kid(kid(c, "ResourceRecordSet"), "ResourceRecords"), "ResourceRecord").length) * (text(c, "Action") === "UPSERT" ? 2 : 1), 0);
      if (count > 1000) return batchFail("The batch has more than 1000 records.");
      const sets = z.sets.map((s) => ({ ...s }));
      for (const c of changes) {
        const s = this.readSet(kid(c, "ResourceRecordSet"));
        const i = sets.findIndex((e) => e.name === s.name && e.type === s.type && (e.setIdentifier ?? null) === s.setIdentifier);
        const action = text(c, "Action");
        if (action === "CREATE" && i >= 0) return batchFail(`[Tried to create resource record set [name='${s.name}', type='${s.type}'] but it already exists]`);
        if (action === "DELETE") {
          if (i < 0 || JSON.stringify(sets[i].values) !== JSON.stringify(s.values) || sets[i].ttl !== s.ttl || (sets[i].alias ?? null) !== s.alias) return batchFail(`[Tried to delete resource record set [name='${s.name}', type='${s.type}'] but the values provided do not match the current values]`);
          sets.splice(i, 1);
        } else if (i >= 0) sets[i] = s;
        else sets.push(s);
      }
      z.sets = sets;
      this.batches.push(changes.map((c) => `${text(c, "Action")} ${text(c, "ResourceRecordSet", "Name")} ${text(c, "ResourceRecordSet", "Type")}`));
      return ok(el("ChangeResourceRecordSetsResponse", el("ChangeInfo", el("Id", "/change/C2") + el("Status", "PENDING"))));
    }
    if ((m = /^\/hostedzone\/(\w+)\/(dnssec|enable-dnssec|disable-dnssec)$/.exec(path))) {
      const z = this.zones.get(m[1]);
      if (m[2] === "enable-dnssec") {
        if (!z.keys.some((k) => k.status === "ACTIVE")) return fail(400, "KeySigningKeyWithActiveStatusNotFound", "No active key-signing key.");
        z.dnssec = "SIGNING";
      }
      if (m[2] === "disable-dnssec") z.dnssec = "NOT_SIGNING";
      if (m[2] !== "dnssec") return ok(el("ChangeInfo", el("Status", "PENDING")));
      const keys = z.keys.map((k) => el("KeySigningKey", el("Name", k.name) + el("KmsArn", k.kms) + el("Flag", "257") + el("SigningAlgorithmType", "13") + el("DigestAlgorithmType", "2") + el("KeyTag", "36381") + el("DigestValue", "5ae0f0c1a9b8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928170f6e5") + el("DSRecord", "36381 13 2 5AE0F0C1A9B8E7D6C5B4A39281706F5E4D3C2B1A09F8E7D6C5B4A3928170F6E5") + el("Status", k.status)));
      return ok(el("GetDNSSECResponse", el("Status", el("ServeSignature", z.dnssec)) + el("KeySigningKeys", keys.join(""))));
    }
    if (path === "/keysigningkey" && method === "POST") {
      const z = this.zones.get(text(xml, "HostedZoneId"));
      z.keys.push({ name: text(xml, "Name"), kms: text(xml, "KeyManagementServiceArn"), status: text(xml, "Status") });
      return ok(el("CreateKeySigningKeyResponse", el("ChangeInfo", el("Status", "PENDING"))), 201);
    }
    if ((m = /^\/keysigningkey\/(\w+)\/(\w+)(\/(activate|deactivate))?$/.exec(path))) {
      const z = this.zones.get(m[1]);
      const k = z.keys.find((key) => key.name === m[2]);
      if (m[4] === "deactivate") {
        if (z.dnssec === "SIGNING") return fail(400, "KeySigningKeyInUse", "The key-signing key is in use.");
        k.status = "INACTIVE";
      } else if (m[4] === "activate") k.status = "ACTIVE";
      else if (method === "DELETE") {
        if (k.status !== "INACTIVE") return fail(400, "InvalidKeySigningKeyStatus", "The key-signing key is not inactive.");
        z.keys = z.keys.filter((key) => key !== k);
      }
      return ok(el("ChangeInfo", el("Status", "PENDING")));
    }
    return fail(404, "NotFound", `No route ${method} ${path}`);
  }
}

const options = (api) => ({ base: "https://route53.test", fetch: api.fetch });
const call = (api, operation, zone, more = {}) => handle({ id: "01req", operation, credentials: { ...KEY, ...more }, zone }, options(api));

const records = [
  { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
  { name: "@", type: "A", ttl: 300, value: "192.0.2.3", priority: null },
  { name: "www", type: "CNAME", ttl: 3600, value: "example.com", priority: null },
  { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
  { name: "@", type: "TXT", ttl: 3600, value: 'v=spf1 include:"x" -all', priority: null },
  { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
  { name: "@", type: "CAA", ttl: 3600, value: '0 issue "letsencrypt.org"', priority: null },
  { name: "*", type: "A", ttl: 300, value: "192.0.2.9", priority: null },
];

test("the signature is the same as in the test suite of AWS Signature Version 4", () => {
  const c = { accessKeyId: "AKIDEXAMPLE", secretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", region: "us-east-1", service: "service", now: new Date("2015-08-30T12:36:00Z") };
  // get-vanilla and get-vanilla-query-order-key-case.
  assert.match(sign({ method: "GET", url: "https://example.amazonaws.com/", ...c }).authorization, /SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31$/);
  assert.match(sign({ method: "GET", url: "https://example.amazonaws.com/?Param2=value2&Param1=value1", ...c }).authorization, /Signature=b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500$/);
});

test("the XML parser reads elements, text and entities", () => {
  const x = parseXml('<?xml version="1.0"?><!-- c --><a:R xmlns:a="x"><B>1 &lt; 2 &amp; &#65;&#x42;</B><C/><D><![CDATA[<raw>]]></D><E><F>f</F></E></a:R>');
  assert.equal(x.name, "R");
  assert.equal(text(x, "B"), "1 < 2 & AB");
  assert.equal(text(x, "D"), "<raw>");
  assert.equal(text(x, "E", "F"), "f");
  assert.ok(kid(x, "C"));
  assert.equal(toXml(kid(x, "B")), "<B>1 &lt; 2 &amp; AB</B>");
  assert.throws(() => parseXml("<a><b></a>"), /not valid/);
  assert.equal(decodeName("\\052.Example.com."), "*.example.com.");
  assert.equal(unquoteTxt('"caf\\303\\251" "\\"x\\""'), 'café"x"');
});

test("verify checks the access key", async () => {
  const api = new FakeRoute53();
  assert.deepEqual(await call(api, "verify", null), { account: null });
  await assert.rejects(call(api, "verify", null, { accessKeyId: "AKIABAD" }), (e) => e instanceof ProviderError && e.status === 422 && /refused the access key/.test(e.message));
  await assert.rejects(call(api, "verify", null, { secretAccessKey: "wrong" }), /refused the access key/);
  await assert.rejects(call(api, "verify", null, { secretAccessKey: "" }), /Enter the access key ID and the secret access key/);
});

test("records gives the record sets in the form of the panel, and shows alias records and routing policies as types that the panel does not have", async () => {
  const api = new FakeRoute53();
  assert.deepEqual(await call(api, "records", { name: "example.com" }), { exists: false, records: [], nameServers: [] });
  api.addZone("example.com", { private: true });
  assert.equal((await call(api, "records", { name: "example.com" })).exists, false);
  const z = api.addZone("example.com");
  z.sets.push(
    { name: "example.com.", type: "A", ttl: 300, values: ["192.0.2.1", "192.0.2.3"] },
    { name: "\\052.example.com.", type: "A", ttl: 300, values: ["192.0.2.9"] },
    { name: "example.com.", type: "MX", ttl: 3600, values: ["10 Mail.Example.net."] },
    { name: "example.com.", type: "TXT", ttl: 3600, values: ['"v=spf1 " "-all"'] },
    { name: "_sip._tcp.example.com.", type: "SRV", ttl: 3600, values: ["10 5 5060 sip.example.com"] },
    { name: "cdn.example.com.", type: "A", alias: "d111111abcdef8.cloudfront.net.", values: [] },
    { name: "eu.example.com.", type: "A", ttl: 60, setIdentifier: "eu-1", values: ["198.51.100.7"] },
  );
  const out = await call(api, "records", { name: "Example.com" });
  assert.deepEqual(out.nameServers, NS);
  assert.deepEqual(out.records, [
    { name: "@", type: "A", ttl: 300, value: "192.0.2.1", priority: null },
    { name: "@", type: "A", ttl: 300, value: "192.0.2.3", priority: null },
    { name: "*", type: "A", ttl: 300, value: "192.0.2.9", priority: null },
    { name: "@", type: "MX", ttl: 3600, value: "mail.example.net", priority: 10 },
    { name: "@", type: "TXT", ttl: 3600, value: "v=spf1 -all", priority: null },
    { name: "_sip._tcp", type: "SRV", ttl: 3600, value: "5 5060 sip.example.com", priority: 10 },
    { name: "cdn", type: "ALIAS", ttl: null, value: "A d111111abcdef8.cloudfront.net", priority: null },
    { name: "eu", type: "POLICY", ttl: 60, value: "A eu-1: 198.51.100.7", priority: null },
  ]);
});

test("sync makes the hosted zone, and then makes the record sets the same as the records of the panel", async () => {
  const api = new FakeRoute53();
  const first = await call(api, "sync", { name: "example.com", serial: 1, dnssec: false, records });
  assert.deepEqual(first, { nameServers: NS, active: true, dnssec: { state: "off", ds: [] }, changes: { added: 8, changed: 0, removed: 0 } });
  const z = api.zoneByName("example.com");
  const set = (name, type) => z.sets.find((s) => s.name === name && s.type === type);
  assert.deepEqual(set("example.com.", "A").values, ["192.0.2.1", "192.0.2.3"]);
  assert.deepEqual(set("example.com.", "MX").values, ["10 mail.example.net."]);
  assert.deepEqual(set("example.com.", "TXT").values, ['"v=spf1 include:\\"x\\" -all"']);
  assert.deepEqual(set("_sip._tcp.example.com.", "SRV").values, ["10 5 5060 sip.example.com."]);
  assert.deepEqual(set("*.example.com.", "A").values, ["192.0.2.9"]);

  // The same records again: no change batch. Route 53 gives the wildcard name as \052.
  set("*.example.com.", "A").name = "\\052.example.com.";
  api.requests = [];
  const again = await call(api, "sync", { name: "example.com", serial: 2, dnssec: false, records });
  assert.deepEqual(again.changes, { added: 0, changed: 0, removed: 0 });
  assert.deepEqual(api.requests, []);

  // Someone adds record sets in Route 53. The panel changes one address of @, removes the CNAME
  // record and changes the TTL of the TXT record. The alias record and the PTR record stay.
  z.sets.push({ name: "old.example.com.", type: "TXT", ttl: 300, values: ['"left over"'] });
  z.sets.push({ name: "1.example.com.", type: "PTR", ttl: 300, values: ["host.example.com."] });
  z.sets.push({ name: "cdn.example.com.", type: "A", alias: "d111111abcdef8.cloudfront.net.", ttl: null, values: [] });
  api.batches = [];
  const changed = records.filter((r) => r.type !== "CNAME").map((r) => (r.value === "192.0.2.3" ? { ...r, value: "192.0.2.2" } : r.type === "TXT" ? { ...r, ttl: 600 } : r));
  const third = await call(api, "sync", { name: "example.com", serial: 3, dnssec: false, records: changed });
  assert.deepEqual(third.changes, { added: 0, changed: 2, removed: 2 });
  assert.deepEqual(api.batches, [["DELETE www.example.com. CNAME", "DELETE old.example.com. TXT", "UPSERT example.com. A", "UPSERT example.com. TXT"]]);
  assert.equal(set("example.com.", "TXT").ttl, 600);
  assert.ok(set("1.example.com.", "PTR"));
  assert.ok(set("cdn.example.com.", "A"));
  assert.equal(set("example.com.", "NS").values.length, 4);

  // A record of the panel with the name and type of the alias record.
  await assert.rejects(call(api, "sync", { name: "example.com", dnssec: false, records: [...changed, { name: "cdn", type: "A", ttl: 300, value: "192.0.2.5", priority: null }] }),
    (e) => e.status === 422 && /cdn A in Route 53 is an alias/.test(e.message));
});

test("a change of more than 1000 records goes in more than one batch", async () => {
  const api = new FakeRoute53();
  const many = Array.from({ length: 1200 }, (_, i) => ({ name: `h${i}`, type: "A", ttl: 300, value: `192.0.2.${i % 250}`, priority: null }));
  const out = await call(api, "sync", { name: "example.com", dnssec: false, records: many });
  assert.equal(out.changes.added, 1200);
  assert.deepEqual(api.batches.map((b) => b.length), [1000, 200]);
  // An UPSERT counts two times, and the values have a limit of 32000 characters.
  const c = (action, count, chars = 10) => ({ action, count, chars });
  assert.deepEqual(batches([c("UPSERT", 400), c("UPSERT", 200), c("CREATE", 1)]).map((b) => b.length), [1, 2]);
  assert.deepEqual(batches([c("CREATE", 1, 20000), c("CREATE", 1, 20000)]).map((b) => b.length), [1, 1]);
});

test("sync turns DNSSEC on with a key-signing key in KMS, and off again", async () => {
  const api = new FakeRoute53();
  api.addZone("example.com");
  const zone = { name: "example.com", dnssec: true, records };
  await assert.rejects(call(api, "sync", zone), /Enter the ARN of a KMS key/);
  await assert.rejects(call(api, "sync", zone, { kmsKeyArn: "arn:aws:kms:eu-west-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab" }), /us-east-1/);
  const kmsKeyArn = "arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab";
  api.requests = [];
  const on = await call(api, "sync", zone, { kmsKeyArn });
  assert.deepEqual(on.dnssec, { state: "active", ds: [{ keyTag: 36381, algorithm: 13, digestType: 2, digest: "5AE0F0C1A9B8E7D6C5B4A39281706F5E4D3C2B1A09F8E7D6C5B4A3928170F6E5" }] });
  const z = api.zoneByName("example.com");
  assert.equal(z.dnssec, "SIGNING");
  assert.deepEqual(z.keys, [{ name: "cloudfactory", kms: kmsKeyArn, status: "ACTIVE" }]);
  assert.deepEqual(api.requests.filter((r) => !r.includes("/rrset")), ["POST /keysigningkey", `POST /hostedzone/${z.id}/enable-dnssec`]);

  api.requests = [];
  assert.equal((await call(api, "sync", zone, { kmsKeyArn })).dnssec.state, "active");
  assert.deepEqual(api.requests, []);

  const off = await call(api, "sync", { ...zone, dnssec: false }, { kmsKeyArn });
  assert.deepEqual(off.dnssec, { state: "off", ds: [] });
  assert.equal(z.dnssec, "NOT_SIGNING");
  assert.deepEqual(z.keys, []);
});

test("remove removes all the record sets and the hosted zone, and a zone that is not there is not an error", async () => {
  const api = new FakeRoute53();
  await call(api, "sync", { name: "example.com", dnssec: true, records }, { kmsKeyArn: "arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab" });
  const z = api.zoneByName("example.com");
  z.sets.push({ name: "cdn.example.com.", type: "A", alias: "d111111abcdef8.cloudfront.net.", ttl: null, values: [] });
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
  assert.equal(api.zones.size, 0);
  assert.deepEqual(await call(api, "remove", { name: "example.com" }), {});
});

test("two public hosted zones with the same name are an error", async () => {
  const api = new FakeRoute53();
  api.addZone("example.com");
  api.addZone("example.com");
  await assert.rejects(call(api, "sync", { name: "example.com", records }), (e) => e.status === 422 && /2 public hosted zones/.test(e.message));
});

test("the server refuses a request with no correct signature, and answers a signed request", async () => {
  const api = new FakeRoute53();
  const app = server({ secret: "whsec_test", ...options(api) }).listen(0);
  await new Promise((resolve) => app.once("listening", resolve));
  const url = `http://127.0.0.1:${app.address().port}/dns/route53`;
  const send = (payload, key = "whsec_test") => {
    const body = JSON.stringify(payload);
    const t = Math.floor(Date.now() / 1000);
    const v1 = createHmac("sha256", key).update(`${t}.${body}`).digest("hex");
    return fetch(url, { method: "POST", body, headers: { "Content-Type": "application/json", "Cloudfactory-Signature": `t=${t},v1=${v1}` } });
  };
  try {
    const request = { id: "01a", contract: "cf-dns-provider/1", provider: "route53", operation: "verify", credentials: KEY };
    assert.equal((await send(request, "wrong")).status, 401);
    const r = await send(request);
    assert.equal(r.status, 200);
    assert.deepEqual(await r.json(), { account: null });
    assert.equal((await send(request)).status, 409);
    const bad = await send({ ...request, id: "01b", credentials: { ...KEY, accessKeyId: "AKIABAD" } });
    assert.equal(bad.status, 422);
    assert.match((await bad.json()).error, /Route 53 refused/);
  } finally {
    app.close();
  }
});
