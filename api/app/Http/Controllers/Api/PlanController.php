<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\Plan;
use App\Models\Website;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/** FR-202: plans set the limits for CPU, memory, disk, disk I/O and processes. */
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
        $plan->update(self::validated($request));
        Audit::record($request, 'Changed plan', $plan->name);
        // Batch 1, finding 8: the websites of the plan get the new limits and extensions. A
        // website with limits that an administrator set for it keeps them.
        foreach ($plan->websites()->with(['image', 'domains', 'server'])->get() as $website) {
            self::applyPlan($this->jobs, $website, $plan, $website->limits == $oldLimits);
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
        ]);

        return [
            'name' => $d['name'],
            'websites' => $d['websites'],
            'mailboxes' => $d['mailboxes'],
            'databases' => $d['databases'],
            'php_extensions' => $d['phpExtensions'],
            'limits' => $d['limits'],
        ];
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
            'databases' => $p->databases,
            'phpExtensions' => $p->php_extensions,
        ];
    }
}
