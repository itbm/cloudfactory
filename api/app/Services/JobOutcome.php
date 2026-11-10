<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\BackupRemoval;
use App\Models\Domain;
use App\Models\Rollout;
use App\Models\Snapshot;
use App\Models\Website;
use Illuminate\Support\Facades\DB;

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
        $changed = self::changeOpen($job, fn (AgentJob $j) => [
            'state' => $state,
            // The agent has the passwords now. The panel does not keep them.
            'payload' => self::withoutPasswords($j->payload),
            'step' => $state === JobState::Succeeded ? count($j->steps) : $j->step,
            'error' => $error,
            'finished_at' => now(),
        ]);
        if (! $changed) {
            return;
        }

        $website = $job->website_id ? Website::find($job->website_id) : null;
        $state === JobState::Succeeded
            ? $this->applySuccess($job, $website, $result)
            : $this->applyFailure($job, $website);

        if ($job->rollout_id && ($rollout = Rollout::find($job->rollout_id))) {
            $this->rollouts->jobFinished($rollout, $job);
        }
    }

    private function applySuccess(AgentJob $job, ?Website $website, array $result): void
    {
        $p = $job->payload;
        match ($job->action) {
            AgentAction::CreateWebsite => $website?->update(['status' => WebsiteStatus::Running]),
            AgentAction::SetWebsitePower => $website?->update([
                'status' => ($p['power'] ?? '') === 'stop' ? WebsiteStatus::Stopped : WebsiteStatus::Running,
            ]),
            AgentAction::DeleteWebsite => $website && $this->deleteWebsite($website),
            // Audit finding R8: the backups of these deleted websites are gone.
            AgentAction::PruneBackups => BackupRemoval::whereIn('website_id', array_filter((array) ($p['forgetWebsiteIds'] ?? []), 'is_string'))->delete(),
            AgentAction::SetSftp => $website?->update([
                'sftp_enabled' => (bool) ($p['enabled'] ?? false),
                'sftp_keys' => $p['authorizedKeys'] ?? [],
            ]),
            AgentAction::ConfigureHost => $job->server->update(['gvisor_platform' => $p['gvisorPlatform'] ?? $job->server->gvisor_platform]),
            AgentAction::AddDomain => Domain::where('name', $p['domain'] ?? '')->update([
                'dns_points_here' => (bool) ($result['dnsPointsHere'] ?? true),
                'certificate_state' => ($result['dnsPointsHere'] ?? true) ? 'valid' : 'pending',
                'certificate_expires_at' => ($result['dnsPointsHere'] ?? true) ? now()->addDays(90) : null,
            ]),
            AgentAction::Backup => $website && $this->recordSnapshot($website, $p, $result),
            // Batch 1, finding 4: the panel removes the row when the item is gone from the server.
            AgentAction::DeleteDatabase => $website?->databases()->where(['engine' => $p['engine'] ?? '', 'name' => $p['name'] ?? ''])->delete(),
            AgentAction::DeleteDatabaseUser => $website?->databaseUsers()->where(['engine' => $p['engine'] ?? '', 'name' => $p['name'] ?? ''])->delete(),
            AgentAction::InstallHostUpdates => $job->server->update([
                'pending_updates' => [],
                'last_update_at' => now(),
                'reboot_required' => (bool) ($result['rebootRequired'] ?? $job->server->reboot_required),
            ]),
            AgentAction::Reboot => $job->server->update(['state' => 'healthy', 'reboot_required' => false]),
            AgentAction::UpdateAgent => $job->server->update([
                'agent_version' => $result['version'] ?? config('cloudfactory.agent.latest_version'),
                'state' => 'healthy',
            ]),
            default => null,
        };
    }

    private function applyFailure(AgentJob $job, ?Website $website): void
    {
        $p = $job->payload;
        match ($job->action) {
            AgentAction::CreateWebsite => $website?->update(['status' => WebsiteStatus::Failed]),
            // FR-1105: the agent went back to the previous image. The panel records the same.
            // Batch 1, finding 3: a change of the PHP settings has the old settings in "revert".
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
            AgentAction::SetWebsitePower => $website?->update(['status' => ($p['power'] ?? '') === 'start' ? WebsiteStatus::Stopped : WebsiteStatus::Running]),
            AgentAction::Reboot => $job->server->update(['state' => 'degraded']),
            default => null,
        };
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
        BackupRemoval::updateOrCreate(['website_id' => $website->id], [
            'website_name' => $website->name,
            'remove_after' => now()->addDays(max(0, (int) config('cloudfactory.deleted_website_backup_days', 30))),
        ]);

        return (bool) $website->delete();
    }

    private function recordSnapshot(Website $website, array $p, array $result): bool
    {
        Snapshot::create([
            'website_id' => $website->id,
            'kind' => $p['kind'] ?? 'manual',
            'restic_id' => $result['snapshotId'] ?? null,
            'size_bytes' => (int) ($result['sizeBytes'] ?? 0),
            // The agent restores a database by "engine:name".
            'databases' => $website->databases()->get()->map(fn ($d) => "{$d->engine}:{$d->name}")->all(),
            'mailboxes' => [],
            // CF-06: the restore checks each dump against the hash from the backup.
            'dump_hashes' => self::dumpHashes($result['dumps'] ?? []),
        ]);
        // FR-902: the agent removed the snapshots that the retention policy does not keep.
        $forgotten = array_filter((array) ($result['forgotten'] ?? []), 'is_string');
        if ($forgotten) {
            $website->snapshots()->whereIn('restic_id', $forgotten)->delete();
        }

        return true;
    }

    private static function withoutPasswords(array $payload): array
    {
        unset($payload['password']);
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
