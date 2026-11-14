# Installation

## Requirements

- Debian 13 or Ubuntu 26.04 LTS.
- An x86-64 or ARM64 CPU.
- systemd and cgroup v2.
- 1 GB of memory for a server. 2 GB of memory for the first server with the panel.
- 10 GB of free disk space.
- Ports 80 and 443 must be free on the first server.
- Port 2200 must be free on each server, for SFTP. To use a different port, give `--sftp-port`.

## Verify the script

Each release has `install.sh`, `install.sh.sha256`, `install.sh.sig` and `cosign.pub`. Do these
steps before you run the script:

```sh
curl -fsSLO https://github.com/itbm/cloudfactory/releases/latest/download/install.sh
curl -fsSLO https://github.com/itbm/cloudfactory/releases/latest/download/install.sh.sha256
curl -fsSLO https://github.com/itbm/cloudfactory/releases/latest/download/install.sh.sig
curl -fsSLO https://github.com/itbm/cloudfactory/releases/latest/download/cosign.pub
sha256sum -c install.sh.sha256
cosign verify-blob --key cosign.pub --signature install.sh.sig install.sh
```

GitHub has the address `releases/latest` only for a release that is not a pre-release. If `curl`
gives `error: 404`, each release is a pre-release. Then put the version of the release in each
address: replace `latest/download` with `download/v0.1.9`, for example
`https://github.com/itbm/cloudfactory/releases/download/v0.1.9/install.sh`.

## Install the panel

Run the script as root on the first server:

```sh
sh install.sh --domain panel.example.com --email you@example.com
```

If you do not give `--domain`, the script uses the full host name of the server. If you do not
give `--email`, the script uses `admin@` and the domain.

At the end, the script shows the address of the panel and the password of the first
administrator. This password is temporary. When you sign in, the panel shows only the form
**Set a new password**. After you set a new password, the panel shows the page
**Set up an authenticator app**. Add the key to an authenticator app (TOTP) and type a code.
An administrator and a reseller must do this before they can use the panel. The temporary
password is also in your terminal history and in any log of the script.

### The panel certificate

A new panel gets a TLS certificate from its own certificate authority (the panel CA). This works
for all domains, also `localhost` and `panel.localhost`. At the end, the script shows the path of
the root certificate of the panel CA (`/etc/cloudfactory/panel-ca.crt`) and its SHA-256
fingerprint.

Browsers show a warning for the panel until you add the root certificate to their trusted
certificates. Do these steps on each computer that opens the panel:

1. Get the root certificate. Copy `/etc/cloudfactory/panel-ca.crt` from the server, or download
   it in the panel: **Settings > Panel certificate > Download root certificate**.
2. Compare the SHA-256 fingerprint of the file with the fingerprint that the script showed.
3. Add the file to the trusted certificates of your system or browser. The page in the panel
   shows the steps for macOS, Windows, Debian, Ubuntu and Firefox.

To show the method, the state of the certificate and the fingerprint again, run
`cf-agent panel certificate` on the server.

#### Use Let's Encrypt

When the panel domain is public, you can use a certificate from Let's Encrypt. All browsers trust
it, and you do not need the root certificate.

1. Point the DNS record of the panel domain to the server. Make sure that ports 80 and 443 are
   open.
2. Sign in as an administrator and open **Settings > Panel certificate**.
3. Select **Let's Encrypt**, then select **Change to Let's Encrypt**.

The agent on the panel server waits two minutes for the new certificate. If the certificate does
not come, the agent goes back to the panel CA and the job fails. The panel then shows the error.
With the CLI, run `cf panel-certificate set letsencrypt`. On the server, you can also run
`cf-agent panel certificate letsencrypt`, or install with `--certificate letsencrypt` when the DNS
already points to the server.

Let's Encrypt does not give certificates for `localhost` and for names that end in `.localhost`,
`.local`, `.internal`, `.home.arpa`, `.test`, `.example` or `.invalid`. For these domains, the
panel and the script refuse Let's Encrypt.

#### Go back to the panel CA

Add the root certificate to your browser before you change back to the panel CA. The panel tells
browsers to use only HTTPS for its domain (HSTS). Thus a browser that opened the panel with the
certificate from Let's Encrypt does not let you continue past the warning.

The panel changes back to its CA only when all other servers trust the panel CA. Each agent gets
the fingerprint of the panel CA from the panel. An agent from an earlier release does not. Update
the agent on these servers first. The page shows the names of the servers.

#### A panel on localhost

Browsers send `localhost` and all names that end in `.localhost` to the computer itself. Thus you
can open a panel with such a domain only in a browser on the server, for example for a test:

```sh
sh install.sh --domain panel.localhost
```

The firewall sends the connections from the server itself to ports 80 and 443 to the edge proxy.
This also applies to the full host name of the server, which `/etc/hosts` usually gives as
`127.0.1.1`.

A different server cannot connect to such a panel. Thus you cannot add a second server to it.
The **Add server** dialog shows a warning, and the installation script on the new server stops
at its checks.

