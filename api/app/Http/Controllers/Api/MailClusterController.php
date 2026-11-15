<?php

namespace App\Http\Controllers\Api;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\ServerRole;
use App\Http\Controllers\Controller;
use App\Models\AgentJob;
use App\Models\MailCluster;
use App\Models\MailDomain;
use App\Models\Server;
use App\Services\Audit;
use App\Services\MailClusters;
use App\Validation\Input;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * Mail clusters. A cluster has three or five servers with the email role. Each member has all
 * the mail, so the mail service continues when one server fails. Only an administrator makes
 * a cluster.
 *
 * The members can be in different regions. They connect through a private network that the
 * agents make, on UDP port 51877.
 */
class MailClusterController extends Controller
{
    public function __construct(private MailClusters $clusters) {}

    public function index(): JsonResponse
    {
        return response()->json(['data' => MailCluster::with('servers')->orderBy('name')->get()->map(fn (MailCluster $c) => self::present($c))]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(self::present(MailCluster::with('servers')->findOrFail($id)));
    }

    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'regex:/^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/', 'unique:mail_clusters,name'],
            'hostname' => ['required', 'string', Input::rule('domain')],
            'members' => ['required', 'array'],
            'members.*.serverId' => ['required', 'string', 'distinct'],
            'members.*.hostname' => ['required', 'string', 'distinct', Input::rule('domain')],
            // The public address of the server, for the private network of the cluster.
            'members.*.endpoint' => ['required', 'ip', 'distinct'],
            // Servers with the same number are near to each other.
            'members.*.region' => ['sometimes', 'integer', 'min:0', 'max:99'],
        ], [
            'name.regex' => 'Use lower-case letters, digits and hyphens in the name, 32 characters or fewer.',
            'members.*.serverId.distinct' => 'A server is a member one time only.',
            'members.*.hostname.distinct' => 'Each member has its own host name.',
            'members.*.endpoint.distinct' => 'Each member has its own address.',
        ]);
        if (! in_array(count($data['members']), MailCluster::SIZES, true)) {
            throw ValidationException::withMessages(['members' => 'A mail cluster has 3 or 5 servers. With an even number, the cluster cannot continue when the network between the two halves fails.']);
        }
        $names = [$data['hostname'], ...array_column($data['members'], 'hostname')];
        if (count(array_unique($names)) !== count($names)) {
            throw ValidationException::withMessages(['hostname' => 'The name of the cluster is not the name of a member.']);
        }
        foreach ($names as $i => $name) {
            if (Server::where('mail_hostname', $name)->exists() || MailCluster::where('hostname', $name)->exists() || MailDomain::where('name', $name)->exists()) {
                throw ValidationException::withMessages([$i === 0 ? 'hostname' : 'members.'.($i - 1).'.hostname' => 'This name is in use.']);
            }
        }
        $servers = [];
        foreach ($data['members'] as $i => $member) {
            $server = Server::find($member['serverId']);
            $problem = match (true) {
                ! $server => 'Select a server.',
                ! in_array(ServerRole::Email->value, $server->roles ?? [], true) => 'Give the server the email role first.',
                $server->mail_cluster_id !== null => 'This server is a member of a mail cluster.',
                $server->mail_hostname !== null => 'This server has a mail server with its own data. The panel cannot move that data into a cluster.',
                $server->state === 'offline' => 'This server is offline.',
                default => null,
            };
            if ($problem !== null) {
                throw ValidationException::withMessages(["members.{$i}.serverId" => $problem]);
            }
            $servers[] = $server;
        }

        $cluster = DB::transaction(function () use ($data, $servers) {
            $network = MailClusters::freeNetwork();
            $cluster = MailCluster::create([
                'name' => $data['name'], 'hostname' => $data['hostname'], 'status' => MailCluster::SETTING_UP, 'phase' => 'prepare', 'network' => $network,
                'secrets' => ['dbPassword' => bin2hex(random_bytes(16)), 'keepSecret' => bin2hex(random_bytes(16)), 'peerSecret' => bin2hex(random_bytes(16))],
            ]);
            foreach ($servers as $i => $server) {
                $member = $data['members'][$i];
                $server->update(['mail_cluster_id' => $cluster->id, 'mail_hostname' => $member['hostname'], 'mail_status' => null, 'mail_cluster_member' => [
                    'id' => $i + 1, 'endpoint' => $member['endpoint'], 'address' => $network.'.'.($i + 1), 'region' => $member['region'] ?? 0, 'step' => 'new',
                ]]);
            }

            return $cluster;
        });
        $this->clusters->run($cluster);
        Audit::record($request, 'Made mail cluster', "{$cluster->name} {$cluster->hostname}");

        return response()->json(self::present($cluster->fresh('servers')), 202);
    }

    /** Starts the step that failed again. */
    public function retry(Request $request, string $id): JsonResponse
    {
        $cluster = MailCluster::with('servers')->findOrFail($id);
        if ($cluster->status !== MailCluster::FAILED) {
            throw ValidationException::withMessages(['mailClusterId' => 'Only a setup that failed can start again.']);
        }
        $open = AgentJob::whereIn('server_id', $cluster->servers->pluck('id'))
            ->whereIn('action', [AgentAction::PrepareMailNode->value, AgentAction::SetMailNode->value, AgentAction::SetMailServer->value])
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
        if ($open) {
            throw ValidationException::withMessages(['mailClusterId' => 'A job of the setup is not complete. Wait until it is complete.']);
        }
        $this->clusters->run($cluster);
        Audit::record($request, 'Started mail cluster setup again', $cluster->name);

        return response()->json(self::present($cluster->fresh('servers')), 202);
    }

    public static function present(MailCluster $c): array
    {
        $members = $c->members();

        return [
            'id' => $c->id,
            'name' => $c->name,
            'hostname' => $c->hostname,
            // "setting_up", "running" or "failed".
            'status' => $c->status,
            // The step of the setup: "prepare", "bootstrap", "join", "first", "rest" or "done".
            'phase' => $c->phase,
            'error' => $c->error,
            'members' => $members->map(fn (Server $s) => [
                'serverId' => $s->id,
                'serverName' => $s->name,
                'serverState' => $s->state,
                'number' => $s->mail_cluster_member['id'] ?? 0,
                'hostname' => $s->mail_hostname,
                'endpoint' => $s->mail_cluster_member['endpoint'] ?? '',
                'address' => $s->mail_cluster_member['address'] ?? '',
                'region' => $s->mail_cluster_member['region'] ?? 0,
                // "new", "prepared" (it has keys), "joined" (its database node is in the
                // cluster) or "running" (its mail server operates).
                'step' => $s->mail_cluster_member['step'] ?? 'new',
            ])->all(),
            // The DNS records that the administrator adds: the shared name gives each member,
            // and each member has its own name.
            'records' => [
                ...$members->map(fn (Server $s) => ['type' => filter_var($s->mail_cluster_member['endpoint'] ?? '', FILTER_VALIDATE_IP, FILTER_FLAG_IPV6) ? 'AAAA' : 'A', 'name' => $c->hostname, 'value' => $s->mail_cluster_member['endpoint'] ?? ''])->all(),
                ...$members->map(fn (Server $s) => ['type' => filter_var($s->mail_cluster_member['endpoint'] ?? '', FILTER_VALIDATE_IP, FILTER_FLAG_IPV6) ? 'AAAA' : 'A', 'name' => $s->mail_hostname, 'value' => $s->mail_cluster_member['endpoint'] ?? ''])->all(),
            ],
            'domains' => MailDomain::whereIn('server_id', $c->servers->pluck('id'))->count(),
            'createdAt' => $c->created_at?->toIso8601String(),
        ];
    }
}
