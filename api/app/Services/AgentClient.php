<?php

namespace App\Services;

use App\Http\Controllers\Api\BackupController;
use App\Models\AgentJob;
use App\Models\Server;
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
                // Trust only the certificate that this agent sent when it joined.
                'verify' => self::trustFile($server),
            ])
            ->acceptJson()
            ->withHeaders(['Connect-Protocol-Version' => '1'])
            ->post('/'.self::SERVICE.'/'.$job->action->value, [
                'jobId' => $job->id,
                ...$job->payload,
                // The credentials of the object storage and the password of the repository are not
                // in the job. The panel adds the current values each time that it sends the job.
                ...($job->action->usesBackupRepository() ? ['credentials' => (object) [
                    ...BackupController::credentials(),
                    'RESTIC_PASSWORD' => BackupController::repositoryPassword(),
                ]] : []),
            ])
            ->throw();
    }

    /** Writes the certificate of the agent to a file for cURL, once. */
    public static function trustFile(Server $server): string
    {
        if (! $server->certificate_pem) {
            throw new \RuntimeException("The server {$server->name} has no agent certificate. Join it again.");
        }
        $dir = config('cloudfactory.agent.certificates');
        $path = $dir.'/'.$server->id.'-'.substr($server->certificate_fingerprint, 0, 16).'.crt';
        if (! is_file($path)) {
            if (! is_dir($dir)) {
                mkdir($dir, 0700, true);
            }
            file_put_contents($path, $server->certificate_pem);
        }

        return $path;
    }
}
