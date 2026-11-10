<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Http\Controllers\Api\AuthController;
use App\Models\AuditEntry;
use App\Models\User;
use App\Services\LoginThrottle;
use App\Services\Totp;
use Tests\TestCase;

/** Audit findings B5, S3 and S8: users, temporary passwords, sign-out and sign-in locks. */
class UserTest extends TestCase
{
    /** A sign-in session, which has the name that the API checks. */
    private function signIn(User $user): string
    {
        return $user->createToken(AuthController::SESSION, ['*'], now()->addHour())->plainTextToken;
    }

    public function test_an_administrator_makes_a_user_for_a_customer(): void
    {
        $customer = $this->customer();
        $this->actingWith($this->user());

        $response = $this->postJson('/api/v1/users', ['name' => 'Ada Jones', 'email' => 'ada@acme.example', 'customerId' => $customer->id])
            ->assertCreated()
            ->assertJsonPath('role', 'owner')->assertJsonPath('customerId', $customer->id)->assertJsonPath('mustChangePassword', true);
        $password = $response->json('temporaryPassword');
        $this->assertSame(20, strlen($password));
        $this->assertDatabaseHas('audit_entries', ['action' => 'Created user', 'target' => 'ada@acme.example', 'customer_id' => $customer->id]);

        // The new user can sign in with the temporary password.
        $this->app['auth']->forgetGuards();
        $this->postJson('/api/v1/auth/login', ['email' => 'ada@acme.example', 'password' => $password])->assertOk();
        // The same email cannot have two users.
        $this->actingWith(User::where('role', Role::Administrator)->first());
        $this->postJson('/api/v1/users', ['name' => 'Ada', 'email' => 'ada@acme.example', 'customerId' => $customer->id])
            ->assertUnprocessable()->assertJsonValidationErrors('email');
        $this->postJson('/api/v1/users', ['name' => 'Ada', 'email' => 'b@acme.example', 'customerId' => '01JBQ3Z5K8N6T4W2X9Y7R5P3M1'])
            ->assertUnprocessable()->assertJsonValidationErrors('customerId');
    }

    public function test_a_user_of_a_reseller_account_is_a_reseller(): void
    {
        $reseller = $this->customer(null, ['role' => 'reseller', 'name' => 'Host Ltd']);
        $this->actingWith($this->user());

        $this->postJson('/api/v1/users', ['name' => 'Rae', 'email' => 'rae@host.example', 'customerId' => $reseller->id])
            ->assertCreated()->assertJsonPath('role', 'reseller');
    }

