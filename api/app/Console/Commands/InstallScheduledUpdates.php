<?php

namespace App\Console\Commands;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Server;
use App\Services\JobDispatcher;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;

/**
 * FR-1112: in the maintenance window of each server (UTC), the panel installs the updates of the
 * update policy: security updates only, or all updates. A server with the policy "manual" waits
 * for an administrator. The scheduler runs this command every 10 minutes.
 */
#[Signature('cloudfactory:host-updates')]
#[Description('Install host updates in the maintenance window of each server')]
class InstallScheduledUpdates extends Command
{
    public function handle(JobDispatcher $jobs): int
    {
        $now = Carbon::now('UTC');
        foreach (Server::whereIn('update_policy', ['security', 'all'])->where('state', 'healthy')->get() as $server) {
            $pending = collect($server->pending_updates ?? [])->reject(fn ($u) => $u['containerTool'] ?? false);
            if ($server->update_policy === 'security') {
                $pending = $pending->filter(fn ($u) => $u['security'] ?? false);
            }
            if ($pending->isEmpty() || ! self::inWindow($server->maintenance_window, $now) || $server->last_update_at?->gt($now->copy()->subHours(20))) {
                continue;
            }
            $busy = AgentJob::where('server_id', $server->id)->where('action', AgentAction::InstallHostUpdates->value)
                ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
            if ($busy) {
                continue;
            }
            $jobs->dispatch($server, AgentAction::InstallHostUpdates, ['policy' => $server->update_policy], "Install scheduled updates on {$server->name}");
            $this->line("Started updates on {$server->name}.");
        }

        return self::SUCCESS;
    }

    /** A window such as "22:00-02:00" can go past midnight. */
    public static function inWindow(string $window, Carbon $now): bool
    {
        [$from, $to] = explode('-', $window) + [1 => ''];
        $time = $now->format('H:i');

        return $from <= $to ? $time >= $from && $time < $to : $time >= $from || $time < $to;
    }
}
