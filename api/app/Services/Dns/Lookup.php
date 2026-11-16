<?php

namespace App\Services\Dns;

/**
 * Asks the public DNS for the name servers of a zone. The panel uses the answer to show if the
 * registrar of a domain sends the queries to the DNS servers of the panel.
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
}
