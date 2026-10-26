<?php

namespace App\Services;

use App\Models\AgentJob;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\RequestException;
use Illuminate\Support\Facades\Http;

/**
 * Calls the agent on a server. The agent serves gRPC and the Connect protocol on the same port.
 * The panel uses the Connect protocol: an HTTP POST with a JSON body, over mutual TLS.
 */
class AgentClient
{
    public const SERVICE = 'cloudfactory.agent.v1.AgentService';

    /**
     * Sends a job to the agent. The agent accepts the job and runs it. It reports progress to the panel.
     *
     * @throws ConnectionException
     * @throws RequestException
     */
    public function send(AgentJob $job): void
    {
        $server = $job->server;
        $config = config('cloudfactory.agent');

        Http::baseUrl($server->agent_url)
            ->timeout($config['timeout'])
            ->withOptions([
                'cert' => $config['client_certificate'],
                'ssl_key' => $config['client_key'],
                'verify' => $config['ca'],
            ])
            ->acceptJson()
            ->withHeaders(['Connect-Protocol-Version' => '1'])
            ->post('/'.self::SERVICE.'/'.$job->action->value, [
                'jobId' => $job->id,
                ...$job->payload,
            ])
            ->throw();
    }
}
