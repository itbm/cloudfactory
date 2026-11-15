<?php

namespace App\Console\Commands;

use App\Events\UsageAlertResolved;
use App\Models\DatabaseServer;
use App\Models\Server;
use App\Models\UsageAlert;
use App\Models\Website;
use App\Services\UsageRecorder;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

/**
 * Removes the old usage history and the old closed alerts. It also closes the open alerts of
 * websites, servers and database servers that do not exist now.
 */
#[Signature('cloudfactory:prune-usage')]
#[Description('Remove old usage history and closed usage alerts')]
class PruneUsage extends Command
{
    /** The time that the panel keeps a closed alert, in days. */
    public const KEEP_ALERT_DAYS = 90;

    public function handle(): int
    {
        $removed = 0;
        foreach (UsageRecorder::KEEP_DAYS as $minutes => $days) {
            $removed += DB::table('usage_samples')->where('resolution', $minutes)->where('bucket_at', '<', now()->subDays($days)->format('Y-m-d H:i:s'))->delete();
        }
        // History of a subject that does not exist now.
        foreach ([UsageAlert::WEBSITE => Website::class, UsageAlert::SERVER => Server::class, UsageAlert::DATABASE_SERVER => DatabaseServer::class] as $type => $model) {
            $table = (new $model)->getTable();
            $removed += DB::table('usage_samples')->where('subject_type', $type)->whereNotIn('subject_id', DB::table($table)->select('id'))->delete();
            foreach (UsageAlert::where('subject_type', $type)->whereNull('resolved_at')->whereNotIn('subject_id', DB::table($table)->select('id'))->get() as $alert) {
                $alert->update(['resolved_at' => now()]);
                UsageAlertResolved::dispatch($alert);
            }
        }
        $alerts = UsageAlert::whereNotNull('resolved_at')->where('resolved_at', '<', now()->subDays(self::KEEP_ALERT_DAYS))->delete();
        $this->info("Removed {$removed} usage buckets and {$alerts} closed alerts.");

        return self::SUCCESS;
    }
}
