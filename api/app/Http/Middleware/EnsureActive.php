<?php

namespace App\Http\Middleware;

use App\Enums\Role;
use App\Http\Controllers\Api\AuthController;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * CF-08: refuse each request from a user of a suspended customer, including the customers of a
 * suspended reseller.
 *
 * Audit finding B5: a user with a temporary password can only read its own account, set a new
 * password and sign out. The person who made the user knows the temporary password.
 */
class EnsureActive
{
    /** The routes that a user with a temporary password can use. */
    private const WITH_TEMPORARY_PASSWORD = ['api/v1/me', 'api/v1/me/password', 'api/v1/auth/logout'];

    /** The routes that an administrator or a reseller can use before TOTP is on. */
    private const BEFORE_TOTP = ['api/v1/me', 'api/v1/me/password', 'api/v1/me/totp/setup', 'api/v1/me/totp/enable', 'api/v1/auth/logout'];

    public function handle(Request $request, Closure $next): Response
    {
        if ($request->user()?->isSuspended()) {
            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }
        if ($request->user()?->must_change_password && ! in_array($request->route()?->uri(), self::WITH_TEMPORARY_PASSWORD, true)) {
            return response()->json(['message' => 'Set a new password first. Your password is temporary.', 'code' => 'password_change_required'], 403);
        }
        if (self::totpSetupRequired($request) && ! in_array($request->route()?->uri(), self::BEFORE_TOTP, true)) {
            return response()->json(['message' => 'Set up TOTP first. Administrators and resellers sign in with a password and a code from an authenticator app.', 'code' => 'totp_setup_required'], 403);
        }

        return $next($request);
    }

    /**
     * Audit finding F09: an administrator or a reseller cannot work in a sign-in session before
     * TOTP is on. With a password only, one stolen password gives all the rights of the account.
     *
     * The rule is for sign-in sessions. An API token has its own scopes and expiry date, and a
     * user makes a token only with the password and, when TOTP is on, a code.
     */
    public static function totpSetupRequired(Request $request): bool
    {
        $user = $request->user();

        return $user !== null
            && config('cloudfactory.require_totp', true)
            && in_array($user->role, [Role::Administrator, Role::Reseller], true)
            && $user->totp_secret === null
            && AuthController::isSession($request);
    }
}
