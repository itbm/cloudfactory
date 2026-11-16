<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\DeliverPluginEvent;
use App\Models\AgentJob;
use App\Models\Plugin;
use App\Models\PluginDelivery;
use App\Models\User;
use App\Services\Audit;
use App\Services\JobOutcome;
use App\Services\Plugins\Events;
use App\Services\Plugins\Outbound;
use Illuminate\Http\Client\Request as HttpRequest;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Queue;
use Laravel\Sanctum\PersonalAccessToken;
use Tests\TestCase;

/**
 * Plugins: programs outside the panel with their own token, signed webhooks and pages in a
 * frame. A plugin must never get more access than its manifest and the administrator give.
 */
class PluginTest extends TestCase
{
    /** @var array{private: \OpenSSLAsymmetricKey, public: string} */
    private array $key;

    protected function setUp(): void
    {
        parent::setUp();
        $private = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $this->key = ['private' => $private, 'public' => openssl_pkey_get_details($private)['key']];
        // The tests resolve each host name to a public address.
        $this->app->instance(Outbound::class, new class extends Outbound
        {
            public array $map = [];

            public function addresses(string $host): array
            {
                return $this->map[$host] ?? ['203.0.113.50'];
            }
        });
    }

    /** @param array<string, mixed> $changes */
    private function manifest(array $changes = []): string
    {
        return json_encode(array_merge([
            'schema' => 1,
            'slug' => 'uptime',
            'name' => 'Uptime monitor',
            'version' => '1.0.0',
            'publisher' => 'Example Ltd',
            'description' => 'Checks each website every minute.',
            'scopes' => ['websites:read', 'audit:read'],
            'events' => ['audit.recorded', 'job.finished'],
            'webhook' => '/hooks/cloudfactory',
            'pages' => [['id' => 'status', 'title' => 'Uptime', 'path' => '/panel/status', 'roles' => ['administrator', 'owner']]],
            'commands' => [['name' => 'uptime', 'description' => 'Shows the uptime of the websites.']],
        ], $changes), JSON_UNESCAPED_SLASHES);
    }

    private function sign(string $manifest): string
    {
        openssl_sign($manifest, $signature, $this->key['private'], OPENSSL_ALGO_SHA256);

        return base64_encode($signature);
    }

    private function admin(): User
    {
        $admin = $this->user(Role::Administrator);
        $this->actingWith($admin);

        return $admin;
    }

    private function trustKey(): string
    {
        return $this->postJson('/api/v1/plugin-keys', ['name' => 'Example Ltd', 'publicKey' => $this->key['public'], 'password' => 'password'])
            ->assertCreated()->json('id');
    }

    /** @return array{plugin: array<string, mixed>, token: string, webhookSecret: string} */
    private function install(?string $manifest = null): array
    {
        $manifest ??= $this->manifest();

        return $this->postJson('/api/v1/plugins', [
            'manifest' => $manifest, 'signature' => $this->sign($manifest),
            'baseUrl' => 'https://uptime.example.net', 'password' => 'password',
        ])->assertCreated()->json();
    }

    public function test_an_administrator_installs_a_signed_plugin_with_the_scopes_of_its_manifest(): void
    {
        $this->admin();
        $this->trustKey();
        $manifest = $this->manifest();

        $this->postJson('/api/v1/plugins/preview', ['manifest' => $manifest, 'signature' => $this->sign($manifest)])->assertOk()
            ->assertJsonPath('manifest.scopes', ['audit:read', 'websites:read'])
            ->assertJsonPath('signedBy.name', 'Example Ltd');

        $out = $this->install($manifest);
        $this->assertStringStartsWith('whsec_', $out['webhookSecret']);
        $this->assertSame('https://uptime.example.net/hooks/cloudfactory', $out['plugin']['webhookUrl']);
        $this->assertSame('https://uptime.example.net/panel/status', $out['plugin']['pages'][0]['url']);
        $this->assertSame('https://uptime.example.net', $out['plugin']['pages'][0]['origin']);
        $this->assertNotNull($out['plugin']['tokenExpiresAt']);

        $token = PersonalAccessToken::findToken($out['token']);
        $this->assertSame(['audit:read', 'websites:read'], $token->abilities);
        $pluginUser = $token->tokenable;
        $this->assertTrue($pluginUser->isPlugin());

        // The user of the plugin is not in the list of users, and nobody signs in as it.
        $this->getJson('/api/v1/users')->assertOk()->assertJsonMissing(['email' => $pluginUser->email]);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Installed plugin', 'target' => 'Uptime monitor 1.0.0']);
    }

