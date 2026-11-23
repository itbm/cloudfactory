<?php

namespace Tests\Feature;

use App\Demo\DemoImport;
use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\CronJob;
use App\Models\Customer;
use App\Models\DnsZone;
use App\Models\Image;
use App\Models\Import;
use App\Models\Mailbox;
use App\Models\Server;
use App\Models\Website;
use App\Services\Imports\Description;
use App\Services\Imports\Runner;
use App\Services\JobOutcome;
use Illuminate\Support\Facades\Storage;
use Illuminate\Testing\TestResponse;
use Tests\TestCase;

/**
 * The import of an account from cPanel or DirectAdmin (docs/imports.md). The agent reads the
 * archive. The tests give its results to the panel: the panel makes the plan, then the records
 * with the checks of the API, and then sends the content jobs when their records are complete.
 */
class ImportTest extends TestCase
{
    private Customer $customer;

    private Server $host;

    /** @var array<string, Image> */
    private array $images = [];

    protected function setUp(): void
    {
        parent::setUp();
        Storage::fake('local');
        $this->customer = $this->customer($this->plan(['websites' => 20, 'databases' => 20, 'mailboxes' => 20]), ['name' => 'Corner Bakery']);
        $this->host = $this->server([
            'name' => 'web-1', 'roles' => ['web', 'email'], 'mail_hostname' => 'mail.host.example', 'mail_status' => 'running', 'agent_features' => ['imports'],
            'database_engines' => ['mariadb'], 'addresses' => ['203.0.113.10'], 'agent_token_hash' => hash('sha256', 'cfa_secret'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'local' => true,
        ]);
        $this->dnsServer();
        foreach (['8.4', '8.5'] as $v) {
            $this->images["apache-{$v}"] = $this->image(['name' => "Apache + PHP-FPM · PHP {$v}", 'family' => 'apache-php-fpm', 'php_version' => $v, 'reference' => "ghcr.io/cloudfactory/apache-php{$v}"]);
        }
        $this->images['franken-8.4'] = $this->image();
        $this->actingWith($this->user());
    }

    /** Finishes a job as the agent does. */
    private function finish(AgentJob|string $job, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = $job instanceof AgentJob ? $job->fresh() : AgentJob::findOrFail($job);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'the agent failed' : null, $result);

        return $job->fresh();
    }

    /** An import of the sample account, ready to start. */
    private function ready(string $name = 'cpmove-bakery.tar.gz'): Import
    {
        $id = $this->postJson('/api/v1/imports', ['customerId' => $this->customer->id, 'serverId' => $this->host->id, 'url' => "https://files.example/{$name}"])
            ->assertCreated()->assertJsonPath('import.status', Import::FETCHING)->json('import.id');
        $fetch = AgentJob::where(['import_id' => $id, 'action' => AgentAction::FetchImport->value])->sole();
        $this->finish($fetch, result: ['sha256' => str_repeat('ab', 32), 'bytes' => 1234, 'account' => DemoImport::account($name)]);

        return Import::findOrFail($id);
    }

    /** The queued jobs of an import, of one action. */
    private function queued(Import $import, AgentAction $action)
    {
        return AgentJob::where(['import_id' => $import->id, 'action' => $action->value, 'state' => JobState::Queued->value])->get();
    }

    private function start(Import $import, array $body = []): TestResponse
    {
        return $this->postJson("/api/v1/imports/{$import->id}/start", $body + [
            'websites' => [['name' => 'bakeryclub.example.org'], ['name' => 'orders.bakeryclub.example.org']],
            'mailDomains' => ['bakeryclub.example.org'],
            'dnsZones' => ['bakeryclub.example.org'],
        ]);
    }

    /** Finishes each queued job of the import, then the jobs that they start, until none is left. */
    private function finishAll(Import $import): void
    {
        for ($round = 0; $round < 10; $round++) {
            $jobs = AgentJob::where('state', JobState::Queued->value)->where(fn ($q) => $q->where('import_id', $import->id))->get();
            if ($jobs->isEmpty()) {
                return;
            }
            foreach ($jobs as $job) {
                $this->finish($job, result: match ($job->action) {
                    AgentAction::FetchImport => ['sha256' => str_repeat('ab', 32), 'bytes' => 1234],
                    AgentAction::ImportFiles => ['files' => 12, 'bytes' => 3000, 'skipped' => [], 'rewritten' => isset($job->payload['wordpressConfig']) ? ['wp-config.php'] : []],
                    AgentAction::ImportMail => ['messages' => 7, 'failed' => ['bea@bakeryclub.example.org' => 1]],
                    default => [],
                });
            }
        }
        $this->fail('The import did not end.');
    }

