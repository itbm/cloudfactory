<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Enums\WebsiteStatus;
use App\Models\AgentJob;
use App\Models\BackupRemoval;
use App\Models\Certificate;
use App\Models\CronJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Import;
use App\Models\MeshTunnel;
use App\Models\NetworkMember;
use App\Models\Pack;
use App\Models\Server;
use App\Models\Snapshot;
use App\Models\Website;
use App\Services\Certificates\Certificates;
use App\Services\Deploys\Deploys;
use App\Services\JobOutcome;
use App\Services\Mesh;
use App\Services\Networks;
use Tests\TestCase;

/**
 * An administrator moves a website to a different server. The files go through the tunnel
 * between the two servers. The website keeps its ID, its Unix user, its domains and its
 * databases.
 */
class WebsiteMoveTest extends TestCase
{
    private function moveServer(string $name, string $address, array $attributes = []): Server
    {
        return $this->server($attributes + ['name' => $name, 'agent_url' => "https://{$address}:7443", 'addresses' => [$address],
            'mesh_public_key' => base64_encode(hash('sha256', $name, true)),
            'mesh_networks' => true, 'mesh_websites' => true, 'mesh_moves' => true, 'database_engines' => ['mariadb', 'postgresql', 'valkey']]);
    }

    /** @return array{0: Server, 1: Server, 2: Website, 3: DatabaseServer} */
    private function setUpMove(): array
    {
        $old = $this->moveServer('web-1', '203.0.113.1');
        $new = $this->moveServer('web-2', '203.0.113.2');
        $website = $this->website($this->customer(), $old, $this->image(), 'shop.example', [
            'sftp_enabled' => true, 'sftp_keys' => ['ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtQx2bS8ZJ4Wm1bZ7zO0Zr3wq8yXc2v1q9w8mL3h4kP owner@laptop'],
        ]);
        // The database stays on the old server. The website connects to it through the
        // network "default".
        $database = $this->databaseServerOf($website, 'mariadb', ['name' => 'shop-db']);
        Database::create(['website_id' => $website->id, 'engine' => 'mariadb', 'name' => $website->unix_user.'_shop', 'database_server_id' => $database->id]);
        CronJob::create(['website_id' => $website->id, 'schedule' => '*/5 * * * *', 'command' => 'php artisan schedule:run', 'enabled' => true]);
        AgentJob::query()->update(['state' => JobState::Succeeded->value]);
        $this->actingWith($this->user());

        return [$old, $new, $website, $database];
    }

    private static function job(AgentAction $action): AgentJob
    {
        return AgentJob::where('action', $action->value)->latest('id')->firstOrFail();
    }

