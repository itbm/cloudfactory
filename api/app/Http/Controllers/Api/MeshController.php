<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Http\Controllers\Controller;
use App\Http\Resources\JobResource;
use App\Models\MeshTunnel;
use App\Models\Server;
use App\Models\Setting;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\Mesh;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Collection;
use Illuminate\Validation\ValidationException;

/**
 * The mesh: the encrypted tunnels between the servers of the panel. Only an administrator
 * manages it. A customer does not see keys, addresses or tunnels.
 */
class MeshController extends Controller
{
    public function __construct(private Mesh $mesh, private JobDispatcher $jobs) {}

    public function show(): JsonResponse
    {
        return response()->json(self::present());
    }

    /**
     * Changes the address range or the UDP port of the mesh. The servers get their addresses
     * from the range, so a change is possible only while the mesh has no tunnel.
     */
    public function update(Request $request): JsonResponse
    {
        $data = $request->validate([
            'range' => ['sometimes', 'string', 'max:18'],
            'port' => ['sometimes', 'integer', 'between:1024,65535'],
        ]);
        if (isset($data['range']) && ! Mesh::validRange($data['range'])) {
            throw ValidationException::withMessages(['range' => 'The address range must be a private IPv4 network with a prefix length from 12 to 22, for example 10.207.0.0/16.']);
        }
        $settings = [...Mesh::settings(), ...$data];
        if ($settings != Mesh::settings()) {
            if (MeshTunnel::exists()) {
                throw ValidationException::withMessages(['range' => 'The mesh has tunnels. Remove all tunnels before you change the address range or the port.']);
            }
            Setting::put('mesh', $settings);
            // The addresses of the old range are not in the new range.
            Server::whereNotNull('mesh_address')->update(['mesh_address' => null]);
            Audit::record($request, 'Changed mesh settings', "{$settings['range']} port {$settings['port']}");
        }

        return response()->json(self::present());
    }

    /** Changes the public IP address that the other servers send the tunnel traffic of a server to. */
    public function updateServer(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        $address = $request->validate(['tunnelAddress' => ['present', 'nullable', 'string', 'max:45']])['tunnelAddress'];
        if ($address !== null && ! Mesh::publicAddress($address)) {
            throw ValidationException::withMessages(['tunnelAddress' => 'The tunnel address must be a public IP address.']);
        }
        $jobs = [];
        if ($address !== $server->mesh_endpoint) {
            $server->update(['mesh_endpoint' => $address]);
            // The peers send to the new address.
            $jobs = $this->mesh->sync(...Mesh::peersOf($server));
            Audit::record($request, 'Changed tunnel address', "{$server->name} ".($address ?? 'automatic'));
        }

        return response()->json(self::present() + ['jobs' => JobResource::collection($jobs)]);
    }

    /** Makes a tunnel between two servers by hand. */
    public function storeTunnel(Request $request): JsonResponse
    {
        $data = $request->validate([
            'servers' => ['required', 'array', 'size:2'],
            'servers.*' => ['required', 'string', 'distinct', 'exists:servers,id'],
        ]);
        [$one, $two] = [Server::findOrFail($data['servers'][0]), Server::findOrFail($data['servers'][1])];
        $made = $this->mesh->connect($one, $two);
        Audit::record($request, 'Made tunnel', "{$one->name} and {$two->name}");

        return response()->json(['tunnel' => self::presentTunnel($made['tunnel'], Server::all()->keyBy('id')), 'jobs' => JobResource::collection($made['jobs'])], 201);
    }

    /** Sends the state to the two servers of a tunnel again. The result of each job has the state of the tunnel. */
    public function repairTunnel(Request $request, string $id): JsonResponse
    {
        $tunnel = MeshTunnel::findOrFail($id);
        $servers = Server::whereIn('id', [$tunnel->server_a_id, $tunnel->server_b_id])->orderBy('name')->get();
        $jobs = $this->mesh->sync(...$servers);
        Audit::record($request, 'Repaired tunnel', $servers->pluck('name')->join(' and '));

        return response()->json(['jobs' => JobResource::collection($jobs)], 202);
    }

