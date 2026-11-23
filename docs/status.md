# Status of release 0.1

The specification, section 10, gives the contents of release 0.1 (Foundation). This document
shows what this repository has now.

Each item is one paragraph, with a blank line before it and after it. Thus two pull requests
that change different items do not conflict. Change the item of your feature. Put a new item
after the item that is nearest to it in subject, not at the end of the list.

## Complete

- Web panel with all screens of the specification, section 9, and their actions

  Where: `web/`

- Web panel on the real API (live mode). The production build uses it.

  Where: `web/src/api/live.ts`

- One console for each role: an administrator console, a reseller console and an owner console, each with its own menu and first page

  Where: `web/src/lib/roles.ts`, `web/src/App.tsx`

- Setup checklist for the administrator, with the effect of each open item

  Where: `api/app/Http/Controllers/Api/SetupController.php`, `web/src/pages/SetupPage.tsx`

- Account page for owners and resellers: plan, use, provider and users

  Where: `api/app/Http/Controllers/Api/AccountController.php`, `web/src/pages/AccountPage.tsx`

- Password reset by email: a link that works one time for 60 minutes, to the panel domain of the brand of the user

  Where: `api/app/Http/Controllers/Api/PasswordResetController.php`, `web/src/pages/PasswordResetPages.tsx`

- Passkeys (FR-103): a user adds, renames and removes passkeys in **Settings**, and signs in with a passkey in place of the password and the TOTP code. A passkey must verify the user, and works only on the panel domain where the user added it

  Where: `api/app/Services/Passkeys.php`, `api/app/Http/Controllers/Api/PasskeyController.php`, `web/src/lib/webauthn.ts`, `web/src/features/Passkeys.tsx`

- Single sign-on with OpenID Connect and SAML 2.0: an administrator adds providers in **Settings > Single sign-on**, and the sign-in page shows a button for each provider. A user connects the account in **Settings**, or the panel connects the user with the same verified email of an accepted domain. The panel still asks for TOTP when TOTP is on

  Where: `api/app/Services/Sso`, `api/app/Http/Controllers/Api/SsoController.php`, `api/app/Http/Controllers/Api/IdentityProviderController.php`, `web/src/features/SingleSignOn.tsx`, `web/src/pages/SsoPage.tsx`

- New image tags from the registry: the panel checks each hour and accepts a tag only with a good signature (FR-1110)

  Where: `api/app/Services/ImageUpdateChecker.php`

- Host CPU, memory and disk use in the agent report

  Where: `agent/internal/status`

- CPU, memory and disk use of each website in the agent report

  Where: `agent/internal/actions/observe.go`

- Usage history and usage alerts: the agent reports the traffic of each website (from the metrics of the edge proxy) and of each server. The panel keeps buckets of 5 minutes for 8 days and of 1 hour for 400 days, and shows graphs of CPU, memory, disk and traffic for each website, server and database server (**Usage** tab, server page). A plan can have a bandwidth limit for each month. An alert opens at the warning threshold of a limit (80% by default) and becomes critical at the critical threshold (95%). The page **Usage alerts** shows the alerts of each role, and an administrator sets the thresholds. The events `UsageAlertRaised` and `UsageAlertResolved` and the email notifier deliver them. The panel does not stop a website at a limit because of an alert

  Where: `agent/internal/caddy/traffic.go`, `api/app/Services/UsageRecorder.php`, `api/app/Http/Controllers/Api/UsageController.php`, `web/src/components/UsageCharts.tsx`, `web/src/pages/AlertsPage.tsx`

- Panel API: accounts, users with temporary passwords, scoped tokens, TOTP, customers, plans, websites, domains, runtimes, databases, backups, servers, images, rollouts, jobs and audit events

  Where: `api/`

- Users: administrators and resellers add users, reset passwords and delete users in the web panel and the CLI. Root on the panel server can reset a password and remove a sign-in lock

  Where: `api/app/Http/Controllers/Api/UserController.php`, `web/src/pages/CustomersPage.tsx`, `cli/cmd/cf/admin.go`

- A PostgreSQL database for the panel, and HTTPS with a pinned certificate between the panel and the agent on the panel server

  Where: `agent/internal/quadlet`, `install/install.sh`

- The panel certificate: a new panel gets a certificate from its own CA, also for `localhost` and `panel.localhost`. An administrator downloads the root certificate and changes to Let's Encrypt in **Settings > Panel certificate**. The agent goes back to the previous method if the new certificate does not come

  Where: `agent/internal/caddy/panelcert.go`, `api/app/Http/Controllers/Api/PanelCertificateController.php`, `web/src/pages/SettingsPage.tsx`

