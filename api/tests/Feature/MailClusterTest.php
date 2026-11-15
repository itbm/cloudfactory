<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\MailCluster;
use App\Models\Server;
use App\Services\JobDispatcher;
use App\Services\JobOutcome;
use App\Services\MailClusters;
use Illuminate\Support\Collection;
use Tests\TestCase;

/**
 * Mail clusters. The panel makes a cluster of three or five servers in steps, and starts a
 * step when the jobs of the step before it are complete.
 */
class MailClusterTest extends TestCase
{
    private const WG = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';

    private const PEM = "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n-----END PUBLIC KEY-----\n";

    /** @return Collection<int, Server> */
    private function servers(int $count = 3): Collection
    {
        return collect(range(1, $count))->map(fn (int $i) => $this->server(['name' => "mail-{$i}", 'roles' => ['email']]));
    }

    private function body(Collection $servers): array
    {
        return [
            'name' => 'eu',
            'hostname' => 'mail.example.net',
            'members' => $servers->values()->map(fn (Server $s, int $i) => [
                'serverId' => $s->id, 'hostname' => 'm'.($i + 1).'.example.net', 'endpoint' => '192.0.2.'.($i + 1), 'region' => $i,
            ])->all(),
        ];
    }

    /** @return Collection<int, AgentJob> */
    private function open(AgentAction $action): Collection
    {
        return AgentJob::where('action', $action->value)->where('state', JobState::Queued->value)->get();
    }

    private function finish(AgentJob $job, JobState $state = JobState::Succeeded, array $result = []): void
    {
        app(JobOutcome::class)->finish($job->fresh(), $state, $state === JobState::Failed ? 'the agent stopped' : null, $result);
    }

    private function keys(): array
    {
        return ['keys' => ['wireguardKey' => self::WG, 'keepKey' => self::PEM, 'nodeKey' => self::PEM]];
    }

    public function test_the_panel_makes_a_cluster_in_steps(): void
    {
        $this->actingWith($this->user());
        $servers = $this->servers();
        $response = $this->postJson('/api/v1/mail-clusters', $this->body($servers))->assertAccepted()
            ->assertJsonPath('status', 'setting_up')->assertJsonPath('phase', 'prepare')
            ->assertJsonPath('members.0.address', '10.77.16.1')->assertJsonPath('members.2.step', 'new')
            ->assertJsonMissingPath('secrets');
        $this->assertCount(6, $response->json('records'));
        $id = $response->json('id');

        // 1. Each server makes its keys.
        $prepare = $this->open(AgentAction::PrepareMailNode);
        $this->assertCount(3, $prepare);
        foreach ($prepare as $job) {
            $this->finish($job, result: $this->keys());
        }
        // 2. The first server starts the database cluster.
        $this->getJson("/api/v1/mail-clusters/{$id}")->assertJsonPath('phase', 'bootstrap');
        $bootstrap = $this->open(AgentAction::SetMailNode);
        $this->assertCount(1, $bootstrap);
        $this->assertSame($servers[0]->id, $bootstrap[0]->server_id);
        $this->assertTrue($bootstrap[0]->payload['bootstrap']);
        $this->assertCount(3, $bootstrap[0]->payload['members']);
        $this->assertSame(self::WG, $bootstrap[0]->payload['members'][2]['wireguardKey']);
        // The job keeps no secrets. The panel adds them when it sends the job.
        $this->assertArrayNotHasKey('dbPassword', $bootstrap[0]->payload);
        $this->assertNotSame('', MailClusters::secretsFor($bootstrap[0])['dbPassword']);
        $this->finish($bootstrap[0]);

        // 3. The other two join.
        $join = $this->open(AgentAction::SetMailNode);
        $this->assertCount(2, $join);
        $this->assertFalse($join[0]->payload['bootstrap']);
        foreach ($join as $job) {
            $this->finish($job);
        }
        // 4. The mail server of the first server, then 5. the others.
        $first = $this->open(AgentAction::SetMailServer);
        $this->assertCount(1, $first);
        $this->assertSame('m1.example.net', $first[0]->payload['hostname']);
        $this->finish($first[0]);
        $rest = $this->open(AgentAction::SetMailServer);
        $this->assertCount(2, $rest);
        foreach ($rest as $job) {
            $this->finish($job);
        }
        $this->getJson("/api/v1/mail-clusters/{$id}")->assertJsonPath('status', 'running')->assertJsonPath('phase', 'done')
            ->assertJsonPath('members.1.step', 'running');
        $this->assertSame('running', $servers[1]->fresh()->mail_status);
        $this->getJson("/api/v1/servers/{$servers[0]->id}")->assertJsonPath('mail.clusterId', $id);
        $this->getJson('/api/v1/mail-clusters')->assertJsonCount(1, 'data');
    }

