<?php

namespace App\Console\Commands;

use App\Services\Exports\AccountExports;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Removes the account exports that expired, with their archives. An export that is not
 * complete after one day fails.
 */
#[Signature('cloudfactory:expire-exports')]
#[Description('Remove the account exports that expired')]
class ExpireExports extends Command
{
    public function handle(): int
    {
        $count = AccountExports::expire();
        $this->info("Changed {$count} account ".($count === 1 ? 'export' : 'exports').'.');

        return self::SUCCESS;
    }
}
