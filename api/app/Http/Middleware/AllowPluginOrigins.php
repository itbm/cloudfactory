<?php

namespace App\Http\Middleware;

use App\Models\Plugin;
use Closure;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Schema;
use Symfony\Component\HttpFoundation\Response;
use Throwable;

/**
 * CF-18 permits only the panel domain and CF_CORS_ORIGINS to call the API from a browser. The
 * pages of the plugins that are on can call the API too, with the page token that the web
 * panel gives them. A browser sends no cookie with these requests (supports_credentials is
 * false), so the token is the only credential.
 */
class AllowPluginOrigins
{
    public const CACHE_KEY = 'plugins.origins';

    public function handle(Request $request, Closure $next): Response
    {
        if ($request->is('api/*') && $request->headers->has('Origin')) {
            $origins = self::origins();
            if ($origins !== []) {
                config(['cors.allowed_origins' => array_values(array_unique([...config('cors.allowed_origins', []), ...$origins]))]);
            }
        }

        return $next($request);
    }

    /** @return list<string> */
    public static function origins(): array
    {
        try {
            return Cache::remember(self::CACHE_KEY, 300, function () {
                if (! Schema::hasTable('plugins')) {
                    return [];
                }
                $origins = [];
                foreach (Plugin::where('enabled', true)->get() as $plugin) {
                    if ($plugin->pages() !== []) {
                        $p = parse_url($plugin->base_url);
                        $origins[] = $p['scheme'].'://'.$p['host'].(isset($p['port']) ? ':'.$p['port'] : '');
                    }
                }

                return array_values(array_unique($origins));
            });
        } catch (Throwable) {
            return [];
        }
    }

    public static function forget(): void
    {
        Cache::forget(self::CACHE_KEY);
    }
}
