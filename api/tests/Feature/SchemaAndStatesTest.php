<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\RolloutState;
use App\Enums\State;
use App\Enums\WebsiteStatus;
use App\Exceptions\StateChangeNotAllowed;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Rollout;
use App\Models\Server;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\JobDispatcher;
use App\Services\JobOutcome;
use App\Validation\InputRules;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Http\Client\RequestException;
use Illuminate\Support\Facades\Http;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\TestCase;

/**
 * The second audit: finding F04 (a job that the agent refuses), one set of input rules for the
 * panel and the agent, and the state tables of websites, jobs and rollouts (finding F06).
 */
class SchemaAndStatesTest extends TestCase
{
    private function joinedServer(): Server
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cf-test-agents-'.uniqid()]);

        return $this->server(['certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
    }

    // F04

    public function test_a_job_that_the_agent_refuses_fails_immediately_with_the_message_of_the_agent(): void
    {
        $server = $this->joinedServer();
        $website = $this->website($this->customer(), $server, $this->image(), 'acme.example', ['status' => 'stopped']);
        $website->update(['status' => WebsiteStatus::Starting]);
        $job = app(JobDispatcher::class)->dispatch($server, AgentAction::SetWebsitePower, ['websiteId' => $website->id, 'power' => 'start'], 'Start', $website);
        Http::fake(fn () => Http::response(['code' => 'invalid_argument', 'message' => 'the PHP extension "xxx" is not valid'], 400));

        // No exception: the queue does not try again.
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));

