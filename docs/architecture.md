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
- The **agent** (`cf-agent`) runs as root on each server. It accepts only the actions in
  [`agent.proto`](../agent/proto/cloudfactory/agent/v1/agent.proto). It has no action that runs
  a shell command that the panel gives.

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

The database containers are not on this address. Each customer has its own database
containers on an address of the database network. Refer to [Databases](#databases).

### Shared runtime images

All websites on a server share one read-only copy of each runtime image. A new website with an
image that the server has already starts with no pull, and 100 websites keep one copy of the
image, not 100.

- The agent pulls each runtime image one time, as root, into its own Podman store in
  `/var/lib/cloudfactory-images/<sha256 of the image reference>`. The signature policy checks
  the image when the agent pulls it. Only a reference with a digest goes in the shared store.
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
  day. It keeps an image that a job asked for in the last hour.

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

## Databases

Each customer has its own database containers on a server: one container for each database
engine that the websites of the customer use there. A customer cannot change the service of a
different customer, because each container has its own database server, its own data, its own
superuser and its own limits. The Unix user `cf-db` runs each container in its own gVisor
sandbox. The agent starts a container when a website of the customer needs the engine for the
first time.

| Container | Engine | Port | Host name in a website container |
| --------- | ------ | ---- | -------------------------------- |
| `cf-mariadb-<customer>` | MariaDB | 3306 | `mariadb.internal` |
| `cf-postgresql-<customer>` | PostgreSQL | 5432 | `postgres.internal` |
| `cf-valkey-<customer>` | Valkey | 6379 | `valkey.internal` |

`<customer>` is the ID of the customer in the panel, in lower case. The name of each database
and each database user starts with the Unix user of the website. The data and the superuser
passwords of a customer are in `/var/lib/cf-db/customers/<customer>`.

### The database network

The installer adds `169.254.80.1/20` to the loopback interface. This makes each address of the
network `169.254.80.0/20` a local address of the server, and the addresses never leave the
server. The agent gives each customer one address of the network on a server, from
`169.254.80.2` to `169.254.95.254`. Thus a server can have database containers for 4093
customers. The containers of a customer publish their ports only on the address of the
customer.

- Each website container gets the three host names with the address of its customer
  (`AddHost=` in the unit). Thus an application uses the same host names and ports on all
  servers, for all customers.
- The agent keeps the record of the customers in `/var/lib/cloudfactory/sites.json`: the
  address number of each customer, and the websites that use its containers.
- The firewall has the set `database_access`. Each element is the UID of a website and the
  address of its customer. A website can connect only to the database ports on the address in
  its element. The agent writes the elements in
  `/etc/cloudfactory/firewall/database-access.nft` and loads the file each time that a website
  is made, removed or moved. Refer to [security.md](security.md#firewall).
- The database servers see `169.254.80.1` as the address of each website container. A MariaDB
  user with the access "website" can connect only from this address.

When the last website of a customer on a server is removed, the agent stops the database
containers of the customer and removes their data and their address.

### Limits of the containers

Each database container of a customer has a memory limit, and no swap, on the cgroup of its
service. A customer that uses all its memory does not take memory from the containers of
other customers.

| Setting in `/etc/cloudfactory/agent.json` | Applies to | Default |
| ----------------------------------------- | ---------- | ------- |
| `databaseMemoryMb` | The MariaDB container and the PostgreSQL container of each customer | One quarter of the memory of the server, from 512 MB to 2048 MB |
| `valkeyMemoryMb` | The data of the Valkey container of each customer | 1/32 of the memory of the server, from 32 MB to 256 MB |
| `databaseCpus` | Each database container of each customer, in vCPUs | No limit |

The Valkey container gets two times `valkeyMemoryMb` and 128 MB more, because Valkey needs
memory to write its data file again. With no CPU limit, the kernel gives each container an
equal part of the CPUs when the CPUs are busy.

These limits do not reserve memory. The sum of the limits can be more than the memory of the
server. Each container also uses memory when its databases have no work, so a server with
many customers needs more memory than a server with shared database containers.

### The shared containers of an earlier release

In release 0.1.9 and earlier, all websites of a server used one MariaDB container
(`cf-mariadb`) and one PostgreSQL container (`cf-postgresql`) on the private service address
`169.254.77.1`. Their data is in `/var/lib/cf-db/mariadb` and `/var/lib/cf-db/postgresql`.

A website from such a release keeps the shared containers until an administrator moves the
databases of its server. The agent has the action `MoveDatabases` for one website:

1. Start the database containers of the customer.
2. Stop the website.
3. Copy each database from the shared container to the container of the customer.
4. Make each database user in the container of the customer, with the same password. The
   agent copies the hash of the password, not the password.
5. Start the website with the host names of the new containers.
6. Remove the old copies from the shared containers.

Before step 5, a failure starts the website again with the shared containers. After step 5,
the job only removes the old copies, and a job that runs again does only that. When the last
website of the server has moved, the agent stops the shared containers. Their data
directories stay on the disk. Refer to
[installation.md](installation.md#move-the-databases-of-a-server-from-an-earlier-release).

An agent of this release adds the database network and the firewall rules to a server that
the installer of an earlier release prepared. If the firewall file does not have the rules
of the installer, the job stops and tells you to run the installer again.

### Valkey

Valkey has no databases with names. It has databases with numbers, and all users can use all
numbers by default. Thus the agent makes a Valkey database from two parts:

- A database number from 1 to 1023. Valkey starts with 1024 databases. A new connection is in
  database 0, so no website gets database 0.
- A Valkey user with the name of the database. The access rule `db=<number>` of Valkey 9.1
  keeps the user in its database number. The rules also remove the commands of an
  administrator and the commands that change or show other databases.

The websites of one customer use the same Valkey container, so these rules keep the websites
of one customer apart. The user file of Valkey,
`/var/lib/cf-db/customers/<customer>/valkey/users.acl`, is the record of the users and their
numbers. The agent reads the file through Valkey, gives a new database the smallest free
number of the customer, and sends the number to the panel in the result of the job and in
each report. The agent connects to Valkey as the administrator user `default`. The password
of this user is in `/var/lib/cf-db/customers/<customer>/valkey.secret`, which only root can
read. The agent refuses a Valkey release before 9.1.2, because earlier releases do not have
the rule for database numbers.

All the Valkey databases of a customer on a server use one memory limit (`valkeyMemoryMb`).
When the memory is full, Valkey removes the keys of that customer that were not used for the
longest time. Refer to [installation.md](installation.md#valkey).

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
