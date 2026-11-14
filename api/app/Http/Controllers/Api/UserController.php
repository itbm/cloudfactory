<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Customer;
use App\Models\User;
use App\Services\Audit;
use App\Services\LoginThrottle;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;

/**
 * Audit finding B5: the users who can sign in. An administrator makes users for all customers
 * and makes more administrators. A reseller makes users for its own customers.
 *
 * The panel sends no email. A new user gets a temporary password, which the panel shows one
 * time to the person who made the user. The user must set a new password at the first sign-in.
 */
class UserController extends Controller
{
    public function index(Request $request): JsonResponse
    {
        $ids = $request->user()->visibleCustomerIds();

        return response()->json(['data' => User::query()
            ->when($ids !== null, fn ($q) => $q->whereIn('customer_id', $ids))
            ->orderBy('name')->orderBy('id')->get()->map(fn (User $u) => self::present($u))]);
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'max:200'],
            'email' => ['required', 'email', 'max:255', Rule::unique('users', 'email')],
            // Null makes an administrator.
            'customerId' => ['present', 'nullable', 'string', 'max:26'],
        ]);
        $customer = $data['customerId'] === null ? null : Customer::find($data['customerId']);
        if ($response = self::refuse($request, $customer, $data['customerId'] !== null)) {
            return $response;
        }
        if ($customer === null) {
            AuthController::confirmIdentity($request);
        }
        [$user, $password] = self::make($data['name'], $data['email'], $customer);
        Audit::record($request, $customer === null ? 'Created administrator' : 'Created user', $user->email, $customer?->id);

        return response()->json(self::present($user) + ['temporaryPassword' => $password], 201);
    }

    /**
     * Gives the user a new temporary password and signs the user out. With removeTotp, the user
     * can sign in with the password only, for example after the loss of a telephone.
     */
    public function resetPassword(Request $request, int $id): JsonResponse
    {
        $data = $request->validate(['removeTotp' => ['sometimes', 'boolean']]);
        $target = $this->find($request, $id);
        if ($target->id === $request->user()->id) {
            return response()->json(['message' => 'Change your own password in the settings.'], 403);
        }
        if ($response = self::refuse($request, $target->customer, $target->customer_id !== null)) {
            return $response;
        }
        $administrator = $target->customer_id === null;
        if ($administrator) {
            AuthController::confirmIdentity($request);
        }
        $password = self::reset($target, (bool) ($data['removeTotp'] ?? false));
        $action = ($data['removeTotp'] ?? false) ? 'Reset password and removed TOTP' : 'Reset password';
        Audit::record($request, $administrator ? "{$action} of administrator" : $action, $target->email, $target->customer_id);

        return response()->json(self::present($target->fresh()) + ['temporaryPassword' => $password]);
    }

    public function destroy(Request $request, int $id): JsonResponse
    {
        $target = $this->find($request, $id);
        if ($target->id === $request->user()->id) {
            return response()->json(['message' => 'You cannot delete your own user.'], 403);
        }
        if ($response = self::refuse($request, $target->customer, $target->customer_id !== null)) {
            return $response;
        }
        if ($target->customer_id === null) {
            AuthController::confirmIdentity($request);
        }
        DB::transaction(function () use ($target) {
            $target->tokens()->delete();
            $target->delete();
        });
        Audit::record($request, $target->customer_id === null ? 'Deleted administrator' : 'Deleted user', $target->email, $target->customer_id);

        return response()->json(null, 204);
    }

    /**
     * Makes a user with a temporary password. The role comes from the customer: a user of a
     * reseller is a reseller, and a user with no customer is an administrator.
     *
     * @return array{0: User, 1: string}
     */
    public static function make(string $name, string $email, ?Customer $customer): array
    {
        $password = self::temporaryPassword();
        $user = new User(['name' => $name, 'email' => $email, 'password' => $password]);
        $user->forceFill([
            'role' => $customer === null ? Role::Administrator : ($customer->role === 'reseller' ? Role::Reseller : Role::Owner),
            'customer_id' => $customer?->id,
            'must_change_password' => true,
        ])->save();

        return [$user, $password];
    }

    /** Sets a new temporary password, removes all sessions and tokens, and removes the sign-in locks. */
    public static function reset(User $user, bool $removeTotp): string
    {
        $password = self::temporaryPassword();
        $user->forceFill(['password' => $password, 'must_change_password' => true] + ($removeTotp ? ['totp_secret' => null, 'totp_last_step' => null] : []))->save();
        $user->tokens()->delete();
        LoginThrottle::unlock($user->email);

        return $password;
    }

    private static function temporaryPassword(): string
    {
        return Str::password(20, symbols: false);
    }

    /**
     * The rules for a change to the users of a customer, or to the administrators when the
     * customer is null.
     *
     * - A website owner cannot change users.
     * - A reseller changes only the users of its own customers, not of its own account.
     * - Only an administrator in a sign-in session changes administrators. An API token with
     *   the users scope cannot, because a new administrator has all rights. Audit finding F09:
     *   the administrator also gives the password and, when TOTP is on, a code
     *   (AuthController::confirmIdentity).
     */
    private static function refuse(Request $request, ?Customer $customer, bool $forCustomer): ?JsonResponse
    {
        $user = $request->user();
        if ($forCustomer && $customer === null) {
            return response()->json(['message' => 'The customer does not exist.', 'errors' => ['customerId' => ['The customer does not exist.']]], 422);
        }
        if ($user->role === Role::Owner) {
            return response()->json(['message' => 'Website owners cannot change users.'], 403);
        }
        if ($user->role === Role::Reseller) {
            if ($customer === null || $customer->reseller_id !== $user->customer_id) {
                return response()->json(['message' => 'A reseller can change only the users of its own customers.'], 403);
            }

            return null;
        }
        if ($customer === null && ! AuthController::isSession($request)) {
            return response()->json(['message' => 'Sign in to the panel to change administrators. An API token cannot change them.'], 403);
        }

        return null;
    }

    private function find(Request $request, int $id): User
    {
        $target = User::findOrFail($id);
        $ids = $request->user()->visibleCustomerIds();
        abort_unless($ids === null || in_array($target->customer_id, $ids, true), 404);

        return $target;
    }

    public static function present(User $u): array
    {
        return [
            'id' => $u->id,
            'name' => $u->name,
            'email' => $u->email,
            'role' => $u->role->value,
            'customerId' => $u->customer_id,
            'totpEnabled' => $u->totp_secret !== null,
            'mustChangePassword' => (bool) $u->must_change_password,
            'createdAt' => $u->created_at?->toIso8601String(),
        ];
    }
}
