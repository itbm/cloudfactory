<?php

namespace App\Demo;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Import;
use App\Models\Mailbox;
use App\Models\MailboxSnapshot;
use App\Models\Pack;
use App\Models\Website;
use App\Services\Certificates\Certificates;
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
 * - a new mailbox, alias or forwarder with the name "fail", for example "fail@example.com";
 * - a public key for a mailbox that has the text "FAIL";
 * - a deploy from a repository with "fail" in its address;
 * - a deploy from a repository with "unhealthy" in its address: the health check after the
 *   switch fails. With a migration command, the job succeeds, and the website keeps the release;
 * - a cron job with "fail" in its command;
 * - each web terminal, because the demonstration has no terminal relay (config demo.terminal);
 * - an install of dependencies with "fail" in its install command;
 * - a DNS zone with a name that starts with "fail.";
 * - an import with "fail" in the name of its file or in its address.
 */
class RunDemoJob implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    /** The error of a health check after the switch that fails. */
    private const UNHEALTHY = 'the health check failed after the switch: 9 of 30 checks failed, last error: status 500';

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
        $outcome->finish($job, JobState::Succeeded, null, self::result($job) + self::filesResult($job));
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
            $job->action === AgentAction::SetMailbox && str_contains((string) ($p['publicKey'] ?? ''), 'FAIL') => [
                'step' => 1,
                'error' => 'the mail server refused the public key: The key contains no key usable for encryption.',
            ],
            in_array($job->action, [AgentAction::SetMailbox, AgentAction::SetMailAlias], true) && ($p['create'] ?? false) && ($p['localPart'] ?? '') === 'fail' => [
                'step' => 0,
                'error' => 'The mail server refused the address fail@'.($p['domain'] ?? 'the domain').': it did not answer on 169.254.77.1:19025 in 30 seconds.',
            ],
            $job->action === AgentAction::SetDnsZones && collect($p['zones'] ?? [])->contains(fn ($z) => str_starts_with((string) ($z['name'] ?? ''), 'fail.')) => [
                'step' => 2,
                'error' => 'knotc -b zone-reload '.collect($p['zones'])->pluck('name')->first(fn ($n) => str_starts_with((string) $n, 'fail.')).': exit status 1: error: (operation timed out)',
            ],
            $job->action === AgentAction::Deploy && str_contains((string) ($p['repository'] ?? ''), 'unhealthy') && ($p['migrationCommand'] ?? '') === '' => [
                'step' => array_search('Switch traffic', AgentAction::Deploy->steps(), true),
                'error' => self::UNHEALTHY,
            ],
            $job->action === AgentAction::Deploy && str_contains((string) ($p['repository'] ?? ''), 'fail') => [
                'step' => 1,
                'error' => "a build command failed: exit status 2: Installing dependencies from lock file\nYour lock file does not contain a compatible set of packages. Please run composer update.",
            ],
            $job->action === AgentAction::InstallDependencies && str_contains((string) ($p['command'] ?? ''), 'fail') => [
                'step' => 0,
                'error' => "the install command failed: exit status 1: npm error code EUSAGE\nnpm error The `npm ci` command can only install with an existing package-lock.json.",
            ],
            // An update of a plugin with "fail" in its name breaks the site. The agent puts back the restore point.
            $job->action === AgentAction::UpdateWordPress && collect($p['plugins'] ?? [])->contains(fn ($n) => str_contains((string) $n, 'fail')) => [
                'step' => 5,
                'error' => 'WordPress does not load: wp eval: PHP Fatal error:  Uncaught Error: Call to undefined function wc_get_order(); the agent put back the restore point',
            ],
            $job->action === AgentAction::OpenTerminal && ! config('demo.terminal') => [
                'step' => 0,
                'error' => 'The demonstration has no terminal relay, so the terminal cannot open.',
            ],
            $job->action === AgentAction::RunCronJob && str_contains((string) ($p['cronJob']['command'] ?? ''), 'fail') => [
                'step' => 0,
                'error' => 'the command ended with exit code 1: PHP Fatal error:  Uncaught RuntimeException: The export did not complete in /app/src/export.php:18',
            ],
            $job->action === AgentAction::FetchImport && str_contains(self::importName($job), 'fail') => [
                'step' => 1,
                'error' => 'the archive is not a cPanel, DirectAdmin, Plesk or HestiaCP account backup',
            ],
            default => null,
        };
    }

    /** The name of the file or the address of the import of a job. */
    private static function importName(AgentJob $job): string
    {
        $import = Import::find($job->payload['importId'] ?? null);

        return strtolower((string) ($import?->source === Import::URL ? $import->url : $import?->file_name));
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
            AgentAction::ReceiveWebsite => ['dnsPointsHere' => true, 'port' => 20000],
            AgentAction::CreateDatabase => ($p['engine'] ?? '') === Database::VALKEY ? ['number' => self::freeValkeyNumber($job)] : [],
            // The demo agent sends the parts of an account export, as a real agent does.
            AgentAction::ExportWebsite => DemoExport::send($job),
            AgentAction::Backup => ['snapshotId' => bin2hex(random_bytes(32)), 'sizeBytes' => random_int(40, 900) * 1024 ** 2, 'dumps' => [], 'forgotten' => []],
            AgentAction::BackupMailbox => self::mailboxBackup($p),
            // The demonstration has all messages of each backup in the mailbox already.
            AgentAction::RestoreMailbox => ['added' => 0, 'present' => (int) (MailboxSnapshot::where('restic_id', $p['snapshotId'] ?? '')->value('messages') ?? 0), 'tooLarge' => 0, 'failed' => 0, 'firstError' => ''],
            AgentAction::InstallHostUpdates => ['rebootRequired' => false],
            // The demonstration agent loads each pack of the panel, as a real agent does.
            AgentAction::SetPacks => [
                'packs' => Pack::orderBy('slug')->get()->map(fn (Pack $pack) => ['slug' => $pack->slug, 'version' => $pack->version])->all(),
                'runtimes' => [...['frankenphp', 'apache-php-fpm', 'node', 'python', 'custom'], ...Pack::orderBy('slug')->pluck('slug')->all()],
            ],
            // The demonstration agent loads each certificate that the server must have.
            AgentAction::SetCertificates => ['certificates' => app(Certificates::class)->wanted($job->server)],
            // Keys with the form of the keys of a real agent. They are not real keys.
            AgentAction::PrepareMailNode => ['keys' => [
                'wireguardKey' => base64_encode(hash('sha256', 'wg'.$job->server_id, true)),
                'keepKey' => "-----BEGIN PUBLIC KEY-----\n".base64_encode(hex2bin('302a300506032b6570032100').hash('sha256', 'keep'.$job->server_id, true))."\n-----END PUBLIC KEY-----\n",
                'nodeKey' => "-----BEGIN PUBLIC KEY-----\n".base64_encode(hex2bin('302a300506032b6570032100').hash('sha256', 'node'.$job->server_id, true))."\n-----END PUBLIC KEY-----\n",
            ]],
            AgentAction::SetMailNode => ['database' => ['size' => 3, 'state' => 'Synced']],
            // The fingerprint of the demonstration is not the fingerprint of the key.
            AgentAction::SetMailbox => ($p['publicKey'] ?? '') !== '' ? ['encryption' => ['fingerprints' => [strtoupper(sha1($p['publicKey']))], 'type' => 'OpenPGP v4: X25519', 'expiresAt' => null, 'warnings' => []]] : [],
            AgentAction::SetMailDomain => ['records' => DemoMailDns::records((string) ($p['domain'] ?? ''), (string) $job->server->mail_hostname)],
            AgentAction::InstallWordPress, AgentAction::CheckWordPress, AgentAction::UpdateWordPress, AgentAction::RestoreWordPress, AgentAction::RunWpCli => DemoWordPress::result($job),
            AgentAction::RunCronJob => ['cronJobId' => $p['cronJob']['id'] ?? '', 'exitCode' => 0, 'output' => "Running scheduled tasks.\nDone in 0.4 s.", 'ranAt' => now()->toIso8601String()],
            AgentAction::RunCommand => DemoCommands::result((string) ($p['command'] ?? '')),
            AgentAction::Deploy => [
                'releaseId' => $p['releaseId'] ?? '',
                'commit' => sha1((string) ($p['releaseId'] ?? '')),
                'message' => 'Update the '.(['home page', 'checkout form', 'product list', 'contact page'][crc32((string) ($p['releaseId'] ?? '')) % 4]),
                'removed' => [],
                'documentRoot' => $p['website']['documentRoot'] ?? '',
                'migrated' => ($p['migrationCommand'] ?? '') !== '',
                'healthError' => str_contains((string) ($p['repository'] ?? ''), 'unhealthy') ? self::UNHEALTHY : '',
            ],
            AgentAction::FetchImport => [
                'sha256' => hash('sha256', (string) ($p['importId'] ?? '')),
                'bytes' => Import::find($p['importId'] ?? null)?->size_bytes ?: 1_870 * 1024 ** 2,
                ...(($p['inspect'] ?? false) ? ['account' => DemoImport::account(self::importName($job))] : []),
            ],
            AgentAction::ImportFiles => [
                'files' => 1_000 + crc32((string) ($p['path'] ?? '')) % 20_000, 'bytes' => 0, 'skipped' => [],
                'rewritten' => isset($p['wordpressConfig']) ? [$p['wordpressConfig']] : [],
            ],
            AgentAction::ImportMail => ['messages' => 140 * count($p['mailboxes'] ?? []), 'failed' => []],
            AgentAction::UpdateAgent => ['version' => config('cloudfactory.agent.latest_version')],
            AgentAction::ScanWebsite => ($website = Website::find($job->website_id)) ? DemoSite::scan($website) : [],
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
            AgentAction::SetPanelBrands => ['panelBrands' => array_map(fn (string $d) => [
                'domain' => $d,
                'method' => PanelCertificate::publicName($d) ? 'letsencrypt' : 'internal',
                'state' => 'valid',
                'expiresAt' => now()->addDays(90)->toIso8601String(),
            ], array_values(array_filter((array) ($p['domains'] ?? []), 'is_string')))],
            AgentAction::SetPanelCertificate => ['panelCertificate' => [
                ...array_intersect_key(PanelCertificate::current(), array_flip(['domain', 'caCertificate'])),
                'method' => $p['method'] ?? 'internal',
                'state' => 'valid',
                'expiresAt' => now()->addDays(90)->toIso8601String(),
            ]],
            default => [],
        };
    }

    /**
     * The archive jobs change the sample files of the website (DemoSite) and give the summary
     * of a real agent.
     *
     * @return array<string, mixed>
     */
    private static function filesResult(AgentJob $job): array
    {
        $p = $job->payload;
        $website = $job->website_id ? Website::find($job->website_id) : null;

        return match (true) {
            $website && $job->action === AgentAction::MakeArchive => DemoSite::makeArchive($website, array_values((array) ($p['paths'] ?? [])), (string) ($p['path'] ?? '')),
            $website && $job->action === AgentAction::ExtractArchive => DemoSite::extract($website, (string) ($p['path'] ?? ''), (string) ($p['to'] ?? '/'), (bool) ($p['overwrite'] ?? false)),
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
    /**
     * A backup of a mailbox: about 80 KB for each message of the mailbox, as in DemoSeeder.
     * The retention removes nothing.
     *
     * @return array<string, mixed>
     */
    private static function mailboxBackup(array $p): array
    {
        $used = (int) (Mailbox::whereKey($p['mailboxId'] ?? '')->value('used_bytes') ?? 0);
        $size = max(64 * 1024, $used);

        return ['snapshotId' => bin2hex(random_bytes(32)), 'sizeBytes' => $size, 'messages' => intdiv($size, 80 * 1024), 'forgotten' => []];
    }

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
