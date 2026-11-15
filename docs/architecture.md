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
  The website user pulls it, and also pulls an image with no digest. This pull does not use
  the shared stores: the agent gives Podman the storage option
  `overlay.mount_program=/usr/bin/fuse-overlayfs` on the command line, and a storage option
  there replaces all storage options of `website-storage.conf`. With the shared stores,
  Podman finds a layer of the new image in a shared store, for example the empty layer that
  most images have, and copies it. The copy fails with the error
  `tar-split.gz: permission denied`, because a website user cannot read all files of a layer
  that root pulled. Thus the user keeps a full copy of such an image.
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

### Node.js and Python websites

A website can use a Node.js image (`node22`, `node24` or `node26`) or a Python image
(`python3.13` or `python3.14`) in place of a PHP image. The container runs the start command of
the website, in the same gVisor sandbox, with the same Unix user, network and limits as a PHP
website.

- The image has the entry point `/usr/local/bin/cf-app`. It changes to the application
  directory (`CF_APP_DIR`, the document root of the website, `.` by default) and runs the
  start command with `sh -c`. For Node.js, it adds `node_modules/.bin` to `PATH`. For Python,
  it activates `.venv` when the directory has one.
- The app must listen on the port in the variable `PORT` (8080) and on `HOST` (`0.0.0.0`). The
  edge proxy sends the requests to this port, the same as for a PHP website.
- When the directory has no `package.json` (Node.js) or no `app.py` (Python) and the website
  uses the usual start command, `cf-app` serves a welcome page. Thus a new website passes its
  first health check before the customer uploads the files.
- The install command (for example `npm ci --omit=dev` or
  `python -m venv .venv && .venv/bin/pip install -r requirements.txt`) runs in a build
  container with the runtime image of the website, the same as a deploy from Git. The job
  `Install dependencies` runs it, then does the steps of a runtime change: a new container on a
  copy, a health check and the switch. If the command fails, the website does not change. A
  change of the version with an install command also runs the install command first, with the
  new version.
- The build container has a read-only root, `HOME=/tmp`, and the same limits as the website. The
  package managers keep their caches in `/tmp`.
- When the start command stops, systemd starts the container again (`Restart=always`).

#### Environment variables

Each website can have up to 50 environment variables. A name has letters, digits and `_`, and
does not start with a digit. The panel refuses `PORT`, `HOST`, `HOME`, `PATH`, `PWD` and the
names that start with `CF_`. A value is on one line and has a maximum of 4096 characters.

- The panel keeps the values encrypted in the column `websites.environment`. The API gives the
  names only. The panel adds the values to the job when it sends the job, so they are not in
  the `agent_jobs` table.
- The agent writes the variables to the file `env` (or `env-b` for the second container of a
  runtime change) in the configuration directory of the website. The file has the mode `0600`
  and the website user is its owner. The container unit names it with `EnvironmentFile=`, so
  the values are not in the unit and not in a command line.
- A change starts a new container with the new variables, does a health check and then
  switches. If the health check fails, the old container continues with the old values.
- The install command and the build commands of a deploy also get the variables. The secrets of
  the Git source replace a variable with the same name.

### Disk limit of a website

