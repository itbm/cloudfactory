<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\SyncDnsZone;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsAccount;
use App\Models\DnsZone;
use App\Models\Plugin;
use App\Models\Setting;
use App\Services\Dns\Zones;
use App\Services\JobOutcome;
use App\Services\Plugins\Manifest;
use App\Services\Plugins\Outbound;
use Illuminate\Http\Client\Request as HttpRequest;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Queue;
use Illuminate\Validation\ValidationException;
use Tests\TestCase;

/**
 * External DNS providers through plugins (cf-dns-provider/1). The panel keeps the records, and
 * the plugin makes the zone at the provider the same after each change.
 */
class DnsProviderTest extends TestCase
{
    private const NS = ['ns1.hosting.example', 'ns2.hosting.example'];

    private const CLOUDFLARE_NS = ['ada.ns.cloudflare.com', 'bob.ns.cloudflare.com'];

    private Customer $customer;

    private Plugin $plugin;

    /** @var list<array<string, mixed>> The requests that the plugin got. */
    private array $calls = [];

    /** @var array<string, array{0: int, 1: array<string, mixed>}> The answer of the plugin for each operation. */
    private array $answers = [];

    protected function setUp(): void
    {
        parent::setUp();
        $this->app->instance(Outbound::class, new class extends Outbound
        {
            public function addresses(string $host): array
            {
                return ['203.0.113.60'];
            }
        });
        $this->answers = [
            'verify' => [200, ['account' => 'Example Ltd']],
            'records' => [200, ['records' => []]],
            'sync' => [200, ['nameServers' => [...self::CLOUDFLARE_NS, 'not a host'], 'active' => false, 'dnssec' => ['state' => 'off', 'ds' => []], 'changes' => ['added' => 2, 'changed' => 0, 'removed' => 1]]],
            'remove' => [200, []],
        ];
        Http::fake(function (HttpRequest $r) {
            $body = json_decode($r->body(), true);
            $this->calls[] = $body + ['url' => $r->url(), 'event' => $r->header('Cloudfactory-Event')[0] ?? null, 'signature' => $r->header('Cloudfactory-Signature')[0] ?? null];
            [$status, $answer] = $this->answers[$body['operation']] ?? [404, []];

            return Http::response($answer, $status);
        });
        $this->customer = $this->customer($this->plan());
        $this->plugin = $this->dnsPlugin();
    }

    /** @param  array<string, mixed>  $provider */
    private function dnsPlugin(array $provider = [], string $slug = 'dns-cloudflare'): Plugin
    {
        $spec = Manifest::parse(json_encode([
            'schema' => 1, 'slug' => $slug, 'name' => 'Cloudflare DNS', 'version' => '1.0.0', 'publisher' => 'Example Ltd', 'scopes' => [],
            'dnsProviders' => [array_merge([
                'id' => 'cloudflare', 'name' => 'Cloudflare', 'path' => '/dns/cloudflare', 'dnssec' => true,
                'credentials' => [
                    ['id' => 'apiToken', 'label' => 'API token', 'secret' => true],
                    ['id' => 'accountId', 'label' => 'Account ID', 'optional' => true],
                ],
            ], $provider)],
        ]));

        return Plugin::create([
            'slug' => $spec['slug'], 'name' => $spec['name'], 'version' => $spec['version'], 'publisher' => $spec['publisher'],
            'manifest' => json_encode($spec, JSON_UNESCAPED_SLASHES), 'enabled' => true, 'base_url' => 'https://dns.example.net', 'webhook_secret' => 'whsec_test',
        ]);
    }

    /** Adds an account for the customer through the API. */
    private function account(?string $customerId = null): DnsAccount
    {
        $id = $this->postJson('/api/v1/dns-accounts', [
            'name' => 'Our Cloudflare', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare',
            'credentials' => ['apiToken' => 'cf-secret-token', 'accountId' => 'acc123'], 'customerId' => $customerId ?? $this->customer->id,
        ])->assertCreated()->json('id');

        return DnsAccount::findOrFail($id);
    }

