<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Http\Controllers\Api\AuthController;
use App\Models\AgentJob;
use App\Models\User;
use App\Services\JobOutcome;
use App\Services\Totp;
use Tests\TestCase;

/** The findings of the second full audit (F01, F05, F09 and F10). */
class SecondAuditTest extends TestCase
{
    private const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGtQx2bS8ZJ4Wm1bZ7zO0Zr3wq8yXc2v1q9w8mL3h4kP owner@laptop';

    /** A sign-in session, which has the name that the API checks. */
    private function signIn(User $user): string
    {
        return $user->createToken(AuthController::SESSION, ['*'], now()->addHour())->plainTextToken;
    }

    private function finish(AgentJob $job): void
    {
        app(JobOutcome::class)->finish($job, JobState::Succeeded);
    }

    /**
     * F01: the owner of a website that the panel could not make must not change its files
     * before "Try again", which makes the directories of the website again as root.
     */
    public function test_sftp_does_not_turn_on_for_a_website_that_the_panel_could_not_make(): void
    {
        $website = $this->website($this->customer(), $this->server(), $this->image(), 'acme.example', ['status' => 'failed']);
        $this->actingWith($this->user());
        $url = "/api/v1/websites/{$website->id}/sftp";

        $this->putJson($url, ['enabled' => true, 'authorizedKeys' => [self::KEY]])
            ->assertUnprocessable()->assertJsonValidationErrors('website');
        $this->assertSame(0, AgentJob::count());
        // The owner can still turn SFTP off.
        $this->putJson($url, ['enabled' => false])->assertAccepted();
    }

