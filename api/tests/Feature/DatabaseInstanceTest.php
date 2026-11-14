<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Server;
use App\Models\Website;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * Each customer has its own database containers on a server, so a customer cannot change the
 * service of a different customer. A website of an earlier release uses the shared database
 * containers until an administrator moves the databases of its server.
 */
class DatabaseInstanceTest extends TestCase
{
    private function newServer(array $attributes = []): Server
    {
        return $this->server($attributes + ['database_engines' => ['mariadb', 'postgresql', 'valkey'], 'database_instances' => true]);
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null);

        return $job->fresh();
    }

    public function test_a_new_website_uses_the_database_containers_of_its_customer(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $server = $this->newServer();
        $image = $this->image();

        $response = $this->postJson('/api/v1/websites', ['name' => 'shop.localhost', 'imageId' => $image->id, 'customerId' => $customer->id, 'serverId' => $server->id])
            ->assertAccepted()->assertJsonPath('website.sharedDatabases', false);

        $website = Website::findOrFail($response->json('website.id'));
        $this->assertSame($customer->id, $website->database_instance);
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame($customer->id, $job->payload['website']['databaseInstance']);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('databaseInstances', true)->assertJsonPath('sharedDatabaseWebsites', 0);
    }

    public function test_a_website_on_a_server_with_an_agent_of_an_earlier_release_uses_the_shared_containers(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $server = $this->server();
        $image = $this->image();

        $response = $this->postJson('/api/v1/websites', ['name' => 'shop.localhost', 'imageId' => $image->id, 'customerId' => $customer->id, 'serverId' => $server->id])
            ->assertAccepted()->assertJsonPath('website.sharedDatabases', true);

        $website = Website::findOrFail($response->json('website.id'));
        $this->assertNull($website->database_instance);
        $this->assertSame('', AgentJob::findOrFail($response->json('job.id'))->payload['website']['databaseInstance']);
        // The panel shows the number of websites to move only when the agent can move them.
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('databaseInstances', false)->assertJsonPath('sharedDatabaseWebsites', null);
        // The shared containers have no Valkey.
        $server->update(['database_engines' => ['mariadb', 'postgresql', 'valkey']]);
        $website->update(['status' => WebsiteStatus::Running]);
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertJsonValidationErrors('engine');
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
    }

    public function test_the_agent_reports_that_it_has_database_containers_for_each_customer(): void
    {
        $server = $this->server(['local' => true, 'agent_token_hash' => hash('sha256', 'cfa_instances'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'connection_checked_at' => now()]);
        $this->assertFalse($server->fresh()->database_instances);

        $this->asLocalAgent('cfa_instances')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'databaseInstances' => true])->assertOk();
        $this->assertTrue($server->fresh()->database_instances);
        $this->assertSame('01jbq3z5k8n6t4w2x9y7r5p3c1', $server->fresh()->databaseInstanceFor('01jbq3z5k8n6t4w2x9y7r5p3c1'));

        // An agent of an earlier release does not send the field.
        $this->asLocalAgent('cfa_instances')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.9'])->assertOk();
        $this->assertFalse($server->fresh()->database_instances);
        $this->assertNull($server->fresh()->databaseInstanceFor('01jbq3z5k8n6t4w2x9y7r5p3c1'));
    }

    public function test_a_staging_copy_uses_the_containers_of_its_customer_also_when_the_source_does_not(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $source = $this->website($customer, $this->newServer(), $this->image());
        $source->databases()->create(['engine' => 'mariadb', 'name' => $source->unix_user.'_shop']);

        $response = $this->postJson("/api/v1/websites/{$source->id}/staging")->assertAccepted();

        $copy = Website::findOrFail($response->json('website.id'));
        $this->assertNull($source->fresh()->database_instance);
        $this->assertSame($customer->id, $copy->database_instance);
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame($customer->id, $job->payload['website']['databaseInstance']);
        $this->assertSame($source->id, $job->payload['copyFrom']);
    }

    public function test_a_failed_website_with_no_databases_gets_the_containers_of_its_customer_at_the_next_attempt(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $server = $this->newServer();
        $website = $this->website($customer, $server, $this->image(), attributes: ['status' => 'failed']);
        $withData = $this->website($customer, $server, $this->image(), 'data.example', ['status' => 'failed']);
        $withData->databases()->create(['engine' => 'mariadb', 'name' => $withData->unix_user.'_shop']);

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/retry")->assertAccepted()->json('job.id');
        $this->assertSame($customer->id, $website->fresh()->database_instance);
        $this->assertSame($customer->id, AgentJob::findOrFail($jobId)->payload['website']['databaseInstance']);

        // A website with databases keeps the shared containers until its databases move.
        $this->postJson("/api/v1/websites/{$withData->id}/retry")->assertAccepted();
        $this->assertNull($withData->fresh()->database_instance);
    }

    public function test_an_administrator_moves_the_databases_of_a_server_to_the_containers_of_each_customer(): void
    {
        $this->actingWith($this->user());
        $server = $this->newServer();
        $image = $this->image();
        $acme = $this->customer();
        $bolt = Customer::create(['name' => 'Bolt', 'email' => 'it@bolt.example', 'role' => 'owner', 'plan_id' => $acme->plan_id]);
        $shop = $this->website($acme, $server, $image);
        $blog = $this->website($bolt, $server, $image, 'bolt.example');
        $new = $this->website($acme, $server, $image, 'new.example', ['database_instance' => $acme->id]);
        $starting = $this->website($acme, $server, $image, 'starting.example', ['status' => 'starting']);
        $failed = $this->website($acme, $server, $image, 'failed.example', ['status' => 'failed']);
        $other = $this->website($acme, $this->newServer(), $image, 'other.example');

        $name = $shop->unix_user.'_shop';
        $database = $shop->databases()->create(['engine' => 'mariadb', 'name' => $name]);
        $data = $shop->databases()->create(['engine' => 'postgresql', 'name' => $shop->unix_user.'_data']);
        $user = $shop->databaseUsers()->create(['engine' => 'mariadb', 'name' => $name, 'access' => 'anywhere']);
        $user->databases()->attach($database->id, ['privileges' => 'readwrite']);
        $reader = $shop->databaseUsers()->create(['engine' => 'postgresql', 'name' => $shop->unix_user.'_reader', 'access' => 'website']);
        $reader->databases()->attach($data->id, ['privileges' => 'readonly']);

        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('sharedDatabaseWebsites', 3);

        $response = $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(2, 'jobs');
        $jobs = AgentJob::where('action', AgentAction::MoveDatabases->value)->get()->keyBy('website_id');
        $this->assertEqualsCanonicalizing([$shop->id, $blog->id], $jobs->keys()->all());
        $this->assertSame("Move the databases of {$shop->name}", $jobs[$shop->id]->title);
        $this->assertSame(['Start database containers', 'Stop website', 'Copy databases', 'Copy users', 'Start website', 'Remove old copies'], $jobs[$shop->id]->steps);
        $this->assertSame([
            'websiteId' => $shop->id,
            'unixUser' => $shop->unix_user,
            'databaseInstance' => $acme->id,
            'databases' => [['engine' => 'postgresql', 'name' => $shop->unix_user.'_data'], ['engine' => 'mariadb', 'name' => $name]],
            'users' => [
                ['engine' => 'postgresql', 'name' => $shop->unix_user.'_reader', 'access' => 'website', 'grants' => [['database' => $shop->unix_user.'_data', 'privileges' => 'readonly']]],
                ['engine' => 'mariadb', 'name' => $name, 'access' => 'anywhere', 'grants' => [['database' => $name, 'privileges' => 'readwrite']]],
            ],
        ], $jobs[$shop->id]->payload);
        // A website with no databases moves too: it gets the host names of the new containers.
        $this->assertSame(['websiteId' => $blog->id, 'unixUser' => $blog->unix_user, 'databaseInstance' => $bolt->id, 'databases' => [], 'users' => []], $jobs[$blog->id]->payload);

        // A second request does not start a second job for a website that moves already.
        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(0, 'jobs');

        // The website uses the containers of its customer when the job is complete.
        $this->finish($jobs[$shop->id]->id);
        $this->assertSame($acme->id, $shop->fresh()->database_instance);
        // After a failure, the website keeps the shared containers, and the move can start again.
        $this->finish($jobs[$blog->id]->id, JobState::Failed);
        $this->assertNull($blog->fresh()->database_instance);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('sharedDatabaseWebsites', 2);
        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(1, 'jobs')->assertJsonPath('jobs.0.websiteId', $blog->id);

        foreach ([$new, $starting, $failed, $other] as $website) {
            $this->assertSame(0, AgentJob::where('action', AgentAction::MoveDatabases->value)->where('website_id', $website->id)->count(), $website->name);
        }
        // Valkey is available for the website after the move.
        $this->postJson("/api/v1/websites/{$shop->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertAccepted();
    }

    public function test_the_move_needs_an_agent_that_has_containers_for_each_customer(): void
    {
        $this->actingWith($this->user());
        $server = $this->server();
        $this->website($this->customer(), $server, $this->image());

        $this->postJson("/api/v1/servers/{$server->id}/move-databases")
            ->assertJsonValidationErrors(['server' => "The agent on the server {$server->name} did not report that it can give each customer its own database containers. Update the agent of the server, then try again."]);
        $this->assertSame(0, AgentJob::count());
    }

    public function test_only_an_administrator_moves_the_databases_of_a_server(): void
    {
        $customer = $this->customer();
        $server = $this->newServer();
        $this->website($customer, $server, $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertForbidden();
        $this->assertSame(0, AgentJob::count());
    }
}
