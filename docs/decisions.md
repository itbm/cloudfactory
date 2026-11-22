# Decisions about the open questions

The product specification, section 15, has seven open questions. Release 0.1 uses these
answers. The project can change them later.

| No. | Question | Decision in release 0.1 | Reason |
| --- | -------- | ----------------------- | ------ |
| 1 | Which language for the panel API? | PHP with Laravel 13. | The project owner selected Laravel 13. |
| 2 | Do we accept Docker as a second container runtime? | No. Podman only. | Rootless Quadlet units and the signature policy are Podman features. |
| 3 | Which second web server for `.htaccess` files? | Apache with PHP-FPM. | Apache reads `.htaccess` files without changes. The image is `apache-php<version>`. |
| 4 | Which billing system first? | Not decided. | Billing is not in release 0.1. |
| 5 | Preview websites for pull requests in 1.0? | Not decided. | Git deploys come in release 0.2. |
| 6 | The final product name? | Not decided. | The code uses "Cloudfactory". A trademark search is necessary (R-01). |
| 7 | How do rootless containers use ports below 1024? | An nftables redirect from each public port to a high port. | Refer to the next section. |

## Quadlet units, not a Compose file

The core containers (the edge proxy, the databases and the panel) are Quadlet units that the
agent writes, not a `compose.yaml` file. The reasons:

- systemd starts, stops and restarts each container, also at boot, with no extra service. It
  logs to the journal, and the agent reads the state from systemd.
- Each website and each service runs as its own rootless Unix user. One Compose project runs
  as one user.
- The units set the gVisor runtime, the pasta network, the dropped capabilities and the
  limits of each container in one place. The agent checks them with tests.
- The panel servers and the other servers use the same method. A server operates when the
  panel is not available.
- Decision 2: Podman only. `podman compose` needs a second tool, for example
  `docker-compose` or `podman-compose`.

## PostgreSQL for the panel, not SQLite

The panel keeps its data in PostgreSQL, in the container `cf-panel-db`. Release 0.1 first used
one SQLite file. The reasons for the change:

- Three containers write to the database: the API, the queue worker and the scheduler. Each
  container is a gVisor sandbox. gVisor keeps a file lock in one sandbox, so the three
  containers do not see the locks of each other. Two writers at the same time can damage a
  SQLite file. A test in two sandboxes showed this.
- A database server has no such limit. It also locks one row at a time, so a long job does
  not stop the sign-in of a user.
- The project has a signed PostgreSQL image for the databases of the websites. The panel uses
  the same image, so there is no new software to keep up to date.

The cost is one more container on the panel server, with approximately 100 MB of memory.

SQLite stays for development and for the fast test run. The CI runs all API tests with SQLite
and with PostgreSQL.

To make a copy of the panel database, run this command as root on the panel server:

```sh
runuser -u cf-panel -- env XDG_RUNTIME_DIR=/run/user/$(id -u cf-panel) \
  podman exec cf-panel-db pg_dump --username=cloudfactory --format=custom cloudfactory >panel.dump
```

## One backup repository for each server

Each server with the role `web` has its own restic repository and its own repository password.
An earlier version used one repository and one password for all servers. The reasons for the
change:

- The agent needs the repository password to make a backup. With one password for all
  servers, a person who has root access to one server can read the backups of all servers.
- With one repository for each server, the panel sends a password only to its server.
- Each server removes its own old data. A weekly job of one server does not lock the
  repository of the other servers.

The costs:

- restic does not remove equal data between two repositories. Two servers with equal files
  keep two copies.
- An administrator must keep one password for each server.
- The credentials of the object storage are still the same for all servers. A person who has
  root access to one server can delete the data of all servers. Versioning or an object lock
  in the bucket prevents the loss. Different credentials for each server need a different
  procedure for each storage provider, so they are not in release 0.1.

## Temporary passwords, not invitations by email

The panel of release 0.1 sends no email. Thus a new user gets a temporary password, which the
panel shows one time to the person who makes the user. The user must set a new password at
the first sign-in. The first administrator from the installation script has the same rule,
because its first password is in the output of the script.

When no administrator can sign in, root on the panel server uses
`cf-agent panel reset-password`. Root access to the panel server is already full access to the
panel data, so this command gives no new access.

## A firewall range for the website users

The firewall must know which connections come from a website. pasta opens each connection of
a container as the Unix user of the container. Thus the agent makes each website user with a
UID from 20000 to 59999, and one nftables rule sends the new connections of this range to the
chain `websites`.

- A rule for each website needs a firewall change for each new website. One range needs none.
- The range is above the UIDs of the system users and the usual login users, and below 60000,
  where some distributions keep special users.
