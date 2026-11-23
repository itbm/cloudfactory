<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Http\Controllers\Api\AuthController;
use App\Models\Passkey;
use App\Models\User;
use App\Services\Totp;
use Tests\Support\SoftAuthenticator;
use Tests\TestCase;

/** FR-103: passkeys. A user adds a passkey in the settings, and signs in with it. */
class PasskeyTest extends TestCase
{
    private const HOST = 'panel.example.com';

    private const ORIGIN = 'https://panel.example.com';

    protected function setUp(): void
    {
        parent::setUp();
        config(['cloudfactory.panel_domain' => self::HOST]);
    }

    /** A sign-in session of the user, as the web panel has. */
    private function sessionOf(User $user): string
    {
        return $user->createToken(AuthController::SESSION, ['*'], now()->addHours(12))->plainTextToken;
    }

    private function url(string $path): string
    {
        return 'https://'.self::HOST.'/api/v1/'.$path;
    }

    /** Adds a passkey for the user with the password, and gives the authenticator. */
    private function addPasskey(User $user, string $session, array $confirm = ['password' => 'password'], string $name = 'Phone'): SoftAuthenticator
    {
        $device = new SoftAuthenticator;
        $start = $this->withToken($session)->postJson($this->url('me/passkeys/options'), $confirm)->assertOk();
        $this->withToken($session)->postJson($this->url('me/passkeys'), ['ceremony' => $start->json('ceremony'), 'name' => $name, 'credential' => $device->create($start->json('publicKey'), self::ORIGIN)])
            ->assertCreated()->assertJsonPath('name', $name)->assertJsonPath('domain', self::HOST);

        return $device;
    }

    /** Signs in with a passkey, and gives the answer. */
    private function signInWith(SoftAuthenticator $device, string $origin = self::ORIGIN, ?int $flags = null)
    {
        $start = $this->postJson($this->url('auth/passkey/options'))->assertOk();
        $device->signCount++;

        return $this->postJson($this->url('auth/passkey'), ['ceremony' => $start->json('ceremony'), 'credential' => $device->get($start->json('publicKey'), $origin, $flags)]);
    }

    public function test_a_user_adds_a_passkey_and_signs_in_with_it_with_no_password(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $session = $this->sessionOf($user);

        $start = $this->withToken($session)->postJson($this->url('me/passkeys/options'), ['password' => 'password'])->assertOk();
        $options = $start->json('publicKey');
        // A discoverable passkey that verifies the user, with no attestation, for this domain.
        $this->assertSame(['id' => self::HOST, 'name' => 'Cloudfactory'], $options['rp']);
        $this->assertEqualsCanonicalizing(['residentKey' => 'required', 'userVerification' => 'required', 'requireResidentKey' => true], $options['authenticatorSelection']);
        $this->assertSame('none', $options['attestation']);
        $this->assertSame([-7, -8, -257], array_column($options['pubKeyCredParams'], 'alg'));
        $this->assertSame($user->email, $options['user']['name']);
        // The user handle is random: it does not show the ID or the email of the user.
        $this->assertSame(43, strlen($options['user']['id']));

        $device = new SoftAuthenticator;
        $this->withToken($session)->postJson($this->url('me/passkeys'), ['ceremony' => $start->json('ceremony'), 'name' => 'Laptop', 'credential' => $device->create($options, self::ORIGIN)])
            ->assertCreated()->assertJson(['name' => 'Laptop', 'domain' => self::HOST, 'synced' => true, 'lastUsedAt' => null]);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Added passkey', 'target' => "Laptop ({$user->email})"]);
        $this->withToken($session)->getJson($this->url('me/passkeys'))->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.name', 'Laptop');

        $this->app['auth']->forgetGuards();
        $token = $this->signInWith($device)->assertOk()->assertJsonStructure(['token', 'expiresAt'])->json('token');
        $this->assertDatabaseHas('audit_entries', ['action' => 'Signed in with passkey', 'user_id' => $user->id]);
        $this->assertNotNull(Passkey::sole()->last_used_at);
        $this->app['auth']->forgetGuards();
        $this->withToken($token)->getJson($this->url('me'))->assertOk()->assertJsonPath('email', $user->email);
    }

    public function test_a_passkey_skips_the_totp_code_but_adding_one_needs_the_code(): void
    {
        $user = $this->user(Role::Administrator);
        $secret = $this->totp($user);
        $session = $this->sessionOf($user);

        $this->withToken($session)->postJson($this->url('me/passkeys/options'), ['password' => 'wrong'])->assertUnprocessable()->assertJsonValidationErrors('password');
        $this->withToken($session)->postJson($this->url('me/passkeys/options'), ['password' => 'password'])->assertUnprocessable()->assertJsonValidationErrors('totp');
        $device = $this->addPasskey($user, $session, ['password' => 'password', 'totp' => Totp::code($secret)]);

        $this->app['auth']->forgetGuards();
        $this->signInWith($device)->assertOk();
    }

