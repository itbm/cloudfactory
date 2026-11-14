# Status of release 0.1

The specification, section 10, gives the contents of release 0.1 (Foundation). This document
shows what this repository has now.

## Complete

| Item | Where |
| ---- | ----- |
| Web panel with all screens of the specification, section 9, and their actions | `web/` |
| Web panel on the real API (live mode). The production build uses it. | `web/src/api/live.ts` |
| New image tags from the registry: the panel checks each hour and accepts a tag only with a good signature (FR-1110) | `api/app/Services/ImageUpdateChecker.php` |
| Host CPU, memory and disk use in the agent report | `agent/internal/status` |
| CPU, memory and disk use of each website in the agent report | `agent/internal/actions/observe.go` |
| Panel API: accounts, users with temporary passwords, scoped tokens, TOTP, customers, plans, websites, domains, runtimes, databases, backups, servers, images, rollouts, jobs and audit events | `api/` |
| Users: administrators and resellers add users, reset passwords and delete users in the web panel and the CLI. Root on the panel server can reset a password and remove a sign-in lock | `api/app/Http/Controllers/Api/UserController.php`, `web/src/pages/CustomersPage.tsx`, `cli/cmd/cf/admin.go` |
| A PostgreSQL database for the panel, and HTTPS with a pinned certificate between the panel and the agent on the panel server | `agent/internal/quadlet`, `install/install.sh` |
| The panel certificate: a new panel gets a certificate from its own CA, also for `localhost` and `panel.localhost`. An administrator downloads the root certificate and changes to Let's Encrypt in **Settings > Panel certificate**. The agent goes back to the previous method if the new certificate does not come | `agent/internal/caddy/panelcert.go`, `api/app/Http/Controllers/Api/PanelCertificateController.php`, `web/src/pages/SettingsPage.tsx` |
| A firewall for the connections that a website opens: no private networks, no cloud metadata service, no port 25, and only the database containers of its own customer | `install/install.sh`, `agent/internal/actions/instances.go` |
| OpenAPI 3.1 document, with a test that compares it to the routes | `openapi/`, `api/tests/Feature/OpenApiTest.php` |
| Agent: websites, limits, runtime changes without downtime, domains and TLS, MariaDB, PostgreSQL and Valkey databases in separate containers for each customer, backups and restores, host security updates, reboots, gVisor install and rollback, signed self-update with rollback | `agent/` |
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
| Areas without an API | The API of release 0.1 has no DNS, email, files, cron, deploys, SSH shell, logs, passkeys, alerts, branding or system settings. In live mode the web panel hides these areas. In mock mode it shows them with sample data. |
| Staging copy | A staging copy gets the files only. It keeps the database settings of the original website, so it uses the live databases. |
| Restore of a full website | A restore of the full website restores the files only. Restore each database separately. |
| Email from the panel | The panel sends no email. A new user gets a temporary password from the person who makes the user. |
| Known security limits | Refer to [security.md](security.md#known-limits): image tags in the signature policy, service images between releases, and one set of object storage credentials for all servers. |
| Disk quotas | The agent sets the disk limit of each website only when the file system has user quotas. The installer does not turn them on. Refer to [installation.md](installation.md#disk-quotas). |
| gVisor benchmark | Release 0.1 must measure websites, MariaDB and DNS in gVisor (R-08). This is not done. |
| Passkeys | The web panel shows passkeys. The API does not support passkeys yet. Users sign in with a password and TOTP, and set up TOTP and change the password in Settings. |
| Tests on real servers | The tests use fakes for the system commands. The Caddy routes have a test with a real edge container. Nobody has installed release 0.1 on a real Debian 13 or Ubuntu 26.04 server yet. Test these items on a real server: the Caddy admin socket in gVisor (`--runtime-flag=host-uds=create`), the route from the edge to a website through the private service address `169.254.77.1`, the MariaDB client host of a website container and the nftables rule between websites (all checked with Debian 13 packages in a container only, with cgroup v1 and no systemd), file owners with `--userns=keep-id` in gVisor, the shared image store with fuse-overlayfs under systemd and AppArmor, a restore with the PostgreSQL group roles, the firewall rules for the connections from the websites (checked with nftables in a network namespace only), the PostgreSQL container of the panel, a reboot from the panel, `needrestart` after an update, and backups to real object storage. |
| DNS, email, Git deploys, migration, Terraform | These are in releases 0.2 to 1.0. |
