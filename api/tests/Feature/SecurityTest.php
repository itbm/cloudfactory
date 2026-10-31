<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Enums\Role;
use App\Http\Controllers\Api\AuthController;
use App\Models\AgentJob;
use App\Models\Domain;
use App\Models\User;
use App\Services\DomainPolicy;
use App\Services\JobOutcome;
use App\Services\LoginThrottle;
use App\Services\Totp;
use Illuminate\Contracts\Hashing\Hasher;
use Illuminate\Support\Facades\Hash;
use Tests\TestCase;

/** Regression tests for the findings of the security assessment of release 0.1 (CF-01 to CF-21). */
class SecurityTest extends TestCase
{
    /** CF-01: each database name starts with the Unix user of the website. */
    public function test_database_names_start_with_the_unix_user_of_the_website(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));
        $prefix = $website->unix_user.'_';

        foreach (['mysql', 'postgres', 'sys', 'information_schema'] as $name) {
            $response = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => $name])->assertAccepted();
            $this->assertSame($prefix.$name, $response->json('name'));
            $job = AgentJob::findOrFail($response->json('job.id'));
            $this->assertSame($prefix.$name, $job->payload['name']);
            $this->assertSame($prefix.$name, $job->payload['user']);
            $this->assertSame($website->unix_user, $job->payload['unixUser']);
        }
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => '../x'])->assertJsonValidationErrors('name');
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => str_repeat('a', 33)])->assertJsonValidationErrors('name');
        // The user can also type the full name.
        $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'postgresql', 'name' => $prefix.'shop'])
            ->assertAccepted()->assertJsonPath('name', $prefix.'shop');
    }

    /** CF-01: two websites can use the same short name, because each has its own prefix. */
    public function test_two_tenants_can_use_the_same_short_name(): void
    {
        $server = $this->server();
        $image = $this->image();
        $a = $this->website($this->customer(), $server, $image, 'a.example');
        $b = $this->website($this->customer(), $server, $image, 'b.example');
        $this->actingWith($this->user());

        $first = $this->postJson("/api/v1/websites/{$a->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted()->json('name');
        $second = $this->postJson("/api/v1/websites/{$b->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted()->json('name');
        $this->assertNotSame($first, $second);
    }

    /** The user's example: a sync user that can read production and change staging. */
    public function test_a_database_user_has_a_permission_for_each_database(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $other = $this->website($customer, $this->server(), $this->image(), 'other.example');
        $this->actingWith($this->user(Role::Owner, $customer));
        $prod = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'prod'])->json('id');
        $staging = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'staging', 'createUser' => false])->json('id');
        $pg = $this->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'postgresql', 'name' => 'reports', 'createUser' => false])->json('id');
        $foreign = $this->postJson("/api/v1/websites/{$other->id}/databases", ['engine' => 'mariadb', 'name' => 'x'])->json('id');

        $response = $this->postJson("/api/v1/websites/{$website->id}/database-users", [
            'engine' => 'mariadb', 'name' => 'sync',
            'grants' => [['databaseId' => $prod, 'privileges' => 'readonly'], ['databaseId' => $staging, 'privileges' => 'readwrite']],
        ])->assertAccepted();
        $this->assertSame(24, strlen($response->json('password')));
        $response->assertJsonPath('user.name', $website->unix_user.'_sync')
            ->assertJsonPath('user.grants.0.database', $website->unix_user.'_prod')
            ->assertJsonPath('user.grants.0.privileges', 'readonly')
            ->assertJsonPath('user.grants.1.privileges', 'readwrite');
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame('SetDatabaseUser', $job->action->value);
        $this->assertTrue($job->payload['create']);
        $this->assertSame([['database' => $website->unix_user.'_prod', 'privileges' => 'readonly'], ['database' => $website->unix_user.'_staging', 'privileges' => 'readwrite']], $job->payload['grants']);
        app(JobOutcome::class)->finish($job, JobState::Succeeded);
        $this->assertArrayNotHasKey('password', $job->fresh()->payload);

        // A database of a different website, or with a different engine, cannot be granted.
        $user = $response->json('user.id');
        $this->patchJson("/api/v1/websites/{$website->id}/database-users/{$user}", ['grants' => [['databaseId' => $foreign, 'privileges' => 'readonly']]])
            ->assertJsonValidationErrors('grants.0.databaseId');
        $this->patchJson("/api/v1/websites/{$website->id}/database-users/{$user}", ['grants' => [['databaseId' => $pg, 'privileges' => 'readonly']]])
            ->assertJsonValidationErrors('grants.0.databaseId');
        $this->patchJson("/api/v1/websites/{$website->id}/database-users/{$user}", ['grants' => [['databaseId' => $prod, 'privileges' => 'admin']]])
            ->assertJsonValidationErrors('grants.0.privileges');

        // Remove the access to production.
        $this->patchJson("/api/v1/websites/{$website->id}/database-users/{$user}", ['grants' => [['databaseId' => $staging, 'privileges' => 'readwrite']], 'access' => 'anywhere'])
            ->assertAccepted()->assertJsonPath('user.access', 'anywhere')->assertJsonCount(1, 'user.grants')->assertJsonPath('password', null);
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertOk()
            ->assertJsonPath('prefix', $website->unix_user.'_')
            ->assertJsonCount(2, 'users');

        // Removing a database removes the access of its users first.
        $jobId = $this->deleteJson("/api/v1/websites/{$website->id}/databases/{$staging}")->assertAccepted()->json('job.id');
        $this->assertSame([$website->unix_user.'_sync'], AgentJob::findOrFail($jobId)->payload['users']);
        // Batch 1, finding 4: the database stays in the panel until the agent removed it.
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertJsonCount(3, 'databases');
        app(JobOutcome::class)->finish(AgentJob::findOrFail($jobId), JobState::Succeeded);
        $this->getJson("/api/v1/websites/{$website->id}/databases")->assertJsonCount(2, 'databases')->assertJsonPath('users.1.grants', []);

        $this->deleteJson("/api/v1/websites/{$website->id}/database-users/{$user}")->assertAccepted()->assertJsonPath('job.action', 'DeleteDatabaseUser');
    }

    /** CF-02: a token cannot make a token with more scopes, and the user must give their password. */
    public function test_a_token_cannot_make_a_wider_token(): void
    {
        $user = $this->user();
        $this->actingWith($user, ['audit:read']);
        $this->postJson('/api/v1/tokens', ['name' => 'x', 'scopes' => ['audit:read'], 'expiresInDays' => 1, 'password' => 'password'])->assertForbidden();
        $this->getJson('/api/v1/tokens')->assertForbidden();

        $this->actingWith($user, ['tokens:write', 'websites:write']);
        $this->postJson('/api/v1/tokens', ['name' => 'x', 'scopes' => ['websites:write', 'customers:write'], 'expiresInDays' => 1, 'password' => 'password'])
            ->assertForbidden()->assertJsonPath('message', 'A new token cannot have scopes that this token does not have: customers:write.');
        $this->postJson('/api/v1/tokens', ['name' => 'x', 'scopes' => ['websites:read'], 'expiresInDays' => 1, 'password' => 'wrong'])
            ->assertJsonValidationErrors('password');
        $this->postJson('/api/v1/tokens', ['name' => 'x', 'scopes' => ['websites:read'], 'expiresInDays' => 1, 'password' => 'password'])->assertCreated();
    }

    public function test_a_new_token_needs_the_totp_code_when_totp_is_on(): void
    {
        $user = $this->user();
        $secret = Totp::generateSecret();
        $user->forceFill(['totp_secret' => $secret])->save();
        $this->actingWith($user);

        $body = ['name' => 'x', 'scopes' => ['websites:read'], 'expiresInDays' => 1, 'password' => 'password'];
        $this->postJson('/api/v1/tokens', $body)->assertJsonValidationErrors('totp');
        $this->postJson('/api/v1/tokens', $body + ['totp' => Totp::code($secret)])->assertCreated();
    }

    /** CF-03: a domain of the panel or a server is reserved. Other domains need a TXT record. */
    public function test_a_customer_cannot_attach_a_reserved_domain(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.cloudfactory.io']);
        $customer = $this->customer();
        $server = $this->server(['agent_url' => 'https://web-1.hosting.example:7443']);
        $website = $this->website($customer, $server, $this->image());
        $this->customer(attributes: ['role' => 'reseller', 'branding' => ['panelDomain' => 'panel.reseller.example']]);
        $this->actingWith($this->user(Role::Owner, $customer));

        foreach (['panel.cloudfactory.io', 'x.panel.cloudfactory.io', 'web-1.hosting.example', 'panel.reseller.example'] as $name) {
            $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => $name, 'kind' => 'alias'])
                ->assertJsonValidationErrors(['name' => 'This domain is reserved for the panel or a server.']);
        }
        $this->postJson('/api/v1/websites', ['name' => 'panel.cloudfactory.io', 'imageId' => $website->image_id])->assertJsonValidationErrors('name');
    }

    public function test_the_edge_proxy_serves_a_domain_only_after_verification(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));
        $txt = [];
        $this->app->instance(DomainPolicy::class, new class($txt) extends DomainPolicy
        {
            public function __construct(public array &$txt) {}

            protected function txtRecords(string $name): array
            {
                return $this->txt[$name] ?? [];
            }
        });

        // A sub-domain of a verified domain of the same customer is verified at once.
        $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'www.acme.example', 'kind' => 'redirect'])
            ->assertAccepted()->assertJsonPath('domain.verified', true)->assertJsonPath('job.action', 'AddDomain');

        $response = $this->postJson("/api/v1/websites/{$website->id}/domains", ['name' => 'bigbrand.example', 'kind' => 'alias'])
            ->assertAccepted()->assertJsonPath('domain.verified', false)->assertJsonPath('job', null)
            ->assertJsonPath('domain.verification.name', '_cloudfactory-challenge.bigbrand.example');
        $token = $response->json('domain.verification.value');

        $this->postJson("/api/v1/websites/{$website->id}/domains/bigbrand.example/verify")->assertJsonValidationErrors('name');
        $txt['_cloudfactory-challenge.bigbrand.example'] = ['something else', $token];
        $this->postJson("/api/v1/websites/{$website->id}/domains/bigbrand.example/verify")
            ->assertAccepted()->assertJsonPath('domain.verified', true)->assertJsonPath('job.action', 'AddDomain');
    }

    public function test_a_claim_that_is_not_verified_does_not_block_the_real_owner(): void
    {
        $squatter = $this->customer();
        $owner = $this->customer();
        $server = $this->server();
        $image = $this->image();
        $squat = $this->website($squatter, $server, $image, 'squat.example');
        $real = $this->website($owner, $server, $image, 'real.example');
        $squat->domains()->create(['name' => 'brand.example', 'kind' => 'alias', 'verification_token' => 'cf-x']);
        $this->actingWith($this->user(Role::Owner, $owner));

        $this->postJson("/api/v1/websites/{$real->id}/domains", ['name' => 'brand.example', 'kind' => 'alias'])->assertAccepted();
        $this->assertSame($real->id, Domain::where('name', 'brand.example')->sole()->website_id);

        // A verified domain stays with its website.
        $this->postJson("/api/v1/websites/{$real->id}/domains", ['name' => 'squat.example', 'kind' => 'alias'])->assertJsonValidationErrors(['name' => 'This domain is in use.']);
    }

    /** CF-07: a website owner cannot select a different plan. */
    public function test_an_owner_cannot_move_a_website_to_a_different_plan(): void
    {
        $business = $this->plan();
        $agency = $this->plan(['name' => 'Agency']);
        $customer = $this->customer($business);
        $server = $this->server();
        $image = $this->image();
        $website = $this->website($customer, $server, $image);
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->patchJson("/api/v1/websites/{$website->id}", ['planId' => $agency->id])->assertForbidden();
        $this->assertSame($business->id, $website->fresh()->plan_id);
        $this->postJson('/api/v1/websites', ['name' => 'new.example', 'imageId' => $image->id, 'planId' => $agency->id])->assertJsonValidationErrors('planId');
        $this->postJson('/api/v1/websites', ['name' => 'new.example', 'imageId' => $image->id])->assertAccepted()->assertJsonPath('website.planId', $business->id);
    }

    public function test_the_reseller_of_the_customer_can_change_the_plan(): void
    {
        $agency = $this->plan(['name' => 'Agency']);
        $reseller = $this->customer(attributes: ['role' => 'reseller', 'plan_ids' => [$agency->id]]);
        $customer = $this->customer(attributes: ['reseller_id' => $reseller->id]);
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $this->patchJson("/api/v1/websites/{$website->id}", ['planId' => $agency->id])->assertOk()->assertJsonPath('website.planId', $agency->id);
    }

    /** CF-08: suspension stops sign-in and all requests, also for the customers of a reseller. */
    public function test_a_suspended_customer_has_no_access(): void
    {
        $reseller = $this->customer(attributes: ['role' => 'reseller']);
        $customer = $this->customer(attributes: ['reseller_id' => $reseller->id]);
        $owner = $this->user(Role::Owner, $customer);
        $token = $owner->createToken('CI', ['*'], now()->addDay())->plainTextToken;
        $this->withToken($token)->getJson('/api/v1/websites')->assertOk();

        $this->actingWith($this->user());
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['suspended' => true])->assertOk();
        $this->app['auth']->forgetGuards();

        $this->assertSame(0, $owner->tokens()->count());
        $this->withToken($token)->getJson('/api/v1/websites')->assertUnauthorized();
        $this->postJson('/api/v1/auth/login', ['email' => $owner->email, 'password' => 'password'])->assertForbidden();
        $this->actingWith($owner);
        $this->getJson('/api/v1/websites')->assertForbidden()->assertJsonPath('message', 'This account is suspended. Contact your provider.');
    }

    /** CF-09: a staging copy counts as a website of the plan. */
    public function test_a_staging_copy_needs_space_in_the_plan(): void
    {
        $customer = $this->customer($this->plan(['name' => 'Starter', 'websites' => 1]));
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));

        $this->postJson("/api/v1/websites/{$website->id}/staging")->assertJsonValidationErrors(['name' => 'The Starter plan allows 1 websites.']);
        $this->assertSame(1, $customer->websites()->count());
    }

    /** CF-12: a TOTP code is good one time only. */
    public function test_a_totp_code_cannot_be_used_again(): void
    {
        $user = $this->user();
        $secret = Totp::generateSecret();
        $user->forceFill(['totp_secret' => $secret])->save();
        $code = Totp::code($secret);

        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password', 'totp' => $code])->assertOk();
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password', 'totp' => $code])->assertJsonValidationErrors('email');
        // An older code is refused too.
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password', 'totp' => Totp::code($secret, time() - 30)])->assertJsonValidationErrors('email');
    }

    /**
     * CF-12: each account has a limit of failed attempts. R-09: the lock is for the account and
     * the address together, so the real user can still sign in from a different address.
     */
    public function test_an_account_locks_after_failed_attempts(): void
    {
        $user = $this->user();
        $attacker = fn () => $this->withServerVariables(['REMOTE_ADDR' => '10.0.0.9']);
        for ($i = 0; $i < LoginThrottle::FREE_FAILURES; $i++) {
            $attacker()->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'wrong'])->assertUnprocessable();
        }
        $attacker()->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])
            ->assertStatus(429)->assertHeader('Retry-After', (string) LoginThrottle::FIRST_LOCK_SECONDS);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Locked sign-in after failed attempts', 'target' => "{$user->email} from 10.0.0.9"]);
        $this->withServerVariables(['REMOTE_ADDR' => '192.0.2.1'])
            ->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertOk();

        $this->travel(LoginThrottle::FIRST_LOCK_SECONDS + 1)->seconds();
        $attacker()->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'password'])->assertOk();
    }

    /** CF-14: the panel checks a password hash also for an email that it does not know. */
    public function test_an_unknown_email_also_checks_a_password_hash(): void
    {
        $hasher = new class(app('hash')) implements Hasher
        {
            public int $checks = 0;

            public function __construct(private Hasher $inner) {}

            public function info($hashedValue)
            {
                return $this->inner->info($hashedValue);
            }

            public function make($value, array $options = [])
            {
                return $this->inner->make($value, $options);
            }

            public function check($value, $hashedValue, array $options = [])
            {
                $this->checks++;

                return $this->inner->check($value, $hashedValue, $options);
            }

            public function needsRehash($hashedValue, array $options = [])
            {
                return $this->inner->needsRehash($hashedValue, $options);
            }
        };
        Hash::swap($hasher);

        $this->postJson('/api/v1/auth/login', ['email' => 'nobody@example.com', 'password' => 'x'])->assertJsonValidationErrors('email');

        $this->assertSame(1, $hasher->checks);
    }

    /** CF-15: each php.ini key has its own format and range. */
    public function test_php_ini_values_are_checked(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->actingWith($this->user(Role::Owner, $customer));

        foreach ([
            'memory_limit' => ['-1', '0', '99G', '1M', 'abc'],
            'max_execution_time' => ['0', '-1', '99999', '1.5'],
            'display_errors' => ['yes'],
            'date.timezone' => ['Mars/Olympus'],
            'upload_max_filesize' => ['-1', '1T'],
            'max_input_vars' => ['1'],
        ] as $key => $values) {
            foreach ($values as $value) {
                $this->putJson("/api/v1/websites/{$website->id}/runtime", ['phpIni' => [$key => $value]])->assertJsonValidationErrors("phpIni.{$key}");
            }
        }
        $this->putJson("/api/v1/websites/{$website->id}/runtime", ['phpIni' => ['memory_limit' => '512M', 'max_execution_time' => '120', 'display_errors' => 'Off', 'date.timezone' => 'Europe/London']])
            ->assertAccepted();
    }

    /** CF-18: only the panel domain can call the API from a browser. */
    public function test_cors_permits_only_the_panel_domain(): void
    {
        $preflight = fn (string $origin) => $this->call('OPTIONS', '/api/v1/me', server: [
            'HTTP_ORIGIN' => $origin,
            'HTTP_ACCESS_CONTROL_REQUEST_METHOD' => 'GET',
        ]);

        $evil = $preflight('https://evil.example')->headers->get('Access-Control-Allow-Origin');
        $this->assertNotContains($evil, ['*', 'https://evil.example']);
        $this->assertSame('https://'.env('CF_PANEL_DOMAIN', 'panel.cloudfactory.io'), $preflight('https://panel.cloudfactory.io')->headers->get('Access-Control-Allow-Origin'));
    }

    /** CF-20: only a reseller has a brand, and its panel domain must not be reserved. */
    public function test_an_owner_cannot_set_a_brand(): void
    {
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));
        $this->patchJson("/api/v1/customers/{$customer->id}", ['branding' => ['panelDomain' => 'panel.cloudfactory.io']])->assertForbidden();

        $reseller = $this->customer(attributes: ['role' => 'reseller']);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => config('cloudfactory.panel_domain')]])->assertJsonValidationErrors('branding.panelDomain');
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => 'panel.agency.example']])->assertJsonValidationErrors('branding.panelDomain');
        $this->actingWith($this->user());
        $this->patchJson("/api/v1/customers/{$reseller->id}", ['branding' => ['panelDomain' => 'panel.agency.example']])->assertOk();
    }

    /** CF-16: the agent refuses an older version unless an administrator allows it. */
    public function test_an_agent_update_sends_the_downgrade_choice(): void
    {
        $server = $this->server();
        $this->actingWith($this->user());

        $this->postJson("/api/v1/servers/{$server->id}/update-agent")->assertAccepted();
        $this->assertFalse(AgentJob::sole()->payload['allowDowngrade']);

        $this->postJson("/api/v1/servers/{$server->id}/update-agent", ['allowDowngrade' => 'yes'])->assertJsonValidationErrors('allowDowngrade');
        $this->postJson("/api/v1/servers/{$server->id}/update-agent", ['allowDowngrade' => true])->assertAccepted();
        $this->assertSame([false, true], AgentJob::all()->map(fn ($job) => $job->payload['allowDowngrade'])->sort()->values()->all());
    }

    /** CF-14: the dummy hash is fixed, so an unknown email does not make a new hash for each request. */
    public function test_the_dummy_hash_is_fixed_and_has_the_default_cost(): void
    {
        $this->assertTrue(password_verify('x', AuthController::DUMMY_HASH) === false);
        $this->assertSame(12, password_get_info(AuthController::DUMMY_HASH)['options']['cost']);
        $this->bcryptRounds(12);
        $this->assertSame(AuthController::DUMMY_HASH, AuthController::dummyHash());

        // With other settings, the panel makes the hash one time and then uses it again.
        $this->bcryptRounds(5);
        $first = AuthController::dummyHash();
        $this->assertSame(5, password_get_info($first)['options']['cost']);
        $this->assertSame($first, AuthController::dummyHash());
    }

    /** CF-14: the sign-in time does not show whether an email has an account. */
    public function test_an_unknown_email_takes_the_same_time_as_a_known_email(): void
    {
        $this->bcryptRounds(12);
        User::factory()->create(['email' => 'known@example.com', 'password' => Hash::make('the-real-password')]);
        $time = function (string $email): float {
            $start = hrtime(true);
            $this->postJson('/api/v1/auth/login', ['email' => $email, 'password' => 'wrong-password'])->assertJsonValidationErrors('email');

            return (hrtime(true) - $start) / 1e9;
        };
        $time('warm-up@example.com');

        // Fewer attempts than the lock-out limit for the known account.
        $known = $unknown = [];
        for ($i = 0; $i < 4; $i++) {
            $known[] = $time('known@example.com');
            $unknown[] = $time("nobody{$i}@example.com");
        }
        sort($known);
        sort($unknown);
        $knownMedian = ($known[1] + $known[2]) / 2;
        $unknownMedian = ($unknown[1] + $unknown[2]) / 2;

        $ratio = $unknownMedian / $knownMedian;
        $this->assertTrue($ratio > 0.67 && $ratio < 1.5, sprintf('known %.3f s, unknown %.3f s', $knownMedian, $unknownMedian));
    }

    /** The hasher reads its settings when it starts, so start it again with the new cost. */
    private function bcryptRounds(int $rounds): void
    {
        config(['hashing.bcrypt.rounds' => $rounds]);
        $this->app->forgetInstance('hash');
        Hash::clearResolvedInstances();
    }
}
