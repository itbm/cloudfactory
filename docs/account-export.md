# Account export

An account export is one archive of the account of a customer. The archive has:

- The description of the account (`account.json`): the websites, the domains, the runtimes,
  the environment variables, the cron jobs, the deploy settings, the databases and their
  users, the database servers, the networks, the DNS zones and the email addresses.
- The files of each website.
- A dump of each MariaDB and PostgreSQL database.
- A zone file of each DNS zone.

Use an export to keep a copy of an account, to move an account to a different Cloudfactory
panel, or to move an account to a different provider. The import of an account reads the same
description format (`cf-account/1`). To move an account to a different Cloudfactory panel,
import the archive on that panel. Refer to
[imports.md](imports.md#an-account-export-of-cloudfactory).

## Who can export an account

| Role | Accounts |
| ---- | -------- |
| Website owner | Its own account. |
| Reseller | Its own account, and the account of each of its customers. The account of a reseller does not include the accounts of its customers. |
| Administrator | The account of each customer. |

An API token must have the scope `exports:write` to start an export and the scope
`exports:read` to see and download exports. A plugin cannot have these scopes, because the
archive has the environment variables of the websites.

## Export an account

### In the web panel

1. Open **My account**. An administrator or a reseller opens **Customers**, opens the menu of
   the customer, and selects **Export account…**.
2. Select **Export account**.
3. Clear **The files of the websites** if you do not need the files.
4. Clear **A dump of each MariaDB and PostgreSQL database** if you do not need the data of the
   databases.
5. Select **Start export**.

The list shows the progress of the export. When the state is **Ready**, select **Download**.

### With the CLI

1. Start the export. The command waits until the archive is ready:

   ```sh
   cf exports create
   ```

   An administrator or a reseller adds `--customer CUSTOMER_ID`. Add `--no-files` or
   `--no-databases` to make a smaller archive.

2. Download the archive:

   ```sh
   cf exports download EXPORT_ID --output acme-export.tar
   ```

   If the download stops, run the same command again. The download continues at the end of
   the file `acme-export.tar.partial`.

Other commands: `cf exports list`, `cf exports get EXPORT_ID` and `cf exports delete EXPORT_ID`.

### With the API

1. Send `POST /api/v1/exports` with `{"customerId": "...", "files": true, "databases": true}`.
   An owner does not send `customerId`.
2. Read `GET /api/v1/exports/{id}` until `state` is `ready` or `failed`.
3. Send `POST /api/v1/exports/{id}/download-link`. The answer has a `url` that is valid for 10
   minutes.
4. Download the `url` with no token. To continue a download that stopped, send the header
   `Range: bytes=N-`, where N is the number of bytes that you have.

Refer to `openapi/openapi.yaml`, tag `Exports`.

## Rules

- An account has one export in progress at a time.
- The panel keeps an archive for 7 days. Then it removes the archive.
- The panel keeps the 3 newest exports of each account. A new export removes the oldest export
  that is complete or failed.
- The websites stay online during the export. A dump of MariaDB uses one transaction, so the
  dump is consistent. The files are not a snapshot: a file that changes during the export can
  be in the archive in its old form or in its new form.
- The panel refuses an export when a database server of the account moves to a different
  server.
- The panel refuses an export, and stops an export in progress, when its disk has less free
  space than the reserve. Refer to [Settings](#settings).
- An export that is not complete after one day fails.
- The audit log records each export, each download link and each removal.

## What the archive does not have

- The email messages and the passwords of the mailboxes. The description has the mailboxes,
  their sizes, the aliases and the public key of the encryption of each mailbox.
- The data of Valkey databases. A Valkey database is a cache.
- Passwords, password hashes and private keys. The panel does not keep the passwords of the
  database users or of the mailboxes. A different panel makes new keys for the deploys and for
  DNSSEC.
- The values of the build secrets of a deploy. The description has their names only.
- A file in the directory of a website that is larger than the disk limit of the website, and
  special files (devices, sockets and FIFOs). A backup does not have them either.
- The backups. The backups stay in the object storage of the servers.
- The accounts of the customers of a reseller. Export each customer separately.

The `warnings` of an export name the items of the account that are not in the archive.

## The archive (`cf-export/1`)

The archive is one tar file (POSIX.1-2001, "pax"). It has no compression, because the large
members are compressed already. The members are in this order:

| Path | Content |
| ---- | ------- |
| `manifest.json` | The format of the archive and the SHA-256 hash of each other member. |
| `account.json` | The description of the account (`cf-account/1`). |
| `README.txt` | A short text for a person who opens the archive. |
| `dns/<zone>.zone` | The zone file (RFC 1035) of each DNS zone. |
| `websites/<website>/databases/mariadb-<database>.sql.gz` | The dump of a MariaDB database: SQL text with gzip. The dump has no `DEFINER` clauses, so a user that is not the superuser can load it. |
| `websites/<website>/databases/postgresql-<database>.pgdump` | The dump of a PostgreSQL database, in the custom format of `pg_dump`, with no owners and no privileges. Use `pg_restore`. |
| `websites/<website>/files.tar.gz` | The files of the website: a tar file with gzip. The paths start at the directory of the website. The archive keeps the permissions, the modification times and the symbolic links. It has no owners. |

### manifest.json

```json
{
  "format": "cf-export/1",
  "description": "account.json",
  "source": { "panel": "cloudfactory", "version": "0.1.0", "account": "01JB...", "exportedAt": "2026-11-22T21:03:00+00:00" },
  "createdAt": "2026-11-22T21:07:12+00:00",
  "files": [
    { "path": "account.json", "bytes": 18234, "sha256": "..." },
    { "path": "websites/shop.example.com/files.tar.gz", "bytes": 734003200, "sha256": "..." }
  ]
}
```

To check an archive, compare the SHA-256 hash of each member with `files`. The agent of each
server calculates the hash of its parts while it sends them.

## The description (`cf-account/1`)

`account.json` is a JSON object. A program that imports an account must ignore the fields that
it does not know. A new field does not change the format name. A change that removes or
changes a field gets a new format name, for example `cf-account/2`.

| Field | Content |
| ----- | ------- |
| `format` | `cf-account/1`. |
| `source` | The panel (`cloudfactory`), its version, the ID of the account and the time of the export. |
| `customer` | The name, the contact email address and the role (`owner` or `reseller`) of the customer. |
| `plan` | The name of the plan, for information. A different panel has its own plans. |
| `users` | The name, the email address and the role of each user. No passwords. |
| `websites` | Each website. Refer to [Websites](#websites). |
| `databaseServers` | The name, the engine, the plan and the limits of each database server. |
| `networks` | The name of each private network, `default`, and the names of its websites and database servers. |
| `dnsZones` | The name of each zone, `dnssec`, the records and `content` (the zone file in the archive). A record has `name`, `type`, `ttl`, `value`, `priority` (MX and SRV only) and `managed`. `managed` is `ns` or `website` for a record that the panel made, and `null` for a record of the user. |
| `mail.domains` | The name of each mail domain, its mailboxes (`localPart`, `quotaBytes`, `usedBytes`, `publicKey`) and its aliases (`localPart`, `kind`, `destinations`). |
| `warnings` | The items that are not in the archive: `item` and `message`. |

### Websites

| Field | Content |
| ----- | ------- |
| `name` | The name of the website, for example `shop.example.com`. |
| `kind` | `public`, `private` or `worker`. |
| `application` | The application, for example `WordPress`, or an empty text. |
| `diskBytes` | The disk space of the files at the last report of the server. |
| `runtime` | `family`, `name`, `image` (the reference and the tag), `custom`, `phpVersion`, `version`, `extensions`, `phpIni`, `workerMode`, `documentRoot`, `command` and `installCommand`. |
| `limits` | The limits of the website: `vcpus`, `memoryBytes`, `diskBytes`, `diskIoBytesPerSecond`, `processes` and `bandwidthBytes`. |
| `domains` | The name and the kind (`primary`, `alias` or `redirect`) of each domain. |
| `environment` | The environment variables: an object of names and values. |
| `cronJobs` | The `schedule`, the `command` and `enabled` of each cron job. |
| `deploy` | The deploy from Git, or `null`: `repository`, `branch`, `webDirectory`, `trigger`, `buildCommands`, `publicKey` and `secrets` (the names only). A different panel makes a new deploy key. Add its public key to the Git host. |
| `sftpKeys` | The public SSH keys for SFTP. |
| `databases` | The `engine`, the `name`, the `server` (the name of the database server) and `content` of each database. `content` is `null` for a Valkey database and when the export has no databases. |
| `databaseUsers` | The `engine`, the `name`, the `access` and the `grants` (`database` and `privileges`) of each database user. No passwords. |
| `content.files` | The files in the archive (`path`, `bytes` and `exclude`), or `null`. |

Each `content` object has the `path` of a member of the archive and its size in `bytes`.

## Restore an account by hand

Use these steps when the destination has no import of accounts.

1. Unpack the archive:

   ```sh
   tar -xf acme-export.tar
   ```

2. Check the hashes in `manifest.json`.
3. Unpack the files of a website into its new directory:

   ```sh
   tar -xzf websites/shop.example.com/files.tar.gz -C /path/to/the/website
   ```

4. Load a MariaDB dump:

   ```sh
   gunzip -c websites/shop.example.com/databases/mariadb-shop.sql.gz | mariadb shop
   ```

5. Load a PostgreSQL dump:

   ```sh
   pg_restore --no-owner --dbname=app websites/shop.example.com/databases/postgresql-app.pgdump
   ```

6. Import each zone file from `dns/` into the DNS service.
7. Set the environment variables, the cron jobs and the domains from `account.json`.

## How an export works

1. The panel writes the description of the account. It keeps the description encrypted in its
   database.
2. The panel sends one `ExportWebsite` job to the server of each website. A website with a
   database in a database server on a different server also gets a job on that server, for
   the dumps of those databases only (`databasesOnly`).
3. The agent makes each part and sends it to the panel at the same time:
   - The files: `cf-agent site-archive` runs as the Unix user of the website, as the file
     manager does. It writes a tar file with gzip to its standard output.
   - A dump: the dump command of a backup runs in the database container. The agent removes
     the `DEFINER` clauses from a MariaDB dump and compresses it with gzip.
4. The agent sends each part in pieces of at most 4 MiB:
   `PUT /api/agent/v1/export-parts/{id}?offset=N`. The panel keeps a piece only when the
   offset is the size of the part that it has. Thus a piece that the agent sends again after
   a connection error does not go into the part two times. The server keeps no copy of a part
   and needs no free disk space.
5. The result of each job has the size and the SHA-256 hash of each part. The panel compares
   the size with the part that it has.
6. When all parts are complete, the queue writes `manifest.json`, `account.json`,
   `README.txt` and the zone files. The export is then ready.
7. The panel sends the archive from these files and the parts. It does not copy the parts
   into one file, so the disk keeps one copy of each part.

An export, a backup and a restore of one website do not run at the same time on its server.
Each export uses a backup slot of the server, so an export does not run with more than the
permitted number of backups.

## Settings

The panel reads these environment variables:

| Variable | Default | Content |
| -------- | ------- | ------- |
| `CF_EXPORT_PATH` | `/data/exports` on a server | The directory of the parts and the archives. |
| `CF_EXPORT_DAYS` | `7` | The number of days that the panel keeps an archive. |
| `CF_EXPORT_KEEP` | `3` | The number of exports that the panel keeps for each account. |
| `CF_EXPORT_RESERVE_BYTES` | 2 GiB | The free disk space that an export must leave. The reserve is this value or 5% of the disk, the larger of the two. |
| `CF_EXPORT_LINK_MINUTES` | `10` | The time that a download link is valid. |

The archives use the disk of the panel server. Make sure that the disk of the panel has
sufficient free space for the largest account and the reserve.
