<?php

namespace App\Jobs;

use App\Models\DnsZone;
use App\Services\Dns\Providers;
use App\Services\Dns\Zones;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Queue\Middleware\WithoutOverlapping;

/**
 * Sends the full zone to its external DNS provider, through the plugin of the provider
 * (Providers::sync). Only one job of a zone runs at a time. A job for a serial that the
 * provider has already does nothing.
 *
 * A job that fails tries again after 1 minute, 5 minutes, 30 minutes and 2 hours. After that,
 * the command "cloudfactory:check-dns" tries again each hour.
 */
class SyncDnsZone implements ShouldQueue
{
    use Queueable;

    /** The wait in seconds before each next try. */
    public const BACKOFF = [60, 300, 1800, 7200];

    /** A job waits while a different job of the zone runs. Each wait counts as a try. */
    public int $tries = 25;

    public function __construct(public string $zoneId) {}

    /** @return list<object> */
    public function middleware(): array
    {
        return [(new WithoutOverlapping("dns-zone-{$this->zoneId}"))->releaseAfter(15)->expireAfter(Providers::TIMEOUT['sync'] + 60)];
    }

    public function handle(Providers $providers, Zones $zones): void
    {
        $zone = DnsZone::with(['account.plugin', 'customer'])->find($this->zoneId);
        if (! $zone || ! $zone->account || $zone->status !== DnsZone::ACTIVE) {
            return;
        }
        $state = $zone->provider_state ?? [];
        if (($state['serial'] ?? 0) >= $zone->serial && ($state['state'] ?? '') === 'ok') {
            return;
        }
        $serial = $zone->serial;
        $result = $providers->sync($zone);
        if ($result['error'] === null) {
            Zones::markProvider($zone, $serial, ['state' => 'ok', 'error' => null, 'tries' => 0, 'syncedAt' => now()->toIso8601String(), 'triedAt' => now()->toIso8601String(), ...$result['state']]);
            // The NS records of the zone get the name servers that the provider gives. The
            // provider keeps its own NS records. Only the DNS servers of the panel get the
            // change, in the handover time.
            $zone = $zone->fresh(['customer']);
            if (Zones::writeNameServers($zone)) {
                $zones->checkDelegation($zone);
                if ($zone->onPanelServers()) {
                    $zones->changed($zone, "Update the name servers of DNS zone {$zone->name}");
                }
            }

            return;
        }
        $tries = (int) ($state['tries'] ?? 0) + 1;
        Zones::markProvider($zone, $serial, ['state' => 'failed', 'error' => $result['error'], 'tries' => $tries, 'triedAt' => now()->toIso8601String()]);
        $wait = self::BACKOFF[$tries - 1] ?? null;
        if ($wait !== null) {
            self::dispatch($zone->id)->delay(now()->addSeconds($wait));
        }
    }
}
