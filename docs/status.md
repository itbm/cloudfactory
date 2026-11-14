# Status of release 0.1

The specification, section 10, gives the contents of release 0.1 (Foundation). This document
shows what this repository has now.

## Complete

| Item | Where |
| ---- | ----- |
| Web panel with all screens of the specification, section 9, and their actions | `web/` |
| Web panel on the real API (live mode). The production build uses it. | `web/src/api/live.ts` |
| One console for each role: an administrator console, a reseller console and an owner console, each with its own menu and first page | `web/src/lib/roles.ts`, `web/src/App.tsx` |
| Setup checklist for the administrator, with the effect of each open item | `api/app/Http/Controllers/Api/SetupController.php`, `web/src/pages/SetupPage.tsx` |
| Account page for owners and resellers: plan, use, provider and users | `api/app/Http/Controllers/Api/AccountController.php`, `web/src/pages/AccountPage.tsx` |
| New image tags from the registry: the panel checks each hour and accepts a tag only with a good signature (FR-1110) | `api/app/Services/ImageUpdateChecker.php` |
| Host CPU, memory and disk use in the agent report | `agent/internal/status` |
| CPU, memory and disk use of each website in the agent report | `agent/internal/actions/observe.go` |
| Panel API: accounts, users with temporary passwords, scoped tokens, TOTP, customers, plans, websites, domains, runtimes, databases, backups, servers, images, rollouts, jobs and audit events | `api/` |
| Users: administrators and resellers add users, reset passwords and delete users in the web panel and the CLI. Root on the panel server can reset a password and remove a sign-in lock | `api/app/Http/Controllers/Api/UserController.php`, `web/src/pages/CustomersPage.tsx`, `cli/cmd/cf/admin.go` |
| A PostgreSQL database for the panel, and HTTPS with a pinned certificate between the panel and the agent on the panel server | `agent/internal/quadlet`, `install/install.sh` |
| The panel certificate: a new panel gets a certificate from its own CA, also for `localhost` and `panel.localhost`. An administrator downloads the root certificate and changes to Let's Encrypt in **Settings > Panel certificate**. The agent goes back to the previous method if the new certificate does not come | `agent/internal/caddy/panelcert.go`, `api/app/Http/Controllers/Api/PanelCertificateController.php`, `web/src/pages/SettingsPage.tsx` |
| A firewall for the connections that a website opens: no private networks, no cloud metadata service, no port 25, and only the database servers of its own customer | `install/install.sh`, `agent/internal/actions/instances.go` |
| Database servers: a customer makes database servers as it makes websites. The plan of the customer gives their number. The plan of a database server gives its limits and the number of its databases. Each database server has its own Unix user, and the user quota of that user applies its disk limit. On a file system with no user quotas, a disk image applies the limit | `api/app/Services/DatabaseServers.php`, `api/app/Http/Controllers/Api/DatabaseServerController.php`, `agent/internal/actions/dbserver.go`, `agent/internal/actions/dbdisk.go`, `web/src/features/DatabaseServersPanel.tsx`, `cli/cmd/cf/dbservers.go` |
| File manager, logs, cron jobs and deploys from Git for each website, in the API, the agent, the web panel and the CLI. The agent does all of this work as the Unix user of the website, and the root helper has no new operation for it | `api/app/Http/Controllers/Api/FileController.php`, `LogController.php`, `CronJobController.php`, `DeployController.php`, `agent/internal/actions/files.go`, `cron.go`, `deploy.go`, `agent/internal/sitefiles`, `web/src/features/FileManager.tsx`, `web/src/pages/website/`, `cli/cmd/cf/sitetools.go` |
| OpenAPI 3.1 document, with a test that compares it to the routes | `openapi/`, `api/tests/Feature/OpenApiTest.php` |
| Agent: websites, limits, runtime changes without downtime, domains and TLS, MariaDB, PostgreSQL and Valkey database servers for each customer (one container for each database server, with limits for the CPU, the memory and the disk space), backups and restores, host security updates, reboots, gVisor install and rollback, signed self-update with rollback | `agent/` |
| A privilege boundary in the agent: a worker with the user `cf-agent` has the network port and the jobs, and a root helper does only the commands, the operations and the connections of its lists | `agent/cmd/cf-agent/serve.go`, `agent/internal/gate`, `agent/internal/actions/policy.go` |
| The mesh: encrypted WireGuard tunnels between the servers. Each server makes its own key, and the panel is the directory of the public keys. An administrator makes, repairs and removes tunnels and makes new keys on the **Network** page and with `cf mesh` | `agent/internal/mesh`, `agent/internal/actions/mesh.go`, `api/app/Services/Mesh.php`, `web/src/pages/NetworkPage.tsx`, `cli/cmd/cf/mesh.go` |
| The networks of the customers, first part: each customer has the network `default`. A website connects to each database server of its customer by the name `<name>.internal` and the usual port of the engine, on the same server and on a different server. The panel makes and removes the tunnels that the networks need. Not done yet: the panel makes a database only through a website on the server of the database server, a website is not a member that other websites can connect to, and a customer cannot make more networks | `agent/internal/mesh/network.go`, `agent/internal/actions/network.go`, `api/app/Services/Networks.php`, `api/app/Services/Mesh.php` |
| Mutual TLS between the panel and each agent, with pinned certificates | `agent/internal/server`, `api/app/Services/AgentClient.php` |
| Rollouts in batches with a canary, a health check, a stop and a roll back | `api/app/Services/RolloutEngine.php` |
| SFTP for each website in its own gVisor container on port 2200: SSH keys only (no passwords), locked in the directory of the website. The agent moves servers of release 0.1 from the SSH service of the host | `images/sftp`, `agent/internal/actions/sftp.go`, `api/app/Http/Controllers/Api/SftpController.php` |
| Backups each night at the policy time, with retention and a weekly prune, in one repository with its own password for each server. Updates in the maintenance window. Checks for jobs and servers that stop answering | `api/app/Console/Commands/` |
| `cf` CLI for all public API areas, with `--json` and `--no-wait`. Lists read all pages | `cli/` |
| Installation script with checks before changes, for a panel and for a new server | `install/` |
| Shared read-only runtime images: one copy of each image on a server for all websites, so a new website starts with no pull | `agent/internal/images` |
| Images: FrankenPHP and Apache with PHP-FPM for PHP 8.4 and 8.5, the Caddy edge, the panel API and web, MariaDB, PostgreSQL, Valkey and SFTP | `images/` |
| CI, image scans, signed multi-arch releases with SBOMs, and a daily rebuild (the schedule is off, refer to [releasing.md](releasing.md#daily-image-rebuild)) | `.github/workflows/` |

## Not complete

| Item | Notes |
| ---- | ----- |
| Areas without an API | The API of release 0.1 has no DNS, email, SSH shell, passkeys, alerts, branding or system settings. The web panel has no pages for these areas. |
| File manager | The file manager reads files up to 2 MiB and writes files up to 5 MiB. It has no archive actions and no change of permissions. Use SFTP for large files. |
| Logs | The panel shows the last lines that the container of a website wrote. A website that was made before this release writes its log file after its next start. The panel has no search in old log lines and no access log of the edge proxy. |
| Deploys | A deploy starts by hand or with a webhook. The panel does not check the signature that a Git host puts on a webhook: the secret is the address. The panel has no deploy on a schedule, and does not go back automatically after the switch. The build runs in the runtime image of the website, which has Git and Composer but not Node.js. A website with a custom image cannot deploy from Git. |
| Staging copy | A staging copy gets the files only. It keeps the database settings of the original website, so it uses the live databases. |
| Restore of a full website | A restore of the full website restores the files only. Restore each database separately. |
| Email from the panel | The panel sends no email. A new user gets a temporary password from the person who makes the user. |
| Known security limits | Refer to [security.md](security.md#known-limits): image tags in the signature policy, service images between releases, and one set of object storage credentials for all servers. |
| Disk quotas | The agent sets the disk limit of each website only when the file system has user quotas. The installer turns them on for an ext4 file system. For XFS and other file systems, you turn them on. A database server on a file system with no user quotas gets a disk image, which needs loop devices. Refer to [installation.md](installation.md#disk-quotas). |
| gVisor benchmark | Release 0.1 must measure websites, MariaDB and DNS in gVisor (R-08). This is not done. |
| Passkeys | The API does not support passkeys yet, and the web panel has no page for them. Users sign in with a password and TOTP, and set up TOTP and change the password in Settings. |
| Tests on real servers | The tests use fakes for the system commands. The Caddy routes have a test with a real edge container. Nobody has installed release 0.1 on a real Debian 13 or Ubuntu 26.04 server yet. Test these items on a real server: the Caddy admin socket in gVisor (`--runtime-flag=host-uds=create`), the route from the edge to a website through the private service address `169.254.77.1`, the MariaDB client host of a website container and the nftables rule between websites (all checked with Debian 13 packages in a container only, with cgroup v1 and no systemd), file owners with `--userns=keep-id` in gVisor, the shared image store with fuse-overlayfs under systemd and AppArmor, a restore with the PostgreSQL group roles, the firewall rules for the connections from the websites (checked with nftables in a network namespace only), the PostgreSQL container of the panel, the tunnels of the mesh with the WireGuard of the kernel (checked between network namespaces with `wireguard-go` only), the connection of a website to a database server on a different server by its network name (checked between network namespaces with `wireguard-go`, a gVisor container for the website and a test program in place of the database server), a reboot from the panel, `needrestart` after an update, backups to real object storage, the log file of a website container (`--log-opt path=` with the `k8s-file` driver in rootless Podman), a cron command with `podman exec` in gVisor, and a deploy: the build container with `systemd-run --user`, a clone with the deploy key, and the switch to the new release. |
| DNS, email, migration, Terraform | These are in releases 0.2 to 1.0. |