    public function test_the_plan_shows_what_the_import_makes(): void
    {
        // The panel has a website with the name of the third website of the backup.
        $this->website($this->customer, $this->host, $this->images['apache-8.4'], 'cornerbakery.example.org');
        DnsZone::create(['customer_id' => $this->customer->id, 'name' => 'cornerbakery.example.org', 'status' => DnsZone::ACTIVE, 'serial' => 1]);
        $import = $this->ready();

        $plan = $this->getJson("/api/v1/imports/{$import->id}")->assertOk()
            ->assertJsonPath('status', Import::READY)
            ->assertJsonPath('panel', 'cpanel')
            ->assertJsonPath('panelName', 'cPanel')
            ->assertJsonPath('account', 'bakery')
            ->assertJsonPath('counts', ['websites' => 3, 'databases' => 2, 'mailboxes' => 4, 'dnsZones' => 2])
            ->assertJsonPath('warnings.0.item', 'files')
            ->assertJsonMissingPath('url')
            ->json('plan');
        $this->assertSame(str_repeat('ab', 32), $import->sha256);
        $this->assertSame([$this->host->id => 'ready'], $import->servers);

        [$club, $orders, $corner] = $plan['websites'];
        // The same PHP version, else the first newer version. Apache comes first.
        $this->assertSame($this->images['apache-8.4']->id, $club['imageId']);
        $this->assertSame('7.4', $orders['phpVersion']);
        $this->assertSame($this->images['apache-8.4']->id, $orders['imageId']);
        $this->assertTrue($club['wordpress']);
        $this->assertSame([['name' => 'bakery_wp', 'bytes' => 48 * 1024 ** 2]], $club['databases']);
        $this->assertNull($club['conflict']);
        $this->assertSame('A website with this name exists.', $corner['conflict']);
        $this->assertSame(1, $plan['mailDomains'][0]['newPasswords']);
        // 1 GiB, 512 MiB, 1 GiB for the mailbox with no limit, and 256 MiB.
        $this->assertSame((2 * 1024 + 768) * 1024 ** 2, $plan['mailDomains'][0]['quotaBytes']);
        $this->assertSame(20, $plan['mailSpace']['mailboxes']);
        $this->assertSame(3812, $plan['mailDomains'][0]['messages']);
        $this->assertSame(['bakeryclub.example.org', false, null], [$plan['dnsZones'][0]['name'], $plan['dnsZones'][0]['exists'], $plan['dnsZones'][0]['conflict']]);
        $this->assertTrue($plan['dnsZones'][1]['exists']);
        $this->assertCount(3, $plan['images']);
    }

