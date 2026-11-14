<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\MeshTunnel;
use App\Models\Server;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use App\Services\Mesh;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The mesh: the encrypted tunnels between the servers of the panel. */
class MeshTest extends TestCase
{
    protected function tearDown(): void
    {
        Mesh::$resolver = null;
        parent::tearDown();
    }

    private static function key(string $seed): string
    {
        return base64_encode(hash('sha256', $seed, true));
    }

    /** A server whose agent reported its tunnel key. */
    private function meshServer(string $name, string $address, array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => $name, 'agent_url' => "https://{$address}:7443", 'mesh_public_key' => self::key($name),
            'certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
    }

    private function finish(AgentJob $job, array $result = [], JobState $state = JobState::Succeeded): void
    {
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);
    }

    /** The body of the request that the panel sends to the agent for a job. */
    private function sent(AgentJob $job): array
    {
        Http::fake(['*' => Http::response(['jobId' => $job->id])]);
        app(AgentClient::class)->send($job->fresh('server'));
        $body = null;
        Http::assertSent(function ($request) use (&$body, $job) {
            if (($request->data()['jobId'] ?? null) === $job->id) {
                $body = $request->data();
            }

            return true;
        });

        return $body;
    }

    public function test_an_administrator_makes_a_tunnel_and_each_server_gets_its_peer(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $db = $this->meshServer('db-1', '203.0.113.2');
        $this->actingWith($this->user());

        $response = $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $db->id]])->assertCreated()
            ->assertJsonPath('tunnel.state', 'waiting')->assertJsonPath('tunnel.pinned', true)->assertJsonCount(2, 'jobs');
        $this->assertDatabaseHas('audit_entries', ['action' => 'Made tunnel']);
        $tunnel = MeshTunnel::firstOrFail();
        // The secret key of the pair is not plain text in the database, and the API never shows it.
        $this->assertTrue(Mesh::validKey($tunnel->preshared_key));
        $this->assertStringNotContainsString($tunnel->preshared_key, (string) DB::table('mesh_tunnels')->value('preshared_key'));
        $response->assertDontSee($tunnel->preshared_key);
        $this->getJson('/api/v1/mesh')->assertOk()->assertDontSee($tunnel->preshared_key)->assertJsonMissingPath('tunnels.0.presharedKey');

        // Each server has an address of the range, in the sequence of the tunnels.
        $addresses = [$web->fresh()->mesh_address, $db->fresh()->mesh_address];
        sort($addresses);
        $this->assertSame(['10.207.0.1', '10.207.0.2'], $addresses);

        // The job has no keys. The panel adds the state when it sends the job.
        $job = AgentJob::where('server_id', $web->id)->where('action', AgentAction::SetMesh->value)->firstOrFail();
        $this->assertSame([], $job->payload);
        $this->assertSame(['Set tunnels and firewall', 'Check tunnels'], $job->steps);
        $mesh = $this->sent($job)['mesh'];
        $this->assertSame(['version' => 1, 'range' => '10.207.0.0/16', 'address' => $web->fresh()->mesh_address, 'port' => 51820, 'peers' => [[
            'server' => $db->id, 'publicKey' => self::key('db-1'), 'presharedKey' => $tunnel->preshared_key,
            'endpoint' => '203.0.113.2:51820', 'address' => $db->fresh()->mesh_address,
        ]]], $mesh);

        // The agent reports the tunnel with the result of the job.
        $this->finish($job, ['mesh' => ['publicKey' => self::key('web-1'), 'version' => 1, 'address' => $mesh['address'], 'tunnels' => [
            ['server' => $db->id, 'handshakeAt' => now()->getTimestamp(), 'reachable' => true, 'roundTripMicros' => 7400],
        ]]]);
        $this->getJson('/api/v1/mesh')->assertOk()->assertJsonPath('tunnels.0.state', 'waiting');
        $other = AgentJob::where('server_id', $db->id)->firstOrFail();
        $this->finish($other, ['mesh' => ['publicKey' => self::key('db-1'), 'version' => 1, 'tunnels' => [
            ['server' => $web->id, 'handshakeAt' => now()->getTimestamp(), 'reachable' => true, 'roundTripMicros' => 7600],
        ]]]);
        $this->getJson('/api/v1/mesh')->assertOk()
            ->assertJsonPath('range', '10.207.0.0/16')->assertJsonPath('port', 51820)
            ->assertJsonPath('tunnels.0.state', 'up')->assertJsonPath('tunnels.0.roundTripMs', 7.6)
            ->assertJsonPath('servers.0.name', 'db-1')->assertJsonPath('servers.0.current', true)->assertJsonPath('servers.0.tunnels', 1)
            ->assertJsonPath('servers.0.tunnelAddress', '203.0.113.2')->assertJsonPath('servers.0.supported', true);

        // A second request for the same pair makes no second tunnel.
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$db->id, $web->id]])->assertCreated();
        $this->assertSame(1, MeshTunnel::count());
    }

    public function test_a_tunnel_needs_two_servers_with_a_key_and_a_public_address(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $old = $this->server(['name' => 'old-1']);
        $private = $this->meshServer('lan-1', '192.168.1.5');
        $named = $this->meshServer('named-1', 'named-1.example');
        $this->actingWith($this->user());

        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $web->id]])->assertJsonValidationErrors('servers.0');
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id]])->assertJsonValidationErrors('servers');
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, 'none']])->assertJsonValidationErrors('servers.1');
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $old->id]])->assertJsonValidationErrors('servers')
            ->assertJsonPath('errors.servers.0', 'The agent on the server old-1 did not report a tunnel key. Update the agent of the server, then try again.');
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $private->id]])->assertJsonValidationErrors('servers')
            ->assertJsonPath('errors.servers.0', 'The panel does not know the public IP address of the server lan-1. Set the tunnel address of the server, then try again.');
        $this->assertSame(0, MeshTunnel::count());
        $this->assertSame(0, AgentJob::count());

        // The panel finds the address of a host name, and keeps it.
        Mesh::$resolver = fn (string $host) => $host === 'named-1.example' ? ['10.0.0.9', '198.51.100.7'] : [];
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $named->id]])->assertCreated();
        $this->assertSame('198.51.100.7', $named->fresh()->mesh_endpoint);

        // An administrator sets the address of the server with the private address.
        $this->patchJson("/api/v1/mesh/servers/{$private->id}", ['tunnelAddress' => '10.1.2.3'])->assertJsonValidationErrors('tunnelAddress');
        $this->patchJson("/api/v1/mesh/servers/{$private->id}", ['tunnelAddress' => '2a01:4f8::5'])->assertOk();
        $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $private->id]])->assertCreated();
        $job = AgentJob::where('server_id', $web->id)->where('state', JobState::Queued->value)->firstOrFail();
        $endpoints = array_column($this->sent($job)['mesh']['peers'], 'endpoint');
        sort($endpoints);
        $this->assertSame(['198.51.100.7:51820', '[2a01:4f8::5]:51820'], $endpoints);
    }

    public function test_the_panel_server_uses_the_address_of_the_panel_domain(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.example']);
        Mesh::$resolver = fn (string $host) => $host === 'panel.example' ? ['203.0.113.9'] : [];
        $panel = $this->meshServer('panel-1', '169.254.77.1', ['local' => true]);
        $this->assertSame('203.0.113.9', Mesh::tunnelAddress($panel));
    }

    public function test_only_an_administrator_manages_the_mesh(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $db = $this->meshServer('db-1', '203.0.113.2');
        $customer = $this->customer();
        foreach ([Role::Owner, Role::Reseller] as $role) {
            $this->actingWith($this->user($role, $customer));
            $this->getJson('/api/v1/mesh')->assertForbidden();
            $this->postJson('/api/v1/mesh/tunnels', ['servers' => [$web->id, $db->id]])->assertForbidden();
            $this->postJson("/api/v1/mesh/servers/{$web->id}/rotate-key")->assertForbidden();
        }
        // A token of an administrator needs the scope of the servers.
        $this->actingWith($this->user(), ['websites']);
        $this->getJson('/api/v1/mesh')->assertForbidden();
        $this->assertSame(0, MeshTunnel::count());
    }

    public function test_the_agent_reports_its_key_and_the_state_of_its_tunnels(): void
    {
        $web = $this->server(['name' => 'web-1', 'agent_url' => 'https://203.0.113.1:7443', 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'agent_token_hash' => hash('sha256', 'cfa_web'), 'local' => true]);
        $db = $this->meshServer('db-1', '203.0.113.2');
        $report = fn (array $mesh) => $this->asLocalAgent('cfa_web')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'mesh' => $mesh]);

        // The first report of a new agent: the panel learns the key. The server has no tunnel.
        $report(['publicKey' => self::key('web-1'), 'version' => 0, 'tunnels' => []])->assertOk();
        $this->assertSame(self::key('web-1'), $web->fresh()->mesh_public_key);
        $this->assertSame(0, AgentJob::count());
        $report(['publicKey' => 'not a key', 'version' => 0])->assertOk();
        $this->assertSame(self::key('web-1'), $web->fresh()->mesh_public_key);
        $report(['version' => 0])->assertJsonValidationErrors('mesh.publicKey');

        $web->update(['mesh_endpoint' => '203.0.113.1']);
        $tunnel = app(Mesh::class)->connect($web->fresh(), $db)['tunnel'];
        $jobs = fn () => AgentJob::where('server_id', $web->id)->where('action', AgentAction::SetMesh->value);
        $this->assertSame(1, $jobs()->count());

        // While the job is open, a report with the old version makes no second job.
        $report(['publicKey' => self::key('web-1'), 'version' => 0, 'tunnels' => []])->assertOk();
        $this->assertSame(1, $jobs()->count());

        // The job failed, and some time passed: the next report makes the panel send the state again.
        $this->finish($jobs()->firstOrFail(), [], JobState::Failed);
        $report(['publicKey' => self::key('web-1'), 'version' => 0, 'tunnels' => []])->assertOk();
        $this->assertSame(1, $jobs()->count());
        $this->travel(Mesh::REPAIR_SECONDS + 1)->seconds();
        $report(['publicKey' => self::key('web-1'), 'version' => 0, 'tunnels' => []])->assertOk();
        $this->assertSame(2, $jobs()->count());
        $this->assertSame(2, $web->fresh()->mesh_wanted_version);

        // The server has the state, and it reports its tunnel.
        $this->finish($jobs()->where('state', JobState::Queued->value)->firstOrFail());
        $report(['publicKey' => self::key('web-1'), 'version' => 2, 'address' => '10.207.0.1', 'tunnels' => [
            ['server' => $db->id, 'handshakeAt' => 1794000000, 'reachable' => false],
            // A tunnel that the panel does not have changes nothing.
            ['server' => '01HZX3K9PQ7R5T8V2W4Y6Z0ABC', 'handshakeAt' => 1, 'reachable' => true],
        ]])->assertOk();
        $this->assertSame(2, $jobs()->count());
        $this->assertSame('down', $tunnel->fresh()->state());
        $this->assertSame([$web->id], array_keys($tunnel->fresh()->status));

        // A report that is not current does not count.
        $this->travel(MeshTunnel::CURRENT_SECONDS + 1)->seconds();
        $this->assertSame('waiting', $tunnel->fresh()->state());

        // The server has a newer version than the panel, for example after a restore of the
        // panel database. The agent refuses an older version, so the panel continues from there.
        $report(['publicKey' => self::key('web-1'), 'version' => 40, 'tunnels' => []])->assertOk();
        $this->assertSame(41, $web->fresh()->mesh_wanted_version);
        $this->assertSame(3, $jobs()->count());
    }

    public function test_a_new_key_goes_to_the_peers_of_the_server(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $db = $this->meshServer('db-1', '203.0.113.2');
        $other = $this->meshServer('web-2', '203.0.113.3');
        $mesh = app(Mesh::class);
        $mesh->connect($web, $db);
        AgentJob::query()->each(fn (AgentJob $job) => $this->finish($job));
        $this->actingWith($this->user());

        $jobId = $this->postJson("/api/v1/mesh/servers/{$db->id}/rotate-key")->assertAccepted()->json('job.id');
        $this->assertDatabaseHas('audit_entries', ['action' => 'Rotated tunnel key', 'target' => 'db-1']);
        $job = AgentJob::findOrFail($jobId);
        $this->assertSame(AgentAction::RotateMeshKey, $job->action);
        $this->assertSame(['jobId' => $jobId], $this->sent($job));

        $this->finish($job, ['publicKey' => self::key('db-1 new')]);
        $this->assertSame(self::key('db-1 new'), $db->fresh()->mesh_public_key);
        // The peer gets the new key. The server itself and a server with no tunnel get no job.
        $open = AgentJob::where('state', JobState::Queued->value)->get();
        $this->assertSame([$web->id], $open->pluck('server_id')->all());
        $this->assertSame(self::key('db-1 new'), $this->sent($open[0])['mesh']['peers'][0]['publicKey']);
        $this->assertSame(0, AgentJob::where('server_id', $other->id)->count());

        // A result that is not a key changes nothing.
        $jobId = $this->postJson("/api/v1/mesh/servers/{$db->id}/rotate-key")->assertAccepted()->json('job.id');
        $this->finish(AgentJob::findOrFail($jobId), ['publicKey' => 'x']);
        $this->assertSame(self::key('db-1 new'), $db->fresh()->mesh_public_key);

        $this->postJson('/api/v1/mesh/servers/'.$this->server(['name' => 'old-1'])->id.'/rotate-key')->assertJsonValidationErrors('server');
    }

    public function test_repair_and_removal_of_a_tunnel(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $db = $this->meshServer('db-1', '203.0.113.2');
        $tunnel = app(Mesh::class)->connect($web, $db)['tunnel'];
        AgentJob::query()->each(fn (AgentJob $job) => $this->finish($job));
        $this->actingWith($this->user());

        $this->postJson("/api/v1/mesh/tunnels/{$tunnel->id}/repair")->assertAccepted()->assertJsonCount(2, 'jobs');
        $this->assertSame(2, $web->fresh()->mesh_wanted_version);
        // A second request while the jobs wait makes no more jobs: they send the newest state.
        $this->postJson("/api/v1/mesh/tunnels/{$tunnel->id}/repair")->assertAccepted()->assertJsonCount(2, 'jobs');
        $this->assertSame(2, AgentJob::where('state', JobState::Queued->value)->count());
        $this->assertSame(3, $this->sent(AgentJob::where('server_id', $web->id)->where('state', JobState::Queued->value)->firstOrFail())['mesh']['version']);

        // The change of the tunnel address of a server goes to its peers.
        $this->patchJson("/api/v1/mesh/servers/{$db->id}", ['tunnelAddress' => '198.51.100.20'])->assertOk()->assertJsonCount(1, 'jobs');
        $job = AgentJob::where('server_id', $web->id)->where('state', JobState::Queued->value)->firstOrFail();
        $this->assertSame('198.51.100.20:51820', $this->sent($job)['mesh']['peers'][0]['endpoint']);
        AgentJob::query()->each(fn (AgentJob $job) => $this->finish($job));

        // With no tunnel, a server is not in the mesh: the state has no address and no peers.
        $this->deleteJson("/api/v1/mesh/tunnels/{$tunnel->id}")->assertAccepted()->assertJsonCount(2, 'jobs');
        $this->assertSame(0, MeshTunnel::count());
        $job = AgentJob::where('server_id', $web->id)->where('state', JobState::Queued->value)->firstOrFail();
        $this->assertSame(['version' => $web->fresh()->mesh_wanted_version, 'peers' => []], $this->sent($job)['mesh']);
        // The server keeps its address for the next tunnel.
        $this->assertNotNull($web->fresh()->mesh_address);
        $this->deleteJson("/api/v1/mesh/tunnels/{$tunnel->id}")->assertNotFound();
    }

    public function test_the_removal_of_a_server_removes_its_tunnels(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $db = $this->meshServer('db-1', '203.0.113.2');
        app(Mesh::class)->connect($web, $db);
        AgentJob::query()->each(fn (AgentJob $job) => $this->finish($job));
        $this->actingWith($this->user());

        $this->deleteJson("/api/v1/servers/{$db->id}")->assertNoContent();
        $this->assertSame(0, MeshTunnel::count());
        $job = AgentJob::where('server_id', $web->id)->where('state', JobState::Queued->value)->firstOrFail();
        $this->assertSame([], $this->sent($job)['mesh']['peers']);
    }

    public function test_the_settings_change_only_while_the_mesh_has_no_tunnel(): void
    {
        $web = $this->meshServer('web-1', '203.0.113.1');
        $db = $this->meshServer('db-1', '203.0.113.2');
        $this->actingWith($this->user());

        foreach (['8.8.0.0/16', '10.0.0.0/8', '10.207.0.1/16', '10.207.0.0/24', '169.254.0.0/16', '192.168.0.0/15', 'fd00::/64', 'x'] as $range) {
            $this->patchJson('/api/v1/mesh', ['range' => $range])->assertJsonValidationErrors('range');
        }
        $this->patchJson('/api/v1/mesh', ['port' => 22])->assertJsonValidationErrors('port');
        $this->patchJson('/api/v1/mesh', ['range' => '172.20.0.0/14', 'port' => 51900])->assertOk()->assertJsonPath('range', '172.20.0.0/14')->assertJsonPath('port', 51900);

        $tunnel = app(Mesh::class)->connect($web, $db)['tunnel'];
        $this->assertStringStartsWith('172.20.0.', $web->fresh()->mesh_address);
        $job = AgentJob::where('server_id', $web->id)->firstOrFail();
        $this->assertSame('203.0.113.2:51900', $this->sent($job)['mesh']['peers'][0]['endpoint']);
        $this->patchJson('/api/v1/mesh', ['range' => '10.207.0.0/16'])->assertJsonValidationErrors('range');
        // The same values are no change.
        $this->patchJson('/api/v1/mesh', ['range' => '172.20.0.0/14'])->assertOk();

        app(Mesh::class)->disconnect($tunnel);
        $this->patchJson('/api/v1/mesh', ['range' => '10.207.0.0/16'])->assertOk();
        // The addresses of the old range are gone.
        $this->assertNull($web->fresh()->mesh_address);
    }

    public function test_the_mesh_has_addresses_for_254_servers(): void
    {
        for ($i = 1; $i <= Mesh::SERVER_ADDRESSES; $i++) {
            Server::create(['name' => "s-{$i}", 'roles' => ['web'], 'mesh_address' => long2ip(ip2long('10.207.0.0') + $i)]);
        }
        $this->expectExceptionMessage('The mesh has no free address for a server.');
        Mesh::addressFor($this->meshServer('web-1', '203.0.113.1'));
    }
}
