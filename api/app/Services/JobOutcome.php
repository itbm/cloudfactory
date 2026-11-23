<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\BackupRemoval;
use App\Models\CronJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Domain;
use App\Models\MailAlias;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Models\Release;
use App\Models\Rollout;
use App\Models\Snapshot;
use App\Models\Website;
use App\Services\Certificates\Certificates;
use App\Services\Deploys\Deploys;
use App\Services\Dns\Zones;
use App\Services\Imports\Runner;
use App\Services\Notifications\Notifier;
use App\Services\Packs\Packs;
use App\Services\Plugins\Events;
use App\Services\Plugins\Hosting;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Str;

/**
 * Spec section 6.3, step 7: the agent reports the result and the actual state.
 * This class changes the panel state to agree with the result.
 */
class JobOutcome
{
    public function __construct(private RolloutEngine $rollouts) {}

    /**
     * Audit finding R1: each change of the job state reads the job again with a lock, and
     * changes only a job that is not complete. Thus a late event cannot move a complete job
     * back to "running", and two final reports cannot both apply their result.
     *
     * @param  callable(AgentJob): array<string, mixed>  $change
     */
    private static function changeOpen(AgentJob $job, callable $change, bool $queuedOnly = false): bool
    {
        return DB::transaction(function () use ($job, $change, $queuedOnly) {
            $current = AgentJob::lockForUpdate()->find($job->id);
            if (! $current || $current->state->finished() || ($queuedOnly && $current->state !== JobState::Queued)) {
                return false;
            }
            $current->update($change($current));
            $job->setRawAttributes($current->getAttributes(), true);

            return true;
        });
    }

    /** The agent accepted the job. */
    public function started(AgentJob $job): void
    {
        self::changeOpen($job, fn (AgentJob $j) => ['state' => JobState::Running, 'started_at' => $j->started_at ?? now()], queuedOnly: true);
    }

    /** The panel sends a job with no news to the agent again. False when the job is complete. */
    public function queueAgain(AgentJob $job): bool
    {
        return self::changeOpen($job, fn () => ['state' => JobState::Queued, 'resent_at' => now()]);
    }

    public function progress(AgentJob $job, int $step): void
    {
        self::changeOpen($job, fn (AgentJob $j) => [
            'state' => JobState::Running,
            'step' => min($step, count($j->steps)),
            'started_at' => $j->started_at ?? now(),
        ]);
    }

