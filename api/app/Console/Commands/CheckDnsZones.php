<?php

namespace App\Console\Commands;

use App\Models\DnsZone;
use App\Services\Dns\Zones;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Asks the public DNS for the name servers of the DNS zones. A zone that the public DNS does
 * not send to the panel yet gets a check each time. All other zones get a check one time each
 * day, so that the panel sees when a domain goes to a different DNS provider.
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

        return self::SUCCESS;
    }
}