- A firewall for the connections that a website opens: no private networks, no cloud metadata service, no port 25, and only the database servers of its own customer

  Where: `install/install.sh`, `agent/internal/actions/instances.go`

- Database servers: a customer makes database servers as it makes websites. The plan of the customer gives their number. The plan of a database server gives its limits and the number of its databases. Each database server has its own Unix user, and the user quota of that user applies its disk limit. On a file system with no user quotas, a disk image applies the limit

  Where: `api/app/Services/DatabaseServers.php`, `api/app/Http/Controllers/Api/DatabaseServerController.php`, `agent/internal/actions/dbserver.go`, `agent/internal/actions/dbdisk.go`, `web/src/features/DatabaseServersPanel.tsx`, `cli/cmd/cf/dbservers.go`

- Email: an administrator turns on a mail server (Stalwart) for a server. A customer adds mail domains with their DNS records, mailboxes with a size limit, aliases and forwarders. The plan gives the number and the total size of the mailboxes. Each mail server has a webmail (Bulwark). A mailbox can encrypt its stored mail with OpenPGP keys that the panel makes. An administrator makes mail clusters of 3 or 5 servers

  Where: `api/app/Http/Controllers/Api/MailDomainController.php`, `api/app/Services/Mail.php`, `agent/internal/actions/mail.go`, `agent/internal/stalwart`, `images/mail`, `web/src/pages/EmailPage.tsx`, `cli/cmd/cf/mail.go`

- Uptime checks: the panel requests up to 5 addresses of each public website on a schedule (each 1 to 60 minutes), from the panel server. A check can expect an HTTP status and a text. After the number of failed requests in a row that the check has, the panel opens an incident and sends the email `uptime.down`, and sends `uptime.up` when the address answers again. Plugins get the same events. The panel connects only to public addresses that it checked, and does not follow redirects. The **Uptime** tab of a website shows the last 24 hours, the response time, the last 30 days and the incidents. The page **Uptime checks** shows the checks of all websites. `cf uptime-checks` does the same work

  Where: `api/app/Services/Uptime`, `api/app/Http/Controllers/Api/UptimeController.php`, `web/src/pages/website/UptimeTab.tsx`, `web/src/pages/UptimePage.tsx`, `cli/cmd/cf/uptime.go`

- File manager, logs, cron jobs and deploys from Git for each website, in the API, the agent, the web panel and the CLI. The file manager makes and extracts archives (zip, tar, tar.gz and tar.bz2) as jobs, and changes permissions, also for a full directory. The agent does all of this work as the Unix user of the website, and the root helper has no new operation for it

  Where: `api/app/Http/Controllers/Api/FileController.php`, `LogController.php`, `CronJobController.php`, `DeployController.php`, `agent/internal/actions/files.go`, `cron.go`, `deploy.go`, `agent/internal/sitefiles`, `web/src/features/FileManager.tsx`, `web/src/pages/website/`, `cli/cmd/cf/sitetools.go`

- Protection of each website in the edge proxy: a firewall (Coraza with the OWASP Core Rule Set) in the mode off, detect or block, with rules that are off and paths with no firewall, the last firewall events, IP address allow and block lists, password-protected paths and a sign-in limit for each IP address. The **Security** tab of a website, `cf security` and `/v1/websites/{id}/security`. The change applies with no restart of the website. Not tested on a real server: the edge image with the two new Caddy modules in gVisor

  Where: `agent/internal/caddy/security.go`, `agent/internal/actions/security.go`, `api/app/Services/WebsiteSecurity.php`, `api/app/Http/Controllers/Api/SecurityController.php`, `web/src/pages/website/SecurityTab.tsx`, `cli/cmd/cf/security.go`, `images/edge/Containerfile`

- Run a command and the command log: a user runs one command line in the container of a website, without a terminal, in the web panel (**Commands**), the API and the CLI (`cf commands run`). The panel records each command before it runs: the user or the token, the time, the IP address, and then the exit code and the end of the output

  Where: `api/app/Http/Controllers/Api/WebsiteCommandController.php`, `agent/internal/actions/command.go`, `web/src/pages/website/CommandsTab.tsx`, `cli/cmd/cf/commands.go`