    /** @param array<string, mixed> $result */
    public function finish(AgentJob $job, JobState $state, ?string $error = null, array $result = []): void
    {
        // Adversarial review, finding A3-02: the change of the state and the effects of the
        // result are one transaction. If an effect fails, the job stays open, and the next
        // report of the agent applies the result again. Before, the result was lost.
        $website = null;
        $applied = DB::transaction(function () use ($job, $state, $error, $result, &$website) {
            $changed = self::changeOpen($job, fn (AgentJob $j) => [
                'state' => $state,
                // The agent has the passwords now. The panel does not keep them.
                'payload' => self::withoutPasswords($j->payload),
                'step' => $state === JobState::Succeeded ? count($j->steps) : $j->step,
                'error' => $error,
                'result' => $state === JobState::Succeeded ? self::keptResult($j, $result) : null,
                'finished_at' => now(),
            ]);
            if (! $changed) {
                return false;
            }

            $website = $job->website_id ? Website::find($job->website_id) : null;
            $state === JobState::Succeeded
                ? $this->applySuccess($job, $website, $result)
                : $this->applyFailure($job, $website);
            $this->databaseServersOf($job, $state === JobState::Succeeded);
            // A job of the setup of a mail cluster: the next step starts when the step is complete.
            if (isset($job->payload['mailClusterId']) && in_array($job->action, [AgentAction::PrepareMailNode, AgentAction::SetMailNode, AgentAction::SetMailServer], true)) {
                app(MailClusters::class)->finished($job, $state === JobState::Succeeded, $result);
            }
            // The container of Adminer and its temporary database users.
            DatabaseTools::jobFinished($job, $state === JobState::Succeeded);
            // The container of a plugin that the panel runs.
            Hosting::jobFinished($job, $state === JobState::Succeeded);
            // A preview of a pull request: the first deploy after the website, or a deploy again.
            Previews::jobFinished($job, $state === JobState::Succeeded);
            if (in_array($job->action, [AgentAction::SetDnsZones, AgentAction::DeleteDnsZones], true)) {
                Zones::jobFinished($job, $state === JobState::Succeeded);
            }
            if ($job->action === AgentAction::SetPacks && $state === JobState::Succeeded) {
                Packs::jobFinished($job, $result);
            }
            if ($job->action === AgentAction::SetCertificates && $state === JobState::Succeeded) {
                Certificates::jobFinished($job, $result);
            }
            // The domains of the server changed: the server gets the certificates from the
            // panel for its domains now, not at the next report.
            if ($state === JobState::Succeeded && in_array($job->action, [AgentAction::CreateWebsite, AgentAction::DeleteWebsite, AgentAction::AddDomain, AgentAction::RemoveDomain], true)) {
                app(Certificates::class)->serverChanged($job->server, $job->customer_id);
            }
            // A job of an import: the next parts of the import can start.
            if ($job->import_id !== null) {
                app(Runner::class)->jobFinished($job, $state === JobState::Succeeded, $result);
            }

            if ($job->rollout_id && ($rollout = Rollout::find($job->rollout_id))) {
                $this->rollouts->jobFinished($rollout, $job);
            }

            return true;
        });
        if (! $applied) {
            return;
        }
        // The users who want it get an email about the failure.
        if ($state === JobState::Failed) {
            app(Notifier::class)->jobFailed($job, $website);
        }
        // FR-1009: the website kept a release that failed its health check after a migration.
        if ($state === JobState::Succeeded && $job->action === AgentAction::Deploy && $website
            && ($release = Release::whereKey($job->payload['releaseId'] ?? '')->whereNotNull('health_error')->first())) {
            app(Notifier::class)->deployNeedsAttention($website, $release);
        }
        // A push came during the deploy: the panel deploys it now. An error does not change the
        // result of the job.
        try {
            Deploys::jobFinished($job, app(JobDispatcher::class));
        } catch (\Throwable $e) {
            Log::warning("The panel could not start the queued deploy after job {$job->id}: {$e->getMessage()}");
        }

        Events::emit(Events::JOB_FINISHED, [
            'id' => $job->id,
            'action' => $job->action->value,
            'title' => $job->title,
            'state' => $state->value,
            'error' => $error,
            'serverId' => $job->server_id,
            'websiteId' => $job->website_id,
            'customerId' => $website?->customer_id,
        ]);
    }

    /**
     * The agent gives the DNS state of the first domain with the result of the job. Before, the
     * panel waited for the next report of the agent, and a new website showed "Waiting for DNS"
     * for about one minute, also for a name such as shop.localhost that always points here.
     *
     * @param  array<string, mixed>  $result
     */
    private function websiteMade(Website $website, array $result): bool
    {
        $website->moveTo(WebsiteStatus::Running);
        // A staging copy: the database number of each new Valkey database.
        foreach (is_array($result['valkey'] ?? null) ? $result['valkey'] : [] as $name => $number) {
            if (is_string($name) && is_int($number) && $number > 0) {
                $website->databases()->where(['engine' => Database::VALKEY, 'name' => $name])->update(['number' => $number]);
            }
        }
        if (array_key_exists('dnsPointsHere', $result)) {
            $website->domains()->where('name', $website->name)->update(['dns_points_here' => (bool) $result['dnsPointsHere']]);
        }

        return true;
    }

    /**
     * The packages that stay after an update job. The server installs all packages only with the
     * policy "all". With "security" it installs the security updates, and with "tools" the held
     * container tools (FR-1113). Before, the panel showed no pending package after each of these
     * jobs, until the next report of the agent.
     *
     * @param  list<array<string, mixed>>  $pending
     * @return list<array<string, mixed>>
     */
    public static function updatesLeft(array $pending, string $policy): array
    {
        return array_values(array_filter($pending, function (array $package) use ($policy) {
            $tool = (bool) ($package['containerTool'] ?? false);

            return match ($policy) {
                'tools' => ! $tool,
                'all' => $tool,
                default => $tool || ! ($package['security'] ?? false),
            };
        }));
    }

