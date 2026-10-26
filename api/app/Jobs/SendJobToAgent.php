<?php

namespace App\Jobs;

use App\Enums\JobState;
use App\Models\AgentJob;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Throwable;

/** Sends one job to its agent. The queue tries again if the agent is not available. */
class SendJobToAgent implements ShouldQueue
{
    use Queueable;

    public int $tries = 5;

    public function __construct(public string $jobId) {}

    /** @return list<int> */
    public function backoff(): array
    {
        return [5, 15, 60, 300];
    }

    public function handle(AgentClient $client): void
    {
        $job = AgentJob::with('server')->find($this->jobId);
        if (! $job || $job->state !== JobState::Queued) {
            return;
        }
        $client->send($job);
        $job->update(['state' => JobState::Running, 'started_at' => now()]);
    }

    public function failed(Throwable $e): void
    {
        $job = AgentJob::find($this->jobId);
        if ($job) {
            app(JobOutcome::class)->finish($job, JobState::Failed, 'The agent is not available: '.$e->getMessage());
        }
    }
}
