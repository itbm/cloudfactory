<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Services\JobOutcome;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

/** Databases, backups, customers and the audit log. */
class DataTest extends TestCase
{
    public function test_a_database_password_is_shown_once_and_not_kept(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user());

        $response = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $password = $response->json('password');
        $this->assertSame(24, strlen($password));
        $this->assertStringNotContainsString($password, (string) DB::table('agent_jobs')->value('payload'));

        $job = AgentJob::findOrFail($response->json('job.id'));
        app(JobOutcome::class)->finish($job, JobState::Succeeded);
        $this->assertArrayNotHasKey('password', $job->fresh()->payload);

        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertJsonValidationErrors('name');
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertJsonPath('users.0.access', 'website');
    }

    public function test_a_finished_backup_makes_a_snapshot_that_can_be_restored(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user());

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/snapshots")->assertAccepted()->json('job.id');
        app(JobOutcome::class)->finish(AgentJob::findOrFail($jobId), JobState::Succeeded, result: ['snapshotId' => 'abc123', 'sizeBytes' => 1024]);

        $snapshot = $this->getJson("/api/v1/websites/{$website->id}/snapshots")->assertJsonCount(1, 'data')->json('data.0.id');
        $this->postJson("/api/v1/websites/{$website->id}/snapshots/{$snapshot}/restore", ['scope' => 'file', 'item' => '/../etc/passwd'])->assertJsonValidationErrors('item');
        $this->postJson("/api/v1/websites/{$website->id}/snapshots/{$snapshot}/restore", ['scope' => 'file', 'item' => '/public/index.php'])->assertAccepted()->assertJsonPath('job.action', 'Restore');
    }

    public function test_the_backup_policy_needs_an_object_storage_target(): void
    {
        $this->actingWith($this->user());

        $this->putJson('/api/v1/backup-policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => '/var/backups'])->assertJsonValidationErrors('target');
        $this->putJson('/api/v1/backup-policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3://backups/cloudfactory'])->assertOk();
        $this->getJson('/api/v1/backup-policy')->assertJsonPath('target', 's3://backups/cloudfactory');
    }

    public function test_a_reseller_adds_owners_but_not_resellers(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['name' => 'Agency', 'role' => 'reseller']);
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $this->postJson('/api/v1/customers', ['name' => 'Bakery', 'email' => 'b@example.com', 'role' => 'reseller', 'planId' => $plan->id])->assertJsonValidationErrors('role');
        $this->postJson('/api/v1/customers', ['name' => 'Bakery', 'email' => 'b@example.com', 'role' => 'owner', 'planId' => $plan->id])
            ->assertCreated()->assertJsonPath('resellerId', $reseller->id);
    }

    public function test_the_audit_log_records_who_did_what(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $user = $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'restart']);

        $this->getJson('/api/v1/audit')->assertOk()
            ->assertJsonPath('data.0.action', 'Restart website')
            ->assertJsonPath('data.0.target', 'acme.example')
            ->assertJsonPath('data.0.actor', $user->name);
    }
}
