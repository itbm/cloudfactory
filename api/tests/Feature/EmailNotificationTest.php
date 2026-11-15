<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\NotificationEvent;
use App\Enums\Role;
use App\Jobs\SendEmailNotification;
use App\Models\AgentJob;
use App\Models\MailServer;
use App\Models\NotificationDelivery;
use App\Models\NotificationTemplate;
use App\Models\User;
use App\Models\Website;
use App\Services\JobOutcome;
use App\Services\Notifications\DemoMailTransport;
use App\Services\Notifications\MailTransport;
use App\Services\Notifications\Notifier;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

/** Email notifications: the SMTP servers, the templates, the preferences and the events. */
class EmailNotificationTest extends TestCase
{
    private DemoMailTransport $mail;

    protected function setUp(): void
    {
        parent::setUp();
        $this->mail = new DemoMailTransport;
        $this->app->instance(MailTransport::class, $this->mail);
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
    }

    private function panelServer(array $attributes = []): MailServer
    {
        return MailServer::create($attributes + ['host' => 'smtp.example.com', 'port' => 587, 'encryption' => 'starttls', 'username' => 'panel', 'password' => 'secret', 'from_address' => 'panel@example.com', 'from_name' => 'Example Hosting']);
    }

    /** Runs the email jobs that are in the fake queue. */
    private function sendQueued(): void
    {
        foreach (Queue::pushed(SendEmailNotification::class) as $job) {
            app()->call([$job, 'handle']);
        }
    }

    private function failJob(Website $website, AgentAction $action = AgentAction::CreateDatabase, string $title = 'Create database shop_main'): AgentJob
    {
        $job = AgentJob::create(['server_id' => $website->server_id, 'website_id' => $website->id, 'action' => $action, 'title' => $title, 'payload' => [], 'steps' => ['One'], 'step' => 0, 'state' => JobState::Running]);
        app(JobOutcome::class)->finish($job, JobState::Failed, 'The database server did not answer.');

        return $job;
    }

    public function test_an_administrator_sets_the_smtp_server_and_the_password_stays_secret(): void
    {
        $admin = $this->actingWith($this->user());
        $this->putJson('/api/v1/mail-server', ['host' => 'smtp.example.com', 'port' => 587, 'encryption' => 'starttls', 'username' => 'panel', 'password' => 'hunter2', 'fromAddress' => 'panel@example.com', 'fromName' => 'Example Hosting'])
            ->assertOk()->assertJson(['configured' => true, 'enabled' => true, 'hasPassword' => true, 'panelFallback' => null])->assertJsonMissingPath('password');
        $this->assertSame('hunter2', MailServer::of(null)->password);
        $this->assertStringNotContainsString('hunter2', (string) MailServer::of(null)->getRawOriginal('password'));

        // Without the password field, the password stays.
        $this->putJson('/api/v1/mail-server', ['host' => 'smtp.example.com', 'port' => 465, 'encryption' => 'tls', 'username' => 'panel', 'fromAddress' => 'panel@example.com'])->assertOk()->assertJson(['hasPassword' => true, 'port' => 465]);
        $this->putJson('/api/v1/mail-server', ['host' => 'smtp.example.com', 'port' => 3306, 'encryption' => 'tls', 'fromAddress' => 'panel@example.com'])->assertUnprocessable()->assertJsonValidationErrors('port');

        $this->postJson('/api/v1/mail-server/test', ['to' => 'ops@example.com'])->assertOk()->assertJson(['sent' => true, 'to' => 'ops@example.com']);
        $this->assertSame('ops@example.com', $this->mail->sent[0]['to']);
        $this->assertNotNull(MailServer::of(null)->tested_at);

        MailServer::of(null)->update(['host' => 'fail.example.com']);
        $this->postJson('/api/v1/mail-server/test')->assertOk()->assertJson(['sent' => false, 'to' => $admin->email])->assertJsonPath('server.testError', fn ($e) => str_contains($e, 'fail.example.com'));
    }