    private function applySuccess(AgentJob $job, ?Website $website, array $result): void
    {
        $p = $job->payload;
        match ($job->action) {
            AgentAction::CreateWebsite => $website && $this->websiteMade($website, $result),
            AgentAction::SetWebsitePower => $website?->moveTo(($p['power'] ?? '') === 'stop' ? WebsiteStatus::Stopped : WebsiteStatus::Running),
            AgentAction::DeleteWebsite => $website && $this->deleteWebsite($website),
            // Audit finding R8: the backups of these deleted websites are gone.
            AgentAction::PruneBackups => BackupRemoval::where('server_id', $job->server_id)->whereIn('website_id', array_filter((array) ($p['forgetWebsiteIds'] ?? []), 'is_string'))->delete(),
            AgentAction::SetSftp => $website?->update([
                'sftp_enabled' => (bool) ($p['enabled'] ?? false),
                'sftp_keys' => $p['authorizedKeys'] ?? [],
            ]),
            AgentAction::ConfigureHost => $job->server->update(['gvisor_platform' => $p['gvisorPlatform'] ?? $job->server->gvisor_platform]),
            // The agent gives the new state of the certificate. The panel does not wait for the next report.
            AgentAction::SetPanelCertificate => $job->server->local && is_array($result['panelCertificate'] ?? null)
                && PanelCertificate::record($result['panelCertificate']),
            // The state of the certificate of each reseller panel domain (white label).
            AgentAction::SetPanelBrands => $job->server->local && is_array($result['panelBrands'] ?? null)
                && Branding::recordCertificates($result['panelBrands']),
            AgentAction::AddDomain => Domain::where('name', $p['domain'] ?? '')->update([
                'dns_points_here' => (bool) ($result['dnsPointsHere'] ?? true),
                'certificate_state' => ($result['dnsPointsHere'] ?? true) ? 'valid' : 'pending',
                'certificate_expires_at' => ($result['dnsPointsHere'] ?? true) ? now()->addDays(90) : null,
            ]),
            AgentAction::Backup => $website && $this->recordSnapshot($job, $website, $p, $result),
            // Batch 1, finding 4: the panel removes the row when the item is gone from the server.
            AgentAction::CreateDatabase => $website && $this->databaseMade($website, $p, $result),
            AgentAction::DeleteDatabase => $website && $this->databaseRemoved($website, $p) && $this->continueRemoval($website, $p),
            // The panel removes the record when the database server is gone from the server.
            AgentAction::DeleteDatabaseServer => $this->databaseServerRemoved($job),
            // The database server operates on the new server.
            AgentAction::ReceiveDatabaseServer => $this->databaseServerMoved($job, $p),
            AgentAction::DeleteDatabaseUser => $website && ($website->databaseUsers()->where(['engine' => $p['engine'] ?? '', 'name' => $p['name'] ?? ''])->delete() || true) && $this->continueRemoval($website, $p),
            AgentAction::InstallHostUpdates => $job->server->update([
                'pending_updates' => self::updatesLeft($job->server->pending_updates ?? [], (string) ($p['policy'] ?? 'security')),
                'last_update_at' => now(),
                'reboot_required' => (bool) ($result['rebootRequired'] ?? $job->server->reboot_required),
            ]),
            AgentAction::RunCronJob => self::cronRan($job, (int) ($result['exitCode'] ?? 0), (string) ($result['output'] ?? '')),
            AgentAction::Deploy => $website && self::deployed($job, $website, $result),
            AgentAction::ActivateRelease => $website && self::releaseLive($website, (string) ($p['releaseId'] ?? '')),
            AgentAction::Reboot => $job->server->update(['state' => 'healthy', 'reboot_required' => false]),
            // The result has the state of each tunnel. The panel does not wait for the next report.
            AgentAction::SetMesh => is_array($result['mesh'] ?? null) && app(Mesh::class)->reported($job->server, $result['mesh']),
            // The peers of the server get its new public key.
            AgentAction::RotateMeshKey => is_string($result['publicKey'] ?? null) && Mesh::validKey($result['publicKey'])
                && app(Mesh::class)->keyOf($job->server, $result['publicKey']),
            AgentAction::UpdateAgent => $job->server->update([
                'agent_version' => $result['version'] ?? config('cloudfactory.agent.latest_version'),
                'state' => 'healthy',
            ]),
            AgentAction::SetMailServer => $job->server->update(['mail_status' => 'running']),
            // The mail server made the DKIM keys of the domain. The result has the DNS records.
            AgentAction::SetMailDomain => MailDomain::whereKey($p['mailDomainId'] ?? '')->where('status', MailDomain::PENDING)->first()
                ?->update(['status' => MailDomain::ACTIVE, 'records' => Mail::records($result['records'] ?? null)]),
            AgentAction::SetMailbox => $this->mailboxSet($p, $result),
            AgentAction::SetMailAlias => MailAlias::whereKey($p['mailAliasId'] ?? '')->where('status', MailDomain::PENDING)->update(['status' => MailDomain::ACTIVE]),
            AgentAction::DeleteMailDomain => MailDomain::whereKey($p['mailDomainId'] ?? '')->where('status', MailDomain::REMOVING)->delete(),
            AgentAction::DeleteMailbox => Mailbox::whereKey($p['mailboxId'] ?? '')->where('status', MailDomain::REMOVING)->delete(),
            AgentAction::DeleteMailAlias => MailAlias::whereKey($p['mailAliasId'] ?? '')->where('status', MailDomain::REMOVING)->delete(),
            default => null,
        };
    }