    public function test_the_import_makes_the_records_and_then_copies_the_content(): void
    {
        $import = $this->ready();
        $this->start($import)->assertAccepted()->assertJsonPath('status', Import::RUNNING);

        // The records, with the checks of the API.
        $club = Website::where('name', 'bakeryclub.example.org')->sole();
        $this->assertSame($this->customer->id, $club->customer_id);
        $this->assertSame($this->host->id, $club->server_id);
        $this->assertSame($this->images['apache-8.4']->id, $club->image_id);
        $this->assertEqualsCanonicalizing(['bakeryclub.example.org', 'www.bakeryclub.example.org', 'bakery-club.example.net'], $club->domains()->pluck('name')->all());
        $this->assertSame(["{$club->unix_user}_wp"], $club->databases()->pluck('name')->all());
        $this->assertFalse(CronJob::where('website_id', $club->id)->sole()->enabled);
        $this->assertNotNull(Website::where('name', 'orders.bakeryclub.example.org')->first());
        $zone = DnsZone::where('name', 'bakeryclub.example.org')->sole();
        // The website keeps the address of the new server. The records of cPanel stay out.
        $this->assertSame(['@ A 203.0.113.10', 'orders A 203.0.113.10', 'www A 203.0.113.10'], $zone->records()->where('managed', 'website')->orderBy('name')->get()->map(fn ($r) => "{$r->name} {$r->type} {$r->value}")->all());
        $this->assertSame(['@ MX', '@ TXT', '_dmarc TXT', 'google._domainkey TXT'], $zone->records()->whereNull('managed')->orderBy('name')->orderBy('type')->get()->map(fn ($r) => "{$r->name} {$r->type}")->unique()->values()->all());

        // The mailboxes wait until the mail server has the domain.
        $this->assertSame(0, Mailbox::count());
        $this->finish(AgentJob::where(['action' => AgentAction::SetMailDomain->value, 'import_id' => $import->id])->sole(), result: ['records' => []]);
        $this->assertSame(['bea', 'hello', 'jobs', 'orders'], Mailbox::orderBy('local_part')->pluck('local_part')->all());
        // A mailbox keeps the password hash of the old panel. The job takes it, the import does
        // not keep it.
        $set = AgentJob::where(['action' => AgentAction::SetMailbox->value, 'import_id' => $import->id])->get()->firstWhere('payload.localPart', 'hello');
        $this->assertStringStartsWith('$6$', $set->payload['passwordHash']);
        $this->assertStringNotContainsString('$6$', json_encode($import->fresh()->items));
        $this->assertArrayNotHasKey('passwordHash', $this->finish($set)->payload);

        // The content waits for its records.
        $this->assertCount(0, $this->queued($import, AgentAction::ImportFiles));
        $this->assertCount(0, $this->queued($import, AgentAction::ImportDatabase));
        $create = AgentJob::where(['action' => AgentAction::CreateWebsite->value, 'website_id' => $club->id])->sole();
        $this->finish($create);
        $files = $this->queued($import, AgentAction::ImportFiles)->sole();
        $this->assertSame('cpmove-bakery/homedir/public_html', $files->payload['path']);
        $this->assertSame(['orders'], $files->payload['exclude']);
        $this->assertSame($club->unix_user, $files->payload['unixUser']);
        $this->assertSame('wp-config.php', $files->payload['wordpressConfig']);
        $this->assertSame("{$club->unix_user}_wp", $files->payload['wordpressValues']['DB_NAME']);
        $this->assertNotEmpty($files->payload['wordpressValues']['DB_PASSWORD']);
        // The import does not keep the new database password.
        $this->assertStringNotContainsString($files->payload['wordpressValues']['DB_PASSWORD'], json_encode($import->fresh()->items));
        $this->assertArrayNotHasKey('wordpressValues', $this->finish($files, result: ['files' => 3, 'rewritten' => ['wp-config.php']])->payload);

        $this->finishAll($import);
        $import->refresh();
        $this->assertSame(Import::DONE, $import->status);
        $this->assertNotNull($import->finished_at);
        $items = collect($this->getJson("/api/v1/imports/{$import->id}")->assertOk()->json('items'))->keyBy(fn ($i) => "{$i['kind']} {$i['name']}");
        $this->assertSame('done', $items['website bakeryclub.example.org']['state']);
        $this->assertSame('3 files copied, database settings changed in wp-config.php', $items['files bakeryclub.example.org']['detail']);
        $this->assertSame('Dump loaded', $items["dump {$club->unix_user}_wp"]['detail']);
        $this->assertSame("New name: {$club->unix_user}_wp", $items['database bakery_wp']['detail']);
        $this->assertSame('7 messages copied, 1 refused by the mail server', $items['messages bakeryclub.example.org']['detail']);
        $this->assertSame('The panel does not know the old password. Make a new password for the mailbox on the Email page, and give it to its owner.', $items['mailbox jobs@bakeryclub.example.org']['detail']);
        $this->assertSame('done', $items['dnsZone bakeryclub.example.org']['state']);
        $cron = $items->first(fn ($i) => $i['kind'] === 'cronJob');
        $this->assertSame(['done', 'Turned off. Change the paths of the old panel, then turn it on.'], [$cron['state'], $cron['detail']]);
        $this->assertSame(['hello', 'orders', 'bea'], array_column($this->queuedOrAll($import, AgentAction::ImportMail)->sole()->payload['mailboxes'], 'localPart'));

        // The import is complete: the server removes the archive.
        $this->assertSame([], $import->servers);
        $this->assertSame(1, AgentJob::where(['import_id' => $import->id, 'action' => AgentAction::DeleteImport->value, 'server_id' => $this->host->id])->count());
        $this->assertTrue(AuditEntry::where('action', 'Imported account')->where('target', 'bakery (cPanel)')->exists());
    }

