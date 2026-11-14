<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Plan;
use App\Models\Server;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DatabaseServers;
use App\Services\JobDispatcher;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The database servers of a customer. A customer makes database servers as it makes websites:
 * each one is a container with one engine (MariaDB, PostgreSQL or Valkey) and its own limits
 * for CPU, memory and disk space.
 *
 * - The plan of the customer gives the number of database servers that the customer can have.
 * - The plan of a database server gives its limits and the number of its databases.
 * - All websites of the customer on the same server can connect to the database server.
 */
class DatabaseServerController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    private function visible(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return DatabaseServer::query()->with('plan')->withCount('databases')->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids));
    }

    private function find(Request $request, string $id): DatabaseServer
    {
        return $this->visible($request)->with(['server', 'customer'])->findOrFail($id);
    }

    public function index(Request $request): JsonResponse
    {
        $request->validate([
            'customerId' => ['nullable', 'string'],
            'serverId' => ['nullable', 'string'],
            'engine' => ['nullable', Rule::in(Database::ENGINES)],
        ]);

        return response()->json(['data' => $this->visible($request)
            ->when($request->query('customerId'), fn ($q, $id) => $q->where('customer_id', $id))
            ->when($request->query('serverId'), fn ($q, $id) => $q->where('server_id', $id))
            ->when($request->query('engine'), fn ($q, $engine) => $q->where('engine', $engine))
            ->orderBy('name')->orderBy('id')->get()->map(fn (DatabaseServer $s) => self::present($s))]);
    }

    public function show(Request $request, string $id): JsonResponse
    {
        return response()->json(self::present($this->find($request, $id)));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'engine' => ['required', Rule::in(Database::ENGINES)],
            'name' => ['sometimes', 'nullable', 'string', 'regex:'.DatabaseServers::NAME],
            'customerId' => ['nullable', 'exists:customers,id'],
            'planId' => ['nullable', 'exists:plans,id'],
            'serverId' => ['nullable', 'exists:servers,id'],
            ...self::limitRules(),
        ], ['name.regex' => 'Use 1 to 40 lower-case letters, digits and hyphens. Start and end with a letter or a digit.']);
        $user = $request->user();
        $customerId = $data['customerId'] ?? $user->customer_id;
        if (! $customerId || ! $user->canSeeCustomer($customerId)) {
            throw ValidationException::withMessages(['customerId' => 'Select a customer that you control.']);
        }
        $customer = Customer::with('plan')->findOrFail($customerId);
        abort_if($customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you make a database server.');
        // Only an administrator, or the reseller of the customer, selects a different plan.
        $planId = $data['planId'] ?? $customer->plan_id;
        if ($planId !== $customer->plan_id && ! WebsiteController::canChoosePlan($user, $customer, $planId)) {
            throw ValidationException::withMessages(['planId' => 'Only an administrator or your reseller can select the plan.']);
        }
        if (isset($data['limits']) && ! $user->isAdministrator()) {
            return response()->json(['message' => 'Only administrators can set resource limits.'], 403);
        }
        $name = $data['name'] ?? null;
        if ($name !== null && $customer->databaseServers()->where('name', $name)->exists()) {
            throw ValidationException::withMessages(['name' => 'This customer has a database server with this name.']);
        }
        $host = self::host($request, $customer, $data['serverId'] ?? null);

        $server = DatabaseServers::make($customer, $host, $data['engine'], Plan::findOrFail($planId), $name, $data['limits'] ?? null);
        $job = $this->jobs->dispatch($host, AgentAction::SetDatabaseServer, ['databaseServer' => $server->forAgent(), 'create' => true], "Create database server {$server->name}", databaseServer: $server);
        Audit::record($request, 'Created database server', "{$customer->name} {$server->name}", $customer->id);

        return response()->json(['databaseServer' => self::present($server->loadCount('databases')->load('plan')), 'job' => new JobResource($job)], 202);
    }

    /**
     * The server that gets the container. The websites of the customer on that server can
     * connect to the database server, so it is a server that has a website of the customer.
     * An administrator can select a different server.
     *
     * @throws ValidationException
     */
    private static function host(Request $request, Customer $customer, ?string $serverId): Server
    {
        $withWebsites = Website::where('customer_id', $customer->id)->selectRaw('server_id, count(*) as websites')->groupBy('server_id')->pluck('websites', 'server_id');
        if ($serverId !== null) {
            $server = Server::findOrFail($serverId);
            if (! $request->user()->isAdministrator() && ! $withWebsites->has($server->id)) {
                throw ValidationException::withMessages(['serverId' => 'Select a server that has a website of this customer.']);
            }
            if (! in_array('web', $server->roles, true)) {
                throw ValidationException::withMessages(['serverId' => 'Select a server with the web role.']);
            }

            return $server;
        }
        if ($withWebsites->isEmpty()) {
            throw ValidationException::withMessages(['serverId' => 'This customer has no website. Make a website first: the websites on the same server use the database server.']);
        }

        // The server with the most websites of the customer.
        return Server::findOrFail($withWebsites->sortDesc()->keys()->first());
    }

    /** Changes the name, the plan or the limits. New limits restart the database server. */
    public function update(Request $request, string $id): JsonResponse
    {
        $server = $this->find($request, $id);
        $data = $request->validate([
            'name' => ['sometimes', 'string', 'regex:'.DatabaseServers::NAME],
            'planId' => ['sometimes', 'exists:plans,id'],
            ...self::limitRules(),
        ], ['name.regex' => 'Use 1 to 40 lower-case letters, digits and hyphens. Start and end with a letter or a digit.']);
        $user = $request->user();
        if (isset($data['limits']) && ! $user->isAdministrator()) {
            return response()->json(['message' => 'Only administrators can set resource limits.'], 403);
        }
        if (isset($data['planId']) && $data['planId'] !== $server->plan_id && ! WebsiteController::canChoosePlan($user, $server->customer, $data['planId'])) {
            return response()->json(['message' => 'Only an administrator or your reseller can change the plan.'], 403);
        }
        if (isset($data['name']) && $data['name'] !== $server->name && $server->customer->databaseServers()->where('name', $data['name'])->exists()) {
            throw ValidationException::withMessages(['name' => 'This customer has a database server with this name.']);
        }
        self::refuseRemoved($server);

        $previous = ['planId' => $server->plan_id, 'limits' => $server->limits];
        $changes = array_filter(['name' => $data['name'] ?? null], fn ($v) => $v !== null);
        $plan = null;
        if (isset($data['planId']) && $data['planId'] !== $server->plan_id) {
            $plan = Plan::findOrFail($data['planId']);
            // The database server must be in the limits of the new plan.
            if ($server->databases_count > $plan->databases) {
                throw ValidationException::withMessages(['planId' => "The {$plan->name} plan allows {$plan->databases} databases in a database server. This database server has {$server->databases_count}."]);
            }
            $changes['plan_id'] = $plan->id;
        }
        // The limits of the new plan, or the limits that an administrator sets.
        $limits = $data['limits'] ?? $plan?->database_limits;
        $resize = $limits !== null && $limits != $server->limits;
        if ($resize) {
            self::refuseOpenChange($server);
            $changes['limits'] = $limits;
        }
        $server->update($changes);

        $job = null;
        if ($resize) {
            // When the job fails, the panel goes back to the plan and the limits of "previous":
            // for example, the agent does not make a disk smaller than the data on it.
            $job = $this->jobs->dispatch($server->server, AgentAction::SetDatabaseServer, ['databaseServer' => $server->forAgent(), 'previous' => $previous], "Change database server {$server->name}", databaseServer: $server);
        }
        Audit::record($request, 'Changed database server', "{$server->customer->name} {$server->name}", $server->customer_id);

        return response()->json(['databaseServer' => self::present($server->fresh(['plan'])->loadCount('databases')), 'job' => $job ? new JobResource($job) : null]);
    }

    /**
     * Makes a database server again after its first job failed, for example because the server
     * could not pull the image. The job of the agent can run again.
     */
    public function retry(Request $request, string $id): JsonResponse
    {
        $server = $this->find($request, $id);
        if ($server->status !== DatabaseServer::FAILED) {
            throw ValidationException::withMessages(['databaseServer' => 'The panel made this database server. Only a database server that the panel could not make can be made again.']);
        }
        abort_if($server->customer->isSuspended(), 409, 'This customer is suspended. Lift the suspension before you make a database server.');
        $server->update(['status' => DatabaseServer::STARTING]);
        $job = $this->jobs->dispatch($server->server, AgentAction::SetDatabaseServer, ['databaseServer' => $server->forAgent(), 'create' => true], "Create database server {$server->name}", databaseServer: $server);
        Audit::record($request, 'Created database server again', "{$server->customer->name} {$server->name}", $server->customer_id);

        return response()->json(['databaseServer' => self::present($server), 'job' => new JobResource($job)], 202);
    }

    /** Removes a database server with no databases. The agent removes its data. */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $server = $this->find($request, $id);
        self::refuseRemoved($server);
        $databases = $server->databases_count;
        $users = $server->users()->count();
        if ($databases > 0 || $users > 0) {
            throw ValidationException::withMessages(['databaseServer' => $databases > 0
                ? "This database server has {$databases} ".($databases === 1 ? 'database' : 'databases').'. Remove the databases first.'
                : "This database server has {$users} database ".($users === 1 ? 'user' : 'users').'. Remove the database users first.']);
        }
        self::refuseOpenChange($server);
        // The panel removes the record when the job succeeds (JobOutcome). Until then, the
        // database server gets no new database.
        $previous = $server->status;
        $server->update(['status' => DatabaseServer::REMOVING]);
        $job = $this->jobs->dispatch($server->server, AgentAction::DeleteDatabaseServer, ['databaseServer' => $server->id, 'previousStatus' => $previous], "Remove database server {$server->name}", databaseServer: $server);
        Audit::record($request, 'Removed database server', "{$server->customer->name} {$server->name}", $server->customer_id);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    private static function refuseRemoved(DatabaseServer $server): void
    {
        if ($server->status === DatabaseServer::REMOVING) {
            throw ValidationException::withMessages(['databaseServer' => 'The panel removes this database server.']);
        }
    }

    /**
     * One change of the limits at a time: the panel goes back to the limits from before a
     * change that failed, and a second change in the same time would make that record wrong.
     */
    private static function refuseOpenChange(DatabaseServer $server): void
    {
        $open = AgentJob::where('database_server_id', $server->id)
            ->whereIn('action', [AgentAction::SetDatabaseServer->value, AgentAction::DeleteDatabaseServer->value])
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
        if ($open) {
            throw ValidationException::withMessages(['databaseServer' => 'A change of this database server is in progress. Wait until it is complete.']);
        }
    }

    /** Only an administrator sets limits directly. The other users select a plan. */
    private static function limitRules(): array
    {
        return [
            'limits' => ['sometimes', 'array'],
            'limits.vcpus' => ['required_with:limits', 'numeric', 'gt:0', 'max:1024'],
            'limits.memoryBytes' => ['required_with:limits', 'integer', 'min:'.DatabaseServer::MIN_MEMORY],
            'limits.diskBytes' => ['required_with:limits', 'integer', 'min:'.DatabaseServer::MIN_DISK],
        ];
    }

    public static function present(DatabaseServer $s): array
    {
        return [
            'id' => $s->id,
            'name' => $s->name,
            'engine' => $s->engine,
            'customerId' => $s->customer_id,
            'serverId' => $s->server_id,
            'planId' => $s->plan_id,
            'status' => $s->status,
            // A website container of the customer on the same server connects to this host
            // name and this port.
            'host' => $s->host(),
            'port' => $s->port,
            'limits' => $s->limits,
            // The last report of the agent: "memoryBytes", "diskBytes" and "diskSizeBytes".
            'usage' => $s->usage,
            'databases' => $s->databases_count ?? $s->databases()->count(),
            // The number of databases that the plan of the database server allows.
            'maxDatabases' => $s->plan->databases,
            'createdAt' => $s->created_at?->toIso8601String(),
        ];
    }
}
