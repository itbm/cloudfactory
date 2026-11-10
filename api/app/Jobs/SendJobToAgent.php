<?php

namespace App\Jobs;

use App\Enums\JobState;
use App\Models\AgentJob;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Support\Facades\Log;
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
        // Audit finding R11: the panel does not call a server that is offline. Each call would
        // hold the queue worker until the connection time limit, and the jobs of the other
        // servers would wait. The job stays in the queue of the panel, and the panel sends it
        // when the server reports again (AgentController::report).
        if ($job->server->state === 'offline') {
            return;
        }
        $client->send($job);
        // Audit finding R1: a fast job can be complete before this line runs. Only a job that
        // is still queued becomes "running".
        app(JobOutcome::class)->started($job);
    }

    public function failed(Throwable $e): void
    {
        $job = AgentJob::find($this->jobId);
        // The agent accepted this job before. The panel sent it again because it had no news.
        // The job stays open until the agent reports, or until it is 3 hours old.
        if ($job && $job->resent_at !== null && $job->state === JobState::Queued) {
            app(JobOutcome::class)->started($job);

            return;
        }
        if ($job) {
            // Audit finding S9: the full error has the address of the agent. It goes to the log
            // of the panel, and the job gets a short text that a customer can read.
            Log::warning('The agent is not available.', ['job' => $job->id, 'server' => $job->server_id, 'error' => $e->getMessage()]);
            app(JobOutcome::class)->finish($job, JobState::Failed, 'The agent of the server did not answer. The administrator can find the cause in the log of the panel.');
        }
    }
}
