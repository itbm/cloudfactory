<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Jobs\RunUptimeChecks;
use App\Models\AuditEntry;
use App\Models\Customer;
use App\Models\MailServer;
use App\Models\NotificationDelivery;
use App\Models\Plugin;
use App\Models\PluginDelivery;
use App\Models\Server;
use App\Models\UptimeCheck;
use App\Models\UptimeIncident;
use App\Models\UptimeResult;
use App\Models\User;
use App\Models\Website;
use App\Services\Plugins\Manifest;
use App\Services\Plugins\Outbound;
use App\Services\Uptime\Monitor;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Request as HttpRequest;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

/** Uptime checks of websites: the API, the schedule, the requests, the incidents and the emails. */
class UptimeTest extends TestCase
{
    private Server $srv;

    private Customer $acme;

    private Website $site;

    private User $owner;

    /** The addresses that the fake DNS gives: host => addresses. */
    private array $dns = [];

    protected function setUp(): void
    {
        parent::setUp();
        $this->srv = $this->server(['addresses' => ['10.20.0.5']]);
        $this->acme = $this->customer();
        $this->site = $this->website($this->acme, $this->srv, $this->image(), 'shop.acme.example');
        $this->owner = $this->user(Role::Owner, $this->acme);
        $this->dns = ['shop.acme.example' => ['203.0.113.50']];
        $dns = &$this->dns;
        $this->app->instance(Outbound::class, new class($dns) extends Outbound
        {
            public function __construct(public array &$dns) {}

            public function addresses(string $host): array
            {
                return $this->dns[$host] ?? [];
            }
        });
        Http::preventStrayRequests();
        MailServer::create(['host' => 'smtp.example.com', 'port' => 587, 'encryption' => 'starttls', 'from_address' => 'panel@example.com']);
    }

    private function addCheck(array $values = [], ?Website $website = null): array
    {
        $website ??= $this->site;

        return $this->postJson("/api/v1/websites/{$website->id}/uptime-checks", $values + ['domain' => $website->name])->assertCreated()->json('uptimeCheck');
    }

    /** Runs the checks as the queue does. */
    private function runChecks(): void
    {
        (new RunUptimeChecks(UptimeCheck::pluck('id')->all()))->handle(app(Monitor::class));
    }

    public function test_an_owner_adds_a_check_with_the_defaults_and_the_panel_checks_the_input(): void
    {
        $this->actingWith($this->owner);
        $check = $this->addCheck();
        $this->assertSame('https://shop.acme.example/', $check['url']);
        $this->assertSame(300, $check['intervalSeconds']);
        $this->assertSame(10, $check['timeoutSeconds']);
        $this->assertSame(2, $check['failuresBeforeAlert']);
        $this->assertNull($check['expectedStatus']);
        $this->assertSame('pending', $check['state']);
        $this->assertNull($check['lastResult']);
        $this->assertSame(['day' => null, 'week' => null, 'month' => null], $check['uptime']);

        $url = "/api/v1/websites/{$this->site->id}/uptime-checks";
        $this->postJson($url, ['domain' => 'other.example'])->assertUnprocessable()->assertJsonValidationErrors('domain');
        $this->postJson($url, ['domain' => 'shop.acme.example', 'path' => 'health'])->assertUnprocessable()->assertJsonValidationErrors('path');
        $this->postJson($url, ['domain' => 'shop.acme.example', 'path' => '/a b'])->assertUnprocessable()->assertJsonValidationErrors('path');
        $this->postJson($url, ['domain' => 'shop.acme.example', 'intervalSeconds' => 30])->assertUnprocessable()->assertJsonValidationErrors('intervalSeconds');
        $this->postJson($url, ['domain' => 'shop.acme.example', 'timeoutSeconds' => 60])->assertUnprocessable()->assertJsonValidationErrors('timeoutSeconds');
        $this->postJson($url, ['domain' => 'shop.acme.example', 'keyword' => "two\nlines"])->assertUnprocessable()->assertJsonValidationErrors('keyword');
        // The same address two times.
        $this->postJson($url, ['domain' => 'SHOP.acme.example'])->assertUnprocessable()->assertJsonValidationErrors('path');

        foreach (['/a', '/b?full=1', '/c', '/d'] as $path) {
            $this->addCheck(['path' => $path, 'intervalSeconds' => 60, 'expectedStatus' => 200, 'keyword' => ' Welcome ']);
        }
        $this->assertSame('Welcome', UptimeCheck::where('path', '/a')->value('keyword'));
        $this->postJson($url, ['domain' => 'shop.acme.example', 'path' => '/e'])->assertUnprocessable()->assertJsonPath('errors.domain.0', 'A website can have 5 uptime checks. Remove one first.');
        $this->assertSame('Added uptime check', AuditEntry::latest('id')->value('action'));
    }