A website can also have a name that Let's Encrypt cannot give a certificate for, for example
`shop.localhost` or `shop.test`. The edge proxy signs the certificate of such a name with its
own CA. On the panel server, this is the panel CA, so a browser that trusts the panel CA also
trusts the website. The panel shows the certificate as active when the edge proxy serves it.
A name that ends in `.localhost` always points to the server, so the panel does not do a DNS
check for it.

## Add a server

1. In the panel, go to **Servers > Add server**. Type a name, select the roles, and copy the
   join command. The command downloads the installation script of the release of the panel, so
   the new server gets the same release.
2. Run the command as root on the new server. For example:

   ```sh
   sh install.sh --join TOKEN --panel https://panel.example.com --panel-ca-sha256 FINGERPRINT
   ```

When the panel has a CA, the command has the fingerprint of its root certificate
(`--panel-ca-sha256`). The new server then trusts a certificate of the panel domain from the
panel CA. It does not trust other certificates from that CA. The fingerprint is in the output of
`cf-agent panel certificate` on the panel server.

The two servers must be able to connect to each other:

- The new server connects to the address of the panel on port 443. The script examines this
  connection in its checks, before it changes the system.
- The panel connects to the agent of the new server on port 7443. The new server gives its
  address when it joins, and the script shows this address at the end. The address is the full
  host name of the new server, for example `web2.example.com`, when the server finds an address
  for this name. In other cases, for example with the short host name `web2`, it is the IP
  address that the new server uses for its connection to the panel. If the new server is behind
  NAT, this IP address is a private address. Then give the public address with `--address`, for
  example `--address 203.0.113.20`.

About one minute after the server joins, the panel tests the connection to the agent. If the
panel cannot connect, the server shows as degraded, and the page of the server shows the reason,
for example `Could not resolve host: web2`. Each job for the server fails until the connection
works. Correct the DNS or the firewall, then select **Test the connection** on the page of the
server. With the CLI: `cf servers check SERVER`. If the address is wrong, remove the server and
add it again.

### Remove a server

You can remove a server that has no websites, for example a server that joined with a wrong
address. You cannot remove the server of the panel.

1. In the panel, go to **Servers**, select **Manage** for the server, and select
   **Remove server**. With the CLI: `cf servers remove SERVER`. The panel removes the server
   and its jobs from its lists. It does not change the server itself.
2. To add the server again, delete `/etc/cloudfactory/agent.json` on the server. Then run a new
   join command from **Servers > Add server**.

The backups of the websites that the server had stay in the object storage, in the directory
`servers/<server-id>/` of the repository.

## Options