    /** F05: a suspended customer has no SFTP access. The access comes back with the same keys. */
    public function test_a_suspension_turns_off_sftp_and_its_end_turns_it_on_again(): void
    {
        $customer = $this->customer();
        $server = $this->server();
        $withSftp = $this->website($customer, $server, $this->image(), 'acme.example', ['sftp_enabled' => true, 'sftp_keys' => [self::KEY]]);
        $noSftp = $this->website($customer, $server, $this->image(), 'second.example', ['sftp_keys' => [self::KEY]]);
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => true])->assertOk();
        $off = AgentJob::where('action', AgentAction::SetSftp->value)->sole();
        $this->assertSame($withSftp->id, $off->website_id);
        $this->assertFalse($off->payload['enabled']);
        $this->assertSame([self::KEY], $off->payload['authorizedKeys']);
        $this->finish($off);
        $withSftp->refresh();
        $this->assertFalse($withSftp->sftp_enabled);
        $this->assertTrue($withSftp->sftp_closed_by_suspension);
        $this->assertSame([self::KEY], $withSftp->sftp_keys);

        // Nobody turns SFTP on while the suspension continues.
        $this->putJson("/api/v1/websites/{$withSftp->id}/sftp", ['enabled' => true])
            ->assertStatus(409)->assertJsonPath('message', 'This customer is suspended. Lift the suspension before you turn on SFTP.');

        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertOk();
        $on = AgentJob::where('action', AgentAction::SetSftp->value)->latest('id')->first();
        $this->assertNotSame($off->id, $on->id);
        $this->assertSame([$withSftp->id, true, [self::KEY]], [$on->website_id, $on->payload['enabled'], $on->payload['authorizedKeys']]);
        $this->finish($on);
        $withSftp->refresh();
        $this->assertTrue($withSftp->sftp_enabled);
        $this->assertFalse($withSftp->sftp_closed_by_suspension);
        // The website that had no SFTP gets none.
        $this->assertSame(2, AgentJob::where('action', AgentAction::SetSftp->value)->count());
        $this->assertFalse($noSftp->fresh()->sftp_enabled);
    }

    /** F05: the suspension of a reseller turns off SFTP for the websites of its customers. */
    public function test_the_suspension_of_a_reseller_turns_off_sftp_of_its_customers(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['role' => 'reseller', 'name' => 'Host Ltd']);
        $customer = $this->customer($plan, ['reseller_id' => $reseller->id]);
        $website = $this->website($customer, $this->server(), $this->image(), 'acme.example', ['sftp_enabled' => true, 'sftp_keys' => [self::KEY]]);
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/customers/{$reseller->id}", ['suspended' => true])->assertOk();
        $job = AgentJob::where('action', AgentAction::SetSftp->value)->sole();
        $this->assertSame([$website->id, false], [$job->website_id, $job->payload['enabled']]);
    }

    /** A website that the panel could not make has no container to start when a suspension ends. */
    public function test_the_end_of_a_suspension_does_not_start_a_website_that_the_panel_could_not_make(): void
    {
        $customer = $this->customer(null, ['suspended' => true, 'suspended_by' => 'administrator']);
        $website = $this->website($customer, $this->server(), $this->image(), 'acme.example', ['status' => 'failed', 'stopped_by_suspension' => true, 'sftp_closed_by_suspension' => true, 'sftp_keys' => [self::KEY]]);
        $this->actingWith($this->user());

        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertOk();
        $this->assertSame(0, AgentJob::count());
        $website->refresh();
        $this->assertSame(['failed', false, false], [$website->status->value, $website->stopped_by_suspension, $website->sftp_closed_by_suspension]);
    }

    /** F09: a stolen session of one administrator cannot take the other administrator accounts. */
    public function test_a_change_to_an_administrator_needs_the_password_and_a_code(): void
    {
        $admin = $this->user();
        $other = $this->user();
        $secret = $this->totp($admin);
        $session = $this->signIn($admin);
        $url = "/api/v1/users/{$other->id}/reset-password";

        $this->withToken($session)->postJson($url, ['removeTotp' => true])
            ->assertUnprocessable()->assertJsonValidationErrors(['password' => 'Type your password to confirm this change.']);
        $this->withToken($session)->postJson($url, ['password' => 'wrong', 'totp' => Totp::code($secret)])
            ->assertUnprocessable()->assertJsonValidationErrors(['password' => 'The password is not correct.']);
        $this->withToken($session)->postJson($url, ['password' => 'password'])
            ->assertUnprocessable()->assertJsonValidationErrors(['totp' => 'The code is not correct.']);
        $this->assertFalse((bool) $other->fresh()->must_change_password);

        $this->withToken($session)->postJson($url, ['password' => 'password', 'totp' => Totp::code($secret)])
            ->assertOk()->assertJsonPath('mustChangePassword', true);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Reset password of administrator', 'target' => $other->email]);

        // The other administrator sees the change. The administrator who made it does not.
        $this->withToken($session)->getJson('/api/v1/me')->assertOk()->assertJsonPath('securityNotices', []);
        $this->app['auth']->forgetGuards();
        $this->actingWith($other->fresh(), ['*']);
        $this->getJson('/api/v1/me')->assertOk()
            ->assertJsonPath('securityNotices.0.action', 'Reset password of administrator')
            ->assertJsonPath('securityNotices.0.target', $other->email)
            ->assertJsonPath('securityNotices.0.actor', $admin->name);
    }

    public function test_a_new_administrator_gets_no_notice_about_its_own_account(): void
    {
        $admin = $this->user();
        $other = $this->user();
        $secret = $this->totp($admin);
        $session = $this->signIn($admin);
        $this->withToken($session)->postJson('/api/v1/users', ['name' => 'New', 'email' => 'new-admin@example.com', 'customerId' => null, 'password' => 'password', 'totp' => Totp::code($secret)])
            ->assertCreated();
        $this->app['auth']->forgetGuards();
        $this->actingWith(User::where('email', 'new-admin@example.com')->firstOrFail(), ['*']);
        $this->getJson('/api/v1/me')->assertOk()->assertJsonPath('securityNotices', []);
        // A different administrator sees that the account was made.
        $this->app['auth']->forgetGuards();
        $this->actingWith($other, ['*']);
        $this->getJson('/api/v1/me')->assertOk()
            ->assertJsonPath('securityNotices.0.action', 'Created administrator')
            ->assertJsonPath('securityNotices.0.target', 'new-admin@example.com');
    }

    public function test_wrong_passwords_for_a_change_to_an_administrator_start_a_wait(): void
    {
        $admin = $this->user();
        $other = $this->user();
        $this->totp($admin);
        $session = $this->signIn($admin);
        for ($i = 0; $i < 5; $i++) {
            $this->withToken($session)->postJson("/api/v1/users/{$other->id}/reset-password", ['password' => 'wrong'])->assertUnprocessable();
        }
        $this->withToken($session)->postJson("/api/v1/users/{$other->id}/reset-password", ['password' => 'password'])
            ->assertStatus(429)->assertHeader('Retry-After');
    }

    public function test_a_change_to_a_user_of_a_customer_needs_no_password(): void
    {
        $customer = $this->customer();
        $user = $this->user(Role::Owner, $customer);
        $this->actingWith($this->user());
        $this->postJson("/api/v1/users/{$user->id}/reset-password")->assertOk();
        $this->assertDatabaseHas('audit_entries', ['action' => 'Reset password', 'target' => $user->email]);
    }

    /** F09: administrators and resellers turn on TOTP before they work in a sign-in session. */
    public function test_an_administrator_and_a_reseller_must_set_up_totp(): void
    {
        $reseller = $this->customer(null, ['role' => 'reseller', 'name' => 'Host Ltd']);
        foreach ([$this->user(), $this->user(Role::Reseller, $reseller)] as $user) {
            $session = $this->signIn($user);
            $this->withToken($session)->getJson('/api/v1/websites')
                ->assertForbidden()->assertJsonPath('code', 'totp_setup_required');
            $this->withToken($session)->getJson('/api/v1/me')->assertOk()->assertJsonPath('totpSetupRequired', true);

            // The user can set up TOTP, and then use the panel.
            $secret = $this->withToken($session)->postJson('/api/v1/me/totp/setup')->assertOk()->json('secret');
            $this->withToken($session)->postJson('/api/v1/me/totp/enable', ['password' => 'password', 'code' => Totp::code($secret)])->assertNoContent();
            $this->app['auth']->forgetGuards();
            $this->withToken($session)->getJson('/api/v1/websites')->assertOk();
            $this->withToken($session)->getJson('/api/v1/me')->assertOk()->assertJsonPath('totpSetupRequired', false);
            $this->app['auth']->forgetGuards();
        }
    }

    public function test_the_totp_rule_is_only_for_sign_in_sessions_of_administrators_and_resellers(): void
    {
        // A website owner selects TOTP freely.
        $owner = $this->user(Role::Owner, $this->customer());
        $this->withToken($this->signIn($owner))->getJson('/api/v1/websites')->assertOk();
        $this->app['auth']->forgetGuards();

        // An API token of an administrator continues to work.
        $admin = $this->user();
        $token = $admin->createToken('Terraform', ['websites:read'], now()->addDay())->plainTextToken;
        $this->withToken($token)->getJson('/api/v1/websites')->assertOk();
        $this->withToken($token)->getJson('/api/v1/me')->assertOk()->assertJsonPath('totpSetupRequired', false);
        $this->app['auth']->forgetGuards();

        // The setting for development turns the rule off.
        config(['cloudfactory.require_totp' => false]);
        $this->withToken($this->signIn($admin))->getJson('/api/v1/websites')->assertOk();
    }

    /** F10: the answer for a record that the user cannot see does not show internal names. */
    public function test_not_found_does_not_show_class_names(): void
    {
        $this->actingWith($this->user());
        $this->getJson('/api/v1/websites/01JBQ3Z5K8N6T4W2X9Y7R5P3M1')
            ->assertNotFound()->assertExactJson(['message' => 'Not found.']);
        $this->getJson('/api/v1/servers/01JBQ3Z5K8N6T4W2X9Y7R5P3M1')
            ->assertNotFound()->assertExactJson(['message' => 'Not found.']);
        // A website of a different customer gives the same answer.
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(Role::Owner, $this->customer(null, ['name' => 'Other Ltd'])));
        $this->getJson("/api/v1/websites/{$website->id}")->assertNotFound()->assertExactJson(['message' => 'Not found.']);
    }
}
