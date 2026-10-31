<?php

namespace App\Console\Commands;

use App\Enums\JobState;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Server;
use App\Services\JobOutcome;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Batch 1, finding 2: the panel does not wait for ever for an agent.
 *
 * - A server that did not report for 5 minutes is "offline". Its next report makes it healthy.
 * - A job with no event for 10 minutes goes to the agent again. The agent knows the job ID. It
 *   reports the same result again, or runs an interrupted job again.
 * - A job with no result after 3 hours fails, so that a rollout or a reboot can continue.
 *
 * The scheduler runs this command each minute.
 */
#[Signature('cloudfactory:check-jobs')]
#[Description('Mark silent servers offline, and send or fail old jobs')]
class CheckJobsAndServers extends Command
{
    public function handle(JobOutcome $outcome): int
    {
        $offline = Server::whereIn('state', ['healthy', 'degraded'])->where('last_seen_at', '<', now()->subMinutes(5))->update(['state' => 'offline']);
        if ($offline) {
            $this->line("{$offline} servers are offline.");
        }

        $open = AgentJob::whereIn('state', [JobState::Queued->value, JobState::Running->value]);
        foreach ((clone $open)->where('created_at', '<', now()->subHours(3))->get() as $job) {
            $outcome->finish($job, JobState::Failed, 'The agent did not report the result in 3 hours.');
            $this->line("Failed job {$job->id}.");
        }
        $stale = (clone $open)->where('updated_at', '<', now()->subMinutes(10))
            ->where(fn ($q) => $q->whereNull('resent_at')->orWhere('resent_at', '<', now()->subMinutes(10)))->get();
        foreach ($stale as $job) {
            // SendJobToAgent sends only a queued job.
            $job->update(['state' => JobState::Queued, 'resent_at' => now()]);
            SendJobToAgent::dispatch($job->id);
            $this->line("Sent job {$job->id} again.");
        }

        return self::SUCCESS;
    }
}
