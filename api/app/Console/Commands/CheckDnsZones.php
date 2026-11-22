<?php

namespace App\Console\Commands;

use App\Jobs\SyncDnsZone;
use App\Models\DnsZone;
use App\Services\Dns\Zones;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;

/**
 * Asks the public DNS for the name servers of the DNS zones. A zone that the public DNS does
 * not send to the panel yet gets a check each time. All other zones get a check one time each
 * day, so that the panel sees when a domain goes to a different DNS provider. A zone that its
 * external DNS provider does not have in the last form goes to the provider again.
 */
#[Signature('cloudfactory:check-dns {--all : Check each zone, not only the zones that wait for their name servers}')]
#[Description('Check in the public DNS that the name servers of each DNS zone are the name servers of the panel')]
class CheckDnsZones extends Command
{
    public function handle(Zones $zones): int
    {
        $query = DnsZone::where('status', DnsZone::ACTIVE)
            ->when(! $this->option('all'), fn ($q) => $q->where(fn ($q) => $q
                ->where('delegation', '!=', DnsZone::HERE)
                ->orWhereNull('delegation_checked_at')
                ->orWhere('delegation_checked_at', '<', now()->subDay())));
        $checked = 0;
        // The oldest check first. A limit keeps one run short when the panel has many zones.
        foreach ($query->orderBy('delegation_checked_at')->limit(500)->get() as $zone) {
            $zones->checkDelegation($zone);
            $checked++;
        }
        $this->info("Checked {$checked} DNS ".($checked === 1 ? 'zone' : 'zones').'.');

        // A zone that its external DNS provider does not have in the last form: the job tried
        // some times and stopped, or the queue lost it. The panel sends it again each hour.
        $resent = 0;
        foreach (DnsZone::where('status', DnsZone::ACTIVE)->whereNotNull('dns_account_id')->get() as $zone) {
            $state = $zone->provider_state ?? [];
            $current = ($state['serial'] ?? 0) >= $zone->serial && ($state['state'] ?? '') === 'ok';
            $tried = isset($state['triedAt']) ? Carbon::parse($state['triedAt']) : null;
            if (! $current && ($tried === null || $tried->lt(now()->subHour()))) {
                SyncDnsZone::dispatch($zone->id);
                $resent++;
            }
        }
        if ($resent > 0) {
            $this->info("Sent {$resent} DNS ".($resent === 1 ? 'zone' : 'zones').' to the DNS provider again.');
        }

        return self::SUCCESS;
    }
}
