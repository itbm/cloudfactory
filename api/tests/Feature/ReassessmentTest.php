<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Models\Domain;
use App\Models\User;
use App\Models\Website;
use App\Services\DomainPolicy;
use App\Services\Totp;
use Illuminate\Support\Facades\Hash;
use Tests\TestCase;

/** Regression tests for the findings of the security reassessment of release 0.1 (R-01 to R-17). */
class ReassessmentTest extends TestCase
{
    /** R-01: a user changes the password and turns TOTP on and off, with a sign-in session only. */
    public function test_a_user_can_change_the_password_and_set_up_totp(): void
    {
        $user = $this->user();
        $other = $this->signIn($user);
        $token = $this->signIn($user);
        $auth = fn () => $this->withToken($token);

        $auth()->putJson('/api/v1/me/password', ['currentPassword' => 'wrong', 'password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertJsonValidationErrors('currentPassword');
        $auth()->putJson('/api/v1/me/password', ['currentPassword' => 'password', 'password' => 'short', 'password_confirmation' => 'short'])
            ->assertJsonValidationErrors('password');
        $auth()->putJson('/api/v1/me/password', ['currentPassword' => 'password', 'password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertNoContent();
        $this->assertTrue(Hash::check('a-new-long-password', $user->fresh()->password));
        // The other session is signed out. This session stays.
        $this->app['auth']->forgetGuards();
        $this->withToken($other)->getJson('/api/v1/me')->assertUnauthorized();
        $this->app['auth']->forgetGuards();

        $secret = $auth()->postJson('/api/v1/me/totp/setup')->assertOk()->assertJsonPath('uri', fn ($uri) => str_starts_with($uri, 'otpauth://totp/'))->json('secret');
        $auth()->postJson('/api/v1/me/totp/enable', ['password' => 'a-new-long-password', 'code' => '000000'])->assertJsonValidationErrors('code');
        $auth()->postJson('/api/v1/me/totp/enable', ['password' => 'wrong', 'code' => Totp::code($secret)])->assertJsonValidationErrors('password');
        $auth()->postJson('/api/v1/me/totp/enable', ['password' => 'a-new-long-password', 'code' => Totp::code($secret, time() - 30)])->assertNoContent();
        $this->assertSame($secret, $user->fresh()->totp_secret);
        $auth()->getJson('/api/v1/me')->assertJsonPath('totpEnabled', true);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Set up TOTP', 'user_id' => $user->id]);

        // A code is good one time only, also here.
        $auth()->postJson('/api/v1/me/totp/disable', ['password' => 'a-new-long-password', 'code' => Totp::code($secret, time() - 30)])->assertJsonValidationErrors('code');
        $auth()->postJson('/api/v1/me/totp/disable', ['password' => 'a-new-long-password', 'code' => Totp::code($secret)])->assertNoContent();
        $this->assertNull($user->fresh()->totp_secret);
    }

    /** R-01: an API token cannot change how the user signs in, also with all scopes. */
    public function test_an_api_token_cannot_change_the_sign_in_settings(): void
    {
        $user = $this->user();
        $token = $user->createToken('Terraform', config('cloudfactory.scopes'), now()->addDay())->plainTextToken;

        $this->withToken($token)->putJson('/api/v1/me/password', ['currentPassword' => 'password', 'password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])->assertForbidden();
        $this->withToken($token)->postJson('/api/v1/me/totp/setup')->assertForbidden();
    }

    /** R-02: an owner cannot switch a website to a custom image of a different client. */
    public function test_an_owner_cannot_switch_to_a_custom_image(): void
    {
        $customer = $this->customer();
        $mine = $this->image(['name' => 'Acme app', 'family' => 'custom', 'php_version' => null, 'reference' => 'registry.example.net/acme/app', 'tag' => '1.0', 'custom' => true]);
        $theirs = $this->image(['name' => 'Client B app', 'family' => 'custom', 'php_version' => null, 'reference' => 'registry.example.net/clientb/app', 'tag' => '1.0', 'custom' => true]);
        $website = $this->website($customer, $this->server(), $mine);
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['imageId' => $theirs->id])->assertJsonValidationErrors(['imageId' => 'Select an official image.']);
        $this->assertSame($mine->id, $website->fresh()->image_id);

        // The owner can go back to the previous image, which the administrator selected.
        $later = $this->image(['name' => 'Acme app 2', 'family' => 'custom', 'php_version' => null, 'reference' => 'registry.example.net/acme/app2', 'tag' => '2.0', 'custom' => true]);
        $website->update(['image_id' => $later->id, 'previous_image_id' => $mine->id]);
        $this->postJson("/api/v1/websites/{$website->id}/runtime/revert")->assertJsonMissingValidationErrors('imageId');
    }

    /** R-03: a reseller proves control of its panel domain, and the brand blocks only that name. */
    public function test_a_brand_domain_needs_proof_and_does_not_block_its_zone(): void
    {
        $txt = [];
        $policy = $this->fakeDns($txt);
        $reseller = $this->customer(attributes: ['role' => 'reseller']);
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => 'co.uk']])
            ->assertJsonValidationErrors(['branding.panelDomain' => "Prove that you control this domain. Add a TXT record _cloudfactory-challenge.co.uk with the value {$policy->claimToken($reseller->id, 'co.uk')}, and then save again. DNS changes can take some minutes."]);
        $this->assertNull($reseller->fresh()->branding);

        $txt['_cloudfactory-challenge.panel.northwind.example'] = [$policy->claimToken($reseller->id, 'panel.northwind.example')];
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => 'panel.northwind.example']])->assertOk();

