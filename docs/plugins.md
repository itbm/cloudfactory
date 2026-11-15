# Plugins

A plugin adds a function to the panel. A plugin is a program that you run outside the panel,
on a host that you select. The panel never runs the code of a plugin.

A plugin can do these things:

- Use the public API with its own token. The token has only the scopes that the administrator
  approves.
- Receive the events of the panel with signed webhooks.
- Add pages to the web panel. A page opens in a frame, with the origin of the plugin.
- Add commands to the `cf` CLI. `cf NAME` runs the program `cf-NAME`.

A plugin cannot do these things:

- Get root access to a server.
- Connect to an agent or to the root helper of an agent.
- Read the database of the panel, the secrets of the panel or the session of a user.
- Get the scopes `tokens:read`, `tokens:write`, `users:write`, `plugins:read` or
  `plugins:write`. With these scopes, a plugin could make tokens, take a user account or
  change its own access.

## Parts of a plugin

| Part | What it is |
| ---- | ---------- |
| `plugin.json` | The manifest. It gives the name, the scopes, the events, the pages and the commands. |
| `plugin.json.sig` | The signature of the manifest. The publisher makes it with an ECDSA P-256 key. |
| A server | Your program. It receives the webhooks, serves the pages and calls the API. |
| `cf-NAME` | Optional. A program for each command of the plugin. |

[`plugins/example`](../plugins/example) has a complete example.

## The manifest

```json
{
  "schema": 1,
  "slug": "uptime",
  "name": "Uptime monitor",
  "version": "1.4.0",
  "publisher": "Example Plugins Ltd",
  "description": "Checks each website every minute.",
  "scopes": ["websites:read", "audit:read"],
  "events": ["audit.recorded", "job.finished"],
  "webhook": "/hooks/cloudfactory",
  "pages": [
    { "id": "status", "title": "Uptime", "path": "/panel/status", "roles": ["administrator", "reseller", "owner"] }
  ],
  "commands": [
    { "name": "uptime", "description": "Shows the uptime of each website." }
  ]
}
```

| Key | Rule |
| --- | ---- |
| `schema` | Always `1`. |
| `slug` | Lower-case letters, digits and `-`. 1 to 40 characters. It must be unique on the panel. |
| `version` | For example `1.4.0`. |
| `scopes` | The scopes of the token. Use only the scopes that the plugin needs. |
| `events` | `audit.recorded`, `job.finished`, or both. |
| `webhook` | The path of the webhook. Necessary when `events` is not empty. |
| `pages` | Up to 10 pages. `roles` gives the roles that see the page in the menu. |
| `commands` | Up to 20 commands. The panel shows them only as information. |

Each path starts with `/`. A path cannot have `..`, a query or a fragment. The paths are
relative to the address of the plugin. The administrator gives the address at the install,
because the publisher does not know where you run the plugin.

The manifest can have up to 32 KB. The panel keeps only the keys of this table.

## Sign a manifest

The publisher signs the exact bytes of `plugin.json`. Do not change the file after you sign
it. A change of one byte makes the signature incorrect.

To sign with cosign:

```sh
cosign generate-key-pair          # one time: makes cosign.key and cosign.pub
cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
```

To sign with OpenSSL:

```sh
openssl ecparam -name prime256v1 -genkey -noout -out plugin.key    # one time
openssl ec -in plugin.key -pubout -out plugin.pub                   # one time
openssl dgst -sha256 -sign plugin.key plugin.json | base64 -w0 > plugin.json.sig
```

Give `plugin.pub` (or `cosign.pub`) to the administrators who install your plugins.

## Install a plugin

Only an administrator can install a plugin. Each step that gives access needs the password
of the administrator, and the TOTP code when TOTP is on.

1. Add the public key of the publisher: **Plugins > Trusted keys > Add key**, or
   `cf plugins keys add --name NAME --file cosign.pub`.
2. Start the server of the plugin on its own host name, for example
   `https://uptime.example.net`. Do not use the host name of the panel. The panel refuses it.
3. Select **Plugins > Install plugin**. Paste the manifest and the signature. Select
   **Check the manifest**.
4. Read the scopes, the events and the pages. Give the address of the plugin.
5. Type your password, and the TOTP code when TOTP is on. Select **Install plugin**.
6. Copy the token and the webhook secret. The panel shows them one time only. Give them to
   the plugin, for example as `CF_TOKEN` and `CF_WEBHOOK_SECRET`.

With the CLI:

```sh
cf plugins install --manifest plugin.json --address https://uptime.example.net
```

The CLI reads the signature from `plugin.json.sig`. It reads your password from `CF_PASSWORD`
or stdin.

### Manage a plugin

| Action | Web panel | CLI |
| ------ | --------- | --- |
| Turn off | The switch on the card of the plugin | `cf plugins disable PLUGIN` |
| Send a test event | **Send a test event** | `cf plugins test PLUGIN` |
| See the deliveries | **Show deliveries** | `cf plugins deliveries PLUGIN` |
| Install a new version | **Install a new version** | `cf plugins upgrade PLUGIN` |
| Make a new token and secret | **Make new credentials** | `cf plugins credentials PLUGIN` |
| Remove | **Remove plugin** | `cf plugins remove PLUGIN` |

A plugin that is off has no access. Its token and its page tokens get status 403, and the
panel sends it no events. A new version keeps the token, and the token gets the scopes of the
new manifest. The review shows the scopes that the new version adds.

The token of a plugin expires after one year. Make new credentials before that date.

### The developer mode

The developer mode is for the development of a plugin. Do not turn it on for a production
panel. In the developer mode, the panel accepts:

