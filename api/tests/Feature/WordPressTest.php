<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Website;
use App\Models\WordPressSite;
use App\Services\JobOutcome;
use App\Services\WordPress;
use Illuminate\Support\Carbon;
use Tests\TestCase;

/** FR-206: one-click WordPress, the updates with a restore point, and WP-CLI. */
class WordPressTest extends TestCase
{
    /** What the agent reports for a site with WordPress 6.8.1, one plugin and one theme. */
    private const INVENTORY = [
        'version' => '6.8.1', 'coreMinor' => '6.8.3', 'coreLatest' => '6.9', 'siteUrl' => 'https://acme.example', 'databaseName' => 'acme_x7k2_wp',
        'plugins' => [['name' => 'akismet', 'title' => 'Akismet Anti-spam', 'status' => 'active', 'version' => '5.3', 'update' => '5.4']],
        'themes' => [['name' => 'twentytwentyfive', 'title' => 'Twenty Twenty-Five', 'status' => 'active', 'version' => '1.2']],
        'checkedAt' => '2026-11-22T10:00:00Z',
    ];

    private function finish(AgentJob $job, JobState $state, array $result = [], ?string $error = null, ?int $step = null): void
    {
        app(JobOutcome::class)->started($job);
        if ($step !== null) {
            app(JobOutcome::class)->progress($job, $step);
        }
        app(JobOutcome::class)->finish($job, $state, $error, $result);
    }

    private function site(array $serverAttributes = []): Website
    {
        $server = $this->server($serverAttributes + ['agent_features' => [WordPress::FEATURE]]);

        return $this->website($this->customer(), $server, $this->image(), 'acme.example', ['unix_user' => 'acme_x7k2']);
    }

    /** A website with a WordPress site that is ready, and its database. */
    private function wordpress(): Website
    {
        $website = $this->site();
        $server = $this->databaseServerOf($website);
        $database = $website->databases()->create(['engine' => 'mariadb', 'name' => 'acme_x7k2_wp', 'database_server_id' => $server->id]);
        $website->wordpress()->create(['status' => WordPressSite::READY, 'database_id' => $database->id, 'version' => '6.8.1', 'core_minor' => '6.8.3',
            'plugins' => self::INVENTORY['plugins'], 'themes' => self::INVENTORY['themes']]);

        return $website;
    }

    private function lastJob(AgentAction $action): AgentJob
    {
        return AgentJob::where('action', $action)->latest('id')->firstOrFail();
    }

