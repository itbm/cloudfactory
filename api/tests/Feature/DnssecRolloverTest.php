<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsZone;
use App\Models\Server;
use App\Services\Dns\Dnssec;
use App\Services\Dns\Lookup;
use App\Services\Dns\Rollover;
use App\Services\Dns\Zones;
use App\Services\JobOutcome;
use Illuminate\Testing\TestResponse;
use Tests\TestCase;

/**
 * A change of the DNSSEC key of a zone, in steps, and the check of the DS records at the
 * registry. Refer to App\Services\Dns\Rollover.
 */
class DnssecRolloverTest extends TestCase
{
    private const NS = ['ns1.hosting.example', 'ns2.hosting.example'];

    private Customer $customer;

    /** @var list<Server> */
    private array $dns;

    private Lookup $lookup;

    /** @var array<string, list<string>> */
    private array $ns = [];

    private function hosting(): void
    {
        $this->customer = $this->customer();
        $this->dns = [$this->dnsServer(self::NS[0]), $this->dnsServer(self::NS[1])];
        $this->ns = ['example.com' => self::NS];
        $this->lookup = $this->fakeNameServers($this->ns);
        $this->actingWith($this->user());
    }

    private function finish(TestResponse|array $jobs): void
    {
        foreach ($jobs instanceof TestResponse ? $jobs->json('jobs.*.id') : $jobs as $id) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($id), JobState::Succeeded, null);
        }
    }

    /** A zone with DNSSEC on, on each DNS server. */
    private function signedZone(): DnsZone
    {
        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])->assertAccepted();
        $this->finish($response);
        $zone = DnsZone::findOrFail($response->json('zone.id'));
        $this->finish($this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => true])->assertAccepted());

        return $zone->fresh();
    }

    /** The DS records that the registry has: the keys of the zone by their public keys. */
    private function registry(string ...$publicKeys): void
    {
        $this->lookup->ds['example.com'] = array_map(fn ($k) => Dnssec::ds('example.com', $k) + ['ttl' => 3600], $publicKeys);
    }

    private function check(DnsZone $zone): TestResponse
    {
        return $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk();
    }

    /** The keys that the panel sends to the agent with the last job of the zone. */
    private function sentKeys(DnsZone $zone): array
    {
        $job = AgentJob::where('dns_zone_id', $zone->id)->latest('id')->first();

        return array_map(fn ($k) => [$k['keyTag'], $k['state'], $k['key'] !== ''], Zones::keysFor($job)['zones'][0]['dnssecKeys']);
    }

    public function test_a_change_of_the_key_goes_through_its_steps(): void
    {
        $this->hosting();
        $zone = $this->signedZone();
        $old = ['tag' => $zone->dnssec_key_tag, 'public' => $zone->dnssec_public_key];
        $this->registry($old['public']);

        // The new key goes to each DNS server. It signs only the DNSKEY records.
        $response = $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertAccepted()
            ->assertJsonCount(2, 'jobs')
            ->assertJsonPath('zone.dnssec.rollover.step', 'publish')
            ->assertJsonPath('zone.dnssec.rollover.until', null)
            ->assertJsonPath('zone.dnssec.rollover.oldDs.keyTag', $old['tag'])
            // The registrar keeps the old DS record in this step.
            ->assertJsonPath('zone.dnssec.ds.keyTag', $old['tag']);
        $zone->refresh();
        $new = ['tag' => $zone->dnssec_next_key_tag, 'public' => $zone->dnssec_next_public_key];
        $this->assertNotSame($old['tag'], $new['tag']);
        $this->assertSame($new['tag'], $response->json('zone.dnssec.rollover.newDs.keyTag'));
        $this->assertStringNotContainsString('PRIVATE', $response->getContent());
        $this->assertStringNotContainsString('PRIVATE', (string) $zone->getRawOriginal('dnssec_next_key'));
        $this->assertSame([[$old['tag'], 'active', true], [$new['tag'], 'ready', true]], $this->sentKeys($zone));
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertUnprocessable()->assertJsonValidationErrors('rollover');

        // The step waits until each DNS server has the new key, and then for the caches.
        $this->check($zone)->assertJsonPath('dnssec.rollover.until', null);
        $this->finish($response);
        $until = $this->check($zone)->json('dnssec.rollover.until');
        $this->assertEqualsWithDelta(now()->addHours(3)->timestamp, strtotime($until), 5);
        $this->travel(2)->hours();
        $this->check($zone)->assertJsonPath('dnssec.rollover.step', 'publish');

        // The zone gets the CDS record of the new key, and the user gives the new DS record to
        // the registrar.
        $this->travel(61)->minutes();
        $this->check($zone)->assertJsonPath('dnssec.rollover.step', 'submit')
            ->assertJsonPath('dnssec.ds.keyTag', $new['tag'])
            ->assertJsonPath('dnssec.parent.state', 'ok')
            ->assertJsonPath('dnssec.parent.keyTags', [$old['tag']]);
        $this->assertSame([[$old['tag'], 'active', true], [$new['tag'], 'submit', true]], $this->sentKeys($zone));
        $this->finish(AgentJob::where('dns_zone_id', $zone->id)->where('state', JobState::Queued)->pluck('id')->all());
        // The registry does not have the new DS record yet.
        $this->travel(1)->days();
        $this->check($zone)->assertJsonPath('dnssec.rollover.step', 'submit');

        // The registry has the new DS record: both keys sign the zone.
        $this->registry($old['public'], $new['public']);
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertUnprocessable();
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertUnprocessable()
            ->assertJsonPath('errors.rollover.0', 'The registry has the new DS record already. The change of the key continues.');
        $this->registry($new['public']);
        $response = $this->check($zone)->assertJsonPath('dnssec.rollover.step', 'retire')
            ->assertJsonPath('dnssec.parent.keyTags', [$new['tag']]);
        // One day for the old DS record in the caches, and one hour more.
        $this->assertEqualsWithDelta(now()->addHours(25)->timestamp, strtotime($response->json('dnssec.rollover.until')), 5);
        $this->assertSame([[$new['tag'], 'active', true], [$old['tag'], 'active', true]], $this->sentKeys($zone));
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertUnprocessable();

        // The old key goes after the wait, but not while the registry has only the old DS record.
        $this->travel(26)->hours();
        $this->registry($old['public']);
        $this->check($zone)->assertJsonPath('dnssec.rollover.step', 'retire');
        $this->registry($new['public']);
        $this->check($zone)->assertJsonPath('dnssec.rollover', null)
            ->assertJsonPath('dnssec.ds.keyTag', $new['tag'])
            ->assertJsonPath('dnssec.parent.state', 'ok')
            ->assertJsonPath('dnssec.parent.keyTags', [$new['tag']]);
        $zone->refresh();
        $this->assertSame($new['tag'], $zone->dnssec_key_tag);
        $this->assertNull($zone->dnssec_next_key);
        $this->assertSame([[$new['tag'], 'active', true]], $this->sentKeys($zone));
    }

    public function test_a_record_with_a_long_ttl_makes_the_last_step_longer(): void
    {
        $this->hosting();
        $zone = $this->signedZone();
        $this->postJson("/api/v1/dns-zones/{$zone->id}/records", ['name' => 'www', 'type' => 'A', 'value' => '192.0.2.1', 'ttl' => 172800])->assertAccepted();
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertAccepted();
        $zone->refresh()->update(['dnssec_rollover' => Rollover::SUBMIT]);
        $this->registry($zone->dnssec_next_public_key);
        $until = $this->check($zone)->assertJsonPath('dnssec.rollover.step', 'retire')->json('dnssec.rollover.until');
        $this->assertEqualsWithDelta(now()->addSeconds(172800 + 3600)->timestamp, strtotime($until), 5);
    }

    public function test_a_change_stops_before_the_registry_has_the_new_ds_record(): void
    {
        $this->hosting();
        $zone = $this->signedZone();
        $tag = $zone->dnssec_key_tag;
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertUnprocessable()
            ->assertJsonPath('errors.rollover.0', 'The key of this zone does not change.');
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertAccepted();

        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertAccepted()->assertJsonCount(2, 'jobs')
            ->assertJsonPath('zone.dnssec.rollover', null)->assertJsonPath('zone.dnssec.ds.keyTag', $tag);
        $zone->refresh();
        $this->assertNull($zone->dnssec_next_key_tag);
        $this->assertSame([[$tag, 'active', true]], $this->sentKeys($zone));

        // DNSSEC off also ends a change.
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertAccepted();
        $this->putJson("/api/v1/dns-zones/{$zone->id}/dnssec", ['enabled' => false])->assertAccepted()->assertJsonPath('zone.dnssec.rollover', null);
        $this->assertNull($zone->fresh()->dnssec_rollover);
        $this->assertSame([], $this->sentKeys($zone));
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertUnprocessable()
            ->assertJsonPath('errors.rollover.0', 'DNSSEC is off for this zone.');
    }

    public function test_the_panel_shows_the_ds_records_at_the_registry(): void
    {
        $this->hosting();
        $zone = $this->signedZone();

        $this->registry($zone->dnssec_public_key);
        $this->check($zone)->assertJsonPath('dnssec.parent.state', 'ok')->assertJsonPath('dnssec.parent.keyTags', [$zone->dnssec_key_tag])
            ->assertJsonPath('dnssec.parent.otherKeyTags', []);
        // The registrar made the DS record with a different digest type.
        $this->lookup->ds['example.com'] = [[
            'keyTag' => $zone->dnssec_key_tag, 'algorithm' => 13, 'digestType' => 4,
            'digest' => Dnssec::digest('example.com', $zone->dnssec_public_key, 4), 'ttl' => 3600,
        ]];
        $this->check($zone)->assertJsonPath('dnssec.parent.state', 'ok');
        // The DS record of a different key: resolvers refuse the answers.
        $other = Dnssec::generate();
        $this->registry($other['publicKey']);
        $this->check($zone)->assertJsonPath('dnssec.parent.state', 'other')->assertJsonPath('dnssec.parent.otherKeyTags', [$other['keyTag']]);
        $this->registry();
        $this->check($zone)->assertJsonPath('dnssec.parent.state', 'none');
        $this->lookup->ds['example.com'] = null;
        $this->check($zone)->assertJsonPath('dnssec.parent.state', 'unknown');

        // The scheduled command checks a zone one time each day, and each time during a change.
        $this->registry($zone->dnssec_public_key);
        $this->artisan('cloudfactory:check-dns')->assertSuccessful();
        $this->assertSame('unknown', $zone->fresh()->dnssec_parent['state']);
        $this->travel(25)->hours();
        $this->artisan('cloudfactory:check-dns')->expectsOutputToContain('Checked the DS records of 1 DNS zone.')->assertSuccessful();
        $this->assertSame('ok', $zone->fresh()->dnssec_parent['state']);
    }

    public function test_the_scheduled_command_moves_a_change_to_the_next_step(): void
    {
        $this->hosting();
        $zone = $this->signedZone();
        $this->finish($this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertAccepted());
        $this->artisan('cloudfactory:check-dns')->assertSuccessful();
        $this->assertNotNull($zone->fresh()->dnssec_rollover_until);
        $this->travel(181)->minutes();
        $this->artisan('cloudfactory:check-dns')->assertSuccessful();
        $this->assertSame(Rollover::SUBMIT, $zone->fresh()->dnssec_rollover);
    }

    public function test_a_different_customer_cannot_change_the_key(): void
    {
        $this->hosting();
        $zone = $this->signedZone();
        $this->actingWith($this->user(Role::Owner, $this->customer()));
        $this->postJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertNotFound();
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/dnssec/rollover")->assertNotFound();
    }

    /** The DS records in a raw answer of the public DNS, with the values of "keymgr ds" of Knot DNS. */
    public function test_the_lookup_reads_the_ds_records_of_the_public_dns(): void
    {
        $sha256 = '866570E5A0289FFDBDDE4C3620A12CDA9ACC083C81EEF3D1F8293C3C5E4ACFD8';
        $sha384 = 'FF1F7F2F17AC1A6FE5AA72D1C5532883915D4B235C01558126CC02A67D05B6BAD8CB51038B03907D41B889B201DD8C39';
        $records = [
            ['host' => 'example.com', 'class' => 'IN', 'ttl' => 86400, 'type' => 43, 'data' => pack('nCC', 10161, 13, 2).hex2bin($sha256)],
            ['host' => 'example.com', 'class' => 'IN', 'ttl' => 86400, 'type' => 43, 'data' => pack('nCC', 10161, 13, 4).hex2bin($sha384)],
            // A record of a different name or type, and a record that is too short, do not count.
            ['host' => 'other.example', 'class' => 'IN', 'ttl' => 60, 'type' => 43, 'data' => pack('nCC', 1, 13, 2).hex2bin($sha256)],
            ['host' => 'example.com', 'class' => 'IN', 'ttl' => 60, 'type' => 46, 'data' => 'x'],
            ['host' => 'example.com', 'class' => 'IN', 'ttl' => 60, 'type' => 43, 'data' => 'abc'],
        ];
        $ds = Lookup::parseDs('example.com', $records);
        $this->assertSame([
            ['keyTag' => 10161, 'algorithm' => 13, 'digestType' => 2, 'digest' => $sha256, 'ttl' => 86400],
            ['keyTag' => 10161, 'algorithm' => 13, 'digestType' => 4, 'digest' => $sha384, 'ttl' => 86400],
        ], $ds);
        $public = '8qC48Q2lp4uhPgO5l1Rvz4TWGqF2VNTBPQaH4/dnO3tpikSrfRpureG2TB0krvEOkUsb5TP3yky99pIJz5CL1A==';
        $this->assertTrue(Dnssec::matches('example.com', $public, $ds[0]));
        $this->assertTrue(Dnssec::matches('example.com', $public, $ds[1]));
        $this->assertFalse(Dnssec::matches('example.org', $public, $ds[0]));
        $this->assertFalse(Dnssec::matches('example.com', $public, ['digestType' => 3] + $ds[0]));
    }
}
