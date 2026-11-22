# Certificates: uploads and wildcard certificates

Each domain of a website gets a TLS certificate from the edge proxy by default. The edge proxy
gets it from Let's Encrypt with the HTTP validation, or from the panel CA for a local name.
This guide tells customers, resellers and administrators how to use two other types of
certificate:

- **An uploaded certificate.** You get the certificate from a certificate authority (CA), and
  you upload it with its private key. Use it for an extended validation (EV) certificate, a
  certificate from your company CA, or a certificate that you also use on other servers.
- **A certificate with DNS validation.** The panel gets the certificate from Let's Encrypt. It
  proves control of the domain with a TXT record in the DNS zone of the domain. This is the
  only method that gives a wildcard certificate, for example `*.example.com`. It also works for
  a domain that does not point to the server yet.

The panel keeps these certificates for each customer. A website of the customer uses a
certificate when the certificate is valid for a domain of the website.

## Which certificate a domain uses

Each verified domain of a website uses the best usable certificate of the customer of the
website:

1. A certificate with the exact name of the domain.
2. A wildcard certificate for the domain. `*.example.com` is valid for `shop.example.com`, but
   not for `example.com` or `a.shop.example.com`.
3. If two certificates are equally good, the certificate that expires last.

A certificate is usable when it has a chain, a private key and an expiry date in the future.
A domain with no usable certificate gets a certificate from the edge proxy, as before. When you
remove a certificate, its domains get a different certificate in the same way.

The **Domains** tab of a website shows the certificate of each domain, with a link to the
certificate. The page of a certificate shows the domains that use it, under **Used by**.

The panel does not send a certificate to a server in these cases:

- The certificate is also valid for a host name of the panel, of a server, of a plugin, of
  Adminer or of a mail server, or for the panel domain of a reseller. The panel refuses the
  upload of such a certificate.
- The certificate is also valid for a domain of a different website on the same server, and
  that domain does not use a certificate from the panel. Examples are a domain of a different
  customer, or a domain that is not verified. The edge proxy could otherwise send the
  certificate for that domain.

In the second case, the domains of the certificate get a certificate from the edge proxy. The
certificate page shows no domain under **Used by**.

## Upload a certificate

You need these items in PEM format:

- The certificate chain: the certificate of the domain first, then each intermediate
  certificate. Do not add the root certificate. If you give the certificates in a different
  order, the panel puts them in the correct order.
- The private key of the certificate, with no password. To remove a password, use
  `openssl pkey -in key.pem -out plain.pem`.

Do these steps:

1. Go to **Certificates**.
2. Select **Upload certificate**.
3. An administrator or a reseller with more than one customer selects the customer.
4. Paste the certificate chain, or select the file.
5. Paste the private key, or select the file.
6. Select **Upload certificate**.

The panel checks the upload. It refuses these items:

- A chain or a key that is larger than 64 KiB.
- A key that is not RSA or ECDSA, or an RSA key with less than 2048 bits.
- A key that is not the key of a certificate in the chain.
- A certificate with no DNS names (subject alternative names).
- A certificate that expired.
- A certificate that is valid for a name of the panel.

The panel accepts a certificate from a CA that browsers do not trust, for example the CA of
your company. The page of the certificate then shows "Browsers do not trust this issuer".

The panel cannot renew an uploaded certificate. Before it expires, get a new certificate from
your CA. Then replace the certificate:

1. Open the certificate on the **Certificates** page.
2. Select **Replace**.
3. Paste the new chain and the new private key.
4. Select **Replace certificate**.

The ID of the certificate does not change. The servers get the new certificate at once.

## Get a wildcard certificate with DNS validation

Before you start, make sure of these items:

- The customer has a DNS zone for the domain on the **DNS** page.
- The registrar of the domain has the name servers of the panel. The **DNS** page shows
  "Uses these name servers" for the zone.
- The zone has no CNAME record at `_acme-challenge` or `_acme-challenge.<sub-domain>`.

Do these steps:

1. Go to **Certificates**.
2. Select **Get a wildcard certificate**.
3. An administrator or a reseller with more than one customer selects the customer.
4. Type the domain, for example `example.com`. The list shows the zones of the customer that
   use the name servers of the panel.
5. To get a certificate for the domain only, clear **Also for each name directly below the
   domain**.
6. Select **Get certificate**.

The certificate is then valid for `example.com` and `*.example.com`. The page of the
certificate shows each stage of the order:

| Stage                                   | What occurs                                                                                   |
| --------------------------------------- | --------------------------------------------------------------------------------------------- |
| Make the order and add the TXT records  | The panel makes an order at Let's Encrypt. It adds a TXT record at `_acme-challenge` for each name. |
| Wait for the DNS servers                | The panel waits until all its DNS servers give the new records.                               |
| Let's Encrypt checks the records        | Let's Encrypt reads the TXT records through the public DNS.                                    |
| Get the certificate                     | The panel sends a new key request (CSR) and gets the certificate.                             |

An order usually takes less than two minutes. Each stage can take 15 minutes at most. After the
order, the panel removes the TXT records. The **DNS** page shows the records of an order with
the label "Panel: certificate". You cannot change these records.

If the order fails, the page shows the error. Correct the cause, then select **Try again**.
The panel does not try a first order again by itself.

With DNS validation, the panel makes a new private key for each order. The private key never
leaves the panel and the servers.

### Renewals

The panel renews a certificate with DNS validation when two thirds of its time is gone. For a
certificate of 90 days, this is 30 days before it expires. To renew a certificate now, open it
and select **Renew now**.