- The web terminal: a user opens a shell in the container of a website, as the website user, in the web panel (**Commands**). The terminal relay of the panel connects the browser to the agent. The command log keeps each command line of the session. The panel keeps each session with a recording of the output

  Where: `agent/internal/actions/terminal.go`, `agent/internal/termlog`, `agent/internal/relay`, `api/app/Http/Controllers/Api/TerminalSessionController.php`, `web/src/features/WebTerminal.tsx`

- Deploys from Git that start by hand, from a signed webhook (GitHub, GitLab, Gitea, Forgejo, Gogs and Bitbucket) or from a check of the branch on a schedule (FR-1004). A health check for 5 minutes after the switch, and an automatic return to the previous release when it fails (FR-1008). A migration command before the switch: after a migration, the panel keeps the release and sends an email (FR-1009)

  Where: `api/app/Services/Deploys/`, `api/app/Http/Controllers/Api/DeployController.php`, `agent/internal/actions/deploy.go`, `branches.go`, `web/src/pages/website/DeploysTab.tsx`

- Previews of pull requests: a website for each pull request to the branch of a Git source. The webhook makes the preview when the pull request opens, deploys each new commit, and removes the preview when the pull request closes. It reads the events of GitHub, GitLab, Gitea, Forgejo, Gogs and Bitbucket Cloud, and ignores a pull request from a fork

  Where: `api/app/Services/Previews.php`, `api/app/Services/Deploys/PullRequest.php`, `api/app/Http/Controllers/Api/PreviewController.php`, `web/src/pages/website/PreviewsCard.tsx`, `cli/cmd/cf/previews.go`

- DNS hosting: a customer makes zones and records, and the plan of the customer gives the number of its zones. Each server with the role `dns` has one Knot DNS container in gVisor. The panel sends each change to each DNS server, makes the name server records and the address records of the websites, sends the zones again to a DNS server that does not have them (after it was offline or lost its data), checks the name servers at the registrar, and signs a zone with DNSSEC when the user turns it on

  Where: `api/app/Services/Dns`, `agent/internal/actions/dns.go`, `images/dns`, `web/src/pages/DnsPage.tsx`, `cli/cmd/cf/dns.go`

- Adminer for MariaDB and PostgreSQL databases: **Open in Adminer** in the web panel and `cf databases adminer`. Each customer has one Adminer container in gVisor on the panel server, which connects only to the database servers of the customer. The panel signs the user in with a temporary database user for one database, and removes the user after 60 minutes

  Where: `api/app/Services/DatabaseTools.php`, `api/app/Http/Controllers/Api/DatabaseToolController.php`, `images/adminer`, `web/src/features/DatabasesPanel.tsx`, `cli/cmd/cf/app.go`

- OpenAPI 3.1 document, with a test that compares it to the routes

  Where: `openapi/`, `api/tests/Feature/OpenApiTest.php`

- Agent: websites, limits, runtime changes without downtime, domains and TLS, MariaDB, PostgreSQL and Valkey database servers for each customer (one container for each database server, with limits for the CPU, the memory and the disk space), backups and restores, host security updates, reboots, gVisor install and rollback, signed self-update with rollback

  Where: `agent/`

- A privilege boundary in the agent: a worker with the user `cf-agent` has the network port and the jobs, and a root helper does only the commands, the operations and the connections of its lists

  Where: `agent/cmd/cf-agent/serve.go`, `agent/internal/gate`, `agent/internal/actions/policy.go`

- The mesh: encrypted WireGuard tunnels between the servers. Each server makes its own key, and the panel is the directory of the public keys. An administrator makes, repairs and removes tunnels and makes new keys on the **Network** page and with `cf mesh`

  Where: `agent/internal/mesh`, `agent/internal/actions/mesh.go`, `api/app/Services/Mesh.php`, `web/src/pages/NetworkPage.tsx`, `cli/cmd/cf/mesh.go`

