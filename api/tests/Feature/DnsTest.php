<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\Domain;
use App\Models\Server;
use App\Services\Dns\Dnssec;
use App\Services\Dns\Zones;
use App\Services\DomainPolicy;
use App\Services\JobOutcome;
use App\Validation\Input;
use Illuminate\Testing\TestResponse;
use Tests\TestCase;

/**
 * DNS hosting. A customer has DNS zones. The panel keeps the records, and sends each full zone
 * to each server with the role "dns" after each change.
 */
class DnsTest extends TestCase
{
    private const NS = ['ns1.hosting.example', 'ns2.hosting.example'];

    private Customer $customer;

    /** @var list<Server> */
    private array $dns;

    /** A customer, and two servers that answer DNS queries. An administrator is signed in. */
    private function hosting(array $plan = []): void
    {
        $this->customer = $this->customer($this->plan($plan));
        $this->dns = [$this->dnsServer(self::NS[0]), $this->dnsServer(self::NS[1])];
        $this->actingWith($this->user());
    }

    private function finish(array|string $jobs, JobState $state = JobState::Succeeded): void
    {
        foreach ((array) $jobs as $id) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($id), $state, $state === JobState::Failed ? 'knotc: the zone did not load' : null);
        }
    }

    /** Makes a zone through the API, and completes its jobs. */
    private function zone(string $name = 'example.com', ?Customer $customer = null): DnsZone
    {
        $response = $this->postJson('/api/v1/dns-zones', ['name' => $name, 'customerId' => ($customer ?? $this->customer)->id])->assertAccepted();
        $this->finish($response->json('jobs.*.id'));

        return DnsZone::findOrFail($response->json('zone.id'));
    }

    private function record(DnsZone $zone, array $record): TestResponse
    {
        return $this->postJson("/api/v1/dns-zones/{$zone->id}/records", $record);
    }

    public function test_a_new_zone_goes_to_each_dns_server_with_its_name_servers(): void
    {
        $this->hosting();

        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])
            ->assertAccepted()
            ->assertJsonPath('zone.name', 'example.com')
            ->assertJsonPath('zone.status', 'updating')
            ->assertJsonPath('zone.nameServers', self::NS)
            ->assertJsonPath('zone.dnssec.enabled', false)
            ->assertJsonPath('zone.delegation.state', 'none')
            ->assertJsonCount(2, 'jobs');
        $this->assertSame([['@', 'NS', self::NS[0], 'ns'], ['@', 'NS', self::NS[1], 'ns']],
            array_map(fn ($r) => [$r['name'], $r['type'], $r['value'], $r['managed']], $response->json('zone.records')));

        $jobs = AgentJob::where('action', AgentAction::SetDnsZones->value)->orderBy('server_id')->get();
        $this->assertEqualsCanonicalizing(array_map(fn ($s) => $s->id, $this->dns), $jobs->pluck('server_id')->all());
        $zone = DnsZone::sole();
        foreach ($jobs as $job) {
            $this->assertSame($zone->id, $job->dns_zone_id);
            $this->assertSame([[
                'name' => 'example.com', 'serial' => $zone->serial,
                'records' => [
                    ['name' => '@', 'type' => 'NS', 'ttl' => 3600, 'value' => self::NS[0], 'priority' => 0],
                    ['name' => '@', 'type' => 'NS', 'ttl' => 3600, 'value' => self::NS[1], 'priority' => 0],
                ],
                'dnssecKey' => '', 'dnssecKeyTag' => 0, 'transfers' => null,
            ]], $job->payload['zones']);
        }

        // One DNS server has the zone: the zone waits for the other one.
        $this->finish($jobs[0]->id);
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()->assertJsonPath('status', 'updating');
        $this->finish($jobs[1]->id);
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()->assertJsonPath('status', 'active')
            ->assertJsonPath('servers', [['nameServer' => self::NS[0], 'state' => 'ok'], ['nameServer' => self::NS[1], 'state' => 'ok']]);
        $this->getJson('/api/v1/dns-zones')->assertOk()->assertJsonPath('nameServers', self::NS)->assertJsonPath('data.0.recordCount', 2);
    }

    public function test_a_zone_needs_a_dns_server(): void
    {
        $this->customer = $this->customer();
        // The role alone is not sufficient: the server needs a host name, and an agent that can operate DNS.
        $this->server(['roles' => ['dns'], 'dns' => true]);
        $this->server(['roles' => ['dns'], 'dns_name' => 'ns9.hosting.example']);
        $this->server(['roles' => ['web'], 'dns' => true, 'dns_name' => 'ns8.hosting.example']);

        $this->actingWith($this->user());
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])
            ->assertUnprocessable()->assertJsonPath('errors.name.0', 'The panel has no DNS server. Give a server the role "DNS" and a name server host name on the page of the server.');
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com'])
            ->assertUnprocessable()->assertJsonPath('errors.name.0', 'DNS hosting is not available. Ask your provider.');
        $this->assertSame(0, DnsZone::count());
    }

    public function test_a_failed_job_shows_on_the_zone_and_the_zone_can_go_again(): void
    {
        $this->hosting();
        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])->assertAccepted();
        [$first, $second] = $response->json('jobs.*.id');
        $this->finish($first);
        $this->finish($second, JobState::Failed);
        $zone = DnsZone::sole();

        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertJsonPath('status', 'failed');
        $again = $this->postJson("/api/v1/dns-zones/{$zone->id}/sync")->assertAccepted()->assertJsonPath('zone.status', 'updating')->assertJsonCount(2, 'jobs');
        $this->assertGreaterThan($zone->serial, $again->json('zone.serial'));
        $this->finish($again->json('jobs.*.id'));
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertJsonPath('status', 'active');

        // The late result of an old job does not change the state of a newer serial.
        AgentJob::whereKey($second)->update(['state' => JobState::Running->value]);
        $this->finish($second, JobState::Failed);
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertJsonPath('status', 'active');
    }

    public function test_each_record_type_has_its_own_form(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $valid = [
            ['type' => 'A', 'name' => '@', 'value' => '203.0.113.10'],
            ['type' => 'AAAA', 'name' => 'www', 'value' => '2001:DB8::10'],
            ['type' => 'CNAME', 'name' => 'shop', 'value' => 'Shops.Example.NET.'],
            ['type' => 'MX', 'name' => '', 'value' => 'mail.example.com', 'priority' => 10],
            ['type' => 'TXT', 'name' => '_dmarc', 'value' => 'v=DMARC1; p=none; rua=mailto:dmarc@example.com'],
            ['type' => 'SRV', 'name' => '_sip._tcp', 'value' => '5 5060 sip.example.com', 'priority' => 10],
            ['type' => 'CAA', 'name' => '@', 'value' => '0 issue "letsencrypt.org"'],
            ['type' => 'NS', 'name' => 'lab', 'value' => 'ns1.other.example'],
            ['type' => 'A', 'name' => '*.dev', 'value' => '203.0.113.11', 'ttl' => 60],
            // A user can type the full name.
            ['type' => 'A', 'name' => 'api.example.com.', 'value' => '203.0.113.12'],
        ];
        foreach ($valid as $record) {
            $this->finish($this->record($zone, $record)->assertAccepted()->json('jobs.*.id'));
        }
        $saved = $zone->records()->whereNull('managed')->get()->keyBy(fn ($r) => "{$r->type} {$r->name}");
        $this->assertSame('2001:db8::10', $saved['AAAA www']->value);
        $this->assertSame('shops.example.net', $saved['CNAME shop']->value);
        $this->assertSame(['@', 10, 3600], [$saved['MX @']->name, $saved['MX @']->priority, $saved['MX @']->ttl]);
        $this->assertSame(60, $saved['A *.dev']->ttl);
        $this->assertTrue($saved->has('A api'));
        $this->assertNull($saved['A @']->priority);

        $invalid = [
            [['type' => 'A', 'name' => 'x', 'value' => '2001:db8::1'], 'value'],
            [['type' => 'A', 'name' => 'x', 'value' => '203.0.113.300'], 'value'],
            [['type' => 'AAAA', 'name' => 'x', 'value' => '203.0.113.1'], 'value'],
            [['type' => 'CNAME', 'name' => 'x', 'value' => 'not a host'], 'value'],
            [['type' => 'MX', 'name' => '@', 'value' => 'mail.example.com'], 'priority'],
            [['type' => 'MX', 'name' => '@', 'value' => 'mail.example.com', 'priority' => 70000], 'priority'],
            [['type' => 'MX', 'name' => '@', 'value' => '203.0.113.1', 'priority' => 10], 'value'],
            [['type' => 'SRV', 'name' => '_x._tcp', 'value' => 'sip.example.com', 'priority' => 1], 'value'],
            [['type' => 'CAA', 'name' => '@', 'value' => '0 issue letsencrypt.org'], 'value'],
            [['type' => 'TXT', 'name' => 'x', 'value' => "line\nbreak"], 'value'],
            [['type' => 'TXT', 'name' => 'x', 'value' => ''], 'value'],
            [['type' => 'PTR', 'name' => 'x', 'value' => 'a.example.com'], 'type'],
            [['type' => 'A', 'name' => 'bad name', 'value' => '203.0.113.1'], 'name'],
            [['type' => 'A', 'name' => '$ORIGIN', 'value' => '203.0.113.1'], 'name'],
            [['type' => 'A', 'name' => 'www.other.example', 'value' => '203.0.113.1', 'ttl' => 30], 'ttl'],
            [['type' => 'A', 'name' => 'x', 'value' => '203.0.113.1', 'ttl' => 700000], 'ttl'],
            // The rules of the DNS for records with the same name.
            [['type' => 'CNAME', 'name' => '@', 'value' => 'a.example.net'], 'name'],
            [['type' => 'NS', 'name' => '@', 'value' => 'ns1.other.example'], 'name'],
            [['type' => 'CNAME', 'name' => 'www', 'value' => 'a.example.net'], 'name'],
            [['type' => 'A', 'name' => 'shop', 'value' => '203.0.113.1'], 'name'],
            [['type' => 'CNAME', 'name' => 'shop', 'value' => 'b.example.net'], 'name'],
            [['type' => 'A', 'name' => '@', 'value' => '203.0.113.10'], 'value'],
        ];
        $jobs = AgentJob::count();
        foreach ($invalid as [$record, $field]) {
            $this->record($zone, $record)->assertUnprocessable()->assertJsonValidationErrors($field);
        }
        $this->assertSame($jobs, AgentJob::count());
        $this->assertSame(12, $zone->records()->count());

        // The agent gets each record in the form of its type.
        $sent = collect(AgentJob::latest('id')->first()->payload['zones'][0]['records'])->keyBy(fn ($r) => "{$r['type']} {$r['name']}");
        $this->assertSame(['name' => '@', 'type' => 'MX', 'ttl' => 3600, 'value' => 'mail.example.com', 'priority' => 10], $sent['MX @']);
        $this->assertSame(0, $sent['A @']['priority']);
    }

    public function test_a_user_changes_and_removes_records_but_not_the_name_servers(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $id = $this->record($zone, ['type' => 'A', 'name' => 'www', 'value' => '203.0.113.10'])->assertAccepted()->json('record.id');
        $serial = $zone->fresh()->serial;

        $this->patchJson("/api/v1/dns-zones/{$zone->id}/records/{$id}", ['value' => '203.0.113.20', 'ttl' => 300, 'type' => 'TXT'])
            ->assertAccepted()->assertJsonPath('record.value', '203.0.113.20')->assertJsonPath('record.ttl', 300)
            // The type of a record stays.
            ->assertJsonPath('record.type', 'A')->assertJsonCount(2, 'jobs');
        $this->assertGreaterThan($serial, $zone->fresh()->serial);
        $this->patchJson("/api/v1/dns-zones/{$zone->id}/records/{$id}", ['value' => 'no'])->assertUnprocessable()->assertJsonValidationErrors('value');
        // The same values again are not a second record.
        $this->patchJson("/api/v1/dns-zones/{$zone->id}/records/{$id}", ['value' => '203.0.113.20'])->assertAccepted();

        $ns = $zone->records()->where('managed', 'ns')->first();
        $this->patchJson("/api/v1/dns-zones/{$zone->id}/records/{$ns->id}", ['value' => 'ns1.evil.example'])
            ->assertUnprocessable()->assertJsonPath('errors.record.0', 'The panel keeps the name servers of the zone. You cannot change this record.');
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/records/{$ns->id}")->assertUnprocessable();

        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/records/{$id}")->assertAccepted()->assertJsonCount(2, 'jobs');
        $this->assertSame(2, $zone->records()->count());
        // A record of a different zone.
        $other = $this->zone('other.example');
        $this->deleteJson("/api/v1/dns-zones/{$other->id}/records/{$ns->id}")->assertNotFound();
    }

    public function test_a_customer_sees_and_changes_only_its_own_zones(): void
    {
        $this->hosting();
        $txt = [];
        $policy = $this->fakeTxt($txt);
        $plan = $this->customer->plan;
        $reseller = $this->customer($plan, ['name' => 'Reseller', 'role' => 'reseller']);
        $resold = $this->customer($plan, ['name' => 'Resold', 'reseller_id' => $reseller->id]);
        $mine = $this->zone('example.com');
        $theirs = $this->zone('resold.example', $resold);

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->getJson('/api/v1/dns-zones')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.name', 'example.com');
        $this->getJson("/api/v1/dns-zones/{$theirs->id}")->assertNotFound();
        $this->record($theirs, ['type' => 'A', 'name' => 'x', 'value' => '203.0.113.1'])->assertNotFound();
        $this->deleteJson("/api/v1/dns-zones/{$theirs->id}")->assertNotFound();
        $this->putJson("/api/v1/dns-zones/{$theirs->id}/dnssec", ['enabled' => true])->assertNotFound();
        // An owner makes a zone only for its own customer.
        $this->postJson('/api/v1/dns-zones', ['name' => 'a.example', 'customerId' => $resold->id])->assertUnprocessable()->assertJsonValidationErrors('customerId');
        // The owner sees the jobs of its zone, and not the jobs of the other zone.
        $this->getJson('/api/v1/jobs')->assertOk()->assertJsonCount(2, 'data')->assertJsonPath('data.0.dnsZoneId', $mine->id);

        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->getJson('/api/v1/dns-zones')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.name', 'resold.example');
        // A reseller is not an administrator: its customer proves control of the new name.
        $this->postJson('/api/v1/dns-zones', ['name' => 'b.example', 'customerId' => $resold->id])->assertUnprocessable();
        $txt['_cloudfactory-challenge.b.example'] = [$policy->claimToken($resold->id, 'b.example')];
        $this->postJson('/api/v1/dns-zones', ['name' => 'b.example', 'customerId' => $resold->id])->assertAccepted();
        $this->getJson("/api/v1/dns-zones/{$mine->id}")->assertNotFound();
    }

    public function test_a_token_needs_the_dns_scope(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $owner = $this->user(Role::Owner, $this->customer);

        $this->actingWith($owner, ['websites:write']);
        $this->getJson('/api/v1/dns-zones')->assertForbidden();
        $this->actingWith($owner, ['dns:read']);
        $this->getJson('/api/v1/dns-zones')->assertOk();
        $this->record($zone, ['type' => 'A', 'name' => 'x', 'value' => '203.0.113.1'])->assertForbidden();
        $this->actingWith($owner, ['dns:write']);
        $this->record($zone, ['type' => 'A', 'name' => 'x', 'value' => '203.0.113.1'])->assertAccepted();
    }

    public function test_a_zone_name_of_a_different_customer_needs_a_proof_of_control(): void
    {
        $this->hosting();
        $txt = [];
        $policy = $this->fakeTxt($txt);
        $other = $this->customer($this->customer->plan, ['name' => 'Other Ltd']);
        $this->zone('example.com', $other);
        $this->zone('deep.sub.example.net', $other);

        $this->actingWith($this->user(Role::Owner, $this->customer));
        foreach (['example.com', 'shop.example.com', 'com'] as $name) {
            $response = $this->postJson('/api/v1/dns-zones', ['name' => $name]);
            if ($name === 'com') {
                // The rule "domain" needs two labels.
                $response->assertUnprocessable();

                continue;
            }
            $response->assertUnprocessable();
            $this->assertStringContainsString("_cloudfactory-challenge.{$name} with the value {$policy->claimToken($this->customer->id, $name)}", $response->json('errors.name.0'));
        }
        // A zone above the zone of the other customer.
        $this->postJson('/api/v1/dns-zones', ['name' => 'sub.example.net'])->assertUnprocessable()
            ->assertJsonPath('errors.name.0', fn (string $m) => str_starts_with($m, 'Prove that you control this domain.'));
        // The own zones of a customer can be one in the other. The first zone needs the proof.
        $txt['_cloudfactory-challenge.mine.example'] = [$policy->claimToken($this->customer->id, 'mine.example')];
        $this->postJson('/api/v1/dns-zones', ['name' => 'mine.example'])->assertAccepted();
        $this->postJson('/api/v1/dns-zones', ['name' => 'shop.mine.example'])->assertAccepted();
        $this->postJson('/api/v1/dns-zones', ['name' => 'mine.example'])->assertUnprocessable()->assertJsonPath('errors.name.0', 'You have this zone already.');

        // With the TXT record in the public DNS, the customer takes the zone.
        $txt['_cloudfactory-challenge.example.com'] = [$policy->claimToken($this->customer->id, 'example.com')];
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com'])->assertAccepted();
        $this->assertSame($this->customer->id, DnsZone::where('name', 'example.com')->sole()->customer_id);
        $this->assertSame(0, DnsRecord::whereHas('zone', fn ($q) => $q->where('customer_id', $other->id)->where('name', 'example.com'))->count());
    }

    /** Adversarial review, findings A6-01 and A6-02: each new zone of a customer needs a proof. */
    public function test_a_customer_proves_control_of_each_new_zone(): void
    {
        $this->hosting();
        $txt = [];
        $policy = $this->fakeTxt($txt);
        $other = $this->customer($this->customer->plan, ['name' => 'Other Ltd']);
        $squatter = $this->user(Role::Owner, $other);
        $owner = $this->user(Role::Owner, $this->customer);

        // No customer has a zone for the name or near it. The customer must prove control.
        $this->actingWith($squatter);
        foreach (['victim.example', 'www.victim.example'] as $name) {
            $response = $this->postJson('/api/v1/dns-zones', ['name' => $name])->assertUnprocessable();
            $this->assertStringStartsWith('Prove that you control this domain.', $response->json('errors.name.0'));
            $this->assertStringContainsString("_cloudfactory-challenge.{$name} with the value {$policy->claimToken($other->id, $name)}", $response->json('errors.name.0'));
        }
        $this->assertSame(0, DnsZone::count());

        // The domain points to the name servers of the panel and has no zone, for example
        // after its zone was removed. That does not show which customer controls it.
        $ns = ['victim.example' => self::NS];
        $this->fakeNameServers($ns);
        $this->postJson('/api/v1/dns-zones', ['name' => 'victim.example'])->assertUnprocessable();
        // The token of a different customer is not a proof.
        $txt['_cloudfactory-challenge.victim.example'] = [$policy->claimToken($this->customer->id, 'victim.example')];
        $this->postJson('/api/v1/dns-zones', ['name' => 'victim.example'])->assertUnprocessable();
        $this->assertSame(0, DnsZone::count());

        // The owner of the domain has the TXT record, and makes the zone.
        $this->actingWith($owner);
        $this->postJson('/api/v1/dns-zones', ['name' => 'victim.example'])->assertAccepted();
        // A name in a zone of the customer needs no second proof.
        $this->postJson('/api/v1/dns-zones', ['name' => 'www.victim.example'])->assertAccepted();
        $this->assertSame([$this->customer->id], DnsZone::pluck('customer_id')->unique()->values()->all());

        // The other customer cannot take a name below the zone.
        $this->actingWith($squatter);
        $this->postJson('/api/v1/dns-zones', ['name' => 'mail.victim.example'])->assertUnprocessable()
            ->assertJsonPath('errors.name.0', fn (string $m) => str_starts_with($m, 'Prove that you control this domain.'));
    }

    /** A verified domain of a website of the customer is a proof that the customer gave before. */
    public function test_a_verified_website_domain_is_a_proof_for_its_zone(): void
    {
        $this->hosting();
        $txt = [];
        $this->fakeTxt($txt);
        $website = $this->website($this->customer, $this->server(), $this->image(), 'shop.example');
        $website->domains()->create(['name' => 'waiting.example', 'kind' => 'alias', 'verification_token' => 'cf-test', 'verified_at' => null]);

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson('/api/v1/dns-zones', ['name' => 'shop.example'])->assertAccepted();
        $this->postJson('/api/v1/dns-zones', ['name' => 'waiting.example'])->assertUnprocessable();
    }

    public function test_only_an_administrator_makes_a_zone_for_a_name_of_the_panel(): void
    {
        $this->hosting();
        config(['cloudfactory.panel_domain' => 'panel.hosting.example']);

        $this->actingWith($this->user(Role::Owner, $this->customer));
        foreach (['hosting.example', 'panel.hosting.example', 'x.panel.hosting.example', 'ns1.hosting.example'] as $name) {
            $this->postJson('/api/v1/dns-zones', ['name' => $name])->assertUnprocessable()->assertJsonPath('errors.name.0', 'This name is reserved for the panel or a server.');
        }
        $this->actingWith($this->user());
        $this->postJson('/api/v1/dns-zones', ['name' => 'hosting.example', 'customerId' => $this->customer->id])->assertAccepted();
    }

    public function test_the_plan_gives_the_number_of_zones(): void
    {
        $this->hosting(['dns_zones' => 2]);
        $reseller = $this->customer($this->plan(['name' => 'Reseller plan', 'dns_zones' => 3]), ['name' => 'Reseller', 'role' => 'reseller']);
        $resold = $this->customer($this->customer->plan, ['name' => 'Resold', 'reseller_id' => $reseller->id]);

        $this->zone('a.example');
        $this->zone('b.example');
        $this->postJson('/api/v1/dns-zones', ['name' => 'c.example', 'customerId' => $this->customer->id])
            ->assertUnprocessable()->assertJsonPath('errors.name.0', 'The Business plan allows 2 DNS zones.');

        // The zones of the customers of a reseller count against the plan of the reseller.
        $this->zone('d.example', $resold);
        $this->zone('e.example', $reseller);
        $this->zone('f.example', $reseller);
        $this->postJson('/api/v1/dns-zones', ['name' => 'g.example', 'customerId' => $resold->id])
            ->assertUnprocessable()->assertJsonPath('errors.name.0', 'The Reseller plan plan of your provider allows 3 DNS zones in total.');

        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->getJson('/api/v1/account')->assertOk()->assertJsonPath('usage.dnsZones', 3)->assertJsonPath('plan.dnsZones', 3);
        $this->actingWith($this->user());
        $this->postJson('/api/v1/plans', ['name' => 'New', 'websites' => 1, 'mailboxes' => 0, 'databases' => 1, 'phpExtensions' => ['opcache'], 'dnsZones' => 7,
            'limits' => ['vcpus' => 1, 'memoryBytes' => 1, 'diskBytes' => 1, 'diskIoBytesPerSecond' => 1, 'processes' => 1]])->assertCreated()->assertJsonPath('dnsZones', 7);
    }

    public function test_dnssec_gives_each_dns_server_the_same_key_and_the_user_the_ds_record(): void
    {
        $this->hosting();
        $zone = $this->zone();

        $response = $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => true])->assertAccepted()
            ->assertJsonPath('zone.dnssec.enabled', true)->assertJsonCount(2, 'jobs');
        $zone->refresh();
        $this->assertTrue(Input::valid('dnssecKey', $zone->dnssec_key));
        // The key is encrypted in the database.
        $this->assertStringNotContainsString('PRIVATE KEY', (string) $zone->getRawOriginal('dnssec_key'));
        $ds = $response->json('zone.dnssec.ds');
        $this->assertSame($zone->dnssec_key_tag, $ds['keyTag']);
        $this->assertSame([13, 2], [$ds['algorithm'], $ds['digestType']]);
        $this->assertMatchesRegularExpression('/^example\.com\. IN DS \d+ 13 2 [0-9A-F]{64}$/', $ds['record']);
        $this->assertSame("example.com. IN DNSKEY 257 3 13 {$zone->dnssec_public_key}", $response->json('zone.dnssec.dnskey'));
        // The answer never has the private key.
        $this->assertStringNotContainsString('PRIVATE', $response->getContent());

        $jobs = AgentJob::whereIn('id', $response->json('jobs.*.id'))->get();
        foreach ($jobs as $job) {
            // The job record has no key. The panel adds the key when it sends the job.
            $this->assertSame('', $job->payload['zones'][0]['dnssecKey']);
            $this->assertSame($zone->dnssec_key, Zones::keysFor($job)['zones'][0]['dnssecKey']);
            $this->assertSame($zone->dnssec_key_tag, $job->payload['zones'][0]['dnssecKeyTag']);
        }
        // The DNS servers have the key. The record of the job does not keep it.
        $this->finish($jobs->pluck('id')->all());
        $this->assertArrayNotHasKey('dnssecKey', AgentJob::find($jobs[0]->id)->payload['zones'][0]);

        // The same request again changes nothing.
        $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => true])->assertAccepted()->assertJsonCount(0, 'jobs')->assertJsonPath('zone.dnssec.ds.keyTag', $ds['keyTag']);

        $off = $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => false])->assertAccepted()
            ->assertJsonPath('zone.dnssec.enabled', false)->assertJsonPath('zone.dnssec.ds', null);
        $this->assertSame('', AgentJob::find($off->json('jobs.0.id'))->payload['zones'][0]['dnssecKey']);
        $this->assertNull($zone->fresh()->dnssec_key);
    }

    /** The values agree with the answer of "keymgr ds" of Knot DNS for the same key. */
    public function test_the_ds_record_agrees_with_the_dns_server(): void
    {
        $public = 'iObIHci9b3C5J0J8o6XAMnVk0HGrmsTnt3lT1FnHgs/0PV4teV/8/rYPC1UQL/Ug3Z2S9x/KCoNaiPGBMEsoDw==';

        $this->assertSame(22976, Dnssec::keyTag(base64_decode($public)));
        $this->assertSame('example.com. IN DS 22976 13 2 3A8145B62D75A51C3967950FEA365FA967C1D59BED5057A9959340281D46EBA1', Dnssec::ds('example.com', $public)['record']);
        $key = Dnssec::generate();
        $this->assertTrue(Input::valid('dnssecKey', $key['key']));
        $this->assertSame(64, strlen(base64_decode($key['publicKey'])));
    }

    public function test_the_panel_checks_the_name_servers_in_the_public_dns(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $this->assertSame('none', $zone->delegation);

        $this->nameServersSeen['example.com'] = ['ns1.registrar.example', self::NS[0]];
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk()
            ->assertJsonPath('delegation.state', 'elsewhere')->assertJsonPath('delegation.nameServers', ['ns1.registrar.example', self::NS[0]]);

        $this->nameServersSeen['example.com'] = array_reverse(self::NS);
        $this->artisan('cloudfactory:check-dns')->expectsOutput('Checked 1 DNS zone.')->assertSuccessful();
        $this->assertTrue($zone->fresh()->delegatedHere());
        // A zone with its name servers here gets a check one time each day.
        $this->artisan('cloudfactory:check-dns')->expectsOutput('Checked 0 DNS zones.')->assertSuccessful();
        $this->travel(25)->hours();
        $this->artisan('cloudfactory:check-dns')->expectsOutput('Checked 1 DNS zone.')->assertSuccessful();
    }

    public function test_a_website_domain_in_a_zone_of_the_customer_gets_its_address_records(): void
    {
        $this->hosting();
        $web = $this->server(['addresses' => ['203.0.113.7', '2001:db8::7']]);
        $website = $this->website($this->customer, $web, $this->image(), 'example.com');
        // The zone gets the records of the domains that the customer has already.
        $zone = $this->zone();
        $this->assertSame([['A', '203.0.113.7', 300], ['AAAA', '2001:db8::7', 300]],
            $zone->records()->where(['name' => '@', 'managed' => 'website'])->orderBy('type')->get()->map(fn ($r) => [$r->type, $r->value, $r->ttl])->all());
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertJsonPath('websites', 1);

        $this->actingWith($this->user(Role::Owner, $this->customer));
        // The name servers of the zone are not here yet, so the domain waits for its proof.
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'www.example.com', 'kind' => 'alias'])->assertAccepted();
        $this->assertSame(2, $zone->records()->where(['name' => 'www', 'managed' => 'website'])->count());
        $jobs = AgentJob::where('dns_zone_id', $zone->id)->where('title', 'Add the DNS records of www.example.com')->count();
        $this->assertSame(2, $jobs);

        // A name with a record of the customer keeps it.
        $this->finish($this->record($zone, ['type' => 'CNAME', 'name' => 'blog', 'value' => 'blogs.example.net'])->assertAccepted()->json('jobs.*.id'));
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'blog.example.com', 'kind' => 'alias'])->assertAccepted();
        $this->assertSame(['CNAME'], $zone->records()->where('name', 'blog')->pluck('type')->all());

        // A record that the user changes is a record of the user after that.
        $record = $zone->records()->where(['name' => 'www', 'type' => 'A'])->sole();
        $this->patchJson("/api/v1/dns-zones/{$zone->id}/records/{$record->id}", ['value' => '198.51.100.1'])->assertAccepted()->assertJsonPath('record.managed', null);
        $this->deleteJson("/api/v1/websites/{$website->id}/domains/www.example.com")->assertAccepted();
        $this->assertSame(['A'], $zone->records()->where('name', 'www')->pluck('type')->all());

        // The server reports a new address: the records of its websites get it.
        $web->forceFill(['agent_token_hash' => hash('sha256', 'cfa_web'), 'certificate_fingerprint' => str_repeat('ab', 32)])->save();
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $web->update(['local' => true]);
        $this->withToken('cfa_web')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'addresses' => ['203.0.113.99']])->assertOk();
        $this->assertSame([['A', '203.0.113.99']], $zone->records()->where(['name' => '@', 'managed' => 'website'])->get()->map(fn ($r) => [$r->type, $r->value])->all());
    }

    public function test_a_zone_with_its_name_servers_here_verifies_the_domains_of_the_customer(): void
    {
        $this->hosting();
        $txt = [];
        $this->fakeTxt($txt);
        $web = $this->server(['addresses' => ['203.0.113.7']]);
        $website = $this->website($this->customer, $web, $this->image(), 'first.example');
        $zone = $this->zone('example.com');

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'shop.example.com', 'kind' => 'alias'])->assertAccepted()->assertJsonPath('job', null);
        $this->assertFalse(Domain::where('name', 'shop.example.com')->sole()->verified());

        // The registrar sends the queries to the panel now: the customer controls the zone.
        $this->nameServersSeen['example.com'] = self::NS;
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk()->assertJsonPath('delegation.state', 'here');
        $this->assertTrue(Domain::where('name', 'shop.example.com')->sole()->verified());
        $this->assertSame(1, AgentJob::where('action', AgentAction::AddDomain->value)->where('title', 'Add domain shop.example.com')->count());

        // A new domain in the zone is verified at once. A different customer still needs a proof.
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'blog.example.com', 'kind' => 'alias'])->assertAccepted()
            ->assertJsonPath('job.action', 'AddDomain');
        $other = $this->customer($this->customer->plan, ['name' => 'Other Ltd']);
        $theirs = $this->website($other, $web, $this->image(), 'other.example');
        $this->actingWith($this->user(Role::Owner, $other));
        $this->postJson("/api/v1/websites/{$theirs->id}/domains", ['name' => 'evil.example.com', 'kind' => 'alias'])->assertAccepted()->assertJsonPath('job', null);
        $this->assertFalse(app(DomainPolicy::class)->preVerified($this->user(Role::Owner, $other), $other->id, 'evil.example.com'));
    }

    public function test_a_removed_zone_stays_until_each_dns_server_removed_it(): void
    {
        $this->hosting();
        $zone = $this->zone();

        $response = $this->deleteJson("/api/v1/dns-zones/{$zone->id}")->assertAccepted()->assertJsonCount(2, 'jobs');
        $jobs = AgentJob::whereIn('id', $response->json('jobs.*.id'))->get();
        $this->assertSame([['names' => ['example.com']], ['names' => ['example.com']]], $jobs->pluck('payload')->all());
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()->assertJsonPath('status', 'removing');
        // A zone that the panel removes takes no change.
        $this->record($zone, ['type' => 'A', 'name' => 'x', 'value' => '203.0.113.1'])->assertUnprocessable()->assertJsonPath('errors.zone.0', 'The panel removes this zone.');
        $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])->assertUnprocessable();

        $this->finish($jobs[0]->id);
        $this->assertNotNull($zone->fresh());
        $this->finish($jobs[1]->id, JobState::Failed);
        $this->assertNotNull($zone->fresh());
        // The user removes the zone again, and the second DNS server removes it now.
        $again = $this->deleteJson("/api/v1/dns-zones/{$zone->id}")->assertAccepted();
        $this->finish($again->json('jobs.*.id'));
        $this->assertNull($zone->fresh());
        $this->assertSame(0, DnsRecord::count());
    }

    public function test_a_change_of_the_name_servers_sends_all_zones_again(): void
    {
        $this->hosting();
        $a = $this->zone('a.example');
        $b = $this->zone('b.example');
        $serial = $a->serial;
        AgentJob::query()->delete();

        // A third DNS server gets its host name.
        $third = $this->server(['roles' => ['web', 'dns'], 'dns' => true]);
        $this->patchJson("/api/v1/servers/{$third->id}", ['dnsName' => 'ns3.hosting.example'])->assertOk()->assertJsonPath('dnsName', 'ns3.hosting.example');
        $jobs = AgentJob::where('action', AgentAction::SetDnsZones->value)->get();
        $this->assertCount(3, $jobs);
        foreach ($jobs as $job) {
            $this->assertNull($job->dns_zone_id);
            $this->assertSame(['a.example', 'b.example'], array_column($job->payload['zones'], 'name'));
            $this->assertSame('Update 2 DNS zones on '.$job->server->name, $job->title);
        }
        $this->assertSame([...self::NS, 'ns3.hosting.example'], $a->records()->where('managed', 'ns')->orderBy('value')->pluck('value')->all());
        $this->assertGreaterThan($serial, $a->fresh()->serial);
        $this->getJson("/api/v1/dns-zones/{$a->id}")->assertJsonPath('status', 'updating');
        $this->finish($jobs->pluck('id')->all());
        $this->getJson("/api/v1/dns-zones/{$b->id}")->assertJsonPath('status', 'active');
        // Two servers cannot have the same host name.
        $this->patchJson("/api/v1/servers/{$this->dns[0]->id}", ['dnsName' => 'ns3.hosting.example'])->assertUnprocessable()->assertJsonValidationErrors('dnsName');

        // The server loses the role: it removes its zones, and the other servers get the zones
        // with two name servers.
        AgentJob::query()->delete();
        $this->patchJson("/api/v1/servers/{$third->id}", ['roles' => ['web']])->assertOk();
        $removal = AgentJob::where('action', AgentAction::DeleteDnsZones->value)->sole();
        $this->assertSame([$third->id, ['names' => ['a.example', 'b.example']]], [$removal->server_id, $removal->payload]);
        $this->assertSame(2, AgentJob::where('action', AgentAction::SetDnsZones->value)->count());
        $this->assertSame(self::NS, $a->records()->where('managed', 'ns')->orderBy('value')->pluck('value')->all());
        $this->assertSame(self::NS, Zones::nameServers());

        // An agent that reports that it can operate DNS gets the zones.
        AgentJob::query()->delete();
        $late = $this->server(['roles' => ['dns'], 'dns_name' => 'ns4.hosting.example', 'agent_token_hash' => hash('sha256', 'cfa_late'), 'local' => true]);
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $this->withToken('cfa_late')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'dns' => true])->assertOk();
        $this->assertSame(3, AgentJob::where('action', AgentAction::SetDnsZones->value)->count());
        $this->assertTrue($late->fresh()->dns);
    }

    /** The agent of a DNS server sends a report with the zones that its DNS server loaded. */
    private function report(Server $server, array $zones): void
    {
        $server->update(['agent_token_hash' => hash('sha256', "cfa_{$server->id}"), 'local' => true]);
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $this->withToken("cfa_{$server->id}")->postJson('/api/agent/v1/report', [
            'agentVersion' => '0.1.0', 'dns' => true,
            'dnsZones' => collect($zones)->map(fn (int $serial, string $name) => ['name' => $name, 'serial' => $serial])->values()->all(),
        ])->assertOk();
    }

    public function test_a_dns_server_that_was_offline_gets_the_zones_that_it_does_not_have(): void
    {
        $this->hosting();
        $a = $this->zone('a.example');
        $b = $this->zone('b.example');
        [$first, $second] = $this->dns;
        AgentJob::query()->delete();

        // A report that agrees with the panel makes no job.
        $this->report($first, ['a.example' => $a->serial, 'b.example' => $b->serial]);
        $this->assertSame(0, AgentJob::count());

        // The second server was offline during a change: its job failed.
        $jobs = $this->record($a, ['type' => 'A', 'name' => 'www', 'value' => '203.0.113.7'])->assertAccepted()->json('jobs.*');
        $old = $a->serial;
        $a->refresh();
        $this->finish(collect($jobs)->firstWhere('serverId', $first->id)['id']);
        $this->finish(collect($jobs)->firstWhere('serverId', $second->id)['id'], JobState::Failed);
        $this->getJson("/api/v1/dns-zones/{$a->id}")->assertJsonPath('status', 'failed');
        AgentJob::query()->delete();

        // The server is back, and reports the zone that it had before. The panel sends only
        // the zone that changed, with all its records.
        $this->report($second, ['a.example' => $old, 'b.example' => $b->serial]);
        $job = AgentJob::sole();
        $this->assertSame([$second->id, AgentAction::SetDnsZones, 'Send DNS zone a.example to '.$second->name.' again'], [$job->server_id, $job->action, $job->title]);
        $this->assertSame(['a.example'], array_column($job->payload['zones'], 'name'));
        $this->assertSame($a->serial, $job->payload['zones'][0]['serial']);
        $this->assertContains('www', array_column($job->payload['zones'][0]['records'], 'name'));
        $this->getJson("/api/v1/dns-zones/{$a->id}")->assertJsonPath('status', 'updating');

        // While the job is in the queue, a new report makes no second job.
        $this->report($second, ['a.example' => $old, 'b.example' => $b->serial]);
        $this->assertSame(1, AgentJob::count());

        $this->finish($job->id);
        $this->report($second, ['a.example' => $a->serial, 'b.example' => $b->serial]);
        $this->assertSame(1, AgentJob::count());
        $this->getJson("/api/v1/dns-zones/{$a->id}")->assertJsonPath('status', 'active');
        $this->assertNull($second->fresh()->dns_resync);
    }

    public function test_a_dns_server_that_lost_its_data_gets_all_zones_and_loses_zones_that_the_panel_does_not_have(): void
    {
        $this->hosting();
        $a = $this->zone('a.example');
        $b = $this->zone('b.example');
        $second = $this->dns[1];
        AgentJob::query()->delete();

        // A new disk: the server has no zone. A zone of an earlier time is on the server only.
        $this->report($second, ['gone.example' => 1700000000]);
        $send = AgentJob::where('action', AgentAction::SetDnsZones->value)->sole();
        $this->assertSame('Send 2 DNS zones to '.$second->name.' again', $send->title);
        $this->assertSame(['a.example', 'b.example'], array_column($send->payload['zones'], 'name'));
        $remove = AgentJob::where('action', AgentAction::DeleteDnsZones->value)->sole();
        $this->assertSame(['names' => ['gone.example']], $remove->payload);
        $this->assertSame($second->id, $remove->server_id);
    }

    public function test_the_panel_waits_longer_after_each_try_that_did_not_correct_a_dns_server(): void
    {
        $this->hosting();
        $a = $this->zone('a.example');
        $second = $this->dns[1];
        AgentJob::query()->delete();
        $tries = function () use ($second) {
            $this->report($second, []);
            $this->finish(AgentJob::where('state', JobState::Queued->value)->pluck('id')->all(), JobState::Failed);

            return AgentJob::count();
        };

        $this->assertSame(1, $tries());
        // The second try comes after 1 minute, the third after 2 more minutes.
        $this->assertSame(1, $tries());
        $this->travel(61)->seconds();
        $this->assertSame(2, $tries());
        $this->travel(61)->seconds();
        $this->assertSame(2, $tries());
        $this->travel(61)->seconds();
        $this->assertSame(3, $tries());
        // A report that agrees with the panel ends the wait.
        $this->travel(5)->minutes();
        $this->report($second, []);
        $this->finish(AgentJob::where('state', JobState::Queued->value)->pluck('id')->all());
        $this->report($second, ['a.example' => $a->serial]);
        $this->assertNull($second->fresh()->dns_resync);
    }

    public function test_a_server_with_a_newer_zone_than_the_panel_gets_a_zone_with_a_larger_serial(): void
    {
        $this->hosting();
        $a = $this->zone('a.example');
        $second = $this->dns[1];
        AgentJob::query()->delete();

        // The panel database is from a backup: the server has a later change.
        $this->report($second, ['a.example' => $a->serial + 500]);
        $job = AgentJob::sole();
        $this->assertGreaterThan($a->serial + 500, $job->payload['zones'][0]['serial']);
        $this->assertSame($a->fresh()->serial, $job->payload['zones'][0]['serial']);
    }

    public function test_a_server_that_does_not_answer_dns_queries_now_loses_the_zones_that_it_reports(): void
    {
        $this->hosting();
        $a = $this->zone('a.example');
        $old = $this->server(['roles' => ['web']]);
        AgentJob::query()->delete();

        $this->report($old, ['a.example' => $a->serial]);
        $job = AgentJob::sole();
        $this->assertSame([$old->id, AgentAction::DeleteDnsZones, ['names' => ['a.example']]], [$job->server_id, $job->action, $job->payload]);
        // An agent of an earlier release sends no list of zones. The panel changes nothing.
        AgentJob::query()->delete();
        $old->update(['dns_resync' => null]);
        $this->withToken("cfa_{$old->id}")->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0'])->assertOk();
        $this->assertSame(0, AgentJob::count());
    }

    public function test_the_steps_of_the_dns_jobs(): void
    {
        $this->assertSame(['Start DNS server', 'Write zones', 'Load zones'], AgentAction::SetDnsZones->steps());
        $this->assertSame(['Remove zones'], AgentAction::DeleteDnsZones->steps());
    }
}
