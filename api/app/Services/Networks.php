<?php

namespace App\Services;

use App\Models\DatabaseServer;
use App\Models\Network;
use App\Models\NetworkMember;
use App\Models\Server;
use App\Models\Website;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * The private networks of the customers.
 *
 * A network has members: websites and database servers of one customer. A website can connect
 * to each database server and to each other website that is in a network with it. The member
 * can be on the server of the website or on a different server: the mesh carries the traffic
 * between the servers (Mesh).
 *
 * A member has a name for the websites, "<name>.internal", and one address of the mesh range.
 * The name and the address are the same on each server. A database server answers on the
 * usual port of its engine. A website answers on port 80, with HTTP: the tunnel between two
 * servers encrypts the traffic.
 *
 * Each customer has the network "default". Each new website and each new database server of
 * the customer is a member of it.
 */
class Networks
{
    /** The end of the name of each network member. */
    public const DOMAIN = '.internal';

    /**
     * Names that a database server cannot have as a network member: each website has the names
     * "mariadb.internal", "postgres.internal" and "valkey.internal" for the database servers of
     * its customer on its own server.
     */
    public const RESERVED = ['mariadb', 'postgres', 'valkey', 'localhost'];

    /** The port that a website answers on as a network member. */
    public const WEBSITE_PORT = 80;

    /** The links of the current request. Mesh::refresh sets it, so one call reads them one time. */
    public static ?Collection $links = null;

    /** True when the agent of the server can operate network members. */
    public static function supports(Server $server): bool
    {
        return Mesh::supports($server) && $server->mesh_networks;
    }

    /** True when a website on the server can be a member that answers. */
    public static function supportsWebsites(Server $server): bool
    {
        return self::supports($server) && $server->mesh_websites;
    }

    /** The network "default" of a customer. */
    public static function defaultFor(string $customerId): Network
    {
        return Network::firstOrCreate(['customer_id' => $customerId, 'name' => Network::DEFAULT], ['is_default' => true]);
    }

    /** Makes a new website or a new database server a member of the network "default" of its customer. */
    public static function join(Website|DatabaseServer $member): void
    {
        if (! $member->customer_id) {
            return;
        }
        $column = $member instanceof Website ? 'website_id' : 'database_server_id';
        NetworkMember::firstOrCreate(['network_id' => self::defaultFor($member->customer_id)->id, $column => $member->id]);
        if ($member instanceof DatabaseServer) {
            self::addressFor($member);
        }
    }

    /** Removes a website or a database server from each network. */
    public static function leave(Website|DatabaseServer $member): void
    {
        NetworkMember::where($member instanceof Website ? 'website_id' : 'database_server_id', $member->id)->delete();
    }

    /**
     * The name of a member for the websites, for example "shop-db.internal". Null for a
     * database server with a name that an engine uses: it is not a network member.
     *
     * The name of a website comes from its Unix user, for example "shop-a1b2.internal" for
     * "shop_a1b2": the Unix user does not change, and no two websites have the same one.
     */
    public static function hostOf(DatabaseServer|Website $member): ?string
    {
        return self::host($member instanceof Website ? self::websiteName($member->unix_user) : $member->name);
    }

    /** The name of a website as a network member, with no ".internal". */
    public static function websiteName(string $unixUser): string
    {
        return str_replace('_', '-', $unixUser);
    }

    public static function host(string $name): ?string
    {
        return in_array($name, self::RESERVED, true) ? null : $name.self::DOMAIN;
    }

    /** The port that a network member answers on: the usual port of its engine. */
    public static function portOf(string $engine): int
    {
        return DatabaseServer::FIRST_PORT[$engine];
    }

    /**
     * Gives the member its address: the first free address of the mesh range after the
     * addresses for servers. The address of a member does not change.
     */
    public static function addressFor(DatabaseServer|Website $member): string
    {
        if ($member->mesh_address) {
            return $member->mesh_address;
        }

        return DB::transaction(function () use ($member) {
            // The database servers and the websites have their addresses from one range.
            $used = DatabaseServer::whereNotNull('mesh_address')->lockForUpdate()->pluck('mesh_address')
                ->concat(Website::whereNotNull('mesh_address')->lockForUpdate()->pluck('mesh_address'))->flip();
            $address = self::freeAddress($used);
            $member->forceFill(['mesh_address' => $address])->saveQuietly();

            return $address;
        });
    }