    /** Finishes the job of the mail domain, then the jobs of the mailboxes and addresses. */
    private function finishMailRecords(Import $import): void
    {
        foreach ([AgentAction::SetMailDomain, AgentAction::SetMailbox, AgentAction::SetMailAlias] as $action) {
            foreach ($this->queued($import, $action) as $job) {
                $this->finish($job);
            }
        }
    }

    /** All jobs of the import of one action. */
    private function queuedOrAll(Import $import, AgentAction $action)
    {
        return AgentJob::where(['import_id' => $import->id, 'action' => $action->value])->get();
    }

    public function test_a_server_with_a_part_downloads_the_archive_first(): void
    {
        // The mail domains of the customer are on a different server.
        $this->host->update(['roles' => ['web'], 'mail_hostname' => null, 'mail_status' => null]);
        $mail = $this->server(['name' => 'mail-1', 'roles' => ['email'], 'mail_hostname' => 'mail.other.example', 'mail_status' => 'running', 'agent_features' => ['imports']]);
        $import = $this->ready();
        $this->start($import, ['websites' => [], 'dnsZones' => []])->assertAccepted();
        $this->finishMailRecords($import);

        $fetch = $this->queued($import, AgentAction::FetchImport)->sole();
        $this->assertSame($mail->id, $fetch->server_id);
        $this->assertSame(['https://files.example/cpmove-bakery.tar.gz', str_repeat('ab', 32), false], [$fetch->payload['url'], $fetch->payload['sha256'], $fetch->payload['inspect']]);
        $this->assertCount(0, $this->queued($import, AgentAction::ImportMail));

        $this->finish($fetch);
        $this->assertSame('ready', $import->fresh()->servers[$mail->id]);
        $this->assertSame($mail->id, $this->queued($import, AgentAction::ImportMail)->sole()->server_id);
    }

    public function test_a_failed_download_fails_the_parts_of_that_server(): void
    {
        $this->host->update(['roles' => ['web'], 'mail_hostname' => null, 'mail_status' => null]);
        $this->server(['name' => 'mail-1', 'roles' => ['email'], 'mail_hostname' => 'mail.other.example', 'mail_status' => 'running']);
        $import = $this->ready();
        $this->start($import, ['websites' => [], 'dnsZones' => []])->assertAccepted();
        $this->finishMailRecords($import);
        $this->finish($this->queued($import, AgentAction::FetchImport)->sole(), JobState::Failed);

        $import->refresh();
        $this->assertSame(Import::DONE, $import->status);
        $messages = collect($import->items)->firstWhere('kind', 'messages');
        $this->assertSame(['failed', 'The server mail-1 could not download the archive.'], [$messages['state'], $messages['error']]);
    }

    public function test_a_failed_record_skips_its_content(): void
    {
        $import = $this->ready();
        $this->start($import, ['mailDomains' => [], 'dnsZones' => []])->assertAccepted();
        $club = Website::where('name', 'bakeryclub.example.org')->sole();
        $this->finish(AgentJob::where(['action' => AgentAction::CreateWebsite->value, 'website_id' => $club->id])->sole(), JobState::Failed);
        $this->finishAll($import);

        $items = collect($import->fresh()->items)->keyBy(fn ($i) => "{$i['kind']} {$i['name']}");
        $this->assertSame('failed', $items['website bakeryclub.example.org']['state']);
        $this->assertSame(['skipped', 'A part before this one failed, so the import did not copy it.'], [$items['files bakeryclub.example.org']['state'], $items['files bakeryclub.example.org']['error']]);
        $this->assertSame('done', $items['files orders.bakeryclub.example.org']['state']);
        $this->assertCount(1, $this->queuedOrAll($import, AgentAction::ImportFiles));
    }

