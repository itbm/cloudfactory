<?php

namespace App\Console\Commands;

use App\Models\Domain;
use App\Services\Notifications\Notifier;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Finds the TLS certificates that expire in 14 days or less, and the domains that have no
 * certificate because the order failed. The panel renews a certificate 30 days before it
 * expires, so such a certificate shows that the renewal does not work. The users who want it
 * get one email for each certificate.
 */
#[Signature('cloudfactory:check-certificates')]
#[Description('Send an email for each TLS certificate that expires soon or that the panel could not get')]
class CheckCertificates extends Command
{
    public const DAYS = 14;

    public function handle(Notifier $notifier): int
    {
        $domains = Domain::with('website.customer')
            ->whereHas('website')
            ->where(fn ($q) => $q
                ->where(fn ($q) => $q->where('certificate_state', 'valid')->where('certificate_expires_at', '<=', now()->addDays(self::DAYS)))
                ->orWhereIn('certificate_state', ['expired', 'failed']))
            ->limit(1000)
            ->get();
        foreach ($domains as $domain) {
            $notifier->certificate($domain);
        }
        $this->line("{$domains->count()} certificates need attention.");

        return self::SUCCESS;
    }
}
