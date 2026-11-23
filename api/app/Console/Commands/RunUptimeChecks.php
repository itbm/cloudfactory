<?php

namespace App\Console\Commands;

use App\Services\Uptime\Monitor;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/** Sends the uptime checks that are due to the queue. The schedule runs it each minute. */
#[Signature('cloudfactory:uptime')]
#[Description('Run the uptime checks of the websites that are due')]
class RunUptimeChecks extends Command
{
    public function handle(Monitor $monitor): int
    {
        $count = $monitor->schedule();
        $this->info("Sent {$count} uptime checks to the queue.");

        return self::SUCCESS;
    }
}