    /**
     * The result that the panel keeps for the user (AgentAction::keepsResult): the counts of
     * an archive operation and the first names that the agent skipped. The values come from
     * the agent, so the panel keeps only the known fields, each with its type.
     *
     * @param  array<string, mixed>  $result
     * @return array<string, mixed>|null
     */
    private static function keptResult(AgentJob $job, array $result): ?array
    {
        if (! $job->action->keepsResult()) {
            return null;
        }
        $kept = [];
        foreach (['files', 'directories', 'links', 'sizeBytes', 'kept', 'skipped'] as $field) {
            $kept[$field] = max(0, (int) ($result[$field] ?? 0));
        }
        $names = array_filter(is_array($result['skippedNames'] ?? null) ? $result['skippedNames'] : [], 'is_string');
        $kept['skippedNames'] = array_map(fn (string $n) => Str::limit($n, 200), array_slice(array_values($names), 0, 20));

        return $kept;
    }

    private function applyFailure(AgentJob $job, ?Website $website): void
    {
        $p = $job->payload;
        match ($job->action) {
            AgentAction::CreateWebsite => $website?->moveTo(WebsiteStatus::Failed),
            // FR-1105: the agent went back to the previous image. The panel records the same.
            // Batch 1, finding 3: a change of the PHP settings has the old settings in "revert".
            // An install with a change of the runtime version goes back in the same way.
            AgentAction::InstallDependencies => isset($p['revert']) ? $website?->update($p['revert']) : null,
            AgentAction::SetRuntime => $website?->update(isset($p['revert']) ? $p['revert'] : [
                'image_id' => $p['previousImageId'] ?? $website->image_id,
                'image_tag' => $p['previousTag'] ?? $website->image_tag,
            ]),
            // Batch 1, finding 4: an item that the agent did not make is not in the panel.
            AgentAction::CreateDatabase => $this->removeNewDatabase($website, $p),
            AgentAction::SetDatabaseUser => ($p['create'] ?? false) ? $website?->databaseUsers()->where(['engine' => $p['engine'] ?? '', 'name' => $p['name'] ?? ''])->delete() : null,
            // The domain stays, so that the owner can see the error and check the DNS again.
            AgentAction::AddDomain => Domain::where('name', $p['domain'] ?? '')->update(['certificate_state' => 'failed']),
            // The power change did not complete. The website has its old state.
            AgentAction::SetWebsitePower => $website?->moveTo(($p['power'] ?? '') === 'start' ? WebsiteStatus::Stopped : WebsiteStatus::Running),
            AgentAction::Reboot => $job->server->update(['state' => 'degraded']),
            // The command failed, or it did not start. The error has the end of its output.
            AgentAction::RunCronJob => self::cronRan($job, preg_match('/exit code (-?\d+)/', (string) $job->error, $m) ? (int) $m[1] : -1, (string) preg_replace('/^the command ended with exit code -?\d+: /', '', (string) $job->error)),
            // The website continues from the release that it had (FR-1008). The release records
            // a migration command that ran: the database can have changes.
            AgentAction::Deploy => Release::whereKey($p['releaseId'] ?? '')->where('website_id', $job->website_id)->where('state', Release::BUILDING)
                ->update(['state' => Release::FAILED, 'error' => $job->error, 'duration_seconds' => self::seconds($job),
                    'migration' => ($p['migrationCommand'] ?? '') !== '' && $job->step >= array_search('Migrate the database', AgentAction::Deploy->steps(), true)]),
            AgentAction::SetDatabaseServer => $this->databaseServerKept($job, $p),
            // The database server is on the server still.
            AgentAction::DeleteDatabaseServer => $this->databaseServerStays($job, $p),
            // The database server stays on the old server.
            AgentAction::ReceiveDatabaseServer => $this->databaseServerNotMoved($job, $p),
            // A mail server that operated before the change operates with its old host name.
            AgentAction::SetMailServer => $job->server->update(($p['previous']['status'] ?? null) === 'running' && ! $job->server->mailDomains()->exists()
                ? ['mail_hostname' => $p['previous']['hostname'] ?? $job->server->mail_hostname, 'mail_status' => 'running']
                : ['mail_status' => 'failed']),
            AgentAction::SetMailDomain => MailDomain::whereKey($p['mailDomainId'] ?? '')->where('status', MailDomain::PENDING)->update(['status' => MailDomain::FAILED]),
            // A new mailbox is possibly not on the mail server. A change that failed keeps the old size.
            AgentAction::SetMailbox => ($p['create'] ?? false)
                ? Mailbox::whereKey($p['mailboxId'] ?? '')->where('status', MailDomain::PENDING)->update(['status' => MailDomain::FAILED])
                : (is_int($p['previousQuotaBytes'] ?? null) ? Mailbox::whereKey($p['mailboxId'] ?? '')->update(['quota_bytes' => $p['previousQuotaBytes']]) : null),
            AgentAction::SetMailAlias => MailAlias::whereKey($p['mailAliasId'] ?? '')->where('status', MailDomain::PENDING)->update(['status' => MailDomain::FAILED]),
            AgentAction::DeleteMailDomain => MailDomain::whereKey($p['mailDomainId'] ?? '')->where('status', MailDomain::REMOVING)->update(['status' => self::mailStatus($p)]),
            AgentAction::DeleteMailbox => Mailbox::whereKey($p['mailboxId'] ?? '')->where('status', MailDomain::REMOVING)->update(['status' => self::mailStatus($p)]),
            AgentAction::DeleteMailAlias => MailAlias::whereKey($p['mailAliasId'] ?? '')->where('status', MailDomain::REMOVING)->update(['status' => self::mailStatus($p)]),
            default => null,
        };
    }

