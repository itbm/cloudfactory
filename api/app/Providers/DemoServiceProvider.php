<?php

namespace App\Providers;

use App\Demo\DemoAgentClient;
use App\Demo\DemoLookup;
use App\Models\MeshTunnel;
use App\Models\Server;
use App\Services\AgentClient;
use App\Services\Dns\Lookup;
use App\Services\Notifications\DemoMailTransport;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\SmtpMailTransport;
use Database\Seeders\DemoSeeder;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Support\ServiceProvider;

/**
 * The demonstration mode (config/demo.php): the panel uses the demo agent, and no real server.
 * The mode is for development, demonstrations and screenshots only.
 */
class DemoServiceProvider extends ServiceProvider
{
    /** True when the demonstration mode is on. A production panel never uses it. */
    public static function enabled(): bool
    {
        return (bool) config('demo.enabled') && ! app()->environment('production');
    }

    public function register(): void
    {
        $this->app->bind(AgentClient::class, fn () => self::enabled() ? new DemoAgentClient : new AgentClient);
        // The demonstration mode asks no real resolver for the name servers of a zone.
        $this->app->bind(Lookup::class, fn () => self::enabled() ? new DemoLookup : new Lookup);
        // The demonstration mode sends no email. It writes each email to the log.
        $this->app->singleton(MailTransport::class, fn () => self::enabled() ? new DemoMailTransport : new SmtpMailTransport);
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