- The networks of the customers: each customer has the network `default`. A website connects to each database server of its customer by the name `<name>.internal` and the usual port of the engine, on the same server and on a different server. The panel makes and removes the tunnels that the networks need. A website can have a database in a database server on a different server: the agent of that server runs the jobs for the database and its users. The agent of that server also makes the backup of such a database, as a second snapshot in its own repository. A website is also a member: a different website of its customer connects to it by the name `<unix-user>.internal` on port 80, on the same server and on a different server. A website can have no public address: a private website has no domain and answers only in its networks, and a worker runs a command of the customer and serves no HTTP. Each one is a website of the plan. Not done yet: a staging copy does not include a database on a different server, and the kind of a website cannot change. A customer can make named networks in addition to `default`, when the plan lets it (`networks` of the plan): the page **Private networks**, `cf networks` and `/v1/networks`. An administrator can move a database server to a different server: the data goes through the tunnel between the two servers, and the database server keeps its name and its member address (**Move to a different server**, `cf database-servers move` and `/v1/database-servers/{id}/move`). A test makes the networks at the reference size: 20 servers, 100 websites and 20 database servers

  Where: `agent/internal/mesh/network.go`, `agent/internal/actions/network.go`, `agent/internal/actions/move.go`, `api/app/Services/Networks.php`, `api/app/Services/Mesh.php`, `api/app/Http/Controllers/Api/NetworkController.php`, `api/tests/Feature/NetworkScaleTest.php`

- Plugins: programs outside the panel with their own token and only the scopes of a signed manifest, signed webhooks for the audit, job and named events, pages in a frame with a short page token, action buttons on websites, servers and customers, before hooks that can refuse a change, `cf-NAME` commands, and plugins that the panel runs from a signed image in a gVisor container. The page **Plugins**, `cf plugins` and `/v1/plugins`

  Where: `api/app/Http/Controllers/Api/PluginController.php`, `api/app/Services/Plugins`, `api/app/Services/Plugins/Hosting.php`, `api/app/Jobs/DeliverPluginEvent.php`, `web/src/pages/PluginsPage.tsx`, `web/src/pages/PluginFramePage.tsx`, `web/src/components/PluginActions.tsx`, `cli/cmd/cf/plugins.go`, [plugins.md](plugins.md)

- Packs: signed manifests from publishers that add a website runtime, for example Go, with no new agent code. The stable contracts `cf-pack/1`, `cf-runtime/1` and `cf-php/1`, with test cases that the panel and the agent share. A `cf-php/1` pack adds versions of PHP to FrankenPHP or to Apache + PHP-FPM. Each release has two packs that the release key signs: PHP 8.3 and 8.2 for each PHP runtime, for websites that come from cPanel (FR-301). An administrator installs, updates and removes a pack on the page **Packs**, with `cf packs` and `/v1/packs`. The panel sends the packs to each web server and sends them again when a server reports different packs. Each server lets Podman pull an image of a pack only with the signature of the publisher. A database pack (`cf-pack/2`, `cf-database/1`) adds a database engine: the image has the program `/cf/manage`, and the agent runs it for the databases, the users, the sizes, the backups, the restores and the staging copies. Each database pack gets a block of 50 host ports, and each database server keeps the version of the engine that it got. The example database pack `plugins/packs/mysql` adds MySQL 8.4, with a contract test against a real MySQL server; the release does not publish it. Customers select the engine and its version for a new database server, with `cf database-servers create --version` and `GET /v1/database-engines`. Not done yet: a database server of a pack engine cannot move, has no Adminer and no access from anywhere, and cannot change its version. Not tested on a real server: the image policy of a pack with Podman and a real registry, and a database server of a pack engine in gVisor

  Where: `agent/internal/packs`, `agent/internal/actions/packs.go`, `api/app/Services/Packs`, `api/app/Http/Controllers/Api/PackController.php`, `web/src/pages/PacksPage.tsx`, `cli/cmd/cf/packs.go`, `agent/internal/actions/engine_pack.go`, `api/app/Services/DatabaseEngines.php`, `schema/pack-vectors.json`, `plugins/packs`, [packs.md](packs.md)

- Certificates of the customers: uploads, and certificates with DNS validation from Let's Encrypt (also wildcard certificates) through the DNS hosting of the panel, with renewals. Each domain uses the best certificate of its customer. Zones at external DNS providers work too, and other DNS services can hold the TXT records through the interface `ChallengeDns`. Refer to [certificates.md](certificates.md)

  Where: `api/app/Services/Certificates/`, `agent/internal/caddy/certificates.go`, `web/src/pages/CertificatesPage.tsx`, `cli/cmd/cf/certificates.go`

- Mutual TLS between the panel and each agent, with pinned certificates

  Where: `agent/internal/server`, `api/app/Services/AgentClient.php`

- Rollouts in batches with a canary, a health check, a stop and a roll back

  Where: `api/app/Services/RolloutEngine.php`

- SFTP for each website in its own gVisor container on port 2200: SSH keys only (no passwords), locked in the directory of the website. The agent moves servers of release 0.1 from the SSH service of the host

  Where: `images/sftp`, `agent/internal/actions/sftp.go`, `api/app/Http/Controllers/Api/SftpController.php`