    public function test_a_refused_record_fails_its_item_and_the_import_continues(): void
    {
        $this->website($this->customer, $this->host, $this->images['apache-8.4'], 'cornerbakery.example.org');
        $import = $this->ready();
        $this->start($import, ['websites' => [['name' => 'cornerbakery.example.org'], ['name' => 'orders.bakeryclub.example.org', 'imageId' => $this->images['apache-8.5']->id]], 'mailDomains' => [], 'dnsZones' => []])->assertAccepted();

        $items = collect($import->fresh()->items)->keyBy(fn ($i) => "{$i['kind']} {$i['name']}");
        $this->assertSame('failed', $items['website cornerbakery.example.org']['state']);
        $this->assertNotEmpty($items['website cornerbakery.example.org']['error']);
        $this->assertSame('skipped', $items['files cornerbakery.example.org']['state']);
        $this->assertSame($this->images['apache-8.5']->id, Website::where('name', 'orders.bakeryclub.example.org')->sole()->image_id);
    }

    public function test_the_import_starts_one_time_with_the_parts_of_the_plan(): void
    {
        $import = $this->ready();
        $this->start($import, ['websites' => [['name' => 'other.example']]])->assertUnprocessable()->assertJsonValidationErrors('websites.0.name');
        $node = $this->image(['name' => 'Node.js', 'family' => 'node', 'php_version' => null, 'reference' => 'ghcr.io/cloudfactory/node']);
        $this->start($import, ['websites' => [['name' => 'bakeryclub.example.org', 'imageId' => $node->id]]])->assertUnprocessable()->assertJsonValidationErrors('websites.0.imageId');
        $this->start($import, ['websites' => [], 'mailDomains' => [], 'dnsZones' => []])->assertUnprocessable()->assertJsonValidationErrors('websites');
        $this->start($import, ['mailDomains' => [], 'dnsZones' => ['example.net']])->assertUnprocessable()->assertJsonValidationErrors('dnsZones.0');

        $this->start($import, ['mailDomains' => [], 'dnsZones' => []])->assertAccepted();
        $this->start($import)->assertConflict();
        $this->deleteJson("/api/v1/imports/{$import->id}")->assertConflict();
    }

    public function test_an_upload_comes_in_parts_and_then_the_server_reads_it(): void
    {
        $id = $this->postJson('/api/v1/imports', ['customerId' => $this->customer->id, 'serverId' => $this->host->id, 'fileName' => 'cpmove-bakery.tar.gz', 'sizeBytes' => 10])
            ->assertCreated()->assertJsonPath('import.status', Import::UPLOADING)->assertJsonPath('job', null)->json('import.id');
        $put = fn (int $offset, string $bytes) => $this->call('PUT', "/api/v1/imports/{$id}/archive?offset={$offset}", server: ['HTTP_ACCEPT' => 'application/json', 'CONTENT_TYPE' => 'application/octet-stream'], content: $bytes);

        $put(0, 'abcdef')->assertOk()->assertJsonPath('receivedBytes', 6)->assertJsonPath('job', null);
        // A part that the client sends again after a connection error.
        $put(0, 'abcdef')->assertConflict()->assertJsonPath('receivedBytes', 6);
        // A part larger than the rest of the file.
        $put(6, 'ghijklmn')->assertUnprocessable();
        $this->assertSame('abcdef', Storage::disk('local')->get("imports/{$id}/archive"));
        $put(6, 'ghij')->assertOk()->assertJsonPath('receivedBytes', 10)->assertJsonPath('job.action', AgentAction::FetchImport->value);
        $put(10, 'x')->assertConflict();

        $import = Import::findOrFail($id);
        $this->assertSame(Import::FETCHING, $import->status);
        $fetch = AgentJob::where(['import_id' => $id, 'action' => AgentAction::FetchImport->value])->sole();
        $this->assertSame(['', true], [$fetch->payload['url'], $fetch->payload['inspect']]);

        // The agent of the job downloads the file from the panel. No other agent can.
        $response = $this->asLocalAgent('cfa_secret')->get("/api/agent/v1/imports/{$id}/archive")->assertOk();
        $this->assertSame('abcdefghij', file_get_contents($response->baseResponse->getFile()->getPathname()));
        $this->server(['agent_token_hash' => hash('sha256', 'cfa_other'), 'certificate_fingerprint' => str_repeat('cd', 32)]);
        $this->flushHeaders();
        $this->withToken('cfa_other')->withHeader('X-Cloudfactory-Edge', '1')->withHeader('X-Client-Cert-Sha256', str_repeat('cd', 32))
            ->get("/api/agent/v1/imports/{$id}/archive")->assertNotFound();
        $this->flushHeaders();

        $this->finish($fetch, result: ['sha256' => str_repeat('cd', 32), 'bytes' => 10, 'account' => DemoImport::account('cpmove-bakery.tar.gz')]);
        $this->assertSame(Import::READY, $import->fresh()->status);
        $this->asLocalAgent('cfa_secret')->get("/api/agent/v1/imports/{$id}/archive")->assertNotFound();
    }

