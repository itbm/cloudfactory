<?php

namespace App\Demo;

use App\Models\AgentJob;
use App\Models\Server;
use App\Models\Website;
use App\Services\AgentCallException;
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

    /**
     * The file manager and the logs, with the sample files of the demonstration mode, and the
     * autoresponders of the mailboxes. A check of a branch gives the commit of the last
     * release, so that no deploy starts.
     */
    public function call(Server $server, string $method, array $payload, int $timeout = 75): array
    {
        if ($method === 'SearchLogs') {
            // An empty website ID is the access log of the server.
            $website = ($payload['websiteId'] ?? '') === '' ? null : (Website::find($payload['websiteId']) ?? throw new AgentCallException('not_found', 'the agent does not know this website'));

            return DemoLogs::search($server, $website, $payload);
        }

        if ($method === 'CheckBranches') {
            return ['heads' => array_map(fn (array $check) => array_filter([
                'websiteId' => $check['websiteId'] ?? '',
                'commit' => Website::find($check['websiteId'] ?? '')?->releases()->whereNotNull('commit')->value('commit'),
            ]), (array) ($payload['checks'] ?? []))];
        }

        return str_ends_with($method, 'Autoresponder') ? DemoMail::call($method, $payload) : DemoSite::call($method, $payload);
    }

    /** The demo agent always answers. */
    public function check(Server $server): ?string
    {
        self::record($server, null);

        return null;
    }
}
