<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseUser;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DatabaseNames;
use App\Services\DatabaseServers;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * FR-502: database users. A user can have access to more than one database of the website, with
 * a permission for each database. For example, a sync user can read "shop" and change "staging".
 * A user is in one database server, and has access only to databases in that database server.
 *
 * Valkey is different: a Valkey database has one user with the same name, which comes and goes
 * with the database. The user can get a new password. All other changes are refused.
 */
class DatabaseUserController extends Controller
{
    use Concerns;

    public const PRIVILEGES = ['readonly', 'readwrite'];

    public function __construct(private JobDispatcher $jobs) {}

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        if ($request->input('engine') === Database::VALKEY) {
            throw ValidationException::withMessages(['engine' => 'A Valkey database has one user, with the name of the database. Make a Valkey database to get its user.']);
        }
        $data = $request->validate([
            'engine' => ['required', Rule::in(Database::SQL_ENGINES)],
            'name' => ['required', 'string', 'max:64'],
            'access' => ['sometimes', Rule::in(['website', 'anywhere'])],
            // The database server of the user. It is necessary only for a user with no
            // database, when the customer has more than one database server of the engine.
            'databaseServerId' => ['sometimes', 'nullable', 'string'],
            ...self::grantRules(),
        ]);
        $name = DatabaseNames::full($website, 'name', $data['name']);
        if (DatabaseUser::where(['engine' => $data['engine'], 'name' => $name])->exists()) {
            throw ValidationException::withMessages(['name' => 'This user exists.']);
        }
        [$grants, $grantServers] = $this->grants($website, $data['engine'], $data['grants'] ?? []);
        $server = self::serverOfNewUser($website, $data['engine'], $grantServers, $data['databaseServerId'] ?? null);

        $user = DB::transaction(function () use ($website, $data, $name, $grants, $server) {
            $user = $website->databaseUsers()->create(['engine' => $data['engine'], 'name' => $name, 'access' => $data['access'] ?? 'website', 'database_server_id' => $server?->id]);
            $user->databases()->attach($grants);

            return $user;
        });
        $password = Str::password(24, symbols: false);
        $job = $this->dispatch($website, $user, 'Create database user', $password, create: true);
        Audit::record($request, 'Created database user', "{$website->name} {$name}", $website->customer_id);

