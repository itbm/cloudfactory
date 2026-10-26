# Status of release 0.1

The specification, section 10, gives the contents of release 0.1 (Foundation). This document
shows what this repository has now.

## Complete

| Item | Where |
| ---- | ----- |
| Web panel with all screens of the specification, section 9, and their actions | `web/` |
| Panel API: accounts, scoped tokens, TOTP, customers, plans, websites, domains, runtimes, databases, backups, servers, images, rollouts, jobs and audit events | `api/` |
| OpenAPI 3.1 document, with a test that compares it to the routes | `openapi/`, `api/tests/Feature/OpenApiTest.php` |
| Agent: websites, limits, runtime changes without downtime, domains and TLS, MariaDB and PostgreSQL databases, backups and restores, host security updates, reboots, gVisor install and rollback, signed self-update with rollback | `agent/` |
| Mutual TLS between the panel and each agent, with pinned certificates | `agent/internal/server`, `api/app/Services/AgentClient.php` |
| Rollouts in batches with a canary, a health check, a stop and a roll back | `api/app/Services/RolloutEngine.php` |
| `cf` CLI for all public API areas, with `--json` and `--no-wait` | `cli/` |
| Installation script with checks before changes, for a panel and for a new server | `install/` |
| Images: FrankenPHP and Apache with PHP-FPM for PHP 8.2 to 8.5, the Caddy edge, the panel API and web, MariaDB and PostgreSQL | `images/` |
| CI, image scans, signed multi-arch releases with SBOMs, and a daily rebuild | `.github/workflows/` |

## Not complete

| Item | Notes |
| ---- | ----- |
| Web panel on the real API | The web panel uses a mock API with the same shapes. The next step is to replace `web/src/api/store.ts` with calls to the API. |
| SFTP | The web panel shows SFTP access. The agent has no SFTP service yet. |
| gVisor benchmark | Release 0.1 must measure websites, MariaDB and DNS in gVisor (R-08). This is not done. |
| Passkeys | The web panel shows passkeys. The API does not support passkeys yet. Users sign in with a password and TOTP. |
| New image tags from the registry | The panel does not read the registry for new tags yet (FR-1110). An operator adds the tag. |
| Signing key | The maintainers must make the cosign key and commit `cosign.pub`. Refer to [releasing.md](releasing.md). |
| Tests on real servers | The tests use fakes for the system commands. The Caddy routes have a test with a real edge container. Nobody has installed release 0.1 on a real Debian 13 or Ubuntu 26.04 server yet. |
| DNS, email, Git deploys, migration, Terraform | These are in releases 0.2 to 1.0. |