    public function test_the_panel_refuses_an_import_that_it_cannot_do(): void
    {
        $body = ['customerId' => $this->customer->id, 'serverId' => $this->host->id];
        $this->postJson('/api/v1/imports', $body + ['url' => 'http://files.example/a.tar.gz'])->assertUnprocessable()->assertJsonValidationErrors('url');
        $this->postJson('/api/v1/imports', $body + ['url' => 'https://user:secret@files.example/a.tar.gz'])->assertUnprocessable()->assertJsonValidationErrors('url');
        $this->postJson('/api/v1/imports', $body + ['url' => 'https://files.example/a.tar.gz', 'fileName' => 'a.tar.gz', 'sizeBytes' => 1])->assertUnprocessable();
        $this->postJson('/api/v1/imports', $body + ['fileName' => '../a.tar.gz', 'sizeBytes' => 1])->assertUnprocessable()->assertJsonValidationErrors('fileName');
        $this->postJson('/api/v1/imports', $body + ['fileName' => 'a.tar.gz', 'sizeBytes' => Runner::maxBytes() + 1])->assertUnprocessable()->assertJsonValidationErrors('sizeBytes');
        config(['cloudfactory.imports.reserve_bytes' => PHP_INT_MAX >> 1]);
        $this->postJson('/api/v1/imports', $body + ['fileName' => 'a.tar.gz', 'sizeBytes' => 1])->assertUnprocessable()->assertJsonValidationErrors('sizeBytes');

        $old = $this->server(['name' => 'web-old']);
        $this->postJson('/api/v1/imports', ['serverId' => $old->id] + $body + ['url' => 'https://files.example/a.tar.gz'])->assertUnprocessable()
            ->assertJsonPath('errors.serverId.0', 'The agent on the server web-old cannot import an account. Update the agent of the server, then try again.');
        $this->customer->update(['suspended' => true, 'suspended_by' => 'administrator']);
        $this->postJson('/api/v1/imports', $body + ['url' => 'https://files.example/a.tar.gz'])->assertConflict();

        // Only an administrator imports an account.
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->getJson('/api/v1/imports')->assertForbidden();
        $this->actingWith($this->user(Role::Reseller, $this->customer(null, ['role' => 'reseller'])));
        $this->postJson('/api/v1/imports', $body + ['url' => 'https://files.example/a.tar.gz'])->assertForbidden();
        $this->assertSame(0, Import::count());
    }

    public function test_an_archive_that_the_agent_cannot_read_fails_the_import(): void
    {
        $id = $this->postJson('/api/v1/imports', ['customerId' => $this->customer->id, 'serverId' => $this->host->id, 'url' => 'https://files.example/photos.tar.gz'])->json('import.id');
        $fetch = AgentJob::where(['import_id' => $id, 'action' => AgentAction::FetchImport->value])->sole();
        $this->finish($fetch, JobState::Failed);
        $this->getJson("/api/v1/imports/{$id}")->assertJsonPath('status', Import::FAILED)->assertJsonPath('error', 'the agent failed')->assertJsonPath('plan', null);
        // The server removes the part of the download that it has.
        $this->assertSame(1, AgentJob::where(['import_id' => $id, 'action' => AgentAction::DeleteImport->value])->count());

        // A description in a format that the panel does not know.
        $id = $this->postJson('/api/v1/imports', ['customerId' => $this->customer->id, 'serverId' => $this->host->id, 'url' => 'https://files.example/b.tar.gz'])->json('import.id');
        $this->finish(AgentJob::where(['import_id' => $id, 'action' => AgentAction::FetchImport->value])->sole(), result: ['account' => ['format' => 'cf-account/9']]);
        $this->getJson("/api/v1/imports/{$id}")->assertJsonPath('status', Import::FAILED)->assertJsonPath('error', 'The description of the account does not have the format cf-account/1.');
    }

