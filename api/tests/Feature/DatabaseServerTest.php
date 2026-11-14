<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Plan;
use App\Models\Server;
use App\Models\Website;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * A customer makes database servers as it makes websites. A database server is one container
 * with one engine and its own limits. The plan of the customer gives the number of its
 * database servers. The plan of a database server gives its limits and the number of its
 * databases.
 */
class DatabaseServerTest extends TestCase
{
    private const ENGINES = ['mariadb', 'postgresql', 'valkey'];

    private const MB = 1024 ** 2;

    private const GB = 1024 ** 3;

    private Customer $customer;

    private Server $host;

    private Website $website;

    /** A customer with one website on a server that can operate database servers. */
    private function hosting(array $plan = []): void
    {
        $this->customer = $this->customer($this->plan($plan));
        $this->host = $this->server(['database_engines' => self::ENGINES, 'database_instances' => true]);
        $this->website = $this->website($this->customer, $this->host, $this->image(), attributes: ['database_instance' => $this->customer->id]);
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);

        return $job->fresh();
    }

    /** Makes a database server through the API, and completes its job. */
    private function databaseServer(string $engine = 'mariadb', array $request = []): DatabaseServer
    {
        $response = $this->postJson('/api/v1/database-servers', $request + ['engine' => $engine, 'customerId' => $this->customer->id])->assertAccepted();
        $this->finish($response->json('job.id'));

        return DatabaseServer::findOrFail($response->json('databaseServer.id'));
    }

    public function test_a_plan_has_the_number_and_the_limits_of_database_servers(): void
    {
        $this->actingWith($this->user());
        $limits = ['vcpus' => 2, 'memoryBytes' => 2 * self::GB, 'diskBytes' => 20 * self::GB, 'diskIoBytesPerSecond' => 50 * self::MB, 'processes' => 100];
        $request = ['name' => 'Pro', 'websites' => 5, 'mailboxes' => 0, 'databases' => 8, 'phpExtensions' => ['opcache'], 'limits' => $limits];

        // A request with no values for database servers gets 3 database servers, each with
        // the vCPUs of a website, the half of its memory and the half of its disk space.
        $id = $this->postJson('/api/v1/plans', $request)->assertCreated()
            ->assertJsonPath('databaseServers', 3)
            ->assertJsonPath('databaseLimits', ['vcpus' => 2, 'memoryBytes' => self::GB, 'diskBytes' => 10 * self::GB])
            ->json('id');

        $this->patchJson("/api/v1/plans/{$id}", $request + ['databaseServers' => 6, 'databaseLimits' => ['vcpus' => 1.5, 'memoryBytes' => 768 * self::MB, 'diskBytes' => 5 * self::GB]])->assertOk()
            ->assertJsonPath('databaseServers', 6)
            ->assertJsonPath('databaseLimits', ['vcpus' => 1.5, 'memoryBytes' => 768 * self::MB, 'diskBytes' => 5 * self::GB]);
        // A request with no values keeps them.
        $this->patchJson("/api/v1/plans/{$id}", $request)->assertOk()->assertJsonPath('databaseServers', 6)->assertJsonPath('databaseLimits.memoryBytes', 768 * self::MB);

        // A database server needs 512 MB of memory and 1 GB of disk space.
        $this->patchJson("/api/v1/plans/{$id}", $request + ['databaseLimits' => ['vcpus' => 1, 'memoryBytes' => 256 * self::MB, 'diskBytes' => 512 * self::MB]])
            ->assertJsonValidationErrors(['databaseLimits.memoryBytes' => 'A database server needs 512 MB of memory or more.', 'databaseLimits.diskBytes' => 'A database server needs 1 GB of disk space or more.']);
        $this->patchJson("/api/v1/plans/{$id}", $request + ['databaseLimits' => ['vcpus' => 1]])->assertJsonValidationErrors(['databaseLimits.memoryBytes', 'databaseLimits.diskBytes']);
    }

    public function test_a_customer_makes_a_database_server_with_the_limits_of_its_plan(): void
    {
        $this->hosting(['database_servers' => 10]);
        $this->actingWith($this->user(Role::Owner, $this->customer));

        $response = $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted()
            ->assertJsonPath('databaseServer.name', 'mariadb-1')
            ->assertJsonPath('databaseServer.engine', 'mariadb')
            ->assertJsonPath('databaseServer.status', 'starting')
            ->assertJsonPath('databaseServer.customerId', $this->customer->id)
            ->assertJsonPath('databaseServer.serverId', $this->host->id)
            ->assertJsonPath('databaseServer.planId', $this->customer->plan_id)
            ->assertJsonPath('databaseServer.host', 'mariadb.internal')
            ->assertJsonPath('databaseServer.port', 3306)
            ->assertJsonPath('databaseServer.limits', ['vcpus' => 2, 'memoryBytes' => self::GB, 'diskBytes' => 10 * self::GB])
            ->assertJsonPath('databaseServer.databases', 0)
            ->assertJsonPath('databaseServer.maxDatabases', 10);
        $id = $response->json('databaseServer.id');

        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetDatabaseServer, $job->action);
        $this->assertSame(['Start database server'], $job->steps);
        $this->assertSame('Create database server mariadb-1', $job->title);
        $this->assertSame($this->host->id, $job->server_id);
        $this->assertSame([
            'databaseServer' => ['id' => $id, 'customer' => $this->customer->id, 'engine' => 'mariadb', 'port' => 3306, 'limits' => ['vcpus' => 2, 'memoryBytes' => self::GB, 'diskBytes' => 10 * self::GB]],
            'create' => true,
        ], $job->payload);
        // The owner of the database server sees its job, which has no website.
        $this->getJson("/api/v1/jobs/{$job->id}")->assertOk()->assertJsonPath('data.databaseServerId', $id)->assertJsonPath('data.websiteId', null);
        $this->getJson("/api/v1/jobs?databaseServerId={$id}")->assertOk()->assertJsonCount(1, 'data');

        $this->finish($job->id);
        $this->getJson("/api/v1/database-servers/{$id}")->assertOk()->assertJsonPath('status', 'running');

        // A second database server of the same engine has the next port. Each engine has its own ports.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'name' => 'reports'])->assertAccepted()
            ->assertJsonPath('databaseServer.name', 'reports')->assertJsonPath('databaseServer.port', 3307);
        $this->postJson('/api/v1/database-servers', ['engine' => 'postgresql'])->assertAccepted()
            ->assertJsonPath('databaseServer.name', 'postgresql-1')->assertJsonPath('databaseServer.port', 5432)->assertJsonPath('databaseServer.host', 'postgres.internal');
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey'])->assertAccepted()
            ->assertJsonPath('databaseServer.port', 6379)->assertJsonPath('databaseServer.host', 'valkey.internal');
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted()
            ->assertJsonPath('databaseServer.name', 'mariadb-2')->assertJsonPath('databaseServer.port', 3308);

        $this->getJson('/api/v1/database-servers')->assertOk()->assertJsonCount(5, 'data');
        $this->getJson('/api/v1/database-servers?engine=mariadb')->assertOk()->assertJsonCount(3, 'data');

        // A name is for one database server of the customer.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'name' => 'reports'])->assertJsonValidationErrors('name');
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'name' => 'Not A Name'])->assertJsonValidationErrors('name');
        $this->postJson('/api/v1/database-servers', ['engine' => 'oracle'])->assertJsonValidationErrors('engine');
    }

    public function test_the_plan_of_the_customer_limits_the_number_of_database_servers(): void
    {
        $this->hosting(['database_servers' => 2]);
        $this->actingWith($this->user(Role::Owner, $this->customer));

        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted();
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey'])->assertAccepted();
        $this->postJson('/api/v1/database-servers', ['engine' => 'postgresql'])
            ->assertJsonValidationErrors(['engine' => 'The Business plan allows 2 database servers.']);
        $this->assertSame(2, DatabaseServer::count());
        // The first database of an engine makes a database server. The plan limit applies.
        $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'postgresql', 'name' => 'data'])
            ->assertJsonValidationErrors(['engine' => 'The Business plan allows 2 database servers.']);
        $this->assertSame(0, Database::count());

        // A customer cannot move to a plan that allows fewer database servers than it has.
        $small = $this->plan(['name' => 'Small', 'database_servers' => 1]);
        $this->actingWith($this->user());
        $this->patchJson("/api/v1/customers/{$this->customer->id}", ['planId' => $small->id])
            ->assertJsonValidationErrors(['planId' => 'The Small plan allows 1 database servers. Acme Ltd has 2.']);
    }

    public function test_the_database_servers_of_the_customers_of_a_reseller_count_for_the_reseller(): void
    {
        $plan = $this->plan(['database_servers' => 2]);
        $reseller = $this->customer($plan, ['name' => 'Northwind', 'role' => 'reseller']);
        $this->customer = $this->customer($plan, ['name' => 'Bakery', 'reseller_id' => $reseller->id]);
        $this->host = $this->server(['database_engines' => self::ENGINES, 'database_instances' => true]);
        $this->website($this->customer, $this->host, $this->image());
        $this->website($reseller, $this->host, $this->image(), 'northwind.example');
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $this->customer->id])->assertAccepted();
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted();
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey', 'customerId' => $this->customer->id])
            ->assertJsonValidationErrors(['engine' => 'The Business plan of your provider allows 2 database servers in total.']);
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey'])
            ->assertJsonValidationErrors(['engine' => 'The Business plan allows 2 database servers in total, for you and your customers.']);
    }

    public function test_only_an_administrator_or_the_reseller_selects_the_plan_and_only_an_administrator_the_limits(): void
    {
        $this->hosting();
        $large = $this->plan(['name' => 'Large', 'databases' => 40, 'database_limits' => ['vcpus' => 4, 'memoryBytes' => 4 * self::GB, 'diskBytes' => 50 * self::GB]]);
        $custom = ['vcpus' => 0.5, 'memoryBytes' => 600 * self::MB, 'diskBytes' => 3 * self::GB];

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'planId' => $large->id])->assertJsonValidationErrors('planId');
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'limits' => $custom])->assertForbidden();
        $this->assertSame(0, DatabaseServer::count());

        $this->actingWith($this->user());
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $this->customer->id, 'planId' => $large->id])->assertAccepted()
            ->assertJsonPath('databaseServer.planId', $large->id)
            ->assertJsonPath('databaseServer.limits', ['vcpus' => 4, 'memoryBytes' => 4 * self::GB, 'diskBytes' => 50 * self::GB])
            ->assertJsonPath('databaseServer.maxDatabases', 40);
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey', 'customerId' => $this->customer->id, 'limits' => $custom])->assertAccepted()
            ->assertJsonPath('databaseServer.limits', $custom);
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey', 'customerId' => $this->customer->id, 'limits' => ['vcpus' => 1, 'memoryBytes' => 100 * self::MB, 'diskBytes' => self::GB]])
            ->assertJsonValidationErrors('limits.memoryBytes');
    }

    public function test_a_database_server_is_on_a_server_that_has_a_website_of_the_customer(): void
    {
        $this->hosting();
        $other = $this->server(['database_engines' => self::ENGINES, 'database_instances' => true]);
        $old = $this->server();
        $noWebsite = Customer::create(['name' => 'Bolt', 'email' => 'it@bolt.example', 'role' => 'owner', 'plan_id' => $this->customer->plan_id]);

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'serverId' => $other->id])
            ->assertJsonValidationErrors(['serverId' => 'Select a server that has a website of this customer.']);
        // A second website on a different server: the default is the server with the most websites.
        $this->website($this->customer, $other, $this->image(), 'two.example');
        $this->website($this->customer, $other, $this->image(), 'three.example');
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted()->assertJsonPath('databaseServer.serverId', $other->id);
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'serverId' => $this->host->id])->assertAccepted()
            ->assertJsonPath('databaseServer.serverId', $this->host->id)
            // The ports are for one server: the two database servers have the usual port.
            ->assertJsonPath('databaseServer.port', 3306);

        $this->actingWith($this->user());
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $noWebsite->id])
            ->assertJsonValidationErrors(['serverId' => 'This customer has no website. Make a website first: the websites on the same server use the database server.']);
        // An administrator can select a server before the customer has a website on it.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $noWebsite->id, 'serverId' => $other->id])->assertAccepted();
        // The agent of the server must be able to operate database servers, and the engine.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $noWebsite->id, 'serverId' => $old->id])
            ->assertJsonValidationErrors(['engine' => "The agent on the server {$old->name} did not report that it can operate database servers. Update the agent of the server, then try again."]);
        $old->update(['database_instances' => true]);
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey', 'customerId' => $noWebsite->id, 'serverId' => $old->id])
            ->assertJsonValidationErrors(['engine' => "The agent on the server {$old->name} did not report that it can operate Valkey. Update the agent of the server, then try again."]);

        // A server with database servers stays in the panel.
        $other->websites()->delete();
        $this->deleteJson("/api/v1/servers/{$other->id}")->assertJsonValidationErrors(['server' => 'This server has 2 database servers. Remove the database servers first.']);
        $this->getJson("/api/v1/servers/{$other->id}")->assertJsonPath('databaseServers', 2);
    }

    public function test_a_user_sees_only_the_database_servers_of_its_customers(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $server = $this->databaseServer();
        $bolt = Customer::create(['name' => 'Bolt', 'email' => 'it@bolt.example', 'role' => 'owner', 'plan_id' => $this->customer->plan_id]);
        $job = AgentJob::where('database_server_id', $server->id)->sole();

        $this->actingWith($this->user(Role::Owner, $bolt));
        $this->getJson('/api/v1/database-servers')->assertOk()->assertJsonCount(0, 'data');
        $this->getJson("/api/v1/database-servers/{$server->id}")->assertNotFound();
        $this->patchJson("/api/v1/database-servers/{$server->id}", ['name' => 'mine'])->assertNotFound();
        $this->deleteJson("/api/v1/database-servers/{$server->id}")->assertNotFound();
        $this->postJson("/api/v1/database-servers/{$server->id}/retry")->assertNotFound();
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'customerId' => $this->customer->id])->assertJsonValidationErrors('customerId');
        $this->getJson("/api/v1/jobs/{$job->id}")->assertNotFound();

        // A token needs the scope for databases.
        $this->actingWith($this->user(Role::Owner, $this->customer), ['websites:read']);
        $this->getJson('/api/v1/database-servers')->assertForbidden();
        $this->actingWith($this->user(Role::Owner, $this->customer), ['databases:read']);
        $this->getJson('/api/v1/database-servers')->assertOk()->assertJsonCount(1, 'data');
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey'])->assertForbidden();
    }

    public function test_a_database_is_in_a_database_server_and_the_first_one_makes_it(): void
    {
        $this->hosting(['databases' => 2]);
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $prefix = $this->website->unix_user.'_';

        // The customer has no MariaDB database server: the panel makes one with the database.
        $response = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted()
            ->assertJsonPath('host', 'mariadb.internal')->assertJsonPath('port', 3306);
        $first = DatabaseServer::sole();
        $this->assertSame([$first->id, 'mariadb-1', 'starting', 3306], [$response->json('databaseServerId'), $first->name, $first->status, $first->port]);
        $job = AgentJob::findOrFail($response->json('job.id'));
        // The agent makes the database server from the job of the database.
        $this->assertEquals($first->forAgent(), $job->payload['databaseServer']);
        $this->assertSame($first->id, $job->database_server_id);
        $this->finish($job->id);
        $this->assertSame('running', $first->fresh()->status);

        // The next database goes into the same database server, while it has space.
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'blog'])->assertAccepted()->assertJsonPath('databaseServerId', $first->id);
        $this->assertSame(1, DatabaseServer::count());
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'third'])
            ->assertJsonValidationErrors(['name' => 'The database server mariadb-1 has 2 databases, and its plan (Business) allows no more. Make a new database server, or give this one a larger plan.']);

        // A second database server: the customer selects it, or the panel selects the one with space.
        $second = $this->databaseServer();
        $this->assertSame(3307, $second->port);
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'third'])->assertAccepted()->assertJsonPath('databaseServerId', $second->id)->assertJsonPath('port', 3307);
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'fourth', 'databaseServerId' => $first->id])
            ->assertJsonValidationErrors(['name' => 'The Business plan allows 2 databases in a database server. The database server mariadb-1 has 2.']);
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'fourth', 'databaseServerId' => $second->id])->assertAccepted();
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'fifth'])
            ->assertJsonValidationErrors(['name' => 'Each MariaDB database server of this customer has the number of databases that its plan allows. Make a new database server, or give one a larger plan.']);

        $this->getJson($databases)->assertOk()
            ->assertJsonCount(4, 'databases')
            ->assertJsonPath('databases.0.name', $prefix.'blog')->assertJsonPath('databases.0.databaseServerId', $first->id)
            ->assertJsonPath('databases.0.host', 'mariadb.internal')->assertJsonPath('databases.0.port', 3306)
            ->assertJsonPath('databases.3.name', $prefix.'third')->assertJsonPath('databases.3.port', 3307)
            ->assertJsonPath('users.0.databaseServerId', $first->id)
            ->assertJsonCount(2, 'servers')
            ->assertJsonPath('servers.0.id', $first->id)->assertJsonPath('servers.0.databases', 2)->assertJsonPath('servers.0.maxDatabases', 2)
            ->assertJsonPath('servers.1.id', $second->id);

        // The removal of a database names its database server for the agent.
        $third = $this->website->databases()->where('name', $prefix.'third')->sole();
        $jobId = $this->deleteJson("{$databases}/{$third->id}")->assertAccepted()->json('job.id');
        $this->assertSame($second->id, AgentJob::findOrFail($jobId)->payload['databaseServer']);
    }

    public function test_a_database_goes_only_into_a_database_server_that_its_website_can_use(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $postgres = $this->databaseServer('postgresql');
        $bolt = Customer::create(['name' => 'Bolt', 'email' => 'it@bolt.example', 'role' => 'owner', 'plan_id' => $this->customer->plan_id]);
        $this->website($bolt, $this->host, $this->image(), 'bolt.example');
        $ofBolt = $this->databaseServer('mariadb', ['customerId' => $bolt->id]);
        $elsewhere = $this->server(['database_engines' => self::ENGINES, 'database_instances' => true]);
        $onOtherServer = $this->databaseServer('mariadb', ['serverId' => $elsewhere->id]);
        $error = ['databaseServerId' => 'Select a database server of the customer of this website, on the server of the website, with the engine of the database.'];

        // A database server of a different customer, on a different server, or with a different engine.
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $ofBolt->id])->assertJsonValidationErrors($error);
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $onOtherServer->id])->assertJsonValidationErrors($error);
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $postgres->id])->assertJsonValidationErrors($error);
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => 'none'])->assertJsonValidationErrors($error);
        $this->assertSame(0, Database::count());
        // With no ID, the website does not get the database server on the different server.
        $response = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $new = DatabaseServer::findOrFail($response->json('databaseServerId'));
        $this->assertSame([$this->customer->id, $this->host->id, 3306], [$new->customer_id, $new->server_id, $new->port]);
        $this->getJson($databases)->assertJsonCount(2, 'servers');

        // A website from an earlier release uses the shared containers, which have no database servers.
        $shared = $this->website($this->customer, $this->host, $this->image(), 'old.example');
        $this->postJson("/api/v1/websites/{$shared->id}/databases", ['engine' => 'postgresql', 'name' => 'data', 'databaseServerId' => $postgres->id])
            ->assertJsonValidationErrors(['databaseServerId' => "This website uses the shared database containers of an earlier release. An administrator must move the databases of the server {$this->host->name} to database servers first."]);
        $response = $this->postJson("/api/v1/websites/{$shared->id}/databases", ['engine' => 'postgresql', 'name' => 'data'])->assertAccepted()
            ->assertJsonPath('databaseServerId', null)->assertJsonPath('port', 5432);
        $this->assertArrayNotHasKey('databaseServer', AgentJob::findOrFail($response->json('job.id'))->payload);
        $this->getJson("/api/v1/websites/{$shared->id}/databases")->assertJsonPath('servers', [])->assertJsonPath('databases.0.databaseServerId', null);
    }

    public function test_a_database_user_is_in_the_database_server_of_its_databases(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $users = "/api/v1/websites/{$this->website->id}/database-users";
        $prefix = $this->website->unix_user.'_';
        $first = $this->databaseServer();
        $second = $this->databaseServer();
        $shop = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'createUser' => false, 'databaseServerId' => $first->id])->assertAccepted()->json('id');
        $blog = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'blog', 'createUser' => false, 'databaseServerId' => $first->id])->assertAccepted()->json('id');
        $reports = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'reports', 'createUser' => false, 'databaseServerId' => $second->id])->assertAccepted()->json('id');

        // The user gets the database server of its databases.
        $response = $this->postJson($users, ['engine' => 'mariadb', 'name' => 'sync', 'grants' => [['databaseId' => $shop, 'privileges' => 'readonly']]])->assertAccepted()
            ->assertJsonPath('user.databaseServerId', $first->id);
        $payload = AgentJob::findOrFail($response->json('job.id'))->payload;
        $this->assertEquals($first->forAgent(), $payload['databaseServer']);
        // The agent gets the databases of the website in the database server of the user only.
        $this->assertSame([$prefix.'blog', $prefix.'shop'], $payload['websiteDatabases']);
        $sync = $response->json('user.id');

        // A user is in one database server.
        $this->postJson($users, ['engine' => 'mariadb', 'name' => 'both', 'grants' => [['databaseId' => $shop, 'privileges' => 'readonly'], ['databaseId' => $reports, 'privileges' => 'readonly']]])
            ->assertJsonValidationErrors(['grants.1.databaseId' => 'A database user is in one database server. Select databases of one database server, or make a user for each database server.']);
        $this->postJson($users, ['engine' => 'mariadb', 'name' => 'both', 'databaseServerId' => $second->id, 'grants' => [['databaseId' => $shop, 'privileges' => 'readonly']]])
            ->assertJsonValidationErrors(['databaseServerId' => 'The databases of the user are in a different database server.']);
        $this->patchJson("{$users}/{$sync}", ['grants' => [['databaseId' => $shop, 'privileges' => 'readwrite'], ['databaseId' => $reports, 'privileges' => 'readonly']]])
            ->assertJsonValidationErrors(['grants.1.databaseId' => 'Select a database in the database server of the user.']);
        $response = $this->patchJson("{$users}/{$sync}", ['grants' => [['databaseId' => $shop, 'privileges' => 'readwrite'], ['databaseId' => $blog, 'privileges' => 'readonly']]])->assertAccepted();
        $this->assertSame($first->id, AgentJob::findOrFail($response->json('job.id'))->payload['databaseServer']['id']);

        // A user with no database: the customer selects the database server when it has two.
        $this->postJson($users, ['engine' => 'mariadb', 'name' => 'later'])->assertJsonValidationErrors(['databaseServerId' => 'Select the database server of the user.']);
        $response = $this->postJson($users, ['engine' => 'mariadb', 'name' => 'later', 'databaseServerId' => $second->id])->assertAccepted()->assertJsonPath('user.databaseServerId', $second->id);
        $this->assertSame([$prefix.'reports'], AgentJob::findOrFail($response->json('job.id'))->payload['websiteDatabases']);
        $this->postJson($users, ['engine' => 'postgresql', 'name' => 'pg'])
            ->assertJsonValidationErrors(['databaseServerId' => 'This customer has no PostgreSQL database server on the server of the website. Make a database first.']);
        $only = $this->databaseServer('postgresql');
        $this->postJson($users, ['engine' => 'postgresql', 'name' => 'pg'])->assertAccepted()->assertJsonPath('user.databaseServerId', $only->id);

        // The removal of a user names its database server for the agent.
        $jobId = $this->deleteJson("{$users}/{$response->json('user.id')}")->assertAccepted()->json('job.id');
        $this->assertSame($second->id, AgentJob::findOrFail($jobId)->payload['databaseServer']);
        $this->assertSame([$prefix.'reports'], AgentJob::findOrFail($jobId)->payload['websiteDatabases']);
    }

    public function test_a_new_plan_gives_a_database_server_new_limits(): void
    {
        $this->hosting(['databases' => 3]);
        $reseller = $this->customer($this->customer->plan, ['name' => 'Northwind', 'role' => 'reseller']);
        $large = $this->plan(['name' => 'Large', 'databases' => 40, 'database_limits' => ['vcpus' => 4, 'memoryBytes' => 4 * self::GB, 'diskBytes' => 50 * self::GB]]);
        $small = $this->plan(['name' => 'Small', 'databases' => 1, 'database_limits' => ['vcpus' => 1, 'memoryBytes' => 512 * self::MB, 'diskBytes' => 2 * self::GB]]);
        $reseller->update(['plan_ids' => [$large->id]]);
        $this->customer->update(['reseller_id' => $reseller->id]);
        $this->actingWith($this->user());
        $server = $this->databaseServer();
        $old = $server->limits;
        $url = "/api/v1/database-servers/{$server->id}";

        // The owner cannot select a plan. A new name needs no job.
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->patchJson($url, ['planId' => $large->id])->assertForbidden();
        $this->patchJson($url, ['limits' => ['vcpus' => 8, 'memoryBytes' => 8 * self::GB, 'diskBytes' => 80 * self::GB]])->assertForbidden();
        $this->patchJson($url, ['name' => 'main'])->assertOk()->assertJsonPath('databaseServer.name', 'main')->assertJsonPath('job', null);
        $this->assertSame($old, $server->fresh()->limits);

        // The reseller of the customer selects one of its plans.
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->patchJson($url, ['planId' => $small->id])->assertForbidden();
        $response = $this->patchJson($url, ['planId' => $large->id])->assertOk()
            ->assertJsonPath('databaseServer.planId', $large->id)
            ->assertJsonPath('databaseServer.limits', ['vcpus' => 4, 'memoryBytes' => 4 * self::GB, 'diskBytes' => 50 * self::GB])
            ->assertJsonPath('databaseServer.maxDatabases', 40);
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetDatabaseServer, $job->action);
        $this->assertSame('Change database server main', $job->title);
        $this->assertSame([
            'databaseServer' => ['id' => $server->id, 'customer' => $this->customer->id, 'engine' => 'mariadb', 'port' => 3306, 'limits' => ['vcpus' => 4, 'memoryBytes' => 4 * self::GB, 'diskBytes' => 50 * self::GB]],
            'previous' => ['planId' => $this->customer->plan_id, 'limits' => $old],
        ], $job->payload);
        // One change at a time.
        $this->actingWith($this->user());
        $this->patchJson($url, ['planId' => $small->id])->assertJsonValidationErrors(['databaseServer' => 'A change of this database server is in progress. Wait until it is complete.']);
        $this->deleteJson($url)->assertJsonValidationErrors('databaseServer');
        $this->finish($job->id);
        $this->assertSame([$large->id, 'running'], [$server->fresh()->plan_id, $server->fresh()->status]);

        // The agent does not make a disk smaller than the data on it: the job fails, and the
        // database server keeps its plan and its limits.
        $response = $this->patchJson($url, ['planId' => $small->id])->assertOk()->assertJsonPath('databaseServer.planId', $small->id);
        $this->finish($response->json('job.id'), JobState::Failed);
        $server->refresh();
        $this->assertSame($large->id, $server->plan_id);
        $this->assertEquals(['vcpus' => 4, 'memoryBytes' => 4 * self::GB, 'diskBytes' => 50 * self::GB], $server->limits);
        $this->assertSame('running', $server->status);

        // A plan must allow the databases that the database server has.
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'blog'])->assertAccepted();
        $this->patchJson($url, ['planId' => $small->id])
            ->assertJsonValidationErrors(['planId' => 'The Small plan allows 1 databases in a database server. This database server has 2.']);

        // An administrator sets limits directly.
        $custom = ['vcpus' => 0.5, 'memoryBytes' => 700 * self::MB, 'diskBytes' => 60 * self::GB];
        $response = $this->patchJson($url, ['limits' => $custom])->assertOk()->assertJsonPath('databaseServer.limits', $custom)->assertJsonPath('databaseServer.planId', $large->id);
        $this->assertSame($custom, AgentJob::findOrFail($response->json('job.id'))->payload['databaseServer']['limits']);
        $this->finish($response->json('job.id'));
        // The same limits again need no job.
        $this->patchJson($url, ['limits' => $custom])->assertOk()->assertJsonPath('job', null);
    }

    public function test_a_change_of_a_plan_gives_its_database_servers_the_new_limits(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $plan = $this->customer->plan;
        $withPlanLimits = $this->databaseServer();
        $custom = ['vcpus' => 0.5, 'memoryBytes' => 700 * self::MB, 'diskBytes' => 60 * self::GB];
        $withOwnLimits = $this->databaseServer('postgresql', ['limits' => $custom]);
        $new = ['vcpus' => 3, 'memoryBytes' => 3 * self::GB, 'diskBytes' => 30 * self::GB];
        $request = ['name' => $plan->name, 'websites' => 10, 'mailboxes' => 10, 'databases' => 10, 'phpExtensions' => $plan->php_extensions, 'limits' => $plan->limits];

        // No change of the limits of database servers: no job.
        $this->patchJson("/api/v1/plans/{$plan->id}", $request + ['databaseServers' => 9])->assertOk();
        $this->assertSame(0, AgentJob::where('title', 'like', 'Apply the limits%')->count());

        $this->patchJson("/api/v1/plans/{$plan->id}", $request + ['databaseLimits' => $new])->assertOk();
        $job = AgentJob::where('title', 'Apply the limits of the plan to mariadb-1')->sole();
        $this->assertSame($withPlanLimits->id, $job->database_server_id);
        $this->assertEquals($new, $job->payload['databaseServer']['limits']);
        $this->assertEquals($withPlanLimits->limits, $job->payload['previous']['limits']);
        $this->assertEquals($new, $withPlanLimits->fresh()->limits);
        // A database server with limits that an administrator set for it keeps them.
        $this->assertEquals($custom, $withOwnLimits->fresh()->limits);
        $this->assertSame(1, AgentJob::where('title', 'like', 'Apply the limits%')->count());

        // One change at a time: the plan waits for the open job of its database server.
        $this->patchJson("/api/v1/plans/{$plan->id}", $request + ['databaseLimits' => ['vcpus' => 1] + $new])
            ->assertJsonValidationErrors(['databaseLimits' => 'A change of the database server mariadb-1 is in progress. Wait until it is complete, then change the limits of the plan.']);
        $this->assertEquals($new, Plan::findOrFail($plan->id)->database_limits);
        $this->finish($job->id);
        $this->patchJson("/api/v1/plans/{$plan->id}", $request + ['databaseLimits' => ['vcpus' => 1] + $new])->assertOk();
    }

    public function test_a_database_server_with_no_databases_can_be_removed(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $server = $this->databaseServer();
        $url = "/api/v1/database-servers/{$server->id}";
        $response = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'createUser' => false])->assertAccepted();
        $this->finish($response->json('job.id'));
        $userJob = $this->postJson("/api/v1/websites/{$this->website->id}/database-users", ['engine' => 'mariadb', 'name' => 'sync'])->assertAccepted()->json('job.id');
        $this->finish($userJob);

        $this->deleteJson($url)->assertJsonValidationErrors(['databaseServer' => 'This database server has 1 database. Remove the databases first.']);
        $this->finish($this->deleteJson("{$databases}/{$response->json('id')}")->assertAccepted()->json('job.id'));
        $this->deleteJson($url)->assertJsonValidationErrors(['databaseServer' => 'This database server has 1 database user. Remove the database users first.']);
        $this->website->databaseUsers()->delete();

        $jobId = $this->deleteJson($url)->assertAccepted()->json('job.id');
        $job = AgentJob::findOrFail($jobId);
        $this->assertSame(AgentAction::DeleteDatabaseServer, $job->action);
        $this->assertSame(['Stop database server', 'Remove data'], $job->steps);
        $this->assertSame(['databaseServer' => $server->id, 'previousStatus' => 'running'], $job->payload);
        // The record stays until the agent removed the database server. It gets no database.
        $this->getJson($url)->assertOk()->assertJsonPath('status', 'removing');
        $this->deleteJson($url)->assertJsonValidationErrors(['databaseServer' => 'The panel removes this database server.']);
        $this->patchJson($url, ['name' => 'again'])->assertJsonValidationErrors('databaseServer');
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'late', 'databaseServerId' => $server->id])->assertJsonValidationErrors('databaseServerId');
        $this->getJson($databases)->assertJsonPath('servers', []);

        // After a failure, the database server is on the server still.
        $this->finish($jobId, JobState::Failed);
        $this->getJson($url)->assertOk()->assertJsonPath('status', 'running');

        $this->finish($this->deleteJson($url)->assertAccepted()->json('job.id'));
        $this->getJson($url)->assertNotFound();
        $this->assertSame(0, DatabaseServer::count());
        // The port and the name are free again.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted()->assertJsonPath('databaseServer.port', 3306)->assertJsonPath('databaseServer.name', 'mariadb-1');
    }

    public function test_a_database_server_that_the_agent_did_not_make_can_be_made_again(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $response = $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb'])->assertAccepted();
        $url = "/api/v1/database-servers/{$response->json('databaseServer.id')}";

        $this->postJson("{$url}/retry")->assertJsonValidationErrors('databaseServer');
        $this->finish($response->json('job.id'), JobState::Failed);
        $this->getJson($url)->assertJsonPath('status', 'failed');

        $again = $this->postJson("{$url}/retry")->assertAccepted()->assertJsonPath('databaseServer.status', 'starting');
        $job = AgentJob::findOrFail($again->json('job.id'));
        $this->assertSame(AgentAction::SetDatabaseServer, $job->action);
        $this->assertTrue($job->payload['create']);
        $this->finish($job->id);
        $this->getJson($url)->assertJsonPath('status', 'running');

        // The job of a database also makes a database server that the agent does not have.
        DatabaseServer::query()->update(['status' => 'failed']);
        $jobId = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted()->json('job.id');
        $this->finish($jobId);
        $this->getJson($url)->assertJsonPath('status', 'running');

        // A suspended customer makes no database server.
        $this->actingWith($this->user());
        $this->customer->update(['suspended' => true]);
        $this->postJson('/api/v1/database-servers', ['engine' => 'valkey', 'customerId' => $this->customer->id])->assertStatus(409);
    }

    public function test_the_report_of_the_agent_gives_the_state_and_the_use_of_each_database_server(): void
    {
        $this->hosting(['database_servers' => 10]);
        $this->host->update(['local' => true, 'agent_token_hash' => hash('sha256', 'cfa_servers'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'connection_checked_at' => now()]);
        $this->actingWith($this->user());
        $running = $this->databaseServer();
        $starting = DatabaseServer::findOrFail($this->postJson('/api/v1/database-servers', ['engine' => 'postgresql', 'customerId' => $this->customer->id])->json('databaseServer.id'));
        $failed = $this->databaseServer('valkey');
        $failed->update(['status' => 'failed']);
        $removing = $this->databaseServer('valkey');
        $removing->update(['status' => 'removing']);
        $elsewhere = $this->databaseServer('mariadb', ['serverId' => $this->server(['database_engines' => self::ENGINES, 'database_instances' => true])->id]);
        $report = fn (array $servers) => $this->asLocalAgent('cfa_servers')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'databaseInstances' => true, 'databaseEngines' => self::ENGINES, 'databaseServers' => $servers])->assertOk();

        $report([
            ['id' => $running->id, 'state' => 'stopped', 'memoryBytes' => 0, 'diskBytes' => 300 * self::MB, 'diskSizeBytes' => 10 * self::GB],
            ['id' => strtoupper($starting->id), 'state' => 'stopped', 'memoryBytes' => 0, 'diskBytes' => 0, 'diskSizeBytes' => 0],
            ['id' => $failed->id, 'state' => 'running', 'memoryBytes' => 90 * self::MB, 'diskBytes' => 2 * self::MB, 'diskSizeBytes' => 10 * self::GB],
            ['id' => $removing->id, 'state' => 'running', 'memoryBytes' => 1, 'diskBytes' => 1, 'diskSizeBytes' => 1],
            // An agent reports only its own database servers.
            ['id' => $elsewhere->id, 'state' => 'stopped', 'memoryBytes' => 5, 'diskBytes' => 5, 'diskSizeBytes' => 5],
            ['id' => 'unknown', 'state' => 'running'],
        ]);

        // A database server that operated shows that its container stopped.
        $this->assertSame('stopped', $running->fresh()->status);
        $this->assertSame(['memoryBytes' => 0, 'diskBytes' => 300 * self::MB, 'diskSizeBytes' => 10 * self::GB], $running->fresh()->usage);
        // The job of a new database server gives its result: the report does not.
        $this->assertSame('starting', $starting->fresh()->status);
        // A container that operates shows that the agent made the database server.
        $this->assertSame('running', $failed->fresh()->status);
        $this->assertSame('removing', $removing->fresh()->status);
        $this->assertSame(['running', null], [$elsewhere->fresh()->status, $elsewhere->fresh()->usage]);
        $this->getJson("/api/v1/database-servers/{$failed->id}")->assertJsonPath('usage.memoryBytes', 90 * self::MB)->assertJsonPath('usage.diskSizeBytes', 10 * self::GB);

        $report([['id' => $running->id, 'state' => 'running', 'memoryBytes' => 200 * self::MB, 'diskBytes' => 300 * self::MB, 'diskSizeBytes' => 10 * self::GB]]);
        $this->assertSame('running', $running->fresh()->status);
    }

    public function test_a_staging_copy_has_its_databases_in_the_database_servers_of_the_source(): void
    {
        $this->hosting(['databases' => 3]);
        $this->actingWith($this->user());
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $prefix = $this->website->unix_user.'_';
        $first = $this->databaseServer();
        $second = $this->databaseServer();
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop', 'databaseServerId' => $first->id])->assertAccepted();
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'reports', 'databaseServerId' => $second->id])->assertAccepted();
        $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'stats', 'databaseServerId' => $second->id])->assertAccepted();

        // The second database server has 2 databases, and its plan allows 3: no space for 2 copies.
        $this->postJson("/api/v1/websites/{$this->website->id}/staging")
            ->assertJsonValidationErrors(['name' => 'The Business plan allows 3 databases in a database server. The database server mariadb-2 has 2.']);
        // The panel made nothing.
        $this->assertSame(1, Website::count());
        $this->assertSame(3, Database::count());

        $this->website->databases()->where('name', $prefix.'stats')->delete();
        $response = $this->postJson("/api/v1/websites/{$this->website->id}/staging")->assertAccepted();
        $copy = Website::findOrFail($response->json('website.id'));
        $to = $copy->unix_user.'_';
        $payload = AgentJob::findOrFail($response->json('job.id'))->payload;
        $this->assertSame([
            ['engine' => 'mariadb', 'from' => $prefix.'shop', 'to' => $to.'shop', 'fromServer' => $first->id, 'toServer' => $first->id],
            ['engine' => 'mariadb', 'from' => $prefix.'reports', 'to' => $to.'reports', 'fromServer' => $second->id, 'toServer' => $second->id],
        ], $payload['copyDatabases']);
        // Each database server has its own user for the copies.
        $this->assertSame([[$to.'app', $first->id], [$to.'app2', $second->id]], array_map(fn ($u) => [$u['name'], $u['databaseServer']], $payload['copyUsers']));
        $this->assertEquals([$first->forAgent(), $second->forAgent()], $payload['databaseServers']);
        $this->assertSame([$first->id, $second->id], [
            $copy->databases()->where('name', $to.'shop')->sole()->database_server_id,
            $copy->databases()->where('name', $to.'reports')->sole()->database_server_id,
        ]);
        $this->assertSame([$first->id, $second->id], $copy->databaseUsers()->orderBy('name')->pluck('database_server_id')->all());
        // The answer has the host name and the port for each user.
        $response->assertJsonPath('databaseCredentials.0.user', $to.'app')->assertJsonPath('databaseCredentials.0.host', 'mariadb.internal')->assertJsonPath('databaseCredentials.0.port', 3306)
            ->assertJsonPath('databaseCredentials.1.user', $to.'app2')->assertJsonPath('databaseCredentials.1.port', 3307)->assertJsonPath('databaseCredentials.1.databaseServerId', $second->id);
    }

    public function test_a_backup_and_a_restore_name_the_database_server_of_each_database(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $databases = "/api/v1/websites/{$this->website->id}/databases";
        $name = $this->website->unix_user.'_shop';
        $response = $this->postJson($databases, ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $serverId = $response->json('databaseServerId');
        $this->putJson('/api/v1/backup-policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3:s3.amazonaws.com/backups'])->assertOk();

        $jobId = $this->postJson("/api/v1/websites/{$this->website->id}/snapshots")->assertAccepted()->json('job.id');
        $this->assertSame([['engine' => 'mariadb', 'name' => $name, 'databaseServer' => $serverId]], AgentJob::findOrFail($jobId)->payload['databases']);
        $this->finish($jobId, result: ['snapshotId' => 'abc123', 'sizeBytes' => 1024, 'dumps' => ["mariadb:{$name}" => str_repeat('a', 64)]]);
        $snapshot = $this->website->snapshots()->sole();

        $jobId = $this->postJson("/api/v1/websites/{$this->website->id}/snapshots/{$snapshot->id}/restore", ['scope' => 'database', 'item' => "mariadb:{$name}"])->assertAccepted()->json('job.id');
        $this->assertSame($serverId, AgentJob::findOrFail($jobId)->payload['databaseServer']);
        // A full restore has no database.
        $jobId = $this->postJson("/api/v1/websites/{$this->website->id}/snapshots/{$snapshot->id}/restore", ['scope' => 'website'])->assertAccepted()->json('job.id');
        $this->assertArrayNotHasKey('databaseServer', AgentJob::findOrFail($jobId)->payload);

        // The agent restores into the database server that has the database now.
        $this->website->databases()->delete();
        $this->postJson("/api/v1/websites/{$this->website->id}/snapshots/{$snapshot->id}/restore", ['scope' => 'database', 'item' => "mariadb:{$name}"])
            ->assertJsonValidationErrors(['item' => "This website does not have the database {$name} now. Make a database with this name, then restore the backup."]);
    }

    public function test_the_removal_of_a_website_keeps_the_database_servers(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $response = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $serverId = $response->json('databaseServerId');
        $name = $response->json('name');

        $jobId = $this->deleteJson("/api/v1/websites/{$this->website->id}")->assertAccepted()->json('job.id');
        $payload = AgentJob::findOrFail($jobId)->payload;
        $this->assertSame([['engine' => 'mariadb', 'name' => $name, 'databaseServer' => $serverId]], $payload['databases']);
        $this->assertSame([['engine' => 'mariadb', 'name' => $name, 'databaseServer' => $serverId]], $payload['databaseUsers']);

        $this->finish($jobId);
        $this->assertSame(0, Website::count());
        $this->getJson("/api/v1/database-servers/{$serverId}")->assertOk()->assertJsonPath('databases', 0);
    }
}
