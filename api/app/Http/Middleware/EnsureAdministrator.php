<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/** Only administrators can see servers, images and updates (spec section 9, screen 3). */
class EnsureAdministrator
{
    public function handle(Request $request, Closure $next): Response
    {
        if (! $request->user()?->isAdministrator()) {
            return response()->json(['message' => 'Only administrators can do this.'], 403);
        }

        return $next($request);
    }
}
