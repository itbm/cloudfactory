<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Website;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

class WebsiteTest extends TestCase
{
    public function test_creating_a_website_queues_a_job_for_the_agent(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $this->actingWith($this->user(Role::Owner, $customer));

        $response = $this->postJson('/api/v1/websites', ['name' => 'acme.example', 'imageId' => $image->id, 'planId' => $customer->plan_id])
            ->assertAccepted()
            ->assertJsonPath('website.name', 'acme.example')
            ->assertJsonPath('website.status', 'starting')
            ->assertJsonPath('job.state', 'queued');

        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame($server->id, $job->server_id);
        $this->assertSame('acme.example', $job->payload['website']['domains'][0]['name']);
        Queue::assertPushed(SendJobToAgent::class, fn ($q) => $q->jobId === $job->id);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Created website', 'target' => 'acme.example']);
    }

    public function test_the_website_name_must_be_a_domain_that_is_not_in_use(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $this->website($customer, $server, $image, 'acme.example');
        $this->actingWith($this->user(Role::Owner, $customer));

        foreach (['acme.example', 'Not a domain', 'nodot', '-bad.example'] as $name) {
            $this->postJson('/api/v1/websites', ['name' => $name, 'imageId' => $image->id, 'planId' => $customer->plan_id])
                ->assertJsonValidationErrors('name');
        }
    }

    public function test_the_plan_limits_the_number_of_websites(): void
    {
        $customer = $this->customer($this->plan(['name' => 'Starter', 'websites' => 1]));
        $server = $this->server();
        $image = $this->image();
        $this->website($customer, $server, $image, 'one.example');
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson('/api/v1/websites', ['name' => 'two.example', 'imageId' => $image->id, 'planId' => $customer->plan_id])
            ->assertJsonValidationErrors(['name' => 'The Starter plan allows 1 websites.']);
    }

    public function test_an_owner_sees_only_their_websites(): void
    {
        $plan = $this->plan();
        $mine = $this->customer($plan);
        $other = $this->customer($plan, ['name' => 'Other']);
        $server = $this->server();
        $image = $this->image();
        $this->website($mine, $server, $image, 'mine.example');
        $theirs = $this->website($other, $server, $image, 'theirs.example');
        $this->actingWith($this->user(Role::Owner, $mine));

        $this->getJson('/api/v1/websites')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.name', 'mine.example');
        $this->getJson("/api/v1/websites/{$theirs->id}")->assertNotFound();
    }

    public function test_a_reseller_sees_the_websites_of_their_customers(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['name' => 'Agency', 'role' => 'reseller']);
        $client = $this->customer($plan, ['name' => 'Bakery', 'reseller_id' => $reseller->id]);
        $stranger = $this->customer($plan, ['name' => 'Stranger']);
        $server = $this->server();
        $image = $this->image();
        $this->website($client, $server, $image, 'bakery.example');
        $this->website($stranger, $server, $image, 'stranger.example');
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $this->getJson('/api/v1/websites')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.name', 'bakery.example');
    }

    public function test_only_administrators_set_limits_directly(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $limits = ['vcpus' => 4, 'memoryBytes' => 1, 'diskBytes' => 1, 'diskIoBytesPerSecond' => 1, 'processes' => 10];

        $this->actingWith($this->user(Role::Owner, $customer));
        $this->patchJson("/api/v1/websites/{$website->id}", ['limits' => $limits])->assertForbidden();

        $this->actingWith($this->user());
        $this->patchJson("/api/v1/websites/{$website->id}", ['limits' => $limits])->assertOk()->assertJsonPath('job.action', 'SetLimits');
    }

    public function test_power_actions_change_the_status(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'stop'])->assertAccepted();
        $this->assertSame('stopping', $website->fresh()->status->value);
        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'explode'])->assertJsonValidationErrors('action');
    }

    public function test_a_staging_copy_is_a_new_website(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image(), 'www.acme.example');
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/staging")->assertAccepted()->assertJsonPath('website.name', 'staging.acme.example');
        $this->assertSame($website->id, Website::where('name', 'staging.acme.example')->value('staging_of'));
        $this->postJson("/api/v1/websites/{$website->id}/staging")->assertJsonValidationErrors('name');
    }

    public function test_domains_redirect_or_alias_and_the_primary_stays(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'www.acme.example', 'kind' => 'redirect'])->assertAccepted();
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'www.acme.example', 'kind' => 'alias'])->assertJsonValidationErrors('name');
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'other.example', 'kind' => 'primary'])->assertJsonValidationErrors('kind');
        $this->deleteJson("/api/v1/websites/{$website->id}/domains/acme.example")->assertJsonValidationErrors('name');
        $this->deleteJson("/api/v1/websites/{$website->id}/domains/www.acme.example")->assertAccepted();
    }
}
