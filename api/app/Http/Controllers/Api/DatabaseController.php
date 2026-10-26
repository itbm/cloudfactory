<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\Database;
use App\Models\DatabaseUser;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-501 and FR-502: MariaDB and PostgreSQL databases and users. */
class DatabaseController extends Controller
{
    use Concerns;

    public function __construct(private JobDispatcher $jobs) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);

        return response()->json([
            'databases' => $website->databases()->get()->map(fn (Database $d) => ['id' => $d->id, 'engine' => $d->engine, 'name' => $d->name, 'sizeBytes' => $d->size_bytes]),
            'users' => $website->databaseUsers()->get()->map(fn (DatabaseUser $u) => ['id' => $u->id, 'engine' => $u->engine, 'name' => $u->name, 'databases' => $u->databases, 'access' => $u->access]),
        ]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $data = $request->validate([
            'engine' => ['required', Rule::in(['mariadb', 'postgresql'])],
            'name' => ['required', 'regex:/^[a-z][a-z0-9_]{1,62}$/'],
        ]);
        if (Database::where($data)->exists()) {
            throw ValidationException::withMessages(['name' => 'This database exists.']);
        }
        if ($website->databases()->count() >= $website->plan->databases) {
            throw ValidationException::withMessages(['name' => "The {$website->plan->name} plan allows {$website->plan->databases} databases."]);
        }
        $database = $website->databases()->create($data);
        $website->databaseUsers()->create([...$data, 'databases' => [$data['name']], 'access' => 'website']);
        $password = Str::password(24, symbols: false);
        $job = $this->jobs->dispatch($website->server, AgentAction::CreateDatabase, [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'engine' => $data['engine'],
            'name' => $data['name'],
            'user' => $data['name'],
            // The agent gets the password over mutual TLS. The panel removes it when the job ends.
            'password' => $password,
            'access' => 'website',
        ], "Create database {$data['name']}", $website);
        Audit::record($request, 'Created database', "{$website->name} {$data['name']}", $website->customer_id);

        return response()->json(['id' => $database->id, 'user' => $data['name'], 'password' => $password, 'job' => new JobResource($job)], 202);
    }

    public function destroy(Request $request, string $websiteId, string $databaseId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $database = $website->databases()->findOrFail($databaseId);
        $database->delete();
        $job = $this->jobs->dispatch($website->server, AgentAction::DeleteDatabase, ['websiteId' => $website->id, 'engine' => $database->engine, 'name' => $database->name], "Remove database {$database->name}", $website);
        Audit::record($request, 'Removed database', "{$website->name} {$database->name}", $website->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }
}
