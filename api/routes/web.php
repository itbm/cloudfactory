<?php

use App\Providers\DemoServiceProvider;
use Illuminate\Foundation\Http\Middleware\PreventRequestForgery;
use Illuminate\Support\Facades\Route;

// The panel API has no web pages. The web panel is a separate app (see web/).
Route::get('/', fn () => response()->json(['name' => 'Cloudfactory panel API', 'version' => '0.1.0', 'documentation' => '/openapi.yaml']));

// The demonstration mode serves the example plugin (plugins/example): its page and its webhook.
// The sample plugin "Uptime monitor" uses this address. A production panel has no such routes.
if (DemoServiceProvider::enabled()) {
    Route::get('/demo-plugins/uptime/panel/status', fn () => response(file_get_contents(base_path('../plugins/example/page.html')), 200, [
        'Content-Type' => 'text/html; charset=utf-8',
        // The page may be in a frame of the web panel only.
        'Content-Security-Policy' => "frame-ancestors 'self' http://localhost:5173 http://127.0.0.1:5173",
    ]));
    Route::post('/demo-plugins/uptime/hooks/cloudfactory', fn () => response()->noContent())
        ->withoutMiddleware(PreventRequestForgery::class);
    // The action "Check now" and the before hook "website.removing" of the sample plugin.
    Route::post('/demo-plugins/uptime/actions/check', fn () => response()->json([
        'message' => 'The website answered in 182 ms from London, 96 ms from Frankfurt and 241 ms from Virginia.',
        'url' => rtrim((string) env('CF_DEMO_PLUGIN_URL', 'http://127.0.0.1:8000'), '/').'/demo-plugins/uptime/panel/status',
    ]))->withoutMiddleware(PreventRequestForgery::class);
    Route::post('/demo-plugins/uptime/hooks/before', fn () => response()->json(['allow' => true]))
        ->withoutMiddleware(PreventRequestForgery::class);
}