    public function test_a_reseller_has_its_own_server_and_an_owner_has_no_access(): void
    {
        $reseller = $this->customer(null, ['role' => 'reseller', 'name' => 'Northwind']);
        $resellerUser = $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->panelServer();
        $this->getJson('/api/v1/mail-server')->assertOk()->assertJson(['configured' => false, 'panelFallback' => true]);
        // The panel does not connect to a private address for a reseller.
        $this->putJson('/api/v1/mail-server', ['host' => '10.0.0.5', 'port' => 25, 'encryption' => 'none', 'fromAddress' => 'hello@northwind.example'])->assertUnprocessable()->assertJsonValidationErrors('host');
        $this->putJson('/api/v1/mail-server', ['host' => '127.0.0.1', 'port' => 25, 'encryption' => 'none', 'fromAddress' => 'hello@northwind.example'])->assertUnprocessable();
        $this->putJson('/api/v1/mail-server', ['host' => '203.0.113.10', 'port' => 587, 'encryption' => 'starttls', 'fromAddress' => 'hello@northwind.example'])->assertOk();
        $this->assertSame($reseller->id, MailServer::where('host', '203.0.113.10')->sole()->customer_id);
        // A reseller sends a test email to its own address only.
        $this->postJson('/api/v1/mail-server/test', ['to' => 'someone@else.example'])->assertOk()->assertJson(['to' => $resellerUser->email]);

        $owner = $this->customer(null, ['reseller_id' => $reseller->id]);
        $this->actingWith($this->user(Role::Owner, $owner));
        $this->getJson('/api/v1/mail-server')->assertForbidden();
        $this->getJson('/api/v1/notification-templates')->assertForbidden();
    }

    public function test_a_failed_job_sends_an_email_to_the_customer_and_the_administrators(): void
    {
        $this->panelServer();
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(['name' => 'web-1']), $this->image(), 'shop.example.com');
        $owner = $this->user(Role::Owner, $customer);
        $quiet = $this->user(Role::Owner, $customer);
        $quiet->forceFill(['notification_preferences' => ['job.failed' => false]])->save();
        $admin = $this->user();
        $other = $this->user(Role::Owner, $this->customer());

        $job = $this->failJob($website);
        $to = NotificationDelivery::where('event', 'job.failed')->pluck('recipient')->sort()->values()->all();
        $this->assertSame(collect([$owner->email, $admin->email])->sort()->values()->all(), $to);
        $this->assertNotContains($other->email, $to);
        $delivery = NotificationDelivery::where('user_id', $owner->id)->sole();
        $this->assertSame('Failed: Create database shop_main', $delivery->subject);
        $this->assertStringContainsString('The error: The database server did not answer.', $delivery->body);
        $this->assertStringContainsString('Server: web-1', $delivery->body);
        $this->assertStringContainsString('https://panel.example.com/jobs', $delivery->body);

