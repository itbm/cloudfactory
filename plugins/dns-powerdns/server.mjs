// The PowerDNS plugin for Cloudfactory. It has no dependencies. Run it with Node.js 22:
//
//   CF_WEBHOOK_SECRET=whsec_... PORT=8080 node server.mjs
//
// The panel sends each operation of the contract cf-dns-provider/1 to /dns/powerdns, with a
// signature. The plugin then calls the API of PowerDNS with the address and the API key of the
// DNS account.
// The plugin keeps no data. Refer to README.md and to docs/plugins.md#dns-providers.
import { createHmac, timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";
import { allowList, handle, ProviderError } from "./powerdns.mjs";

const secret = process.env.CF_WEBHOOK_SECRET ?? "";
// The hosts and networks in a private network that the plugin can connect to, with commas.
const allow = allowList(process.env.POWERDNS_ALLOW ?? "");
// The kind of a new zone: Native (the database of PowerDNS copies it) or Master (AXFR copies it).
const kind = process.env.POWERDNS_KIND === "Master" ? "Master" : "Native";
// A zone of the panel has up to 1000 records. Each TXT record can have 2048 characters.
const MAX_BODY = 4 * 1024 * 1024;
const MAX_SEEN = 10000;
const seen = new Set();

/** Checks the header "Cloudfactory-Signature": t=<Unix time>,v1=<HMAC-SHA256 of "<t>.<body>">. */
export function verify(key, header, body, now = Date.now()) {
  const parts = Object.fromEntries(String(header ?? "").split(",").map((p) => p.split("=")));
  const t = Number(parts.t);
  if (!key || !parts.v1 || !Number.isFinite(t) || Math.abs(now / 1000 - t) > 300) return false;
  const want = Buffer.from(createHmac("sha256", key).update(`${t}.${body}`).digest("hex"));
  const got = Buffer.from(parts.v1);
  return want.length === got.length && timingSafeEqual(want, got);
}

function json(res, status, value) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(value));
}

export function server(options = {}) {
  const key = options.secret ?? secret;
  return createServer((req, res) => {
    if (req.method === "GET" && req.url === "/health") return json(res, 200, { ok: true });
    if (req.method !== "POST" || req.url !== "/dns/powerdns") {
      res.writeHead(404);
      return res.end();
    }
    const chunks = [];
    let size = 0;
    req.on("data", (c) => {
      size += c.length;
      if (size > MAX_BODY) return req.destroy();
      chunks.push(c);
    });
    req.on("end", async () => {
      const body = Buffer.concat(chunks).toString("utf8");
      if (!verify(key, req.headers["cloudfactory-signature"], body)) return json(res, 401, { error: "The signature is not correct." });
      let request;
      try {
        request = JSON.parse(body);
      } catch {
        return json(res, 400, { error: "The body is not JSON." });
      }
      if (request.contract !== "cf-dns-provider/1") return json(res, 400, { error: `The plugin does not know the contract ${request.contract}.` });
      // The panel sends each request one time. A copy of a request is not from the panel.
      if (seen.has(request.id)) return json(res, 409, { error: "The plugin got this request before." });
      if (seen.size >= MAX_SEEN) seen.clear();
      seen.add(request.id);
      try {
        json(res, 200, await handle(request, { allow: options.allow ?? allow, kind: options.kind ?? kind, fetch: options.fetch }));
      } catch (error) {
        if (!(error instanceof ProviderError)) console.error(error);
        json(res, error instanceof ProviderError ? error.status : 500, { error: error instanceof ProviderError ? error.message : "The plugin had an error. Look at its log." });
      }
    });
  });
}

if (import.meta.url === `file://${process.argv[1]}`) {
  if (!secret) {
    console.error("Set CF_WEBHOOK_SECRET to the webhook secret that the panel gave at the install.");
    process.exit(1);
  }
  server().listen(Number(process.env.PORT ?? 8080));
}
