<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Jobs\SendEmailNotification;
use App\Models\MailServer;
use App\Models\NotificationDelivery;
use App\Models\User;
use App\Services\LoginThrottle;
use App\Services\Notifications\DemoMailTransport;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\Notifier;
use App\Services\Totp;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Queue;
use Illuminate\Support\Facades\RateLimiter;
use Tests\TestCase;

/** A user who does not know the password sets a new one with a link in an email. */
class PasswordResetTest extends TestCase
{
    private DemoMailTransport $mail;

    protected function setUp(): void
    {
        parent::setUp();
        $this->mail = new DemoMailTransport;
        $this->app->instance(MailTransport::class, $this->mail);
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        MailServer::create(['host' => 'smtp.example.com', 'port' => 587, 'encryption' => 'starttls', 'from_address' => 'panel@example.com']);
    }

    /** Sends the emails in the fake queue, and gives the link of the last email. */
    private function link(): string
    {
        foreach (Queue::pushed(SendEmailNotification::class) as $job) {
            app()->call([$job, 'handle']);
        }
        $this->assertNotEmpty($this->mail->sent);
        preg_match('#https://\S+/reset-password\#\S+#', end($this->mail->sent)['text'], $m);
        $this->assertNotEmpty($m, 'The email has no link.');

        return $m[0];
    }

    /** @return array{email: string, token: string} */
    private function parts(string $link): array
    {
        parse_str((string) parse_url($link, PHP_URL_FRAGMENT), $parts);

        return $parts;
    }

    public function test_the_link_sets_a_new_password_and_stops_the_sessions_and_tokens(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $user->createToken('Web session', ['*'], now()->addHours(12));
        $user->createToken('CI', ['websites:read']);

        $this->postJson('/api/v1/auth/password-reset', ['email' => strtoupper($user->email)])
            ->assertStatus(202)->assertJsonPath('message', fn ($m) => str_contains($m, '60 minutes'));
        $link = $this->link();
        $this->assertStringStartsWith('https://panel.example.com/reset-password#email=', $link);
        $parts = $this->parts($link);
        $this->assertSame($user->email, $parts['email']);
        // The panel keeps only a hash of the code.
        $this->assertNotSame($parts['token'], DB::table('password_reset_tokens')->where('email', $user->email)->value('token'));

        $this->postJson('/api/v1/auth/password-reset/confirm', $parts + ['password' => 'short', 'password_confirmation' => 'short'])
            ->assertUnprocessable()->assertJsonValidationErrors('password');
        $this->postJson('/api/v1/auth/password-reset/confirm', $parts + ['password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertNoContent();
        $this->assertSame(0, $user->tokens()->count());
        $this->assertDatabaseHas('audit_entries', ['action' => 'Reset password with an email link', 'user_id' => $user->id, 'target' => $user->email]);
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'a-new-long-password'])->assertOk();

        // The code works one time only.
        $this->postJson('/api/v1/auth/password-reset/confirm', $parts + ['password' => 'a-different-password', 'password_confirmation' => 'a-different-password'])
            ->assertUnprocessable()->assertJsonValidationErrors(['token' => 'This link does not work. It works one time only, for 60 minutes. Ask for a new link.']);
    }

