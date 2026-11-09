# Installation

## Requirements

- Debian 13 or Ubuntu 26.04 LTS.
- An x86-64 or ARM64 CPU.
- systemd and cgroup v2.
- 1 GB of memory for a server. 2 GB of memory for the first server with the panel.
- 10 GB of free disk space.
- Ports 80 and 443 must be free on the first server.

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
administrator. After you sign in, open **Settings**, change the password and set up an
authenticator app (TOTP). The first password is also in your terminal history and in any log of
the script.

Point the DNS record of the panel domain to the server. The edge proxy then gets a TLS
certificate.

## Add a server

1. In the panel, go to **Servers > Add server**. Copy the join token.
2. Run the script as root on the new server:

   ```sh
   sh install.sh --join TOKEN --panel https://panel.example.com
   ```

## Options

| Option             | Description                                                              |
| ------------------ | ------------------------------------------------------------------------ |
| `--domain DOMAIN`  | The domain of the panel.                                                 |
| `--email EMAIL`    | The email address of the first administrator.                            |
| `--join TOKEN`     | Add this server to a panel with this join token.                         |
| `--panel URL`      | The `https://` address of the panel. Use it with `--join`.               |
| `--name NAME`      | The name of the server in the panel. The default is the host name.       |
| `--roles ROLES`    | The roles of the server: `web`, `database`, `email`, `dns`, `backup`.    |
| `--address HOST`   | The host name or IP address that the panel uses to reach the server.     |
| `--panel-ip IP`    | Accept the agent port only from this IP address.                         |
| `--registry URL`   | Use a different image registry, for example a mirror.                    |
| `--allow-unsigned REGISTRY` | Let Podman pull unsigned images from this registry. You can use this option more than one time. |
| `--check`          | Do only the checks. Do not change the system.                            |

## What the script does

The script first does all checks. If a check fails, the script shows all problems and stops.
It does not change the system.

Then the script does these steps:

1. Install Podman, OpenSSH, the quota tools and the other system packages.
2. Turn off the automatic upgrades of the APT timer. The agent installs the host updates, by
   the update policy of the server and in its maintenance window.
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
10. Set up SFTP for the websites (refer to [SFTP](#sftp)).
11. Set the firewall.
12. Download the agent and check its signature.
13. Install the agent service and the rollback service.
14. On the first server only: start the panel in gVisor.
15. On the first server only: make the first administrator.
16. Join the panel and start the agent.

If a step fails, the script stops and shows the step. You can run the script again after you
correct the problem.

## After the installation

### Backups

The panel does not make backups until you set the object storage.

1. Sign in as an administrator and open **Backups**.
2. In **Object storage**, type a restic repository on a different server from the websites,
   for example `s3:s3.eu-west-2.amazonaws.com/my-bucket/cloudfactory` or `b2:my-bucket:cloudfactory`.
3. Type the credentials of the object storage, for example the access key ID and the secret
   access key. The panel keeps them encrypted and does not show them again.
4. Select **Save policy**.

The time of the nightly backups is in UTC. Each Sunday, the panel also removes the data that
no snapshot uses. With the CLI, put the credentials in the environment, then run
`cf backups policy set --target REPOSITORY --credentials-from-env`.

The panel makes one repository password for all servers. restic encrypts each snapshot with
this password. Keep a copy in a safe place: without it, you cannot restore, for example after
you lose the panel. To show it, run `cf backups policy password` or send
`GET /api/v1/backup-policy/repository-password` as an administrator. The panel records each
view in the audit log.

### SFTP

The owner of a website signs in with SFTP as the Unix user of the website. The script adds
`/etc/ssh/sshd_config.d/50-cloudfactory-sftp.conf`. For the members of the group `cf-sites`
only, sshd:

- Locks the user in the directory of the website. The user starts in `/app`.
- Allows only SFTP: no shell, no terminal and no forwarding.
- Reads the SSH keys from `/etc/cloudfactory/sftp/USER.keys`. The agent writes this file.
- Accepts SSH keys only. Password sign-in is off, and the agent locks the password of each
  website user.

The script does not change the access of other users. Port 22 stays open in the firewall.

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

The firewall accepts only SSH, HTTP, HTTPS and the agent port. It does not accept the
database ports 3306 (MariaDB) and 5432 (PostgreSQL), so the databases are not open to the
internet. A database user with the access "anywhere" can connect only after you add a rule
for its port to `/etc/nftables.d-cloudfactory.nft`. Give only the addresses that need it.

The firewall also has an output rule for the private service address `169.254.77.1`. Only
root and the edge proxy (`cf-edge`) can open a new connection to a port there, except the
database ports. So a website cannot connect to a different website on the server.

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
  (`--userns=keep-id` in gVisor).
- A backup and a restore of a website with databases.