    public function test_the_removal_of_an_import_removes_its_archives(): void
    {
        $import = $this->ready();
        Storage::disk('local')->put($import->archivePath(), 'x');
        $this->deleteJson("/api/v1/imports/{$import->id}")->assertAccepted()->assertJsonPath('jobs.0.action', AgentAction::DeleteImport->value);
        $this->assertNull(Import::find($import->id));
        $this->assertFalse(Storage::disk('local')->exists($import->archivePath()));
        $this->assertTrue(AuditEntry::where('action', 'Removed account import')->exists());
    }

    public function test_the_panel_ends_the_imports_that_wait_too_long(): void
    {
        $ready = $this->ready();
        $upload = Import::create(['customer_id' => $this->customer->id, 'server_id' => $this->host->id, 'source' => Import::UPLOAD, 'file_name' => 'a.tar.gz', 'size_bytes' => 10, 'status' => Import::UPLOADING]);
        $recent = $this->ready('cpmove-new.tar.gz');
        Import::whereKey($ready->id)->update(['updated_at' => now()->subDays(8)]);
        Import::whereKey($upload->id)->update(['updated_at' => now()->subDays(2)]);

        $this->artisan('cloudfactory:prune-imports')->assertSuccessful();
        $this->assertSame(Import::FAILED, $ready->fresh()->status);
        $this->assertSame([], $ready->fresh()->servers);
        $this->assertSame(Import::FAILED, $upload->fresh()->status);
        $this->assertSame(Import::READY, $recent->fresh()->status);
    }

