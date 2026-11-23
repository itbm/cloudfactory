<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DatabaseServer;
use App\Models\Image;
use App\Models\Pack;
use App\Models\Server;
use App\Models\Website;
use App\Services\DatabaseEngines;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * Database packs (cf-pack/2, cf-database/1, docs/packs.md): a signed manifest adds a database
 * engine. A database server of the engine keeps the version that it got, and has the ports of
 * the block of the pack. The websites connect to it by its network name.
 */
class DatabasePackTest extends TestCase
{
    /** @var array{private: \OpenSSLAsymmetricKey, public: string} */
    private array $key;

    private Customer $customer;

    private Server $host;

    private Website $website;

    protected function setUp(): void
    {
        parent::setUp();
        $private = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $this->key = ['private' => $private, 'public' => openssl_pkey_get_details($private)['key']];
        $this->actingWith($this->user(Role::Administrator));
        $this->postJson('/api/v1/trusted-keys', ['name' => 'Example Packs Ltd', 'publicKey' => $this->key['public'], 'password' => 'password'])->assertCreated();
        $this->customer = $this->customer($this->plan());
        $this->host = $this->server(['database_engines' => ['mariadb', 'postgresql', 'valkey', 'mysql'], 'database_instances' => true]);
        $this->website = $this->website($this->customer, $this->host, $this->image());
    }

    /** The example database pack of the repository, with changes. */
    private function manifest(array $changes = []): string
    {
        $m = json_decode((string) file_get_contents(base_path('../plugins/packs/mysql/pack.json')), true);

        return json_encode(array_merge($m, $changes), JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES)."\n";
    }

    private function sign(string $manifest): string
    {
        openssl_sign($manifest, $signature, $this->key['private'], OPENSSL_ALGO_SHA256);

        return base64_encode($signature);
    }

    private function install(?string $manifest = null): Pack
    {
        $manifest ??= $this->manifest();
        $id = $this->postJson('/api/v1/packs', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'password' => 'password'])->assertCreated()->json('id');

