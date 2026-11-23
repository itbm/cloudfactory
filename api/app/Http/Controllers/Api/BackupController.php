<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Snapshot;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DatabaseServers;
use App\Services\JobDispatcher;
use App\Validation\Input;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Collection;
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
            // "website": the files and the databases on the server of the website.
            // "databases": only the databases in the database servers on a different server.
            'part' => $s->part ?? Snapshot::WEBSITE,
            'serverId' => $s->server_id ?? $website->server_id,
            'mailboxes' => $s->mailboxes,
            'createdAt' => $s->created_at?->toIso8601String(),
        ])]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        self::requireTarget();
        self::refuseBusy($website);
        $job = self::start($this->jobs, $website, 'manual', "Back up {$website->name}");
        Audit::record($request, 'Backed up website', $website->name, $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Adversarial review, findings A4-01 and A4-03: a website has one backup or restore at a
     * time, and none while one of its database servers moves to a different server.
     *
     * @throws ValidationException
     */
    public static function refuseBusy(Website $website): void
    {
        if ($moving = self::movingServerOf($website)) {
            DatabaseServers::refuseMoving($moving, 'website');
        }
        $open = AgentJob::where('website_id', $website->id)->whereIn('action', [AgentAction::Backup->value, AgentAction::Restore->value])
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
        if ($open) {
            throw ValidationException::withMessages(['website' => 'A backup or a restore of this website is in progress. Wait until it is complete.']);
        }
    }

    /** A database server of the website that moves to a different server now, or null. */
    public static function movingServerOf(Website $website): ?DatabaseServer
    {
        return DatabaseServer::where('status', DatabaseServer::MOVING)
            ->whereIn('id', $website->databases()->whereNotNull('database_server_id')->select('database_server_id'))->first();
    }

    /**
     * Starts a backup of a website. It gives the job of the server of the website.
     *
     * The agent of the server of the website makes the backup of the files and of the
     * databases on that server. A website can also have databases in a database server on a
     * different server. The agent of each such server makes a backup of these databases
     * only, in the repository of its server.
     */
    public static function start(JobDispatcher $jobs, Website $website, string $kind, string $title): AgentJob
    {
        $job = $jobs->dispatch($website->server, AgentAction::Backup, self::backupPayload($website, $kind), $title, $website);
        $remote = $website->backupDatabases()->with('databaseServer.server')->orderBy('name')->get()
            ->filter(fn ($d) => $d->databaseServer && $d->databaseServer->server_id !== $website->server_id)
            ->groupBy(fn ($d) => $d->databaseServer->server_id);
        foreach ($remote as $databases) {
            $server = $databases[0]->databaseServer->server;
            $jobs->dispatch($server, AgentAction::Backup, [
                ...self::backupPayload($website, $kind),
                'databases' => $databases->map(fn ($d) => ['engine' => $d->engine, 'name' => $d->name, 'databaseServer' => $d->database_server_id])->values()->all(),
                'target' => self::repository($server),
                'databasesOnly' => true,
                // The agent of a different server has no record of the website.
                'customer' => $website->customer_id,
            ], "{$title}: databases on {$server->name}", $website);
        }

        return $job;
    }

    /**
     * The databases of a website in a backup (Website::backupDatabases), in the database
     * servers on the server of the website.
     *
     * @return Collection<int, Database>
     */
    public static function localDatabases(Website $website)
    {
        return $website->backupDatabases()->whereHas('databaseServer', fn ($q) => $q->where('server_id', $website->server_id))->orderBy('name')->get()->values();
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
            // Each database with the ID of its database server. The agent of the server of the
            // website makes the backup, so the backup has only the databases in the database
            // servers on that server.
            'databases' => self::localDatabases($website)->map(fn ($d) => ['engine' => $d->engine, 'name' => $d->name, 'databaseServer' => $d->database_server_id])->all(),
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
        // Adversarial review, finding X1-03: a restore changes the databases or the files.
        $needs = $data['scope'] === 'database' ? ['databases:write'] : ($data['scope'] === 'file' ? ['websites:write'] : ['websites:write', 'databases:write']);
        foreach ($needs as $scope) {
            abort_unless($request->user()->tokenCan($scope), 403, "This restore needs a token with the scope {$scope}.");
        }
        $removeNewFiles = (bool) ($data['removeNewFiles'] ?? false);
        $dumpHash = $databaseServer = null;
        // A snapshot with only databases is in the repository of a different server. The
        // agent of that server restores a database from it.
        $partOnly = $snapshot->part === Snapshot::DATABASES;
        // A snapshot from before the website moved is in the repository of its old server.
        // The files of the website are on the new server now, so the agent of the old server
        // restores only a database from it.
        $moved = ! $partOnly && $snapshot->server_id !== null && $snapshot->server_id !== $website->server_id;
        $server = $partOnly || $moved ? Server::findOrFail($snapshot->server_id) : $website->server;
        if ($partOnly && $data['scope'] !== 'database') {
            throw ValidationException::withMessages(['scope' => 'This backup has only databases. Select a database to restore.']);
        }
        if ($moved && $data['scope'] !== 'database') {
            throw ValidationException::withMessages(['scope' => "This backup is from the server {$server->name}, from before the website moved. The panel can restore only a database from it."]);
        }
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
            if (! $database) {
                throw ValidationException::withMessages(['item' => "This website does not have the database {$name} now. Make a database with this name, then restore the backup."]);
            }
            $databaseServer = $database->database_server_id;
            // The agent that has the backup restores it. It can restore only into a database
            // server on its own server.
            if ($database->databaseServer->server_id !== $server->id) {
                throw ValidationException::withMessages(['item' => "The database {$name} is now in a database server on a different server from this backup. The panel cannot restore it from this backup."]);
            }
        }
        if ($data['scope'] === 'file') {
            $request->validate(['item' => [Input::rule('restorePath')]]);
        }
        self::requireTarget();
        // The key of a server has no access to the repository that all servers used.
        if ($snapshot->shared_repository && self::credentials() === [] && $server->backupCredentials() !== []) {
            throw ValidationException::withMessages(['snapshot' => 'This backup is in the repository that all servers used in an earlier version. An administrator must set the shared credentials of the object storage in Backups to restore it.']);
        }
        $what = $data['scope'] === 'website' ? 'full website' : $data['item'];
        self::refuseBusy($website);
        $job = $this->jobs->dispatch($server, AgentAction::Restore, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'snapshotId' => $snapshot->restic_id ?? $snapshot->id,
            'scope' => $data['scope'],
            'item' => $data['item'] ?? null,
            'dumpSha256' => $dumpHash,
            ...($databaseServer ? ['databaseServer' => $databaseServer, 'customer' => $website->customer_id] : []),
            'deleteExtra' => $removeNewFiles,
            // A snapshot from before the change to one repository for each server is in the
            // repository that all servers used. AgentClient then sends the old password.
            'target' => $snapshot->shared_repository ? self::policy()['target'] : self::repository($server),
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
        return self::policy() + ['manual' => self::manualBackups(), 'credentialNames' => array_keys(self::credentials()), 'servers' => self::serverKeys()];
    }

    /**
     * Second audit, finding F08: each server that has a repository, with the names of the
     * credentials of its own key. A server with no names uses the shared key in the policy.
     *
     * @return list<array{serverId: string, server: string, repository: ?string, credentialNames: list<string>}>
     */
    private static function serverKeys(): array
    {
        $hasTarget = (bool) preg_match(self::TARGET, (string) (self::policy()['target'] ?? ''));

        return self::repositoryServers()->orderBy('name')->get()->map(fn (Server $s) => [
            'serverId' => $s->id,
            'server' => $s->name,
            'repository' => $hasTarget ? self::repository($s) : null,
            'credentialNames' => array_keys($s->backupCredentials()),
        ])->all();
    }

    /** The servers that have a restic repository: the servers with the role "web". */
    private static function repositoryServers(): Builder
    {
        return Server::whereJsonContains('roles', 'web');
    }

    /**
     * Sets the key of one server for the object storage. The storage provider must limit the
     * key to the repository of the server. The panel sends the key to that server only.
     */
    public function updateServerCredentials(Request $request, string $serverId): JsonResponse
    {
        $server = self::repositoryServers()->findOrFail($serverId);
        $data = $request->validate([
            'credentials' => ['required', 'array', 'min:1'],
            'credentials.*' => ['string', 'max:4096', 'not_regex:/[\r\n\0]/'],
        ]);
        self::checkCredentialNames($data['credentials']);
        $server->setBackupCredentials($data['credentials']);
        Audit::record($request, 'Set storage key of server', $server->name);

        return response()->json(self::presentPolicy());
    }

    /** Removes the key of one server. The server then uses the shared key in the policy. */
    public function destroyServerCredentials(Request $request, string $serverId): JsonResponse
    {
        $server = self::repositoryServers()->findOrFail($serverId);
        $server->setBackupCredentials([]);
        Audit::record($request, 'Removed storage key of server', $server->name);

        return response()->json(self::presentPolicy());
    }

    /**
     * @param  array<string, string>  $credentials
     *
     * @throws ValidationException
     */
    private static function checkCredentialNames(array $credentials): void
    {
        foreach (array_keys($credentials) as $key) {
            if (! in_array($key, self::CREDENTIAL_NAMES, true)) {
                throw ValidationException::withMessages(["credentials.{$key}" => 'Use one of: '.implode(', ', self::CREDENTIAL_NAMES).'.']);
            }
        }
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
        AuthController::refusePlugin($request);
        // Adversarial review, finding A9-02: the password opens the backups of all customers.
        // A token that can only read backups does not get it.
        abort_unless($request->user()->tokenCan('backups:write'), 403, 'This request needs a token with the scope backups:write.');
        Audit::record($request, 'Viewed backup repository passwords', 'restic repositories');
        $hasTarget = (bool) preg_match(self::TARGET, (string) (self::policy()['target'] ?? ''));
        $shared = self::sharedRepositoryPassword();

        return response()->json([
            'repositories' => self::repositoryServers()->orderBy('name')->get()->map(fn (Server $s) => [
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
        self::checkCredentialNames($data['credentials'] ?? []);
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

    /**
     * Second audit, finding F08: the credentials of the object storage that a server gets
     * with a backup job. A server with its own key gets only that key. The key has access to
     * the repository of the server only, so a restore from the repository that all servers
     * used in an earlier version gets the shared key.
     *
     * @return array<string, string>
     */
    public static function credentialsFor(Server $server, bool $sharedRepository = false): array
    {
        $own = $sharedRepository ? [] : $server->backupCredentials();

        return $own !== [] ? $own : self::credentials();
    }

    /**
     * True when each server that has a repository can get credentials: the shared key is
     * set, or each of these servers has its own key.
     */
    public static function hasCredentials(): bool
    {
        if (self::credentials() !== []) {
            return true;
        }

        return self::repositoryServers()->exists() && ! self::repositoryServers()->whereNull('backup_credentials')->exists();
    }
}
