<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DatabaseTool;
use App\Models\DatabaseToolLogin;
use App\Models\MeshTunnel;
use App\Models\Server;
use App\Models\Website;
use App\Services\DatabaseTools;
use App\Services\JobOutcome;
use App\Services\Mesh;
use Tests\TestCase;

/**
 * The database tool (Adminer): one container for each customer on the panel server, with a
 * path on the Adminer domain, and a temporary database user for each database that a user
 * opens.
 */
class DatabaseToolTest extends TestCase
{
    private Customer $customer;

    private Server $panel;

    private Server $data;

    private Website $website;

    private Database $database;

    protected function setUp(): void
    {
        parent::setUp();
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        $this->customer = $this->customer();
        $this->panel = $this->networkServer('panel', '203.0.113.1', ['local' => true, 'agent_version' => '0.3.0', 'mesh_endpoint' => '203.0.113.1']);
        $this->data = $this->networkServer('db-1', '203.0.113.2');
        $this->website = $this->website($this->customer, $this->panel, $this->image());
        $server = $this->databaseServerOf($this->website, 'mariadb', ['name' => 'shop-db']);
        $this->database = Database::create(['website_id' => $this->website->id, 'engine' => 'mariadb', 'name' => $this->website->unix_user.'_shop', 'database_server_id' => $server->id]);
    }

    private function networkServer(string $name, string $address, array $attributes = []): Server
    {
        return $this->server(['name' => $name, 'agent_url' => "https://{$address}:7443", 'mesh_public_key' => base64_encode(hash('sha256', $name, true)),
            'mesh_networks' => true, 'database_engines' => ['mariadb', 'postgresql', 'valkey'], 'database_instances' => true, ...$attributes]);
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null);

