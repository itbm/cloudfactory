<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Image;
use App\Models\Rollout;
use App\Services\JobOutcome;
use Tests\TestCase;

class RolloutTest extends TestCase
{
    private Image $image;

    protected function setUp(): void
    {
        parent::setUp();
        $customer = $this->customer();
        $server = $this->server();
        $this->image = $this->image(['available_tag' => '8.4.0-2', 'available_digest' => 'sha256:'.str_repeat('a', 64)]);
        foreach (range(1, 20) as $i) {
            $this->website($customer, $server, $this->image, "site{$i}.example");
        }
        $this->actingWith($this->user());
    }

    /** Finish all open jobs of the rollout with the same result. */
    private function finishOpenJobs(Rollout $rollout, JobState $state): int
    {
        $jobs = AgentJob::where('rollout_id', $rollout->id)->where('state', 'queued')->get();
        foreach ($jobs as $job) {
            app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'The health check failed.' : null);
        }

        return $jobs->count();
    }

    public function test_a_rollout_goes_through_the_stages(): void
    {
        $id = $this->postJson('/api/v1/rollouts', ['imageId' => $this->image->id])->assertCreated()->json('id');
        $rollout = Rollout::findOrFail($id);

        $this->assertSame(1, $this->finishOpenJobs($rollout, JobState::Succeeded)); // canary
        $this->assertSame(1, $this->finishOpenJobs($rollout, JobState::Succeeded)); // 10% = 2 websites
        $this->assertSame(8, $this->finishOpenJobs($rollout, JobState::Succeeded)); // 50% = 10 websites
        $this->assertSame(10, $this->finishOpenJobs($rollout, JobState::Succeeded)); // everyone

        $this->getJson("/api/v1/rollouts/{$id}")->assertJsonPath('state', 'done')->assertJsonPath('healthy', 20);
        $this->assertSame('8.4.0-2', $this->image->fresh()->tag);
        $this->assertNull($this->image->fresh()->available_tag);
    }

    public function test_a_failed_canary_pauses_the_rollout(): void
    {
        $id = $this->postJson('/api/v1/rollouts', ['imageId' => $this->image->id])->json('id');

        $this->finishOpenJobs(Rollout::findOrFail($id), JobState::Failed);

        $this->getJson("/api/v1/rollouts/{$id}")->assertJsonPath('state', 'paused')->assertJsonPath('rolledBack', 1)
            ->assertJsonPath('rollbacks.0.reason', 'The health check failed.');
    }

    public function test_only_one_rollout_runs_at_a_time(): void
    {
        $this->postJson('/api/v1/rollouts', ['imageId' => $this->image->id])->assertCreated();
        $this->postJson('/api/v1/rollouts', ['imageId' => $this->image->id])->assertJsonValidationErrors('imageId');
    }

    public function test_an_image_without_a_verified_signature_is_not_rolled_out(): void
    {
        $this->image->update(['signature_verified' => false]);

        $this->postJson('/api/v1/rollouts', ['imageId' => $this->image->id])->assertJsonValidationErrors(['imageId' => 'The new image has no verified signature.']);
    }

    public function test_roll_back_puts_updated_websites_on_the_previous_tag(): void
    {
        $id = $this->postJson('/api/v1/rollouts', ['imageId' => $this->image->id])->json('id');
        $rollout = Rollout::findOrFail($id);
        $this->finishOpenJobs($rollout, JobState::Succeeded);
        $this->postJson("/api/v1/rollouts/{$id}/pause")->assertJsonPath('state', 'paused');

        $this->postJson("/api/v1/rollouts/{$id}/roll-back")->assertJsonPath('state', 'rolled_back');
        $this->assertSame(0, $this->image->websites()->where('image_tag', '8.4.0-2')->count());
    }
}
