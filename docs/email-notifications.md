# Email notifications

The panel sends an email when an event occurs, for example when a job fails. This guide tells
administrators and resellers how to set up the emails. It also tells each user how to select
the emails that they get.

## Events

| Event                         | Key                    | Who can get it                     |
| ----------------------------- | ---------------------- | ---------------------------------- |
| Failed jobs                   | `job.failed`           | Administrators, resellers, owners  |
| Failed backups                | `backup.failed`        | Administrators, resellers, owners  |
| Certificates that expire soon | `certificate.expiring` | Administrators, resellers, owners  |
| Account suspended             | `account.suspended`    | Resellers, owners (always on)      |
| Suspension lifted             | `account.restored`     | Resellers, owners                  |
| Sign-in from a new device     | `signin.new_device`    | All users (always on)              |
| Usage alerts                  | `usage.alert`          | Administrators, resellers, owners  |
| Servers that are offline      | `server.offline`       | Administrators                     |

Who gets the email of an event:

- **Failed job or backup:** the users of the customer of the website, the database server or
  the DNS zone, the users of its reseller, and the administrators.
- **Certificate:** the users of the customer of the website, and the administrators. The panel
  renews a certificate 30 days before it expires. Each day at 08:00, the panel finds each
  certificate that expires in 14 days or less, and each domain that has no certificate because
  the order failed. You get one email for each certificate and expiry date.
- **Suspension:** the users of the customer. When a reseller is suspended, the users of its
  customers also get the email.
- **Sign-in from a new device:** the user. The web panel keeps a random device ID in the
  browser. The first sign-in of a user gives no email. A client with no device ID, for example
  the `cf` command, is known by its user agent.
- **Usage alert:** the users of the customer and of its reseller. The usage alerts feature
  makes the alert. A mailbox that is 90% full also gets an alert. The email names the mailbox.
- **Server offline:** the administrators, when a server does not report for 5 minutes.

## Set up the SMTP server (administrators)

The panel sends all emails through an SMTP server. Until you set one, the panel sends no
email. The setup checklist shows this as an open item.

1. Go to **Settings**, then **Email delivery**.
2. Type the host, the port, the user name and the password of your SMTP server.
3. Type the from address. The SMTP server must be permitted to send for its domain (SPF and
   DKIM). If not, the emails go to the spam folder.
4. Select **Save**.
5. Select **Send test email**. The panel sends the email at once and shows the answer of the
   server.

The panel connects only to the SMTP ports 25, 465, 587 and 2525. The panel never shows the
password again.

| Encryption | Use it for                                                         |
| ---------- | ------------------------------------------------------------------ |
| STARTTLS   | Port 587 or 2525. The server must give STARTTLS, or the email fails. |
| TLS        | Port 465. TLS from the start of the connection.                    |
| None       | Port 25 to a relay in your private network only.                   |

With the command line tool:

```sh
CF_SMTP_PASSWORD='…' cf mail-server set --host smtp.example.com --user panel@example.com \
  --from panel@example.com --from-name "Example Hosting"
cf mail-server test --to ops@example.com
```

## SMTP server and templates of a reseller

A reseller can set its own SMTP server in **Settings**, **Email delivery**. Then the
customers of the reseller get emails from the address of the reseller. The emails have the
brand name of the reseller and links to the panel domain of the reseller.

If the reseller has no SMTP server, the emails to its customers go through the server of the
panel. They still have the name of the reseller as the from name.

The emails to the users of the reseller itself always come from the panel, because the
reseller is a customer of the panel.

For a reseller, the SMTP host must have a public address. The panel does not connect to a
private address for a reseller. A reseller can send a test email only to its own address.

## Templates

Go to **Settings**, **Email delivery**, **Email templates**, and select **Edit** for an event.
You can change the subject and the text. The preview shows the email with example values.

In a template, `{{name}}` is a value. Select a value under the text to add it. Each event has
the values `{{name}}` (the name of the user), `{{brand}}` and `{{panelUrl}}`, and its own
values, for example `{{website}}` and `{{error}}`. A template with a value that the event
does not have is an error.

The panel uses the first template that it finds:

1. The template of the reseller, for a customer of a reseller.
2. The template of the panel.
3. The default template.

Select **Reset** to go back to the next template in this list.

The panel makes the HTML part of the email from the text. It escapes the text, and each web
address becomes a link.

## Select the emails that you get (all users)

Go to **Settings**, then **Notifications**. Turn each event on or off. You cannot turn off the
security emails: account suspended, and sign-in from a new device.

With the command line tool:

```sh
cf notifications preferences
cf notifications off job.failed usage.alert
```

## Sent emails

**Settings**, **Notifications**, **Sent emails** shows each email that the panel sent, or
tried to send. Select an email to read its text.

| State        | Meaning                                                              |
| ------------ | -------------------------------------------------------------------- |
| In the queue | The queue worker sends it soon.                                      |
| Sent         | The SMTP server accepted the email.                                  |
| Failed       | The SMTP server did not accept the email 3 times (after 1 and 5 minutes). |
| Not sent     | No SMTP server was set, or it was off.                               |

An administrator sees all emails. A reseller sees the emails to its users and to the users of
its customers. A customer sees the emails to the users of its account. Only the user and the
administrators see a sign-in email, because it has the IP address of the sign-in.

After you correct the SMTP server, select **Send again** for a failed email.

## API

The routes are in [openapi/openapi.yaml](../openapi/openapi.yaml), with the tag
"Notifications". An API token needs the scope `notifications:read` or `notifications:write`.

A different feature can send an email with `App\Services\Notifications\Notifier`. For
example, the usage alerts feature calls:

```php
app(Notifier::class)->usageAlert($customer, [
    'resource' => 'Disk', 'website' => 'shop.example.com',
    'used' => '9.1 GB', 'limit' => '10 GB', 'percent' => 91, 'level' => 'warning',
], "usage:{$alert->id}:warning");
```

The last argument is a key. The panel sends one email for each key and user.
