<?php

use App\Http\Middleware\AuthenticateAgent;
use App\Http\Middleware\EnsureActive;
use App\Http\Middleware\EnsureAdministrator;
use App\Http\Middleware\EnsureScope;
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Request;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
        api: __DIR__.'/../routes/api.php',
        commands: __DIR__.'/../routes/console.php',
        health: '/up',
    )
    ->withMiddleware(function (Middleware $middleware): void {
        $middleware->alias([
            'scope' => EnsureScope::class,
            'admin' => EnsureAdministrator::class,
            'agent' => AuthenticateAgent::class,
            'active' => EnsureActive::class,
        ]);
        // Audit batch 3, finding 1: all requests come through the edge proxy, which sets
        // X-Forwarded-For to the address of the client. The API ports are open only on the
        // service address of the host (CF-10), so only the edge proxy and the local agent can
        // connect. The agent checks (AuthenticateAgent) use the client certificate, not the
        // address.
        $middleware->trustProxies(
            at: array_filter(explode(',', (string) env('CF_TRUSTED_PROXIES', '127.0.0.1,::1,169.254.0.0/16'))),
            headers: Request::HEADER_X_FORWARDED_FOR | Request::HEADER_X_FORWARDED_PROTO,
        );
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        $exceptions->shouldRenderJsonWhen(
            fn (Request $request) => $request->is('api/*') || $request->expectsJson(),
        );
    })->create();
