# Cloudflare DNS plugin

This plugin sends the DNS zones of the panel to Cloudflare. The panel keeps the records. After
each change, the panel sends the full zone to the plugin, and the plugin makes the zone at
Cloudflare the same. The plugin keeps no data.

The plugin uses the contract `cf-dns-provider/1`. Refer to
[docs/plugins.md](../../docs/plugins.md#dns-providers).

| File | Function |
| ---- | -------- |
| `plugin.json` | The manifest. It has the provider `cloudflare` and its credentials. |
| `server.mjs` | The server. It checks the signature of each request and refuses a request that it received before. |
| `cloudflare.mjs` | The operations `verify`, `records`, `sync` and `remove` with the Cloudflare API. |
| `test/` | The tests. Run `node --test test/*.test.mjs`. |

The plugin needs Node.js 22. It has no dependencies.

## What the plugin changes at Cloudflare

- The plugin changes only the record types of the panel: A, AAAA, CNAME, MX, TXT, SRV, CAA
  and NS. Records of other types, for example HTTPS, do not change.
- The NS records of the zone itself do not change. Cloudflare keeps them.
- A record that stays the same keeps its proxy setting. A changed record of the same name and
  type also keeps it.
- The plugin sends the changes with the batch endpoint of Cloudflare, in parts of 200 changes.
  Each part is one transaction at Cloudflare.
- A TTL that is longer than one day becomes one day, because Cloudflare does not accept more.
  The TTL "automatic" of Cloudflare is 300 seconds in the panel.
- When the zone is not at Cloudflare and the account has an account ID, the plugin adds the
  zone. Without an account ID, add the zone at Cloudflare first.
- When DNSSEC is on in the panel, the plugin turns on DNSSEC at Cloudflare and gives the DS
  record to the panel.

## Make an API token

1. At Cloudflare, go to **My Profile > API Tokens**, and select **Create Token**.
2. Use **Create Custom Token**.
3. Give the permissions **Zone: Read** and **DNS: Edit**.
4. To let the panel add and remove zones, also give **Zone: Edit**.
5. In **Zone Resources**, select the zones, or all zones of the account.
6. Select **Continue to summary**, and then **Create Token**.

To add zones, the panel also needs the account ID. The ID is on the overview page of each zone
at Cloudflare.

## Run the plugin

1. Sign the manifest with your key:

   ```sh
   cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
   ```

2. In the panel, add `cosign.pub` in **Plugins > Trusted keys**.
3. Install the plugin with the address of the server. Put the server behind HTTPS.

   ```sh
   cf plugins install --manifest plugin.json --address https://dns-cloudflare.example.net
   ```

4. Start the server with the webhook secret from the install:

   ```sh
   CF_WEBHOOK_SECRET=whsec_... PORT=8080 node server.mjs
   ```

The provider Cloudflare is now in the card **External DNS providers** on the **DNS** page.
`GET /health` gives `{"ok":true}` for a health check.

| Variable | Value |
| -------- | ----- |
| `CF_WEBHOOK_SECRET` | The webhook secret from the install. Necessary. |
| `PORT` | The port of the server. The default is 8080. |
| `CLOUDFLARE_BATCH_SIZE` | The number of changes in one batch. The default is 200, the limit of the free plan. A paid plan accepts more. |
| `CLOUDFLARE_API` | The address of the Cloudflare API. For tests only. |

## Run the plugin in the panel

1. Build and push the image:

   ```sh
   podman build -t ghcr.io/example/dns-cloudflare:1.0.0 -f Containerfile .
   podman push --digestfile digest ghcr.io/example/dns-cloudflare:1.0.0
   ```

2. Add the image to `plugin.json`, with the digest from the file `digest`:

   ```json
   "image": { "ref": "ghcr.io/example/dns-cloudflare:1.0.0", "digest": "sha256:...", "port": 8080 }
   ```

3. Sign the manifest again.
4. Install the plugin in the panel:

   ```sh
   cf plugins install --hosted --manifest plugin.json
   ```

The panel gives `CF_WEBHOOK_SECRET` to the container.

## Security

- The plugin gets the API token in each request from the panel. It does not keep the token
  and does not write it to the log.
- The plugin refuses a request with a signature that is not correct, or that is older than 5
  minutes. It also refuses a request with an ID that it received before.
- Give the token only the permissions of [Make an API token](#make-an-api-token), and only
  for the zones that the panel uses.
