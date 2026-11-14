<?php

namespace App\Jobs;

use App\Enums\JobState;
use App\Models\AgentJob;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\RequestException;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Sends one job to its agent. The queue tries again if the agent is not available. A job that
 * the agent refuses fails immediately, because a second attempt gets the same answer.
 */
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
        try {
            $client->send($job);
        } catch (ConnectionException $e) {
            // The server page shows the reason to the administrator at the first failure. The
            // queue tries again, and the job fails only after the last attempt.
            AgentClient::record($job->server, AgentClient::reason($e));
            throw $e;
        } catch (RequestException $e) {
            if (! self::refused($e->response->status())) {
                // A temporary error, for example "unavailable" (HTTP 503). The queue tries again.
                throw $e;
            }
            // Audit finding F04: the agent got the request and refused it. The connection is
            // good, and the job fails now with the message of the agent.
            AgentClient::record($job->server, null);
            Log::warning('The agent refused a job.', ['job' => $job->id, 'server' => $job->server_id, 'status' => $e->response->status(), 'body' => mb_substr($e->response->body(), 0, 2000)]);
            app(JobOutcome::class)->finish($job, JobState::Failed, self::refusal($e));

            return;
        }
        if ($job->server->connection_error !== null || $job->server->connection_checked_at === null) {
            AgentClient::record($job->server, null);
        }
        // Audit finding R1: a fast job can be complete before this line runs. Only a job that
        // is still queued becomes "running".
        app(JobOutcome::class)->started($job);
    }

    /**
     * True for an answer that does not change on a second attempt: the agent refuses the request
     * (HTTP 4xx). "Request timeout" (408) and "too many requests" (429) are temporary.
     */
    public static function refused(int $status): bool
    {
        return $status >= 400 && $status < 500 && ! in_array($status, [408, 429], true);
    }

    /**
     * The text of a refused job. The agent gives a message with the value that it refuses, for
     * example: the PHP extension "x" is not valid. The message has no secrets.
     */
    private static function refusal(RequestException $e): string
    {
        $message = $e->response->json('message');
        $message = is_string($message) ? trim(mb_substr($message, 0, 500)) : '';

        return $message !== ''
            ? "The server refused the job: {$message}"
            : "The server refused the job (HTTP status {$e->response->status()}). The administrator can find the cause in the log of the panel.";
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
            app(JobOutcome::class)->finish($job, JobState::Failed, 'The agent of the server did not answer. The administrator can find the cause on the page of the server and in the log of the panel.');
        }
    }
}