- The limits are in the firewall of the host, not in the container, because a website
  controls all software in its container.

## Ports below 1024

The installation script adds these nftables rules:

| Public port | Protocol | Redirect to |
| ----------- | -------- | ----------- |
| 80          | TCP      | 8080        |
| 443         | TCP      | 8443        |
| 443         | UDP      | 8443 (HTTP/3) |

The reasons:

- A redirect does not change the client IP address (FR-1707). The edge proxy sees the real
  address.
- A lower value for `net.ipv4.ip_unprivileged_port_start` lets all users open low ports. That
  includes the users of the websites.
- Rootful containers give more access to an attacker who escapes from gVisor.

The DNS server uses the same method: the agent adds redirects from port 53 (UDP and TCP) to
port 8053 when a server gets its first zone. Release 0.3 (email) will add a redirect for
port 25 in the same method.

## Knot DNS, and zones from the panel

The DNS server is Knot DNS in one container on each server with the role `dns`.

- Knot DNS is only an authoritative server. It has no resolver, and it signs zones with
  DNSSEC itself. PowerDNS needs a database on each server. BIND has a resolver and more code
  than this work needs.
- The panel sends each full zone to each DNS server in a job. The servers do not use zone
  transfers (AXFR) or notifications between them. Thus there is no primary server that must
  be in operation, no transfer key to keep, and no open port between the servers. The cost
  is one job for each DNS server and each change.
- Secondary DNS is only outbound: a DNS server outside the panel can copy the zones from the
  DNS servers of the panel, but the panel never copies a zone from a different server. The
  panel keeps the records, checks each record with the shared input rules, and gives each
  record to the plugins of the external DNS providers. A zone that a different server sends
  would skip these checks, and the panel would not be the only source of the records. To
  move a zone into the panel, import its zone file.
- Each DNS server of the panel is a primary server for the secondaries. A secondary can copy
  a zone from each DNS server, so the transfers continue when one DNS server is offline.
- The root helper writes the zone files from records that it checks. It does not accept the
  text of a zone file.
- The panel makes the DNSSEC key of a zone and gives it to each DNS server. With a key that
  each server made itself, the servers would give different signatures and the registrar
  would need one DS record for each server.

## Who checks the registry for new image tags

The panel checks the registry, not the agent (FR-1110). The reasons:

- One check is sufficient for all servers. The panel decides when a rollout starts, so it
  must know about a new tag first.
- The panel verifies the cosign signature of each new tag with the release public key before
  it shows the tag. Each agent verifies the signature again when it pulls the image, through
  the Podman signature policy (FR-1101).
- The agents do not need access to the registry API for tags. They only pull images.

The scheduler container (`cf-panel-scheduler`) runs `php artisan cloudfactory:check-images`
each hour. An administrator can also check one image now on the Updates page.

## SFTP in a container with OpenSSH, not SFTPGo

Release 0.1 used the SSH service of the host for SFTP. Later releases use one container for
SFTP on each server, on port 2200. The container runs OpenSSH (`internal-sftp`), not SFTPGo.

| Item | OpenSSH in a container | SFTPGo in a container |
| ---- | ---------------------- | --------------------- |
| Isolation between websites | Each session changes to the Unix user of its website, in a chroot. The kernel of gVisor checks each file access. | One process for all websites, as root. SFTPGo checks the paths itself. |
| Owner of new files | The website user, because the session runs as that user. | Root makes the file, then SFTPGo changes the owner. |
| Users and keys | Two files and one keys file for each user. No restart. | A user database or a REST API, with an admin password. |
| Extra parts | None. | A web admin UI, a REST API, FTP and WebDAV. Each one is more to secure. |
| Same as release 0.1 | Yes: the same keys files, chroot, keys only and no forwarding. | No. |

The reasons for the port 2200:

- Port 22 stays for the SSH service of the host. The administrators keep their access.
- 2200 is above 1024 and is not a port of a different Cloudfactory service. Set a different
  port with `--sftp-port` or `sftpPort`.

The reason for a container that root runs: each session must change to the UID of its website.
A rootless container can use only the UIDs of its own user. gVisor and the small set of
capabilities limit what root can do in the container.

## A root helper for the agent, with fixed lists

