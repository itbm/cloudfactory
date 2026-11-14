<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Crypt;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-901 to FR-903: backups and restore. */
class BackupController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json(['data' => $website->snapshots()->limit(100)->get()->map(fn ($s) => [
            'id' => $s->id,
            'kind' => $s->kind,
            'sizeBytes' => $s->size_bytes,
            'databases' => $s->databases,
            'mailboxes' => $s->mailboxes,
            'createdAt' => $s->created_at?->toIso8601String(),
        ])]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        self::requireTarget();
        $job = $this->jobs->dispatch($website->server, AgentAction::Backup, self::backupPayload($website, 'manual'), "Back up {$website->name}", $website);
        Audit::record($request, 'Backed up website', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * The payload of a backup. A scheduled backup also applies the retention policy (FR-902).
     *
     * Audit finding R8: the agent applies the policy only to the scheduled backups. For the
     * manual backups of a website, it keeps the newest ones.
     */
    public static function backupPayload(Website $website, string $kind): array
    {
        $policy = self::policy();
        $retention = $kind === 'scheduled'
            ? ['daily' => (int) $policy['daily'], 'weekly' => (int) $policy['weekly'], 'monthly' => (int) $policy['monthly']]
            : ['last' => self::manualBackups()];

        return [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'kind' => $kind,
            // Each database with the ID of its database server. The shared database containers
            // of an earlier release have no ID.
            'databases' => $website->sqlDatabases()->orderBy('name')->get()->map(fn ($d) => ['engine' => $d->engine, 'name' => $d->name, ...($d->database_server_id ? ['databaseServer' => $d->database_server_id] : [])])->all(),
            'target' => self::repository($website->server),
            'retention' => $retention,
        ];
    }

    /**
     * Audit finding S2: the restic repository of a server. Each server has its own repository
     * below the backup target, and its own password (Server::backupPassword). A person who
     * has root access to one server can then not read the backups of the other servers.
     */
    public static function repository(Server $server): string
    {
        return self::serverTarget((string) (self::policy()['target'] ?? ''), $server->id);
    }

    /**
     * Adds "servers/ID" to the path of a restic repository. The forms are
     * "s3:host/bucket/path", "b2:bucket:path", "azure:container:/path" and "gs:bucket:/path".
     */
    public static function serverTarget(string $target, string $serverId): string
    {
        $suffix = 'servers/'.$serverId;
        [$kind, $rest] = array_pad(explode(':', $target, 2), 2, '');
        if ($kind === 's3') {
            return $kind.':'.rtrim($rest, '/').'/'.$suffix;
        }
        [$bucket, $path] = array_pad(explode(':', $rest, 2), 2, '');
        $path = rtrim($path, '/');
        // With no path, B2 has "b2:bucket:servers/ID". Azure and Google have a path from "/".
        $start = $path === '' ? ($kind === 'b2' ? '' : '/') : $path.'/';

        return "{$kind}:{$bucket}:{$start}{$suffix}";
    }

    /** The number of manual backups that the panel keeps for each website. */
    public static function manualBackups(): int
    {
        return max(1, (int) config('cloudfactory.manual_backups', 10));
    }

    /**
     * A backup or a restore needs a backup target. Without one, the job would fail on the agent.
     *
     * @throws ValidationException
     */
    public static function requireTarget(): void
    {
        if (! preg_match(self::TARGET, (string) (self::policy()['target'] ?? ''))) {
            throw ValidationException::withMessages(['target' => 'No backup target is set. An administrator sets it in Backups.']);
        }
    }

    /** FR-903: restore a full website, one database, one mailbox or one file. */
    public function restore(Request $request, string $websiteId, string $snapshotId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $snapshot = $website->snapshots()->findOrFail($snapshotId);
        $data = $request->validate([
            // Audit finding R5: the agent of this release has no mailbox restore.
            'scope' => ['required', Rule::in(['website', 'database', 'file'])],
            'item' => ['required_unless:scope,website', 'nullable', 'string', 'max:1024'],
            // Audit finding R8: a full restore can remove the files that are not in the backup,
            // for example the files that an attacker added.
            'removeNewFiles' => ['sometimes', 'boolean', 'prohibited_unless:scope,website'],
        ]);
        $removeNewFiles = (bool) ($data['removeNewFiles'] ?? false);
        $dumpHash = $databaseServer = null;
        if ($data['scope'] === 'database') {
            $request->validate(['item' => [Rule::in($snapshot->databases)]]);
            // CF-06: the agent restores a dump only when its hash agrees with the hash from the backup.
            $dumpHash = $snapshot->dump_hashes[$data['item']] ?? null;
            if (! $dumpHash) {
                throw ValidationException::withMessages(['item' => 'This backup has no checked dump of the database.']);
            }
            // The agent restores the dump into the database server that has the database now.
            [$engine, $name] = array_pad(explode(':', $data['item'], 2), 2, '');
            $database = $website->databases()->where(['engine' => $engine, 'name' => $name])->first();
            if (! $database && $website->database_instance !== null) {
                throw ValidationException::withMessages(['item' => "This website does not have the database {$name} now. Make a database with this name, then restore the backup."]);
            }
            $databaseServer = $database?->database_server_id;
        }
        if ($data['scope'] === 'file') {
            $request->validate(['item' => ['regex:#^/[^\0]*$#', 'not_regex:#(^|/)\.\.(/|$)#']]);
        }
        self::requireTarget();
        $what = $data['scope'] === 'website' ? 'full website' : $data['item'];
        $job = $this->jobs->dispatch($website->server, AgentAction::Restore, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'snapshotId' => $snapshot->restic_id ?? $snapshot->id,
            'scope' => $data['scope'],
            'item' => $data['item'] ?? null,
            'dumpSha256' => $dumpHash,
            ...($databaseServer ? ['databaseServer' => $databaseServer] : []),
            'deleteExtra' => $removeNewFiles,
            // A snapshot from before the change to one repository for each server is in the
            // repository that all servers used. AgentClient then sends the old password.
            'target' => $snapshot->shared_repository ? self::policy()['target'] : self::repository($website->server),
            ...($snapshot->shared_repository ? ['sharedRepository' => true] : []),
        ], "Restore {$what}", $website);
        Audit::record($request, 'Restored from backup', "{$website->name} {$what}".($removeNewFiles ? ', new files removed' : ''), $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** The policy. The credentials are not in the response: only their names (write-only). */
    public function showPolicy(Request $request): JsonResponse
    {
        // Audit finding S9: a customer sees when the backups run and how long they stay. The
        // address of the object storage and the names of its credentials are for administrators.
        if (! $request->user()->isAdministrator()) {
            return response()->json(array_intersect_key(self::presentPolicy(), array_flip(['daily', 'weekly', 'monthly', 'time', 'manual'])));
        }

        return response()->json(self::presentPolicy());
    }

    private static function presentPolicy(): array
    {
        return self::policy() + ['manual' => self::manualBackups(), 'credentialNames' => array_keys(self::credentials())];
    }

    /**
     * The restic repository and the repository password of each server that has websites.
     * An administrator needs them to restore with restic directly, for example when the panel
     * is lost. The panel keeps the passwords encrypted and sends each one only to its server.
     *
     * "shared" is the repository that all servers used before audit finding S2. It is null
     * when the panel never used it.
     */
    public function showRepositoryPassword(Request $request): JsonResponse
    {
        Audit::record($request, 'Viewed backup repository passwords', 'restic repositories');
        $hasTarget = (bool) preg_match(self::TARGET, (string) (self::policy()['target'] ?? ''));
        $shared = self::sharedRepositoryPassword();

        return response()->json([
            'repositories' => Server::whereJsonContains('roles', 'web')->orderBy('name')->get()->map(fn (Server $s) => [
                'serverId' => $s->id,
                'server' => $s->name,
                'repository' => $hasTarget ? self::repository($s) : null,
                'password' => $s->backupPassword(),
            ])->all(),
            'shared' => $shared === null ? null : ['repository' => $hasTarget ? self::policy()['target'] : null, 'password' => $shared],
        ]);
    }

    /**
     * The password of the repository that all servers used before audit finding S2, or null.
     * The panel uses it only to restore a snapshot from that time.
     */
    public static function sharedRepositoryPassword(): ?string
    {
        $value = Setting::get('backup_repository_password');

        return is_string($value) ? Crypt::decryptString($value) : null;
    }

    /** FR-902: the administrator sets the retention policy. */
    public function updatePolicy(Request $request): JsonResponse
    {
        $data = $request->validate([
            'daily' => ['required', 'integer', 'between:0,365'],
            'weekly' => ['required', 'integer', 'between:0,520'],
            'monthly' => ['required', 'integer', 'between:0,240'],
            'time' => ['required', 'date_format:H:i'],
            // The restic repository format, for example "s3:s3.amazonaws.com/bucket/path".
            'target' => ['required', 'regex:'.self::TARGET],
            // The environment variables of restic for the object storage. Null keeps the
            // credentials. An empty object removes them.
            'credentials' => ['sometimes', 'nullable', 'array'],
            'credentials.*' => ['string', 'max:4096', 'not_regex:/[\r\n\0]/'],
        ]);
        foreach (array_keys($data['credentials'] ?? []) as $key) {
            if (! in_array($key, self::CREDENTIAL_NAMES, true)) {
                throw ValidationException::withMessages(["credentials.{$key}" => 'Use one of: '.implode(', ', self::CREDENTIAL_NAMES).'.']);
            }
        }
        if (array_key_exists('credentials', $data) && $data['credentials'] !== null) {
            // The credentials are encrypted at rest with the application key.
            Setting::put('backup_credentials', Crypt::encryptString(json_encode((object) $data['credentials'])));
        }
        unset($data['credentials']);
        Setting::put('backup_policy', $data);
        Audit::record($request, 'Changed backup policy', "{$data['daily']} daily, {$data['weekly']} weekly, {$data['monthly']} monthly");

        return response()->json(self::presentPolicy());
    }

    public const TARGET = '#^(s3|b2|azure|gs):\S+$#';

    /** The restic environment variables that the agent accepts (agent/internal/actions/data.go). */
    public const CREDENTIAL_NAMES = [
        'AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY', 'AWS_DEFAULT_REGION',
        'B2_ACCOUNT_ID', 'B2_ACCOUNT_KEY',
        'AZURE_ACCOUNT_NAME', 'AZURE_ACCOUNT_KEY', 'AZURE_ACCOUNT_SAS',
        'GOOGLE_PROJECT_ID', 'GOOGLE_ACCESS_TOKEN',
    ];

    public static function policy(): array
    {
        return Setting::get('backup_policy', config('cloudfactory.backups'));
    }

    /** @return array<string, string> */
    public static function credentials(): array
    {
        $value = Setting::get('backup_credentials');
        if (! is_string($value)) {
            return [];
        }

        return (array) json_decode(Crypt::decryptString($value), true);
    }
}
