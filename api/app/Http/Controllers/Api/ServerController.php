<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\ServerRole;
use App\Enums\WebsiteStatus;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseUser;
use App\Models\JoinToken;
use App\Models\Server;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Services\DatabaseServers;
use App\Services\JobDispatcher;
use App\Services\PanelCertificate;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-1112 to FR-1118 and FR-1201 to FR-1203: servers and host updates. */
class ServerController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    public function index(): JsonResponse
    {
        return response()->json(['data' => Server::withCount(['websites', 'sharedDatabaseWebsites', 'databaseServers'])->orderBy('name')->get()->map(fn (Server $s) => self::present($s))]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(self::present(Server::withCount(['websites', 'sharedDatabaseWebsites', 'databaseServers'])->findOrFail($id)));
    }

    public function update(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        $data = $request->validate([
            'name' => ['sometimes', 'regex:/^[a-z][a-z0-9-]{1,30}$/', Rule::unique('servers', 'name')->ignore($server->id)],
            'location' => ['sometimes', 'string', 'max:100'],
            'roles' => ['sometimes', 'array', 'min:1'],
            'roles.*' => [Rule::enum(ServerRole::class)],
            // FR-1112: security updates only, all updates, or manual.
            'updatePolicy' => ['sometimes', Rule::in(['security', 'all', 'manual'])],
            'maintenanceWindow' => ['sometimes', 'regex:/^([01]\d|2[0-3]):[0-5]\d-([01]\d|2[0-3]):[0-5]\d$/'],
            // FR-310: systrap by default. KVM only on bare metal.
            'gvisorPlatform' => ['sometimes', Rule::in(['systrap', 'kvm'])],
        ]);
        if (($data['gvisorPlatform'] ?? null) === 'kvm' && ! $server->bare_metal) {
            throw ValidationException::withMessages(['gvisorPlatform' => 'The KVM platform needs a bare-metal server.']);
        }
        // The agent writes the units again and restarts the containers. The panel records the new
        // platform when the job succeeds.
        $platform = $data['gvisorPlatform'] ?? null;
        unset($data['gvisorPlatform']);
        $server->update(collect($data)->mapWithKeys(fn ($v, $k) => [Str::snake($k) => $v])->all());
        $job = null;
        if ($platform !== null && $platform !== $server->gvisor_platform) {
            $job = $this->jobs->dispatch($server, AgentAction::ConfigureHost, ['gvisorPlatform' => $platform], "Change gVisor platform of {$server->name} to {$platform}");
        }
        Audit::record($request, 'Changed server settings', $server->name);

        return response()->json(self::present($server->fresh()) + ['job' => $job ? new JobResource($job) : null]);
    }

    /** FR-1201: a token for "install.sh --join". It works for one server, for 24 hours. */
    public function joinToken(Request $request): JsonResponse
    {
        $token = 'cfj_'.Str::random(32);
        JoinToken::create(['token_hash' => hash('sha256', $token), 'expires_at' => now()->addDay()]);
        Audit::record($request, 'Made server join token');

        // The new server trusts the CA of the panel by the fingerprint of its root certificate.
        // It needs it when the panel has a certificate from its own CA, and it keeps it for a
        // later change back from Let's Encrypt.
        $pin = PanelCertificate::current()['caSha256'];

        return response()->json([
            'token' => $token,
            'expiresAt' => now()->addDay()->toIso8601String(),
            // R-14: the same signed installation script from GitHub Releases as in the README.
            'command' => 'curl -fsSL '.self::installUrl()." | sh -s -- --join {$token} --panel https://".config('cloudfactory.panel_domain')
                .($pin ? " --panel-ca-sha256 {$pin}" : ''),
            'installUrl' => self::installUrl(),
            'panelCaSha256' => $pin,
        ], 201);
    }

    /**
     * The installation script for a new server: the script of the release of this panel. Before,
     * the address was "releases/latest", which GitHub does not have while each release is a
     * pre-release. Then the command of the panel failed with error 404.
     */
    public static function installUrl(): string
    {
        return config('cloudfactory.install_url')
            ?: 'https://github.com/'.config('cloudfactory.repository').'/releases/download/v'.config('cloudfactory.agent.latest_version').'/install.sh';
    }

    /**
     * FR-1111: installs the updates now. "tools" updates only the container tools (FR-1113). The
     * administrator updates them on one server, checks it, and then updates the other servers.
     */
    public function installUpdates(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        $policy = $request->validate(['policy' => ['sometimes', Rule::in(['security', 'all', 'tools'])]])['policy'] ?? null;
        $policy ??= $server->update_policy === 'all' ? 'all' : 'security';
        if ($policy === 'tools') {
            $busy = AgentJob::where('action', AgentAction::InstallHostUpdates->value)->whereIn('state', [JobState::Queued->value, JobState::Running->value])->get()
                ->first(fn (AgentJob $j) => ($j->payload['policy'] ?? null) === 'tools');
            if ($busy) {
                throw ValidationException::withMessages(['policy' => 'The container tools are being updated on a different server. Wait until that update is complete.']);
            }
        }
        $job = $this->jobs->dispatch($server, AgentAction::InstallHostUpdates, ['policy' => $policy], $policy === 'tools' ? "Update container tools on {$server->name}" : "Install updates on {$server->name}");
        Audit::record($request, $policy === 'tools' ? 'Updated container tools' : 'Installed host updates', $server->name);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** FR-1116: reboot only one server of each role at a time. */
    public function reboot(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        $busy = Server::where('id', '!=', $server->id)->where('state', 'rebooting')->get()
            ->first(fn (Server $s) => array_intersect($s->roles, $server->roles));
        if ($busy) {
            throw ValidationException::withMessages(['server' => "{$busy->name} has the same role and is rebooting. Wait until it is healthy."]);
        }
        $server->update(['state' => 'rebooting']);
        $job = $this->jobs->dispatch($server, AgentAction::Reboot, ['now' => true], "Reboot {$server->name}");
        Audit::record($request, 'Rebooted server', $server->name);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public function updateAgent(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        $version = config('cloudfactory.agent.latest_version');
        // CF-16: the agent refuses an older version unless an administrator allows it.
        $allowDowngrade = $request->validate(['allowDowngrade' => ['sometimes', 'boolean']])['allowDowngrade'] ?? false;
        $job = $this->jobs->dispatch($server, AgentAction::UpdateAgent, ['version' => $version, 'allowDowngrade' => $allowDowngrade], "Update agent on {$server->name}", ref: $version);
        Audit::record($request, 'Updated agent', "{$server->name} {$version}");

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /**
     * Moves the databases of a server from an earlier release to database servers. On such a
     * server, all websites use one shared MariaDB container and one shared PostgreSQL
     * container.
     *
     * The panel starts one job for each website that uses the shared containers. The job copies
     * the databases and the database users of the website to database servers of its
     * customer: one for each engine, on the usual port of the engine. The panel makes a
     * database server that the customer does not have. The passwords, the host names and the
     * ports stay the same. A website does not operate during the copy of its databases.
     */
    public function moveDatabases(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        if (! $server->database_instances) {
            throw ValidationException::withMessages(['server' => "The agent on the server {$server->name} did not report that it can operate database servers. Update the agent of the server, then try again."]);
        }
        $open = AgentJob::where('server_id', $server->id)->where('action', AgentAction::MoveDatabases->value)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->pluck('website_id')->all();
        $jobs = [];
        foreach ($server->sharedDatabaseWebsites()->with(['databases', 'databaseUsers.databases', 'customer', 'server'])->orderBy('name')->get() as $website) {
            // A website that the panel makes now is not on the server yet. A website with an
            // open job moves already.
            if ($website->status === WebsiteStatus::Starting || in_array($website->id, $open, true)) {
                continue;
            }
            // Null: the panel removes a database server that the website needs. The
            // administrator moves the databases of this website when the removal is complete.
            $payload = DB::transaction(fn () => self::movePayload($website));
            if ($payload !== null) {
                $jobs[] = $this->jobs->dispatch($server, AgentAction::MoveDatabases, $payload, "Move the databases of {$website->name}", $website);
            }
        }
        if ($jobs) {
            Audit::record($request, 'Moved databases to database servers', $server->name);
        }

        return response()->json(['jobs' => JobResource::collection($jobs)], 202);
    }

    /**
     * The databases and the database users of a website, for the job that moves them, with
     * the database server that gets each one. The agent copies the hash of each password, so
     * the panel does not send a password.
     *
     * The plan of the customer does not limit the database servers of a move: the databases
     * exist, so they must get a database server.
     *
     * @return array<string, mixed>|null
     */
    public static function movePayload(Website $website): ?array
    {
        /** @var array<string, DatabaseServer> $servers */
        $servers = [];
        foreach ([...$website->databases->pluck('engine'), ...$website->databaseUsers->pluck('engine')] as $engine) {
            $servers[$engine] ??= DatabaseServers::forMove($website, $engine);
            if ($servers[$engine] === null) {
                return null;
            }
        }
        ksort($servers);

        return [
            'websiteId' => $website->id,
            'unixUser' => $website->unix_user,
            'databaseInstance' => $website->customer_id,
            'databaseServers' => array_values(array_map(fn (DatabaseServer $s) => $s->forAgent(), $servers)),
            'databases' => $website->databases->sortBy('name')->map(fn (Database $d) => ['engine' => $d->engine, 'name' => $d->name, 'databaseServer' => $servers[$d->engine]->id])->values()->all(),
            'users' => $website->databaseUsers->sortBy('name')->map(fn (DatabaseUser $u) => [
                'engine' => $u->engine,
                'name' => $u->name,
                'access' => $u->access,
                'grants' => array_map(fn ($g) => ['database' => $g['database'], 'privileges' => $g['privileges']], $u->grants()),
                'databaseServer' => $servers[$u->engine]->id,
            ])->values()->all(),
        ];
    }

    /**
     * Tests the connection from the panel to the agent of the server, and gives the server with
     * the result. The administrator uses this after a change of the DNS or of a firewall.
     */
    public function checkConnection(string $id, AgentClient $client): JsonResponse
    {
        $server = Server::withCount(['websites', 'sharedDatabaseWebsites', 'databaseServers'])->findOrFail($id);
        $client->check($server);

        return response()->json(self::present($server));
    }

    /**
     * Removes a server from the panel, for example a server that joined with a wrong address.
     * The panel does not change the server itself. A server with websites stays: delete or
     * move its websites first.
     */
    public function destroy(Request $request, string $id): JsonResponse
    {
        $server = Server::withCount(['websites', 'sharedDatabaseWebsites', 'databaseServers'])->findOrFail($id);
        if ($server->local) {
            throw ValidationException::withMessages(['server' => 'This server has the panel. You cannot remove it.']);
        }
        if ($server->websites_count > 0) {
            throw ValidationException::withMessages(['server' => "This server has {$server->websites_count} ".($server->websites_count === 1 ? 'website' : 'websites').'. Delete the websites first.']);
        }
        if ($server->database_servers_count > 0) {
            throw ValidationException::withMessages(['server' => "This server has {$server->database_servers_count} ".($server->database_servers_count === 1 ? 'database server' : 'database servers').'. Remove the database servers first.']);
        }
        DB::transaction(function () use ($server) {
            // The jobs of the server go with it. The audit log keeps the record of the actions.
            AgentJob::where('server_id', $server->id)->delete();
            $server->delete();
        });
        Audit::record($request, 'Removed server', $server->name);

        return response()->json(null, 204);
    }

    /** The host and the port of the agent address, for example "web2.example.com:7443". */
    private static function address(?string $agentUrl): ?string
    {
        $parts = $agentUrl ? parse_url($agentUrl) : false;
        if (! $parts || empty($parts['host'])) {
            return null;
        }

        return $parts['host'].(isset($parts['port']) ? ':'.$parts['port'] : '');
    }

    public static function present(Server $s): array
    {
        $state = $s->state;
        $agentUpdate = $s->agent_version && version_compare($s->agent_version, config('cloudfactory.agent.latest_version'), '<');
        if ($state === 'healthy' && $agentUpdate) {
            $state = 'agent_update';
        }
        // The agent reports to the panel, but the panel cannot connect to the agent: the jobs of
        // the server fail. Thus the server is not healthy.
        if ($s->connection_error !== null && in_array($state, ['healthy', 'agent_update'], true)) {
            $state = 'degraded';
        }

        return [
            'id' => $s->id,
            'name' => $s->name,
            'location' => $s->location,
            'roles' => $s->roles,
            'state' => $state,
            'agentVersion' => $s->agent_version,
            // The state can be "degraded" or "offline" while the agent is old. Thus the panel tells this apart.
            'agentUpdateAvailable' => (bool) $agentUpdate,
            // The address that the panel uses to connect to the agent, and the reason when the
            // last connection failed.
            'agentAddress' => self::address($s->agent_url),
            'connectionError' => $s->connection_error,
            // The server of the panel. It cannot be removed.
            'panel' => (bool) $s->local,
            'os' => $s->os,
            'kernel' => $s->kernel,
            'arch' => $s->arch,
            'podmanVersion' => $s->podman_version,
            'gvisorVersion' => $s->gvisor_version,
            'gvisorPlatform' => $s->gvisor_platform,
            // The database engines that the agent of the server can operate.
            'databaseEngines' => $s->database_engines ?? Database::SQL_ENGINES,
            // True when the agent can operate the database servers of customers.
            'databaseInstances' => (bool) $s->database_instances,
            // The number of websites that use the shared database containers of an earlier
            // release. An administrator moves their databases to database servers.
            'sharedDatabaseWebsites' => $s->database_instances ? ($s->shared_database_websites_count ?? null) : null,
            // The number of database servers of customers on this server.
            'databaseServers' => $s->database_servers_count ?? null,
            'bareMetal' => $s->bare_metal,
            'updatePolicy' => $s->update_policy,
            'maintenanceWindow' => $s->maintenance_window,
            // FR-1118: pending updates, the date of the last update, and whether the server needs a reboot.
            'pendingUpdates' => $s->pending_updates ?? [],
            'lastUpdateAt' => $s->last_update_at?->toIso8601String(),
            'rebootRequired' => $s->reboot_required,
            'services' => Server::presentServices($s->services),
            // Spec section 10: the port of the SFTP container. 22 means an agent of release 0.1.
            'sftpPort' => $s->sftp_port ?? 22,
            'usage' => $s->usage,
            'websites' => $s->websites_count ?? null,
            'lastSeenAt' => $s->last_seen_at?->toIso8601String(),
        ];
    }
}
