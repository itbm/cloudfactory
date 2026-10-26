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
- Each account has its own Podman network. A website cannot connect to a website of a
  different account.
- Each container listens only on `127.0.0.1`. Only the edge proxy can reach it.
- The home directory of each website user has mode `0700`. The edge proxy does not read the
  files of a website. The website container serves its static files.
- The agent writes restored files and staging copies as the website user. A symbolic link in
  a backup cannot make the agent write outside the home directory of the website.

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
  `127.0.0.1`). A user with access from all hosts must use TLS.
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
  (`allowDowngrade`).

## The panel

- The panel keeps the certificate of each agent from the join. For each request to an agent,
  it trusts only that certificate. It does not use a public certificate authority.
- A join token is good for one use only. It expires after a short time.
- When an agent sends an event to the panel, it sends its token. The request must come through
  the edge proxy, and the fingerprint of the client certificate must agree with the fingerprint
  of the server. Only the agent on the panel server can connect directly, from a loopback
  address (`CF_AGENT_DIRECT_ADDRESSES`).
- The API accepts browser requests only from the panel domain (`CF_PANEL_DOMAIN`) and the
  origins in `CF_CORS_ORIGINS`.
- The panel keeps job data encrypted in the database. When a job is complete, the panel
  removes passwords from the job data.

## Users and API tokens

- Users sign in with a password and, if they set it, a TOTP code. A TOTP code works only one
  time.
- After 5 failed sign-in attempts, the panel locks the account for 30 seconds. Each new failure
  doubles the time, to a maximum of 1 hour. The panel checks a password hash also for an
  unknown email address, so the response time does not show which accounts exist.
- Each API token has scopes, for example `websites:read` or `websites:write`. A write scope
  also gives the read scope. A token can have an expiry date.
- To make a token, the user must give the password and, if set, a TOTP code. A token needs the
  `tokens:write` scope to make a token. A new token cannot have a scope that the current token
  does not have.
- An owner sees only the websites of the owner. A reseller sees only the websites of the
  reseller's customers. Only an administrator can change servers and images.
- Only an administrator or the reseller of the customer can change the plan of a website. The
  panel checks the website limit of the plan in a database transaction, also for staging copies.
- When an administrator suspends a customer, the panel deletes the API tokens of the customer.
  A suspended user cannot use the API.
- Only a reseller can set a brand. The panel domain of the brand cannot be a reserved domain.
- The panel checks each PHP setting of a runtime separately. It refuses `-1` (no limit) and a
  memory limit above the memory of the website.
- The panel writes an audit event for each change.

## Signed software

- The release pipeline signs each image with cosign. It also attaches an SBOM and scans each
  image with Trivy.
- Podman on each server accepts only images with a valid signature from the release key. It
  refuses all other images, except from a registry that the administrator gave with
  `--allow-unsigned`. The file `/etc/containers/policy.json` sets this rule.
- The release pipeline signs the agent binary, the CLI and the installation script. Each
  release has a `.sig` file and a `.sha256` file for each file.
- Each GitHub workflow uses actions at a fixed commit SHA. Each job has only the permissions
  that it needs. Dependabot proposes updates to the actions.

## Firewall

The installation script sets an nftables table. The table blocks all incoming traffic except
SSH, HTTP, HTTPS and the agent port.
