<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Plan;
use App\Services\Audit;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/** FR-202: plans set the limits for CPU, memory, disk, disk I/O and processes. */
class PlanController extends Controller
{
    public function index(): JsonResponse
    {
        return response()->json(['data' => Plan::orderBy('name')->get()->map(fn (Plan $p) => self::present($p))]);
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
        $plan->update(self::validated($request));
        Audit::record($request, 'Changed plan', $plan->name);

        return response()->json(self::present($plan));
    }

    private static function validated(Request $request): array
    {
        $d = $request->validate([
            'name' => ['required', 'string', 'max:100'],
            'websites' => ['required', 'integer', 'min:1'],
            'mailboxes' => ['required', 'integer', 'min:0'],
            'databases' => ['required', 'integer', 'min:0'],
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
