<?php

namespace App\Console\Commands;

use App\Services\Uptime\Monitor;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/** Removes the results of uptime checks after 8 days, and the closed incidents after 90 days. */
#[Signature('cloudfactory:prune-uptime')]
#[Description('Remove old results of uptime checks and old closed incidents')]
class PruneUptime extends Command
{
    public function handle(): int
    {
        [$results, $incidents] = Monitor::prune();
        $this->info("Removed {$results} results and {$incidents} closed incidents.");

        return self::SUCCESS;
    }
}