- a manifest with no signature,
- an address with `http://`,
- a webhook on the loopback interface (`127.0.0.1` or `::1`).

## The token of a plugin

The panel makes one user for each plugin. The user has the name `Plugin: NAME` and the role
"administrator". Nobody can sign in as this user. The user is not in the list of users.

The token belongs to this user. The scopes of the token are the only limit. Thus a plugin
with `websites:read` can read the websites of all customers.

The audit log shows each change of a plugin with the actor `Plugin: NAME (token Plugin)`.

## Events

The panel sends each event to the webhook of each plugin that is on and wants the event.

| Event | When | Data |
| ----- | ---- | ---- |
| `audit.recorded` | Each change in the audit log | `action`, `target`, `actor`, `customerId`, `pluginId`, `at` |
| `job.finished` | A job of an agent succeeded or failed | `id`, `action`, `title`, `state`, `error`, `serverId`, `websiteId`, `customerId` |
| `plugin.test` | An administrator sends a test event | `message`, `by` |

`pluginId` is the ID of the plugin that made the change, or `null`. Use it to ignore your own
changes. If you do not ignore them, a plugin that makes a change for each event can make a
loop.

### The request

The panel sends `POST` to the webhook with a JSON body:

```json
{
  "id": "01jd2m4w3h6ya8c0vvv1q8x0zr",
  "event": "job.finished",
  "createdAt": "2026-11-15T10:42:07+00:00",
  "data": { "title": "Create website shop.example.com", "state": "succeeded" }
}
```

The headers:

| Header | Value |
| ------ | ----- |
| `Cloudfactory-Event` | The name of the event |
| `Cloudfactory-Delivery` | The ID of the delivery. It is the same for each attempt. |
| `Cloudfactory-Signature` | `t=<Unix time>,v1=<HMAC-SHA256>` |

### Check the signature

Do these steps for each request. Refuse the request if a step fails.

1. Read `t` and `v1` from `Cloudfactory-Signature`.
2. Make sure that `t` is not older than 5 minutes.
3. Calculate HMAC-SHA256 of `<t>.<body>` with the webhook secret. Use the body as bytes,
   before you parse it.
4. Compare the result with `v1` in constant time.

```js
import { createHmac, timingSafeEqual } from "node:crypto";

function verify(secret, header, body) {
  const { t, v1 } = Object.fromEntries(header.split(",").map((p) => p.split("=")));
  if (Math.abs(Date.now() / 1000 - Number(t)) > 300) return false;
  const want = createHmac("sha256", secret).update(`${t}.${body}`).digest("hex");
  return want.length === v1.length && timingSafeEqual(Buffer.from(want), Buffer.from(v1));
}
```

### Answers and attempts

Answer with a status from 200 to 299 in 10 seconds. Do slow work after the answer.

The panel does not follow a redirect. When a delivery fails, the panel tries again after 1
minute, 5 minutes, 30 minutes, 2 hours and 6 hours. After 6 attempts, the delivery is
"failed". An administrator can send it again. Use `Cloudfactory-Delivery` to ignore an event
that you received before.

### Addresses that the panel refuses

The panel resolves the host name of the webhook one time, and connects to the address that it
checked. It refuses these addresses:

- the loopback interface, except in the developer mode,
- link-local addresses, which include the private service address `169.254.77.1` and the
  cloud metadata service,
- multicast and unspecified addresses.

Private addresses, for example `10.0.0.0/8`, are permitted. Thus you can run a plugin in a
private network.

## Pages

A page opens in a frame in the web panel. The frame has the origin of the plugin, so the
script of the page cannot read the panel, its storage or the session of the user. The frame
cannot open a different page in the top window.

The web panel gives the page a page token with `postMessage`:

1. The page sends `{ "type": "cloudfactory:ready", "version": 1 }` to `window.parent`.
2. The web panel asks the API for a page token
   (`POST /v1/plugin-pages/{id}/{pageId}/session`). It sends the token only to the origin of
   the page.
3. The page receives this message:

   ```json
   {
     "type": "cloudfactory:session",
     "version": 1,
     "token": "…",
     "expiresAt": "2026-11-15T10:57:07+00:00",
     "scopes": ["audit:read", "websites:read"],
     "user": { "id": 3, "name": "Olivia Owner", "role": "owner", "customerId": "…" },
     "page": { "pluginId": "…", "id": "status", "title": "Uptime", "url": "…", "origin": "…" },
     "apiBase": "https://panel.example.com/api/v1",
     "panelOrigin": "https://panel.example.com"
   }
   ```

4. The page calls the API with `Authorization: Bearer <token>`.

The page token:

- belongs to the user who opened the page, so the rights of that user apply. An owner sees
  only the websites of the owner.
- has the scopes of the plugin.
- expires after 15 minutes. The web panel sends a new one each 12 minutes.
- cannot make a different token or a second page token.

Check that a message comes from `window.parent`. Keep the page token in memory only.

To set the height of the frame, send `{ "type": "cloudfactory:resize", "height": N }`.

The API accepts calls from the browser (CORS) from the origin of each plugin that is on and
has pages. Your page can also send the page token to the server of the plugin, which then
calls the API.

## Commands

`cf NAME ARG...` runs the program `cf-NAME` from the `PATH` when `cf` has no command `NAME`.
A plugin cannot replace a command of `cf`. The program gets the arguments and these
environment variables:

| Variable | Value |
| -------- | ----- |
| `CF_PANEL` | The address of the panel of your sign-in |
| `CF_TOKEN` | Your token |

The program runs as your user, with your token. Install only the programs of publishers that
you trust.

## Security

Refer to [security.md](security.md#plugins).
