<?php

namespace App\Providers;

use App\Demo\DemoAgentClient;
use App\Models\Server;
use App\Services\AgentClient;
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
    }

    public function boot(): void
    {
        // The sample servers have this agent release, and one has an earlier release.
        if (self::enabled() && env('CF_AGENT_LATEST') === null) {
            config(['cloudfactory.agent.latest_version' => DemoSeeder::AGENT]);
        }
        // No agent reports in the demonstration mode. Without a report, the panel shows each
        // server as offline after 5 minutes (cloudfactory:check-jobs).
        $this->callAfterResolving(Schedule::class, function (Schedule $schedule) {
            if (self::enabled()) {
                $schedule->call(fn () => Server::whereIn('state', Server::WORKING)->update(['last_seen_at' => now()]))->everyMinute();
            }
        });
    }
}
