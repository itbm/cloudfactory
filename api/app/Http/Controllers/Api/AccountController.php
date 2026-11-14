<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\Database;
use App\Models\User;
use App\Models\Website;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * The account of the signed-in user: the customer, its plan, how much of the plan it uses, who
 * gives the service, and who can sign in. An owner sees this instead of the list of customers
 * and plans, which is for resellers and administrators.
 */
class AccountController extends Controller
{
    public function show(Request $request): JsonResponse
    {
        $user = $request->user();
        $customer = $user->customer;
        if (! $customer) {
            // An administrator belongs to no customer.
            return response()->json(['customer' => null, 'provider' => null, 'plan' => null, 'usage' => null, 'backups' => self::backups(), 'users' => []]);
        }
        $websites = Website::where('customer_id', $customer->id)->get(['id', 'usage']);
        $reseller = $customer->reseller;

        return response()->json([
            'customer' => [
                'id' => $customer->id,
                'name' => $customer->name,
                'email' => $customer->email,
                'role' => $customer->role,
                'suspended' => $customer->isSuspended(),
                'createdAt' => $customer->created_at?->toIso8601String(),
            ],
            // Who gives the service to this customer. Null: the operator of the panel.
            'provider' => $reseller ? ['name' => $reseller->name, 'email' => $reseller->email] : null,
            'plan' => $customer->plan ? PlanController::present($customer->plan) : null,
            'usage' => [
                'websites' => $websites->count(),
                'databases' => Database::whereIn('website_id', $websites->pluck('id'))->count(),
                'diskBytes' => (int) $websites->sum(fn (Website $w) => $w->usage['diskBytes'] ?? 0),
                // Only for a reseller: the number of its customers.
                'customers' => $user->role === Role::Reseller ? Customer::where('reseller_id', $customer->id)->count() : null,
            ],
            'backups' => self::backups(),
            'users' => User::where('customer_id', $customer->id)->orderBy('name')->get()->map(fn (User $u) => UserController::present($u)),
        ]);
    }

    /**
     * Whether the panel can make backups. A customer does not see the object storage, but must
     * know when a backup can only fail.
     *
     * @return array{available: bool}
     */
    private static function backups(): array
    {
        $target = (string) (BackupController::policy()['target'] ?? '');

        return ['available' => preg_match(BackupController::TARGET, $target) === 1 && BackupController::credentials() !== []];
    }
}