    public function test_an_api_token_can_read_the_passkeys_but_cannot_change_them(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $this->addPasskey($user, $this->sessionOf($user));
        $id = $user->passkeys()->value('id');
        $this->app['auth']->forgetGuards();
        $token = $user->createToken('CI', ['*'])->plainTextToken;

        $this->withToken($token)->getJson($this->url('me/passkeys'))->assertOk()->assertJsonCount(1, 'data');
        $this->withToken($token)->postJson($this->url('me/passkeys/options'), ['password' => 'password'])->assertForbidden();
        $this->withToken($token)->postJson($this->url('me/passkeys'), ['ceremony' => 'x', 'name' => 'x', 'credential' => ['id' => 'x']])->assertForbidden();
        $this->withToken($token)->patchJson($this->url("me/passkeys/{$id}"), ['name' => 'Other'])->assertForbidden();
        $this->withToken($token)->deleteJson($this->url("me/passkeys/{$id}"), ['password' => 'password'])->assertForbidden();
        $this->assertSame(1, $user->passkeys()->count());
    }

    public function test_a_challenge_works_one_time_only(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $device = $this->addPasskey($user, $this->sessionOf($user));
        $this->app['auth']->forgetGuards();

        $start = $this->postJson($this->url('auth/passkey/options'))->assertOk();
        $device->signCount++;
        $credential = $device->get($start->json('publicKey'), self::ORIGIN);
        $this->postJson($this->url('auth/passkey'), ['ceremony' => $start->json('ceremony'), 'credential' => $credential])->assertOk();
        $this->app['auth']->forgetGuards();
        $this->postJson($this->url('auth/passkey'), ['ceremony' => $start->json('ceremony'), 'credential' => $credential])
            ->assertUnprocessable()->assertJsonValidationErrors('ceremony');

        // A challenge stops after 5 minutes.
        $start = $this->postJson($this->url('auth/passkey/options'))->assertOk();
        $this->travel(6)->minutes();
        $device->signCount++;
        $this->postJson($this->url('auth/passkey'), ['ceremony' => $start->json('ceremony'), 'credential' => $device->get($start->json('publicKey'), self::ORIGIN)])
            ->assertUnprocessable()->assertJsonValidationErrors('ceremony');
    }

    public function test_the_panel_refuses_a_different_origin_a_passkey_with_no_user_verification_and_an_unknown_passkey(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $device = $this->addPasskey($user, $this->sessionOf($user));
        $this->app['auth']->forgetGuards();

        // A phishing site cannot use the answer: the origin is different.
        $this->signInWith($device, 'https://panel.example.com.evil.example')->assertUnprocessable()->assertJsonValidationErrors('credential');
        $this->signInWith($device, 'https://sub.panel.example.com')->assertUnprocessable()->assertJsonValidationErrors('credential');
        $this->signInWith($device, 'http://panel.example.com')->assertUnprocessable()->assertJsonValidationErrors('credential');
        // The passkey must verify the user (a fingerprint, a face or a PIN).
        $this->signInWith($device, self::ORIGIN, SoftAuthenticator::USER_PRESENT)->assertUnprocessable()->assertJsonValidationErrors('credential');
        $this->signInWith(new SoftAuthenticator)->assertUnprocessable()->assertJsonValidationErrors(['credential' => 'The panel does not know this passkey. Sign in with your password, or use a different passkey.']);
        $this->assertDatabaseMissing('audit_entries', ['action' => 'Signed in with passkey']);
    }

    public function test_a_wrong_signature_or_a_wrong_user_handle_is_refused(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $device = $this->addPasskey($user, $this->sessionOf($user));
        $this->app['auth']->forgetGuards();

        $start = $this->postJson($this->url('auth/passkey/options'))->assertOk();
        $device->signCount++;
        $credential = $device->get($start->json('publicKey'), self::ORIGIN);
        $credential['response']['signature'] = (new SoftAuthenticator)->get($start->json('publicKey'), self::ORIGIN)['response']['signature'];
        $this->postJson($this->url('auth/passkey'), ['ceremony' => $start->json('ceremony'), 'credential' => $credential])->assertUnprocessable()->assertJsonValidationErrors('credential');

        $device->userHandle = random_bytes(32);
        $this->signInWith($device)->assertUnprocessable()->assertJsonValidationErrors('credential');
    }

