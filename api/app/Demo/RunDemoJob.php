<?php

namespace App\Demo;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Website;
use App\Services\JobOutcome;
use App\Services\Mesh;
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
 * - a new database with a name that ends with "_fail";
 * - a deploy from a repository with "fail" in its address;
 * - a cron job with "fail" in its command;
 * - a DNS zone with a name that starts with "fail.".
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
            $job->action === AgentAction::SetDnsZones && collect($p['zones'] ?? [])->contains(fn ($z) => str_starts_with((string) ($z['name'] ?? ''), 'fail.')) => [
                'step' => 2,
                'error' => 'knotc -b zone-reload '.collect($p['zones'])->pluck('name')->first(fn ($n) => str_starts_with((string) $n, 'fail.')).': exit status 1: error: (operation timed out)',
            ],
            $job->action === AgentAction::Deploy && str_contains((string) ($p['repository'] ?? ''), 'fail') => [
                'step' => 1,
                'error' => "a build command failed: exit status 2: Installing dependencies from lock file\nYour lock file does not contain a compatible set of packages. Please run composer update.",
            ],
            $job->action === AgentAction::RunCronJob && str_contains((string) ($p['cronJob']['command'] ?? ''), 'fail') => [
                'step' => 0,
                'error' => 'the command ended with exit code 1: PHP Fatal error:  Uncaught RuntimeException: The export did not complete in /app/src/export.php:18',
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
            AgentAction::RunCronJob => ['cronJobId' => $p['cronJob']['id'] ?? '', 'exitCode' => 0, 'output' => "Running scheduled tasks.\nDone in 0.4 s.", 'ranAt' => now()->toIso8601String()],
            AgentAction::Deploy => [
                'releaseId' => $p['releaseId'] ?? '',
                'commit' => sha1((string) ($p['releaseId'] ?? '')),
                'message' => 'Update the '.(['home page', 'checkout form', 'product list', 'contact page'][crc32((string) ($p['releaseId'] ?? '')) % 4]),
                'removed' => [],
                'documentRoot' => $p['website']['documentRoot'] ?? '',
            ],
            AgentAction::UpdateAgent => ['version' => config('cloudfactory.agent.latest_version')],
            // Each peer answers through its tunnel.
            AgentAction::SetMesh => ['mesh' => [
                'publicKey' => $job->server->mesh_public_key,
                'version' => $job->server->mesh_wanted_version,
                'address' => $job->server->mesh_address,
                'networks' => true,
                'websites' => true,
                'moves' => true,
                'tunnels' => array_map(fn (array $peer) => [
                    'server' => $peer['server'], 'handshakeAt' => now()->getTimestamp(), 'reachable' => true, 'roundTripMicros' => random_int(4000, 24000),
                ], Mesh::stateFor($job->server)['peers']),
            ]],
            AgentAction::RotateMeshKey => ['publicKey' => base64_encode(random_bytes(32))],
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