    /** The agent removed a database server. Its record goes, and the networks do not have it. */
    private function databaseServerRemoved(AgentJob $job): void
    {
        DatabaseServer::whereKey($job->database_server_id)->where('status', DatabaseServer::REMOVING)->delete();
        Mesh::refreshLater();
    }

    /**
     * The new server received the data of a database server, and the database server operates
     * there. The panel records the new server and the new port, and the old server removes its
     * copy. The network address stays, so the tunnels now take the traffic to the new server.
     *
     * @param  array<string, mixed>  $p
     */
    private function databaseServerMoved(AgentJob $job, array $p): void
    {
        $server = DatabaseServer::whereKey($job->database_server_id)->where('status', DatabaseServer::MOVING)
            ->where('moving_to_server_id', $job->server_id)->first();
        if (! $server) {
            return;
        }
        $from = $server->server;
        $server->update([
            'server_id' => $job->server_id,
            'port' => (int) ($p['databaseServer']['port'] ?? $server->port),
            'status' => DatabaseServer::RUNNING,
            'moving_to_server_id' => null,
        ]);
        app(JobDispatcher::class)->dispatch($from, AgentAction::DeleteDatabaseServer, ['databaseServer' => $server->id, 'moved' => true],
            "Remove the old copy of database server {$server->name}", databaseServer: $server);
        Mesh::refreshLater();
    }

