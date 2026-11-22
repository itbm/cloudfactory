<?php

namespace App\Services\Certificates;

use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Services\Dns\Zones;
use App\Validation\InputRules;
use Illuminate\Support\Facades\DB;

/**
 * The DNS zones of the panel as the DNS service of the DNS validation. The customer must have
 * a DNS zone with the name in it, and the public DNS must use the zone: the registrar sends the
 * queries to the name servers of the panel, or to the name servers of the external DNS
 * provider of the zone. The panel adds the TXT records to the zone with the mark "acme", sends
 * the zone to each DNS server or to its provider, and removes the records after the order.
 */
class PanelDns implements ChallengeDns
{
    /** The TTL of a TXT record of the DNS validation: the shortest TTL. */
    public const TTL = InputRules::MIN_DNS_SHORT_TTL;

    public function __construct(private Zones $zones) {}

    public function key(): string
    {
        return 'panel';
    }

    public function holds(string $customerId, string $recordName): bool
    {
        return Zones::zoneOf($customerId, $recordName) !== null;
    }

    public function problem(string $customerId, string $recordName): ?string
    {
        $zone = Zones::zoneOf($customerId, $recordName);
        if ($zone === null) {
            return null;
        }
        if (! $zone->delegatedHere()) {
            $servers = $zone->dns_account_id !== null ? 'the name servers of '.($zone->account?->providerName() ?? 'its DNS provider') : 'the name servers of this panel';

            return "The public DNS does not send the queries for {$zone->name} to {$servers}, so the certificate authority cannot see the records of the zone. Give {$servers} to the registrar of {$zone->name}, and then check the zone on the DNS page.";
        }
        $name = $zone->relative($recordName);
        if ($zone->records()->where('name', $name)->where('type', 'CNAME')->exists()) {
            return "The zone {$zone->name} has a CNAME record for {$recordName}. Remove the CNAME record, or get the certificate where the CNAME record points.";
        }

        return null;
    }

    public function present(string $customerId, array $records): array
    {
        $serials = [];
        foreach (collect($records)->groupBy(fn (array $r) => Zones::zoneOf($customerId, $r['name'])?->id) as $zoneId => $inZone) {
            $zone = DnsZone::find($zoneId);
            if (! $zone) {
                throw new AcmeException("The DNS zone of {$inZone->first()['name']} is not there now.");
            }
            DB::transaction(function () use ($zone, $inZone) {
                DnsZone::whereKey($zone->id)->lockForUpdate()->first();
                if ($zone->records()->count() + $inZone->count() > InputRules::MAX_DNS_RECORDS_PER_ZONE) {
                    throw new AcmeException("The DNS zone {$zone->name} has too many records for the records of the DNS validation.");
                }
                foreach ($inZone as $r) {
                    $zone->records()->firstOrCreate(
                        ['name' => $zone->relative($r['name']), 'type' => 'TXT', 'value' => $r['value'], 'managed' => DnsRecord::ACME],
                        ['ttl' => self::TTL],
                    );
                }
            });
            $this->zones->changed($zone, "Add the DNS validation of a certificate to {$zone->name}");
            $serials[$zone->id] = $zone->fresh()->serial;
        }

        return ['zones' => $serials];
    }

    /**
     * True when each DNS server of the panel has the serial of the records, and the external
     * DNS provider of a zone at a provider has it too. In the handover time of a zone, the
     * panel checks both.
     */
    public function ready(array $state): bool
    {
        $servers = Zones::servers();
        foreach ((array) ($state['zones'] ?? []) as $zoneId => $serial) {
            $zone = DnsZone::find($zoneId);
            if (! $zone) {
                return false;
            }
            if ($zone->dns_account_id !== null && (($zone->provider_state['serial'] ?? 0) < $serial || ($zone->provider_state['state'] ?? '') !== 'ok')) {
                return false;
            }
            if (! $zone->onPanelServers()) {
                continue;
            }
            if ($servers->isEmpty()) {
                return false;
            }
            foreach ($servers as $server) {
                $sync = $zone->sync[$server->id] ?? null;
                if (($sync['serial'] ?? 0) < $serial || ($sync['state'] ?? '') !== 'ok') {
                    return false;
                }
            }
        }

        return true;
    }

    public function cleanUp(string $customerId, array $records, array $state): void
    {
        foreach (collect($records)->groupBy(fn (array $r) => Zones::zoneOf($customerId, $r['name'])?->id) as $zoneId => $inZone) {
            $zone = DnsZone::find($zoneId);
            if (! $zone || $zone->status !== DnsZone::ACTIVE) {
                continue;
            }
            $removed = 0;
            foreach ($inZone as $r) {
                $removed += $zone->records()->where(['name' => $zone->relative($r['name']), 'type' => 'TXT', 'value' => $r['value'], 'managed' => DnsRecord::ACME])->delete();
            }
            if ($removed > 0) {
                $this->zones->changed($zone, "Remove the DNS validation of a certificate from {$zone->name}");
            }
        }
    }
}