    public function destroyTunnel(Request $request, string $id): JsonResponse
    {
        $tunnel = MeshTunnel::findOrFail($id);
        $names = Server::whereIn('id', [$tunnel->server_a_id, $tunnel->server_b_id])->orderBy('name')->pluck('name')->join(' and ');
        $jobs = $this->mesh->disconnect($tunnel);
        Audit::record($request, 'Removed tunnel', $names);

        return response()->json(['jobs' => JobResource::collection($jobs)], 202);
    }

    /**
     * Makes a new tunnel key on a server. The private key does not leave the server. When the
     * job is complete, the panel sends the new public key to the peers of the server. The
     * tunnels of the server do not operate between these two steps.
     */
    public function rotateKey(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);
        if (! Mesh::supports($server)) {
            throw ValidationException::withMessages(['server' => "The agent on the server {$server->name} did not report a tunnel key. Update the agent of the server, then try again."]);
        }
        $job = $this->jobs->dispatch($server, AgentAction::RotateMeshKey, [], "Make a new tunnel key on {$server->name}");
        Audit::record($request, 'Rotated tunnel key', $server->name);

        return response()->json(['job' => new JobResource($job)], 202);
    }

    /** @return array<string, mixed> */
    public static function present(): array
    {
        $servers = Server::orderBy('name')->get()->keyBy('id');
        $tunnels = MeshTunnel::orderBy('created_at')->get();
        $count = $tunnels->flatMap(fn (MeshTunnel $t) => [$t->server_a_id, $t->server_b_id])->countBy();

        return [
            ...Mesh::settings(),
            'servers' => $servers->values()->map(fn (Server $s) => [
                'id' => $s->id,
                'name' => $s->name,
                'location' => $s->location,
                'state' => $s->state,
                // False for an agent of an earlier release: it cannot have tunnels.
                'supported' => Mesh::supports($s),
                'publicKey' => $s->mesh_public_key,
                'address' => $s->mesh_address,
                // The public IP address for the tunnel traffic. Null: the panel finds it from
                // the address of the agent when the server gets its first tunnel.
                'tunnelAddress' => $s->mesh_endpoint,
                'tunnels' => $count->get($s->id, 0),
                // True when the server has the mesh state that the panel wants.
                'current' => $s->mesh_version === $s->mesh_wanted_version,
            ])->all(),
            'tunnels' => $tunnels->map(fn (MeshTunnel $t) => self::presentTunnel($t, $servers))->all(),
        ];
    }

    /**
     * @param  Collection<string, Server>  $servers
     * @return array<string, mixed>
     */
    public static function presentTunnel(MeshTunnel $t, Collection $servers): array
    {
        $sides = array_map(function (string $id) use ($t, $servers) {
            $side = $t->side($id);

            return [
                'serverId' => $id,
                'server' => $servers->get($id)?->name,
                // Null: the server did not report the tunnel in the last minutes.
                'reachable' => $side['reachable'] ?? null,
                'handshakeAt' => ($side['handshakeAt'] ?? 0) > 0 ? date(DATE_ATOM, $side['handshakeAt']) : null,
                'roundTripMs' => ($side['roundTripMicros'] ?? 0) > 0 ? round($side['roundTripMicros'] / 1000, 2) : null,
            ];
        }, [$t->server_a_id, $t->server_b_id]);
        $times = array_filter(array_column($sides, 'roundTripMs'), fn ($v) => $v !== null);

        return [
            'id' => $t->id,
            'state' => $t->state(),
            // The larger of the two measured round-trip times, in milliseconds.
            'roundTripMs' => $times ? max($times) : null,
            'pinned' => $t->pinned,
            'sides' => $sides,
            'createdAt' => $t->created_at?->toIso8601String(),
        ];
    }
}
