<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\DeliverPluginEvent;
use App\Models\AgentJob;
use App\Models\Plugin;
use App\Models\PluginDelivery;
use App\Models\User;
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
        $this->postJson('/api/v1/plugins', ['manifest' => $this->manifest(['scopes' => ['websites:write']]), 'signature' => $this->sign($manifest), 'baseUrl' => 'https://uptime.example.net', 'password' => 'password'])
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
        $next = $this->manifest(['version' => '1.1.0', 'scopes' => ['websites:read']]);
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
            $this->assertStringContainsString("does not send events to {$ip}", PluginDelivery::find($delivery)->error, $ip);
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

    public function test_only_an_administrator_manages_plugins(): void
    {
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->getJson('/api/v1/plugins')->assertForbidden();
        $this->getJson('/api/v1/plugin-keys')->assertForbidden();
        $this->actingWith($this->user(Role::Administrator), ['websites:read']);
        $this->getJson('/api/v1/plugins')->assertForbidden();
    }
}
