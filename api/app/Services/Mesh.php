<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\MeshTunnel;
use App\Models\Server;
use App\Models\Setting;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * The mesh: the encrypted tunnels between the servers of the panel.
 *
 * The panel is the directory of the mesh. It has the public key, the mesh address and the
 * public address of each server, and one secret key for each pair of servers. It sends each
 * server its full list of peers (the agent action SetMesh). The panel has no private key.
 *
 * The panel also monitors the mesh: each agent reports the state of its tunnels, and the
 * panel sends the state again to a server that does not have the current version.
 */
class Mesh
{
    public const DEFAULT_RANGE = '10.207.0.0/16';

    public const DEFAULT_PORT = 51820;

    /** The number of mesh addresses for servers: the first addresses of the range. */
    public const SERVER_ADDRESSES = 254;

    /** The panel sends the state again to a server at most one time in this number of seconds. */
    public const REPAIR_SECONDS = 120;

    /**
     * Gives the IP addresses of a host name. Tests replace it.
     *
     * @var (callable(string): list<string>)|null
     */
    public static $resolver = null;

    public function __construct(private JobDispatcher $jobs) {}

    /** @return array{range: string, port: int} */
    public static function settings(): array
    {
        $saved = (array) Setting::get('mesh', []);

        return ['range' => (string) ($saved['range'] ?? self::DEFAULT_RANGE), 'port' => (int) ($saved['port'] ?? self::DEFAULT_PORT)];
    }

    /** True for a private IPv4 network with a prefix length from 12 to 22, for example "10.207.0.0/16". */
    public static function validRange(string $range): bool
    {
        if (! preg_match('/^(\d{1,3}(?:\.\d{1,3}){3})\/(\d{1,2})$/', $range, $m)) {
            return false;
        }
        $address = ip2long($m[1]);
        $bits = (int) $m[2];
        if ($address === false || long2ip($address) !== $m[1] || $bits < 12 || $bits > 22) {
            return false;
        }
        // No host bits, and all of the network is private.
        $mask = -1 << (32 - $bits) & 0xFFFFFFFF;

        return ($address & $mask) === $address
            && filter_var($m[1], FILTER_VALIDATE_IP, FILTER_FLAG_IPV4 | FILTER_FLAG_NO_PRIV_RANGE) === false
            && filter_var(long2ip($address | (~$mask & 0xFFFFFFFF)), FILTER_VALIDATE_IP, FILTER_FLAG_IPV4 | FILTER_FLAG_NO_PRIV_RANGE) === false
            && ! str_starts_with($m[1], '127.') && ! str_starts_with($m[1], '169.254.') && $m[1] !== '0.0.0.0';
    }

    /** True for a public IP address: an address that a different server can send to. */
    public static function publicAddress(string $address): bool
    {
        return filter_var($address, FILTER_VALIDATE_IP, FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE) !== false;
    }

    /** True when the agent of the server reported its tunnel key: it can have tunnels. */
    public static function supports(Server $server): bool
    {
        return $server->mesh_public_key !== null;
    }

    /** The tunnels of a server. */
    public static function tunnelsOf(Server $server): Collection
    {
        return MeshTunnel::where('server_a_id', $server->id)->orWhere('server_b_id', $server->id)->get();
    }

    /** The servers that have a tunnel to a server. */
    public static function peersOf(Server $server): Collection
    {
        $ids = self::tunnelsOf($server)->map(fn (MeshTunnel $t) => $t->otherId($server->id));

        return Server::whereIn('id', $ids)->orderBy('name')->get();
    }

    /**
     * The public IP address that the other servers send the tunnel traffic to. An administrator
     * can set it. Without that, it is the address of the agent of the server (for the panel
     * server: of the panel domain). The panel keeps the address that it found, so a later
     * change of the DNS does not move the tunnels to a different address.
     */
    public static function tunnelAddress(Server $server): ?string
    {
        if ($server->mesh_endpoint) {
            return $server->mesh_endpoint;
        }
        $host = $server->local ? config('cloudfactory.panel_domain') : parse_url((string) $server->agent_url, PHP_URL_HOST);
        $host = trim((string) $host, '[]');
        if ($host === '') {
            return null;
        }
        $addresses = filter_var($host, FILTER_VALIDATE_IP) ? [$host] : self::resolve($host);
        foreach ($addresses as $address) {
            if (self::publicAddress($address)) {
                $server->update(['mesh_endpoint' => $address]);

                return $address;
            }
        }

        return null;
    }

