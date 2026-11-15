<?php

namespace App\Demo;

use App\Models\MailDomain;
use App\Services\MailDns;

/**
 * The DNS check of the demonstration mode. The sample domains are not in the public DNS, so
 * the check finds all records. For a domain with a name that starts with "nodns.", it finds
 * only the MX record, to show the page of a domain with missing records.
 */
class DemoMailDns extends MailDns
{
    public function check(MailDomain $domain): array
    {
        $missing = str_starts_with($domain->name, 'nodns.');
        $records = array_map(fn (array $r) => ['found' => ! $missing || $r['purpose'] === 'mx'] + $r, $domain->records ?? []);
        $domain->update(['records' => $records, 'dns_checked_at' => now()]);

        return $records;
    }

    /**
     * The DNS records that a mail server gives for a domain: an example of the result of
     * SetMailDomain. The DKIM keys are not real keys.
     *
     * @return list<array{type: string, name: string, value: string, purpose: string}>
     */
    public static function records(string $domain, string $host): array
    {
        $key = fn (string $kind, int $length) => substr(base64_encode(hash('sha512', "{$kind}|{$domain}", true).hash('sha512', "{$domain}|{$kind}", true).hash('sha512', $kind, true).hash('sha512', $domain, true).hash('sha512', "2|{$kind}|{$domain}", true)), 0, $length);

        return [
            ['type' => 'MX', 'name' => $domain, 'value' => "10 {$host}.", 'purpose' => 'mx'],
            ['type' => 'TXT', 'name' => $domain, 'value' => 'v=spf1 mx -all', 'purpose' => 'spf'],
            ['type' => 'TXT', 'name' => "v1-ed25519-20261114._domainkey.{$domain}", 'value' => 'v=DKIM1; k=ed25519; h=sha256; p='.$key('ed25519', 44), 'purpose' => 'dkim'],
            ['type' => 'TXT', 'name' => "v1-rsa-20261114._domainkey.{$domain}", 'value' => 'v=DKIM1; k=rsa; h=sha256; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA'.$key('rsa', 348), 'purpose' => 'dkim'],
            ['type' => 'TXT', 'name' => "_dmarc.{$domain}", 'value' => "v=DMARC1; p=reject; rua=mailto:postmaster@{$domain}", 'purpose' => 'dmarc'],
        ];
    }
}
