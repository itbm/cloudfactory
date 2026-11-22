<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\AuditEntry;
use App\Models\Customer;
use App\Models\DnsZone;
use App\Services\Dns\ZoneFile;
use App\Services\JobOutcome;
use Tests\TestCase;

/**
 * The import and the export of zone files. A customer moves a domain from a different DNS
 * provider or hosting panel: the export of that provider goes into the zone, and the panel
 * checks each record as it checks a new record.
 */
class DnsImportTest extends TestCase
{
    /** A zone file as cPanel and BIND write it, with most of the forms of RFC 1035. */
    private const ZONE_FILE = <<<'ZONE'
        ; cPanel first:11.118.0.12 (update_time):1700000000
        $TTL 14400
        $ORIGIN example.com.
        @       86400   IN      SOA     ns1.oldhost.example. hostmaster.example.com. (
                                        2026111501      ; serial
                                        3600            ; refresh
                                        1800 1209600 86400 )
        @       86400   IN      NS      ns1.oldhost.example.
        @       86400   IN      NS      ns2.oldhost.example.
        @               IN      A       192.0.2.10
                        IN      AAAA    2001:DB8::10
        www     300     IN      CNAME   example.com.
        mail    IN      14400   A       192.0.2.20
        @       3600    IN      MX      10 mail
        @       3600    IN      MX      20 mx2.backup.example.
        @       IN      TXT     "v=spf1 +a +mx " "include:spf.example.net -all"
        _dmarc  1h      IN      TXT     "v=DMARC1; p=none; rua=mailto:dmarc@example.com"
        quote   IN      TXT     "say \"hello\"\059 twice"
        _sip._tcp IN    SRV     10 5 5060 sip.example.com.
        @       IN      CAA     0 issue "letsencrypt.org"
        *.dev   IN      A       192.0.2.30
        shop.example.com. 30 IN A 192.0.2.40
        dev     IN      NS      ns1.dev-provider.example.
        10      IN      PTR     host.example.com.
        version CH      TXT     "1.0"
        other.example.net. IN A 192.0.2.50
        $INCLUDE /etc/bind/extra.zone
        @       IN      A       192.0.2.10
        broken  IN      A       300.1.2.3
        bad     IN      TXT     "no end
        ZONE;

    private Customer $customer;

    private DnsZone $zone;

