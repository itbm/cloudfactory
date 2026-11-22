<?php

namespace App\Services\Certificates;

/**
 * The DNS services that can hold the records of the DNS validation of an ACME order. Each
 * provider has the container tag TAG. The DNS hosting of the panel comes first.
 */
class DnsProviders
{
    public const TAG = 'certificates.dns';

    /** @return list<ChallengeDns> */
    public function all(): array
    {
        return array_values(iterator_to_array(app()->tagged(self::TAG), false));
    }

    public function find(string $key): ?ChallengeDns
    {
        foreach ($this->all() as $provider) {
            if ($provider->key() === $key) {
                return $provider;
            }
        }

        return null;
    }

    /** The provider that holds the record name for the customer, or null. */
    public function for(string $customerId, string $recordName): ?ChallengeDns
    {
        foreach ($this->all() as $provider) {
            if ($provider->holds($customerId, $recordName)) {
                return $provider;
            }
        }

        return null;
    }

    /** The name of the TXT record of the DNS validation of a name of a certificate. */
    public static function recordName(string $name): string
    {
        return '_acme-challenge.'.(str_starts_with($name, '*.') ? substr($name, 2) : $name);
    }

    /**
     * Why the panel cannot get a certificate for the names with DNS validation, or null when
     * it can.
     *
     * @param  list<string>  $names
     */
    public function problem(string $customerId, array $names): ?string
    {
        foreach ($names as $name) {
            $record = self::recordName($name);
            $provider = $this->for($customerId, $record);
            if ($provider === null) {
                $base = str_starts_with($name, '*.') ? substr($name, 2) : $name;

                return "The panel cannot add DNS records for {$base}. Make a DNS zone for the domain on the DNS page, and give the name servers of the zone to the registrar of the domain.";
            }
            if (($problem = $provider->problem($customerId, $record)) !== null) {
                return $problem;
            }
        }

        return null;
    }
}
