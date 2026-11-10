<?php

namespace App\Http\Middleware;

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

    public function handle(Request $request, Closure $next): Response
    {
        if ($request->user()?->isSuspended()) {
            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }
        if ($request->user()?->must_change_password && ! in_array($request->route()?->uri(), self::WITH_TEMPORARY_PASSWORD, true)) {
            return response()->json(['message' => 'Set a new password first. Your password is temporary.', 'code' => 'password_change_required'], 403);
        }

        return $next($request);
    }
}
