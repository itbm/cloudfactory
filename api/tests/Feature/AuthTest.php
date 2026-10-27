<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Models\AuditEntry;
use App\Models\User;
use App\Services\Totp;
use Tests\TestCase;

class AuthTest extends TestCase
{
    public function test_sign_in_needs_the_correct_password(): void
    {
        $user = $this->user();

        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'wrong'])
            ->assertUnprocessable()->assertJsonValidationErrors('email');
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])
            ->assertOk()->assertJsonStructure(['token', 'expiresAt']);
    }

    public function test_sign_in_needs_the_totp_code_when_totp_is_on(): void
    {
        $user = $this->user();
        $secret = Totp::generateSecret();
        $user->forceFill(['totp_secret' => $secret])->save();

        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password', 'totp' => '000000'])
            ->assertUnprocessable()->assertJsonValidationErrors(['email' => 'The email, the password or the code is not correct.']);
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password', 'totp' => Totp::code($secret)])
            ->assertOk();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Signed in with password and TOTP', 'user_id' => $user->id]);
    }

    public function test_a_token_has_scopes_and_an_expiry_date(): void
    {
        $this->actingWith($this->user());

        $this->postJson('/api/v1/tokens', ['name' => 'Terraform', 'scopes' => ['websites:read'], 'expiresInDays' => 0])
            ->assertJsonValidationErrors('expiresInDays');
        $this->postJson('/api/v1/tokens', ['name' => 'Terraform', 'scopes' => ['everything'], 'expiresInDays' => 30])
            ->assertJsonValidationErrors('scopes.0');
        $this->postJson('/api/v1/tokens', ['name' => 'Terraform', 'scopes' => ['websites:read'], 'expiresInDays' => 30, 'password' => 'password'])
            ->assertCreated()->assertJsonPath('scopes', ['websites:read']);
    }

    public function test_a_read_scope_cannot_change_data(): void
    {
        $this->actingWith($this->user(), ['websites:read']);

        $this->getJson('/api/v1/websites')->assertOk();
        $this->postJson('/api/v1/websites', [])->assertForbidden()->assertJsonPath('message', 'The token does not have the websites:write scope.');
        $this->getJson('/api/v1/audit')->assertForbidden();
    }

    public function test_a_write_scope_also_gives_read_access(): void
    {
        $this->actingWith($this->user(), ['websites:write']);

        $this->getJson('/api/v1/websites')->assertOk();
    }

    public function test_an_expired_token_does_not_work(): void
    {
        $user = $this->user();
        $token = $user->createToken('Old', ['*'], now()->subMinute())->plainTextToken;

        $this->withToken($token)->getJson('/api/v1/me')->assertUnauthorized();
    }

    public function test_a_revoked_token_does_not_work(): void
    {
        $user = $this->user();
        $new = $user->createToken('CI', ['*'], now()->addDay());

        $this->withToken($new->plainTextToken)->deleteJson("/api/v1/tokens/{$new->accessToken->id}")->assertNoContent();
        $this->app['auth']->forgetGuards();
        $this->withToken($new->plainTextToken)->getJson('/api/v1/me')->assertUnauthorized();
        $this->assertSame(1, AuditEntry::where('action', 'Revoked API token')->count());
    }

    public function test_only_administrators_see_servers(): void
    {
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));
        $this->getJson('/api/v1/servers')->assertForbidden();

        $this->actingWith($this->user(Role::Administrator));
        $this->getJson('/api/v1/servers')->assertOk();
    }

    public function test_me_shows_the_role(): void
    {
        $user = User::factory()->create(['role' => Role::Reseller]);
        $this->actingWith($user);

        $this->getJson('/api/v1/me')->assertOk()->assertJsonPath('role', 'reseller')->assertJsonPath('totpEnabled', false);
    }
}