    /** Runs the jobs that send the zone to its provider. */
    private function runSync(DnsZone $zone): void
    {
        app()->call([new SyncDnsZone($zone->id), 'handle']);
    }

    /** @return list<array<string, mixed>> */
    private function callsFor(string $operation): array
    {
        return array_values(array_filter($this->calls, fn ($c) => $c['operation'] === $operation));
    }

    public function test_the_manifest_can_offer_dns_providers(): void
    {
        $spec = $this->plugin->dnsProviders()[0];
        $this->assertSame(['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV', 'CAA', 'NS'], $spec['types']);
        $this->assertSame(['id' => 'apiToken', 'label' => 'API token', 'secret' => true, 'optional' => false, 'hint' => ''], $spec['credentials'][0]);

        $limited = Manifest::parse(json_encode(['schema' => 1, 'slug' => 'x', 'name' => 'X', 'version' => '1.0.0', 'publisher' => 'X', 'scopes' => [],
            'dnsProviders' => [['id' => 'x', 'name' => 'X', 'path' => '/dns', 'types' => ['TXT', 'A']]]]));
        $this->assertSame(['A', 'TXT'], $limited['dnsProviders'][0]['types']);
        $this->assertFalse($limited['dnsProviders'][0]['dnssec']);

        foreach ([
            ['id' => 'x', 'name' => 'X', 'path' => '/../dns'],
            ['id' => 'x', 'name' => 'X', 'path' => '/dns', 'types' => ['PTR']],
            ['id' => 'x', 'name' => 'X', 'path' => '/dns', 'credentials' => [['id' => 'api-token', 'label' => 'Token']]],
            ['id' => 'x', 'name' => 'X', 'path' => '/dns', 'credentials' => [['id' => 'token', 'label' => 'A'], ['id' => 'token', 'label' => 'B']]],
        ] as $provider) {
            try {
                Manifest::parse(json_encode(['schema' => 1, 'slug' => 'x', 'name' => 'X', 'version' => '1.0.0', 'publisher' => 'X', 'scopes' => [], 'dnsProviders' => [$provider]]));
                $this->fail('The manifest is not valid: '.json_encode($provider));
            } catch (ValidationException) {
                $this->addToAssertionCount(1);
            }
        }
    }