    public function test_a_user_with_a_temporary_password_must_set_a_new_password(): void
    {
        $customer = $this->customer();
        $this->actingWith($this->user());
        $password = $this->postJson('/api/v1/users', ['name' => 'Ada', 'email' => 'ada@acme.example', 'customerId' => $customer->id])->json('temporaryPassword');
        $this->app['auth']->forgetGuards();

        $token = $this->postJson('/api/v1/auth/login', ['email' => 'ada@acme.example', 'password' => $password])->json('token');
        $this->withToken($token)->getJson('/api/v1/me')->assertOk()->assertJsonPath('mustChangePassword', true);
        $this->withToken($token)->getJson('/api/v1/websites')->assertForbidden()->assertJsonPath('code', 'password_change_required');
        $this->withToken($token)->postJson('/api/v1/tokens', ['name' => 'x', 'scopes' => ['websites:read'], 'expiresInDays' => 1, 'password' => $password])
            ->assertForbidden();

        $this->withToken($token)->putJson('/api/v1/me/password', ['currentPassword' => $password, 'password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertNoContent();
        $this->withToken($token)->getJson('/api/v1/me')->assertJsonPath('mustChangePassword', false);
        $this->withToken($token)->getJson('/api/v1/websites')->assertOk();
    }

    public function test_only_a_sign_in_session_of_an_administrator_makes_an_administrator(): void
    {
        $admin = $this->user();
        $body = ['name' => 'Second', 'email' => 'second@panel.example', 'customerId' => null];

        // An API token cannot, also with the users scope.
        $this->actingWith($admin, ['users:write']);
        $this->postJson('/api/v1/users', $body)->assertForbidden();
        $this->app['auth']->forgetGuards();
        $api = $admin->createToken('CI', ['*'], now()->addDay())->plainTextToken;
        $this->withToken($api)->postJson('/api/v1/users', $body)->assertForbidden();
        $this->app['auth']->forgetGuards();

        $this->withToken($this->signIn($admin))->postJson('/api/v1/users', $body)
            ->assertCreated()->assertJsonPath('role', 'administrator')->assertJsonPath('customerId', null);
    }

    public function test_a_reseller_changes_only_the_users_of_its_own_customers(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['role' => 'reseller', 'name' => 'Host Ltd']);
        $mine = $this->customer($plan, ['name' => 'Mine', 'reseller_id' => $reseller->id]);
        $other = $this->customer($plan, ['name' => 'Other']);
        $otherUser = $this->user(Role::Owner, $other);
        $admin = $this->user();
        $colleague = $this->user(Role::Reseller, $reseller);
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $id = $this->postJson('/api/v1/users', ['name' => 'Mia', 'email' => 'mia@mine.example', 'customerId' => $mine->id])->assertCreated()->json('id');
        $this->postJson('/api/v1/users', ['name' => 'X', 'email' => 'x@other.example', 'customerId' => $other->id])->assertForbidden();
        $this->postJson('/api/v1/users', ['name' => 'X', 'email' => 'x@host.example', 'customerId' => $reseller->id])->assertForbidden();
        $this->postJson('/api/v1/users', ['name' => 'X', 'email' => 'x@panel.example', 'customerId' => null])->assertForbidden();

        // The list has the users of the reseller and of its customers only.
        $emails = collect($this->getJson('/api/v1/users')->assertOk()->json('data'))->pluck('email');
        $this->assertContains('mia@mine.example', $emails);
        $this->assertContains($colleague->email, $emails);
        $this->assertNotContains($otherUser->email, $emails);
        $this->assertNotContains($admin->email, $emails);

        $this->postJson("/api/v1/users/{$otherUser->id}/reset-password")->assertNotFound();
        $this->postJson("/api/v1/users/{$admin->id}/reset-password")->assertNotFound();
        $this->postJson("/api/v1/users/{$colleague->id}/reset-password")->assertForbidden();
        $this->deleteJson("/api/v1/users/{$otherUser->id}")->assertNotFound();
        $this->postJson("/api/v1/users/{$id}/reset-password")->assertOk();
        $this->deleteJson("/api/v1/users/{$id}")->assertNoContent();
        $this->assertDatabaseMissing('users', ['id' => $id]);
    }

    public function test_a_website_owner_cannot_change_users(): void
    {
        $customer = $this->customer();
        $colleague = $this->user(Role::Owner, $customer);
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->getJson('/api/v1/users')->assertOk()->assertJsonCount(2, 'data');
        $this->postJson('/api/v1/users', ['name' => 'X', 'email' => 'x@acme.example', 'customerId' => $customer->id])->assertForbidden();
        $this->postJson("/api/v1/users/{$colleague->id}/reset-password")->assertForbidden();
        $this->deleteJson("/api/v1/users/{$colleague->id}")->assertForbidden();
    }

    public function test_the_users_routes_need_the_users_scope(): void
    {
        $customer = $this->customer();
        $this->actingWith($this->user(), ['customers:write']);

        $this->getJson('/api/v1/users')->assertForbidden();
        $this->postJson('/api/v1/users', ['name' => 'X', 'email' => 'x@acme.example', 'customerId' => $customer->id])
            ->assertForbidden()->assertJsonPath('message', 'The token does not have the users:write scope.');
        // A customer with its first user needs the two scopes.
        $this->postJson('/api/v1/customers', ['name' => 'New', 'email' => 'new@new.example', 'role' => 'owner', 'planId' => $customer->plan_id, 'createUser' => true])
            ->assertForbidden();
        $this->assertDatabaseMissing('customers', ['name' => 'New']);
    }

    public function test_a_password_reset_signs_the_user_out(): void
    {
        $customer = $this->customer();
        $target = $this->user(Role::Owner, $customer);
        $target->forceFill(['totp_secret' => Totp::generateSecret()])->save();
        $old = $this->signIn($target);
        $api = $target->createToken('CI', ['websites:read'], now()->addDay())->plainTextToken;
        $admin = $this->actingWith($this->user());

        $this->postJson("/api/v1/users/{$admin->id}/reset-password")->assertForbidden();
        $password = $this->postJson("/api/v1/users/{$target->id}/reset-password")->assertOk()->assertJsonPath('totpEnabled', true)->json('temporaryPassword');
        $this->assertSame(0, $target->tokens()->count());
        $this->assertTrue($target->fresh()->must_change_password);
        $this->assertNotNull($target->fresh()->totp_secret);

        $this->postJson("/api/v1/users/{$target->id}/reset-password", ['removeTotp' => true])->assertOk()->assertJsonPath('totpEnabled', false);
        $this->assertNull($target->fresh()->totp_secret);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Reset password and removed TOTP', 'target' => $target->email]);

        $this->app['auth']->forgetGuards();
        $this->withToken($old)->getJson('/api/v1/me')->assertUnauthorized();
        $this->app['auth']->forgetGuards();
        $this->withToken($api)->getJson('/api/v1/websites')->assertUnauthorized();
        $this->assertNotSame('', $password);
    }

    public function test_an_administrator_cannot_delete_its_own_user(): void
    {
        $admin = $this->user();
        $second = $this->user();
        $this->withToken($this->signIn($admin))->deleteJson("/api/v1/users/{$admin->id}")->assertForbidden();
        $this->withToken($this->signIn($admin))->deleteJson("/api/v1/users/{$second->id}")->assertNoContent();
        $this->assertDatabaseMissing('users', ['id' => $second->id]);
    }

    public function test_a_new_customer_can_get_its_first_user(): void
    {
        $plan = $this->plan();
        $this->actingWith($this->user());

        $response = $this->postJson('/api/v1/customers', ['name' => 'New Ltd', 'email' => 'it@new.example', 'role' => 'owner', 'planId' => $plan->id, 'createUser' => true])
            ->assertCreated()->assertJsonPath('user.email', 'it@new.example')->assertJsonPath('user.role', 'owner');
        $this->assertSame(20, strlen($response->json('user.temporaryPassword')));
        $this->assertDatabaseHas('users', ['email' => 'it@new.example', 'customer_id' => $response->json('id'), 'must_change_password' => true]);

        // With an email that a user has already, the panel makes no customer.
        $this->postJson('/api/v1/customers', ['name' => 'Second Ltd', 'email' => 'it@new.example', 'role' => 'owner', 'planId' => $plan->id, 'createUser' => true])
            ->assertUnprocessable()->assertJsonValidationErrors('email');
        $this->assertDatabaseMissing('customers', ['name' => 'Second Ltd']);
        // With no createUser, the answer has no user.
        $this->postJson('/api/v1/customers', ['name' => 'Third Ltd', 'email' => 'it@third.example', 'role' => 'owner', 'planId' => $plan->id])
            ->assertCreated()->assertJsonMissingPath('user');
    }

    public function test_the_first_administrator_must_set_a_new_password(): void
    {
        $this->artisan('cloudfactory:install', ['email' => 'admin@panel.example'])->assertSuccessful();

        $this->assertTrue(User::where('email', 'admin@panel.example')->firstOrFail()->must_change_password);
    }

    // Audit finding B5: the way back for an administrator who lost the password.
    public function test_the_reset_command_gives_a_temporary_password(): void
    {
        $admin = $this->user();
        $admin->forceFill(['totp_secret' => Totp::generateSecret()])->save();
        $old = $this->signIn($admin);

        $this->artisan('cloudfactory:reset-password', ['email' => 'nobody@panel.example'])->assertFailed();
        $this->artisan('cloudfactory:reset-password', ['email' => $admin->email])
            ->expectsOutputToContain('Temporary password for '.$admin->email)
            ->expectsOutputToContain('TOTP is still on')
            ->assertSuccessful();
        $this->assertTrue($admin->fresh()->must_change_password);
        $this->assertNotNull($admin->fresh()->totp_secret);
        $this->withToken($old)->getJson('/api/v1/me')->assertUnauthorized();

        $this->artisan('cloudfactory:reset-password', ['email' => $admin->email, '--remove-totp' => true])->assertSuccessful();
        $this->assertNull($admin->fresh()->totp_secret);
        $this->assertSame(2, AuditEntry::where('actor', 'System')->where('target', $admin->email)->count());
    }

    // Audit finding S8.
    public function test_sign_out_deletes_the_session_token(): void
    {
        $user = $this->user();
        $token = $this->signIn($user);
        $other = $this->signIn($user);

        $this->withToken($token)->postJson('/api/v1/auth/logout')->assertNoContent();
        $this->app['auth']->forgetGuards();
        $this->withToken($token)->getJson('/api/v1/me')->assertUnauthorized();
        $this->app['auth']->forgetGuards();
        $this->withToken($other)->getJson('/api/v1/me')->assertOk();
    }

    public function test_a_password_change_deletes_the_api_tokens(): void
    {
        $user = $this->user();
        $session = $this->signIn($user);
        $otherSession = $this->signIn($user);
        $api = $user->createToken('CI', ['websites:read'], now()->addDay())->plainTextToken;

        $this->withToken($session)->putJson('/api/v1/me/password', ['currentPassword' => 'password', 'password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertNoContent();
        foreach ([$api, $otherSession] as $old) {
            $this->app['auth']->forgetGuards();
            $this->withToken($old)->getJson('/api/v1/websites')->assertUnauthorized();
        }
        $this->app['auth']->forgetGuards();
        $this->withToken($session)->getJson('/api/v1/me')->assertOk();
    }

    public function test_a_suspended_account_gets_the_same_answer_for_each_password(): void
    {
        $customer = $this->customer(null, ['suspended' => true]);
        $user = $this->user(Role::Owner, $customer);

        $wrong = $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'wrong'])->assertUnprocessable();
        $correct = $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertUnprocessable();
        $this->assertSame($wrong->json(), $correct->json());

        // With a correct code, the user has shown more than the password, and gets the reason.
        $secret = Totp::generateSecret();
        $user->forceFill(['totp_secret' => $secret])->save();
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password', 'totp' => Totp::code($secret)])
            ->assertForbidden()->assertJsonPath('message', 'This account is suspended. Contact your provider.');
    }

    // Audit finding S3.
    public function test_many_failures_do_not_lock_the_network_that_the_user_signs_in_from(): void
    {
        $user = $this->user();
        // The user signed in from this address before.
        $this->withServerVariables(['REMOTE_ADDR' => '192.0.2.10'])->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertOk();

        // An attack from many addresses.
        for ($i = 0; $i < LoginThrottle::DISTRIBUTED_FAILURES; $i++) {
            LoginThrottle::fail($user->email, '198.51.100.'.($i % 250));
        }
        $this->assertGreaterThan(0, LoginThrottle::lockedFor($user->email, '203.0.113.7'), 'a new address is not locked');
        $this->assertSame(0, LoginThrottle::lockedFor($user->email, '192.0.2.10'), 'the address of the user is locked');

        $this->withServerVariables(['REMOTE_ADDR' => '203.0.113.7'])->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertStatus(429);
        $this->withServerVariables(['REMOTE_ADDR' => '192.0.2.10'])->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertOk();
        // A good sign-in removes the lock for the new addresses also.
        $this->assertSame(0, LoginThrottle::lockedFor($user->email, '203.0.113.7'));
    }

    public function test_an_ipv6_client_is_its_64_bit_network(): void
    {
        $this->assertSame('2001:db8:1:2::/64', LoginThrottle::network('2001:db8:1:2:aaaa:bbbb:cccc:dddd'));
        $this->assertSame('192.0.2.1', LoginThrottle::network('192.0.2.1'));
        $this->assertSame('192.0.2.1', LoginThrottle::network('::ffff:192.0.2.1'));
        $this->assertSame('not-an-address', LoginThrottle::network('not-an-address'));

        // Addresses of one /64 network share one count.
        for ($i = 1; $i <= LoginThrottle::FREE_FAILURES; $i++) {
            LoginThrottle::fail('a@panel.example', "2001:db8:1:2::{$i}");
        }
        $this->assertGreaterThan(0, LoginThrottle::lockedFor('a@panel.example', '2001:db8:1:2::ffff'));
        $this->assertSame(0, LoginThrottle::lockedFor('a@panel.example', '2001:db8:1:3::1'));
    }

    public function test_the_unlock_command_removes_the_lock(): void
    {
        for ($i = 0; $i < LoginThrottle::DISTRIBUTED_FAILURES; $i++) {
            LoginThrottle::fail('a@panel.example', '198.51.100.'.($i % 250));
        }
        for ($i = 0; $i < LoginThrottle::FREE_FAILURES; $i++) {
            LoginThrottle::fail('a@panel.example', '203.0.113.7');
        }
        $this->assertGreaterThan(0, LoginThrottle::lockedFor('a@panel.example', '192.0.2.99'));

        $this->artisan('cloudfactory:unlock', ['email' => 'a@panel.example', '--address' => 'bad'])->assertFailed();
        $this->artisan('cloudfactory:unlock', ['email' => 'a@panel.example'])->assertSuccessful();
        $this->assertSame(0, LoginThrottle::lockedFor('a@panel.example', '192.0.2.99'));
        $this->assertGreaterThan(0, LoginThrottle::lockedFor('a@panel.example', '203.0.113.7'));
        $this->artisan('cloudfactory:unlock', ['email' => 'a@panel.example', '--address' => '203.0.113.7'])->assertSuccessful();
        $this->assertSame(0, LoginThrottle::lockedFor('a@panel.example', '203.0.113.7'));
    }
}
