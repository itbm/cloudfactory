<?php

namespace App\Http\Controllers\Agent;

use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Jobs\CheckAgentConnection;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Domain;
use App\Models\Server;
use App\Services\JobOutcome;
use App\Services\PanelCertificate;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Carbon;
use Illuminate\Validation\Rule;

/** Reports from the agents: job progress, results and the actual state of the server. */
class AgentController extends Controller
{
    /** The maximum length of the error text of a job. */
    public const ERROR_LENGTH = 4000;

    public function __construct(private JobOutcome $outcome) {}

    public function jobEvent(Request $request, string $id): JsonResponse
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');
        $job = AgentJob::where('server_id', $server->id)->findOrFail($id);
        $data = $request->validate([
            'state' => ['required', Rule::in(['running', 'succeeded', 'failed'])],
            'step' => ['nullable', 'integer', 'min:0', 'max:1000'],
            // Audit finding R5: the panel cuts a long error text. It does not refuse the report,
            // because the result of the job would be lost.
            'error' => ['nullable', 'string', 'max:100000'],
            'result' => ['nullable', 'array'],
        ]);
        if (isset($data['error'])) {
            $data['error'] = mb_substr($data['error'], 0, self::ERROR_LENGTH);
        }

        if ($data['state'] === 'running') {
            $this->outcome->progress($job, $data['step'] ?? $job->step);
        } else {
            $this->outcome->finish($job, JobState::from($data['state']), $data['error'] ?? null, $data['result'] ?? []);
        }