    public function test_a_passkey_works_only_on_the_domain_where_the_user_made_it(): void
    {
        $reseller = $this->customer(null, ['role' => 'reseller', 'branding' => ['name' => 'Northwind Hosting', 'panelDomain' => 'panel.northwind.example']]);
        $owner = $this->user(Role::Owner, $this->customer(null, ['reseller_id' => $reseller->id]));
        $session = $this->sessionOf($owner);

        // On the domain of the reseller, the passkey is for that domain, with the brand of the reseller.
        $start = $this->withToken($session)->postJson('https://panel.northwind.example/api/v1/me/passkeys/options', ['password' => 'password'])->assertOk();
        $this->assertSame(['id' => 'panel.northwind.example', 'name' => 'Northwind Hosting'], $start->json('publicKey.rp'));
        $device = new SoftAuthenticator;
        $this->withToken($session)->postJson('https://panel.northwind.example/api/v1/me/passkeys', ['ceremony' => $start->json('ceremony'), 'name' => 'Phone', 'credential' => $device->create($start->json('publicKey'), 'https://panel.northwind.example')])
            ->assertCreated()->assertJsonPath('domain', 'panel.northwind.example');
        $this->app['auth']->forgetGuards();

        // The same passkey on the panel domain: the panel refuses it.
        $start = $this->postJson($this->url('auth/passkey/options'))->assertOk();
        $device->signCount++;
        $this->postJson($this->url('auth/passkey'), ['ceremony' => $start->json('ceremony'), 'credential' => $device->get($start->json('publicKey'), self::ORIGIN, null, 'panel.northwind.example')])
            ->assertUnprocessable()->assertJsonValidationErrors('credential');

        $start = $this->postJson('https://panel.northwind.example/api/v1/auth/passkey/options')->assertOk();
        $device->signCount++;
        $this->postJson('https://panel.northwind.example/api/v1/auth/passkey', ['ceremony' => $start->json('ceremony'), 'credential' => $device->get($start->json('publicKey'), 'https://panel.northwind.example')])
            ->assertOk();

        // A domain that is not a panel domain has no passkeys.
        $this->getJson('https://other.example/api/v1/auth/methods')->assertJsonPath('passkeys', false);
        $this->postJson('https://other.example/api/v1/auth/passkey/options')->assertStatus(409);
        $this->getJson($this->url('auth/methods'))->assertJsonPath('passkeys', true);
    }

    public function test_a_suspended_user_cannot_sign_in_with_a_passkey(): void
    {
        $customer = $this->customer();
        $user = $this->user(Role::Owner, $customer);
        $device = $this->addPasskey($user, $this->sessionOf($user));
        $customer->update(['suspended' => true]);
        $this->app['auth']->forgetGuards();

        $this->signInWith($device)->assertForbidden()->assertJsonPath('message', 'This account is suspended. Contact your provider.');
    }

    public function test_the_user_renames_and_removes_a_passkey_and_a_reset_removes_all(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $other = $this->user(Role::Owner, $this->customer());
        $session = $this->sessionOf($user);
        $device = $this->addPasskey($user, $session);
        $this->addPasskey($user, $session, name: 'Key');
        $this->assertSame(1, Passkey::distinct()->count('user_handle'), 'All passkeys of a user have one user handle.');
        $passkey = Passkey::where('name', 'Phone')->sole();

        $this->app['auth']->forgetGuards();
        $this->withToken($this->sessionOf($other))->patchJson($this->url("me/passkeys/{$passkey->id}"), ['name' => 'Mine'])->assertNotFound();
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->patchJson($this->url("me/passkeys/{$passkey->id}"), ['name' => 'Work phone'])->assertOk()->assertJsonPath('name', 'Work phone');
        $this->withToken($session)->deleteJson($this->url("me/passkeys/{$passkey->id}"))->assertUnprocessable()->assertJsonValidationErrors('password');
        $this->withToken($session)->deleteJson($this->url("me/passkeys/{$passkey->id}"), ['password' => 'password'])->assertNoContent();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Removed passkey', 'target' => "Work phone ({$user->email})"]);
        $this->app['auth']->forgetGuards();
        $this->signInWith($device)->assertUnprocessable()->assertJsonValidationErrors('credential');

        // A reset by an administrator removes the passkeys: only the person with the temporary
        // password can sign in.
        $this->actingWith($this->user());
        $this->postJson("/api/v1/users/{$user->id}/reset-password")->assertOk();
        $this->assertSame(0, $user->passkeys()->count());
    }

    public function test_a_user_has_at_most_20_passkeys(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        foreach (range(1, 20) as $i) {
            Passkey::create(['user_id' => $user->id, 'name' => "Key {$i}", 'rp_id' => self::HOST, 'credential_hash' => hash('sha256', (string) $i), 'user_handle' => 'abc', 'record' => '{}']);
        }
        $this->withToken($this->sessionOf($user))->postJson($this->url('me/passkeys/options'), ['password' => 'password'])
            ->assertUnprocessable()->assertJsonValidationErrors(['passkey' => 'A user can have 20 passkeys. Remove a passkey first.']);
    }

    public function test_an_administrator_must_still_set_up_totp_before_adding_a_passkey(): void
    {
        $admin = $this->user(Role::Administrator);
        $this->withToken($this->sessionOf($admin))->postJson($this->url('me/passkeys/options'), ['password' => 'password'])
            ->assertForbidden()->assertJsonPath('code', 'totp_setup_required');
    }
}
