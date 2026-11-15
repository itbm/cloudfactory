<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Models\Network;
use App\Models\Server;
use App\Services\Mesh;
use App\Services\Networks;
use Tests\TestCase;

/**
 * Named networks, like a VPC: a customer makes networks in addition to "default", and selects
 * the websites and the database servers of each one. The plan gives the number.
 */
class NamedNetworkTest extends TestCase
{
    private function networkServer(): Server
    {
        return $this->server(['name' => 'web-1', 'agent_url' => 'https://203.0.113.1:7443',
            'mesh_public_key' => base64_encode(hash('sha256', 'web-1', true)), 'mesh_networks' => true, 'mesh_websites' => true,
            'database_engines' => ['mariadb', 'postgresql', 'valkey']]);
    }

    /** @return list<string> */
    private function names(Server $server, string $websiteId): array
    {
        $site = collect(Mesh::stateFor($server->fresh())['websites'] ?? [])->firstWhere('id', $websiteId);

        return array_column($site['names'] ?? [], 'name');
    }

    public function test_a_named_network_separates_the_websites_of_one_customer(): void
    {
        $server = $this->networkServer();
        $customer = $this->customer($this->plan(['networks' => 2]));
        $image = $this->image();
        $shop = $this->website($customer, $server, $image);
        $blog = $this->website($customer, $server, $image, 'blog.acme.example');
        $database = $this->databaseServerOf($shop, 'mariadb', ['name' => 'shop-db']);
        $owner = $this->user(Role::Owner, $customer);
        $this->actingWith($owner);

        // Each one is in "default", so the blog has the database server.
        $this->assertContains('shop-db.internal', $this->names($server, $blog->id));
        $default = $this->getJson('/api/v1/networks')->assertOk()->assertJsonCount(1, 'data')
            ->assertJsonPath('data.0.name', 'default')->assertJsonPath('data.0.isDefault', true)->json('data.0');
        $this->assertEqualsCanonicalizing([$shop->id, $blog->id], $default['websiteIds']);

        // A network for the shop and its database server, and the two leave "default".
        $shopNet = $this->postJson('/api/v1/networks', ['name' => 'shop', 'websiteIds' => [$shop->id], 'databaseServerIds' => [$database->id]])
            ->assertCreated()->assertJsonPath('isDefault', false)->json('id');
        $this->patchJson("/api/v1/networks/{$default['id']}", ['websiteIds' => [$blog->id], 'databaseServerIds' => []])->assertOk();

        $this->assertSame(['shop-db.internal'], $this->names($server, $shop->id));
        $this->assertNotContains('shop-db.internal', $this->names($server, $blog->id));
        $this->assertNotContains(Networks::hostOf($shop), $this->names($server, $blog->id));

        // The blog joins the network: it gets the database server and the shop again.
        $this->patchJson("/api/v1/networks/{$shopNet}", ['websiteIds' => [$shop->id, $blog->id]])->assertOk()
            ->assertJsonPath('databaseServerIds', [$database->id]);
        $this->assertEqualsCanonicalizing(['shop-db.internal', Networks::hostOf($shop)], $this->names($server, $blog->id));

        // A rename keeps the members. The network goes, and the blog loses the names.
        $this->patchJson("/api/v1/networks/{$shopNet}", ['name' => 'store'])->assertOk()->assertJsonPath('name', 'store');
        $this->deleteJson("/api/v1/networks/{$shopNet}")->assertNoContent();
        $this->assertSame([], $this->names($server, $blog->id));
        $this->assertSame(1, Network::count());
    }

    public function test_the_plan_limits_the_named_networks(): void
    {
        $customer = $this->customer($this->plan(['networks' => 1]));
        $this->actingWith($this->user(Role::Owner, $customer));
        $this->postJson('/api/v1/networks', ['name' => 'one'])->assertCreated();
        $this->postJson('/api/v1/networks', ['name' => 'two'])->assertJsonValidationErrors('name');
        $this->postJson('/api/v1/networks', ['name' => 'one'])->assertJsonValidationErrors('name');
        $this->getJson('/api/v1/account')->assertOk()->assertJsonPath('usage.networks', 1)->assertJsonPath('plan.networks', 1);

        // A plan with 0 has only "default".
        $other = $this->customer($this->plan(['name' => 'Starter']), ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->postJson('/api/v1/networks', ['name' => 'one'])->assertJsonValidationErrors('name');
    }

    public function test_the_networks_of_the_customers_of_a_reseller_count_against_the_plan_of_the_reseller(): void
    {
        $reseller = $this->customer($this->plan(['name' => 'Reseller', 'networks' => 2]), ['name' => 'Host Co', 'email' => 'it@host.example', 'role' => 'reseller']);
        $one = $this->customer($this->plan(['networks' => 5]), ['name' => 'One', 'email' => 'it@one.example', 'reseller_id' => $reseller->id]);
        $this->actingWith($this->user());
        $this->postJson('/api/v1/networks', ['name' => 'a', 'customerId' => $one->id])->assertCreated();
        $this->postJson('/api/v1/networks', ['name' => 'b', 'customerId' => $reseller->id])->assertCreated();
        $this->postJson('/api/v1/networks', ['name' => 'c', 'customerId' => $one->id])->assertJsonValidationErrors('name');
        $this->assertSame(2, Networks::countFor($reseller->fresh()));
    }

    public function test_a_network_takes_only_members_of_its_customer_and_keeps_its_default(): void
    {
        $server = $this->networkServer();
        $customer = $this->customer($this->plan(['networks' => 3]));
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $image = $this->image();
        $shop = $this->website($customer, $server, $image);
        $bolt = $this->website($other, $server, $image, 'bolt.example');
        $boltDb = $this->databaseServerOf($bolt, 'mariadb', ['name' => 'bolt-db']);
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson('/api/v1/networks', ['name' => 'mixed', 'websiteIds' => [$shop->id, $bolt->id]])->assertJsonValidationErrors('websiteIds');
        $this->postJson('/api/v1/networks', ['name' => 'mixed', 'databaseServerIds' => [$boltDb->id]])->assertJsonValidationErrors('databaseServerIds');
        $this->postJson('/api/v1/networks', ['name' => 'default'])->assertJsonValidationErrors('name');
        $this->postJson('/api/v1/networks', ['name' => 'Not Valid'])->assertJsonValidationErrors('name');
        $default = Network::where(['customer_id' => $customer->id, 'is_default' => true])->firstOrFail();
        $this->patchJson("/api/v1/networks/{$default->id}", ['name' => 'main'])->assertJsonValidationErrors('name');
        $this->deleteJson("/api/v1/networks/{$default->id}")->assertJsonValidationErrors('network');
        // The networks of a different customer are not visible.
        $boltNet = Networks::defaultFor($other->id);
        $this->getJson("/api/v1/networks/{$boltNet->id}")->assertNotFound();
        $this->patchJson("/api/v1/networks/{$boltNet->id}", ['websiteIds' => [$shop->id]])->assertNotFound();
        $this->assertSame([$default->id], array_column($this->getJson('/api/v1/networks')->json('data'), 'id'));
    }
}