    protected function setUp(): void
    {
        parent::setUp();
        $this->customer = $this->customer();
        $this->dnsServer('ns1.hosting.example');
        $this->dnsServer('ns2.hosting.example');
        $this->actingWith($this->user());
        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])->assertAccepted();
        $this->finish($response->json('jobs.*.id'));
        $this->zone = DnsZone::findOrFail($response->json('zone.id'));
    }

    private function finish(array $jobs): void
    {
        foreach ($jobs as $id) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($id), JobState::Succeeded);
        }
    }

    /** The records of the zone as "name type value", with the priority before the value. */
    private function records(): array
    {
        return $this->zone->records()->whereNull('managed')->orderBy('name')->orderBy('type')->orderBy('value')->get()
            ->map(fn ($r) => trim("{$r->name} {$r->type} {$r->priority} {$r->value}"))->all();
    }

    public function test_the_parser_reads_the_forms_of_a_zone_file(): void
    {
        $parsed = ZoneFile::parse('example.com', self::ZONE_FILE);

        $this->assertSame([
            [10, '@', 'A', 14400, '192.0.2.10', null],
            [11, '@', 'AAAA', 14400, '2001:db8::10', null],
            [12, 'www', 'CNAME', 300, 'example.com', null],
            [13, 'mail', 'A', 14400, '192.0.2.20', null],
            [14, '@', 'MX', 3600, 'mail.example.com', 10],
            [15, '@', 'MX', 3600, 'mx2.backup.example', 20],
            [16, '@', 'TXT', 14400, 'v=spf1 +a +mx include:spf.example.net -all', null],
            [17, '_dmarc', 'TXT', 3600, 'v=DMARC1; p=none; rua=mailto:dmarc@example.com', null],
            [18, 'quote', 'TXT', 14400, 'say "hello"; twice', null],
            [19, '_sip._tcp', 'SRV', 14400, '5 5060 sip.example.com', 10],
            [20, '@', 'CAA', 14400, '0 issue "letsencrypt.org"', null],
            [21, '*.dev', 'A', 14400, '192.0.2.30', null],
            [22, 'shop', 'A', 60, '192.0.2.40', null],
            [23, 'dev', 'NS', 14400, 'ns1.dev-provider.example', null],
        ], array_map(fn ($r) => [$r['line'], $r['name'], $r['type'], $r['ttl'], $r['value'], $r['priority']], $parsed['records']));
        $this->assertSame('The TTL was 30 seconds. The panel uses 60 seconds.', $parsed['records'][12]['note']);
        $this->assertSame('shop.example.com. 30 IN A 192.0.2.40', $parsed['records'][12]['text']);

        $this->assertSame([
            [4, 'soa'], [8, 'panel'], [9, 'panel'], [24, 'type'], [25, 'class'], [26, 'outside'],
            [27, 'directive'], [28, 'duplicate'], [29, 'invalid'], [30, 'syntax'],
        ], array_map(fn ($s) => [$s['line'], $s['code']], $parsed['skipped']));
        $reasons = array_column($parsed['skipped'], 'reason', 'line');
        $this->assertSame('The panel makes the SOA record of the zone.', $reasons[4]);
        $this->assertStringStartsWith('The panel does not have records of the type PTR.', $reasons[24]);
        $this->assertSame('The name other.example.net is not in the zone example.com.', $reasons[26]);
        $this->assertSame('The same record is on line 10.', $reasons[28]);
        $this->assertSame('Enter an IPv4 address, for example 203.0.113.10.', $reasons[29]);
        // The SOA record has brackets over three lines: the result gives the full record.
        $this->assertStringContainsString('2026111501 ; serial 3600 ; refresh 1800 1209600 86400 )', $parsed['skipped'][0]['text']);
    }

    public function test_the_parser_works_with_a_zone_that_the_panel_does_not_have(): void
    {
        // The import of a different hosting panel shows a preview before it makes the zone.
        $parsed = ZoneFile::parse('new.example.', "www 3600 IN A 192.0.2.1\nnew.example. IN MX 0 .\n");

        $this->assertSame([['www', 'A', '192.0.2.1']], array_map(fn ($r) => [$r['name'], $r['type'], $r['value']], $parsed['records']));
        $this->assertSame([[2, 'invalid', 'Enter a value.']], array_map(fn ($s) => [$s['line'], $s['code'], $s['reason']], $parsed['skipped']));
        $this->assertSame(0, DnsZone::where('name', 'new.example')->count());
    }

    public function test_a_preview_changes_nothing_and_an_import_adds_the_records(): void
    {
        $before = $this->zone->serial;
        $jobs = AgentJob::count();

        $preview = $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => self::ZONE_FILE, 'dryRun' => true])
            ->assertOk()
            ->assertJsonPath('dryRun', true)
            ->assertJsonCount(14, 'added')
            ->assertJsonPath('existing', 0)
            ->assertJsonCount(10, 'skipped')
            ->assertJsonPath('jobs', []);
        $this->assertSame('The TTL was 30 seconds. The panel uses 60 seconds.', $preview->json('added.12.note'));
        $this->assertSame([], $this->records());
        $this->assertSame($before, $this->zone->fresh()->serial);
        $this->assertSame($jobs, AgentJob::count());

        $import = $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => self::ZONE_FILE])
            ->assertAccepted()
            ->assertJsonPath('dryRun', false)
            ->assertJsonCount(14, 'added')
            ->assertJsonCount(2, 'jobs')
            ->assertJsonPath('zone.recordCount', 16);
        $this->assertContains('@ MX 10 mail.example.com', $this->records());
        $this->assertContains('_sip._tcp SRV 10 5 5060 sip.example.com', $this->records());
        $this->assertGreaterThan($before, $this->zone->fresh()->serial);
        $this->assertSame(2, AgentJob::where('action', AgentAction::SetDnsZones->value)->where('title', 'Import 14 DNS records to example.com')->count());
        $this->assertSame('example.com (14 records)', AuditEntry::where('action', 'Imported DNS records')->sole()->target);
        $this->finish($import->json('jobs.*.id'));

        // The same file again: the zone has each record, so the import adds nothing.
        $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => self::ZONE_FILE])
            ->assertOk()->assertJsonCount(0, 'added')->assertJsonPath('existing', 14)->assertJsonPath('jobs', []);
    }

    public function test_an_import_obeys_the_rules_of_the_zone(): void
    {
        $this->finish($this->postJson("/api/v1/dns-zones/{$this->zone->id}/records", ['type' => 'CNAME', 'name' => 'blog', 'value' => 'blogs.example.net'])->assertAccepted()->json('jobs.*.id'));
        // A website on this panel has the name: the panel made its address records.
        $website = $this->website($this->customer, $this->server(['addresses' => ['203.0.113.7']]), $this->image(), 'shop.example.com');
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'www.example.com', 'kind' => 'alias'])->assertAccepted();

        $response = $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => implode("\n", [
            'blog IN A 192.0.2.1',
            'www IN A 192.0.2.2',
            'www IN TXT "the website keeps its address"',
            'ftp IN CNAME files.example.net.',
            'ftp IN A 192.0.2.3',
        ])])->assertAccepted();

        $this->assertSame([['www', 'TXT'], ['ftp', 'CNAME']], array_map(fn ($r) => [$r['name'], $r['type']], $response->json('added')));
        $this->assertSame([[1, 'invalid'], [2, 'website'], [5, 'invalid']], array_map(fn ($s) => [$s['line'], $s['code']], $response->json('skipped')));
        $this->assertSame('This name has a CNAME record. A name with a CNAME record cannot have other records.', $response->json('skipped.0.reason'));
        $this->assertSame(['203.0.113.7'], $this->zone->records()->where(['name' => 'www', 'type' => 'A'])->pluck('value')->all());
    }

    public function test_the_export_is_a_zone_file_that_the_panel_reads_again(): void
    {
        $this->finish($this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => self::ZONE_FILE])->assertAccepted()->json('jobs.*.id'));
        $long = str_repeat('k', 300);
        $this->finish($this->postJson("/api/v1/dns-zones/{$this->zone->id}/records", ['type' => 'TXT', 'name' => 'dkim._domainkey', 'value' => "v=DKIM1; p={$long}"])->assertAccepted()->json('jobs.*.id'));

        $file = $this->getJson("/api/v1/dns-zones/{$this->zone->id}/export")->assertOk()->assertJsonPath('name', 'example.com')->json('zoneFile');

        $this->assertStringContainsString("\$ORIGIN example.com.\n", $file);
        $this->assertMatchesRegularExpression('/^@ +3600 +IN SOA +ns1\.hosting\.example\. hostmaster\.example\.com\. '.$this->zone->fresh()->serial.' 3600 900 1209600 300$/m', $file);
        $this->assertMatchesRegularExpression('/^@ +3600 +IN MX +10 mail\.example\.com\.$/m', $file);
        $this->assertMatchesRegularExpression('/^quote +14400 +IN TXT +"say \\\\"hello\\\\"; twice"$/m', $file);
        $this->assertStringContainsString('"v=DKIM1; p='.str_repeat('k', 244).'" "'.str_repeat('k', 56).'"', $file);

        // The records of the export are the records of the zone: the panel reads them again.
        $parsed = ZoneFile::parse('example.com', $file);
        $this->assertSame(['soa', 'panel', 'panel'], array_column($parsed['skipped'], 'code'));
        $mine = $this->zone->records()->where('managed', null)->get()->map(fn ($r) => [$r->name, $r->type, $r->ttl, $r->value, $r->priority])->sort()->values()->all();
        $this->assertSame($mine, collect($parsed['records'])->map(fn ($r) => [$r['name'], $r['type'], $r['ttl'], $r['value'], $r['priority']])->sort()->values()->all());
    }

    public function test_only_a_user_who_controls_the_zone_can_import_or_export_it(): void
    {
        $other = $this->customer(null, ['name' => 'Other Ltd', 'email' => 'it@other.example']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => 'www IN A 192.0.2.1'])->assertNotFound();
        $this->getJson("/api/v1/dns-zones/{$this->zone->id}/export")->assertNotFound();

        $this->actingWith($this->user(Role::Owner, $this->customer), ['dns:read']);
        $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => 'www IN A 192.0.2.1'])->assertForbidden();
        $this->getJson("/api/v1/dns-zones/{$this->zone->id}/export")->assertOk();

        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => str_repeat('x', ZoneFile::MAX_BYTES + 1)])
            ->assertUnprocessable()->assertJsonValidationErrors('zoneFile');
        $this->postJson("/api/v1/dns-zones/{$this->zone->id}/import", ['zoneFile' => 'www IN A 192.0.2.1', 'dryRun' => true])->assertOk()->assertJsonCount(1, 'added');
    }
}