    /**
     * @param  Collection<string, mixed>  $used  The addresses that members have, as keys.
     *
     * @throws ValidationException
     */
    private static function freeAddress(Collection $used): string
    {
        [$network, $bits] = explode('/', Mesh::settings()['range']);
        $first = ip2long($network) + Mesh::SERVER_ADDRESSES + 2;
        // The last address of the range is not an address of a member.
        $last = ip2long($network) + (1 << (32 - (int) $bits)) - 2;
        for ($n = $first; $n <= $last; $n++) {
            if (! $used->has(long2ip($n))) {
                return long2ip($n);
            }
        }
        throw ValidationException::withMessages(['name' => 'The address range of the mesh has no free address for a network member. An administrator can set a larger range.']);
    }

    /**
     * Each pair of a website and a member that are in one network: the website can connect to
     * the member. The member is a database server or a different website of the network. A
     * database server that the agent removes, and a database server with no network name, are
     * not in the list.
     *
     * "kind" is "database" or "website". "name" has no ".internal".
     *
     * @return Collection<int, object{website_id: string, website_server_id: string, kind: string, member_id: string, server_id: string, name: string, port: int, mesh_address: ?string}>
     */
    public static function links(): Collection
    {
        if (self::$links !== null) {
            return self::$links;
        }
        $databases = DB::table('network_members as w')
            ->join('network_members as d', 'd.network_id', '=', 'w.network_id')
            ->join('websites', 'websites.id', '=', 'w.website_id')
            ->join('database_servers', 'database_servers.id', '=', 'd.database_server_id')
            ->where('database_servers.status', '!=', DatabaseServer::REMOVING)
            ->whereNotIn('database_servers.name', self::RESERVED)
            ->select([
                'websites.id as website_id', 'websites.server_id as website_server_id',
                'database_servers.id as member_id', 'database_servers.server_id as server_id',
                'database_servers.name', 'database_servers.engine', 'database_servers.mesh_address',
            ])
            ->distinct()
            ->orderBy('websites.id')->orderBy('database_servers.id')
            ->get()
            ->map(fn (object $row) => (object) [
                'website_id' => $row->website_id, 'website_server_id' => $row->website_server_id, 'kind' => 'database',
                'member_id' => $row->member_id, 'server_id' => $row->server_id, 'name' => $row->name,
                'port' => self::portOf($row->engine), 'mesh_address' => $row->mesh_address,
            ]);
        $websites = DB::table('network_members as w')
            ->join('network_members as m', 'm.network_id', '=', 'w.network_id')
            ->join('websites', 'websites.id', '=', 'w.website_id')
            ->join('websites as members', 'members.id', '=', 'm.website_id')
            ->whereColumn('members.id', '!=', 'websites.id')
            ->whereNotNull('members.server_id')
            ->select([
                'websites.id as website_id', 'websites.server_id as website_server_id',
                'members.id as member_id', 'members.server_id as server_id',
                'members.unix_user', 'members.mesh_address',
            ])
            ->distinct()
            ->orderBy('websites.id')->orderBy('members.id')
            ->get()
            ->map(fn (object $row) => (object) [
                'website_id' => $row->website_id, 'website_server_id' => $row->website_server_id, 'kind' => 'website',
                'member_id' => $row->member_id, 'server_id' => $row->server_id, 'name' => self::websiteName($row->unix_user),
                'port' => self::WEBSITE_PORT, 'mesh_address' => $row->mesh_address,
            ]);

        return $databases->concat($websites)->values();
    }

    /**
     * True when a website can connect to a database server on a different server: the two are
     * in one network, and the agents of the two servers can operate network members.
     */
    public static function reaches(Website $website, DatabaseServer $server): bool
    {
        if (self::hostOf($server) === null || $server->status === DatabaseServer::REMOVING) {
            return false;
        }
        $shared = NetworkMember::where('website_id', $website->id)
            ->whereIn('network_id', NetworkMember::where('database_server_id', $server->id)->select('network_id'))
            ->exists();

        return $shared && self::supports($website->server) && self::supports($server->server);
    }

