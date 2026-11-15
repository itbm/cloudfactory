# Example plugin: Uptime monitor

This directory has a small plugin for the panel. Use it as a start for your own plugin.
Refer to [docs/plugins.md](../../docs/plugins.md) for the full rules.

| File | Function |
| ---- | -------- |
| `plugin.json` | The manifest: the scopes, the events, the page, the action, the before hook and the command. |
| `page.html` | The page. It asks the panel for a page token and reads the websites. |
| `server.mjs` | The server. It serves the page, runs the action, answers the before hook and checks the signature of each request. |

The uptime values on the page are examples. The plugin does not check the websites.

## Run it

1. Sign the manifest with your key:

   ```sh
   cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
   ```

2. In the panel, add `cosign.pub` in **Plugins > Trusted keys**.
3. Start the server on a host name that is not the panel. Put it behind HTTPS.

   ```sh
   CF_PANEL=https://panel.example.com PORT=8090 node server.mjs
   ```

4. Install the plugin with the address of the server:

   ```sh
   cf plugins install --manifest plugin.json --address https://uptime.example.net
   ```

5. Start the server again with the webhook secret from the install:

   ```sh
   CF_PANEL=https://panel.example.com CF_WEBHOOK_SECRET=whsec_... PORT=8090 node server.mjs
   ```

The page **Uptime** is now in the menu of each role. Select **Plugins > Send a test event** to
test the webhook.

The page of each website now has the button **Check uptime now**. To stop the deletion of some
websites, give their names to the server in `PROTECTED_WEBSITES`, separated by commas:

```sh
PROTECTED_WEBSITES=shop.example.com,www.example.com CF_WEBHOOK_SECRET=whsec_... node server.mjs
```

In the demonstration mode of the API, the sample plugin "Uptime monitor" uses `page.html` from
this directory.

## Run it in the panel

1. Build and push the image:

   ```sh
   podman build -t ghcr.io/example/uptime:1.4.0 -f Containerfile .
   podman push --digestfile digest ghcr.io/example/uptime:1.4.0
   ```

2. Add the image to `plugin.json`, with the digest from the file `digest`:

   ```json
   "image": { "ref": "ghcr.io/example/uptime:1.4.0", "digest": "sha256:...", "port": 8080 }
   ```

3. Sign the manifest again, and add `cosign.pub` in **Plugins > Trusted keys**.
4. Install the plugin in the panel:

   ```sh
   cf plugins install --hosted --manifest plugin.json
   ```

The panel gives `CF_PANEL`, `CF_TOKEN` and `CF_WEBHOOK_SECRET` to the container.
