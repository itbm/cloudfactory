# Go runtime (example of a runtime manifest)

This directory is a runtime that comes from a manifest, with no new agent code. It is the
test case for the runtime contract in `agent/internal/quadlet/runtimes.go`.

| File | Purpose |
| --- | --- |
| `runtime.json` | The manifest. The agent reads it when its settings name the directory. |
| `Containerfile` | The image. It follows the contract of the kind `app`. |
| `cf-app` | The start script of the image. |
| `welcome.go` | The page of a new website that has no application yet. |

## The contract of the kind `app`

The agent runs a runtime of the kind `app` as it runs Node.js and Python:

- The image has `/usr/local/bin/cf-app`. The script goes to `CF_APP_DIR` and runs the
  start command of the website.
- The application listens on the port in `PORT` (8080), on the address in `HOST`.
- The website must have a start command. The build command and the install command run in
  a build container of the same image.
- The container has the same isolation as every website: gVisor, a read-only root file
  system, no capabilities and its own network namespace.

A manifest can give only the kind `app`. It cannot change the unit in other ways.

## Turn it on

The switch is off by default. With no switch, the agent has only the built-in runtimes, as
before.

1. Copy the manifest to the server:

   ```sh
   install -d -m 0755 /etc/cloudfactory/runtimes
   install -m 0644 plugins/runtimes/golang/runtime.json /etc/cloudfactory/runtimes/golang.json
   ```

2. Add `"runtimeManifests": "/etc/cloudfactory/runtimes"` to
   `/etc/cloudfactory/agent.json`.
3. Restart the agent: `systemctl restart cf-agent`.
4. Build the image, sign it, and add it to the panel: on the **Apps** page, select **Add your
   own image**, then **Runtime from an agent manifest**. The runtime name is `golang`, and
   the usual start command can be `./server` with the install command
   `go build -o server .`. Refer to [plugins.md](../../../docs/plugins.md#runtime-images-in-the-panel).

Customers can then select Go on the **New website** page. The panel puts these websites only
on the servers whose agent has the manifest.
