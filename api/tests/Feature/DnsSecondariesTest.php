<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsSecondary;
use App\Models\DnsZone;
use App\Models\Server;
use App\Models\Setting;
use App\Services\AgentClient;
use App\Services\Dns\Secondaries;
use App\Services\Dns\Zones;
use App\Services\JobOutcome;
use App\Validation\InputRules;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/**
 * Secondary DNS: DNS servers outside the panel copy the zones from the DNS servers of the
 * panel with zone transfers. The secondaries of the panel copy all zones. The owner of a zone
 * can also give its own secondaries for the zone.
 */
class DnsSecondariesTest extends TestCase
{
    private const NS = ['ns1.hosting.example', 'ns2.hosting.example'];

    private Customer $customer;

    /** @var list<Server> */
    private array $dns;

    private function hosting(): void
    {
        $this->customer = $this->customer();
        $agent = ['certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)];
        $this->dns = [
            $this->dnsServer(self::NS[0], ['addresses' => ['192.0.2.1', '2001:db8::1'], 'agent_url' => 'https://192.0.2.1:7443', ...$agent]),
            $this->dnsServer(self::NS[1], ['addresses' => ['192.0.2.2'], 'agent_url' => 'https://192.0.2.2:7443', ...$agent]),
        ];
        $this->actingWith($this->user());
    }

    private function finish(array|string $jobs, JobState $state = JobState::Succeeded): void
    {
        foreach ((array) $jobs as $id) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($id), $state, $state === JobState::Failed ? 'knotc: reload failed' : null);
        }
    }

    private function zone(string $name = 'example.com'): DnsZone
    {
        $response = $this->postJson('/api/v1/dns-zones', ['name' => $name, 'customerId' => $this->customer->id])->assertAccepted();
        $this->finish($response->json('jobs.*.id'));

        return DnsZone::findOrFail($response->json('zone.id'));
    }

    /** The body that the panel sends to the agent for a job. */
    private function sent(AgentJob $job): array
    {
        Http::fake(['*' => Http::response(['jobId' => $job->id])]);
        app(AgentClient::class)->send($job->fresh('server'));
        $body = null;
        Http::assertSent(function ($request) use (&$body, $job) {
            if (($request->data()['jobId'] ?? null) === $job->id) {
                $body = $request->data();
            }

            return true;
        });

        return $body;
    }

    public function test_an_administrator_adds_a_secondary_and_each_dns_server_gets_the_list(): void
    {
        $this->hosting();
        $zone = $this->zone();

        $response = $this->postJson('/api/v1/dns-secondaries', [
            'name' => 'Anycast secondary',
            'addresses' => ['198.51.100.53', '2001:DB8:0:0::53', '198.51.100.53'],
            'nameServer' => 'ns3.secondary.example',
            'catalog' => true,
        ])->assertAccepted()
            ->assertJsonPath('secondary.addresses', ['198.51.100.53', '2001:db8::53'])
            ->assertJsonPath('secondary.nameServer', 'ns3.secondary.example')
            ->assertJsonPath('secondary.tsig.algorithm', 'hmac-sha256')
            ->assertJsonPath('secondary.notify', true)
            ->assertJsonPath('secondary.catalog', true)
            ->assertJsonCount(2, 'jobs');
        $secondary = DnsSecondary::sole();
        $this->assertMatchesRegularExpression('/^cf-[0-9a-z]{26}$/', $secondary->tsig_name);
        $this->assertSame(32, strlen(base64_decode($secondary->tsig_secret)));
        // The secret is encrypted in the database, and no answer has it.
        $this->assertStringNotContainsString($secondary->tsig_secret, (string) $secondary->getRawOriginal('tsig_secret'));
        $this->assertStringNotContainsString($secondary->tsig_secret, $response->getContent());
        $this->assertSame(1, Secondaries::version());

        // The job has no secret. The panel adds the current list when it sends the job.
        $job = AgentJob::where('action', AgentAction::SetDnsSecondaries->value)->where('server_id', $this->dns[0]->id)->sole();
        $this->assertSame(['version' => 1], $job->payload);
        $body = $this->sent($job);
        $this->assertSame([[
            'id' => $secondary->id,
            'addresses' => ['198.51.100.53', '2001:db8::53'],
            'tsig' => ['name' => $secondary->tsig_name, 'algorithm' => 'hmac-sha256', 'secret' => $secondary->tsig_secret],
            'notify' => true,
            'catalog' => true,
        ]], $body['secondaries']);
        $this->assertSame('catalog.cloudfactory.invalid', $body['catalogZone']);
        $this->assertSame(1, $body['version']);

        // Each zone gets the NS record of the secondary, and each DNS server gets the zones.
        $this->assertContains('ns3.secondary.example', $zone->records()->where('type', 'NS')->pluck('value')->all());
        $this->assertSame(2, AgentJob::where('action', AgentAction::SetDnsZones->value)->where('state', JobState::Queued->value)->count());
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertOk()->assertJsonPath('nameServers', [...self::NS, 'ns3.secondary.example']);

        // The list page has the primaries, the catalog zone and the state on each DNS server.
        $this->finish($response->json('jobs.0.id'));
        $this->getJson('/api/v1/dns-secondaries')->assertOk()
            ->assertJsonPath('data.0.name', 'Anycast secondary')
            ->assertJsonPath('catalogZone', 'catalog.cloudfactory.invalid')
            ->assertJsonPath('primaries', [['nameServer' => self::NS[0], 'addresses' => ['192.0.2.1', '2001:db8::1']], ['nameServer' => self::NS[1], 'addresses' => ['192.0.2.2']]])
            ->assertJsonPath('servers', [['server' => self::NS[0], 'state' => 'ok'], ['server' => self::NS[1], 'state' => 'pending']]);

        // The secret is only on its own route, and the audit log records each look.
        $this->getJson("/api/v1/dns-secondaries/{$secondary->id}/secret")->assertOk()
            ->assertExactJson(['name' => $secondary->tsig_name, 'algorithm' => 'hmac-sha256', 'secret' => $secondary->tsig_secret]);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Viewed the TSIG secret of a secondary DNS server']);
    }

    public function test_a_change_makes_a_new_version_and_the_removal_of_a_name_server_changes_the_zones(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $id = $this->postJson('/api/v1/dns-secondaries', ['name' => 'Backup', 'addresses' => ['10.0.0.53'], 'nameServer' => 'ns3.secondary.example'])->assertAccepted()->json('secondary.id');
        $secret = DnsSecondary::find($id)->tsig_secret;

        // The same values: no new version and no job.
        $this->patchJson("/api/v1/dns-secondaries/{$id}", ['name' => 'Backup'])->assertOk()->assertJsonCount(0, 'jobs');
        $this->assertSame(1, Secondaries::version());

        // A new algorithm gives a new secret. The name server stays, so the zones do not change.
        $zoneJobs = AgentJob::where('action', AgentAction::SetDnsZones->value)->count();
        $this->patchJson("/api/v1/dns-secondaries/{$id}", ['algorithm' => 'hmac-sha512'])->assertAccepted()->assertJsonPath('secondary.tsig.algorithm', 'hmac-sha512');
        $this->assertSame(64, strlen(base64_decode(DnsSecondary::find($id)->tsig_secret)));
        $this->assertNotSame($secret, DnsSecondary::find($id)->tsig_secret);
        $this->assertSame(2, Secondaries::version());
        $this->assertSame($zoneJobs, AgentJob::where('action', AgentAction::SetDnsZones->value)->count());

        // The administrator copies the secret of the secondary DNS service, and then turns off the key.
        $given = base64_encode(str_repeat('k', 32));
        $this->patchJson("/api/v1/dns-secondaries/{$id}", ['secret' => $given])->assertAccepted();
        $this->assertSame($given, DnsSecondary::find($id)->tsig_secret);
        $this->patchJson("/api/v1/dns-secondaries/{$id}", ['tsig' => false])->assertAccepted()->assertJsonPath('secondary.tsig', null);
        $this->getJson("/api/v1/dns-secondaries/{$id}/secret")->assertNotFound();

        // The removal: a new version with an empty list, and the zones lose the NS record.
        $this->deleteJson("/api/v1/dns-secondaries/{$id}")->assertAccepted()->assertJsonCount(2, 'jobs');
        $this->assertSame(5, Secondaries::version());
        $this->assertSame(self::NS, $zone->records()->where('type', 'NS')->orderBy('value')->pluck('value')->all());
        $job = AgentJob::where('action', AgentAction::SetDnsSecondaries->value)->latest('id')->first();
        $this->assertEquals(['secondaries' => [], 'catalogZone' => '', 'version' => 5], array_intersect_key($this->sent($job), array_flip(['secondaries', 'catalogZone', 'version'])));
    }

    public function test_the_input_of_a_secondary_has_rules(): void
    {
        $this->hosting();
        foreach ([
            'no address' => [['addresses' => []], 'addresses'],
            'not an address' => [['addresses' => ['ns1.example.net']], 'addresses'],
            'multicast' => [['addresses' => ['224.0.0.1']], 'addresses'],
            'IPv4 in IPv6' => [['addresses' => ['::ffff:192.0.2.1']], 'addresses'],
            'a zone key' => [['keyName' => 'transfer.example.com'], 'keyName'],
            'a key name' => [['keyName' => 'Key Name'], 'keyName'],
            'an algorithm' => [['algorithm' => 'hmac-md5'], 'algorithm'],
            'a short secret' => [['secret' => base64_encode('short')], 'secret'],
            'not base64' => [['secret' => str_repeat('!', 44)], 'secret'],
            'a name server of the panel' => [['nameServer' => self::NS[0]], 'nameServer'],
        ] as $case => [$fields, $error]) {
            $this->postJson('/api/v1/dns-secondaries', [...['name' => 'x', 'addresses' => ['192.0.2.53']], ...$fields])
                ->assertUnprocessable()->assertJsonValidationErrors($error);
        }
        $first = $this->postJson('/api/v1/dns-secondaries', ['name' => 'a', 'addresses' => ['192.0.2.53'], 'keyName' => 'cf-secondary'])->assertAccepted();
        $this->postJson('/api/v1/dns-secondaries', ['name' => 'b', 'addresses' => ['192.0.2.54'], 'keyName' => 'cf-secondary'])->assertUnprocessable()->assertJsonValidationErrors('keyName');
        // The panel has at most MAX_DNS_SECONDARIES secondaries.
        for ($i = 1; $i < InputRules::MAX_DNS_SECONDARIES; $i++) {
            $this->postJson('/api/v1/dns-secondaries', ['name' => "s{$i}", 'addresses' => ["192.0.2.{$i}"]])->assertAccepted();
        }
        $this->postJson('/api/v1/dns-secondaries', ['name' => 'more', 'addresses' => ['192.0.2.99']])->assertUnprocessable()->assertJsonValidationErrors('name');
        $this->assertSame('cf-secondary', $first->json('secondary.tsig.name'));
    }

    public function test_only_an_administrator_sees_and_changes_the_secondaries(): void
    {
        $this->hosting();
        $id = $this->postJson('/api/v1/dns-secondaries', ['name' => 'a', 'addresses' => ['192.0.2.53']])->assertAccepted()->json('secondary.id');
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->getJson('/api/v1/dns-secondaries')->assertForbidden();
        $this->getJson("/api/v1/dns-secondaries/{$id}/secret")->assertForbidden();
        $this->postJson('/api/v1/dns-secondaries', ['name' => 'b', 'addresses' => ['192.0.2.54']])->assertForbidden();
        $this->deleteJson("/api/v1/dns-secondaries/{$id}")->assertForbidden();
        // An administrator token needs the DNS scope.
        $this->actingWith($this->user(), ['servers:write']);
        $this->getJson('/api/v1/dns-secondaries')->assertForbidden();
    }

    public function test_a_dns_server_with_a_different_version_gets_the_list_again(): void
    {
        $this->hosting();
        $server = $this->dns[0];
        $server->update(['agent_token_hash' => hash('sha256', "cfa_{$server->id}"), 'local' => true]);
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $report = fn (int $version) => $this->withToken("cfa_{$server->id}")->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'dns' => true, 'dnsZones' => [], 'dnsSecondaries' => $version])->assertOk();
        $jobs = fn () => AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetDnsSecondaries->value);

        // No secondary ever: nothing to send.
        $report(0);
        $this->assertSame(0, $jobs()->count());

        $this->postJson('/api/v1/dns-secondaries', ['name' => 'a', 'addresses' => ['192.0.2.53']])->assertAccepted();
        // The job is in the queue: the panel waits.
        $report(0);
        $this->assertSame(1, $jobs()->count());
        // The job failed: the panel tries again after RETRY_MINUTES.
        $this->finish($jobs()->sole()->id, JobState::Failed);
        $report(0);
        $this->assertSame(1, $jobs()->count());
        $this->travel(Secondaries::RETRY_MINUTES + 1)->minutes();
        $report(0);
        $this->assertSame(2, $jobs()->count());
        $this->finish($jobs()->latest('id')->first()->id);
        // The server has the version: nothing to send.
        $report(1);
        $this->assertSame(2, $jobs()->count());
    }

    public function test_a_new_name_of_the_catalog_zone_makes_a_new_version(): void
    {
        $this->hosting();
        $server = $this->dns[0];
        $server->update(['agent_token_hash' => hash('sha256', "cfa_{$server->id}"), 'local' => true]);
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $report = fn (int $version) => $this->withToken("cfa_{$server->id}")->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'dns' => true, 'dnsZones' => [], 'dnsSecondaries' => $version])->assertOk();
        $jobs = fn () => AgentJob::where('action', AgentAction::SetDnsSecondaries->value);

        $this->finish($this->postJson('/api/v1/dns-secondaries', ['name' => 'a', 'addresses' => ['192.0.2.53']])->assertAccepted()->json('jobs.*.id'));
        // No secondary copies the catalog zone: the name has no effect.
        config(['cloudfactory.dns_catalog_zone' => 'catalog.panel-a.example']);
        $report(1);
        $this->assertSame(1, Secondaries::version());
        $this->assertSame('catalog.panel-a.example', Setting::get(Secondaries::CATALOG));

        $secondary = DnsSecondary::sole();
        $this->finish($this->patchJson("/api/v1/dns-secondaries/{$secondary->id}", ['catalog' => true])->assertAccepted()->json('jobs.*.id'));
        $this->assertSame(2, Secondaries::version());
        $this->assertSame(4, $jobs()->count());
        $report(2);
        $this->assertSame(4, $jobs()->count());

        // Each DNS server gets the new name.
        config(['cloudfactory.dns_catalog_zone' => 'catalog.panel-b.example']);
        $report(2);
        $this->assertSame(3, Secondaries::version());
        $this->assertSame(6, $jobs()->count());
        $this->assertSame('catalog.panel-b.example', $this->sent($jobs()->latest('id')->first())['catalogZone']);
        $report(2);
        $this->assertSame(6, $jobs()->count());
    }

    public function test_the_owner_of_a_zone_sends_it_to_its_own_secondaries(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $this->actingWith($this->user(Role::Owner, $this->customer));

        $response = $this->putJson("/api/v1/dns-zones/{$zone->id}/transfers", [
            'addresses' => ['198.51.100.53', '2001:db8:0::53'],
            'nameServers' => ['ns.backup-dns.example'],
        ])->assertAccepted()
            ->assertJsonPath('zone.transfers.addresses', ['198.51.100.53', '2001:db8::53'])
            ->assertJsonPath('zone.transfers.notify', true)
            ->assertJsonPath('zone.transfers.tsig', ['name' => 'transfer.example.com', 'algorithm' => 'hmac-sha256'])
            ->assertJsonPath('zone.transfers.nameServers', ['ns.backup-dns.example'])
            ->assertJsonPath('zone.transfers.primaries', ['192.0.2.1', '2001:db8::1', '192.0.2.2'])
            ->assertJsonPath('zone.nameServers', [...self::NS, 'ns.backup-dns.example'])
            ->assertJsonCount(2, 'jobs');
        $zone->refresh();
        $this->assertStringNotContainsString($zone->transfer_tsig_secret, $response->getContent());
        $this->assertContains('ns.backup-dns.example', $zone->records()->where('type', 'NS')->pluck('value')->all());

        // The job has the transfers with no secret. The panel adds the secret when it sends the job.
        $job = AgentJob::find($response->json('jobs.0.id'));
        $this->assertSame(['addresses' => ['198.51.100.53', '2001:db8::53'], 'tsig' => ['name' => 'transfer.example.com', 'algorithm' => 'hmac-sha256', 'secret' => ''], 'notify' => true], $job->payload['zones'][0]['transfers']);
        $this->assertSame($zone->transfer_tsig_secret, Zones::keysFor($job)['zones'][0]['transfers']['tsig']['secret']);
        $this->getJson("/api/v1/dns-zones/{$zone->id}/transfers/secret")->assertOk()
            ->assertExactJson(['name' => 'transfer.example.com', 'algorithm' => 'hmac-sha256', 'secret' => $zone->transfer_tsig_secret]);

        // The addresses must be public.
        foreach (['127.0.0.1', '10.1.2.3', '100.64.0.1', '169.254.169.254', 'fd00::1', 'fe80::1', '2002:a00:1::1', '0.0.0.0'] as $address) {
            $this->putJson("/api/v1/dns-zones/{$zone->id}/transfers", ['addresses' => [$address]])->assertUnprocessable()->assertJsonValidationErrors('addresses');
        }

        // A job that waited in the queue gets the current state: no transfers after "stop".
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/transfers")->assertAccepted()->assertJsonPath('zone.transfers.addresses', [])->assertJsonCount(2, 'jobs');
        $this->assertNull(Zones::keysFor($job)['zones'][0]['transfers']);
        $this->assertSame(self::NS, $zone->records()->where('type', 'NS')->orderBy('value')->pluck('value')->all());
        $this->getJson("/api/v1/dns-zones/{$zone->id}/transfers/secret")->assertNotFound();
        // With no transfers, "stop" makes no job.
        $this->deleteJson("/api/v1/dns-zones/{$zone->id}/transfers")->assertAccepted()->assertJsonCount(0, 'jobs');
    }

    public function test_a_different_customer_cannot_see_the_transfers_of_a_zone(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $this->putJson("/api/v1/dns-zones/{$zone->id}/transfers", ['addresses' => ['198.51.100.53'], 'tsig' => false])->assertAccepted()->assertJsonPath('zone.transfers.tsig', null);
        $this->actingWith($this->user(Role::Owner, $this->customer(attributes: ['name' => 'Other'])));
        $this->getJson("/api/v1/dns-zones/{$zone->id}/transfers/secret")->assertNotFound();
        $this->putJson("/api/v1/dns-zones/{$zone->id}/transfers", ['addresses' => ['198.51.100.54']])->assertNotFound();
    }

    public function test_the_name_servers_of_a_secondary_do_not_prove_the_delegation_alone(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $this->postJson('/api/v1/dns-secondaries', ['name' => 'a', 'addresses' => ['192.0.2.53'], 'nameServer' => 'ns3.secondary.example'])->assertAccepted();

        // The registrar has the name servers of the panel and of the secondary.
        $this->nameServersSeen['example.com'] = [self::NS[0], 'ns3.secondary.example'];
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk()->assertJsonPath('delegation.state', 'here');
        // Only the secondary: a secondary DNS service also answers for the zones of other people.
        $this->nameServersSeen['example.com'] = ['ns3.secondary.example'];
        $this->postJson("/api/v1/dns-zones/{$zone->id}/check")->assertOk()->assertJsonPath('delegation.state', 'elsewhere');
    }

    public function test_no_zone_has_the_name_of_the_catalog_zone(): void
    {
        $this->hosting();
        $this->postJson('/api/v1/dns-zones', ['name' => 'catalog.cloudfactory.invalid', 'customerId' => $this->customer->id])->assertUnprocessable()->assertJsonValidationErrors('name');
        $this->postJson('/api/v1/dns-zones', ['name' => 'x.catalog.cloudfactory.invalid', 'customerId' => $this->customer->id])->assertUnprocessable()->assertJsonValidationErrors('name');
    }

    public function test_the_steps_of_the_job(): void
    {
        $this->assertSame(['Start DNS server', 'Set secondary DNS servers'], AgentAction::SetDnsSecondaries->steps());
    }
}
