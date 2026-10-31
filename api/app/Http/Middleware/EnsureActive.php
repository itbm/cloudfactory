<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * CF-08: refuse each request from a user of a suspended customer, including the customers of a
 * suspended reseller.
 */
class EnsureActive
{
    public function handle(Request $request, Closure $next): Response
    {
        if ($request->user()?->isSuspended()) {
            return response()->json(['message' => 'This account is suspended. Contact your provider.'], 403);
        }

        return $next($request);
    }
}