        $job->refresh();
        $this->assertSame(JobState::Failed, $job->state);
        $this->assertSame('The server refused the job: the PHP extension "xxx" is not valid', $job->error);
        $this->assertNotNull($job->finished_at);
        // The panel records the failure as for each failed job: the website has its old state.
        $this->assertSame(WebsiteStatus::Stopped, $website->fresh()->status);
        // The agent answered, thus the connection is good.
        $this->assertNull($server->fresh()->connection_error);
        $this->assertNotNull($server->fresh()->connection_checked_at);
    }

    public function test_a_refusal_with_no_message_has_the_http_status(): void
    {
        $job = app(JobDispatcher::class)->dispatch($this->joinedServer(), AgentAction::Reboot, [], 'Reboot');
        Http::fake(fn () => Http::response('404 page not found', 404));

        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));

        $this->assertSame(JobState::Failed, $job->fresh()->state);
        $this->assertStringContainsString('HTTP status 404', $job->fresh()->error);
    }

    /** @return array<string, array{int}> */
    public static function temporaryStatuses(): array
    {
        return ['request timeout' => [408], 'too many requests' => [429], 'internal' => [500], 'unavailable' => [503], 'gateway timeout' => [504]];
    }

    #[DataProvider('temporaryStatuses')]
    public function test_a_temporary_error_of_the_agent_keeps_the_job_for_a_second_attempt(int $status): void
    {
        $job = app(JobDispatcher::class)->dispatch($this->joinedServer(), AgentAction::Reboot, [], 'Reboot');
        Http::fake(fn () => Http::response(['code' => 'unavailable', 'message' => 'try again'], $status));

        try {
            (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
            $this->fail('The queue must get the exception, so that it tries again.');
        } catch (RequestException) {
            $this->assertSame(JobState::Queued, $job->fresh()->state);
        }
    }

    // One set of input rules

    public function test_the_panel_refuses_values_that_the_agent_refuses(): void
    {
        $plan = $this->plan();
        $website = $this->website($this->customer($plan), $this->server(), $this->image());
        $this->actingWith($this->user());
        $long = str_repeat('x', 41);

        // F04: before, the panel accepted an extension name of each length.
        $this->postJson('/api/v1/plans', ['name' => 'Big', 'websites' => 1, 'mailboxes' => 0, 'databases' => 0, 'phpExtensions' => [$long],
            'limits' => $plan->limits])->assertJsonValidationErrors(['phpExtensions.0' => InputRules::MESSAGES['phpExtension']]);
        // A plan from before this rule can have such a name. The owner cannot turn it on.
        $plan->update(['php_extensions' => [$long]]);
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['extensions' => [$long]])->assertJsonValidationErrors('extensions.0');

        // The agent accepts at most 8 parts, and no part that starts with a dot.
        foreach (['a/b/c/d/e/f/g/h/i', '.git', 'public/', '../x'] as $root) {
            $this->putJson("/api/v1/websites/{$website->id}/runtime", ['documentRoot' => $root])->assertJsonValidationErrors(['documentRoot' => InputRules::MESSAGES['documentRoot']]);
        }
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['documentRoot' => 'web/htdocs'])->assertAccepted();

        // The agent refuses a path with an empty part or with "/" at the end.
        $snapshot = $website->snapshots()->create(['kind' => 'manual', 'restic_id' => 'abc12345', 'size_bytes' => 1, 'databases' => [], 'mailboxes' => []]);
        foreach (['/public/', '/a//b', '/a/./b', '/a/../b', 'public'] as $path) {
            $this->postJson("/api/v1/websites/{$website->id}/snapshots/{$snapshot->id}/restore", ['scope' => 'file', 'item' => $path])->assertJsonValidationErrors('item');
        }

        // The agent counts bytes. 513 letters of 2 bytes are more than 1024 bytes.
        $image = ['name' => 'App', 'reference' => 'registry.example.com/app', 'tag' => '1.0', 'policy' => 'manual'];
        $this->postJson('/api/v1/images', [...$image, 'tag' => '-1'])->assertJsonValidationErrors(['tag' => InputRules::MESSAGES['imageTag']]);
        $this->postJson('/api/v1/images', [...$image, 'reference' => 'nginx'])->assertJsonValidationErrors(['reference' => InputRules::MESSAGES['imageReference']]);
        $id = $this->postJson('/api/v1/images', $image)->assertCreated()->json('id');
        $this->patchJson("/api/v1/images/{$id}", ['command' => [str_repeat('é', 513)]])->assertJsonValidationErrors('command.0');
        $this->patchJson("/api/v1/images/{$id}", ['command' => ["a\nb"]])->assertJsonValidationErrors('command.0');
        $this->patchJson("/api/v1/images/{$id}", ['command' => array_fill(0, InputRules::MAX_COMMAND_ARGUMENTS + 1, 'a')])->assertJsonValidationErrors('command');
        $this->patchJson("/api/v1/images/{$id}", ['command' => ['node', 'server.js']])->assertOk();
    }

    // State tables

    /**
     * Each table, written a second time. A change of a table in App\Enums must be a decision,
     * thus this list does not read the tables.
     *
     * @return array<string, array{class-string<State>, array<string, list<string>>}>
     */
    public static function tables(): array
    {
        return [
            'website' => [WebsiteStatus::class, [
                'starting' => ['running', 'stopping', 'stopped', 'failed'],
                'running' => ['stopping', 'stopped', 'starting', 'failed'],
                'stopping' => ['stopped', 'running', 'starting', 'failed'],
                'stopped' => ['starting', 'running', 'failed'],
                'failed' => ['starting'],
            ]],
            'job' => [JobState::class, [
                'queued' => ['running', 'succeeded', 'failed'],
                'running' => ['queued', 'succeeded', 'failed'],
                'succeeded' => [],
                'failed' => [],
            ]],
            'rollout' => [RolloutState::class, [
                'running' => ['paused', 'done', 'rolled_back'],
                'paused' => ['running', 'rolled_back'],
                'done' => [],
                'rolled_back' => [],
            ]],
        ];
    }

    /** A record of the model that has the state, in the given state. */
    private function recordIn(State $state): Model
    {
        static $number = 0;
        $server = Server::first() ?? $this->server();
        $number++;

        return match ($state::class) {
            WebsiteStatus::class => $this->website($this->customer(), $server, $this->image(), "w{$number}.example", ['status' => $state]),
            JobState::class => AgentJob::create(['server_id' => $server->id, 'action' => AgentAction::Reboot, 'title' => 'Reboot', 'payload' => [], 'steps' => [], 'state' => $state]),
            RolloutState::class => Rollout::create(['image_id' => $this->image()->id, 'from_tag' => '1', 'to_tag' => '2', 'state' => $state, 'stage' => 0, 'website_ids' => [], 'updated_ids' => [], 'rolled_back' => []]),
        };
    }

    /**
     * @param  class-string<State>  $enum
     * @param  array<string, list<string>>  $table
     */
    #[DataProvider('tables')]
    public function test_the_model_permits_only_the_changes_of_the_table(string $enum, array $table): void
    {
        $this->assertEqualsCanonicalizing(array_column($enum::cases(), 'value'), array_keys($table), 'The test must have each state.');

        foreach ($enum::cases() as $from) {
            foreach ($enum::cases() as $to) {
                $permitted = in_array($to->value, $table[$from->value], true);
                $change = "{$enum::what()}: {$from->value} to {$to->value}";
                $this->assertSame($permitted, $from->canChangeTo($to), $change);

                // update(): the guard of the model. The same state is no change.
                $record = $this->recordIn($from);
                $attribute = $record::stateAttribute();
                try {
                    $record->update([$attribute => $to]);
                    $this->assertTrue($permitted || $from === $to, "{$change} was saved.");
                    $this->assertSame($to, $record->fresh()->{$attribute});
                } catch (StateChangeNotAllowed $e) {
                    $this->assertFalse($permitted, "{$change} was refused.");
                    $this->assertSame(409, $e->getStatusCode());
                    $this->assertSame($from, $record->fresh()->{$attribute}, "{$change} changed the record.");
                }

                // moveTo(): for a late result. It does not throw.
                $record = $this->recordIn($from);
                $this->assertSame($permitted || $from === $to, $record->moveTo($to), $change);
                $this->assertSame($permitted ? $to : $from, $record->fresh()->{$attribute}, $change);
                $this->assertSame($permitted ? $to : $from, $record->{$attribute}, $change);

                // changeTo(): for a request of a user. The same state is refused too.
                $record = $this->recordIn($from);
                try {
                    $record->changeTo($to);
                    $this->assertTrue($permitted, "{$change} was done.");
                } catch (StateChangeNotAllowed) {
                    $this->assertFalse($permitted, "{$change} was refused.");
                }
                $this->assertSame($permitted ? $to : $from, $record->fresh()->{$attribute}, $change);
            }
        }
    }

    public function test_the_guard_uses_the_state_in_the_database(): void
    {
        $job = $this->recordIn(JobState::Queued);
        // A different request ends the job. This copy is old.
        AgentJob::findOrFail($job->id)->update(['state' => JobState::Succeeded]);

        $this->assertSame(JobState::Queued, $job->state);
        $this->assertFalse($job->moveTo(JobState::Running));
        $this->assertSame(JobState::Succeeded, $job->fresh()->state);
        $this->expectException(StateChangeNotAllowed::class);
        $this->expectExceptionMessage('The job is succeeded. It cannot change to failed.');
        $job->update(['state' => JobState::Failed]);
    }

    public function test_a_change_of_other_attributes_is_not_checked(): void
    {
        $job = $this->recordIn(JobState::Failed);

        $job->update(['error' => 'A new text.']);
        $this->assertSame('A new text.', $job->fresh()->error);
    }

    public function test_a_late_job_result_does_not_change_a_website_that_the_panel_could_not_make(): void
    {
        $server = $this->server();
        $website = $this->website($this->customer(), $server, $this->image(), 'acme.example', ['status' => 'starting']);
        $jobs = app(JobDispatcher::class);
        $create = $jobs->dispatch($server, AgentAction::CreateWebsite, [], 'Create', $website);
        $stop = $jobs->dispatch($server, AgentAction::SetWebsitePower, ['websiteId' => $website->id, 'power' => 'stop'], 'Stop', $website);
        $outcome = app(JobOutcome::class);

        $outcome->finish($create, JobState::Failed, 'no disk space');
        $this->assertSame(WebsiteStatus::Failed, $website->fresh()->status);
        // Before, the failed stop showed the website as "running". It has no container.
        $outcome->finish($stop, JobState::Failed, 'no such unit');
        $this->assertSame(JobState::Failed, $stop->fresh()->state);
        $this->assertSame(WebsiteStatus::Failed, $website->fresh()->status);
    }

    public function test_the_api_gives_409_for_a_power_action_that_the_state_does_not_permit(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->actingWith($this->user());

        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'stop'])->assertAccepted();
        app(JobOutcome::class)->finish(AgentJob::firstOrFail(), JobState::Succeeded);
        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'stop'])
            ->assertStatus(409)->assertJsonPath('message', 'The website is stopped. It cannot change to stopping.');
        // A refused action starts no job.
        $this->assertSame(1, AgentJob::count());
        $this->postJson("/api/v1/websites/{$website->id}/power", ['action' => 'start'])->assertAccepted();

        // The panel could not make this website. It has no container to start or to stop.
        $failed = $this->website($website->customer, $website->server, $website->image, 'failed.example', ['status' => 'failed']);
        $this->expectException(StateChangeNotAllowed::class);
        $this->expectExceptionMessage('The website is failed. It cannot change to stopping.');
        $failed->update(['status' => WebsiteStatus::Stopping]);
    }

    // F06

    private function rollout(): Rollout
    {
        $image = $this->image(['available_tag' => '8.4.0-2', 'available_digest' => 'sha256:'.str_repeat('a', 64)]);
        $customer = $this->customer();
        $server = $this->server();
        foreach (range(1, 4) as $i) {
            $this->website($customer, $server, $image, "site{$i}.example");
        }
        $this->actingWith($this->user());

        return Rollout::findOrFail($this->postJson('/api/v1/rollouts', ['imageId' => $image->id])->assertCreated()->json('id'));
    }

    private function finishOpenJobs(Rollout $rollout): void
    {
        foreach (AgentJob::where('rollout_id', $rollout->id)->where('state', 'queued')->get() as $job) {
            app(JobOutcome::class)->finish($job, JobState::Succeeded);
        }
    }

    public function test_a_rolled_back_rollout_does_not_resume(): void
    {
        $rollout = $this->rollout();
        $this->finishOpenJobs($rollout); // The canary. The second stage starts.

        $this->postJson("/api/v1/rollouts/{$rollout->id}/roll-back")->assertOk()->assertJsonPath('state', 'rolled_back');
        $jobs = AgentJob::count();

        // Before, "resume" started the next stage again with the new image.
        $this->postJson("/api/v1/rollouts/{$rollout->id}/resume")
            ->assertStatus(409)->assertJsonPath('message', 'The rollout is rolled back. It cannot change to running.');
        // Before, a second "roll back" ran again.
        $this->postJson("/api/v1/rollouts/{$rollout->id}/roll-back")
            ->assertStatus(409)->assertJsonPath('message', 'The rollout is rolled back already.');
        $this->postJson("/api/v1/rollouts/{$rollout->id}/pause")->assertStatus(409);
        $this->assertSame(RolloutState::RolledBack, $rollout->fresh()->state);
        $this->assertSame($jobs, AgentJob::count(), 'A refused action started a job.');
    }

    public function test_a_complete_rollout_does_not_change(): void
    {
        $rollout = $this->rollout();
        for ($stage = 0; $stage < 4; $stage++) {
            $this->finishOpenJobs($rollout);
        }
        $this->assertSame(RolloutState::Done, $rollout->fresh()->state);
        $jobs = AgentJob::count();

        // Before, "pause" made the complete rollout "paused", and no new rollout could start.
        foreach (['pause', 'resume', 'roll-back'] as $action) {
            $this->postJson("/api/v1/rollouts/{$rollout->id}/{$action}")->assertStatus(409);
        }
        $this->assertSame(RolloutState::Done, $rollout->fresh()->state);
        $this->assertSame($jobs, AgentJob::count());
        $rollout->image->update(['available_tag' => '8.4.0-3']);
        $this->postJson('/api/v1/rollouts', ['imageId' => $rollout->image_id])->assertCreated();
    }

    public function test_pause_and_resume_are_done_one_time_only(): void
    {
        $rollout = $this->rollout();

        $this->postJson("/api/v1/rollouts/{$rollout->id}/resume")
            ->assertStatus(409)->assertJsonPath('message', 'The rollout is running already.');
        $this->postJson("/api/v1/rollouts/{$rollout->id}/pause")->assertOk()->assertJsonPath('state', 'paused');
        $this->postJson("/api/v1/rollouts/{$rollout->id}/pause")
            ->assertStatus(409)->assertJsonPath('message', 'The rollout is paused already.');

        // The canary ends while the rollout is paused. The next stage starts at "resume", one time.
        $this->finishOpenJobs($rollout);
        $this->assertSame(1, AgentJob::count());
        $this->postJson("/api/v1/rollouts/{$rollout->id}/resume")->assertOk()->assertJsonPath('state', 'running');
        $this->postJson("/api/v1/rollouts/{$rollout->id}/resume")->assertStatus(409);
        $this->assertSame(2, AgentJob::count());
    }

    public function test_a_roll_back_keeps_the_result_of_a_job_that_ended(): void
    {
        $rollout = $this->rollout();
        $canary = AgentJob::where('rollout_id', $rollout->id)->firstOrFail();

        $this->postJson("/api/v1/rollouts/{$rollout->id}/roll-back")->assertOk();

        $this->assertSame(JobState::Failed, $canary->fresh()->state);
        $this->assertSame('Stopped by the roll back.', $canary->fresh()->error);
        $this->assertSame('8.4.0-1', Website::findOrFail($canary->website_id)->image_tag);
    }
}
