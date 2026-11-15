<?php

namespace Tests\Feature;

use App\Console\Commands\DemoFixtures;
use App\Demo\DemoAgentClient;
use App\Demo\Fixtures;
use App\Demo\RunDemoJob;
use App\Enums\JobState;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Image;
use App\Models\Server;
use App\Models\User;
use App\Models\Website;
use App\Providers\DemoServiceProvider;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Database\Seeders\DemoSeeder;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The demonstration mode: the sample data, and the demo agent that completes each job. */
class DemoTest extends TestCase
{
    protected function setUp(): void
    {
        parent::setUp();
        config(['demo.enabled' => true, 'demo.step_ms' => 0]);
        // The demo agent connects to no server.
        Http::preventStrayRequests();
    }

    /** Does the work of the queue for one job: the panel sends it, and the demo agent runs it. */
    private function runJob(string $jobId): AgentJob
    {
        (new SendJobToAgent($jobId))->handle(app(AgentClient::class));
        (new RunDemoJob($jobId))->handle(app(JobOutcome::class));

        return AgentJob::findOrFail($jobId);
    }

    private function signInAs(string $email): void
    {
        $this->app['auth']->forgetGuards();
        $this->actingWith(User::where('email', $email)->firstOrFail());
    }

    public function test_the_mode_is_off_by_default_and_on_a_production_panel(): void
    {
        $this->assertInstanceOf(DemoAgentClient::class, app(AgentClient::class));

        config(['demo.enabled' => false]);
        $this->assertNotInstanceOf(DemoAgentClient::class, app(AgentClient::class));

        config(['demo.enabled' => true]);
        $this->app->detectEnvironment(fn () => 'production');
        $this->assertFalse(DemoServiceProvider::enabled());
        $this->assertNotInstanceOf(DemoAgentClient::class, app(AgentClient::class));
        $this->artisan('demo:reset')->assertFailed();
    }

    public function test_the_sample_data_has_a_user_of_each_role_who_can_sign_in(): void
    {
        $this->seed(DemoSeeder::class);

        foreach (Fixtures::USERS as $role => $email) {
            $this->app['auth']->forgetGuards();
            $token = $this->postJson('/api/v1/auth/login', ['email' => $email, 'password' => DemoSeeder::PASSWORD])->assertOk()->json('token');
            $this->app['auth']->forgetGuards();
            $this->withToken($token)->getJson('/api/v1/me')->assertOk()->assertJsonPath('role', $role);
        }
        // An example of each state that the pages show.
        $this->assertSame(1, Website::where('status', 'failed')->count());
        $this->assertSame(1, Server::where('state', 'degraded')->count());
        $this->assertTrue(Server::where('reboot_required', true)->exists());
        $this->assertTrue(Image::whereNotNull('available_tag')->exists());
        $this->assertTrue(Database::where('engine', 'valkey')->exists());
    }

    public function test_each_role_sees_only_its_own_part_of_the_sample_data(): void
    {
        $this->seed(DemoSeeder::class);

        $this->signInAs('admin@example.com');
        $this->getJson('/api/v1/websites?perPage=100')->assertOk()->assertJsonCount(12, 'data');
        $this->getJson('/api/v1/servers')->assertOk()->assertJsonCount(4, 'data');
        $this->getJson('/api/v1/mail/domains')->assertOk()->assertJsonCount(4, 'data');

        // The reseller: its own website and the websites of its two customers.
        $this->signInAs('reseller@example.com');
        $this->assertEqualsCanonicalizing(
            ['northwind.example.com', 'cornerbakery.example.org', 'harbourbooks.example.net', 'events.harbourbooks.example.net'],
            $this->getJson('/api/v1/websites?perPage=100')->assertOk()->json('data.*.name'),
        );
        $this->getJson('/api/v1/servers')->assertForbidden();
        $this->assertSame(['cornerbakery.example.org', 'harbourbooks.example.net', 'northwind.example.com'], $this->getJson('/api/v1/mail/domains')->assertOk()->json('data.*.name'));

        $this->signInAs('owner@example.com');
        $this->assertSame(['acme.example.com'], $this->getJson('/api/v1/mail/domains')->assertOk()->json('data.*.name'));
        $this->getJson('/api/v1/mail/mailboxes')->assertOk()->assertJsonCount(4, 'data');
        $this->getJson('/api/v1/mail/aliases')->assertOk()->assertJsonCount(2, 'data');
        $names = $this->getJson('/api/v1/websites?perPage=100')->assertOk()->json('data.*.name');
        $this->assertCount(7, $names);
        $this->assertSame(['orders-1c16.internal', 'queue-00b1.internal'], array_values(array_filter($names, fn ($n) => ! str_ends_with($n, 'acme.example.com'))));
    }

