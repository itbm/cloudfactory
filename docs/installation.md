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
administrator. Change the password and set two-factor authentication after you sign in.

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
| `--check`          | Do only the checks. Do not change the system.                            |

## What the script does

The script first does all checks. If a check fails, the script shows all problems and stops.
It does not change the system.

Then the script does these steps:

1. Install Podman and the system packages.
2. Hold the container packages, so that a host update cannot change them.
3. Install gVisor and check its SHA-512 sum.
4. Install the AppArmor profiles for Podman and gVisor.
5. Set the image signature policy.
6. Make the service users.
7. Set the firewall.
8. Download the agent and check its signature.
9. Install the agent service and the rollback service.
10. On the first server only: start the panel in gVisor.
11. On the first server only: make the first administrator.
12. Join the panel and start the agent.

If a step fails, the script stops and shows the step. You can run the script again after you
correct the problem.