The Unix user of a website owns the files of the website. The type of the disk limit comes
from the file system of `/srv/cloudfactory/sites`. A database server has the same three
types (refer to [Disk limit](#disk-limit)):

| File system | Disk limit |
| ----------- | ---------- |
| User quotas are on | The user quota of the Unix user of the website |
| No user quotas, and the server has loop devices | A disk image with the size of the limit, for each new website |
| No user quotas and no loop devices | No limit |

The installer turns on the user quotas of an ext4 or XFS file system. Refer to
[installation.md](installation.md#disk-quotas).

**User quota.** The hard limit of the quota is the disk limit, and the soft limit is 95% of
it. The quota counts all files of the user on the file system: the files of the website, and
the files in the home directory of the user. A website that is at its limit gets the error
"disk quota exceeded" when it writes. A change of the limit applies immediately, and the
website does not stop.

**Disk image.** The website has its own file system:

| Path | Content |
| ---- | ------- |
| `/srv/cloudfactory/sites/<user>` | The directory of the website user: the mount point of the disk image. Root owns it. |
| `/srv/cloudfactory/sites/<user>/app` | The files of the website. The website user owns the directory. |
| `/srv/cloudfactory/disks/<user>.img` | The disk image. Only root can open the directory `disks`. |

1. The root helper makes the Unix user. Then it makes the file `<user>.img` with the size of
   the disk limit, and makes an ext4 file system in it. The file is sparse: it uses only the
   disk space that the website uses.
2. A systemd mount unit (`srv-cloudfactory-sites-<user>.mount`) mounts the file as a loop
   device on the directory of the website user, now and at each start of the server. The
   mount is a part of `local-fs.target`, so it is there before the user manager of the
   website starts the container. The mount has the options `nodev` and `nosuid`. With the
   option `discard`, the file gives the disk space of removed files back to the server.
3. The root helper makes the directory `app` in that file system.

These rules apply to a website with a disk image:

- A website that fills its disk gets the error "no space left on device". It cannot fill the
  disk of the server, and the other websites continue to operate.
- When the mount is not there, the directory `app` is not there, and Podman does not start
  the container. Thus a website never starts with an empty directory in place of its files.
- A larger limit makes the file system larger while the website operates.
- For a smaller limit, the agent first compares the disk space that the website uses with the
  new limit. If the files need more space, the job fails, the website does not stop, and the
  disk keeps its size. If not, the agent stops the website, closes its SFTP sessions, makes
  the file system smaller and starts the website again. A file system cannot become smaller
  while a process uses it.
- The limit applies to the directory of the website user only. The home directory of the
  user is not on the disk image. It has the files of Podman, the log file of the website
  (10 MB at most) and, for a custom image, the copy of the image. The website cannot write
  there: its container, its cron jobs and the build container of a deploy write only in
  `app` and in `/tmp`, which is in memory.
- The file system has the directory `lost+found`. Only root can open it. An SFTP user sees
  its name next to `app`.
- The kernel counts the disk space of the file system. The status report of the agent gets
  the use from the kernel, and does not read the files of the website. For a website with no
  disk image, the agent measures the files.
- When the agent removes the website, it closes the SFTP sessions of the website, removes
  the Unix user, unmounts the file system, and removes the mount unit and the disk image.

A website keeps the type of disk that it started with:

- A website with a directory keeps the directory when the server has no user quotas. The
  agent does not move the files of a website into a disk image. Such a website has no disk
  limit.
- A website with a disk image keeps the image when the file system gets user quotas later.
- A website with no disk limit gets no disk image. When a website with a disk image loses its
  limit, the image keeps its size, because a file system always has a size.

The status report of the agent has the part `disk-quotas`. The part is on when the server
applies the disk limit of each website: with user quotas, or when each website with a disk
limit has a disk image. If not, the part is off, and its text gives the cause: no loop
devices, or the number of websites with a disk limit and no disk image. The panel then shows
"Not applied on this server" next to the disk limit of each website of that server.

The disk images of the websites and of the database servers use the same code
(`agent/internal/actions/loopdisk.go`).

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
  | `/srv/cloudfactory/sites` | `/srv/cloudfactory/sites` | Read and write, with the later mounts of the host (`rslave`) |
  | `/etc/cloudfactory/sftp` (one `<user>.keys` file for each user) | `/etc/cloudfactory/sftp` | Read only |
  | `/etc/cloudfactory/sftp-users` (`passwd` and `group`) | `/var/lib/extrausers` | Read only |
  | `/var/lib/cloudfactory/sftp-host-keys` | `/etc/ssh/keys` | Read and write |

- The disk image of a website is a mount below `/srv/cloudfactory/sites` (refer to
  [Disk limit of a website](#disk-limit-of-a-website)). With `rslave`, the container gets each
  mount and each unmount that the host makes there after the start of the container. gVisor
  refuses a shared mount (`rshared`). The container does not need one, because it makes no
  mounts.
- The container starts with `--runtime-flag=dcache=0`. Without this flag, gVisor keeps files
  of the host open in a cache after a session closes them. The host then cannot unmount the
  disk image of a website ("target is busy"). The agent also closes the SFTP sessions of a
  website before it unmounts the disk image, because an open session has its root directory
  on that file system.
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
  encrypted with the key of the application. It sends the public key to the agent in the job.
  It adds the protected private keys (`setKeyFile`, `privateKeys`) each time that it sends a
  `SetMailbox` job, so that the job record does not have them. The agent writes them to the
  file `OpenPGP/private-key.asc` in the files of the mailbox (JMAP `FileNode`).
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
                                               ├─► /admin/*, /api/admin/*, /setup/* ──► 404 (refused)
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
the image.

#### PGP plugin

The image of the webmail has the plugin "PGP True End-to-End", release 2.0.2. The build gets
the package from the extension directory of Bulwark, and stops when its SHA-256 hash is not
the hash in `images/webmail/Containerfile`. To change the release, change the release and the
hash together.

The start script of the image (`images/webmail/start.mjs`) starts the webmail, and then:

1. Signs in to the admin dashboard of the webmail on `127.0.0.1`, with `ADMIN_PASSWORD` from
   the settings file. The agent makes this password and keeps it, as the secret.
2. Turns on the plugins of the admin, and turns off the plugins that users upload.
3. Installs the plugin when the installed release is different, and turns it on for all
   users.

When a step fails, the webmail operates without the plugin, and the log of the container
tells why. The next start tries again.

The edge proxy refuses the paths of the admin dashboard and of the setup. It lets `GET`
requests through for the few paths of the dashboard API that users need: the rules of the
webmail (`/api/admin/policy`), the code and the public settings of the plugins, the themes and
the images of the login page.

The build adds `images/webmail/pgp-plugin/provision.js` to the plugin
(`images/webmail/pgp-plugin/patch.mjs`). The user does not upload a key:

1. When the user signs in, the plugin looks for a public key with the description
   `CloudFactory` on the mail server. This key tells that the panel made the keys of the
   mailbox. Without it, the plugin asks nothing.
2. When the browser has no key for the mailbox, the plugin asks one time for the password of
   the mailbox.
3. With that password, the plugin reads `OpenPGP/private-key.asc` through JMAP on the same
   host name. The mail server examines the password.
4. The plugin adds the keys that open with the password, keeps them encrypted in the browser,
   and unlocks them. It then decrypts the mail in the browser.

A key that has an older password of the mailbox does not open. The plugin then tells the user
to download the key in the panel. In a new session, the plugin asks for the password again
to unlock the keys, as it does for all keys.

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
## DNS hosting

A server with the role `dns` and a name server host name is a DNS server. It has one
container, `cf-dns`, with Knot DNS. The container answers the queries for the zones of all
customers. It does not resolve other names.

```text
 resolver ──► port 53, UDP and TCP ──► nftables redirect ──► port 8053
                                                               │
                                          cf-dns (rootless Podman, gVisor, user cf-dns)
                                                               │ reads
                              /var/lib/cloudfactory/dns/etc    knot.conf and zones/<name>.zone
                              /var/lib/cloudfactory/dns/db     keys and signatures (KASP, journal)
```

- The Unix user `cf-dns` runs the container. The installation script does not make the user.
  The root helper makes it when the server gets its first zone.
- Root owns `knot.conf` and the zone files, and the container reads them from a read-only
  volume. The user `cf-dns` owns only the directory of the keys and the signatures.
- The firewall redirects port 53 to port 8053, as it does for ports 80 and 443. The agent
  adds the rules, which have the comment `# cloudfactory-dns`, when it starts the DNS server.
  The installation script keeps the rules when you run it again.
- The answers to one network have a rate limit (`mod-rrl`), so that an attacker cannot use
  the server to send traffic to a different address. The server does not give its name or its
  version.

### The panel sends full zones

The DNS servers do not use zone transfers between them. The panel has the records, and it
sends each changed zone to each DNS server in a job:

| Job | Work |
| --- | ---- |
| `SetDnsZones` | Start the DNS server if necessary, write the zone files, and load the zones. One job has 100 zones at most. |
| `DeleteDnsZones` | Remove the zones, their files and their keys. |

A zone in a job has its name, its serial and all its records. The root helper does not accept
a zone file from the worker. It checks each record with the shared input rules and writes the
zone file itself, so a person who controls the worker or the panel database cannot put a
directive such as `$INCLUDE` in a zone file.

Each change of a zone gives the zone a higher serial. The panel keeps, for each DNS server,
the last serial that the server loaded. The state of a zone is `active` when each DNS server
has the last serial, `updating` while a job runs, and `failed` when a server could not load
the zone. A server that could not load a change continues to answer with the records that it
had before.

### A DNS server that does not have the last zones

The panel corrects a DNS server by itself. The report of each agent, which the agent sends
each minute and when it starts, has the list of the zones of the server with the serial that
the DNS server loaded last. The panel compares the list with its zones:

| The server reports | The panel does |
| ------------------ | -------------- |
| No zone, or a zone with a different serial | It sends the full zone again. |
| The last serial, but the last job of the zone failed | It sends the full zone again. |
| A zone that the panel does not have, or that the panel removes | It removes the zone from the server. |
| Zones, and the server does not answer DNS queries now | It removes all zones from the server. |
| A zone with a larger serial than the panel has | It gives the zone a larger serial and sends it to each DNS server. |

Thus a server that was offline during a change, a server with a new disk and a server whose
DNS server could not load a zone get the last zones with no work of an administrator. A
server that starts with no data gets all zones.

- The panel does nothing while a DNS job of the server is in the queue or runs.
- After a try that did not correct the server, the panel waits 1 minute, then 2, then 4, up
  to 60 minutes between two tries. A report that agrees with the panel ends the wait.
- The agent records the serial of a zone only after the DNS server loaded the zone. A zone
  file that the DNS server could not load does not count.
- When the DNS container starts, Knot DNS loads the zone files from the disk. The server
  thus answers with its last zones before the panel sends a correction.

**Send again now** on the page of the zone, or `cf dns sync`, sends a zone again immediately.

### Records that the panel makes

- The panel makes the NS records of each zone from the name server host names of the DNS
  servers. A user cannot change them. When the role `dns` or the name server host name of a
  server changes, the panel sends all zones again.
- When a website of the customer has a domain in a zone, the panel makes an A record, and an
  AAAA record if the server has an IPv6 address, for the domain. The records point to the
  public addresses that the agent of the server of the website reported. A user can change
  such a record. Then the record belongs to the user, and the panel does not change it again.

### The name servers of the domain

A zone has an effect only when the registrar of the domain has the name servers of the
panel. The panel asks the public DNS for the NS records of the domain and shows one of these
states: the domain uses the name servers of the panel, it uses other name servers, it has no
name servers, or the panel could not check. When the domain uses the name servers of the
panel, the panel verifies the domains of the websites in the zone with no TXT record.

### DNSSEC

DNSSEC is off for a new zone. When a user turns it on, the panel makes one ECDSA P-256 key
(algorithm 13) for the zone. The key signs the zone and its keys (a combined signing key).
The panel keeps the private key encrypted, as it keeps the other secrets, and sends it to
each DNS server with the zone, so all servers sign with the same key. The root helper gives
the key to Knot DNS, and Knot DNS signs the zone with NSEC3 (no more iterations, no salt).
The panel shows the DS record that the customer gives to the registrar.

The panel does not change the key of a zone on a schedule. To change the key, remove the DS
record at the registrar, turn DNSSEC off and on, and give the new DS record to the registrar.

## Files, logs, cron jobs and deploys of a website

The agent does all of this work as the Unix user of the website (`runuser -u <user>`). The
root helper permits each program as a website user, so this work adds no operation to its
lists.

### File manager and logs

The panel does not make a job for a file operation or for the logs. The API calls the agent
and gives the answer in the same request.

```text
 web panel or cf ──► API ──► worker ──► runuser -u <user> -- cf-agent site-files <directory>
```

- The worker starts `cf-agent site-files` as the website user and sends one request as JSON
  on standard input. The program opens the directory of the website as a root directory
  (`os.Root`), so a path or a symbolic link cannot go out of that directory.
- A path starts with `/`. `/` is the directory of the website, which is `/app` in the
  container.
- The limits are 2 MiB for a read, 5 MiB for a write and 2000 entries for a list.
- The unit of a website container has the log driver `k8s-file`. The container writes its
  standard output and its standard error to `website.log` in the home directory of the
  website user. Podman starts a new file at 10 MB. For the logs, the agent reads the last
  lines of this file as the website user. A line from standard output has the stream
  `access`, and a line from standard error has the stream `error`.

### Cron jobs

- The panel keeps the cron jobs of a website and sends the full list to the agent with the
  job `SetCronJobs`. The agent keeps the list in its state file.
- Each minute, the worker starts the commands whose schedule agrees with the time in UTC. A
  schedule has the 5 fields of cron. The agent does not start a command while the previous
  run of that cron job continues.
- The agent runs a command with `podman exec --workdir /app <container> sh -c <command>` as
  the website user. Thus the command runs in the gVisor container of the website, with the
  limits of the website. The agent stops a command after 1 hour.
- The agent sends the exit code and the end of the output of each run in its status report.
  The panel shows them for the last run.
- **Run now** makes the job `RunCronJob`. The job fails when the exit code is not 0.

### Deploys from Git

A website can have one Git source: a repository, a branch, a web directory, build commands
and secrets. The panel makes an Ed25519 deploy key for the source and shows the public key.

The job `Deploy` has these steps:

1. **Get the code.** The agent clones the branch into `releases/<release ID>` in the
   directory of the website.
2. **Build.** The agent runs the build commands in that directory.
3. The steps of a runtime change (refer to
   [Change of runtime without downtime](#change-of-runtime-without-downtime)): the agent
   starts a second container with the document root `releases/<release ID>/<web directory>`,
   does a health check, changes the route, and stops the old container.
4. **Remove old releases.** The agent removes the release directories that the panel does
   not keep. The panel keeps the live release and the last 4 releases before it.

- Steps 1 and 2 run in a build container, not in the live container. The build container
  uses the runtime image of the website and gVisor, has the same limits as a website, and
  runs as the website user. It sees the directory of the website as `/app`.
- The agent writes the deploy key and the secrets to files that only the website user can
  read, for the time of the build, and then removes them. The secrets are environment
  variables of the build commands. The panel adds the key and the secrets to the job when
  it sends the job, so they are not in the `agent_jobs` table.
- The link `current` in the directory of the website points to the live release. The
  directory `shared` is for files that all releases use, for example `.env` and uploads.
  A build command can make a link to it.
- When a step fails, the old container continues, and the agent removes the new release
  directory. The release has the state `failed` and shows the cause.
- **Go back** makes the job `ActivateRelease`. It does step 3 with a release that the server
  keeps.
- A Git host starts a deploy with `POST /api/v1/hooks/deploy/<token>`. The request needs no
  sign-in: the token in the address is the secret. The panel ignores a push to a different
  branch.
- While a website deploys from Git, the panel refuses a change of the document root on the
  Runtime tab. Change the web directory of the Git source.
- For a Node.js or Python website, the install command is the first build command. It runs in
  the web directory of the release. The default web directory is `.`.

## Databases

A customer makes database servers as it makes websites. A database server is one container
with one database engine: MariaDB, PostgreSQL or Valkey. It has its own data, its own
superuser, its own port and its own limits. Each database of a website is in one database
server of the customer of the website. The database server is on the server of the website,
or on a different server that is in a network with the website
(refer to [The networks of the customers](#the-networks-of-the-customers)).

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

A website can also connect to a database server by its network name, for example
`shop-db.internal`, with the usual port of the engine. That name is the same on each server.
Refer to [The networks of the customers](#the-networks-of-the-customers).

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
limit of a database server is as the disk limit of a website (refer to
[Disk limit of a website](#disk-limit-of-a-website)). The type of the limit comes from the
file system of `/var/lib/cf-db`:

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
image keeps the image when the file system gets user quotas later. A database server with a
directory keeps the directory when the file system has no user quotas, and then has no disk
limit.

The status report of the agent has the part `database-disk-limits`. The part is on when the
server applies the disk limit of each database server. If not, the part is off, and its text
gives the cause: no loop devices, or the number of database servers with a disk limit and no
disk image. The part `disk-quotas` has the same rule for the websites.

A database engine that cannot write does not lose data that it saved before, but it can stop:

| Engine | At the disk limit |
| ------ | ----------------- |
| MariaDB | A statement that needs more disk space fails. MariaDB can stop with the error "Out of disk space". systemd starts it again, and it operates when it has free disk space. |
| PostgreSQL | A statement that needs more disk space fails. PostgreSQL can refuse connections until it has free disk space. |
| Valkey | Valkey refuses the commands that write. |

To correct this, make the disk limit of the database server larger. Then remove the data that
is not necessary.

The disk limit of a website does not include its databases.

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

### Adminer

Adminer is a web tool for MariaDB and PostgreSQL databases. Adminer has no driver for Valkey,
so the panel does not open a Valkey database in Adminer.

Each customer has one Adminer container (`images/adminer`). The container is on the panel
server, in gVisor, with its own Unix user (`adminer_<random>`) and the limits of
`DatabaseTools::LIMITS`. The panel makes it with the usual website jobs (`CreateWebsite` with
the runtime `custom`, and `SetRuntime` when the release of the panel server changes). The
panel makes the container when a user opens the first database of the customer.

- **Address.** All customers use one Adminer domain, `adminer.<panel domain>` by default
  (`CF_ADMINER_DOMAIN`). The edge proxy sends the path `/<id of the tool>/` of that domain to
  the container of the customer. A website route has the field `routePath` for this. The
  websites with the same domain and different paths share one TLS policy in the edge proxy.
  Each sign-in adds a random key to the path: `/<id of the tool>/<key>/`. Adminer keeps its
  session cookie on the path of the request, so each sign-in has its own cookie, and a page of
  a different sign-in on the same domain does not get it. `index.php` refuses each other path.
  The Adminer domain is a sub-domain of the panel domain, so no website can have it. The web
  panel keeps its token in the session storage of the browser, which is separate for each
  origin, so Adminer cannot read the token.
- **Network.** The container is a member of the networks of its customer, like a website
  that connects to each MariaDB and PostgreSQL database server of the customer
  (`DatabaseTools::links()` in `Networks::links()`). The mesh, the firewall and the host file
  thus let it connect only to the database servers of its customer. A database server on a
  different server is reachable through the tunnel to that server.
- **Sign-in.** The panel stores no database passwords. To open a database, the panel makes a
  temporary database user, `<unix user of the website>_adminer_<random>`, with access to that
  database only (`SetDatabaseUser`). The access is read and write, or read only. The API gives
  the password one time, in the answer of
  `POST /v1/websites/{websiteId}/databases/{databaseId}/adminer`, and does not keep it. The
  web panel opens a new window, waits until `GET /v1/adminer-logins/{id}` gives `ready`, and
  sends the sign-in form of Adminer with the user and the password. The command
  `cf databases adminer` writes a local HTML file that sends the same form.
- **Expiry.** A temporary user is valid for 60 minutes (`CF_ADMINER_MINUTES`). Each minute,
  the command `cloudfactory:expire-adminer-logins` removes the users that are not valid
  (`DeleteDatabaseUser`), and the users of a database that was removed.
- **Checks in Adminer.** `images/adminer/index.php` shows no sign-in form. It refuses a
  driver that is not MariaDB or PostgreSQL, and a server name that is not `<name>.internal`
  with an optional port, before Adminer connects. It turns off the permanent login, shows only
  the databases of the sign-in, and refuses frames from other sites.

## The mesh between servers

The mesh is the set of encrypted tunnels between the servers of one panel. A tunnel lets two
servers send private traffic to each other through the internet. In this release, only root
on a server can use a tunnel, and the only traffic is the test of the tunnel. A later release
uses the tunnels for the private networks of the customers.

Each server has one WireGuard interface with the name `cf-mesh`. The values of the mesh are
in the panel (**Network**):

| Value | Default | Function |
| ----- | ------- | -------- |
| Address range | `10.207.0.0/16` | Each server gets one address from the first 254 addresses of the range, with its first tunnel. |
| UDP port | 51820 | The port of the tunnels on each server. |
| Tunnel address | Automatic | The public IP address that the other servers send the tunnel traffic of a server to. The default is the address that the panel connects to the agent with. |

You can change the address range and the port only while the mesh has no tunnel.

### Keys

- The root helper of the agent makes the key pair of its server. The private key is in
  `/etc/cloudfactory/mesh/private.key`. Only root can read it, and it never leaves the
  server. The agent sends the public key to the panel in each report.
- The panel is the directory of the public keys. It gives a server the public keys of only
  the servers that it has a tunnel to.
- The panel makes one pre-shared key for each tunnel. It keeps the key encrypted in its
  database, and it sends the key to the two servers of the tunnel through the agent
  connection (mutual TLS).
- **Make new key** (`cf mesh rotate-key SERVER`) makes a new key pair on a server. The tunnels
  of the server do not operate until the panel sends the new public key to the other servers.
  That occurs automatically when the job is complete.

### The state of a server

The panel sends a server the full state of its tunnels with the action `SetMesh`: the address
range, the address of the server, the port, and each other server with its public key, its
pre-shared key, its tunnel address and its mesh address. The state has a version number.

1. The worker of the agent examines the state and sends it to the root helper.
2. The root helper examines the state again. It refuses a state with an older version than
   the one that it has.
3. The root helper writes the state to `/etc/cloudfactory/mesh/mesh.json`, writes the
   elements of the firewall sets, and applies the WireGuard configuration with
   `wg syncconf`. A tunnel that did not change continues to operate.
4. The agent sends one ICMP echo request to each other server through its tunnel. The result
   of the job has the answer, the round-trip time and the time of the last handshake.

When the root helper starts, for example after a reboot, it applies the state of the file
again. A state with no address and no tunnels removes the interface.

If the server has no `wg` command, the root helper installs the package `wireguard-tools`.
The installer of this release installs it.

### How the panel monitors the tunnels

Each agent report has the public key of the server, the version of its state and the state
of each tunnel. The agent sends a report each minute.

- A tunnel is **up** when the two servers got an answer through it in the last three minutes.
  It is **down** when one server got no answer. Before the first reports, the panel shows that
  it waits for the servers.
- When a server reports a different version than the panel wants, the panel sends the state
  again. It does this one time in two minutes for a server at most.
- When a server reports a new public key, the panel sends the key to the other servers of its
  tunnels.
- **Repair** (`cf mesh repair SERVER SERVER`) sends the state to the two servers of a tunnel
  again and tests the tunnel. Use it after you correct a problem, for example a firewall of
  your provider that does not let the UDP port through.
- When you remove a server from the panel, the panel removes its tunnels from the other
  servers.

The panel makes a tunnel when a website on one server uses a database server on the other
server (refer to [The networks of the customers](#the-networks-of-the-customers)). It removes
that tunnel when no website needs it. An administrator can also make a tunnel
(**Network > Make tunnel**, or `cf mesh connect SERVER SERVER`). The panel does not remove a
tunnel of an administrator. A server with an agent from an earlier release cannot have
tunnels until you update its agent.

## The networks of the customers

A network is a private network of one customer. Its members are websites and database
servers of the customer. A website can connect to each database server and to each other
website that is in a network with it. The member can be on the server of the website or on a
different server.

Each customer has the network `default`. Each new website and each new database server of
the customer is a member of it.

### Named networks

A customer can make more networks, to keep some websites and database servers apart. A
network of this type is a named network. For example, the network `backend` has a shop and
its database server, and the blog of the same customer is not in it.

- The plan of the customer gives the number of named networks (`networks` of the plan). The
  network `default` is not one of them. With 0, the customer has only the network `default`.
  A new plan gets 0. For a reseller, the named networks of its customers count too.
- A network can have any number of websites and database servers of its customer. A website
  or a database server can be in more than one network, or in none.
- A website connects to a member only when one network has the two. To separate two
  members, remove one of them from each network that has the two. Thus a member that must
  be apart from the others must also leave the network `default`.
- The name of a network has lower-case letters, digits and `-`, and a maximum of 40
  characters. The names of the members do not change with the network: a database server has
  the same network name in each network.
- The network `default` keeps its name, and the panel does not remove it. When a named
  network is removed, its members stay.
- When a website has a database in a database server on a different server, the panel
  refuses a change that leaves the two with no network. The website would lose its database.
- The names `mariadb.internal`, `postgres.internal` and `valkey.internal` do not use the
  networks. A website can always use them for the database servers of its customer on its
  own server.

The user changes the networks on the page **Private networks**, with `cf networks`, or with
the paths `/v1/networks` of the API. A token needs the scope `websites:read` to read the networks and `websites:write` to change them. A change needs
no new function of the agent: the panel sends the new list of connections to each server with
the mesh state, and the agent changes the firewall elements and the hosts files. No
container restarts.

### Names and addresses

A database server has one name for the websites: the name of the database server and
`.internal`, for example `shop-db.internal`. It answers on the usual port of its engine:
3306 for MariaDB, 5432 for PostgreSQL and 6379 for Valkey. The name and the port are the
same for each website, on each server. Thus the configuration of an app does not change when
the database server or the website is on a different server.

A website has one name for the other websites of its networks: its Unix user with `-` in
place of `_`, and `.internal`. For example, the website with the Unix user `shop_a1b2` has
the name `shop-a1b2.internal`. It answers on port 80, with HTTP. The connection does not go
through the edge proxy, so it has no TLS of its own: the tunnel between two servers encrypts
it. The panel shows the name on the **Settings** tab of the website, and `cf website show`
prints it. A database server cannot have the name of a website of its customer.

The panel gives each member one address of the mesh range (the member address). The
addresses of the servers are the first 254 addresses of the range, and the member addresses
are after them. A database server gets its address when the panel makes it. A website gets
its address when a different website is in a network with it. A member address does not
change. The name points to the member address on each server.

The names `mariadb.internal`, `postgres.internal` and `valkey.internal` stay. They point to
the database servers of the customer on the server of the website, where each database
server has its own port. A database server cannot have the name `mariadb`, `postgres`,
`valkey` or `localhost`.

### Move a database server

An administrator can move a database server to a different server. The database server
keeps its ID, its name, its databases, its users, its network name and its member address.
Thus a website that uses the network name continues to connect to it after the move. The
traffic of the websites goes to the new server through the tunnels.

The data goes through the WireGuard tunnel between the two servers:

1. The panel makes a token of 32 random bytes and the tunnel between the two servers. It
   sends `ReceiveDatabaseServer` to the new server and then `SendDatabaseServer` to the old
   server. The two jobs have the token.
2. The new server makes the Unix user, the disk and the unit of the database server. It does
   not start it. Its firewall lets the mesh address of the old server connect to TCP port
   7445 on the mesh address of the new server, only for the time of the move (the set
   `mesh_transfers`).
3. The old server stops the database server. Its root helper connects from its mesh address
   and sends the token, and then one tar stream: the secret of the superuser and the files of
   the data directory.
4. The root helper of the new server compares the token, writes the files with the owner of
   the database server, and answers `ok`. Then the new server starts the database server.
5. The panel records the new server and the new port for the engine name, and the old server
   removes its copy. The member address is now on the new server, so the next mesh state
   sends the traffic of the websites there.

When the move fails, the database server stays on the old server. The old server starts it
again, and the new server removes what it received. The old server keeps its data until the
new server operates the database server.

- The database server does not operate while the data moves. The time depends on the size of
  the data and the speed of the connection between the servers.
- A website on a different server connects only with the network name. So, before the move,
  each website with a database in the database server must be on the new server, or in a
  network with the database server. The panel refuses the move when this is not true.
- The engine name (for example `mariadb.internal`) works only on the server of the database
  server. An app that uses it on the old server loses the connection. The panel warns about
  this before the move.
- Both agents must report that they can move database servers (`moves` in the mesh part of
  the status report, `servers.mesh_moves` in the panel).

The administrator moves a database server in the list of the database servers (**Move to a
different server**), with `cf database-servers move DATABASE_SERVER --server SERVER`, or with
`POST /v1/database-servers/{id}/move`.

### Reference size

The test `api/tests/Feature/NetworkScaleTest.php` makes a panel with 20 servers, 100 websites
and 20 database servers of one customer in the network `default`. That is 190 tunnels and
120 members. The panel makes all states in less than 30 seconds, and the mesh state of one
server is less than 256 KB. After one change, the panel makes the new states in less than 5
seconds.

### A website with no public address

A website has one of three kinds. Each kind is a website of the plan of the customer.

| Kind | Domains and edge route | Port 80 in its networks | Container |
| --- | --- | --- | --- |
| `public` | Yes | Yes | The web server of the image |
| `private` | No | Yes | The web server of the image |
| `worker` | No | No | The start command of the customer |

- **A private website** has no domain, no TLS certificate and no route in the edge proxy. The
  agent gets a website with no domains, which it could operate before: a new website has no
  route until its domain is verified. Only a website that is in a network with it can
  connect to it, by its network name on port 80.
- **A background worker** (the kind `worker`) runs one command of the customer, for example `php artisan queue:work`. The
  panel sends the command as `sh -c "<command>"`, as for a cron job. The command runs in the
  container of the worker: the same gVisor sandbox, Unix user, limits and firewall rules as
  for a website. The container has no port on the private service address, and the worker
  is not a member that answers. It can connect to the members of its networks. systemd
  starts the command again when it stops. After a start, the agent makes sure that the
  service stays active for 5 seconds. Thus a command with a wrong name fails the job. The
  owner changes the command on the **Runtime** tab. The agent then starts a second
  container with the new command and stops the first one, as for a change of the runtime.
  For a short time, the two containers operate together.

The user types a short name for a private website or a worker, for example `queue`. The
panel makes the Unix user from it (`queue_a1b2`), and the name of the website is the network
name (`queue-a1b2.internal`). This name is not a name of the internet, and no two websites
have the same one. The panel makes a private website or a worker only on a server whose
agent reported the function (`websites` in its mesh report). The kind of a website does not
change after the panel made it. A private website and a worker cannot have a domain or a
staging copy.

### What each server gets

The panel sends the networks of a server with its mesh state (the action `SetMesh`):

- The members on the server. Each member has its address, its port, and the mesh addresses
  of the other servers that have a website in a network with it.
- The websites on the server that are in a network with a member. Each website has the
  name, the address and the port of each member that it can connect to.
- For each tunnel, the addresses of the members on the other server that a website of this
  server uses. WireGuard sends the traffic for these addresses into that tunnel.

A server with no tunnel also gets its members and its websites, with the address range. A
website then uses the same name for a database server on its own server.

The agent does this with the networks:

1. The worker keeps the networks in its state. From them and from its records of the
   websites and the database servers, it makes the firewall rules and the names of each
   website. It leaves out a member or a website that the server does not have yet.
2. The root helper examines each rule and each name. Then it loads the firewall elements from
   `/etc/cloudfactory/firewall/network.nft` and writes the hosts file of each website:
   `/etc/cloudfactory/websites/<user>/hosts`.
3. The worker does the two steps again after each change of the websites and the database
   servers, and when the agent starts.

The container of a website has the hosts file of the website as its `/etc/hosts`. The agent
changes the file in place, so a container that operates gets a new name with no restart. A
website from an earlier release has the three names of the engines in its unit. It gets the
hosts file, and thus the network names, when the agent writes its unit again: at the next
restart, deploy or change of the runtime.

### The path of a connection

A website connects to `shop-db.internal:3306`, which is the member address.

- **The database server is on a different server.** The firewall of the server of the website
  lets only the Unix user of that website connect to the member address and the port. The
  tunnel carries the connection. On the server of the database server, the firewall accepts
  the connection only from a server that has a website in a network with the member. Then it
  changes the destination to the address and the port of the container.
- **The database server is on the same server.** The firewall changes the destination to the
  address and the port of the container. It lets only the Unix user of a website that is in
  a network with the member do this.

A website that is a member has the same path. The destination changes to the port of the
container of the website on the private service address (`169.254.77.1`). The rule that
lets a website connect to a member is the first rule of the chain `output`, before the rule
that closes the ports of the private service address. It compares the member address and
port 80 from before the change. Thus the port of the container itself stays closed for each
website. When the runtime of a website changes, its container gets a new port, and the agent
writes the rules again. A website that its owner stopped stays a member: a connection to it
gets a refusal.

An agent reports that a website on it can be a member (`websites` in its mesh report). The
panel makes a website a member only when the agent of its server and the agent of the server
of the client reported this. An agent from an earlier release gets no member on port 80.

In the two cases, the database server sees the address `169.254.80.1` as the client. A
website that is a member also sees this address. This is
the address that it sees for each website container. Thus a database user that can connect
from a website can connect from each website of the networks of the database server, with
its password.

The panel changes the mesh after each change of a website, a database server or a network.
It makes the tunnels that the networks need, removes the tunnels that it made and that no
network needs, and sends the new state only to the servers whose state is different.

### A database on a different server

A website can have a database in a database server of its customer on a different server,
when the two are in one network. The user selects the database server for a new database.
With no selection, the panel uses a database server on the server of the website first.

- **The jobs.** The panel sends the job for a database or for a database user to the agent
  of the server of the database server. That agent has no record of the website, so the
  job has the ID of the customer. The agent does the job only when the database server is a
  database server of that customer. For a website that the agent has, the record of the
  agent decides, and a different customer in the job is refused.
- **The connection.** The app connects to the network name of the database server, for
  example `shop-db.internal:3306`. The database user is the same as for a website on the
  server of the database server: the database server sees one address for each website.
- **The size.** Each agent reports the size of each database in its database servers. The
  panel accepts the size of a database from the agent of its database server.
- **The removal of the website.** The agent of the server of the website cannot remove a
  database on a different server. The panel first sends a job for each such database and
  each such database user to the agent of that server. When these jobs are complete, the
  panel sends the job that removes the website. When one fails, the website stays, and the
  user removes it again.

Limits of this release:

- A backup of a website with a database on a different server has two snapshots, in two
  repositories (refer to [Backups](#backups)). When one of the two jobs fails, the backup
  has only the other part.
- The panel cannot make a staging copy of a website that has a database on a different
  server.

In this release, a network member is a database server. A later release adds websites as
members, private websites, and networks with a name.

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
- A website can have a database in a database server on a different server. The agent of
  the server of the website cannot make a dump of it. Thus a backup of such a website has
  two parts. The agent of the server of the website makes the snapshot of the files and of
  the databases on that server. The agent of the server of the database server makes a
  snapshot with only the dumps of the databases on that server, in the repository of that
  server. The kind of that snapshot is `scheduled-databases` or `manual-databases`, and the
  retention policy applies to each kind separately. The dump does not go to a different
  server. A restore of such a database goes to the agent that has the snapshot.
- Each snapshot has the tags `website=<website ID>` and `kind=scheduled` (a nightly backup) or
  `kind=manual` (a backup that a user starts). The retention policy removes only scheduled
  backups. The panel keeps the 10 newest manual backups of each website.
- When a website is deleted, the panel records each server that has a backup of it, and the
  date after which its backups can go (30 days by default).
- On Sunday, each healthy server removes the backups of those websites from its repository,
  and then the data that no snapshot uses.
- A snapshot from the time when all servers used one repository has a mark. The panel
  restores it from the old repository with the old password.

## Plugins

```text
 plugin host ──► public API (token with the scopes of the manifest)
      ▲
      └──── signed webhooks (DeliverPluginEvent, queue worker)
 web panel ──► frame with the origin of the plugin ◄── page token (postMessage)
 cf NAME ──► cf-NAME on the PATH
```

A plugin is a program outside the panel. The panel keeps its signed manifest (`plugins`), the
keys that it trusts (`plugin_keys`) and the deliveries of its events (`plugin_deliveries`).
Each plugin has its own user (`users.plugin_id`) and token. `App\Services\Audit` and
`App\Services\JobOutcome` give the events `audit.recorded` and `job.finished` to
`App\Services\Plugins\Events`, which records a delivery for each plugin that wants the
event. The queue worker sends each delivery. `App\Services\Plugins\Hooks` asks the plugins
with a before hook during the request, and `PluginController::runAction` sends an action to
its plugin during the request. Both use `App\Services\Plugins\Caller`, which has the address
rules and the signature of the webhooks. No part of the plugin system calls an agent.
Refer to [plugins.md](plugins.md) and [security.md](security.md#plugins).

## White label

The administrator sets the default brand in **Settings > Brand**: a name, a colour, a logo and
a support email. Each reseller sets its own brand on the **Brand** page. The reseller and its
customers see only the brand of the reseller. A field that the reseller did not set comes from
the reseller (its name and its email) or from the built-in values. It never comes from the
default brand, so a customer of a reseller does not see the name of the hosting company.

- **Checks.** White text on the colour must have a contrast of 4.5:1 or more (WCAG 2.2 AA). A
  logo is a PNG, JPEG or WebP image of 256 KB or less and 2048 by 2048 pixels or less. The
  panel reads the real type of the image. SVG is not allowed, because an SVG file can hold a
  script.
- **Panel domain.** A reseller can set its own panel domain. It must prove control of the
  domain, as for a website domain. The panel sends the full list of reseller panel domains to
  the agent on the panel server (`SetPanelBrands`). The edge proxy adds the domains to the panel
  route and gets a certificate for each one: from Let's Encrypt for a public name, else from the
  panel CA. The sign-in page reads `GET /api/v1/brand`, which gives the brand of the domain of
  the request.
- **Name servers.** Where the panel has DNS hosting, a reseller can give one host name to each
  name server of the panel, for example `ns1.agency.example`. The zones of the reseller and of
  its customers then get these names in their NS records, and so in the SOA record. A
  delegation to these names counts as a delegation to the panel. When the administrator adds or
  removes a DNS server, the number of names does not agree, and the zones use the names of the
  panel until the reseller gives a new list.
- **Mail host.** A reseller can set a mail host name. The email hosting shows it to the customers
  of the reseller.

Refer to the [screenshots](screenshots/white-label).

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