    /**
     * The new server could not receive or start a database server. The database server
     * operates on the old server: the old server starts it again when it could not send the
     * data, and this job starts it when the data went to the new server. The new server
     * removes what it received.
     *
     * @param  array<string, mixed>  $p
     */
    private function databaseServerNotMoved(AgentJob $job, array $p): void
    {
        $server = DatabaseServer::whereKey($job->database_server_id)->where('status', DatabaseServer::MOVING)
            ->where('moving_to_server_id', $job->server_id)->first();
        if (! $server) {
            return;
        }
        $server->update(['status' => DatabaseServer::RUNNING, 'moving_to_server_id' => null]);
        $jobs = app(JobDispatcher::class);
        $jobs->dispatch($server->server, AgentAction::SetDatabaseServer, ['databaseServer' => $server->forAgent()],
            "Start database server {$server->name} again", databaseServer: $server);
        $jobs->dispatch($job->server, AgentAction::DeleteDatabaseServer, ['databaseServer' => $server->id, 'moved' => true],
            "Remove the incomplete copy of database server {$server->name}", databaseServer: $server);
        Mesh::refreshLater();
    }

    /** The agent could not remove a database server. It is a member of its networks again. */
    private function databaseServerStays(AgentJob $job, array $p): void
    {
        DatabaseServer::whereKey($job->database_server_id)->where('status', DatabaseServer::REMOVING)
            ->update(['status' => in_array($p['previousStatus'] ?? '', [DatabaseServer::RUNNING, DatabaseServer::STOPPED, DatabaseServer::FAILED], true) ? $p['previousStatus'] : DatabaseServer::RUNNING]);
        Mesh::refreshLater();
    }

    /**
     * The database servers in the payload of a job that makes one when the agent does not have
     * it: SetDatabaseServer, CreateDatabase, SetDatabaseUser and a staging copy.
     *
     * After the job, the agent has each database server and it operates. When the job failed,
     * a database server that the panel made for the job is possibly not on the server: its
     * state is "failed" until a job, or a report of the agent, shows that it operates.
     */
    private function databaseServersOf(AgentJob $job, bool $succeeded): void
    {
        $p = $job->payload;
        $ids = [];
        foreach ([$p['databaseServer'] ?? null, ...(is_array($p['databaseServers'] ?? null) ? $p['databaseServers'] : [])] as $server) {
            if (is_array($server) && is_string($server['id'] ?? null)) {
                $ids[] = $server['id'];
            }
        }
        if (! $ids) {
            return;
        }
        DatabaseServer::whereIn('id', $ids)->where('server_id', $job->server_id)
            ->whereIn('status', $succeeded ? [DatabaseServer::STARTING, DatabaseServer::FAILED, DatabaseServer::STOPPED] : [DatabaseServer::STARTING])
            ->update(['status' => $succeeded ? DatabaseServer::RUNNING : DatabaseServer::FAILED]);
    }

    /**
     * SetDatabaseServer failed. A change of the limits has the plan and the limits from before
     * the change in "previous": the agent kept them, for example because the data does not
     * fit on a smaller disk. The panel records the same.
     *
     * @param  array<string, mixed>  $p
     */
    private function databaseServerKept(AgentJob $job, array $p): void
    {
        $previous = $p['previous'] ?? null;
        if (! is_array($previous) || ! is_array($previous['limits'] ?? null)) {
            return;
        }
        DatabaseServer::find($job->database_server_id)?->update(array_filter([
            'limits' => $previous['limits'],
            'plan_id' => is_string($previous['planId'] ?? null) ? $previous['planId'] : null,
        ], fn ($v) => $v !== null));
    }

    /** Records the result of a cron job that a user ran with "Run now". */
    private static function cronRan(AgentJob $job, int $exitCode, string $output): bool
    {
        CronJob::whereKey($job->payload['cronJob']['id'] ?? '')->where('website_id', $job->website_id)
            ->update(['last_run_at' => $job->started_at ?? now(), 'last_exit_code' => $exitCode, 'last_output' => Str::limit($output, 4000)]);

        return true;
    }

    private static function seconds(AgentJob $job): int
    {
        return (int) max(0, ($job->started_at ?? $job->created_at)->diffInSeconds(now()));
    }

