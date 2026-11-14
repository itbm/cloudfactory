<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\MeshTunnel;
use App\Models\Network;
use App\Models\NetworkMember;
use App\Models\Server;
use App\Services\JobOutcome;
use App\Services\Mesh;
use Illuminate\Support\Facades\Schema;
use Tests\TestCase;

/**
 * The private networks of the customers. A website connects to a database server of its
 * networks by the name "<name>.internal", on the same server and on a different server.
 */
class NetworkTest extends TestCase
{
    private static function key(string $seed): string
    {
        return base64_encode(hash('sha256', $seed, true));
    }

    /** A server whose agent reported its tunnel key, and that it can operate network members. */
    private function networkServer(string $name, string $address, array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => $name, 'agent_url' => "https://{$address}:7443", 'mesh_public_key' => self::key($name),
            'mesh_networks' => true, 'database_engines' => ['mariadb', 'postgresql', 'valkey']]);
    }

    private function meshJobs(Server $server): int
    {
        return AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetMesh->value)->count();
    }

    public function test_a_new_website_and_a_new_database_server_join_the_default_network(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $website = $this->website($customer, $server, $this->image());
        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db']);

        $network = Network::where('customer_id', $customer->id)->firstOrFail();
        $this->assertSame('default', $network->name);
        $this->assertTrue($network->is_default);
        $this->assertSame(1, Network::count());
        $this->assertDatabaseHas('network_members', ['network_id' => $network->id, 'website_id' => $website->id]);
        $this->assertDatabaseHas('network_members', ['network_id' => $network->id, 'database_server_id' => $database->id]);
        // The address is after the addresses for servers, and it does not change.
        $this->assertSame('10.207.1.0', $database->fresh()->mesh_address);
        $second = $this->databaseServerOf($website, 'postgresql', ['name' => 'reports']);
        $this->assertSame('10.207.1.1', $second->fresh()->mesh_address);

        // A second customer has its own network.
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $this->website($other, $server, $this->image(), 'bolt.example');
        $this->assertSame(2, Network::count());
        $this->assertSame(1, NetworkMember::whereIn('network_id', Network::where('customer_id', $other->id)->pluck('id'))->count());

        // The API gives the name and the port for the websites.
        $this->actingWith($this->user());
        $this->getJson("/api/v1/database-servers/{$database->id}")->assertOk()
            ->assertJsonPath('networkHost', 'shop-db.internal')->assertJsonPath('networkPort', 3306)->assertJsonPath('host', 'mariadb.internal');
        $this->getJson("/api/v1/database-servers/{$second->id}")->assertOk()
            ->assertJsonPath('networkHost', 'reports.internal')->assertJsonPath('networkPort', 5432);

        // The members go with the website and with the database server.
        $website->delete();
        $second->delete();
        $this->assertSame(1, NetworkMember::where('network_id', $network->id)->count());
    }

    public function test_a_website_gets_the_names_of_the_database_servers_on_its_own_server(): void
    {
        $server = $this->networkServer('web-1', '203.0.113.1');
        $customer = $this->customer();
        $website = $this->website($customer, $server, $this->image());
        // A customer with no database server changes no mesh state.
        $this->assertSame(0, $this->meshJobs($server));

        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db', 'port' => 3307]);
        $this->assertSame(1, $this->meshJobs($server));
        // The server has no tunnel: it gets the range, and no address and no port.
        $this->assertSame([
            'version' => 1,
            'range' => '10.207.0.0/16',
            'peers' => [],
            // The member answers on the usual port of its engine, not on its port on the server.
            'members' => [['id' => $database->id, 'address' => '10.207.1.0', 'port' => 3306, 'servers' => []]],
            'websites' => [['id' => $website->id, 'names' => [['name' => 'shop-db.internal', 'address' => '10.207.1.0', 'port' => 3306]]]],
        ], Mesh::stateFor($server->fresh()));
        $this->assertSame(0, MeshTunnel::count());
        $this->assertNull($server->fresh()->mesh_address);

        // The website of a different customer on the same server gets no name.
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $this->website($other, $server, $this->image(), 'bolt.example');
        $this->assertCount(1, Mesh::stateFor($server->fresh())['websites']);
        $this->assertSame(1, $this->meshJobs($server));

        // A second website of the customer gets the name, and the server gets the new state.
        AgentJob::query()->update(['state' => JobState::Succeeded->value]);
        $staging = $this->website($customer, $server, $this->image(), 'staging.acme.example');
        $this->assertSame(2, $this->meshJobs($server));
        $this->assertSame([$website->id, $staging->id], array_column(Mesh::stateFor($server->fresh())['websites'], 'id'));

        // A new name of the database server is a new name for the websites.
        AgentJob::query()->update(['state' => JobState::Succeeded->value]);
        $database->update(['name' => 'orders']);
        $this->assertSame(3, $this->meshJobs($server));
        $this->assertSame('orders.internal', Mesh::stateFor($server->fresh())['websites'][0]['names'][0]['name']);

        // A database server that the agent removes is not a member.
        $database->update(['status' => DatabaseServer::REMOVING]);
        $this->assertSame(['version' => 4, 'peers' => []], Mesh::stateFor($server->fresh()));
    }

    public function test_an_agent_from_before_the_networks_gets_no_network_fields(): void
    {
        $server = $this->server(['mesh_public_key' => self::key('old')]);
        $website = $this->website($this->customer(), $server, $this->image());
        $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db']);

        $this->assertSame(['version' => 0, 'peers' => []], Mesh::stateFor($server->fresh()));
        $this->assertSame(0, $this->meshJobs($server));

        // The agent gets a new release and reports that it can operate network members.
        app(Mesh::class)->reported($server, ['publicKey' => self::key('old'), 'version' => 0, 'networks' => true, 'tunnels' => []]);
        $this->assertTrue($server->fresh()->mesh_networks);
        $this->assertSame(1, $this->meshJobs($server));
        $this->assertCount(1, Mesh::stateFor($server->fresh())['members']);

        // A second report with the same values makes no second job.
        app(Mesh::class)->reported($server->fresh(), ['publicKey' => self::key('old'), 'version' => 1, 'networks' => true, 'tunnels' => []]);
        $this->assertSame(1, $this->meshJobs($server));
    }

    public function test_two_servers_get_a_tunnel_when_a_website_uses_a_database_server_on_the_other(): void
    {
        $web = $this->networkServer('web-1', '203.0.113.1');
        $db = $this->networkServer('db-1', '203.0.113.2');
        $third = $this->networkServer('web-3', '203.0.113.3');
        $customer = $this->customer();
        $website = $this->website($customer, $web, $this->image());
        $local = $this->website($customer, $db, $this->image(), 'api.acme.example');
        // The website of a different customer on the third server.
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $this->website($other, $third, $this->image(), 'bolt.example');
        $this->assertSame(0, MeshTunnel::count());

        $database = $this->databaseServerOf($local, 'postgresql', ['name' => 'orders']);

        // The panel made the tunnel. It is not a tunnel of an administrator.
        $tunnel = MeshTunnel::firstOrFail();
        $this->assertFalse($tunnel->pinned);
        $this->assertEqualsCanonicalizing([$web->id, $db->id], [$tunnel->server_a_id, $tunnel->server_b_id]);
        $this->assertSame(1, MeshTunnel::count());
        [$web, $db] = [$web->fresh(), $db->fresh()];
        $this->assertNotNull($web->mesh_address);
        $this->assertNotNull($db->mesh_address);
        $this->assertNull($third->fresh()->mesh_address);
        $this->assertSame([1, 1, 0], [$this->meshJobs($web), $this->meshJobs($db), $this->meshJobs($third)]);
        $address = $database->fresh()->mesh_address;

        // The server of the website: the tunnel carries the address of the member, and the
        // website has the name.
        $state = Mesh::stateFor($web);
        $this->assertSame([$address], $state['peers'][0]['members']);
        $this->assertSame($db->id, $state['peers'][0]['server']);
        $this->assertArrayNotHasKey('members', $state);
        $this->assertSame([['id' => $website->id, 'names' => [['name' => 'orders.internal', 'address' => $address, 'port' => 5432]]]], $state['websites']);

        // The server of the database server: only the server of the website can connect.
        $state = Mesh::stateFor($db);
        $this->assertSame([['id' => $database->id, 'address' => $address, 'port' => 5432, 'servers' => [$web->mesh_address]]], $state['members']);
        $this->assertArrayNotHasKey('members', $state['peers'][0]);
        $this->assertSame([['id' => $local->id, 'names' => [['name' => 'orders.internal', 'address' => $address, 'port' => 5432]]]], $state['websites']);

        // The API shows the tunnel, and an administrator cannot remove it while a website uses it.
        $this->actingWith($this->user());
        $this->getJson('/api/v1/mesh')->assertOk()->assertJsonPath('tunnels.0.pinned', false)->assertJsonPath('tunnels.0.inUse', true)
            ->assertJsonPath('servers.0.networks', true);
        $this->deleteJson("/api/v1/mesh/tunnels/{$tunnel->id}")->assertJsonValidationErrors('tunnel');
        $this->assertSame(1, MeshTunnel::count());

        // The website on the other server goes: the tunnel goes, and the two servers get the
        // new state.
        AgentJob::query()->update(['state' => JobState::Succeeded->value]);
        $website->delete();
        $this->assertSame(0, MeshTunnel::count());
        $this->assertSame([2, 2], [$this->meshJobs($web), $this->meshJobs($db)]);
        $this->assertSame(['version' => 2, 'peers' => []], Mesh::stateFor($web->fresh()));
        $this->assertSame([], Mesh::stateFor($db->fresh())['members'][0]['servers']);
    }

    public function test_a_tunnel_of_an_administrator_stays_when_no_network_uses_it(): void
    {
        $web = $this->networkServer('web-1', '203.0.113.1');
        $db = $this->networkServer('db-1', '203.0.113.2');
        app(Mesh::class)->connect($web, $db);
        $customer = $this->customer();
        $website = $this->website($customer, $web, $this->image());
        $local = $this->website($customer, $db, $this->image(), 'api.acme.example');
        $this->databaseServerOf($local, 'mariadb', ['name' => 'shop-db']);
        $this->assertSame(1, MeshTunnel::count());
        $this->assertTrue(MeshTunnel::firstOrFail()->pinned);

        $website->delete();
        $this->assertSame(1, MeshTunnel::count());
        $this->actingWith($this->user());
        $this->getJson('/api/v1/mesh')->assertOk()->assertJsonPath('tunnels.0.inUse', false);
        $this->deleteJson('/api/v1/mesh/tunnels/'.MeshTunnel::firstOrFail()->id)->assertAccepted();
        $this->assertSame(0, MeshTunnel::count());
    }

    public function test_a_server_that_cannot_have_a_tunnel_gets_no_name_of_the_other_server(): void
    {
        $web = $this->networkServer('web-1', '203.0.113.1');
        // The agent of this server is from before the networks.
        $old = $this->server(['name' => 'old-1', 'agent_url' => 'https://203.0.113.9:7443', 'mesh_public_key' => self::key('old-1')]);
        $customer = $this->customer();
        $website = $this->website($customer, $web, $this->image());
        $local = $this->website($customer, $old, $this->image(), 'api.acme.example');
        $this->databaseServerOf($local, 'mariadb', ['name' => 'shop-db']);

        $this->assertSame(0, MeshTunnel::count());
        $this->assertSame(['version' => 0, 'peers' => []], Mesh::stateFor($web->fresh()));
        $this->assertSame(['version' => 0, 'peers' => []], Mesh::stateFor($old->fresh()));

        // The old server gets a new agent: the tunnel and the name come.
        app(Mesh::class)->reported($old, ['publicKey' => self::key('old-1'), 'version' => 0, 'networks' => true, 'tunnels' => []]);
        $this->assertSame(1, MeshTunnel::count());
        $this->assertSame('shop-db.internal', Mesh::stateFor($web->fresh())['websites'][0]['names'][0]['name']);
        $this->assertSame($website->id, Mesh::stateFor($web->fresh())['websites'][0]['id']);
    }

    public function test_a_database_server_cannot_have_a_name_that_an_engine_uses(): void
    {
        $server = $this->networkServer('web-1', '203.0.113.1');
        $customer = $this->customer();
        $website = $this->website($customer, $server, $this->image());
        $this->actingWith($this->user());
        foreach (['mariadb', 'postgres', 'valkey', 'localhost'] as $name) {
            $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $customer->id, 'name' => $name])->assertJsonValidationErrors('name');
        }
        $this->assertSame(0, DatabaseServer::count());

        // A database server from before the networks can have such a name. It has no network
        // name, and the websites use the name of its engine and its port.
        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'mariadb']);
        $this->getJson("/api/v1/database-servers/{$database->id}")->assertOk()->assertJsonPath('networkHost', null)->assertJsonPath('host', 'mariadb.internal');
        $this->assertSame(['version' => 0, 'peers' => []], Mesh::stateFor($server->fresh()));
        $this->patchJson("/api/v1/database-servers/{$database->id}", ['name' => 'valkey'])->assertJsonValidationErrors('name');
        $this->patchJson("/api/v1/database-servers/{$database->id}", ['name' => 'shop-db'])->assertOk()->assertJsonPath('databaseServer.networkHost', 'shop-db.internal');
        $this->assertCount(1, Mesh::stateFor($server->fresh())['members']);
    }

    public function test_the_database_server_is_a_member_again_when_the_agent_cannot_remove_it(): void
    {
        $server = $this->networkServer('web-1', '203.0.113.1');
        $website = $this->website($this->customer(), $server, $this->image());
        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db', 'status' => DatabaseServer::RUNNING]);
        $this->actingWith($this->user());

        $job = AgentJob::findOrFail($this->deleteJson("/api/v1/database-servers/{$database->id}")->assertAccepted()->json('job.id'));
        $this->assertArrayNotHasKey('members', Mesh::stateFor($server->fresh()));
        app(JobOutcome::class)->finish($job, JobState::Failed, 'failed', []);
        $this->assertCount(1, Mesh::stateFor($server->fresh())['members']);

        AgentJob::query()->where('action', AgentAction::SetMesh->value)->update(['state' => JobState::Succeeded->value]);
        $job = AgentJob::findOrFail($this->deleteJson("/api/v1/database-servers/{$database->id}")->assertAccepted()->json('job.id'));
        app(JobOutcome::class)->finish($job, JobState::Succeeded, null, []);
        $this->assertSame(0, DatabaseServer::count());
        $this->assertSame(0, NetworkMember::whereNotNull('database_server_id')->count());
        $this->assertArrayNotHasKey('members', Mesh::stateFor($server->fresh()));
    }

    public function test_a_new_address_range_gives_the_members_new_addresses(): void
    {
        $server = $this->networkServer('web-1', '203.0.113.1');
        $website = $this->website($this->customer(), $server, $this->image());
        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db']);
        $this->actingWith($this->user());

        $this->patchJson('/api/v1/mesh', ['range' => '10.99.0.0/22'])->assertOk();
        $state = Mesh::stateFor($server->fresh());
        $this->assertSame('10.99.0.0/22', $state['range']);
        $this->assertSame('10.99.1.0', $state['members'][0]['address']);
        $this->assertSame('10.99.1.0', $database->fresh()->mesh_address);
    }

    public function test_the_migration_gives_each_customer_its_default_network(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $database = $this->databaseServerOf($website, 'mariadb');
        // The state from before the migration: no network.
        Network::query()->delete();
        $this->assertSame(0, NetworkMember::count());
        $empty = Customer::create(['name' => 'Empty', 'email' => 'it@empty.example', 'role' => 'owner', 'plan_id' => $customer->plan_id]);

        $migration = require database_path('migrations/2026_11_14_000700_add_networks.php');
        Schema::dropIfExists('network_members');
        Schema::dropIfExists('networks');
        Schema::table('database_servers', fn ($table) => $table->dropUnique(['mesh_address']));
        Schema::table('database_servers', fn ($table) => $table->dropColumn('mesh_address'));
        Schema::table('servers', fn ($table) => $table->dropColumn(['mesh_networks', 'mesh_state_hash']));
        $migration->up();

        $network = Network::where('customer_id', $customer->id)->firstOrFail();
        $this->assertTrue($network->is_default);
        $this->assertDatabaseHas('network_members', ['network_id' => $network->id, 'website_id' => $website->id]);
        $this->assertDatabaseHas('network_members', ['network_id' => $network->id, 'database_server_id' => $database->id]);
        $this->assertSame(0, Network::where('customer_id', $empty->id)->count());
    }
}
