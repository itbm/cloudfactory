<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\Plan;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;

/**
 * FR-202: plans set the limits for CPU, memory, disk, disk I/O and processes of a website, and
 * the limits for CPU, memory and disk of a database server. A plan also gives the numbers of
 * websites and of database servers of a customer, and the number of databases of each
 * database server.
 */
class PlanController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    /**
     * Audit batch 3, finding 5: an administrator sees all plans. A reseller sees the plans that
     * it can give, and the plans of its customers. An owner sees only its own plan.
     */
    public function index(Request $request): JsonResponse
    {
        $user = $request->user();
        $plans = Plan::orderBy('name');
        if (! $user->isAdministrator()) {
            $customer = $user->customer;
            $ids = $customer ? $customer->allowedPlanIds() : [];
            if ($customer && $user->role === Role::Reseller) {
                $ids = [...$ids, ...Customer::where('reseller_id', $customer->id)->pluck('plan_id')->all()];
            } elseif ($customer) {
                $ids = [$customer->plan_id];
            }
            $plans->whereIn('id', array_unique($ids));
        }

        return response()->json(['data' => $plans->get()->map(fn (Plan $p) => self::present($p))]);
    }

    public function store(Request $request): JsonResponse
    {
        $plan = Plan::create(self::validated($request));
        Audit::record($request, 'Created plan', $plan->name);

        return response()->json(self::present($plan), 201);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $plan = Plan::findOrFail($id);
        $oldLimits = $plan->limits;
        $oldDatabaseLimits = $plan->database_limits;
        $new = self::validated($request);
        // The database servers that get the new limits: those with the limits of the plan. A
        // database server with limits that an administrator set for it keeps them.
        $databaseServers = isset($new['database_limits']) && $new['database_limits'] != $oldDatabaseLimits
            ? $plan->databaseServers()->with('server')->where('status', '!=', DatabaseServer::REMOVING)->get()->filter(fn (DatabaseServer $s) => $s->limits == $oldDatabaseLimits)
            : collect();
        // One change of the limits of a database server at a time (DatabaseServerController).
        $busy = AgentJob::whereIn('database_server_id', $databaseServers->pluck('id'))->where('action', AgentAction::SetDatabaseServer->value)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->value('database_server_id');
        if ($busy) {
            throw ValidationException::withMessages(['databaseLimits' => "A change of the database server {$databaseServers->firstWhere('id', $busy)->name} is in progress. Wait until it is complete, then change the limits of the plan."]);
        }
        $plan->update($new);
        Audit::record($request, 'Changed plan', $plan->name);
        // Batch 1, finding 8: the websites of the plan get the new limits and extensions. A
        // website with limits that an administrator set for it keeps them.
        foreach ($plan->websites()->with(['image', 'domains', 'server'])->get() as $website) {
            self::applyPlan($this->jobs, $website, $plan, $website->limits == $oldLimits);
        }
        // The database servers of the plan get the new limits in the same way. The agent
        // restarts a database server to apply them.
        foreach ($databaseServers as $server) {
            self::applyDatabaseLimits($this->jobs, $server, $plan);
        }

        return response()->json(self::present($plan));
    }

    private static function validated(Request $request): array
    {
        $d = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'websites' => ['required', 'integer', 'min:1', 'max:1000000'],
            'mailboxes' => ['required', 'integer', 'min:0', 'max:1000000'],
            'databases' => ['required', 'integer', 'min:0', 'max:1000000'],
            'phpExtensions' => ['required', 'array'],
            'phpExtensions.*' => ['string', 'regex:/^[a-z0-9_]+$/'],
            'limits.vcpus' => ['required', 'numeric', 'gt:0'],
            'limits.memoryBytes' => ['required', 'integer', 'gt:0'],
            'limits.diskBytes' => ['required', 'integer', 'gt:0'],
            'limits.diskIoBytesPerSecond' => ['required', 'integer', 'gt:0'],
            'limits.processes' => ['required', 'integer', 'gt:0'],
            // The number of database servers of a customer with the plan, and the limits of
            // each database server with the plan. A request with none keeps the values of the
            // plan. A new plan gets 3 database servers, each with the vCPUs of a website, the
            // half of its memory and the half of its disk space.
            'databaseServers' => ['sometimes', 'integer', 'min:0', 'max:1000000'],
            'databaseLimits' => ['sometimes', 'array'],
            'databaseLimits.vcpus' => ['required_with:databaseLimits', 'numeric', 'gt:0', 'max:1024'],
            'databaseLimits.memoryBytes' => ['required_with:databaseLimits', 'integer', 'min:'.DatabaseServer::MIN_MEMORY],
            'databaseLimits.diskBytes' => ['required_with:databaseLimits', 'integer', 'min:'.DatabaseServer::MIN_DISK],
        ], [
            'databaseLimits.memoryBytes.min' => 'A database server needs 512 MB of memory or more.',
            'databaseLimits.diskBytes.min' => 'A database server needs 1 GB of disk space or more.',
        ]);

        return [
            'name' => $d['name'],
            'websites' => $d['websites'],
            'mailboxes' => $d['mailboxes'],
            'databases' => $d['databases'],
            'php_extensions' => $d['phpExtensions'],
            'limits' => $d['limits'],
            ...(isset($d['databaseServers']) ? ['database_servers' => $d['databaseServers']] : []),
            ...(isset($d['databaseLimits']) ? ['database_limits' => [
                'vcpus' => (float) $d['databaseLimits']['vcpus'],
                'memoryBytes' => (int) $d['databaseLimits']['memoryBytes'],
                'diskBytes' => (int) $d['databaseLimits']['diskBytes'],
            ]] : []),
        ];
    }

    /**
     * Gives a database server the limits of its plan, and sends the change to the agent. When
     * the job fails, the database server keeps its limits (JobOutcome): for example, the agent
     * does not make a disk smaller than the data on it.
     */
    public static function applyDatabaseLimits(JobDispatcher $jobs, DatabaseServer $server, Plan $plan): void
    {
        $previous = ['planId' => $server->plan_id, 'limits' => $server->limits];
        $server->update(['limits' => $plan->database_limits]);
        $jobs->dispatch($server->server, AgentAction::SetDatabaseServer, ['databaseServer' => $server->forAgent(), 'previous' => $previous], "Apply the limits of the plan to {$server->name}", databaseServer: $server);
    }

    /**
     * Gives a website the limits and the extensions of its plan, and sends the changes to the
     * agent. A website keeps only the extensions that the plan has.
     */
    public static function applyPlan(JobDispatcher $jobs, Website $website, Plan $plan, bool $limits = true): void
    {
        $extensions = array_values(array_intersect($website->extensions, $plan->php_extensions));
        $limitsChanged = $limits && $website->limits != $plan->limits;
        $extensionsChanged = $extensions !== array_values($website->extensions);
        $website->update(array_filter([
            'limits' => $limitsChanged ? $plan->limits : null,
            'extensions' => $extensionsChanged ? $extensions : null,
        ], fn ($v) => $v !== null));
        if ($limitsChanged) {
            $jobs->dispatch($website->server, AgentAction::SetLimits, ['websiteId' => $website->id, 'unixUser' => $website->unix_user, 'limits' => $website->limits], 'Apply resource limits', $website);
        }
        if ($extensionsChanged) {
            $jobs->dispatch($website->server, AgentAction::SetRuntime, JobDispatcher::websitePayload($website) + [
                'previousImageId' => $website->image_id,
                'previousTag' => $website->currentTag(),
            ], 'Apply PHP extensions of the plan', $website);
        }
    }

    public static function present(Plan $p): array
    {
        return [
            'id' => $p->id,
            'name' => $p->name,
            'limits' => $p->limits,
            'websites' => $p->websites,
            'mailboxes' => $p->mailboxes,
            // The number of databases of each database server with the plan.
            'databases' => $p->databases,
            'phpExtensions' => $p->php_extensions,
            // The number of database servers of a customer with the plan.
            'databaseServers' => $p->database_servers,
            // The limits of each database server with the plan.
            'databaseLimits' => $p->database_limits,
        ];
    }
}
