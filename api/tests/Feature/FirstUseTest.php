<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Rollout;
use App\Models\Server;
use App\Models\Website;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * The faults that a person found on the first use of a new panel: a server that was always
 * degraded, a website that could not be repaired, and missing database drivers.
 */
class FirstUseTest extends TestCase
{
    private function agentServer(array $attributes = []): Server
    {
        return $this->server($attributes + ['agent_token_hash' => hash('sha256', 'cfa_secret'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'local' => true]);
    }

    private function report(array $services): void
    {
        $this->asLocalAgent('cfa_secret')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'services' => $services])->assertOk();
    }

    public function test_an_optional_part_that_is_off_does_not_make_the_server_degraded(): void
    {
        $server = $this->agentServer();

        $this->report([
            ['name' => 'cf-agent', 'state' => 'running'],
            ['name' => 'firewall', 'state' => 'running'],
            ['name' => 'disk-quotas', 'state' => 'stopped', 'optional' => true, 'detail' => 'The file system of /srv/websites has no project quotas.'],
        ]);

        $this->assertSame('healthy', $server->fresh()->state);
    }

    public function test_the_report_of_an_agent_of_an_earlier_release_does_not_make_the_server_degraded(): void
    {
        $server = $this->agentServer();

        // The agent of 0.1.9 reported the nftables service, which is not active before the first
        // restart of the server, and the disk quotas without "optional".
        $this->report([
            ['name' => 'cf-agent', 'state' => 'running'],
            ['name' => 'nftables', 'state' => 'stopped'],
            ['name' => 'disk-quotas', 'state' => 'stopped'],
        ]);

        $this->assertSame('healthy', $server->fresh()->state);
        $this->actingWith($this->user());
        $this->getJson("/api/v1/servers/{$server->id}")->assertOk()
            ->assertJsonCount(2, 'services')
            ->assertJsonPath('services.0.name', 'cf-agent')
            ->assertJsonPath('services.1.name', 'disk-quotas')
            ->assertJsonPath('services.1.optional', true);
    }

    public function test_a_necessary_service_that_does_not_run_makes_the_server_degraded_and_gives_the_reason(): void
    {
        $server = $this->agentServer();

        $this->report([
            ['name' => 'cf-agent', 'state' => 'running'],
            ['name' => 'firewall', 'state' => 'stopped', 'detail' => 'The firewall rules are not loaded. Run: nft -f /etc/nftables.d-cloudfactory.nft'],
        ]);

        $this->assertSame('degraded', $server->fresh()->state);
        $this->actingWith($this->user());
        $this->getJson("/api/v1/servers/{$server->id}")->assertOk()
            ->assertJsonPath('state', 'degraded')
            ->assertJsonPath('services.1.optional', false)
            ->assertJsonPath('services.1.detail', 'The firewall rules are not loaded. Run: nft -f /etc/nftables.d-cloudfactory.nft');

        $this->report([['name' => 'cf-agent', 'state' => 'running'], ['name' => 'firewall', 'state' => 'running']]);
        $this->assertSame('healthy', $server->fresh()->state);
    }

    public function test_a_degraded_server_gets_a_new_website_when_no_server_is_healthy(): void
    {
        $customer = $this->customer();
        $degraded = $this->server(['state' => 'degraded']);
        $this->server(['state' => 'offline']);
        $image = $this->image();
        $this->actingWith($this->user(Role::Owner, $customer));

        $id = $this->postJson('/api/v1/websites', ['name' => 'one.example', 'imageId' => $image->id, 'planId' => $customer->plan_id])
            ->assertAccepted()->json('website.id');

        $this->assertSame($degraded->id, Website::findOrFail($id)->server_id);
    }

    public function test_a_healthy_server_gets_the_new_website_before_a_degraded_server(): void
    {
        $customer = $this->customer();
        $this->server(['state' => 'degraded']);
        $healthy = $this->server();
        $image = $this->image();
        // The healthy server has more websites, but it is the better server.
        $this->website($customer, $healthy, $image, 'busy.example');
        $this->actingWith($this->user(Role::Owner, $customer));

        $id = $this->postJson('/api/v1/websites', ['name' => 'one.example', 'imageId' => $image->id, 'planId' => $customer->plan_id])
            ->assertAccepted()->json('website.id');

        $this->assertSame($healthy->id, Website::findOrFail($id)->server_id);
    }

    public function test_no_server_gets_a_website_when_all_servers_are_offline(): void
    {
        $customer = $this->customer();
        $this->server(['state' => 'offline']);
        $image = $this->image();
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson('/api/v1/websites', ['name' => 'one.example', 'imageId' => $image->id, 'planId' => $customer->plan_id])
            ->assertUnprocessable()->assertJsonValidationErrors('serverId');
    }

    public function test_a_new_website_has_the_database_drivers_that_the_plan_allows(): void
    {
        $plan = $this->plan(['php_extensions' => ['opcache', 'gd', 'mysqli', 'pdo_mysql', 'pdo_pgsql', 'redis']]);
        $customer = $this->customer($plan);
        $this->server();
        $image = $this->image();
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson('/api/v1/websites', ['name' => 'one.example', 'imageId' => $image->id, 'planId' => $plan->id])
            ->assertAccepted()
            ->assertJsonPath('website.runtime.extensions', ['opcache', 'gd', 'mysqli', 'pdo_mysql', 'pdo_pgsql']);
    }

    public function test_a_website_that_the_panel_could_not_make_can_be_made_again(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image(), attributes: ['status' => 'failed']);
        $this->actingWith($this->user(Role::Owner, $customer));

        $response = $this->postJson("/api/v1/websites/{$website->id}/retry")->assertAccepted()
            ->assertJsonPath('website.status', 'starting')
            ->assertJsonPath('job.action', 'CreateWebsite');

        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame($website->id, $job->website_id);
        $this->assertSame($website->unix_user, $job->payload['website']['unixUser']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Created website again', 'target' => 'acme.example']);

        app(JobOutcome::class)->finish($job, JobState::Succeeded, null);
        $this->assertSame('running', $website->fresh()->status->value);
    }

    public function test_only_a_failed_website_can_be_made_again(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $running = $this->website($customer, $server, $image);
        $copy = $this->website($customer, $server, $image, 'staging.acme.example', ['status' => 'failed', 'staging_of' => $running->id]);
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$running->id}/retry")->assertUnprocessable()->assertJsonValidationErrors('website');
        $this->postJson("/api/v1/websites/{$copy->id}/retry")->assertUnprocessable()->assertJsonValidationErrors('website');
        $this->assertSame(0, AgentJob::count());
    }

    public function test_a_failed_website_cannot_start_before_the_panel_makes_it(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image(), attributes: ['status' => 'failed']);
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'start'])
            ->assertUnprocessable()->assertJsonValidationErrors('website');
        $this->assertSame('failed', $website->fresh()->status->value);
    }

    public function test_a_different_image_for_a_failed_website_makes_the_website_again(): void
    {
        $customer = $this->customer();
        $bad = $this->image(['name' => 'FrankenPHP · PHP 8.5', 'php_version' => '8.5', 'reference' => 'ghcr.io/cloudfactory/frankenphp-php8.5']);
        $good = $this->image();
        $website = $this->website($customer, $this->server(), $bad, attributes: ['status' => 'failed']);
        $this->actingWith($this->user());

        $response = $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $good->id])->assertAccepted()
            ->assertJsonPath('website.status', 'starting')
            ->assertJsonPath('job.action', 'CreateWebsite');

        $website->refresh();
        $this->assertSame($good->id, $website->image_id);
        // The first image never operated, so the panel does not offer to go back to it.
        $this->assertNull($website->previous_image_id);
        $this->assertSame($good->reference.':'.$good->tag, AgentJob::findOrFail($response->json('job.id'))->payload['website']['image']);
    }

    public function test_a_rollout_does_not_update_a_website_that_the_panel_could_not_make(): void
    {
        $image = $this->image(['tag' => '0.1.7', 'available_tag' => '0.1.9']);
        $customer = $this->customer();
        $server = $this->server();
        $running = $this->website($customer, $server, $image, 'one.example');
        $failed = $this->website($customer, $server, $image, 'two.example', ['status' => 'failed']);
        $this->actingWith($this->user());
        $this->getJson("/api/v1/websites/{$failed->id}")->assertJsonPath('data.updateReady', false);

        $id = $this->postJson('/api/v1/rollouts', ['imageId' => $image->id])->assertCreated()->assertJsonPath('websites', 1)->json('id');
        $jobs = AgentJob::where('rollout_id', $id)->get();
        $this->assertSame([$running->id], $jobs->pluck('website_id')->all());
        app(JobOutcome::class)->finish($jobs[0], JobState::Succeeded, null);

        $this->assertSame('done', Rollout::findOrFail($id)->state);
        $this->assertSame('0.1.9', $image->fresh()->tag);
        // The failed website follows the image. It does not keep the old tag.
        $this->assertNull($failed->fresh()->image_tag);
        $this->assertSame('0.1.9', $failed->fresh()->currentTag());
    }

    public function test_a_request_with_no_token_gets_401_also_with_no_accept_header(): void
    {
        // Before, the framework looked for a "login" route, and the answer was status 500.
        $this->get('/api/v1/me')->assertUnauthorized()->assertJson(['message' => 'Unauthenticated.']);
        $this->get('/api/v1/websites', ['Accept' => 'text/html'])->assertUnauthorized();
        $this->withToken('not-a-token')->get('/api/v1/me')->assertUnauthorized();
    }

    public function test_a_rate_limit_tells_the_user_how_long_to_wait(): void
    {
        // The limit of the sign-in route is 10 requests in one minute for each address. Each
        // request has a different email, so the limit of an account does not apply.
        for ($i = 0; $i < 10; $i++) {
            $this->postJson('/api/v1/auth/login', ['email' => "nobody{$i}@example.com", 'password' => 'not-the-password'])->assertStatus(422);
        }
        $answer = $this->postJson('/api/v1/auth/login', ['email' => 'nobody10@example.com', 'password' => 'not-the-password'])->assertStatus(429);
        $answer->assertHeader('Retry-After');
        $this->assertMatchesRegularExpression('/^Too many requests\. Try again in \d+ seconds\.$/', $answer->json('message'));
    }

    public function test_a_rollout_that_ended_gives_the_time_of_the_end(): void
    {
        $image = $this->image(['available_tag' => '8.4.0-2']);
        $this->website($this->customer(), $this->server(), $image);
        $this->actingWith($this->user());

        $id = $this->postJson('/api/v1/rollouts', ['imageId' => $image->id])->assertCreated()->assertJsonPath('finishedAt', null)->json('id');
        foreach (AgentJob::where('rollout_id', $id)->get() as $job) {
            app(JobOutcome::class)->finish($job, JobState::Succeeded, null);
        }

        $this->assertSame('done', Rollout::findOrFail($id)->state);
        $this->assertNotNull($this->getJson("/api/v1/rollouts/{$id}")->assertOk()->json('finishedAt'));
    }
}
