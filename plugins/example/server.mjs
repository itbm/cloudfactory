// An example plugin server for Cloudfactory. It has no dependencies. Run it with Node.js 22:
//
//   CF_WEBHOOK_SECRET=whsec_... PORT=8090 node server.mjs
//
// It serves the page of plugin.json, and receives the events of the panel. Refer to
// docs/plugins.md.
import { createHmac, timingSafeEqual } from "node:crypto";
import { readFileSync } from "node:fs";
import { createServer } from "node:http";

const secret = process.env.CF_WEBHOOK_SECRET ?? "";
const page = readFileSync(new URL("./page.html", import.meta.url));
const seen = new Set();

/** Checks the header "Cloudfactory-Signature": t=<Unix time>,v1=<HMAC-SHA256 of "<t>.<body>">. */
function verify(header, body) {
  const parts = Object.fromEntries(String(header ?? "").split(",").map((p) => p.split("=")));
  const t = Number(parts.t);
  if (!secret || !parts.v1 || !Number.isFinite(t) || Math.abs(Date.now() / 1000 - t) > 300) return false;
  const want = Buffer.from(createHmac("sha256", secret).update(`${t}.${body}`).digest("hex"));
  const got = Buffer.from(parts.v1);
  return want.length === got.length && timingSafeEqual(want, got);
}

createServer((req, res) => {
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
      seen.add(id);
      const event = JSON.parse(body);
      console.log(`${event.createdAt} ${event.event}`, JSON.stringify(event.data));
    });
    return;
  }
  res.writeHead(404);
  res.end();
}).listen(Number(process.env.PORT ?? 8090));
