<?php

namespace App\Http\Controllers\Agent;

use App\Enums\JobState;
use App\Http\Controllers\Controller;
use App\Jobs\CheckAgentConnection;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\CronJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Domain;
use App\Models\Mailbox;
use App\Models\Server;
use App\Services\Dns\Zones;
use App\Services\JobOutcome;
use App\Services\Mesh;
use App\Services\Packs\Packs;
use App\Services\PanelCertificate;
use App\Services\UsageRecorder;
use App\Validation\InputRules;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Carbon;
use Illuminate\Support\Str;
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
            'pendingUpdates' => ['array', 'max:20000'],
            'pendingUpdates.*.name' => ['required', 'string', 'max:255'],
            'pendingUpdates.*.from' => ['nullable', 'string', 'max:255'],
            'pendingUpdates.*.to' => ['required', 'string', 'max:255'],
            'pendingUpdates.*.security' => ['boolean'],
            'pendingUpdates.*.containerTool' => ['boolean'],
            'rebootRequired' => ['boolean'],
            'lastUpdateAt' => ['nullable', 'date'],
            'services' => ['array', 'max:1000'],
            'services.*.name' => ['required', 'string', 'max:255'],
            'services.*.state' => ['required', Rule::in(['running', 'stopped', 'failed'])],
            // The reason for a service that does not run, and a part that the server can operate without.
            'services.*.detail' => ['nullable', 'string', 'max:1000'],
            'services.*.optional' => ['boolean'],
            'usage' => ['array'],
            'usage.cpuPercent' => ['numeric', 'between:0,100'],
            'usage.memoryPercent' => ['numeric', 'between:0,100'],
            'usage.diskPercent' => ['numeric', 'between:0,100'],
            'usage.networkInBytes' => ['integer', 'min:0'],
            'usage.networkOutBytes' => ['integer', 'min:0'],
            // Adversarial review, findings A3-08 and A5-08: each list and each value has a limit.
            'websites' => ['array', 'max:100000'],
            'websites.*.id' => ['required', 'string', 'max:64'],
            'websites.*.cpuPercent' => ['numeric', 'between:0,102400'],
            'websites.*.memoryBytes' => ['integer', 'between:0,1125899906842624'],
            'websites.*.diskBytes' => ['integer', 'between:0,1152921504606846976'],
            'websites.*.processes' => ['integer', 'between:0,4194304'],
            'websites.*.bytesIn' => ['integer', 'between:0,1125899906842624'],
            'websites.*.bytesOut' => ['integer', 'between:0,1125899906842624'],
            // FR-402 and FR-403: the DNS and the certificate of each domain on the server.
            'domains' => ['nullable', 'array', 'max:1000000'],
            'domains.*.name' => ['required', 'string', 'max:253'],
            'domains.*.dnsPointsHere' => ['boolean'],
            'domains.*.certificate' => [Rule::in(['pending', 'valid', 'expired', 'failed'])],
            'domains.*.expiresAt' => ['nullable', 'date'],
            // Batch 1, finding 12: the size of each database.
            'databases' => ['nullable', 'array', 'max:1000000'],
            'databases.*.engine' => ['required', Rule::in(Database::ENGINES)],
            'databases.*.name' => ['required', 'string', 'max:64'],
            'databases.*.sizeBytes' => ['integer', 'min:0'],
            // Valkey: the database number and the number of keys.
            'databases.*.number' => ['nullable', 'integer', 'between:1,65535'],
            'databases.*.keys' => ['nullable', 'integer', 'min:0'],
            // The engines that the agent can operate. An agent of an earlier release does not send them.
            'databaseEngines' => ['nullable', 'array', 'max:20'],
            'databaseEngines.*' => ['string', 'max:32'],
            // The website runtimes and the optional features of the agent. An agent of an
            // earlier release does not send them.
            'runtimes' => ['nullable', 'array', 'max:100'],
            'runtimes.*' => ['string', 'max:32'],
            'features' => ['nullable', 'array', 'max:50'],
            'features.*' => ['string', 'max:64'],
            // The packs of the server (SetPacks): the name and the version of each one.
            'packs' => ['nullable', 'array', 'max:'.InputRules::MAX_PACKS],
            'packs.*.slug' => ['required', 'string', 'max:40'],
            'packs.*.version' => ['required', 'string', 'max:40'],
            // True when the agent can operate the database servers of customers.
            'databaseInstances' => ['boolean'],
            // The state and the resource use of each database server on the server.
            'databaseServers' => ['nullable', 'array', 'max:100000'],
            'databaseServers.*.id' => ['required', 'string', 'max:64'],
            'databaseServers.*.state' => ['required', Rule::in(['running', 'stopped'])],
            'databaseServers.*.memoryBytes' => ['integer', 'min:0'],
            'databaseServers.*.diskBytes' => ['integer', 'min:0'],
            'databaseServers.*.diskSizeBytes' => ['integer', 'min:0'],
            // DNS hosting: true when the agent can operate the DNS server, and the public
            // addresses of the server. An agent of an earlier release does not send them.
            'dns' => ['boolean'],
            'addresses' => ['nullable', 'array', 'max:16'],
            'addresses.*' => ['string', 'ip'],
            // The DNS zones of the server, with the serial that its DNS server loaded. An agent
            // that cannot operate DNS does not send them.
            'dnsZones' => ['nullable', 'array', 'max:100000'],
            'dnsZones.*.name' => ['required', 'string', 'max:253'],
            'dnsZones.*.serial' => ['required', 'integer', 'min:0', 'max:4294967295'],
            // Spec section 10: the port of the SFTP container. An agent of release 0.1 does not send it.
            'sftpPort' => ['nullable', 'integer', 'between:1,65535'],
            // The certificate of the panel domain. Only the agent on the panel server sends it.
            'panelCertificate' => ['nullable', 'array'],
            'panelCertificate.domain' => ['required_with:panelCertificate', 'string', 'max:253'],
            'panelCertificate.method' => ['required_with:panelCertificate', Rule::in(PanelCertificate::METHODS)],
            'panelCertificate.state' => ['required_with:panelCertificate', Rule::in(['valid', 'pending', 'expired'])],
            'panelCertificate.expiresAt' => ['nullable', 'date'],
            'panelCertificate.caCertificate' => ['nullable', 'string', 'max:8000'],
            // Email: the state of the mail server, and the use of each mailbox.
            'mail' => ['nullable', 'array'],
            'mail.state' => ['required_with:mail', Rule::in(['running', 'stopped'])],
            'mail.mailboxes' => ['array', 'max:1000000'],
            'mail.mailboxes.*.address' => ['required', 'string', 'max:320'],
            'mail.mailboxes.*.usedBytes' => ['required', 'integer', 'min:0'],
            // The tunnel key of the server and the state of its tunnels to other servers. An
            // agent of an earlier release does not send it.
            'mesh' => ['nullable', 'array'],
            'mesh.publicKey' => ['required_with:mesh', 'string', 'max:64'],
            'mesh.version' => ['integer', 'between:0,9007199254740991'],
            'mesh.address' => ['nullable', 'string', 'max:45'],
            // True when the agent can operate the members of the networks of the customers.
            'mesh.networks' => ['boolean'],
            'mesh.websites' => ['boolean'],
            'mesh.tunnels' => ['nullable', 'array', 'max:1000'],
            'mesh.tunnels.*.server' => ['required', 'string', 'max:64'],
            'mesh.tunnels.*.handshakeAt' => ['integer', 'min:0'],
            'mesh.tunnels.*.reachable' => ['boolean'],
            'mesh.tunnels.*.roundTripMicros' => ['integer', 'min:0'],
            // The panel CA that the agent trusts. An agent of an earlier release does not send it.
            'panelCaSha256' => ['nullable', 'string', 'regex:/^[0-9a-f]{64}$/'],
            // The results of the cron jobs that ran since the last reports.
            'cronRuns' => ['nullable', 'array', 'max:10000'],
            'cronRuns.*.websiteId' => ['required', 'string', 'max:64'],
            'cronRuns.*.id' => ['required', 'string', 'max:64'],
            'cronRuns.*.at' => ['required', 'date'],
            'cronRuns.*.exitCode' => ['required', 'integer'],
            'cronRuns.*.output' => ['nullable', 'string', 'max:100000'],
        ]);

        // A server is degraded when a necessary service does not run. An optional part, for
        // example the disk quotas, does not change the state.
        $failed = Server::hasFailedService($data['services'] ?? []);
        $wasOffline = $server->state === 'offline';
        $answered = Zones::servers()->contains('id', $server->id);
        $addresses = $server->addresses ?? [];
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
            'agent_runtimes' => $data['runtimes'] ?? null,
            'agent_features' => $data['features'] ?? null,
            'agent_packs' => $data['packs'] ?? null,
            'database_instances' => $data['databaseInstances'] ?? false,
            'dns' => $data['dns'] ?? false,
            'addresses' => $data['addresses'] ?? $server->addresses,
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
        // The agent can operate DNS now, for example after an update: the zones get the NS
        // record of this server, and the server gets the zones.
        if ($answered !== Zones::servers()->contains('id', $server->id)) {
            app(Zones::class)->pushAll();
        }
        if (($server->addresses ?? []) !== $addresses) {
            app(Zones::class)->serverAddressesChanged($server);
        }
        // The server gets the zones that it does not have, for example after it was offline,
        // and loses the zones that it must not have.
        // The server gets the packs of the panel when it has other packs, for example after
        // it was offline or after a failed job.
        if (is_array($data['packs'] ?? null)) {
            app(Packs::class)->reconcile($server->fresh(), $data['packs']);
        }
        if (is_array($data['dnsZones'] ?? null)) {
            app(Zones::class)->reconcile($server->fresh(), $data['dnsZones']);
        }
        // The mesh: the public key of the server and the state of its tunnels.
        if (isset($data['mesh'])) {
            app(Mesh::class)->reported($server, $data['mesh']);
        }
        // FR-203: the current resource use of each website.
        foreach ($data['websites'] ?? [] as $use) {
            $server->websites()->whereKey($use['id'])->update(['usage' => json_encode(collect($use)->except(['id', 'bytesIn', 'bytesOut']))]);
        }
        // The usage history, and the alerts of the resources that are near their limits.
        app(UsageRecorder::class)->record($server, $data);
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
                // The agent reports the databases in its database servers. The website of a
                // database can be on a different server.
                Database::where(fn ($q) => $q->whereIn('website_id', $websiteIds)->orWhereIn('database_server_id', $server->databaseServers()->select('id')))
                    ->where(['engine' => $d['engine'], 'name' => $d['name']])->update($values);
            }
        }
        // The agent sends each result in more than one report. The panel keeps the newest run.
        foreach ($data['cronRuns'] ?? [] as $run) {
            $at = Carbon::parse($run['at']);
            CronJob::whereIn('website_id', $websiteIds)->where('website_id', $run['websiteId'])->whereKey($run['id'])
                ->where(fn ($q) => $q->whereNull('last_run_at')->orWhere('last_run_at', '<', $at))
                ->update(['last_run_at' => $at, 'last_exit_code' => $run['exitCode'], 'last_output' => Str::limit((string) ($run['output'] ?? ''), 4000)]);
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
                // A move or a removal gives its own result.
                in_array($databaseServer->status, [DatabaseServer::REMOVING, DatabaseServer::MOVING], true) => $databaseServer->status,
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
        if (isset($data['mail']) && $server->mail_hostname !== null) {
            self::recordMail($server, $data['mail']);
        }
        // CF-10: only the panel server has the panel and its edge proxy.
        if ($server->local && isset($data['panelCertificate'])) {
            PanelCertificate::record($data['panelCertificate']);
        }

        // Each agent trusts the CA of the panel, also when the panel has a certificate from
        // Let's Encrypt now. Thus a later change to the CA of the panel does not stop the agents.
        return response()->json(['ok' => true, 'panelCaSha256' => PanelCertificate::current()['caSha256']]);
    }

    /**
     * The mail report of an agent. A mail server that stopped changes only a mail server that
     * operated: the job that starts the mail server gives its own result (JobOutcome). An
     * agent reports only the mailboxes of the mail domains on its own server.
     *
     * @param  array<string, mixed>  $mail
     */
    private static function recordMail(Server $server, array $mail): void
    {
        if ($mail['state'] === 'running' && in_array($server->mail_status, ['failed', 'stopped'], true)) {
            $server->update(['mail_status' => 'running']);
        } elseif ($mail['state'] === 'stopped' && $server->mail_status === 'running') {
            $server->update(['mail_status' => 'stopped']);
        }
        $used = collect($mail['mailboxes'] ?? [])->pluck('usedBytes', 'address');
        if ($used->isEmpty()) {
            return;
        }
        foreach (Mailbox::with('domain')->whereIn('mail_domain_id', $server->mailDomains()->select('id'))->get() as $mailbox) {
            $bytes = $used->get($mailbox->address());
            if ($bytes !== null && (int) $bytes !== $mailbox->used_bytes) {
                $mailbox->update(['used_bytes' => (int) $bytes]);
            }
        }
    }
}
