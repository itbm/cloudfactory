// An example plugin server for Cloudfactory. It has no dependencies. Run it with Node.js 22:
//
//   CF_WEBHOOK_SECRET=whsec_... PORT=8090 node server.mjs
//
// It serves the page of plugin.json, receives the events of the panel, runs the action
// "Check uptime now", and answers the before hook "website.removing". Refer to docs/plugins.md.
import { createHmac, timingSafeEqual } from "node:crypto";
import { readFileSync } from "node:fs";
import { createServer } from "node:http";

const secret = process.env.CF_WEBHOOK_SECRET ?? "";
const page = readFileSync(new URL("./page.html", import.meta.url));
const seen = new Set();
const MAX_BODY = 65536;
const MAX_SEEN = 10000;

/** Checks the header "Cloudfactory-Signature": t=<Unix time>,v1=<HMAC-SHA256 of "<t>.<body>">. */
function verify(header, body) {
  const parts = Object.fromEntries(String(header ?? "").split(",").map((p) => p.split("=")));
  const t = Number(parts.t);
  if (!secret || !parts.v1 || !Number.isFinite(t) || Math.abs(Date.now() / 1000 - t) > 300) return false;
  const want = Buffer.from(createHmac("sha256", secret).update(`${t}.${body}`).digest("hex"));
  const got = Buffer.from(parts.v1);
  return want.length === got.length && timingSafeEqual(want, got);
}

/** Reads the body, and answers 401 when the signature is not correct. */
function signedBody(req, res, then) {
  const chunks = [];
  let size = 0;
  req.on("data", (c) => {
    size += c.length;
    // The panel sends small bodies. A larger body is not from the panel.
    if (size > MAX_BODY) return req.destroy();
    chunks.push(c);
  });
  req.on("end", async () => {
    const body = Buffer.concat(chunks).toString("utf8");
    if (!verify(req.headers["cloudfactory-signature"], body)) {
      res.writeHead(401);
      return res.end();
    }
    // An error in the handler must not stop the program.
    try {
      await then(JSON.parse(body), body);
    } catch (error) {
      console.error(error);
      if (!res.headersSent) res.writeHead(500);
      res.end();
    }
  });
}

function json(res, value) {
  res.writeHead(200, { "Content-Type": "application/json" });
  res.end(JSON.stringify(value));
}

createServer((req, res) => {
  if (req.method === "POST" && req.url === "/actions/check") {
    // An action. The request has a token of the user for 15 minutes. Use it to call the API with
    // the rights of that user. Answer in 10 seconds.
    return signedBody(req, res, async (action) => {
      const r = await fetch(`${action.apiBase}/websites/${action.target.id}`, { headers: { Authorization: `Bearer ${action.token}`, Accept: "application/json" } });
      if (!r.ok) return json(res, { message: `The panel answered ${r.status}. The website was not checked.` });
      const site = (await r.json()).data;
      const started = Date.now();
      const answer = await fetch(`https://${site.name}/`, { method: "HEAD", signal: AbortSignal.timeout(5000) }).catch(() => null);
      json(res, { message: answer ? `${site.name} answered ${answer.status} in ${Date.now() - started} ms.` : `${site.name} did not answer in 5 seconds.` });
    });
  }
  if (req.method === "POST" && req.url === "/hooks/before") {
    // A before hook. Answer {"allow": true}, or {"allow": false, "message": "..."}, in 5 seconds.
    return signedBody(req, res, (hook) => {
      if (hook.hook === "website.removing" && process.env.PROTECTED_WEBSITES?.split(",").includes(hook.data.name)) {
        return json(res, { allow: false, message: "This website is on the list of protected websites of the uptime monitor." });
      }
      json(res, { allow: true });
    });
  }
  if (req.method === "GET" && req.url === "/panel/status") {
    // Only the web panel can show the page in a frame. Put the address of your panel here.
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": `frame-ancestors ${process.env.CF_PANEL ?? "'none'"}` });
    return res.end(page);
  }
  if (req.method === "POST" && req.url === "/hooks/cloudfactory") {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const body = Buffer.concat(chunks).toString("utf8");
      if (!verify(req.headers["cloudfactory-signature"], body)) {
        res.writeHead(401);
        return res.end();
      }
      // Answer at once. Do slow work after the answer.
      res.writeHead(204);
      res.end();
      const id = req.headers["cloudfactory-delivery"];
      if (seen.has(id)) return; // The panel sent this event before.
      if (seen.size >= MAX_SEEN) seen.clear();
      seen.add(id);
      const event = JSON.parse(body);
      console.log(`${event.createdAt} ${event.event}`, JSON.stringify(event.data));
    });
    return;
  }
  res.writeHead(404);
  res.end();
}).listen(Number(process.env.PORT ?? 8090));
