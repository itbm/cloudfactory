<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Rollout;
use App\Models\Website;

/**
 * FR-1103 to FR-1107: update an image in stages (canary, 10%, 50%, then all websites).
 * A website that fails its health check goes back to the previous image. A stage starts
 * only when all websites of the previous stage are complete.
 */
class RolloutEngine
{
    public function __construct(private JobDispatcher $dispatcher) {}

    public function start(Image $image): Rollout
    {
        $ids = Website::where('image_id', $image->id)
            ->where('status', '!=', 'stopped')
            ->orderBy('created_at')
            ->pluck('id')
            ->all();

        $rollout = Rollout::create([
            'image_id' => $image->id,
            'from_tag' => $image->tag,
            'to_tag' => $image->available_tag,
            'state' => 'running',
            'stage' => 0,
            'website_ids' => $ids,
            'updated_ids' => [],
            'rolled_back' => [],
        ]);
        $this->startStage($rollout);

        return $rollout;
    }

    public function pause(Rollout $rollout): void
    {
        $rollout->update(['state' => 'paused']);
    }

    public function resume(Rollout $rollout): void
    {
        $rollout->update(['state' => 'running']);
        if (! $this->stageBusy($rollout)) {
            $this->advance($rollout);
        }
    }

    /** FR-1107: put all updated websites back on the previous image. */
    public function rollBack(Rollout $rollout): void
    {
        $rollout->update(['state' => 'rolled_back']);

        // Jobs that the agent has not started do not run. The website stays on the previous tag.
        $queued = AgentJob::where('rollout_id', $rollout->id)->where('state', JobState::Queued->value)->get();
        foreach ($queued as $job) {
            $job->update(['state' => JobState::Failed, 'error' => 'Stopped by the roll back.', 'finished_at' => now()]);
        }
        Website::whereIn('id', $queued->pluck('website_id'))->update(['image_tag' => $rollout->from_tag]);

        foreach (Website::whereIn('id', $rollout->updated_ids)->get() as $website) {
            $this->revertWebsite($rollout, $website);
        }
    }

    private function revertWebsite(Rollout $rollout, Website $website): void
    {
        $website->update(['image_tag' => $rollout->from_tag]);
        $this->dispatcher->dispatch(
            $website->server,
            AgentAction::SetRuntime,
            JobDispatcher::websitePayload($website),
            "Roll back {$rollout->image->name} to {$rollout->from_tag}",
            $website,
            $rollout->from_tag,
        );
    }

    public function jobFinished(Rollout $rollout, AgentJob $job): void
    {
        $rollout->refresh();
        // A job that was running during a roll back can still succeed. Put that website back too.
        if ($rollout->state === 'rolled_back') {
            if ($job->state === JobState::Succeeded && ($website = Website::find($job->website_id))) {
                $this->revertWebsite($rollout, $website);
            }

            return;
        }
        if ($job->state === JobState::Succeeded) {
            $rollout->updated_ids = array_values(array_unique([...$rollout->updated_ids, $job->website_id]));
        } else {
            $rollout->rolled_back = [...$rollout->rolled_back, ['websiteId' => $job->website_id, 'reason' => $job->error ?? 'The health check failed.']];
        }
        $rollout->save();

        if ($rollout->state === 'running' && ! $this->stageBusy($rollout)) {
            $this->advance($rollout);
        }
    }

    private function stageBusy(Rollout $rollout): bool
    {
        return AgentJob::where('rollout_id', $rollout->id)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])
            ->exists();
    }

    private function advance(Rollout $rollout): void
    {
        // A canary that fails stops the rollout. An administrator then decides.
        if ($rollout->stage === 0 && count($rollout->rolled_back) > 0) {
            $rollout->update(['state' => 'paused']);

            return;
        }
        if ($rollout->stage >= count(Rollout::STAGES) - 1) {
            $rollout->update(['state' => 'done']);
            $rollout->image->update([
                'tag' => $rollout->to_tag,
                'digest' => $rollout->image->available_digest ?? $rollout->image->digest,
                'available_tag' => null,
                'available_digest' => null,
            ]);
            Website::whereIn('id', $rollout->updated_ids)->update(['image_tag' => null]);

            return;
        }
        $rollout->update(['stage' => $rollout->stage + 1]);
        $this->startStage($rollout);
    }

    private function startStage(Rollout $rollout): void
    {
        $done = count($rollout->updated_ids) + count($rollout->rolled_back);
        $target = $rollout->stageTarget($rollout->stage);
        $handled = [...$rollout->updated_ids, ...array_column($rollout->rolled_back, 'websiteId')];
        $next = array_slice(array_values(array_diff($rollout->website_ids, $handled)), 0, max(0, $target - $done));

        if ($next === []) {
            $this->advance($rollout);

            return;
        }

        $image = $rollout->image;
        foreach (Website::whereIn('id', $next)->get() as $website) {
            $payload = JobDispatcher::websitePayload($website);
            $payload['website']['image'] = $image->reference.':'.$rollout->to_tag;
            $payload['website']['imageDigest'] = $image->available_digest;
            $payload['previousImageId'] = $image->id;
            $payload['previousTag'] = $website->currentTag();
            $payload['healthCheck'] = ['status' => 200, 'maxErrorRate' => 0.01, 'seconds' => 300];
            $website->update(['image_tag' => $rollout->to_tag]);
            $this->dispatcher->dispatch($website->server, AgentAction::SetRuntime, $payload, "Update {$image->name} to {$rollout->to_tag}", $website, $rollout->to_tag, $rollout->id);
        }
    }
}
