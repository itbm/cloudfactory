<?php

namespace App\Jobs;

use App\Models\Certificate;
use App\Services\Certificates\AcmeOrders;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * Does the next stage of an ACME order, and puts itself in the queue again for the next check
 * while the order runs. The job stops when a new order of the certificate starts. The command
 * cloudfactory:certificates also does the stages, for example when the queue lost the job.
 */
class AdvanceCertificateOrder implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public function __construct(public string $certificateId, public ?string $startedAt = null) {}

    public function handle(AcmeOrders $orders): void
    {
        $certificate = Certificate::find($this->certificateId);
        if (! $certificate || ! $certificate->ordering()) {
            return;
        }
        $startedAt = $this->startedAt ?? ($certificate->order['startedAt'] ?? null);
        if (($certificate->order['startedAt'] ?? null) !== $startedAt) {
            return;
        }
        // With the queue "sync", the job runs in the request of the user. The command does the
        // stages that wait.
        if (! $orders->advance($certificate) || config('queue.default') === 'sync') {
            return;
        }
        $certificate->refresh();
        if (($certificate->order['startedAt'] ?? null) === $startedAt) {
            $wait = $certificate->next_try_at ? (int) ceil(now()->diffInSeconds($certificate->next_try_at, false)) : AcmeOrders::POLL_SECONDS;
            self::dispatch($certificate->id, $startedAt)->delay(max(1, $wait));
        }
    }
}
