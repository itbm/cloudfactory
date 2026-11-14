# Screenshots

These screenshots show the web panel of release 0.1 with the mock API. For the panel on the
real API, refer to [live/README.md](live/README.md).

The mock API shows areas that the API of release 0.1 does not have, for example DNS, email and
files. Refer to [status.md](../status.md).

The [main README](../../README.md) uses the screenshots in [readme/](readme). They show the
production build of the web panel on the panel API, with the sample data of `php artisan db:seed`
and sample reports for resource use, jobs and backups.

## 01-websites

![01-websites](01-websites.png)

## 02-new-website

![02-new-website](02-new-website.png)

## 03-website-overview

![03-website-overview](03-website-overview.png)

## 04-website-runtime

![04-website-runtime](04-website-runtime.png)

## 05-website-domains

![05-website-domains](05-website-domains.png)

## 06-website-databases

![06-website-databases](06-website-databases.png)

## 07-website-backups

![07-website-backups](07-website-backups.png)

## 08-website-access

![08-website-access](08-website-access.png)

## 09-website-logs

![09-website-logs](09-website-logs.png)

## 10-website-settings

![10-website-settings](10-website-settings.png)

## 11-updates

![11-updates](11-updates.png)

## 12-servers

![12-servers](12-servers.png)

## 13-server-detail

![13-server-detail](13-server-detail.png)

## 14-jobs

![14-jobs](14-jobs.png)

## 15-databases

![15-databases](15-databases.png)

## 16-backups

![16-backups](16-backups.png)

## 17-files

![17-files](17-files.png)

## 18-dns

![18-dns](18-dns.png)

## 19-email

![19-email](19-email.png)

## 20-apps

![20-apps](20-apps.png)

## 21-customers

![21-customers](21-customers.png)

## 22-settings

![22-settings](22-settings.png)

## 23-login

![23-login](23-login.png)

## 24-websites-phone

![24-websites-phone](24-websites-phone.png)

## 25-database-user-permissions

The permissions of one database user on each database of the website (CF-01).

![25-database-user-permissions](25-database-user-permissions.png)

## 26-new-database-user

A new database user: the name starts with the Unix user of the website, and each database has its own permission.

![26-new-database-user](26-new-database-user.png)

## 27-database-user-password

The panel shows the password of a new database user one time only.

![27-database-user-password](27-database-user-password.png)

## 28-new-database

A new database, with the prefix of the website.

![28-new-database](28-new-database.png)

## 29-domain-not-verified

A domain that is not verified. The edge proxy serves it only after the TXT record is correct (CF-03).

![29-domain-not-verified](29-domain-not-verified.png)

## 30-new-token

A new API token needs the password and the TOTP code of the user (CF-02).

![30-new-token](30-new-token.png)

## 31-panel-certificate

The certificate of the panel domain from the panel CA, and the root certificate to download.

![31-panel-certificate](31-panel-certificate.png)

## 32-panel-certificate-localhost

A panel on `panel.localhost`. Let's Encrypt is not available for this domain. This screenshot
shows live mode with the answers of the API from a test.

![32-panel-certificate-localhost](32-panel-certificate-localhost.png)

## 33-change-to-lets-encrypt

The change to Let's Encrypt. If the certificate does not come, the panel keeps the current
certificate.

![33-change-to-lets-encrypt](33-change-to-lets-encrypt.png)

## 34-change-back-to-panel-ca

The change back to the panel CA. Add the root certificate to the browser first (HSTS).

![34-change-back-to-panel-ca](34-change-back-to-panel-ca.png)

## 35-new-valkey-database

A new Valkey database. It always gets a user with the same name, so the form has no option
for the user.

![35-new-valkey-database](35-new-valkey-database.png)

## 36-valkey-database

The databases of a website with a Valkey database: the host, the database number and the
number of keys. The user of a Valkey database can only get a new password.

![36-valkey-database](36-valkey-database.png)
