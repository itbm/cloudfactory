<?php

use App\Http\Middleware\AuthenticateAgent;
use App\Http\Middleware\EnsureActive;
use App\Http\Middleware\EnsureAdministrator;
use App\Http\Middleware\EnsureScope;
use Illuminate\Database\Eloquent\ModelNotFoundException;
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Exceptions\ThrottleRequestsException;
use Illuminate\Http\Request;
use Symfony\Component\HttpKernel\Exception\NotFoundHttpException;

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
        // The panel has no sign-in page in the API: the web panel is a different container. By
        // default, the framework sends a guest to the route "login". That route does not exist,
        // so a request with no token and no "Accept: application/json" header (for example from
        // curl) got status 500 and an exception in the log. Now it gets status 401.
        $middleware->redirectGuestsTo(null);
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
        // Audit finding F10: the default message for a record that the user cannot see is
        // "No query results for model [App\Models\Website] <ID>", which shows the names of
        // internal classes.
        $exceptions->render(function (NotFoundHttpException $e) {
            if ($e->getPrevious() instanceof ModelNotFoundException) {
                return response()->json(['message' => 'Not found.'], 404);
            }
        });
        // A rate limit: the default message is "Too Many Attempts.", which does not tell the
        // user what to do. The web panel shows this message, for example on the sign-in page.
        $exceptions->render(function (ThrottleRequestsException $e) {
            $seconds = max(1, (int) ($e->getHeaders()['Retry-After'] ?? 60));

            return response()->json(['message' => "Too many requests. Try again in {$seconds} seconds."], 429, $e->getHeaders());
        });
    })->create();
