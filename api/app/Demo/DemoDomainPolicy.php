<?php

namespace App\Demo;

use App\Models\DnsZone;
use App\Services\DomainPolicy;

/**
 * The domain rules of the demonstration mode. A zone at an external DNS provider that has the
 * last change gives the claim token that the panel sent with it, as the real provider does.
 * The other TXT queries go to the real resolver.
 */
class DemoDomainPolicy extends DomainPolicy
{
    protected function txtRecords(string $name): array
    {
        if (str_starts_with($name, self::CHALLENGE.'.')) {
            $zone = DnsZone::where('name', substr($name, strlen(self::CHALLENGE) + 1))->whereNotNull('dns_account_id')->first();
            if ($zone && ($zone->provider_state['state'] ?? null) === 'ok') {
                return [$this->claimToken($zone->customer_id, $zone->name)];
            }
        }

        return parent::txtRecords($name);
    }
}
