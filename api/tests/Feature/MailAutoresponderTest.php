<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Models\Customer;
use App\Models\Mailbox;
use App\Models\MailDomain;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/**
 * The autoresponder of a mailbox. The mail server keeps it: the panel reads and changes it
 * through the agent, with no job, and keeps no copy.
 */
class MailAutoresponderTest extends TestCase
{
    private const AGENT = 'https://web-1.internal:7443/cloudfactory.agent.v1.AgentService/';

    private Customer $customer;

    private function mailbox(string $status = MailDomain::ACTIVE): Mailbox
    {
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cloudfactory-test-pki']);
        $this->customer = $this->customer();
        $server = $this->server(['roles' => ['web', 'email'], 'mail_hostname' => 'mail.host.example', 'mail_status' => 'running', 'certificate_pem' => 'test', 'certificate_fingerprint' => str_repeat('ab', 32)]);
        $domain = MailDomain::create(['customer_id' => $this->customer->id, 'server_id' => $server->id, 'name' => 'acme.example', 'status' => MailDomain::ACTIVE, 'records' => []]);

        return Mailbox::create(['customer_id' => $this->customer->id, 'mail_domain_id' => $domain->id, 'local_part' => 'info', 'quota_bytes' => 1024 ** 3, 'status' => $status]);
    }

