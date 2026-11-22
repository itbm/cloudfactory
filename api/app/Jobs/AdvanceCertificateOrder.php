<?php

namespace App\Jobs;

use App\Models\Certificate;
use App\Services\Certificates\AcmeOrders;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * Does the first stage of a new ACME order at once: the order and the TXT records. The
 * command cloudfactory:certificates does the stages that wait.
 */
class AdvanceCertificateOrder implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public function __construct(public string $certificateId) {}

    public function handle(AcmeOrders $orders): void
    {
        $certificate = Certificate::find($this->certificateId);
        if ($certificate) {
            $orders->advance($certificate);
        }
    }
}