    public function test_the_plugin_token_has_only_the_scopes_of_the_manifest(): void
    {
        $this->admin();
        $this->trustKey();
        $token = $this->install()['token'];
        $this->app['auth']->forgetGuards();

        $this->withToken($token)->getJson('/api/v1/websites')->assertOk();
        $this->withToken($token)->getJson('/api/v1/audit')->assertOk();
        $this->withToken($token)->postJson('/api/v1/websites', [])->assertForbidden();
        $this->withToken($token)->getJson('/api/v1/servers')->assertForbidden();
        $this->withToken($token)->getJson('/api/v1/tokens')->assertForbidden();
        $this->withToken($token)->getJson('/api/v1/plugins')->assertForbidden();
        // A plugin token is not a sign-in session, so it gets no page token.
        $pluginId = Plugin::first()->id;
        $this->withToken($token)->postJson("/api/v1/plugin-pages/{$pluginId}/status/session")->assertForbidden();
    }

    public function test_a_manifest_cannot_ask_for_the_scopes_that_give_more_access(): void
    {
        $this->admin();
        $this->trustKey();
        foreach (['tokens:write', 'users:write', 'plugins:write', 'plugins:read', 'tokens:read', '*'] as $scope) {
            $manifest = $this->manifest(['scopes' => [$scope]]);
            $this->postJson('/api/v1/plugins/preview', ['manifest' => $manifest])->assertUnprocessable()->assertJsonValidationErrors('scopes.0');
        }
        $this->postJson('/api/v1/plugins/preview', ['manifest' => $this->manifest(['webhook' => '/../etc'])])->assertJsonValidationErrors('webhook');
        $this->postJson('/api/v1/plugins/preview', ['manifest' => $this->manifest(['events' => ['website.secret']])])->assertJsonValidationErrors('events.0');
        $this->postJson('/api/v1/plugins/preview', ['manifest' => '[1,2]'])->assertJsonValidationErrors('manifest');
    }