    public function test_an_owner_reads_and_sets_the_autoresponder_of_a_mailbox(): void
    {
        $mailbox = $this->mailbox();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        Http::fake([
            // The JSON of the agent has no field with an empty value.
            self::AGENT.'GetAutoresponder' => Http::response(['text' => 'Back soon.', 'filterScript' => 'filters']),
            self::AGENT.'SetAutoresponder' => fn ($r) => Http::response($r['autoresponder']),
        ]);

        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder")->assertOk()->assertExactJson(['autoresponder' => [
            'enabled' => false, 'subject' => null, 'text' => 'Back soon.', 'startsAt' => null, 'endsAt' => null, 'filterScript' => 'filters',
        ]]);
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/GetAutoresponder') && $r['domain'] === 'acme.example' && $r['localPart'] === 'info');

        $this->putJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder", [
            'enabled' => true, 'subject' => 'Out of office', 'text' => "Hello,\n\nI am away.\n\tAnn",
            'startsAt' => '2026-12-20T09:00:00+01:00', 'endsAt' => '2027-01-04T23:59:59Z',
        ])->assertOk()->assertJsonPath('autoresponder.enabled', true)->assertJsonPath('autoresponder.startsAt', '2026-12-20T08:00:00Z');
        // The agent gets each time in UTC.
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetAutoresponder') && $r['autoresponder'] === [
            'enabled' => true, 'subject' => 'Out of office', 'text' => "Hello,\n\nI am away.\n\tAnn", 'startsAt' => '2026-12-20T08:00:00Z', 'endsAt' => '2027-01-04T23:59:59Z',
        ]);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Turned on autoresponder', 'target' => 'info@acme.example']);

        // Off, with no subject and no dates. The mail server keeps the text for the next time.
        $this->putJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder", ['enabled' => false, 'subject' => null, 'text' => 'Back soon.', 'startsAt' => null, 'endsAt' => null])
            ->assertOk()->assertJsonPath('autoresponder.subject', null);
        Http::assertSent(fn ($r) => str_ends_with($r->url(), '/SetAutoresponder') && $r['autoresponder'] === ['enabled' => false, 'subject' => '', 'text' => 'Back soon.', 'startsAt' => '', 'endsAt' => '']);
        $this->assertDatabaseHas('audit_entries', ['action' => 'Turned off autoresponder']);
    }

    public function test_the_panel_checks_the_autoresponder_before_it_calls_the_agent(): void
    {
        $mailbox = $this->mailbox();
        $this->actingWith($this->user());
        Http::fake([self::AGENT.'*' => Http::response([])]);
        $url = "/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder";
        $valid = ['enabled' => true, 'subject' => null, 'text' => 'Away', 'startsAt' => null, 'endsAt' => null];

        $this->putJson($url, ['enabled' => true, 'text' => 'Away'])->assertJsonValidationErrors(['subject', 'startsAt', 'endsAt']);
        $this->putJson($url, ['text' => ''] + $valid)->assertJsonValidationErrors(['text' => 'Type the text of the answer.']);
        $this->putJson($url, ['subject' => "Away\nBcc: someone@example.com"] + $valid)->assertJsonValidationErrors('subject');
        $this->putJson($url, ['subject' => str_repeat('a', 201)] + $valid)->assertJsonValidationErrors('subject');
        $this->putJson($url, ['text' => "Away\x1b[31m"] + $valid)->assertJsonValidationErrors('text');
        // The mail server accepts a text of less than 2048 bytes: a letter with an accent has two.
        $this->putJson($url, ['text' => str_repeat('é', 1001)] + $valid)->assertJsonValidationErrors('text');
        $this->putJson($url, ['startsAt' => '2026-12-20'] + $valid)->assertJsonValidationErrors('startsAt');
        $this->putJson($url, ['startsAt' => '2026-12-20T10:00:00+01:00', 'endsAt' => '2026-12-20T09:00:00Z'] + $valid)->assertJsonValidationErrors(['endsAt' => 'The end must be after the start.']);
        Http::assertNothingSent();

        $this->putJson($url, ['text' => str_repeat('a', 2000), 'endsAt' => '2027-01-04T23:59:59Z'] + $valid)->assertOk();
    }

    public function test_only_users_of_the_customer_use_the_autoresponder_of_an_active_mailbox(): void
    {
        $mailbox = $this->mailbox();
        Http::fake([self::AGENT.'*' => Http::response([])]);
        $valid = ['enabled' => false, 'subject' => null, 'text' => null, 'startsAt' => null, 'endsAt' => null];

        $this->actingWith($this->user(Role::Owner, $this->customer(null, ['name' => 'Other', 'email' => 'other@example.com'])));
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder")->assertNotFound();
        $this->putJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder", $valid)->assertNotFound();

        // A token that can only read mail does not change the autoresponder.
        $this->actingWith($this->user(Role::Owner, $this->customer), ['mail:read']);
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder")->assertOk();
        $this->putJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder", $valid)->assertForbidden();

        // An administrator reads the autoresponder of a suspended customer, but does not change it.
        $this->actingWith($this->user());
        $this->customer->update(['suspended' => true]);
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder")->assertOk();
        $this->putJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder", $valid)->assertStatus(409);
        $this->customer->update(['suspended' => false]);

        $mailbox->update(['status' => MailDomain::PENDING]);
        $this->getJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder")->assertJsonValidationErrors(['mailboxId' => 'The panel makes this mailbox. Wait until it is complete.']);
        $mailbox->update(['status' => MailDomain::REMOVING]);
        $this->putJson("/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder", $valid)->assertJsonValidationErrors(['mailboxId' => 'The panel removes this mailbox.']);
        Http::assertSentCount(2);
    }

    public function test_the_errors_of_the_agent_have_clear_messages(): void
    {
        $mailbox = $this->mailbox();
        $this->actingWith($this->user());
        $url = "/api/v1/mail/mailboxes/{$mailbox->id}/autoresponder";

        Http::fake([
            self::AGENT.'GetAutoresponder' => Http::sequence()
                ->push(['code' => 'not_found', 'message' => 'the mail server does not have this mailbox: info@acme.example'], 404)
                ->pushFailedConnection('cURL error 7: Failed to connect')
                ->push(['code' => 'failed_precondition', 'message' => 'this server has no mail server. Turn on email for the server first'], 400)
                // An agent of an earlier release does not have the method.
                ->push('404 page not found', 404),
            self::AGENT.'SetAutoresponder' => Http::response(['code' => 'invalid_argument', 'message' => 'the mail server refused the autoresponder: textBody'], 400),
        ]);
        $this->getJson($url)->assertNotFound()->assertJsonPath('message', 'The mail server does not have this mailbox.');
        $this->getJson($url)->assertStatus(503)->assertJsonPath('message', 'The panel cannot connect to the mail server of this mailbox. Try again later.');
        $this->getJson($url)->assertStatus(409)->assertJsonPath('message', 'This server has no mail server. Turn on email for the server first');
        $this->getJson($url)->assertStatus(503)->assertJsonPath('message', 'The agent of this server is from an earlier release. Update the agent of the server.');
        $this->putJson($url, ['enabled' => true, 'subject' => null, 'text' => 'Away', 'startsAt' => null, 'endsAt' => null])->assertStatus(422)->assertJsonPath('message', 'The mail server refused the autoresponder: textBody');
    }
}
