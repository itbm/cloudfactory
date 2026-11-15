<?php

namespace App\Services;

use App\Models\MailDomain;

/**
 * Checks the DNS records of a mail domain. The mail server works without them, but other mail
 * servers do not send mail to the domain without the MX record, and they refuse or mark mail
 * from the domain without the SPF, DKIM and DMARC records.
 */
class MailDns
{
    /**
     * Looks up each record of the domain, and saves which records the DNS has.
     *
     * @return list<array<string, mixed>>
     */
    public function check(MailDomain $domain): array
    {
        $records = array_map(fn (array $r) => $r + ['found' => null], $domain->records ?? []);
        foreach ($records as &$record) {
            $record['found'] = $this->found($record);
        }
        unset($record);
        $domain->update(['records' => $records, 'dns_checked_at' => now()]);

        return $records;
    }

    /** @param  array<string, mixed>  $record */
    private function found(array $record): bool
    {
        $values = $this->lookup((string) $record['name'], (string) $record['type']);
        $expected = (string) $record['value'];

        return match ($record['purpose']) {
            // "10 mail.example.net.": the DNS has an MX record with the same mail server.
            'mx' => in_array(self::host(preg_replace('/^\d+\s+/', '', $expected)), array_map(self::host(...), $values), true),
            // The domain can have a longer SPF record. It must permit the mail server.
            'spf' => collect($values)->contains(fn (string $v) => str_starts_with(strtolower($v), 'v=spf1') && (bool) preg_match('/\s\+?(mx|include:|a:|ip4:|ip6:)/i', $v)),
            'dmarc' => collect($values)->contains(fn (string $v) => str_starts_with(strtolower(trim($v)), 'v=dmarc1')),
            // The DKIM key must be the same. DNS servers divide a long value into parts.
            default => in_array(self::compact($expected), array_map(self::compact(...), $values), true),
        };
    }

    private static function host(string $name): string
    {
        return strtolower(rtrim(trim($name), '.'));
    }

    private static function compact(string $value): string
    {
        return preg_replace('/[\s"]+/', '', $value);
    }

    /**
     * The values of the records of one type: "MX" gives the mail servers, "TXT" the texts.
     *
     * @return list<string>
     */
    protected function lookup(string $name, string $type): array
    {
        $records = @dns_get_record($name, $type === 'MX' ? DNS_MX : DNS_TXT) ?: [];

        return array_values(array_map(fn ($r) => (string) ($type === 'MX' ? ($r['target'] ?? '') : ($r['txt'] ?? '')), $records));
    }
}
