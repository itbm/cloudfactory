<?php

namespace App\Providers;

use App\Services\Certificates\DnsProviders;
use App\Services\Certificates\PanelDns;
use App\Services\Mesh;
use Illuminate\Cache\RateLimiting\Limit;
use Illuminate\Database\Events\TransactionRolledBack;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Event;
use Illuminate\Support\Facades\RateLimiter;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    /**
     * Register any application services.
     */
    public function register(): void
    {
        // The DNS services of the DNS validation of certificates. A provider for a DNS service
        // outside the panel adds itself with the same tag (ChallengeDns).
        $this->app->tag([PanelDns::class], DnsProviders::TAG);
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
        // Each change of a DNS zone makes one job for each DNS server.
        RateLimiter::for('dns-changes', fn (Request $request) => self::forCustomer($request, Limit::perMinute(60), 'dns-changes'));
        // Each change of a mailbox or of an address makes one job for the mail server.
        RateLimiter::for('mail-changes', fn (Request $request) => self::forCustomer($request, Limit::perMinute(30), 'mail-changes'));
        // A change of the panel domain of a reseller starts a certificate order (finding A8-08).
        RateLimiter::for('customer-changes', fn (Request $request) => self::forCustomer($request, Limit::perHour(60), 'customer-changes'));
        // Each request of the file manager and of the logs starts a command on the server.
        RateLimiter::for('site-tools', fn (Request $request) => Limit::perMinute(300)->by('site-tools:'.($request->user()?->id ?? $request->ip())));
        // Each new certificate and each renewal starts an order at the certificate authority,
        // and Let's Encrypt limits the orders of an account.
        RateLimiter::for('certificate-changes', fn (Request $request) => self::forCustomer($request, Limit::perHour(20), 'certificate-changes'));
        RateLimiter::for('domain-checks', fn (Request $request) => self::forCustomer($request, Limit::perMinute(10), 'domain-checks'));
        Event::listen(TransactionRolledBack::class, fn () => Mesh::forgetPendingRefresh());
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
