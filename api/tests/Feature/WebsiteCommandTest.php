<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\Website;
use App\Models\WebsiteCommand;
use App\Services\JobOutcome;
use Tests\TestCase;

/** Run a command in the container of a website, and the command log (FR-802). */
class WebsiteCommandTest extends TestCase
{
    private function site(array $attributes = []): Website
    {
        return $this->website($this->customer(), $this->server(), $this->image(), 'acme.example', $attributes);
    }

    private function finish(string $jobId, JobState $state, array $result = [], ?string $error = null): void
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->started($job);
        app(JobOutcome::class)->finish($job, $state, $error, $result);
    }

    public function test_a_command_runs_in_the_container_and_the_command_log_records_it(): void
    {
        $website = $this->site();
        $owner = $this->actingWith($this->user(Role::Owner, $website->customer));
        $base = "/api/v1/websites/{$website->id}/commands";

        $answer = $this->withServerVariables(['REMOTE_ADDR' => '198.51.100.7'])
            ->postJson($base, ['command' => '  php artisan migrate --force ', 'timeoutSeconds' => 120])
            ->assertAccepted()->assertJsonPath('command.state', 'running')->assertJsonPath('command.command', 'php artisan migrate --force')
            ->assertJsonPath('command.actor', $owner->name)->assertJsonPath('command.ip', '198.51.100.7')->assertJsonPath('command.timeoutSeconds', 120)
            ->assertJsonPath('job.action', 'RunCommand')->json();
        $job = AgentJob::findOrFail($answer['job']['id']);
        $this->assertSame(['websiteId' => $website->id, 'command' => 'php artisan migrate --force', 'timeoutSeconds' => 120], $job->payload);
        $this->assertSame($job->id, $answer['command']['jobId']);
        $this->assertSame(1, AuditEntry::where('action', 'Ran command')->where('target', 'acme.example: php artisan migrate --force')->count());

        // The job succeeds when the command ran. The entry has the exit code and the output.
        $this->finish($job->id, JobState::Succeeded, ['exitCode' => 1, 'output' => "Migrating\nSQLSTATE[HY000] error", 'timedOut' => false, 'durationMs' => 1200]);
        $this->getJson("{$base}/{$answer['command']['id']}")->assertOk()
            ->assertJsonPath('command.state', 'done')->assertJsonPath('command.exitCode', 1)->assertJsonPath('command.timedOut', false)
            ->assertJsonPath('command.output', "Migrating\nSQLSTATE[HY000] error");
        $this->assertNotNull(WebsiteCommand::find($answer['command']['id'])->finished_at);

        // With no time limit, the command gets 600 seconds. A command that does not start fails.
        $second = $this->postJson($base, ['command' => 'php -v'])->assertAccepted()->json();
        $this->assertSame(600, AgentJob::findOrFail($second['job']['id'])->payload['timeoutSeconds']);
        $this->finish($second['job']['id'], JobState::Failed, [], 'runuser -u acme_x7k2 -- podman exec: exit status 125');
        $this->getJson("{$base}/{$second['command']['id']}")->assertJsonPath('command.state', 'failed')->assertJsonPath('command.exitCode', null)
            ->assertJsonPath('command.output', 'The step "Run the command" failed on the server. Your provider can see the details.');

        // The list has the newest command first, without the output. A search finds the text.
        $list = $this->getJson($base)->assertOk()->assertJsonCount(2, 'data')->assertJsonPath('meta.total', 2)->json('data');
        $this->assertSame(['php -v', 'php artisan migrate --force'], array_column($list, 'command'));
        $this->assertArrayNotHasKey('output', $list[0]);
        $this->getJson("{$base}?q=migrate")->assertJsonCount(1, 'data')->assertJsonPath('data.0.exitCode', 1);
        $this->getJson("{$base}?source=ssh")->assertJsonValidationErrors('source');

        // A later result of the same job does not change a complete entry.
        $this->finish($job->id, JobState::Succeeded, ['exitCode' => 0, 'output' => 'again']);
        $this->getJson("{$base}/{$answer['command']['id']}")->assertJsonPath('command.exitCode', 1);
    }

    public function test_the_command_log_keeps_the_end_of_a_long_output_and_the_time_limit(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $id = $this->postJson("/api/v1/websites/{$website->id}/commands", ['command' => 'yes | head -n 100000'])->json('job.id');
        $this->finish($id, JobState::Succeeded, ['exitCode' => 124, 'output' => str_repeat('y', WebsiteCommand::OUTPUT_MAX).'end', 'timedOut' => true]);
        $c = WebsiteCommand::firstOrFail();
        $this->assertSame([124, true, WebsiteCommand::OUTPUT_MAX], [$c->exit_code, $c->timed_out, strlen($c->output)]);
        $this->assertStringEndsWith('end', $c->output);
    }

    public function test_the_api_refuses_a_command_that_cannot_run(): void
    {
        $website = $this->site();
        $base = "/api/v1/websites/{$website->id}/commands";
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $this->postJson($base, ['command' => "true\nrm -rf /"])->assertJsonValidationErrors('command');
        $this->postJson($base, ['command' => ''])->assertJsonValidationErrors('command');
        $this->postJson($base, ['command' => 'true', 'timeoutSeconds' => 0])->assertJsonValidationErrors('timeoutSeconds');
        $this->postJson($base, ['command' => 'true', 'timeoutSeconds' => 3601])->assertJsonValidationErrors('timeoutSeconds');

        // Two commands of one website run at the same time.
        $this->postJson($base, ['command' => 'sleep 60'])->assertAccepted();
        $this->postJson($base, ['command' => 'sleep 60'])->assertAccepted();
        $this->postJson($base, ['command' => 'sleep 60'])->assertStatus(409)->assertJsonPath('message', '2 commands of this website run now. Wait until one ends.');
        $this->assertSame(2, WebsiteCommand::count());
        $this->assertSame(2, AgentJob::where('action', AgentAction::RunCommand)->count());

        $website->forceFill(['status' => 'stopped'])->save();
        $this->postJson($base, ['command' => 'true'])->assertStatus(409)->assertJsonPath('message', 'The website does not run. Start it before you run a command.');
        $website->forceFill(['status' => 'running'])->save();
        $website->customer->update(['suspended' => true]);
        $this->postJson($base, ['command' => 'true'])->assertStatus(409);
        // The command log stays readable.
        $this->getJson($base)->assertOk()->assertJsonCount(2, 'data');

        // A user of a different customer does not see the website.
        $other = $this->customer(null, ['name' => 'Other', 'email' => 'other@example.com']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson($base)->assertNotFound();
        $this->getJson("{$base}/".WebsiteCommand::firstOrFail()->id)->assertNotFound();
        $this->postJson($base, ['command' => 'true'])->assertNotFound();

        // A token with only the read scope reads the log, and cannot run a command.
        $this->app['auth']->forgetGuards();
        $website->customer->update(['suspended' => false]);
        $this->actingWith($this->user(Role::Owner, $website->customer), ['websites:read']);
        $this->getJson($base)->assertOk();
        $this->postJson($base, ['command' => 'true'])->assertForbidden();
    }

    public function test_a_customer_does_not_see_the_name_and_the_address_of_an_administrator_in_the_command_log(): void
    {
        $website = $this->site();
        $admin = $this->actingWith($this->user());
        $base = "/api/v1/websites/{$website->id}/commands";
        $id = $this->withServerVariables(['REMOTE_ADDR' => '198.51.100.1'])->postJson($base, ['command' => 'php artisan cache:clear'])->json('command.id');
        $this->getJson("{$base}/{$id}")->assertJsonPath('command.actor', $admin->name)->assertJsonPath('command.ip', '198.51.100.1');

        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->getJson($base)->assertJsonPath('data.0.actor', 'Administrator')->assertJsonPath('data.0.ip', null);
        $this->getJson("{$base}/{$id}")->assertJsonPath('command.actor', 'Administrator')->assertJsonPath('command.ip', null);
    }
}