    public function test_the_description_keeps_only_the_fields_of_the_format(): void
    {
        $raw = DemoImport::account('user.admin.bakery.tar.zst');
        $raw['websites'][0]['name'] = 'not a domain';
        $raw['websites'][1]['cronJobs'] = [['schedule' => '* * * * *', 'command' => 'php run.php', 'enabled' => true], ['schedule' => 'often', 'command' => 'x']];
        $raw['websites'][1]['databases'][] = ['engine' => 'postgresql', 'name' => 'pg'];
        $raw['websites'][1]['databases'][] = ['engine' => 'mariadb', 'name' => 'bad-name'];
        $raw['websites'][1]['content']['files']['path'] = '../etc';
        $raw['mail']['domains'][0]['mailboxes'][0]['passwordHash'] = '{PLAIN}secret';
        $raw['mail']['domains'][0]['aliases'][] = ['localPart' => 'empty', 'destinations' => ['not an address']];
        $raw['extra'] = 'ignored';
        $d = Description::normalise($raw);

        $this->assertSame('directadmin', $d['source']['panel']);
        $this->assertSame(['orders.bakeryclub.example.org', 'cornerbakery.example.org'], array_column($d['websites'], 'name'));
        $this->assertSame([['schedule' => '* * * * *', 'command' => 'php run.php', 'enabled' => false]], $d['websites'][0]['cronJobs']);
        $this->assertSame(['bakery_orders'], array_column($d['websites'][0]['databases'], 'name'));
        $this->assertNull($d['websites'][0]['content']['files']);
        $this->assertSame('backup/bakery_orders.sql.gz', $d['websites'][0]['databases'][0]['content']['path']);
        $this->assertSame('', $d['mail']['domains'][0]['mailboxes'][0]['passwordHash']);
        $this->assertSame(['info', 'owner'], array_column($d['mail']['domains'][0]['aliases'], 'localPart'));
        $this->assertArrayNotHasKey('extra', $d);

        // A Plesk backup has the content in nested archives.
        $plesk = Description::normalise(DemoImport::account('backup_bakery_2611221200.tar'));
        $this->assertSame('plesk', $plesk['source']['panel']);
        $this->assertSame('clients/bakery/domains/bakeryclub.example.org/backup_user-data_2611221200.tgz/httpdocs', $plesk['websites'][0]['content']['files']['path']);
        $this->assertSame('clients/bakery/domains/bakeryclub.example.org/mailnames/hello/backup_mailbox_2611221200.tgz', $plesk['mail']['domains'][0]['mailboxes'][0]['content']['path']);

        // A HestiaCP backup has an archive of the files of each web domain.
        $hestia = Description::normalise(DemoImport::account('bakery.2026-11-22_05-00-00.tar'));
        $this->assertSame('hestia', $hestia['source']['panel']);
        $this->assertSame('', $hestia['source']['version']);
        $this->assertSame('web/orders.bakeryclub.example.org/domain_data.tar.zst/public_html', $hestia['websites'][1]['content']['files']['path']);
        $this->assertSame('db/bakery_wp/bakery_wp.mysql.sql.zst', $hestia['websites'][0]['databases'][0]['content']['path']);
        $this->assertSame('mail/bakeryclub.example.org/accounts.tar.zst/hello', $hestia['mail']['domains'][0]['mailboxes'][0]['content']['path']);

        // An account export of Cloudfactory has an archive of the files of each website, and no
        // messages and no passwords of the mailboxes.
        $export = Description::normalise(DemoImport::account('corner-bakery-export-20261122-2103.tar'));
        $this->assertSame(['cloudfactory', '0.1.0', 'Corner Bakery'], [$export['source']['panel'], $export['source']['version'], $export['source']['account']]);
        $this->assertSame('websites/bakeryclub.example.org/files.tar.gz', $export['websites'][0]['content']['files']['path']);
        $this->assertSame([], $export['websites'][0]['content']['files']['exclude']);
        $this->assertSame(['orders', 'websites/orders.bakeryclub.example.org/databases/mariadb-orders_k3m9_orders.sql.gz'], [$export['websites'][1]['databases'][0]['name'], $export['websites'][1]['databases'][0]['content']['path']]);
        $this->assertSame(['', null, 0], [$export['mail']['domains'][0]['mailboxes'][0]['passwordHash'], $export['mail']['domains'][0]['mailboxes'][0]['content'], $export['mail']['domains'][0]['mailboxes'][0]['messages']]);
        $this->assertStringNotContainsString('SOA', $export['dnsZones'][0]['zoneFile']);
        $this->assertSame('cloudfactory', Description::normalise(DemoImport::account('https://files.example/acme.cf-export.tar'))['source']['panel']);

        $this->expectExceptionMessage('comes from a panel that the import does not know');
        Description::normalise(['format' => Description::FORMAT, 'source' => ['panel' => 'ispconfig']]);
    }

    public function test_a_database_name_loses_the_account_of_the_old_panel(): void
    {
        $this->assertSame('wp', Runner::databaseSuffix('bakery_wp', 'bakery'));
        $this->assertSame('other_wp', Runner::databaseSuffix('other_wp', 'bakery'));
        $this->assertSame('db', Runner::databaseSuffix('bakery_', 'bakery'));
        $this->assertSame('shop_2', Runner::databaseSuffix('Shop-2', ''));
    }

    public function test_a_website_can_have_a_document_root_from_its_creation(): void
    {
        $id = $this->postJson('/api/v1/websites', ['name' => 'docroot.example', 'imageId' => $this->images['apache-8.4']->id, 'customerId' => $this->customer->id, 'serverId' => $this->host->id, 'documentRoot' => 'web'])
            ->assertAccepted()->json('website.id');
        $this->assertSame('web', Website::findOrFail($id)->document_root);
        $this->postJson('/api/v1/websites', ['name' => 'bad.example', 'imageId' => $this->images['apache-8.4']->id, 'customerId' => $this->customer->id, 'serverId' => $this->host->id, 'documentRoot' => '../x'])
            ->assertUnprocessable()->assertJsonValidationErrors('documentRoot');
    }
}
