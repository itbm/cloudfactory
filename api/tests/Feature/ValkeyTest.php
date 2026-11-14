<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseUser;
use App\Models\Server;
use App\Models\Website;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * Valkey is the third database engine. A Valkey database is one database number of the shared
 * Valkey server, with one user that has the name of the database. The agent gives the number.
 */
class ValkeyTest extends TestCase
{
    private const ENGINES = ['mariadb', 'postgresql', 'valkey'];

    private function site(array $server = []): Website
    {
        $website = $this->website($this->customer(), $this->server($server + ['database_engines' => self::ENGINES]), $this->image());
        $this->actingWith($this->user());

        return $website;
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);

        return $job->fresh();
    }

    /** Makes a Valkey database with the number from the agent, and gives its row. */
    private function cache(Website $website, int $number = 7): Database
    {
        $jobId = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertAccepted()->json('job.id');
        $this->finish($jobId, result: ['number' => $number]);

        return $website->databases()->where('engine', 'valkey')->sole();
    }

    public function test_a_valkey_database_has_one_user_and_gets_its_number_from_the_agent(): void
    {
        $website = $this->site();
        $name = $website->unix_user.'_cache';

        $response = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertAccepted()
            ->assertJsonPath('name', $name)->assertJsonPath('user', $name);
        $this->assertSame(24, strlen($response->json('password')));
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::CreateDatabase, $job->action);
        $this->assertSame(['engine' => 'valkey', 'name' => $name, 'user' => $name, 'access' => 'website'], array_intersect_key($job->payload, array_flip(['engine', 'name', 'user', 'access'])));

        // Before the agent made the database, the panel does not know its number.
        $this->getJson("/api/v1/websites/{$website->id}/databases")
            ->assertJsonPath('databases.0.engine', 'valkey')->assertJsonPath('databases.0.number', null)
            ->assertJsonPath('users.0.name', $name)->assertJsonPath('users.0.engine', 'valkey')
            ->assertJsonPath('users.0.grants.0.database', $name)->assertJsonPath('users.0.grants.0.privileges', 'readwrite');

        $this->finish($job->id, result: ['number' => 7]);
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertJsonPath('databases.0.number', 7)->assertJsonPath('databases.0.keys', null);
        $this->assertArrayNotHasKey('password', $job->fresh()->payload);

        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertJsonValidationErrors('name');
    }

    public function test_a_valkey_database_needs_its_user_and_an_agent_that_can_operate_valkey(): void
    {
        $website = $this->site();
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache', 'createUser' => false])
            ->assertJsonValidationErrors(['createUser' => 'A Valkey database always has a user with the same name.']);

        // An agent of an earlier release does not report its engines.
        $website->server->update(['database_engines' => null]);
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])
            ->assertJsonValidationErrors(['engine' => "The agent on the server {$website->server->name} did not report that it can operate Valkey. Update the agent of the server, then try again."]);
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $this->assertSame(0, Database::where('engine', 'valkey')->count());
        $this->getJson("/api/v1/servers/{$website->server_id}")->assertJsonPath('databaseEngines', ['mariadb', 'postgresql']);
    }

    public function test_a_valkey_database_that_the_agent_did_not_make_is_not_in_the_panel(): void
    {
        $website = $this->site();
        $jobId = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'cache'])->assertAccepted()->json('job.id');

        $this->finish($jobId, JobState::Failed);

        $this->assertSame(0, Database::count());
        $this->assertSame(0, DatabaseUser::count());
    }

    public function test_the_user_of_a_valkey_database_can_only_get_a_new_password(): void
    {
        $website = $this->site();
        $database = $this->cache($website);
        $user = $website->databaseUsers()->sole();
        $users = "/api/v1/websites/{$website->id}/database-users";

        $this->postJson($users, ['engine' => 'valkey', 'name' => 'second'])->assertJsonValidationErrors('engine');
        $this->patchJson("{$users}/{$user->id}", ['access' => 'anywhere'])->assertJsonValidationErrors('user');
        $this->patchJson("{$users}/{$user->id}", ['grants' => []])->assertJsonValidationErrors('user');
        $this->patchJson("{$users}/{$user->id}", ['grants' => [['databaseId' => $database->id, 'privileges' => 'readonly']], 'resetPassword' => true])->assertJsonValidationErrors('user');
        $this->patchJson("{$users}/{$user->id}", [])->assertJsonValidationErrors('user');
        $this->deleteJson("{$users}/{$user->id}")->assertJsonValidationErrors(['user' => 'The user of a Valkey database goes with its database. Remove the database.']);
        $this->assertSame('website', $user->fresh()->access);
        $this->assertSame(1, $user->databases()->count());

        $response = $this->patchJson("{$users}/{$user->id}", ['resetPassword' => true])->assertAccepted();
        $this->assertSame(24, strlen($response->json('password')));
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetDatabaseUser, $job->action);
        $this->assertSame('valkey', $job->payload['engine']);
        $this->assertSame($user->name, $job->payload['name']);
        $this->assertFalse($job->payload['create']);
    }

    public function test_the_removal_of_a_valkey_database_removes_its_user(): void
    {
        $website = $this->site();
        $database = $this->cache($website);
        $website->databases()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop']);
        $shop = $website->databaseUsers()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_cache', 'access' => 'website']);

        $jobId = $this->deleteJson("/api/v1/websites/{$website->id}/databases/{$database->id}")->assertAccepted()->json('job.id');
        $job = AgentJob::findOrFail($jobId);
        $this->assertSame(['engine' => 'valkey', 'name' => $database->name, 'users' => [$database->name]], array_intersect_key($job->payload, array_flip(['engine', 'name', 'users'])));
        // The rows stay until the agent removed the database.
        $this->assertSame(1, Database::where('engine', 'valkey')->count());

        $this->finish($jobId);

        $this->assertSame(0, Database::where('engine', 'valkey')->count());
        $this->assertSame(0, DatabaseUser::where('engine', 'valkey')->count());
        // A MariaDB user with the same name stays.
        $this->assertNotNull($shop->fresh());
    }

    public function test_the_report_of_the_agent_gives_the_number_the_keys_and_the_engines(): void
    {
        $server = $this->server(['local' => true, 'agent_token_hash' => hash('sha256', 'cfa_valkey'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'connection_checked_at' => now()]);
        $website = $this->website($this->customer(), $server, $this->image());
        $name = $website->unix_user.'_cache';
        $website->databases()->create(['engine' => 'valkey', 'name' => $name]);
        $website->databases()->create(['engine' => 'mariadb', 'name' => $name]);
        $this->assertFalse($server->hasDatabaseEngine('valkey'));

        $this->asLocalAgent('cfa_valkey')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.9', 'databaseEngines' => self::ENGINES, 'databases' => [
            ['engine' => 'valkey', 'name' => $name, 'sizeBytes' => 0, 'number' => 3, 'keys' => 1204],
            ['engine' => 'mariadb', 'name' => $name, 'sizeBytes' => 4096],
        ]])->assertOk();

        $valkey = $website->databases()->where('engine', 'valkey')->sole();
        $this->assertSame([3, 1204, 0], [$valkey->number, $valkey->key_count, $valkey->size_bytes]);
        $maria = $website->databases()->where('engine', 'mariadb')->sole();
        $this->assertSame([null, null, 4096], [$maria->number, $maria->key_count, $maria->size_bytes]);
        $this->assertTrue(Server::findOrFail($server->id)->hasDatabaseEngine('valkey'));

        // An agent of an earlier release does not send the engines.
        $this->asLocalAgent('cfa_valkey')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.9'])->assertOk();
        $this->assertFalse(Server::findOrFail($server->id)->hasDatabaseEngine('valkey'));
        $this->assertSame(3, $valkey->fresh()->number);
    }

    public function test_a_backup_has_no_valkey_data(): void
    {
        $website = $this->site();
        $this->cache($website);
        $website->databases()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop']);
        $this->putJson('/api/v1/backup-policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3:s3.amazonaws.com/backups'])->assertOk();

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/snapshots")->assertAccepted()->json('job.id');
        $this->assertSame([['engine' => 'mariadb', 'name' => $website->unix_user.'_shop']], AgentJob::findOrFail($jobId)->payload['databases']);
        $this->finish($jobId, result: ['snapshotId' => 'abc123', 'sizeBytes' => 1024]);

        $this->getJson("/api/v1/websites/{$website->id}/snapshots")->assertJsonPath('data.0.databases', ["mariadb:{$website->unix_user}_shop"]);
    }

    public function test_a_staging_copy_gets_a_new_empty_valkey_database(): void
    {
        $website = $this->site();
        $this->cache($website);
        $website->databases()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop']);

        $response = $this->postJson("/api/v1/websites/{$website->id}/staging")->assertAccepted();
        $copy = Website::findOrFail($response->json('website.id'));
        $name = $copy->unix_user.'_cache';
        $credentials = collect($response->json('databaseCredentials'))->keyBy('engine');
        $this->assertSame($name, $credentials['valkey']['user']);
        $this->assertSame([$name], $credentials['valkey']['databases']);
        $this->assertSame(24, strlen($credentials['valkey']['password']));

        $job = AgentJob::findOrFail($response->json('job.id'));
        // The agent copies the data of the MariaDB database only.
        $this->assertSame([['engine' => 'mariadb', 'from' => $website->unix_user.'_shop', 'to' => $copy->unix_user.'_shop']], $job->payload['copyDatabases']);
        $this->assertContains(['engine' => 'valkey', 'name' => $name, 'password' => $credentials['valkey']['password']], $job->payload['copyUsers']);
        $this->assertSame($name, $copy->databaseUsers()->where('engine', 'valkey')->sole()->name);

        $job = $this->finish($job->id, result: ['port' => 20001, 'dnsPointsHere' => true, 'valkey' => [$name => 9]]);
        $this->assertSame(9, $copy->databases()->where('engine', 'valkey')->sole()->number);
        $this->assertSame(7, $website->databases()->where('engine', 'valkey')->sole()->number);
        $this->assertStringNotContainsString($credentials['valkey']['password'], json_encode($job->payload));
    }

    public function test_the_removal_of_a_website_removes_its_valkey_databases(): void
    {
        $website = $this->site();
        $database = $this->cache($website);

        $jobId = $this->deleteJson("/api/v1/websites/{$website->id}")->assertAccepted()->json('job.id');

        $payload = AgentJob::findOrFail($jobId)->payload;
        $this->assertSame([['engine' => 'valkey', 'name' => $database->name]], $payload['databases']);
        $this->assertSame([['engine' => 'valkey', 'name' => $database->name]], $payload['databaseUsers']);
    }
}