    public function test_the_demo_agent_completes_the_email_of_a_new_domain(): void
    {
        $this->seed(DemoSeeder::class);
        $this->signInAs('owner@example.com');

        $response = $this->postJson('/api/v1/mail/domains', ['name' => 'shop.acme.example.com'])->assertStatus(202);
        $domain = $response->json('mailDomain.id');
        $this->runJob($response->json('job.id'));
        $shown = $this->getJson("/api/v1/mail/domains/{$domain}")->assertOk()->assertJsonPath('status', 'active')->assertJsonPath('dnsReady', false)->json();
        $this->assertSame(['mx', 'spf', 'dkim', 'dkim', 'dmarc'], array_column($shown['records'], 'purpose'));
        $this->postJson("/api/v1/mail/domains/{$domain}/check")->assertOk()->assertJsonPath('dnsReady', true);

        $mailbox = $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain, 'localPart' => 'orders', 'quotaBytes' => 1024 ** 3])->assertStatus(202);
        $this->assertNotEmpty($mailbox->json('password'));
        $this->runJob($mailbox->json('job.id'));
        $this->getJson('/api/v1/mail/mailboxes?domainId='.$domain)->assertJsonPath('data.0.status', 'active');
        // The demo agent fails the address "fail", to show a failure.
        $failed = $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain, 'localPart' => 'fail', 'quotaBytes' => 1024 ** 3])->assertStatus(202);
        $this->assertSame(JobState::Failed, $this->runJob($failed->json('job.id'))->state);
        $this->getJson('/api/v1/mail/mailboxes?domainId='.$domain)->assertJsonPath('data.0.status', 'failed')->assertJsonPath('data.1.status', 'active');
    }

    public function test_the_demo_agent_completes_a_new_website_with_no_server(): void
    {
        $this->seed(DemoSeeder::class);
        $this->signInAs('owner@example.com');
        $image = Image::where('family', 'frankenphp')->first();

        $response = $this->postJson('/api/v1/websites', ['name' => 'blog.acme.example.com', 'application' => 'WordPress', 'imageId' => $image->id])->assertStatus(202);
        $this->assertSame('starting', $response->json('website.status'));

        $job = $this->runJob($response->json('job.id'));
        $this->assertSame(JobState::Succeeded, $job->state);
        $this->assertSame(count($job->steps), $job->step);
        $website = Website::findOrFail($response->json('website.id'));
        $this->assertSame('running', $website->status->value);
        $this->assertTrue((bool) $website->domains()->first()->dns_points_here);
        $this->assertNotNull($website->usage);
    }

    public function test_the_demo_agent_fails_a_website_with_a_name_that_starts_with_fail(): void
    {
        $this->seed(DemoSeeder::class);
        $this->signInAs('owner@example.com');
        $image = Image::where('family', 'frankenphp')->first();

        $response = $this->postJson('/api/v1/websites', ['name' => 'fail.acme.example.com', 'application' => 'Website', 'imageId' => $image->id])->assertStatus(202);
        $job = $this->runJob($response->json('job.id'));

        $this->assertSame(JobState::Failed, $job->state);
        $this->assertSame(2, $job->step);
        $this->assertStringContainsString('status 503', $job->error);
        $this->assertSame('failed', Website::findOrFail($response->json('website.id'))->status->value);
    }

    public function test_the_demo_agent_gives_the_results_that_the_panel_records(): void
    {
        $this->seed(DemoSeeder::class);
        $this->signInAs('owner@example.com');
        $website = Website::where('name', 'acme.example.com')->firstOrFail();
        $snapshots = $website->snapshots()->count();

        $backup = $this->postJson("/api/v1/websites/{$website->id}/snapshots")->assertStatus(202)->json('job.id');
        $this->assertSame(JobState::Succeeded, $this->runJob($backup)->state);
        $this->assertSame($snapshots + 1, $website->snapshots()->count());

        // Valkey: the agent gives the database number. The numbers 1 and 2 are in use on this server.
        $database = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'valkey', 'name' => 'sessions'])->assertStatus(202)->json('job.id');
        $this->assertSame(JobState::Succeeded, $this->runJob($database)->state);
        $this->assertSame(3, $website->databases()->where('engine', 'valkey')->value('number'));

        $failed = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'will_fail'])->assertStatus(202)->json('job.id');
        $this->assertSame(JobState::Failed, $this->runJob($failed)->state);
        $this->assertFalse($website->databases()->where('name', 'like', '%_will_fail')->exists());
    }

    public function test_the_demo_agent_answers_the_connection_check_and_the_server_jobs(): void
    {
        $this->seed(DemoSeeder::class);
        $this->signInAs('admin@example.com');
        $server = Server::where('name', 'web-2')->firstOrFail();

        $this->postJson("/api/v1/servers/{$server->id}/check-connection")->assertOk()->assertJsonPath('connectionError', null);

        $updates = $this->postJson("/api/v1/servers/{$server->id}/install-updates", ['policy' => 'all'])->assertStatus(202)->json('job.id');
        $this->assertSame(JobState::Succeeded, $this->runJob($updates)->state);
        // The policy "all" keeps the held container tools (FR-1113).
        $this->assertSame(['podman'], array_column($server->fresh()->pending_updates, 'name'));
    }

    /**
     * The tests of the web panel use files with the answers of the API. This test fails when an
     * answer has a field that the files do not have, or the reverse. Then run
     * "php artisan demo:fixtures", and correct the web panel if its tests fail.
     */
    public function test_the_fixtures_of_the_web_panel_agree_with_the_api(): void
    {
        foreach (DemoFixtures::all() as $role => $answers) {
            $file = base_path("../web/src/test/fixtures/{$role}.json");
            $this->assertFileExists($file, 'Run "php artisan demo:fixtures".');
            $saved = json_decode((string) file_get_contents($file), true);

            $now = array_map(fn (string $key) => preg_replace('/[0-9a-z]{26}/', '{id}', $key), array_keys($answers));
            $then = array_map(fn (string $key) => preg_replace('/[0-9a-z]{26}/', '{id}', $key), array_keys($saved));
            $this->assertSame($then, $now, "The requests of the {$role} fixtures changed. Run \"php artisan demo:fixtures\".");
            foreach (array_values($answers) as $i => $answer) {
                $this->assertSame(
                    Fixtures::structure(array_values($saved)[$i]),
                    Fixtures::structure($answer),
                    "The answer to \"{$then[$i]}\" for the {$role} changed. Run \"php artisan demo:fixtures\".",
                );
            }
        }
    }

    public function test_the_demo_agent_has_files_logs_cron_jobs_and_deploys(): void
    {
        $this->seed(DemoSeeder::class);
        $this->signInAs('owner@example.com');
        $website = Website::where('name', 'api.acme.example.com')->firstOrFail();
        $base = "/api/v1/websites/{$website->id}";

        // The file manager works on a small tree of files, with no server.
        $this->getJson("{$base}/files")->assertOk()->assertJsonPath('entries.0.name', 'public')->assertJsonPath('entries.0.type', 'directory');
        $this->putJson("{$base}/files/content", ['path' => '/public/hello.txt', 'content' => 'Hello'])->assertOk()->assertJsonPath('file.sizeBytes', 5);
        $this->getJson("{$base}/files/content?path=/public/hello.txt")->assertOk()->assertJsonPath('content', 'Hello')->assertJsonPath('encoding', 'utf8');
        $this->putJson("{$base}/files/content", ['path' => '/public/hello.txt', 'content' => 'x', 'createOnly' => true])->assertStatus(409);
        $this->postJson("{$base}/files/directories", ['path' => '/public/images'])->assertCreated();
        $this->postJson("{$base}/files/move", ['from' => '/public/hello.txt', 'to' => '/public/images/hello.txt'])->assertOk()->assertJsonPath('file.path', '/public/images/hello.txt');
        $this->deleteJson("{$base}/files?path=/public/images")->assertNoContent();
        $this->getJson("{$base}/files/content?path=/public/images/hello.txt")->assertNotFound();
        $this->getJson("{$base}/logs")->assertOk()->assertJsonStructure(['lines' => [['at', 'stream', 'text']]]);

        // "Run now" gives the cron job a result.
        $cron = $website->cronJobs()->firstOrFail();
        $job = $this->runJob($this->postJson("{$base}/cron-jobs/{$cron->id}/run")->assertAccepted()->json('job.id'));
        $this->assertSame([JobState::Succeeded, 0], [$job->state, $cron->fresh()->last_exit_code]);

        // A deploy goes live. A repository with "fail" in its address fails in the build.
        $this->getJson("{$base}/deploy")->assertOk()->assertJsonPath('source.trigger', 'webhook')->assertJsonPath('source.secrets', ['COMPOSER_AUTH', 'SENTRY_AUTH_TOKEN'])->assertJsonCount(5, 'releases');
        $answer = $this->postJson("{$base}/deploy/releases")->assertAccepted()->json();
        $this->assertSame(JobState::Succeeded, $this->runJob($answer['job']['id'])->state);
        $this->getJson("{$base}/deploy")->assertJsonPath('releases.0.id', $answer['release']['id'])->assertJsonPath('releases.0.live', true)->assertJsonPath('releases.0.state', 'ready');
        $this->assertSame("releases/{$answer['release']['id']}/public", $website->fresh()->document_root);

        $this->putJson("{$base}/deploy", ['repository' => 'git@github.com:acme-example/fail.git', 'branch' => 'main'])->assertOk();
        $this->travel(1)->minutes();
        $failed = $this->postJson("{$base}/deploy/releases")->assertAccepted()->json();
        $this->assertSame(JobState::Failed, $this->runJob($failed['job']['id'])->state);
        $this->getJson("{$base}/deploy")->assertJsonPath('releases.0.state', 'failed')->assertJsonPath('releases.1.live', true);
    }
}