    public function test_the_answer_is_the_same_for_an_unknown_email_and_for_a_plugin(): void
    {
        $plugin = User::factory()->create(['plugin_id' => 'example', 'email' => 'plugin@example.com']);
        $user = $this->user(Role::Owner, $this->customer());
        $known = $this->postJson('/api/v1/auth/password-reset', ['email' => $user->email])->assertStatus(202)->json();
        $this->assertSame($known, $this->postJson('/api/v1/auth/password-reset', ['email' => 'nobody@example.com'])->assertStatus(202)->json());
        $this->assertSame($known, $this->postJson('/api/v1/auth/password-reset', ['email' => $plugin->email])->assertStatus(202)->json());
        $this->assertSame([$user->id], NotificationDelivery::pluck('user_id')->all());

        $this->postJson('/api/v1/auth/password-reset/confirm', ['email' => 'nobody@example.com', 'token' => 'x', 'password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertUnprocessable()->assertJsonValidationErrors('token');
    }

    public function test_a_link_stops_after_60_minutes_and_a_new_link_replaces_the_old_one(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $this->postJson('/api/v1/auth/password-reset', ['email' => $user->email])->assertStatus(202);
        $first = $this->parts($this->link());

        // One email in each minute: a second request in the same minute sends nothing.
        $this->postJson('/api/v1/auth/password-reset', ['email' => $user->email])->assertStatus(202);
        $this->assertSame(1, NotificationDelivery::count());

        $this->travel(2)->minutes();
        $this->postJson('/api/v1/auth/password-reset', ['email' => $user->email])->assertStatus(202);
        $second = $this->parts($this->link());
        $this->assertNotSame($first['token'], $second['token']);
        $this->postJson('/api/v1/auth/password-reset/confirm', $first + ['password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertUnprocessable()->assertJsonValidationErrors('token');

        $this->travel(61)->minutes();
        $this->postJson('/api/v1/auth/password-reset/confirm', $second + ['password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertUnprocessable()->assertJsonValidationErrors('token');
    }

    public function test_a_new_password_does_not_turn_off_totp_and_removes_the_sign_in_lock(): void
    {
        $user = $this->user(Role::Administrator);
        $secret = $this->totp($user);
        for ($i = 0; $i < LoginThrottle::FREE_FAILURES; $i++) {
            LoginThrottle::fail($user->email, '127.0.0.1');
        }
        $this->assertGreaterThan(0, LoginThrottle::lockedFor($user->email, '127.0.0.1'));

        $this->postJson('/api/v1/auth/password-reset', ['email' => $user->email])->assertStatus(202);
        $this->postJson('/api/v1/auth/password-reset/confirm', $this->parts($this->link()) + ['password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertNoContent();
        $this->assertSame(0, LoginThrottle::lockedFor($user->email, '127.0.0.1'));
        $this->assertNotNull($user->fresh()->totp_secret);
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'a-new-long-password'])
            ->assertUnprocessable()->assertJsonPath('totpRequired', true);
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'a-new-long-password', 'totp' => Totp::code($secret)])->assertOk();
    }

    public function test_a_temporary_password_becomes_a_normal_password(): void
    {
        $user = $this->user(Role::Owner, $this->customer());
        $user->forceFill(['must_change_password' => true, 'temporary_password_at' => now()->subDays(30)])->save();
        $this->postJson('/api/v1/auth/password-reset', ['email' => $user->email])->assertStatus(202);
        $this->postJson('/api/v1/auth/password-reset/confirm', $this->parts($this->link()) + ['password' => 'a-new-long-password', 'password_confirmation' => 'a-new-long-password'])
            ->assertNoContent();
        $this->assertFalse($user->fresh()->must_change_password);
        $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'a-new-long-password'])->assertOk();
    }

    public function test_the_copy_of_the_email_in_the_panel_has_no_link(): void
    {
        $reseller = $this->customer(null, ['role' => 'reseller', 'branding' => ['name' => 'Northwind Hosting', 'panelDomain' => 'panel.northwind.example']]);
        $customer = $this->customer(null, ['reseller_id' => $reseller->id]);
        $owner = $this->user(Role::Owner, $customer);
        // The request comes to the domain of the panel, but the link is for the brand of the user.
        $this->postJson('http://evil.example/api/v1/auth/password-reset', ['email' => $owner->email])->assertStatus(202);
        $link = $this->link();
        $this->assertStringStartsWith('https://panel.northwind.example/reset-password#', $link);
        $this->assertSame('Set a new password for Northwind Hosting', end($this->mail->sent)['subject']);

        $delivery = NotificationDelivery::sole();
        $this->assertStringNotContainsString($this->parts($link)['token'], $delivery->body);
        $this->assertStringContainsString(Notifier::placeholder('resetUrl'), $delivery->body);
        // The queue has the link only in encrypted form.
        $job = Queue::pushed(SendEmailNotification::class)->first();
        $this->assertStringNotContainsString($this->parts($link)['token'], (string) $job->secrets);

        // The reseller reads the copy, but cannot send it again.
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->getJson("/api/v1/notifications/{$delivery->id}")->assertOk()->assertJsonPath('body', fn ($b) => ! str_contains($b, $this->parts($link)['token']));
        $delivery->update(['state' => 'failed']);
        $this->postJson("/api/v1/notifications/{$delivery->id}/retry")->assertStatus(409);
    }

    public function test_the_template_must_have_the_link_and_the_user_cannot_turn_the_email_off(): void
    {
        $this->actingWith($this->user());
        $this->putJson('/api/v1/notification-templates/password.reset', ['subject' => 'Reset', 'body' => 'Hello {{name}}'])
            ->assertUnprocessable()->assertJsonValidationErrors(['body' => 'The text must have {{resetUrl}}.']);
        $this->putJson('/api/v1/notification-templates/password.reset', ['subject' => 'Reset {{resetUrl}}', 'body' => 'Open {{resetUrl}}'])
            ->assertUnprocessable()->assertJsonValidationErrors('subject');
        $this->putJson('/api/v1/notification-templates/password.reset', ['subject' => 'Reset for {{brand}}', 'body' => "Open {{ resetUrl }}\nFrom {{ip}}"])->assertOk();

        $this->putJson('/api/v1/me/notification-preferences', ['events' => ['password.reset' => false]])->assertUnprocessable();
    }

    public function test_the_sign_in_page_shows_the_link_only_with_an_smtp_server(): void
    {
        $this->getJson('/api/v1/auth/methods')->assertOk()->assertExactJson(['passwordReset' => true]);
        MailServer::of(null)->update(['enabled' => false]);
        $this->getJson('/api/v1/auth/methods')->assertOk()->assertExactJson(['passwordReset' => false]);

        // A reseller with its own server: its panel domain has the link.
        $reseller = $this->customer(null, ['role' => 'reseller', 'branding' => ['name' => 'Northwind Hosting', 'panelDomain' => 'panel.northwind.example']]);
        MailServer::create(['customer_id' => $reseller->id, 'host' => '203.0.113.25', 'port' => 587, 'encryption' => 'starttls', 'from_address' => 'noreply@northwind.example']);
        $this->getJson('/api/v1/auth/methods')->assertOk()->assertJsonPath('passwordReset', false);
        $this->getJson('http://panel.northwind.example/api/v1/auth/methods')->assertOk()->assertJsonPath('passwordReset', true);
    }

    public function test_the_requests_have_a_limit_for_each_address(): void
    {
        RateLimiter::clear('post-auth-password-reset');
        for ($i = 0; $i < 5; $i++) {
            $this->postJson('/api/v1/auth/password-reset', ['email' => "user{$i}@example.com"])->assertStatus(202);
        }
        $this->postJson('/api/v1/auth/password-reset', ['email' => 'user9@example.com'])->assertTooManyRequests();
    }
}
