<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Models\AgentJob;
use App\Models\DatabaseServer;
use App\Models\MeshTunnel;
use App\Models\Network;
use App\Models\NetworkMember;
use App\Models\Server;
use App\Models\Website;
use App\Services\Mesh;
use App\Services\Networks;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

/**
 * The reference size of the networks: 20 servers, 100 websites and 20 database servers of
 * different engines in one network. Each website connects to each other member, on its own
 * server and on the 19 other servers.
 */
class NetworkScaleTest extends TestCase
{
    private const SERVERS = 20;

    private const WEBSITES = 100;

    private const DATABASE_SERVERS = 20;

    /** @var list<Server> */
    private array $servers = [];

    /** @var list<Website> */
    private array $websites = [];

    /** @var list<DatabaseServer> */
    private array $databaseServers = [];

    private function build(): void
    {
        $customer = $this->customer($this->plan(['websites' => self::WEBSITES + 10, 'database_servers' => self::DATABASE_SERVERS, 'networks' => 5]));
        $image = $this->image();
        // One transaction for all the records, as a seeder or an import makes them. The panel
        // then calculates the mesh one time, after the commit.
        DB::transaction(function () use ($customer, $image) {
            for ($i = 1; $i <= self::SERVERS; $i++) {
                $this->servers[] = $this->server(['name' => sprintf('web-%02d', $i), 'agent_url' => "https://203.0.113.{$i}:7443",
                    'mesh_public_key' => base64_encode(hash('sha256', "web-{$i}", true)), 'mesh_networks' => true, 'mesh_websites' => true,
                    'database_engines' => ['mariadb', 'postgresql', 'valkey']]);
            }
            for ($i = 0; $i < self::WEBSITES; $i++) {
                $this->websites[] = $this->website($customer, $this->servers[$i % self::SERVERS], $image, sprintf('site%03d.acme.example', $i));
            }
            $engines = ['mariadb', 'postgresql', 'valkey'];
            for ($i = 0; $i < self::DATABASE_SERVERS; $i++) {
                $engine = $engines[$i % 3];
                $this->databaseServers[] = $this->databaseServerOf($this->websites[$i], $engine, ['name' => sprintf('db-%02d', $i),
                    'port' => DatabaseServer::FIRST_PORT[$engine] + intdiv($i, 3)]);
            }
        });
        // The test data never commits, so autovacuum does not analyse it. PostgreSQL then plans
        // the queries of the mesh with the statistics of earlier tests (the demo data, for
        // example), and a bad plan can make one change take minutes. A server keeps its
        // statistics current with autovacuum, so the test analyses the tables here.
        if (DB::getDriverName() === 'pgsql') {
            DB::statement('ANALYZE');
        }
    }

    /** @return list<string> */
    private function names(array $state, string $websiteId): array
    {
        $site = collect($state['websites'] ?? [])->firstWhere('id', $websiteId);

        return array_column($site['names'] ?? [], 'name');
    }