- Backups each night at the policy time, with retention and a weekly prune, in one repository with its own password for each server. Updates in the maintenance window. Checks for jobs and servers that stop answering

  Where: `api/app/Console/Commands/`

- Account import from cPanel, DirectAdmin, Plesk and HestiaCP (and VestaCP): the agent reads a backup (an upload in parts, or a download from an HTTPS URL) and gives a plan. An administrator selects the parts, and the panel makes the websites, domains, databases with their dumps, cron jobs (turned off), mail domains, mailboxes with their password hashes and messages, aliases, forwarders and DNS records with its own API. The page **Imports**, `cf imports` and `/v1/imports`. Not done yet: CyberPanel and Virtualmin backups, Plesk for Windows backups, and PostgreSQL databases. Not tested on a real server: refer to [imports.md](imports.md#not-tested-on-a-real-server)

  Where: `agent/internal/migrate`, `agent/internal/actions/imports.go`, `api/app/Services/Imports`, `api/app/Http/Controllers/Api/ImportController.php`, `web/src/pages/ImportsPage.tsx`, `cli/cmd/cf/imports.go`, [imports.md](imports.md)

- `cf` CLI for all public API areas, with `--json` and `--no-wait`. Lists read all pages

  Where: `cli/`

- Installation script with checks before changes, for a panel and for a new server

  Where: `install/`

- Shared read-only runtime images: one copy of each image on a server for all websites, so a new website starts with no pull

  Where: `agent/internal/images`

- Node.js (22, 24 and 26) and Python (3.13 and 3.14) websites: a start command, an install command that runs in a build container, a change of version with no stop, Git deploys, private websites and workers. Environment variables for each website, encrypted in the panel and written to a file that only the website user can read

  Where: `images/node`, `images/python`, `agent/internal/actions/deploy.go`, `api/app/Http/Controllers/Api/RuntimeController.php`, `web/src/pages/website/RuntimeTab.tsx`

- Images: FrankenPHP and Apache with PHP-FPM for PHP 8.4 and 8.5 (and PHP 8.2 and 8.3 in the packs of older PHP versions), Node.js and Python, the Caddy edge, the panel API and web, MariaDB, PostgreSQL, Valkey, SFTP and the mail server

  Where: `images/`

- CI, image scans, signed multi-arch releases with SBOMs, and a daily rebuild (the schedule is off, refer to [releasing.md](releasing.md#daily-image-rebuild))

  Where: `.github/workflows/`

## Not complete

- **Areas without an API**: The API of release 0.1 has no SSH shell and no system settings. The web panel has no pages for these areas. The web terminal and **Run a command** give a shell and commands in the container of a website. The CLI has no terminal.

- **File manager**: The file manager reads files up to 2 MiB and writes files up to 5 MiB. Use SFTP or an archive for large files. It cannot make a `.tar.bz2` archive or extract a `.7z` or `.rar` archive, or an archive with a password. It cannot change the owner of a file.

- **Logs**: The panel shows the last lines that the container of a website wrote. A website that was made before this release writes its log file after its next start. The panel has no search in old log lines and no access log of the edge proxy.

- **Deploys**: The build and the migration command run in the runtime image of the website. The PHP images have Git and Composer but not Node.js: to build front-end files with npm, use a Node.js website or build the files before the push. A website with a custom image cannot deploy from Git. A preview of a pull request does not get a copy of the databases or the files of the website, and the panel does not show its address on the pull request.

- **Staging copy**: A staging copy gets the files only. It keeps the database settings of the original website, so it uses the live databases.

- **Restore of a full website**: A restore of the full website restores the files only. Restore each database separately.

- **Email from the panel**: A new user gets a temporary password from the person who makes the user, not an email with a link. A user who lost the authenticator app cannot reset TOTP by email: an administrator or the reseller resets it.

- **Known security limits**: Refer to [security.md](security.md#known-limits): image tags in the signature policy, service images between releases, and one set of object storage credentials for all servers.

- **Disk limits**: The agent applies the disk limit of each website and each database server with a user quota. The installer turns the user quotas on for an ext4 file system. For XFS, you turn them on. On a file system with no user quotas, each new website and each new database server with a disk limit gets a disk image, which needs loop devices. A website that has a directory on such a file system keeps it, and has no disk limit: the agent does not move its files into a disk image. A server with no user quotas and no loop devices has no disk limits. Refer to [installation.md](installation.md#disk-quotas).

- **gVisor benchmark**: Release 0.1 must measure websites, MariaDB and DNS in gVisor (R-08). This is not done.

- **Single sign-on**: Single sign-on works only on the panel domain, not on the panel domain of a reseller brand. The panel does not make a user at the first sign-in: an administrator or a reseller makes each user first. The panel does not read groups or roles from the provider, does not accept a sign-in that starts at the provider, and does not sign the user out of the provider. The `cf` CLI cannot sign in with a provider. Nobody has tested the providers on a real panel yet: the tests use a fake OpenID Connect provider and SAML answers that the tests sign.

- **Passkeys**: The `cf` CLI cannot add or use a passkey: it signs in with a password and TOTP. Passkeys do not work on a panel that has no domain, for example on an IP address. Nobody has tested the passkeys of Windows Hello and of Android on a real panel yet.

- **Email on a real server**: The tests of the mail server use a Stalwart container with Docker, not the unit of the agent with Podman and gVisor. Nobody has turned on email on a real server yet. Test these items there: the first start in the recovery mode, the nftables redirect of the mail ports, the rules for the user `cf-mail`, the copy of the certificate from the edge proxy, and delivery to and from other mail servers. The panel has no spam settings and no backup of the mailboxes.

- **Webmail on a real server**: A test starts Bulwark, Stalwart and Caddy with Docker, and a browser signs in through the routes of the agent. The unit with Podman and gVisor, the firewall chain `webmail` and the connection from the webmail to the public address of its own server are not tested on a real server. The image installs the PGP plugin. In a browser test, the plugin gets the key from the files of the mailbox after one password prompt, and decrypts a message. A mailbox that had keys before this change gets the key file at its next change in the panel. A release of the image must also give the source of the plugin (GPL-3.0); the Containerfile names the commit.

- **Mail clusters on real servers**: A test makes a cluster of three members with Docker, stops one member while mail arrives, and starts it again. WireGuard, the nftables rules, Podman with host networking and Linux 6.12 on the hosts are not tested. After a member fails, the cluster store refuses some locks for about 10 minutes, so some senders get a temporary error (452) and send again. In one of two test runs, one sender got this error for more than 15 minutes, and the test failed. The cause is not known yet. The panel cannot add, replace or remove a member, cannot remove a cluster, and cannot move a mail server with its own data into a cluster. The shared name has no valid certificate.

- **Tests on real servers**: The tests use fakes for the system commands. The Caddy routes have a test with a real edge container. Nobody has installed release 0.1 on a real Debian 13 or Ubuntu 26.04 server yet. Test these items on a real server: the Caddy admin socket in gVisor (`--runtime-flag=host-uds=create`), the route from the edge to a website through the private service address `169.254.77.1`, the MariaDB client host of a website container and the nftables rule between websites (all checked with Debian 13 packages in a container only, with cgroup v1 and no systemd), file owners with `--userns=keep-id` in gVisor, the shared image store with fuse-overlayfs under systemd and AppArmor, a restore with the PostgreSQL group roles, the firewall rules for the connections from the websites (checked with nftables in a network namespace only), the PostgreSQL container of the panel, the tunnels of the mesh with the WireGuard of the kernel (checked between network namespaces with `wireguard-go` only), the connection of a website to a database server on a different server by its network name (checked between network namespaces with `wireguard-go`, a gVisor container for the website and a test program in place of the database server), the connection of a website to a different website by its network name (checked the same way, with a test program in place of the website container), the move of a database server between two servers through the tunnel (checked with a pipe in place of the tunnel only), a reboot from the panel, `needrestart` after an update, backups to real object storage, the log file of a website container (`--log-opt path=` with the `k8s-file` driver in rootless Podman), a cron command and a command of **Run a command** with `podman exec` in gVisor (with the time limit of `timeout` in the container), the web terminal with `podman exec -it` in gVisor through the root helper (the PTY, `SIGHUP` to the shell when the connection closes, and the route of the edge proxy to the terminal relay with a WebSocket for 12 hours), and a deploy: the build container with `systemd-run --user`, a clone with the deploy key, and the switch to the new release.

- **DNS: not complete**: The panel does not import the records of a different DNS provider, it has no secondary DNS (zone transfers to or from other providers), and it does not change a DNSSEC key on a schedule. Nobody has run the container `cf-dns` on a real server yet.

- **Migration, Terraform**: Account import from CyberPanel and Virtualmin, and Terraform, are in releases 0.2 to 1.0.