    /**
     * The website serves the new release. The agent gives the commit of the release and the
     * releases that it removed from the server.
     *
     * @param  array<string, mixed>  $result
     */
    private static function deployed(AgentJob $job, Website $website, array $result): bool
    {
        $id = (string) ($job->payload['releaseId'] ?? '');
        $health = is_string($result['healthError'] ?? null) && $result['healthError'] !== '' ? Str::limit($result['healthError'], 2000) : null;
        $release = Release::whereKey($id)->where('website_id', $website->id)->first();
        $release?->update([
            'state' => Release::READY,
            // A release from a webhook or a check has the commit of the push. The agent gives the
            // commit that it got.
            'commit' => is_string($result['commit'] ?? null) && $result['commit'] !== '' ? substr($result['commit'], 0, 64) : $release->commit,
            'message' => is_string($result['message'] ?? null) ? Str::limit($result['message'], 200) : '',
            'duration_seconds' => self::seconds($job),
            'migration' => (bool) ($result['migrated'] ?? false),
            // FR-1009: the health check failed after the migration. The website kept the release.
            'health_error' => $health,
        ]);
        $removed = array_filter((array) ($result['removed'] ?? []), 'is_string');
        if ($removed) {
            $website->releases()->whereIn('id', $removed)->update(['state' => Release::REMOVED]);
        }

        return self::releaseLive($website, $id);
    }

    /** Records the release that the website serves, and the document root of that release. */
    private static function releaseLive(Website $website, string $releaseId): bool
    {
        $release = $website->releases()->whereKey($releaseId)->first();
        if (! $release) {
            return false;
        }
        $website->releases()->where('live', true)->update(['live' => false]);
        $release->update(['live' => true]);
        $website->update(['document_root' => $release->document_root]);

        return true;
    }

    /** Valkey: the agent gives the database number, which the application needs to connect. */
    private function databaseMade(Website $website, array $p, array $result): bool
    {
        if (($p['engine'] ?? '') === Database::VALKEY && is_int($result['number'] ?? null) && $result['number'] > 0) {
            $website->databases()->where(['engine' => Database::VALKEY, 'name' => $p['name'] ?? ''])->update(['number' => $result['number']]);
        }

        return true;
    }

    /** The user of a Valkey database goes with its database. The users of the other engines stay. */
    private function databaseRemoved(Website $website, array $p): bool
    {
        $engine = $p['engine'] ?? '';
        $website->databases()->where(['engine' => $engine, 'name' => $p['name'] ?? ''])->delete();
        if ($engine === Database::VALKEY) {
            $website->databaseUsers()->where(['engine' => $engine, 'name' => $p['name'] ?? ''])->delete();
        }

        return true;
    }

    /**
     * A job for a database or a database user on a different server can be a part of the
     * removal of its website. The panel then continues with the removal (WebsiteRemoval).
     */
    private function continueRemoval(Website $website, array $p): bool
    {
        if ($p[WebsiteRemoval::THEN] ?? false) {
            app(WebsiteRemoval::class)->continue($website);
        }

        return true;
    }

    private function removeNewDatabase(?Website $website, array $p): void
    {
        if (! $website) {
            return;
        }
        $website->databases()->where(['engine' => $p['engine'] ?? '', 'name' => $p['name'] ?? ''])->delete();
        if (isset($p['user'])) {
            $website->databaseUsers()->where(['engine' => $p['engine'] ?? '', 'name' => $p['user']])->delete();
        }
    }

    /**
     * Audit finding R8: the backups of a deleted website stay in the repository for a set time,
     * so that an administrator can restore them with restic. Then the weekly job removes them.
     */
    private function deleteWebsite(Website $website): bool
    {
        // The DNS zones of the customer lose the records that the panel made for the website.
        foreach ($website->domains()->pluck('name') as $name) {
            app(Zones::class)->websiteDomainRemoved($website->customer_id, $name);
        }
        // The backups of the databases on a different server are in the repository of that
        // server. Each server removes the backups in its own repository.
        $servers = $website->snapshots()->reorder()->whereNotNull('server_id')->distinct()->pluck('server_id')->push($website->server_id)->unique();
        foreach ($servers as $serverId) {
            BackupRemoval::updateOrCreate(['website_id' => $website->id, 'server_id' => $serverId], [
                'website_name' => $website->name,
                'remove_after' => now()->addDays(max(0, (int) config('cloudfactory.deleted_website_backup_days', 30))),
            ]);
        }

        return (bool) $website->delete();
    }

