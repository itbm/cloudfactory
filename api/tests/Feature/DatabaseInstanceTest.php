<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\Server;
use App\Models\Website;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * Each customer has its own database servers on a server, so a customer cannot change the
 * service of a different customer. A website of an earlier release uses the shared database
 * containers until an administrator moves the databases of its server to database servers.
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

    public function test_a_new_website_uses_the_database_servers_of_its_customer(): void
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

    public function test_the_agent_reports_that_it_can_operate_database_servers(): void
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

    public function test_a_staging_copy_uses_the_database_servers_of_its_customer_also_when_the_source_does_not(): void
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
        // The copy of a database in the shared containers goes into a database server, which
        // the panel makes when the customer has none.
        $server = DatabaseServer::sole();
        $this->assertSame([$customer->id, $source->server_id, 'mariadb', 3306, 'starting'], [$server->customer_id, $server->server_id, $server->engine, $server->port, $server->status]);
        $this->assertSame([['engine' => 'mariadb', 'from' => $source->unix_user.'_shop', 'to' => $copy->unix_user.'_shop', 'toServer' => $server->id]], $job->payload['copyDatabases']);
        $this->assertEquals([$server->forAgent()], $job->payload['databaseServers']);
        $this->assertSame($server->id, $job->payload['copyUsers'][0]['databaseServer']);
        $this->assertSame($server->id, $copy->databases()->sole()->database_server_id);
        $this->assertNull($source->databases()->sole()->database_server_id);
        // The agent made the database server with the copy.
        $this->finish($job->id);
        $this->assertSame('running', $server->fresh()->status);
    }

    public function test_a_failed_website_with_no_databases_gets_the_database_servers_of_its_customer_at_the_next_attempt(): void
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

    public function test_an_administrator_moves_the_databases_of_a_server_to_database_servers(): void
    {
        $this->actingWith($this->user());
        $server = $this->newServer();
        $image = $this->image();
        // The plan of the customer does not limit the database servers of a move: the
        // databases exist, so they must get a database server.
        $acme = $this->customer($this->plan(['database_servers' => 0]));
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
        $this->assertSame(['Start database servers', 'Stop website', 'Copy databases', 'Copy users', 'Start website', 'Remove old copies'], $jobs[$shop->id]->steps);
        // The panel made one database server for each engine of the website, on the usual
        // port of the engine: the applications of the website connect to that port.
        $servers = DatabaseServer::where('customer_id', $acme->id)->get()->keyBy('engine');
        $this->assertSame(['mariadb', 'postgresql'], $servers->keys()->sort()->values()->all());
        $this->assertSame([3306, 5432, 'starting', $server->id], [$servers['mariadb']->port, $servers['postgresql']->port, $servers['mariadb']->status, $servers['mariadb']->server_id]);
        $this->assertEquals([
            'websiteId' => $shop->id,
            'unixUser' => $shop->unix_user,
            'databaseInstance' => $acme->id,
            'databaseServers' => [$servers['mariadb']->forAgent(), $servers['postgresql']->forAgent()],
            'databases' => [
                ['engine' => 'postgresql', 'name' => $shop->unix_user.'_data', 'databaseServer' => $servers['postgresql']->id],
                ['engine' => 'mariadb', 'name' => $name, 'databaseServer' => $servers['mariadb']->id],
            ],
            'users' => [
                ['engine' => 'postgresql', 'name' => $shop->unix_user.'_reader', 'access' => 'website', 'grants' => [['database' => $shop->unix_user.'_data', 'privileges' => 'readonly']], 'databaseServer' => $servers['postgresql']->id],
                ['engine' => 'mariadb', 'name' => $name, 'access' => 'anywhere', 'grants' => [['database' => $name, 'privileges' => 'readwrite']], 'databaseServer' => $servers['mariadb']->id],
            ],
        ], $jobs[$shop->id]->payload);
        // A website with no databases moves too: it can use the database servers of its
        // customer after the move. It needs no database server now.
        $this->assertSame(['websiteId' => $blog->id, 'unixUser' => $blog->unix_user, 'databaseInstance' => $bolt->id, 'databaseServers' => [], 'databases' => [], 'users' => []], $jobs[$blog->id]->payload);
        $this->assertSame(0, DatabaseServer::where('customer_id', $bolt->id)->count());

        // A second request does not start a second job for a website that moves already.
        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(0, 'jobs');

        // When the job is complete, the website uses the database servers of its customer,
        // and each database and each user is in its database server.
        $this->finish($jobs[$shop->id]->id);
        $this->assertSame($acme->id, $shop->fresh()->database_instance);
        $this->assertSame([$servers['mariadb']->id, $servers['postgresql']->id], [$database->fresh()->database_server_id, $data->fresh()->database_server_id]);
        $this->assertSame([$servers['mariadb']->id, $servers['postgresql']->id], [$user->fresh()->database_server_id, $reader->fresh()->database_server_id]);
        $this->assertSame(['running', 'running'], [$servers['mariadb']->fresh()->status, $servers['postgresql']->fresh()->status]);
        $this->getJson("/api/v1/websites/{$shop->id}/databases")->assertJsonCount(2, 'servers')->assertJsonPath('databases.0.databaseServerId', $servers['postgresql']->id);
        // After a failure, the website keeps the shared containers, and the move can start again.
        $this->finish($jobs[$blog->id]->id, JobState::Failed);
        $this->assertNull($blog->fresh()->database_instance);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('sharedDatabaseWebsites', 2);
        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(1, 'jobs')->assertJsonPath('jobs.0.websiteId', $blog->id);

        foreach ([$new, $starting, $failed, $other] as $website) {
            $this->assertSame(0, AgentJob::where('action', AgentAction::MoveDatabases->value)->where('website_id', $website->id)->count(), $website->name);
        }
        // A new database goes into the database server of the move.
        $this->postJson("/api/v1/websites/{$shop->id}/databases", ['engine' => 'mariadb', 'name' => 'blog'])->assertAccepted()->assertJsonPath('databaseServerId', $servers['mariadb']->id);
        // Valkey is available for the website after the move, when the plan of the customer
        // allows a database server for it.
        $this->postJson("/api/v1/websites/{$shop->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertJsonValidationErrors('engine');
        $acme->plan->update(['database_servers' => 3]);
        $this->postJson("/api/v1/websites/{$shop->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertAccepted();
    }

    public function test_a_move_uses_the_database_server_that_the_customer_has_on_the_usual_port(): void
    {
        $this->actingWith($this->user());
        $server = $this->newServer();
        $customer = $this->customer();
        $old = $this->website($customer, $server, $this->image());
        $database = $old->databases()->create(['engine' => 'mariadb', 'name' => $old->unix_user.'_shop']);
        $this->website($customer, $server, $this->image(), 'new.example', ['database_instance' => $customer->id]);
        $first = DatabaseServer::findOrFail($this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $customer->id])->assertAccepted()->json('databaseServer.id'));
        $second = DatabaseServer::findOrFail($this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $customer->id])->assertAccepted()->json('databaseServer.id'));
        $this->assertSame([3306, 3307], [$first->port, $second->port]);

        // The panel removes the database server on the usual port: the website waits.
        $first->update(['status' => 'removing']);
        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(0, 'jobs');
        $first->update(['status' => 'running']);

        $jobId = $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertAccepted()->assertJsonCount(1, 'jobs')->json('jobs.0.id');
        $payload = AgentJob::findOrFail($jobId)->payload;
        $this->assertEquals([$first->forAgent()], $payload['databaseServers']);
        $this->assertSame($first->id, $payload['databases'][0]['databaseServer']);
        $this->assertSame(2, DatabaseServer::count());
        $this->finish($jobId);
        $this->assertSame($first->id, $database->fresh()->database_server_id);
    }

    public function test_the_move_needs_an_agent_that_can_operate_database_servers(): void
    {
        $this->actingWith($this->user());
        $server = $this->server();
        $this->website($this->customer(), $server, $this->image());

        $this->postJson("/api/v1/servers/{$server->id}/move-databases")
            ->assertJsonValidationErrors(['server' => "The agent on the server {$server->name} did not report that it can operate database servers. Update the agent of the server, then try again."]);
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
