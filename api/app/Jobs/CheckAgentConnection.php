<?php

namespace App\Jobs;

use App\Models\Server;
use App\Services\AgentClient;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;

/**
 * Tests that the panel can connect to the agent of a server. The panel does this after the
 * first report of a new server. Before, a server with an address that the panel cannot reach
 * showed as healthy, and its first job waited for more than 6 minutes and then failed.
 */
class CheckAgentConnection implements ShouldQueue
{
    use Queueable;

    public int $tries = 1;

    public function __construct(public string $serverId) {}

    public function handle(AgentClient $client): void
    {
        $server = Server::find($this->serverId);
        if ($server) {
            $client->check($server);
        }
    }
}