        // A different customer can use a different name in the same zone, but not the panel domain.
        $owner = $this->customer();
        $website = $this->website($owner, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $owner));
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'shop.northwind.example', 'kind' => 'alias'])->assertAccepted();
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'panel.northwind.example', 'kind' => 'alias'])
            ->assertJsonValidationErrors(['name' => 'This domain is reserved for the panel or a server.']);
    }

    /** R-04: the real owner of a domain takes it back from an unverified website of a different customer. */
    public function test_the_real_owner_takes_a_primary_domain_from_an_unverified_website(): void
    {
        $txt = [];
        $policy = $this->fakeDns($txt);
        $squatter = $this->customer();
        $owner = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $squat = $this->website($squatter, $server, $image, 'bakery-second-shop.example');
        $squat->domains()->update(['verified_at' => null]);
        $this->actingWith($this->user(Role::Owner, $owner));

        $this->postJson('/api/v1/websites', ['name' => 'bakery-second-shop.example', 'imageId' => $image->id])
            ->assertJsonValidationErrors('name');
        $this->assertSame('bakery-second-shop.example', $squat->fresh()->name);

        $txt['_cloudfactory-challenge.bakery-second-shop.example'] = [$policy->claimToken($owner->id, 'bakery-second-shop.example')];
        $this->postJson('/api/v1/websites', ['name' => 'bakery-second-shop.example', 'imageId' => $image->id])
            ->assertAccepted()->assertJsonPath('website.domains.0.verified', true);
        $this->assertSame('parked-'.strtolower($squat->id).'.invalid', $squat->fresh()->name);
        $this->assertSame($owner->id, Domain::where('name', 'bakery-second-shop.example')->sole()->website->customer_id);

        // The squatter cannot take it back: the domain is verified now.
        $this->actingWith($this->user(Role::Owner, $squatter));
        $this->postJson('/api/v1/websites', ['name' => 'bakery-second-shop.example', 'imageId' => $image->id])
            ->assertJsonValidationErrors(['name' => 'This domain is in use.']);
    }

    /** R-06: a reseller cannot lift a suspension that an administrator set. */
    public function test_a_reseller_cannot_lift_the_suspension_of_an_administrator(): void
    {
        $reseller = $this->customer(attributes: ['role' => 'reseller']);
        $customer = $this->customer(attributes: ['reseller_id' => $reseller->id]);
        $resellerUser = $this->user(Role::Reseller, $reseller);

        $this->actingWith($this->user());
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => true])->assertOk()->assertJsonPath('suspendedBy', 'administrator');
        $this->actingWith($resellerUser);
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertForbidden();
        $this->assertTrue($customer->fresh()->suspended);

        // The reseller can lift its own suspension.
        $this->actingWith($this->user());
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertOk()->assertJsonPath('suspendedBy', null);
        $this->actingWith($resellerUser);
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => true])->assertOk()->assertJsonPath('suspendedBy', 'reseller');
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertOk()->assertJsonPath('suspended', false);
    }

    /** R-07: a reseller gives only the plans that the administrator set, within its own quota. */
    public function test_a_reseller_gives_only_its_plans_and_shares_its_quota(): void
    {
        $small = $this->plan(['name' => 'Reseller S', 'websites' => 2]);
        $starter = $this->plan(['name' => 'Starter', 'websites' => 5]);
        $agency = $this->plan(['name' => 'Agency', 'websites' => 50]);
        $reseller = $this->customer($small, ['role' => 'reseller']);
        $customer = $this->customer($small, ['reseller_id' => $reseller->id]);
        $resellerUser = $this->user(Role::Reseller, $reseller);
        $this->actingWith($resellerUser);

        $this->patchJson("/api/v1/customers/{$customer->id}", ['planId' => $agency->id])->assertJsonValidationErrors('planId');
        $this->postJson('/api/v1/customers', ['name' => 'Big', 'email' => 'big@example.com', 'role' => 'owner', 'planId' => $agency->id])->assertJsonValidationErrors('planId');
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['allowedPlanIds' => [$agency->id]])->assertForbidden();

        $this->actingWith($this->user());
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['allowedPlanIds' => [$starter->id]])->assertOk()
            ->assertJsonPath('allowedPlanIds', [$small->id, $starter->id]);
        $this->actingWith($resellerUser->fresh());
        $this->patchJson("/api/v1/customers/{$customer->id}", ['planId' => $starter->id])->assertOk();

        // The websites of all customers of the reseller count against the plan of the reseller.
        $server = $this->server();
        $image = $this->image();
        $this->website($reseller, $server, $image, 'one.example');
        $this->website($customer, $server, $image, 'two.example');
        $this->postJson('/api/v1/websites', ['name' => 'three.example', 'imageId' => $image->id, 'customerId' => $customer->id])
            ->assertJsonValidationErrors(['name' => 'The Reseller S plan of your provider allows 2 websites in total.']);
    }

    /** R-14: the join command uses the installation script from GitHub Releases. */
    public function test_the_join_command_uses_github_releases(): void
    {
        $this->actingWith($this->user());

        $this->postJson('/api/v1/servers/join-tokens')->assertCreated()
            ->assertJsonPath('command', fn ($c) => str_starts_with($c, 'curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh -s -- --join cfj_'));
    }

    private function signIn(User $user): string
    {
        return $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertOk()->json('token');
    }

    /** A DNS that answers TXT queries from the array. */
    private function fakeDns(array &$txt): DomainPolicy
    {
        $policy = new class($txt) extends DomainPolicy
        {
            public function __construct(public array &$txt) {}

            protected function txtRecords(string $name): array
            {
                return $this->txt[$name] ?? [];
            }
        };
        $this->app->instance(DomainPolicy::class, $policy);

        return $policy;
    }
}
