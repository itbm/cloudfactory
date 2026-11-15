<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\DnsZone;
use App\Services\Branding;
use App\Services\Dns\Zones;
use App\Services\JobOutcome;
use App\Services\Notifications\Senders;
use Tests\TestCase;

/**
 * White label. The administrator sets the default brand. A reseller sets its own brand, its
 * own panel domain and its own name server host names. The customers of a reseller see only
 * the brand of the reseller.
 */
class BrandingTest extends TestCase
{
    /** A PNG image of 2 by 2 pixels. */
    private const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFklEQVR4nGPkL8tjYGBgYmBgYGBgAAALLwD3EqPsIgAAAABJRU5ErkJggg==';

    private const NS = ['ns1.hosting.example', 'ns2.hosting.example'];

    /** The start of a PNG file with the size in its header. */
    private static function pngOfSize(int $width, int $height): string
    {
        $ihdr = 'IHDR'.pack('NN', $width, $height)."\x08\x02\x00\x00\x00";

        return 'data:image/png;base64,'.base64_encode("\x89PNG\r\n\x1a\n".pack('N', 13).$ihdr.pack('N', crc32($ihdr)));
    }

    /** A reseller with a customer. */
    private function reseller(array $branding = []): array
    {
        $reseller = $this->customer(attributes: ['role' => 'reseller', 'name' => 'Agency Ltd', 'email' => 'ops@agency.example', 'branding' => $branding ?: null]);
        $owner = $this->customer(attributes: ['reseller_id' => $reseller->id]);

        return [$reseller, $owner];
    }

    public function test_a_new_panel_has_the_built_in_brand(): void
    {
        $this->getJson('/api/v1/brand')->assertOk()
            ->assertExactJson(['name' => 'Cloudfactory', 'colour' => '#1d5fd6', 'logo' => null, 'supportEmail' => null, 'builtIn' => true]);
    }

    public function test_the_administrator_sets_the_default_brand(): void
    {
        $this->actingWith($this->user());
        $this->putJson('/api/v1/brand/default', ['name' => 'Hostco', 'colour' => '#7c2d12', 'logo' => self::PNG, 'supportEmail' => 'help@hostco.example'])
            ->assertOk()->assertJsonPath('name', 'Hostco')->assertJsonPath('logo', self::PNG);
        $this->getJson('/api/v1/brand')->assertJsonPath('name', 'Hostco')->assertJsonPath('builtIn', false)->assertJsonPath('colour', '#7c2d12');
        $this->getJson('/api/v1/me')->assertJsonPath('brand.name', 'Hostco')->assertJsonPath('brand.source', 'panel');

        // A null value removes the field.
        $this->putJson('/api/v1/brand/default', ['logo' => null])->assertOk()->assertJsonPath('logo', null)->assertJsonPath('name', 'Hostco');

        [$reseller] = $this->reseller();
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->putJson('/api/v1/brand/default', ['name' => 'Mine'])->assertForbidden();
    }

    /** White text on the colour of the buttons must be easy to read (WCAG 2.2 AA). */
    public function test_a_colour_must_have_enough_contrast_with_white(): void
    {
        $this->actingWith($this->user());
        $this->putJson('/api/v1/brand/default', ['colour' => '#ffd700'])->assertJsonValidationErrors(['colour' => 'Select a darker colour.']);
        $this->putJson('/api/v1/brand/default', ['colour' => 'red'])->assertJsonValidationErrors('colour');
        $this->putJson('/api/v1/brand/default', ['colour' => '#0f766e'])->assertOk();
    }

    /** The panel checks the real type and size of a logo. An SVG file can hold a script. */
    public function test_a_logo_must_be_a_small_png_jpeg_or_webp_image(): void
    {
        $this->actingWith($this->user());
        $svg = 'data:image/svg+xml;base64,'.base64_encode('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>');
        $this->putJson('/api/v1/brand/default', ['logo' => $svg])->assertJsonValidationErrors(['logo' => 'Upload a PNG, JPEG or WebP image.']);
        // A PNG type with other content.
        $this->putJson('/api/v1/brand/default', ['logo' => 'data:image/png;base64,'.base64_encode('<html>')])->assertJsonValidationErrors('logo');
        $this->putJson('/api/v1/brand/default', ['logo' => 'https://example.com/logo.png'])->assertJsonValidationErrors('logo');
        $this->putJson('/api/v1/brand/default', ['logo' => self::pngOfSize(3000, 10)])->assertJsonValidationErrors(['logo' => 'Upload an image of 2048 by 2048 pixels or less.']);
        // A JPEG type with PNG content is kept as PNG.
        $this->putJson('/api/v1/brand/default', ['logo' => str_replace('image/png', 'image/jpeg', self::PNG)])->assertOk()->assertJsonPath('logo', self::PNG);
    }

