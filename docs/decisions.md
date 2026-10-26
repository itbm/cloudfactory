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
