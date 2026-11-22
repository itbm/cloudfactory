# Route 53 DNS plugin

This plugin sends the DNS zones of the panel to public hosted zones in Amazon Route 53. The
panel keeps the records. After each change, the panel sends the full zone to the plugin, and
the plugin makes the hosted zone the same. The plugin keeps no data.

The plugin uses the contract `cf-dns-provider/1`. Refer to
[docs/plugins.md](../../docs/plugins.md#dns-providers).

| File | Function |
| ---- | -------- |
| `plugin.json` | The manifest. It has the provider `route53` and its credentials. |
| `server.mjs` | The server. It checks the signature of each request and refuses a request that it received before. |
| `route53.mjs` | The operations `verify`, `records`, `sync` and `remove` with the Route 53 API. It signs each request with AWS Signature Version 4. |
| `test/` | The tests. Run `node --test test/*.test.mjs`. |

The plugin needs Node.js 22. It has no dependencies.

## What the plugin changes in Route 53

- The plugin changes only the record types of the panel: A, AAAA, CNAME, MX, TXT, SRV, CAA
  and NS. Record sets of other types do not change.
- The SOA record and the NS records of the zone itself do not change. Route 53 keeps them.
- Alias records and record sets with a routing policy (for example weighted or latency) do not
  change. The panel cannot have a record with the same name and type. When a zone moves to
  Route 53, the panel shows these record sets as records that it did not copy.
- Route 53 keeps one TTL for each record set (a name and a type). When the records of a set
  have different TTLs in the panel, the set gets the shortest TTL.
- The plugin sends the changes in change batches of up to 1000 records. Each batch is one
  transaction in Route 53.
- When the zone is not in Route 53, the plugin adds a public hosted zone. Private hosted zones
  do not change. Two public hosted zones with the same name are an error.
- When DNSSEC is on in the panel, the plugin adds the key-signing key `cloudfactory` with the
  KMS key of the DNS account, and turns on DNSSEC signing. It gives the DS record to the panel.
  When DNSSEC is off in the panel, the plugin turns off DNSSEC signing and removes the key.
- To remove a zone, the plugin removes all the record sets of the hosted zone, also alias
  records, and then the hosted zone.

## Make an IAM user

1. In the IAM console, make a user for the panel, with no access to the console.
2. Attach this policy to the user:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Action": [
           "route53:ListHostedZonesByName",
           "route53:GetHostedZone",
           "route53:CreateHostedZone",
           "route53:DeleteHostedZone",
           "route53:ListResourceRecordSets",
           "route53:ChangeResourceRecordSets",
           "route53:GetDNSSEC",
           "route53:EnableHostedZoneDNSSEC",
           "route53:DisableHostedZoneDNSSEC",
           "route53:CreateKeySigningKey",
           "route53:ActivateKeySigningKey",
           "route53:DeactivateKeySigningKey",
           "route53:DeleteKeySigningKey"
         ],
         "Resource": "*"
       }
     ]
   }
   ```

3. Make an access key for the user. Select the use case **Application running outside AWS**.

Without DNSSEC, you can remove the actions with `DNSSEC` and `KeySigningKey` from the policy.

## Prepare a KMS key for DNSSEC

Route 53 signs the zone with a key in AWS KMS. Each DNS account with DNSSEC needs a key.

1. In the KMS console, select the region **US East (N. Virginia)** (`us-east-1`).
2. Make a customer managed key with the key type **Asymmetric**, the key usage **Sign and
   verify**, and the key spec **ECC_NIST_P256**.
3. Add these two statements to the list `"Statement"` of the key policy, so that Route 53 can
   use the key:

   ```json
   {
     "Sid": "Allow Route 53 DNSSEC",
     "Effect": "Allow",
     "Principal": { "Service": "dnssec-route53.amazonaws.com" },
     "Action": ["kms:DescribeKey", "kms:GetPublicKey", "kms:Sign"],
     "Resource": "*"
   },
   {
     "Sid": "Allow Route 53 DNSSEC to make a grant",
     "Effect": "Allow",
     "Principal": { "Service": "dnssec-route53.amazonaws.com" },
     "Action": "kms:CreateGrant",
     "Resource": "*",
     "Condition": { "Bool": { "kms:GrantIsForAWSResource": true } }
   }
   ```

4. Let the IAM user of the panel use the key: give it `kms:DescribeKey`, `kms:GetPublicKey`,
   `kms:Sign` and `kms:CreateGrant` on the key.
5. Give the ARN of the key in the field **KMS key ARN for DNSSEC** of the DNS account.

## Run the plugin

1. Sign the manifest with your key:

   ```sh
   cosign sign-blob --yes --key cosign.key --output-signature plugin.json.sig plugin.json
   ```

2. In the panel, add `cosign.pub` in **Plugins > Trusted keys**.
3. Install the plugin with the address of the server. Put the server behind HTTPS.

   ```sh
   cf plugins install --manifest plugin.json --address https://dns-route53.example.net
   ```

4. Start the server with the webhook secret from the install:

   ```sh
   CF_WEBHOOK_SECRET=whsec_... PORT=8080 node server.mjs
   ```

| Variable | Value |
| -------- | ----- |
| `CF_WEBHOOK_SECRET` | The webhook secret from the install. Necessary. |
| `PORT` | The port of the server. The default is 8080. |
| `ROUTE53_API` | The address of the Route 53 API. For tests only. |

To run the plugin in the panel, use `Containerfile`. Refer to
[Run the plugin in the panel](../dns-cloudflare/README.md#run-the-plugin-in-the-panel) in the
Cloudflare DNS plugin. The steps are the same.

## Security

- The plugin gets the access key in each request from the panel. It does not keep the key
  and does not write it to the log.
- The plugin refuses a request with a signature that is not correct, or that is older than 5
  minutes. It also refuses a request with an ID that it received before.
- Give the IAM user only the policy of [Make an IAM user](#make-an-iam-user).
- To limit the user to some hosted zones, remove `CreateHostedZone` and `DeleteHostedZone`.
  Then put the other actions, except `ListHostedZonesByName`, in a statement with the ARNs of
  the hosted zones in `"Resource"`. Add each hosted zone in Route 53 before you make the zone
  in the panel.
