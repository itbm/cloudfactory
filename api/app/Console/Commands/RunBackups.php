<?php

namespace App\Console\Commands;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Api\BackupController;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Website;
use App\Services\JobDispatcher;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;

/**
 * FR-901 and FR-902: the nightly backup of each website at the time of the backup policy (UTC),
 * with the retention policy. On Sunday, one server also removes the data that no snapshot uses.
 * The scheduler runs this command each minute. It starts the backups once each day.
 */
#[Signature('cloudfactory:backups {--now : Start the backups now}')]
#[Description('Start the scheduled backups of all websites')]
class RunBackups extends Command
{
    public function handle(JobDispatcher $jobs): int
    {
        $policy = BackupController::policy();
        if (! preg_match(BackupController::TARGET, (string) ($policy['target'] ?? ''))) {
            $this->line('No backup target is set.');

            return self::SUCCESS;
        }
        $now = Carbon::now('UTC');
        $today = $now->toDateString();
        if (! $this->option('now') && ($now->format('H:i') < $policy['time'] || Setting::get('backups_last_run') === $today)) {
            return self::SUCCESS;
        }
        Setting::put('backups_last_run', $today);

        $count = 0;
        Website::with('server')->whereNull('staging_of')->where('status', '!=', WebsiteStatus::Failed->value)->orderBy('name')
            ->each(function (Website $website) use ($jobs, &$count) {
                $jobs->dispatch($website->server, AgentAction::Backup, BackupController::backupPayload($website, 'scheduled'), "Nightly backup of {$website->name}", $website);
                $count++;
            });
        $this->line("Started {$count} backups.");

        // All servers use the same repository. One server removes the unused data each week.
        if ($now->isSunday() && ($server = Server::whereJsonContains('roles', 'web')->where('state', 'healthy')->orderBy('name')->first())) {
            $jobs->dispatch($server, AgentAction::PruneBackups, ['target' => $policy['target']], 'Remove unused backup data');
            $this->line("Started the weekly prune on {$server->name}.");
        }

        return self::SUCCESS;
    }
}