    public function test_a_website_moves_with_its_files_and_its_access(): void
    {
        [$old, $new, $website, $database] = $this->setUpMove();
        $address = Networks::addressFor($website);
        $zone = DnsZone::create(['customer_id' => $website->customer_id, 'name' => 'shop.example', 'serial' => 1, 'status' => DnsZone::ACTIVE]);
        $zone->records()->create(['name' => '@', 'type' => 'A', 'ttl' => 300, 'value' => '203.0.113.1', 'managed' => DnsRecord::WEBSITE]);
        $before = Snapshot::create(['website_id' => $website->id, 'kind' => 'scheduled', 'restic_id' => str_repeat('a', 64), 'size_bytes' => 1, 'databases' => [], 'mailboxes' => []]);

        $response = $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertAccepted()
            ->assertJsonPath('website.status', 'moving')
            ->assertJsonPath('website.movingToServerId', $new->id);
        $this->assertCount(2, $response->json('jobs'));

        // The new server receives first, then the old server sends. The two jobs have the
        // same token and the mesh address of the other server.
        $receive = self::job(AgentAction::ReceiveWebsite);
        $send = self::job(AgentAction::SendWebsite);
        $this->assertSame([$new->id, $old->id], [$receive->server_id, $send->server_id]);
        $this->assertLessThan($send->id, $receive->id);
        $this->assertMatchesRegularExpression('/^[0-9a-f]{64}$/', $receive->payload['token']);
        $this->assertSame($receive->payload['token'], $send->payload['token']);
        $this->assertSame($old->fresh()->mesh_address, $receive->payload['from']);
        $this->assertSame($new->fresh()->mesh_address, $send->payload['to']);
        $this->assertSame([$website->id, $website->unix_user], [$receive->payload['website']['id'], $receive->payload['website']['unixUser']]);
        $this->assertSame([false, false], [$receive->payload['stopped'], $send->payload['stopped']]);
        $this->assertTrue($receive->payload['sftpEnabled']);
        $this->assertSame($website->sftp_keys, $receive->payload['sftpKeys']);
        $this->assertSame('php artisan schedule:run', $receive->payload['cronJobs'][0]['command']);
        // The two servers have a tunnel for the move.
        $this->assertSame(1, MeshTunnel::where(fn ($q) => $q->whereIn('server_a_id', [$old->id, $new->id])->whereIn('server_b_id', [$old->id, $new->id]))->count());
        // The new server gives the website its network names when it starts it.
        $names = collect(Mesh::stateFor($new->fresh())['websites'] ?? [])->firstWhere('id', $website->id);
        $this->assertSame('shop-db.internal', $names['names'][0]['name'] ?? null);

        // While it moves, the website accepts no change. A user can read it.
        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'start'])->assertStatus(409);
        $this->deleteJson("/api/v1/websites/{$website->id}")->assertStatus(409);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertStatus(409);
        $this->getJson("/api/v1/websites/{$website->id}")->assertOk()->assertJsonPath('data.status', 'moving');
        // The database server does not move at the same time.
        $this->postJson("/api/v1/database-servers/{$database->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors('databaseServer');
        // A result of the old server does not change the status.
        app(JobOutcome::class)->finish($send, JobState::Succeeded, null, []);
        $this->assertSame(WebsiteStatus::Moving, $website->fresh()->status);
        // A push to the Git host and a scheduled check of the branch deploy after the move.
        $this->assertFalse(Deploys::allowed($website->fresh()));

        app(JobOutcome::class)->finish($receive, JobState::Succeeded, null, ['port' => 20000, 'dnsPointsHere' => true]);
        $moved = $website->fresh();
        $this->assertSame([$new->id, WebsiteStatus::Running, null, $address], [$moved->server_id, $moved->status, $moved->moving_to_server_id, $moved->mesh_address]);
        // The old server removes its copy, with no database. The record stays.
        $delete = self::job(AgentAction::DeleteWebsite);
        $this->assertSame($old->id, $delete->server_id);
        $this->assertSame([[], [], true], [$delete->payload['databases'], $delete->payload['databaseUsers'], $delete->payload['moved']]);
        app(JobOutcome::class)->finish($delete, JobState::Succeeded, null, []);
        $this->assertNotNull($website->fresh());
        $this->assertSame(1, Database::where('website_id', $website->id)->count());
        // The token is not kept.
        $this->assertArrayNotHasKey('token', $receive->fresh()->payload);
        // The DNS zone of the panel points to the new server.
        $this->assertSame(['203.0.113.2'], $zone->records()->where(['name' => '@', 'managed' => DnsRecord::WEBSITE])->pluck('value')->all());
        // The backups from before the move are in the repository of the old server. Only their
        // databases can come back, and the old server removes their files later.
        $this->assertSame($old->id, $before->fresh()->server_id);
        $removal = BackupRemoval::where('website_id', $website->id)->sole();
        $this->assertSame([$old->id, true], [$removal->server_id, $removal->files_only]);
        $this->postJson("/api/v1/websites/{$website->id}/snapshots/{$before->id}/restore", ['scope' => 'website'])
            ->assertJsonValidationErrors(['scope' => 'This backup is from the server web-1, from before the website moved. The panel can restore only a database from it.']);

        // The old server now takes the traffic of the website to its database server.
        $members = collect(Mesh::stateFor($old->fresh())['members'] ?? [])->firstWhere('id', $database->id);
        $this->assertContains($new->fresh()->mesh_address, $members['servers'] ?? []);
    }

    public function test_the_weekly_prune_removes_only_the_files_of_a_website_that_moved(): void
    {
        [$old, $new, $website] = $this->setUpMove();
        config(['cloudfactory.deleted_website_backup_days' => 0]);
        Snapshot::create(['website_id' => $website->id, 'server_id' => $old->id, 'part' => Snapshot::WEBSITE, 'kind' => 'scheduled', 'restic_id' => str_repeat('a', 64), 'size_bytes' => 1, 'databases' => [], 'mailboxes' => []]);
        $databases = Snapshot::create(['website_id' => $website->id, 'server_id' => $old->id, 'part' => Snapshot::DATABASES, 'kind' => 'scheduled-databases', 'restic_id' => str_repeat('b', 64), 'size_bytes' => 1, 'databases' => [], 'mailboxes' => []]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertAccepted();
        app(JobOutcome::class)->finish(self::job(AgentAction::ReceiveWebsite), JobState::Succeeded, null, []);

        $this->putJson('/api/v1/backup-policy', ['target' => 's3:s3.example.com/backups', 'daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '02:00'])->assertOk();
        $this->travelTo(now()->next('Sunday')->setTime(3, 0));
        $this->artisan('cloudfactory:backups')->assertSuccessful();
        $prune = AgentJob::where('action', AgentAction::PruneBackups->value)->where('server_id', $old->id)->sole();
        $this->assertSame([[], [$website->id]], [$prune->payload['forgetWebsiteIds'], $prune->payload['forgetMovedWebsiteIds']]);
        app(JobOutcome::class)->finish($prune, JobState::Succeeded, null, []);
        $this->assertSame(0, BackupRemoval::count());
        $this->assertSame([$databases->id], Snapshot::where('website_id', $website->id)->pluck('id')->all());
    }

    public function test_a_failed_move_leaves_the_website_on_the_old_server(): void
    {
        [$old, $new, $website] = $this->setUpMove();
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertAccepted();
        app(JobOutcome::class)->finish(self::job(AgentAction::ReceiveWebsite), JobState::Failed, 'the old server did not send the files', []);

        $website = $website->fresh();
        $this->assertSame([$old->id, WebsiteStatus::Running, null], [$website->server_id, $website->status, $website->moving_to_server_id]);
        // The old server starts it again with SFTP, and the new server removes what it received.
        $start = self::job(AgentAction::SetWebsitePower);
        $this->assertSame([$old->id, 'start'], [$start->server_id, $start->payload['power']]);
        $sftp = self::job(AgentAction::SetSftp);
        $this->assertSame([$old->id, true], [$sftp->server_id, $sftp->payload['enabled']]);
        $remove = self::job(AgentAction::DeleteWebsite);
        $this->assertSame([$new->id, true, []], [$remove->server_id, $remove->payload['moved'], $remove->payload['databases']]);
        app(JobOutcome::class)->finish($remove, JobState::Succeeded, null, []);
        $this->assertNotNull($website->fresh());
    }

    public function test_a_stopped_website_stays_stopped_and_a_suspension_waits_for_the_move(): void
    {
        [, $new, $website] = $this->setUpMove();
        $website->update(['status' => WebsiteStatus::Stopped]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertAccepted();
        $this->assertTrue(self::job(AgentAction::ReceiveWebsite)->payload['stopped']);
        $this->assertTrue(self::job(AgentAction::SendWebsite)->payload['stopped']);
        app(JobOutcome::class)->finish(self::job(AgentAction::ReceiveWebsite), JobState::Succeeded, null, []);
        $this->assertSame(WebsiteStatus::Stopped, $website->fresh()->status);

        // A suspension while the website moves stops it on the new server at the end.
        $website->update(['status' => WebsiteStatus::Running]);
        AgentJob::query()->update(['state' => JobState::Succeeded->value]);
        $other = $this->moveServer('web-3', '203.0.113.3');
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $other->id])->assertAccepted();
        $this->patchJson("/api/v1/customers/{$website->customer_id}", ['suspended' => true])->assertOk();
        $this->assertSame(WebsiteStatus::Moving, $website->fresh()->status);
        $this->assertSame(0, AgentJob::where('action', AgentAction::SetWebsitePower->value)->count());
        app(JobOutcome::class)->finish(self::job(AgentAction::ReceiveWebsite), JobState::Succeeded, null, []);
        $stop = self::job(AgentAction::SetWebsitePower);
        $this->assertSame([$other->id, 'stop'], [$stop->server_id, $stop->payload['power']]);
        $this->assertSame([WebsiteStatus::Stopping, true], [$website->fresh()->status, $website->fresh()->stopped_by_suspension]);
    }

    public function test_a_change_of_the_plan_while_the_website_moves_reaches_the_new_server(): void
    {
        [, $new, $website] = $this->setUpMove();
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertAccepted();
        $website->update(['limits' => [...$website->limits, 'processes' => 50]]);
        app(JobOutcome::class)->finish(self::job(AgentAction::ReceiveWebsite), JobState::Succeeded, null, []);
        $limits = self::job(AgentAction::SetLimits);
        $this->assertSame([$new->id, 50], [$limits->server_id, $limits->payload['limits']['processes']]);
        $this->assertSame(0, AgentJob::where('action', AgentAction::SetRuntime->value)->count());
    }

    public function test_the_new_server_gets_the_uploaded_certificate_of_the_website(): void
    {
        [$old, $new, $website] = $this->setUpMove();
        $certificate = Certificate::create([
            'customer_id' => $website->customer_id, 'source' => Certificate::UPLOAD, 'names' => ['shop.example'],
            'certificate' => "-----BEGIN CERTIFICATE-----\nshop.example\n-----END CERTIFICATE-----\n", 'private_key' => 'key',
            'fingerprint' => hash('sha256', 'shop.example'), 'issuer' => 'Test', 'not_before' => now()->subDay(), 'expires_at' => now()->addDays(60), 'trusted' => true,
        ]);
        $this->assertSame([], app(Certificates::class)->forServer($new));
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertAccepted();
        app(JobOutcome::class)->finish(self::job(AgentAction::ReceiveWebsite), JobState::Succeeded, null, []);

        $sync = self::job(AgentAction::SetCertificates);
        $this->assertSame($new->id, $sync->server_id);
        $this->assertSame([$certificate->id], array_map(fn (array $e) => $e['certificate']->id, app(Certificates::class)->forServer($new)));
        $this->assertSame([], app(Certificates::class)->forServer($old));
    }

    public function test_a_move_needs_a_network_to_the_database_servers_on_other_servers(): void
    {
        [, $new, $website] = $this->setUpMove();
        NetworkMember::whereNotNull('website_id')->delete();
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['serverId' => 'The database server shop-db of this website is not on the new server. Put the website and the database server in one network first.']);
        $this->assertSame(WebsiteStatus::Running, $website->fresh()->status);

        // A database server with the name of its engine has no network name.
        DatabaseServer::query()->update(['name' => 'mariadb']);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors('serverId')
            ->assertJsonFragment(['The database server mariadb of this website is not on the new server, and it has no network name. Move the database server to the new server first, or give it a different name.']);
    }

    public function test_only_an_administrator_moves_a_website_to_a_server_that_can(): void
    {
        [$old, $new, $website] = $this->setUpMove();
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $old->id])
            ->assertJsonValidationErrors(['serverId' => 'The website is on the server web-1. Select a different server.']);

        $new->update(['mesh_moves' => false]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['serverId' => 'The agent on the server web-2 cannot move websites. Update the agent of the server, then try again.']);
        $new->update(['mesh_moves' => true, 'state' => 'offline']);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors('serverId');
        $new->update(['state' => 'healthy', 'roles' => ['dns']]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors(['serverId' => 'Select a server with the web role.']);
        $new->update(['roles' => ['web']]);
        // The image of a PHP pack operates only on a server whose agent loaded the pack.
        $pack = Pack::create(['slug' => 'php-legacy-apache', 'name' => 'Older PHP for Apache + PHP-FPM', 'version' => '0.0.0', 'publisher' => 'Cloudfactory', 'manifest' => '{}']);
        $image = $website->image;
        $image->update(['pack_id' => $pack->id]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['serverId' => 'The agent on the server web-2 does not have the pack Older PHP for Apache + PHP-FPM yet. The panel sends the packs to each web server: wait for the server to load the pack, or select a different server.']);
        $new->update(['agent_packs' => [['slug' => 'php-legacy-apache', 'version' => '0.0.0']]]);

        // A job of the website that is not complete goes to the old server first.
        $pending = AgentJob::create(['server_id' => $old->id, 'website_id' => $website->id, 'action' => AgentAction::SetLimits, 'title' => 'Apply resource limits', 'payload' => [], 'steps' => [], 'state' => JobState::Queued]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['website' => 'A change of this website is in progress. Wait until it is complete.']);
        $pending->update(['state' => JobState::Succeeded]);
        // The job that makes a staging copy reads the files of the website on the old server.
        $copy = $website->replicate();
        $copy->fill(['name' => 'staging.'.$website->name, 'unix_user' => 'stagingm_1a2b', 'staging_of' => $website->id])->save();
        $making = AgentJob::create(['server_id' => $old->id, 'website_id' => $copy->id, 'action' => AgentAction::CreateWebsite, 'title' => 'Make staging copy', 'payload' => [], 'steps' => [], 'state' => JobState::Queued]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['website' => 'A change of this website is in progress. Wait until it is complete.']);
        $making->update(['state' => JobState::Failed]);
        // An import sends the next parts of its websites to the server of the import.
        $import = Import::create(['customer_id' => $website->customer_id, 'server_id' => $old->id, 'source' => Import::UPLOAD, 'file_name' => 'a.tar.gz', 'status' => Import::RUNNING]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])
            ->assertJsonValidationErrors(['website' => 'An import of this customer is in progress on this server. Wait until it is complete.']);
        $import->update(['status' => Import::DONE]);
        $website->update(['status' => WebsiteStatus::Stopping]);
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertJsonValidationErrors('website');
        $website->update(['status' => WebsiteStatus::Running]);

        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->postJson("/api/v1/websites/{$website->id}/move", ['serverId' => $new->id])->assertForbidden();
        $this->assertSame(0, AgentJob::where('action', AgentAction::ReceiveWebsite->value)->count());
        $this->assertSame(Website::find($website->id)->server_id, $old->id);
    }
}
