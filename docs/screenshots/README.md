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

The databases of a website, with a Valkey database. The actions of a database user are in one menu.

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
