<?php

namespace App\Console\Commands;

use App\Models\Import;
use App\Services\Imports\Runner;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

/**
 * Ends the imports that wait too long (docs/imports.md), and removes their archives from the
 * panel and from the servers: an upload that did not complete in one day, and a plan that
 * nobody started in 7 days.
 */
#[Signature('cloudfactory:prune-imports')]
#[Description('Remove the archives of imports that did not start')]
class PruneImports extends Command
{
    public const UPLOAD_DAYS = 1;

    public const READY_DAYS = 7;

    public function handle(Runner $runner): int
    {
        $stale = fn () => Import::where(fn ($q) => $q
            ->where(fn ($q) => $q->where('status', Import::UPLOADING)->where('updated_at', '<', now()->subDays(self::UPLOAD_DAYS)))
            ->orWhere(fn ($q) => $q->where('status', Import::READY)->where('updated_at', '<', now()->subDays(self::READY_DAYS))));
        $ended = 0;
        foreach ($stale()->pluck('id') as $id) {
            // The lock and the second check: the user can start the import at the same time.
            DB::transaction(function () use ($stale, $id, $runner, &$ended) {
                $import = $stale()->whereKey($id)->lockForUpdate()->first();
                if ($import) {
                    $runner->fail($import, $import->status === Import::UPLOADING
                        ? 'The upload did not complete in '.self::UPLOAD_DAYS.' day. Start a new import.'
                        : 'Nobody started the import in '.self::READY_DAYS.' days. The panel removed the archive. Start a new import.');
                    $ended++;
                }
            });
        }
        $this->info("Ended {$ended} imports.");

        return self::SUCCESS;
    }
}