        return $job->fresh();
    }

    private function open(?Database $database = null, array $body = [])
    {
        $database ??= $this->database;

        return $this->postJson("/api/v1/websites/{$database->website_id}/databases/{$database->id}/adminer", $body);
    }

    public function test_an_owner_opens_a_database_with_single_sign_on(): void
    {
        $owner = $this->actingWith($this->user(Role::Owner, $this->customer));
        $open = $this->open()->assertStatus(202);

        // The first request starts the Adminer of the customer on the panel server.
        $tool = DatabaseTool::firstOrFail();
        $this->assertSame($this->customer->id, $tool->customer_id);
        $this->assertSame($this->panel->id, $tool->server_id);
        $this->assertMatchesRegularExpression('/^adminer_[a-z0-9]{4}$/', $tool->unix_user);
        [$start, $user] = $open->json('jobs');
        $start = AgentJob::findOrFail($start['id']);
        $this->assertSame(AgentAction::CreateWebsite, $start->action);
        $this->assertSame($this->panel->id, $start->server_id);
        $website = $start->payload['website'];
        $this->assertSame('ghcr.io/itbm/adminer:0.3.0', $website['image']);
        $this->assertSame('custom', $website['runtime']);
        $this->assertSame(8080, $website['containerPort']);
        $this->assertSame([['name' => 'adminer.panel.example.com', 'kind' => 'primary']], $website['domains']);
        $this->assertSame('/'.strtolower($tool->id).'/', $website['routePath']);
        $this->assertSame($this->customer->id, $website['databaseInstance']);

        // The temporary database user has access to the one database.
        $user = AgentJob::findOrFail($user['id']);
        $this->assertSame(AgentAction::SetDatabaseUser, $user->action);
        $this->assertSame($this->website->id, $user->website_id);
        $this->assertTrue($user->payload['create']);
        $this->assertSame('website', $user->payload['access']);
        $this->assertSame([['database' => $this->database->name, 'privileges' => 'readwrite']], $user->payload['grants']);
        $this->assertStringStartsWith($this->website->unix_user.'_adminer_', $user->payload['name']);

        // The form of Adminer: the user, the password and the database. The database server is
        // on the panel server, so Adminer uses the name of the engine and the port.
        $open->assertJsonPath('form.driver', 'server')->assertJsonPath('form.server', 'mariadb.internal:3306')
            ->assertJsonPath('form.username', $user->payload['name'])->assertJsonPath('form.password', $user->payload['password'])
            ->assertJsonPath('form.db', $this->database->name)
            ->assertJsonPath('login.status', 'starting')
            ->assertJsonPath('login.url', 'https://adminer.panel.example.com/'.strtolower($tool->id).'/');

        // The login is ready when the container and the database user are ready.
        $id = $open->json('login.id');
        $this->finish($user->id);
        $this->getJson("/api/v1/adminer-logins/{$id}")->assertOk()->assertJsonPath('login.status', 'starting');
        $this->finish($start->id);
        $this->getJson("/api/v1/adminer-logins/{$id}")->assertOk()->assertJsonPath('login.status', 'ready')->assertJsonMissingPath('login.password');
        $this->assertSame(DatabaseTool::RUNNING, $tool->fresh()->status);

        // The panel does not keep the password.
        $this->assertArrayNotHasKey('password', $user->fresh()->payload);

        // A second database uses the same container: only the database user is new.
        $second = $this->open(body: ['privileges' => 'readonly'])->assertStatus(202);
        $this->assertCount(1, $second->json('jobs'));
        $this->assertSame([['database' => $this->database->name, 'privileges' => 'readonly']], AgentJob::findOrFail($second->json('jobs.0.id'))->payload['grants']);
        $this->assertSame(1, DatabaseTool::count());
        $this->assertNotNull($owner);
    }

    public function test_only_the_customer_of_the_database_and_its_provider_can_open_it(): void
    {
        $other = $this->customer();
        $this->actingWith($this->user(Role::Owner, $other));
        $this->open()->assertNotFound();
        $this->assertSame(0, DatabaseTool::count());

        // The login of a different user is not visible.
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $id = $this->open()->assertStatus(202)->json('login.id');
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson("/api/v1/adminer-logins/{$id}")->assertNotFound();

        // A token with read access only cannot open a database.
        $this->actingWith($this->user(Role::Owner, $this->customer), ['databases:read']);
        $this->open()->assertForbidden();

        // An administrator can open the database. The container is the container of the
        // customer of the database, not of the administrator.
        $this->actingWith($this->user());
        $this->open()->assertStatus(202);
        $this->assertSame([$this->customer->id], DatabaseTool::pluck('customer_id')->all());
    }

    public function test_adminer_cannot_open_a_valkey_database_or_a_suspended_customer(): void
    {
        $this->actingWith($this->user());
        $valkey = $this->databaseServerOf($this->website, 'valkey', ['name' => 'cache']);
        $database = Database::create(['website_id' => $this->website->id, 'engine' => 'valkey', 'name' => $this->website->unix_user.'_cache', 'number' => 1, 'database_server_id' => $valkey->id]);
        $this->open($database)->assertJsonValidationErrors('database');

        $this->customer->update(['suspended' => true]);
        $this->open()->assertStatus(409);
        $this->assertSame(0, DatabaseTool::count());
    }

    public function test_adminer_connects_to_a_database_server_on_a_different_server_through_the_networks(): void
    {
        $remote = DatabaseServer::create([
            'customer_id' => $this->customer->id, 'server_id' => $this->data->id, 'plan_id' => $this->website->plan_id, 'engine' => 'postgresql',
            'name' => 'orders', 'port' => 5433, 'status' => DatabaseServer::RUNNING,
            'limits' => ['vcpus' => 1, 'memoryBytes' => 1024 ** 3, 'diskBytes' => 5 * 1024 ** 3],
        ]);
        $database = Database::create(['website_id' => $this->website->id, 'engine' => 'postgresql', 'name' => $this->website->unix_user.'_orders', 'database_server_id' => $remote->id]);
        $this->actingWith($this->user());
        $open = $this->open($database)->assertStatus(202);
        // The network name and the usual port of the engine.
        $open->assertJsonPath('form.driver', 'pgsql')->assertJsonPath('form.server', 'orders.internal');
        // The job of the database user goes to the agent of the database server.
        $this->assertSame($this->data->id, AgentJob::findOrFail($open->json('jobs.1.id'))->server_id);

        // The container is a website of the networks: it connects to each MariaDB and
        // PostgreSQL database server of its customer, and to nothing else.
        Mesh::refreshLater();
        $tool = DatabaseTool::firstOrFail();
        $this->assertSame(1, MeshTunnel::count());
        $state = Mesh::stateFor($this->panel->fresh());
        $names = collect($state['websites'])->firstWhere('id', $tool->id)['names'];
        $this->assertSame(['orders.internal', 'shop-db.internal'], collect($names)->pluck('name')->sort()->values()->all());
        $state = Mesh::stateFor($this->data->fresh());
        $this->assertSame([$this->panel->fresh()->mesh_address], $state['members'][0]['servers']);

        // A database server of a different customer is not in the names of the container.
        $other = $this->customer();
        DatabaseServer::create([
            'customer_id' => $other->id, 'server_id' => $this->data->id, 'plan_id' => $this->website->plan_id, 'engine' => 'mariadb',
            'name' => 'theirs', 'port' => 3306, 'status' => DatabaseServer::RUNNING, 'limits' => [],
        ]);
        $this->assertNotContains('theirs.internal', collect(DatabaseTools::links())->pluck('name')->map(fn ($n) => "{$n}.internal")->all());

        // A failed container is not in the networks.
        $tool->update(['status' => DatabaseTool::FAILED]);
        $this->assertSame(0, DatabaseTools::links()->count());
    }

    public function test_a_failed_container_starts_again_and_a_new_agent_brings_a_new_image(): void
    {
        $this->actingWith($this->user());
        $open = $this->open()->assertStatus(202);
        $this->finish($open->json('jobs.0.id'), JobState::Failed);
        $this->getJson('/api/v1/adminer-logins/'.$open->json('login.id'))->assertJsonPath('login.status', 'failed');

        // The next request makes the container again.
        $again = $this->open()->assertStatus(202);
        $this->assertSame(AgentAction::CreateWebsite, AgentJob::findOrFail($again->json('jobs.0.id'))->action);
        $this->finish($again->json('jobs.0.id'));
        $this->assertCount(1, $this->open()->json('jobs'));

        // The agent of the panel server has a new version: the container gets the new image.
        $this->panel->update(['agent_version' => '0.4.0']);
        $update = AgentJob::findOrFail($this->open()->assertStatus(202)->json('jobs.0.id'));
        $this->assertSame(AgentAction::SetRuntime, $update->action);
        $this->assertSame('ghcr.io/itbm/adminer:0.4.0', $update->payload['website']['image']);
    }

    public function test_the_panel_removes_a_temporary_user_when_it_expires(): void
    {
        $this->actingWith($this->user());
        $open = $this->open()->assertStatus(202);
        foreach ($open->json('jobs') as $job) {
            $this->finish($job['id']);
        }
        $login = DatabaseToolLogin::firstOrFail();
        $this->assertSame(DatabaseToolLogin::READY, $login->status);

        // Before it expires, nothing happens.
        $this->artisan('cloudfactory:expire-adminer-logins')->assertSuccessful();
        $this->assertSame(0, AgentJob::where('action', AgentAction::DeleteDatabaseUser)->count());

        $this->travel(61)->minutes();
        $this->getJson("/api/v1/adminer-logins/{$login->id}")->assertJsonPath('login.status', 'expired');
        $this->artisan('cloudfactory:expire-adminer-logins')->assertSuccessful();
        $job = AgentJob::where('action', AgentAction::DeleteDatabaseUser)->firstOrFail();
        $this->assertSame($login->name, $job->payload['name']);
        $this->assertSame($this->customer->id, $job->payload['customer']);
        $this->assertSame(DatabaseToolLogin::REMOVING, $login->fresh()->status);
        // A second run sends no second job.
        $this->artisan('cloudfactory:expire-adminer-logins')->assertSuccessful();
        $this->assertSame(1, AgentJob::where('action', AgentAction::DeleteDatabaseUser)->count());

        $this->finish($job->id);
        $this->assertSame(0, DatabaseToolLogin::count());
    }

    public function test_the_panel_removes_the_temporary_user_of_a_database_that_is_gone(): void
    {
        $this->actingWith($this->user());
        $open = $this->open()->assertStatus(202);
        foreach ($open->json('jobs') as $job) {
            $this->finish($job['id']);
        }
        $this->database->delete();
        $this->artisan('cloudfactory:expire-adminer-logins')->assertSuccessful();
        $this->assertSame(1, AgentJob::where('action', AgentAction::DeleteDatabaseUser)->count());
    }

    public function test_the_adminer_domain_is_a_setting(): void
    {
        config(['cloudfactory.adminer.domain' => 'db.example.net']);
        $this->assertSame('db.example.net', DatabaseTools::domain());
        config(['cloudfactory.adminer.domain' => null]);
        $this->assertSame('adminer.panel.example.com', DatabaseTools::domain());
    }
}
