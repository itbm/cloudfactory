# Screenshots in live mode

These screenshots show the web panel of release 0.1 on the real panel API, with the sample
data of `php artisan db:seed`. No agent was connected, so the jobs stay at step 1.

## 01-sign-in

Sign in with the email and the password.

![01-sign-in](01-sign-in.png)

## 02-sign-in-error

A wrong password. The API gives the error.

![02-sign-in-error](02-sign-in-error.png)

## 03-websites

Websites of the sample data, as an administrator.

![03-websites](03-websites.png)

## 04-servers

Servers.

![04-servers](04-servers.png)

## 05-add-server

Add server. The command has a new join token, the panel URL, the name and the roles.

![05-add-server](05-add-server.png)

## 06-apps

Apps. Live mode shows only the apps that have an image on the panel.

![06-apps](06-apps.png)

## 07-new-website

New website. An administrator selects the customer.

![07-new-website](07-new-website.png)

## 08-website-created

The new website, with the job from the API.

![08-website-created](08-website-created.png)

## 09-settings

Settings. Live mode shows the API tokens and the audit log.

![09-settings](09-settings.png)

## 10-customers

Customers and plans.

![10-customers](10-customers.png)

## 11-owner-websites

Websites, as a website owner.

![11-owner-websites](11-owner-websites.png)

## 12-owner-new-website

New website, as a website owner. The owner cannot select the server.

![12-owner-new-website](12-owner-new-website.png)

## 13-owner-website-created

The new website of the owner.

![13-owner-website-created](13-owner-website-created.png)

## 14-updates

Updates, after "Check now". The local panel has no release public key, so the check gives an error.

![14-updates](14-updates.png)

## 15-sign-in-one-error

R-08: a wrong password and a wrong code give the same error, so the answer does not show that
a password is correct.

![15-sign-in-one-error](15-sign-in-one-error.png)

## 16-sign-in-settings

R-01: Settings, Sign-in and security. The user changes the password and sets up an
authenticator app.

![16-sign-in-settings](16-sign-in-settings.png)

## 17-set-up-totp

R-01: the panel makes a new TOTP key. TOTP is on only after the user gives the password and a
code from the app.

![17-set-up-totp](17-set-up-totp.png)

## 18-password-changed

R-01: TOTP is on, so a password change also needs a code. The panel signs out the other
sessions.

![18-password-changed](18-password-changed.png)

## 19-add-server-github

R-14: the "Add server" command downloads the signed installation script from GitHub Releases.

![19-add-server-github](19-add-server-github.png)

## 20-reseller-websites

Audit batch 3, issue 2: a reseller sees the websites of its customers.

![20-reseller-websites](20-reseller-websites.png)

## 21-reseller-new-website

A reseller selects the customer of a new website. The website gets the plan of the customer.

![21-reseller-new-website](21-reseller-new-website.png)

## 22-reseller-customers

The own row of a reseller has no plan menu and no Suspend button.

![22-reseller-customers](22-reseller-customers.png)

## 23-sftp

SFTP access of a website: on or off, SSH keys with their fingerprints, and a new password that shows once.

![23-sftp](23-sftp.png)

## 24-document-root

The document root of a website, for example "." for WordPress.

![24-document-root](24-document-root.png)

## 25-backup-policy

The backup policy with a restic repository and the credentials of the object storage.

![25-backup-policy](25-backup-policy.png)

## 26-backup-policy-error

The form shows an error and does not show "Saved" (audit batch 2, finding 10).

![26-backup-policy-error](26-backup-policy-error.png)

## 27-container-tools

A held container tool has an update. "Update container tools" installs it (FR-1113).

![27-container-tools](27-container-tools.png)
