<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\GitSource;
use App\Models\Preview;
use App\Models\Release;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\JobOutcome;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The preview websites of pull requests (App\Services\Previews). */
class PreviewTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private const GITHUB = ['X-GitHub-Event' => 'pull_request'];

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

    /** Connects the website to a repository, turns on the previews, and gives the path of the webhook. */
    private function previews(Website $website, array $settings = [], array $source = []): string
    {
        $deploy = $this->putJson("/api/v1/websites/{$website->id}/deploy", $source + ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main', 'buildCommands' => ['composer install --no-dev']])->assertOk()->json();
        $this->putJson("/api/v1/websites/{$website->id}/previews", $settings + ['enabled' => true, 'domain' => 'preview.acme.example'])->assertOk()->assertJsonPath('settings.enabled', true);

        return (string) parse_url($deploy['source']['webhookUrl'], PHP_URL_PATH);
    }

    /** A pull request event of GitHub. */
    private function github(string $action, int $number = 7, array $more = []): array
    {
        return array_replace_recursive([
            'action' => $action,
            'number' => $number,
            'pull_request' => [
                'number' => $number,
                'title' => 'Add the CSV export',
                'html_url' => "https://github.com/acme/shop/pull/{$number}",
                'head' => ['ref' => 'feature/csv', 'sha' => str_repeat('a', 40), 'repo' => ['full_name' => 'acme/shop']],
                'base' => ['ref' => 'main', 'repo' => ['full_name' => 'acme/shop']],
            ],
        ], $more);
    }

    private function deployJob(Website $site): ?AgentJob
    {
        return AgentJob::where(['website_id' => $site->id, 'action' => AgentAction::Deploy])->latest()->orderByDesc('id')->first();
    }

    public function test_the_settings_need_a_git_source_and_a_domain_that_the_customer_controls(): void
    {
        $website = $this->site();
        $owner = $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->getJson("/api/v1/websites/{$website->id}/previews")->assertOk()->assertJsonPath('settings', null)
            ->assertJsonPath('suggestedDomain', 'preview.acme.example')->assertJsonPath('previews', []);
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true, 'domain' => 'preview.acme.example'])->assertJsonValidationErrors('enabled');

        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main'])->assertOk();
        $this->getJson("/api/v1/websites/{$website->id}/previews")->assertJsonPath('settings', ['enabled' => false, 'domain' => null, 'limit' => 3, 'copyEnvironment' => false]);
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true])->assertJsonValidationErrors('domain');
        // The customer does not control a different domain, the panel domain or a name that is too long.
        foreach (['preview.other.example', config('cloudfactory.panel_domain'), str_repeat('a', 60).'.'.str_repeat('b', 60).'.'.str_repeat('c', 60).'.'.str_repeat('d', 60).'.acme.example', 'bad_name.acme.example'] as $domain) {
            $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true, 'domain' => $domain])->assertJsonValidationErrors('domain');
        }
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true, 'domain' => 'preview.acme.example', 'limit' => 11])->assertJsonValidationErrors('limit');
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true, 'domain' => 'preview.acme.example', 'limit' => 5, 'copyEnvironment' => true])
            ->assertOk()->assertJsonPath('settings', ['enabled' => true, 'domain' => 'preview.acme.example', 'limit' => 5, 'copyEnvironment' => true]);
        $this->assertTrue(AuditEntry::where(['action' => 'Turned on previews', 'user_id' => $owner->id])->exists());

        // A private website has no public address.
        $private = $this->website($website->customer, $website->server, $website->image, 'queue-a1b2.internal', ['kind' => Website::PRIVATE]);
        $this->putJson("/api/v1/websites/{$private->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main'])->assertOk();
        $this->putJson("/api/v1/websites/{$private->id}/previews", ['enabled' => true, 'domain' => 'preview.acme.example'])->assertJsonValidationErrors('enabled');

        // A different customer cannot see the previews.
        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->getJson("/api/v1/websites/{$website->id}/previews")->assertNotFound();
    }

    public function test_a_pull_request_makes_a_preview_that_deploys_its_branch_and_goes_when_it_closes(): void
    {
        $website = $this->site(['environment' => ['APP_KEY' => 'live-key']]);
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $url = $this->previews($website);
        $this->putJson("/api/v1/websites/{$website->id}/deploy/secrets/NPM_TOKEN", ['value' => 's3cret-value'])->assertOk();
        $this->app['auth']->forgetGuards();

        $answer = $this->postJson($url, $this->github('opened'), self::GITHUB)->assertAccepted()->assertJsonPath('status', 'started')->json();
        $site = Website::findOrFail($answer['previewWebsiteId']);
        $this->assertSame(['pr-7.preview.acme.example', 'starting', $website->server_id, $website->image_id, $website->plan_id], [$site->name, $site->status->value, $site->server_id, $site->image_id, $site->plan_id]);
        $this->assertSame('Preview of acme.example, pull request #7', $site->application);
        // The name is verified at once: the customer controls the domain of the previews.
        $this->assertNotNull($site->domains()->first()->verified_at);
        // The preview does not get the variables and the secrets of the website by default.
        $this->assertSame([], $site->variables());
        $own = $site->gitSource;
        $this->assertSame(['git@github.com:acme/shop.git', 'feature/csv', 'manual', [], $website->gitSource->public_key], [$own->repository, $own->branch, $own->trigger, $own->secrets, $own->public_key]);
        $create = AgentJob::where(['website_id' => $site->id, 'action' => AgentAction::CreateWebsite])->firstOrFail();
        $this->assertSame('Create preview of pull request #7', $create->title);
        $this->assertSame([['name' => 'pr-7.preview.acme.example', 'kind' => 'primary']], $create->payload['website']['domains']);
        $this->actingWith($this->user());
        $this->assertSame(['websiteId' => $website->id, 'number' => 7, 'branch' => 'feature/csv', 'title' => 'Add the CSV export', 'url' => 'https://github.com/acme/shop/pull/7'],
            $this->getJson("/api/v1/websites/{$site->id}")->assertOk()->json('data.previewOf'));
        $this->app['auth']->forgetGuards();

        // New commits while the panel makes the website: the first deploy gets them.
        $this->postJson($url, $this->github('synchronize'), self::GITHUB)->assertAccepted()->assertJsonPath('status', 'waiting');
        $this->assertNull($this->deployJob($site));

        // The website is made: the first deploy starts, with the head branch and the key.
        $this->finish($create, JobState::Succeeded, ['dnsPointsHere' => true]);
        $deploy = $this->deployJob($site);
        $this->assertSame(['feature/csv', 'git@github.com:acme/shop.git', 'webhook'], [$deploy->payload['branch'], $deploy->payload['repository'], Release::find($deploy->payload['releaseId'])->trigger]);
        Http::fake([self::AGENT.'Deploy' => Http::response(['jobId' => $deploy->id])]);
        (new SendJobToAgent($deploy->id))->handle(app(AgentClient::class));
        Http::assertSent(fn ($r) => str_starts_with($r['deployKey'], '-----BEGIN OPENSSH PRIVATE KEY-----') && (array) $r['secrets'] === []);

        // New commits during the deploy: the panel deploys again after it.
        $this->postJson($url, $this->github('synchronize', more: ['pull_request' => ['title' => 'Add the CSV and the PDF export']]), self::GITHUB)->assertAccepted()->assertJsonPath('status', 'queued');
        $this->assertSame([true, 'Add the CSV and the PDF export'], [Preview::first()->deploy_again, Preview::first()->title]);
        $this->travel(1)->minutes();
        $this->finish($deploy, JobState::Succeeded, ['commit' => str_repeat('b', 40), 'message' => 'Add the export', 'removed' => []]);
        $again = $this->deployJob($site);
        $this->assertNotSame($deploy->id, $again->id);
        $this->assertFalse(Preview::first()->deploy_again);
        $this->finish($again, JobState::Succeeded, ['commit' => str_repeat('c', 40), 'message' => 'Add the PDF export', 'removed' => []]);

        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->getJson("/api/v1/websites/{$website->id}/previews")->assertOk()
            ->assertJsonPath('previews.0.number', 7)->assertJsonPath('previews.0.name', 'pr-7.preview.acme.example')->assertJsonPath('previews.0.state', 'live')
            ->assertJsonPath('previews.0.release.commit', str_repeat('c', 40))->assertJsonPath('previews.0.url', 'https://github.com/acme/shop/pull/7');
        // The Git source of a preview comes from the website of the pull request.
        $this->putJson("/api/v1/websites/{$site->id}/deploy", ['repository' => 'git@github.com:acme/other.git', 'branch' => 'main'])->assertJsonValidationErrors('repository');
        $this->putJson("/api/v1/websites/{$site->id}/previews", ['enabled' => true, 'domain' => 'preview.acme.example'])->assertJsonValidationErrors('repository');
        $this->getJson("/api/v1/websites/{$site->id}/previews")->assertJsonPath('settings', null);
        $this->app['auth']->forgetGuards();

        // A new title changes only the title.
        $this->postJson($url, $this->github('edited', more: ['pull_request' => ['title' => 'Export the orders']]), self::GITHUB)->assertOk()->assertJsonPath('status', 'updated');
        $this->assertSame('Export the orders', Preview::first()->title);

        // The pull request closes: the panel removes the preview website.
        $this->postJson($url, $this->github('closed'), self::GITHUB)->assertAccepted()->assertJsonPath('status', 'removing');
        $removal = AgentJob::where(['website_id' => $site->id, 'action' => AgentAction::DeleteWebsite])->firstOrFail();
        $this->assertTrue(AuditEntry::where(['action' => 'Removed preview', 'target' => 'pr-7.preview.acme.example'])->exists());
        $this->postJson($url, $this->github('closed'), self::GITHUB)->assertAccepted();
        $this->assertSame(1, AgentJob::where(['website_id' => $site->id, 'action' => AgentAction::DeleteWebsite])->count());
        $this->finish($removal, JobState::Succeeded);
        $this->assertSame([0, 1], [Preview::count(), Website::count()]);
        $this->postJson($url, $this->github('closed'), self::GITHUB)->assertOk()->assertJsonPath('status', 'ignored');
    }

    public function test_the_panel_ignores_forks_other_branches_and_events_when_the_previews_are_off(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $url = $this->previews($website);
        $this->app['auth']->forgetGuards();

        $fork = $this->github('opened', more: ['pull_request' => ['head' => ['repo' => ['full_name' => 'mallory/shop']]]]);
        $this->postJson($url, $fork, self::GITHUB)->assertOk()->assertJsonPath('status', 'ignored');
        $deleted = $this->github('opened');
        $deleted['pull_request']['head']['repo'] = null;
        $this->postJson($url, $deleted, self::GITHUB)->assertOk()->assertJsonPath('status', 'ignored');
        $this->postJson($url, $this->github('opened', more: ['pull_request' => ['base' => ['ref' => 'develop']]]), self::GITHUB)->assertOk()->assertJsonPath('status', 'ignored');
        $this->postJson($url, $this->github('labeled'), self::GITHUB)->assertOk()->assertJsonPath('status', 'ignored');
        // A branch name that is not correct.
        $this->postJson($url, $this->github('opened', more: ['pull_request' => ['head' => ['ref' => '--upload-pack=x']]]), self::GITHUB)->assertJsonValidationErrors('branch');
        $this->postJson($url, $this->github('opened', 0), self::GITHUB)->assertJsonValidationErrors('number');
        $this->assertSame(0, Preview::count());

        // The trigger is "manual": a push does not deploy, but a pull request makes a preview.
        $this->postJson($url, ['ref' => 'refs/heads/main'])->assertOk()->assertJsonPath('status', 'ignored');
        $this->assertSame(0, Release::count());
        $this->postJson($url, $this->github('opened'), self::GITHUB)->assertAccepted()->assertJsonPath('status', 'started');

        // A suspended customer gets no new preview.
        $website->customer->update(['suspended' => true]);
        $this->postJson($url, $this->github('opened', 8), self::GITHUB)->assertStatus(409);
        $website->customer->update(['suspended' => false]);

        // The previews go off: the panel removes them, and the webhook is off with the trigger "manual".
        $this->actingWith($this->user());
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => false])->assertOk()->assertJsonPath('settings.enabled', false);
        $this->assertSame(1, AgentJob::where('action', AgentAction::DeleteWebsite)->count());
        $this->app['auth']->forgetGuards();
        $this->postJson($url, $this->github('opened', 9), self::GITHUB)->assertNotFound();
        GitSource::where('website_id', $website->id)->update(['trigger' => GitSource::WEBHOOK]);
        $this->postJson($url, $this->github('opened', 9), self::GITHUB)->assertOk()->assertJsonPath('status', 'ignored');
        // A pull request event never deploys the branch of the website.
        $this->assertSame(0, Release::where('website_id', $website->id)->count());
    }

    public function test_gitlab_gitea_and_bitbucket_events(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $url = $this->previews($website, ['limit' => 10]);
        $this->app['auth']->forgetGuards();

        $gitlab = fn (string $action, array $more = []) => ['object_kind' => 'merge_request', 'object_attributes' => $more + [
            'iid' => 3, 'action' => $action, 'source_branch' => 'fix-login', 'target_branch' => 'main', 'source_project_id' => 5, 'target_project_id' => 5,
            'title' => 'Fix the login', 'url' => 'https://gitlab.com/acme/shop/-/merge_requests/3',
        ]];
        $headers = ['X-Gitlab-Event' => 'Merge Request Hook'];
        $this->postJson($url, $gitlab('open', ['source_project_id' => 6]), $headers)->assertOk()->assertJsonPath('status', 'ignored');
        $this->postJson($url, $gitlab('open'), $headers)->assertAccepted()->assertJsonPath('status', 'started');
        $this->postJson($url, $gitlab('update', ['title' => 'Fix the sign-in']), $headers)->assertOk()->assertJsonPath('status', 'updated');
        $this->postJson($url, $gitlab('update', ['oldrev' => str_repeat('a', 40), 'title' => 'Fix the sign-in']), $headers)->assertAccepted()->assertJsonPath('status', 'waiting');
        $this->postJson($url, $gitlab('approved'), $headers)->assertOk()->assertJsonPath('status', 'ignored');
        $this->assertSame(['fix-login', 'Fix the sign-in'], [Preview::where('number', 3)->value('branch'), Preview::where('number', 3)->value('title')]);
        $this->postJson($url, $gitlab('merge'), $headers)->assertAccepted()->assertJsonPath('status', 'removing');

        $gitea = $this->github('opened', 4, ['pull_request' => ['head' => ['ref' => 'docs'], 'html_url' => 'https://git.example/acme/shop/pulls/4']]);
        $this->postJson($url, $gitea, ['X-Gitea-Event' => 'pull_request', 'X-GitHub-Event' => 'pull_request'])->assertAccepted()->assertJsonPath('status', 'started');
        $this->postJson($url, ['action' => 'synchronized'] + $gitea, ['X-Forgejo-Event' => 'pull_request'])->assertAccepted()->assertJsonPath('status', 'waiting');
        $this->assertSame('docs', Preview::where('number', 4)->value('branch'));
        // A push event of Gitea is not a pull request.
        $this->postJson($url, ['ref' => 'refs/heads/main'], ['X-Gitea-Event' => 'push'])->assertOk()->assertJsonPath('status', 'ignored');

        $bitbucket = ['pullrequest' => [
            'id' => 5, 'title' => 'Faster search', 'links' => ['html' => ['href' => 'https://bitbucket.org/acme/shop/pull-requests/5']],
            'source' => ['branch' => ['name' => 'search'], 'repository' => ['full_name' => 'acme/shop']],
            'destination' => ['branch' => ['name' => 'main'], 'repository' => ['full_name' => 'acme/shop']],
        ]];
        $this->postJson($url, $bitbucket, ['X-Event-Key' => 'pullrequest:created'])->assertAccepted()->assertJsonPath('status', 'started');
        $this->assertSame(['search', 'https://bitbucket.org/acme/shop/pull-requests/5'], [Preview::where('number', 5)->value('branch'), Preview::where('number', 5)->value('url')]);
        $this->postJson($url, $bitbucket, ['X-Event-Key' => 'pullrequest:rejected'])->assertAccepted()->assertJsonPath('status', 'removing');
    }

    public function test_the_limits_keep_the_cause_and_the_next_event_tries_again(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $url = $this->previews($website, ['limit' => 1]);
        $this->app['auth']->forgetGuards();

        $this->postJson($url, $this->github('opened', 1), self::GITHUB)->assertAccepted();
        $this->postJson($url, $this->github('opened', 2), self::GITHUB)->assertOk()->assertJsonPath('status', 'failed')
            ->assertJsonPath('message', 'The limit of the previews of this website is 1. Close a pull request, or change the limit.');
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->getJson("/api/v1/websites/{$website->id}/previews")->assertJsonPath('previews.1.number', 2)->assertJsonPath('previews.1.state', 'failed')
            ->assertJsonPath('previews.1.websiteId', null)->assertJsonPath('previews.1.error', 'The limit of the previews of this website is 1. Close a pull request, or change the limit.');

        // The plan of the customer limits the previews too: each one is a website of the plan.
        $website->customer->plan->update(['websites' => 2]);
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true, 'limit' => 5])->assertOk();
        $this->app['auth']->forgetGuards();
        $this->postJson($url, $this->github('synchronize', 2), self::GITHUB)->assertOk()->assertJsonPath('status', 'failed')->assertJsonPath('message', 'The Business plan allows 2 websites.');

        $website->customer->plan->update(['websites' => 10]);
        $this->postJson($url, $this->github('synchronize', 2), self::GITHUB)->assertAccepted()->assertJsonPath('status', 'started');
        $this->assertNull(Preview::where('number', 2)->value('error'));

        // A preview with no website goes at once when its pull request closes.
        Preview::create(['website_id' => $website->id, 'number' => 3, 'branch' => 'x', 'error' => 'The website has 2 previews.']);
        $this->postJson($url, $this->github('closed', 3), self::GITHUB)->assertAccepted();
        $this->assertFalse(Preview::where('number', 3)->exists());
    }

    public function test_the_previews_get_the_variables_and_the_secrets_only_when_the_settings_permit_it(): void
    {
        $website = $this->site(['environment' => ['APP_KEY' => 'live-key']]);
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $url = $this->previews($website, ['copyEnvironment' => true]);
        $this->putJson("/api/v1/websites/{$website->id}/deploy/secrets/NPM_TOKEN", ['value' => 's3cret-value'])->assertOk();
        $this->app['auth']->forgetGuards();

        $site = Website::findOrFail($this->postJson($url, $this->github('opened'), self::GITHUB)->assertAccepted()->json('previewWebsiteId'));
        $this->assertSame(['APP_KEY' => 'live-key'], $site->variables());
        $this->assertSame(['NPM_TOKEN' => 's3cret-value'], $site->gitSource->secrets);
    }

    public function test_a_user_makes_and_removes_a_preview_and_the_previews_go_with_the_website(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->putJson("/api/v1/websites/{$website->id}/deploy", ['repository' => 'git@github.com:acme/shop.git', 'branch' => 'main'])->assertOk();
        $this->postJson("/api/v1/websites/{$website->id}/previews", ['number' => 12, 'branch' => 'feature/x'])->assertJsonValidationErrors('number');
        $this->putJson("/api/v1/websites/{$website->id}/previews", ['enabled' => true, 'domain' => 'preview.acme.example'])->assertOk();

        $answer = $this->postJson("/api/v1/websites/{$website->id}/previews", ['number' => 12, 'branch' => 'feature/x', 'title' => 'Try the new menu'])
            ->assertAccepted()->assertJsonPath('status', 'started')->assertJsonPath('preview.state', 'creating')->assertJsonPath('preview.name', 'pr-12.preview.acme.example')->json();
        $site = Website::findOrFail($answer['preview']['websiteId']);
        $this->postJson("/api/v1/websites/{$website->id}/previews", ['number' => 13, 'branch' => '../x'])->assertJsonValidationErrors('branch');
        $this->finish(AgentJob::where(['website_id' => $site->id, 'action' => AgentAction::CreateWebsite])->firstOrFail(), JobState::Succeeded);
        $this->assertSame('manual', Release::where('website_id', $site->id)->value('trigger'));

        $this->deleteJson("/api/v1/websites/{$website->id}/previews/12")->assertAccepted()->assertJsonPath('job.action', 'DeleteWebsite');
        $this->deleteJson("/api/v1/websites/{$website->id}/previews/99")->assertNotFound();

        // The removal of the website removes its previews too.
        $this->postJson("/api/v1/websites/{$website->id}/previews", ['number' => 14, 'branch' => 'feature/y'])->assertAccepted();
        $other = Preview::where('number', 14)->value('preview_website_id');
        $this->deleteJson("/api/v1/websites/{$website->id}")->assertAccepted();
        $this->assertTrue(AgentJob::where(['website_id' => $other, 'action' => AgentAction::DeleteWebsite])->exists());
    }

    public function test_a_disconnect_removes_the_previews(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        $url = $this->previews($website);
        $this->app['auth']->forgetGuards();
        $site = $this->postJson($url, $this->github('opened'), self::GITHUB)->assertAccepted()->json('previewWebsiteId');

        $this->actingWith($this->user(Role::Owner, $website->customer));
        $this->deleteJson("/api/v1/websites/{$website->id}/deploy")->assertNoContent();
        $this->assertTrue(AgentJob::where(['website_id' => $site, 'action' => AgentAction::DeleteWebsite])->exists());
    }
}
