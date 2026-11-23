<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\JobDispatcher;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/** The protection of a website in the edge proxy (App\Services\WebsiteSecurity). */
class WebsiteProtectionTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private function site(array $attributes = []): Website
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cloudfactory-test-pki']);

        return $this->website($this->customer(), $this->server(['certificate_pem' => 'test', 'certificate_fingerprint' => str_repeat('ab', 32)]), $this->image(), 'acme.example', $attributes);
    }

    public function test_a_new_website_has_no_protection(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $this->getJson("/api/v1/websites/{$website->id}/security")->assertOk()->assertExactJson(['security' => [
            'firewall' => ['mode' => 'off', 'excludedRules' => [], 'skippedPaths' => []],
            'ipRules' => ['allow' => [], 'block' => []],
            'protectedPaths' => [],
            'signInLimit' => ['requestsPerMinute' => 0, 'paths' => []],
        ]]);
        // The same value makes no job.
        $this->patchJson("/api/v1/websites/{$website->id}/security", ['firewall' => ['mode' => 'off']])->assertOk()->assertJsonPath('job', null);
        $this->assertSame(0, AgentJob::count());
    }

    public function test_a_change_saves_the_protection_and_the_job_sends_it_to_the_agent(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));

        $answer = $this->patchJson("/api/v1/websites/{$website->id}/security", [
            'firewall' => ['mode' => 'detect', 'excludedRules' => [942100, 920450, 942100], 'skippedPaths' => ['/wp-admin/']],
            'ipRules' => ['allow' => [], 'block' => ['198.51.100.7', '203.0.113.9/24', '2001:DB8::1/128', '198.51.100.7']],
            'protectedPaths' => [['path' => '/staging/', 'users' => [['name' => 'client', 'password' => 'correct horse']]]],
            'signInLimit' => ['requestsPerMinute' => 10, 'paths' => ['/wp-login.php', '/xmlrpc.php']],
        ])->assertAccepted()->assertJsonPath('job.action', 'SetWebsiteSecurity')->assertJsonPath('job.title', 'Set website protection')->json();

        // The API gives the IP ranges in one form, the rules in order, and no password hashes.
        $this->assertSame(['mode' => 'detect', 'excludedRules' => [920450, 942100], 'skippedPaths' => ['/wp-admin/']], $answer['security']['firewall']);
        $this->assertSame(['198.51.100.7', '203.0.113.0/24', '2001:db8::1'], $answer['security']['ipRules']['block']);
        $this->assertSame([['path' => '/staging/', 'users' => [['name' => 'client']]]], $answer['security']['protectedPaths']);
        $this->assertStringNotContainsString('$2y$', json_encode($answer));
        $this->assertStringNotContainsString('$2y$', $this->getJson("/api/v1/websites/{$website->id}")->getContent());
        $this->assertDatabaseHas('audit_entries', ['action' => 'Changed website protection', 'target' => 'acme.example: firewall detect; IP rules (0 allowed, 3 blocked); protected paths (/staging/); sign-in limit 10 a minute']);

        // The panel keeps a bcrypt hash of the password.
        $hash = $website->fresh()->security['protectedPaths'][0]['users'][0]['passwordHash'];
        $this->assertTrue(password_verify('correct horse', $hash));

        // The job has only the website. The panel adds the protection when it sends the job.
        $job = AgentJob::find($answer['job']['id']);
        $this->assertSame(AgentAction::SetWebsiteSecurity, $job->action);
        $this->assertSame(['websiteId' => $website->id, 'unixUser' => $website->unix_user], $job->payload);
        Http::fake([self::AGENT.'*' => Http::response(['jobId' => $job->id])]);
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetWebsiteSecurity')
            && $r['security']['firewall'] === 'detect'
            && $r['security']['firewallExclusions'] === [920450, 942100]
            && $r['security']['protectedPaths'][0]['users'][0] === ['name' => 'client', 'passwordHash' => $hash]
            && $r['security']['signInLimit'] === 10);

        // A job that makes the route of the website also has the protection.
        $runtime = app(JobDispatcher::class)->dispatch($website->server, AgentAction::SetRuntime, JobDispatcher::websitePayload($website->fresh()), 'Change runtime', $website);
        $this->assertArrayNotHasKey('security', $runtime->payload['website']);
        Http::fake([self::AGENT.'*' => Http::response(['jobId' => $runtime->id])]);
        (new SendJobToAgent($runtime->id))->handle(app(AgentClient::class));
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetRuntime') && $r['website']['security']['allow'] === [] && $r['website']['security']['signInPaths'] === ['/wp-login.php', '/xmlrpc.php']);
    }

    public function test_a_change_of_one_part_keeps_the_other_parts_and_the_passwords(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $url = "/api/v1/websites/{$website->id}/security";
        $this->patchJson($url, [
            'protectedPaths' => [['path' => '/staging/', 'users' => [['name' => 'client', 'password' => 'first password']]]],
            'signInLimit' => ['requestsPerMinute' => 20, 'paths' => ['/login']],
        ])->assertAccepted();
        $hash = $website->fresh()->security['protectedPaths'][0]['users'][0]['passwordHash'];

        // A user with no password keeps the password. A new user needs one.
        $this->patchJson($url, ['protectedPaths' => [['path' => '/staging/', 'users' => [['name' => 'client'], ['name' => 'agency']]]]])
            ->assertJsonValidationErrors('protectedPaths.0.users.1.password');
        $this->patchJson($url, ['protectedPaths' => [['path' => '/staging/', 'users' => [['name' => 'client'], ['name' => 'agency', 'password' => 'second password']]]]])
            ->assertAccepted()->assertJsonPath('security.protectedPaths.0.users.1.name', 'agency')->assertJsonPath('security.signInLimit.requestsPerMinute', 20);
        $users = $website->fresh()->security['protectedPaths'][0]['users'];
        $this->assertSame($hash, $users[0]['passwordHash']);
        $this->assertTrue(password_verify('second password', $users[1]['passwordHash']));

        // The firewall turns on. The sign-in limit and the paths stay.
        $this->patchJson($url, ['firewall' => ['mode' => 'block']])->assertAccepted()
            ->assertJsonPath('security.firewall.mode', 'block')->assertJsonPath('security.protectedPaths.0.path', '/staging/')->assertJsonPath('security.signInLimit.paths', ['/login']);
        // No paths: all protection of the paths goes.
        $this->patchJson($url, ['protectedPaths' => []])->assertAccepted()->assertJsonPath('security.protectedPaths', []);
        $this->assertSame([], $website->fresh()->security['protectedPaths']);
    }

    public function test_the_api_refuses_values_that_the_edge_proxy_cannot_use(): void
    {
        $website = $this->site();
        $this->actingWith($this->user());
        $url = "/api/v1/websites/{$website->id}/security";
        $bad = [
            'firewall.mode' => ['firewall' => ['mode' => 'panic']],
            'firewall.excludedRules.0' => ['firewall' => ['mode' => 'block', 'excludedRules' => [0]]],
            'firewall.skippedPaths.0' => ['firewall' => ['mode' => 'block', 'skippedPaths' => ['/a/../b']]],
            'ipRules.block.1' => ['ipRules' => ['block' => ['198.51.100.7', '203.0.113.300']]],
            'ipRules.allow.0' => ['ipRules' => ['allow' => ['203.0.113.0/33']]],
            'protectedPaths.0.path' => ['protectedPaths' => [['path' => 'staging', 'users' => [['name' => 'a', 'password' => 'long enough']]]]],
            'protectedPaths.0.users' => ['protectedPaths' => [['path' => '/staging/', 'users' => []]]],
            'protectedPaths.0.users.0.name' => ['protectedPaths' => [['path' => '/staging/', 'users' => [['name' => 'a b', 'password' => 'long enough']]]]],
            'protectedPaths.0.users.0.password' => ['protectedPaths' => [['path' => '/staging/', 'users' => [['name' => 'a', 'password' => 'short']]]]],
            'protectedPaths.1.path' => ['protectedPaths' => [['path' => '/x/', 'users' => [['name' => 'a', 'password' => 'long enough']]], ['path' => '/x/', 'users' => [['name' => 'b', 'password' => 'long enough']]]]],
            'signInLimit.requestsPerMinute' => ['signInLimit' => ['requestsPerMinute' => 2, 'paths' => ['/login']]],
            'signInLimit.paths' => ['signInLimit' => ['requestsPerMinute' => 10, 'paths' => []]],
        ];
        foreach ($bad as $field => $body) {
            $this->patchJson($url, $body)->assertJsonValidationErrors($field);
        }
        $this->assertNull($website->fresh()->security);
    }

    public function test_only_a_public_website_that_runs_gets_protection(): void
    {
        $website = $this->site();
        $other = $this->customer(null, ['name' => 'Other', 'email' => 'other@example.com']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson("/api/v1/websites/{$website->id}/security")->assertNotFound();
        $this->patchJson("/api/v1/websites/{$website->id}/security", ['firewall' => ['mode' => 'block']])->assertNotFound();

        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(), ['websites:read']);
        $this->getJson("/api/v1/websites/{$website->id}/security")->assertOk();
        $this->patchJson("/api/v1/websites/{$website->id}/security", ['firewall' => ['mode' => 'block']])->assertForbidden();

        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user());
        $website->customer->update(['suspended' => true]);
        $this->patchJson("/api/v1/websites/{$website->id}/security", ['firewall' => ['mode' => 'block']])->assertStatus(409);
        $website->customer->update(['suspended' => false]);
        $website->update(['kind' => Website::WORKER]);
        $this->patchJson("/api/v1/websites/{$website->id}/security", ['firewall' => ['mode' => 'block']])->assertStatus(409);
        $this->assertSame(0, AgentJob::count());
    }

    public function test_firewall_events_come_from_the_agent(): void
    {
        $website = $this->site();
        $this->actingWith($this->user(Role::Owner, $website->customer));
        Http::fake([self::AGENT.'GetFirewallEvents' => Http::response(['events' => [
            ['at' => '2026-11-22T21:26:59Z', 'clientIp' => '203.0.113.9', 'host' => 'acme.example', 'uri' => '/?q=<script>', 'action' => 'blocked', 'score' => 20,
                'rules' => [['id' => 941100, 'message' => 'XSS Attack Detected via libinjection', 'data' => 'Matched Data: XSS']]],
            // The JSON of the agent has no field with an empty value.
            ['clientIp' => '198.51.100.4', 'uri' => '/', 'action' => 'detected', 'score' => 5, 'rules' => [['id' => 913100]]],
        ]])]);

        $this->getJson("/api/v1/websites/{$website->id}/security/events?limit=10")->assertOk()
            ->assertJsonPath('events.0.rules.0.id', 941100)->assertJsonPath('events.0.host', 'acme.example')
            ->assertJsonPath('events.1.at', null)->assertJsonPath('events.1.host', null)->assertJsonPath('events.1.rules.0.message', '');
        Http::assertSent(fn ($r) => $r['websiteId'] === $website->id && $r['limit'] === 10);
        $this->getJson("/api/v1/websites/{$website->id}/security/events?limit=500")->assertJsonValidationErrors('limit');
    }
}
