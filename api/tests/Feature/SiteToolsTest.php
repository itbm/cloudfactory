<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\GitSource;
use App\Models\Release;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The file manager, the logs, the cron jobs and the deploys of a website. */
class SiteToolsTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private function site(array $attributes = []): Website
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cloudfactory-test-pki']);

        return $this->website($this->customer(), $this->server(['certificate_pem' => 'test', 'certificate_fingerprint' => str_repeat('ab', 32)]), $this->image(), 'acme.example', $attributes);
    }

    private function finish(AgentJob $job, JobState $state, array $result = [], ?string $error = null): void
    {
        app(JobOutcome::class)->started($job);
        app(JobOutcome::class)->finish($job, $state, $error, $result);
    }

    public function test_the_file_manager_sends_each_request_to_the_agent_of_the_website(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        Http::fake([
            // The JSON of the agent has each 64-bit number as text, and no field with an empty value.
            self::AGENT.'ListFiles' => Http::response(['entries' => [
                ['name' => 'css', 'type' => 'directory', 'modified' => '2026-11-14T10:00:00Z', 'mode' => '0750'],
                ['name' => 'index.php', 'type' => 'file', 'sizeBytes' => '1234', 'modified' => '2026-11-14T10:00:00Z', 'mode' => '0640'],
            ]]),
            self::AGENT.'ReadFile' => Http::sequence()
                ->push(['file' => ['name' => 'index.php', 'type' => 'file', 'sizeBytes' => '13'], 'content' => base64_encode('<?php echo 1;')])
                ->push(['file' => ['name' => 'logo.png', 'type' => 'file', 'sizeBytes' => '4'], 'content' => base64_encode("\x89PNG")])
                ->push(['file' => ['name' => 'empty.txt', 'type' => 'file']]),
            self::AGENT.'WriteFile' => Http::response(['name' => 'new.txt', 'type' => 'file', 'sizeBytes' => '5']),
            self::AGENT.'MakeDirectory' => Http::response(['name' => 'images', 'type' => 'directory']),
            self::AGENT.'MoveFile' => Http::response(['name' => 'b.txt', 'type' => 'file']),
            self::AGENT.'DeleteFile' => Http::response([]),
        ]);

        $this->getJson("/api/v1/websites/{$website->id}/files?path=/public")->assertOk()
            ->assertJsonPath('entries.0.path', '/public/css')->assertJsonPath('entries.0.sizeBytes', 0)
            ->assertJsonPath('entries.1.sizeBytes', 1234)->assertJsonPath('entries.1.type', 'file')->assertJsonPath('truncated', false);
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/ListFiles') && $r['websiteId'] === $website->id && $r['path'] === '/public');

        $this->getJson("/api/v1/websites/{$website->id}/files/content?path=/public/index.php")->assertOk()
            ->assertJsonPath('encoding', 'utf8')->assertJsonPath('content', '<?php echo 1;')->assertJsonPath('file.path', '/public/index.php');
        $this->getJson("/api/v1/websites/{$website->id}/files/content?path=/public/logo.png")->assertOk()
            ->assertJsonPath('encoding', 'base64')->assertJsonPath('content', base64_encode("\x89PNG"));
        $this->getJson("/api/v1/websites/{$website->id}/files/content?path=/empty.txt")->assertOk()->assertJsonPath('content', '')->assertJsonPath('file.sizeBytes', 0);

        $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => '/new.txt', 'content' => 'héllo', 'createOnly' => true])->assertOk()->assertJsonPath('file.path', '/new.txt');
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/WriteFile') && $r['content'] === base64_encode('héllo') && $r['createOnly'] === true);
        $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => '/empty.txt', 'content' => ''])->assertOk();
        $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => '/a.bin', 'content' => '***', 'encoding' => 'base64'])->assertJsonValidationErrors('content');

        $this->postJson("/api/v1/websites/{$website->id}/files/directories", ['path' => '/public/images'])->assertCreated()->assertJsonPath('file.type', 'directory');
        $this->postJson("/api/v1/websites/{$website->id}/files/move", ['from' => '/a.txt', 'to' => '/public/b.txt'])->assertOk()->assertJsonPath('file.path', '/public/b.txt');
        $this->deleteJson("/api/v1/websites/{$website->id}/files?path=/public/b.txt")->assertNoContent();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Deleted file', 'target' => 'acme.example: /public/b.txt']);
    }

    public function test_the_file_manager_refuses_paths_that_go_out_of_the_website(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        Http::preventStrayRequests();

        foreach (['/../other', '/a/../../b', 'public', '/a//b', '/a/', '/./a', "/a\nb"] as $path) {
            $this->getJson("/api/v1/websites/{$website->id}/files?path=".urlencode($path))->assertJsonValidationErrors('path');
            $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => $path, 'content' => 'x'])->assertJsonValidationErrors('path');
        }
        $this->postJson("/api/v1/websites/{$website->id}/files/move", ['from' => '/a', 'to' => '/../b'])->assertJsonValidationErrors('to');
        $this->deleteJson("/api/v1/websites/{$website->id}/files?path=/")->assertJsonValidationErrors('path');
    }

    public function test_the_file_manager_gives_the_errors_of_the_agent(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        Http::fake([
            self::AGENT.'ReadFile' => Http::response(['code' => 'not_found', 'message' => 'the file or the directory does not exist'], 404),
            self::AGENT.'WriteFile' => Http::response(['code' => 'already_exists', 'message' => 'a file or a directory with this name exists already'], 409),
            self::AGENT.'MakeDirectory' => Http::response(['code' => 'internal', 'message' => 'runuser -u acme -- /usr/local/bin/cf-agent: exit status 1'], 500),
            self::AGENT.'ListFiles' => Http::response('404 page not found', 404),
            self::AGENT.'DeleteFile' => fn () => throw new ConnectionException('cURL error 7: Failed to connect'),
        ]);

        $this->getJson("/api/v1/websites/{$website->id}/files/content?path=/none")->assertNotFound()->assertJsonPath('message', 'The file or the directory does not exist');
        $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => '/a', 'content' => 'x', 'createOnly' => true])->assertStatus(409);
        // An error with no clear cause does not show the server from the inside.
        $this->postJson("/api/v1/websites/{$website->id}/files/directories", ['path' => '/a'])->assertStatus(502)->assertJsonPath('message', 'The server could not do this. Try again later.');
        // An agent of an earlier release does not have the method.
        $this->getJson("/api/v1/websites/{$website->id}/files")->assertStatus(503)->assertJsonPath('message', 'The agent of this server is from an earlier release. Update the agent of the server.');
        $this->deleteJson("/api/v1/websites/{$website->id}/files?path=/a")->assertStatus(503);
    }

    public function test_only_users_of_the_customer_use_the_file_manager_and_a_suspension_stops_changes(): void
    {
        $website = $this->site();
        $other = $this->customer(null, ['name' => 'Other', 'email' => 'other@example.com']);
        Http::fake([self::AGENT.'*' => Http::response([])]);

        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson("/api/v1/websites/{$website->id}/files")->assertNotFound();
        $this->getJson("/api/v1/websites/{$website->id}/logs")->assertNotFound();
        $this->getJson("/api/v1/websites/{$website->id}/cron-jobs")->assertNotFound();
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertNotFound();
        Http::assertNothingSent();

        // A token with no websites scope.
        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(), ['databases:read']);
        $this->getJson("/api/v1/websites/{$website->id}/files")->assertForbidden();
        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(), ['websites:read']);
        $this->getJson("/api/v1/websites/{$website->id}/files")->assertOk();
        $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => '/a', 'content' => 'x'])->assertForbidden();

        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user());
        $website->customer->update(['suspended' => true]);
        $this->getJson("/api/v1/websites/{$website->id}/files")->assertOk();
        $this->putJson("/api/v1/websites/{$website->id}/files/content", ['path' => '/a', 'content' => 'x'])->assertStatus(409);
        $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", ['schedule' => '* * * * *', 'command' => 'true'])->assertStatus(409);
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'https://example.com/a.git', 'branch' => 'main'])->assertStatus(409);

        $website->customer->update(['suspended' => false]);
        $website->forceFill(['status' => 'failed'])->save();
        $this->getJson("/api/v1/websites/{$website->id}/files")->assertJsonValidationErrors('website');
    }

    public function test_a_change_of_permissions_goes_to_the_agent(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        Http::fake([self::AGENT.'SetFileMode' => Http::sequence()
            ->push(['file' => ['name' => 'deploy.sh', 'type' => 'file', 'mode' => '0750'], 'changed' => '1'])
            ->push(['file' => ['name' => 'public', 'type' => 'directory', 'mode' => '0755'], 'changed' => '214'])
            ->push(['code' => 'invalid_argument', 'message' => 'a link has no permissions of its own; change the permissions of its target'], 400)]);

        $this->putJson("/api/v1/websites/{$website->id}/files/permissions", ['path' => '/deploy.sh', 'mode' => '750'])->assertOk()
            ->assertJsonPath('file.path', '/deploy.sh')->assertJsonPath('file.mode', '0750')->assertJsonPath('changed', 1);
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetFileMode') && $r['mode'] === '0750' && $r['recursive'] === false && $r['fileMode'] === '');
        $this->putJson("/api/v1/websites/{$website->id}/files/permissions", ['path' => '/public', 'mode' => '0755', 'recursive' => true, 'fileMode' => '644'])->assertOk()
            ->assertJsonPath('changed', 214);
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetFileMode') && $r['path'] === '/public' && $r['recursive'] === true && $r['fileMode'] === '0644');
        $this->assertDatabaseHas('audit_entries', ['action' => 'Changed permissions', 'target' => 'acme.example: /public to 0755 (folders), 0644 (files)']);
        $this->putJson("/api/v1/websites/{$website->id}/files/permissions", ['path' => '/public/link', 'mode' => '0644'])->assertStatus(422)
            ->assertJsonPath('message', 'A link has no permissions of its own; change the permissions of its target');

        foreach (['4755', '0800', 'rwx', '', '00644'] as $mode) {
            $this->putJson("/api/v1/websites/{$website->id}/files/permissions", ['path' => '/a', 'mode' => $mode])->assertJsonValidationErrors('mode');
        }
        $this->putJson("/api/v1/websites/{$website->id}/files/permissions", ['path' => '/public', 'mode' => '0755', 'recursive' => true])->assertJsonValidationErrors('fileMode');
        $this->putJson("/api/v1/websites/{$website->id}/files/permissions", ['path' => '/../a', 'mode' => '0644'])->assertJsonValidationErrors('path');
        Http::assertSentCount(3);
    }

    public function test_an_archive_and_an_extract_are_jobs_with_a_summary(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        Http::fake([self::AGENT.'*' => Http::response(['jobId' => 'x'])]);
        $base = "/api/v1/websites/{$website->id}/files";

        $id = $this->postJson("{$base}/archives", ['paths' => ['/public', '/composer.json'], 'path' => '/site.tar.gz'])->assertAccepted()
            ->assertJsonPath('job.action', 'MakeArchive')->assertJsonPath('job.ref', '/site.tar.gz')->assertJsonPath('job.steps', ['Make the archive'])
            ->assertJsonPath('job.result', null)->json('job.id');
        $job = AgentJob::findOrFail($id);
        $this->assertSame(['websiteId' => $website->id, 'paths' => ['/public', '/composer.json'], 'path' => '/site.tar.gz'], $job->payload);
        $this->assertSame($website->id, $job->website_id);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Made archive', 'target' => 'acme.example: /site.tar.gz']);

        // The panel keeps the known fields of the summary only.
        $this->finish($job, JobState::Succeeded, ['files' => 120, 'directories' => 14, 'links' => 1, 'sizeBytes' => 48213, 'skipped' => 1, 'skippedNames' => ['/public/fifo', 7], 'other' => 'x']);
        $this->getJson("/api/v1/jobs/{$id}")->assertOk()->assertJsonPath('data.result', [
            'files' => 120, 'directories' => 14, 'links' => 1, 'sizeBytes' => 48213, 'kept' => 0, 'skipped' => 1, 'skippedNames' => ['/public/fifo'],
        ]);

        // The default destination of an extract is the folder of the archive.
        $id = $this->postJson("{$base}/extract", ['path' => '/backups/site.zip'])->assertAccepted()->assertJsonPath('job.action', 'ExtractArchive')->json('job.id');
        $this->assertSame(['websiteId' => $website->id, 'path' => '/backups/site.zip', 'to' => '/backups', 'overwrite' => false], AgentJob::findOrFail($id)->payload);
        $id = $this->postJson("{$base}/extract", ['path' => '/site.TAR.BZ2', 'to' => '/public', 'overwrite' => true])->assertAccepted()->json('job.id');
        $this->assertSame(['websiteId' => $website->id, 'path' => '/site.TAR.BZ2', 'to' => '/public', 'overwrite' => true], AgentJob::findOrFail($id)->payload);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Extracted archive', 'target' => 'acme.example: /site.TAR.BZ2 to /public (replace files)']);
        // A failed job has no summary. Other actions keep no result.
        $this->finish(AgentJob::findOrFail($id), JobState::Failed, [], 'the archive is not complete');
        $this->assertNull(AgentJob::findOrFail($id)->result);
        $cron = $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", ['schedule' => '* * * * *', 'command' => 'true'])->json('job.id');
        $this->finish(AgentJob::findOrFail($cron), JobState::Succeeded, ['files' => 1]);
        $this->assertNull(AgentJob::findOrFail($cron)->result);

        foreach ([
            [['paths' => ['/public'], 'path' => '/site.rar'], 'path'],
            [['paths' => ['/public'], 'path' => '/site.tar.bz2'], 'path'],
            [['paths' => [], 'path' => '/site.zip'], 'paths'],
            [['paths' => ['/../x'], 'path' => '/site.zip'], 'paths.0'],
            [['paths' => ['/a/css', '/b/css'], 'path' => '/site.zip'], 'paths'],
            [['paths' => ['/', '/public'], 'path' => '/site.zip'], 'paths'],
            [['paths' => ['/a', '/a'], 'path' => '/site.zip'], 'paths.0'],
            [['paths' => array_fill(0, 2001, '/a'), 'path' => '/site.zip'], 'paths'],
        ] as [$body, $error]) {
            $this->postJson("{$base}/archives", $body)->assertJsonValidationErrors($error);
        }
        $this->postJson("{$base}/extract", ['path' => '/site.rar'])->assertJsonValidationErrors('path');
        $this->postJson("{$base}/extract", ['path' => '/site.zip', 'to' => '/a/../b'])->assertJsonValidationErrors('to');

        // A suspended customer cannot change files.
        $website->customer->update(['suspended' => true]);
        $this->postJson("{$base}/archives", ['paths' => ['/public'], 'path' => '/site.zip'])->assertStatus(409);
        $this->postJson("{$base}/extract", ['path' => '/site.zip'])->assertStatus(409);
        $this->putJson("{$base}/permissions", ['path' => '/a', 'mode' => '0644'])->assertStatus(409);
        $this->assertSame(3, AgentJob::whereIn('action', [AgentAction::MakeArchive, AgentAction::ExtractArchive])->count());
    }

    public function test_logs_give_the_lines_of_one_stream(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        Http::fake([self::AGENT.'GetLogs' => Http::response(['lines' => [
            ['at' => '2026-11-14T10:00:00Z', 'stream' => 'access', 'text' => '{"status":200}'],
            ['at' => '2026-11-14T10:00:01Z', 'stream' => 'error', 'text' => 'PHP Warning'],
            ['at' => '2026-11-14T10:00:02Z', 'stream' => 'access'],
        ]])]);

        $this->getJson("/api/v1/websites/{$website->id}/logs?lines=50")->assertOk()->assertJsonCount(3, 'lines')->assertJsonPath('lines.2.text', '');
        Http::assertSent(fn ($r) => $r['lines'] === 50 && $r['websiteId'] === $website->id);
        $this->getJson("/api/v1/websites/{$website->id}/logs?stream=error")->assertOk()->assertJsonCount(1, 'lines')->assertJsonPath('lines.0.text', 'PHP Warning');
        $this->getJson("/api/v1/websites/{$website->id}/logs?lines=5000")->assertJsonValidationErrors('lines');
    }

    public function test_each_change_of_a_cron_job_sends_all_cron_jobs_to_the_agent(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $id = $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", ['schedule' => '*/5 * * * *', 'command' => 'php artisan schedule:run'])
            ->assertAccepted()->assertJsonPath('job.action', 'SetCronJobs')->assertJsonPath('cronJob.enabled', true)->assertJsonPath('cronJob.lastResult', null)->json('cronJob.id');
        $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", ['schedule' => '0 3 * * *', 'command' => 'php bin/report.php', 'enabled' => false])->assertAccepted();
        $job = AgentJob::where('action', AgentAction::SetCronJobs)->latest('id')->first();
        $this->assertSame($website->id, $job->payload['websiteId']);
        $this->assertSame([['*/5 * * * *', true], ['0 3 * * *', false]], array_map(fn ($c) => [$c['schedule'], $c['enabled']], $job->payload['cronJobs']));

        $this->patchJson("/api/v1/websites/{$website->id}/cron-jobs/{$id}", ['enabled' => false])->assertAccepted()->assertJsonPath('cronJob.enabled', false)->assertJsonPath('cronJob.command', 'php artisan schedule:run');
        $this->getJson("/api/v1/websites/{$website->id}/cron-jobs")->assertOk()->assertJsonCount(2, 'data');
        $this->deleteJson("/api/v1/websites/{$website->id}/cron-jobs/{$id}")->assertAccepted();
        $this->assertCount(1, AgentJob::where('action', AgentAction::SetCronJobs)->latest('id')->first()->payload['cronJobs']);

        foreach ([['schedule' => '61 * * * *', 'command' => 'true'], ['schedule' => '* * * *', 'command' => 'true'], ['schedule' => '@daily', 'command' => 'true']] as $bad) {
            $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", $bad)->assertJsonValidationErrors('schedule');
        }
        $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", ['schedule' => '* * * * *', 'command' => "true\nrm -rf /"])->assertJsonValidationErrors('command');

        // A website can have 20 cron jobs.
        for ($i = $website->cronJobs()->count(); $i < 20; $i++) {
            $website->cronJobs()->create(['schedule' => '* * * * *', 'command' => 'true']);
        }
        $this->postJson("/api/v1/websites/{$website->id}/cron-jobs", ['schedule' => '* * * * *', 'command' => 'true'])->assertJsonValidationErrors('schedule');
    }

    public function test_a_cron_job_gets_the_result_of_each_run(): void
    {
        $website = $this->site();
        $cron = $website->cronJobs()->create(['schedule' => '* * * * *', 'command' => 'php bin/report.php']);
        $this->actingWith($this->user());

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/cron-jobs/{$cron->id}/run")->assertAccepted()->assertJsonPath('job.action', 'RunCronJob')->json('job.id');
        $this->finish(AgentJob::find($jobId), JobState::Failed, [], 'the command ended with exit code 255: PHP Fatal error: boom');
        $this->getJson("/api/v1/websites/{$website->id}/cron-jobs")->assertJsonPath('data.0.lastResult', 'failed')->assertJsonPath('data.0.lastExitCode', 255)->assertJsonPath('data.0.lastOutput', 'PHP Fatal error: boom');

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/cron-jobs/{$cron->id}/run")->json('job.id');
        $this->finish(AgentJob::find($jobId), JobState::Succeeded, ['exitCode' => 0, 'output' => 'done']);
        $this->assertSame([0, 'done'], [$cron->fresh()->last_exit_code, $cron->fresh()->last_output]);

        // The agent reports the runs of the schedule. A report of a different server changes nothing.
        $server = $website->server;
        $server->forceFill(['agent_token_hash' => hash('sha256', 'cfa_one'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'local' => true])->save();
        $at = now()->addMinute()->startOfSecond();
        $report = ['agentVersion' => '0.2.0', 'cronRuns' => [['websiteId' => $website->id, 'id' => $cron->id, 'at' => $at->toIso8601String(), 'exitCode' => 3, 'output' => 'from the schedule']]];
        $this->app['auth']->forgetGuards();
        $this->asLocalAgent('cfa_one')->postJson('/api/agent/v1/report', $report)->assertOk();
        $this->assertSame([3, 'from the schedule'], [$cron->fresh()->last_exit_code, $cron->fresh()->last_output]);
        // The agent sends each result more than one time. An older run does not replace a newer run.
        $report['cronRuns'][0] = ['at' => $at->copy()->subHour()->toIso8601String(), 'exitCode' => 0, 'output' => 'old'] + $report['cronRuns'][0];
        $this->asLocalAgent('cfa_one')->postJson('/api/agent/v1/report', $report)->assertOk();
        $this->assertSame(3, $cron->fresh()->last_exit_code);

        $website->forceFill(['status' => 'stopped'])->save();
        $this->actingWith($this->user());
        $this->postJson("/api/v1/websites/{$website->id}/cron-jobs/{$cron->id}/run")->assertStatus(409);
    }

    private function connect(Website $website, array $more = []): array
    {
        return $this->putJson("/api/v1/websites/{$website->id}/deploy", $more + ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main', 'buildCommands' => ['composer install --no-dev']])->assertOk()->json();
    }

    public function test_a_website_connects_to_a_repository_and_keeps_its_secrets_encrypted(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertOk()->assertJsonPath('source', null)->assertJsonPath('releases', []);

        $deploy = $this->connect($website);
        $this->assertStringStartsWith('ssh-ed25519 ', $deploy['source']['deployKey']);
        $this->assertSame(['manual', 'public', []], [$deploy['source']['trigger'], $deploy['source']['webDirectory'], $deploy['source']['secrets']]);
        $this->assertMatchesRegularExpression('#/api/v1/hooks/deploy/[A-Za-z0-9]{48}$#', $deploy['source']['webhookUrl']);

        // A change of the settings keeps the key and the webhook address.
        $changed = $this->connect($website, ['branch' => 'release', 'trigger' => 'webhook', 'webDirectory' => '.']);
        $this->assertSame([$deploy['source']['deployKey'], $deploy['source']['webhookUrl'], 'release'], [$changed['source']['deployKey'], $changed['source']['webhookUrl'], $changed['source']['branch']]);

        $this->putJson("/api/v1/websites/{$website->id}/deploy/secrets/NPM_TOKEN", ['value' => 's3cret-value'])->assertOk()->assertJsonPath('source.secrets', ['NPM_TOKEN']);
        $this->putJson("/api/v1/websites/{$website->id}/deploy/secrets/bad-name", ['value' => 'x'])->assertJsonValidationErrors('name');
        $this->putJson("/api/v1/websites/{$website->id}/deploy/secrets/A", ['value' => "x\ny"])->assertJsonValidationErrors('value');
        // The database has no plain text of the private key, the token and the secret.
        $row = (array) DB::table('git_sources')->first();
        $this->assertStringNotContainsString('PRIVATE KEY', $row['private_key']);
        $this->assertStringNotContainsString('s3cret-value', $row['secrets']);
        $this->assertStringNotContainsString($row['webhook_token'], $deploy['source']['webhookUrl']);
        $this->assertStringNotContainsString('s3cret-value', json_encode($this->getJson("/api/v1/websites/{$website->id}/deploy")->json()));
        $this->deleteJson("/api/v1/websites/{$website->id}/deploy/secrets/NPM_TOKEN")->assertOk()->assertJsonPath('source.secrets', []);
        $this->deleteJson("/api/v1/websites/{$website->id}/deploy/secrets/NPM_TOKEN")->assertNotFound();

        foreach (['https://user:pass@github.com/acme/shop.git', 'file:///etc/passwd', '--upload-pack=x', 'http://github.com/acme/shop.git'] as $repository) {
            $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => $repository, 'branch' => 'main'])->assertJsonValidationErrors('repository');
        }
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'https://github.com/acme/shop.git', 'branch' => '--force'])->assertJsonValidationErrors('branch');
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'https://github.com/acme/shop.git', 'branch' => 'main', 'webDirectory' => '../x'])->assertJsonValidationErrors('webDirectory');

        $this->deleteJson("/api/v1/websites/{$website->id}/deploy")->assertNoContent();
        $this->assertSame(0, GitSource::count());
    }

    public function test_a_deploy_makes_a_release_and_the_website_serves_it(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertJsonValidationErrors('repository');
        $this->connect($website);
        $this->putJson("/api/v1/websites/{$website->id}/deploy/secrets/NPM_TOKEN", ['value' => 's3cret-value'])->assertOk();

        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->assertJsonPath('job.action', 'Deploy')->assertJsonPath('release.state', 'building')->json();
        $first = $answer['release']['id'];
        $job = AgentJob::find($answer['job']['id']);
        $this->assertSame("releases/{$first}/public", $job->payload['website']['documentRoot']);
        $this->assertSame(['git@github.com:acme/shop.git', 'main', ['composer install --no-dev'], []], [$job->payload['repository'], $job->payload['branch'], $job->payload['buildCommands'], $job->payload['keepReleases']]);
        // The job has no key and no secret. The panel adds them when it sends the job.
        $this->assertStringNotContainsString('s3cret-value', json_encode($job->payload));
        $this->assertStringNotContainsString('PRIVATE KEY', json_encode($job->payload));
        Http::fake([self::AGENT.'Deploy' => Http::response(['jobId' => $job->id])]);
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertSent(fn ($r) => str_starts_with($r['deployKey'], '-----BEGIN OPENSSH PRIVATE KEY-----') && (array) $r['secrets'] === ['NPM_TOKEN' => 's3cret-value'] && $r['releaseId'] === $first);

        // One deploy at a time.
        $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertStatus(409);

        $this->finish($job, JobState::Succeeded, ['commit' => str_repeat('a', 40), 'message' => 'First', 'removed' => []]);
        $this->assertSame("releases/{$first}/public", $website->fresh()->document_root);
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('releases.0.state', 'ready')->assertJsonPath('releases.0.live', true)->assertJsonPath('releases.0.message', 'First');
        // The document root is now the release. The runtime settings do not change it.
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['documentRoot' => 'web'])->assertJsonValidationErrors('documentRoot');

        // A second deploy fails in the build. The website continues from the first release.
        $this->travel(1)->minutes();
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $second = $answer['release']['id'];
        $this->assertSame([$first], AgentJob::find($answer['job']['id'])->payload['keepReleases']);
        $this->finish(AgentJob::find($answer['job']['id']), JobState::Failed, [], 'a build command failed: exit status 2: composer: lock file is out of date');
        $this->assertSame("releases/{$first}/public", $website->fresh()->document_root);
        // The owner sees the output of the build command. It is from the container of the website.
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('releases.0.id', $second)->assertJsonPath('releases.0.state', 'failed')
            ->assertJsonPath('releases.0.error', 'a build command failed: exit status 2: composer: lock file is out of date')->assertJsonPath('releases.1.live', true);
        $this->postJson("/api/v1/websites/{$website->id}/deploy/releases/{$second}/activate")->assertJsonValidationErrors('release');
        $this->postJson("/api/v1/websites/{$website->id}/deploy/releases/{$first}/activate")->assertJsonValidationErrors('release');

        // A third deploy goes live, and the agent removes the first release.
        $this->travel(1)->minutes();
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $third = $answer['release']['id'];
        $this->finish(AgentJob::find($answer['job']['id']), JobState::Succeeded, ['commit' => str_repeat('c', 40), 'message' => 'Third', 'removed' => []]);
        $this->assertSame([$third], Release::where('live', true)->pluck('id')->all());

        // Go back to the first release.
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases/{$first}/activate")->assertAccepted()->assertJsonPath('job.action', 'ActivateRelease')->json();
        $back = AgentJob::find($answer['job']['id']);
        $this->assertSame([$first, "releases/{$first}/public"], [$back->payload['releaseId'], $back->payload['website']['documentRoot']]);
        $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertStatus(409);
        $this->finish($back, JobState::Succeeded);
        $this->assertSame([$first], Release::where('live', true)->pluck('id')->all());
        $this->assertSame("releases/{$first}/public", $website->fresh()->document_root);

        // A release that the agent removed is not for "Go back".
        $this->travel(1)->minutes();
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $this->finish(AgentJob::find($answer['job']['id']), JobState::Succeeded, ['commit' => str_repeat('d', 40), 'message' => 'Fourth', 'removed' => [$third]]);
        $this->postJson("/api/v1/websites/{$website->id}/deploy/releases/{$third}/activate")->assertJsonValidationErrors('release');
    }

    public function test_a_different_error_of_a_deploy_does_not_show_the_server_to_the_owner(): void
    {
        $website = $this->site();
        $owner = $this->user(Role::Owner, $website->customer);
        $this->actingWith($owner);
        $this->connect($website);
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $error = 'runuser -u acme_1234 -- rm -rf -- /srv/cloudfactory/sites/acme_1234/app/releases/x: exit status 1: permission denied';
        $this->finish(AgentJob::find($answer['job']['id']), JobState::Failed, [], $error);

        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('releases.0.error', 'A step failed on the server. Your provider can see the details.');
        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user());
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('releases.0.error', $error);
    }

    public function test_the_webhook_starts_a_deploy_of_the_branch(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $url = parse_url($this->connect($website)['source']['webhookUrl'], PHP_URL_PATH);
        $this->app['auth']->forgetGuards();

        // The trigger is "manual": the webhook is off.
        $this->postJson($url)->assertNotFound();
        GitSource::first()->update(['trigger' => 'webhook']);
        $this->postJson('/api/v1/hooks/deploy/'.str_repeat('x', 48))->assertNotFound();

        $this->postJson($url, ['ref' => 'refs/heads/feature'])->assertOk()->assertJsonPath('status', 'ignored');
        $this->assertSame(0, Release::count());
        $id = $this->postJson($url, ['ref' => 'refs/heads/main'])->assertAccepted()->assertJsonPath('status', 'started')->json('releaseId');
        $this->assertSame(['webhook', 'building'], [Release::find($id)->trigger, Release::find($id)->state]);
        $this->postJson($url)->assertAccepted()->assertJsonPath('status', 'busy');
        $this->assertSame(1, AgentJob::where('action', AgentAction::Deploy)->count());

        Release::query()->update(['state' => 'failed']);
        $website->customer->update(['suspended' => true]);
        $this->postJson($url)->assertStatus(409);
    }
}
