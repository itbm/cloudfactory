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
 * FR-501 and FR-502: MariaDB, PostgreSQL and Valkey databases. The names start with the Unix
 * user of the website (CF-01). DatabaseUserController changes the users and their access.
 *
 * Each database is in one database server of the customer of the website
 * (DatabaseServerController). The plan of the database server gives the number of its
 * databases. A customer cannot change the service of a different customer.
 *
 * A Valkey database is one database number of a Valkey database server. It always has one user
 * with the same name. The agent gives the number, and the panel shows it when the job is
 * complete.
 */
class DatabaseController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json([
            'prefix' => DatabaseNames::prefix($website),
            'databases' => $website->databases()->with('databaseServer')->orderBy('name')->get()->map(fn (Database $d) => self::present($d)),
            'users' => $website->databaseUsers()->with('databases')->orderBy('name')->get()->map(fn (DatabaseUser $u) => DatabaseUserController::present($u)),
            // The database servers that can get a database of this website: those of its
            // customer on its server. Empty for a website that uses the shared database
            // containers of an earlier release.
            'servers' => $website->database_instance === null ? [] : DatabaseServers::forWebsite($website)->map(fn (DatabaseServer $s) => DatabaseServerController::present($s))->all(),
        ]);
    }

    public static function present(Database $d): array
    {
        return [
            'id' => $d->id,
            'engine' => $d->engine,
            'name' => $d->name,
            // Null for a database in the shared database containers of an earlier release.
            'databaseServerId' => $d->database_server_id,
            // The host name and the port that the application of the website connects to.
            'host' => DatabaseServer::HOSTS[$d->engine],
            'port' => $d->databaseServer?->port ?? DatabaseServer::FIRST_PORT[$d->engine],
            'sizeBytes' => $d->size_bytes,
            // Valkey only: the database number that the application selects, and the number of
            // keys. The number is null until the agent made the database.
            'number' => $d->number,
            'keys' => $d->key_count,
        ];
    }

    /** Makes a database. By default it also makes a user with the same name and read-write access. */
    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'engine' => ['required', Rule::in(Database::ENGINES)],
            'name' => ['required', 'string', 'max:64'],
            'createUser' => ['sometimes', 'boolean'],
            // The database server that gets the database. With none, the panel selects the
            // first database server of the engine that has space.
            'databaseServerId' => ['sometimes', 'nullable', 'string'],
        ]);
        $name = DatabaseNames::full($website, 'name', $data['name']);
        $withUser = $data['createUser'] ?? true;
        $serverId = $data['databaseServerId'] ?? null;
        // A website from an earlier release uses the shared database containers until an
        // administrator moves its databases.
        $shared = $website->database_instance === null;
        if ($shared && $serverId !== null) {
            throw ValidationException::withMessages(['databaseServerId' => "This website uses the shared database containers of an earlier release. An administrator must move the databases of the server {$website->server->name} to database servers first."]);
        }
        if ($data['engine'] === Database::VALKEY) {
            if (! $withUser) {
                throw ValidationException::withMessages(['createUser' => 'A Valkey database always has a user with the same name.']);
            }
            if (! $website->server->hasDatabaseEngine(Database::VALKEY)) {
                throw ValidationException::withMessages(['engine' => "The agent on the server {$website->server->name} did not report that it can operate Valkey. Update the agent of the server, then try again."]);
            }
            // Valkey is in a database server of the customer. The shared database containers
            // of an earlier release have none.
            if ($shared) {
                throw ValidationException::withMessages(['engine' => "This website uses the shared database containers of an earlier release, which have no Valkey. An administrator must move the databases of the server {$website->server->name} to database servers first."]);
            }
        }
        if (Database::where(['engine' => $data['engine'], 'name' => $name])->exists()) {
            throw ValidationException::withMessages(['name' => 'This database exists.']);
        }
        if ($withUser && DatabaseUser::where(['engine' => $data['engine'], 'name' => $name])->exists()) {
            throw ValidationException::withMessages(['name' => 'A database user with this name exists. Make the database with no user.']);
        }

        // Audit finding R6: the count and the new row are in one transaction, with a lock
        // that stops two requests from passing the limit at the same time.
        [$database, $server] = DB::transaction(function () use ($website, $data, $name, $withUser, $shared, $serverId) {
            $server = null;
            if ($shared) {
                // The shared containers have no database server. The plan of the website
                // gives the number of its databases, as in the earlier release.
                $locked = Website::with('plan')->lockForUpdate()->findOrFail($website->id);
                if ($locked->databases()->count() >= $locked->plan->databases) {
                    throw ValidationException::withMessages(['name' => "The {$locked->plan->name} plan allows {$locked->plan->databases} databases."]);
                }
            } else {
                // The plan of the database server gives the number of its databases.
                $server = DatabaseServers::checkSpace(DatabaseServers::forDatabase($website, $data['engine'], $serverId)[0]);
            }
            $database = $website->databases()->create(['engine' => $data['engine'], 'name' => $name, 'database_server_id' => $server?->id]);
            if ($withUser) {
                $user = $website->databaseUsers()->create(['engine' => $data['engine'], 'name' => $name, 'access' => 'website', 'database_server_id' => $server?->id]);
                $user->databases()->attach($database->id, ['privileges' => 'readwrite']);
            }

            return [$database, $server];
        });
        $password = $withUser ? Str::password(24, symbols: false) : null;
        $job = $this->jobs->dispatch($website->server, AgentAction::CreateDatabase, array_filter([
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'engine' => $data['engine'],
            'name' => $name,
            'user' => $withUser ? $name : null,
            // The agent gets the password over mutual TLS. The panel removes it when the job ends.
            'password' => $password,
            'access' => $withUser ? 'website' : null,
            // The agent makes a database server that it does not have.
            'databaseServer' => $server?->forAgent(),
        ]), "Create database {$name}", $website, databaseServer: $server);
        Audit::record($request, 'Created database', "{$website->name} {$name}", $website->customer_id);

        return response()->json([
            'id' => $database->id,
            'name' => $name,
            'user' => $withUser ? $name : null,
            'password' => $password,
            'databaseServerId' => $server?->id,
            'host' => DatabaseServer::HOSTS[$data['engine']],
            'port' => $server?->port ?? DatabaseServer::FIRST_PORT[$data['engine']],
            'job' => new JobResource($job),
        ], 202);
    }

    public function destroy(Request $request, string $websiteId, string $databaseId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $database = $website->databases()->with('users')->findOrFail($databaseId);
        $users = $database->users->pluck('name')->sort()->values()->all();
        // Batch 1, finding 4: the panel removes the row when the job succeeds (JobOutcome).
        // The agent removes the access of each user to the database, then the database.
        $job = $this->jobs->dispatch($website->server, AgentAction::DeleteDatabase, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'engine' => $database->engine,
            'name' => $database->name,
            'users' => $users,
            ...($database->database_server_id ? ['databaseServer' => $database->database_server_id] : []),
        ], "Remove database {$database->name}", $website, databaseServer: $database->databaseServer);
        Audit::record($request, 'Removed database', "{$website->name} {$database->name}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }
}