        return response()->json(['ok' => true]);
    }

    /** FR-1118 and FR-1301: the agent reports the state of the host on a schedule. */
    public function report(Request $request): JsonResponse
    {
        /** @var Server $server */
        $server = $request->attributes->get('server');
        $data = $request->validate([
            'agentVersion' => ['required', 'string', 'max:32'],
            'kernel' => ['nullable', 'string', 'max:100'],
            'podmanVersion' => ['nullable', 'string', 'max:32'],
            'gvisorVersion' => ['nullable', 'string', 'max:64'],
            'pendingUpdates' => ['array'],
            'pendingUpdates.*.name' => ['required', 'string'],
            'pendingUpdates.*.from' => ['nullable', 'string'],
            'pendingUpdates.*.to' => ['required', 'string'],
            'pendingUpdates.*.security' => ['boolean'],
            'pendingUpdates.*.containerTool' => ['boolean'],
            'rebootRequired' => ['boolean'],
            'lastUpdateAt' => ['nullable', 'date'],
            'services' => ['array'],
            'services.*.name' => ['required', 'string'],
            'services.*.state' => ['required', Rule::in(['running', 'stopped', 'failed'])],
            // The reason for a service that does not run, and a part that the server can operate without.
            'services.*.detail' => ['nullable', 'string', 'max:1000'],
            'services.*.optional' => ['boolean'],
            'usage' => ['array'],
            'usage.cpuPercent' => ['numeric', 'between:0,100'],
            'usage.memoryPercent' => ['numeric', 'between:0,100'],
            'usage.diskPercent' => ['numeric', 'between:0,100'],
            'websites' => ['array'],
            'websites.*.id' => ['required', 'string'],
            'websites.*.cpuPercent' => ['numeric'],
            'websites.*.memoryBytes' => ['integer'],
            'websites.*.diskBytes' => ['integer'],
            'websites.*.processes' => ['integer'],
            // FR-402 and FR-403: the DNS and the certificate of each domain on the server.
            'domains' => ['nullable', 'array'],
            'domains.*.name' => ['required', 'string', 'max:253'],
            'domains.*.dnsPointsHere' => ['boolean'],
            'domains.*.certificate' => [Rule::in(['pending', 'valid', 'expired', 'failed'])],
            'domains.*.expiresAt' => ['nullable', 'date'],
            // Batch 1, finding 12: the size of each database.
            'databases' => ['nullable', 'array'],
            'databases.*.engine' => ['required', Rule::in(Database::ENGINES)],
            'databases.*.name' => ['required', 'string', 'max:64'],
            'databases.*.sizeBytes' => ['integer', 'min:0'],
            // Valkey: the database number and the number of keys.
            'databases.*.number' => ['nullable', 'integer', 'between:1,65535'],
            'databases.*.keys' => ['nullable', 'integer', 'min:0'],
            // The engines that the agent can operate. An agent of an earlier release does not send them.
            'databaseEngines' => ['nullable', 'array', 'max:20'],
            'databaseEngines.*' => ['string', 'max:32'],
            // True when the agent can operate the database servers of customers.
            'databaseInstances' => ['boolean'],
            // The state and the resource use of each database server on the server.
            'databaseServers' => ['nullable', 'array', 'max:100000'],
            'databaseServers.*.id' => ['required', 'string', 'max:64'],
            'databaseServers.*.state' => ['required', Rule::in(['running', 'stopped'])],
            'databaseServers.*.memoryBytes' => ['integer', 'min:0'],
            'databaseServers.*.diskBytes' => ['integer', 'min:0'],
            'databaseServers.*.diskSizeBytes' => ['integer', 'min:0'],
            // Spec section 10: the port of the SFTP container. An agent of release 0.1 does not send it.
            'sftpPort' => ['nullable', 'integer', 'between:1,65535'],
            // The certificate of the panel domain. Only the agent on the panel server sends it.
            'panelCertificate' => ['nullable', 'array'],
            'panelCertificate.domain' => ['required_with:panelCertificate', 'string', 'max:253'],
            'panelCertificate.method' => ['required_with:panelCertificate', Rule::in(PanelCertificate::METHODS)],
            'panelCertificate.state' => ['required_with:panelCertificate', Rule::in(['valid', 'pending', 'expired'])],
            'panelCertificate.expiresAt' => ['nullable', 'date'],
            'panelCertificate.caCertificate' => ['nullable', 'string', 'max:8000'],
            // The panel CA that the agent trusts. An agent of an earlier release does not send it.
            'panelCaSha256' => ['nullable', 'string', 'regex:/^[0-9a-f]{64}$/'],
        ]);

        // A server is degraded when a necessary service does not run. An optional part, for
        // example the disk quotas, does not change the state.
        $failed = Server::hasFailedService($data['services'] ?? []);
        $wasOffline = $server->state === 'offline';
        $server->update([
            'agent_version' => $data['agentVersion'],
            'kernel' => $data['kernel'] ?? $server->kernel,
            'podman_version' => $data['podmanVersion'] ?? $server->podman_version,
            'gvisor_version' => $data['gvisorVersion'] ?? $server->gvisor_version,
            'pending_updates' => $data['pendingUpdates'] ?? [],
            'reboot_required' => $data['rebootRequired'] ?? false,
            'last_update_at' => $data['lastUpdateAt'] ?? $server->last_update_at,
            'services' => $data['services'] ?? [],
            'usage' => $data['usage'] ?? null,
            'sftp_port' => $data['sftpPort'] ?? $server->sftp_port,
            'panel_ca_sha256' => $data['panelCaSha256'] ?? null,
            'database_engines' => $data['databaseEngines'] ?? null,
            'database_instances' => $data['databaseInstances'] ?? false,
            // A server that was offline is back with this report.
            'state' => in_array($server->state, ['rebooting', 'joining'], true) ? $server->state : ($failed ? 'degraded' : 'healthy'),
        ]);
        // The first report of a new server: its agent listens now, so the panel tests that it can
        // connect to the address that the server gave when it joined.
        if ($server->connection_checked_at === null) {
            $server->update(['connection_checked_at' => now()]);
            CheckAgentConnection::dispatch($server->id);
        }
        // Audit finding R11: the panel kept the jobs of this server while it was offline.
        if ($wasOffline) {
            foreach (AgentJob::where('server_id', $server->id)->where('state', JobState::Queued->value)->orderBy('created_at')->pluck('id') as $jobId) {
                SendJobToAgent::dispatch($jobId);
            }
        }
        // FR-203: the current resource use of each website.
        foreach ($data['websites'] ?? [] as $use) {
            $server->websites()->whereKey($use['id'])->update(['usage' => json_encode(collect($use)->except('id'))]);
        }
        // An agent reports only the domains and databases of its own websites.
        $websiteIds = $server->websites()->pluck('id');
        foreach ($data['domains'] ?? [] as $d) {
            Domain::whereIn('website_id', $websiteIds)->where('name', $d['name'])->whereNotNull('verified_at')->update([
                'dns_points_here' => $d['dnsPointsHere'] ?? false,
                'certificate_state' => $d['certificate'] ?? 'pending',
                'certificate_expires_at' => isset($d['expiresAt']) ? Carbon::parse($d['expiresAt']) : null,
            ]);
        }
        foreach ($data['databases'] ?? [] as $d) {
            // Valkey has no size in bytes. Its report has the database number and the number of keys.
            $values = $d['engine'] === Database::VALKEY
                ? array_filter(['number' => $d['number'] ?? null, 'key_count' => $d['keys'] ?? null], fn ($v) => $v !== null)
                : ['size_bytes' => $d['sizeBytes'] ?? 0];
            if ($values) {
                Database::whereIn('website_id', $websiteIds)->where(['engine' => $d['engine'], 'name' => $d['name']])->update($values);
            }
        }
        // The agent reports the ID of a database server in lower case, as the panel has it.
        foreach ($data['databaseServers'] ?? [] as $use) {
            $databaseServer = $server->databaseServers()->whereKey(strtolower($use['id']))->first();
            if (! $databaseServer) {
                continue;
            }
            // A container that operates shows that the agent made the database server. A
            // container that stopped changes only a database server that operated: the job
            // that makes or changes a database server gives its own result (JobOutcome).
            $status = match (true) {
                $databaseServer->status === DatabaseServer::REMOVING => DatabaseServer::REMOVING,
                $use['state'] === 'running' => DatabaseServer::RUNNING,
                $databaseServer->status === DatabaseServer::RUNNING => DatabaseServer::STOPPED,
                default => $databaseServer->status,
            };
            $databaseServer->update(['status' => $status, 'usage' => [
                'memoryBytes' => (int) ($use['memoryBytes'] ?? 0),
                'diskBytes' => (int) ($use['diskBytes'] ?? 0),
                'diskSizeBytes' => (int) ($use['diskSizeBytes'] ?? 0),
            ]]);
        }
        // CF-10: only the panel server has the panel and its edge proxy.
        if ($server->local && isset($data['panelCertificate'])) {
            PanelCertificate::record($data['panelCertificate']);
        }

        // Each agent trusts the CA of the panel, also when the panel has a certificate from
        // Let's Encrypt now. Thus a later change to the CA of the panel does not stop the agents.
        return response()->json(['ok' => true, 'panelCaSha256' => PanelCertificate::current()['caSha256']]);
    }
}
