<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\MeshTunnel;
use App\Models\NetworkMember;
use App\Models\Server;
use App\Services\JobOutcome;
use App\Services\Mesh;
use Tests\TestCase;

/**
 * An administrator moves a database server to a different server. The data goes through the
 * tunnel between the two servers. The database server keeps its ID, its name and its network
 * address.
 */
class DatabaseServerMoveTest extends TestCase
{
    private function moveServer(string $name, string $address, array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => $name, 'agent_url' => "https://{$address}:7443", 'mesh_public_key' => base64_encode(hash('sha256', $name, true)),
            'mesh_networks' => true, 'mesh_websites' => true, 'mesh_moves' => true, 'database_engines' => ['mariadb', 'postgresql', 'valkey']]);
    }

    /** @return array{0: Server, 1: Server, 2: DatabaseServer} */
    private function setUpMove(): array
    {
        $old = $this->moveServer('web-1', '203.0.113.1');
        $new = $this->moveServer('web-2', '203.0.113.2');
        $customer = $this->customer();
        $website = $this->website($customer, $old, $this->image());
        $this->website($customer, $new, $this->image(), 'api.acme.example');
        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db']);
        Database::create(['website_id' => $website->id, 'engine' => 'mariadb', 'name' => $website->unix_user.'_shop', 'database_server_id' => $database->id]);
        // A database server of the same engine is on the new server, so the moved one gets the
        // next port there.
        $this->databaseServerOf($website, 'mariadb', ['name' => 'other-db', 'server_id' => $new->id]);
        AgentJob::query()->update(['state' => JobState::Succeeded->value]);
        $this->actingWith($this->user());

        return [$old, $new, $database];
    }

    private static function job(AgentAction $action): AgentJob
    {
        return AgentJob::where('action', $action->value)->latest('id')->firstOrFail();
    }

    public function test_a_database_server_moves_and_keeps_its_name_and_address(): void
    {
        [$old, $new, $database] = $this->setUpMove();
        $address = $database->fresh()->mesh_address;
        $this->assertNotNull($address);

        $response = $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])->assertAccepted()
            ->assertJsonPath('databaseServer.status', DatabaseServer::MOVING)
            ->assertJsonPath('databaseServer.movingToServerId', $new->id);
        $this->assertCount(2, $response->json('jobs'));

        // The new server receives first, then the old server sends. The two jobs have the
        // same token and the mesh address of the other server.
        $receive = self::job(AgentAction::ReceiveDatabaseServer);
        $send = self::job(AgentAction::SendDatabaseServer);
        $this->assertSame($new->id, $receive->server_id);
        $this->assertSame($old->id, $send->server_id);
        $this->assertLessThan($send->id, $receive->id);
        $this->assertMatchesRegularExpression('/^[0-9a-f]{64}$/', $receive->payload['token']);
        $this->assertSame($receive->payload['token'], $send->payload['token']);
        $this->assertSame($old->fresh()->mesh_address, $receive->payload['from']);
        $this->assertSame($new->fresh()->mesh_address, $send->payload['to']);
        $this->assertSame(3307, $receive->payload['databaseServer']['port']);
        $this->assertSame($database->id, $receive->payload['databaseServer']['id']);
        // The two servers have a tunnel for the move.
        $this->assertSame(1, MeshTunnel::where(fn ($q) => $q->whereIn('server_a_id', [$old->id, $new->id])->whereIn('server_b_id', [$old->id, $new->id]))->count());

        // While it moves, the database server does not change, and gets no new database.
        $this->patchJson("/api/v1/database-servers/{$database->id}", ['limits' => ['vcpus' => 2, 'memoryBytes' => 1024 ** 3, 'diskBytes' => 5 * 1024 ** 3]])->assertJsonValidationErrors('databaseServer');
        $this->deleteJson("/api/v1/database-servers/{$database->id}")->assertJsonValidationErrors('databaseServer');
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors('databaseServer');
        // A report of the old server does not change the status.
        app(JobOutcome::class)->finish($send, JobState::Succeeded, null, []);
        $this->assertSame(DatabaseServer::MOVING, $database->fresh()->status);

        app(JobOutcome::class)->finish($receive, JobState::Succeeded, null, []);
        $moved = $database->fresh();
        $this->assertSame([$new->id, 3307, DatabaseServer::RUNNING, null, 'shop-db', $address], [$moved->server_id, $moved->port, $moved->status, $moved->moving_to_server_id, $moved->name, $moved->mesh_address]);
        // The old server removes its copy. The record stays.
        $delete = self::job(AgentAction::DeleteDatabaseServer);
        $this->assertSame($old->id, $delete->server_id);
        app(JobOutcome::class)->finish($delete, JobState::Succeeded, null, []);
        $this->assertNotNull($database->fresh());
        $this->assertSame(1, Database::where('database_server_id', $database->id)->count());
        // The token is not kept.
        $this->assertArrayNotHasKey('token', $receive->fresh()->payload);

        // The new server has the member. The old server takes the traffic of its website to
        // the new server.
        $members = collect(Mesh::stateFor($new->fresh())['members'] ?? [])->pluck('address')->all();
        $this->assertContains($address, $members);
        $peer = collect(Mesh::stateFor($old->fresh())['peers'])->firstWhere('server', $new->id);
        $this->assertContains($address, $peer['members']);
    }

    public function test_a_failed_move_leaves_the_database_server_on_the_old_server(): void
    {
        [$old, $new, $database] = $this->setUpMove();
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])->assertAccepted();
        $receive = self::job(AgentAction::ReceiveDatabaseServer);
        app(JobOutcome::class)->finish($receive, JobState::Failed, 'the old server did not send the data', []);

        $database = $database->fresh();
        $this->assertSame([$old->id, DatabaseServer::FIRST_PORT['mariadb'], DatabaseServer::RUNNING, null], [$database->server_id, $database->port, $database->status, $database->moving_to_server_id]);
        // The old server starts it again, and the new server removes what it received.
        $start = self::job(AgentAction::SetDatabaseServer);
        $this->assertSame($old->id, $start->server_id);
        $remove = self::job(AgentAction::DeleteDatabaseServer);
        $this->assertSame($new->id, $remove->server_id);
        app(JobOutcome::class)->finish($remove, JobState::Succeeded, null, []);
        $this->assertNotNull($database->fresh());
    }

    public function test_a_move_needs_a_network_for_the_websites_on_other_servers(): void
    {
        [, $new, $database] = $this->setUpMove();
        // The website with the database leaves the network "default".
        NetworkMember::whereNotNull('website_id')->delete();
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['serverId' => 'Put the website and the database server in one network first.']);
        $this->assertSame(DatabaseServer::RUNNING, $database->fresh()->status);
    }

    public function test_only_an_administrator_moves_a_database_server_to_a_server_that_can(): void
    {
        [$old, $new, $database] = $this->setUpMove();
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $old->id])->assertJsonValidationErrors('serverId');

        $new->update(['mesh_moves' => false]);
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors('serverId')->assertJsonFragment(['The agent on the server web-2 cannot move database servers. Update the agent of the server, then try again.']);
        $new->update(['mesh_moves' => true, 'state' => 'offline']);
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors('serverId');
        $new->update(['state' => 'healthy']);

        $this->actingWith($this->user(Role::Owner, $database->customer));
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])->assertForbidden();
        $this->assertSame(0, AgentJob::where('action', AgentAction::ReceiveDatabaseServer->value)->count());
    }

    public function test_an_agent_reports_that_it_can_move(): void
    {
        $server = $this->moveServer('web-1', '203.0.113.1', ['mesh_moves' => false]);
        app(Mesh::class)->reported($server, ['publicKey' => $server->mesh_public_key, 'version' => 0, 'networks' => true, 'websites' => true, 'moves' => true, 'tunnels' => []]);
        $this->assertTrue($server->fresh()->mesh_moves);
        app(Mesh::class)->reported($server, ['publicKey' => $server->mesh_public_key, 'version' => 0, 'networks' => true, 'websites' => true, 'tunnels' => []]);
        $this->assertFalse($server->fresh()->mesh_moves);
    }
}
