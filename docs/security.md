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
- Root owns the directory of a website (`/srv/cloudfactory/sites/<user>`). The website user
  owns only the directory `app` in it. As root, the agent makes and changes only these two
  directories, and it stops if one of them is not a directory. The website user makes the
  directory `app/public`. Thus a symbolic link that a website puts in its own files cannot
  make root change the files of a different website.
- The server applies the disk limit of a website with a user quota. On a file system with no
  user quotas, a new website with a disk limit has its own file system: a disk image, which
  the server mounts on the directory of the website with the options `nodev` and `nosuid`.
  Thus a website that fills its disk cannot fill the disk of the server. Only root can open
  the directory of the disk images (`/srv/cloudfactory/disks`). Refer to
  [architecture.md](architecture.md#disk-limit-of-a-website).

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
  see the home directories, with the Podman storage of each user, or the disk image files.
  It gets the mounts of the host below `/srv/cloudfactory/sites` (`rslave`), and it cannot
  give a mount to the host.
- SFTP accepts SSH keys only. A key cannot open a shell, a terminal or a forwarded port.
- Only a website user with at least one key is in the user list of the container. The agent
  never puts a user with a UID or GID below 1000 in the list.
- The keys and the user list are read-only in the container. Root owns them.
- The container has its own SSH host keys. A problem in the container does not give the host
  keys of the server.
- When SFTP goes off for a website, or the last key is removed, the agent stops the open SFTP
  sessions of that website user. A session does not stay open after the access is removed.
- When a customer is suspended, the panel turns off SFTP for the websites of the customer and
  keeps the keys. When the suspension ends, the panel turns SFTP on again for these websites.
  The panel refuses to turn on SFTP for a suspended customer and for a website in the state
  `failed`.

## Files, logs, cron jobs and deploys

- The agent does each file operation, each read of the logs, each cron command and each
  build as the Unix user of the website. The root helper has no new command and no new
  operation for these functions.
- The file manager opens the directory of the website as a root directory (`os.Root` in
  Go). The kernel refuses a path that goes out of it, also through a symbolic link. The API
  and the agent check each path with the same rule (`filePath` in `schema/input-rules.json`).
- A cron command runs in the gVisor container of the website. A build runs in a second
  gVisor container with all capabilities dropped, a read-only root file system and the
  limits of a website. The firewall rules for the website user apply to both.
- A customer controls the commands of its cron jobs and its builds. This gives the customer
  no new rights: the customer can run the same code in its website.
- The agent keeps a limited quantity of the output of each command: the first 8 MiB of the
  standard output, and the start and the end of the error output. It discards the remainder
  while the command runs. A command of a customer thus cannot use all the memory of the
  agent with its output. A cron job keeps only the last 16 KiB of its output.
- The panel encrypts the private deploy key, the webhook token and the build secrets in its
  database. The API gives the names of the secrets, not their values. The private key and
  the secrets are in a job only when the panel sends it to the agent.
- The address of a webhook has a random token. Each person who has the address can start a
  deploy of the branch, but cannot change what the deploy does. The route accepts 30
  requests each minute. The panel does not start a second deploy while one is in progress.
- A user needs the token scope for websites to use these routes. An owner sees only its own
  websites. For a suspended customer, the panel refuses each change of a file, a cron job and
  a Git source, and each deploy.
- The panel records each change of a file, a cron job and a Git source in the audit log.
  It does not record the content of a file.

## Databases

- Each customer has its own database servers on a server. A database server is one
  container with one engine: MariaDB, PostgreSQL or Valkey. Each container is its own gVisor
  sandbox with its own data, superuser password and limits for the CPU, the memory and the
  disk space. A customer thus cannot read the data of a different customer through a fault
  of a database engine, and cannot use the CPU time, the memory, the disk space or the
  connections of the database server of a different customer.
- The disk limit of a database server is a user quota of its Unix user. On a file system
  with no user quotas, it is a file system with the size of the limit (a disk image on a
  loop device). A database server that fills its disk cannot fill the disk of the server.
  Refer to [architecture.md](architecture.md#disk-limit).
- The firewall lets a website connect only to the database address of its customer, on the
  ports of the database servers of its customer (refer to [Firewall](#firewall)). A website
  cannot open a connection to the database servers of a different customer, also when it
  knows a user name and a password.
- The websites of one customer on a server can connect to all database servers of the
  customer there. The database users and their permissions keep these websites apart. A
  customer that needs more separation for one website makes a second database server for
  it: the websites then do not use the same memory, disk space or connections.
- Each database user has a limit for its connections at the same time: 100 for MariaDB and
  80 for PostgreSQL. The application of one website thus cannot use all connections of a
  database server.
- A PostgreSQL user cannot connect to the databases `postgres` and `template1`.
- The plan of a customer limits the number of its database servers. The plan of a database
  server limits the number of its databases. Only an administrator sets limits that are
  different from the plan.
- The name of each database and each database user starts with the Unix user of the website,
  for example `u1a2b3c_shop`. Two accounts cannot use the same name. The agent refuses a name
  without this prefix and the system names, for example `mysql` and `postgres`.
- A database user can have access to more than one database of the same website. Each pair of
  user and database has its own permission: `readonly` or `readwrite`. For example, a sync user
  can have `readonly` access to the production database and `readwrite` access to the staging
  database.
- The agent refuses to make a database that exists. It does not give access to a database of a
  different website.
- A MariaDB user with the access "website" connects only from `169.254.80.1`, the address
  that a database server of a customer sees for a website container. A user with access from
  all hosts must use TLS.
- A backup keeps the SHA-256 sum of each database dump. Before a restore, the agent compares the
  dump with this sum. If they are different, the agent stops. The restore uses a temporary
  database user with the permissions of the website only.
- A Valkey database is a database number and a user with the name of the database. The access
  rules of Valkey keep the user in its database number: the user cannot select, move, copy to,
  swap or empty a different database. The user gets no command of an administrator (for
  example `CONFIG`, `ACL`, `MONITOR`, `CLIENT LIST` and `CLIENT KILL`), no function, and no
  list of the channels. It can use only the channels with a name that starts with the prefix
  of the website. Each connection needs a user and a password: Valkey has no user with no
  password.
- The Valkey users are not limited to a client address in Valkey, because each connection of
  a website comes from the same address. The firewall lets only the websites of the customer
  connect to a Valkey database server, and each connection needs the password. A Valkey user
  cannot have the access "anywhere".
- `INFO` shows numbers of the Valkey server to each Valkey user, for example the number of
  keys of each database number and the memory in use. It shows no keys and no values.
- All the Valkey databases of one database server use the memory of that database server. A
  website that fills the memory makes Valkey remove keys of the other websites that use the
  same database server. It cannot remove keys of a different database server or of a
  different customer. Thus Valkey is for data that an application can make again.
- Each database is in a database server of the customer of its website. Cloudfactory has no
  database container that the websites of different customers share. The agent refuses a
  request for a database or a database user that has no database server, and a database
  server of a different customer.

## Email

- One mail server (Stalwart) on a server has the mail of all customers of that server. The
  panel permits a change of a mail domain, a mailbox or an address only to a user of its
  customer, to the reseller of that customer and to an administrator.
- The container `cf-mail` runs in gVisor as the Unix user `cf-mail`, not as root. It has no
  capabilities, cannot get new privileges, and has a read-only root file system.
- The management API of the mail server has all rights on the mail. It is a part of the JMAP
  API, on the private service address `169.254.77.1:19025`, which the firewall keeps for root
  and the edge proxy. Each request also needs the password of the administrator. The worker of
  the agent connects through the root helper.
- The worker cannot run a program as `cf-mail`, and cannot run a program in the container.
  The root helper lets it only pull the image of this agent version, and start and stop the
  service.
- The root helper makes the unit, the settings and the firewall rules from its own settings.
  The worker sends only the host name, and the root helper examines it.
- The root helper copies the certificate of the mail host name from the storage of the edge
  proxy. The user `cf-edge` owns that storage, so the root helper reads the files with no
  symbolic links, and accepts only a certificate for the host name with its own key.
- The panel sends the bcrypt hash of a mailbox password to the agent. The panel does not keep
  the password, and the job record has no hash after the job.
- A customer adds a domain that a different customer could own only after a DNS TXT record
  shows that the customer controls the domain, as for a website domain.
- The mail server opens connections only to the DNS and to port 25 of public addresses. It
  cannot connect to a service of the server, to a private network or to the metadata service
  of a cloud provider.
- The mail server refuses mail for a domain that it does not have (no open relay). A mail
  program must sign in to send mail.
- A private key that the panel makes is protected with the password of the mailbox, and the
  panel keeps it encrypted with the key of the application. Only the customer of the mailbox,
  its reseller and an administrator can download it, and each download is an audit event.
  Somebody with the database of the panel and the key of the application still needs the
  password of the mailbox to use the private key.
- The webmail runs as the Unix user `cf-webmail`, not as `cf-mail`. It cannot read the data
  store of the mail server. It has no capabilities, cannot get new privileges, and has a
  read-only root file system. The worker cannot run a program as `cf-webmail` or in its
  container.
- The edge proxy sends only the JMAP and DAV paths of the mail host name to the mail server.
  The JMAP path is also the path of the management API. Thus the edge proxy refuses a
  request that signs in as the administrator of the mail server (`admin`), with status 403.
  The administrator can sign in only on the private service address.
- The admin dashboard of the webmail has a random password of 64 characters, which no person
  knows. Only the start script of the image uses it, on the address of the container, to
  install the PGP plugin. The edge proxy refuses the paths of the dashboard and of the setup,
  except `GET` requests for the paths that users need. The account of the administrator of
  the mail server does not open the dashboard.
- The PGP plugin is a pinned release. The build of the image stops when the package does not
  have the expected SHA-256 hash. Users cannot upload plugins. The plugin runs in the
  privileged sandbox of Bulwark: it has the same origin as the webmail. The build adds
  `images/webmail/pgp-plugin/provision.js` to the plugin, and the image has no other change
  to the plugin.
- The files of each mailbox on the mail server have the private keys that the panel made,
  each protected with the password of the mailbox. Only the owner of the mailbox and the
  administrator of the mail server can read the file. A person who gets the data of the mail
  server also needs the password to use the keys. The keys are not in the job records: the
  panel adds them each time that it sends the job.
- The plugin reads the key file with the password of the mailbox. It does not keep the
  password. It keeps the keys in the browser, encrypted with that password.
- In a mail cluster, the database and the cluster store listen only on the private network of
  the cluster. Only the members can connect to it, and only root and `cf-mail` can connect to
  the cluster ports on a member. The secrets of the cluster are encrypted in the database of
  the panel, and are not in the job records.
### Adminer

- Each customer has its own Adminer container, in gVisor, with its own Unix user and limits.
  No two customers share an Adminer process.
- The container is a member of the networks of its customer only. The firewall lets it
  connect to the MariaDB and PostgreSQL database servers of that customer, and to nothing
  else in the private networks.
- Adminer opens only MariaDB and PostgreSQL, and only a server name that ends in `.internal`.
  It refuses other names before it connects. This is a second check: the firewall is the
  first.
- The user types no password. The panel makes a temporary database user with access to one
  database only, and gives the password to the browser one time. The panel does not keep the
  password. The user is removed after 60 minutes, or when its database is removed.
- A user who can manage the databases of a website (the scope `databases`) can open them in
  Adminer. The panel refuses this for a suspended customer, and writes an audit event for each
  sign-in.
- Adminer has no permanent login, sends `Cache-Control: no-store`, and refuses frames from
  other sites (`frame-ancestors 'none'` and `X-Frame-Options: DENY`).
- Adminer does not ask `adminer.org` for a new version, and its content security policy lets
  the browser connect only to Adminer (`connect-src 'self'`) and refuses workers
  (`worker-src 'none'`).
- Each sign-in has its own random address, which Adminer does not show on a different
  sign-in. Adminer keeps its session cookie on that address only. The address stops working
  when the temporary user expires.

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

## DNS zones

- A zone belongs to one customer. A user can see and change only the zones of the customers
  that the user controls, and each DNS request needs the token scope `dns`.
- A customer proves control of a domain before the customer makes a zone for it. The proof is
  a DNS TXT record at `_cloudfactory-challenge.<domain>`, as for the domain of a website. The
  value of the record is a keyed hash of the customer and the name, so the record of one
  customer is not a proof for a different customer. An administrator needs no proof.
- A customer needs no new record for a name in a zone that the customer has already, or for
  a verified domain of a website of the customer and the names below it.
- All customers use the same name servers. Thus name servers that point to the panel are not
  a proof for a new zone, and a zone of one customer does not let a different customer make
  a zone below it or above it. For a name that is the same as, above or below a zone of a
  different customer, the TXT record is the proof.
- A domain that uses the name servers of the panel and has no zone cannot show a TXT record.
  An administrator makes the zone for such a domain.
- A customer cannot make a zone for the domain of the panel, the host name of a server or a
  name server host name.
- The plan of a customer gives the number of its zones. A zone has 1000 records at most. A
  customer can make 60 DNS changes in one minute.
- The panel and the root helper check each record with the same rules
  (`schema/input-rules.json`): the type, the name, the TTL, and the value for the type. The
  root helper writes the zone file from the checked records. A record cannot have a new line,
  a `$` directive or an open quotation mark.
- The container `cf-dns` runs in gVisor with a Unix user that has no other work. It can read
  the zone files and cannot change them. It can write only its keys and signatures.
- The private DNSSEC key of a zone is encrypted in the panel database. On a DNS server, only
  the user `cf-dns` and root can read it.
- The DNS server answers only for its zones. It has no recursion, and it limits the rate of
  the answers to one network.

## The edge proxy

- The admin API of Caddy listens on a Unix socket, not on a TCP port. Only the user `cf-edge`
  and `root` can open the socket.
- The panel route is always the first route. A website route cannot replace it.
- For a stopped website, the route gives a fixed page with the status 503 ("This website is
  not available"). The proxy does not connect to the port of the stopped website, and the
  page shows no data of the server.

## The agent

- The agent accepts only the typed actions in `agent.proto`. It checks each value, for example
  user names, domains and image references, before it uses the value.
- The agent listens on port 7443 with TLS 1.3 only.
- The agent requires a client certificate. It accepts only the panel certificate with the
  SHA-256 fingerprint that it got when it joined.
- The installation script can limit port 7443 to the IP address of the panel (`--panel-ip`).
- The agent installs only gVisor archives with a SHA-512 sum that is in the agent or in the
  `--sha512` option. It does not download the sum from the archive server.
- The agent refuses to update to an older version. An administrator can allow it for one
  update: make the file `/etc/cloudfactory/allow-agent-downgrade` on the server as root, and
  then start the update with `allowDowngrade` (`cf servers update-agent --allow-downgrade`).
  The root helper removes the file when it uses it. The option of the panel alone is not
  sufficient, because the worker of the agent sends it, and the root helper does not trust
  the worker. Each agent release has a signed manifest that names the version, the file
  and the SHA-256 hash of the binary. The agent and the installation script refuse a binary
  that does not agree with the manifest of the version that they asked for.
- The agent checks the panel certificate on each connection, also for a resumed TLS session.
- An agent trusts a certificate of the panel domain from a public CA, or from the panel CA with
  the SHA-256 fingerprint in `agent.json`. It does not trust the panel CA for other names. It
  gets a new fingerprint only from the panel, on a connection that it checked.
- `cf-agent join` accepts only an `https://` panel address, except a loopback address.
- The agent writes the php.ini of each website in `/etc/cloudfactory/websites/<user>/`, which
  only root can change, not in the home directory of the website user. It does not follow a
  symbolic link when it writes a file.
- The agent sends its status report apart from the domain checks. It does some checks at the
  same time and gives the full set a time limit. Thus a slow DNS server of a customer cannot
  make the server show as offline.
- The agent accepts a website user only with a UID from 20000 to 59999, the range that the
  firewall limits.

## The root helper of the agent

The agent service has two processes:

- The **root helper** (`cf-agent serve`) runs as root. systemd starts it. It does not listen
  on the network, and it does not connect to the panel.
- The **worker** (`cf-agent work`) runs as the Unix user `cf-agent`. It has the agent port,
  the connection to the panel, the jobs and the state of the websites. It has no
  capabilities, and a setuid program cannot give it new rights (`no_new_privs`). The user
  has no home directory, no shell and no containers.

The worker has most of the code of the agent. It reads the data that comes from the network,
and it starts the work in the directories that the customers control. A fault in that code
gives an attacker the rights of `cf-agent`, not the rights of root.

The worker asks the root helper for each step that needs root. The two processes use the
Unix socket `/run/cloudfactory-agent/gate.sock`. Only root and the group `cf-agent` can open the
socket, and the root helper accepts only a process with the UID of the worker. The root
helper does only the work of three lists:

| List | What the worker can ask for |
| --- | --- |
| Commands | A program as the Unix user of a website (`runuser -u <user>`). A command of a fixed form as the Unix user of a database server or as `cf-edge`, for example a database client in the container of that database server. Some root commands with fixed arguments: the commands of the status report, the updates of the operating system, the reboot, and restic with paths that the root helper examines. |
| Operations | Root work that has a name, for example `user.ensure`, `unit.write`, `user.quota`, `site.resize` and `db.prepare`. Each operation has typed values, and the root helper examines each value. |
| Connections | The admin socket of the edge proxy, and some ports on the private service address and on the database network. One of these ports is the management API of the mail server. |

These rules apply to the three lists:

- The worker gives names, not paths: a Unix user, the ID of a database server, the ID of a
  job. The root helper makes each path from its own settings.
- All work in the files of a website runs as the Unix user of that website. Root opens a
  file in a directory of a different user only in these places: the home directory of a
  website user, the files of a restore and the records of the jobs. There, the root helper
  uses `openat2` with the flags `RESOLVE_BENEATH` and `RESOLVE_NO_SYMLINKS`. Thus a symbolic
  link cannot send root to a different file.
- The worker cannot give the text of a unit of a database server or of `cf-edge`, and it
  cannot run a program of its choice as these users. The root helper makes
  these units from its own settings.
- The root helper makes, mounts and changes the disk image of a website and of a database
  server. The worker gives only the Unix user of the website or the ID of the database
  server, and the disk limit. The root helper makes the path of the image file, the mount
  point and the mount unit from its own settings, and it runs `mkfs.ext4`, `resize2fs` and
  `e2fsck` only on that image file. The worker cannot mount a file of its choice.
- The root helper makes the Unix user of a database server (`db-<id>`) from the ID of the
  database server. It uses such a user only when the UID of the user is in the range from
  10000 to 19999. As that user, the worker can run only a database client in the container
  of that database server. It cannot use the container of a different database server.
- Root owns the directories that lead to the data directory of a database server. In the
  data directory, which the Unix user of the database server owns, the root helper writes
  only the user file of Valkey, with `openat2` and no symbolic links.
- The root helper examines the signature and the version of a new agent release again. It
  installs a release only when the signed manifest has the line `rootHelper=1`. Thus the
  worker cannot install a release that has no root helper.
- The service goes back to the previous agent binary when the agent stops five times in
  five minutes. After the update from a release with no root helper, the root helper
  removes that previous binary when the worker has operated for 10 minutes. Thus a person
  who can stop the worker again and again cannot make the service go back to an agent that
  is one root process.
- The root helper connects the worker to the port of a website, to the HTTPS port of the edge
  proxy, to the SFTP port and to the agent port of the panel on the private service address.
  It also connects the worker to a Valkey database server. It refuses the database of the
  panel and all other addresses. Thus the firewall has no rule for the user `cf-agent`.
- The root helper writes to the log of the service each command and each connection that it
  refuses, and each operation that is not in its list (`journalctl -u cf-agent`). The lines
  start with `root helper: refused`.

The worker can read these files of the agent:

- `/etc/cloudfactory/agent.json` (mode `0640`, group `cf-agent`), which has the token of the
  agent.
- The TLS key of the agent in `/var/lib/cloudfactory/pki/` (mode `0640`, group `cf-agent`).
- The records of the jobs in `/var/lib/cloudfactory/jobs/`. The worker owns this directory.

A person who controls the worker cannot do these things:

- Run a program of their choice as root, as the Unix user of a database server, or as
  `cf-edge` or `cf-panel`.
- Read or change a file of root, for example `/etc/shadow`, an SSH key or a unit of the
  system.
- On the panel server: read the files of the panel, for example `APP_KEY` and the private
  key of the panel, or connect to the database of the panel.
- Change the firewall. The exceptions are the elements of the set `database_access`, the
  database addresses and the elements of the sets of the mesh and of the networks, which the
  root helper examines.
- Read the private tunnel key of the server, or the pre-shared keys after the root helper has
  them. The worker sees a pre-shared key only in the job that brings it from the panel.
- Install an agent binary that the release key did not sign.

The boundary does not make the worker safe to lose. A person who controls the worker can
still do these things, because they are the function of the agent:

- Run a program as the Unix user of each website on the server.
- Use the administrator account of each database server, through the database client in its
  container.
- Change the configuration of the edge proxy through its admin API. That includes the routes
  of all websites and, on the panel server, the route of the panel. It also includes the
  files that the edge proxy can read: the TLS keys of the websites and the private key of the
  panel CA.
- Start a backup or a restore, install the updates of the operating system and reboot the
  server.
- Use the token and the TLS key of the agent to send reports and job events to the panel.
- Install an earlier agent release that has a root helper and a valid signature.

If the root helper cannot start the worker, the agent operates as one root process, as in
release 0.1.9. The websites then continue to operate, the root helper writes the cause to the
log, and the panel shows `agent-root-helper` as an optional part of the server that is off.
The setting `CF_AGENT_ONE_PROCESS=1` has the same effect. Refer to
[installation.md](installation.md#the-two-processes-of-the-agent).

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
- A new panel has a certificate from its own CA (the panel CA). Caddy keeps the private key of
  the CA in the data directory of the edge proxy, `/var/lib/cloudfactory/caddy`. Caddy does not
  add the root certificate to the trust store of the server. A person who adds the root
  certificate to a browser lets the CA sign certificates for all names in that browser. Keep the
  data directory of the edge proxy safe, or use Let's Encrypt for a public panel domain.
- The panel changes to Let's Encrypt only after the edge proxy serves a certificate that a
  public CA signed. If the certificate does not come, the agent sets the previous method again.
  Thus a failed ACME order does not lock out the administrator.
- The panel keeps job data encrypted in the database. When a job is complete, the panel
  removes passwords from the job data.

## Secrets in the database

The panel does not keep a secret as plain text in its database. The test
`api/tests/Feature/SecretsAtRestTest.php` makes each kind of secret, then looks for it in
each row of each table.

| Secret | How the panel keeps it |
| --- | --- |
| Credentials of the backup storage, for example the S3 keys: the shared key, and the key of each server | Encrypted with `APP_KEY`. The API does not show them again. The panel sends the key of a server only to that server. |
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
- The private key of the panel CA, in `/var/lib/cloudfactory/caddy/caddy/pki/authorities/cloudfactory/`.

## Users and API tokens

- Users sign in with a password and a TOTP code, with a passkey, or with a single sign-on
  provider. An administrator and a
  reseller must have TOTP. A website owner can turn it on. A TOTP code works only one
  time. The panel examines the password first, and asks for the code only when the password is
  correct and TOTP is on for the user. Thus a user with no TOTP does not see the code step, and
  a wrong password gives its error immediately. The request for the code shows that the
  password is correct. This is the same as for most services. The sign-in limits protect the
  password: a wrong password and a wrong code are failed attempts. A suspended account with no
  TOTP gets the same answer for a correct and an incorrect password.
- An administrator or a reseller makes a user, and the panel shows a temporary password of
  20 characters one time. The first administrator from the installation script has a temporary
  password also. With a temporary password, the API accepts only the password change, the
  account data and the sign-out. The web panel shows only the form **Set a new password**.
- A user who does not know the password can ask for an email with a link (**Forgot your
  password?** on the sign-in page). The sign-in page shows the link only when an SMTP server
  can send the email. Obey these rules:
  - The answer is the same for each email, also for an email with no user. The panel checks a
    password hash also for an unknown email, so the response time does not show which accounts
    exist.
  - The link goes to the panel domain of the brand of the user, not to the domain of the
    request. A request with a different `Host` header cannot send the link to a different site.
  - The code of the link is after `#`. The browser does not send it to a server, so no access
    log has it. The panel keeps only a bcrypt hash of the code.
  - A link works one time only, for 60 minutes. A new link stops the earlier link. The panel
    sends one link in each minute for one user, and accepts 5 requests in each minute from one
    address.
  - The panel keeps a copy of each email in **Sent emails**, and an administrator or the
    reseller of the user can read it. The copy has a placeholder in the place of the link. The
    queue has the link encrypted with `APP_KEY`. Thus nobody can send the email again: the
    user asks for a new link.
  - A new password signs out all sessions of the user, deletes the API tokens of the user, and
    removes the sign-in lock for the network of the request. It does not turn off TOTP: the user
    still needs a code to sign in. A user who lost the authenticator app asks an administrator
    or the reseller.
  - The template of the email must have the link (`{{resetUrl}}`) in the text, and not in the
    subject. A user cannot turn off this email.
- A user can add passkeys in **Settings** and then sign in with a passkey, in place of the
  password and the TOTP code (FR-103). The panel uses WebAuthn. Obey these rules:
  - Each passkey must verify the user, with a fingerprint, a face or a PIN. Thus a passkey is
    a sign-in with two factors: the device and the person. The panel refuses a passkey that
    only shows that a person is present.
  - A passkey works only on the panel domain where the user added it: the panel domain or
    the panel domain of a reseller brand. The panel examines each answer against the domain
    of the passkey, not against the `Host` header, and accepts only the exact HTTPS origin of
    that domain. A different domain or a subdomain cannot use the passkey. Thus a fake site
    cannot get a sign-in from a passkey.
  - Passkeys do not work on the address of a website or on an IP address. The sign-in page
    shows the passkey button only on a panel domain.
  - To add or remove a passkey, the user gives the password and, when TOTP is on, a code. Only
    a sign-in session can add, rename or remove a passkey, not an API token. A stolen session
    alone cannot add a passkey. After 5 incorrect attempts, the panel refuses these changes
    for 15 minutes.
  - A challenge works one time only, for 5 minutes. The panel asks for no attestation, so it
    does not know the make of the device. The panel keeps only the public key of a passkey, not a secret.
  - At the start of a sign-in, the browser shows the passkeys of the domain. The panel does
    not ask for an email, so the answer shows no data of a user.
  - A passkey is in a password manager or in a cloud account when **Synced** shows next to
    it. Then it is on more than one device.
  - A user can have 20 passkeys. Each sign-in with a passkey is an audit event, and a new
    device gets the email **Sign-in from a new device**.
  - A suspended user cannot sign in with a passkey. A reset of the password by an
    administrator or a reseller removes all passkeys of the user. A reset by email does not:
    the user can still sign in with a passkey.
  - An administrator or a reseller must still set up TOTP. A passkey does not replace this
    rule, because the panel asks for TOTP to confirm changes to administrators.
- An administrator can add OpenID Connect and SAML 2.0 identity providers in **Settings >
  Single sign-on**. Then the sign-in page shows the button **Sign in with NAME** for each
  provider that is on. Obey these rules:
  - The panel signs in only a user who connected the account of the provider in **Settings**.
    The panel does not make users: an administrator or a reseller makes the user first.
  - An administrator can let the panel connect the user with the same email at the first
    sign-in (**Connect users by email**). Then the panel accepts only an email that the
    provider verified (`email_verified` in OpenID Connect), and only for the email domains in
    the list of the provider. A SAML answer has no verified flag, so the panel trusts the
    emails of a SAML provider when this setting is on. Turn it on only when the provider
    controls all the email addresses of these domains. Otherwise, a person who gets an email
    address of the domain at the provider can sign in as the user with that email.
  - To connect or disconnect an account, the user gives the password and, when TOTP is on, a
    code. Only a sign-in session can do this, not an API token. One account of a provider
    connects to one user only.
  - A sign-in with a provider does not replace TOTP. When TOTP is on for the user, the panel
    asks for a code after the provider signs the user in. After 5 wrong codes, the sign-in
    stops. The wrong codes count for the sign-in lock of the account.
  - Single sign-on works only on the panel domain. It does not work on the panel domain of a
    reseller brand, on the address of a website or on an IP address. The provider sends the
    browser back only to the addresses that the administrator registered.
  - OpenID Connect: the panel uses the authorization code flow with PKCE (`S256`), a state
    and a nonce. It reads the discovery document of the issuer, and the issuer in the
    document must be the same. The panel verifies the signature of the ID token with the
    keys of the provider, and examines the issuer, the audience, the expiry time and the
    nonce. It accepts only `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`,
    `ES384` and `EdDSA`. It refuses `HS256` and `none`. Each address of the provider must
    use HTTPS.
  - SAML: the panel sends the request with the HTTP-Redirect binding, and the provider posts
    the answer. The answer or its assertion must have a valid signature of the certificate
    of the provider. The panel examines the issuer, the destination, the audience, the time
    and the ID of the request (`InResponseTo`). It refuses an answer that it did not ask for
    (a sign-in that starts at the provider), an encrypted assertion and a transient NameID.
    The panel does not sign its requests.
  - The panel addresses for the providers are the same for all providers. The state of the
    sign-in identifies the provider. The state works one time only, for 10 minutes. A cookie
    (`HttpOnly`, path `/api/v1/auth/sso`) binds the state to the browser that started the
    sign-in. Thus a person cannot sign a different browser in to an account of that person.
  - After the sign-in, the browser gets a ticket after `#` in the address. The browser does
    not send it to a server, so no access log has it. The ticket works one time only, for
    5 minutes.
  - The panel keeps the client secret encrypted with `APP_KEY`, and does not show it again.
  - Each sign-in, each connection and each change of a provider is an audit event. A new
    device gets the email **Sign-in from a new device**.
  - A suspended user cannot sign in with a provider. A password reset by an administrator or
    a reseller disconnects all accounts of the user. A removal of a provider disconnects all
    its accounts. **Sign out** signs the user out of the panel, not out of the provider.
- An administrator or a reseller with no TOTP can sign in, but the API then accepts only the
  account data, the password change, the TOTP setup and the sign-out (the answer has the code
  `totp_setup_required`). The web panel shows only the page **Set up an authenticator app**.
  This rule is for sign-in sessions. An API token continues to work: it has scopes and an
  expiry date, and it cannot make, reset or delete an administrator. For a development
  installation, `CF_REQUIRE_TOTP=false` in the environment of the panel API turns the rule
  off. Do not use this setting on a server with customers.
- To make, reset or delete an administrator, the signed-in administrator must give the
  password again and a TOTP code. A stolen session alone cannot take the other administrator
  accounts. After 5 incorrect attempts, the panel refuses these changes for 15 minutes.
- For 7 days after such a change, the web panel shows a notice to each other administrator:
  who made, reset or deleted which administrator, and when.
- An administrator can make, reset and delete all users. A reseller can do this only for the
  users of its own customers. An owner sees the list of its users only. Only a sign-in session
  of an administrator can make, reset or delete an administrator. An API token cannot. A user
  cannot reset or delete its own account in this way. A password reset signs the user out,
  deletes the API tokens and the passkeys of the user, disconnects the single sign-on
  accounts of the user, and removes a sign-in lock. Each of these changes is an audit event.
- On the panel server, root can run `cf-agent panel reset-password EMAIL` and
  `cf-agent panel unlock EMAIL`. This is the procedure when no administrator can sign in.
- In **Settings**, a user changes the password, turns TOTP on or off, adds or removes
  passkeys, and connects or disconnects single sign-on accounts. Each change needs the
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
  a failed job. For these users, the audit log shows "Administrator". A failed job shows only
  the name of the step when the error has a command, a path of the server, a socket or a
  network address.
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
- The "Add server" command downloads the signed installation script of the release of the
  panel from GitHub Releases. `CF_INSTALL_URL` gives a different address, for example a mirror.
- The CSV export of the audit log puts a single quotation mark before a cell that starts with
  `=`, `+`, `-`, `@`, a tab or a carriage return. A spreadsheet program then does not read the
  cell as a formula.

## The `cf` CLI

- The CLI refuses a panel address that does not start with `https://`, because the token
  would go over the network with no encryption. An `http://` address is permitted only for
  the same computer (`localhost` or a loopback address).
- `cf logout` deletes the session in the panel, then removes the token from the computer.
- `cf NAME` runs `cf-NAME` from the `PATH` only when `cf` has no command `NAME`, so a plugin
  cannot replace a command of `cf`. The program gets the token of your sign-in.

## Plugins

A plugin is a program with its own token ([plugins.md](plugins.md)). The boundary is the
public API. The panel process never runs the code of a plugin, and no plugin path leads to an
agent or to the root helper.

- **Token.** Each plugin has its own user and token. The token has only the scopes of the
  manifest. A manifest cannot ask for `tokens:*`, `users:write` or `plugins:*`, so a plugin
  cannot make tokens, reset a password or change its own access. Nobody can sign in as the
  user of a plugin.
- **Approval.** Only an administrator installs a plugin, adds a trusted key, makes new
  credentials, changes the address of a plugin or turns on the developer mode. Each of these
  needs the password and the TOTP code (audit finding F09). The review shows each scope in
  plain words, and an update shows the scopes that it adds.
- **Signature.** The panel installs only a manifest that a trusted ECDSA P-256 key signed. The
  signature covers the exact bytes of the manifest, with the scopes. Only the developer mode
  accepts no signature.
- **Off means off.** A plugin that is off, or removed, gets status 403 for its token and its
  page tokens, and gets no events.
- **Webhooks.** Each delivery has an HMAC-SHA256 signature with a secret of 40 random
  characters and a time. The panel resolves the host one time and connects to the checked
  address, so a DNS answer that changes has no effect. It refuses the loopback interface, the
  link-local range (with `169.254.77.1` and the cloud metadata service), and multicast
  addresses. It does not follow redirects. The secret is encrypted at rest.
- **Pages.** A page has the origin of the plugin. The panel refuses an address with the host
  name of the panel, so the frame always separates the page from the panel. The page gets a
  token of the user who opens it, for 15 minutes, only after it asks, and only to its own
  origin. The rights of the user still apply. A page token cannot make a token. The frame has
  no permission to change the top window. The Content Security Policy of the web panel
  permits frames from `https:` only.
- **Actions.** An action is a button of a plugin on a website, a server or a customer. Only a
  sign-in session runs an action, and only on a target that the user can see. The plugin gets
  a token of that user for 15 minutes, with the scopes of the plugin, so the rights of the user
  still apply. The token of an action cannot make a token or run an action. The web panel shows
  a link from the answer only when the link is on the address of the plugin.
- **Before hooks.** A before hook lets a plugin refuse a change. It cannot change the request.
  The panel waits 5 seconds or less for each plugin. The panel does not ask a plugin about its
  own changes, so a plugin cannot block itself in a loop.
- **Containers.** The panel runs a plugin only from the image of a signed manifest. The
  signature covers the digest, and the agent pulls the image by its digest. The container is a
  website with the runtime `custom`: its own Unix user, rootless Podman, gVisor and fixed
  limits. Its database instance is the plugin, so the firewall lets it connect to no database
  server. It gets no files of a customer, no socket of the agent and no root helper. The token
  and the webhook secret go only to the container, as environment variables in a file that
  only its Unix user can read (mode 0600, `EnvironmentFile=` in the unit). The job payload with them is encrypted at rest,
  and the panel removes the environment from the payload when the job is complete. The host
  name is on its own domain (`plugins.` and the panel domain by default), and the panel refuses
  a host name that a website, a domain or a plugin has.
- **CORS.** The API accepts browser calls from the origins of the plugins with pages. The API
  sends no cookies (`supports_credentials` is false), so a call needs a token.

What the boundary does not protect:

- A plugin can do all that its scopes permit, for all customers. A plugin with
  `websites:write` can delete each website. Approve only the scopes that the plugin needs.
- A page can do all that its scopes permit, as the user who opens it, for 15 minutes.
- The host of the plugin keeps the token. A person who gets access to that host gets the
  token. Turn off the plugin, or make new credentials, at once. For a plugin in the panel,
  root on its server can read the token.
- A container of a plugin can connect to the internet and to the public API, like a website.
- A plugin can show false data on its page. The panel shows the origin of the page above the
  frame.
- A plugin with a before hook can stop the changes of its hook, for all users. A plugin that is
  slow makes each of these changes up to 5 seconds slower. A hook with `"onError": "refuse"`
  stops the changes while the plugin is down. Turn off the plugin to remove its hooks.

## Packs

A pack adds a website runtime ([packs.md](packs.md)). A pack is data: names, usual commands
and images with digests. The panel and the agent never run a program that a manifest names.

- **Approval.** Only an administrator installs, updates or removes a pack, adds a trusted key
  or turns on the developer mode for packs. An install, an update and the developer mode need
  the password and the TOTP code. The audit log records each change. A token needs the scope
  `packs:write`, and a plugin cannot get the scopes `packs:read` and `packs:write`.
- **Signature.** The panel installs a pack only when a trusted key or the release key signed
  the exact bytes of the manifest. The panel keeps these bytes. It does not trim them, so the
  signature stays correct.
- **Checks in three places.** The panel, the worker of the agent and the root helper each
  check the manifest and the signature. The panel and the agent use the same rules and the
  same test cases (`schema/pack-vectors.json`). A key that the contract does not have is an
  error, so an agent never ignores a key of a later contract.
- **Images.** The signature of the manifest covers the digest of each image, and the agent
  pulls only that digest. The agent adds a rule to the Podman signature policy for each
  repository of a signed pack: Podman pulls an image of the repository only with a signature
  of the key of the pack. The agent never changes a rule of the installer, and adds no rule
  for a repository that a rule of the installer covers. Thus a pack cannot make the policy
  of the release registry weaker.
- **One repository, one pack.** An image repository can be in one pack only, and not in an
  own image of the panel. Two packs with different keys cannot share a policy rule.
- **Names.** The name of a pack cannot be the name of a built-in runtime, an image family or
  a database engine. A pack cannot replace a built-in runtime.
- **Isolation.** A website of a pack runtime has the same unit and the same isolation as each
  other website: gVisor, a read-only root file system, no capabilities and its own network
  namespace.
- **Removal.** The panel refuses to remove a pack that websites use, and refuses to remove a
  trusted key that signed an installed pack. The agent also refuses to remove a pack whose
  runtime a website of its server uses.

Known limits:

- The image of a pack runs the code of each website of its runtime, so it can do all that
  such a website can do.
- A person who gets the private key of a publisher can sign a new version of its packs. Do
  not install a new version until the publisher has a new key. To stop trusting the old key,
  remove its packs, then remove the key.
- The developer mode accepts packs with no signature. A pack with no signature gets no policy
  rule, so its images must pass the policy of the installer. Do not use the developer mode on
  a production panel.

## Backups

- Each server with the role `web` has its own restic repository and its own repository
  password. The panel sends a password only to its server, with each backup job. A person who
  has root access to one server cannot read the backups of the other servers.
- The agent does not keep the repository password or the credentials of the object storage
  in a file. It gets them with each job and gives them to restic in the environment, not in
  the command line.
- Each server with the role `web` can have its own key for the object storage. The storage
  provider limits the key to the repository of that server. The panel sends the key only to
  that server, with each backup job. A person who has root access to one server can then not
  read, delete or change the backups of the other servers. Refer to
  [installation.md](installation.md#one-storage-key-for-each-server).
- A server that has no key of its own gets the shared key of the backup policy. The shared
  key has access to the repositories of all servers. The **Backups** page shows the servers
  that use the shared key. Refer to [Known limits](#known-limits).
- A server must delete old data from its own repository. Thus a person who has root access
  to a server can delete the backups of that server. Versioning or an object lock in the
  bucket keeps deleted data for a set time.
- A backup keeps the SHA-256 sum of each database dump (refer to [Databases](#databases)).
- restic reads the files of a website as root while the website operates, and the owner of
  the website can replace a directory with a symbolic link at any time. Thus the agent runs
  each `restic backup` in a jail (`cf-agent jail`): a mount namespace whose root is an empty
  file system in memory. The jail has only the system programs (`/usr`), the CA
  certificates, the files that find a host name, four devices, the paths of the backup
  (read-only) and a temporary directory. It has no `/proc`, no `/root`, no `/etc/shadow`, no
  `/etc/cloudfactory` and no files of a different website. A link in the files of a website
  then points to a file of the same backup or to nothing. The agent opens each path of the
  jail without symbolic links, and the root helper refuses a `restic backup` that is not in
  a jail with exactly the paths of the backup.
- The backup of a database is in the repository of the server of its database server. For a
  website with a database on a different server, the agent of that server makes the dump
  and keeps it in its own repository. No dump and no repository password goes to a
  different server, and no database user for backups is open to the network.

## The mesh between servers

The mesh is the set of encrypted tunnels between the servers of one panel (refer to
[architecture.md](architecture.md#the-mesh-between-servers)). The traffic of a tunnel goes
through the internet, so the design assumes that a person can read and change each packet.

- **Encryption.** Each tunnel is a WireGuard tunnel. WireGuard encrypts each packet and
  accepts a packet only from the key of the other server.
- **Private keys.** The root helper of each server makes the key pair of the server. The
  private key stays in a file of root on the server. The panel, the worker of the agent and
  the other servers do not get it.
- **Pre-shared keys.** Each tunnel also has a pre-shared key, which the panel makes. A person
  who records the traffic now and breaks the key exchange later, for example with a quantum
  computer, also needs this key. The panel keeps it encrypted in its database.
- **The panel is the directory.** A server gets the public key of a different server only
  from the panel, through the agent connection with mutual TLS. A person who controls the
  panel can thus add a server to the mesh. That person can already run jobs on all servers.
- **The addresses of each key.** A server accepts from a tunnel only the packets that have
  the mesh address of the other server as their source, or the address of a network member
  on the other server (`AllowedIPs`). A network member opens no connection, so a packet from
  a member address is an answer. Thus the source address of a new connection identifies the
  server that has the key. An address is the address of one server or of one member of one
  server: the agent refuses a state that gives one address to two servers.
- **The tunnel port.** The firewall accepts the UDP port of the tunnels only from the tunnel
  addresses of the other servers of its tunnels. For all other addresses, the port is closed.
- **Traffic that comes out of a tunnel.** The firewall has a chain for it. The chain accepts
  an ICMP echo request, with a rate limit, and drops all other traffic. Thus a tunnel does not
  open SSH, the agent port, a database or a website of a server to the other servers. The
  one exception is a network member: refer to
  [The networks of the customers](#the-networks-of-the-customers).
- **Traffic into a tunnel.** Only root, and a website for the members of its networks, can
  open a connection to an address of the mesh range. Each other website user, the worker of
  the agent and the service users get a refusal. A server does not forward packets from or to
  the interface of the mesh.
- **Named networks.** A customer can separate its own websites and database servers with
  named networks. The firewall then refuses a connection from a website to a member address
  that is in none of its networks. This is a separation between the applications of one
  customer. It does not replace the separation between customers, which does not depend on
  the networks. The names of the engines stay open between a website and the database
  servers of its customer on the same server.
- **The move of a database server.** The data of a database server that moves goes through
  the tunnel between its old server and its new server, never through the panel. Only the
  root helpers send and receive it: the worker cannot connect to the mesh range and cannot
  read the data. The new server opens TCP port 7445 on its mesh address only for the mesh
  address of the old server, and only for the time of the move. It accepts a connection only
  with the token of the move: 32 random bytes from the panel, which the panel removes from
  the jobs when they end. The root helper refuses a peer that is not in its mesh state, a
  path outside the data directory, a hard link, a device file and a secret that is not of
  the engine. It writes each file with the owner of the database server, and it applies the
  disk limit of the database server.
- **Checks of the state.** The worker and the root helper examine each value of the state
  from the panel: the range is a private IPv4 network, each mesh address is in the range,
  each tunnel address is a public IP address, and each key has the correct form. The root
  helper refuses a state with an older version, so a recorded job cannot bring an old key
  back.
- **A server that an attacker controls.** That server has its own private key and the
  pre-shared keys of its tunnels. It can send packets with its own mesh address into its
  tunnels, and the other servers drop all of them except the echo requests. It cannot read
  the traffic of a tunnel between two other servers. Make a new key on the server after you
  repair it, or remove the server from the panel. The panel then removes its key from the
  other servers.

## The networks of the customers

A website connects to a database server of its networks by a name such as
`shop-db.internal` (refer to
[architecture.md](architecture.md#the-networks-of-the-customers)). The database server can be
on a different server. A website also connects to a different website of its networks, by a
name such as `shop-a1b2.internal` on port 80. These are the controls:

- **The firewall decides, not the name.** The hosts file of a website has only the names of
  the members of its networks. A website that writes an address in its own configuration gets
  the same answer from the firewall as with a name.
- **On the server of the website.** The firewall lets a connection to a member address pass
  only for the Unix user of a website that is in a network with the member, and only on the
  port of the member. pasta opens each connection of a container as the Unix user of the
  container. Thus a website of a different customer on the same server gets a refusal.
- **On the server of the member.** The firewall accepts a connection from a tunnel only when
  three values agree: the mesh address of the server that sent it, the member address, and
  the port of the member. The server that sent it must have a website in a network with the
  member. The firewall compares the address and the port from before the address change, so
  the port of the container on the server gives no access. All other traffic from a tunnel is
  dropped, as before.
- **What a server can do to a member of a different server.** A server whose root is in the
  hands of an attacker can connect to each member that one of its websites can connect to. It
  cannot connect to a member of a customer that has no website on it. Each connection also
  needs the password of a database user. Put the websites of customers that must not share
  this limit on different servers.
- **The same client address.** A database server sees `169.254.80.1` as the client for each
  website container, on its own server and on a different server. Thus a database user with
  the access `website` can connect from each website that the firewall lets through, and from
  no other address. A database server cannot find from the address which website connects. As
  before, the password of the database user is the control between the websites of one
  customer.
- **A website that is a member has no password.** A database server asks for the password
  of a database user. A website on port 80 does not: each website that is in a network with
  it can send it requests, and the website sees the same client address for each of them.
  Thus the members of one network trust each other. An application that must know which
  website sent a request needs its own control, for example a token in the request. A server
  whose root is in the hands of an attacker can send requests to each website member that
  one of its websites can connect to.
- **A website member and the edge proxy.** A connection to the network name does not go
  through the edge proxy. It has no TLS, no access log of the edge and no stop page. Between
  two servers, the WireGuard tunnel encrypts it. The port of the container on the private
  service address stays closed for each website: the firewall accepts a connection of a
  website only when the connection was to the member address and port 80 before the address
  change, and the Unix user of the website has that member in its networks.
- **A private website.** It has no route in the edge proxy, so no address on the internet
  can send it a request. The port of its container on the private service address is closed
  for each website, as for a public website. The only path is the member address on port 80.
- **A worker with a command of the customer.** The command is one line with no control
  characters (the rule `commandLine`, as for a cron job), and the agent checks each argument.
  It runs only in the container of the worker: in the gVisor sandbox, as the Unix user of
  the website, with no Linux capabilities, with a read-only image, and with the CPU, memory
  and process limits of the plan. The firewall rules for the connections that a website
  opens also apply. Thus the command can do what the PHP code of a website of the same
  customer can do, and no more. A worker counts as a website of the plan, so a customer
  cannot make more containers than the plan allows.
- **Checks of the root helper.** The worker of the agent makes the rules, and the root helper
  examines each one. A rule can open only an address of the mesh range that is not the
  address of a server, only for a UID of a website user, and only on the usual port of an
  engine or on port 80. The address change of a member can go only to a port of a database
  server on the database network, or, for port 80, to a port of a website container on the
  private service address (20000 to 29999). A name must end in `.internal`, and it can point only to an address of
  the mesh range. Thus a worker that an attacker controls cannot use these operations to
  open SSH, the agent port or a public address, or to give a website a false address for a
  public name. It can give a website of its server access to a network member, as it can
  give a website access to a database server of a different customer on its server. Each
  connection also needs the password of a database user.
- **The hosts file.** Root owns the file and its directory. The website container has the
  file as a read-only mount.
- **Database jobs for a website of a different server.** The agent of the server of a
  database server runs the jobs for its databases and its database users. For a website of
  a different server, the agent has no record of the website, so the job has the ID of the
  customer. The agent does the job only when the database server is a database server of
  that customer. For a website of its own server, the agent uses its record of the website:
  a job with a different customer is refused. As before, each name of a database and of a
  database user starts with the Unix user of the website, and the panel gives each website
  a different Unix user.
- **Tunnels that the panel makes.** The panel makes a tunnel between two servers only when a
  website on one server is in a network with a database server on the other. It removes the
  tunnel when this is no longer true.

## Firewall

The installation script sets an nftables table. The table blocks all incoming traffic except
SSH, SFTP (port 2200 by default), HTTP, HTTPS and the agent port. The agent changes only the
SFTP rule, which has the comment `# cloudfactory-sftp`, and the rules of the DNS server,
which have the comment `# cloudfactory-dns`. The DNS rules open port 53 (UDP and TCP) on a
server that has DNS zones.

The table also limits the outgoing connections:

- Only root and the edge proxy can connect to a website port or a panel port on the private
  service address. Only root and the user `cf-panel` can connect to the database of the panel.
  The worker of the agent (the user `cf-agent`) has no rule. It gets a connection only from
  the root helper (refer to [The root helper of the agent](#the-root-helper-of-the-agent)).
- Each customer has one address of the database network `169.254.80.0/20` on a server for
  its database servers. Only root and the website users can connect to this network. The
  set `database_access` has one element for each website and each database server of its
  customer: the UID of the website, the address of its customer and the port of the database
  server. The agent writes the elements, and nftables loads them from
  `/etc/cloudfactory/firewall/database-access.nft`.
- Each database server has its own Unix user, with a UID from 10000 to 19999. The table
  refuses each new connection of these users. A database server answers its clients, and
  opens no connection itself. Thus a person who controls a database server cannot connect
  from it to the internet, to a private network or to a service of the server.
- The chain `websites` gets each new connection of a user with a UID from 20000 to 59999,
  thus each connection that a website opens. It permits the database ports on the address of
  the customer of the website, DNS, and the HTTP and HTTPS ports of the server. It refuses all other addresses of the server, the
  private networks (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`), the
  link-local addresses (`169.254.0.0/16`, `fe80::/10`), the unique local addresses
  (`fc00::/7`), multicast, and port 25 of other servers.
- On a server with email, the agent adds the file `/etc/cloudfactory/firewall/mail.nft`. It
  opens the mail ports 25, 465, 587 and 993 and redirects them to the ports of the mail
  container. The chain `mail` gets each new connection of the user `cf-mail`. It permits DNS
  and port 25 of public addresses, and refuses all other connections.
- The chain `webmail` gets each new connection of the user `cf-webmail`. It permits DNS, the
  edge proxy of the server (port 443, which the firewall sends to 8443) and port 443 of public
  addresses. It refuses all other addresses of the server and the private networks.
- On a member of a mail cluster, the file also accepts the private network (UDP port 51877)
  from the other members, and the cluster ports on the interface `cf-mail`.
- The mesh has four sets: `mesh_port`, `mesh_range`, `mesh_endpoints4` and `mesh_endpoints6`.
  The table accepts the UDP port in `mesh_port` only from the addresses in the two sets of
  tunnel addresses. The chain `mesh_input` gets all traffic that comes out of a tunnel. It
  accepts an ICMP echo request and drops the rest. Only root can open a connection to an
  address in `mesh_range`. The chain `mesh_forward` drops each packet that a server would
  forward from or to the interface `cf-mesh`. The agent writes the elements of the sets, and
  nftables loads them from `/etc/cloudfactory/firewall/mesh.nft`. On a server from an earlier
  release, the agent adds the sets and the rules to the file of the installer with the first
  tunnel. Refer to [The mesh between servers](#the-mesh-between-servers).
- The networks have two sets and one map: `network_access` (the UID of a website, a member
  address and a port), `mesh_access` (the mesh address of a server, a member address and a
  port) and `mesh_members` (a member address and a port give the address and the port of the
  container). The NAT chains change the destination of a connection to a member of the
  server, from a tunnel and from a website of the server. They also set the source address
  `169.254.80.1` for such a connection. The agent writes the elements, and nftables loads
  them from `/etc/cloudfactory/firewall/network.nft`. Refer to
  [The networks of the customers](#the-networks-of-the-customers).
- The options `--allow-website-smtp` and `--allow-website-network` of the installation script
  open port 25 or a private network for the websites. Refer to
  [installation.md](installation.md#connections-from-the-websites).

## Known limits

These items stay open in release 0.1:

- **Zones from before the TXT proof was mandatory.** The panel asks for the TXT proof when a
  customer makes a zone. It does not examine the zones that customers made before this rule.
  An administrator examines these zones on the **DNS** page and removes a zone that its
  customer does not control.
- **One origin for Adminer.** All customers use the same Adminer domain, so the browser puts
  the Adminer pages of all customers in one origin. Each sign-in has its own random address,
  `/<tool>/<random key>/`, and the session cookie of Adminer has this path only. Adminer
  serves no other path, and keeps nothing in the storage of the browser. Thus a script on the
  Adminer page of one sign-in does not get the cookie of a different sign-in, and cannot find
  its address. The separation is the cookie path and a random address, not a separate origin.
  Use one browser profile for each customer when you manage the databases of different
  customers. A domain for each customer gives a separate origin.
- **A server that had the role `dns`.** When you turn off DNS on a server, the panel removes
  the zones from the server. The empty container `cf-dns` and the firewall rules for port 53
  stay until you remove them.

- **Image tags.** The signature policy accepts a signed image for each tag of its repository
  (`matchRepository`). A person who can write to the registry can thus give an old signed
  image a new tag. That person cannot add an image with no signature. The correction is to
  pull each image by its digest from the signed release manifest.
- **Service images between releases.** The edge, SFTP, database and panel images get security
  fixes only with a new release. The daily rebuild of the images is off (refer to
  [releasing.md](releasing.md#daily-image-rebuild)), and the rebuild tags are only for the
  runtime images of the websites.
- **Credentials of the object storage.** The panel does not make the keys of the servers,
  and it does not examine their permissions. An administrator makes each key at the storage
  provider and sets it on the **Backups** page. Until each server has its own key, a person
  who has root access to a server with the shared key can delete the backup data of all
  servers, but cannot read it. A restore from the repository of an earlier version sends
  the shared key to the server of the website. Do these steps at the storage provider:
  - Use a bucket that has only the Cloudfactory backups.
  - Give each server its own key, with access to its own path only.
  - Turn on versioning or an object lock for the bucket, with a retention time that is longer
    than the longest backup retention. Deleted data then stays until that time ends.
  - Give no key the permission to change the bucket settings (versioning, lifecycle, policy)
    or to delete object versions.
- **Custom registry.** An administrator can set a custom registry address. The panel server
  then sends requests to that address.
- **Join token.** The person who has a join token gives the agent address that the panel
  calls. Give a join token only to a person whom you trust with a server.
- **Shared database containers of release 0.1.9.** A server from release 0.1.9 or an earlier
  release has one shared MariaDB container and one shared PostgreSQL container, which run as
  the Unix user `cf-db`. This release does not operate them, and the update does not remove
  them. Until an administrator removes them, the containers run with their data, and the
  firewall of the earlier release lets each website of the server connect to them. Remove
  them after the update, and run the installer again (refer to
  [installation.md](installation.md#a-server-from-release-019)).
- **Firewall rule of the database servers on a server from an earlier release.** The
  installer writes the rule that refuses the connections of the Unix users of the database
  servers. A server that you installed with an earlier release gets the rule when you run
  the installer again.
- **Rights of the worker of the agent.** The worker is not root, but it can act as each
  website user, it manages each database server, and it has the full admin API of the edge
  proxy. Refer to [The root helper of the agent](#the-root-helper-of-the-agent). A possible
  correction for the edge proxy is a root helper that accepts only the route changes of a
  fixed form.
- **Servers with no disk limits.** A server with no user quotas and no loop devices (for
  example a container of an LXC host with a file system that is not ext4 or XFS) cannot apply
  the disk limit of a website or of a database server. On such a server, a customer that
  fills the disk of the server stops the websites and the database servers of all customers.
  The panel shows `disk-quotas` and `database-disk-limits` as parts of the server that are
  off.
- **Websites and database servers from before the disk images.** On a file system with no
  user quotas, a website or a database server that has a directory keeps it, and has no disk
  limit. Only a new website or a new database server gets a disk image. The panel shows
  `disk-quotas` or `database-disk-limits` as a part of the server that is off, with the
  number of these websites or database servers. To give them a limit, turn on the user
  quotas.
- **Home directory of a website with a disk image.** The disk image limits the files of the
  website. It does not limit the home directory of the website user, which has the files of
  Podman, the log file of the website (10 MB at most) and, for a custom image, the copy of
  the image. A customer with a custom image can thus use disk space of the server with the
  size of that image. A user quota counts these files.
- **Sum of the disk limits.** A user quota and a disk image use only the disk space that
  the website or the database server uses, and the sum of the disk limits can be more than
  the disk of the server. Monitor the free disk space of the server.
- **Base images.** The Containerfiles name the base images by tag, not by digest.
