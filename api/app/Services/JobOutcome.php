<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\Domain;
use App\Models\Rollout;
use App\Models\Snapshot;
use App\Models\Website;

/**
 * Spec section 6.3, step 7: the agent reports the result and the actual state.
 * This class changes the panel state to agree with the result.
 */
class JobOutcome
{
    public function __construct(private RolloutEngine $rollouts) {}

    public function progress(AgentJob $job, int $step): void
    {
        $job->update([
            'state' => JobState::Running,
            'step' => min($step, count($job->steps)),
            'started_at' => $job->started_at ?? now(),
        ]);
    }

    /** @param array<string, mixed> $result */
    public function finish(AgentJob $job, JobState $state, ?string $error = null, array $result = []): void
    {
        if ($job->state->finished()) {
            return;
        }
        $job->update([
            'state' => $state,
            // The agent has the password now. The panel does not keep it.
            'payload' => array_diff_key($job->payload, ['password' => true]),
            'step' => $state === JobState::Succeeded ? count($job->steps) : $job->step,
            'error' => $error,
            'finished_at' => now(),
        ]);

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
            AgentAction::DeleteWebsite => $website?->delete(),
            AgentAction::AddDomain => Domain::where('name', $p['domain'] ?? '')->update([
                'dns_points_here' => (bool) ($result['dnsPointsHere'] ?? true),
                'certificate_state' => ($result['dnsPointsHere'] ?? true) ? 'valid' : 'pending',
                'certificate_expires_at' => ($result['dnsPointsHere'] ?? true) ? now()->addDays(90) : null,
            ]),
            AgentAction::Backup => $website && Snapshot::create([
                'website_id' => $website->id,
                'kind' => $p['kind'] ?? 'manual',
                'restic_id' => $result['snapshotId'] ?? null,
                'size_bytes' => (int) ($result['sizeBytes'] ?? 0),
                // The agent restores a database by "engine:name".
                'databases' => $website->databases()->get()->map(fn ($d) => "{$d->engine}:{$d->name}")->all(),
                'mailboxes' => [],
            ]),
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
        match ($job->action) {
            AgentAction::CreateWebsite => $website?->update(['status' => WebsiteStatus::Failed]),
            // FR-1105: the agent went back to the previous image. The panel records the same.
            AgentAction::SetRuntime => $website?->update([
                'image_id' => $job->payload['previousImageId'] ?? $website->image_id,
                'image_tag' => $job->payload['previousTag'] ?? $website->image_tag,
            ]),
            AgentAction::Reboot => $job->server->update(['state' => 'degraded']),
            default => null,
        };
    }
}