    public function test_a_customer_adds_an_account_and_the_plugin_checks_the_credentials(): void
    {
        $owner = $this->actingWith($this->user(Role::Owner, $this->customer));

        $this->getJson('/api/v1/dns-accounts')->assertOk()
            ->assertJsonPath('providers.0.name', 'Cloudflare')
            ->assertJsonPath('providers.0.pluginId', $this->plugin->id)
            ->assertJsonPath('providers.0.credentials.0.id', 'apiToken');

        $this->postJson('/api/v1/dns-accounts', ['name' => 'Ours', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => []])
            ->assertUnprocessable()->assertJsonPath('errors', ['credentials.apiToken' => ['Enter the API token.']]);
        $this->postJson('/api/v1/dns-accounts', ['name' => 'Ours', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => ['apiToken' => 'x', 'password' => 'y']])
            ->assertUnprocessable()->assertJsonValidationErrors('credentials.password');
        // Only an administrator makes an account of the panel.
        $this->postJson('/api/v1/dns-accounts', ['name' => 'Ours', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => ['apiToken' => 'x'], 'customerId' => $this->customer($this->plan())->id])
            ->assertUnprocessable()->assertJsonValidationErrors('customerId');
        $this->assertSame([], $this->calls);

        // The plugin refuses the token, and the panel shows its error.
        $this->answers['verify'] = [403, ['error' => 'Cloudflare refused the API token: Invalid API Token.']];
        $this->postJson('/api/v1/dns-accounts', ['name' => 'Ours', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => ['apiToken' => 'bad']])
            ->assertUnprocessable()->assertJsonPath('errors.credentials.0', 'Cloudflare refused the API token: Invalid API Token.');
        // No answer: the error does not show the address of the plugin.
        $this->answers['verify'] = [502, []];
        $error = $this->postJson('/api/v1/dns-accounts', ['name' => 'Ours', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => ['apiToken' => 'bad']])
            ->assertUnprocessable()->json('errors.credentials.0');
        $this->assertStringNotContainsString('dns.example.net', $error);
        $this->assertDatabaseCount('dns_accounts', 0);

        $this->answers['verify'] = [200, ['account' => 'Example Ltd']];
        $response = $this->postJson('/api/v1/dns-accounts', ['name' => 'Ours', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => ['apiToken' => ' cf-token ', 'accountId' => 'acc123']])
            ->assertCreated()
            ->assertJsonPath('customerId', $this->customer->id)
            ->assertJsonPath('providerName', 'Cloudflare')
            ->assertJsonPath('account', 'Example Ltd')
            ->assertJsonPath('available', true)
            ->assertJsonPath('credentials', ['apiToken' => null, 'accountId' => 'acc123']);
        $this->assertStringNotContainsString('cf-token', $response->getContent());

        $verify = $this->callsFor('verify');
        $last = end($verify);
        $this->assertSame('https://dns.example.net/dns/cloudflare', $last['url']);
        $this->assertSame('dns.provider', $last['event']);
        $this->assertStringStartsWith('t=', $last['signature']);
        $this->assertSame(['contract' => 'cf-dns-provider/1', 'provider' => 'cloudflare', 'credentials' => ['apiToken' => 'cf-token', 'accountId' => 'acc123'], 'zone' => null],
            array_intersect_key($last, array_flip(['contract', 'provider', 'credentials', 'zone'])));
        // The database has the credentials encrypted.
        $account = DnsAccount::firstOrFail();
        $this->assertStringNotContainsString('cf-token', (string) $account->getRawOriginal('credentials'));
        $this->assertSame('cf-token', $account->credentials['apiToken']);

        // An empty secret keeps its value. A new token goes to the plugin first.
        $this->patchJson("/api/v1/dns-accounts/{$account->id}", ['name' => 'Main', 'credentials' => ['apiToken' => '', 'accountId' => 'acc456']])->assertOk()
            ->assertJsonPath('name', 'Main')->assertJsonPath('credentials.accountId', 'acc456');
        $this->assertSame('cf-token', $account->fresh()->credentials['apiToken']);

        // A different customer does not see the account.
        $this->actingWith($this->user(Role::Owner, $this->customer($this->plan())));
        $this->getJson('/api/v1/dns-accounts')->assertOk()->assertJsonCount(0, 'data');
        $this->patchJson("/api/v1/dns-accounts/{$account->id}", ['name' => 'Mine'])->assertNotFound();

        $this->actingWith($owner);
        $this->deleteJson("/api/v1/dns-accounts/{$account->id}")->assertNoContent();
        $this->assertDatabaseCount('dns_accounts', 0);
    }

    public function test_an_account_of_the_panel_is_for_each_customer_but_only_an_administrator_changes_it(): void
    {
        $this->actingWith($this->user());
        $account = $this->postJson('/api/v1/dns-accounts', [
            'name' => 'Panel Cloudflare', 'pluginId' => $this->plugin->id, 'provider' => 'cloudflare', 'credentials' => ['apiToken' => 'cf-panel', 'accountId' => 'panel-acc'], 'customerId' => null,
        ])->assertCreated()->assertJsonPath('customerId', null)->json('id');

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->getJson('/api/v1/dns-accounts')->assertOk()
            ->assertJsonPath('data.0.id', $account)
            // A customer does not see the values of the panel.
            ->assertJsonPath('data.0.credentials', []);
        $this->patchJson("/api/v1/dns-accounts/{$account}", ['name' => 'Mine'])->assertForbidden();
        $this->deleteJson("/api/v1/dns-accounts/{$account}")->assertForbidden();
    }

