<?php

namespace App\Console\Commands;

use App\Models\PluginDelivery;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Adversarial review, findings A7-04 and A7-05: removes the old deliveries of plugin events,
 * and ends the deliveries that stopped with no result.
 */
#[Signature('cloudfactory:prune-plugin-deliveries')]
#[Description('Remove old deliveries of plugin events')]
class PrunePluginDeliveries extends Command
{
    /** The time that the panel keeps a delivery, in days. */
    public const KEEP_DAYS = 30;

    /** A delivery with no result after this time has no attempt in the queue. */
    public const STALE_DAYS = 2;

    public function handle(): int
    {
        $ended = PluginDelivery::where('state', PluginDelivery::PENDING)->where('updated_at', '<', now()->subDays(self::STALE_DAYS))
            ->update(['state' => PluginDelivery::FAILED, 'error' => 'The delivery stopped with no result.']);
        $removed = PluginDelivery::where('created_at', '<', now()->subDays(self::KEEP_DAYS))->delete();
        $this->info("Ended {$ended} deliveries and removed {$removed} old deliveries.");

        return self::SUCCESS;
    }
}
