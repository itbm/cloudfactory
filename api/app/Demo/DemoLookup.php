<?php

namespace App\Demo;

use App\Models\DnsZone;
use App\Services\Dns\Lookup;
use App\Services\Dns\Zones;

/**
 * The public DNS of the demonstration mode. It asks no real resolver.
 *
 * - A zone that the public DNS sends to other name servers keeps these name servers, so the
 *   sample zone that waits for its registrar continues to wait.
 * - A new zone with a name that starts with "elsewhere." has the name servers of a registrar.
 * - A zone at an external DNS provider has the name servers that the provider gave.
 * - Each other zone has the name servers of the panel.
 */
class DemoLookup extends Lookup
{
    public const REGISTRAR = ['ns1.registrar.example', 'ns2.registrar.example'];

    public function nameServers(string $zone): array
    {
        $known = DnsZone::where('name', $zone)->first();
        if ($known && $known->delegation === DnsZone::ELSEWHERE) {
            return $known->delegation_name_servers ?? self::REGISTRAR;
        }
        // A zone at an external DNS provider has the name servers of the provider.
        if ($known && $known->dns_account_id !== null && ($known->provider_state['nameServers'] ?? []) !== []) {
            return $known->provider_state['nameServers'];
        }

        return str_starts_with($zone, 'elsewhere.') ? self::REGISTRAR : Zones::nameServers();
    }
}
