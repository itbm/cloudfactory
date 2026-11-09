<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Http\Controllers\Api\BackupController;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Setting;
use App\Models\Snapshot;
use App\Services\AgentClient;
use App\Services\JobDispatcher;
use App\Services\JobOutcome;
use App\Services\RolloutEngine;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The findings of the completeness audit of release 0.1 (batches 1 to 3). */
class CompletenessTest extends TestCase
{
    private const POLICY = ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3:s3.amazonaws.com/backups'];

    private function finish(string $jobId, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);

        return $job->fresh();
    }

    public function test_backup_credentials_are_write_only_and_go_to_the_agent_when_it_gets_the_job(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->actingWith($this->user());
        $this->putJson('/api/v1/backup-policy', self::POLICY + ['credentials' => ['AWS_ACCESS_KEY_ID' => 'AKIA1', 'AWS_SECRET_ACCESS_KEY' => 'secret']])
            ->assertOk()->assertJsonPath('credentialNames', ['AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY'])->assertJsonMissingPath('credentials');
        $this->putJson('/api/v1/backup-policy', self::POLICY + ['credentials' => ['PATH' => '/tmp']])->assertJsonValidationErrors('credentials.PATH');
        // A policy change with no credentials keeps them.
        $this->putJson('/api/v1/backup-policy', self::POLICY)->assertOk()->assertJsonPath('credentialNames', ['AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY']);
        $this->assertStringNotContainsString('secret', (string) DB::table('settings')->where('key', 'backup_credentials')->value('value'));

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/snapshots")->assertAccepted()->json('job.id');
        $job = AgentJob::with('server')->findOrFail($jobId);
        $this->assertArrayNotHasKey('credentials', $job->payload);
        $job->server->update(['certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
        Http::fake(['*' => Http::response(['jobId' => $jobId])]);
        app(AgentClient::class)->send($job->fresh('server'));
        $password = BackupController::repositoryPassword();
        Http::assertSent(fn ($r) => (array) $r->data()['credentials'] === ['AWS_ACCESS_KEY_ID' => 'AKIA1', 'AWS_SECRET_ACCESS_KEY' => 'secret', 'RESTIC_PASSWORD' => $password] && $r->data()['target'] === self::POLICY['target']);
    }

    public function test_all_servers_use_one_repository_password_that_only_an_administrator_can_read(): void
    {
        $admin = $this->user();
        $this->actingWith($admin);
        $first = $this->getJson('/api/v1/backup-policy/repository-password')->assertOk()->json('password');
        $this->assertGreaterThanOrEqual(32, strlen($first));
        $this->getJson('/api/v1/backup-policy/repository-password')->assertOk()->assertJsonPath('password', $first);
        $this->assertSame($first, BackupController::repositoryPassword());
        $this->assertStringNotContainsString($first, (string) DB::table('settings')->where('key', 'backup_repository_password')->value('value'));
        $this->assertDatabaseHas('audit_entries', ['action' => 'Viewed backup repository password']);

        $this->actingWith($this->user(Role::Reseller));
        $this->getJson('/api/v1/backup-policy/repository-password')->assertForbidden();
    }

    public function test_nightly_backups_apply_the_retention_and_remove_forgotten_snapshots(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        Setting::put('backup_policy', self::POLICY);
        $old = Snapshot::create(['website_id' => $website->id, 'kind' => 'scheduled', 'restic_id' => 'old1', 'databases' => [], 'mailboxes' => []]);

        // Sunday 2 November 2026, 03:00 UTC.
        Carbon::setTestNow(Carbon::parse('2026-11-01 03:00:00', 'UTC'));
        $this->artisan('cloudfactory:backups')->assertSuccessful();
        $this->artisan('cloudfactory:backups')->assertSuccessful();
        $backups = AgentJob::where('action', AgentAction::Backup->value)->get();
        $this->assertCount(1, $backups, 'The backups start once each day.');
        $this->assertSame(['daily' => 7, 'weekly' => 4, 'monthly' => 6], $backups[0]->payload['retention']);
        $this->assertSame(1, AgentJob::where('action', AgentAction::PruneBackups->value)->count());

        $this->finish($backups[0]->id, result: ['snapshotId' => 'new1', 'forgotten' => ['old1']]);
        $this->assertModelMissing($old);
        $this->assertSame(['new1'], $website->snapshots()->pluck('restic_id')->all());
        Carbon::setTestNow();
    }

    public function test_sftp_access_accepts_checked_ssh_keys_only(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->actingWith($this->user());
        $key = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ7m0Yv0Hj8d1bqQn5x9sP1rZ2y3w4v5u6t7s8r9q0p me@laptop';
        $url = "/api/v1/websites/{$website->id}/sftp";

        $this->putJson($url, ['enabled' => true, 'authorizedKeys' => ['command="sh" '.$key]])->assertJsonValidationErrors('authorizedKeys.0');
        // No password: a request for one is refused, and SFTP needs at least one key.
        $this->putJson($url, ['enabled' => true, 'newPassword' => true, 'authorizedKeys' => [$key]])->assertJsonValidationErrors('newPassword');
        $this->putJson($url, ['enabled' => true, 'authorizedKeys' => []])->assertJsonValidationErrors('authorizedKeys');
        $this->assertSame(0, AgentJob::count());

        $response = $this->putJson($url, ['enabled' => true, 'authorizedKeys' => [$key]])->assertAccepted()->assertJsonMissingPath('password');
        $job = $this->finish($response->json('job.id'));
        $this->assertSame(['websiteId', 'unixUser', 'enabled', 'authorizedKeys'], array_keys($job->payload));
        $this->getJson("/api/v1/websites/{$website->id}")
            ->assertJsonPath('data.sftp.enabled', true)->assertJsonMissingPath('data.sftp.passwordSet')
            ->assertJsonPath('data.sftp.username', $website->unix_user)->assertJsonPath('data.sftp.authorizedKeys', [$key]);

        $this->finish($this->putJson($url, ['enabled' => false])->assertAccepted()->json('job.id'));
        $this->getJson("/api/v1/websites/{$website->id}")->assertJsonPath('data.sftp.enabled', false);
    }

    public function test_the_sftp_port_comes_from_the_report_of_the_agent(): void
    {
        $server = $this->server(['agent_token_hash' => hash('sha256', 'cfa_secret'), 'local' => true]);
        $website = $this->website($this->customer(), $server, $this->image());
        $this->actingWith($this->user());
        // An agent of release 0.1 uses the SSH service of the host.
        $this->getJson("/api/v1/websites/{$website->id}")->assertJsonPath('data.sftp.port', 22);

        $this->withToken('cfa_secret')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'sftpPort' => 2200])->assertOk();
        $this->getJson("/api/v1/websites/{$website->id}")->assertJsonPath('data.sftp.port', 2200);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('data.sftpPort', 2200);
        // A report with no port keeps the last port.
        $this->withToken('cfa_secret')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0'])->assertOk();
        $this->assertSame(2200, $server->fresh()->sftp_port);
        $this->withToken('cfa_secret')->postJson('/api/agent/v1/report', ['agentVersion' => '0.2.0', 'sftpPort' => 70000])->assertJsonValidationErrors('sftpPort');
    }

    public function test_a_verified_domain_becomes_primary_and_the_old_primary_becomes_an_alias(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $website->domains()->create(['name' => 'www.acme.example', 'kind' => 'alias', 'verification_token' => 'cf-x', 'verified_at' => now()]);
        $website->domains()->create(['name' => 'new.acme.example', 'kind' => 'alias', 'verification_token' => 'cf-y']);
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/domains/new.acme.example/primary")->assertJsonValidationErrors('name');
        $response = $this->postJson("/api/v1/websites/{$website->id}/domains/www.acme.example/primary")->assertAccepted()
            ->assertJsonPath('website.name', 'www.acme.example')->assertJsonPath('job.action', 'AddDomain');
        $this->assertSame(['kind' => 'primary', 'primary' => 'www.acme.example'], array_intersect_key(AgentJob::findOrFail($response->json('job.id'))->payload, ['kind' => 1, 'primary' => 1]));
        $this->assertSame('alias', $website->domains()->where('name', 'acme.example')->first()->kind->value);

        $this->postJson("/api/v1/websites/{$website->id}/domains/acme.example/check")->assertAccepted()->assertJsonPath('domain.certificate.state', 'pending');
        // A failed check shows the error on the domain.
        $this->finish(AgentJob::latest('id')->first()->id, JobState::Failed);
        $this->assertSame('failed', $website->domains()->where('name', 'acme.example')->value('certificate_state'));
    }

    public function test_a_staging_copy_gets_its_own_databases_and_user(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $website->databases()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop']);
        $website->databases()->create(['engine' => 'postgresql', 'name' => $website->unix_user.'_reports']);
        $this->actingWith($this->user());

        $response = $this->postJson("/api/v1/websites/{$website->id}/staging")->assertAccepted();
        $copy = $response->json('website');
        $credentials = collect($response->json('databaseCredentials'))->keyBy('engine');
        $this->assertSame($copy['unixUser'].'_app', $credentials['mariadb']['user']);
        $this->assertSame([$copy['unixUser'].'_shop'], $credentials['mariadb']['databases']);
        $this->assertSame(24, strlen($credentials['postgresql']['password']));

        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertContains(['engine' => 'mariadb', 'from' => $website->unix_user.'_shop', 'to' => $copy['unixUser'].'_shop'], $job->payload['copyDatabases']);
        $this->assertSame(2, Database::where('website_id', $copy['id'])->count());
        $job = $this->finish($job->id);
        $this->assertStringNotContainsString($credentials['mariadb']['password'], json_encode($job->payload));
    }

    public function test_a_suspension_stops_the_websites_and_its_end_starts_only_those(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $running = $this->website($customer, $server, $this->image());
        $stopped = $this->website($customer, $server, $this->image(), 'stopped.example', ['status' => 'stopped']);
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => true])->assertOk();
        $this->assertSame(1, AgentJob::where('action', AgentAction::SetWebsitePower->value)->count());
        $this->assertSame('stop', AgentJob::sole()->payload['power']);
        $this->finish(AgentJob::sole()->id);

        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertOk();
        $start = AgentJob::latest('id')->first();
        $this->assertSame(['start', $running->id], [$start->payload['power'], $start->website_id]);
        $this->assertSame(2, AgentJob::count());
        $this->assertSame('stopped', $stopped->fresh()->status->value);
    }

    public function test_a_gvisor_platform_change_and_container_tools_go_to_the_agent(): void
    {
        $server = $this->server(['bare_metal' => true]);
        $other = $this->server();
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/servers/{$server->id}", ['gvisorPlatform' => 'kvm'])->assertOk()
            ->assertJsonPath('gvisorPlatform', 'systrap')->assertJsonPath('job.action', 'ConfigureHost');
        $this->finish(AgentJob::sole()->id);
        $this->assertSame('kvm', $server->fresh()->gvisor_platform);

        $this->postJson("/api/v1/servers/{$server->id}/install-updates", ['policy' => 'tools'])->assertAccepted();
        $this->postJson("/api/v1/servers/{$other->id}/install-updates", ['policy' => 'tools'])->assertJsonValidationErrors('policy');
    }

    public function test_the_report_sets_the_domains_and_the_database_sizes(): void
    {
        $server = $this->server(['agent_token_hash' => hash('sha256', 'cfa_secret'), 'local' => true]);
        $website = $this->website($this->customer(), $server, $this->image());
        $db = $website->databases()->create(['engine' => 'mariadb', 'name' => $website->unix_user.'_shop']);
        $foreign = $this->website($this->customer(), $this->server(), $this->image(), 'other.example');

        $this->withToken('cfa_secret')->postJson('/api/agent/v1/report', [
            'agentVersion' => '0.1.0', 'pendingUpdates' => [['name' => 'podman', 'to' => '5.4.3', 'containerTool' => true]],
            'domains' => [
                ['name' => 'acme.example', 'dnsPointsHere' => true, 'certificate' => 'valid', 'expiresAt' => '2027-01-25T00:00:00Z'],
                ['name' => 'other.example', 'dnsPointsHere' => false, 'certificate' => 'failed'],
            ],
            'databases' => [['engine' => 'mariadb', 'name' => $db->name, 'sizeBytes' => 4096]],
        ])->assertOk();
        $domain = $website->domains()->sole();
        $this->assertSame(['valid', true, '2027-01-25'], [$domain->certificate_state, $domain->dns_points_here, $domain->certificate_expires_at->toDateString()]);
        $this->assertSame(4096, $db->fresh()->size_bytes);
        // An agent cannot change the domains of a different server.
        $this->assertSame('pending', $foreign->domains()->sole()->certificate_state);
    }

    public function test_failed_jobs_leave_the_panel_as_the_server_is(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->actingWith($this->user());

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->json('job.id');
        $this->finish($jobId, JobState::Failed);
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertJsonCount(0, 'databases')->assertJsonCount(0, 'users');

        // Batch 1, finding 3: a failed change of the PHP settings puts back the old settings, not an older image.
        $website->update(['previous_image_id' => $this->image(['name' => 'Old', 'tag' => '8.3.0-1'])->id, 'php_ini' => ['memory_limit' => '256M']]);
        $jobId = $this->putJson("/api/v1/websites/{$website->id}/runtime", ['phpIni' => ['memory_limit' => '512M'], 'documentRoot' => 'web'])->assertAccepted()->json('job.id');
        $this->assertSame('web', AgentJob::findOrFail($jobId)->payload['website']['documentRoot']);
        $image = $website->image_id;
        $this->finish($jobId, JobState::Failed);
        $website->refresh();
        $this->assertSame([$image, '256M', 'public'], [$website->image_id, $website->php_ini['memory_limit'], $website->document_root]);

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['documentRoot' => '../etc'])->assertJsonValidationErrors('documentRoot');
    }

    public function test_worker_mode_is_checked_against_the_new_image(): void
    {
        $apache = $this->image(['name' => 'Apache', 'family' => 'apache-php-fpm', 'reference' => 'ghcr.io/cloudfactory/apache-php8.4']);
        $website = $this->website($this->customer(), $this->server(), $apache);
        $franken = $this->image();
        $this->actingWith($this->user());

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $franken->id, 'workerMode' => true])->assertAccepted()->assertJsonPath('website.runtime.workerMode', true);
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $apache->id])->assertAccepted()->assertJsonPath('website.runtime.workerMode', false);
    }

    public function test_a_plan_change_reaches_the_websites_of_the_plan(): void
    {
        $plan = $this->plan();
        $website = $this->website($this->customer($plan), $this->server(), $this->image(), attributes: ['extensions' => ['opcache', 'redis']]);
        $custom = $this->website($this->customer($plan), $this->server(), $this->image(), 'custom.example', ['limits' => ['vcpus' => 8] + $plan->limits]);
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/plans/{$plan->id}", [
            'name' => 'Business', 'websites' => 10, 'mailboxes' => 10, 'databases' => 10, 'phpExtensions' => ['opcache'],
            'limits' => ['vcpus' => 4] + $plan->limits,
        ])->assertOk();
        $this->assertSame(4, $website->fresh()->limits['vcpus']);
        $this->assertSame(['opcache'], $website->fresh()->extensions);
        $this->assertSame(8, $custom->fresh()->limits['vcpus'], 'Limits that an administrator set for a website stay.');
        $this->assertSame(['SetLimits', 'SetRuntime'], AgentJob::where('website_id', $website->id)->orderBy('id')->pluck('action')->map->value->all());
    }

    public function test_a_rollout_updates_stopped_websites_and_accepts_redirects(): void
    {
        $image = $this->image(['available_tag' => '8.4.1-1', 'available_digest' => 'sha256:new']);
        $server = $this->server();
        $this->website($this->customer(), $server, $image);
        $stopped = $this->website($this->customer(), $server, $image, 'stopped.example', ['status' => 'stopped']);

        $rollout = app(RolloutEngine::class)->start($image);
        $this->assertContains($stopped->id, $rollout->website_ids);
        $this->assertSame(0, AgentJob::firstOrFail()->payload['healthCheck']['status']);
    }

    public function test_silent_servers_go_offline_and_old_jobs_go_again_or_fail(): void
    {
        $server = $this->server(['last_seen_at' => now()->subMinutes(6)]);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::Reboot, [], 'Reboot');
        $job->update(['state' => JobState::Running]);
        AgentJob::whereKey($job->id)->update(['updated_at' => now()->subMinutes(11)]);
        $old = app(JobDispatcher::class)->dispatch($server, AgentAction::InstallHostUpdates, [], 'Updates');
        AgentJob::whereKey($old->id)->update(['created_at' => now()->subHours(4)]);

        $this->artisan('cloudfactory:check-jobs')->assertSuccessful();
        $this->assertSame('offline', $server->fresh()->state);
        $this->assertNotNull($job->fresh()->resent_at);
        $this->assertSame(JobState::Failed, $old->fresh()->state);
    }

    public function test_host_updates_start_in_the_maintenance_window(): void
    {
        $server = $this->server(['update_policy' => 'security', 'maintenance_window' => '23:00-02:00', 'pending_updates' => [
            ['name' => 'openssl', 'to' => '3.5.1-2', 'security' => true],
        ]]);
        $this->server(['update_policy' => 'manual', 'pending_updates' => [['name' => 'openssl', 'to' => '3.5.1-2', 'security' => true]]]);

        Carbon::setTestNow(Carbon::parse('2026-11-02 12:00:00', 'UTC'));
        $this->artisan('cloudfactory:host-updates')->assertSuccessful();
        $this->assertSame(0, AgentJob::count());
        Carbon::setTestNow(Carbon::parse('2026-11-03 01:00:00', 'UTC'));
        $this->artisan('cloudfactory:host-updates')->assertSuccessful();
        $this->artisan('cloudfactory:host-updates')->assertSuccessful();
        $this->assertSame([$server->id], AgentJob::pluck('server_id')->all());
        Carbon::setTestNow();
    }

    public function test_sign_in_failures_count_for_the_address_of_the_client(): void
    {
        $this->user()->forceFill(['email' => 'admin@example.com'])->save();
        for ($i = 0; $i < 6; $i++) {
            $this->withHeader('X-Forwarded-For', '203.0.113.9')->postJson('/api/v1/auth/login', ['email' => 'admin@example.com', 'password' => 'wrong']);
        }
        $this->withHeader('X-Forwarded-For', '203.0.113.9')->postJson('/api/v1/auth/login', ['email' => 'admin@example.com', 'password' => 'password'])->assertStatus(429);
        // The real administrator has a different address.
        $this->withHeader('X-Forwarded-For', '198.51.100.7')->postJson('/api/v1/auth/login', ['email' => 'admin@example.com', 'password' => 'password'])->assertOk();
    }

    public function test_page_sizes_and_plans_follow_the_rules(): void
    {
        $plan = $this->plan();
        $hidden = $this->plan(['name' => 'Hidden']);
        $customer = $this->customer($plan);
        $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->getJson('/api/v1/websites?perPage=-5')->assertOk()->assertJsonPath('meta.per_page', 1);
        $this->getJson('/api/v1/plans')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.id', $plan->id);
        $this->actingWith($this->user());
        $this->getJson('/api/v1/audit?perPage=-1')->assertOk();
        $this->getJson('/api/v1/plans')->assertJsonCount(2, 'data');
        $this->assertNotNull($hidden);
    }
}
