<?php

namespace App\Demo;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Website;
use App\Services\JobOutcome;
use App\Services\PanelCertificate;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * The demo agent: it reports each step of a job, and then the result, through the same
 * JobOutcome class that the reports of a real agent use.
 *
 * To show a failure, the demo agent fails these jobs:
 * - a new website or a new domain with a name that starts with "fail.", for example
 *   "fail.example.com";
 * - a new database with a name that ends with "_fail".
 */
class RunDemoJob implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public int $timeout = 120;

    public function __construct(public string $jobId) {}

    public function handle(JobOutcome $outcome): void
    {
        $job = AgentJob::with('server')->find($this->jobId);
        if (! $job || $job->state->finished()) {
            return;
        }
        $outcome->started($job);
        $failure = self::failure($job);
        // With the queue "sync", the job runs in the request of the user, so it does not wait.
        $pause = config('queue.default') === 'sync' ? 0 : max(0, (int) config('demo.step_ms')) * 1000;

        foreach (array_keys($job->steps) as $index) {
            if ($pause > 0) {
                usleep($pause);
            }
            if ($failure !== null && $index === $failure['step']) {
                $outcome->finish($job, JobState::Failed, $failure['error']);

                return;
            }
            $outcome->progress($job, $index + 1);
        }
        $outcome->finish($job, JobState::Succeeded, null, self::result($job));
        self::afterSuccess($job);
    }

    /**
     * The step that fails and the error text, or null. The texts are examples of the errors
     * of a real agent.
     *
     * @return array{step: int, error: string}|null
     */
    public static function failure(AgentJob $job): ?array
    {
        $p = $job->payload;

        return match (true) {
            $job->action === AgentAction::CreateWebsite && str_starts_with((string) ($p['website']['domains'][0]['name'] ?? ''), 'fail.') => [
                'step' => 2,
                'error' => 'podman pull '.($p['website']['image'] ?? 'the image').': the registry gave status 503 (service unavailable)',
            ],
            $job->action === AgentAction::AddDomain && str_starts_with((string) ($p['domain'] ?? ''), 'fail.') => [
                'step' => 2,
                'error' => 'The certificate authority could not connect to the domain: no A or AAAA record points to this server.',
            ],
            $job->action === AgentAction::CreateDatabase && str_ends_with((string) ($p['name'] ?? ''), '_fail') => [
                'step' => 0,
                'error' => 'runuser -u cf-db -- podman exec cf-mariadb mariadb --execute "CREATE DATABASE": exit status 1: ERROR 2002 (HY000): Can\'t connect to local server through socket \'/run/mysqld/mysqld.sock\' (2)',
            ],
            default => null,
        };
    }

    /**
     * The result that a real agent sends with a completed job.
     *
     * @return array<string, mixed>
     */
    public static function result(AgentJob $job): array
    {
        $p = $job->payload;

        return match ($job->action) {
            AgentAction::CreateWebsite, AgentAction::AddDomain => ['dnsPointsHere' => true],
            AgentAction::CreateDatabase => ($p['engine'] ?? '') === Database::VALKEY ? ['number' => self::freeValkeyNumber($job)] : [],
            AgentAction::Backup => ['snapshotId' => bin2hex(random_bytes(32)), 'sizeBytes' => random_int(40, 900) * 1024 ** 2, 'dumps' => [], 'forgotten' => []],
            AgentAction::InstallHostUpdates => ['rebootRequired' => false],
            AgentAction::UpdateAgent => ['version' => config('cloudfactory.agent.latest_version')],
            AgentAction::SetPanelCertificate => ['panelCertificate' => [
                ...array_intersect_key(PanelCertificate::current(), array_flip(['domain', 'caCertificate'])),
                'method' => $p['method'] ?? 'internal',
                'state' => 'valid',
                'expiresAt' => now()->addDays(90)->toIso8601String(),
            ]],
            default => [],
        };
    }

    /** No real agent reports the use of a new website, so the demo agent gives it some. */
    private static function afterSuccess(AgentJob $job): void
    {
        if ($job->action === AgentAction::CreateWebsite && $job->website_id) {
            Website::whereKey($job->website_id)->update(['usage' => json_encode([
                'cpuPercent' => 2, 'memoryBytes' => 96 * 1024 ** 2, 'diskBytes' => 48 * 1024 ** 2, 'processes' => 6,
            ])]);
        }
    }

    /** The first Valkey database number that no website on the server of the job uses. */
    private static function freeValkeyNumber(AgentJob $job): int
    {
        $used = Database::where('engine', Database::VALKEY)
            ->whereIn('website_id', Website::where('server_id', $job->server_id)->select('id'))
            ->whereNotNull('number')->pluck('number')->all();
        for ($n = 1; in_array($n, $used, true); $n++) {
        }

        return $n;
    }
}
