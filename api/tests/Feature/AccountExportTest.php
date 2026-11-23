<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\BuildAccountExport;
use App\Models\AccountExport;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\DnsZone;
use App\Models\MailDomain;
use App\Models\Server;
use App\Models\Website;
use App\Services\Exports\AccountExports;
use App\Services\Exports\TarWriter;
use App\Services\JobOutcome;
use App\Services\Plugins\Manifest;
use Illuminate\Support\Facades\Queue;
use Illuminate\Testing\TestResponse;
use Tests\TestCase;

/** Account exports (docs/account-export.md). */
class AccountExportTest extends TestCase
{
    private string $dir;

    private Customer $customer;

    private Server $web;

    private Website $website;

    protected function setUp(): void
    {
        parent::setUp();
        $this->dir = sys_get_temp_dir().'/cf-exports-'.bin2hex(random_bytes(4));
        config(['cloudfactory.exports.path' => $this->dir, 'cloudfactory.exports.reserve_bytes' => 0]);
        $this->customer = $this->customer();
        $this->web = $this->server(['name' => 'web-1', 'local' => true, 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'agent_token_hash' => hash('sha256', 'cfa_web')]);
        $this->website = $this->website($this->customer, $this->web, $this->image(), 'acme.example', ['environment' => ['APP_KEY' => 'base64:secret', 'APP_ENV' => 'production']]);
        $mariadb = $this->databaseServerOf($this->website);
        Database::create(['website_id' => $this->website->id, 'engine' => 'mariadb', 'name' => 'acme_shop', 'database_server_id' => $mariadb->id]);
        $valkey = $this->databaseServerOf($this->website, 'valkey', ['name' => 'valkey-1']);
        Database::create(['website_id' => $this->website->id, 'engine' => 'valkey', 'name' => 'cache', 'number' => 0, 'database_server_id' => $valkey->id]);
    }

    protected function tearDown(): void
    {
        if (is_dir($this->dir)) {
            exec('rm -rf '.escapeshellarg($this->dir));
        }
        parent::tearDown();
    }

    private function owner(array $scopes = ['*']): void
    {
        $this->actingWith($this->user(Role::Owner, $this->customer), $scopes);
    }

    /** A piece of a part from the agent of a server. */
    private function piece(string $token, string $partId, int $offset, string $bytes, ?string $fingerprint = null): TestResponse
    {
        return $this->call('PUT', "/api/agent/v1/export-parts/{$partId}?offset={$offset}", [], [], [], $this->transformHeadersToServerVars([
            'Authorization' => "Bearer {$token}",
            'X-Cloudfactory-Direct-Cert-Sha256' => $fingerprint ?? self::LOCAL_FINGERPRINT,
            'Accept' => 'application/json',
            'Content-Type' => 'application/octet-stream',
        ]), $bytes);
    }

