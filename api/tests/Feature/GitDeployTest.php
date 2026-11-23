<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\GitSource;
use App\Models\NotificationDelivery;
use App\Models\Release;
use App\Models\Website;
use App\Services\JobDispatcher;
use App\Services\JobOutcome;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/**
 * The deploys that the panel starts by itself: from a signed webhook and from a check of the
 * branch on a schedule (FR-1004), and the health check after a deploy (FR-1008, FR-1009).
 */
class GitDeployTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private const A = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';

    private const B = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';

    private function site(): Website
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cloudfactory-test-pki']);

        return $this->website($this->customer(), $this->server(['certificate_pem' => 'test', 'certificate_fingerprint' => str_repeat('ab', 32)]), $this->image());
    }

    private function connect(Website $website, array $more = []): array
    {
        return $this->putJson("/api/v1/websites/{$website->id}/deploy", $more + ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main'])->assertOk()->json('source');
    }

    private function finish(AgentJob $job, JobState $state, array $result = [], ?string $error = null): void
    {
        app(JobOutcome::class)->started($job);
        app(JobOutcome::class)->finish($job, $state, $error, $result);
    }

    private function deployJob(): AgentJob
    {
        return AgentJob::where('action', AgentAction::Deploy)->latest('created_at')->orderByDesc('id')->firstOrFail();
    }

    /** A request of a Git host with the signature of GitHub. */
    private function github(string $url, string $secret, array $body, string $event = 'push')
    {
        $json = json_encode($body);

        return $this->call('POST', $url, [], [], [], [
            'CONTENT_TYPE' => 'application/json',
            'HTTP_X_GITHUB_EVENT' => $event,
            'HTTP_X_HUB_SIGNATURE_256' => 'sha256='.hash_hmac('sha256', $json, $secret),
        ], $json);
    }

    private function push(string $branch, string $commit): array
    {
        return ['ref' => "refs/heads/{$branch}", 'before' => self::A, 'after' => $commit];
    }

    public function test_the_webhook_needs_the_signature_of_the_git_host(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $source = $this->connect($website, ['trigger' => 'webhook']);
        $url = parse_url($source['webhookUrl'], PHP_URL_PATH);
        $secret = $source['webhookSecret'];
        $this->assertMatchesRegularExpression('/^[A-Za-z0-9]{40}$/', $secret);
        $this->app['auth']->forgetGuards();

        // No signature, a wrong signature, the secret of a different source and a bad address.
        $this->postJson($url, $this->push('main', self::B))->assertUnauthorized();
        $this->github($url, 'wrong-secret', $this->push('main', self::B))->assertUnauthorized();
        $this->call('POST', $url, [], [], [], ['CONTENT_TYPE' => 'application/json', 'HTTP_X_HUB_SIGNATURE_256' => 'sha256='.hash_hmac('sha256', '{}', $secret)], json_encode($this->push('main', self::B)))->assertUnauthorized();
        $this->github('/api/v1/hooks/deploy/'.str_repeat('x', 48), $secret, $this->push('main', self::B))->assertNotFound();
        $this->assertSame(0, Release::count());

        // The test request of GitHub, a push to a different branch, a tag and a removed branch.
        $this->github($url, $secret, ['zen' => 'Keep it simple.'], 'ping')->assertOk()->assertJsonPath('status', 'ok');
        $this->github($url, $secret, $this->push('feature', self::B))->assertOk()->assertJsonPath('status', 'ignored');
        $this->github($url, $secret, ['ref' => 'refs/tags/v1.0', 'after' => self::B])->assertOk()->assertJsonPath('status', 'ignored');
        $this->github($url, $secret, ['ref' => 'refs/heads/main', 'after' => str_repeat('0', 40), 'deleted' => true])->assertOk()->assertJsonPath('status', 'ignored');
        $this->github($url, $secret, ['action' => 'opened'], 'issues')->assertOk()->assertJsonPath('status', 'ignored');
        $this->assertSame(0, Release::count());

        // A push to the branch starts a deploy. The release has the commit of the push.
        $id = $this->github($url, $secret, $this->push('main', self::B))->assertAccepted()->assertJsonPath('status', 'started')->json('releaseId');
        $this->assertSame(['webhook', 'building', self::B], [Release::find($id)->trigger, Release::find($id)->state, Release::find($id)->commit]);
        $this->assertSame(1, AuditEntry::where('action', 'Started deploy from webhook')->count());

        // A new address and secret. The old address stops at once.
        $this->actingWith($this->user());
        $new = $this->postJson("/api/v1/websites/{$website->id}/deploy/webhook")->assertOk()->json('source');
        $this->app['auth']->forgetGuards();
        $this->assertNotSame([$url, $secret], [parse_url($new['webhookUrl'], PHP_URL_PATH), $new['webhookSecret']]);
        $this->github($url, $secret, $this->push('main', self::B))->assertNotFound();
        $this->github(parse_url($new['webhookUrl'], PHP_URL_PATH), $secret, $this->push('main', self::B))->assertUnauthorized();
    }

    public function test_the_webhook_reads_the_requests_of_each_git_host(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $source = $this->connect($website, ['trigger' => 'webhook']);
        $url = parse_url($source['webhookUrl'], PHP_URL_PATH);
        $secret = $source['webhookSecret'];
        $this->app['auth']->forgetGuards();
        $send = function (array $headers, string $body) use ($url) {
            $server = ['CONTENT_TYPE' => 'application/json'];
            foreach ($headers as $name => $value) {
                $server['HTTP_'.strtoupper(str_replace('-', '_', $name))] = $value;
            }

            return $this->call('POST', $url, [], [], [], $server, $body);
        };
        $deployed = function (string $commit) {
            $release = Release::latest('created_at')->orderByDesc('id')->first();
            $this->assertSame($commit, $release?->commit);
            Release::query()->update(['state' => Release::FAILED]);
        };

        // GitLab sends the secret in a header.
        $body = json_encode(['object_kind' => 'push', 'ref' => 'refs/heads/main', 'after' => self::A, 'checkout_sha' => self::A]);
        $send(['X-Gitlab-Event' => 'Push Hook', 'X-Gitlab-Token' => 'wrong'], $body)->assertUnauthorized();
        $send(['X-Gitlab-Event' => 'Push Hook', 'X-Gitlab-Token' => $secret], $body)->assertAccepted()->assertJsonPath('status', 'started');
        $deployed(self::A);
        $send(['X-Gitlab-Event' => 'Tag Push Hook', 'X-Gitlab-Token' => $secret], $body)->assertOk()->assertJsonPath('status', 'ignored');

        // Gitea and Forgejo sign with no prefix. They also send the event header of GitHub.
        $body = json_encode(['ref' => 'refs/heads/main', 'after' => self::B]);
        $send(['X-Forgejo-Event' => 'push', 'X-GitHub-Event' => 'push', 'X-Forgejo-Signature' => hash_hmac('sha256', $body, $secret)], $body)->assertAccepted();
        $deployed(self::B);
        $send(['X-Gitea-Event' => 'pull_request', 'X-Gitea-Signature' => hash_hmac('sha256', '{"action":"opened"}', $secret)], '{"action":"opened"}')->assertOk()->assertJsonPath('status', 'ignored');

        // Bitbucket Cloud: the changes of a push, with the signature in X-Hub-Signature.
        $body = json_encode(['push' => ['changes' => [
            ['new' => ['type' => 'branch', 'name' => 'feature', 'target' => ['hash' => self::B]], 'old' => null],
            ['new' => ['type' => 'branch', 'name' => 'main', 'target' => ['hash' => self::A]], 'old' => ['type' => 'branch', 'name' => 'main']],
        ]]]);
        $send(['X-Event-Key' => 'repo:push', 'X-Hub-Signature' => 'sha256='.hash_hmac('sha256', $body, $secret)], $body)->assertAccepted();
        $deployed(self::A);
        $body = json_encode(['push' => ['changes' => [['new' => null, 'old' => ['type' => 'branch', 'name' => 'main']]]]]);
        $send(['X-Event-Key' => 'repo:push', 'X-Hub-Signature' => 'sha256='.hash_hmac('sha256', $body, $secret)], $body)->assertOk()->assertJsonPath('status', 'ignored');

        // Bitbucket Data Center.
        $body = json_encode(['changes' => [['ref' => ['id' => 'refs/heads/main', 'displayId' => 'main', 'type' => 'BRANCH'], 'toHash' => self::B, 'type' => 'UPDATE']]]);
        $send(['X-Event-Key' => 'repo:refs_changed', 'X-Hub-Signature' => 'sha256='.hash_hmac('sha256', $body, $secret)], $body)->assertAccepted();
        $deployed(self::B);

        // GitHub can send a form with the JSON in "payload". The signature is of the form.
        $form = 'payload='.urlencode(json_encode($this->push('main', self::A)));
        $this->call('POST', $url, ['payload' => json_encode($this->push('main', self::A))], [], [], [
            'CONTENT_TYPE' => 'application/x-www-form-urlencoded', 'HTTP_X_GITHUB_EVENT' => 'push',
            'HTTP_X_HUB_SIGNATURE_256' => 'sha256='.hash_hmac('sha256', $form, $secret),
        ], $form)->assertAccepted();
        $deployed(self::A);

        // A script signs as GitHub does, and gives the branch with no "refs/heads/".
        $body = json_encode(['ref' => 'main']);
        $send(['X-Hub-Signature-256' => 'sha256='.hash_hmac('sha256', $body, $secret)], $body)->assertAccepted();
        $this->assertNull(Release::latest('created_at')->orderByDesc('id')->first()->commit);
    }

    public function test_a_push_during_a_deploy_deploys_when_the_deploy_is_complete(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $source = $this->connect($website);
        $url = parse_url($source['webhookUrl'], PHP_URL_PATH);
        $this->app['auth']->forgetGuards();

        // The trigger is "manual": a push starts no deploy.
        $this->github($url, $source['webhookSecret'], $this->push('main', self::A))->assertOk()->assertJsonPath('status', 'ignored');
        GitSource::first()->update(['trigger' => GitSource::WEBHOOK]);

        $this->github($url, $source['webhookSecret'], $this->push('main', self::A))->assertAccepted()->assertJsonPath('status', 'started');
        $first = $this->deployJob();
        $this->github($url, $source['webhookSecret'], $this->push('main', self::B))->assertAccepted()->assertJsonPath('status', 'queued');
        $this->assertSame(1, AgentJob::where('action', AgentAction::Deploy)->count());
        $this->assertTrue(GitSource::first()->deploy_queued);

        // The first deploy got commit A. The panel then deploys commit B.
        $this->travel(1)->minutes();
        $this->finish($first, JobState::Succeeded, ['commit' => self::A, 'message' => 'First']);
        $this->assertSame(2, AgentJob::where('action', AgentAction::Deploy)->count());
        $this->assertSame([self::B, 'webhook'], [Release::latest('created_at')->first()->commit, Release::latest('created_at')->first()->trigger]);
        $this->assertFalse(GitSource::first()->deploy_queued);

        // A deploy that got the commit of the queued push already: no second deploy.
        $this->github($url, $source['webhookSecret'], $this->push('main', self::B))->assertAccepted()->assertJsonPath('status', 'queued');
        $this->travel(1)->minutes();
        $this->finish($this->deployJob(), JobState::Succeeded, ['commit' => self::B, 'message' => 'Second']);
        $this->assertSame(2, AgentJob::where('action', AgentAction::Deploy)->count());

        // A suspended customer: no deploy.
        $website->customer->update(['suspended' => true]);
        $this->github($url, $source['webhookSecret'], $this->push('main', self::A))->assertStatus(409);
    }

    public function test_the_branch_check_deploys_each_new_commit_one_time(): void
    {
        $website = $this->site();
        $owner = $this->user(Role::Owner, $website->customer);
        $this->actingWith($owner);
        $this->connect($website, ['trigger' => 'schedule']);
        $this->assertSame(5, GitSource::first()->check_minutes);
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main', 'checkMinutes' => 0])->assertJsonValidationErrors('checkMinutes');
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main', 'trigger' => 'cron'])->assertJsonValidationErrors('trigger');

        $head = self::A;
        $answer = null;
        Http::fake([self::AGENT.'CheckBranches' => function () use (&$head, &$answer, $website) {
            return $answer ?? Http::response(['heads' => [['websiteId' => $website->id, 'commit' => $head]]]);
        }]);
        $this->artisan('cloudfactory:check-branches')->assertSuccessful();
        // The check has the branch and the private key of the website.
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/CheckBranches') && $r['checks'][0]['branch'] === 'main'
            && $r['checks'][0]['websiteId'] === $website->id && str_starts_with($r['checks'][0]['deployKey'], '-----BEGIN OPENSSH PRIVATE KEY-----'));
        $release = Release::firstOrFail();
        $this->assertSame([self::A, 'schedule', 'building'], [$release->commit, $release->trigger, $release->state]);
        $this->assertSame(1, AuditEntry::where('action', 'Started deploy from branch check')->where('customer_id', $website->customer_id)->count());
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('source.trigger', 'schedule')->assertJsonPath('source.checkError', null)
            ->assertJsonPath('releases.0.trigger', 'schedule');

        // The next check is after 5 minutes, and not during a deploy.
        $this->artisan('cloudfactory:check-branches');
        $this->travel(5)->minutes();
        $this->artisan('cloudfactory:check-branches');
        Http::assertSentCount(1);
        $this->finish($this->deployJob(), JobState::Succeeded, ['commit' => self::A, 'message' => 'First']);

        // The same commit: no deploy.
        $this->artisan('cloudfactory:check-branches');
        Http::assertSentCount(2);
        $this->assertSame(1, Release::count());

        // A new commit deploys. When its deploy fails, the next check does not deploy it again.
        $head = self::B;
        $this->travel(5)->minutes();
        $this->artisan('cloudfactory:check-branches');
        $this->assertSame(2, Release::count());
        $this->finish($this->deployJob(), JobState::Failed, [], 'a build command failed: exit status 2: composer: lock file is out of date');
        $this->travel(5)->minutes();
        $this->artisan('cloudfactory:check-branches');
        Http::assertSentCount(4);
        $this->assertSame(2, Release::count());

        // A check that fails. The owner sees the output of Git, which is from the build
        // container of the website, but not an error that shows the server.
        $answer = Http::response(['heads' => [['websiteId' => $website->id, 'error' => 'the agent cannot read the branch: exit status 128: ERROR: Repository not found.']]]);
        $this->travel(5)->minutes();
        $this->artisan('cloudfactory:check-branches');
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('source.checkError', 'the agent cannot read the branch: exit status 128: ERROR: Repository not found.');
        GitSource::first()->update(['check_error' => 'runuser -u acme_1234 -- mkdir -p -- /home/acme_1234/.cf-ssh: exit status 1: permission denied']);
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('source.checkError', 'A step failed on the server. Your provider can see the details.');

        // A server that does not answer.
        $answer = Http::response(['code' => 'unavailable', 'message' => 'the agent stops'], 503);
        $this->travel(5)->minutes();
        $this->artisan('cloudfactory:check-branches');
        $this->assertSame('the agent stops', GitSource::first()->check_error);

        // No check for a suspended customer, for a different trigger, or on a server that is offline.
        $sent = count(Http::recorded());
        $website->customer->update(['suspended' => true]);
        $this->travel(5)->minutes();
        $this->artisan('cloudfactory:check-branches');
        $website->customer->update(['suspended' => false]);
        $website->server->update(['state' => 'offline']);
        $this->artisan('cloudfactory:check-branches');
        $website->server->update(['state' => 'healthy']);
        GitSource::first()->update(['trigger' => GitSource::WEBHOOK]);
        $this->artisan('cloudfactory:check-branches');
        Http::assertSentCount($sent);
    }

    public function test_a_deploy_has_a_health_check_and_goes_back_when_it_fails(): void
    {
        $website = $this->site();
        $owner = $this->user(Role::Owner, $website->customer);
        $this->actingWith($owner);
        $this->connect($website);
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $job = AgentJob::find($answer['job']['id']);
        // FR-1008: the agent checks the website for 5 minutes after the switch.
        $this->assertSame([JobDispatcher::HEALTH_CHECK, ''], [$job->payload['healthCheck'], $job->payload['migrationCommand']]);
        $this->assertSame(['Get the code', 'Build', 'Migrate the database'], array_slice($job->steps, 0, 3));

        // The agent went back to the previous release. The owner sees the number of failed
        // checks, but not the address of the container, and gets the email of a failed job.
        $job->update(['step' => 6]);
        $error = 'the health check failed after the switch: 9 of 30 checks failed, last error: dial tcp 169.254.77.1:20001: connect: connection refused';
        $this->finish($job, JobState::Failed, [], $error);
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('releases.0.state', 'failed')->assertJsonPath('releases.0.live', false)
            ->assertJsonPath('releases.0.migration', false)->assertJsonPath('releases.0.error', 'the health check failed after the switch: 9 of 30 checks failed.');
        $this->assertSame(1, NotificationDelivery::where('event', 'job.failed')->where('user_id', $owner->id)->count());
        $this->assertStringNotContainsString('releases/', (string) $website->fresh()->document_root);
    }

    public function test_after_a_migration_the_website_keeps_the_release_and_the_owner_gets_an_alert(): void
    {
        $website = $this->site();
        $owner = $this->user(Role::Owner, $website->customer);
        $this->actingWith($owner);
        $this->connect($website, ['migrationCommand' => 'php artisan migrate --force']);
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('source.migrationCommand', 'php artisan migrate --force');
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main', 'migrationCommand' => "a\nb"])->assertJsonValidationErrors('migrationCommand');

        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $job = AgentJob::find($answer['job']['id']);
        $this->assertSame('php artisan migrate --force', $job->payload['migrationCommand']);

        // FR-1009: the agent kept the new release. The owner gets one email.
        $health = 'the health check failed after the switch: 9 of 30 checks failed, last error: status 500';
        $this->finish($job, JobState::Succeeded, ['commit' => self::A, 'message' => 'Add the invoice table', 'migrated' => true, 'healthError' => $health]);
        $release = Release::find($answer['release']['id']);
        $this->assertSame([true, true, Release::READY, $health], [$release->live, $release->migration, $release->state, $release->health_error]);
        $this->getJson("/api/v1/websites/{$website->id}/deploy")->assertJsonPath('releases.0.healthError', 'the health check failed after the switch: 9 of 30 checks failed.')->assertJsonPath('releases.0.migration', true);
        $email = NotificationDelivery::where('event', 'deploy.attention')->where('user_id', $owner->id)->sole();
        $this->assertStringContainsString($website->name, $email->subject);
        $this->assertStringContainsString('the health check failed after the switch: 9 of 30 checks failed.', $email->body);
        $this->assertStringContainsString("/websites/{$website->id}/deploys", $email->body);
        $this->assertSame(0, NotificationDelivery::where('event', 'job.failed')->count());

        // A deploy that passes its health check gives no email.
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $this->finish(AgentJob::find($answer['job']['id']), JobState::Succeeded, ['commit' => self::B, 'migrated' => true]);
        $this->assertSame(1, NotificationDelivery::where('event', 'deploy.attention')->count());
        $this->assertNull(Release::find($answer['release']['id'])->health_error);

        // A deploy that failed after its migration step records the migration.
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $failed = AgentJob::find($answer['job']['id']);
        $failed->update(['step' => 4]);
        $this->finish($failed, JobState::Failed, [], 'the website did not answer in 1m30s: status 502');
        $this->assertTrue(Release::find($answer['release']['id'])->migration);
        $answer = $this->postJson("/api/v1/websites/{$website->id}/deploy/releases")->assertAccepted()->json();
        $this->finish(AgentJob::find($answer['job']['id']), JobState::Failed, [], 'a build command failed: exit status 1: x');
        $this->assertFalse(Release::find($answer['release']['id'])->migration);
    }
}
