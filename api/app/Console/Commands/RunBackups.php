<?php

namespace App\Console\Commands;

use App\Enums\AgentAction;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Api\BackupController;
use App\Http\Controllers\Api\MailBackupController;
use App\Models\BackupRemoval;
use App\Models\Mailbox;
use App\Models\MailboxSnapshot;
use App\Models\MailDomain;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Website;
use App\Services\JobDispatcher;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;

/**
 * FR-901 and FR-902: the nightly backup of each website and each mailbox at the time of the
 * backup policy (UTC), with the retention policy. On Sunday, each server also removes from its
 * repository the backups of the websites and the mailboxes that were deleted some time ago,
 * and the data that no snapshot uses.
 * The scheduler runs this command each minute. It starts the backups once each day.
 */
#[Signature('cloudfactory:backups {--now : Start the backups now}')]
#[Description('Start the scheduled backups of all websites and mailboxes')]
class RunBackups extends Command
{
    /** The maximum number of deleted websites, and of deleted mailboxes, in the weekly job of one server. */
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

        // The backup of each mailbox, on the server of its mail domain.
        $mailboxes = 0;
        $busy = MailBackupController::busyIds();
        Mailbox::with('domain.server')->where('status', MailDomain::ACTIVE)->orderBy('id')
            ->each(function (Mailbox $mailbox) use ($jobs, $busy, &$mailboxes) {
                // A backup that did not end yesterday continues. The mailbox has its backup
                // the next night.
                if (isset($busy[$mailbox->id])) {
                    return;
                }
                MailBackupController::start($jobs, $mailbox, 'scheduled', "Nightly backup of mailbox {$mailbox->address()}");
                $mailboxes++;
            });
        $this->line("Started {$mailboxes} mailbox backups.");

        // Audit finding S2: each server has its own repository, and removes its unused data
        // each week. A server that is not healthy does it in a later week.
        if ($now->isSunday()) {
            foreach (BackupController::repositoryServers()->whereIn('state', Server::WORKING)->orderBy('name')->get() as $server) {
                // Audit finding R8: the job also removes the backups of the websites and the
                // mailboxes that were deleted some time ago. A limit keeps the job short. The
                // rest goes next week.
                $deleted = BackupRemoval::where('server_id', $server->id)->where('remove_after', '<=', $now)->orderBy('remove_after')->limit(self::DELETED_WEBSITES)->pluck('website_id')->all();
                $mailboxIds = MailboxSnapshot::where('server_id', $server->id)->where('remove_after', '<=', $now)->distinct()->orderBy('mailbox_id')->limit(self::DELETED_WEBSITES)->pluck('mailbox_id')->all();
                $jobs->dispatch($server, AgentAction::PruneBackups, ['target' => BackupController::repository($server), 'forgetWebsiteIds' => $deleted, 'forgetMailboxIds' => $mailboxIds], "Remove unused backup data of {$server->name}");
                $this->line("Started the weekly prune on {$server->name}.");
            }
        }

        return self::SUCCESS;
    }
}
