<?php

namespace App\Jobs;

use App\Models\UptimeCheck;
use App\Services\Uptime\Monitor;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * Requests the addresses of some uptime checks in parallel, and records the results. The
 * schedule (cloudfactory:uptime) sends the checks that are due, in batches of 25.
 */
class RunUptimeChecks implements ShouldQueue
{
    use Queueable;

    /** A batch does not run again: the schedule sends the checks again when they are due. */
    public int $tries = 1;

    /** The time limit of the requests is 30 seconds at most. */
    public int $timeout = 120;

    /** @param  list<string>  $checkIds */
    public function __construct(public array $checkIds) {}

    public function handle(Monitor $monitor): void
    {
        $monitor->run(UptimeCheck::whereIn('id', $this->checkIds)->get());
    }
}