    public function test_a_failed_step_stops_and_can_start_again(): void
    {
        $this->actingWith($this->user());
        $id = $this->postJson('/api/v1/mail-clusters', $this->body($this->servers()))->json('id');
        $prepare = $this->open(AgentAction::PrepareMailNode);
        $this->postJson("/api/v1/mail-clusters/{$id}/retry")->assertUnprocessable();
        $this->finish($prepare[0], JobState::Failed);
        $this->finish($prepare[1], result: $this->keys());
        // Keys that are not correct are a failure too.
        $this->postJson("/api/v1/mail-clusters/{$id}/retry")->assertUnprocessable();
        $this->finish($prepare[2], result: ['keys' => ['wireguardKey' => 'x']]);
        $this->getJson("/api/v1/mail-clusters/{$id}")->assertJsonPath('status', 'failed')->assertJsonPath('phase', 'prepare');
        $this->assertSame(0, $this->open(AgentAction::SetMailNode)->count());

        // Only the two servers without keys do the step again.
        $this->postJson("/api/v1/mail-clusters/{$id}/retry")->assertAccepted()->assertJsonPath('status', 'setting_up');
        $again = $this->open(AgentAction::PrepareMailNode);
        $this->assertCount(2, $again);
        foreach ($again as $job) {
            $this->finish($job, result: $this->keys());
        }
        $this->assertCount(1, $this->open(AgentAction::SetMailNode));
    }

    public function test_the_panel_refuses_a_cluster_that_cannot_operate(): void
    {
        $this->actingWith($this->user());
        $servers = $this->servers(4);
        $this->postJson('/api/v1/mail-clusters', $this->body($servers))->assertUnprocessable()->assertJsonValidationErrors('members');
        $three = $servers->take(3);
        $body = $this->body($three);
        $body['members'][1]['endpoint'] = '192.0.2.1';
        $this->postJson('/api/v1/mail-clusters', $body)->assertJsonValidationErrors('members.1.endpoint');
        $body = $this->body($three);
        $body['hostname'] = 'm2.example.net';
        $this->postJson('/api/v1/mail-clusters', $body)->assertJsonValidationErrors('hostname');

        $three[1]->update(['roles' => ['web']]);
        $this->postJson('/api/v1/mail-clusters', $this->body($three))->assertJsonValidationErrors('members.1.serverId');
        $three[1]->update(['roles' => ['email'], 'mail_hostname' => 'own.example.net', 'mail_status' => 'running']);
        $this->postJson('/api/v1/mail-clusters', $this->body($three))->assertJsonValidationErrors('members.1.serverId');
        $three[1]->update(['mail_hostname' => null, 'mail_status' => null, 'state' => 'offline']);
        $this->postJson('/api/v1/mail-clusters', $this->body($three))->assertJsonValidationErrors('members.1.serverId');
        $this->assertSame(0, MailCluster::count());
    }

    public function test_only_an_administrator_makes_a_cluster(): void
    {
        $servers = $this->servers();
        $this->actingWith($this->user(Role::Owner, $this->customer($this->plan())));
        $this->postJson('/api/v1/mail-clusters', $this->body($servers))->assertForbidden();
        $this->getJson('/api/v1/mail-clusters')->assertForbidden();
    }

    public function test_a_member_keeps_its_mail_server_and_a_different_member_does_its_jobs(): void
    {
        $this->actingWith($this->user());
        $servers = $this->servers();
        $this->postJson('/api/v1/mail-clusters', $this->body($servers))->assertAccepted();
        $this->putJson("/api/v1/servers/{$servers[0]->id}/mail", ['hostname' => 'x.example.net'])->assertJsonValidationErrors('hostname');
        $this->deleteJson("/api/v1/servers/{$servers[0]->id}")->assertJsonValidationErrors('server');

        $servers->each(fn (Server $s) => $s->update(['mail_status' => 'running']));
        $servers[0]->update(['state' => 'offline']);
        $job = app(JobDispatcher::class)->dispatch($servers[0]->fresh(), AgentAction::SetMailbox, [], 'Add mailbox');
        $this->assertSame($servers[1]->id, $job->server_id);
        // A job for the server itself stays with the server.
        $job = app(JobDispatcher::class)->dispatch($servers[0]->fresh(), AgentAction::SetMailServer, [], 'Start mail server');
        $this->assertSame($servers[0]->id, $job->server_id);
    }
}