The servers use the old certificate until the new certificate is ready. If a renewal fails,
the panel tries again after 1 hour, then after 2, 4, 8 and 16 hours, and then each 24 hours.

### Email when a certificate expires soon

The event `certificate.expiring` (refer to [email-notifications.md](email-notifications.md))
also sends an email for a domain that uses a certificate from the panel. The value
`{{advice}}` tells the user what to do for the type of the certificate: replace an uploaded
certificate, or check the DNS zone of a certificate with DNS validation.

## Use the command line tool

```sh
# The certificates of the customers, and the domains that use them.
cf certificates list

# Upload a certificate and its key. "-" reads the standard input.
cf certificates upload --cert chain.pem --key key.pem

# Get a certificate for example.com and *.example.com. The command waits for the order.
cf certificates request example.com --wildcard

# Show the chain of a certificate in PEM format.
cf certificates get example.com --pem

# Replace an uploaded certificate, renew a certificate, or remove a certificate.
cf certificates replace example.com --cert new-chain.pem --key new-key.pem
cf certificates renew example.com
cf certificates delete example.com
```

An administrator or a reseller adds `--customer ID` to `upload` and `request`.

The API is in [openapi/openapi.yaml](../openapi/openapi.yaml), under the tag
`Certificates`. A token needs the scope `websites`.

## Limits

| Limit                                              | Value  | Setting                         |
| -------------------------------------------------- | ------ | ------------------------------- |
| Certificates for each customer                     | 20     | `CF_CERTIFICATES_PER_CUSTOMER`  |
| Uploads, replacements, orders and renewals         | 20 each hour for each customer | none |
| Size of a chain, and size of a private key         | 64 KiB | none                            |
| DNS names in one certificate                       | 100    | none                            |
| Certificates on one server                         | 2000   | none (`schema/input-rules.json`) |
| Hosts on one server that use a certificate from the panel | 5000 | none (`schema/input-rules.json`) |

A customer that is suspended cannot add, replace or renew a certificate. Its certificates stay
on the servers.

## Settings for administrators

| Setting                         | Default                                              | Effect                                                    |
| ------------------------------- | ---------------------------------------------------- | --------------------------------------------------------- |
| `CF_ACME_DIRECTORY`             | `https://acme-v02.api.letsencrypt.org/directory`     | The ACME CA of the DNS validation. For tests, use the Let's Encrypt staging directory. |
| `CF_ACME_EMAIL`                 | none                                                 | The contact email of the ACME account. The CA can send expiry notices to it. |
| `CF_CERTIFICATES_PER_CUSTOMER`  | `20`                                                 | The number of certificates for each customer.             |

The panel makes one ACME account for each directory. It keeps the account key in the
settings, encrypted with the application key. If the CA does not know the account any more,
the panel makes a new account.

The scheduler runs `cloudfactory:certificates` each minute. The command starts the renewals
that are due, and it continues the orders that are open. A queue job also continues each order
while it is open, so an order does not wait for the scheduler.

## How the servers get the certificates

The panel sends the full list of the certificates of a server to its agent, with the job
`SetCertificates`. It sends the list when a certificate changes, and when a website or a
domain of the server changes. The agent gives each certificate to the edge proxy (Caddy) with
its hosts. The edge proxy serves the certificate for these hosts, and does not get a
certificate for these hosts itself.

Each report of the agent has the certificates that the edge proxy has. When the list is not
the list of the panel, the panel sends the list again. For example, the panel sends the list
again after a new installation of the edge proxy. If the job fails, the panel waits 1 minute,
then 2, 4 and so on, up to 60 minutes.

The check of a domain that uses a certificate from the panel asks the edge proxy for the
certificate, as a browser does. The check compares the fingerprint with the certificate from
the panel. The DNS of the domain is not important for this check.

## Add a DNS provider for the DNS validation

The DNS hosting of the panel holds the TXT records by default. A DNS service outside the panel,
for example Cloudflare or Route 53, can also hold them. To add such a service, do these steps:

1. Make a class that implements `App\Services\Certificates\ChallengeDns`.
2. Give the class the container tag `certificates.dns` (`DnsProviders::TAG`) in a service
   provider.

The panel asks each provider in turn for each record name. The first provider that holds the
name for the customer gets the records. The DNS hosting of the panel is always first. The
interface has these functions:

| Function    | What it does                                                                                     |
| ----------- | ------------------------------------------------------------------------------------------------ |
| `key()`     | Gives a short key that is unique among the providers, for example `cloudflare`.                  |
| `holds()`   | Tells if the provider holds a record name, for example `_acme-challenge.example.com`, for the customer. |
| `problem()` | Gives a text for the user when the provider cannot use the name now, or null.                     |
| `present()` | Adds the TXT records. It gives a state that the panel keeps as JSON.                             |
| `ready()`   | Tells if the name servers of the zone give the records now.                                       |
| `cleanUp()` | Removes the records. A record that is not there is not an error.                                  |

A wildcard name `*.example.com` uses the record of `example.com`. One record name can have two
values, for example for `example.com` and `*.example.com`.

## Security

- The panel keeps each private key encrypted with the application key. The API never gives a
  private key, also not to an administrator.
- The agent gets the private keys only in the `SetCertificates` job, through the connection
  with mutual TLS. The job record in the panel has no keys.
- A user can see and change only the certificates of the customers that the user controls.
- A certificate goes only to the servers of the websites of its customer. The rules in
  [Which certificate a domain uses](#which-certificate-a-domain-uses) stop a certificate of one
  customer for the domain of a different customer.
- The audit log shows each upload, replacement, order, renewal and removal.
