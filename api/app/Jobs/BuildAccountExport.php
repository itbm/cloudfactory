<?php

namespace App\Jobs;

use App\Models\AccountExport;
use App\Services\Exports\AccountExports;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * Makes the archive of an account export when the panel has all its parts
 * (AccountExports::build). The queue does not try again: a failed build marks the export as
 * failed, and the user starts a new export.
 */
class BuildAccountExport implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    /** An archive of a large account takes time to write. */
    public int $timeout = 3600;

    public function __construct(public string $exportId) {}

    public function handle(): void
    {
        if ($export = AccountExport::find($this->exportId)) {
            AccountExports::build($export);
        }
    }
}