    public function test_a_private_website_has_no_check_and_each_user_sees_only_their_own_checks(): void
    {
        $private = $this->website($this->acme, $this->srv, $this->image(), 'orders.internal', ['kind' => Website::PRIVATE]);
        $this->actingWith($this->owner);
        $this->postJson("/api/v1/websites/{$private->id}/uptime-checks", ['domain' => 'orders.internal'])->assertStatus(409);
        $check = $this->addCheck();

        // A different customer does not see the check.
        $other = $this->user(Role::Owner, $this->customer(null, ['name' => 'Other', 'email' => 'other@example.com']));
        $this->actingWith($other);
        $this->getJson("/api/v1/websites/{$this->site->id}/uptime-checks")->assertNotFound();
        $this->patchJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}", ['enabled' => false])->assertNotFound();
        $this->getJson('/api/v1/uptime-checks')->assertOk()->assertJsonCount(0, 'data')->assertJsonPath('summary.total', 0);

        // The reseller of the customer and an administrator see it.
        $reseller = Customer::create(['name' => 'Reseller', 'email' => 'r@example.com', 'role' => 'reseller', 'plan_id' => $this->acme->plan_id]);
        $this->acme->update(['reseller_id' => $reseller->id]);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->getJson('/api/v1/uptime-checks')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.websiteName', 'shop.acme.example');
        $this->actingWith($this->user());
        $this->getJson('/api/v1/uptime-checks')->assertOk()->assertJsonPath('summary', ['total' => 1, 'up' => 0, 'down' => 0, 'pending' => 1, 'paused' => 0]);

