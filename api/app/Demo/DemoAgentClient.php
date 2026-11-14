<?php

namespace App\Demo;

use App\Models\AgentJob;
use App\Models\Server;
use App\Services\AgentClient;

/**
 * The agent client of the demonstration mode (config/demo.php). It connects to no server. A
 * queue job does the steps of each job and reports the result, as the agent of a server does.
 * Thus the web panel shows the progress and the result of each change with no real server.
 */
class DemoAgentClient extends AgentClient
{
    public function send(AgentJob $job): void
    {
        RunDemoJob::dispatch($job->id);
    }

    /** The demo agent always answers. */
    public function check(Server $server): ?string
    {
        self::record($server, null);

        return null;
    }
}
