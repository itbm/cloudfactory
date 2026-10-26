<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Services\Audit;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\Rule;

/** FR-102 and FR-106: resellers make website owners, set their limits and set their brand. */
class CustomerController extends Controller
{
    public function index(Request $request): JsonResponse
    {
        $ids = $request->user()->visibleCustomerIds();

        return response()->json(['data' => Customer::withCount('websites')
            ->when($ids !== null, fn ($q) => $q->whereIn('id', $ids))
            ->orderBy('name')->get()->map(fn (Customer $c) => self::present($c))]);
    }

    public function store(Request $request): JsonResponse
    {
        $user = $request->user();
        if ($user->role === Role::Owner) {
            return response()->json(['message' => 'Website owners cannot add customers.'], 403);
        }
        $data = $request->validate([
            'name' => ['required', 'string', 'max:200'],
            'email' => ['required', 'email'],
            // A reseller can add website owners only.
            'role' => ['required', Rule::in($user->isAdministrator() ? ['reseller', 'owner'] : ['owner'])],
            'planId' => ['required', 'exists:plans,id'],
        ]);
        $customer = Customer::create([
            'name' => $data['name'],
            'email' => $data['email'],
            'role' => $data['role'],
            'plan_id' => $data['planId'],
            'reseller_id' => $user->role === Role::Reseller ? $user->customer_id : null,
        ]);
        Audit::record($request, 'Created customer', $customer->name, $customer->id);

        return response()->json(self::present($customer), 201);
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $customer = $this->find($request, $id);
        $data = $request->validate([
            'name' => ['sometimes', 'string', 'max:200'],
            'planId' => ['sometimes', 'exists:plans,id'],
            'suspended' => ['sometimes', 'boolean'],
            'branding' => ['sometimes', 'array'],
            'branding.name' => ['sometimes', 'string', 'max:100'],
            'branding.colour' => ['sometimes', 'regex:/^#[0-9a-fA-F]{6}$/'],
            'branding.panelDomain' => ['sometimes', 'regex:'.WebsiteController::DOMAIN],
            'branding.supportEmail' => ['sometimes', 'email'],
        ]);
        if ($customer->id === $request->user()->customer_id && (isset($data['planId']) || isset($data['suspended']))) {
            return response()->json(['message' => 'You cannot change your own plan or status.'], 403);
        }
        $customer->update(array_filter([
            'name' => $data['name'] ?? null,
            'plan_id' => $data['planId'] ?? null,
            'suspended' => $data['suspended'] ?? null,
            'branding' => isset($data['branding']) ? [...($customer->branding ?? []), ...$data['branding']] : null,
        ], fn ($v) => $v !== null));
        Audit::record($request, 'Changed customer', $customer->name, $customer->id);

        return response()->json(self::present($customer->fresh()));
    }

    private function find(Request $request, string $id): Customer
    {
        $customer = Customer::findOrFail($id);
        abort_unless($request->user()->canSeeCustomer($customer->id), 404);

        return $customer;
    }

    public static function present(Customer $c): array
    {
        return [
            'id' => $c->id,
            'name' => $c->name,
            'email' => $c->email,
            'role' => $c->role,
            'planId' => $c->plan_id,
            'resellerId' => $c->reseller_id,
            'branding' => $c->branding,
            'suspended' => $c->suspended,
            'websites' => $c->websites_count ?? null,
            'createdAt' => $c->created_at?->toIso8601String(),
        ];
    }
}