    /**
     * True when a link is one that the servers can operate. A website is a member that
     * answers only when the agent of its server and the agent of the server of the client
     * can do this: an agent from before this function refuses port 80.
     *
     * @param  Collection<string, Server>  $servers  The servers, by ID.
     */
    private static function usable(object $link, Collection $servers): bool
    {
        if ($link->kind !== 'website') {
            return true;
        }
        $one = $servers->get($link->website_server_id);
        $two = $servers->get($link->server_id);

        return $one && $two && self::supportsWebsites($one) && self::supportsWebsites($two);
    }

    /**
     * The pairs of servers that need a tunnel: one has a website and the other has a member of
     * a network of that website. The key is the two server IDs, the smaller first.
     *
     * @return array<string, array{0: string, 1: string}>
     */
    public static function pairs(): array
    {
        $pairs = [];
        $servers = Server::all()->keyBy('id');
        foreach (self::links() as $link) {
            if ($link->website_server_id !== $link->server_id && $link->website_server_id && self::usable($link, $servers)) {
                $pair = [$link->website_server_id, $link->server_id];
                sort($pair);
                $pairs[implode(' ', $pair)] = $pair;
            }
        }

        return $pairs;
    }

    /**
     * The networks of a server for its agent.
     *
     * - "members": the database servers and the websites on this server that are network
     *   members, with the mesh addresses of the other servers that have a website in a network
     *   with them. A website is in the list only when a different website is in a network
     *   with it.
     * - "websites": the websites on this server, with the name, the address and the port of
     *   each member that they can connect to.
     * - "peerMembers": for each peer, the addresses of its members that a website of this
     *   server can connect to.
     *
     * @param  Collection<string, Server>  $peers  The servers that this server has a complete tunnel to.
     * @return array{members: list<array<string, mixed>>, websites: list<array<string, mixed>>, peerMembers: array<string, list<string>>}
     */
    public static function stateFor(Server $server, Collection $peers): array
    {
        $members = $websites = $peerMembers = $names = [];
        if (! self::supports($server)) {
            return ['members' => [], 'websites' => [], 'peerMembers' => []];
        }
        // A peer is in the networks only when its agent can operate network members.
        $peers = $peers->filter(fn (Server $peer) => self::supports($peer));
        $servers = $peers->all() + [$server->id => $server];
        $servers = collect($servers);
        $addresses = [];
        $address = function (object $link) use (&$addresses): string {
            return $addresses[$link->member_id] ??= $link->mesh_address
                ?? self::addressFor($link->kind === 'website' ? Website::findOrFail($link->member_id) : DatabaseServer::findOrFail($link->member_id));
        };
        foreach (self::links() as $link) {
            $local = $link->server_id === $server->id;
            $here = $link->website_server_id === $server->id;
            if ((! $local && ! $here) || ! self::usable($link, $servers)) {
                continue;
            }
            if ($local) {
                $members[$link->member_id] ??= ['id' => $link->member_id, 'address' => $address($link), 'port' => $link->port, 'servers' => []];
                if (! $here && $peers->has($link->website_server_id)) {
                    $members[$link->member_id]['servers'][] = $peers[$link->website_server_id]->mesh_address;
                }
            }
            if ($here && ($local || $peers->has($link->server_id))) {
                // The database servers are first in the links. If a database server and a
                // website have the same name, the name is the database server.
                if (isset($names[$link->website_id][$link->name])) {
                    continue;
                }
                $names[$link->website_id][$link->name] = true;
                $websites[$link->website_id] ??= ['id' => $link->website_id, 'names' => []];
                $websites[$link->website_id]['names'][] = ['name' => $link->name.self::DOMAIN, 'address' => $address($link), 'port' => $link->port];
                if (! $local) {
                    $peerMembers[$link->server_id][] = $address($link);
                }
            }
        }
        foreach ($members as &$member) {
            $member['servers'] = array_values(array_unique($member['servers']));
            sort($member['servers']);
        }
        unset($member);
        foreach ($peerMembers as &$list) {
            $list = array_values(array_unique($list));
            sort($list);
        }

        return ['members' => array_values($members), 'websites' => array_values($websites), 'peerMembers' => $peerMembers];
    }
}