    public function test_one_network_at_the_reference_size(): void
    {
        $started = microtime(true);
        $this->build();
        $built = microtime(true) - $started;

        // Each server has a website and a database server, so each pair of servers has a tunnel.
        $this->assertSame(self::SERVERS * (self::SERVERS - 1) / 2, MeshTunnel::count());
        $this->assertSame(self::WEBSITES + self::DATABASE_SERVERS, NetworkMember::count());
        $this->assertSame(1, Network::count());
        // The member addresses are unique, and they are not the addresses of the servers.
        $addresses = collect([...Website::pluck('mesh_address'), ...DatabaseServer::pluck('mesh_address'), ...Server::pluck('mesh_address')])->filter();
        $this->assertSame(self::WEBSITES + self::DATABASE_SERVERS + self::SERVERS, $addresses->count());
        $this->assertSame($addresses->count(), $addresses->unique()->count());

        $states = [];
        foreach ($this->servers as $server) {
            $states[$server->id] = Mesh::stateFor($server->fresh());
        }
        $first = $this->servers[0];
        $state = $states[$first->id];
        $this->assertCount(self::SERVERS - 1, $state['peers']);
        // A website has the name of each database server and of each other website.
        $site = $this->websites[0];
        $names = $this->names($state, $site->id);
        $this->assertCount(self::DATABASE_SERVERS + self::WEBSITES - 1, $names);
        $this->assertContains('db-19.internal', $names);
        $this->assertContains(Networks::hostOf($this->websites[99]), $names);
        $this->assertNotContains(Networks::hostOf($site), $names);
        // A member on this server accepts the 19 other servers.
        $member = collect($state['members'])->firstWhere('id', $this->databaseServers[0]->id);
        $this->assertCount(self::SERVERS - 1, $member['servers']);
        // Each tunnel carries the members of the other server: 5 websites and 1 database server.
        $this->assertCount(self::WEBSITES / self::SERVERS + self::DATABASE_SERVERS / self::SERVERS, $state['peers'][0]['members']);
        // The state of a server stays small for its agent job.
        foreach ($states as $s) {
            $this->assertLessThan(256 * 1024, strlen(json_encode($s)));
        }

        // Each server got one job with the state.
        foreach ($this->servers as $server) {
            $this->assertSame(1, AgentJob::where(['server_id' => $server->id, 'action' => AgentAction::SetMesh->value])->count());
        }
        $this->assertLessThan(30, $built, "The panel needed {$built} seconds for the reference size.");

        // One more website at this size: a change takes a short time, and each server gets the new name.
        $queries = 0;
        DB::listen(function () use (&$queries) {
            $queries++;
        });
        $started = microtime(true);
        $new = $this->website($this->websites[0]->customer, $this->servers[5], $this->image(), 'new.acme.example');
        $change = microtime(true) - $started;
        $this->assertLessThan(5, $change, "One change needed {$change} seconds.");
        $this->assertLessThan(1000, $queries, "One change needed {$queries} queries.");
        $this->assertContains(Networks::hostOf($new), $this->names(Mesh::stateFor($this->servers[0]->fresh()), $site->id));
    }

    public function test_a_named_network_at_the_reference_size(): void
    {
        $this->build();
        $customer = $this->websites[0]->customer;
        $default = Networks::defaultFor($customer->id);
        // The first 10 websites and the first 2 database servers go to their own network.
        $inside = array_slice($this->websites, 0, 10);
        $databases = array_slice($this->databaseServers, 0, 2);
        $this->actingWith($this->user());
        $backend = $this->postJson('/api/v1/networks', ['name' => 'backend', 'customerId' => $customer->id,
            'websiteIds' => array_map(fn ($w) => $w->id, $inside), 'databaseServerIds' => array_map(fn ($d) => $d->id, $databases)])->assertCreated()->json('id');
        $this->patchJson("/api/v1/networks/{$default->id}", [
            'websiteIds' => array_map(fn ($w) => $w->id, array_slice($this->websites, 10)),
            'databaseServerIds' => array_map(fn ($d) => $d->id, array_slice($this->databaseServers, 2)),
        ])->assertOk();
        $this->assertNotNull($backend);

        $inSite = $this->websites[0];
        $outSite = $this->websites[50];
        $inNames = $this->names(Mesh::stateFor($inSite->server->fresh()), $inSite->id);
        $outNames = $this->names(Mesh::stateFor($outSite->server->fresh()), $outSite->id);
        $this->assertCount(10 - 1 + 2, $inNames);
        $this->assertContains('db-01.internal', $inNames);
        $this->assertNotContains('db-02.internal', $inNames);
        $this->assertCount(90 - 1 + 18, $outNames);
        $this->assertNotContains('db-00.internal', $outNames);
        $this->assertNotContains(Networks::hostOf($inSite), $outNames);
    }
}
