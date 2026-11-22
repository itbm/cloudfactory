# DigitalOcean DNS plugin

This plugin sends the DNS zones of the panel to DigitalOcean. The panel keeps the records.
After each change, the panel sends the full zone to the plugin, and the plugin makes the domain
at DigitalOcean the same. The plugin keeps no data.

The plugin uses the contract `cf-dns-provider/1`. Refer to
[docs/plugins.md](../../docs/plugins.md#dns-providers).

| File | Function |
| ---- | -------- |
| `plugin.json` | The manifest. It has the provider `digitalocean` and its credentials. |
| `server.mjs` | The server. It checks the signature of each request and refuses a request that it received before. |
| `digitalocean.mjs` | The operations `verify`, `records`, `sync` and `remove` with the DigitalOcean API. |
| `test/` | The tests. Run `node --test test/*.test.mjs`. |

The plugin needs Node.js 22. It has no dependencies.

## What the plugin changes at DigitalOcean

- The plugin changes only the record types of the panel: A, AAAA, CNAME, MX, TXT, SRV, CAA
  and NS. Records of other types do not change.
- The SOA record and the NS records of the domain itself do not change. DigitalOcean keeps
  them. The name servers are `ns1.digitalocean.com`, `ns2.digitalocean.com` and
  `ns3.digitalocean.com`.
- When the domain is not at DigitalOcean, the plugin adds it.
- The plugin sends one request for each changed record, with 4 requests at the same time. A
  sync stops after 45 seconds, because the panel waits 60 seconds for the answer. Then the zone
  has the status **Failed**, and the next sync continues with the changes that are left.
- DigitalOcean has no DNSSEC. Thus a zone with DNSSEC cannot move to DigitalOcean.

## Make an API token

1. In the DigitalOcean control panel, go to **API > Tokens**, and select **Generate New Token**.
2. Select **Custom Scopes**.
3. Give the scopes `domain:read`, `domain:create`, `domain:update`, `domain:delete` and
   `account:read`.
4. Select **Generate Token**.

## Run the plugin

1. Sign the manifest with your key:

   ```sh
   cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
   ```

2. In the panel, add `cosign.pub` in **Plugins > Trusted keys**.
3. Install the plugin with the address of the server. Put the server behind HTTPS.

   ```sh
   cf plugins install --manifest plugin.json --address https://dns-digitalocean.example.net
   ```

4. Start the server with the webhook secret from the install:

   ```sh
   CF_WEBHOOK_SECRET=whsec_... PORT=8080 node server.mjs
   ```

| Variable | Value |
| -------- | ----- |
| `CF_WEBHOOK_SECRET` | The webhook secret from the install. Necessary. |
| `PORT` | The port of the server. The default is 8080. |
| `DIGITALOCEAN_API` | The address of the DigitalOcean API. For tests only. |

To run the plugin in the panel, use `Containerfile`. Refer to
[Run the plugin in the panel](../dns-cloudflare/README.md#run-the-plugin-in-the-panel) in the
Cloudflare DNS plugin. The steps are the same.

## Security

- The plugin gets the API token in each request from the panel. It does not keep the token
  and does not write it to the log.
- The plugin refuses a request with a signature that is not correct, or that is older than 5
  minutes. It also refuses a request with an ID that it received before.
- Give the token only the scopes of [Make an API token](#make-an-api-token).