Until release 0.1.9, the agent was one root process. It ran many commands in directories that
the customers control, and three security faults had this cause. Later releases divide the
agent into a worker with the user `cf-agent` and a root helper (refer to
[architecture.md](architecture.md#the-processes-of-the-agent)).

| Item | Decision | Reason |
| ---- | -------- | ------ |
| Number of services | One service unit. The root helper starts the worker. | An update, a restart and `cf-agent rollback` operate as before. A server from an earlier release needs no new unit. |
| Which process is root | The process with the network port is not root. | The code that reads data from the network and from the customers is the most probable place of a fault. |
| Interface | Three lists: commands, operations and connections. No request has a path of root. | A list of fixed forms is small and a person can examine it. A general "run as root" request gives no protection. |
| Local connections | The root helper opens them for the worker. | The firewall of a server from an earlier release does not change, and the worker cannot connect to the database of the panel. |
| Failure of the worker start | The agent operates as one root process and tells the panel. | The websites of the server are more important than the boundary. The administrator sees the state and can correct it. |
| Earlier releases | The root helper does not install a release that has no root helper. It removes the previous binary of such a release 10 minutes after the worker starts. | A worker that an attacker controls cannot go back to an agent with one root process. The automatic rollback still protects the first minutes after the update. |

The boundary has limits: the worker can still act as each website user, and it manages the
database servers. [security.md](security.md#the-root-helper-of-the-agent) gives the full list.

## Stalwart for email, in one container for each server

| Item | Decision | Reason |
| ---- | -------- | ------ |
| Mail server | Stalwart, release 1.1 or later | One program has SMTP, IMAP, DKIM, SPF, DMARC and the size limits of the mailboxes, and a management API for each change. Postfix with Dovecot and Rspamd is three services with configuration files and no API. |
| Licence | AGPL-3.0, the licence of Cloudfactory | Cloudfactory uses the image of Stalwart with no change to the program. Mox (MIT) and Apache James (Apache-2.0) are alternatives with a different licence. A change to one of them replaces only the package `agent/internal/stalwart` and the image. |
| Number of containers | One for each server, for all customers | A server has one port 25 and one reputation of its address. The panel keeps the customers apart. |
| Ports | The mail server listens above 1024, and nftables redirects the public ports | The container has no capabilities and does not run as root. The edge proxy uses the same method. |
| Certificate | The edge proxy gets it, and the root helper copies it | The server has one ACME client, and the mail server needs no port 80. |
| Passwords | The panel makes a password and sends a bcrypt hash | The agent and the job records never have the password. |
| Webmail | Bulwark, on the host name of the mail server | Bulwark has the AGPL-3.0 licence and uses JMAP, which Stalwart has. It has mail, calendars and contacts. Stalwart 1.1 has no webmail. |
| Spam training | Not in this release | The first release has the parts that each mail program needs. |

## Mail clusters with the database and the cluster store on each member

| Item | Decision | Reason |
| ---- | -------- | ------ |
| Size | 3 or 5 servers | A cluster needs a majority after one failure. With an even number, the cluster stops when the network between the two halves fails. |
| Data store | MariaDB Galera, one node on each member | Each member has all the mail. The panel needs no external database. |
| Locks and counters | Stalwart Keep, one member on each server | Stalwart needs a shared in-memory store for a cluster. Keep replicates it with no external service. |
| Network | WireGuard between the members | The members can be in different regions. The database and the cluster store are never on a public address. |
| Host names | Each member has its own name and certificate. The cluster has a shared name for MX | Each member gets its certificate with the HTTP challenge, as a single mail server does. A certificate for the shared name needs a relay of the ACME challenge between the members, which this release does not have. |

## Generated OpenPGP keys

| Item | Decision | Reason |
| ---- | -------- | ------ |
| Who makes the keys | The panel, when the user asks | The user does not need a separate program. The user can still give a public key of their own. |
| Protection of the private key | The password of the mailbox | The user has one password for the mail program, the webmail and the key. |
| Decryption in the webmail | A plugin of the webmail | Bulwark has no OpenPGP decryption. The plugin "PGP True End-to-End" (GPL-3.0) decrypts in the browser. |
| Keys in the webmail | A file in the files of the mailbox, which a patch of the plugin reads | The user uploads no key. The mail server examines the password, so the panel needs no new sign-in and no new password check. The plugin has no access to the sign-in password of the webmail, so it asks for the password one time. |
| Installation of the plugin | A pinned copy in the image, installed by the start script | Each server has the same reviewed release, with no person in the admin dashboard. The dashboard stays closed on the public host name. A new release of the plugin comes with a new image. |
## WireGuard tunnels between the servers, with the panel as the directory of keys

The websites of a customer must be able to use a database on a different server. The traffic
between two servers goes through the internet.

| Item | Decision | Reason |
| ---- | -------- | ------ |
| Protection of the traffic | One WireGuard tunnel for each pair of servers that needs one. | The kernel has WireGuard, it has a small configuration, and it protects all protocols. TLS for each database engine needs certificates and settings for each engine and each client. |
| Who has the keys | Each server makes its own key pair. The panel has only the public keys and the pre-shared keys. | A copy of the database of the panel does not give the private keys of the servers. |
| Who can add a tunnel | Only the panel, through the agent connection. | The panel already has mutual TLS with each agent, and it knows which servers need a tunnel. |
| Commands from the panel to the agents | They stay on port 7443 with mutual TLS. They do not use the tunnels. | A fault in the mesh must not stop the panel from repairing the mesh. |
| Where root is necessary | The root helper has four operations: apply a state, give the status, make a new key, and apply the rules and the names of the networks. The worker cannot run `wg`, `ip` or `nft`. | The same boundary as for the other work of the agent. |
| What a tunnel permits | An echo request, and a connection to a network member from a server that has a website in a network with it. | A tunnel between two servers must not open the services of a server. Each function adds its own rule. |
| The name of a database server | `<name>.internal`, with one address of the mesh range that does not change. The name and the address are the same on each server. | An app keeps its configuration when the database server or the website is on a different server. `mariadb.internal` says nothing about which database server it is. |
| The port of a network name | The usual port of the engine. | Each database server has its own address, so each one can have the usual port. The port on the server stays for `mariadb.internal`. |
| How a website finds a name | A hosts file for each website, which the agent changes in place. | A new name needs no restart of the container. A DNS server on each server is one more service to operate and to protect. |
| How a connection gets to the container | The firewall changes the destination from the member address to the address of the container, on the server of the member. | The database containers do not change and do not start again. A website on the same server uses the same address as a website on a different server. |
| The client address that a database server sees | `169.254.80.1` for each website, also on a different server. | The database users from before the networks continue to operate, and the three engines need no rule for each server. The firewall and the password do the control of access. |
| Who makes the tunnels for a network | The panel, when a website on one server is in a network with a database server on the other. It removes them when no network needs them. | A customer does not know the servers. A tunnel that nobody needs is one more path to protect. |
| The network `default` | Each customer has it, and each new website and database server joins it. | A customer with one network needs no decision. It gives the same access as before on one server. |
| Test and repair | One action, **Repair**: the panel sends the state again, and each server tests the tunnel. The reports of the agents give the state of each tunnel each minute. | A test with no repair has no use for an administrator, and the state is on the page already. |

## Compatibility while the version is 0.x

| Item | Decision | Reason |
| ---- | -------- | ------ |
| Backwards compatibility | None while the version is 0.x. A release can rename or remove a column, a setting, an API field, a route or a CLI command. | The project owner decided it. There are no production installations yet. A compatibility layer for data that does not exist makes the code larger and slower to change. |
| Migrations | Change the migration that made the table, or make the new form directly. No migration moves old data. | Each 0.x release is a new installation. |
| The pack contracts | `cf-pack/1` and `cf-runtime/1` are stable. A change gets a new contract name. | Publishers outside the project make packs. They must not need to follow each change of the panel. |
| When it ends | Release 1.0. | Then installations have data to keep. |

## Packs: signed manifests that the panel sends to each server

| Item | Decision | Reason |
| ---- | -------- | ------ |
| What a pack can add | A website runtime of the type `app` only (`cf-runtime/1`). Database engines come later. | The agent already runs Node.js and Python this way, with the same isolation. A pack gives only data, so the agent runs no code of the publisher outside a website container. |
| The signature | ECDSA P-256 with SHA-256 over the exact bytes of the manifest (`cosign sign-blob`), and `cosign sign` on each image with the same key. | Plugins already use this format, and Podman checks cosign signatures. One key covers the manifest and the images. |
| Trusted keys | One list for plugins and packs (`/v1/trusted-keys`), and the release key. | An administrator trusts a publisher, not a type of extension. |
| Where the packs are | The panel keeps them. It sends all packs to each web server in one job, and sends them again when a report of the agent differs. | The panel is the source of the desired state, as for the zones of DNS and the mesh. A new server gets the packs after its first report. |
| The image policy | One `sigstoreSigned` rule for each repository of a signed pack, with the key of the pack. No rule for a repository that a rule of the installer covers. | A pack can then never make the rule of the release registry weaker. |
| Unknown keys | An error, except a key that starts with `x-`. | An agent must never ignore a key of a later contract. Publishers can still add their own data. |
| Updates of the images | The pack gives the digests. The panel does not check the registry for pack images. A new digest for an image in use waits for a rollout. | The signature of the manifest covers the digests. A new tag in the registry has no signature of the manifest. |
