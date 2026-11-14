<?php

namespace Tests\Feature;

use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Server;
use App\Models\Website;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;
use Illuminate\Support\Str;
use Tests\TestCase;

/**
 * Each customer has its own database servers on a server, so a customer cannot change the
 * service of a different customer. Each website uses the database servers of its customer.
 * Cloudfactory has no database containers that all customers share.
 */
class DatabaseInstanceTest extends TestCase
{
    private function newServer(array $attributes = []): Server
    {
        return $this->server($attributes + ['database_engines' => ['mariadb', 'postgresql', 'valkey'], 'database_instances' => true]);
    }

    public function test_a_new_website_uses_the_database_servers_of_its_customer(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $server = $this->newServer();
        $image = $this->image();

        $response = $this->postJson('/api/v1/websites', ['name' => 'shop.localhost', 'imageId' => $image->id, 'customerId' => $customer->id, 'serverId' => $server->id])
            ->assertAccepted()->assertJsonMissingPath('website.sharedDatabases');

        // The agent gets the customer of the website with each website.
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame($customer->id, $job->payload['website']['databaseInstance']);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('databaseInstances', true)->assertJsonMissingPath('sharedDatabaseWebsites');
        // The first database makes the database server of the customer.
        $website = Website::findOrFail($response->json('website.id'));
        $website->update(['status' => WebsiteStatus::Running]);
        $created = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $databaseServer = DatabaseServer::sole();
        $this->assertSame([$customer->id, $server->id, 'mariadb', 3306], [$databaseServer->customer_id, $databaseServer->server_id, $databaseServer->engine, $databaseServer->port]);
        $created->assertJsonPath('databaseServerId', $databaseServer->id)->assertJsonPath('port', 3306);
        $this->assertSame($databaseServer->id, Database::sole()->database_server_id);
    }

    public function test_a_website_on_a_server_with_an_agent_of_an_earlier_release_cannot_have_a_database(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $server = $this->server(['database_instances' => false]);
        $image = $this->image();

        $response = $this->postJson('/api/v1/websites', ['name' => 'shop.localhost', 'imageId' => $image->id, 'customerId' => $customer->id, 'serverId' => $server->id])->assertAccepted();

        $website = Website::findOrFail($response->json('website.id'));
        $this->assertSame($customer->id, AgentJob::findOrFail($response->json('job.id'))->payload['website']['databaseInstance']);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('databaseInstances', false);
        // The agent of the server cannot operate a database server, and the panel has no
        // other place for a database.
        $website->update(['status' => WebsiteStatus::Running]);
        $jobs = AgentJob::count();
        foreach (['mariadb', 'postgresql', 'valkey'] as $engine) {
            $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => $engine, 'name' => 'shop'])
                ->assertJsonValidationErrors(['engine' => "The agent on the server {$server->name} did not report that it can operate database servers. Update the agent of the server, then try again."]);
        }
        $this->assertSame([0, 0, $jobs], [Database::count(), DatabaseServer::count(), AgentJob::count()]);
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertOk()->assertJsonCount(0, 'databases')->assertJsonCount(0, 'servers');
    }

    public function test_the_agent_reports_that_it_can_operate_database_servers(): void
    {
        $server = $this->server(['local' => true, 'agent_token_hash' => hash('sha256', 'cfa_instances'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'connection_checked_at' => now(), 'database_instances' => false]);
        $this->assertFalse($server->fresh()->database_instances);

        $this->asLocalAgent('cfa_instances')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'databaseInstances' => true])->assertOk();
        $this->assertTrue($server->fresh()->database_instances);

        // An agent of an earlier release does not send the field.
        $this->asLocalAgent('cfa_instances')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.9'])->assertOk();
        $this->assertFalse($server->fresh()->database_instances);
    }

    public function test_a_new_attempt_for_a_failed_website_sends_the_customer_of_the_website(): void
    {
        $this->actingWith($this->user());
        $customer = $this->customer();
        $website = $this->website($customer, $this->newServer(), $this->image(), attributes: ['status' => 'failed']);

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/retry")->assertAccepted()->json('job.id');

        $this->assertSame($customer->id, AgentJob::findOrFail($jobId)->payload['website']['databaseInstance']);
    }

    public function test_the_panel_has_no_action_that_moves_databases(): void
    {
        $this->actingWith($this->user());
        $server = $this->newServer();
        $this->website($this->customer(), $server, $this->image());

        $this->postJson("/api/v1/servers/{$server->id}/move-databases")->assertNotFound();
        $this->assertSame(0, AgentJob::count());
    }

    /**
     * The migration that removes the shared database containers of release 0.1.9 from the
     * panel. A record with no database server was in the shared containers.
     */
    public function test_the_migration_removes_the_records_of_the_shared_containers(): void
    {
        $migration = require database_path('migrations/2026_11_14_000600_remove_shared_databases.php');
        $customer = $this->customer();
        $server = $this->newServer();
        $website = $this->website($customer, $server, $this->image());
        $databaseServer = $this->databaseServerOf($website);
        $kept = $website->databases()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop', 'database_server_id' => $databaseServer->id]);
        $keptUser = $website->databaseUsers()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop', 'access' => 'website', 'database_server_id' => $databaseServer->id]);
        $keptUser->databases()->attach($kept->id, ['privileges' => 'readwrite']);

        // The state of the panel before the migration: a website has the column, and the
        // shared containers have a database, a user with access to it, and a job of the move.
        $migration->down();
        $this->assertSame($customer->id, DB::table('websites')->where('id', $website->id)->value('database_instance'));
        $now = now();
        $old = ['website_id' => $website->id, 'engine' => 'postgresql', 'name' => $website->unix_user.'_old', 'database_server_id' => null, 'created_at' => $now, 'updated_at' => $now];
        $oldDatabase = (string) Str::ulid();
        $oldUser = (string) Str::ulid();
        DB::table('databases')->insert(['id' => $oldDatabase] + $old);
        DB::table('database_users')->insert(['id' => $oldUser, 'access' => 'website'] + $old);
        DB::table('database_grants')->insert(['database_id' => $oldDatabase, 'database_user_id' => $oldUser, 'privileges' => 'readwrite']);
        DB::table('agent_jobs')->insert([
            'id' => (string) Str::ulid(), 'server_id' => $server->id, 'website_id' => $website->id, 'action' => 'MoveDatabases', 'title' => 'Move the databases',
            'payload' => '{}', 'steps' => '[]', 'step' => 0, 'state' => 'succeeded', 'created_at' => $now, 'updated_at' => $now,
        ]);

        $migration->up();

        $this->assertFalse(Schema::hasColumn('websites', 'database_instance'));
        $this->assertSame([$kept->id], DB::table('databases')->pluck('id')->all());
        $this->assertSame([$keptUser->id], DB::table('database_users')->pluck('id')->all());
        $this->assertSame([[$kept->id, $keptUser->id]], DB::table('database_grants')->get()->map(fn ($g) => [$g->database_id, $g->database_user_id])->all());
        $this->assertSame(0, DB::table('agent_jobs')->where('action', 'MoveDatabases')->count());
    }
}
