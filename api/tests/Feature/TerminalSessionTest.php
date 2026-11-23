<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\TerminalSession;
use App\Models\Website;
use App\Models\WebsiteCommand;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use App\Services\TerminalTickets;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The web terminal of a website, and its commands in the command log (FR-802). */
class TerminalSessionTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private function site(array $server = []): Website
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cloudfactory-test-pki', 'cloudfactory.panel_domain' => 'panel.example.com']);

        return $this->website($this->customer(), $this->server($server + ['certificate_pem' => 'test', 'certificate_fingerprint' => str_repeat('ab', 32),
            'agent_token_hash' => hash('sha256', 'cfa_secret')]), $this->image(), 'acme.example');
    }

    private function finish(string $jobId, JobState $state, ?string $error = null): void
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->started($job);
        app(JobOutcome::class)->finish($job, $state, $error, $state === JobState::Succeeded ? ['sessionId' => $job->payload['sessionId']] : []);
    }

    /** A request from the agent of the website's server, through the edge proxy. */
    private function agent(): static
    {
        return $this->withToken('cfa_secret')->withHeader('X-Cloudfactory-Edge', '1')->withHeader('X-Client-Cert-Sha256', str_repeat('AB', 32));
    }

    public function test_a_user_opens_the_terminal_and_the_agent_records_the_commands_and_the_end(): void
    {
        $website = $this->site();
        $owner = $this->actingWith($this->user(Role::Owner, $website->customer));
        $base = "/api/v1/websites/{$website->id}/terminal-sessions";

        $answer = $this->withServerVariables(['REMOTE_ADDR' => '198.51.100.7'])->postJson($base, ['cols' => 120, 'rows' => 40])
            ->assertAccepted()->assertJsonPath('session.state', 'opening')->assertJsonPath('session.actor', $owner->name)
            ->assertJsonPath('session.ip', '198.51.100.7')->assertJsonPath('job.action', 'OpenTerminal')->json();
        $id = $answer['session']['id'];
        $this->assertSame("/api/terminal/{$id}/browser", $answer['path']);
        // The browser ticket is good for a short time, for this session and the role "browser".
        [$until] = explode('.', $answer['ticket']);
        $this->assertSame(TerminalTickets::make('browser', $id, (int) $until), $answer['ticket']);
        $this->assertEqualsWithDelta(now()->addMinute()->getTimestamp(), (int) $until, 2);
        $this->assertSame(1, AuditEntry::where('action', 'Opened terminal')->where('target', 'acme.example')->count());

        // The job has no ticket. The panel adds the relay and a new ticket of the agent when it sends the job.
        $job = AgentJob::findOrFail($answer['job']['id']);
        $this->assertSame(['websiteId' => $website->id, 'sessionId' => $id, 'cols' => 120, 'rows' => 40], $job->payload);
        Http::fake([self::AGENT.'OpenTerminal' => Http::response(['jobId' => $job->id])]);
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertSent(function ($r) use ($id) {
            [$until] = explode('.', $r['ticket']);

            return $r['relayUrl'] === 'https://panel.example.com' && $r['sessionId'] === $id && $r['ticket'] === TerminalTickets::make('agent', $id, (int) $until);
        });

        // The shell started. The agent sends the commands while the session is open.
        $this->finish($job->id, JobState::Succeeded);
        $this->getJson("{$base}/{$id}")->assertOk()->assertJsonPath('session.state', 'open');
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/commands", ['commands' => [
            ['command' => 'php artisan migrate:status', 'at' => '2026-11-22T10:00:01Z'],
            ['command' => 'wp plugin list', 'at' => '2026-11-22T10:00:05Z'],
        ]])->assertOk();
        // The end has the last commands and the recording. A second report of the end has no effect.
        $end = ['reason' => 'The shell ended.', 'exitCode' => 0, 'commands' => [['command' => 'exit', 'at' => '2026-11-22T10:00:09Z']],
            'recording' => "{\"version\":2,\"width\":120,\"height\":40}\n[0.5,\"o\",\"app$ \"]\n", 'recordingCut' => false,
            'startedAt' => '2026-11-22T10:00:00Z', 'endedAt' => '2026-11-22T10:00:10Z'];
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/end", $end)->assertOk();
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/end", ['reason' => 'again', 'commands' => [['command' => 'x', 'at' => '2026-11-22T10:00:11Z']]])->assertOk();
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/commands", ['commands' => [['command' => 'late', 'at' => '2026-11-22T10:00:12Z']]])->assertStatus(409);

        $this->getJson("{$base}/{$id}")->assertJsonPath('session.state', 'ended')->assertJsonPath('session.endReason', 'The shell ended.')
            ->assertJsonPath('session.exitCode', 0)->assertJsonPath('session.commandCount', 3)->assertJsonPath('session.hasRecording', true)
            ->assertJsonPath('session.endedAt', '2026-11-22T10:00:10+00:00');
        $this->get("{$base}/{$id}/recording")->assertOk()->assertHeader('Content-Type', 'application/x-asciicast')->assertSee('"o","app$ "', false);
        $this->getJson($base)->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.id', $id)->assertJsonPath('data.0.hasRecording', true);

        // The commands are in the command log of the website, with the person of the session.
        $log = $this->getJson("/api/v1/websites/{$website->id}/commands?source=terminal")->assertOk()->json('data');
        $this->assertSame(['exit', 'wp plugin list', 'php artisan migrate:status'], array_column($log, 'command'));
        $this->assertSame([$owner->name, '198.51.100.7', 'terminal', $id, 'done', null], [$log[0]['actor'], $log[0]['ip'], $log[0]['source'], $log[0]['sessionId'], $log[0]['state'], $log[0]['exitCode']]);
        $this->assertSame('2026-11-22T10:00:05+00:00', $log[1]['createdAt']);
        $this->getJson("/api/v1/websites/{$website->id}/commands?sessionId={$id}&q=wp")->assertJsonCount(1, 'data');
        $this->getJson("/api/v1/websites/{$website->id}/commands?source=ssh")->assertJsonValidationErrors('source');
    }

    public function test_the_agent_on_the_panel_server_connects_to_the_local_relay(): void
    {
        $website = $this->site(['local' => true]);
        $this->actingWith($this->user());
        $jobId = $this->postJson("/api/v1/websites/{$website->id}/terminal-sessions", ['cols' => 80, 'rows' => 24])->assertAccepted()->json('job.id');
        Http::fake([self::AGENT.'OpenTerminal' => Http::response(['jobId' => $jobId])]);
        (new SendJobToAgent($jobId))->handle(app(AgentClient::class));
        Http::assertSent(fn ($r) => $r['relayUrl'] === 'http://169.254.77.1:19082');
    }

    public function test_a_terminal_that_does_not_open_fails_with_a_public_error(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $answer = $this->postJson("/api/v1/websites/{$website->id}/terminal-sessions", ['cols' => 80, 'rows' => 24])->json();
        $this->finish($answer['job']['id'], JobState::Failed, 'runuser -u acme_x7k2 -- podman exec: exit status 125');
        $this->getJson("/api/v1/websites/{$website->id}/terminal-sessions/{$answer['session']['id']}")->assertJsonPath('session.state', 'failed')
            ->assertJsonPath('session.endReason', 'The step "Open the terminal" failed on the server. Your provider can see the details.')
            ->assertJsonPath('session.hasRecording', false);
        $this->get("/api/v1/websites/{$website->id}/terminal-sessions/{$answer['session']['id']}/recording")->assertNotFound();
    }

    public function test_the_api_refuses_a_terminal_that_cannot_open(): void
    {
        $website = $this->site();
        $base = "/api/v1/websites/{$website->id}/terminal-sessions";
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $this->postJson($base, ['cols' => 5, 'rows' => 24])->assertJsonValidationErrors('cols');
        $this->postJson($base, ['cols' => 80, 'rows' => 501])->assertJsonValidationErrors('rows');
        $this->postJson($base, [])->assertJsonValidationErrors(['cols', 'rows']);

        // Two terminals of one website are open at the same time.
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertAccepted();
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertAccepted();
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertStatus(409)->assertJsonPath('message', '2 terminals of this website are open now. Close one first.');
        $this->assertSame(2, AgentJob::where('action', AgentAction::OpenTerminal)->count());
        // A session with no end report after the longest time of a session does not hold a slot.
        TerminalSession::query()->update(['created_at' => now()->subHours(13)]);
        $this->getJson($base)->assertJsonPath('data.0.state', 'ended');
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertAccepted();

        $website->forceFill(['status' => 'stopped'])->save();
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertStatus(409)->assertJsonPath('message', 'The website does not run. Start it before you open a terminal.');
        $website->forceFill(['status' => 'running'])->save();
        $website->customer->update(['suspended' => true]);
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertStatus(409);

        // A user of a different customer does not see the website or its sessions.
        $other = $this->customer(null, ['name' => 'Other', 'email' => 'other@example.com']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson($base)->assertNotFound();
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertNotFound();

        // A token with only the read scope cannot open a terminal.
        $this->app['auth']->forgetGuards();
        $website->customer->update(['suspended' => false]);
        $this->actingWith($this->user(Role::Owner, $website->customer), ['websites:read']);
        $this->getJson($base)->assertOk();
        $this->postJson($base, ['cols' => 80, 'rows' => 24])->assertForbidden();
    }

    public function test_an_agent_reports_only_the_sessions_of_its_own_server(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $id = $this->postJson("/api/v1/websites/{$website->id}/terminal-sessions", ['cols' => 80, 'rows' => 24])->json('session.id');

        $this->server(['name' => 'web-9', 'agent_token_hash' => hash('sha256', 'cfa_other'), 'certificate_fingerprint' => str_repeat('cd', 32)]);
        $this->app['auth']->forgetGuards();
        $this->withToken('cfa_other')->withHeader('X-Cloudfactory-Edge', '1')->withHeader('X-Client-Cert-Sha256', str_repeat('CD', 32))
            ->postJson("/api/agent/v1/terminal-sessions/{$id}/commands", ['commands' => [['command' => 'ls', 'at' => '2026-11-22T10:00:01Z']]])->assertNotFound();
        // A request with no agent token.
        $this->withToken('nope')->postJson("/api/agent/v1/terminal-sessions/{$id}/end", ['reason' => 'x', 'commands' => []])->assertUnauthorized();
        $this->assertSame(0, WebsiteCommand::count());

        // A bad report.
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/commands", ['commands' => [['command' => 'ls']]])->assertJsonValidationErrors('commands.0.at');
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/end", ['commands' => []])->assertJsonValidationErrors('reason');
        // A long command is cut to the limit of the log.
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/commands", ['commands' => [['command' => str_repeat('a', 2100), 'at' => '2026-11-22T10:00:01Z']]])->assertOk();
        $this->assertSame(WebsiteCommand::TERMINAL_COMMAND_MAX, strlen(WebsiteCommand::firstOrFail()->command));
    }

    public function test_a_customer_does_not_see_the_name_and_the_address_of_an_administrator_in_the_sessions(): void
    {
        $website = $this->site();
        $admin = $this->actingWith($this->user());
        $base = "/api/v1/websites/{$website->id}/terminal-sessions";
        $id = $this->withServerVariables(['REMOTE_ADDR' => '198.51.100.1'])->postJson($base, ['cols' => 80, 'rows' => 24])->json('session.id');
        $this->getJson("{$base}/{$id}")->assertJsonPath('session.actor', $admin->name);
        $this->agent()->postJson("/api/agent/v1/terminal-sessions/{$id}/commands", ['commands' => [['command' => 'ls', 'at' => '2026-11-22T10:00:01Z']]])->assertOk();

        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->getJson($base)->assertJsonPath('data.0.actor', 'Administrator')->assertJsonPath('data.0.ip', null);
        $this->getJson("{$base}/{$id}")->assertJsonPath('session.actor', 'Administrator');
        $this->getJson("/api/v1/websites/{$website->id}/commands")->assertJsonPath('data.0.actor', 'Administrator')->assertJsonPath('data.0.ip', null);
    }
}
