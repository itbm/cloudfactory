<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\CheckAgentConnection;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Server;
use App\Services\AgentClient;
use App\Services\JobDispatcher;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

/**
 * The faults from the test that added a second server: the server joined with a host name that
 * the panel could not find. It showed as healthy, its first job waited for more than 6 minutes
 * and then failed with no cause, and the administrator could not remove the server.
 */
class AddServerTest extends TestCase
{
    private const NO_HOST = 'cURL error 6: Could not resolve host: web2 (see https://curl.haxx.se/libcurl/c/libcurl-errors.html) for https://web2:7443/cloudfactory.agent.v1.AgentService/InstallHostUpdates';

    private function joined(array $attributes = []): Server
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cf-test-agents-'.uniqid()]);

        return $this->server($attributes + ['name' => 'web2', 'agent_url' => 'https://web2:7443', 'certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
    }

    public function test_the_server_page_gets_the_reason_when_the_panel_cannot_connect_to_the_agent(): void
    {
        $server = $this->joined();
        $this->actingWith($this->user());
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        Http::fake(fn () => throw new ConnectionException(self::NO_HOST));

        try {
            (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
            $this->fail('The send must fail.');
        } catch (ConnectionException) {
            // The queue tries again later.
        }

        // The reason has no address of a documentation page, and the server is not healthy.
        $this->assertSame('Could not resolve host: web2', $server->fresh()->connection_error);
        $this->getJson("/api/v1/servers/{$server->id}")->assertOk()
            ->assertJsonPath('state', 'degraded')
            ->assertJsonPath('agentAddress', 'web2:7443')
            ->assertJsonPath('connectionError', 'Could not resolve host: web2');
        $this->assertSame(JobState::Queued, $job->fresh()->state);
    }

    public function test_a_job_that_the_agent_accepts_removes_the_connection_problem(): void
    {
        $server = $this->joined(['connection_error' => 'Could not resolve host: web2', 'connection_checked_at' => now()]);
        $this->actingWith($this->user());
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        Http::fake(['*' => Http::response(['accepted' => true])]);

        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));

        $this->assertNull($server->fresh()->connection_error);
        $this->getJson("/api/v1/servers/{$server->id}")->assertOk()->assertJsonPath('state', 'healthy')->assertJsonPath('connectionError', null);
    }

    public function test_the_first_report_of_a_new_server_starts_a_test_of_the_connection(): void
    {
        $server = $this->joined(['local' => true, 'agent_token_hash' => hash('sha256', 'cfa_web2'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT]);

        $this->asLocalAgent('cfa_web2')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.9'])->assertOk();
        Queue::assertPushed(CheckAgentConnection::class, fn (CheckAgentConnection $check) => $check->serverId === $server->id);

        // Only the first report starts the test.
        Queue::fake();
        $this->asLocalAgent('cfa_web2')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.9'])->assertOk();
        Queue::assertNotPushed(CheckAgentConnection::class);
    }

    public function test_the_test_of_the_connection_records_the_result(): void
    {
        $server = $this->joined();
        $down = true;
        // The agent answers a plain request with "not found". Each answer shows that the connection works.
        Http::fake(function () use (&$down) {
            if ($down) {
                throw new ConnectionException('cURL error 7: Failed to connect to web2 port 7443 after 3 ms: Could not connect to server (see https://curl.haxx.se/libcurl/c/libcurl-errors.html) for https://web2:7443/');
            }

            return Http::response('not found', 404);
        });
        (new CheckAgentConnection($server->id))->handle(app(AgentClient::class));
        $this->assertSame('Failed to connect to web2 port 7443 after 3 ms: Could not connect to server', $server->fresh()->connection_error);

        $down = false;
        $this->actingWith($this->user());
        $this->postJson("/api/v1/servers/{$server->id}/check-connection")->assertOk()->assertJsonPath('connectionError', null)->assertJsonPath('state', 'healthy');
        Http::assertSent(fn ($request) => $request->method() === 'GET' && $request->url() === 'https://web2:7443/');
        $this->assertNull($server->fresh()->connection_error);
    }

    public function test_a_new_website_goes_to_a_server_that_the_panel_can_reach(): void
    {
        $customer = $this->customer();
        $image = $this->image();
        // The server with the problem has fewer websites, so it came first before.
        $problem = $this->server(['name' => 'web-problem', 'connection_error' => 'Could not resolve host: web2']);
        $good = $this->server(['name' => 'web-good']);
        $this->website($customer, $good, $image, 'first.example');
        $this->actingWith($this->user());

        $this->postJson('/api/v1/websites', ['name' => 'second.example', 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertStatus(202)->assertJsonPath('website.serverId', $good->id);
        $this->assertSame(0, $problem->websites()->count());
    }

    public function test_the_administrator_can_remove_a_server_with_no_websites(): void
    {
        $server = $this->joined();
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        $this->actingWith($this->user());

        $this->deleteJson("/api/v1/servers/{$server->id}")->assertNoContent();

        $this->assertNull(Server::find($server->id));
        $this->assertNull(AgentJob::find($job->id));
        $this->assertDatabaseHas('audit_entries', ['action' => 'Removed server', 'target' => 'web2']);
        // The name is free again, so the server can join again with a correct address.
        $this->getJson('/api/v1/servers')->assertOk()->assertJsonMissing(['name' => 'web2']);
    }

    public function test_a_server_with_websites_and_the_server_of_the_panel_stay(): void
    {
        $server = $this->joined();
        $this->website($this->customer(), $server, $this->image());
        $panel = $this->server(['name' => 'panel-server', 'local' => true]);
        $this->actingWith($this->user());

        $this->deleteJson("/api/v1/servers/{$server->id}")->assertStatus(422)->assertJsonPath('errors.server.0', 'This server has 1 website. Delete the websites first.');
        $this->deleteJson("/api/v1/servers/{$panel->id}")->assertStatus(422)->assertJsonPath('errors.server.0', 'This server has the panel. You cannot remove it.');
        $this->assertNotNull(Server::find($server->id));
        $this->getJson("/api/v1/servers/{$panel->id}")->assertOk()->assertJsonPath('panel', true);
    }

    public function test_only_an_administrator_removes_a_server(): void
    {
        $server = $this->joined();
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->deleteJson("/api/v1/servers/{$server->id}")->assertForbidden();
        $this->postJson("/api/v1/servers/{$server->id}/check-connection")->assertForbidden();
    }
}