    public function test_a_new_website_with_wordpress_installs_it_when_the_website_runs(): void
    {
        $customer = $this->customer();
        $this->server(['agent_features' => [WordPress::FEATURE]]);
        $image = $this->image();
        $this->actingWith($this->user(Role::Owner, $customer));

        $response = $this->postJson('/api/v1/websites', ['name' => 'blog.example', 'imageId' => $image->id, 'planId' => $customer->plan_id, 'application' => 'WordPress',
            'wordpress' => ['title' => 'My blog', 'adminUser' => 'jane', 'adminEmail' => 'Jane@Blog.example', 'locale' => 'de_DE']])
            ->assertAccepted()->assertJsonPath('job.action', 'CreateWebsite')
            ->assertJsonPath('wordpress.admin.user', 'jane')->assertJsonPath('wordpress.admin.loginUrl', 'https://blog.example/wp-login.php');
        $password = $response->json('wordpress.admin.password');
        $this->assertMatchesRegularExpression('/^[A-Za-z0-9]{24}$/', $password);

        // The panel makes a MariaDB database server, the database and its user.
        $website = Website::findOrFail($response->json('website.id'));
        $database = Database::where('website_id', $website->id)->firstOrFail();
        $this->assertSame([$website->unix_user.'_wp', 'mariadb'], [$database->name, $database->engine]);
        $this->assertSame('mariadb', DatabaseServer::findOrFail($database->database_server_id)->engine);
        $this->assertDatabaseHas('database_users', ['website_id' => $website->id, 'name' => $database->name, 'access' => 'website']);
        $site = $website->wordpress;
        $this->assertSame(WordPressSite::INSTALLING, $site->status);
        // The install request waits, encrypted, for the website.
        $this->assertStringNotContainsString($password, (string) $site->getRawOriginal('pending_install'));
        $this->assertFalse(AgentJob::where('action', AgentAction::InstallWordPress)->exists());

        $this->finish(AgentJob::findOrFail($response->json('job.id')), JobState::Succeeded);
        $install = $this->lastJob(AgentAction::InstallWordPress);
        $this->assertSame($website->id, $install->website_id);
        $this->assertSame(['My blog', 'jane', 'jane@blog.example', 'de_DE', 'https://blog.example', $password, $database->name],
            [$install->payload['title'], $install->payload['adminUser'], $install->payload['adminEmail'], $install->payload['locale'], $install->payload['url'], $install->payload['adminPassword'], $install->payload['databaseName']]);
        $this->assertNull($site->fresh()->pending_install);

        $this->finish($install, JobState::Succeeded, ['wordpress' => self::INVENTORY]);
        // The panel does not keep the passwords.
        $this->assertArrayNotHasKey('adminPassword', $install->fresh()->payload);
        $this->assertArrayNotHasKey('databasePassword', $install->fresh()->payload);
        $this->getJson("/api/v1/websites/{$website->id}/wordpress")->assertOk()
            ->assertJsonPath('wordpress.status', 'ready')->assertJsonPath('wordpress.version', '6.8.1')->assertJsonPath('wordpress.coreMinor', '6.8.3')
            ->assertJsonPath('wordpress.plugins.0.update', '5.4')->assertJsonPath('wordpress.adminUrl', 'https://acme.example/wp-admin/')
            ->assertJsonPath('wordpress.autoUpdates.core', 'minor')->assertJsonPath('wordpress.autoUpdates.plugins', false);
    }

    public function test_the_panel_refuses_wordpress_that_cannot_work(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $this->actingWith($this->user(Role::Owner, $customer));
        $wordpress = ['title' => 'My blog', 'adminUser' => 'jane', 'adminEmail' => 'jane@blog.example'];

        // An agent with no WP-CLI.
        $this->postJson('/api/v1/websites', ['name' => 'blog.example', 'imageId' => $image->id, 'wordpress' => $wordpress])->assertJsonValidationErrors('wordpress');
        $server->update(['agent_features' => [WordPress::FEATURE]]);
        // A runtime with no PHP.
        $node = $this->image(['name' => 'Node.js 22', 'family' => 'node', 'php_version' => null, 'reference' => 'ghcr.io/cloudfactory/node-22', 'tag' => '22.0.0-1']);
        $this->postJson('/api/v1/websites', ['name' => 'blog.example', 'imageId' => $node->id, 'command' => 'node server.js', 'wordpress' => $wordpress])->assertJsonValidationErrors('wordpress');
        // A user name that attackers try first, and a token with no database scope.
        $this->postJson('/api/v1/websites', ['name' => 'blog.example', 'imageId' => $image->id, 'wordpress' => ['adminUser' => 'admin'] + $wordpress])->assertJsonValidationErrors('wordpress.adminUser');
        $this->actingWith($this->user(Role::Owner, $customer), ['websites:write']);
        $this->postJson('/api/v1/websites', ['name' => 'blog.example', 'imageId' => $image->id, 'wordpress' => $wordpress])->assertForbidden();
        $this->assertSame(0, Website::count());
    }

