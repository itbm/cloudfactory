<?php

namespace Tests\Feature;

use App\Models\MeshTunnel;
use App\Models\Server;
use App\Services\Mesh;
use App\Services\Networks;
use Tests\TestCase;

/**
 * A website is a network member that answers: a different website in a network with it
 * connects to "<name>.internal" on port 80, on the same server and on a different server.
 */
class WebsiteMemberTest extends TestCase
{
    private function networkServer(string $name, string $address, array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => $name, 'agent_url' => "https://{$address}:7443",
            'mesh_public_key' => base64_encode(hash('sha256', $name, true)), 'mesh_networks' => true, 'mesh_websites' => true,
            'database_engines' => ['mariadb', 'postgresql', 'valkey']]);
    }

    public function test_two_websites_on_one_server_have_the_names_of_each_other(): void
    {
        $server = $this->networkServer('web-1', '203.0.113.1');
        $customer = $this->customer();
        $shop = $this->website($customer, $server, $this->image());
        // One website has no member that answers, so it has no address.
        $this->assertSame([], Networks::stateFor($server->fresh(), collect())['members']);
        $this->assertNull($shop->fresh()->mesh_address);

        $api = $this->website($customer, $server, $this->image(), 'api.acme.example');
        // The website of a different customer is in no network with them.
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $bolt = $this->website($other, $server, $this->image(), 'bolt.example');

        $state = Mesh::stateFor($server->fresh());
        [$shop, $api] = [$shop->fresh(), $api->fresh()];
        $this->assertNotNull($shop->mesh_address);
        $this->assertNotNull($api->mesh_address);
        $this->assertNotSame($shop->mesh_address, $api->mesh_address);
        $this->assertNull($bolt->fresh()->mesh_address);
        $this->assertSame(Networks::websiteName($shop->unix_user).'.internal', Networks::hostOf($shop));
        $this->assertStringNotContainsString('_', Networks::hostOf($shop));
        // No tunnel: the two websites are on one server.
        $this->assertSame(0, MeshTunnel::count());
        $this->assertSame([], $state['peers']);
        $this->assertEqualsCanonicalizing([
            ['id' => $shop->id, 'address' => $shop->mesh_address, 'port' => 80, 'servers' => []],
            ['id' => $api->id, 'address' => $api->mesh_address, 'port' => 80, 'servers' => []],
        ], $state['members']);
        $this->assertEqualsCanonicalizing([
            ['id' => $shop->id, 'names' => [['name' => Networks::hostOf($api), 'address' => $api->mesh_address, 'port' => 80]]],
            ['id' => $api->id, 'names' => [['name' => Networks::hostOf($shop), 'address' => $shop->mesh_address, 'port' => 80]]],
        ], $state['websites']);

        // A database server and a website have their addresses from one range.
        $database = $this->databaseServerOf($shop, 'mariadb', ['name' => 'shop-db']);
        $this->assertNotContains($database->fresh()->mesh_address, [$shop->mesh_address, $api->mesh_address]);

        // The API gives the name and the port of the website.
        $this->actingWith($this->user());
        $this->getJson("/api/v1/websites/{$shop->id}")->assertOk()
            ->assertJsonPath('data.networkHost', Networks::hostOf($shop))->assertJsonPath('data.networkPort', 80);
    }

    public function test_two_servers_get_a_tunnel_for_two_websites_of_one_customer(): void
    {
        $one = $this->networkServer('web-1', '203.0.113.1');
        $two = $this->networkServer('web-2', '203.0.113.2');
        $customer = $this->customer();
        $shop = $this->website($customer, $one, $this->image());
        $api = $this->website($customer, $two, $this->image(), 'api.acme.example');

        $this->assertSame(1, MeshTunnel::count());
        [$one, $two, $shop, $api] = [$one->fresh(), $two->fresh(), $shop->fresh(), $api->fresh()];
        $state = Mesh::stateFor($one);
        // The website of this server answers the other server, and connects to the website there.
        $this->assertSame([['id' => $shop->id, 'address' => $shop->mesh_address, 'port' => 80, 'servers' => [$two->mesh_address]]], $state['members']);
        $this->assertSame([['id' => $shop->id, 'names' => [['name' => Networks::hostOf($api), 'address' => $api->mesh_address, 'port' => 80]]]], $state['websites']);
        $this->assertSame([$api->mesh_address], $state['peers'][0]['members']);
        $state = Mesh::stateFor($two);
        $this->assertSame([['id' => $api->id, 'address' => $api->mesh_address, 'port' => 80, 'servers' => [$one->mesh_address]]], $state['members']);
        $this->assertSame([$shop->mesh_address], $state['peers'][0]['members']);

        // The website on the second server goes: the tunnel goes, and the first website is
        // no member that answers.
        $api->delete();
        $this->assertSame(0, MeshTunnel::count());
        $this->assertArrayNotHasKey('members', Mesh::stateFor($one->fresh()));
    }

    public function test_an_agent_from_before_this_function_gets_no_website_as_a_member(): void
    {
        $one = $this->networkServer('web-1', '203.0.113.1');
        $old = $this->networkServer('web-2', '203.0.113.2', ['mesh_websites' => false]);
        $customer = $this->customer();
        $shop = $this->website($customer, $one, $this->image());
        $api = $this->website($customer, $one, $this->image(), 'api.acme.example');
        $legacy = $this->website($customer, $old, $this->image(), 'old.acme.example');
        $second = $this->website($customer, $old, $this->image(), 'old2.acme.example');

        // No tunnel for the websites, and the old agent gets no member on port 80.
        $this->assertSame(0, MeshTunnel::count());
        $state = Mesh::stateFor($old->fresh());
        $this->assertArrayNotHasKey('members', $state);
        $this->assertArrayNotHasKey('websites', $state);
        // The websites on the new agent have only the names of each other.
        $state = Mesh::stateFor($one->fresh());
        $this->assertCount(2, $state['members']);
        $this->assertEqualsCanonicalizing([[Networks::hostOf($api)], [Networks::hostOf($shop)]], array_map(fn ($w) => array_column($w['names'], 'name'), $state['websites']));

        // The agent reports that it can do it: the tunnel and the names come.
        app(Mesh::class)->reported($old->fresh(), ['publicKey' => $old->mesh_public_key, 'version' => 0, 'networks' => true, 'websites' => true, 'tunnels' => []]);
        $this->assertTrue($old->fresh()->mesh_websites);
        $this->assertSame(1, MeshTunnel::count());
        $this->assertCount(2, Mesh::stateFor($old->fresh())['members']);
        $this->assertNotNull($legacy->fresh()->mesh_address);
        $this->assertNotNull($second->fresh()->mesh_address);
    }

    public function test_a_database_server_keeps_its_name_when_a_website_has_the_same_name(): void
    {
        $server = $this->networkServer('web-1', '203.0.113.1');
        $customer = $this->customer();
        $shop = $this->website($customer, $server, $this->image());
        $api = $this->website($customer, $server, $this->image(), 'api.acme.example');
        // The panel refuses the name of a website of the customer for a new database server.
        $this->actingWith($this->user());
        $this->postJson('/api/v1/database-servers', ['customerId' => $customer->id, 'serverId' => $server->id, 'engine' => 'mariadb', 'name' => Networks::websiteName($api->unix_user)])
            ->assertJsonValidationErrors('name');

        // A record from a different path: the name is the database server, one time.
        $database = $this->databaseServerOf($shop, 'mariadb', ['name' => Networks::websiteName($api->unix_user)]);
        $names = collect(Mesh::stateFor($server->fresh())['websites'])->firstWhere('id', $shop->id)['names'];
        $this->assertSame([['name' => Networks::hostOf($api), 'address' => $database->fresh()->mesh_address, 'port' => 3306]], $names);
    }
}
