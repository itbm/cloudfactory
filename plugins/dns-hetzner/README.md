# Hetzner DNS plugin

This plugin sends the DNS zones of the panel to Hetzner DNS, with the DNS API of the Hetzner
Cloud API. The panel keeps the records. After each change, the panel sends the full zone to the
plugin, and the plugin makes the zone at Hetzner the same. The plugin keeps no data.

The plugin uses the contract `cf-dns-provider/1`. Refer to
[docs/plugins.md](../../docs/plugins.md#dns-providers).

| File | Function |
| ---- | -------- |
| `plugin.json` | The manifest. It has the provider `hetzner` and its credentials. |
| `server.mjs` | The server. It checks the signature of each request and refuses a request that it received before. |
| `hetzner.mjs` | The operations `verify`, `records`, `sync` and `remove` with the Hetzner Cloud API. |
| `test/` | The tests. Run `node --test test/*.test.mjs`. |

The plugin needs Node.js 22. It has no dependencies.

## What the plugin changes at Hetzner

- The plugin changes only the record types of the panel: A, AAAA, CNAME, MX, TXT, SRV, CAA
  and NS. Record sets of other types do not change.
- The SOA record and the NS records of the zone itself do not change. Hetzner keeps them.
- Hetzner keeps one TTL for each record set (a name and a type). When the records of a set
  have different TTLs in the panel, the set gets the shortest TTL.
- When the zone is not at Hetzner, the plugin adds it as a primary zone. A secondary zone at
  Hetzner is an error.
- The plugin sends one request for each changed record set. Hetzner runs the changes as
  actions. The plugin sends the deletes, waits for their actions, and then sends the other
  changes and waits for their actions.
- A sync stops after 45 seconds, because the panel waits 60 seconds for the answer. Then the
  zone has the status **Failed**, and the next sync continues with the changes that are left.
- The Hetzner API accepts 3600 requests in one hour for each project. The first sync of a
  large zone uses a part of this limit.
- The Hetzner API has no DNSSEC. Thus a zone with DNSSEC cannot move to Hetzner.

The plugin uses the DNS API of the Hetzner Console. It does not use the API of the old DNS
Console (dns.hetzner.com). Move a zone from the old DNS Console to the Hetzner Console first.

## Make an API token

1. In the Hetzner Console, open the project of your DNS zones.
2. Go to **Security > API tokens**, and select **Generate API token**.
3. Select the permission **Read & Write**.
4. Select **Generate API token**.

## Run the plugin

1. Sign the manifest with your key:

   ```sh
   cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
   ```

2. In the panel, add `cosign.pub` in **Plugins > Trusted keys**.
3. Install the plugin with the address of the server. Put the server behind HTTPS.

   ```sh
   cf plugins install --manifest plugin.json --address https://dns-hetzner.example.net
   ```

4. Start the server with the webhook secret from the install:

   ```sh
   CF_WEBHOOK_SECRET=whsec_... PORT=8080 node server.mjs
   ```

| Variable | Value |
| -------- | ----- |
| `CF_WEBHOOK_SECRET` | The webhook secret from the install. Necessary. |
| `PORT` | The port of the server. The default is 8080. |
| `HETZNER_API` | The address of the Hetzner Cloud API. For tests only. |

To run the plugin in the panel, use `Containerfile`. Refer to
[Run the plugin in the panel](../dns-cloudflare/README.md#run-the-plugin-in-the-panel) in the
Cloudflare DNS plugin. The steps are the same.

## Security

- The plugin gets the API token in each request from the panel. It does not keep the token
  and does not write it to the log.
- The plugin refuses a request with a signature that is not correct, or that is older than 5
  minutes. It also refuses a request with an ID that it received before.
- A token can change all the resources of its project, for example servers. Thus use a
  project that has only your DNS zones.