    public function test_an_install_on_a_website_that_runs_and_a_failed_install(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $first = $this->postJson("/api/v1/websites/{$website->id}/wordpress", ['title' => 'Acme', 'adminUser' => 'owner', 'adminEmail' => 'owner@acme.example'])
            ->assertAccepted()->assertJsonPath('job.action', 'InstallWordPress')->assertJsonPath('wordpress.status', 'installing')->json();
        $this->assertSame('en_GB', $this->lastJob(AgentAction::InstallWordPress)->payload['locale']);
        // One WordPress job at a time.
        $this->postJson("/api/v1/websites/{$website->id}/wordpress", ['title' => 'Acme', 'adminUser' => 'owner', 'adminEmail' => 'owner@acme.example'])->assertJsonValidationErrors('wordpress');

        $this->finish(AgentJob::findOrFail($first['job']['id']), JobState::Failed, [], 'Check the website: the document root /app/public has files; WordPress installs only in an empty document root');
        $this->getJson("/api/v1/websites/{$website->id}/wordpress")->assertJsonPath('wordpress.status', 'failed')
            ->assertJsonPath('wordpress.error', 'Check the website: the document root /app/public has files; WordPress installs only in an empty document root');

        // A new try uses the same database, with a new password.
        $second = $this->postJson("/api/v1/websites/{$website->id}/wordpress", ['title' => 'Acme', 'adminUser' => 'owner', 'adminEmail' => 'owner@acme.example'])->assertAccepted()->json();
        $this->assertNotSame($first['admin']['password'], $second['admin']['password']);
        $this->assertSame(1, $website->databases()->count());

        // WordPress installs only on a website that runs.
        $this->finish(AgentJob::findOrFail($second['job']['id']), JobState::Failed, [], 'Install WordPress: wp core install: Error: no connection');
        $website->forceFill(['status' => 'stopped'])->save();
        $this->postJson("/api/v1/websites/{$website->id}/wordpress", ['title' => 'Acme', 'adminUser' => 'owner', 'adminEmail' => 'owner@acme.example'])->assertStatus(409);
    }

    public function test_an_update_has_a_restore_point_and_the_result_of_the_agent(): void
    {
        $website = $this->wordpress();
        $this->actingWith($this->user(Role::Owner, $website->customer));

        // The panel knows only the plugins that the agent reported.
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", ['plugins' => ['hello']])->assertJsonValidationErrors('plugins');
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", ['plugins' => ['--all']])->assertJsonValidationErrors('plugins.0');
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", [])->assertJsonValidationErrors('core');

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", ['all' => true])->assertAccepted()->json('job.id');
        $job = AgentJob::findOrFail($jobId);
        $server = DatabaseServer::firstOrFail();
        $this->assertSame(['minor', ['akismet'], [], ['engine' => 'mariadb', 'name' => 'acme_x7k2_wp', 'databaseServer' => $server->id], WordPress::HEALTH_CHECK],
            [$job->payload['core'], $job->payload['plugins'], $job->payload['themes'], $job->payload['database'], $job->payload['healthCheck']]);
        $this->assertSame(AgentAction::UpdateWordPress->steps(), $job->steps);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Updated WordPress', 'target' => 'acme.example: WordPress (minor), akismet']);
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/restore")->assertJsonValidationErrors('wordpress');

        $after = ['version' => '6.8.3', 'coreMinor' => null, 'plugins' => [['update' => null, 'version' => '5.4'] + self::INVENTORY['plugins'][0]]] + self::INVENTORY;
        unset($after['coreMinor']);
        $this->finish($job, JobState::Succeeded, ['before' => self::INVENTORY, 'wordpress' => $after]);
        $site = $website->wordpress()->first();
        $this->assertSame(['6.8.3', null, null], [$site->version, $site->core_minor, $site->plugins[0]['update']]);
        $this->assertSame(['succeeded', '6.8.1', '6.8.3', false], [$site->last_update['state'], $site->last_update['from'], $site->last_update['to'], $site->last_update['automatic']]);
        $this->assertSame(['6.8.1', true], [$site->restore_point['version'], $site->restore_point['database']]);

        // A failed update after the restore point: the agent put it back.
        $jobId = $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", ['core' => 'latest'])->assertAccepted()->json('job.id');
        $this->finish(AgentJob::findOrFail($jobId), JobState::Failed, [], 'Health check: the health check failed after the update: 12 of 30 checks failed, last error: status 500; the agent put back the restore point', step: 5);
        $site->refresh();
        $this->assertSame(['rolledBack', '6.8.3'], [$site->last_update['state'], $site->restore_point['version']]);
        $this->assertStringContainsString('put back the restore point', $site->error);

        // A roll back of the last update.
        $jobId = $this->postJson("/api/v1/websites/{$website->id}/wordpress/restore")->assertAccepted()->assertJsonPath('job.action', 'RestoreWordPress')->json('job.id');
        $this->assertSame(['engine' => 'mariadb', 'name' => 'acme_x7k2_wp', 'databaseServer' => $server->id], AgentJob::findOrFail($jobId)->payload['database']);
        $this->finish(AgentJob::findOrFail($jobId), JobState::Succeeded, ['wordpress' => self::INVENTORY]);
        $this->assertNotNull($site->fresh()->restore_point['restoredAt']);
        $this->assertSame('6.8.1', $site->fresh()->version);
    }