    private function finish(AgentJob $job, JobState $state, array $result = []): void
    {
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'mariadb-dump: exit status 2' : null, $result);
    }

    /** Starts an export, and sends all parts of its job with this content. */
    private function completeExport(array $content): AccountExport
    {
        $this->owner();
        $id = $this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id');
        $job = AgentJob::where('action', AgentAction::ExportWebsite->value)->latest()->firstOrFail();
        $parts = [];
        foreach (AccountExport::findOrFail($id)->parts as $part) {
            $bytes = $content[$part->path];
            $this->piece('cfa_web', $part->id, 0, $bytes)->assertOk()->assertJson(['sizeBytes' => strlen($bytes)]);
            $parts[$part->id] = ['sizeBytes' => strlen($bytes), 'sha256' => hash('sha256', $bytes)];
        }
        $this->finish($job, JobState::Succeeded, ['parts' => $parts]);
        Queue::assertPushed(BuildAccountExport::class, fn (BuildAccountExport $b) => $b->exportId === $id);
        AccountExports::build(AccountExport::findOrFail($id));

        return AccountExport::findOrFail($id);
    }

    /**
     * Reads a tar archive: path => content. It reads the extended headers ("x") for long paths.
     *
     * @return array<string, string>
     */
    private static function untar(string $tar): array
    {
        $files = [];
        $offset = 0;
        $pax = [];
        while ($offset + 512 <= strlen($tar)) {
            $header = substr($tar, $offset, 512);
            if ($header === str_repeat("\0", 512)) {
                break;
            }
            $sum = array_sum(array_map('ord', str_split(substr_replace($header, '        ', 148, 8))));
            self::assertSame($sum, octdec(trim(substr($header, 148, 8), "\0 ")), 'The checksum of a header is wrong.');
            $name = rtrim(substr($header, 0, 100), "\0");
            $size = octdec(trim(substr($header, 124, 12), "\0 "));
            $type = $header[156];
            $data = substr($tar, $offset + 512, $size);
            $offset += 512 + (int) (ceil($size / 512) * 512);
            if ($type === 'x') {
                preg_match_all('/\d+ (\w+)=(.*)\n/U', $data, $m, PREG_SET_ORDER);
                $pax = array_column($m, 2, 1);

                continue;
            }
            $files[$pax['path'] ?? $name] = $data;
            $pax = [];
        }

        return $files;
    }

    public function test_an_owner_exports_the_account(): void
    {
        $zone = DnsZone::create(['customer_id' => $this->customer->id, 'name' => 'acme.example', 'serial' => 2026112201, 'status' => DnsZone::ACTIVE]);
        $zone->records()->create(['name' => '@', 'type' => 'NS', 'ttl' => 3600, 'value' => 'ns1.hosting.example']);
        $zone->records()->create(['name' => 'www', 'type' => 'A', 'ttl' => 300, 'value' => '203.0.113.10']);
        MailDomain::create(['customer_id' => $this->customer->id, 'server_id' => $this->web->id, 'name' => 'acme.example', 'status' => MailDomain::ACTIVE]);
        $this->owner();

        $made = $this->postJson('/api/v1/exports', ['files' => true, 'databases' => true])->assertStatus(202);
        $made->assertJsonPath('state', 'running')->assertJsonPath('parts.total', 2)->assertJsonPath('parts.complete', 0);
        $export = AccountExport::findOrFail($made->json('id'));

        // One job for the website, with the files and the MariaDB dump. No Valkey data.
        $job = AgentJob::where('action', AgentAction::ExportWebsite->value)->sole();
        $this->assertSame($this->web->id, $job->server_id);
        $this->assertSame($this->website->id, $job->website_id);
        $this->assertSame($this->website->unix_user, $job->payload['unixUser']);
        $files = $export->parts->firstWhere('path', 'websites/acme.example/files.tar.gz');
        $dump = $export->parts->firstWhere('path', 'websites/acme.example/databases/mariadb-acme_shop.sql.gz');
        $this->assertSame($files->id, $job->payload['filesPart']);
        $this->assertSame([['engine' => 'mariadb', 'name' => 'acme_shop', 'databaseServer' => DatabaseServer::where('engine', 'mariadb')->value('id'), 'part' => $dump->id]], $job->payload['databases']);

        // The agent sends the files in two pieces. A piece that it sends again is not added two times.
        $this->piece('cfa_web', $files->id, 0, 'files-')->assertOk()->assertExactJson(['sizeBytes' => 6]);
        $this->piece('cfa_web', $files->id, 6, 'archive')->assertOk()->assertExactJson(['sizeBytes' => 13]);
        $this->piece('cfa_web', $files->id, 6, 'archive')->assertStatus(409)->assertJsonPath('sizeBytes', 13);
        $this->piece('cfa_web', $files->id, 2, 'xx')->assertStatus(409)->assertJsonPath('sizeBytes', 13);
        $this->piece('cfa_web', $dump->id, 0, 'dump')->assertOk();
        $this->getJson("/api/v1/exports/{$export->id}")->assertJsonPath('receivedBytes', 17);

        $this->finish($job, JobState::Succeeded, ['parts' => [
            $files->id => ['sizeBytes' => 13, 'sha256' => hash('sha256', 'files-archive')],
            $dump->id => ['sizeBytes' => 4, 'sha256' => hash('sha256', 'dump')],
        ]]);
        Queue::assertPushed(BuildAccountExport::class);
        (new BuildAccountExport($export->id))->handle();

        $shown = $this->getJson("/api/v1/exports/{$export->id}")->assertOk();
        $shown->assertJsonPath('state', 'ready')->assertJsonPath('parts.complete', 2)->assertJsonPath('customerName', 'Acme Ltd');
        $this->assertNotNull($shown->json('expiresAt'));
        $this->assertContains('mail', array_column($shown->json('warnings'), 'item'));
        $this->assertContains('websites/acme.example/databases/cache', array_column($shown->json('warnings'), 'item'));

        $link = $this->postJson("/api/v1/exports/{$export->id}/download-link")->assertOk();
        $this->assertStringStartsWith('/api/v1/export-downloads/'.$export->id.'?expires=', $link->json('url'));
        $this->assertMatchesRegularExpression('/^acme-ltd-export-\d{8}-\d{4}\.tar$/', $link->json('fileName'));
        $download = $this->get($link->json('url'))->assertOk()->assertHeader('Content-Type', 'application/x-tar')->assertHeader('Accept-Ranges', 'bytes');
        $tar = $download->streamedContent();
        $this->assertSame($shown->json('sizeBytes') ?? AccountExport::find($export->id)->size_bytes, strlen($tar));
        $this->assertSame(0, strlen($tar) % 512);

        $members = self::untar($tar);
        $this->assertSame(['manifest.json', 'account.json', 'README.txt', 'dns/acme.example.zone', 'websites/acme.example/databases/mariadb-acme_shop.sql.gz', 'websites/acme.example/files.tar.gz'], array_keys($members));
        $this->assertSame('files-archive', $members['websites/acme.example/files.tar.gz']);
        $manifest = json_decode($members['manifest.json'], true);
        $this->assertSame('cf-export/1', $manifest['format']);
        foreach ($manifest['files'] as $file) {
            $this->assertSame($file['sha256'], hash('sha256', $members[$file['path']]), $file['path']);
            $this->assertSame($file['bytes'], strlen($members[$file['path']]));
        }
        $account = json_decode($members['account.json'], true);
        $this->assertSame('cf-account/1', $account['format']);
        $this->assertSame(['APP_ENV' => 'production', 'APP_KEY' => 'base64:secret'], $account['websites'][0]['environment']);
        $this->assertSame(['path' => 'websites/acme.example/files.tar.gz', 'bytes' => 13, 'exclude' => []], $account['websites'][0]['content']['files']);
        $this->assertSame(4, $account['websites'][0]['databases'][0]['content']['bytes']);
        $this->assertNull($account['websites'][0]['databases'][1]['content'], 'a Valkey database has no data in the export');
        $this->assertSame(strlen($members['dns/acme.example.zone']), $account['dnsZones'][0]['content']['bytes']);
        $this->assertStringContainsString("@ 3600 IN SOA ns1.hosting.example. hostmaster.acme.example. 2026112201 3600 900 1209600 300\n", $members['dns/acme.example.zone']);
        $this->assertStringContainsString("\nwww 300 IN A 203.0.113.10\n", $members['dns/acme.example.zone']);
        // No key has a password or a private key.
        $this->assertDoesNotMatchRegularExpression('/"\w*(password|privateKey)\w*":/i', $members['account.json']);
        // An empty map stays a JSON object.
        $this->assertStringContainsString('"phpIni": {}', $members['account.json']);

        // A download continues at an offset.
        $this->get($link->json('url'), ['Range' => 'bytes=1000-'])->assertStatus(206)
            ->assertHeader('Content-Range', 'bytes 1000-'.(strlen($tar) - 1).'/'.strlen($tar))
            ->assertStreamedContent(substr($tar, 1000));
        $this->get($link->json('url'), ['Range' => 'bytes=-10'])->assertStatus(206)->assertStreamedContent(substr($tar, -10));
        $this->get($link->json('url'), ['Range' => 'bytes='.strlen($tar).'-'])->assertStatus(416);
        // The link has a signature.
        $this->get(str_replace('signature=', 'signature=0', $link->json('url')))->assertForbidden();
        $this->get('/api/v1/export-downloads/'.$export->id)->assertForbidden();

        $this->assertDatabaseHas('audit_entries', ['action' => 'Started account export', 'target' => 'Acme Ltd']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Downloaded account export', 'target' => 'Acme Ltd']);
    }

    public function test_the_databases_on_a_different_server_have_their_own_job(): void
    {
        $data = $this->server(['name' => 'db-1', 'agent_token_hash' => hash('sha256', 'cfa_data'), 'certificate_fingerprint' => str_repeat('ab', 32)]);
        $remote = DatabaseServer::create(['customer_id' => $this->customer->id, 'server_id' => $data->id, 'plan_id' => $this->website->plan_id, 'engine' => 'postgresql',
            'name' => 'pg-1', 'port' => 5433, 'status' => DatabaseServer::RUNNING, 'limits' => ['vcpus' => 1, 'memoryBytes' => 1024 ** 3, 'diskBytes' => 5 * 1024 ** 3]]);
        Database::create(['website_id' => $this->website->id, 'engine' => 'postgresql', 'name' => 'acme_app', 'database_server_id' => $remote->id]);
        $this->owner();

        $export = AccountExport::findOrFail($this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id'));
        $job = AgentJob::where('action', AgentAction::ExportWebsite->value)->where('server_id', $data->id)->sole();
        $this->assertTrue($job->payload['databasesOnly']);
        $this->assertSame($this->customer->id, $job->payload['customer']);
        $part = $export->parts->firstWhere('path', 'websites/acme.example/databases/postgresql-acme_app.pgdump');
        $this->assertSame($part->id, $job->payload['databases'][0]['part']);
        $this->assertSame($data->id, $part->server_id);

        // The agent of a different server cannot send the part.
        $this->piece('cfa_web', $part->id, 0, 'PGDMP')->assertNotFound();
    }

    public function test_an_export_fails_when_a_job_fails_or_a_part_is_not_complete(): void
    {
        $this->owner();
        $export = AccountExport::findOrFail($this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id'));
        $job = AgentJob::where('action', AgentAction::ExportWebsite->value)->sole();
        $this->finish($job, JobState::Failed);
        $this->assertSame(AccountExport::FAILED, $export->fresh()->state);
        $this->assertStringContainsString('mariadb-dump: exit status 2', $export->fresh()->error);
        // The job is complete: the agent cannot send more.
        $this->piece('cfa_web', $export->parts->first()->id, 0, 'late')->assertStatus(410);

        // A new export: the result names more bytes than the panel has.
        $export = AccountExport::findOrFail($this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id'));
        $job = AgentJob::where('action', AgentAction::ExportWebsite->value)->latest('created_at')->latest('id')->first();
        $parts = $export->parts->mapWithKeys(fn ($p) => [$p->id => ['sizeBytes' => 10, 'sha256' => str_repeat('a', 64)]])->all();
        foreach ($export->parts as $p) {
            $this->piece('cfa_web', $p->id, 0, 'short')->assertOk();
        }
        $this->finish($job, JobState::Succeeded, ['parts' => $parts]);
        $this->assertSame(AccountExport::FAILED, $export->fresh()->state);
        $this->assertStringContainsString('did not get all of', $export->fresh()->error);
        $this->assertDirectoryDoesNotExist($this->dir.'/'.$export->id);
        Queue::assertNotPushed(BuildAccountExport::class);
    }

    public function test_an_account_with_no_websites_completes_at_once(): void
    {
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $this->actingWith($this->user(Role::Owner, $other));
        $id = $this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id');
        Queue::assertPushed(BuildAccountExport::class, fn (BuildAccountExport $b) => $b->exportId === $id);
        AccountExports::build(AccountExport::findOrFail($id));
        $export = AccountExport::findOrFail($id);
        $this->assertSame(AccountExport::READY, $export->state);
        $members = self::untar(stream_get_contents(self::stream($export)));
        $this->assertSame(['manifest.json', 'account.json', 'README.txt'], array_keys($members));
        $this->assertSame([], json_decode($members['account.json'], true)['websites']);
    }

    /** @return resource */
    private static function stream(AccountExport $export)
    {
        $out = fopen('php://memory', 'w+b');
        $archive = AccountExports::archive($export);
        $archive->send($out, 0, $archive->size() - 1);
        rewind($out);

        return $out;
    }

    public function test_an_export_without_files_and_databases_has_the_description_only(): void
    {
        $this->owner();
        $id = $this->postJson('/api/v1/exports', ['files' => false, 'databases' => false])->assertStatus(202)->json('id');
        $this->assertSame(0, AgentJob::where('action', AgentAction::ExportWebsite->value)->count());
        AccountExports::build(AccountExport::findOrFail($id));
        $account = json_decode(self::untar(stream_get_contents(self::stream(AccountExport::findOrFail($id))))['account.json'], true);
        $this->assertNull($account['websites'][0]['content']['files']);
        $this->assertNull($account['websites'][0]['databases'][0]['content']);
    }

    public function test_who_can_export_an_account(): void
    {
        // A token needs the scope "exports".
        $this->owner(['websites:read', 'websites:write']);
        $this->postJson('/api/v1/exports', [])->assertForbidden();
        $this->getJson('/api/v1/exports')->assertForbidden();

        // An owner cannot export a different account.
        $other = $this->customer(null, ['name' => 'Bolt', 'email' => 'it@bolt.example']);
        $this->owner();
        $this->postJson('/api/v1/exports', ['customerId' => $other->id])->assertNotFound();

        // An administrator chooses the customer.
        $this->actingWith($this->user());
        $this->postJson('/api/v1/exports', [])->assertStatus(422)->assertJsonValidationErrors('customerId');
        $id = $this->postJson('/api/v1/exports', ['customerId' => $this->customer->id])->assertStatus(202)->json('id');
        // One export of an account at a time.
        $this->postJson('/api/v1/exports', ['customerId' => $this->customer->id])->assertStatus(422)->assertJsonValidationErrors('export');

        // A reseller exports the account of its customer. The owner of a different account does not see the export.
        $reseller = $this->customer(null, ['name' => 'Hosting Co', 'email' => 'it@hosting.example', 'role' => 'reseller']);
        $client = $this->customer(null, ['name' => 'Client', 'email' => 'it@client.example', 'reseller_id' => $reseller->id]);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->postJson('/api/v1/exports', ['customerId' => $client->id])->assertStatus(202);
        $this->getJson("/api/v1/exports/{$id}")->assertNotFound();
        $this->assertCount(1, $this->getJson('/api/v1/exports')->assertOk()->json('data'));

        // A plugin cannot have the scope.
        $this->assertContains('exports:read', Manifest::FORBIDDEN_SCOPES);
        $this->assertContains('exports:write', Manifest::FORBIDDEN_SCOPES);
    }

    public function test_the_panel_refuses_an_export_when_its_disk_is_full(): void
    {
        $this->owner();
        $id = $this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id');
        config(['cloudfactory.exports.reserve_bytes' => PHP_INT_MAX]);
        $part = AccountExport::findOrFail($id)->parts->first();
        $this->piece('cfa_web', $part->id, 0, 'bytes')->assertStatus(507);
        AccountExports::remove(AccountExport::findOrFail($id));
        $this->postJson('/api/v1/exports', [])->assertStatus(422)->assertJsonValidationErrors('export');
    }

    public function test_a_piece_larger_than_the_limit_is_refused(): void
    {
        $this->owner();
        $part = AccountExport::findOrFail($this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id'))->parts->first();
        $this->piece('cfa_web', $part->id, 0, str_repeat('x', AccountExports::CHUNK_BYTES + 1))->assertStatus(413);
        $this->piece('cfa_web', $part->id, 0, str_repeat('x', AccountExports::CHUNK_BYTES))->assertOk();
    }

    public function test_a_user_removes_an_export_and_the_panel_removes_expired_exports(): void
    {
        $content = ['websites/acme.example/files.tar.gz' => 'files', 'websites/acme.example/databases/mariadb-acme_shop.sql.gz' => 'dump'];
        $export = $this->completeExport($content);
        $this->assertDirectoryExists($this->dir.'/'.$export->id);

        // Expired exports go.
        $this->travel(8)->days();
        $this->assertSame(1, AccountExports::expire());
        $this->assertDatabaseMissing('account_exports', ['id' => $export->id]);
        $this->assertDirectoryDoesNotExist($this->dir.'/'.$export->id);
        $this->travelBack();

        // A user removes an export in progress. The agent then cannot send its parts.
        $id = $this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id');
        $part = AccountExport::findOrFail($id)->parts->first();
        $this->deleteJson("/api/v1/exports/{$id}")->assertNoContent();
        $this->piece('cfa_web', $part->id, 0, 'late')->assertNotFound();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Removed account export']);

        // An export that is not complete after a day fails, when no job is open.
        $id = $this->postJson('/api/v1/exports', [])->assertStatus(202)->json('id');
        AgentJob::where('action', AgentAction::ExportWebsite->value)->update(['state' => JobState::Failed->value]);
        $this->travel(25)->hours();
        $this->assertSame(1, AccountExports::expire());
        $this->assertSame(AccountExport::FAILED, AccountExport::findOrFail($id)->state);
    }

    public function test_the_panel_keeps_the_newest_exports(): void
    {
        config(['cloudfactory.exports.keep' => 2]);
        $content = ['websites/acme.example/files.tar.gz' => 'files', 'websites/acme.example/databases/mariadb-acme_shop.sql.gz' => 'dump'];
        $first = $this->completeExport($content);
        $this->travel(1)->minutes();
        $second = $this->completeExport($content);
        $this->travel(1)->minutes();
        $third = $this->completeExport($content);
        $this->assertDatabaseMissing('account_exports', ['id' => $first->id]);
        $this->assertDirectoryDoesNotExist($this->dir.'/'.$first->id);
        $this->assertSame([$third->id, $second->id], array_column($this->getJson('/api/v1/exports')->json('data'), 'id'));
    }

    public function test_a_long_path_gets_an_extended_header(): void
    {
        $name = str_repeat('a', 60).'.example';
        $long = 'websites/'.$name.'/databases/mariadb-'.str_repeat('b', 40).'.sql.gz';
        $out = fopen('php://memory', 'w+b');
        (new TarWriter($out))->addString($long, 'x', 0);
        fwrite($out, TarWriter::end());
        rewind($out);
        $this->assertSame([$long => 'x'], self::untar(stream_get_contents($out)));
    }
}
