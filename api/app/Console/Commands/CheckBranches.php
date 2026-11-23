<?php

namespace App\Console\Commands;

use App\Services\Deploys\BranchChecks;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Str;

/**
 * FR-1004: the deploys on a schedule. The scheduler runs this command each minute. It checks
 * the branch of each Git source with the trigger "schedule" whose interval has passed, and
 * deploys each new commit.
 */
#[Signature('cloudfactory:check-branches')]
#[Description('Check the branch of each website that deploys on a schedule, and deploy new commits')]
class CheckBranches extends Command
{
    public function handle(BranchChecks $checks): int
    {
        $result = $checks->run();
        $this->line(sprintf('Checked %d %s. Started %d %s.', $result['checks'], Str::plural('branch', $result['checks']), $result['deploys'], Str::plural('deploy', $result['deploys'])));

        return self::SUCCESS;
    }
}
