<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Models\AgentJob;
use App\Models\Server;
use App\Models\Website;
use App\Services\Mesh;
use App\Services\Networks;
use Tests\TestCase;

/**
 * A private website has no domain: only the websites of its networks connect to it. A worker
 * runs a command of the customer and serves no HTTP. Each one is a website of the plan.
 */
class WebsiteKindTest extends TestCase
{
    private function networkServer(array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => 'web-1', 'agent_url' => 'https://203.0.113.1:7443',
            'mesh_public_key' => base64_encode(hash('sha256', 'web-1', true)), 'mesh_networks' => true, 'mesh_websites' => true]);
    }

    public function test_a_private_website_has_a_network_name_and_no_domain(): void
    {
        $server = $this->networkServer();
        $customer = $this->customer();
        $image = $this->image();
        $shop = $this->website($customer, $server, $image);
        $this->actingWith($this->user());

        $response = $this->postJson('/api/v1/websites', ['kind' => 'private', 'name' => 'orders-api', 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertStatus(202)->assertJsonPath('website.kind', 'private')->assertJsonPath('website.domains', []);
        $private = Website::findOrFail($response->json('website.id'));
        // The name is the network name: the first 8 letters, 4 random characters and ".internal".
        $this->assertMatchesRegularExpression('/^ordersap-[a-z0-9]{4}\.internal$/', $private->name);
        $this->assertSame($private->name, Networks::hostOf($private));
        $response->assertJsonPath('website.networkHost', $private->name)->assertJsonPath('website.networkPort', 80)->assertJsonPath('website.command', null);
        $this->assertSame(0, $private->domains()->count());

        $job = AgentJob::where('action', AgentAction::CreateWebsite->value)->latest('id')->firstOrFail();
        $this->assertSame([], $job->payload['website']['domains']);
        $this->assertFalse($job->payload['website']['worker']);

        // The other website of the customer has its name, and the private website is a member.
        $state = Mesh::stateFor($server->fresh());
        $names = collect($state['websites'])->firstWhere('id', $shop->id)['names'];
        $this->assertSame([['name' => $private->name, 'address' => $private->fresh()->mesh_address, 'port' => 80]], $names);
        $this->assertContains($private->id, array_column($state['members'], 'id'));

        // It has no public address: no domain, and no staging copy.
        $this->postJson("/api/v1/websites/{$private->id}/domains", ['name' => 'orders.acme.example', 'kind' => 'alias'])->assertJsonValidationErrors('name');
        $this->postJson("/api/v1/websites/{$private->id}/staging")->assertJsonValidationErrors('website');
        $this->putJson("/api/v1/websites/{$private->id}/runtime", ['command' => 'php worker.php'])->assertJsonValidationErrors('command');
    }

    public function test_a_worker_runs_the_command_of_the_customer_and_is_no_member_that_answers(): void
    {
        $server = $this->networkServer();
        $customer = $this->customer();
        $image = $this->image();
        $shop = $this->website($customer, $server, $image);
        $this->actingWith($this->user());

        // A worker needs a command. A website cannot have one.
        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'queue', 'imageId' => $image->id, 'customerId' => $customer->id])->assertJsonValidationErrors('command');
        $this->postJson('/api/v1/websites', ['name' => 'new.acme.example', 'command' => 'php x.php', 'imageId' => $image->id, 'customerId' => $customer->id])->assertJsonValidationErrors('command');
        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'queue', 'command' => "php a.php\nrm -rf /", 'imageId' => $image->id, 'customerId' => $customer->id])->assertJsonValidationErrors('command');
        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'Queue.Example', 'command' => 'php a.php', 'imageId' => $image->id, 'customerId' => $customer->id])->assertJsonValidationErrors('name');

        $command = 'php artisan queue:work --tries=3 && echo "done"';
        $response = $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'queue', 'command' => $command, 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertStatus(202)->assertJsonPath('website.kind', 'worker')->assertJsonPath('website.command', $command)
            ->assertJsonPath('website.networkHost', null)->assertJsonPath('website.networkPort', null);
        $worker = Website::findOrFail($response->json('website.id'));
        $this->assertMatchesRegularExpression('/^queue-[a-z0-9]{4}\.internal$/', $worker->name);

        // The agent runs the command in the shell of the container of the worker.
        $job = AgentJob::where('action', AgentAction::CreateWebsite->value)->latest('id')->firstOrFail();
        $this->assertSame(['sh', '-c', $command], $job->payload['website']['command']);
        $this->assertTrue($job->payload['website']['worker']);
        $this->assertSame([], $job->payload['website']['domains']);

        // The worker connects to the website. No website connects to the worker.
        $state = Mesh::stateFor($server->fresh());
        $this->assertSame([$shop->id], array_column($state['members'], 'id'));
        $names = collect($state['websites'])->keyBy('id');
        $this->assertSame([Networks::hostOf($shop)], array_column($names[$worker->id]['names'], 'name'));
        $this->assertArrayNotHasKey($shop->id, $names->all());
        $this->assertNull($worker->fresh()->mesh_address);

        // The owner changes the command: the agent starts a new container.
        $this->putJson("/api/v1/websites/{$worker->id}/runtime", ['command' => 'php artisan horizon'])->assertStatus(202)->assertJsonPath('website.command', 'php artisan horizon');
        $job = AgentJob::where('action', AgentAction::SetRuntime->value)->latest('id')->firstOrFail();
        $this->assertSame(['sh', '-c', 'php artisan horizon'], $job->payload['website']['command']);
        $this->assertSame($command, $job->payload['revert']['command']);
        $this->postJson("/api/v1/websites/{$worker->id}/domains", ['name' => 'queue.acme.example', 'kind' => 'alias'])->assertJsonValidationErrors('name');
    }

    public function test_a_private_website_and_a_worker_count_against_the_website_limit(): void
    {
        $server = $this->networkServer();
        $customer = $this->customer();
        $customer->plan->update(['websites' => 2]);
        $image = $this->image();
        $this->actingWith($this->user());

        $this->postJson('/api/v1/websites', ['kind' => 'private', 'name' => 'api', 'imageId' => $image->id, 'customerId' => $customer->id])->assertStatus(202);
        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'queue', 'command' => 'php q.php', 'imageId' => $image->id, 'customerId' => $customer->id])->assertStatus(202);
        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'mail', 'command' => 'php m.php', 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertJsonValidationErrors('name');
        $this->postJson('/api/v1/websites', ['name' => 'shop.acme.example', 'imageId' => $image->id, 'customerId' => $customer->id])->assertJsonValidationErrors('name');
        $this->assertSame(2, $customer->websites()->count());
    }

    public function test_a_server_with_an_agent_from_before_this_function_gets_no_worker(): void
    {
        $server = $this->networkServer(['mesh_websites' => false]);
        $customer = $this->customer();
        $image = $this->image();
        $this->actingWith($this->user());

        $this->postJson('/api/v1/websites', ['kind' => 'worker', 'name' => 'queue', 'command' => 'php q.php', 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertJsonValidationErrors('kind');
        $this->postJson('/api/v1/websites', ['kind' => 'private', 'name' => 'api', 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertJsonValidationErrors('kind');
        $this->assertSame(0, Website::count());
    }
}