    /**
     * The customers of a reseller see only the brand of the reseller. A field that the reseller
     * did not set comes from the reseller or the built-in values, never from the default brand.
     */
    public function test_the_customers_of_a_reseller_see_only_its_brand(): void
    {
        $this->actingWith($this->user());
        $this->putJson('/api/v1/brand/default', ['name' => 'Hostco', 'colour' => '#7c2d12', 'logo' => self::PNG, 'supportEmail' => 'help@hostco.example'])->assertOk();
        [$reseller, $owner] = $this->reseller();

        $this->actingWith($this->user(Role::Owner, $owner));
        $this->getJson('/api/v1/me')->assertOk()
            ->assertJsonPath('brand.name', 'Agency Ltd')
            ->assertJsonPath('brand.colour', '#1d5fd6')
            ->assertJsonPath('brand.logo', null)
            ->assertJsonPath('brand.supportEmail', 'ops@agency.example')
            ->assertJsonPath('brand.source', 'reseller')
            ->assertJsonPath('brand.builtIn', false);

        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['name' => 'Agency Hosting', 'colour' => '#0f766e', 'logo' => self::PNG]])->assertOk()
            ->assertJsonPath('branding.name', 'Agency Hosting')->assertJsonPath('branding.logo', self::PNG);
        $this->getJson('/api/v1/me')->assertJsonPath('brand.name', 'Agency Hosting')->assertJsonPath('brand.colour', '#0f766e');

        $this->actingWith($this->user(Role::Owner, $owner));
        $this->getJson('/api/v1/me')->assertJsonPath('brand.name', 'Agency Hosting')->assertJsonPath('brand.logo', self::PNG);

        // An owner cannot change the brand of its reseller.
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['name' => 'Mine']])->assertNotFound();
        // The reseller checks its fields as the administrator does.
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['colour' => '#eeeeee']])->assertJsonValidationErrors('branding.colour');
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['logo' => 'data:image/gif;base64,R0lGODlhAQABAAAAACw=']])->assertJsonValidationErrors('branding.logo');
        // A null value removes a field.
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['logo' => null]])->assertOk()->assertJsonMissingPath('branding.logo');
    }

    /**
     * The sign-in page on the panel domain of a reseller shows the brand of the reseller. The
     * edge proxy on the panel server gets the domain and its certificate.
     */
    public function test_a_reseller_panel_domain_has_its_brand_and_its_certificate(): void
    {
        $txt = [];
        $policy = $this->fakeTxt($txt);
        $panel = $this->server(['local' => true]);
        [$reseller] = $this->reseller(['name' => 'Agency Hosting']);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $txt['_cloudfactory-challenge.panel.agency.example'] = [$policy->claimToken($reseller->id, 'panel.agency.example')];
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => 'panel.agency.example']])->assertOk()
            ->assertJsonPath('branding.panelDomain', 'panel.agency.example')
            ->assertJsonPath('brandStatus.certificate.state', 'pending')
            ->assertJsonPath('brandStatus.certificate.method', 'internal');

        $job = AgentJob::where('action', AgentAction::SetPanelBrands->value)->sole();
        $this->assertSame($panel->id, $job->server_id);
        $this->assertSame(['domains' => ['panel.agency.example']], $job->payload);

        app(JobOutcome::class)->finish($job, JobState::Succeeded, null, ['panelBrands' => [
            // A name that is not public gets its certificate from the panel CA.
            ['domain' => 'panel.agency.example', 'method' => 'internal', 'state' => 'valid', 'expiresAt' => '2027-02-01T00:00:00Z'],
            // A domain that no reseller has is not kept.
            ['domain' => 'panel.other.example', 'method' => 'letsencrypt', 'state' => 'valid'],
        ]]);
        $status = collect($this->getJson('/api/v1/customers')->json('data'))->firstWhere('id', $reseller->id)['brandStatus'];
        $this->assertSame(['valid', '2027-02-01T00:00:00Z'], [$status['certificate']['state'], $status['certificate']['expiresAt']]);

        // The sign-in page of each domain.
        $this->getJson('http://panel.agency.example/api/v1/brand')->assertJsonPath('name', 'Agency Hosting')->assertJsonPath('builtIn', false);
        $this->getJson('http://localhost/api/v1/brand')->assertJsonPath('name', 'Cloudfactory');

        // Without the domain, the agent gets an empty list.
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => null]])->assertOk()->assertJsonPath('brandStatus.certificate', null);
        $this->assertSame(['domains' => []], AgentJob::where('action', AgentAction::SetPanelBrands->value)->latest('id')->first()->payload);
        // A save with the same domain sends no job.
        $count = AgentJob::count();
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['name' => 'Agency']])->assertOk();
        $this->assertSame($count, AgentJob::count());
    }

    /**
     * A reseller gives its own host names to the name servers of the panel. The zones of the
     * reseller and of its customers then have these names, and a delegation to these names is
     * a delegation to the panel.
     */
    public function test_a_reseller_has_its_own_name_server_names(): void
    {
        foreach (self::NS as $host) {
            $this->dnsServer($host);
        }
        [$reseller, $owner] = $this->reseller();
        $other = $this->customer();
        $this->actingWith($this->user());
        $zone = DnsZone::findOrFail($this->postJson('/api/v1/dns-zones', ['name' => 'shop.example', 'customerId' => $owner->id])->assertAccepted()->json('zone.id'));
        $otherZone = DnsZone::findOrFail($this->postJson('/api/v1/dns-zones', ['name' => 'other.example', 'customerId' => $other->id])->json('zone.id'));

        $txt = [];
        $policy = $this->fakeTxt($txt);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['nameServers' => ['ns1.agency.example']]])
            ->assertJsonValidationErrors(['branding.nameServers' => 'Give 2 host names: one for each name server of the panel.']);
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['nameServers' => ['ns1.agency.example', 'ns2.agency.example']]])
            ->assertJsonValidationErrors('branding.nameServers.0');
        foreach (['ns1', 'ns2'] as $ns) {
            $txt["_cloudfactory-challenge.{$ns}.agency.example"] = [$policy->claimToken($reseller->id, "{$ns}.agency.example")];
        }
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['nameServers' => ['ns1.agency.example', 'ns2.agency.example']]])->assertOk()
            ->assertJsonPath('branding.nameServers', ['ns1.agency.example', 'ns2.agency.example'])
            ->assertJsonPath('brandStatus.panelNameServers', self::NS);

        // The zone of the customer has the new NS records. The zone of a different customer does not change.
        $this->assertSame(['ns1.agency.example', 'ns2.agency.example'], $zone->records()->where('type', 'NS')->orderBy('value')->pluck('value')->all());
        $this->assertSame(self::NS, $otherZone->records()->where('type', 'NS')->orderBy('value')->pluck('value')->all());
        $job = AgentJob::where('action', AgentAction::SetDnsZones->value)->where('dns_zone_id', $zone->id)->latest('id')->first();
        $this->assertSame('ns1.agency.example', $job->payload['zones'][0]['records'][0]['value']);

        // The customer sees the names of its reseller, also for each server.
        $this->actingWith($this->user(Role::Owner, $owner));
        $this->getJson('/api/v1/dns-zones')->assertJsonPath('nameServers', ['ns1.agency.example', 'ns2.agency.example'])
            ->assertJsonPath('data.0.nameServers', ['ns1.agency.example', 'ns2.agency.example'])
            ->assertJsonPath('data.0.servers.*.nameServer', ['ns1.agency.example', 'ns2.agency.example']);
        $this->getJson('/api/v1/me')->assertJsonPath('brand.nameServers', ['ns1.agency.example', 'ns2.agency.example']);

        // A registrar that sends the queries to the names of the reseller sends them to the panel.
        $this->nameServersSeen['shop.example'] = ['ns1.agency.example', 'ns2.agency.example'];
        app(Zones::class)->checkDelegation($zone->fresh());
        $this->assertSame(DnsZone::HERE, $zone->fresh()->delegation);

        // A new zone of a customer of the reseller gets the names of the reseller.
        $this->actingWith($this->user());
        $this->postJson('/api/v1/dns-zones', ['name' => 'second.example', 'customerId' => $owner->id])->assertJsonPath('zone.nameServers', ['ns1.agency.example', 'ns2.agency.example']);

        // With a third DNS server, the number does not agree: the customers see the names of the panel again.
        $this->dnsServer('ns3.hosting.example');
        $this->getJson("/api/v1/dns-zones/{$zone->id}")->assertJsonPath('nameServers', [...self::NS, 'ns3.hosting.example']);
    }

    public function test_a_reseller_mail_host_needs_proof(): void
    {
        $txt = [];
        $policy = $this->fakeTxt($txt);
        [$reseller, $owner] = $this->reseller();
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['mailHost' => 'mail.agency.example']])->assertJsonValidationErrors('branding.mailHost');
        $txt['_cloudfactory-challenge.mail.agency.example'] = [$policy->claimToken($reseller->id, 'mail.agency.example')];
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['mailHost' => 'mail.agency.example']])->assertOk();
        $this->actingWith($this->user(Role::Owner, $owner));
        $this->getJson('/api/v1/me')->assertJsonPath('brand.mailHost', 'mail.agency.example');
    }

    public function test_emails_have_the_same_brand_as_the_panel(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        Branding::saveDefaults(['name' => 'Acme Cloud', 'colour' => '#0f766e']);
        $this->assertSame(['name' => 'Acme Cloud', 'colour' => '#0f766e', 'url' => 'https://panel.example.com'], Senders::brand(null));

        // A reseller with no brand of its own: its name and the built-in colour, not the default brand.
        [$reseller] = $this->reseller();
        $this->assertSame(['name' => 'Agency Ltd', 'colour' => Branding::COLOUR, 'url' => 'https://panel.example.com'], Senders::brand($reseller));

        [$reseller] = $this->reseller(['name' => 'Agency Cloud', 'colour' => '#aa3300', 'panelDomain' => 'panel.agency.example']);
        $this->assertSame(['name' => 'Agency Cloud', 'colour' => '#aa3300', 'url' => 'https://panel.agency.example'], Senders::brand($reseller));
    }
}
