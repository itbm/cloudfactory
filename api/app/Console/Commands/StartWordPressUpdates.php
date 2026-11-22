<?php

namespace App\Console\Commands;

use App\Services\WordPress;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;

/**
 * FR-206: the automatic updates of the WordPress sites. In the maintenance window of its server
 * (UTC), each site with automatic updates gets one check a day. When the check finds updates
 * that the settings of the site allow, the panel starts an update with a restore point. The
 * scheduler runs this command every 10 minutes.
 */
#[Signature('cloudfactory:wordpress-updates')]
#[Description('Look for WordPress updates in the maintenance window of each server')]
class StartWordPressUpdates extends Command
{
    public function handle(): int
    {
        $started = WordPress::startAutomaticChecks(Carbon::now('UTC'));
        if ($started > 0) {
            $this->line("Started {$started} WordPress checks.");
        }

        return self::SUCCESS;
    }
}
