<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Models\AgentJob;
use App\Models\JoinToken;
use App\Models\Server;
use App\Services\AgentClient;
use App\Services\JobDispatcher;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

class AgentTest extends TestCase
{
    private function joinBody(string $token): array
    {
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $cert = openssl_csr_sign(openssl_csr_new(['commonName' => 'web-9.internal'], $key), null, $key, 30);
        openssl_x509_export($cert, $pem);

        return [
            'token' => $token, 'name' => 'web-9', 'agentUrl' => 'https://web-9.internal:7443',
            'certificateSha256' => openssl_x509_fingerprint($cert, 'sha256'), 'certificatePem' => $pem, 'agentVersion' => '0.1.0',
            'os' => 'debian-13', 'arch' => 'arm64', 'roles' => ['web'],
        ];
    }

    public function test_the_fingerprint_must_agree_with_the_certificate(): void
    {
        JoinToken::create(['token_hash' => hash('sha256', 'cfj_x'), 'expires_at' => now()->addHour()]);

        $this->postJson('/api/agent/v1/join', ['certificateSha256' => str_repeat('ab', 32)] + $this->joinBody('cfj_x'))
            ->assertUnprocessable()->assertJsonPath('message', 'The certificate does not agree with its fingerprint.');
    }

    public function test_the_panel_calls_the_agent_with_the_connect_protocol(): void
    {
        Http::fake(['*' => Http::response(['jobId' => 'x'])]);
        $body = $this->joinBody('unused');
        $server = $this->server(['certificate_fingerprint' => $body['certificateSha256'], 'certificate_pem' => $body['certificatePem']]);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::Reboot, [], 'Reboot');

        app(AgentClient::class)->send($job);

        Http::assertSent(fn ($request) => $request->url() === 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/Reboot'
            && $request['jobId'] === $job->id
            && $request->hasHeader('Connect-Protocol-Version', '1'));
        $this->assertStringEqualsFile(AgentClient::trustFile($server), $body['certificatePem']);
    }

    public function test_a_server_joins_with_a_token_that_works_once(): void
    {
        $this->actingWith($this->user());
        $token = $this->postJson('/api/v1/servers/join-tokens')->assertCreated()->json('token');

        $this->postJson('/api/agent/v1/join', $this->joinBody($token))->assertCreated()->assertJsonStructure(['serverId', 'agentToken']);
        $this->assertSame('web-9', Server::sole()->name);

        $this->postJson('/api/agent/v1/join', ['name' => 'web-10'] + $this->joinBody($token))->assertUnauthorized();
    }

    public function test_an_expired_join_token_does_not_work(): void
    {
        JoinToken::create(['token_hash' => hash('sha256', 'cfj_old'), 'expires_at' => now()->subMinute()]);

        $this->postJson('/api/agent/v1/join', $this->joinBody('cfj_old'))->assertUnauthorized();
    }

    public function test_the_agent_must_send_the_certificate_that_it_joined_with(): void
    {
        $server = $this->server(['certificate_fingerprint' => str_repeat('ab', 32), 'agent_token_hash' => hash('sha256', 'cfa_secret')]);

        $this->withToken('cfa_secret')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0'])->assertUnauthorized();
        $this->withToken('cfa_secret')->withHeader('X-Client-Cert-Sha256', str_repeat('ab', 32))
            ->postJson('/api/agent/v1/report', [
                'agentVersion' => '0.1.1',
                'pendingUpdates' => [['name' => 'openssl', 'from' => '3.5.1-1', 'to' => '3.5.1-2', 'security' => true]],
                'rebootRequired' => true,
                'services' => [['name' => 'caddy', 'state' => 'running']],
            ])->assertOk();

        $server->refresh();
        $this->assertSame('0.1.1', $server->agent_version);
        $this->assertTrue($server->reboot_required);
        $this->assertCount(1, $server->pending_updates);
    }

    public function test_job_events_change_the_state_of_the_website(): void
    {
        $server = $this->server(['agent_token_hash' => hash('sha256', 'cfa_secret')]);
        $website = $this->website($this->customer(), $server, $this->image(), attributes: ['status' => 'starting']);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::CreateWebsite, JobDispatcher::websitePayload($website), 'Create', $website);

        $this->withToken('cfa_secret')->postJson("/api/agent/v1/jobs/{$job->id}/events", ['state' => 'running', 'step' => 2])->assertOk();
        $this->assertSame(2, $job->fresh()->step);

        $this->withToken('cfa_secret')->postJson("/api/agent/v1/jobs/{$job->id}/events", ['state' => 'succeeded'])->assertOk();
        $this->assertSame('running', $website->fresh()->status->value);
        $this->assertSame(count(AgentAction::CreateWebsite->steps()), $job->fresh()->step);
    }

    public function test_an_agent_cannot_report_the_job_of_a_different_server(): void
    {
        $this->server(['agent_token_hash' => hash('sha256', 'cfa_one')]);
        $other = $this->server();
        $job = app(JobDispatcher::class)->dispatch($other, AgentAction::Reboot, [], 'Reboot');

        $this->withToken('cfa_one')->postJson("/api/agent/v1/jobs/{$job->id}/events", ['state' => 'succeeded'])->assertNotFound();
        $this->assertSame('queued', AgentJob::find($job->id)->state->value);
    }
}
