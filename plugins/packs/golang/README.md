# Go runtime (example pack)

This directory is an example of a [pack](../../../docs/packs.md): a website runtime with no
new agent code. It is the test case for the contracts `cf-pack/1` and `cf-runtime/1`.

| File | Purpose |
| --- | --- |
| `pack.json` | The manifest. The shared test cases in `schema/pack-vectors.json` change this file. |
| `Containerfile` | The image. It obeys the runtime contract `cf-runtime/1`. |
| `cf-app` | The start script of the image. |
| `welcome.go` | The page of a new website that has no application yet. |

The digests in `pack.json` are examples. Before you install the pack, build the images,
then write their real digests in the manifest.

## The runtime contract

The agent runs the runtime as it runs Node.js and Python:

- The image has `/usr/local/bin/cf-app`. The script goes to `CF_APP_DIR` and runs the
  start command of the website.
- The application listens on the port in `PORT` (8080), on the address in `HOST`.
- The website must have a start command. The build command and the install command run in
  a build container of the same image.
- The container has the same isolation as each other website: gVisor, a read-only root file
  system, no capabilities and its own network namespace.

## Make and install the pack

1. In `pack.json`, change the registry in each `ref` to your registry. This example uses
   `ghcr.io/example`.
2. Build and push an image for each version, for example:

   ```sh
   podman build --build-arg GO_VERSION=1.27 -t ghcr.io/example/cf-golang:1.27 plugins/packs/golang
   podman push --digestfile digest ghcr.io/example/cf-golang:1.27
   ```

3. Sign each image, and write its digest in `pack.json`:

   ```sh
   cosign sign --yes --key cosign.key ghcr.io/example/cf-golang@$(cat digest)
   ```

4. Sign the manifest:

   ```sh
   cosign sign-blob --yes --key cosign.key --tlog-upload=false --output-signature pack.json.sig pack.json
   ```

5. In the panel, add `cosign.pub` on **Packs > Trusted keys**.
6. Select **Install pack**, then select `pack.json` and `pack.json.sig`.

Customers can then select Go on the **New website** page. The panel puts these websites only
on the servers that loaded the pack. Refer to [packs.md](../../../docs/packs.md) for each
step.
