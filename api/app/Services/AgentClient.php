<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Http\Controllers\Api\BackupController;
use App\Models\AgentJob;
use App\Models\GitSource;
use App\Models\Server;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\PendingRequest;
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
        $payload = $job->payload;
        // Audit finding S2: each server has its own repository password. A restore of a
        // snapshot from before that change uses the password that all servers had.
        $shared = (bool) ($payload['sharedRepository'] ?? false);
        unset($payload['sharedRepository']);

        self::connection($server)
            ->acceptJson()
            ->withHeaders(['Connect-Protocol-Version' => '1'])
            ->post('/'.self::SERVICE.'/'.$job->action->value, [
                'jobId' => $job->id,
                ...$payload,
                // The credentials of the object storage and the password of the repository are not
                // in the job. The panel adds the current values each time that it sends the job.
                // Second audit, finding F08: a server with its own key gets that key only.
                // The mesh state has the secret key of each pair of servers. It is not in the job:
                // the panel adds the current state each time that it sends the job.
                ...($job->action === AgentAction::SetMesh ? ['mesh' => Mesh::stateFor($server)] : []),
                ...($job->action->usesBackupRepository() ? ['credentials' => (object) [
                    ...BackupController::credentialsFor($server, $shared),
                    'RESTIC_PASSWORD' => $shared ? (string) BackupController::sharedRepositoryPassword() : $server->backupPassword(),
                ]] : []),
                ...($job->action === AgentAction::Deploy ? self::deploySecrets($job) : []),
            ])
            ->throw();
    }

    /**
     * The private SSH key and the secrets of a deploy. They are not in the job: the panel adds
     * the current values each time that it sends the job, as for the backup credentials.
     *
     * @return array<string, mixed>
     */
    private static function deploySecrets(AgentJob $job): array
    {
        $source = GitSource::where('website_id', $job->website_id)->first();
        if (! $source) {
            return [];
        }

        return [
            // An "https://" repository is public, so the agent gets no key for it.
            'deployKey' => str_starts_with($source->repository, 'https://') ? '' : (string) $source->private_key,
            'secrets' => (object) ($source->secrets ?? []),
        ];
    }

    /**
     * Calls a method of the agent that gives its answer at once and makes no job: the file
     * manager and the logs. Gives the answer as an array. The JSON of the agent has no field
     * with an empty value, and it has each 64-bit number as text.
     *
     * @param  array<string, mixed>  $payload
     * @return array<string, mixed>
     *
     * @throws AgentCallException
     */
    public function call(Server $server, string $method, array $payload): array
    {
        try {
            $response = self::connection($server)
                ->timeout(75)
                ->acceptJson()
                ->withHeaders(['Connect-Protocol-Version' => '1'])
                ->post('/'.self::SERVICE.'/'.$method, $payload);
        } catch (ConnectionException|\RuntimeException $e) {
            throw new AgentCallException('unavailable', 'The panel cannot connect to the server of this website. Try again later.');
        }
        if ($response->successful()) {
            return (array) $response->json();
        }
        // A method that the agent does not have gives status 404 with no Connect error.
        $code = $response->json('code');

        throw new AgentCallException(is_string($code) ? $code : ($response->status() === 404 ? 'unimplemented' : 'unknown'), (string) $response->json('message'));
    }

    /** A request to the agent of a server, with the certificate of the panel (mutual TLS). */
    private static function connection(Server $server): PendingRequest
    {
        $config = config('cloudfactory.agent');

        return Http::baseUrl($server->agent_url)
            ->timeout($config['timeout'])
            ->connectTimeout($config['connect_timeout'] ?? 3)
            ->withOptions([
                'cert' => $config['client_certificate'],
                'ssl_key' => $config['client_key'],
                // Trust only the certificate that this agent sent when it joined.
                'verify' => self::trustFile($server),
            ]);
    }

    /**
     * Tests the connection to the agent of a server, and records the result on the server. The
     * agent answers a plain request with "not found": each answer shows that the address, the
     * port and the two certificates are correct. Gives the reason of a failure, or null.
     */
    public function check(Server $server): ?string
    {
        try {
            self::connection($server)->get('/');
            $reason = null;
        } catch (ConnectionException $e) {
            $reason = self::reason($e);
        } catch (\RuntimeException $e) {
            $reason = $e->getMessage();
        }
        self::record($server, $reason);

        return $reason;
    }

    /** Records the result of a connection on the server. The server page shows a failure. */
    public static function record(Server $server, ?string $reason): void
    {
        $server->update(['connection_error' => $reason === null ? null : mb_substr($reason, 0, 500), 'connection_checked_at' => now()]);
    }

    /**
     * The reason of a failed connection, with no address of a documentation page. For example:
     * "Could not resolve host: web2" from "cURL error 6: Could not resolve host: web2 (see
     * https://curl.haxx.se/libcurl/c/libcurl-errors.html) for https://web2:7443/".
     */
    public static function reason(\Throwable $e): string
    {
        $text = preg_replace('/^cURL error \d+: /', '', $e->getMessage());
        $text = preg_replace('/ \(see https?:\/\/\S+\)( for \S+)?$/', '', (string) $text);

        return trim((string) $text) ?: 'The connection failed.';
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
