# Screenshots

These screenshots show the web panel on the panel API in demonstration mode (`CF_DEMO=true`),
with the sample data of `api/database/seeders/DemoSeeder.php`. The panel has one console for
each role: an administrator console, a reseller console and an owner console. To see the same
pages on your computer, refer to [development.md](../development.md).

The [main README](../../README.md) uses the screenshots in [readme/](readme), from the same
data. The screenshots in [live/](live/README.md) show earlier releases on a real server. Their
menu is the menu before the consoles.

To sign in to the demonstration mode, use `admin@example.com`, `reseller@example.com` or
`owner@example.com` with the password `password`.

## Administrator console

An administrator signs in to the Overview page. The menu has the platform, the customers, the hosting pages and the settings.

### admin-01-overview

The first page of an administrator: the health of the servers, what needs attention, and the failed jobs that no later job resolved.

![admin-01-overview](admin-01-overview.png)

### admin-02-finish-setup

The setup checklist. Each open item shows what can go wrong until it is complete, and a link to the page that completes it. The data comes from `GET /api/v1/setup`.

![admin-02-finish-setup](admin-02-finish-setup.png)

### admin-03-servers

The servers.

![admin-03-servers](admin-03-servers.png)

### admin-04-server

The page of a server, with its operating system updates.

![admin-04-server](admin-04-server.png)

### admin-05-images-and-updates

The runtime images. An image whose last check failed shows "Unknown: the last check failed", not "Up to date".

![admin-05-images-and-updates](admin-05-images-and-updates.png)

### admin-06-customers

The customers. The actions of a row are in one menu.

![admin-06-customers](admin-06-customers.png)

### admin-07-change-plan

A change of the plan of a customer. The dialog shows the new limits and the effect before the administrator confirms.

![admin-07-change-plan](admin-07-change-plan.png)

### admin-08-users

The users who can sign in.

![admin-08-users](admin-08-users.png)

### admin-09-plans

The plans.

![admin-09-plans](admin-09-plans.png)

### admin-10-websites

All websites of the panel.

![admin-10-websites](admin-10-websites.png)

### admin-11-website

The overview of a website. A failed job that a later job resolved shows as "Failed, then done". Only an administrator gets the error of the server, in "Details for administrators".

![admin-11-website](admin-11-website.png)

### admin-12-website-databases

The databases of a website, with a Valkey database. A MariaDB or PostgreSQL database has **Open in Adminer**. The actions of a database user are in one menu.

![admin-12-website-databases](admin-12-website-databases.png)

### admin-13-website-settings

The limits and the isolation of a website.

![admin-13-website-settings](admin-13-website-settings.png)

### admin-14-jobs

All jobs. Each failed job has a reference.

![admin-14-jobs](admin-14-jobs.png)

### admin-15-backups

The backup policy and the snapshots.

![admin-15-backups](admin-15-backups.png)

### admin-16-panel-certificate

The certificate of the panel domain.

![admin-16-panel-certificate](admin-16-panel-certificate.png)

### admin-17-databases

The database servers of each customer, with their plans, their use and their names in the network of the customer. Below them, the page shows the databases and the database users of all websites.

![admin-17-databases](admin-17-databases.png)

### admin-18-new-database-server

A new database server for a customer. The plan gives its limits and the number of its databases. Only an administrator can set limits that are different from the plan.

![admin-18-new-database-server](admin-18-new-database-server.png)

### admin-19-edit-plan

The values of a plan for websites and for database servers.

![admin-19-edit-plan](admin-19-edit-plan.png)

### admin-20-network

The encrypted tunnels between the servers. Each tunnel shows its state, the time of its last handshake and its round-trip time. The panel makes a tunnel when the network of a customer needs it. An administrator makes, repairs and removes tunnels, and makes a new key on a server.

![admin-20-network](admin-20-network.png)

### admin-21-dns

The DNS zones of all customers.

![admin-21-dns](admin-21-dns.png)

### admin-22-dns-zone-failed

A DNS zone that a name server did not load.

![admin-22-dns-zone-failed](admin-22-dns-zone-failed.png)

### admin-23-server-dns

The DNS role and the name server name of a server.

![admin-23-server-dns](admin-23-server-dns.png)

## Reseller console

A reseller signs in to the Customers page. The menu has no page of the platform.

### reseller-01-customers

The customers of the reseller.

![reseller-01-customers](reseller-01-customers.png)

### reseller-02-plans

The plans that the reseller can give. Only an administrator changes a plan.

![reseller-02-plans](reseller-02-plans.png)

### reseller-03-websites

The websites of the reseller and of its customers.

![reseller-03-websites](reseller-03-websites.png)

### reseller-04-account

The account of the reseller.

![reseller-04-account](reseller-04-account.png)

### reseller-05-dns

The DNS zones of the customers of a reseller.

![reseller-05-dns](reseller-05-dns.png)

## Owner console

A website owner signs in to the My websites page. The menu has no page of an administrator or a reseller, and the pages have no administrator text.

### owner-01-websites

The websites of the owner.

![owner-01-websites](owner-01-websites.png)

### owner-02-website

The overview of a website. A failed job shows a short text and a reference, not the command of the server.

![owner-02-website](owner-02-website.png)

### owner-03-website-domains

The domains of a website. A certificate that the panel CA signed says that browsers do not trust it.

![owner-03-website-domains](owner-03-website-domains.png)

### owner-04-databases

The databases of all websites of the owner.

![owner-04-databases](owner-04-databases.png)

### owner-05-backups

The snapshots.

![owner-05-backups](owner-05-backups.png)

### owner-06-activity

The jobs of the owner.

![owner-06-activity](owner-06-activity.png)

### owner-07-account

The account: the plan, how much of it is in use, the provider and the users. The data comes from `GET /api/v1/account`.

