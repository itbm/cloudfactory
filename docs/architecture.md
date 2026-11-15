# Architecture

This document describes the components of Cloudfactory release 0.1 and how they operate
together. The product specification, section 6, gives the requirements.

## Overview

```text
 Browser ──► web panel ─┐
                        ├─► panel API (Laravel) ──► queue worker ──► cf-agent on each server
 cf CLI ────────────────┘        ▲                    (Connect over mutual TLS, port 7443)
                                 │                                 │
                                 └──── job events and host reports ◄┘
```

- The **web panel** and the **`cf` CLI** use only the public API. The API has no private
  routes for the panel.
- The **panel API** keeps the desired state in its database. It does not change a server
  directly. It sends a typed job to the agent.
- The **agent** (`cf-agent`) runs on each server. It accepts only the actions in
  [`agent.proto`](../agent/proto/cloudfactory/agent/v1/agent.proto). It has no action that runs
  a shell command that the panel gives. The agent has two processes: a worker with the user
  `cf-agent`, and a root helper (refer to [The processes of the agent](#the-processes-of-the-agent)).

## Flow of a change

1. The client sends a request, for example "change the runtime of a website".
2. The API checks the request and the token scope. It records the desired state.
3. The API makes a job and sends status 202 with the job.
4. A queue worker sends the job to the agent. The worker uses the Connect protocol (JSON over
   HTTP/2) and mutual TLS.
5. The agent accepts the job and does the steps in the background.
6. The agent sends an event after each step to `/api/agent/v1/jobs/{id}/events`.
7. The API changes the state of the website, server or rollout to agree with the result.

The agent keeps each job in its data directory. If the agent stops during a job, it continues
the job when it starts again. A reboot job uses this method: the agent writes a marker, starts
the reboot and waits for the shutdown. After the reboot, the agent starts the containers,
checks the websites and only then sends the result.

Time limits make sure that a job always gets a result:

- The agent stops a job after 30 minutes. A backup, a restore, a host update and the weekly
  removal of backup data have a limit of 170 minutes.
- If a job has no event for 10 minutes, the panel sends the job again. The agent knows the job
  ID. It reports the result of a complete job again, and does not run the job a second time.
- If a job has no result after 3 hours, the panel sets the job to "failed".
- A server that does not report for 5 minutes is "offline". The panel does not call an offline
  server. The jobs of that server wait, and the panel sends them when the server reports again.
- The panel reads a job with a lock before each change of its state. Thus a late event does
  not open a complete job again.

## The processes of the agent

```text
 panel ──► port 7443 ──► worker (user cf-agent)
                           │  jobs, state, reports, connection to the panel
                           │
                           │  /run/cloudfactory-agent/gate.sock
                           ▼
                         root helper (root)
                           commands · operations · connections
```

systemd starts `cf-agent serve` as root. This process is the root helper. It makes the Unix
socket `/run/cloudfactory-agent/gate.sock`, starts the worker (`cf-agent work`) as the user
`cf-agent`, and waits. When the worker stops, the root helper stops with the same result,
and systemd starts the two processes again. Thus the service has one unit, and an update, a
restart and a rollback of the agent operate as with one process.

The worker has the agent port, the jobs, the state of the websites and the connection to
the panel. For each step that needs root, it sends a request through the socket. The root
helper has three lists, and it refuses a request that is not in a list:

- **Commands.** The worker gives the arguments of a command. The policy
  (`agent/internal/actions/policy.go`) accepts a program as the Unix user of a website, a
  command of a fixed form as a service user, and some root commands with fixed arguments.
- **Operations.** An operation is root work with a name and typed values, for example
  `user.ensure`, `unit.write` and `db.prepare` (`agent/internal/actions/root.go`). The worker
  gives names, for example a Unix user or the ID of a database server. The root helper makes
  each path from its own settings.
- **Connections.** The root helper opens a connection for the worker and gives the data
  through the socket (`agent/internal/actions/dial.go`): the admin socket of the edge proxy,
  some ports on the private service address, and the Valkey database servers.

The code of an action is the same for the two sides. A function that needs root starts with
a check: in the worker, it sends the operation to the root helper. In the root helper, it
does the work. If the root helper cannot start the worker, it does the work of the worker
itself, as one root process.

[security.md](security.md#the-root-helper-of-the-agent) gives what the boundary protects and
what it does not protect.

## Input rules of the panel and the agent

The panel checks each value before it makes a job. The agent checks each value again before it
uses the value in a file path, a unit name or a command. The two checks must agree. If the
panel accepts a value that the agent refuses, the user sees an error only after the job fails.

Thus the rules are in one file, [`schema/input-rules.json`](../schema/input-rules.json). A
generator writes the Go checks of the agent and the PHP rules of the panel from this file. The
tests of the two components run the same examples
([`schema/input-vectors.json`](../schema/input-vectors.json)). CI makes sure that the generated
files are current. Refer to [development.md](development.md#input-rules).

A pattern in the rules file uses only the syntax that Go and PHP read in the same way. Lengths
are in bytes in the two languages.

## Jobs that the agent refuses

The agent answers a request that it refuses with an HTTP status from 400 to 499 and a message,
for example `the PHP extension "x" is not valid`. A second attempt gets the same answer. Thus
the panel does not try again: the job fails immediately, and the job shows the message of the
agent.

These errors are temporary, and the panel tries again 4 times in approximately 6 minutes: no
connection, a status from 500 to 599, status 408 and status 429.

## States of websites, jobs and rollouts

A website, a job and a rollout each have a state. Each state has a table of the changes that
are permitted. The tables are in the enums `WebsiteStatus`, `JobState` and `RolloutState` in
[`api/app/Enums`](../api/app/Enums).

| Record  | State         | Can change to                           |
| ------- | ------------- | --------------------------------------- |
| Website | `starting`    | `running`, `stopping`, `stopped`, `failed` |
| Website | `running`     | `stopping`, `stopped`, `starting`, `failed` |
| Website | `stopping`    | `stopped`, `running`, `starting`, `failed` |
| Website | `stopped`     | `starting`, `running`, `failed`         |
| Website | `failed`      | `starting`                              |
| Job     | `queued`      | `running`, `succeeded`, `failed`        |
| Job     | `running`     | `queued`, `succeeded`, `failed`         |
| Job     | `succeeded`   | No change                               |
| Job     | `failed`      | No change                               |
| Rollout | `running`     | `paused`, `done`, `rolled_back`         |
| Rollout | `paused`      | `running`, `rolled_back`                |
| Rollout | `done`        | No change                               |
| Rollout | `rolled_back` | No change                               |

The model checks each change of the state against the table when it saves the record
(`App\Models\Concerns\GuardsState`). The check reads the state in the database, because a
different request can change the record at the same time. There are three results:

- A request of a user that the table does not permit gets HTTP status 409 with a message, for
  example "The rollout is rolled back already."
- A late result from a server that the table does not permit changes nothing. The panel writes
  a warning to its log. For example, the result of an old power job does not change a website
  with the state `failed`.
- The actions "pause", "resume" and "roll back" of a rollout lock the row of the rollout first.
  Thus two requests at the same time cannot do one action two times.

A test makes each change between each two states of each table
(`api/tests/Feature/SchemaAndStatesTest.php`). If you change a table, change the test and this
section too.

## A website on a server

Each website has:

- One Unix user, with a UID and a GID from 20000 to 59999. The user owns the files of the
  website in `/srv/cloudfactory/sites/<user>`. The firewall limits the connections of the
  users in this range (refer to [security.md](security.md#firewall)).
- One rootless Podman container. A Quadlet unit in `/etc/containers/systemd/users/<uid>/`
  starts it. The container uses the gVisor runtime `runsc`.
- The `pasta` network mode. Each container has its own network namespace.
- One port on the private service address `169.254.77.1` from the range 20000 to 29999. The
  agent takes and records a port in one locked step, so two websites never get the same port.

### The private service address

The installer adds `169.254.77.1/32` to the loopback interface (`cloudfactory-address.service`).
This address never leaves the server. The website ports, the database ports and the panel
ports are open only on it.

The ports are not on `127.0.0.1`, because of gVisor. pasta sends a connection from
`127.0.0.1` to `127.0.0.1` in the network namespace of the container, and gVisor
(`network=sandbox`) does not listen there. A connection to `169.254.77.1` comes from a
different address, so pasta sends it through its network device into gVisor. Rootless Podman
bridge networks have the same problem for all connections, so websites do not use them.

An nftables rule refuses a new connection to a website port on `169.254.77.1` unless it comes
from root or from the user `cf-edge`. pasta opens each connection as the Unix user of the
container, so a website cannot connect to a different website.

The database servers are not on this address. Each customer has its own database servers on
an address of the database network. Refer to [Databases](#databases).

### Shared runtime images

All websites on a server share one read-only copy of each runtime image. A new website with an
image that the server has already starts with no pull, and 100 websites keep one copy of the
image, not 100.

- The agent pulls each runtime image one time, as root, into its own Podman store in
  `/var/lib/cloudfactory-images/<sha256 of the image reference>`. The signature policy checks
  the image when the agent pulls it. Only a reference with a digest goes in the shared store.
  The image of a database server is the exception: its reference has the version of the
  agent as its tag, and a release does not change the content of its tags.
- The agent pulls into a temporary store. Then it lets the website users read the metadata
  of the store and gives the store its final name in one step. After that, nothing writes the
  store again. Podman writes its metadata with mode `0600` each time that it changes a store,
  so a store that changes while websites read it can stop a website for a short time.
- `/etc/cloudfactory/website-storage.conf` lists the stores as `additionalimagestores`. The
  file `~/.config/containers/storage.conf` of each website user is a link to it. Root owns
  `~/.config/containers`. Each website user keeps only its own small writable container layer
  in its home directory.
- The website users use `fuse-overlayfs`. The containers use `--userns=keep-id`. With the
  overlay driver of the kernel, Podman must copy each image for each user to change the file
  owners. fuse-overlayfs changes the owners without a copy. Before the shared store, each
  website kept about 1.2 GB: the image and its copy.
- In the container, the files of the image have the owner `nobody`. The website process runs
  as the website user and reads them as "other", the same as before.
- A custom image stays in the storage of its website user, so other customers never see it.
- After a runtime change, the agent removes the previous image from the storage of the website
  user, if it is there: a custom image, or a copy from before the shared store. Then it removes
  the shared images that no website uses, in two steps: first from `website-storage.conf`, then,
  at the next prune, the files. The agent also prunes after it deletes a website and once a
  day. It keeps an image that a job asked for in the last hour, and each image that a
  container unit of a website or of a database server names.

The container unit of a website sets these limits:

- `NoNewPrivileges=true` and `DropCapability=all`.
- A read-only root file system. Only the website directory and `/tmp` are writable.
- Memory, CPU, process and disk I/O limits from the plan of the website. These limits are
  systemd properties of the service (`MemoryMax`, `CPUQuota`, `TasksMax`,
  `IOReadBandwidthMax` and `IOWriteBandwidthMax`), not Podman options. Each gVisor container
  starts with `--runtime-flag=ignore-cgroups`, so the sandbox stays in the cgroup of its
  service. Without this flag, runsc makes its own cgroup and must write
  `/sys/fs/cgroup/cgroup.subtree_control`, which a rootless user cannot do. A change of the
  limits in the panel uses `systemctl set-property` on the running service and also changes
  the unit.

## The edge proxy

One Caddy container on each web server receives all HTTP and HTTPS traffic. It runs as the
user `cf-edge`, in gVisor, with the `pasta` network mode. It listens on port 8080 and port 8443.
The firewall redirects port 80 to 8080 and port 443 to 8443 (refer to
[decisions.md](decisions.md)). It also redirects the connections from the server itself to its
own addresses, for example from a browser on the server to `localhost`.

pasta sends a connection from a loopback address (`127.0.0.0/8` or `::1`) to the loopback
interface in the network namespace of the container. gVisor does not listen there, so pasta
closes the connection. Thus the firewall changes the source address of a redirected IPv4
connection from a loopback address to `169.254.77.1`. IPv6 has no private service address, so
the firewall does not redirect a connection from `::1`. Nothing listens on port 80 or 443, so
the connection is refused, and the browser then uses `127.0.0.1`.

The agent changes the Caddy routes through the Caddy admin API. Each website has one route.
The route sends traffic to `169.254.77.1:<port>`. Caddy gets the TLS certificates
automatically. The agent starts the edge container before it adds the first route, also
during the panel installation.

## SFTP for the websites

One SFTP container, `cf-sftp`, on each server serves all websites. It listens on port 2200 (the
setting `sftpPort` in `/etc/cloudfactory/agent.json`). The SSH service of the host is only for
the administrators. The image `sftp` has OpenSSH and `libnss-extrausers`.

```text
 SFTP client ──► port 2200 ──► cf-sftp (root Podman, gVisor, bridge network)
                                 │ sshd: key check, chroot, change to the website user
                                 ▼
                     /srv/cloudfactory/sites/<user>/app  (owner: the website user)
```

- Root runs the container, with a Quadlet unit in `/etc/containers/systemd/cf-sftp.container`.
  A rootless container cannot change to the UIDs of the website users.
- The container uses the bridge network of Podman, because Podman has the `pasta` network
  mode only for rootless containers. Podman publishes the SFTP port with a destination NAT
  rule, so the connection comes through the network device into gVisor, and sshd sees the
  real client address. The firewall drops the traffic from the bridge to the ports on
  `169.254.77.1`. The bridge has IPv4 only, so an SFTP client must use the IPv4 address of
  the server.
- Podman adds the NAT rule to the firewall when the container starts. A start of
  `nftables.service` removes all rules, so the unit starts the container again after it
  (`PartOf=nftables.service`). After `systemctl reload nftables`, run
  `systemctl restart cf-sftp`.
- The container mounts these host directories:

  | Host directory | In the container | Access |
  | -------------- | ---------------- | ------ |
  | `/srv/cloudfactory/sites` | `/srv/cloudfactory/sites` | Read and write |
  | `/etc/cloudfactory/sftp` (one `<user>.keys` file for each user) | `/etc/cloudfactory/sftp` | Read only |
  | `/etc/cloudfactory/sftp-users` (`passwd` and `group`) | `/var/lib/extrausers` | Read only |
  | `/var/lib/cloudfactory/sftp-host-keys` | `/etc/ssh/keys` | Read and write |

- The agent writes the keys file of a website for each SFTP change. Then it writes `passwd` and
  `group` again, with each website user that has a key, and the UID and GID of the user on the
  host. sshd reads these files at each sign-in, so the container does not restart.
- After the sign-in, the session runs as the website user, with the same UID and GID as on the
  host. A new file has the correct owner on the host and in the website container
  (`--userns=keep-id`). The umask is `0027`.
- The agent starts the container when it starts, and checks that port 2200 answers on
  `169.254.77.1`. A new agent version brings a new image and restarts the container.
- On a server from release 0.1, the agent then removes the SFTP configuration from the SSH
  service of the host. Until this step is complete, the agent reports port 22 to the panel.
  Refer to [installation.md](installation.md#servers-that-you-installed-with-release-01).

## Email

A server can have one mail server for the mail domains of the customers. An administrator
turns it on for a server with a host name (`PUT /v1/servers/{id}/mail`, or
`cf servers mail SERVER --hostname NAME`). The mail server is Stalwart, release 1.1 or later,
in the container `cf-mail` of the Unix user `cf-mail`. The image `mail` is the image of
Stalwart with no file capabilities.

```text
 other mail servers ──► port 25  ─┐
 mail programs ──► 465, 587, 993 ─┼─► nftables redirect ──► cf-mail (rootless Podman, gVisor)
                                  │      ports 2025, 2465, 2587 and 2993
 worker of the agent ──► root helper ──► 169.254.77.1:19025 ──► management API (port 8080)
```

One container has the mail of all customers of the server, because a server has one port 25.
The panel keeps the customers apart: each mail domain has one customer, and the panel sends a
job only for a domain of that customer.

| Part | Where | Notes |
| ---- | ----- | ----- |
| Mail domains, mailboxes, aliases and forwarders | The database of the panel, and the data store of the mail server | The panel is the source. The agent applies each change through the management API (`agent/internal/stalwart`). |
| Settings of the mail server | The data store of the mail server, in `/var/lib/cf-mail/data` | The file `/etc/cloudfactory/mail/config.json` names only the data store. Root owns it. |
| Password of the administrator of the management API | `/etc/cloudfactory/mail/admin.env` | Only root and the group `cf-mail` can read the file. The root helper gives the password to the worker. |
| TLS certificate | `/etc/cloudfactory/mail/certs` | The edge proxy gets and renews the certificate of the mail host name. The root helper copies it for the mail server, at the start of the agent and one time each day. |
| Passwords of the mailboxes | The mail server keeps a bcrypt hash | The panel makes the hash and sends only the hash. The panel shows the password one time. The agent never has it. |

The first start of the mail server is in the recovery mode of Stalwart, which has only the
management API. The agent sets the listeners, the host name and the certificate, and then
starts the mail server again. Thus the mail server never listens with the default settings of
its image.

A new mail domain gets two DKIM keys from the mail server. The result of the job has the DNS
records that the owner of the domain adds: MX, SPF, DKIM and DMARC. The panel reads the DNS
when the user asks (`POST /v1/mail/domains/{id}/check`) and shows which records it found.

The status report of the agent has the state of the mail server and the size of the messages
of each mailbox. The plan of a customer gives the number of its mailboxes and the total size
of these mailboxes.

### Encryption of the stored mail (OpenPGP)

A mailbox can have an OpenPGP public key. The mail server then encrypts each new message with
that key before it stores the message. Thus a copy of the data store does not show the mail.
There are two methods:

- The panel makes the keys (`encrypt: true` on `POST /v1/mail/mailboxes` or
  `PATCH /v1/mail/mailboxes/{id}`). `App\Services\OpenPgpKey` makes an Ed25519 key for
  signatures and a Curve25519 key for encryption. The password of the mailbox protects the
  private key (S2K iterated and salted SHA-256, AES-256). The panel keeps the private key
  encrypted with the key of the application, and sends only the public key to the agent.
- The user gives a public key of their own (`publicKey`). The panel then has no private key.

The user downloads the private key (`GET /v1/mail/mailboxes/{id}/private-key`, or
`cf mail mailboxes private-key ADDRESS`) and adds it to a mail program or to the PGP plugin of
the webmail. Each download is an audit event. A new password protects the private key again
when the user gives the current password. Without the current password, the panel keeps the
old protection and shows that the key has an older password (`privateKey.passwordCurrent`).

The mail server encrypts only new mail. It does not encrypt the messages that the mailbox has
before the key, and it cannot decrypt the stored mail. Search on the server does not find
words in an encrypted message.

### Webmail (Bulwark)

Each mail server has a webmail: Bulwark, release 1.21 or later, in the container `cf-webmail`
of the Unix user `cf-webmail`. The webmail is on the host name of the mail server.

```text
 browser ──► https://MAIL-HOST ──► edge proxy ─┬─► /.well-known/jmap, /jmap/*, /dav/* ──► 169.254.77.1:19025 (mail server)
                                               └─► all other paths ──► 169.254.77.1:19027 (cf-webmail)
 cf-webmail ──► https://MAIL-HOST (examines each sign-in through the edge proxy)
```

The browser uses one origin for the webmail and for JMAP, thus the mail server needs no CORS
settings. The edge proxy does not send `/api` to the mail server: the management API is not
on the public host name. Mail programs use the same name for CalDAV and CardDAV
(`https://MAIL-HOST/dav`).

| Part | Where | Notes |
| ---- | ----- | ----- |
| Settings and secret | `/etc/cloudfactory/webmail/webmail.env` | Only root and the group `cf-webmail` can read the file. The secret encrypts the settings of the users. The agent keeps it when the settings change. |
| Settings of the users | `/var/lib/cf-webmail/data` | Encrypted with the secret. The webmail keeps no mail. |
| Root certificate of the panel CA | `/etc/cloudfactory/webmail/ca.crt` | Only for a host name that is not public, for example `mail.example.test`. |

The webmail sends no telemetry and does not look for updates: the release of the agent sets
the image. Its admin dashboard is off.

### Mail clusters

A mail cluster puts the same mail on 3 or 5 servers with the email role. When one server
fails, the other servers continue to receive and deliver mail. The servers can be in
different regions. An administrator makes a cluster (`POST /v1/mail-clusters`,
`cf mail clusters create`, or the Servers page).

Each member has:

- the mail server `cf-mail`, with its own host name and certificate;
- a node of a MariaDB Galera cluster, `cf-mail-db`, with the data store of the mail servers;
- a member of the cluster store, `cf-mail-keep` (Stalwart Keep), with the locks and the
  counters of the mail servers;
- the private network `cf-mail` (WireGuard, UDP port 51877). The database and the cluster
  store listen only on the address of the member in this network.

The panel makes the cluster in steps. A step starts when all jobs of the step before it are
complete:

| Step | Job | What occurs |
| ---- | --- | ----------- |
| `prepare` | `PrepareMailNode` on each server | Each server makes its keys and gives the public keys. |
| `bootstrap` | `SetMailNode` on the first server | The first server makes the private network and starts the database cluster. |
| `join` | `SetMailNode` on the other servers | The other servers join the private network and the database cluster. |
| `first` | `SetMailServer` on the first server | The first mail server starts and writes the settings to the database. |
| `rest` | `SetMailServer` on the other servers | The other mail servers start with those settings. |

A job that fails stops the setup. An administrator starts the step again
(`POST /v1/mail-clusters/{id}/retry`): only the servers that did not complete the step do it
again. The secrets of the cluster are not in the jobs. The panel adds them each time that it
sends a `SetMailNode` job. When the server of a mail domain is offline, a different member does
the mail jobs of that domain.

The shared name of the cluster has an A or AAAA record for each member. The MX records of the
mail domains use the shared name. Mail programs and the webmail use the host name of one
member.

## Databases

A customer makes database servers as it makes websites. A database server is one container
with one database engine: MariaDB, PostgreSQL or Valkey. It has its own data, its own
superuser, its own port and its own limits. Each database of a website is in one database
server of the customer of the website, on the server of the website.

- A customer cannot change the service of a different customer, because the customers do
  not use the same containers. Each database server has its own Unix user and its own gVisor
  sandbox, as each website has. Refer to
  [The Unix user of a database server](#the-unix-user-of-a-database-server).
- A customer can have more than one database server of an engine, for example one MariaDB
  database server for its shop and one for its reports.
- All websites of the customer on the server can connect to each database server of the
  customer there. The database users and their permissions keep these websites apart.

The panel has the rules, and the agent applies them:

| Item | Source |
| ---- | ------ |
| The number of database servers of a customer | The plan of the customer (`databaseServers`). The database servers of the customers of a reseller count against the plan of the reseller too. |
| The limits of a database server: vCPUs, memory and disk space | The plan of the database server (`databaseLimits`). An administrator can set different limits for one database server. |
| The number of databases in a database server | The plan of the database server (`databases`) |
| The plan of a database server | The plan of the customer. An administrator, or the reseller of the customer, can select a different plan. |

The panel makes a database server in two cases: when a user makes one (API
`POST /v1/database-servers`), and when a website gets its first database of an engine and its
customer has no database server of that engine on the server. The agent has the actions
`SetDatabaseServer` (make, or change the limits) and `DeleteDatabaseServer`. The panel removes
only a database server that has no databases and no database users.

| Container | Engine | Ports | Host name in a website container |
| --------- | ------ | ----- | -------------------------------- |
| `cf-mariadb-<id>` | MariaDB | 3306 to 3355 | `mariadb.internal` |
| `cf-postgresql-<id>` | PostgreSQL | 5432 to 5481 | `postgres.internal` |
| `cf-valkey-<id>` | Valkey | 6379 to 6428 | `valkey.internal` |

`<id>` is the ID of the database server in the panel, in lower case. The first database
server of an engine of a customer on a server has the usual port of the engine. The next one
has the next port, for example 3307. Thus a customer can have 50 database servers of each
engine on a server. A database server keeps its port. The name of each database and each
database user starts with the Unix user of the website.

The files of a database server are in `/var/lib/cf-db/servers/<id>`:

| Path | Content |
| ---- | ------- |
| `disk` | The disk of the database server: a directory, or the mount point of the disk image. Refer to [Disk limit](#disk-limit). |
| `disk/data` | The data directory. The Unix user of the database server owns it. |
| `disk.img` | The disk image of the database server, only on a file system with no user quotas |
| `mariadb.env`, `postgresql.env` | The superuser password. Root owns the file. Only root and the Unix user of the database server can read it. |
| `valkey.secret` | The password of the Valkey administrator. Only root can read it. |

Root owns the directory `/var/lib/cf-db/servers/<id>`. Only root and the Unix user of the
database server can open it, and that user cannot change it.

### The Unix user of a database server

The agent makes one Unix user for each database server, as it makes one for each website.
The user owns the data of the database server and starts its container.

| Item | Value |
| ---- | ----- |
| Name | `db-<id>` |
| UID and GID | From 10000 to 19999. The website users have the range from 20000 to 59999. |
| Home directory | `/var/lib/cf-db/homes/db-<id>` |
| Sign-in | No password and no login shell |
| Group | `cf-sites`, which can read the shared image store |
| Container unit | `/etc/containers/systemd/users/<uid>/`. Root owns the file. |

- The database process in the container is the user 999, which is the database user of each
  database image: `mysql`, `postgres` and `valkey`. Podman gives that user the UID of the
  Unix user of the database server on the host (`UserNS=keep-id:uid=999,gid=999`). Thus the
  Unix user of the database server is the owner of each file that the database server writes.
- The container has no capability (`DropCapability=all`). The entry point of the image does
  not change the owner of a file, and does not change its user.
- The image is in the shared image store of root, as the runtime images of the websites are
  (refer to [Shared runtime images](#shared-runtime-images)). The user keeps only its small
  writable container layer in its home directory. Thus the image does not use the disk limit
  of the database server.
- The firewall refuses each new connection of the users in this range. A database server
  answers its clients, and opens no connection itself. Refer to
  [security.md](security.md#firewall).
- A person who breaks out of the gVisor sandbox of a database server has only the rights of
  the Unix user of that database server. That user cannot read the files of a different
  database server or of a website.
- The removal of a database server removes its Unix user, its home directory and its user
  quota.
- Root owns `/var/lib/cf-db`, and the other users cannot list it. No Unix user has this
  directory as its home directory.

### The database network

The installer adds `169.254.80.1/20` to the loopback interface. This makes each address of the
network `169.254.80.0/20` a local address of the server, and the addresses never leave the
server. The agent gives each customer one address of the network on a server, from
`169.254.80.2` to `169.254.95.254`. Thus a server can have database servers for 4093
customers. The database servers of a customer publish their ports only on the address of the
customer.

- Each website container gets the three host names with the address of its customer
  (`AddHost=` in the unit). Thus an application uses the same host names on all servers, for
  all customers. The panel shows the port of each database.
- The agent keeps the record of the customers in `/var/lib/cloudfactory/sites.json`: the
  address number of each customer, the websites that can use its database servers, and each
  database server with its engine, its port and its limits.
- The firewall has the set `database_access`. Each element is the UID of a website, the
  address of its customer and the port of one database server of its customer. A website can
  connect only to the addresses and ports in its elements. The agent writes the elements in
  `/etc/cloudfactory/firewall/database-access.nft` and loads the file after each change of
  the websites and of the database servers. Refer to [security.md](security.md#firewall).
- The database servers see `169.254.80.1` as the address of each website container. A MariaDB
  user with the access "website" can connect only from this address.

The database servers of a customer stay when the last website of the customer on a server is
removed. A user removes a database server in the panel. The address of a customer is free
again when the customer has no website and no database server on the server.

### Limits of a database server

The agent gets the limits of each database server from the panel. The settings
`databaseMemoryMb`, `valkeyMemoryMb` and `databaseCpus` of an earlier version of this release
are not used.

| Limit | How the agent applies it |
| ----- | ------------------------ |
| vCPUs | `CPUQuota` on the cgroup of the service of the container |
| Memory | `MemoryMax` on the cgroup of the service, with no swap. The panel accepts 512 MB or more. |
| Disk space | A file system with the size of the limit. The panel accepts 1 GB or more. |

- A database server that uses all its memory or all its CPU time does not take them from
  other containers. When MariaDB or PostgreSQL uses more memory than its limit, the kernel
  stops that database server, and systemd starts it again.
- Valkey gets a limit for its data from the memory limit: half of the memory limit after
  128 MB, and not less than 32 MB. For example, 512 MB of memory give 192 MB of data. Valkey
  needs the other memory for the connections, for the gVisor sandbox, and to write its data
  file again.
- PostgreSQL gets a limit for its write-ahead log from the disk limit: one eighth of the
  disk, from 64 MB to 1 GB.
- The limits do not reserve memory or CPU time. The sum of the limits can be more than the
  server has. Each container also uses memory when its databases have no work.
- A change of the vCPUs or of the memory restarts the database server. The open connections
  stop, and the data stays.
- Each MariaDB user can have 100 connections at the same time, and each PostgreSQL user 80.
  A database server accepts 151 connections (MariaDB) or 100 connections (PostgreSQL). Thus
  the application of one website cannot use all connections of its database server.

The status report of the agent has the memory and the disk space that each database server
uses. The panel shows them next to the limits.

### Disk limit

The Unix user of a database server owns all files of the database server. Thus the disk
limit of a database server is as the disk limit of a website. The type of the limit comes
from the file system of `/var/lib/cf-db`:

| File system | Disk limit |
| ----------- | ---------- |
| User quotas are on | The user quota of the Unix user of the database server |
| No user quotas, and the server has loop devices | A disk image with the size of the limit |
| No user quotas and no loop devices | No limit |

The installer turns on the user quotas of an ext4 or XFS file system. Refer to
[installation.md](installation.md#disk-quotas).

**User quota.** The hard limit of the quota is the disk limit, and the soft limit is 95% of
it. The disk of the database server is the directory `disk`.

- A database server that is at its limit gets the error "disk quota exceeded" when it
  writes. It cannot fill the disk of the server, and the other database servers and the
  websites continue to operate.
- A change of the limit applies immediately. The database server does not stop.
- A smaller limit must be more than the disk space that the database server uses. If it is
  not, the job fails and the limit stays as it is, because a database server that is above
  its limit cannot write. A database server with no limit always gets its first limit.
- The kernel counts the disk space of the user. The status report of the agent gets the use
  and the limit from the kernel (`quotactl`), and does not read the files of the database
  server.
- The limit includes the files of Podman in the home directory of the user, which are small.
  It does not include the image.

**Disk image.** Each database server has its own file system:

1. The agent makes the file `disk.img` with the size of the disk limit, and makes an ext4
   file system in it. The file is sparse: it uses only the disk space that the database
   server uses.
2. A systemd mount unit mounts the file as a loop device on `disk`, now and at each start of
   the server. The mount is a part of `local-fs.target`, so it is there before the user
   manager of the database server starts the container.
3. The data directory `disk/data` is in that file system.

- A database server that fills its disk gets the error "no space left on device". It cannot
  fill the disk of the server, and the database servers of other customers continue to
  operate.
- When the mount is not there, the data directory is not there, and Podman does not start
  the container. Thus a database server never starts with an empty directory in place of its
  data.
- A larger limit makes the file system larger while the database server operates.
- A smaller limit stops the database server, makes the file system smaller and starts the
  database server again. If the data needs more space than the new limit, the job fails and
  the disk keeps its size.

**No limit.** A server with no user quotas and no loop devices (no `/dev/loop-control`, for
example a container of an LXC host with a file system that is not ext4 or XFS) cannot limit
the disk space of a database server. The data directory is then a directory on the disk of
the server. The panel then shows `database-disk-limits` as an optional part of the server
that is off. This does not make the server degraded.

A database server keeps the type of disk that it started with. A database server with a disk
image keeps the image when the file system gets user quotas later.

A database engine that cannot write does not lose data that it saved before, but it can stop:

| Engine | At the disk limit |
| ------ | ----------------- |
| MariaDB | A statement that needs more disk space fails. MariaDB can stop with the error "Out of disk space". systemd starts it again, and it operates when it has free disk space. |
| PostgreSQL | A statement that needs more disk space fails. PostgreSQL can refuse connections until it has free disk space. |
| Valkey | Valkey refuses the commands that write. |

To correct this, make the disk limit of the database server larger. Then remove the data that
is not necessary.

The disk quota of a website does not include its databases.

### Stop of a database server

A database server must write its data to the disk before it stops. If it gets `SIGKILL`
before that, it repairs its data at the next start, and the start is slower.

| Step | Time | Source |
| ---- | ---- | ------ |
| Podman waits for a database server after the stop signal | 60 seconds | `StopTimeout` in the container unit |
| systemd waits for the services of a Unix user when the server stops | 120 seconds | `/etc/systemd/system/user@.service.d/zz-cloudfactory-stop.conf` |

- The systemd package of Ubuntu sets 5 seconds for the services of a Unix user
  (`/usr/lib/systemd/system/user@.service.d/timeout.conf`). After that time, systemd stops
  all containers of the user with `SIGKILL`. systemd reads the drop-in files of a unit in the
  sequence of their names, and the last value applies. Thus the file of Cloudfactory has a
  name that comes after `timeout.conf`.
- The installer writes the file. On a server of an earlier release, the agent writes it
  when it adds the database network.
- The 120 seconds are also for the website containers and for the other containers of the
  server.

### A server from release 0.1.9

In release 0.1.9 and earlier, all websites of a server used one MariaDB container
(`cf-mariadb`) and one PostgreSQL container (`cf-postgresql`) on the private service address
`169.254.77.1`. The containers ran as the Unix user `cf-db`, and their data is in
`/var/lib/cf-db/mariadb` and `/var/lib/cf-db/postgresql`.

This release does not have these containers:

- The panel sends the customer of a website with each website, and a database server with
  each request for a database or a database user. The agent refuses a request for a database
  that has no database server.
- The agent does not start, stop or change the shared containers, and it has no action that
  moves their databases.
- The update of the panel removes the records of the databases and the database users that
  have no database server.

An administrator removes the shared containers and the user `cf-db` by hand. Refer to
[installation.md](installation.md#a-server-from-release-019).

An agent of this release adds the database network, the firewall rules and the stop time of
the user services to a server that the installer of an earlier release prepared. If the
firewall file does not have the rules of the installer, the job stops and tells you to run
the installer again.

### Valkey

Valkey has no databases with names. It has databases with numbers, and all users can use all
numbers by default. Thus the agent makes a Valkey database from two parts:

- A database number from 1 to 1023. Valkey starts with 1024 databases. A new connection is in
  database 0, so no website gets database 0.
- A Valkey user with the name of the database. The access rule `db=<number>` of Valkey 9.1
  keeps the user in its database number. The rules also remove the commands of an
  administrator and the commands that change or show other databases.

The websites of one customer can use the same Valkey database server, so these rules keep the
websites of one customer apart. The user file of Valkey, `users.acl` in the data directory of
the database server, is the record of the users and their numbers. The agent reads the file
through Valkey, gives a new database the smallest free number of the database server, and
sends the number to the panel in the result of the job and in each report. The agent connects
to Valkey as the administrator user `default`. The password of this user is in
`/var/lib/cf-db/servers/<id>/valkey.secret`, which only root can read. The agent refuses a
Valkey release before 9.1.2, because earlier releases do not have the rule for database
numbers.

All the Valkey databases of one database server use the memory limit of that database server.
When the memory is full, Valkey removes the keys of that database server that were not used
for the longest time. A customer that needs a separate limit for one website makes a second
Valkey database server. Refer to [installation.md](installation.md#valkey).

## Change of runtime without downtime

The agent uses a blue and green method:

1. The agent starts a second container with the new image on a new port.
2. The agent sends HTTP requests to the new container. It compares the status with the expected
   status of the website.
3. If the new container is healthy, the agent changes the Caddy route to the new port. Then it
   stops the old container.
4. If the new container is not healthy, the agent stops it. The old container continues to
   operate.

After the change, the agent watches the website for the time of the health check. If the error
rate is more than the limit, the agent changes the website back to the old image.

## Rollouts

A rollout sends a new image tag to a set of websites. The panel API does the rollout in
batches. The first batch is the canary. If a job fails in a batch, the rollout stops. An
operator can roll back a rollout. The API then stops the jobs in the queue and changes the
updated websites back to the old tag.

## Updates of the host and the agent

- **Host updates.** The agent installs security updates with `apt`. It holds the Podman,
  crun and gVisor packages. After the update, it runs `needrestart`, which restarts the
  services that use changed files. `needrestart` does not restart the agent. The agent
  reports when the server must reboot.
- **gVisor.** The agent checks the SHA-512 sum of each `runsc` release. It keeps the previous
  release, so that an operator can roll back with `cf-agent gvisor rollback`.
- **Agent.** The agent downloads the new binary and its signature. It verifies the signature
  with the release public key. It keeps the previous binary. If the new agent does not start,
  the service `cf-agent-rollback` puts the previous binary back.
- **Edge proxy and SFTP.** A new agent version brings new images for the edge proxy and the
  SFTP container. The agent starts each of them again when its unit changed.
- **Panel.** The installation script of a new release, or `cf-agent panel update`, changes the
  panel containers to the images of the version of the agent. Refer to
  [installation.md](installation.md#update-to-a-new-release).

## Backups

The agent makes the backups with restic. The panel sends the address of the repository, the
credentials of the object storage and the repository password with each job. The agent does
not keep them.

- Each server with the role `web` has its own repository, `<target>/servers/<server ID>`, and
  its own password. The panel makes the password at the first use and keeps it encrypted.
- Each of these servers can have its own key for the object storage, which an administrator
  sets (`PUT /v1/backup-policy/servers/{serverId}/credentials`). The panel sends that key
  with the jobs of that server only (`BackupController::credentialsFor`). A server with no
  key of its own gets the shared key of the backup policy. A restore from the repository
  that all servers used in an earlier version always gets the shared key.
- A backup has the files of the website and a dump of each MariaDB and PostgreSQL database.
  The panel keeps the SHA-256 sum of each dump, and the agent compares the dump with this sum
  before a restore. A backup has no Valkey data.
- Each snapshot has the tags `website=<website ID>` and `kind=scheduled` (a nightly backup) or
  `kind=manual` (a backup that a user starts). The retention policy removes only scheduled
  backups. The panel keeps the 10 newest manual backups of each website.
- When a website is deleted, the panel records its server and the date after which its
  backups can go (30 days by default).
- On Sunday, each healthy server removes the backups of those websites from its repository,
  and then the data that no snapshot uses.
- A snapshot from the time when all servers used one repository has a mark. The panel
  restores it from the old repository with the old password.

## The panel on the first server

The installation script installs the panel on the first server. `cf-agent panel install` makes
five Quadlet units for the user `cf-panel`. Each unit is a container in gVisor.

| Unit | Function | Port on `169.254.77.1` |
| ---- | -------- | ---------------------- |
| `cf-panel-db` | The PostgreSQL database of the panel | 19432 |
| `cf-panel-api` | The API | 19080 (HTTP) and 19443 (HTTPS) |
| `cf-panel-queue` | The queue worker, which sends the jobs to the agents | None |
| `cf-panel-scheduler` | The scheduler, for example for the backups and the image checks | None |
| `cf-panel-web` | The web panel | 19081 |

The edge proxy sends the panel domain to the ports 19080 and 19081. It ends the TLS connection
of the browser with the certificate of the panel domain.

### The panel certificate

The edge proxy gets the certificate of the panel domain with one of two methods. A TLS
automation policy with the ID `cf-panel-tls` sets the method. It is the first policy, so that it
applies before the policies of the websites.

| Method | Issuer | Domains |
| ------ | ------ | ------- |
| `internal` | The panel CA: the CA `cloudfactory` of the Caddy PKI app. Caddy signs the certificate with the root certificate (`sign_with_root`) for 90 days, and renews it. | All domains, also `localhost` and `panel.localhost`. A new panel uses this method. |
| `letsencrypt` | Let's Encrypt, with ACME, as for the websites. | Public domains only. |

The CA stays in the Caddy configuration with Let's Encrypt too. Thus a change back to the panel
CA uses the same root certificate. Caddy does not add the root certificate to the trust store
of the host (`install_trust` is false).

The administrator changes the method in **Settings > Panel certificate**. The API sends the job
`SetPanelCertificate` to the agent on the panel server. The agent changes the policy, then
connects to the edge proxy and checks the certificate that it gets:

- `internal`: the panel CA signed the certificate.
- `letsencrypt`: a public CA signed the certificate, and the panel CA did not.

If the check does not pass in two minutes, the agent sets the previous method again, and the
job fails. With each report, the agent on the panel server sends the method, the state of the
certificate and the root certificate of the panel CA. The panel keeps them.

The other agents connect to the panel domain through the edge proxy. Each agent trusts the
public CAs, and the panel CA by the SHA-256 fingerprint of its root certificate
(`panelCaSha256` in `agent.json`). The edge proxy sends the root certificate with the
certificate of the panel, so the agent does not need the file. The agent gets the fingerprint
from the join command (`--panel-ca-sha256`), and from the answer of the panel to the join and to
each report. In each report, the agent sends the fingerprint that it trusts. The panel changes
back to its CA only when all other servers trust it.

The API, the queue worker and the scheduler use one PostgreSQL database. The data files are in
`/var/lib/cloudfactory/panel/db`. The password is in `/var/lib/cloudfactory/panel/db.env`, and
only `cf-panel` can read it. The firewall lets only root and `cf-panel` connect to port 19432.
Refer to [decisions.md](decisions.md) for the reason.

The agent on the panel server also joins the panel. It does not use the edge proxy. It uses
the HTTPS port of the API, `https://169.254.77.1:19443`:

- The port has the certificate of the panel (`/var/lib/cloudfactory/panel/pki/panel.crt`). The
  installation script gives the SHA-256 fingerprint of this certificate to `cf-agent join`
  (`--panel-certificate-sha256`). The agent accepts only this certificate.
- The agent sends its own certificate. The API compares its fingerprint with the certificate
  from the join.

The panel reaches this agent through `169.254.1.3`, which pasta maps to `127.0.0.1` on the
host.

Root on the panel server can run two account commands of the panel through the agent:
`cf-agent panel reset-password EMAIL` and `cf-agent panel unlock EMAIL`. The agent runs them
in the API container. Refer to [installation.md](installation.md#users).
