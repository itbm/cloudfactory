<?php

namespace App\Providers;

use Illuminate\Cache\RateLimiting\Limit;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\RateLimiter;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    /**
     * Register any application services.
     */
    public function register(): void
    {
        //
    }

    /**
     * Bootstrap any application services.
     */
    public function boot(): void
    {
        // Audit finding S5: each new domain starts a certificate order, and each check starts
        // DNS queries and a TLS connection on the server. The limits are for each customer, so
        // that one customer cannot use up the certificate orders of a server. An administrator
        // has no limit, for example for a migration of many websites.
        RateLimiter::for('domain-changes', fn (Request $request) => self::forCustomer($request, Limit::perHour(30), 'domain-changes'));
        RateLimiter::for('domain-checks', fn (Request $request) => self::forCustomer($request, Limit::perMinute(10), 'domain-checks'));
    }

    private static function forCustomer(Request $request, Limit $limit, string $name): Limit
    {
        $user = $request->user();
        if ($user?->isAdministrator()) {
            return Limit::none();
        }

        return $limit->by($name.':'.($user?->customer_id ?? $request->ip()));
    }
}
