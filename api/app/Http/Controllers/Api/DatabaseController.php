<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Database;
use App\Models\DatabaseUser;
use App\Services\Audit;
use App\Services\DatabaseNames;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * FR-501 and FR-502: MariaDB and PostgreSQL databases. The names start with the Unix user of
 * the website (CF-01). DatabaseUserController changes the users and their access.
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
            'databases' => $website->databases()->orderBy('name')->get()->map(fn (Database $d) => ['id' => $d->id, 'engine' => $d->engine, 'name' => $d->name, 'sizeBytes' => $d->size_bytes]),
            'users' => $website->databaseUsers()->with('databases')->orderBy('name')->get()->map(fn (DatabaseUser $u) => DatabaseUserController::present($u)),
        ]);
    }

    /** Makes a database. By default it also makes a user with the same name and read-write access. */
    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'engine' => ['required', Rule::in(['mariadb', 'postgresql'])],
            'name' => ['required', 'string', 'max:64'],
            'createUser' => ['sometimes', 'boolean'],
        ]);
        $name = DatabaseNames::full($website, 'name', $data['name']);
        $withUser = $data['createUser'] ?? true;
        if (Database::where(['engine' => $data['engine'], 'name' => $name])->exists()) {
            throw ValidationException::withMessages(['name' => 'This database exists.']);
        }
        if ($withUser && DatabaseUser::where(['engine' => $data['engine'], 'name' => $name])->exists()) {
            throw ValidationException::withMessages(['name' => 'A database user with this name exists. Make the database with no user.']);
        }
        if ($website->databases()->count() >= $website->plan->databases) {
            throw ValidationException::withMessages(['name' => "The {$website->plan->name} plan allows {$website->plan->databases} databases."]);
        }

        $database = DB::transaction(function () use ($website, $data, $name, $withUser) {
            $database = $website->databases()->create(['engine' => $data['engine'], 'name' => $name]);
            if ($withUser) {
                $user = $website->databaseUsers()->create(['engine' => $data['engine'], 'name' => $name, 'access' => 'website']);
                $user->databases()->attach($database->id, ['privileges' => 'readwrite']);
            }

            return $database;
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
        ]), "Create database {$name}", $website);
        Audit::record($request, 'Created database', "{$website->name} {$name}", $website->customer_id);

        return response()->json(['id' => $database->id, 'name' => $name, 'user' => $withUser ? $name : null, 'password' => $password, 'job' => new JobResource($job)], 202);
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
        ], "Remove database {$database->name}", $website);
        Audit::record($request, 'Removed database', "{$website->name} {$database->name}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }
}
