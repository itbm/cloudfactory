<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Database;
use App\Models\DatabaseUser;
use App\Models\Website;
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
 * FR-502: database users. A user can have access to more than one database of the website, with
 * a permission for each database. For example, a sync user can read "shop" and change "staging".
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
            ...self::grantRules(),
        ]);
        $name = DatabaseNames::full($website, 'name', $data['name']);
        if (DatabaseUser::where(['engine' => $data['engine'], 'name' => $name])->exists()) {
            throw ValidationException::withMessages(['name' => 'This user exists.']);
        }
        $grants = $this->grants($website, $data['engine'], $data['grants'] ?? []);

        $user = DB::transaction(function () use ($website, $data, $name, $grants) {
            $user = $website->databaseUsers()->create(['engine' => $data['engine'], 'name' => $name, 'access' => $data['access'] ?? 'website']);
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
        DB::transaction(function () use ($website, $user, $data) {
            if (isset($data['access'])) {
                $user->update(['access' => $data['access']]);
            }
            if (array_key_exists('grants', $data)) {
                $user->databases()->sync($this->grants($website, $user->engine, $data['grants'] ?? []));
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
            'websiteDatabases' => self::websiteDatabases($website, $user->engine),
        ], "Remove database user {$user->name}", $website);
        Audit::record($request, 'Removed database user', "{$website->name} {$user->name}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public static function present(DatabaseUser $u): array
    {
        return ['id' => $u->id, 'engine' => $u->engine, 'name' => $u->name, 'access' => $u->access, 'grants' => $u->grants()];
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
     * @return array<string, array{privileges: string}>
     */
    private function grants(Website $website, string $engine, array $grants): array
    {
        $ids = $website->databases()->where('engine', $engine)->pluck('id')->all();
        $out = [];
        foreach ($grants as $i => $grant) {
            if (! in_array($grant['databaseId'], $ids, true)) {
                throw ValidationException::withMessages(["grants.{$i}.databaseId" => 'Select a database of this website with the same engine.']);
            }
            $out[$grant['databaseId']] = ['privileges' => $grant['privileges']];
        }

        return $out;
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
            'websiteDatabases' => self::websiteDatabases($website, $user->engine),
        ], fn ($v) => $v !== null), "{$title} {$user->name}", $website);
    }

    /** All databases of the website with this engine. The agent removes access to the others. */
    private static function websiteDatabases(Website $website, string $engine): array
    {
        return $website->databases()->where('engine', $engine)->orderBy('name')->pluck('name')->all();
    }
}