        return response()->json(['user' => self::present($user->load('databases')), 'password' => $password, 'job' => new JobResource($job)], 202);
    }

    /** Changes the databases that the user can use, where it can connect from, or its password. */
    public function update(Request $request, string $websiteId, string $userId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $user = $website->databaseUsers()->findOrFail($userId);
        $data = $request->validate([
            'access' => ['sometimes', Rule::in(['website', 'anywhere'])],
            'resetPassword' => ['sometimes', 'boolean'],
            ...self::grantRules(),
        ]);
        if ($user->engine === Database::VALKEY && (isset($data['access']) || array_key_exists('grants', $data) || ! ($data['resetPassword'] ?? false))) {
            throw ValidationException::withMessages(['user' => 'A Valkey user has access to its own database only, from the container of the website only. You can only make a new password.']);
        }
        $grants = null;
        if (array_key_exists('grants', $data)) {
            [$grants, $grantServers] = $this->grants($website, $user->engine, $data['grants'] ?? []);
            foreach ($grantServers as $serverId => $field) {
                if ($serverId !== (string) $user->database_server_id) {
                    throw ValidationException::withMessages([$field => 'Select a database in the database server of the user.']);
                }
            }
        }
        DB::transaction(function () use ($user, $data, $grants) {
            if (isset($data['access'])) {
                $user->update(['access' => $data['access']]);
            }
            if ($grants !== null) {
                $user->databases()->sync($grants);
            }
        });
        $password = ($data['resetPassword'] ?? false) ? Str::password(24, symbols: false) : null;
        $job = $this->dispatch($website, $user, 'Change database user', $password, create: false);
        Audit::record($request, 'Changed database user', "{$website->name} {$user->name}", $website->customer_id);

        return response()->json(['user' => self::present($user->fresh('databases')), 'password' => $password, 'job' => new JobResource($job)], 202);
    }

    public function destroy(Request $request, string $websiteId, string $userId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $user = $website->databaseUsers()->findOrFail($userId);
        if ($user->engine === Database::VALKEY) {
            throw ValidationException::withMessages(['user' => 'The user of a Valkey database goes with its database. Remove the database.']);
        }
        // Batch 1, finding 4: the panel removes the row when the job succeeds (JobOutcome).
        $job = $this->jobs->dispatch($website->server, AgentAction::DeleteDatabaseUser, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'engine' => $user->engine,
            'name' => $user->name,
            'websiteDatabases' => self::websiteDatabases($website, $user),
            ...($user->database_server_id ? ['databaseServer' => $user->database_server_id] : []),
        ], "Remove database user {$user->name}", $website, databaseServer: $user->databaseServer);
        Audit::record($request, 'Removed database user', "{$website->name} {$user->name}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public static function present(DatabaseUser $u): array
    {
        return [
            'id' => $u->id,
            'engine' => $u->engine,
            'name' => $u->name,
            'access' => $u->access,
            // Null for a user in the shared database containers of an earlier release.
            'databaseServerId' => $u->database_server_id,
            'grants' => $u->grants(),
        ];
    }

    private static function grantRules(): array
    {
        return [
            'grants' => ['sometimes', 'array', 'max:100'],
            'grants.*.databaseId' => ['required', 'string', 'distinct'],
            'grants.*.privileges' => ['required', Rule::in(self::PRIVILEGES)],
        ];
    }

    /**
     * Checks that each database belongs to the website and has the engine of the user.
     *
     * The second value has the database servers of the databases: the ID of each one, with the
     * request field of its first database. The ID is "" for the shared database containers of
     * an earlier release.
     *
     * @return array{0: array<string, array{privileges: string}>, 1: array<string, string>}
     */
    private function grants(Website $website, string $engine, array $grants): array
    {
        $servers = $website->databases()->where('engine', $engine)->pluck('database_server_id', 'id');
        $out = $used = [];
        foreach ($grants as $i => $grant) {
            if (! $servers->has($grant['databaseId'])) {
                throw ValidationException::withMessages(["grants.{$i}.databaseId" => 'Select a database of this website with the same engine.']);
            }
            $out[$grant['databaseId']] = ['privileges' => $grant['privileges']];
            $used[(string) $servers[$grant['databaseId']]] ??= "grants.{$i}.databaseId";
        }

        return [$out, $used];
    }

    /**
     * The database server of a new user: the database server of its databases. A user with no
     * database gets the database server of the request, or the only database server of the
     * engine that the website can use.
     *
     * @param  array<string, string>  $grantServers  From grants().
     *
     * @throws ValidationException
     */
    private static function serverOfNewUser(Website $website, string $engine, array $grantServers, ?string $requested): ?DatabaseServer
    {
        if ($website->database_instance === null) {
            if ($requested !== null) {
                throw ValidationException::withMessages(['databaseServerId' => "This website uses the shared database containers of an earlier release. An administrator must move the databases of the server {$website->server->name} to database servers first."]);
            }

            return null;
        }
        if (count($grantServers) > 1) {
            throw ValidationException::withMessages([array_values($grantServers)[1] => 'A database user is in one database server. Select databases of one database server, or make a user for each database server.']);
        }
        $ofGrants = array_key_first($grantServers);
        if ($ofGrants !== null && $requested !== null && (string) $ofGrants !== $requested) {
            throw ValidationException::withMessages(['databaseServerId' => 'The databases of the user are in a different database server.']);
        }
        $id = $ofGrants !== null ? (string) $ofGrants : $requested;
        $servers = DatabaseServers::forWebsite($website)->where('engine', $engine)->values();
        if ($id === null) {
            if ($servers->count() === 1) {
                return $servers[0];
            }
            throw ValidationException::withMessages(['databaseServerId' => $servers->isEmpty()
                ? 'This customer has no '.DatabaseServers::engineName($engine).' database server on the server of the website. Make a database first.'
                : 'Select the database server of the user.']);
        }
        $server = $servers->firstWhere('id', $id);
        if (! $server) {
            throw ValidationException::withMessages(['databaseServerId' => 'Select a database server of the customer of this website, on the server of the website, with the engine of the user.']);
        }

        return $server;
    }

    /** The agent sets the full state of the user: its password, its host and all of its grants. */
    private function dispatch(Website $website, DatabaseUser $user, string $title, ?string $password, bool $create)
    {
        $user->load('databases');

        return $this->jobs->dispatch($website->server, AgentAction::SetDatabaseUser, array_filter([
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'engine' => $user->engine,
            'name' => $user->name,
            'password' => $password,
            'access' => $user->access,
            'create' => $create,
            'grants' => array_map(fn ($g) => ['database' => $g['database'], 'privileges' => $g['privileges']], $user->grants()),
            'websiteDatabases' => self::websiteDatabases($website, $user),
            // The agent makes a database server that it does not have.
            'databaseServer' => $user->databaseServer?->forAgent(),
        ], fn ($v) => $v !== null), "{$title} {$user->name}", $website, databaseServer: $user->databaseServer);
    }

    /**
     * All databases of the website in the database server of the user. The agent removes the
     * access of the user to those that are not in its grants.
     */
    private static function websiteDatabases(Website $website, DatabaseUser $user): array
    {
        return $website->databases()->where(['engine' => $user->engine, 'database_server_id' => $user->database_server_id])->orderBy('name')->pluck('name')->all();
    }
}