    public function test_a_zone_at_a_provider_gets_the_records_of_the_provider_and_each_change(): void
    {
        $this->dnsServer(self::NS[0]);
        $this->actingWith($this->user());
        $account = $this->account();
        $this->answers['records'] = [200, ['records' => [
            ['name' => '@', 'type' => 'NS', 'ttl' => 86400, 'value' => 'ada.ns.cloudflare.com'],
            ['name' => 'www', 'type' => 'CNAME', 'ttl' => 1, 'value' => 'example.com'],
            ['name' => '@', 'type' => 'MX', 'ttl' => 300, 'value' => 'mail.example.net', 'priority' => 10],
            ['name' => '@', 'type' => 'TXT', 'ttl' => 300, 'value' => 'v=spf1 -all'],
            ['name' => 'bad', 'type' => 'PTR', 'ttl' => 300, 'value' => 'x'],
        ]]];
        Queue::fake();

        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $account->id])
            ->assertAccepted()
            // The DNS servers of the panel do not get the zone.
            ->assertJsonCount(0, 'jobs')
            ->assertJsonPath('zone.provider.name', 'Cloudflare')
            ->assertJsonPath('zone.provider.accountName', 'Our Cloudflare')
            ->assertJsonPath('zone.provider.state', 'pending')
            ->assertJsonPath('zone.provider.handover', null)
            ->assertJsonPath('zone.provider.imported.added', 3)
            ->assertJsonPath('zone.provider.imported.skipped.0.code', 'invalid')
            ->assertJsonPath('zone.status', 'updating')
            ->assertJsonPath('zone.servers', [])
            ->assertJsonPath('zone.nameServers', []);
        $zone = DnsZone::findOrFail($response->json('zone.id'));
        $this->assertSame(0, AgentJob::count());
        $this->assertSame([['example.com']], array_map(fn ($c) => [$c['zone']['name']], $this->callsFor('records')));
        // The TTL 1 of Cloudflare (automatic) becomes the shortest TTL of the panel.
        $this->assertSame(60, $zone->records()->where('type', 'CNAME')->value('ttl'));
        Queue::assertPushed(SyncDnsZone::class, fn (SyncDnsZone $job) => $job->zoneId === $zone->id);

        $this->runSync($zone);
        $sync = $this->callsFor('sync')[0];
        $this->assertSame(['name' => 'example.com', 'serial' => $zone->serial, 'dnssec' => false], array_diff_key($sync['zone'], ['records' => true]));
        $this->assertEqualsCanonicalizing(['@ MX mail.example.net', '@ TXT v=spf1 -all', 'www CNAME example.com'],
            array_map(fn ($r) => "{$r['name']} {$r['type']} {$r['value']}", $sync['zone']['records']));
        $zone->refresh();
        $this->assertSame('ok', $zone->provider_state['state']);
        $this->assertSame(self::CLOUDFLARE_NS, $zone->provider_state['nameServers']);
        // The NS records of the zone are the name servers of the provider. They do not go to the provider.
        $this->assertEqualsCanonicalizing(self::CLOUDFLARE_NS, $zone->records()->where('managed', 'ns')->pluck('value')->all());
        // A second job for the same serial does nothing.
        $this->runSync($zone->fresh());
        $this->assertCount(1, $this->callsFor('sync'));

        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()
            ->assertJsonPath('status', 'active')
            ->assertJsonPath('provider.state', 'ok')
            ->assertJsonPath('provider.changes', ['added' => 2, 'changed' => 0, 'removed' => 1])
            ->assertJsonPath('nameServers', self::CLOUDFLARE_NS)
            ->assertJsonPath('delegation.state', 'none');

        // The registrar has the name servers of Cloudflare: the customer controls the domain.
        $this->nameServersSeen['example.com'] = array_reverse(self::CLOUDFLARE_NS);
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk()->assertJsonPath('delegation.state', 'here');
        $this->assertTrue(Zones::controls($this->customer->id, 'shop.example.com'));

        // A change goes to the provider. A failure shows the error, and the job tries again later.
        $this->answers['sync'] = [502, ['error' => 'Cloudflare did not answer.']];
        Queue::fake();
        $this->postJson("/api/v1/dns-zones/{$zone->id}/records", ['type' => 'A', 'name' => 'shop', 'value' => '192.0.2.10'])->assertAccepted()->assertJsonCount(0, 'jobs');
        Queue::assertPushed(SyncDnsZone::class, 1);
        $this->runSync($zone->fresh());
        Queue::assertPushed(SyncDnsZone::class, 2);
        $records = $this->callsFor('sync')[1]['zone']['records'];
        $this->assertContains(['name' => 'shop', 'type' => 'A', 'ttl' => 3600, 'value' => '192.0.2.10', 'priority' => null], $records);
        $this->assertNotContains('NS', array_column($records, 'type'));
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()
            ->assertJsonPath('status', 'failed')
            ->assertJsonPath('provider.state', 'failed')
            ->assertJsonPath('provider.error', 'Cloudflare did not answer.');

        // The hourly check sends the zone again.
        Queue::fake();
        $this->travel(2)->hours();
        $this->artisan('cloudfactory:check-dns')->assertSuccessful();
        Queue::assertPushed(SyncDnsZone::class, 1);
    }

    public function test_a_zone_takes_only_the_record_types_of_its_provider(): void
    {
        $this->actingWith($this->user());
        $this->plugin = $this->dnsPlugin(['types' => ['A', 'TXT']], 'dns-small');
        $account = $this->account();
        $zone = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $account->id])->assertAccepted()->json('zone.id');

        $this->postJson("/api/v1/dns-zones/{$zone}/records", ['type' => 'TXT', 'name' => '@', 'value' => 'hello'])->assertAccepted();
        $this->postJson("/api/v1/dns-zones/{$zone}/records", ['type' => 'MX', 'name' => '@', 'value' => 'mail.example.net', 'priority' => 10])
            ->assertUnprocessable()->assertJsonPath('errors.type.0', 'Cloudflare does not take MX records. Select one of these types: A, TXT.');
    }

    public function test_a_wrong_account_or_a_plugin_that_is_off_makes_no_zone(): void
    {
        $this->actingWith($this->user());
        $other = $this->account($this->customer($this->plan())->id);
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $other->id])
            ->assertUnprocessable()->assertJsonPath('errors.accountId.0', 'Select a DNS account of this customer, or of the panel.');

        $account = $this->account();
        // The provider must answer before the panel makes the zone. The panel has no DNS server,
        // but a zone at a provider does not need one.
        $this->answers['records'] = [401, ['error' => 'Cloudflare refused the API token.']];
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $account->id])
            ->assertUnprocessable()->assertJsonPath('errors.accountId.0', 'Cloudflare refused the API token.');
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $account->id, 'importRecords' => false])
            ->assertAccepted();
        $this->assertSame(1, DnsZone::count());

        $this->plugin->update(['enabled' => false]);
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.org', 'customerId' => $this->customer->id, 'accountId' => $account->id])
            ->assertUnprocessable()->assertJsonValidationErrors('accountId');
        $this->getJson('/api/v1/dns-accounts')->assertJsonPath('data.0.available', false)->assertJsonCount(0, 'providers');
    }

    public function test_a_zone_moves_from_the_panel_to_a_provider_with_a_handover_time(): void
    {
        $servers = [$this->dnsServer(self::NS[0]), $this->dnsServer(self::NS[1])];
        $this->actingWith($this->user());
        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])->assertAccepted();
        foreach ($response->json('jobs.*.id') as $id) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($id), JobState::Succeeded, null);
        }
        $zone = DnsZone::findOrFail($response->json('zone.id'));
        $this->postJson("/api/v1/dns-zones/{$zone->id}/records", ['type' => 'A', 'name' => 'www', 'value' => '192.0.2.1'])->assertAccepted();
        $this->nameServersSeen['example.com'] = self::NS;
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertJsonPath('delegation.state', 'here');
        $account = $this->account();

        $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => true])->assertAccepted();
        $this->postJson("/api/v1/dns-zones/{$zone->id}/move", ['accountId' => $account->id])
            ->assertUnprocessable()->assertJsonPath('errors.accountId.0', 'Turn off DNSSEC before you move the zone. First remove the DS record at the registrar, and wait one day.');
        $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => false])->assertAccepted();
        AgentJob::query()->delete();
        Queue::fake();

        $this->postJson("/api/v1/dns-zones/{$zone->id}/move", ['accountId' => $account->id])->assertAccepted()
            // The DNS servers of the panel keep the zone in the handover time.
            ->assertJsonCount(2, 'jobs')
            ->assertJsonPath('zone.provider.name', 'Cloudflare')
            ->assertJsonPath('zone.provider.handover', ['endsAt' => null])
            ->assertJsonCount(2, 'zone.servers')
            // The public DNS still gives the name servers of the panel.
            ->assertJsonPath('zone.delegation.state', 'elsewhere');
        Queue::assertPushed(SyncDnsZone::class);
        $this->postJson("/api/v1/dns-zones/{$zone->id}/move", ['accountId' => $account->id])->assertUnprocessable();

        // After the first change at the provider, the DNS servers of the panel answer with the
        // NS records of the provider, so that resolvers go to the provider.
        AgentJob::query()->delete();
        $this->runSync($zone->fresh());
        $sent = AgentJob::where('action', AgentAction::SetDnsZones->value)->firstOrFail()->payload['zones'][0];
        $this->assertEqualsCanonicalizing(self::CLOUDFLARE_NS, array_column(array_filter($sent['records'], fn ($r) => $r['type'] === 'NS'), 'value'));
        $this->assertContains(['name' => 'www', 'type' => 'A', 'value' => '192.0.2.1'], array_map(fn ($r) => array_intersect_key($r, array_flip(['name', 'type', 'value'])), $sent['records']));
        $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => true])->assertUnprocessable();

        // The registrar has the name servers of Cloudflare. The handover time starts.
        $this->nameServersSeen['example.com'] = self::CLOUDFLARE_NS;
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk()->assertJsonPath('delegation.state', 'here');
        $zone->refresh();
        $this->assertTrue($zone->onPanelServers());
        $this->assertEqualsWithDelta(now()->addHours(Zones::HANDOVER_HOURS)->timestamp, $zone->handover_ends_at->timestamp, 5);

        // After the handover time, the DNS servers of the panel remove the zone.
        $this->travel(Zones::HANDOVER_HOURS + 1)->hours();
        $this->assertFalse($zone->fresh()->onPanelServers());
        AgentJob::query()->delete();
        app(Zones::class)->reconcile($servers[0], [['name' => 'example.com', 'serial' => $zone->serial]]);
        $job = AgentJob::where('server_id', $servers[0]->id)->firstOrFail();
        $this->assertSame(AgentAction::DeleteDnsZones, $job->action);
        $this->assertSame(['example.com'], $job->payload['names']);
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertJsonPath('servers', [])->assertJsonPath('provider.handover', null);

        // DNSSEC: the provider signs the zone and gives the DS record.
        $this->answers['sync'][1]['dnssec'] = ['state' => 'active', 'ds' => [['keyTag' => 2371, 'algorithm' => 13, 'digestType' => 2, 'digest' => 'abc123']]];
        $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => true])->assertAccepted();
        $this->runSync($zone->fresh());
        $this->assertTrue(end($this->calls)['zone']['dnssec']);
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()
            ->assertJsonPath('dnssec.state', 'active')
            ->assertJsonPath('dnssec.ds.record', 'example.com. IN DS 2371 13 2 ABC123')
            ->assertJsonPath('dnssec.dnskey', null);
    }

    public function test_a_zone_moves_back_to_the_panel(): void
    {
        $this->dnsServer(self::NS[0]);
        $this->actingWith($this->user());
        $account = $this->account();
        $zone = DnsZone::findOrFail($this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $account->id])->json('zone.id'));
        $this->runSync($zone);

        $this->postJson("/api/v1/dns-zones/{$zone->id}/move", ['accountId' => null])->assertAccepted()
            ->assertJsonCount(1, 'jobs')
            ->assertJsonPath('zone.provider', null)
            ->assertJsonPath('zone.nameServers', [self::NS[0]]);
        $this->assertSame([self::NS[0]], $zone->records()->where('managed', 'ns')->pluck('value')->all());
        // The zone stays at the old provider until the customer removes it there.
        $this->assertCount(0, $this->callsFor('remove'));
        $this->deleteJson("/api/v1/dns-accounts/{$account->id}")->assertNoContent();
    }

    public function test_removing_a_zone_removes_it_at_the_provider_unless_the_user_keeps_it(): void
    {
        $this->actingWith($this->user());
        $account = $this->account();
        $first = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id, 'accountId' => $account->id])->json('zone.id');
        $second = $this->postJson('/api/v1/dns-zones', ['name' => 'example.org', 'customerId' => $this->customer->id, 'accountId' => $account->id])->json('zone.id');

        $this->deleteJson("/api/v1/dns-accounts/{$account->id}")->assertUnprocessable()->assertJsonPath('errors.account.0', '2 DNS zones use this account. Move the zones to a different DNS provider, or remove them, first.');
        $this->deleteJson("/api/v1/plugins/{$this->plugin->id}")->assertUnprocessable()->assertJsonValidationErrors('plugin');

        $this->answers['remove'] = [403, ['error' => 'The API token cannot remove zones.']];
        $this->deleteJson("/api/v1/dns-zones/{$first}")->assertUnprocessable()
            ->assertJsonPath('errors.zone.0', 'The panel could not remove the zone at Cloudflare. The API token cannot remove zones. Try again, or keep the zone at Cloudflare.');
        $this->assertNotNull(DnsZone::find($first));

        $this->answers['remove'] = [200, []];
        $this->deleteJson("/api/v1/dns-zones/{$first}")->assertAccepted()->assertJsonCount(0, 'jobs');
        $this->assertNull(DnsZone::find($first));
        $this->assertSame('example.com', $this->callsFor('remove')[1]['zone']['name']);

        $this->deleteJson("/api/v1/dns-zones/{$second}?keepAtProvider=1")->assertAccepted();
        $this->assertNull(DnsZone::find($second));
        $this->assertCount(2, $this->callsFor('remove'));
        $this->deleteJson("/api/v1/dns-accounts/{$account->id}")->assertNoContent();
    }

    public function test_a_new_version_of_the_plugin_must_keep_the_providers_that_accounts_use(): void
    {
        $this->actingWith($this->user());
        $this->account();
        $this->assertSame(['cloudflare'], array_column($this->getJson("/api/v1/plugins/{$this->plugin->id}")->assertOk()->json('dnsProviders'), 'id'));

        Setting::put('plugins.developer_mode', true);
        $manifest = json_encode(['schema' => 1, 'slug' => 'dns-cloudflare', 'name' => 'Cloudflare DNS', 'version' => '2.0.0', 'publisher' => 'Example Ltd', 'scopes' => []]);
        $this->putJson("/api/v1/plugins/{$this->plugin->id}", ['manifest' => $manifest, 'password' => 'password'])
            ->assertUnprocessable()->assertJsonPath('errors.manifest.0', 'The new version does not offer the DNS provider "cloudflare". DNS accounts use it. Remove the accounts first.');
    }
}