    private function recordSnapshot(AgentJob $job, Website $website, array $p, array $result): bool
    {
        // A backup with only databases is in the repository of a different server.
        $part = ($p['databasesOnly'] ?? false) ? Snapshot::DATABASES : Snapshot::WEBSITE;
        Snapshot::create([
            'website_id' => $website->id,
            'server_id' => $job->server_id,
            'part' => $part,
            'kind' => $p['kind'] ?? 'manual',
            'restic_id' => $result['snapshotId'] ?? null,
            'size_bytes' => (int) ($result['sizeBytes'] ?? 0),
            // The agent restores a database by "engine:name".
            // The backup has the databases of the job, not the databases that the website has now.
            'databases' => collect($p['databases'] ?? [])->map(fn ($d) => ($d['engine'] ?? '').':'.($d['name'] ?? ''))->values()->all(),
            'mailboxes' => [],
            // CF-06: the restore checks each dump against the hash from the backup.
            'dump_hashes' => self::dumpHashes($result['dumps'] ?? []),
        ]);
        // FR-902: the agent removed the snapshots that the retention policy does not keep.
        $forgotten = array_filter((array) ($result['forgotten'] ?? []), 'is_string');
        if ($forgotten) {
            // The IDs are from the repository of the server of the job.
            $website->snapshots()->where('part', $part)->where(fn ($q) => $q->where('server_id', $job->server_id)->orWhereNull('server_id'))->whereIn('restic_id', $forgotten)->delete();
        }

        return true;
    }

    /**
     * The mail server has the mailbox. A job that set the encryption gives what the mail
     * server found in the public key, and the panel then keeps the key.
     */
    private function mailboxSet(array $p, array $result): void
    {
        $mailbox = Mailbox::find($p['mailboxId'] ?? '');
        if (! $mailbox) {
            return;
        }
        $changes = $mailbox->status === MailDomain::PENDING ? ['status' => MailDomain::ACTIVE] : [];
        if ($p['setEncryption'] ?? false) {
            $key = is_string($p['publicKey'] ?? null) ? $p['publicKey'] : '';
            $changes += $key === '' ? ['public_key' => null, 'encryption' => null] : ['public_key' => $key, 'encryption' => Mail::encryption($result['encryption'] ?? null)];
        }
        $mailbox->update($changes);
    }

    /** The state of a mail record from before a removal that failed. */
    private static function mailStatus(array $p): string
    {
        return in_array($p['previousStatus'] ?? '', [MailDomain::ACTIVE, MailDomain::FAILED], true) ? $p['previousStatus'] : MailDomain::ACTIVE;
    }

    private static function withoutPasswords(array $payload): array
    {
        unset($payload['password'], $payload['passwordHash'], $payload['token'], $payload['wordpressValues']);
        // The token and the webhook secret of a plugin that the panel runs.
        if (is_array($payload['website'] ?? null)) {
            unset($payload['website']['environment']);
        }
        // The DNS servers have the DNSSEC keys now. The zone keeps its key.
        if (is_array($payload['zones'] ?? null)) {
            $payload['zones'] = array_map(fn ($z) => is_array($z) ? array_diff_key($z, ['dnssecKey' => true]) : $z, $payload['zones']);
        }
        if (isset($payload['copyUsers'])) {
            $payload['copyUsers'] = array_map(fn ($u) => array_diff_key($u, ['password' => true]), $payload['copyUsers']);
        }

        return $payload;
    }

    /** @return array<string, string> "engine:name" => SHA-256 */
    private static function dumpHashes(mixed $dumps): array
    {
        if (! is_array($dumps)) {
            return [];
        }

        return array_filter($dumps, fn ($hash, $key) => is_string($key) && is_string($hash) && preg_match('/^[0-9a-f]{64}$/', $hash), ARRAY_FILTER_USE_BOTH);
    }
}
