<?php

namespace App\Services\Dns;

/**
 * Asks the public DNS for the name servers and the DS records of a zone. The panel uses the
 * answers to show if the registrar of a domain sends the queries to the DNS servers of the
 * panel, and if the registry has the DS record of the DNSSEC key of the zone.
 */
class Lookup
{
    /**
     * The host names in the NS records of the zone, in lower case and with no dot at the end.
     * The list is empty when the public DNS has no NS records for the zone.
     *
     * @return list<string>
     */
    public function nameServers(string $zone): array
    {
        $records = @dns_get_record($zone, DNS_NS) ?: [];
        // Adversarial review, finding A6-11: an answer can have the NS records of a different
        // name, for example after a CNAME record. Only the records of the zone itself count.
        $records = array_filter($records, fn ($r) => strtolower(rtrim((string) ($r['host'] ?? ''), '.')) === strtolower($zone));
        $hosts = array_map(fn ($r) => strtolower(rtrim((string) ($r['target'] ?? ''), '.')), $records);

        return array_values(array_unique(array_filter($hosts)));
    }

    /** The type of a DS record. */
    private const DS = 43;

    /**
     * The DS records of the zone at the registry of the parent zone, as the public DNS gives
     * them. Null when the lookup failed. A resolver gives the DS records from the name servers
     * of the parent zone, also when the zone itself does not answer.
     *
     * @return list<array{keyTag: int, algorithm: int, digestType: int, digest: string, ttl: int}>|null
     */
    public function ds(string $zone): ?array
    {
        $records = @dns_get_record($zone, self::DS, $authority, $additional, true);

        return $records === false ? null : self::parseDs($zone, $records);
    }

    /**
     * The DS records in the raw answer of dns_get_record (RFC 4034, section 5.1): the key tag,
     * the algorithm, the digest type and the digest.
     *
     * @param  list<array<string, mixed>>  $records
     * @return list<array{keyTag: int, algorithm: int, digestType: int, digest: string, ttl: int}>
     */
    public static function parseDs(string $zone, array $records): array
    {
        $out = [];
        foreach ($records as $r) {
            $data = (string) ($r['data'] ?? '');
            if (($r['type'] ?? null) !== self::DS || strlen($data) < 5 || strtolower(rtrim((string) ($r['host'] ?? ''), '.')) !== strtolower($zone)) {
                continue;
            }
            $head = unpack('ntag/Calgorithm/Ctype', $data);
            $out[] = ['keyTag' => $head['tag'], 'algorithm' => $head['algorithm'], 'digestType' => $head['type'], 'digest' => strtoupper(bin2hex(substr($data, 4))), 'ttl' => (int) ($r['ttl'] ?? 0)];
        }

        return $out;
    }
}
