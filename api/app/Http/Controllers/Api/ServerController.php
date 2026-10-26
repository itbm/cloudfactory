<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\ServerRole;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\JoinToken;
use App\Models\Server;
use App\Services\Audit;
use App\Services\JobDispatcher;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Str;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/** FR-1112 to FR-1118 and FR-1201 to FR-1203: servers and host updates. */
class ServerController extends Controller
{
    public function __construct(private JobDispatcher $jobs) {}

    public function index(): JsonResponse
    {
        return response()->json(['data' => Server::withCount('websites')->orderBy('name')->get()->map(fn (Server $s) => self::present($s))]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(self::present(Server::withCount('websites')->findOrFail($id)));
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
        $server->update(collect($data)->mapWithKeys(fn ($v, $k) => [Str::snake($k) => $v])->all());
        Audit::record($request, 'Changed server settings', $server->name);

        return response()->json(self::present($server->fresh()));
    }

    /** FR-1201: a token for "install.sh --join". It works for one server, for 24 hours. */
    public function joinToken(Request $request): JsonResponse
    {
        $token = 'cfj_'.Str::random(32);
        JoinToken::create(['token_hash' => hash('sha256', $token), 'expires_at' => now()->addDay()]);
        Audit::record($request, 'Made server join token');

        return response()->json([
            'token' => $token,
            'expiresAt' => now()->addDay()->toIso8601String(),
            'command' => "curl -fsSL https://get.cloudfactory.io | sh -s -- --join {$token} --panel https://".config('cloudfactory.panel_domain'),
        ], 201);
    }

    public function installUpdates(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        $job = $this->jobs->dispatch($server, AgentAction::InstallHostUpdates, ['policy' => $server->update_policy], "Install updates on {$server->name}");
        Audit::record($request, 'Installed host updates', $server->name);

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
        $job = $this->jobs->dispatch($server, AgentAction::UpdateAgent, ['version' => $version], "Update agent on {$server->name}", ref: $version);
        Audit::record($request, 'Updated agent', "{$server->name} {$version}");

        return response()->json(['job' => new JobResource($job)], 202);
    }

    public static function present(Server $s): array
    {
        $state = $s->state;
        if ($state === 'healthy' && $s->agent_version && version_compare($s->agent_version, config('cloudfactory.agent.latest_version'), '<')) {
            $state = 'agent_update';
        }

        return [
            'id' => $s->id,
            'name' => $s->name,
            'location' => $s->location,
            'roles' => $s->roles,
            'state' => $state,
            'agentVersion' => $s->agent_version,
            'os' => $s->os,
            'kernel' => $s->kernel,
            'arch' => $s->arch,
            'podmanVersion' => $s->podman_version,
            'gvisorVersion' => $s->gvisor_version,
            'gvisorPlatform' => $s->gvisor_platform,
            'bareMetal' => $s->bare_metal,
            'updatePolicy' => $s->update_policy,
            'maintenanceWindow' => $s->maintenance_window,
            // FR-1118: pending updates, the date of the last update, and whether the server needs a reboot.
            'pendingUpdates' => $s->pending_updates ?? [],
            'lastUpdateAt' => $s->last_update_at?->toIso8601String(),
            'rebootRequired' => $s->reboot_required,
            'services' => $s->services ?? [],
            'usage' => $s->usage,
            'websites' => $s->websites_count ?? null,
            'lastSeenAt' => $s->last_seen_at?->toIso8601String(),
        ];
    }
}
