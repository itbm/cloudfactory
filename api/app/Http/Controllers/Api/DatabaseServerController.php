<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\WebsiteStatus;
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
use App\Services\Mesh;
use App\Services\Networks;
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
            'name' => ['sometimes', 'nullable', 'string', 'regex:'.DatabaseServers::NAME, Rule::notIn(Networks::RESERVED)],
            'customerId' => ['nullable', 'exists:customers,id'],
            'planId' => ['nullable', 'exists:plans,id'],
            'serverId' => ['nullable', 'exists:servers,id'],
            ...self::limitRules(),
        ], ['name.regex' => 'Use 1 to 40 lower-case letters, digits and hyphens. Start and end with a letter or a digit.', 'name.not_in' => 'Each website has this name for the database servers of its engine. Use a different name, for example :input-1.']);
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
        self::refuseWebsiteName($customer, $name);
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
            'name' => ['sometimes', 'string', 'regex:'.DatabaseServers::NAME, Rule::notIn(Networks::RESERVED)],
            'planId' => ['sometimes', 'exists:plans,id'],
            ...self::limitRules(),
        ], ['name.regex' => 'Use 1 to 40 lower-case letters, digits and hyphens. Start and end with a letter or a digit.', 'name.not_in' => 'Each website has this name for the database servers of its engine. Use a different name, for example :input-1.']);
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
        if (isset($data['name']) && $data['name'] !== $server->name) {
            self::refuseWebsiteName($server->customer, $data['name']);
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

    /**
     * Moves a database server to a different server. Only an administrator can do this. The
     * database server keeps its ID, its name, its databases, its users and its network
     * address: the websites that use the network name continue to connect to it.
     *
     * 1. The new server makes the database server, and waits for the data.
     * 2. The old server stops the database server, and sends the data through the tunnel.
     * 3. The new server starts the database server. Then the panel records the new server,
     *    and the old server removes its copy (JobOutcome).
     *
     * The database server does not operate while its data goes to the new server.
     */
    public function move(Request $request, string $id): JsonResponse
    {
        abort_unless($request->user()->isAdministrator(), 403, 'Only an administrator can move a database server.');
        $data = $request->validate(['serverId' => ['required', 'string', 'exists:servers,id']]);
        $databaseServer = $this->find($request, $id);
        $to = Server::findOrFail($data['serverId']);
        $from = $databaseServer->server;
        self::refuseRemoved($databaseServer);
        if (! in_array($databaseServer->status, [DatabaseServer::RUNNING, DatabaseServer::STOPPED], true)) {
            throw ValidationException::withMessages(['databaseServer' => 'Only a database server that the panel made can move. Make it again first.']);
        }
        if ($to->id === $from->id) {
            throw ValidationException::withMessages(['serverId' => "The database server is on the server {$to->name}. Select a different server."]);
        }
        if (! in_array('web', $to->roles, true)) {
            throw ValidationException::withMessages(['serverId' => 'Select a server with the web role.']);
        }
        if ($to->state !== 'healthy' || $from->state !== 'healthy') {
            throw ValidationException::withMessages(['serverId' => 'The two servers must be healthy. Wait until the agents report again.']);
        }
        DatabaseServers::checkHost($to, $databaseServer->engine, 'serverId');
        foreach ([$from, $to] as $server) {
            if (! Networks::supports($server) || ! $server->mesh_moves || Mesh::tunnelAddress($server) === null) {
                throw ValidationException::withMessages(['serverId' => "The agent on the server {$server->name} cannot move database servers. Update the agent of the server, then try again."]);
            }
        }
        if (Networks::hostOf($databaseServer) === null) {
            throw ValidationException::withMessages(['databaseServer' => "A database server with the name {$databaseServer->name} has no network name, so the websites on a different server cannot connect to it. Give it a different name first."]);
        }
        Networks::checkMove($databaseServer, $to);
        // The check of the networks uses the server of each website. A website that moves
        // changes its server at the end of its move.
        $movingWebsite = Website::where('status', WebsiteStatus::Moving->value)
            ->whereIn('id', Database::where('database_server_id', $databaseServer->id)->whereNotNull('website_id')->select('website_id'))->orderBy('name')->first();
        if ($movingWebsite) {
            throw ValidationException::withMessages(['databaseServer' => "The website {$movingWebsite->name} has a database in this database server, and it moves to a different server now. Wait until the move is complete."]);
        }
        self::refuseOpenChange($databaseServer);
        $port = DatabaseServers::freePort($databaseServer->customer_id, $to->id, $databaseServer->engine);
        if ($port === null) {
            throw ValidationException::withMessages(['serverId' => "The customer has the maximum number of database servers of this engine on the server {$to->name}."]);
        }

        // The secret of this move. Only the two agents get it.
        $token = bin2hex(random_bytes(32));
        $databaseServer->update(['status' => DatabaseServer::MOVING, 'moving_to_server_id' => $to->id]);
        // The tunnel between the two servers. A refresh starts the jobs that make it.
        $fromAddress = Mesh::addressFor($from);
        $toAddress = Mesh::addressFor($to);
        Mesh::refreshLater();
        $receive = $this->jobs->dispatch($to, AgentAction::ReceiveDatabaseServer, [
            'databaseServer' => [...$databaseServer->forAgent(), 'port' => $port],
            'from' => $fromAddress,
            'token' => $token,
            'timeoutSeconds' => self::MOVE_TIMEOUT,
            'fromServerId' => $from->id,
        ], "Receive database server {$databaseServer->name} from {$from->name}", databaseServer: $databaseServer);
        $send = $this->jobs->dispatch($from, AgentAction::SendDatabaseServer, [
            'databaseServer' => $databaseServer->id,
            'engine' => $databaseServer->engine,
            'to' => $toAddress,
            'token' => $token,
        ], "Send database server {$databaseServer->name} to {$to->name}", databaseServer: $databaseServer);
        Audit::record($request, 'Moved database server', "{$databaseServer->customer->name} {$databaseServer->name} from {$from->name} to {$to->name}", $databaseServer->customer_id);

        return response()->json([
            'databaseServer' => self::present($databaseServer->fresh(['plan'])->loadCount('databases')),
            'jobs' => [new JobResource($receive), new JobResource($send)],
        ], 202);
    }

    /** The time in seconds in which the old server must connect to the new server. */
    public const MOVE_TIMEOUT = 900;

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
        if ($server->status === DatabaseServer::MOVING) {
            throw ValidationException::withMessages(['databaseServer' => 'This database server moves to a different server now. Wait until the move is complete.']);
        }
    }

    /**
     * One change of the limits at a time: the panel goes back to the limits from before a
     * change that failed, and a second change in the same time would make that record wrong.
     */
    private static function refuseOpenChange(DatabaseServer $server): void
    {
        $open = AgentJob::where('database_server_id', $server->id)
            ->whereIn('action', [AgentAction::SetDatabaseServer->value, AgentAction::DeleteDatabaseServer->value, AgentAction::SendDatabaseServer->value, AgentAction::ReceiveDatabaseServer->value])
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
            // The server that receives the database server while its status is "moving".
            'movingToServerId' => $s->moving_to_server_id,
            'planId' => $s->plan_id,
            'status' => $s->status,
            // A website container of the customer on the same server connects to this host
            // name and this port.
            'host' => $s->host(),
            'port' => $s->port,
            // Each website that is in a network with the database server connects to this
            // host name and this port, on the same server and on a different server. Null for
            // a database server with a name that an engine uses.
            'networkHost' => Networks::hostOf($s),
            'networkPort' => Networks::portOf($s->engine),
            'limits' => $s->limits,
            // The last report of the agent: "memoryBytes", "diskBytes" and "diskSizeBytes".
            'usage' => $s->usage,
            'databases' => $s->databases_count ?? $s->databases()->count(),
            // The number of databases that the plan of the database server allows.
            'maxDatabases' => $s->plan->databases,
            'createdAt' => $s->created_at?->toIso8601String(),
        ];
    }

    /**
     * A website of the customer is a network member with a name from its Unix user. A
     * database server cannot have the same name.
     *
     * @throws ValidationException
     */
    private static function refuseWebsiteName(Customer $customer, ?string $name): void
    {
        if ($name !== null && $customer->websites()->where('unix_user', str_replace('-', '_', $name))->exists()) {
            throw ValidationException::withMessages(['name' => "A website of this customer has the network name {$name}".Networks::DOMAIN.'. Use a different name.']);
        }
    }
}