        $this->sendQueued();
        $this->assertSame(['sent', 'sent'], NotificationDelivery::pluck('state')->all());
        $this->assertCount(2, $this->mail->sent);
        // One email for each job.
        app(Notifier::class)->jobFailed($job, $website);
        $this->assertSame(2, NotificationDelivery::count());
    }

    public function test_a_failed_backup_has_its_own_event(): void
    {
        $this->panelServer();
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image(), 'shop.example.com');
        $owner = $this->user(Role::Owner, $customer);
        $this->failJob($website, AgentAction::Backup, 'Back up shop.example.com');

        $delivery = NotificationDelivery::where('user_id', $owner->id)->sole();
        $this->assertSame('backup.failed', $delivery->event);
        $this->assertSame('The backup of shop.example.com failed', $delivery->subject);
    }

    public function test_the_customers_of_a_reseller_get_the_brand_and_the_server_of_the_reseller(): void
    {
        $this->panelServer();
        $reseller = $this->customer(null, ['role' => 'reseller', 'name' => 'Northwind', 'branding' => ['name' => 'Northwind Hosting', 'colour' => '#aa3300', 'panelDomain' => 'panel.northwind.example', 'supportEmail' => 'help@northwind.example']]);
        $customer = $this->customer(null, ['reseller_id' => $reseller->id]);
        $website = $this->website($customer, $this->server(), $this->image(), 'shop.example.com');
        $owner = $this->user(Role::Owner, $customer);
        $resellerUser = $this->user(Role::Reseller, $reseller);
        MailServer::create(['customer_id' => $reseller->id, 'host' => '203.0.113.25', 'port' => 587, 'encryption' => 'starttls', 'from_address' => 'noreply@northwind.example']);
        NotificationTemplate::create(['customer_id' => $reseller->id, 'event' => 'job.failed', 'subject' => '[{{brand}}] {{job}} failed', 'body' => 'Hi {{name}}, see {{panelUrl}}.']);

        $this->failJob($website);
        $delivery = NotificationDelivery::where('user_id', $owner->id)->sole();
        $this->assertSame($reseller->id, $delivery->sender_id);
        $this->assertSame('[Northwind Hosting] Create database shop_main failed', $delivery->subject);
        $this->assertSame("Hi {$owner->name}, see https://panel.northwind.example.", $delivery->body);
        // The reseller itself is a customer of the panel: its email has the panel brand.
        $own = NotificationDelivery::where('user_id', $resellerUser->id)->sole();
        $this->assertNull($own->sender_id);
        $this->assertSame('Failed: Create database shop_main', $own->subject);

        $this->sendQueued();
        $servers = array_column($this->mail->sent, 'server', 'to');
        $this->assertSame('203.0.113.25', $servers[$owner->email]);
        $this->assertSame('smtp.example.com', $servers[$resellerUser->email]);
    }

    public function test_with_no_server_of_its_own_a_reseller_uses_the_panel_server(): void
    {
        $this->panelServer();
        $reseller = $this->customer(null, ['role' => 'reseller', 'branding' => ['name' => 'Northwind Hosting']]);
        $customer = $this->customer(null, ['reseller_id' => $reseller->id]);
        $website = $this->website($customer, $this->server(), $this->image());
        $owner = $this->user(Role::Owner, $customer);
        $this->failJob($website);

        $delivery = NotificationDelivery::where('user_id', $owner->id)->sole();
        $this->assertNull($delivery->sender_id);
        $this->sendQueued();
        $this->assertSame('sent', $delivery->fresh()->state);
    }

    public function test_an_email_with_no_smtp_server_is_skipped(): void
    {
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->user(Role::Owner, $customer);
        $this->failJob($website);

        $this->assertSame(['skipped'], NotificationDelivery::pluck('state')->all());
        Queue::assertNotPushed(SendEmailNotification::class);
    }

    public function test_the_queue_tries_again_and_then_records_the_failure(): void
    {
        $this->panelServer(['host' => 'fail.example.com']);
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image());
        $this->user(Role::Owner, $customer);
        $this->failJob($website);
        $delivery = NotificationDelivery::sole();

        foreach ([1, 2] as $attempt) {
            (new SendEmailNotification($delivery->id))->handle($this->mail);
            $this->assertSame(['queued', $attempt], [$delivery->fresh()->state, $delivery->fresh()->attempts]);
        }
        (new SendEmailNotification($delivery->id))->handle($this->mail);
        $this->assertSame('failed', $delivery->fresh()->state);
        $this->assertStringContainsString('fail.example.com', $delivery->fresh()->error);

        // After a correction of the server, the administrator sends it again.
        MailServer::of(null)->update(['host' => 'smtp.example.com']);
        $this->actingWith($this->user());
        $this->postJson("/api/v1/notifications/{$delivery->id}/retry")->assertOk()->assertJson(['state' => 'queued', 'attempts' => 0]);
        (new SendEmailNotification($delivery->id))->handle($this->mail);
        $this->assertSame('sent', $delivery->fresh()->state);
        $this->postJson("/api/v1/notifications/{$delivery->id}/retry")->assertStatus(409);
    }

    public function test_templates_can_be_changed_previewed_and_reset(): void
    {
        $this->actingWith($this->user());
        $list = $this->getJson('/api/v1/notification-templates')->assertOk()->json('data');
        $this->assertSame(array_map(fn ($e) => $e->value, NotificationEvent::cases()), array_column($list, 'event'));

        $this->putJson('/api/v1/notification-templates/job.failed', ['subject' => 'Oops: {{job}}', 'body' => 'Hello {{nobody}}'])
            ->assertUnprocessable()->assertJsonValidationErrors('body');
        $this->putJson('/api/v1/notification-templates/job.failed', ['subject' => "Two\nlines", 'body' => 'x'])->assertUnprocessable()->assertJsonValidationErrors('subject');
        $this->putJson('/api/v1/notification-templates/job.failed', ['subject' => 'Oops: {{ job }}', 'body' => "Hello {{name}}\n\n<b>{{error}}</b>"])
            ->assertOk()->assertJson(['changed' => true, 'subject' => 'Oops: {{ job }}']);
        $preview = $this->postJson('/api/v1/notification-templates/job.failed/preview', ['subject' => 'Oops: {{ job }}', 'body' => "Hello {{name}}\n\n<b>{{error}}</b> {{panelUrl}}"])->assertOk();
        $this->assertSame('Oops: Create database shop_main', $preview->json('subject'));
        $this->assertStringContainsString('&lt;b&gt;The database server did not answer.&lt;/b&gt;', $preview->json('html'));
        $this->assertStringContainsString('<a href="https://panel.example.com"', $preview->json('html'));
        $this->putJson('/api/v1/notification-templates/nothing', ['subject' => 'a', 'body' => 'b'])->assertNotFound();

        $this->deleteJson('/api/v1/notification-templates/job.failed')->assertOk()->assertJson(['changed' => false, 'subject' => NotificationEvent::JobFailed->defaultSubject()]);
    }

    public function test_a_reseller_changes_only_the_templates_of_its_customers(): void
    {
        NotificationTemplate::create(['event' => 'job.failed', 'subject' => 'Panel: {{job}}', 'body' => 'Panel body']);
        $reseller = $this->customer(null, ['role' => 'reseller']);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $list = collect($this->getJson('/api/v1/notification-templates')->assertOk()->json('data'))->keyBy('event');
        $this->assertFalse($list->has('server.offline'));
        // The reseller starts from the template of the panel.
        $this->assertSame(['Panel: {{job}}', false, 'Panel: {{job}}'], [$list['job.failed']['subject'], $list['job.failed']['changed'], $list['job.failed']['originalSubject']]);
        $this->putJson('/api/v1/notification-templates/server.offline', ['subject' => 'a', 'body' => 'b'])->assertNotFound();
        $this->putJson('/api/v1/notification-templates/job.failed', ['subject' => 'Ours: {{job}}', 'body' => 'Ours'])->assertOk()->assertJson(['changed' => true]);
        $this->assertSame('Panel: {{job}}', NotificationTemplate::whereNull('customer_id')->sole()->subject);
    }

    public function test_each_user_chooses_the_events_but_not_the_security_events(): void
    {
        $customer = $this->customer();
        $owner = $this->actingWith($this->user(Role::Owner, $customer));
        $events = collect($this->getJson('/api/v1/me/notification-preferences')->assertOk()->json('data'))->keyBy('event');
        $this->assertFalse($events->has('server.offline'));
        $this->assertTrue($events['job.failed']['email']);
        $this->assertTrue($events['signin.new_device']['required']);

        $this->putJson('/api/v1/me/notification-preferences', ['events' => ['job.failed' => false, 'usage.alert' => false]])->assertOk()
            ->assertJsonPath('data.0.email', false);
        $this->assertSame(['job.failed' => false, 'usage.alert' => false], $owner->fresh()->notification_preferences);
        $this->putJson('/api/v1/me/notification-preferences', ['events' => ['signin.new_device' => false]])->assertUnprocessable();
        $this->putJson('/api/v1/me/notification-preferences', ['events' => ['server.offline' => true]])->assertUnprocessable();
    }

    public function test_a_sign_in_from_a_new_device_sends_an_email(): void
    {
        $this->panelServer();
        $user = User::factory()->create(['role' => Role::Owner, 'customer_id' => $this->customer()->id, 'password' => Hash::make('correct horse battery')]);
        $login = fn (array $extra = [], string $agent = 'Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0') => $this->withHeader('User-Agent', $agent)
            ->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => 'correct horse battery'] + $extra)->assertOk();

        // The first sign-in of a user gives no email.
        $login(['deviceId' => 'browser-one-aaaaaaaa']);
        $login(['deviceId' => 'browser-one-aaaaaaaa']);
        $this->assertSame(0, NotificationDelivery::count());
        $login(['deviceId' => 'browser-two-bbbbbbbb'], 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15');
        $delivery = NotificationDelivery::sole();
        $this->assertSame('signin.new_device', $delivery->event);
        $this->assertStringContainsString('Device: Safari on macOS', $delivery->body);
        // A client with no device ID is known by its user agent.
        $login([], 'cf/0.2.0');
        $login([], 'cf/0.2.0');
        $this->assertSame(2, NotificationDelivery::count());
    }

    public function test_a_suspension_and_its_end_send_an_email(): void
    {
        $this->panelServer();
        $reseller = $this->customer(null, ['role' => 'reseller', 'name' => 'Northwind', 'branding' => ['name' => 'Northwind Hosting', 'supportEmail' => 'help@northwind.example']]);
        $customer = $this->customer(null, ['reseller_id' => $reseller->id, 'name' => 'Acme Ltd']);
        $owner = $this->user(Role::Owner, $customer);
        $this->actingWith($this->user(Role::Reseller, $reseller));

        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => true])->assertOk();
        $delivery = NotificationDelivery::where('user_id', $owner->id)->sole();
        $this->assertSame('Your account Acme Ltd is suspended', $delivery->subject);
        $this->assertStringContainsString('speak to Northwind Hosting: help@northwind.example', $delivery->body);
        $this->patchJson("/api/v1/customers/{$customer->id}", ['suspended' => false])->assertOk();
        $this->assertSame(['account.suspended', 'account.restored'], NotificationDelivery::orderBy('created_at')->orderBy('id')->pluck('event')->all());
    }

    public function test_certificates_that_expire_soon_send_one_email_each(): void
    {
        $this->panelServer();
        $customer = $this->customer();
        $website = $this->website($customer, $this->server(), $this->image(), 'shop.example.com');
        $this->user(Role::Owner, $customer);
        $website->domains()->update(['certificate_state' => 'valid', 'certificate_expires_at' => now()->addDays(10)]);
        $this->website($customer, $this->server(), $this->image(), 'fine.example.com')->domains()->update(['certificate_state' => 'valid', 'certificate_expires_at' => now()->addDays(60)]);

        $this->artisan('cloudfactory:check-certificates')->assertSuccessful();
        $this->artisan('cloudfactory:check-certificates')->assertSuccessful();
        $delivery = NotificationDelivery::sole();
        $this->assertSame('Check the certificate of shop.example.com', $delivery->subject);
        $this->assertStringContainsString('(10 days)', $delivery->body);
    }

    public function test_an_offline_server_sends_an_email_to_the_administrators(): void
    {
        $this->panelServer();
        $admin = $this->user();
        $this->user(Role::Owner, $this->customer());
        $this->server(['name' => 'web-2', 'last_seen_at' => now()->subMinutes(10)]);
        $this->artisan('cloudfactory:check-jobs')->assertSuccessful();

        $delivery = NotificationDelivery::sole();
        $this->assertSame([$admin->id, 'The server web-2 is offline'], [$delivery->user_id, $delivery->subject]);
    }

    public function test_usage_alerts_from_the_alerts_feature_send_one_email_each(): void
    {
        $this->panelServer();
        $customer = $this->customer(null, ['name' => 'Acme Ltd']);
        $owner = $this->user(Role::Owner, $customer);
        $notifier = app(Notifier::class);
        $values = ['resource' => 'Disk', 'website' => 'shop.example.com', 'used' => '9.1 GB', 'limit' => '10 GB', 'percent' => 91, 'level' => 'warning'];
        $this->assertSame(1, $notifier->usageAlert($customer, $values, 'usage:a1:warning'));
        $this->assertSame(0, $notifier->usageAlert($customer, $values, 'usage:a1:warning'));
        $this->assertSame('Acme Ltd uses 91% of its Disk limit', NotificationDelivery::where('user_id', $owner->id)->sole()->subject);
    }

    public function test_each_user_sees_only_the_emails_of_their_account(): void
    {
        $this->panelServer();
        $reseller = $this->customer(null, ['role' => 'reseller']);
        $customer = $this->customer(null, ['reseller_id' => $reseller->id]);
        $owner = $this->user(Role::Owner, $customer);
        $colleague = $this->user(Role::Owner, $customer);
        $stranger = $this->user(Role::Owner, $this->customer());
        $admin = $this->user();
        $notifier = app(Notifier::class);
        $notifier->send(NotificationEvent::UsageAlert, [$owner, $stranger, $admin], ['resource' => 'Disk']);
        $notifier->newDevice($colleague, 'Firefox on Linux', '203.0.113.7');

        $this->actingWith($owner);
        $this->assertSame([$owner->email], array_column($this->getJson('/api/v1/notifications')->assertOk()->json('data'), 'recipient'));
        $hidden = NotificationDelivery::where('user_id', $colleague->id)->sole();
        $this->getJson("/api/v1/notifications/{$hidden->id}")->assertNotFound();
        $this->actingWith($colleague);
        $this->getJson("/api/v1/notifications/{$hidden->id}")->assertOk()->assertJsonPath('body', fn ($b) => str_contains($b, '203.0.113.7'));

        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->assertSame([$owner->email], array_column($this->getJson('/api/v1/notifications')->json('data'), 'recipient'));
        $this->actingWith($admin);
        $this->assertSame(4, $this->getJson('/api/v1/notifications')->json('meta.total'));
        $this->assertSame(1, $this->getJson('/api/v1/notifications?event=signin.new_device')->json('meta.total'));
    }

    public function test_a_token_needs_the_notifications_scope(): void
    {
        $this->actingWith($this->user(), ['websites:read']);
        $this->getJson('/api/v1/mail-server')->assertForbidden();
        $this->actingWith($this->user(), ['notifications:read']);
        $this->getJson('/api/v1/mail-server')->assertOk();
        $this->putJson('/api/v1/me/notification-preferences', ['events' => []])->assertForbidden();
    }
}
