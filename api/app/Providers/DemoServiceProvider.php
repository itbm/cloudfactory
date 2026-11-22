<?php

namespace App\Providers;

use App\Demo\DemoAcme;
use App\Demo\DemoAgentClient;
use App\Demo\DemoDomainPolicy;
use App\Demo\DemoLookup;
use App\Demo\DemoMailDns;
use App\Models\MeshTunnel;
use App\Models\Server;
use App\Services\AgentClient;
use App\Services\Certificates\Acme;
use App\Services\Dns\Lookup;
use App\Services\DomainPolicy;
use App\Services\MailDns;
use App\Services\Notifications\DemoMailTransport;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\SmtpMailTransport;
use App\Services\Plugins\Outbound;
use Database\Seeders\DemoSeeder;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Support\ServiceProvider;

/**
 * The demonstration mode (config/demo.php): the panel uses the demo agent, and no real server.
 * The mode is for development, demonstrations and screenshots only.
 */
class DemoServiceProvider extends ServiceProvider
{
    /** The values of APP_ENV that permit the demonstration mode. */
    public const ENVIRONMENTS = ['local', 'testing', 'demo', 'development'];

    /** True when the demonstration mode is on. A production panel never uses it. */
    public static function enabled(): bool
    {
        // Adversarial review, finding A8-11: only these environments. A wrong word in APP_ENV
        // on a server with customers does not start the mode.
        return (bool) config('demo.enabled') && app()->environment(self::ENVIRONMENTS);
    }

    public function register(): void
    {
        $this->app->bind(AgentClient::class, fn () => self::enabled() ? new DemoAgentClient : new AgentClient);
        // The demonstration mode asks no real resolver for the name servers of a zone.
        $this->app->bind(Lookup::class, fn () => self::enabled() ? new DemoLookup : new Lookup);
        // The demonstration mode gets the certificates with DNS validation from a demonstration CA.
        $this->app->bind(Acme::class, fn () => self::enabled() ? new DemoAcme : Acme::fromConfig());
        // A zone at an external DNS provider gives its claim token, as the real provider does.
        $this->app->bind(DomainPolicy::class, fn () => self::enabled() ? new DemoDomainPolicy : new DomainPolicy);
        // The demonstration mode sends no email. It writes each email to the log.
        $this->app->singleton(MailTransport::class, fn () => self::enabled() ? new DemoMailTransport : new SmtpMailTransport);
        $this->app->bind(MailDns::class, fn () => self::enabled() ? new DemoMailDns : new MailDns);

        // The sample plugin "Uptime monitor" is on the address of the demo API (127.0.0.1).
        $this->app->bind(Outbound::class, fn () => self::enabled() ? new class extends Outbound
        {
            public function permitsLoopback(): bool
            {
                return true;
            }
        } : new Outbound);
    }

    public function boot(): void
    {
        // The sample servers have this agent release, and one has an earlier release.
        // The sample administrator and the sample reseller have no TOTP. The demonstration mode
        // does not ask them to set it up, unless CF_REQUIRE_TOTP has a value.
        if (self::enabled() && env('CF_REQUIRE_TOTP') === null) {
            config(['cloudfactory.require_totp' => false]);
        }
        if (self::enabled() && env('CF_AGENT_LATEST') === null) {
            config(['cloudfactory.agent.latest_version' => DemoSeeder::AGENT]);
        }
        // No agent reports in the demonstration mode. Without a report, the panel shows each
        // server as offline after 5 minutes (cloudfactory:check-jobs).
        $this->callAfterResolving(Schedule::class, function (Schedule $schedule) {
            if (self::enabled()) {
                $schedule->call(fn () => Server::whereIn('state', Server::WORKING)->update(['last_seen_at' => now()]))->everyMinute();
                // The sample tunnels keep the state that their servers reported.
                $schedule->call(fn () => MeshTunnel::all()->each(fn (MeshTunnel $t) => $t->update([
                    'status' => array_map(fn (array $side) => ['reportedAt' => now()->getTimestamp()] + $side, $t->status ?? []),
                ])))->everyMinute();
            }
        });
    }
}
