<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Server;
use App\Services\PanelCertificate;
use Tests\TestCase;

/** The TLS certificate of the panel domain: the CA of the panel or Let's Encrypt. */
class PanelCertificateTest extends TestCase
{
    private string $caPem;

    private string $caSha256;

    protected function setUp(): void
    {
        parent::setUp();
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $cert = openssl_csr_sign(openssl_csr_new(['commonName' => 'Cloudfactory panel CA'], $key), null, $key, 3650);
        openssl_x509_export($cert, $pem);
        $this->caPem = $pem;
        $this->caSha256 = openssl_x509_fingerprint($cert, 'sha256');
    }

    private function panelServer(): Server
    {
        return $this->server(['name' => 'panel-1', 'agent_token_hash' => hash('sha256', 'cfa_panel'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'local' => true]);
    }

    private function report(string $method = 'internal', string $state = 'valid'): array
    {
        return ['agentVersion' => '0.2.0', 'panelCertificate' => [
            'domain' => 'panel.example.com', 'method' => $method, 'state' => $state, 'expiresAt' => '2027-02-09T10:00:00Z', 'caCertificate' => $this->caPem,
        ]];
    }

    public function test_the_panel_server_reports_the_certificate_and_the_agents_get_the_ca(): void
    {
        $this->panelServer();
        $this->actingWith($this->user());
        $this->getJson('/api/v1/panel-certificate')->assertOk()->assertJson(['domain' => 'panel.example.com', 'method' => null, 'state' => 'pending', 'caSha256' => null]);

        $this->asLocalAgent('cfa_panel')->postJson('/api/agent/v1/report', $this->report())
            ->assertOk()->assertJson(['panelCaSha256' => $this->caSha256]);

        $this->getJson('/api/v1/panel-certificate')->assertOk()->assertJson([
            'method' => 'internal', 'state' => 'valid', 'expiresAt' => '2027-02-09T10:00:00Z',
            'caCertificate' => trim($this->caPem)."\n", 'caSha256' => $this->caSha256, 'letsEncryptAvailable' => true,
        ]);
    }

    public function test_only_the_panel_server_reports_the_certificate(): void
    {
        $this->server(['certificate_fingerprint' => str_repeat('ab', 32), 'agent_token_hash' => hash('sha256', 'cfa_remote')]);

        $this->withToken('cfa_remote')->withHeader('X-Cloudfactory-Edge', '1')->withHeader('X-Client-Cert-Sha256', str_repeat('ab', 32))
            ->postJson('/api/agent/v1/report', $this->report())->assertOk();

        $this->assertNull(PanelCertificate::current()['method']);
    }

    public function test_the_join_command_trusts_the_panel_ca(): void
    {
        $this->actingWith($this->user());
        $this->assertStringNotContainsString('--panel-ca-sha256', $this->postJson('/api/v1/servers/join-tokens')->json('command'));

        PanelCertificate::record($this->report()['panelCertificate']);

        $response = $this->postJson('/api/v1/servers/join-tokens')->assertCreated();
        $this->assertStringEndsWith("--panel https://panel.example.com --panel-ca-sha256 {$this->caSha256}", $response->json('command'));
        $this->assertSame($this->caSha256, $response->json('panelCaSha256'));
    }

    public function test_an_administrator_changes_the_panel_to_lets_encrypt(): void
    {
        $server = $this->panelServer();
        PanelCertificate::record($this->report()['panelCertificate']);
        $this->actingWith($this->user());

        $this->putJson('/api/v1/panel-certificate', ['method' => 'letsencrypt'])->assertAccepted()
            ->assertJsonPath('job.title', "Change the panel certificate to Let's Encrypt");
        $job = AgentJob::sole();
        $this->assertSame(AgentAction::SetPanelCertificate, $job->action);
        $this->assertSame($server->id, $job->server_id);
        $this->assertSame(['method' => 'letsencrypt'], $job->payload);

        // One change at a time.
        $this->putJson('/api/v1/panel-certificate', ['method' => 'internal'])->assertUnprocessable()->assertJsonValidationErrors('method');

        // The agent gives the new state with the result of the job.
        $this->asLocalAgent('cfa_panel')->postJson("/api/agent/v1/jobs/{$job->id}/events", [
            'state' => 'succeeded', 'result' => ['panelCertificate' => $this->report('letsencrypt')['panelCertificate']],
        ])->assertOk();
        $this->getJson('/api/v1/panel-certificate')->assertOk()->assertJson(['method' => 'letsencrypt', 'state' => 'valid', 'job' => ['state' => 'succeeded']]);
    }

    public function test_lets_encrypt_needs_a_public_domain(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.localhost']);
        $this->panelServer();
        PanelCertificate::record(['domain' => 'panel.localhost'] + $this->report()['panelCertificate']);
        $this->actingWith($this->user());

        $this->getJson('/api/v1/panel-certificate')->assertJson(['letsEncryptAvailable' => false]);
        $this->putJson('/api/v1/panel-certificate', ['method' => 'letsencrypt'])->assertUnprocessable()
            ->assertJsonPath('errors.method.0', "Let's Encrypt gives certificates only for public domains. panel.localhost is not a public domain.");
        $this->putJson('/api/v1/panel-certificate', ['method' => 'zerossl'])->assertUnprocessable();
        $this->assertSame(0, AgentJob::count());
    }

    public function test_the_panel_waits_for_the_report_of_its_agent(): void
    {
        $this->panelServer();
        $this->actingWith($this->user());

        $this->putJson('/api/v1/panel-certificate', ['method' => 'letsencrypt'])->assertUnprocessable()
            ->assertJsonPath('errors.method.0', 'The agent on the panel server has not reported the panel certificate. Update the agent on the panel server, and wait one minute.');
    }

    public function test_the_panel_uses_its_ca_only_when_all_servers_trust_it(): void
    {
        $this->panelServer();
        PanelCertificate::record($this->report('letsencrypt')['panelCertificate']);
        $this->server(['name' => 'web-2', 'certificate_fingerprint' => str_repeat('ab', 32), 'agent_token_hash' => hash('sha256', 'cfa_web2')]);
        $this->actingWith($this->user());

        $this->getJson('/api/v1/panel-certificate')->assertJson(['serversWithoutCa' => ['web-2']]);
        $this->putJson('/api/v1/panel-certificate', ['method' => 'internal'])->assertUnprocessable()
            ->assertJsonPath('errors.method.0', 'These servers do not trust the panel CA yet: web-2. Update their agent, and wait one minute.');

        // The agent learns the fingerprint from the answer to its report, and sends it in the next report.
        $agent = fn (array $body) => $this->withToken('cfa_web2')->withHeader('X-Cloudfactory-Edge', '1')->withHeader('X-Client-Cert-Sha256', str_repeat('ab', 32))
            ->postJson('/api/agent/v1/report', $body);
        $agent(['agentVersion' => '0.2.0'])->assertOk()->assertJson(['panelCaSha256' => $this->caSha256]);
        $agent(['agentVersion' => '0.2.0', 'panelCaSha256' => str_repeat('cd', 32)])->assertOk();
        $this->getJson('/api/v1/panel-certificate')->assertJson(['serversWithoutCa' => ['web-2']]);
        $agent(['agentVersion' => '0.2.0', 'panelCaSha256' => $this->caSha256])->assertOk();

        $this->getJson('/api/v1/panel-certificate')->assertJson(['serversWithoutCa' => []]);
        $this->putJson('/api/v1/panel-certificate', ['method' => 'internal'])->assertAccepted()
            ->assertJsonPath('job.title', 'Change the panel certificate to the panel CA');
    }

    public function test_only_an_administrator_sees_the_panel_certificate(): void
    {
        $this->actingWith($this->user(Role::Owner, $this->customer()));

        $this->getJson('/api/v1/panel-certificate')->assertForbidden();
        $this->putJson('/api/v1/panel-certificate', ['method' => 'internal'])->assertForbidden();
    }

    public function test_the_panel_keeps_only_a_valid_ca_certificate(): void
    {
        PanelCertificate::record(['caCertificate' => 'not a certificate'] + $this->report()['panelCertificate']);

        $this->assertNull(PanelCertificate::current()['caCertificate']);
        $this->assertNull(PanelCertificate::joinPin());
    }
}
