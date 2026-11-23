<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Mailbox;
use App\Models\MailboxSnapshot;
use App\Models\MailCluster;
use App\Models\MailDomain;
use App\Models\Server;
use App\Models\Setting;
use App\Services\JobOutcome;
use Illuminate\Support\Carbon;
use Tests\TestCase;

/**
 * The backups of a mailbox: a restic snapshot of all its folders and messages, in the
 * repository of the server of the mail domain. A restore adds the messages that the mailbox
 * does not have.
 */
class MailBackupTest extends TestCase
{
    private Customer $customer;

    private Server $host;

    protected function setUp(): void
    {
        parent::setUp();
        Setting::put('backup_policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3:s3.example.com/bucket']);
    }

    private function mailbox(string $local = 'info', string $status = MailDomain::ACTIVE, ?Customer $customer = null): Mailbox
    {
        $this->customer ??= $this->customer();
        $customer ??= $this->customer;
        // A server with only the email role has a repository too.
        $this->host ??= $this->server(['roles' => ['email'], 'mail_hostname' => 'mail.host.example', 'mail_status' => 'running']);
        $domain = MailDomain::firstOrCreate(['customer_id' => $customer->id, 'name' => $customer->is($this->customer) ? 'acme.example' : 'other.example'], ['server_id' => $this->host->id, 'status' => MailDomain::ACTIVE, 'records' => []]);

        return Mailbox::create(['customer_id' => $customer->id, 'mail_domain_id' => $domain->id, 'local_part' => $local, 'quota_bytes' => 1024 ** 3, 'status' => $status]);
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);

        return $job->fresh();
    }

    private function snapshot(Mailbox $mailbox, string $kind = 'scheduled', ?string $serverId = null): MailboxSnapshot
    {
        return MailboxSnapshot::create(['mailbox_id' => $mailbox->id, 'address' => $mailbox->address(), 'server_id' => $serverId ?? $this->host->id, 'kind' => $kind,
            'restic_id' => str_repeat('ab', 32), 'size_bytes' => 2048, 'messages' => 12]);
    }

    public function test_an_owner_backs_up_a_mailbox_and_lists_its_backups(): void
    {
        $mailbox = $this->mailbox();
        $this->actingWith($this->user(Role::Owner, $this->customer));

        $id = $this->postJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertAccepted()->json('job.id');
        $job = AgentJob::findOrFail($id);
        $this->assertSame(AgentAction::BackupMailbox, $job->action);
        $this->assertSame($this->host->id, $job->server_id);
        $this->assertSame($this->customer->id, $job->customer_id);
        $this->assertSame(['mailboxId' => $mailbox->id, 'domain' => 'acme.example', 'localPart' => 'info', 'kind' => 'manual',
            'target' => "s3:s3.example.com/bucket/servers/{$this->host->id}", 'retention' => ['last' => 10]], $job->payload);
        $this->assertSame(['Export and upload the messages', 'Apply retention'], $job->steps);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Backed up mailbox', 'target' => 'info@acme.example']);
        // One backup or restore of a mailbox at a time. The removal of the mailbox waits too.
        $this->postJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertJsonValidationErrors('mailboxId');
        $this->deleteJson("/api/v1/mail/mailboxes/{$mailbox->id}")->assertJsonValidationErrors('mailboxId');

        $old = $this->snapshot($mailbox, 'manual');
        $old->update(['restic_id' => str_repeat('cd', 32)]);
        $this->finish($id, result: ['snapshotId' => str_repeat('ef', 32), 'sizeBytes' => 3000797, 'messages' => 6, 'forgotten' => [str_repeat('cd', 32)]]);
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertOk()->assertJsonCount(1, 'data')
            ->assertJsonPath('data.0.kind', 'manual')->assertJsonPath('data.0.sizeBytes', 3000797)->assertJsonPath('data.0.messages', 6)
            ->assertJsonPath('data.0.serverId', $this->host->id);
        $this->assertModelMissing($old);

        // A different customer does not see the mailbox.
        $other = $this->customer(null, ['name' => 'Other Ltd', 'email' => 'it@other.example']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertNotFound();
        $this->postJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertNotFound();
    }

    public function test_a_backup_needs_a_target_and_a_mailbox_on_the_mail_server(): void
    {
        $pending = $this->mailbox('new', MailDomain::PENDING);
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson("/api/v1/mail/mailboxes/{$pending->id}/backups")->assertJsonValidationErrors(['mailboxId' => 'Wait until it is complete']);
        $removing = $this->mailbox('old', MailDomain::REMOVING);
        $this->postJson("/api/v1/mail/mailboxes/{$removing->id}/backups")->assertJsonValidationErrors(['mailboxId' => 'The panel removes this mailbox.']);

        Setting::put('backup_policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => '']);
        $mailbox = $this->mailbox();
        $this->postJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertJsonValidationErrors('target');
        $this->assertSame(0, AgentJob::count());
    }

    public function test_a_restore_adds_the_messages_of_a_backup(): void
    {
        $mailbox = $this->mailbox();
        $snapshot = $this->snapshot($mailbox);
        $user = $this->user(Role::Owner, $this->customer);
        $url = "/api/v1/mail/mailboxes/{$mailbox->id}/backups/{$snapshot->id}/restore";

        // A restore changes the mailbox: a token that can only change the backups cannot do it.
        $this->actingWith($user, ['backups:write']);
        $this->postJson($url)->assertForbidden();
        $this->actingWith($user, ['backups:write', 'mail:write']);
        $id = $this->postJson($url)->assertAccepted()->json('job.id');
        $job = AgentJob::findOrFail($id);
        $this->assertSame(AgentAction::RestoreMailbox, $job->action);
        $this->assertSame(['mailboxId' => $mailbox->id, 'domain' => 'acme.example', 'localPart' => 'info', 'backupId' => $snapshot->id, 'snapshotId' => str_repeat('ab', 32),
            'target' => "s3:s3.example.com/bucket/servers/{$this->host->id}"], $job->payload);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Restored mailbox from backup']);
        $this->postJson($url)->assertJsonValidationErrors('mailboxId');
        Carbon::setTestNow(Carbon::parse('2026-11-22 14:03:00', 'UTC'));
        $this->finish($id, result: ['added' => 3, 'present' => 9, 'tooLarge' => 1, 'failed' => 0, 'firstError' => '']);
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups")->assertJsonPath('data.0.lastRestore', ['at' => '2026-11-22T14:03:00+00:00', 'added' => 3, 'present' => 9, 'tooLarge' => 1, 'failed' => 0]);

        // The backup of a different mailbox is not a backup of this mailbox.
        $sales = $this->mailbox('sales');
        $this->postJson("/api/v1/mail/mailboxes/{$sales->id}/backups/{$snapshot->id}/restore")->assertNotFound();
        // A backup in the repository of a server that does not have the mailbox.
        $elsewhere = $this->snapshot($mailbox, 'manual', $this->server()->id);
        $this->postJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups/{$elsewhere->id}/restore")->assertJsonValidationErrors('snapshot');
    }

    public function test_a_member_of_the_mail_cluster_restores_a_backup_in_its_repository(): void
    {
        $mailbox = $this->mailbox();
        $cluster = MailCluster::create(['name' => 'eu', 'hostname' => 'mail.eu.example', 'network' => '10.77.16', 'status' => 'running', 'phase' => 'done', 'secrets' => []]);
        $member = $this->server(['roles' => ['email'], 'mail_cluster_id' => $cluster->id]);
        $this->host->update(['mail_cluster_id' => $cluster->id]);
        $snapshot = $this->snapshot($mailbox, 'scheduled', $member->id);
        $this->actingWith($this->user());

        $id = $this->postJson("/api/v1/mail/mailboxes/{$mailbox->id}/backups/{$snapshot->id}/restore")->assertAccepted()->json('job.id');
        $this->assertSame($member->id, AgentJob::findOrFail($id)->server_id);
        $this->assertSame("s3:s3.example.com/bucket/servers/{$member->id}", AgentJob::findOrFail($id)->payload['target']);
    }

    public function test_the_nightly_job_backs_up_each_mailbox(): void
    {
        $info = $this->mailbox();
        $sales = $this->mailbox('sales');
        $this->mailbox('new', MailDomain::PENDING);
        // A backup of sales from the night before is still open.
        $this->actingWith($this->user());
        $this->postJson("/api/v1/mail/mailboxes/{$sales->id}/backups")->assertAccepted();

        $this->artisan('cloudfactory:backups', ['--now' => true])->expectsOutputToContain('Started 1 mailbox backups.')->assertSuccessful();
        $job = AgentJob::where('action', AgentAction::BackupMailbox->value)->latest('id')->firstOrFail();
        $this->assertSame($info->id, $job->payload['mailboxId']);
        $this->assertSame('scheduled', $job->payload['kind']);
        $this->assertSame(['daily' => 7, 'weekly' => 4, 'monthly' => 6], $job->payload['retention']);
        $this->assertSame('Nightly backup of mailbox info@acme.example', $job->title);
    }

    public function test_the_backups_of_a_removed_mailbox_stay_for_a_set_time(): void
    {
        config(['cloudfactory.deleted_mailbox_backup_days' => 30]);
        $mailbox = $this->mailbox();
        $snapshot = $this->snapshot($mailbox);
        $this->actingWith($this->user());
        // Sunday 1 November 2026.
        Carbon::setTestNow(Carbon::parse('2026-11-01 02:00:00', 'UTC'));
        $this->finish($this->deleteJson("/api/v1/mail/mailboxes/{$mailbox->id}")->assertAccepted()->json('job.id'));
        $this->assertModelMissing($mailbox);
        $this->assertEquals(Carbon::parse('2026-12-01 02:00:00', 'UTC'), $snapshot->fresh()->remove_after);

        // In the first 30 days, the weekly job keeps the backups.
        Carbon::setTestNow(Carbon::parse('2026-11-08 03:00:00', 'UTC'));
        $this->artisan('cloudfactory:backups')->assertSuccessful();
        $prune = AgentJob::where('action', AgentAction::PruneBackups->value)->where('server_id', $this->host->id)->sole();
        $this->assertSame([], $prune->payload['forgetMailboxIds']);
        $this->finish($prune->id);
        $this->assertModelExists($snapshot);

        // After 30 days, the job removes them, and then the record.
        Carbon::setTestNow(Carbon::parse('2026-12-06 03:00:00', 'UTC'));
        $this->artisan('cloudfactory:backups')->assertSuccessful();
        $prune = AgentJob::where('action', AgentAction::PruneBackups->value)->where('server_id', $this->host->id)->latest('id')->firstOrFail();
        $this->assertSame([$mailbox->id], $prune->payload['forgetMailboxIds']);
        $this->finish($prune->id, JobState::Failed);
        $this->assertModelExists($snapshot);
        Carbon::setTestNow(Carbon::parse('2026-12-13 03:00:00', 'UTC'));
        $this->artisan('cloudfactory:backups')->assertSuccessful();
        $this->finish(AgentJob::where('action', AgentAction::PruneBackups->value)->where('server_id', $this->host->id)->latest('id')->firstOrFail()->id);
        $this->assertModelMissing($snapshot);
    }

    public function test_an_administrator_sets_the_storage_key_of_a_mail_server(): void
    {
        $this->mailbox();
        $this->actingWith($this->user());
        $this->getJson('/api/v1/backup-policy')->assertOk()->assertJsonPath('servers.0.serverId', $this->host->id);
        $this->putJson("/api/v1/backup-policy/servers/{$this->host->id}/credentials", ['credentials' => ['AWS_ACCESS_KEY_ID' => 'AKIA1', 'AWS_SECRET_ACCESS_KEY' => 's3cret']])->assertOk();
    }
}
