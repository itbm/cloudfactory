<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Http\Controllers\Api\BackupController;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Server;
use App\Models\Website;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * A database of a website in a database server on a different server. The website connects
 * through the network of its customer. The agent of the server of the database server runs
 * the jobs for the database and for its users.
 */
class RemoteDatabaseTest extends TestCase
{
    private Customer $customer;

    private Server $web;

    private Server $data;

    private Website $website;

    private DatabaseServer $remote;

    protected function setUp(): void
    {
        parent::setUp();
        $this->customer = $this->customer();
        $this->web = $this->networkServer('web-1', '203.0.113.1');
        $this->data = $this->networkServer('db-1', '203.0.113.2');
        $this->website = $this->website($this->customer, $this->web, $this->image());
        $this->remote = DatabaseServer::create([
            'customer_id' => $this->customer->id, 'server_id' => $this->data->id, 'plan_id' => $this->website->plan_id, 'engine' => 'mariadb',
            'name' => 'shop-db', 'port' => 3307, 'status' => DatabaseServer::RUNNING,
            'limits' => ['vcpus' => 1, 'memoryBytes' => 1024 ** 3, 'diskBytes' => 5 * 1024 ** 3],
        ]);
        $this->actingWith($this->user());
    }

    private function networkServer(string $name, string $address): Server
    {
        return $this->server(['name' => $name, 'agent_url' => "https://{$address}:7443", 'mesh_public_key' => base64_encode(hash('sha256', $name, true)),
            'mesh_networks' => true, 'database_engines' => ['mariadb', 'postgresql', 'valkey'], 'database_instances' => true]);
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);