    public function test_a_failed_update_before_the_restore_point_keeps_the_old_restore_point(): void
    {
        $website = $this->wordpress();
        $website->wordpress->update(['restore_point' => ['at' => '2026-11-01T00:00:00Z', 'version' => '6.7', 'database' => true]]);
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $jobId = $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", ['plugins' => ['akismet']])->json('job.id');
        $this->finish(AgentJob::findOrFail($jobId), JobState::Failed, [], 'Save a restore point: save the files: exit status 2', step: 1);
        $site = $website->wordpress()->first();
        $this->assertSame(['failed', '6.7'], [$site->last_update['state'], $site->restore_point['version']]);
    }

    public function test_automatic_updates_run_in_the_maintenance_window(): void
    {
        $website = $this->wordpress();
        $website->server->update(['maintenance_window' => '02:00-05:00']);
        $site = $website->wordpress;
        $site->update(['auto_update_plugins' => true]);

        Carbon::setTestNow('2026-11-22 12:00:00');
        $this->artisan('cloudfactory:wordpress-updates')->assertSuccessful();
        $this->assertFalse(AgentJob::where('action', AgentAction::CheckWordPress)->exists());

        Carbon::setTestNow('2026-11-22 03:00:00');
        $this->artisan('cloudfactory:wordpress-updates')->assertSuccessful();
        $check = $this->lastJob(AgentAction::CheckWordPress);
        $this->assertTrue($check->payload['automatic']);
        // One check a day.
        $this->artisan('cloudfactory:wordpress-updates')->assertSuccessful();
        $this->assertSame(1, AgentJob::where('action', AgentAction::CheckWordPress)->count());

        $this->finish($check, JobState::Succeeded, ['wordpress' => self::INVENTORY]);
        $update = $this->lastJob(AgentAction::UpdateWordPress);
        $this->assertSame(['minor', ['akismet'], [], true], [$update->payload['core'], $update->payload['plugins'], $update->payload['themes'], $update->payload['automatic']]);
        $this->finish($update, JobState::Succeeded, ['before' => self::INVENTORY, 'wordpress' => self::INVENTORY]);
        $this->assertTrue($site->fresh()->last_update['automatic']);

        // No automatic updates: no check.
        Carbon::setTestNow('2026-11-23 03:00:00');
        $site->update(['auto_update_core' => 'off', 'auto_update_plugins' => false]);
        $this->artisan('cloudfactory:wordpress-updates')->assertSuccessful();
        $this->assertSame(1, AgentJob::where('action', AgentAction::CheckWordPress)->count());
        Carbon::setTestNow();
    }

    public function test_the_settings_of_the_automatic_updates(): void
    {
        $website = $this->wordpress();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->patchJson("/api/v1/websites/{$website->id}/wordpress", ['autoUpdates' => ['core' => 'all', 'themes' => true]])->assertOk()
            ->assertJsonPath('wordpress.autoUpdates', ['core' => 'all', 'plugins' => false, 'themes' => true]);
        $this->patchJson("/api/v1/websites/{$website->id}/wordpress", ['autoUpdates' => ['core' => 'nightly']])->assertJsonValidationErrors('autoUpdates.core');
        $this->assertDatabaseHas('audit_entries', ['action' => 'Changed WordPress updates', 'target' => 'acme.example: WordPress all, plugins off, themes on']);
    }