    /** @return list<string> The IPv4 addresses of a host name, then its IPv6 addresses. */
    private static function resolve(string $host): array
    {
        if (self::$resolver !== null) {
            return (self::$resolver)($host);
        }
        $v4 = @gethostbynamel($host) ?: [];
        $v6 = array_column(@dns_get_record($host, DNS_AAAA) ?: [], 'ipv6');

        return [...$v4, ...$v6];
    }

    /** "203.0.113.2:51820" or "[2001:db8::2]:51820". */
    private static function endpoint(string $address, int $port): string
    {
        return (str_contains($address, ':') ? "[{$address}]" : $address).':'.$port;
    }

    /**
     * Gives the server its mesh address: the first free address of the range. The address of
     * a server does not change.
     */
    public static function addressFor(Server $server): string
    {
        if ($server->mesh_address) {
            return $server->mesh_address;
        }

        return DB::transaction(function () use ($server) {
            [$network] = explode('/', self::settings()['range']);
            $used = Server::whereNotNull('mesh_address')->lockForUpdate()->pluck('mesh_address')->flip();
            for ($i = 1; $i <= self::SERVER_ADDRESSES; $i++) {
                $address = long2ip(ip2long($network) + $i);
                if (! $used->has($address)) {
                    $server->update(['mesh_address' => $address]);

                    return $address;
                }
            }
            throw ValidationException::withMessages(['server' => 'The mesh has no free address for a server. The maximum is '.self::SERVER_ADDRESSES.' servers.']);
        });
    }

    /**
     * Makes a tunnel between two servers, and sends the new state to the two servers.
     *
     * @return array{tunnel: MeshTunnel, jobs: list<AgentJob>}
     */
    public function connect(Server $one, Server $two, bool $pinned = true): array
    {
        if ($one->id === $two->id) {
            throw ValidationException::withMessages(['servers' => 'A tunnel needs two different servers.']);
        }
        foreach ([$one, $two] as $server) {
            if (! self::supports($server)) {
                throw ValidationException::withMessages(['servers' => "The agent on the server {$server->name} did not report a tunnel key. Update the agent of the server, then try again."]);
            }
            if (self::tunnelAddress($server) === null) {
                throw ValidationException::withMessages(['servers' => "The panel does not know the public IP address of the server {$server->name}. Set the tunnel address of the server, then try again."]);
            }
        }
        [$a, $b] = strcmp($one->id, $two->id) < 0 ? [$one, $two] : [$two, $one];
        $tunnel = DB::transaction(function () use ($a, $b, $pinned) {
            self::addressFor($a);
            self::addressFor($b);
            $tunnel = MeshTunnel::firstOrCreate(['server_a_id' => $a->id, 'server_b_id' => $b->id], [
                // WireGuard uses the key in addition to the key pairs of the two servers.
                'preshared_key' => base64_encode(random_bytes(32)),
                'pinned' => $pinned,
            ]);
            if ($pinned && ! $tunnel->pinned) {
                $tunnel->update(['pinned' => true]);
            }

            return $tunnel;
        });

        return ['tunnel' => $tunnel, 'jobs' => $this->sync($a, $b)];
    }

    /**
     * Removes a tunnel, and sends the new state to the two servers.
     *
     * @return list<AgentJob>
     */
    public function disconnect(MeshTunnel $tunnel): array
    {
        $servers = Server::whereIn('id', [$tunnel->server_a_id, $tunnel->server_b_id])->get();
        $tunnel->delete();

        return $this->sync(...$servers);
    }

    /**
     * Sends the current mesh state to each of the servers. Each call gives the state a new
     * version. The job has no keys: the panel adds the state when it sends the job
     * (AgentClient), so a job that waits in the queue always sends the current state.
     *
     * @return list<AgentJob>
     */
    public function sync(Server ...$servers): array
    {
        $jobs = [];
        foreach ($servers as $server) {
            if (! self::supports($server)) {
                continue;
            }
            Server::whereKey($server->id)->increment('mesh_wanted_version');
            $server->refresh();
            $server->update(['mesh_state_hash' => self::fingerprint($server)]);
            // A job that the panel did not send yet sends the new state.
            $waiting = AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetMesh->value)->where('state', JobState::Queued->value)->first();
            $jobs[] = $waiting ?? $this->jobs->dispatch($server, AgentAction::SetMesh, [], "Set the tunnels of {$server->name}");
        }