        return $job->fresh();
    }

    /** Makes a database of the website in the database server on the second server. */
    private function remoteDatabase(string $name = 'shop'): Database
    {
        $made = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => $name, 'databaseServerId' => $this->remote->id])->assertStatus(202);
        $this->finish($made->json('job.id'));

        return Database::findOrFail($made->json('id'));
    }

    public function test_a_database_can_be_in_a_database_server_on_a_different_server(): void
    {
        // The website can use the database server: the list has it.
        $list = $this->getJson("/api/v1/websites/{$this->website->id}/databases")->assertOk();
        $list->assertJsonPath('servers.0.id', $this->remote->id)->assertJsonPath('servers.0.remote', true);

        $made = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $this->remote->id])->assertStatus(202);
        // The application connects to the name of the network member, on the usual port of
        // the engine. The port of the database server on its own server is 3307.
        $made->assertJsonPath('host', 'shop-db.internal')->assertJsonPath('port', 3306)->assertJsonPath('databaseServerId', $this->remote->id);
        // The job goes to the agent of the server of the database server.
        $job = AgentJob::findOrFail($made->json('job.id'));
        $this->assertSame($this->data->id, $job->server_id);
        $this->assertSame($this->website->id, $job->website_id);
        $this->assertSame(AgentAction::CreateDatabase, $job->action);
        $this->assertSame($this->customer->id, $job->payload['databaseServer']['customer']);
        $this->finish($job->id);

        $this->getJson("/api/v1/websites/{$this->website->id}/databases")->assertOk()
            ->assertJsonPath('databases.0.host', 'shop-db.internal')->assertJsonPath('databases.0.port', 3306);

        // A second user and a change of it go to the same agent.
        $user = $this->postJson("/api/v1/websites/{$this->website->id}/database-users", ['engine' => 'mariadb', 'name' => 'reader', 'grants' => [['databaseId' => $made->json('id'), 'privileges' => 'readonly']]])->assertStatus(202);
        $this->assertSame($this->data->id, AgentJob::findOrFail($user->json('job.id'))->server_id);
        $this->finish($user->json('job.id'));

        // The removal of the user and of the database names the customer: the agent of the
        // second server has no record of the website.
        $gone = $this->deleteJson("/api/v1/websites/{$this->website->id}/database-users/{$user->json('user.id')}")->assertStatus(202);
        $job = AgentJob::findOrFail($gone->json('job.id'));
        $this->assertSame($this->data->id, $job->server_id);
        $this->assertSame($this->customer->id, $job->payload['customer']);
        $this->finish($job->id);
        $gone = $this->deleteJson("/api/v1/websites/{$this->website->id}/databases/{$made->json('id')}")->assertStatus(202);
        $job = AgentJob::findOrFail($gone->json('job.id'));
        $this->assertSame($this->data->id, $job->server_id);
        $this->assertSame($this->customer->id, $job->payload['customer']);
        $this->finish($job->id);
        $this->assertSame(0, $this->website->databases()->count());
    }

    public function test_a_new_database_with_no_selection_stays_on_the_server_of_the_website(): void
    {
        // The customer has a database server on the server of the website also. It is first.
        $local = $this->databaseServerOf($this->website, 'mariadb', ['name' => 'local-db']);
        $made = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertStatus(202);
        $made->assertJsonPath('databaseServerId', $local->id)->assertJsonPath('host', 'mariadb.internal')->assertJsonPath('port', 3306);
        $this->assertSame($this->web->id, AgentJob::findOrFail($made->json('job.id'))->server_id);

        // With no database server of the engine that the website can use, the panel makes one
        // on the server of the website.
        $made = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'postgresql', 'name' => 'shop'])->assertStatus(202);
        $this->assertSame($this->web->id, DatabaseServer::findOrFail($made->json('databaseServerId'))->server_id);
    }

    public function test_a_website_cannot_use_a_database_server_that_it_cannot_connect_to(): void
    {
        $refused = ['databaseServerId' => 'Select a database server of the customer of this website that the website can connect to, with the engine of the database.'];
        // A database server of a different customer.
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $theirs = DatabaseServer::create(['customer_id' => $other->id] + $this->remote->only(['server_id', 'plan_id', 'engine', 'port', 'status', 'limits']) + ['name' => 'bolt-db']);
        $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $theirs->id])
            ->assertStatus(422)->assertJsonValidationErrors($refused);
        // An agent that cannot operate network members.
        $this->data->update(['mesh_networks' => false]);
        $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $this->remote->id])
            ->assertStatus(422)->assertJsonValidationErrors($refused);
        $this->getJson("/api/v1/websites/{$this->website->id}/databases")->assertOk()->assertJsonCount(0, 'servers');
    }

    public function test_the_agent_of_the_database_server_reports_the_size_of_the_database(): void
    {
        $database = $this->remoteDatabase();
        $this->data->update(['certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'agent_token_hash' => hash('sha256', 'cfa_data'), 'local' => true]);
        $this->asLocalAgent('cfa_data')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'databaseInstances' => true, 'databaseEngines' => ['mariadb'],
            'databases' => [['engine' => 'mariadb', 'name' => $database->name, 'sizeBytes' => 4096]]])->assertOk();
        $this->assertSame(4096, $database->fresh()->size_bytes);
    }

    public function test_the_removal_of_a_website_first_removes_its_databases_on_a_different_server(): void
    {
        $database = $this->remoteDatabase();
        $local = $this->databaseServerOf($this->website, 'postgresql', ['name' => 'local-db']);
        $made = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'postgresql', 'name' => 'reports', 'databaseServerId' => $local->id])->assertStatus(202);
        $this->finish($made->json('job.id'));

        $removal = $this->deleteJson("/api/v1/websites/{$this->website->id}")->assertStatus(202);
        // The first jobs go to the second server: one for the user, one for the database.
        $jobs = AgentJob::where('server_id', $this->data->id)->whereIn('action', [AgentAction::DeleteDatabase->value, AgentAction::DeleteDatabaseUser->value])->orderBy('id')->get();
        $this->assertSame([AgentAction::DeleteDatabaseUser, AgentAction::DeleteDatabase], $jobs->pluck('action')->all());
        $this->assertSame($jobs[0]->id, $removal->json('job.id'));
        $this->assertSame(0, AgentJob::where('action', AgentAction::DeleteWebsite->value)->count());

        // A job that fails stops the removal. The website stays.
        $this->finish($jobs[0]->id, JobState::Failed);
        $this->finish($jobs[1]->id);
        $this->assertSame(0, AgentJob::where('action', AgentAction::DeleteWebsite->value)->count());
        $this->assertNotNull($this->website->fresh());
        $this->assertNull($database->fresh());

        // The user removes the website again. Only the user is left on the second server.
        $removal = $this->deleteJson("/api/v1/websites/{$this->website->id}")->assertStatus(202);
        $this->assertSame(AgentAction::DeleteDatabaseUser, AgentJob::findOrFail($removal->json('job.id'))->action);
        $this->finish($removal->json('job.id'));
        // Then the agent of the server of the website removes the website and its local database.
        $last = AgentJob::where('action', AgentAction::DeleteWebsite->value)->get();
        $this->assertCount(1, $last);
        $this->assertSame($this->web->id, $last[0]->server_id);
        $this->assertSame(['reports'], array_map(fn ($d) => preg_replace('/^.*_/', '', $d['name']), $last[0]->payload['databases']));
        $this->finish($last[0]->id);
        $this->assertNull($this->website->fresh());
    }

    public function test_a_backup_has_only_the_databases_on_the_server_of_the_website(): void
    {
        $this->remoteDatabase();
        $local = $this->databaseServerOf($this->website, 'postgresql', ['name' => 'local-db']);
        $made = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'postgresql', 'name' => 'reports', 'databaseServerId' => $local->id])->assertStatus(202);
        $this->finish($made->json('job.id'));

        $payload = BackupController::backupPayload($this->website, 'manual');
        $this->assertSame([$local->id], array_column($payload['databases'], 'databaseServer'));
    }

    public function test_a_staging_copy_refuses_a_website_with_a_database_on_a_different_server(): void
    {
        $this->remoteDatabase();
        $this->postJson("/api/v1/websites/{$this->website->id}/staging")->assertStatus(422)
            ->assertJsonValidationErrors(['website' => 'This website has a database in a database server on a different server. The panel cannot make a staging copy of such a website yet.']);
    }
}
