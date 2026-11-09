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

Release 0.3 (email) and release 0.2 (DNS) will add redirects for ports 25 and 53 in the same
method.

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