    public function test_the_panel_refuses_a_manifest_with_no_trusted_signature(): void
    {
        $this->admin();
        $manifest = $this->manifest();
        // No trusted key.
        $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'baseUrl' => 'https://uptime.example.net', 'password' => 'password'])
            ->assertJsonValidationErrors('signature');
        $this->trustKey();
        // A change after the signature.
        $this->postJson('/api/v1/plugins', ['manifest' => $this->manifest(['scopes' => ['websites:write', 'audit:read']]), 'signature' => $this->sign($manifest), 'baseUrl' => 'https://uptime.example.net', 'password' => 'password'])
            ->assertJsonValidationErrors('signature');
        // No signature.
        $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'baseUrl' => 'https://uptime.example.net', 'password' => 'password'])
            ->assertJsonValidationErrors('signature');
        // The wrong password.
        $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'baseUrl' => 'https://uptime.example.net', 'password' => 'wrong'])
            ->assertJsonValidationErrors('password');
        $this->assertSame(0, Plugin::count());

        // The developer mode accepts no signature, and needs the password to turn on.
        $this->putJson('/api/v1/plugin-settings', ['developerMode' => true])->assertJsonValidationErrors('password');
        $this->putJson('/api/v1/plugin-settings', ['developerMode' => true, 'password' => 'password'])->assertOk();
        $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'baseUrl' => 'http://plugin.localhost:9000', 'password' => 'password'])
            ->assertCreated()->assertJsonPath('plugin.signedBy', null);
    }

    public function test_the_address_of_a_plugin_uses_https_and_is_not_the_panel(): void
    {
        $this->admin();
        $this->trustKey();
        config(['app.url' => 'https://panel.example.com']);
        $manifest = $this->manifest();
        foreach (['http://uptime.example.net', 'https://panel.example.com', 'https://user:pw@uptime.example.net', 'https://uptime.example.net/?a=1', 'ftp://x'] as $url) {
            $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'baseUrl' => $url, 'password' => 'password'])
                ->assertJsonValidationErrors('baseUrl');
        }
    }

    public function test_a_page_token_has_the_rights_of_the_user_and_the_scopes_of_the_plugin(): void
    {
        $this->admin();
        $this->trustKey();
        $pluginId = $this->install()['plugin']['id'];
        $customer = $this->customer();
        $mine = $this->website($customer, $this->server(), $this->image());
        $other = $this->website($this->customer(), $this->server(['name' => 'web-2']), $this->image(), 'other.example');
        $owner = $this->user(Role::Owner, $customer);
        $reseller = $this->user(Role::Reseller, $this->customer(null, ['role' => 'reseller']));
        $this->totp($reseller);

        // The owner sees the page in the menu, and the reseller does not.
        $session = $owner->createToken('Web session', ['*'])->plainTextToken;
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->getJson('/api/v1/plugin-pages')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.title', 'Uptime');
        $resellerSession = $reseller->createToken('Web session', ['*'])->plainTextToken;
        $this->app['auth']->forgetGuards();
        $this->withToken($resellerSession)->getJson('/api/v1/plugin-pages')->assertOk()->assertJsonCount(0, 'data');
        $this->withToken($resellerSession)->postJson("/api/v1/plugin-pages/{$pluginId}/status/session")->assertNotFound();

        $this->app['auth']->forgetGuards();
        $page = $this->withToken($session)->postJson("/api/v1/plugin-pages/{$pluginId}/status/session")->assertCreated()
            ->assertJsonPath('user.role', 'owner')->json();
        $this->app['auth']->forgetGuards();
        $this->withToken($page['token'])->getJson('/api/v1/websites')->assertOk()
            ->assertJsonFragment(['id' => $mine->id])->assertJsonMissing(['id' => $other->id]);
        $this->withToken($page['token'])->deleteJson("/api/v1/websites/{$mine->id}")->assertForbidden();
        // A page token is not a session: it cannot make a token or a second page token.
        $this->withToken($page['token'])->postJson("/api/v1/plugin-pages/{$pluginId}/status/session")->assertForbidden();
        $this->assertLessThanOrEqual(15, now()->diffInMinutes(PersonalAccessToken::findToken($page['token'])->expires_at));
    }

    public function test_a_plugin_that_is_off_has_no_access(): void
    {
        $admin = $this->admin();
        $this->trustKey();
        $out = $this->install();
        $id = $out['plugin']['id'];
        $this->totp($admin);
        $session = $admin->createToken('Web session', ['*'])->plainTextToken;
        $this->app['auth']->forgetGuards();
        $page = $this->withToken($session)->postJson("/api/v1/plugin-pages/{$id}/status/session")->assertCreated()->json('token');

        $this->app['auth']->forgetGuards();
        $this->withToken($session)->patchJson("/api/v1/plugins/{$id}", ['enabled' => false])->assertOk()->assertJsonPath('enabled', false);
        $this->app['auth']->forgetGuards();
        $this->withToken($out['token'])->getJson('/api/v1/websites')->assertForbidden();
        $this->assertNull(PersonalAccessToken::findToken($page));
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->getJson('/api/v1/plugin-pages')->assertJsonCount(0, 'data');

        // Removed: the token and the user are gone.
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->deleteJson("/api/v1/plugins/{$id}")->assertNoContent();
        $this->assertNull(PersonalAccessToken::findToken($out['token']));
        $this->assertSame(0, User::whereNotNull('plugin_id')->count());
    }

    public function test_an_update_changes_the_scopes_of_the_token_and_new_credentials_stop_the_old_ones(): void
    {
        $this->admin();
        $this->trustKey();
        $out = $this->install();
        $id = $out['plugin']['id'];
        $next = $this->manifest(['version' => '1.1.0', 'scopes' => ['websites:read'], 'events' => []]);
        $this->putJson("/api/v1/plugins/{$id}", ['manifest' => $next, 'signature' => $this->sign($next), 'password' => 'password'])
            ->assertOk()->assertJsonPath('version', '1.1.0');
        $this->assertSame(['websites:read'], PersonalAccessToken::findToken($out['token'])->abilities);
        $other = $this->manifest(['slug' => 'different']);
        $this->putJson("/api/v1/plugins/{$id}", ['manifest' => $other, 'signature' => $this->sign($other), 'password' => 'password'])
            ->assertJsonValidationErrors('manifest');

        $new = $this->postJson("/api/v1/plugins/{$id}/credentials", ['password' => 'password'])->assertOk()->json();
        $this->assertNull(PersonalAccessToken::findToken($out['token']));
        $this->assertNotNull(PersonalAccessToken::findToken($new['token']));
        $this->assertNotSame($out['webhookSecret'], $new['webhookSecret']);
    }

    public function test_the_panel_sends_signed_events_to_the_webhook(): void
    {
        $this->admin();
        $this->trustKey();
        $out = $this->install();
        Http::fake(['uptime.example.net/*' => Http::response('', 204)]);

        $this->postJson('/api/v1/plugin-keys', ['name' => 'Second', 'publicKey' => openssl_pkey_get_details(openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']))['key'], 'password' => 'password'])->assertCreated();
        $delivery = PluginDelivery::where('event', Events::AUDIT_RECORDED)->get()->sole(fn (PluginDelivery $d) => $d->payload['data']['action'] === 'Trusted plugin key');
        Queue::assertPushed(DeliverPluginEvent::class, fn (DeliverPluginEvent $j) => $j->deliveryId === $delivery->id);

        (new DeliverPluginEvent($delivery->id))->handle(app(Outbound::class));
        $this->assertSame(PluginDelivery::DELIVERED, $delivery->fresh()->state);
        Http::assertSent(function (HttpRequest $r) use ($out, $delivery) {
            [$t, $v1] = explode(',', $r->header('Cloudfactory-Signature')[0]);
            $time = (int) substr($t, 2);
            $expected = hash_hmac('sha256', "{$time}.{$r->body()}", $out['webhookSecret']);

            return $r->url() === 'https://uptime.example.net/hooks/cloudfactory'
                && $r->header('Cloudfactory-Event')[0] === 'audit.recorded'
                && $r->header('Cloudfactory-Delivery')[0] === $delivery->id
                && hash_equals($expected, substr($v1, 3));
        });
    }

    public function test_a_failed_delivery_is_sent_again_later_and_then_fails(): void
    {
        $this->admin();
        $this->trustKey();
        $id = $this->install()['plugin']['id'];
        Http::fake(['*' => Http::response('no', 500)]);
        $delivery = $this->postJson("/api/v1/plugins/{$id}/test")->assertAccepted()->json('id');

        for ($i = 1; $i <= 6; $i++) {
            (new DeliverPluginEvent($delivery))->handle(app(Outbound::class));
        }
        $d = PluginDelivery::find($delivery);
        $this->assertSame(PluginDelivery::FAILED, $d->state);
        $this->assertSame(6, $d->attempts);
        $this->assertSame(500, $d->response_status);
        $this->getJson("/api/v1/plugins/{$id}/deliveries")->assertOk()->assertJsonPath('data.0.state', 'failed');
        $this->postJson("/api/v1/plugins/{$id}/deliveries/{$delivery}/retry")->assertAccepted()->assertJsonPath('state', 'pending');
    }

    public function test_the_panel_does_not_send_events_to_its_own_server(): void
    {
        $this->admin();
        $this->trustKey();
        $id = $this->install()['plugin']['id'];
        Http::fake();
        $outbound = app(Outbound::class);
        foreach (['127.0.0.1', '169.254.77.1', '::1', 'fe80::1', '0.0.0.0', '::ffff:127.0.0.1', '224.0.0.1'] as $ip) {
            $outbound->map['uptime.example.net'] = ['198.51.100.7', $ip];
            $delivery = $this->postJson("/api/v1/plugins/{$id}/test")->json('id');
            (new DeliverPluginEvent($delivery))->handle($outbound);
            $this->assertStringContainsString("does not connect to {$ip}", PluginDelivery::find($delivery)->error, $ip);
        }
        Http::assertNothingSent();
        $this->assertNull(Outbound::refused('10.1.2.3', false));
        $this->assertNull(Outbound::refused('127.0.0.1', true));
        $this->assertNotNull(Outbound::refused('169.254.77.1', true));
    }

    public function test_a_finished_job_is_an_event(): void
    {
        $this->admin();
        $this->trustKey();
        $this->install($this->manifest(['events' => ['job.finished']]));
        $customer = $this->customer();
        $site = $this->website($customer, $this->server(), $this->image());
        $job = AgentJob::create(['server_id' => $site->server_id, 'website_id' => $site->id, 'action' => 'SetWebsitePower', 'title' => 'Start acme.example', 'payload' => [], 'steps' => ['Start'], 'step' => 0, 'state' => JobState::Running]);
        app(JobOutcome::class)->finish($job, JobState::Failed, 'The container did not start.');

        $d = PluginDelivery::where('event', Events::JOB_FINISHED)->sole();
        $this->assertSame('failed', $d->payload['data']['state']);
        $this->assertSame($customer->id, $d->payload['data']['customerId']);
        // The plugin wants no audit events.
        $this->assertSame(0, PluginDelivery::where('event', Events::AUDIT_RECORDED)->count());
    }

    public function test_a_plugin_can_receive_only_the_named_events_that_it_wants(): void
    {
        $this->admin();
        $this->trustKey();
        $this->install($this->manifest(['events' => ['website.removed']]));

        Audit::system('Created website', 'acme.example');
        Audit::system('Removed website', 'acme.example');

        $d = PluginDelivery::sole();
        $this->assertSame('website.removed', $d->event);
        $this->assertSame('Removed website', $d->payload['data']['action']);
        $this->assertSame('acme.example', $d->payload['data']['target']);

        $this->postJson('/api/v1/plugins/preview', ['manifest' => $this->manifest(['events' => ['website.exploded']])])
            ->assertUnprocessable()->assertJsonValidationErrors('events.0');
    }

    public function test_a_before_hook_can_refuse_a_change(): void
    {
        $admin = $this->admin();
        $this->trustKey();
        $out = $this->install($this->manifest(['hooks' => [['event' => 'website.removing', 'path' => '/hooks/before']]]));
        $this->assertSame([['event' => 'website.removing', 'path' => '/hooks/before', 'onError' => 'allow']], $out['plugin']['hooks']);
        $site = $this->website($this->customer(), $this->server(), $this->image());

        // Http::fake keeps the first answer for a URL, so the test changes the answer itself.
        $answer = ['allow' => false, 'message' => 'The website has an open invoice.'];
        Http::fake(function () use (&$answer) {
            return Http::response($answer);
        });
        $this->deleteJson("/api/v1/websites/{$site->id}")->assertUnprocessable()
            ->assertJsonPath('errors.website.0', 'The plugin Uptime monitor refused this change: The website has an open invoice.');
        $this->assertNotNull($site->fresh());
        Http::assertSent(function (HttpRequest $r) use ($out, $site, $admin) {
            [$t, $v1] = array_map(fn ($p) => explode('=', $p, 2)[1], explode(',', $r->header('Cloudfactory-Signature')[0]));

            return $r->url() === 'https://uptime.example.net/hooks/before'
                && $r->header('Cloudfactory-Event')[0] === 'website.removing'
                && hash_equals(hash_hmac('sha256', "{$t}.{$r->body()}", $out['webhookSecret']), $v1)
                && $r['data']['id'] === $site->id && $r['user']['id'] === $admin->id;
        });

        $answer = ['allow' => true];
        $this->deleteJson("/api/v1/websites/{$site->id}")->assertAccepted();
    }

    public function test_a_before_hook_that_does_not_answer_allows_the_change_unless_its_manifest_says_refuse(): void
    {
        $this->admin();
        $this->trustKey();
        $id = $this->install($this->manifest(['scopes' => ['customers:read'], 'events' => [], 'hooks' => [['event' => 'customer.creating', 'path' => '/hooks/before']]]))['plugin']['id'];
        $plan = $this->plan();
        Http::fake(['*' => Http::response('down', 503)]);

        $this->postJson('/api/v1/customers', ['name' => 'Acme', 'email' => 'acme@example.com', 'role' => 'owner', 'planId' => $plan->id])->assertCreated();

        $manifest = $this->manifest(['version' => '1.1.0', 'scopes' => ['customers:read'], 'events' => [], 'hooks' => [['event' => 'customer.creating', 'path' => '/hooks/before', 'onError' => 'refuse']]]);
        $this->putJson("/api/v1/plugins/{$id}", ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'password' => 'password'])->assertOk();
        $this->postJson('/api/v1/customers', ['name' => 'Beta', 'email' => 'beta@example.com', 'role' => 'owner', 'planId' => $plan->id])
            ->assertUnprocessable()->assertJsonValidationErrors('name');

        // A plugin that is off is not asked.
        $this->patchJson("/api/v1/plugins/{$id}", ['enabled' => false])->assertOk();
        $this->postJson('/api/v1/customers', ['name' => 'Beta', 'email' => 'beta@example.com', 'role' => 'owner', 'planId' => $plan->id])->assertCreated();
    }

    public function test_an_action_sends_the_plugin_a_token_with_the_rights_of_the_user(): void
    {
        $this->admin();
        $this->trustKey();
        $pluginId = $this->install($this->manifest(['actions' => [
            ['id' => 'check', 'title' => 'Check now', 'target' => 'website', 'path' => '/actions/check', 'roles' => ['owner'], 'confirm' => 'Check the website now?'],
        ]]))['plugin']['id'];
        $customer = $this->customer();
        $mine = $this->website($customer, $this->server(), $this->image());
        $other = $this->website($this->customer(), $this->server(['name' => 'web-2']), $this->image(), 'other.example');
        $owner = $this->user(Role::Owner, $customer);
        $session = $owner->createToken('Web session', ['*'])->plainTextToken;
        $this->app['auth']->forgetGuards();

        $this->withToken($session)->getJson('/api/v1/plugin-actions')->assertOk()->assertJsonCount(1, 'data')
            ->assertJsonPath('data.0.title', 'Check now')->assertJsonPath('data.0.target', 'website')->assertJsonPath('data.0.confirm', 'Check the website now?');

        $sent = null;
        $answer = Http::response(['message' => 'The website answers in 120 ms.', 'url' => 'https://uptime.example.net/report/1']);
        Http::fake(function (HttpRequest $r) use (&$sent, &$answer) {
            $sent = $r;

            return $answer;
        });
        $this->withToken($session)->postJson("/api/v1/plugin-actions/{$pluginId}/check", ['targetId' => $mine->id])->assertOk()
            ->assertJsonPath('message', 'The website answers in 120 ms.')->assertJsonPath('url', 'https://uptime.example.net/report/1');
        $this->assertSame('https://uptime.example.net/actions/check', $sent->url());
        $this->assertSame(['type' => 'website', 'id' => $mine->id, 'name' => $mine->name, 'customerId' => $customer->id], $sent['target']);

        // The token has the rights of the owner and the scopes of the plugin.
        $this->app['auth']->forgetGuards();
        $this->withToken($sent['token'])->getJson('/api/v1/websites')->assertOk()
            ->assertJsonFragment(['id' => $mine->id])->assertJsonMissing(['id' => $other->id]);
        $this->withToken($sent['token'])->deleteJson("/api/v1/websites/{$mine->id}")->assertForbidden();
        // The token is not a session: it cannot run an action.
        $this->withToken($sent['token'])->postJson("/api/v1/plugin-actions/{$pluginId}/check", ['targetId' => $mine->id])->assertForbidden();

        // A website of a different customer is not found. A link to a different site is not given.
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->postJson("/api/v1/plugin-actions/{$pluginId}/check", ['targetId' => $other->id])->assertNotFound();
        $answer = Http::response(['message' => 'Done.', 'url' => 'https://evil.example/login']);
        $this->withToken($session)->postJson("/api/v1/plugin-actions/{$pluginId}/check", ['targetId' => $mine->id])->assertOk()->assertJsonPath('url', null);

        // A plugin that fails gives status 502, and its token stops.
        $answer = Http::response('', 500);
        $this->withToken($session)->postJson("/api/v1/plugin-actions/{$pluginId}/check", ['targetId' => $mine->id])->assertStatus(502);
        $this->assertSame(0, PersonalAccessToken::where('name', "Plugin action {$pluginId}")->count());
    }

    public function test_a_manifest_gives_only_known_targets_and_hooks(): void
    {
        $this->admin();
        $this->postJson('/api/v1/plugins/preview', ['manifest' => $this->manifest([
            'actions' => [['id' => 'x', 'title' => 'X', 'target' => 'agent', 'path' => '/x', 'roles' => ['administrator']]],
            'hooks' => [['event' => 'agent.connecting', 'path' => '/../x']],
        ])])->assertUnprocessable()->assertJsonValidationErrors(['actions.0.target', 'hooks.0.event', 'hooks.0.path']);
    }

    public function test_the_panel_runs_a_plugin_from_its_signed_image_in_a_container(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.example.com', 'app.url' => 'https://panel.example.com']);
        $this->admin();
        $this->trustKey();
        $panel = $this->server(['name' => 'panel', 'local' => true]);
        $image = ['ref' => 'ghcr.io/example/uptime:1.4.0', 'digest' => 'sha256:'.str_repeat('a', 64), 'port' => 9000];
        $manifest = $this->manifest(['image' => $image]);

        $out = $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'hosted' => true, 'password' => 'password'])
            ->assertCreated()
            ->assertJsonPath('plugin.baseUrl', 'https://uptime.plugins.panel.example.com')
            ->assertJsonPath('plugin.hosting.serverName', 'panel')
            ->assertJsonPath('plugin.hosting.status', 'starting')
            // The container gets the credentials. Nobody needs to see them.
            ->assertJsonPath('token', null)->assertJsonPath('webhookSecret', null)
            ->json('plugin');

        $job = AgentJob::where('action', AgentAction::CreateWebsite)->sole();
        $this->assertSame($panel->id, $job->server_id);
        $w = $job->payload['website'];
        $this->assertSame('custom', $w['runtime']);
        $this->assertSame([$image['ref'], $image['digest'], 9000], [$w['image'], $w['imageDigest'], $w['containerPort']]);
        $this->assertMatchesRegularExpression('/^plugin_[a-z0-9]{4}$/', $w['unixUser']);
        $this->assertSame([['name' => 'uptime.plugins.panel.example.com', 'kind' => 'primary']], $w['domains']);
        // Its own database instance: it connects to no database server of a customer.
        $this->assertSame($out['id'], $w['databaseInstance']);
        $this->assertSame('https://panel.example.com', $w['environment']['CF_PANEL']);
        $this->assertStringStartsWith('whsec_', $w['environment']['CF_WEBHOOK_SECRET']);
        $this->assertSame(['audit:read', 'websites:read'], PersonalAccessToken::findToken($w['environment']['CF_TOKEN'])->abilities);

        // When the job is complete, the panel keeps no credentials in the job.
        app(JobOutcome::class)->finish($job, JobState::Succeeded);
        $this->assertSame('running', Plugin::find($out['id'])->container_status);
        $this->assertArrayNotHasKey('environment', $job->fresh()->payload['website']);

        // Off stops the container. New credentials go to a new container.
        $this->patchJson("/api/v1/plugins/{$out['id']}", ['enabled' => false])->assertOk();
        $stop = AgentJob::where('action', AgentAction::SetWebsitePower)->sole();
        $this->assertSame('stop', $stop->payload['power']);
        app(JobOutcome::class)->finish($stop, JobState::Succeeded);
        $this->assertSame('stopped', Plugin::find($out['id'])->container_status);
        $this->postJson("/api/v1/plugins/{$out['id']}/credentials", ['password' => 'password'])->assertOk()->assertJsonPath('token', null);
        $update = AgentJob::where('action', AgentAction::SetRuntime)->sole();
        $this->assertNotSame($w['environment']['CF_TOKEN'], $update->payload['website']['environment']['CF_TOKEN']);
        $this->assertNull(PersonalAccessToken::findToken($w['environment']['CF_TOKEN']));

        // A plugin that the panel runs keeps its host name, and its manifest keeps an image.
        $this->patchJson("/api/v1/plugins/{$out['id']}", ['baseUrl' => 'https://evil.example', 'password' => 'password'])->assertUnprocessable();
        $plain = $this->manifest(['version' => '1.5.0']);
        $this->putJson("/api/v1/plugins/{$out['id']}", ['manifest' => $plain, 'signature' => $this->sign($plain), 'password' => 'password'])->assertUnprocessable();

        // Remove: the agent removes the container and its Unix user.
        $this->deleteJson("/api/v1/plugins/{$out['id']}")->assertNoContent();
        $remove = AgentJob::where('action', AgentAction::DeleteWebsite)->sole();
        $this->assertSame($w['unixUser'], $remove->payload['unixUser']);
    }

    public function test_the_panel_runs_only_a_plugin_with_an_image_on_a_free_host_name(): void
    {
        $this->admin();
        $this->trustKey();
        $this->server(['name' => 'panel', 'local' => true]);
        $manifest = $this->manifest();
        $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'hosted' => true, 'password' => 'password'])
            ->assertUnprocessable()->assertJsonValidationErrors('hosting');

        $manifest = $this->manifest(['image' => ['ref' => 'ghcr.io/example/uptime:1.4.0', 'digest' => 'sha256:'.str_repeat('b', 64)]]);
        $this->website($this->customer(), $this->server(), $this->image(), 'taken.example.com');
        $this->postJson('/api/v1/plugins', ['manifest' => $manifest, 'signature' => $this->sign($manifest), 'hosted' => true, 'host' => 'taken.example.com', 'password' => 'password'])
            ->assertUnprocessable()->assertJsonValidationErrors('host');
        // An image with no digest: the signature would not cover the image.
        $this->postJson('/api/v1/plugins/preview', ['manifest' => $this->manifest(['image' => ['ref' => 'ghcr.io/example/uptime:latest', 'digest' => 'latest']])])
            ->assertUnprocessable()->assertJsonValidationErrors('image.digest');
    }

    public function test_only_an_administrator_manages_plugins(): void
    {
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->getJson('/api/v1/plugins')->assertForbidden();
        $this->getJson('/api/v1/plugin-keys')->assertForbidden();
        $this->actingWith($this->user(Role::Administrator), ['websites:read']);
        $this->getJson('/api/v1/plugins')->assertForbidden();
    }
}