    public function test_a_check_finds_wordpress_that_the_panel_did_not_install(): void
    {
        $website = $this->site();
        $server = $this->databaseServerOf($website);
        $database = $website->databases()->create(['engine' => 'mariadb', 'name' => 'acme_x7k2_wp', 'database_server_id' => $server->id]);
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $jobId = $this->postJson("/api/v1/websites/{$website->id}/wordpress/check")->assertAccepted()->assertJsonPath('wordpress.status', 'checking')->json('job.id');
        $this->finish(AgentJob::findOrFail($jobId), JobState::Succeeded, ['wordpress' => self::INVENTORY]);
        $this->assertSame([WordPressSite::READY, $database->id], [$website->wordpress()->first()->status, $website->wordpress()->first()->database_id]);

        // No WordPress: the panel forgets the site.
        $other = $this->website($website->customer, $website->server, $website->image, 'shop.example');
        $jobId = $this->postJson("/api/v1/websites/{$other->id}/wordpress/check")->assertAccepted()->json('job.id');
        $this->finish(AgentJob::findOrFail($jobId), JobState::Failed, [], 'Check WordPress: WordPress does not operate in /app/public');
        $this->assertNull($other->wordpress()->first());
        $this->getJson("/api/v1/websites/{$other->id}/wordpress")->assertOk()->assertJsonPath('wordpress', null);
    }

    public function test_wp_cli_keeps_no_passwords_and_gives_the_output(): void
    {
        $website = $this->wordpress();
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $response = $this->postJson("/api/v1/websites/{$website->id}/wordpress/cli", ['args' => ['user', 'update', '1', '--user_pass=Secret123', '--display_name=Jane Doe']])
            ->assertAccepted()->assertJsonPath('job.action', 'RunWpCli')
            ->assertJsonPath('command.command', "wp user update 1 --user_pass=*** '--display_name=Jane Doe'")->assertJsonPath('command.status', 'running');
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(['user', 'update', '1', '--user_pass=Secret123', '--display_name=Jane Doe'], $job->payload['args']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Ran WP-CLI command', 'target' => "acme.example: wp user update 1 --user_pass=*** '--display_name=Jane Doe'"]);

        $this->finish($job, JobState::Succeeded, ['exitCode' => 0, 'output' => 'Success: Updated user 1.', 'ranAt' => '2026-11-22T10:00:00Z', 'wordpress' => self::INVENTORY]);
        $this->assertArrayNotHasKey('args', $job->fresh()->payload);
        $this->getJson("/api/v1/websites/{$website->id}/wordpress/commands")->assertOk()
            ->assertJsonPath('data.0.exitCode', 0)->assertJsonPath('data.0.output', 'Success: Updated user 1.')->assertJsonPath('data.0.status', 'done');

        foreach ([[], ['shell'], ['--debug', 'db', 'cli'], ['eval', "echo 1;\necho 2;"], array_fill(0, 41, 'x')] as $bad) {
            $this->postJson("/api/v1/websites/{$website->id}/wordpress/cli", ['args' => $bad])->assertJsonValidationErrors('args');
        }
        $website->forceFill(['status' => 'stopped'])->save();
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/cli", ['args' => ['plugin', 'list']])->assertStatus(409);
    }

    public function test_only_users_of_the_customer_see_wordpress(): void
    {
        $website = $this->wordpress();
        $this->actingWith($this->user(Role::Owner, $this->customer(null, ['name' => 'Other Ltd', 'email' => 'it@other.example'])));
        $this->getJson("/api/v1/websites/{$website->id}/wordpress")->assertNotFound();
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/cli", ['args' => ['plugin', 'list']])->assertNotFound();
        $this->postJson("/api/v1/websites/{$website->id}/wordpress/updates", ['all' => true])->assertNotFound();
    }
}
