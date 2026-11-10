<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Http\Controllers\Api\ImageController;
use App\Http\Controllers\Api\WebsiteController;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\Domain;
use App\Models\Rollout;
use App\Models\Setting;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\Audit;
use App\Services\JobDispatcher;
use App\Services\JobOutcome;
use App\Services\RolloutEngine;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

/** Regression tests for the findings of the audit of 10 November 2026 (S4 to S9, R1 to R11). */
class AuditFixesTest extends TestCase
{
    // S4

    public function test_nobody_starts_a_website_of_a_suspended_customer(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['role' => 'reseller', 'name' => 'Host Ltd']);
        $customer = $this->customer($plan, ['reseller_id' => $reseller->id, 'suspended' => true, 'suspended_by' => 'administrator']);
        $website = $this->website($customer, $this->server(), $this->image(), 'acme.example', ['status' => 'stopped']);
        $this->actingWith($this->user(Role::Reseller, $reseller));

        foreach (['start', 'restart'] as $action) {
            $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => $action])
                ->assertStatus(409)->assertJsonPath('message', 'This customer is suspended. Lift the suspension before you start a website.');
        }
        $this->postJson("/api/v1/websites/{$website->id}/staging")->assertStatus(409);
        $this->postJson('/api/v1/websites', ['name' => 'new.example', 'imageId' => $website->image_id, 'customerId' => $customer->id])->assertStatus(409);
        $this->assertSame(0, AgentJob::count());
        $this->assertSame('stopped', $website->fresh()->status->value);

        // A stop is still possible, and the reseller cannot lift the suspension of the administrator.
        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'stop'])->assertAccepted();
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertForbidden();
    }

    // S5

    public function test_a_website_has_a_limit_of_domains(): void
    {
        config(['cloudfactory.domains_per_website' => 3]);
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'a.acme.example', 'kind' => 'alias'])->assertAccepted();
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'b.acme.example', 'kind' => 'alias'])->assertAccepted();
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'c.acme.example', 'kind' => 'alias'])
            ->assertJsonValidationErrors(['name' => 'A website can have 3 domains.']);
    }

    public function test_a_customer_has_a_rate_limit_for_new_domains_and_checks(): void
    {
        config(['cloudfactory.domains_per_website' => 1000]);
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));

        for ($i = 0; $i < 30; $i++) {
            $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => "s{$i}.acme.example", 'kind' => 'alias'])->assertAccepted();
        }
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'more.acme.example', 'kind' => 'alias'])->assertStatus(429);

        for ($i = 0; $i < 10; $i++) {
            $this->postJson("/api/v1/websites/{$website->id}/domains/acme.example/check")->assertAccepted();
        }
        $this->postJson("/api/v1/websites/{$website->id}/domains/acme.example/check")->assertStatus(429);

        // An administrator has no limit.
        $this->actingWith($this->user());
        $this->postJson("/api/v1/websites/{$website->id}/domains/acme.example/check")->assertAccepted();
    }

    public function test_a_customer_that_proves_control_takes_a_verified_domain(): void
    {
        $first = $this->customer();
        $second = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $old = $this->website($first, $server, $image, 'old.example');
        $old->domains()->create(['name' => 'shop.example', 'kind' => 'alias', 'verification_token' => 'cf-x', 'verified_at' => now()]);
        $new = $this->website($second, $server, $image, 'new.example');
        $this->actingWith($this->user(Role::Owner, $second));
        $txt = [];
        $policy = $this->fakeTxt($txt);

        // With no proof, the domain stays. The message gives the TXT record.
        $token = $policy->claimToken($second->id, 'shop.example');
        $message = $this->postJson("/api/v1/websites/{$new->id}/domains", ['name' => 'shop.example', 'kind' => 'alias'])
            ->assertUnprocessable()->json('errors.name.0');
        $this->assertStringContainsString("_cloudfactory-challenge.shop.example with the value {$token}", $message);
        $this->assertSame($old->id, Domain::where('name', 'shop.example')->sole()->website_id);

        // The domain has a new owner, who sets the TXT record.
        $txt['_cloudfactory-challenge.shop.example'] = [$token];
        $this->postJson("/api/v1/websites/{$new->id}/domains", ['name' => 'shop.example', 'kind' => 'alias'])
            ->assertAccepted()->assertJsonPath('domain.verified', true)->assertJsonPath('job.action', 'AddDomain');
        $this->assertSame($new->id, Domain::where('name', 'shop.example')->sole()->website_id);
        // The edge proxy of the old website stops serving the domain.
        $remove = AgentJob::where('action', AgentAction::RemoveDomain->value)->sole();
        $this->assertSame($old->id, $remove->website_id);
        $this->assertSame('shop.example', $remove->payload['domain']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Removed domain, because a different customer proved control of it', 'customer_id' => $first->id]);

        // The primary domain of a website: the website gets an internal name.
        $txt['_cloudfactory-challenge.old.example'] = [$policy->claimToken($second->id, 'old.example')];
        $this->postJson("/api/v1/websites/{$new->id}/domains", ['name' => 'old.example', 'kind' => 'alias'])->assertAccepted();
        $this->assertSame('parked-'.strtolower($old->id).'.invalid', $old->fresh()->name);
        $this->assertFalse($old->domains()->sole()->verified());
        $this->assertSame(2, AgentJob::where('action', AgentAction::RemoveDomain->value)->where('website_id', $old->id)->count());
    }

    // S9

    public function test_a_customer_sees_only_the_times_of_the_backup_policy(): void
    {
        Setting::put('backup_policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3:s3.example.com/bucket']);
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->getJson('/api/v1/backup-policy')->assertOk()->assertExactJson(['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00']);

        $this->actingWith($this->user());
        $this->getJson('/api/v1/backup-policy')->assertOk()->assertJsonPath('target', 's3:s3.example.com/bucket')->assertJsonStructure(['credentialNames']);
    }

    public function test_a_customer_does_not_see_the_name_and_the_address_of_an_administrator(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['role' => 'reseller', 'name' => 'Host Ltd']);
        $customer = $this->customer($plan, ['reseller_id' => $reseller->id]);
        $admin = $this->user();
        $provider = $this->user(Role::Reseller, $reseller);
        $owner = $this->user(Role::Owner, $customer);
        foreach ([[$admin, '198.51.100.1'], [$provider, '198.51.100.2'], [$owner, '198.51.100.3']] as [$user, $ip]) {
            $request = Request::create('/', 'POST', server: ['REMOTE_ADDR' => $ip]);
            $request->setUserResolver(fn () => $user);
            Audit::record($request, 'Changed website', 'acme.example', $customer->id);
        }

        $this->actingWith($owner);
        $rows = collect($this->getJson('/api/v1/audit')->assertOk()->json('data'))->map(fn ($r) => [$r['actor'], $r['ip']])->all();
        $this->assertSame([[$owner->name, '198.51.100.3'], ['Your provider', null], ['Administrator', null]], $rows);
        // A search for the name or the address of the administrator finds nothing.
        $this->getJson('/api/v1/audit?q='.urlencode($admin->name))->assertJsonCount(0, 'data');
        $this->getJson('/api/v1/audit?q=198.51.100.1')->assertJsonCount(0, 'data');

        $this->actingWith($provider);
        $rows = collect($this->getJson('/api/v1/audit')->json('data'))->map(fn ($r) => [$r['actor'], $r['ip']])->all();
        $this->assertSame([[$owner->name, '198.51.100.3'], [$provider->name, '198.51.100.2'], ['Administrator', null]], $rows);

        $this->actingWith($admin);
        $this->getJson('/api/v1/audit?q=198.51.100.1')->assertJsonCount(1, 'data')->assertJsonPath('data.0.actor', $admin->name);
        $this->assertSame(3, AuditEntry::count());
    }

    public function test_a_customer_gets_a_short_error_of_a_failed_job(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $server = $this->server(), $this->image());
        $dispatcher = app(JobDispatcher::class);
        $command = $dispatcher->dispatch($server, AgentAction::SetRuntime, [], 'Change runtime', $website);
        $command->update(['state' => JobState::Failed, 'step' => 1, 'error' => "runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/1001 podman pull ghcr.io/x: exit status 125: Error: secret detail\nline 2"]);
        $plain = $dispatcher->dispatch($server, AgentAction::SetRuntime, [], 'Change runtime', $website);
        $plain->update(['state' => JobState::Failed, 'error' => "the website did not answer: status 500\n".str_repeat('x', 500)]);

        $this->actingWith($this->user(Role::Owner, $customer));
        $step = $command->steps[1];
        $this->getJson("/api/v1/jobs/{$command->id}")->assertOk()->assertJsonPath('data.error', "The step \"{$step}\" failed on the server. Your provider can see the details.");
        $this->getJson("/api/v1/jobs/{$plain->id}")->assertOk()->assertJsonPath('data.error', 'the website did not answer: status 500');

        $this->actingWith($this->user());
        $this->assertStringContainsString('secret detail', $this->getJson("/api/v1/jobs/{$command->id}")->json('data.error'));
    }

    public function test_the_error_of_an_agent_that_does_not_answer_has_no_address(): void
    {
        $job = app(JobDispatcher::class)->dispatch($this->server(), AgentAction::Reboot, [], 'Reboot');

        (new SendJobToAgent($job->id))->failed(new \RuntimeException('cURL error 7: Failed to connect to web-1.internal port 7443'));
        $this->assertSame(JobState::Failed, $job->fresh()->state);
        $this->assertStringNotContainsString('web-1.internal', $job->fresh()->error);
    }

    // R1

    public function test_a_late_event_does_not_open_a_complete_job_again(): void
    {
        $server = $this->server();
        $outcome = app(JobOutcome::class);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        // The caller has an old copy of the job, for example in the queue worker.
        $stale = AgentJob::findOrFail($job->id);

        $outcome->finish($job, JobState::Succeeded);
        $outcome->started($stale);
        $this->assertSame(JobState::Succeeded, $job->fresh()->state);
        $outcome->progress(AgentJob::findOrFail($job->id), 1);
        $this->assertSame(JobState::Succeeded, $job->fresh()->state);
        $this->assertFalse($outcome->queueAgain(AgentJob::findOrFail($job->id)));
        // A second final report changes nothing.
        $outcome->finish($stale, JobState::Failed, 'late');
        $this->assertSame(JobState::Succeeded, $job->fresh()->state);
        $this->assertNull($job->fresh()->error);
    }

    public function test_the_send_does_not_mark_a_complete_job_as_running(): void
    {
        $server = $this->server(['certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cf-test-agents-'.uniqid()]);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        // The agent completes the job and reports before the answer of the send arrives.
        Http::fake(function () use ($job) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($job->id), JobState::Succeeded);

            return Http::response(['accepted' => true]);
        });

        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        $this->assertSame(JobState::Succeeded, $job->fresh()->state);
    }

    // R2

    public function test_each_rollout_result_is_recorded(): void
    {
        $image = $this->image(['available_tag' => '8.4.1-1', 'available_digest' => 'sha256:new']);
        $server = $this->server();
        $customer = $this->customer();
        foreach (range(1, 4) as $i) {
            $this->website($customer, $server, $image, "site{$i}.example");
        }
        $rollout = app(RolloutEngine::class)->start($image);
        $outcome = app(JobOutcome::class);

        // Each caller has its own old copy of the rollout, as two queue workers have.
        for ($stage = 0; $stage < 10 && $rollout->fresh()->state === 'running'; $stage++) {
            foreach (AgentJob::where('rollout_id', $rollout->id)->where('state', JobState::Queued->value)->get() as $job) {
                $outcome->finish($job, JobState::Succeeded);
            }
        }
        $done = Rollout::findOrFail($rollout->id);
        $this->assertSame('done', $done->state);
        $this->assertCount(4, $done->updated_ids);
        $this->assertSame(4, AgentJob::where('rollout_id', $rollout->id)->count(), 'a stage started two times');
    }

    // R5

    public function test_the_panel_and_the_agent_use_the_same_examples_for_names(): void
    {
        $names = json_decode((string) file_get_contents(base_path('../testdata/names.json')), true);

        foreach ($names['domains']['valid'] as $name) {
            $this->assertSame(1, preg_match(WebsiteController::DOMAIN, $name), "The panel refuses the domain {$name}.");
        }
        foreach ($names['domains']['invalid'] as $name) {
            $this->assertSame(0, preg_match(WebsiteController::DOMAIN, $name), "The panel accepts the domain {$name}.");
        }
        foreach ($names['images']['valid'] as $name) {
            $this->assertSame(1, preg_match(ImageController::REFERENCE, $name), "The panel refuses the image {$name}.");
        }
        foreach ($names['images']['invalid'] as $name) {
            $this->assertSame(0, preg_match(ImageController::REFERENCE, $name), "The panel accepts the image {$name}.");
        }
    }

    public function test_the_panel_refuses_names_that_the_agent_refuses(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->actingWith($this->user());

        foreach (['a.-b.example', '1.2.3.4', 'bad-.example'] as $name) {
            $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => $name, 'kind' => 'alias'])->assertJsonValidationErrors('name');
            $this->postJson('/api/v1/websites', ['name' => $name, 'imageId' => $website->image_id, 'customerId' => $website->customer_id])->assertJsonValidationErrors('name');
        }
        $this->postJson('/api/v1/images', ['name' => 'Nginx', 'reference' => 'nginx', 'tag' => '1.27', 'policy' => 'manual'])->assertJsonValidationErrors('reference');
        $this->postJson('/api/v1/images', ['name' => 'Nginx', 'reference' => 'registry.example.com/nginx', 'tag' => '1.27', 'policy' => 'manual'])->assertCreated();
    }

    public function test_the_agent_has_no_mailbox_restore(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $snapshot = $website->snapshots()->create(['kind' => 'manual', 'restic_id' => 'abc', 'size_bytes' => 1, 'databases' => [], 'mailboxes' => []]);
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/snapshots/{$snapshot->id}/restore", ['scope' => 'mailbox', 'item' => 'info@acme.example'])
            ->assertJsonValidationErrors('scope');
    }

    public function test_a_long_error_of_the_agent_does_not_lose_the_result(): void
    {
        $server = $this->server(['local' => true, 'agent_token_hash' => hash('sha256', 'cfa_test'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT]);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');

        $this->asLocalAgent('cfa_test')->postJson("/api/agent/v1/jobs/{$job->id}/events", ['state' => 'failed', 'error' => str_repeat('é', 9000)])->assertOk();
        $this->assertSame(JobState::Failed, $job->fresh()->state);
        $this->assertSame(4000, mb_strlen($job->fresh()->error));
    }

    // R6

    public function test_the_websites_of_a_reseller_count_against_its_total(): void
    {
        $plan = $this->plan(['name' => 'Small', 'websites' => 2]);
        $reseller = $this->customer($plan, ['role' => 'reseller', 'name' => 'Host Ltd']);
        $customer = $this->customer($plan, ['reseller_id' => $reseller->id]);
        $server = $this->server();
        $image = $this->image();
        $this->website($customer, $server, $image, 'one.example');
        $this->website($customer, $server, $image, 'two.example');
        $this->actingWith($this->user());

        $this->postJson('/api/v1/websites', ['name' => 'own.example', 'imageId' => $image->id, 'customerId' => $reseller->id])
            ->assertJsonValidationErrors(['name' => 'The Small plan allows 2 websites in total, for you and your customers.']);
    }

    public function test_a_plan_change_needs_space_in_the_new_plan(): void
    {
        $big = $this->plan();
        $small = $this->plan(['name' => 'Small', 'websites' => 1, 'databases' => 1]);
        $customer = $this->customer($big);
        $server = $this->server();
        $image = $this->image();
        $website = $this->website($customer, $server, $image, 'one.example');
        $this->website($customer, $server, $image, 'two.example');
        $website->databases()->create(['engine' => 'mariadb', 'name' => 'a_one']);
        $website->databases()->create(['engine' => 'mariadb', 'name' => 'a_two']);
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/customers/{$customer->id}", ['planId' => $small->id])
            ->assertJsonValidationErrors(['planId' => 'The Small plan allows 1 websites. Acme Ltd has 2.']);
        $this->patchJson("/api/v1/websites/{$website->id}", ['planId' => $small->id])
            ->assertJsonValidationErrors(['planId' => 'The Small plan allows 1 databases. This website has 2.']);
        $this->assertSame($big->id, $customer->fresh()->plan_id);
        $this->assertSame($big->id, $website->fresh()->plan_id);
    }

    public function test_a_staging_copy_needs_space_for_its_databases(): void
    {
        $plan = $this->plan(['databases' => 1]);
        $customer = $this->customer($plan);
        $website = $this->website($customer, $this->server(), $this->image());
        $website->databases()->create(['engine' => 'mariadb', 'name' => 'a_one']);
        $website->databases()->create(['engine' => 'mariadb', 'name' => 'a_two']);
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/staging")->assertJsonValidationErrors('name');
        $this->assertSame(1, Website::count());
    }

    // R7

    public function test_a_staging_copy_does_not_get_the_sftp_settings(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image(), 'acme.example', ['sftp_enabled' => true, 'sftp_keys' => ['ssh-ed25519 AAAA test']]);
        $this->actingWith($this->user());

        $id = $this->postJson("/api/v1/websites/{$website->id}/staging")->assertAccepted()->json('website.id');
        $copy = Website::findOrFail($id);
        $this->assertFalse((bool) $copy->sftp_enabled);
        $this->assertEmpty($copy->sftp_keys);
        $this->assertTrue((bool) $website->fresh()->sftp_enabled);
    }

    // R11

    public function test_the_panel_keeps_the_jobs_of_an_offline_server(): void
    {
        $server = $this->server(['state' => 'offline', 'local' => true, 'agent_token_hash' => hash('sha256', 'cfa_test'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT]);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        Http::fake();

        // The queue worker does not call the agent.
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertNothingSent();
        $this->assertSame(JobState::Queued, $job->fresh()->state);

        // The server reports again. The panel sends the job.
        Queue::fake();
        $this->asLocalAgent('cfa_test')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0'])->assertOk();
        $this->assertSame('healthy', $server->fresh()->state);
        Queue::assertPushed(SendJobToAgent::class, fn (SendJobToAgent $send) => $send->jobId === $job->id);
    }
}
