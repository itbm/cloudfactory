<?php

namespace App\Console\Commands;

use App\Services\DatabaseTools;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Removes the temporary database users of Adminer that expired, from their database servers.
 * Adminer cannot sign in again with such a user, and a session that is open stops at its next
 * request.
 */
#[Signature('cloudfactory:expire-adminer-logins')]
#[Description('Remove the temporary database users of Adminer that expired')]
class ExpireAdminerLogins extends Command
{
    public function handle(DatabaseTools $tools): int
    {
        $count = $tools->expire();
        $this->info("Removed {$count} temporary database ".($count === 1 ? 'user' : 'users').'.');

        return self::SUCCESS;
    }
}