        return Pack::findOrFail($id);
    }

    private function finish(string $jobId): void
    {
        app(JobOutcome::class)->finish(AgentJob::findOrFail($jobId), JobState::Succeeded, null, []);
    }

    private function databaseServer(array $request = []): DatabaseServer
    {
        $response = $this->postJson('/api/v1/database-servers', $request + ['engine' => 'mysql', 'customerId' => $this->customer->id])->assertAccepted();
        $this->finish($response->json('job.id'));

        return DatabaseServer::findOrFail($response->json('databaseServer.id'));
    }

    public function test_a_database_pack_adds_an_engine_with_a_block_of_ports(): void
    {
        $pack = $this->install();
        $this->assertSame('database', $pack->kind);
        $this->assertSame(20000, $pack->database_port);
        // A database pack has no images for websites.
        $this->assertSame(0, Image::where('pack_id', $pack->id)->count());

        $this->getJson("/api/v1/packs/{$pack->id}")->assertOk()
            ->assertJsonPath('kind', 'database')
            ->assertJsonPath('database.port', 3306)
            ->assertJsonPath('database.dataDir', '/var/lib/mysql')
            ->assertJsonPath('database.firstPort', 20000)
            ->assertJsonPath('database.lastPort', 20049)
            ->assertJsonPath('images.0.version', '8.4')
            ->assertJsonPath('images.0.id', null);

        $engines = collect($this->getJson('/api/v1/database-engines')->assertOk()->json('data'))->keyBy('engine');
        $this->assertSame(['mariadb', 'postgresql', 'valkey', 'mysql'], $engines->keys()->all());
        $this->assertSame(['name' => 'MySQL', 'port' => 3306, 'versions' => [['version' => '8.4', 'supportEndsAt' => '2032-04-30']], 'users' => true, 'moves' => false, 'adminer' => false, 'anywhere' => false],
            collect($engines['mysql'])->only(['name', 'port', 'versions', 'users', 'moves', 'adminer', 'anywhere'])->all());
        $this->assertNull($engines['mariadb']['pack']);

        // A second database pack gets the next block.
        $other = $this->install($this->manifest(['slug' => 'mongodb', 'name' => 'MongoDB', 'images' => [
            ['version' => '8.0', 'ref' => 'ghcr.io/example/cf-mongodb:8.0', 'digest' => 'sha256:'.str_repeat('8', 64)],
        ]]));
        $this->assertSame(20050, $other->database_port);
    }

    public function test_a_database_server_of_a_pack_engine(): void
    {
        $this->install();
        $server = $this->databaseServer();
        $this->assertSame('mysql', $server->engine);
        $this->assertSame('8.4', $server->engine_version);
        $this->assertSame(20000, $server->port);
        $this->assertSame('mysql-1', $server->name);

        // The agent takes the image of the version from the verified pack.
        $job = AgentJob::where('action', AgentAction::SetDatabaseServer->value)->latest()->firstOrFail();
        $this->assertSame(['engine' => 'mysql', 'version' => '8.4', 'port' => 20000], collect($job->payload['databaseServer'])->only(['engine', 'version', 'port'])->all());

        // The websites connect by the network name and the port of the engine.
        $this->getJson("/api/v1/database-servers/{$server->id}")->assertOk()
            ->assertJsonPath('engineVersion', '8.4')
            ->assertJsonPath('host', 'mysql-1.internal')
            ->assertJsonPath('port', 3306)
            ->assertJsonPath('networkPort', 3306);

        // A second database server gets the next port of the block.
        $this->assertSame(20001, $this->databaseServer(['name' => 'orders'])->port);

        // A database and a user, as for MariaDB.
        $response = $this->postJson("/api/v1/websites/{$this->website->id}/databases", ['engine' => 'mysql', 'name' => 'shop', 'databaseServerId' => $server->id])->assertAccepted()
            ->assertJsonPath('host', 'mysql-1.internal')->assertJsonPath('port', 3306);
        $create = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::CreateDatabase, $create->action);
        $this->assertSame('mysql', $create->payload['engine']);
        $this->assertSame('8.4', $create->payload['databaseServer']['version']);
        $this->assertTrue($this->website->fresh()->backupDatabases()->where('engine', 'mysql')->exists());
    }

    public function test_a_pack_engine_refuses_what_the_contract_does_not_have(): void
    {
        $this->install();
        // A version that the pack does not have, and a version for a built-in engine.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mysql', 'version' => '9.0', 'customerId' => $this->customer->id])->assertJsonValidationErrors('version');
        $this->postJson('/api/v1/database-servers', ['engine' => 'mariadb', 'version' => '11.4', 'customerId' => $this->customer->id])->assertJsonValidationErrors('version');
        // A server whose agent does not report the engine.
        $this->host->update(['database_engines' => ['mariadb', 'postgresql', 'valkey']]);
        $this->postJson('/api/v1/database-servers', ['engine' => 'mysql', 'customerId' => $this->customer->id])->assertJsonValidationErrors('engine');
        $this->host->update(['database_engines' => ['mariadb', 'postgresql', 'valkey', 'mysql']]);
        // The websites need the network name of the database server.
        $this->postJson('/api/v1/database-servers', ['engine' => 'mysql', 'name' => 'mariadb', 'customerId' => $this->customer->id])->assertJsonValidationErrors('name');

        $server = $this->databaseServer();
        $other = $this->server(['database_engines' => ['mariadb', 'mysql'], 'database_instances' => true, 'state' => 'healthy']);
        $this->postJson("/api/v1/database-servers/{$server->id}/move", ['serverId' => $other->id])->assertJsonValidationErrors('databaseServer');
        $this->postJson("/api/v1/websites/{$this->website->id}/database-users", ['engine' => 'mysql', 'name' => 'ro', 'access' => 'anywhere', 'databaseServerId' => $server->id])
            ->assertJsonValidationErrors('access');
        $this->assertFalse(DatabaseEngines::isPack('mariadb'));
    }

    public function test_a_database_pack_goes_only_when_no_database_server_uses_it(): void
    {
        $pack = $this->install();
        $server = $this->databaseServer();

        $this->deleteJson("/api/v1/packs/{$pack->id}")->assertJsonValidationErrors('pack');
        // An update cannot remove the version of a database server, or change the kind.
        $nine = $this->manifest(['version' => '1.1.0', 'images' => [['version' => '9.0', 'ref' => 'ghcr.io/example/cf-mysql:9.0', 'digest' => 'sha256:'.str_repeat('9', 64)]]]);
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $nine, 'signature' => $this->sign($nine), 'password' => 'password'])->assertJsonValidationErrors('manifest');
        $runtime = (string) file_get_contents(base_path('../plugins/packs/golang/pack.json'));
        $runtime = json_encode(['slug' => 'mysql', 'version' => '2.0.0'] + json_decode($runtime, true), JSON_UNESCAPED_SLASHES);
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $runtime, 'signature' => $this->sign($runtime), 'password' => 'password'])->assertJsonValidationErrors('manifest');
        // An update that keeps the version.
        $both = $this->manifest(['version' => '1.1.0', 'images' => [
            ['version' => '9.0', 'ref' => 'ghcr.io/example/cf-mysql:9.0', 'digest' => 'sha256:'.str_repeat('9', 64)],
            ['version' => '8.4', 'ref' => 'ghcr.io/example/cf-mysql:8.4', 'digest' => 'sha256:'.str_repeat('a', 64)],
        ]]);
        $this->putJson("/api/v1/packs/{$pack->id}", ['manifest' => $both, 'signature' => $this->sign($both), 'password' => 'password'])->assertOk();
        $this->assertSame(20000, $pack->fresh()->database_port);

        // With no database server, the pack goes.
        $server->delete();
        $this->deleteJson("/api/v1/packs/{$pack->id}")->assertSuccessful();
        $this->assertSame(['mariadb', 'postgresql', 'valkey'], DatabaseEngines::all());
    }

    public function test_an_image_repository_is_in_one_pack_only(): void
    {
        $this->install();
        $copy = $this->manifest(['slug' => 'mysql-copy']);
        $this->postJson('/api/v1/packs', ['manifest' => $copy, 'signature' => $this->sign($copy), 'password' => 'password'])->assertJsonValidationErrors('manifest');
        // The name of a built-in engine.
        $maria = $this->manifest(['slug' => 'valkey']);
        $this->postJson('/api/v1/packs', ['manifest' => $maria, 'signature' => $this->sign($maria), 'password' => 'password'])->assertJsonValidationErrors('manifest');
    }
}