![owner-07-account](owner-07-account.png)

### owner-08-settings

The password, TOTP, the API tokens and the audit log.

![owner-08-settings](owner-08-settings.png)

### owner-09-new-website

The form for a new website.

![owner-09-new-website](owner-09-new-website.png)

### owner-10-apps

The start points for common applications.

![owner-10-apps](owner-10-apps.png)

### owner-11-website-files

The file manager of a website. The server does each operation as the Unix user of the website.

![owner-11-website-files](owner-11-website-files.png)

### owner-12-website-deploys

The deploys of a website from a Git repository: the repository, the migration command, the deploy key, the signed webhook, the build secrets and the releases. A release that failed its health check after the switch shows "Went back".

![owner-12-website-deploys](owner-12-website-deploys.png)

### owner-13-website-cron

The cron jobs of a website, with the result of the last run of each one.

![owner-13-website-cron](owner-13-website-cron.png)

### owner-14-website-logs

The last log lines of a website.

![owner-14-website-logs](owner-14-website-logs.png)

### owner-15-dns

The DNS zones of an owner.

![owner-15-dns](owner-15-dns.png)

### owner-16-dns-zone

A DNS zone with its name servers, DNSSEC and records.

![owner-16-dns-zone](owner-16-dns-zone.png)

### owner-17-dns-add-record

The dialogue to add a DNS record.

![owner-17-dns-add-record](owner-17-dns-add-record.png)

### owner-18-website-databases

The databases of a website. **Open in Adminer** opens a MariaDB or PostgreSQL database in Adminer in a new window.

![owner-18-website-databases](owner-18-website-databases.png)

### owner-19-adminer

Adminer after **Open in Adminer**. The panel signed in with a temporary database user for this database only. The user typed no password.

![owner-19-adminer](owner-19-adminer.png)

## Phone width (390 px)

Under 640 px, each row of a table shows as a card, and the tab bar scrolls with a shadow at the edge that has more tabs.

### phone-01-owner-websites

The websites.

![phone-01-owner-websites](phone-01-owner-websites.png)

### phone-02-owner-website

The overview of a website.

![phone-02-owner-website](phone-02-owner-website.png)

### phone-03-owner-website-databases

The databases of a website as cards.

![phone-03-owner-website-databases](phone-03-owner-website-databases.png)

### phone-04-owner-account

The account.

![phone-04-owner-account](phone-04-owner-account.png)

### phone-05-owner-activity

The jobs.

![phone-05-owner-activity](phone-05-owner-activity.png)

### phone-06-admin-overview

The overview of the administrator.

![phone-06-admin-overview](phone-06-admin-overview.png)

### phone-07-admin-finish-setup

The setup checklist.

![phone-07-admin-finish-setup](phone-07-admin-finish-setup.png)

### phone-08-admin-customers

The customers as cards.

![phone-08-admin-customers](phone-08-admin-customers.png)

### phone-09-owner-databases

The database servers and the databases of an owner as cards.

![phone-09-owner-databases](phone-09-owner-databases.png)

### phone-10-owner-dns-zone

A DNS zone at phone width.

![phone-10-owner-dns-zone](phone-10-owner-dns-zone.png)

## White label

The sample reseller Northwind Agency has the brand "Northwind Hosting", with its own logo,
colour, panel domain, name servers and mail host. The files are in [white-label/](white-label).

| File | Page |
| ---- | ---- |
| [01-reseller-sign-in.png](white-label/01-reseller-sign-in.png) | The sign-in page on the panel domain of the reseller. |
| [02-customer-websites.png](white-label/02-customer-websites.png) | A customer of the reseller sees only the brand of the reseller. |
| [03-customer-dns-zone.png](white-label/03-customer-dns-zone.png) | The DNS zone of a customer has the name servers of the reseller. |
| [04-reseller-brand.png](white-label/04-reseller-brand.png) | The Brand page of the reseller. |
| [05-reseller-panel-domain-proof.png](white-label/05-reseller-panel-domain-proof.png) | A new panel domain needs a TXT record that proves control. |
| [06-administrator-default-brand.png](white-label/06-administrator-default-brand.png) | The administrator sets the default brand in Settings. |
| [07-reseller-brand-phone.png](white-label/07-reseller-brand-phone.png) | The Brand page on a phone. |

## Certificates

The certificates of the customers: uploads, and certificates with DNS validation from
Let's Encrypt. In the demonstration mode, a demo CA signs the certificates. Refer to
[certificates.md](../certificates.md). The files are in [certificates/](certificates).

| File | Page |
| ---- | ---- |
| [01-owner-certificates.png](certificates/01-owner-certificates.png) | The certificates of a customer, with the domains that use each one. |
| [02-owner-certificate.png](certificates/02-owner-certificate.png) | A wildcard certificate with DNS validation, its details and the domains that use it. |
| [03-owner-failed-order.png](certificates/03-owner-failed-order.png) | An order that failed, because the zone does not use the name servers of the panel. |
| [04-owner-get-wildcard-certificate.png](certificates/04-owner-get-wildcard-certificate.png) | Get a wildcard certificate for a zone of the customer. |
| [05-owner-renewal.png](certificates/05-owner-renewal.png) | A renewal: the stages of the order and the TXT records. |
| [06-owner-upload-certificate.png](certificates/06-owner-upload-certificate.png) | Upload a certificate chain and its private key. |
| [07-owner-website-domains.png](certificates/07-owner-website-domains.png) | The domains of a website, with a link to the certificate of each domain. |
| [08-admin-certificates.png](certificates/08-admin-certificates.png) | An administrator sees the certificates of all customers. |
| [09-owner-certificates-phone.png](certificates/09-owner-certificates-phone.png) | The certificates on a phone. |