        // A token with the read scope reads, and cannot change.
        $this->actingWith($this->owner, ['websites:read']);
        $this->getJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}")->assertOk();
        $this->deleteJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}")->assertForbidden();
        $this->actingWith($this->owner, ['dns:read']);
        $this->getJson('/api/v1/uptime-checks')->assertForbidden();
    }

    public function test_the_schedule_sends_the_checks_that_are_due_and_not_the_paused_checks(): void
    {
        $this->travelTo(Carbon::parse('2026-11-20 10:00:30', 'UTC'));
        $this->actingWith($this->user());
        $due = $this->addCheck();
        $off = $this->addCheck(['path' => '/off', 'enabled' => false]);
        $stopped = $this->website($this->acme, $this->srv, $this->image(), 'old.acme.example', ['status' => 'stopped']);
        $this->addCheck([], $stopped);
        $suspended = $this->customer(null, ['name' => 'Suspended', 'email' => 's@example.com', 'suspended' => true]);
        $this->addCheck([], $this->website($suspended, $this->srv, $this->image(), 'gone.example'));

        $this->assertSame(1, app(Monitor::class)->schedule());
        Queue::assertPushed(RunUptimeChecks::class, fn (RunUptimeChecks $job) => $job->checkIds === [$due['id']]);
        // The check is not due again until its result comes, or for 10 minutes.
        $this->assertSame(0, app(Monitor::class)->schedule());
        $this->assertEquals(now()->addSeconds(Monitor::LEASE_SECONDS), UptimeCheck::find($due['id'])->next_check_at);

        $this->getJson("/api/v1/websites/{$this->site->id}/uptime-checks")->assertOk()
            ->assertJsonPath('data.1.id', $off['id'])->assertJsonPath('data.1.state', 'paused')->assertJsonPath('data.1.pausedReason', 'The check is off.');
        $this->getJson("/api/v1/websites/{$stopped->id}/uptime-checks")->assertJsonPath('data.0.pausedReason', 'The website does not run.');
        $this->assertSame(3, collect($this->getJson('/api/v1/uptime-checks?state=paused')->json('data'))->count());
    }

    public function test_a_check_goes_down_after_two_failures_and_up_again_with_an_email_each_time(): void
    {
        $this->travelTo(Carbon::parse('2026-11-20 10:00:10', 'UTC'));
        $this->actingWith($this->owner);
        $check = $this->addCheck(['intervalSeconds' => 300]);
        $admin = $this->user();
        $status = 200;
        Http::fake(function (HttpRequest $request) use (&$status) {
            $this->assertSame('https://shop.acme.example/', $request->url());
            $this->assertSame('Cloudfactory-Uptime/1', $request->header('User-Agent')[0]);

            return Http::response('<h1>Shop</h1>', $status);
        });

        $this->runChecks();
        $row = UptimeCheck::find($check['id']);
        $this->assertSame('up', $row->state);
        $this->assertSame(200, $row->last_status);
        $this->assertEquals(Carbon::parse('2026-11-20 10:05:00', 'UTC'), $row->next_check_at);

        // The first failure: the check stays up, and the next request is after 1 minute.
        $status = 503;
        $this->travelTo(Carbon::parse('2026-11-20 10:05:05', 'UTC'));
        $this->runChecks();
        $row->refresh();
        $this->assertSame('up', $row->state);
        $this->assertSame(1, $row->failures);
        $this->assertEquals(Carbon::parse('2026-11-20 10:06:00', 'UTC'), $row->next_check_at);
        $this->assertSame(0, UptimeIncident::count());

        // The second failure: the check is down from the first failure.
        $this->travelTo(Carbon::parse('2026-11-20 10:06:05', 'UTC'));
        $this->runChecks();
        $row->refresh();
        $this->assertSame('down', $row->state);
        $this->assertSame('HTTP status 503.', $row->last_error);
        $incident = UptimeIncident::sole();
        $this->assertEquals(Carbon::parse('2026-11-20 10:05:05', 'UTC'), $incident->started_at);
        $email = NotificationDelivery::where('event', 'uptime.down')->sole();
        $this->assertSame($this->owner->email, $email->recipient);
        $this->assertSame('shop.acme.example is down', $email->subject);
        $this->assertStringContainsString('Error: HTTP status 503.', $email->body);
        // An administrator gets the uptime emails only after they turn them on.
        $this->assertFalse(NotificationDelivery::where('recipient', $admin->email)->exists());

        $api = $this->getJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}")->assertOk();
        $api->assertJsonPath('state', 'down')->assertJsonPath('incident.id', $incident->id)->assertJsonPath('since', '2026-11-20T10:05:05+00:00');
        // A check that is down keeps its interval.
        $this->assertEquals(Carbon::parse('2026-11-20 10:11:00', 'UTC'), $row->next_check_at);

        // More failures make no second email.
        $this->travelTo(Carbon::parse('2026-11-20 10:11:05', 'UTC'));
        $this->runChecks();
        $this->assertSame(1, NotificationDelivery::where('event', 'uptime.down')->count());

        $status = 200;
        $this->travelTo(Carbon::parse('2026-11-20 10:16:05', 'UTC'));
        $this->runChecks();
        $this->assertSame('up', $row->fresh()->state);
        $this->assertEquals(Carbon::parse('2026-11-20 10:16:05', 'UTC'), $incident->fresh()->resolved_at);
        $up = NotificationDelivery::where('event', 'uptime.up')->sole();
        $this->assertSame('shop.acme.example is up again', $up->subject);
        $this->assertStringContainsString('It was down for 11 minutes', $up->body);
        $this->assertSame(5, UptimeResult::count());
    }

    public function test_an_administrator_who_turns_the_event_on_gets_the_email_and_a_plugin_gets_the_webhook(): void
    {
        $admin = $this->user();
        $this->actingWith($admin);
        $this->putJson('/api/v1/me/notification-preferences', ['events' => ['uptime.down' => true]])->assertOk();
        $this->addCheck(['failuresBeforeAlert' => 1]);
        Plugin::create([
            'slug' => 'status', 'name' => 'Status', 'version' => '1.0.0', 'publisher' => 'Example', 'base_url' => 'https://status.example.net',
            'webhook_secret' => 'secret', 'manifest' => json_encode(['scopes' => ['websites:read'], 'events' => ['uptime.down', 'uptime.up'], 'webhook' => '/events']),
        ]);
        Http::fake(fn () => throw new ConnectionException('cURL error 28: Operation timed out after 10001 milliseconds with 0 bytes received (see https://curl.haxx.se/libcurl/c/libcurl-errors.html) for https://shop.acme.example/'));

        $this->runChecks();
        $this->assertSame('No answer in 10 seconds.', UptimeIncident::sole()->error);
        $this->assertTrue(NotificationDelivery::where(['event' => 'uptime.down', 'recipient' => $admin->email])->exists());
        $delivery = PluginDelivery::where('event', 'uptime.down')->sole();
        $this->assertSame('https://shop.acme.example/', $delivery->payload['data']['check']['url']);
        $this->assertSame('shop.acme.example', $delivery->payload['data']['website']['name']);
        $this->assertTrue(Manifest::permits(['websites:read'], 'uptime.up'));
        $this->assertFalse(Manifest::permits(['dns:read'], 'uptime.up'));
    }

    public function test_the_status_the_text_and_the_address_of_the_answer(): void
    {
        $this->actingWith($this->owner);
        $this->addCheck(['path' => '/status', 'expectedStatus' => 204]);
        $this->addCheck(['path' => '/home', 'keyword' => 'Add to basket']);
        $this->addCheck(['path' => '/moved']);
        Http::fake([
            'shop.acme.example/status' => Http::response('', 200),
            'shop.acme.example/home' => Http::response('<p>Database error</p>', 200),
            // A redirect is up: the panel does not follow it.
            'shop.acme.example/moved' => Http::response('', 301, ['Location' => 'https://elsewhere.example/']),
        ]);
        $this->runChecks();
        $this->assertSame('HTTP status 200. The check expects 204.', UptimeCheck::where('path', '/status')->value('last_error'));
        $this->assertSame('The answer does not have the text "Add to basket".', UptimeCheck::where('path', '/home')->value('last_error'));
        $this->assertNull(UptimeCheck::where('path', '/moved')->value('last_error'));
        $this->assertSame(301, UptimeCheck::where('path', '/moved')->value('last_status'));
        Http::assertSentCount(3);
    }

    public function test_the_panel_connects_only_to_public_addresses_and_to_its_servers(): void
    {
        $this->actingWith($this->owner);
        $this->addCheck(['failuresBeforeAlert' => 1]);
        Http::fake(['*' => Http::response('ok')]);

        // The DNS of the customer points to the private network of the operator.
        $this->dns['shop.acme.example'] = ['203.0.113.50', '10.0.0.8'];
        $this->runChecks();
        $this->assertSame('shop.acme.example has the address 10.0.0.8, which is not public. The panel connects only to public addresses and to the addresses of its servers.', UptimeCheck::sole()->last_error);
        foreach (['127.0.0.1', '169.254.169.254', '::1', 'fd00::1', '100.64.0.1'] as $ip) {
            $this->dns['shop.acme.example'] = [$ip];
            $this->runChecks();
            $this->assertStringContainsString("the address {$ip}, which is not public", (string) UptimeCheck::sole()->last_error);
        }
        Http::assertNothingSent();

        // The private address of a server of the panel is permitted.
        $this->dns['shop.acme.example'] = ['10.20.0.5'];
        $this->runChecks();
        $this->assertSame('up', UptimeCheck::sole()->state);
        Http::assertSentCount(1);

        // No address: the check fails.
        $this->dns['shop.acme.example'] = [];
        $this->runChecks();
        $this->assertStringStartsWith('The panel cannot find the address of shop.acme.example.', (string) UptimeCheck::sole()->last_error);
    }

    public function test_no_answer_from_all_websites_on_three_servers_is_a_problem_of_the_panel(): void
    {
        $this->actingWith($this->user());
        foreach (range(1, 5) as $i) {
            $server = $i <= 3 ? $this->server() : $this->srv;
            $website = $this->website($this->acme, $server, $this->image(), "site{$i}.example");
            $this->dns["site{$i}.example"] = ['203.0.113.'.$i];
            $this->addCheck(['failuresBeforeAlert' => 1], $website);
        }
        $answer = null;
        Http::fake(function (HttpRequest $request) use (&$answer) {
            if ($answer && str_contains($request->url(), $answer)) {
                return Http::response('ok');
            }
            throw new ConnectionException('cURL error 7: Failed to connect to site1.example port 443 after 3 ms: Could not connect to server');
        });
        $this->runChecks();
        $this->assertSame(0, UptimeResult::count());
        $this->assertSame(0, UptimeIncident::count());
        $this->assertSame(5, UptimeCheck::where('state', 'pending')->count());

        // With one answer, the failures are real.
        $answer = 'site5';
        $this->runChecks();
        $this->assertSame(4, UptimeIncident::count());
        $this->assertSame('The server refused the connection, or the panel cannot connect to it.', UptimeIncident::first()->error);
    }

    public function test_a_check_that_stops_while_it_is_down_ends_its_incident_with_no_email(): void
    {
        $this->actingWith($this->owner);
        $check = $this->addCheck(['failuresBeforeAlert' => 1]);
        Http::fake(['*' => Http::response('', 500)]);
        $this->runChecks();
        $this->assertSame('down', UptimeCheck::sole()->state);

        $this->patchJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}", ['enabled' => false])->assertOk()
            ->assertJsonPath('uptimeCheck.state', 'paused')->assertJsonPath('uptimeCheck.incident', null);
        $incident = UptimeIncident::sole();
        $this->assertNotNull($incident->resolved_at);
        $this->assertTrue($incident->ended_by_pause);
        $this->assertSame('pending', UptimeCheck::sole()->state);
        $this->assertSame(0, NotificationDelivery::where('event', 'uptime.up')->count());

        // A stopped website: the schedule ends the incident.
        $this->patchJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}", ['enabled' => true])->assertOk();
        $this->runChecks();
        $this->assertSame(1, UptimeIncident::whereNull('resolved_at')->count());
        $this->site->update(['status' => 'stopped']);
        app(Monitor::class)->schedule();
        $this->assertSame(0, UptimeIncident::whereNull('resolved_at')->count());
        $this->assertSame('pending', UptimeCheck::sole()->state);
    }

    public function test_a_change_of_the_address_starts_the_check_again(): void
    {
        $this->actingWith($this->owner);
        $this->site->domains()->create(['name' => 'www.shop.acme.example', 'kind' => 'alias', 'verification_token' => 'cf-test', 'verified_at' => now()]);
        $check = $this->addCheck();
        Http::fake(['*' => Http::response('ok')]);
        $this->runChecks();
        $this->assertSame('up', UptimeCheck::sole()->state);

        $url = "/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}";
        $this->patchJson($url, ['failuresBeforeAlert' => 3, 'intervalSeconds' => 60])->assertOk()->assertJsonPath('uptimeCheck.state', 'up');
        $this->patchJson($url, ['domain' => 'www.shop.acme.example', 'path' => '/health'])->assertOk()
            ->assertJsonPath('uptimeCheck.url', 'https://www.shop.acme.example/health')->assertJsonPath('uptimeCheck.state', 'pending');
        $this->patchJson($url, ['expectedStatus' => null, 'keyword' => ''])->assertOk()->assertJsonPath('uptimeCheck.keyword', null);

        // When the domain goes, its checks go too.
        $this->site->domains()->where('name', 'www.shop.acme.example')->delete();
        $this->assertSame(0, UptimeCheck::count());
    }

    public function test_the_uptime_percent_the_history_and_the_days(): void
    {
        $this->travelTo(Carbon::parse('2026-11-01 00:00:00', 'UTC'));
        $this->actingWith($this->owner);
        $id = $this->addCheck(['intervalSeconds' => 60])['id'];
        $check = UptimeCheck::find($id);
        $check->update(['state' => 'up', 'last_checked_at' => now()]);
        $this->travelTo(Carbon::parse('2026-11-20 12:00:00', 'UTC'));
        // 36 minutes down yesterday evening, and 1 day down 10 days ago.
        $check->incidents()->create(['started_at' => '2026-11-19 20:00:00', 'resolved_at' => '2026-11-19 20:36:00', 'error' => 'HTTP status 502.']);
        $check->incidents()->create(['started_at' => '2026-11-10 12:00:00', 'resolved_at' => '2026-11-11 12:00:00', 'error' => 'No answer in 10 seconds.']);
        $check->results()->createMany([
            ['checked_at' => '2026-11-20 11:01:00', 'ok' => true, 'status' => 200, 'response_ms' => 100],
            ['checked_at' => '2026-11-20 11:02:00', 'ok' => true, 'status' => 200, 'response_ms' => 300],
            ['checked_at' => '2026-11-20 11:03:00', 'ok' => false, 'status' => 503, 'response_ms' => 40, 'error' => 'HTTP status 503.'],
        ]);

        $body = $this->getJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$id}")->assertOk()->json();
        $this->assertSame(97.5, $body['uptime']['day']);
        $this->assertSame(99.64, $body['uptime']['week']);
        // The check was made on 1 November: 19.5 days.
        $this->assertSame(94.74, $body['uptime']['month']);
        $this->assertCount(48, $body['history']);
        $point = collect($body['history'])->firstWhere('at', '2026-11-20T11:00:00Z');
        $this->assertSame(['at' => '2026-11-20T11:00:00Z', 'checks' => 3, 'failures' => 1, 'responseMs' => 147, 'maxResponseMs' => 300], $point);
        $this->assertCount(30, $body['days']);
        $this->assertSame(['date' => '2026-11-19', 'uptime' => 97.5, 'downSeconds' => 2160], $body['days'][28]);
        $this->assertEquals(50, collect($body['days'])->firstWhere('date', '2026-11-10')['uptime']);
        $this->assertEquals(100, $body['days'][29]['uptime']);
        $this->assertCount(2, $body['incidents']);
        $this->assertSame(86400, $body['incidents'][1]['durationSeconds']);

        $this->getJson('/api/v1/uptime-incidents')->assertOk()->assertJsonCount(2, 'data')->assertJsonPath('data.0.websiteName', 'shop.acme.example');
        $this->getJson('/api/v1/uptime-incidents?state=open')->assertOk()->assertJsonCount(0, 'data');
        $this->assertSame('1 hour 5 minutes', Monitor::duration(3900));
        $this->assertSame('2 days 3 hours', Monitor::duration(2 * 86400 + 3 * 3600 + 120));
        $this->assertSame('less than 1 minute', Monitor::duration(30));
    }

    public function test_check_now_gives_the_result_and_the_panel_removes_old_data(): void
    {
        $this->actingWith($this->owner);
        $check = $this->addCheck();
        Http::fake(['*' => Http::response('ok', 200)]);
        $this->postJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}/run")->assertOk()
            ->assertJsonPath('result.ok', true)->assertJsonPath('result.status', 200)->assertJsonPath('uptimeCheck.state', 'up');
        $this->patchJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}", ['enabled' => false])->assertOk();
        $this->postJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}/run")->assertStatus(409);

        UptimeResult::create(['uptime_check_id' => $check['id'], 'checked_at' => now()->subDays(9), 'ok' => true]);
        UptimeIncident::create(['uptime_check_id' => $check['id'], 'started_at' => now()->subDays(100), 'resolved_at' => now()->subDays(99), 'error' => 'x']);
        UptimeIncident::create(['uptime_check_id' => $check['id'], 'started_at' => now()->subDays(10), 'resolved_at' => now()->subDays(9), 'error' => 'x']);
        $this->artisan('cloudfactory:prune-uptime')->assertSuccessful();
        $this->assertSame(1, UptimeResult::count());
        $this->assertSame(1, UptimeIncident::count());

        $this->deleteJson("/api/v1/websites/{$this->site->id}/uptime-checks/{$check['id']}")->assertNoContent();
        $this->assertSame(0, UptimeCheck::count());
        $this->assertSame(0, UptimeResult::count());
    }
}