| Option             | Description                                                              |
| ------------------ | ------------------------------------------------------------------------ |
| `--domain DOMAIN`  | The domain of the panel.                                                 |
| `--email EMAIL`    | The email address of the first administrator.                            |
| `--certificate METHOD` | The TLS certificate of the panel domain: `internal` (from the panel CA) or `letsencrypt`. A new panel uses `internal`. A panel that operates keeps its method. |
| `--join TOKEN`     | Add this server to a panel with this join token.                         |
| `--panel URL`      | The `https://` address of the panel. Use it with `--join`.               |
| `--panel-ca-sha256 SHA256` | The SHA-256 fingerprint of the root certificate of the panel CA. Use it with `--join`. |
| `--name NAME`      | The name of the server in the panel. The default is the host name.       |
| `--roles ROLES`    | The roles of the server: `web`, `database`, `email`, `dns`, `backup`.    |
| `--address HOST`   | The host name or IP address that the panel uses to reach the server. The default is the full host name, for example `web2.example.com`, when the server finds an address for it. In other cases, the default is the IP address that the server uses for its connection to the panel. |
| `--panel-ip IP`    | Accept the agent port only from this IP address.                         |
| `--sftp-port PORT` | The SFTP port of the websites. The default is `2200`. You cannot use port 22. |
| `--registry URL`   | Use a different image registry, for example a mirror.                    |
| `--allow-unsigned REGISTRY` | Let Podman pull unsigned images from this registry. You can use this option more than one time. |
| `--allow-website-smtp` | Let the websites connect to port 25 (SMTP) of other servers. Refer to [Connections from the websites](#connections-from-the-websites). |
| `--allow-website-network NETWORK` | Let the websites connect to this private address or network, for example `10.0.5.0/24`. You can use this option more than one time. |
| `--check`          | Do only the checks. Do not change the system.                            |

## What the script does

The script first does all checks. If a check fails, the script shows all problems and stops.
It does not change the system.

Then the script does these steps:

1. Install Podman, OpenSSH, the quota tools and the other system packages.
2. Turn off the automatic upgrades of the APT timer. The agent installs the host updates, by
   the update policy of the server and in its maintenance window. After an update, the agent
   runs `needrestart`, which restarts the services that use changed files. The script tells
   `needrestart` not to restart the agent, because a restart stops the update job.
3. Add the private service address `169.254.77.1/32` and the database network
   `169.254.80.1/20` to the loopback interface (`cloudfactory-address.service`). The
   containers publish their ports only on these addresses. Each customer gets one address of
   the database network for its database servers.
4. Let systemd give the CPU, I/O, memory and process controllers to the user services
   (`Delegate=`). Rootless Podman needs them for the limits of each website.
5. Hold the container packages, so that a host update cannot change them.
6. Install gVisor. Compare its SHA-512 sum with the sum in the script.
7. Install the AppArmor profiles for Podman and gVisor.
8. Set the image signature policy. Podman refuses all images that have no valid signature,
   except from a registry that you give with `--allow-unsigned`.
9. Make the service users.
10. Make the group of the website users and the directories for SFTP (refer to [SFTP](#sftp)).
    The script does not change the SSH service of the host.
11. Set the firewall. The firewall accepts SSH (port 22), HTTP, HTTPS, the agent port and the
    SFTP port. It sends connections to ports 80 and 443 to the edge proxy, also connections
    from the server itself. It also limits the connections that a website opens (refer to
    [Connections from the websites](#connections-from-the-websites)).
12. Download the agent and check its signature.
13. Install the agent service and the rollback service.
14. On the first server only: start the panel in gVisor. The panel has its own PostgreSQL
    database in the container `cf-panel-db`.
15. On the first server only: make the first administrator, and get the certificate of the
    panel domain from the panel CA (refer to [The panel certificate](#the-panel-certificate)).
16. Join the panel and start the agent. On the first server, the agent connects to the HTTPS
    port of the panel API on the private service address, and accepts only the certificate of
    the panel.

If a step fails, the script stops and shows the step. You can run the script again after you
correct the problem. The script does not keep your options. Give all options again each time
that you run it.

## After the installation

### Backups

The panel does not make backups until you set the object storage.

1. Sign in as an administrator and open **Backups**.
2. In **Object storage**, select the storage type: S3 or compatible, Backblaze B2, Azure Blob
   Storage or Google Cloud Storage.
3. Type the location on a different server from the websites, for example
   `s3.eu-west-2.amazonaws.com/my-bucket/cloudfactory` for S3 or `my-bucket:cloudfactory` for
   B2. The page shows the restic repository that it makes from the type and the location, for
   example `s3:s3.eu-west-2.amazonaws.com/my-bucket/cloudfactory`. You can also paste a full
   restic repository.
4. Type the credentials of the object storage in **Credentials**, for example the access key
   ID and the secret access key. The panel keeps them encrypted and does not show them again.
5. Select **Save policy**.

The time of the nightly backups is in UTC. With the CLI, put the credentials in the
environment, then run `cf backups policy set --target REPOSITORY --credentials-from-env`.

#### One repository for each server

Each server with the role `web` has its own restic repository below the address that you
typed, and its own repository password. For example, with the address
`s3:s3.eu-west-2.amazonaws.com/my-bucket/cloudfactory`, the repository of a server is
`s3:s3.eu-west-2.amazonaws.com/my-bucket/cloudfactory/servers/SERVER-ID`. restic encrypts each
snapshot with the password of its repository. The panel sends a password only to its server.
Thus a person who has root access to one server cannot read the backups of the other servers.

Keep a copy of each repository address and password in a safe place. Without them, you cannot
restore after you lose the panel. To show them, run `cf backups policy password` or send
`GET /api/v1/backup-policy/repository-password` as an administrator. The panel records each
view in the audit log. Do this again after you add a server, because a new server gets a new
password.

All servers use the same credentials of the object storage. A person who has root access to
one server can thus delete the data of all repositories, but cannot read it. To prevent a
loss of data, turn on versioning or an object lock in the bucket.

#### What the panel keeps

- **Nightly backups.** The retention policy on the **Backups** page gives the number of daily,
  weekly and monthly backups of each website.
- **Manual backups.** The panel keeps the 10 newest manual backups of each website. The
  retention policy does not remove them. To change the number, set `CF_MANUAL_BACKUPS` in
  `/var/lib/cloudfactory/panel/panel.env` and restart the panel services.
- **Deleted websites.** The backups of a deleted website stay in the repository for 30 days.
  In this time, you can restore them with restic. To change the time, set
  `CF_DELETED_WEBSITE_BACKUP_DAYS`.

Each Sunday, each server removes the backups of the websites that were deleted more than
30 days ago, and the data that no snapshot uses. A server that is offline does this in a
later week.

#### Restore

A restore of the full website puts the files from the backup into the website and starts the
website again. It does not delete the files that are not in the backup. To delete them, select
**Remove files that are not in the backup**, or use `cf backups restore --remove-new-files`.
A restore of the full website does not restore the databases. Restore each database
separately.

#### Backups from an earlier version

An earlier version of release 0.1 used one repository and one password for all servers. The
panel can restore the snapshots from that time: it uses the old repository and the old
password for them. `cf backups policy password` shows that repository in the row
"(all servers, earlier version)". The panel does not remove data from the old repository.
Delete the old repository in the object storage when you do not need its snapshots.

### Users

The panel does not send email. A new user gets a temporary password, which the panel shows
one time. Give it to the user in a safe way. At the first sign-in, the user must set a new
password.

- To add a customer with its first user, go to **Customers > Add customer** and keep
  **Make a user who can sign in** selected. With the CLI:
  `cf customers create --name NAME --email EMAIL --plan ID --with-user`.
- To add a user to a customer, or to add an administrator, go to **Customers > Users >
  Add user**. With the CLI: `cf users create --name NAME --email EMAIL --customer ID`, or
  `--administrator` in place of `--customer ID`.
- To give a user a new temporary password, select **Reset password** in the row of the user,
  or run `cf users reset-password USER`. For a user who lost the TOTP device, also select
  **Also remove TOTP** (`--remove-totp`).

An administrator can change all users. A reseller can change only the users of its own
customers. Only an administrator who is signed in with a password can make, reset or delete
an administrator. An API token cannot. A user cannot reset or delete its own account in this
way.

To make, reset or delete an administrator, you must confirm that it is you:

- In the web panel, type your password and a code from your authenticator app in the dialog.
- With the CLI, sign in with `cf login`, give your password in `CF_PASSWORD` or on stdin, and
  add `--totp CODE`. For example: `cf users delete admin2@example.com --totp 123456`.

After 5 incorrect attempts, the panel refuses these changes for 15 minutes. For 7 days, the
web panel shows a notice about the change to each other administrator.

A new administrator or reseller must set up an authenticator app (TOTP) after the first
sign-in. Until then, the panel shows only the page **Set up an authenticator app**. An API
token continues to work.

If no administrator can sign in, run these commands as root on the panel server:

```sh
# Give a user a new temporary password. Add --remove-totp for a lost TOTP device.
cf-agent panel reset-password you@example.com
# Remove the sign-in lock of an account after too many failed attempts.
cf-agent panel unlock you@example.com
```

### Update to a new release

1. Download and verify the `install.sh` of the new release (refer to
   [Verify the script](#verify-the-script)).
2. Run the script as root on the panel server, with the same options as for the installation.
   The script installs the new agent. The agent then changes the panel containers and the
   edge proxy to the images of the new release. The data of the panel stays.
3. In the panel, go to **Servers**. For each other server, open the server and select
   **Update agent**. With the CLI: `cf servers update-agent SERVER`. The agent checks the
   signature of the new binary, and the previous binary comes back if the new agent does not
   start.

If you updated the agent on the panel server in a different way, run `cf-agent panel update`
as root there. This command changes the panel to the version of the agent.

After the update from release 0.1.9 or an earlier release, move the databases of each server
to the database servers of each customer. Refer to
[Move the databases of a server from an earlier release](#move-the-databases-of-a-server-from-an-earlier-release).

The update also changes the official runtime images to the tag of the new release. An image
that no website uses changes immediately. An image that websites use shows the new tag on the
**Updates** page, and you start the rollout there. An image with the update policy `manual`
does not change.

### A website that the panel could not make

If the first job of a new website fails, the website has the status **Failed**. The page of the
website shows the cause from the agent, for example an image that the server could not pull.

1. Correct the cause.
2. On the page of the website, select **Try again**. With the CLI: `cf websites retry WEBSITE`.

If the cause is the image, select a different image on the **Runtime** tab. The panel then
makes the website with that image. You cannot start, stop or restart a failed website, and a
rollout does not include it.

### SFTP

The owner of a website signs in with SFTP as the Unix user of the website, on port 2200 (or
the port of `--sftp-port`). For example:

```sh
sftp -P 2200 acme_x7k2@web-01.example.com
```

The SFTP service is the container `cf-sftp`, not the SSH service of the host. Port 22 is only
for the administrators. The agent starts the container when it starts. The container runs
OpenSSH in gVisor. It:

- Locks the user in the directory of the website. The user starts in `/app`.
- Allows only SFTP: no shell, no terminal and no forwarding.
- Accepts SSH keys only. Passwords are refused.
- Reads the SSH keys from `/etc/cloudfactory/sftp/USER.keys`. The agent writes these files.
- Reads the website users from `/etc/cloudfactory/sftp-users`. The agent writes this
  directory. It has only the users that have an SSH key.
- Runs each session as the Unix user of the website. Thus a file that you upload has the
  correct owner on the host and in the website container.

The container keeps its host keys in `/var/lib/cloudfactory/sftp-host-keys`. They are not the
host keys of the server. To show the fingerprints, for example for your customers:

```sh
for f in /var/lib/cloudfactory/sftp-host-keys/*.pub; do ssh-keygen -lf "$f"; done
```

To change the SFTP port of a server:

1. Set `"sftpPort"` in `/etc/cloudfactory/agent.json`, for example `"sftpPort": 2222`.
2. Restart the agent: `systemctl restart cf-agent`.

The agent changes the container and the SFTP rule in `/etc/nftables.d-cloudfactory.nft`. The
panel shows the new port after the next report of the agent (1 minute). Open sessions stop.
If the server has a firewall at your provider (for example a security group), open the SFTP
port there.

Optional: the website users cannot sign in to the SSH service of the host, because they have
no key there and a locked password. To refuse them on the host in all cases, add
`DenyGroups cf-sites` to the SSH configuration of the host.

### Database servers

A customer makes database servers as it makes websites. A database server is one container
with one engine (MariaDB, PostgreSQL or Valkey), its own data and its own limits. Each
database of a website is in one database server of its customer.

- All websites of a customer on a server can use the database servers of the customer on
  that server. A website cannot connect to the database servers of a different customer.
- A customer can have more than one database server of an engine, if its plan permits.
- A database server that uses all its memory, all its CPU time or all its disk space does
  not stop the database servers of other customers.

#### Plans for database servers

A plan has four values for database servers. To see or change them, go to **Customers**,
select the **Plans** tab and select a plan.

| Value | Function |
| ----- | -------- |
| **Database servers** | The number of database servers of a customer with this plan. The database servers of the customers of a reseller also count against the plan of the reseller. |
| **Databases in each one** | The number of databases in a database server with this plan |
| **CPU (vCPU)**, **Memory (MB)** and **Disk (GB)** | The limits of a database server with this plan. The minimum is 512 MB of memory and 1 GB of disk space. |

A new installation has these plans:

| Plan | Database servers | Databases in each one | Limits of a database server |
| ---- | ---------------- | --------------------- | --------------------------- |
| Starter | 3 | 3 | 1 vCPU, 512 MB of memory, 5 GB of disk space |
| Business | 6 | 10 | 2 vCPU, 1 GB of memory, 20 GB of disk space |
| Agency | 12 | 50 | 4 vCPU, 2 GB of memory, 50 GB of disk space |

After an update from an earlier release, each plan permits 3 database servers. A database
server gets the vCPUs of a website of the plan, half of its memory and half of its disk
space, but not less than 512 MB of memory and 1 GB of disk space. The value **Databases** of
the plan was the number of databases of a website. It is now the number of databases in each
database server. Examine your plans after the update.

When you change the limits of a plan, each database server with the limits of the plan gets
the new limits. New limits for the CPU or the memory restart these database servers. A
database server with limits that an administrator set for it keeps them.

#### Make a database server

1. Go to **Databases** and select **New database server**.
2. Select the customer and the engine.
3. Optional: type a name. With no name, the panel gives one, for example `mariadb-2`.
4. If the customer has websites on more than one server, select the server. The websites on
   that server can connect to the database server.
5. An administrator, or the reseller of the customer, can select a plan. The plan gives the
   limits and the number of databases. For other users, the database server gets the plan of
   the customer.
6. Select **Create database server**.

With the CLI: `cf database-servers create --engine mariadb --customer ID`.

The agent starts the container. The first start of a database server needs some minutes on
a slow server. The list shows the host name and the port for the applications, for example
`mariadb.internal:3306`. The first database server of an engine of a customer on a server
has the usual port of the engine (3306, 5432 or 6379). The next one has the next port, for
example 3307. A database server keeps its port.

You do not have to make the first database server. When you make a database for a website
and its customer has no database server of that engine on the server, the panel makes one
with the plan of the customer.

When you make a database, you can select the database server. With no selection, the panel
uses the first database server of the engine that has space for one more database.

#### Change or remove a database server

To change a database server, select **Change** in the list of the database servers. With the
CLI: `cf database-servers set DATABASE_SERVER`.

- You can change the name. The host name and the port do not change.
- An administrator, or the reseller of the customer, can select a different plan. The
  database server gets the limits of the new plan.
- An administrator can set limits that are different from the plan: **Set limits that are
  different from the plan**. With the CLI: `--vcpus`, `--memory-mb` and `--disk-gb`.
- New limits for the CPU or the memory restart the database server. The open connections
  stop, and the data stays.
- More disk space needs no restart. Less disk space stops the database server for a short
  time. If the data needs more space than the new limit, the job fails and nothing changes.

To remove a database server, first remove its databases and its database users. Then select
**Remove**. With the CLI: `cf database-servers delete DATABASE_SERVER`. The agent removes the
container and its data.

If the first job of a database server fails, for example because the server could not pull
the image, correct the cause and select **Try again**. With the CLI:
`cf database-servers retry DATABASE_SERVER`.

#### Limits of a database server

- **Memory.** When MariaDB or PostgreSQL uses more memory than its limit, the kernel stops
  that database server, and systemd starts it again. For Valkey, refer to [Valkey](#valkey).
- **Disk space.** Each database server has a disk image file with the size of its disk
  limit, in `/var/lib/cf-db/servers`. The file uses only the disk space that the database
  server uses. A database server that fills its disk gets the error "no space left on
  device" and cannot write. Remove data, or give it more disk space. The sum of the disk
  limits can be more than the disk of the server, so monitor the free disk space of the
  server.
- **Servers with no loop devices.** The disk image needs the device `/dev/loop-control`. A
  virtual machine and a physical server have it. A container of an LXC host usually does
  not. On such a server, the database servers have no disk limit, and the panel shows
  `database-disk-limits` as an optional part that is off. This does not make the server
  degraded.
- **Connections.** Each MariaDB user can have 100 connections at the same time, and each
  PostgreSQL user 80.
- **Memory of the server.** Each container uses memory, also when its databases have no
  work. The limits do not reserve memory: the sum of the limits can be more than the memory
  of the server. Give a server with many database servers sufficient memory.
- **Unix user.** All database containers of a server run as the Unix user `cf-db`, each in
  its own gVisor sandbox. The database files of all customers have this owner on the host.
- **Disk quota of a website.** The disk quota of a website applies to the files of the
  website only, not to its databases.

#### Move the databases of a server from an earlier release

In release 0.1.9 and earlier, all websites of a server used one shared MariaDB container and
one shared PostgreSQL container. After you update the agent of such a server, its websites
continue to use the shared containers. The page of the server shows the number of these
websites. A website with the shared containers cannot have a Valkey database.

To move the databases to database servers of each customer:

1. Make sure that the last backup of each website is complete (refer to [Backups](#backups)).
2. In the panel, go to **Servers** and open the server.
3. Select **Move the databases**. With the CLI: `cf servers move-databases SERVER`.

The panel starts one job for each website. The databases of the website go to the database
server of its customer that has the usual port of the engine (3306 or 5432), because the
applications connect to that port. If the customer does not have that database server, the
panel makes it with the plan of the customer, also when the plan permits no more database
servers or databases. The job does these steps:

1. Start the database servers.
2. Stop the website.
3. Copy the databases of the website to the database servers.
4. Make the database users there, with the same passwords.
5. Start the website.
6. Remove the old copies from the shared containers.

The website does not operate from step 2 to step 5. The time depends on the size of the
databases. The applications need no change: the host names, the ports, the user names and
the passwords stay the same. An application must connect to the host name `mariadb.internal`
or `postgres.internal`. If an application connects to the address `169.254.77.1`, change it
to the host name before the move.

Before the move, make sure that the disk limit of the plan of each customer is sufficient
for the databases of the customer. If a database server has no space for a copy, the job
fails. Then give the database server more disk space and start the move again.

In step 5, the agent makes sure that a website that answered before the move answers with
the database servers too. A website that did not answer before the move, for example a
website that gives the status 503 during maintenance, moves without this check.

If a job fails before the end of step 5, the agent removes the new copies, and the website
starts again with the shared containers. Find the cause in the job, then select **Move the
databases** again. When the last website of the server has moved, the agent stops the shared
containers. Their data directories, `/var/lib/cf-db/mariadb` and
`/var/lib/cf-db/postgresql`, stay on the disk. Remove them when you are sure that all
websites operate correctly.

A new website on the server uses the database servers of its customer immediately. A staging
copy does too, also when its source website uses the shared containers.

If you changed the firewall rules in `/etc/nftables.d-cloudfactory.nft`, the first job stops
and tells you to run the installation script again on the server. The script writes the
rules of this release.

### Valkey

Valkey is a key-value store that keeps its data in memory. Use it for a cache, for sessions
and for queues. An application that can use Redis can use Valkey with the same client library.

A Valkey database is in a Valkey database server of the customer (refer to
[Database servers](#database-servers)). When you make the first Valkey database of a customer
on a server, the panel makes a Valkey database server with the plan of the customer. The
agent of the server must be from a release that has Valkey. If it is not, the panel does not
let you select Valkey. Update the agent (refer to
[Update to a new release](#update-to-a-new-release)). A website that uses the shared database
containers of an earlier release cannot have a Valkey database until its databases move
(refer to
[Move the databases of a server from an earlier release](#move-the-databases-of-a-server-from-an-earlier-release)).

A Valkey database has three parts:

- A database number from 1 to 1023. The agent gives the smallest number that is free in the
  database server.
- A user with the name of the database. The user has access to this database number only.
- A password. The panel shows it one time only.

To make a Valkey database:

1. On the page of the website, go to **Databases** and select **New database**.
2. Select **Valkey** and type a name. If the customer has more than one Valkey database
   server, you can select one.
3. Select **Create database**.
4. Copy the password.
5. When the job is complete, read the port and the database number in the list of the
   databases.

With the CLI: `cf databases create WEBSITE --engine valkey --name cache`. The command shows
the password, and then the host, the port, the user and the database number.

Give these values to the application:

| Setting | Value |
| ------- | ----- |
| Host | `valkey.internal` |
| Port | The port from the list of the databases. The first Valkey database server of a customer on a server has port `6379`. |
| User | The name of the database, for example `acme_x7k2_cache` |
| Password | The password from the panel |
| Database number | The number from the list of the databases, for example `3` |
| TLS | Off. The connection does not leave the server. |

As an address: `redis://acme_x7k2_cache:PASSWORD@valkey.internal:6379/3`.

The application must select its database number. A new connection starts in database 0, and
the user has no access to database 0. If the application does not select its number, Valkey
answers each command with `NOPERM`.

For a PHP website, turn on the extension `redis` on the **Runtime** tab. The plan of the
website must permit this extension.

Example for Laravel (`.env`). Laravel uses one database for the cache and one for all other
data. Give the two settings the same number:

```sh
REDIS_HOST=valkey.internal
REDIS_PORT=6379
REDIS_USERNAME=acme_x7k2_cache
REDIS_PASSWORD=PASSWORD
REDIS_DB=3
REDIS_CACHE_DB=3
```

Example for WordPress with the plugin Redis Object Cache (`wp-config.php`):

```php
define( 'WP_REDIS_HOST', 'valkey.internal' );
define( 'WP_REDIS_PORT', 6379 );
define( 'WP_REDIS_PASSWORD', [ 'acme_x7k2_cache', 'PASSWORD' ] );
define( 'WP_REDIS_DATABASE', 3 );
```

To make a new password, select **New password** for the user of the database, or use
`cf database-users set WEBSITE USER --reset-password`. Valkey closes the connections that use
the old password.

These limits apply:

- **Memory.** All the Valkey databases of one database server use the memory of that
  database server. Valkey can use a part of the memory limit for its data: half of the limit
  after 128 MB. For example, a database server with 512 MB of memory keeps 192 MB of data,
  and one with 1 GB keeps 448 MB. When the data memory is full, Valkey removes the keys that
  were not used for the longest time, from all databases of that database server. The
  database servers of other customers are not affected. Do not keep data in Valkey that the
  application cannot make again. To give one website its own memory, make a second Valkey
  database server for it.
- **Backups.** A backup has no Valkey data, and a restore does not change it. Valkey keeps
  its data on the disk of the server (an append-only file), so the data stays after a restart.
- **Staging copy.** The data of a Valkey database is not copied. The copy gets a new, empty
  Valkey database with its own number, user and password for each Valkey database of the
  website.
- **Users.** A Valkey database always has one user. The user can connect only from the
  container of the website. You cannot give it more databases or remove it. To remove the
  user, remove the database.
- **Channels.** All databases of a database server use the same channels for publish and
  subscribe.
  A website can use only the channels with a name that starts with its prefix, for example
  `acme_x7k2_events`.
- **Commands.** Valkey refuses the commands of an administrator (for example `CONFIG`, `ACL`,
  `MONITOR` and `CLIENT LIST`) and the commands that change or show other databases (for
  example `FLUSHALL`, `SWAPDB`, `MOVE`, `FUNCTION`, `SCRIPT FLUSH` and `PUBSUB`). To remove
  all the keys of the database, use `FLUSHDB`. Scripts with `EVAL` are permitted.

To change the memory limit, change the database server (refer to
[Change or remove a database server](#change-or-remove-a-database-server)).

### Servers that you installed with release 0.1

Release 0.1 used the SSH service of the host for SFTP, on port 22. Later releases move SFTP to
the container. You do not have to run the script again. When the agent starts with the new
release, it does these steps:

1. It writes the user list of the container from the SSH keys in `/etc/cloudfactory/sftp`. The
   keys stay the same.
2. It starts the container `cf-sftp` on port 2200.
3. It adds the SFTP rule after the rule for port 22 in `/etc/nftables.d-cloudfactory.nft`.
4. It checks that the container answers on port 2200.
5. It removes `/etc/ssh/sshd_config.d/50-cloudfactory-sftp.conf` and reloads the SSH service.
   Open SSH sessions of the administrators do not stop.

If step 4 fails, the agent keeps the SSH configuration of release 0.1, so SFTP continues on
port 22, and the panel continues to show port 22. The agent tries again at its next start. If
you changed `50-cloudfactory-sftp.conf`, the agent does not remove it. Remove it yourself.

Before you update, do these steps:

1. Open port 2200 in the firewall of your provider, if the server has one.
2. Tell the website owners that the SFTP port changes to 2200. Their SFTP client also shows a
   new host key on the first connection, because the container has its own host keys.

### Disk quotas

The disk limit of a plan needs user quotas on the file system of `/srv/cloudfactory`. The
script installs the quota tools, but it does not change your file systems. To turn on the
quotas:

1. Add `usrquota` to the mount options of the file system in `/etc/fstab`. For XFS, use
   `uquota`.
2. Mount the file system again, or reboot.
3. For ext4, run `quotacheck -cum MOUNT` and then `quotaon -u MOUNT`.
4. Restart the agent: `systemctl restart cf-agent`.

On a server with no quotas, the panel shows `disk-quotas` as an optional part that is off. This
does not make the server degraded.

The shared runtime images in `/var/lib/cloudfactory-images` belong to root, so they do not
count toward the disk limit of a website. Each runtime image uses about 0.6 to 0.8 GB one time
for each server, not one time for each website.

### The state of a server

A server is **degraded** when its agent answers but a necessary service does not run. The
necessary services are the agent (`cf-agent`), SSH (`ssh`), the SFTP container (`cf-sftp`), the
edge proxy (`cf-edge.service`) and the firewall rules (`firewall`). The page of the server shows
each service that does not run, the reason, and the command that repairs it when there is one.
A degraded server continues to do work: it gets new websites when no healthy server is
available, and it makes backups and installs scheduled updates.

A server is also degraded when the panel cannot connect to its agent. The page of the server
shows the address and the reason. Each job for such a server fails, and a new website goes to a
different server when one is available. Refer to [Add a server](#add-a-server).

The `firewall` check looks for the table `inet cloudfactory`. If the table is not there, run
`nft -f /etc/nftables.d-cloudfactory.nft` as root.

### Ports

The firewall accepts only SSH, SFTP, HTTP, HTTPS and the agent port. The database servers
publish their ports only on the database network `169.254.80.0/20` of the loopback interface,
so the databases are not open to the internet.

A database user with the access "anywhere" is not limited to the address of the website
containers, and its connections must use TLS. A different computer can connect only after you
send a port of the server to the database address of the customer with your own firewall
rules. Cloudfactory does not make such rules. Give only the addresses that need it. A Valkey
user cannot have the access "anywhere".

The firewall has an output rule for the private service address `169.254.77.1`. Only root and
the edge proxy (`cf-edge`) can open a new connection to a port there, except the ports of
the shared database containers of an earlier release. So a website cannot connect to a
different website on the server.

The firewall also has rules for the database network:

- Only root and the website users can open a new connection to an address of the network.
- A website user can connect only to the address of its customer, on the ports of the
  database servers of its customer. The ports are from 3306 (MariaDB), from 5432
  (PostgreSQL) and from 6379 (Valkey). The set `database_access` has one element for each
  website and each database server of its customer: the UID of the website, the address of
  the customer and the port of the database server. The agent writes the elements of the set
  in `/etc/cloudfactory/firewall/database-access.nft`. Do not change this file.

### Connections from the websites

Each website user has a UID from 20000 to 59999. The firewall applies these rules to each new
connection of these users, thus to each connection that a website opens:

| Destination | Result |
| ----------- | ------ |
| The database servers of the customer of the website | Permitted |
| The database servers of other customers | Refused |
| The shared MariaDB and PostgreSQL containers of an earlier release | Permitted |
| DNS (port 53) | Permitted |
| The addresses of this server, on the HTTP and HTTPS ports | Permitted |
| All other services of this server, for example SSH and the agent port | Refused |
| Private networks, link-local addresses (for example the metadata service of a cloud provider at `169.254.169.254`) and multicast addresses | Refused |
| Port 25 (SMTP) of other servers | Refused |
| All other addresses on the internet, with the ports 465 and 587 for email | Permitted |

To change these rules, run the script again with all your options and one of these options:

- `--allow-website-smtp` lets the websites connect to port 25. Without this option, a website
  must send email through a mail service on port 465 or 587.
- `--allow-website-network NETWORK` lets the websites connect to a private address or
  network, for example a database server at `10.0.5.10`.

Do not make a Unix user with a UID from 20000 to 59999 for a different purpose. The agent
refuses a website user with a UID that is not in this range.

### Proxies in front of the panel

The panel API trusts the `X-Forwarded-For` header only from the edge proxy on the same
server. If you put a different proxy or a load balancer in front of the panel, add its
address to `CF_TRUSTED_PROXIES` in `/var/lib/cloudfactory/panel/panel.env`, separated by
commas. Otherwise the sign-in limits apply to the address of the proxy.

### Check on a real server

Release 0.1.9 was installed in a virtual machine with Debian 13 and in a virtual machine with
Ubuntu 26.04. On both, the installation completed, the administrator signed in, and a website
started and answered through the edge proxy on the private service address `169.254.77.1`.
The virtual machines had no hardware virtualisation, so gVisor used the `systrap` platform.

The database containers of each customer were checked in the virtual machine with Ubuntu
26.04, with release 0.1.9 and the agent of this release:

- The agent added the database network and the firewall rules to the server of release 0.1.9.
- MariaDB and PostgreSQL of a customer started in gVisor, with the memory limit.
- The move of a website with a MariaDB database and a PostgreSQL database. After the move,
  the website container connected to MariaDB and to PostgreSQL with the same host names, user
  names and passwords, and the data was the same.
- A move that failed, and a website that did not answer after the move. The website started
  again with the shared containers.
- A website of a different customer, a website with the shared containers and a different
  Unix user of the server (not `root`) could not connect to the containers of a customer.
- The shared containers stopped when the last website of the server had moved.
- The containers of a customer and their data went with the last website of the customer.
- After a restart of the server, the database network and the firewall rules were there, the
  containers of the customer started, and the website connected to its databases.

The virtual machine is slow, because it has no hardware virtualisation. A new MariaDB
container needed 6 minutes to start there, which is more than the 3 minutes that the agent
waits. Thus the check used an agent with a longer time. The start time of a new database
container on a server with hardware virtualisation was not measured.

These items were not checked on a real server. After you install the release, check them, and
report a problem in the issues of the repository:

- Valkey in gVisor, and a website container that connects to it. The access rules of the
  Valkey users are checked with a real Valkey server when the Valkey image is built, but not
  in gVisor.
- Files that you upload with SFTP have the correct owner in the website container
  (`--userns=keep-id` in gVisor). The SFTP image was checked with Docker and the same
  capabilities, not in gVisor.
- The SFTP container (OpenSSH as root in gVisor, with the bridge network of Podman) answers
  on port 2200 and sees the real client address.
- A backup and a restore of a website with databases, with real object storage.
- The firewall rules for the connections from the websites, also with IPv6. They were checked
  with nftables in a network namespace, not with a real website container.
- A reboot from the panel, and the restart of the services by `needrestart` after an update.
- An update of the panel with the installation script of a later release.
