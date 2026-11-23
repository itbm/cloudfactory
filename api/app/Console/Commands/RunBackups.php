<?php

namespace App\Console\Commands;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Api\BackupController;
use App\Models\BackupRemoval;
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
 * with the retention policy. On Sunday, each server also removes from its repository the
 * backups of the websites that were deleted some time ago, the backups of the files of the
 * websites that moved to a different server some time ago, and the data that no snapshot uses.
 * The scheduler runs this command each minute. It starts the backups once each day.
 */
#[Signature('cloudfactory:backups {--now : Start the backups now}')]
#[Description('Start the scheduled backups of all websites')]
class RunBackups extends Command
{
    /** The maximum number of deleted websites in the weekly job of one server. */
    public const DELETED_WEBSITES = 200;

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
                // The files of a website that moves are on the way to its new server.
                if ($website->status === WebsiteStatus::Moving) {
                    $this->line("No backup of {$website->name}: the website moves to a different server.");

                    return;
                }
                // A backup would start a database server that moves to a different server
                // (adversarial review, finding A4-01). The website has its backup the next night.
                if ($moving = BackupController::movingServerOf($website)) {
                    $this->line("No backup of {$website->name}: the database server {$moving->name} moves to a different server.");

                    return;
                }
                BackupController::start($jobs, $website, 'scheduled', "Nightly backup of {$website->name}");
                $count++;
            });
        $this->line("Started {$count} backups.");

        // Audit finding S2: each server has its own repository, and removes its unused data
        // each week. A server that is not healthy does it in a later week.
        if ($now->isSunday()) {
            foreach (Server::whereJsonContains('roles', 'web')->whereIn('state', Server::WORKING)->orderBy('name')->get() as $server) {
                // Audit finding R8: the job also removes the backups of the websites that were
                // deleted some time ago. A limit keeps the job short. The rest goes next week.
                // A website that moved to a different server: the job removes only the backups
                // of its files. The backups of its databases on this server stay.
                $removals = BackupRemoval::where('server_id', $server->id)->where('remove_after', '<=', $now)->orderBy('remove_after')->limit(self::DELETED_WEBSITES)->get(['website_id', 'files_only']);
                $jobs->dispatch($server, AgentAction::PruneBackups, [
                    'target' => BackupController::repository($server),
                    'forgetWebsiteIds' => $removals->where('files_only', false)->pluck('website_id')->values()->all(),
                    'forgetMovedWebsiteIds' => $removals->where('files_only', true)->pluck('website_id')->values()->all(),
                ], "Remove unused backup data of {$server->name}");
                $this->line("Started the weekly prune on {$server->name}.");
            }
        }

        return self::SUCCESS;
    }
}
