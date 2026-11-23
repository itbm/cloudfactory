# Import an account from a different panel

This guide is for an administrator. It tells you how to move a hosting account from cPanel,
DirectAdmin, Plesk, HestiaCP or a different Cloudfactory panel to Cloudfactory. The import reads
the backup of the account and makes its websites, databases, cron jobs, mailboxes and DNS zones
for a customer of the panel.

The old panel continues to operate during the import. The websites and the mail move to
Cloudfactory only when you change the DNS of their domains.

## The backups that the panel reads

| Panel        | Backup                                                                                                             |
| ------------ | ------------------------------------------------------------------------------------------------------------------ |
| cPanel       | The account backup of `pkgacct` or of **Backup** in cPanel, for example `cpmove-USER.tar.gz` or `backup-MM.DD.YYYY_HH-MM-SS_USER.tar.gz` |
| DirectAdmin  | The user backup of DirectAdmin, for example `user.admin.USER.tar.gz` or `user.admin.USER.tar.zst`                  |
| Plesk        | A backup of a subscription or a customer from **Backup Manager** or `pleskbackup`, downloaded as one file, for example `backup_example.com_2311221200.tar` |
| HestiaCP     | A user backup from **Backups** in HestiaCP or from `v-backup-user`, for example `USER.2026-11-22_05-00-00.tar`. The agent also reads a VestaCP backup, which has the same layout. |
| Cloudfactory | An account export (`cf-export/1`) of a different Cloudfactory panel, for example `corner-bakery-export-20261122-2103.tar`. Refer to [An account export of Cloudfactory](#an-account-export-of-cloudfactory). |

The file can be a tar archive with no compression, or with gzip, bzip2 or zstd compression. The
agent finds the type from the content of the file, not from its name. It also reads an archive
in the archive, for example `homedir.tar` of cPanel, `backup_user-data_2311221200.tgz` of
Plesk, `domain_data.tar.zst` of HestiaCP or `files.tar.gz` of a Cloudfactory account export.

For Plesk, make the backup with no password protection, or with password protection off for
the download. The backup of Plesk for Windows (a ZIP file) is not supported.

## What the import copies

| Part           | What the import does                                                                                                                                                                                                                              |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Websites       | It makes one PHP website for each web domain: the main domain, each addon domain and each subdomain (in Plesk, each site of a subscription). The website gets the image with the same PHP version. If the panel has no such image, the website gets the image with the next newer version. |
| Domains        | It adds the aliases (parked domains, domain pointers, Plesk domain aliases and HestiaCP web aliases) and the redirects to their website. A HestiaCP web domain with the document root of a different web domain becomes an alias of that website. |
| Files          | It copies the document root of each website. A document root in a different document root, for example a subdomain in `public_html`, goes to its own website only.                                                                                |
| Databases      | It makes a MariaDB database for each MySQL or MariaDB database of a website, and loads the dump. The name of the database gets the Unix user of the website in place of the account name: `bakery_wp` becomes `<unix-user>_wp`.                     |
| Database users | It makes one user for each database, with the name of the database and a new password. It does not copy the other users of the old panel.                                                                                                         |
| WordPress      | It writes the new database name, user, password and host into `wp-config.php`. You can turn this off before you start the import.                                                                                                                |
| Cron jobs      | It adds each cron job of the account to its website, **turned off**. Read each command, change the paths of the old panel, then turn the cron job on.                                                                                            |
| Mail           | It makes each mail domain, mailbox, alias and forwarder, and copies the messages of each folder. A mailbox keeps its password when the old panel has a hash that the mail server can use: bcrypt, SHA-512-crypt, SHA-256-crypt or MD5-crypt. Plesk usually encrypts the passwords with a key of its server, and HestiaCP can use Argon2id: these mailboxes get a new password. |
| DNS zones      | It makes each DNS zone and adds its records. If the customer has the zone already, the import adds only the records that the zone does not have.                                                                                                  |

The import does not copy these parts. The plan shows a warning for each one that the backup has:

- the files of the home directory that are not in a document root;
- PostgreSQL databases;
- TLS certificates (the panel gets a new certificate for each domain when its DNS points to
  the new server);
- FTP accounts (use SFTP with an SSH key);
- autoresponders, catch-all addresses and forwarders to a program;
- the `private_html` directory of DirectAdmin and the `public_shtml` directory of HestiaCP (the
  import copies `public_html` only);
- the redirect of a HestiaCP web domain;
- the mail of a Plesk domain alias, and the scheduled tasks of Plesk that are not in the
  crontab of the system user;
- the SOA and NS records of a zone, and the records of the cPanel services, for example
  `cpanel`, `whm` and `webmail`.

## An account export of Cloudfactory

The import reads the description of the account (`account.json`) and the parts of an account
export ([account-export.md](account-export.md)). To make the export, open **My account** on the
old panel, select **Export account**, and download the archive. Keep the files and the dumps of
the databases in the export.

The import of an export is different from the import of a backup of a different panel:

- It makes the public websites with a PHP image or a static image. It gives a warning for each
  private website, worker, and website with a different runtime, for example Node.js, Python
  or a pack. Make these websites again, and copy their files from `files.tar.gz` in the export.
- A website gets the Apache image with the same PHP version. A static website gets the newest
  Apache image. To use a FrankenPHP image, select it for the website in the plan.
- It copies all files of each website from `files.tar.gz`, with the document root and the
  excluded paths of the old website.
- A database gets the Unix user of the new website in place of the Unix user of the old
  website: `shop_x7k2_wp` becomes `<unix-user>_wp`.
- The export has no messages and no passwords of the mailboxes. The import makes each mailbox
  with a new password and no messages.
- It adds only the DNS records of the user. The panel makes its own name servers and the
  records of its websites.

The import does not copy these parts of an export. The plan shows a warning for each one:

- the environment variables, the deploy from Git, the SFTP keys, the PHP extensions and the PHP
  settings of each website;
- the PostgreSQL databases (make the database, then load its dump from the export with
  `pg_restore`) and the Valkey databases;
- the database servers and the private networks of the account;
- the users of the account.

## Before you start

1. Make the customer on the panel, if it is not there. The plan of the customer must have
   space for the websites, the databases, the mailboxes and the DNS zones.
2. Make sure that the web server for the websites has the role `web`.
3. Make the backup on the old panel.

## Import an account in the web panel

1. Open **Imports**.
2. Select **Import account**.
3. Select the customer and the web server.
4. Select **Upload a file** and the backup file. Or select **Download from a URL** and enter an
   HTTPS URL of the backup, for example a link to object storage.
5. Select **Read the archive**. The agent reads the backup. This can take some minutes for a
   large backup.
6. Read the plan. Clear the parts that you do not want to import.
7. If necessary, select a different image for a website.
8. Select **Start import**.

The page shows each part of the import with its state. A part that fails does not stop the
other parts. The parts that need it are skipped: for example, the files of a website that the
import could not make.

## Import an account with the CLI

1. Make the import and send the backup:

   ```sh
   cf imports create --customer "Corner Bakery" --server web-2 --file cpmove-bakery.tar.gz
   ```

   The command sends the file in parts and waits for the plan. To use a URL, give
   `--url https://files.example/cpmove-bakery.tar.gz` in place of `--file`.

2. Read the plan:

   ```sh
   cf imports get IMPORT
   ```

3. Start the import of all parts that the plan can import:

   ```sh
   cf imports start IMPORT --all
   ```

   To import only some parts, give `--website NAME`, `--mail DOMAIN` and `--zone ZONE` in place
   of `--all`. To select a different image, give `--website NAME=IMAGE_ID`. To keep
   `wp-config.php` as it is, give `--keep-wp-config`.

If the upload stops, for example after a connection error, continue it:

```sh
cf imports upload IMPORT --file cpmove-bakery.tar.gz
```

## After the import

1. Read the notes of the parts. For example, a database can have a new name.
2. Make a new password for each mailbox that has the note "The panel does not know the old
   password". Give the password to the owner of the mailbox.
3. Make the other database users of the old panel again, if the websites need them.
4. Turn on the cron jobs that you need.
5. Test each website with its address on the new server.
6. Change the DNS of each domain to the new server. If the panel hosts the DNS zone, change the
   name servers of the domain at the registrar.
7. Remove the import (**Remove import**, or `cf imports delete IMPORT`). The websites, the
   databases and the mail stay. Only the archive and the record of the import go.

## The archive

- The panel accepts an archive of up to 50 GiB. To change the limit, set
  `CF_IMPORT_MAX_BYTES` (in bytes) in the environment of the panel.
- An upload goes to the panel in parts of 4 MiB. The panel needs free disk space for the full
  archive and a reserve of 2 GiB. To change the reserve, set `CF_IMPORT_RESERVE_BYTES`. For a
  large archive, give a URL: then the archive does not go through the panel.
- The web server downloads the archive and keeps it until the import ends. Each other server
  that the import needs, for example the server of a database server or of a mail server,
  downloads it too. Each server checks the SHA-256 hash of the first download.
- When the import ends, the panel and each server delete the archive.
- The panel removes an upload that did not complete after 1 day, and an import that is ready
  and did not start after 7 days (`cloudfactory:prune-imports`, each hour).

## Security

The backup comes from a different server, and possibly from the customer. The agent does not
trust it:

- The worker of the agent (the user `cf-agent`) downloads and reads the archive. Root never
  opens it.
- The agent never extracts the archive. It reads it as a stream, and refuses each name that goes
  out of the archive.
- The agent reads the XML files of a Plesk backup with no external entities, and with limits on
  the size and the depth.
- A Plesk backup can have a mailbox password as text. The agent makes a SHA-512-crypt hash of
  it, and only the hash goes to the panel.
- The Unix user of the website extracts the files of the website from a new tar stream. The
  stream has only directories, regular files and symbolic links.
- The database client in the container of the database server loads a dump as a temporary user
  that can use only the one database. The agent removes the `CREATE DATABASE` and `USE`
  statements of the dump.
- The mail server gets each message through its JMAP API, in the account of the mailbox. The
  agent never has the password of a mailbox.
- A download from a URL connects only to public addresses, also after a redirect, and uses no
  proxy. It stops when the server has less than 2 GiB of free disk space, and when no data
  comes for 2 minutes.
- The import makes each record with the API of the panel, as the administrator who started the
  import. Thus each record has the same checks, plan limits, plugin hooks and audit entries as a
  change in the web panel. A token that starts an import needs the scopes for each part:
  `databases:write`, `mail:write` and `dns:write`.

## Not tested on a real server

The tests use a fake JMAP server for the messages, and fakes for the system commands. These
items are not tested on a real server yet:

- the download and the extraction with Podman and gVisor;
- the load of a dump into a database server;
- the copy of messages into the JMAP account of a mailbox, as the administrator of Stalwart;
- a mailbox password hash of cPanel or DirectAdmin in Stalwart;
- a backup from a real Plesk server. Plesk does not publish the schema of its backup files. The
  agent reads the XML elements that the public sources and other migration tools show
  (`domain`, `phosting`, `sites`, `mailuser`, `database`, `dnsrec` and the `cid` content
  references). Test an import of a Plesk backup before you move many accounts.
- a backup from a real HestiaCP or VestaCP server. The agent reads the layout that the backup
  script of HestiaCP (`v-backup-user`) writes.
- an account export from a real server. The tests use an archive with the layout of
  [account-export.md](account-export.md). The agent also read an export of the demonstration
  panel correctly.
