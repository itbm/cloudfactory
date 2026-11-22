# PowerDNS plugin

This plugin sends the DNS zones of the panel to a PowerDNS Authoritative Server, with the HTTP
API of PowerDNS. The panel keeps the records. After each change, the panel sends the full zone
to the plugin, and the plugin makes the zone in PowerDNS the same. The plugin keeps no data.

Use this plugin when you or your customers have PowerDNS servers. Each DNS account has the
address and the key of one API.

The plugin uses the contract `cf-dns-provider/1`. Refer to
[docs/plugins.md](../../docs/plugins.md#dns-providers).

| File | Function |
| ---- | -------- |
| `plugin.json` | The manifest. It has the provider `powerdns` and its credentials. |
| `server.mjs` | The server. It checks the signature of each request and refuses a request that it received before. |
| `powerdns.mjs` | The operations `verify`, `records`, `sync` and `remove` with the API of PowerDNS. |
| `test/` | The tests. Run `node --test test/*.test.mjs`. |

The plugin needs Node.js 22. It has no dependencies. It uses the API v1 of the PowerDNS
Authoritative Server.

## What the plugin changes in PowerDNS

- The plugin sends all the changes of a zone in one `PATCH` request. PowerDNS applies it in
  one transaction.
- The plugin changes only the record types of the panel: A, AAAA, CNAME, MX, TXT, SRV, CAA
  and NS. Record sets of other types do not change.
- The SOA record and the NS records of the zone itself do not change. PowerDNS keeps them.
- PowerDNS keeps one TTL for each record set (a name and a type). When the records of a set
  have different TTLs in the panel, the set gets the shortest TTL.
- When the zone is not in PowerDNS, the plugin adds it with the name servers of the DNS
  account. Without name servers in the account, add the zone in PowerDNS first. A secondary
  zone in PowerDNS is an error.
- When DNSSEC is on in the panel, the plugin turns on DNSSEC in PowerDNS with the default keys
  of the server. It gives the SHA-256 DS record of the active key to the panel. When DNSSEC is
  off in the panel, the plugin turns off DNSSEC and PowerDNS removes the keys.

## Prepare PowerDNS

1. Turn on the API in the configuration of PowerDNS:

   ```ini
   api=yes
   api-key=<a long random value>
   webserver=yes
   webserver-address=127.0.0.1
   webserver-port=8081
   ```

2. Put a reverse proxy with HTTPS in front of the API, for example
   `https://pdns.example.net:8443`. The plugin uses HTTP with no TLS only for an address in
   `POWERDNS_ALLOW`.
3. Let only the address of the plugin connect to the API. Set `webserver-allow-from` and the
   firewall.

## Add a DNS account

| Field | Value |
| ----- | ----- |
| API address | The address of the API, for example `https://pdns.example.net:8443`. The plugin adds `/api/v1`. |
| API key | The value of `api-key`. |
| Server ID | Optional. The default is `localhost`. |
| Name servers for new zones | Optional. The NS records of a zone that the plugin adds, with commas, for example `ns1.example.net, ns2.example.net`. |

## Run the plugin

1. Sign the manifest with your key:

   ```sh
   cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
   ```

2. In the panel, add `cosign.pub` in **Plugins > Trusted keys**.
3. Install the plugin with the address of the server. Put the server behind HTTPS.

   ```sh
   cf plugins install --manifest plugin.json --address https://dns-powerdns.example.net
   ```

4. Start the server with the webhook secret from the install:

   ```sh
   CF_WEBHOOK_SECRET=whsec_... PORT=8080 node server.mjs
   ```

| Variable | Value |
| -------- | ----- |
| `CF_WEBHOOK_SECRET` | The webhook secret from the install. Necessary. |
| `PORT` | The port of the server. The default is 8080. |
| `POWERDNS_ALLOW` | The hosts and networks in a private network that the plugin can connect to, with commas. For example `pdns.internal, 10.0.0.0/8`. The default is none. |
| `POWERDNS_KIND` | The kind of a new zone: `Native` (the default) or `Master`. Use `Master` when the secondary servers copy the zones with AXFR. |

To run the plugin in the panel, use `Containerfile`. Refer to
[Run the plugin in the panel](../dns-cloudflare/README.md#run-the-plugin-in-the-panel) in the
Cloudflare DNS plugin. The steps are the same.

## Security

- The user gives the address of the API. Thus the plugin does not connect to an address on
  this host or in a private network, for example `127.0.0.1`, `10.0.0.0/8` or the metadata
  service of a cloud at `169.254.169.254`. The plugin checks each address that DNS gives for
  the host name, and connects to the address that it checked. It does not follow redirects.
- To use a PowerDNS server in your private network, add its host name or its network to
  `POWERDNS_ALLOW`. Then each user of the panel can send requests to that host. Add only the
  hosts of your PowerDNS servers.
- The plugin shows only the `error` text of an answer of PowerDNS, with up to 200 characters.
  It does not show other answers.
- The plugin gets the API key in each request from the panel. It does not keep the key and
  does not write it to the log.
- The plugin refuses a request with a signature that is not correct, or that is older than 5
  minutes. It also refuses a request with an ID that it received before.
