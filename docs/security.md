# Security model

This document describes how Cloudfactory release 0.1 protects the servers and the websites.
The product specification, section 6.4, gives the requirements.

## Isolation of websites

- Each website has its own Unix user and its own rootless container.
- Each container runs in the gVisor sandbox. gVisor uses its own network stack
  (`network=sandbox`). On a server with KVM, the agent uses the KVM platform. On other servers,
  it uses the `systrap` platform.
- The container has no capabilities, cannot get new privileges and has a read-only root file
  system.
- Each container has its own network namespace (`pasta`). A container cannot connect to a
  different container directly. The SFTP container is the only container that root runs. It
  uses the bridge network of Podman, and the firewall drops its traffic to the private
  service address.
- Each container port is open only on the private service address `169.254.77.1`. An
  nftables rule lets only root and the edge proxy open a website port there, so a website
  cannot connect to a different website. All websites can reach the database ports.
- Each website user has a UID and a GID from 20000 to 59999. The firewall limits each new
  connection of these users. A website cannot connect to the other services of the server
  (for example SSH and the agent port), to private networks, to link-local addresses (for
  example the metadata service of a cloud provider) or to port 25 of other servers. Refer to
  [Firewall](#firewall).
- The home directory of each website user has mode `0700`. The edge proxy does not read the
  files of a website. The website container serves its static files.
- The shared runtime images in `/var/lib/cloudfactory-images` are read-only for the website
  users. Root owns the stores, and the directory has mode `0750` with the group `cf-sites`, so
  only the website users can read them. A website user writes only its own container layer in
  its own home directory. A custom image stays in the storage of its website user.
- SFTP runs in its own container (`cf-sftp`) in gVisor, not in the SSH service of the host.
  Refer to [SFTP](#sftp).
- The agent writes restored files and staging copies as the website user. A symbolic link in
  a backup cannot make the agent write outside the home directory of the website.

## SFTP

- One SFTP container on each server serves all websites, on port 2200 by default. Port 22 is
  only for the administrators.
- The container runs OpenSSH in gVisor. Root in the container starts sshd, because each session
  must change to the Unix user of its website. The container has only the capabilities
  `SYS_CHROOT`, `SETUID`, `SETGID` and `KILL`, cannot get new privileges, and has a read-only
  root file system.
- After the sign-in, the session runs as the website user, locked in the directory of the
  website (`ChrootDirectory`). The kernel of gVisor checks the file permissions, so a session
  cannot read the files of a different website. A bug in sshd after the sign-in has only the
  permissions of the website user.
- The container sees only the website directories in `/srv/cloudfactory/sites`. It does not
  see the home directories, with the Podman storage of each user.
- SFTP accepts SSH keys only. A key cannot open a shell, a terminal or a forwarded port.
- Only a website user with at least one key is in the user list of the container. The agent
  never puts a user with a UID or GID below 1000 in the list.
- The keys and the user list are read-only in the container. Root owns them.
- The container has its own SSH host keys. A problem in the container does not give the host
  keys of the server.

## Databases

- The name of each database and each database user starts with the Unix user of the website,
  for example `u1a2b3c_shop`. Two accounts cannot use the same name. The agent refuses a name
  without this prefix and the system names, for example `mysql` and `postgres`.
- A database user can have access to more than one database of the same website. Each pair of
  user and database has its own permission: `readonly` or `readwrite`. For example, a sync user
  can have `readonly` access to the production database and `readwrite` access to the staging
  database.
- The agent refuses to make a database that exists. It does not give access to a database of a
  different website.
- MariaDB users connect only from the configured host (`databaseClientHost`, default
  `169.254.77.1`, the address that MariaDB sees for a website container). A user with access from all hosts must use TLS.
- A backup keeps the SHA-256 sum of each database dump. Before a restore, the agent compares the
  dump with this sum. If they are different, the agent stops. The restore uses a temporary
  database user with the permissions of the website only.

## Domains

- The edge proxy serves a domain only after the customer verifies it. To verify a domain, the
  customer adds a DNS TXT record at `_cloudfactory-challenge.<domain>`. A sub-domain of a
  verified domain of the same customer does not need a new record.
- A customer cannot add the domain of the panel, a reseller panel domain or the domain of an
  agent.
- A domain that is not verified does not stop the real owner. The owner can add it and verify it.
- The panel moves a domain away from the website of a different customer only after the new
  owner proves control with the DNS TXT record.
- A website has 50 domains at most (`CF_DOMAINS_PER_WEBSITE`), because each domain starts a
  certificate order. A customer can add 30 domains in one hour and start 10 domain checks in
  one minute. An administrator has no such limit.

## The edge proxy

- The admin API of Caddy listens on a Unix socket, not on a TCP port. Only the user `cf-edge`
  and `root` can open the socket.
- The panel route is always the first route. A website route cannot replace it.

## The agent

- The agent accepts only the typed actions in `agent.proto`. It checks each value, for example
  user names, domains and image references, before it uses the value.
- The agent listens on port 7443 with TLS 1.3 only.
- The agent requires a client certificate. It accepts only the panel certificate with the
  SHA-256 fingerprint that it got when it joined.
- The installation script can limit port 7443 to the IP address of the panel (`--panel-ip`).
- The agent installs only gVisor archives with a SHA-512 sum that is in the agent or in the
  `--sha512` option. It does not download the sum from the archive server.
- The agent refuses to update to an older version. An administrator can allow it for one update
  (`allowDowngrade`). Each agent release has a signed manifest that names the version, the file
  and the SHA-256 hash of the binary. The agent and the installation script refuse a binary
  that does not agree with the manifest of the version that they asked for.
- The agent checks the panel certificate on each connection, also for a resumed TLS session.
- `cf-agent join` accepts only an `https://` panel address, except a loopback address.
- The agent writes the php.ini of each website in `/etc/cloudfactory/websites/<user>/`, which
  only root can change, not in the home directory of the website user. It does not follow a
  symbolic link when it writes a file.
- The agent sends its status report apart from the domain checks. It does some checks at the
  same time and gives the full set a time limit. Thus a slow DNS server of a customer cannot
  make the server show as offline.
- The agent accepts a website user only with a UID from 20000 to 59999, the range that the
  firewall limits.

## The panel

- The panel keeps the certificate of each agent from the join. For each request to an agent,
  it trusts only that certificate. It does not use a public certificate authority.
- A join token is good for one use only. It expires after a short time.
- When an agent sends an event to the panel, it sends its token and its client certificate.
  The fingerprint of the certificate must agree with the fingerprint of the server. Thus a
  token alone is not sufficient. The request comes through the edge proxy. Only the agent on
  the panel server connects directly, to the HTTPS port of the API on the private service
  address. That agent accepts only the certificate of the panel.
- All connections between an agent and the panel use TLS. `CF_AGENT_DIRECT_ADDRESSES` is for
  development only: it lets an agent from the given addresses report with no certificate.
  Keep it empty on a server.
- The panel keeps its data in PostgreSQL. The database listens only on the private service
  address. Each connection needs a password, and the firewall lets only root and the user
  `cf-panel` connect.
- The API accepts browser requests only from the panel domain (`CF_PANEL_DOMAIN`) and the
  origins in `CF_CORS_ORIGINS`.
- The panel keeps job data encrypted in the database. When a job is complete, the panel
  removes passwords from the job data.

## Secrets in the database

The panel does not keep a secret as plain text in its database. The test
`api/tests/Feature/SecretsAtRestTest.php` makes each kind of secret, then looks for it in
each row of each table.

| Secret | How the panel keeps it |
| --- | --- |
| Credentials of the backup storage, for example the S3 keys | Encrypted with `APP_KEY`. The API does not show them again. |
| restic repository passwords, one for each server | Encrypted with `APP_KEY`. The panel sends a password only to its server. Only an administrator can show the passwords, and the audit log records each view. |
| TOTP secret of a user | Encrypted with `APP_KEY`. A secret that is not set up yet is encrypted in the cache. |
| Job data, which can hold database passwords for the agent | Encrypted with `APP_KEY`. The panel removes the passwords when the job is complete. |
| Sessions, if the panel keeps one | Encrypted with `APP_KEY` (`SESSION_ENCRYPT`). |
| User passwords | Bcrypt hash. |
| Sign-in and API tokens | SHA-256 hash. The panel shows a token one time only. |
| Agent tokens and join tokens | SHA-256 hash. |
| Database passwords | Not kept. The panel shows a new password one time only. |
| SFTP | No passwords. SFTP accepts SSH keys only. The panel keeps the public keys. The SFTP container keeps its own host keys in `/var/lib/cloudfactory/sftp-host-keys` (mode `0700`). |

These secrets are files, not rows in the database. Each file has mode 0600 or is in a
directory with mode 0700:

- `APP_KEY` in `/var/lib/cloudfactory/panel/panel.env`. Anyone with this key can decrypt the
  secrets above. Keep a copy of this file in a safe place. If you lose it, you must enter
  the backup credentials again, and users must set up TOTP again.
- The private key of the panel for mutual TLS, in `/var/lib/cloudfactory/panel/pki/panel.key`.

## Users and API tokens

- Users sign in with a password and, if they set it, a TOTP code. A TOTP code works only one
  time. The panel checks the password and the code together and gives one error for both, so
  the answer does not show that a password is correct. A suspended account with no TOTP gets
  the same answer for a correct and an incorrect password.
- The panel sends no email. An administrator or a reseller makes a user, and the panel shows a
  temporary password of 20 characters one time. The first administrator from the installation
  script has a temporary password also. With a temporary password, the API accepts only the
  password change, the account data and the sign-out. The web panel shows only the form
  **Set a new password**.
- An administrator can make, reset and delete all users. A reseller can do this only for the
  users of its own customers. An owner sees the list of its users only. Only a sign-in session
  of an administrator can make, reset or delete an administrator. An API token cannot. A user
  cannot reset or delete its own account in this way. A password reset signs the user out,
  deletes the API tokens of the user and removes a sign-in lock. Each of these changes is an
  audit event.
- On the panel server, root can run `cf-agent panel reset-password EMAIL` and
  `cf-agent panel unlock EMAIL`. This is the procedure when no administrator can sign in.
- In **Settings**, a user changes the password and turns TOTP on or off. Each change needs the
  current password and, when TOTP is on, a code. Only a sign-in session can do this, not an API
  token. A password change signs out the other sessions of the user and deletes the API
  tokens of the user. A new password has 12 or more characters.
- **Sign out** deletes the session token in the panel, so a copy of the token stops. A session
  token expires after 12 hours.
- After 5 failed sign-in attempts from one network, the panel locks the account for that
  network for 30 seconds. Each new failure doubles the time, to a maximum of 1 hour. A network
  is one IPv4 address or one IPv6 `/64` prefix. Somebody who knows an email address cannot
  lock the real user out from a different network.
- After 100 failures in one hour from all networks, the panel locks the account with the same
  times, but only for the networks from which the user did not sign in during the last
  30 days. Thus an attacker with many addresses cannot keep an administrator out of the panel.
  Each lock is an audit event. The panel checks a password hash also for an unknown email address,
  so the response time does not show which accounts exist.
- Each API token has scopes, for example `websites:read` or `websites:write`. A write scope
  also gives the read scope. A token can have an expiry date. A token with the scope
  `users:write` can make users and reset passwords for customers, and can thus get the access
  of those users. Give this scope only to a tool that needs it.
- To make a token, the user must give the password and, if set, a TOTP code. A token needs the
  `tokens:write` scope to make a token. A new token cannot have a scope that the current token
  does not have.
- An owner sees only the websites of the owner. A reseller sees only the websites of the
  reseller's customers. Only an administrator can change servers and images.
- Only an administrator or the reseller of the customer can change the plan of a website. The
  panel checks the website limit of the plan in a database transaction, also for staging copies.
- A reseller can give its customers only its own plan and the plans that an administrator sets
  for it (`allowedPlanIds`). The websites of all customers of a reseller also count against the
  website limit of the plan of the reseller.
- When an administrator suspends a customer, the panel deletes the API tokens of the customer.
  A suspended user cannot use the API. Only an administrator can lift a suspension that an
  administrator set. No request starts a website while its customer, or the reseller of its
  customer, is suspended.
- A customer or a reseller does not see the address of the backup storage, the name and the
  client address of an administrator in the audit log, or the command line and the output of
  a failed job. For these users, the audit log shows "Administrator", and a failed job shows
  the name of the step.
- Only an administrator selects a custom image, for a new website and for a runtime change. The
  owner can go back to the previous image.
- Only a reseller can set a brand. The panel domain of the brand cannot be a reserved domain. A
  reseller must prove that it controls the panel domain, with a verified domain of its own or a
  DNS TXT record. The brand reserves only that exact name, not the names below it.
- A website that has not verified its primary domain does not block the real owner of the
  domain. When the real owner proves control with a DNS TXT record, the panel gives the other
  website an internal name (`parked-<id>.invalid`) and records an audit event.
- The panel checks each PHP setting of a runtime separately. It refuses `-1` (no limit) and a
  memory limit above the memory of the website.
- The panel writes an audit event for each change.

## Signed software

- The release pipeline signs each image with cosign. It also attaches an SBOM and scans each
  image with Trivy.
- Podman on each server accepts only images with a valid signature from the release key. It
  refuses all other images, except from a registry that the administrator gave with
  `--allow-unsigned`. The file `/etc/containers/policy.json` sets this rule. The rule does not
  check the tag of an image (refer to [Known limits](#known-limits)).
- The release pipeline signs the agent binary, the CLI and the installation script. Each
  release has a `.sig` file and a `.sha256` file for each file.
- The agent and the CLI build with the Go release in the `toolchain` line of `go.mod`. CI runs
  `govulncheck`, `composer audit` and `npm audit` on each pull request.
- The edge and panel-web images build Caddy from source with a patched Go release, in place of
  the binary in the upstream Caddy image. The Debian and Ubuntu based images install the
  security updates of the operating system at build time. The daily rebuild reads the list of
  base images from the Containerfiles.
- The PHP runtime images have an ImageMagick policy that turns off coders that websites rarely
  need (EXR, PostScript, PDF, MSVG, URL) and all external programs.
- Each GitHub workflow uses actions at a fixed commit SHA. Each job has only the permissions
  that it needs. Dependabot proposes updates to the actions, the Composer, npm and Go
  packages, and the base images of the containers.
- The edge proxy starts again when a new release changes its unit. An update of the panel
  server changes the panel containers to the images of the new release (refer to
  [installation.md](installation.md#update-to-a-new-release)).

## Web panel

- The panel and its edge route send `Strict-Transport-Security`, a Content Security Policy,
  `X-Content-Type-Options` and `Referrer-Policy`.
- The "Add server" command downloads the signed installation script from GitHub Releases
  (`CF_INSTALL_URL`).
- The CSV export of the audit log puts a single quotation mark before a cell that starts with
  `=`, `+`, `-`, `@`, a tab or a carriage return. A spreadsheet program then does not read the
  cell as a formula.

## The `cf` CLI

- The CLI refuses a panel address that does not start with `https://`, because the token
  would go over the network with no encryption. An `http://` address is permitted only for
  the same computer (`localhost` or a loopback address).
- `cf logout` deletes the session in the panel, then removes the token from the computer.

## Backups

- Each server with the role `web` has its own restic repository and its own repository
  password. The panel sends a password only to its server, with each backup job. A person who
  has root access to one server cannot read the backups of the other servers.
- The agent does not keep the repository password or the credentials of the object storage
  in a file. It gets them with each job and gives them to restic in the environment, not in
  the command line.
- All servers use the same credentials of the object storage. Refer to
  [Known limits](#known-limits).
- A backup keeps the SHA-256 sum of each database dump (refer to [Databases](#databases)).

## Firewall

The installation script sets an nftables table. The table blocks all incoming traffic except
SSH, SFTP (port 2200 by default), HTTP, HTTPS and the agent port. The agent changes only the
SFTP rule, which has the comment `# cloudfactory-sftp`.

The table also limits the outgoing connections:

- Only root and the edge proxy can connect to a website port or a panel port on the private
  service address. Only root and the user `cf-panel` can connect to the database of the panel.
- The chain `websites` gets each new connection of a user with a UID from 20000 to 59999,
  thus each connection that a website opens. It permits the shared databases, DNS, and the
  HTTP and HTTPS ports of the server. It refuses all other addresses of the server, the
  private networks (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`), the
  link-local addresses (`169.254.0.0/16`, `fe80::/10`), the unique local addresses
  (`fc00::/7`), multicast, and port 25 of other servers.
- The options `--allow-website-smtp` and `--allow-website-network` of the installation script
  open port 25 or a private network for the websites. Refer to
  [installation.md](installation.md#connections-from-the-websites).

## Known limits

These items stay open in release 0.1:

- **Image tags.** The signature policy accepts a signed image for each tag of its repository
  (`matchRepository`). A person who can write to the registry can thus give an old signed
  image a new tag. That person cannot add an image with no signature. The correction is to
  pull each image by its digest from the signed release manifest.
- **Service images between releases.** The edge, SFTP, database and panel images get security
  fixes only with a new release. The daily rebuild of the images is off (refer to
  [releasing.md](releasing.md#daily-image-rebuild)), and the rebuild tags are only for the
  runtime images of the websites.
- **Credentials of the object storage.** All servers use the same credentials. A person who
  has root access to one server can delete the backup data of all servers, but cannot read
  it. Turn on versioning or an object lock in the bucket.
- **Custom registry.** An administrator can set a custom registry address. The panel server
  then sends requests to that address.
- **Join token.** The person who has a join token gives the agent address that the panel
  calls. Give a join token only to a person whom you trust with a server.
- **Base images.** The Containerfiles name the base images by tag, not by digest.
