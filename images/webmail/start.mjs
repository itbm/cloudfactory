// The start script of the webmail container. It starts the webmail (server.js) and installs the
// PGP plugin that is in the image. The plugin is installed through the admin dashboard of the
// webmail, on the address of the container. The edge proxy refuses the paths of the dashboard,
// so this script is the only user of the dashboard.
//
// The script does not stop the webmail when the installation fails: the webmail then operates
// without the plugin, and the log tells why. The next start tries again.
// SPDX-License-Identifier: AGPL-3.0-only
import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";

const PLUGIN_ID = "pgp-true-end-to-end";
const PLUGIN_FILE = "/opt/cf-webmail/plugins/pgp-true-end-to-end.zip";
// The SHA-256 hash of the code of the plugin in the image. The webmail keeps the same hash of
// the installed code (bundleHash): a different hash is a different plugin.
const CODE_HASH_FILE = "/opt/cf-webmail/plugins/pgp-true-end-to-end.code.sha256";
const BASE = `http://127.0.0.1:${process.env.PORT || 3000}`;

const server = spawn(process.execPath, ["server.js"], { stdio: "inherit" });
for (const signal of ["SIGTERM", "SIGINT"]) process.on(signal, () => server.kill(signal));
server.on("exit", (code, signal) => process.exit(code ?? (signal ? 1 : 0)));

const log = (message) => console.log(`cf-webmail: ${message}`);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// The server answers. The admin dashboard does not need the mail server, so the install does
// not wait for it: /api/health/ready also checks the connection to the mail server.
async function ready() {
  for (let i = 0; i < 120; i++) {
    try {
      const res = await fetch(`${BASE}/api/health/live`);
      if (res.ok) return;
    } catch {
      // The server is not listening yet.
    }
    await sleep(1000);
  }
  throw new Error("the webmail did not answer in 2 minutes");
}

async function install() {
  const password = process.env.ADMIN_PASSWORD;
  if (!password) {
    log("no admin password: the PGP plugin is not installed");
    return;
  }
  await ready();
  const login = await fetch(`${BASE}/api/admin/auth`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ password }),
  });
  if (!login.ok) throw new Error(`sign-in to the admin dashboard: status ${login.status}`);
  // The session cookies of the dashboard, without their attributes.
  const cookie = login.headers.getSetCookie().map((c) => c.split(";")[0]).join("; ");
  const headers = { cookie };

  // The plugins of the admin operate; the users cannot upload plugins of their own.
  const policyRes = await fetch(`${BASE}/api/admin/policy`, { headers });
  if (!policyRes.ok) throw new Error(`the policy of the webmail: status ${policyRes.status}`);
  const policy = await policyRes.json();
  const features = policy.features ?? {};
  if (features.pluginsEnabled !== true || features.pluginsUploadEnabled !== false) {
    policy.features = { ...features, pluginsEnabled: true, pluginsUploadEnabled: false };
    const res = await fetch(`${BASE}/api/admin/policy`, {
      method: "PUT",
      headers: { ...headers, "content-type": "application/json" },
      body: JSON.stringify(policy),
    });
    if (!res.ok) throw new Error(`change of the policy of the webmail: status ${res.status}: ${await res.text()}`);
    log("turned on the plugins of the admin");
  }

  const zip = await readFile(PLUGIN_FILE);
  const list = await fetch(`${BASE}/api/admin/plugins`, { headers });
  if (!list.ok) throw new Error(`list of the plugins: status ${list.status}`);
  const body = await list.json();
  const plugins = Array.isArray(body) ? body : body.plugins ?? [];
  const installed = plugins.find((p) => p.id === PLUGIN_ID);
  // The image sets the version of the plugin that it has.
  const version = process.env.CF_PGP_PLUGIN_VERSION;
  const codeHash = (await readFile(CODE_HASH_FILE, "utf8")).trim();
  const current = installed && installed.version === version && installed.bundleHash === codeHash;
  if (current && installed.enabled) {
    log(`the PGP plugin ${installed.version} is installed`);
    return;
  }
  if (!current) {
    const form = new FormData();
    form.append("file", new Blob([zip], { type: "application/zip" }), `${PLUGIN_ID}.zip`);
    const res = await fetch(`${BASE}/api/admin/plugins`, { method: "POST", headers, body: form });
    if (!res.ok) throw new Error(`installation of the PGP plugin: status ${res.status}: ${await res.text()}`);
  }
  // On for each user. A user can turn it off in the settings of the webmail.
  const res = await fetch(`${BASE}/api/admin/plugins`, {
    method: "PATCH",
    headers: { ...headers, "content-type": "application/json" },
    body: JSON.stringify({ id: PLUGIN_ID, enabled: true }),
  });
  if (!res.ok) throw new Error(`activation of the PGP plugin: status ${res.status}`);
  log(`installed the PGP plugin ${version} (code sha256 ${codeHash})`);
}

install().catch((error) => log(`the PGP plugin is not installed: ${error.message}`));
