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

## Install the panel

Run the script as root on the first server:

```sh
sh install.sh --domain panel.example.com --email you@example.com
```

If you do not give `--domain`, the script uses the full host name of the server. If you do not
give `--email`, the script uses `admin@` and the domain.

At the end, the script shows the address of the panel and the password of the first
administrator. This password is temporary. When you sign in, the panel shows only the form
**Set a new password**. After you set a new password, open **Settings** and set up an
authenticator app (TOTP). The temporary password is also in your terminal history and in any
log of the script.

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

## Add a server

1. In the panel, go to **Servers > Add server**. Copy the join command.
2. Run the command as root on the new server. For example:

   ```sh
   sh install.sh --join TOKEN --panel https://panel.example.com --panel-ca-sha256 FINGERPRINT
   ```

When the panel has a CA, the command has the fingerprint of its root certificate
(`--panel-ca-sha256`). The new server then trusts a certificate of the panel domain from the
panel CA. It does not trust other certificates from that CA. The fingerprint is in the output of
`cf-agent panel certificate` on the panel server.

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
| `--address HOST`   | The host name or IP address that the panel uses to reach the server.     |
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
3. Add the private service address `169.254.77.1/32` to the loopback interface
   (`cloudfactory-address.service`). The containers publish their ports only on this address.
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
2. In **Object storage**, type a restic repository on a different server from the websites,
   for example `s3:s3.eu-west-2.amazonaws.com/my-bucket/cloudfactory` or `b2:my-bucket:cloudfactory`.
3. Type the credentials of the object storage, for example the access key ID and the secret
   access key. The panel keeps them encrypted and does not show them again.
4. Select **Save policy**.

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

The panel shows the service `disk-quotas` as stopped on a server with no quotas.

The shared runtime images in `/var/lib/cloudfactory-images` belong to root, so they do not
count toward the disk limit of a website. Each runtime image uses about 0.6 to 0.8 GB one time
for each server, not one time for each website.

### Ports

The firewall accepts only SSH, SFTP, HTTP, HTTPS and the agent port. It does not accept the
database ports 3306 (MariaDB) and 5432 (PostgreSQL), so the databases are not open to the
internet. A database user with the access "anywhere" can connect only after you add a rule
for its port to `/etc/nftables.d-cloudfactory.nft`. Give only the addresses that need it.

The firewall also has an output rule for the private service address `169.254.77.1`. Only
root and the edge proxy (`cf-edge`) can open a new connection to a port there, except the
database ports. So a website cannot connect to a different website on the server.

### Connections from the websites

Each website user has a UID from 20000 to 59999. The firewall applies these rules to each new
connection of these users, thus to each connection that a website opens:

| Destination | Result |
| ----------- | ------ |
| MariaDB and PostgreSQL on the private service address | Permitted |
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

Nobody has installed release 0.1 on a real Debian 13 or Ubuntu 26.04 server yet. After you
install it, check these items, and report a problem in the issues of the repository:

- The edge proxy reaches a website through the private service address `169.254.77.1`.
  This was checked with Debian 13 packages in a container, not on a real server.
- A website container can connect to MariaDB and PostgreSQL.
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
