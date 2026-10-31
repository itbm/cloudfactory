<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * FR-104: check the scope of the API token. A GET or HEAD request needs "<area>:read".
 * Other methods need "<area>:write". A token with the "*" ability has all scopes.
 */
class EnsureScope
{
    public function handle(Request $request, Closure $next, string $area): Response
    {
        $ability = $area.($request->isMethodSafe() ? ':read' : ':write');
        $token = $request->user()?->currentAccessToken();

        // A write scope also gives read access.
        $allowed = $token && ($token->can($ability) || ($request->isMethodSafe() && $token->can($area.':write')));
        if (! $allowed) {
            return response()->json(['message' => "The token does not have the {$ability} scope."], 403);
        }

        return $next($request);
    }
}
