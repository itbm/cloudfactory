<?php

namespace App\Console\Commands;

use App\Models\Certificate;
use App\Services\Certificates\AcmeOrders;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * The ACME orders of the certificates with DNS validation (docs/certificates.md). The command
 * starts the renewals that are due, and does the next stage of each order when it is time. It
 * runs each minute, and it checks the orders again for up to --seconds seconds, so that a stage
 * that waits does not wait for the next minute.
 */
#[Signature('cloudfactory:certificates {--seconds=50 : Check the orders that run again for this time}')]
#[Description('Renew the certificates with DNS validation, and continue their orders')]
class AdvanceCertificates extends Command
{
    /** The number of renewals that the command starts in one run. */
    public const RENEWALS = 20;

    public function handle(AcmeOrders $orders): int
    {
        $renewals = Certificate::where('source', Certificate::ACME)->whereNull('order')->whereNotNull('certificate')
            ->where('renew_at', '<=', now())
            ->where(fn ($q) => $q->whereNull('next_try_at')->orWhere('next_try_at', '<=', now()))
            ->orderBy('renew_at')->limit(self::RENEWALS)->get();
        foreach ($renewals as $certificate) {
            $orders->start($certificate);
        }
        $end = microtime(true) + max(0, (int) $this->option('seconds'));
        do {
            $due = Certificate::whereNotNull('order')->where(fn ($q) => $q->whereNull('next_try_at')->orWhere('next_try_at', '<=', now()))->get();
            foreach ($due as $certificate) {
                $orders->advance($certificate);
            }
            $next = Certificate::whereNotNull('order')->min('next_try_at');
            if ($next === null) {
                break;
            }
            $wait = max(1, min(10, now()->diffInSeconds($next, false)));
            if (microtime(true) + $wait > $end) {
                break;
            }
            sleep((int) ceil($wait));
        } while (true);
        if ($renewals->isNotEmpty()) {
            $this->line("Started {$renewals->count()} renewals.");
        }

        return self::SUCCESS;
    }
}