        return $jobs;
    }

    /**
     * The mesh state of a server for its agent: its address and its peers, with the keys, and
     * the networks of the customers on the server (Networks). A server with no tunnel is not
     * in the mesh: it has no interface and no open tunnel port. It can have network members:
     * a website finds a database server on its own server with the same name.
     *
     * @return array<string, mixed>
     */
    public static function stateFor(Server $server): array
    {
        $settings = self::$cache['settings'] ?? self::settings();
        $peers = [];
        $complete = collect();
        $tunnels = isset(self::$cache['tunnels'])
            ? self::$cache['tunnels']->filter(fn (MeshTunnel $t) => $t->server_a_id === $server->id || $t->server_b_id === $server->id)
            : ($server->mesh_address ? self::tunnelsOf($server) : collect());
        foreach ($server->mesh_address ? $tunnels : [] as $tunnel) {
            $id = $tunnel->otherId($server->id);
            $other = isset(self::$cache['servers']) ? self::$cache['servers']->get($id) : Server::find($id);
            $address = $other ? self::tunnelAddress($other) : null;
            // A peer that is not complete gets its tunnel when the panel has its values.
            if (! $other || ! $other->mesh_public_key || ! $other->mesh_address || $address === null) {
                continue;
            }
            $complete[$other->id] = $other;
            $peers[] = [
                'server' => $other->id,
                'publicKey' => $other->mesh_public_key,
                'presharedKey' => $tunnel->preshared_key,
                'endpoint' => self::endpoint($address, $settings['port']),
                'address' => $other->mesh_address,
            ];
        }
        usort($peers, fn ($x, $y) => strcmp($x['server'], $y['server']));
        $networks = Networks::stateFor($server, $complete);
        foreach ($peers as &$peer) {
            if (isset($networks['peerMembers'][$peer['server']])) {
                $peer['members'] = $networks['peerMembers'][$peer['server']];
            }
        }
        unset($peer);
        // An agent from before the networks does not know the fields, so it does not get them.
        $members = array_filter(['members' => $networks['members'], 'websites' => $networks['websites']]);
        if (! $peers) {
            return ['version' => $server->mesh_wanted_version, ...($members ? ['range' => $settings['range']] : []), 'peers' => [], ...$members];
        }

        return [
            'version' => $server->mesh_wanted_version,
            'range' => $settings['range'],
            'address' => $server->mesh_address,
            'port' => $settings['port'],
            'peers' => $peers,
            ...$members,
        ];
    }

    /** The hash of the mesh state of a server, with no version. */
    private static function fingerprint(Server $server): string
    {
        $state = self::stateFor($server);
        unset($state['version']);

        return hash('sha256', json_encode($state));
    }

    /**
     * Makes the mesh agree with the networks of the customers, after a change of a website, of
     * a database server or of a network.
     *
     * - Two servers get a tunnel when one has a website and the other has a database server
     *   of a network of that website.
     * - A tunnel that the panel made for the networks goes when no network uses it. A tunnel
     *   that an administrator made stays.
     * - Each server whose state is different gets the new state.
     *
     * @return list<AgentJob>
     */
    public function refresh(): array
    {
        Networks::$links = null;
        Networks::$links = Networks::links();
        try {
            $servers = Server::whereNotNull('mesh_public_key')->get()->keyBy('id');
            $needed = Networks::pairs();
            foreach ($needed as $key => [$a, $b]) {
                $one = $servers->get($a);
                $two = $servers->get($b);
                // A server that cannot have the tunnel now gets it later: a report of its
                // agent starts this function again.
                if (! $one || ! $two || ! Networks::supports($one) || ! Networks::supports($two)
                    || self::tunnelAddress($one) === null || self::tunnelAddress($two) === null) {
                    continue;
                }
                try {
                    self::addressFor($one);
                    self::addressFor($two);
                } catch (ValidationException) {
                    continue;
                }
                MeshTunnel::firstOrCreate(['server_a_id' => $a, 'server_b_id' => $b], ['preshared_key' => base64_encode(random_bytes(32)), 'pinned' => false]);
            }
            foreach (MeshTunnel::where('pinned', false)->get() as $tunnel) {
                if (! isset($needed[$tunnel->server_a_id.' '.$tunnel->server_b_id])) {
                    $tunnel->delete();
                }
            }
            // One read of the servers, the tunnels and the settings for all the states below.
            $servers = Server::whereNotNull('mesh_public_key')->get()->keyBy('id');
            self::$cache = ['servers' => Server::all()->keyBy('id'), 'tunnels' => MeshTunnel::all(), 'settings' => self::settings()];
            $changed = [];
            foreach ($servers as $server) {
                // A server that never got a state has the state of a server with no tunnel.
                $previous = $server->mesh_state_hash ?? hash('sha256', json_encode(['peers' => []]));
                if (self::fingerprint($server) !== $previous) {
                    $changed[] = $server;
                }
            }

            return $this->sync(...$changed);
        } finally {
            Networks::$links = null;
            self::$cache = null;
        }
    }

    /**
     * Calls refresh after the current database transaction, so that a job never has a record
     * that the transaction removes again.
     */
    public static function refreshLater(): void
    {
        // Many changes in one transaction (for example an import) need one refresh only.
        if (DB::transactionLevel() === 0) {
            app(self::class)->refresh();

            return;
        }
        if (self::$refreshPending) {
            return;
        }
        self::$refreshPending = true;
        DB::afterCommit(function () {
            self::$refreshPending = false;
            app(self::class)->refresh();
        });
    }

    /**
     * The servers, the tunnels and the settings while refresh calculates the states of all
     * servers. Null at other times.
     *
     * @var array{servers: Collection<string, Server>, tunnels: Collection<int, MeshTunnel>, settings: array<string, mixed>}|null
     */
    private static ?array $cache = null;

    /** True while a refresh waits for the end of the current transaction. */
    private static bool $refreshPending = false;

    /**
     * A rollback drops the refresh that waits for the commit. The next change then asks for a
     * new one. A refresh more than necessary does no harm.
     */
    public static function forgetPendingRefresh(): void
    {
        self::$refreshPending = false;
    }

    /**
     * Records what an agent reported about the mesh: in its status report, or in the result of
     * a job. Then the panel repairs the mesh where the report shows a difference.
     *
     * @param  array<string, mixed>  $mesh
     */
    public function reported(Server $server, array $mesh): void
    {
        $key = $mesh['publicKey'] ?? null;
        if (! is_string($key) || ! self::validKey($key)) {
            return;
        }
        $version = max(0, (int) ($mesh['version'] ?? 0));
        // An agent that can operate network members reports it. A server that can do it now,
        // or that has its first key now, gets its part of the networks.
        $networks = (bool) ($mesh['networks'] ?? false);
        // A later agent also reports that a website can be a member that answers.
        $websites = $networks && ($mesh['websites'] ?? false);
        // A later agent also reports that it can send and receive a database server.
        $moves = $networks && ($mesh['moves'] ?? false);
        $new = $server->mesh_public_key === null || $server->mesh_networks !== $networks || $server->mesh_websites !== $websites;
        $this->keyOf($server, $key);
        $server->update(['mesh_version' => $version, 'mesh_networks' => $networks, 'mesh_websites' => $websites, 'mesh_moves' => $moves]);
        if ($new) {
            $this->refresh();
            $server->refresh();
        }

        $tunnels = self::tunnelsOf($server)->keyBy(fn (MeshTunnel $t) => $t->otherId($server->id));
        foreach (is_array($mesh['tunnels'] ?? null) ? $mesh['tunnels'] : [] as $reported) {
            $tunnel = is_array($reported) ? $tunnels->get((string) ($reported['server'] ?? '')) : null;
            if (! $tunnel) {
                continue;
            }
            $tunnel->update(['status' => [...($tunnel->status ?? []), $server->id => [
                'reachable' => (bool) ($reported['reachable'] ?? false),
                'handshakeAt' => max(0, (int) ($reported['handshakeAt'] ?? 0)),
                'roundTripMicros' => max(0, (int) ($reported['roundTripMicros'] ?? 0)),
                'reportedAt' => now()->getTimestamp(),
            ]]]);
        }

        // The server has a newer version than the panel wants, for example after a restore of
        // the database of the panel. The agent refuses an older version, so the panel continues
        // from the version of the server.
        if ($version > $server->mesh_wanted_version) {
            $server->update(['mesh_wanted_version' => $version]);
            $this->repair($server);
        } elseif ($version < $server->mesh_wanted_version) {
            $this->repair($server);
        }
    }

    /** Records the public key of a server. The peers of a server with a new key get the key. */
    public function keyOf(Server $server, string $key): void
    {
        if ($server->mesh_public_key === $key) {
            return;
        }
        $server->update(['mesh_public_key' => $key]);
        $this->sync(...self::peersOf($server));
    }

    /**
     * Sends the state again to a server that does not have the current version. It does
     * nothing while a job is open, and for a short time after a job, so that a server that
     * refuses the state does not get a new job with each report.
     */
    private function repair(Server $server): void
    {
        $recent = AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetMesh->value)
            ->where(fn ($q) => $q->whereIn('state', [JobState::Queued->value, JobState::Running->value])
                ->orWhere('finished_at', '>', now()->subSeconds(self::REPAIR_SECONDS)))
            ->exists();
        if (! $recent) {
            $this->sync($server);
        }
    }

    /** A WireGuard key: 32 bytes in base64. */
    public static function validKey(string $key): bool
    {
        $bytes = base64_decode($key, true);

        return $bytes !== false && strlen($bytes) === 32 && base64_encode($bytes) === $key;
    }
}
