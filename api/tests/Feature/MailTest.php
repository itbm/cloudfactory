<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\Role;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\MailAlias;
use App\Models\Mailbox;
use App\Models\MailDomain;
use App\Models\Server;
use App\Services\JobOutcome;
use App\Services\MailDns;
use Tests\TestCase;

/**
 * Email hosting. All customers of a server use one mail server, and the panel keeps them apart:
 * a customer adds only a domain that it controls, and sees only its own mail domains, mailboxes
 * and addresses. The plan gives the number of mailboxes and their total size.
 */
class MailTest extends TestCase
{
    private const MB = 1024 ** 2;

    private const GB = 1024 ** 3;

    private Customer $customer;

    private Server $host;

    /** A customer with a verified website domain on a server with email turned on. */
    private function hosting(array $plan = []): void
    {
        $this->customer = $this->customer($this->plan($plan));
        $this->host = $this->server(['roles' => ['web', 'email'], 'mail_hostname' => 'mail.host.example', 'mail_status' => 'running']);
        $this->website($this->customer, $this->host, $this->image());
    }

    private function finish(string $jobId, JobState $state = JobState::Succeeded, array $result = []): AgentJob
    {
        $job = AgentJob::findOrFail($jobId);
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'failed' : null, $result);

        return $job->fresh();
    }

    private const RECORDS = [
        ['type' => 'MX', 'name' => 'acme.example', 'value' => '10 mail.host.example.', 'purpose' => 'mx'],
        ['type' => 'TXT', 'name' => 'acme.example', 'value' => 'v=spf1 mx -all', 'purpose' => 'spf'],
        ['type' => 'TXT', 'name' => 'v1-ed25519._domainkey.acme.example', 'value' => 'v=DKIM1; k=ed25519; p=abc', 'purpose' => 'dkim'],
        ['type' => 'TXT', 'name' => '_dmarc.acme.example', 'value' => 'v=DMARC1; p=reject', 'purpose' => 'dmarc'],
        ['type' => 'SRV', 'name' => '_imaps._tcp.acme.example', 'value' => '0 1 993 mail.host.example.', 'purpose' => 'other'],
    ];

    private function domain(string $name = 'acme.example'): MailDomain
    {
        $response = $this->postJson('/api/v1/mail/domains', ['name' => $name, 'customerId' => $this->customer->id])->assertAccepted();
        $this->finish($response->json('job.id'), result: ['records' => self::RECORDS]);

        return MailDomain::findOrFail($response->json('mailDomain.id'));
    }

    private function mailbox(MailDomain $domain, string $localPart = 'info', int $quota = self::GB): Mailbox
    {
        $response = $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => $localPart, 'quotaBytes' => $quota])->assertAccepted();
        $this->finish($response->json('job.id'));

        return Mailbox::findOrFail($response->json('mailbox.id'));
    }

    public function test_an_administrator_turns_on_email_for_a_server(): void
    {
        $this->actingWith($this->user());
        $server = $this->server(['roles' => ['web']]);

        $this->putJson("/api/v1/servers/{$server->id}/mail", ['hostname' => 'mail.host.example'])->assertUnprocessable()->assertJsonValidationErrors('hostname');
        $server->update(['roles' => ['web', 'email']]);
        $response = $this->putJson("/api/v1/servers/{$server->id}/mail", ['hostname' => 'mail.host.example'])->assertAccepted()
            ->assertJsonPath('server.mail.hostname', 'mail.host.example')->assertJsonPath('server.mail.status', 'starting');
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetMailServer, $job->action);
        $this->assertSame('mail.host.example', $job->payload['hostname']);
        // One change at a time.
        $this->putJson("/api/v1/servers/{$server->id}/mail", ['hostname' => 'mail.host.example'])->assertUnprocessable();

        $this->finish($job->id);
        $this->assertSame('running', $server->fresh()->mail_status);
        $this->getJson("/api/v1/servers/{$server->id}")->assertJsonPath('mail.status', 'running');
    }

    public function test_a_start_that_fails_marks_the_mail_server_and_can_run_again(): void
    {
        $this->actingWith($this->user());
        $server = $this->server(['roles' => ['email']]);
        $job = $this->putJson("/api/v1/servers/{$server->id}/mail", ['hostname' => 'mail.host.example'])->json('job.id');
        $this->finish($job, JobState::Failed);
        $this->assertSame('failed', $server->fresh()->mail_status);
        $this->putJson("/api/v1/servers/{$server->id}/mail", ['hostname' => 'mail.host.example'])->assertAccepted();
    }

    public function test_the_host_name_does_not_change_while_the_server_has_mail_domains(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $this->domain();

        $this->putJson("/api/v1/servers/{$this->host->id}/mail", ['hostname' => 'mx.other.example'])->assertUnprocessable()->assertJsonValidationErrors('hostname');
        $this->patchJson("/api/v1/servers/{$this->host->id}", ['roles' => ['web']])->assertUnprocessable()->assertJsonValidationErrors('roles');
        $this->deleteJson("/api/v1/servers/{$this->host->id}")->assertUnprocessable();
    }

    public function test_only_an_administrator_turns_on_email(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->putJson("/api/v1/servers/{$this->host->id}/mail", ['hostname' => 'mail.host.example'])->assertForbidden();
    }

    public function test_an_owner_adds_a_domain_of_one_of_its_websites(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));

        $response = $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example'])->assertAccepted()
            ->assertJsonPath('mailDomain.status', 'pending')->assertJsonPath('mailDomain.mailHost', 'mail.host.example');
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetMailDomain, $job->action);
        $this->assertSame($this->host->id, $job->server_id);
        $this->assertSame('acme.example', $job->payload['domain']);

        $this->finish($job->id, result: ['records' => self::RECORDS]);
        $shown = $this->getJson('/api/v1/mail/domains/'.$response->json('mailDomain.id'))->assertOk()->assertJsonPath('status', 'active')->json();
        // The panel keeps only the records that it can check.
        $this->assertSame(['mx', 'spf', 'dkim', 'dmarc'], array_column($shown['records'], 'purpose'));
        $this->assertNull($shown['records'][0]['found']);
        $this->assertFalse($shown['dnsReady']);
        // The owner sees the job of its mail domain.
        $this->getJson("/api/v1/jobs/{$job->id}")->assertOk();
        // A sub-domain of a verified domain needs no proof. The same name is refused.
        $this->postJson('/api/v1/mail/domains', ['name' => 'news.acme.example'])->assertAccepted();
        $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example'])->assertUnprocessable()->assertJsonValidationErrors('name');
    }

    public function test_a_customer_proves_that_it_controls_a_domain_with_no_website(): void
    {
        $this->hosting();
        $txt = [];
        $policy = $this->fakeTxt($txt);
        $this->actingWith($this->user(Role::Owner, $this->customer));

        $response = $this->postJson('/api/v1/mail/domains', ['name' => 'other.example'])->assertUnprocessable()
            ->assertJsonPath('verification.name', '_cloudfactory-challenge.other.example');
        $this->assertSame($policy->claimToken($this->customer->id, 'other.example'), $response->json('verification.value'));
        $this->assertSame(0, MailDomain::count());

        $txt['_cloudfactory-challenge.other.example'] = [$response->json('verification.value')];
        $this->postJson('/api/v1/mail/domains', ['name' => 'other.example'])->assertAccepted();
    }

    public function test_nobody_adds_the_panel_domain_or_a_mail_host_name(): void
    {
        $this->hosting();
        config(['cloudfactory.panel_domain' => 'panel.example']);
        $this->actingWith($this->user());
        foreach (['panel.example', 'mail.host.example'] as $name) {
            $this->postJson('/api/v1/mail/domains', ['name' => $name, 'customerId' => $this->customer->id])->assertUnprocessable()->assertJsonValidationErrors('name');
        }
    }

    public function test_a_mail_domain_needs_a_server_with_email(): void
    {
        $this->hosting();
        $this->host->update(['mail_status' => null, 'mail_hostname' => null]);
        $this->actingWith($this->user());
        $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example', 'customerId' => $this->customer->id])->assertUnprocessable()->assertJsonValidationErrors('serverId');
    }

    public function test_a_customer_sees_only_its_own_email(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $domain = $this->domain();
        $mailbox = $this->mailbox($domain);
        $alias = MailAlias::create(['customer_id' => $this->customer->id, 'mail_domain_id' => $domain->id, 'local_part' => 'sales', 'kind' => 'alias', 'destinations' => ['info@acme.example'], 'status' => 'active']);
        $job = AgentJob::where('customer_id', $this->customer->id)->firstOrFail();

        $other = $this->customer($this->customer->plan, ['name' => 'Other', 'email' => 'it@other.example']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson('/api/v1/mail/domains')->assertOk()->assertJsonCount(0, 'data');
        $this->getJson('/api/v1/mail/mailboxes')->assertOk()->assertJsonCount(0, 'data');
        $this->getJson('/api/v1/mail/aliases')->assertOk()->assertJsonCount(0, 'data');
        $this->getJson("/api/v1/mail/domains/{$domain->id}")->assertNotFound();
        $this->deleteJson("/api/v1/mail/domains/{$domain->id}")->assertNotFound();
        $this->patchJson("/api/v1/mail/mailboxes/{$mailbox->id}", ['newPassword' => true])->assertNotFound();
        $this->deleteJson("/api/v1/mail/mailboxes/{$mailbox->id}")->assertNotFound();
        $this->deleteJson("/api/v1/mail/aliases/{$alias->id}")->assertNotFound();
        $this->getJson("/api/v1/jobs/{$job->id}")->assertNotFound();
        // No mailbox and no address in the domain of a different customer.
        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'spy', 'quotaBytes' => self::GB])->assertUnprocessable()->assertJsonValidationErrors('domainId');
        $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'spy', 'kind' => 'forwarder', 'destinations' => ['spy@other.example']])->assertUnprocessable()->assertJsonValidationErrors('domainId');
        $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example'])->assertUnprocessable();

        // The reseller of a customer sees the email of that customer.
        $reseller = $this->customer($this->customer->plan, ['name' => 'Reseller', 'email' => 'r@reseller.example', 'role' => 'reseller']);
        $this->customer->update(['reseller_id' => $reseller->id]);
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->getJson('/api/v1/mail/mailboxes')->assertOk()->assertJsonCount(1, 'data')->assertJsonPath('data.0.address', 'info@acme.example');
    }

    public function test_a_mailbox_gets_a_password_one_time_and_the_agent_gets_only_a_hash(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $domain = $this->domain();

        $response = $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'info', 'quotaBytes' => 2 * self::GB])->assertAccepted()
            ->assertJsonPath('mailbox.address', 'info@acme.example')->assertJsonPath('mailbox.status', 'pending');
        $password = $response->json('password');
        $this->assertSame(20, strlen($password));
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetMailbox, $job->action);
        $this->assertSame(['domain' => 'acme.example', 'localPart' => 'info', 'quotaBytes' => 2 * self::GB], array_intersect_key($job->payload, ['domain' => 1, 'localPart' => 1, 'quotaBytes' => 1]));
        $this->assertTrue(password_verify($password, $job->payload['passwordHash']));
        $this->assertArrayNotHasKey('password', $job->payload);

        // The panel does not keep the hash after the job.
        $this->assertArrayNotHasKey('passwordHash', $this->finish($job->id)->payload);
        $this->getJson('/api/v1/mail/mailboxes')->assertJsonPath('data.0.status', 'active')->assertJsonMissingPath('data.0.password');

        // The user can set its own password.
        $own = $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'sales', 'quotaBytes' => self::GB, 'password' => 'correct horse battery'])->assertAccepted();
        $this->assertSame('correct horse battery', $own->json('password'));
        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'short', 'quotaBytes' => self::GB, 'password' => 'short'])->assertUnprocessable()->assertJsonValidationErrors('password');
    }

    public function test_the_address_of_a_mailbox_follows_the_shared_rule_and_is_free(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $domain = $this->domain();
        $this->mailbox($domain);

        foreach (['Info', 'a..b', '.a', 'a+b', 'a@b', ''] as $bad) {
            $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => $bad, 'quotaBytes' => self::GB])->assertUnprocessable()->assertJsonValidationErrors('localPart');
        }
        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'info', 'quotaBytes' => self::GB])->assertUnprocessable()->assertJsonValidationErrors('localPart');
        $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'info', 'kind' => 'forwarder', 'destinations' => ['a@b.example']])->assertUnprocessable()->assertJsonValidationErrors('localPart');
        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'tiny', 'quotaBytes' => self::MB])->assertUnprocessable()->assertJsonValidationErrors('quotaBytes');
    }

    public function test_the_plan_limits_the_number_and_the_total_size_of_mailboxes(): void
    {
        $this->hosting(['mailboxes' => 2, 'mail_storage_bytes' => 3 * self::GB]);
        $this->actingWith($this->user());
        $domain = $this->domain();
        $first = $this->mailbox($domain, 'one', 2 * self::GB);

        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'two', 'quotaBytes' => 2 * self::GB])->assertUnprocessable()->assertJsonValidationErrors('quotaBytes');
        $this->mailbox($domain, 'two', self::GB);
        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'three', 'quotaBytes' => 100 * self::MB])->assertUnprocessable()->assertJsonValidationErrors('localPart');
        // A larger mailbox must be in the limit too. A smaller mailbox is always permitted.
        $this->patchJson("/api/v1/mail/mailboxes/{$first->id}", ['quotaBytes' => 3 * self::GB])->assertUnprocessable()->assertJsonValidationErrors('quotaBytes');
        $this->patchJson("/api/v1/mail/mailboxes/{$first->id}", ['quotaBytes' => self::GB])->assertAccepted();

        $this->getJson('/api/v1/plans')->assertJsonPath('data.0.mailStorageBytes', 3 * self::GB);
        // The customer cannot move to a plan with fewer mailboxes than it has.
        $small = $this->plan(['name' => 'Small', 'mailboxes' => 1]);
        $this->patchJson("/api/v1/customers/{$this->customer->id}", ['planId' => $small->id])->assertUnprocessable()->assertJsonValidationErrors('planId');
    }

    public function test_a_change_of_a_mailbox_sends_the_new_size_or_a_new_password(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $mailbox = $this->mailbox($this->domain());

        $this->patchJson("/api/v1/mail/mailboxes/{$mailbox->id}", [])->assertOk()->assertJsonPath('job', null);
        $response = $this->patchJson("/api/v1/mail/mailboxes/{$mailbox->id}", ['quotaBytes' => 2 * self::GB])->assertAccepted()->assertJsonPath('password', null);
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(2 * self::GB, $job->payload['quotaBytes']);
        $this->assertArrayNotHasKey('passwordHash', $job->payload);
        // One change at a time.
        $this->patchJson("/api/v1/mail/mailboxes/{$mailbox->id}", ['newPassword' => true])->assertUnprocessable();
        // A change that failed keeps the old size.
        $this->finish($job->id, JobState::Failed);
        $this->assertSame(self::GB, $mailbox->fresh()->quota_bytes);

        $response = $this->patchJson("/api/v1/mail/mailboxes/{$mailbox->id}", ['newPassword' => true])->assertAccepted();
        $this->assertTrue(password_verify($response->json('password'), AgentJob::findOrFail($response->json('job.id'))->payload['passwordHash']));
    }

    public function test_a_mailbox_that_the_agent_could_not_make_is_failed_and_can_be_removed(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $domain = $this->domain();
        $response = $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'info', 'quotaBytes' => self::GB]);
        $this->finish($response->json('job.id'), JobState::Failed);
        $id = $response->json('mailbox.id');
        $this->assertSame('failed', Mailbox::findOrFail($id)->status);
        $this->patchJson("/api/v1/mail/mailboxes/{$id}", ['newPassword' => true])->assertUnprocessable();

        $job = $this->deleteJson("/api/v1/mail/mailboxes/{$id}")->assertAccepted()->json('job.id');
        $this->assertSame('removing', Mailbox::findOrFail($id)->status);
        $this->deleteJson("/api/v1/mail/mailboxes/{$id}")->assertUnprocessable();
        // A removal that failed keeps the mailbox in its old state.
        $this->finish($job, JobState::Failed);
        $this->assertSame('failed', Mailbox::findOrFail($id)->status);
        $this->finish($this->deleteJson("/api/v1/mail/mailboxes/{$id}")->json('job.id'));
        $this->assertNull(Mailbox::find($id));
    }

    public function test_an_alias_is_for_one_mailbox_of_the_customer_and_a_forwarder_for_a_list(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $domain = $this->domain();
        $this->mailbox($domain);

        $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'sales', 'kind' => 'alias', 'destinations' => ['someone@other.example']])->assertUnprocessable()->assertJsonValidationErrors('destinations');
        $response = $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'sales', 'kind' => 'alias', 'destinations' => ['info@acme.example']])->assertAccepted()
            ->assertJsonPath('mailAlias.address', 'sales@acme.example');
        $job = AgentJob::findOrFail($response->json('job.id'));
        $this->assertSame(AgentAction::SetMailAlias, $job->action);
        $this->assertSame(['info@acme.example'], $job->payload['destinations']);
        $this->finish($job->id);

        $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'team', 'kind' => 'forwarder', 'destinations' => ['team@acme.example']])->assertUnprocessable()->assertJsonValidationErrors('destinations');
        $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'team', 'kind' => 'forwarder', 'destinations' => ['not an address']])->assertUnprocessable();
        $forwarder = $this->postJson('/api/v1/mail/aliases', ['domainId' => $domain->id, 'localPart' => 'team', 'kind' => 'forwarder', 'destinations' => ['info@acme.example', 'ann@other.example']])->assertAccepted();
        $this->finish($forwarder->json('job.id'));
        $this->getJson('/api/v1/mail/aliases')->assertJsonCount(2, 'data')->assertJsonPath('data.1.kind', 'forwarder')->assertJsonPath('data.1.status', 'active');

        // A domain with addresses stays.
        $this->deleteJson("/api/v1/mail/domains/{$domain->id}")->assertUnprocessable();
        $remove = $this->deleteJson('/api/v1/mail/aliases/'.$forwarder->json('mailAlias.id'))->assertAccepted();
        $this->assertSame(AgentAction::DeleteMailAlias, AgentJob::findOrFail($remove->json('job.id'))->action);
        $this->finish($remove->json('job.id'));
        $this->assertSame(1, MailAlias::count());
    }

    public function test_an_empty_mail_domain_can_be_removed(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $domain = $this->domain();

        $job = $this->deleteJson("/api/v1/mail/domains/{$domain->id}")->assertAccepted()->json('job.id');
        $this->assertSame(AgentAction::DeleteMailDomain, AgentJob::findOrFail($job)->action);
        $this->getJson("/api/v1/mail/domains/{$domain->id}")->assertJsonPath('status', 'removing');
        $this->postJson('/api/v1/mail/mailboxes', ['domainId' => $domain->id, 'localPart' => 'info', 'quotaBytes' => self::GB])->assertUnprocessable();
        $this->finish($job);
        $this->assertNull(MailDomain::find($domain->id));
    }

    public function test_a_mail_domain_that_failed_can_be_added_again(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $response = $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example', 'customerId' => $this->customer->id]);
        $id = $response->json('mailDomain.id');
        $this->finish($response->json('job.id'), JobState::Failed);
        $this->assertSame('failed', MailDomain::findOrFail($id)->status);

        $this->finish($this->postJson("/api/v1/mail/domains/{$id}/retry")->assertAccepted()->json('job.id'), result: ['records' => self::RECORDS]);
        $this->assertSame('active', MailDomain::findOrFail($id)->status);
        $this->postJson("/api/v1/mail/domains/{$id}/retry")->assertUnprocessable();
    }

    public function test_the_panel_checks_the_dns_records_of_a_mail_domain(): void
    {
        $this->hosting();
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $domain = $this->domain();
        $dns = new class extends MailDns
        {
            public array $zone = [];

            protected function lookup(string $name, string $type): array
            {
                return $this->zone["{$type} {$name}"] ?? [];
            }
        };
        $this->app->instance(MailDns::class, $dns);

        $dns->zone = ['MX acme.example' => ['mail.host.example'], 'TXT acme.example' => ['google-site-verification=1', 'v=spf1 mx include:_spf.example.net ~all']];
        $shown = $this->postJson("/api/v1/mail/domains/{$domain->id}/check")->assertOk()->assertJsonPath('dnsReady', false)->json();
        $this->assertSame([true, true, false, false], array_column($shown['records'], 'found'));
        $this->assertNotNull($shown['dnsCheckedAt']);

        $dns->zone += ['TXT v1-ed25519._domainkey.acme.example' => ['v=DKIM1; k=ed25519;p=abc'], 'TXT _dmarc.acme.example' => ['v=DMARC1; p=none']];
        $this->postJson("/api/v1/mail/domains/{$domain->id}/check")->assertOk()->assertJsonPath('dnsReady', true);
        // A different mail server in the MX record.
        $dns->zone['MX acme.example'] = ['mx.elsewhere.example'];
        $this->postJson("/api/v1/mail/domains/{$domain->id}/check")->assertJsonPath('records.0.found', false);
    }

    public function test_the_report_of_the_agent_gives_the_use_of_each_mailbox(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $mailbox = $this->mailbox($this->domain());
        // A mailbox with the same address on a different server does not change.
        $elsewhere = $this->server(['roles' => ['email'], 'mail_hostname' => 'mx2.host.example', 'mail_status' => 'running', 'agent_token_hash' => hash('sha256', 'cfa_other'), 'local' => true, 'certificate_fingerprint' => self::LOCAL_FINGERPRINT]);
        $this->host->update(['agent_token_hash' => hash('sha256', 'cfa_token'), 'local' => true, 'certificate_fingerprint' => self::LOCAL_FINGERPRINT]);
        $report = ['agentVersion' => '0.1.0', 'mail' => ['state' => 'running', 'mailboxes' => [['address' => 'info@acme.example', 'usedBytes' => 12345]]]];

        $this->asLocalAgent('cfa_other')->postJson('/api/agent/v1/report', $report)->assertOk();
        $this->assertSame(0, $mailbox->fresh()->used_bytes);
        $this->asLocalAgent('cfa_token')->postJson('/api/agent/v1/report', $report)->assertOk();
        $this->assertSame(12345, $mailbox->fresh()->used_bytes);

        $this->asLocalAgent('cfa_token')->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'mail' => ['state' => 'stopped']])->assertOk();
        $this->assertSame('stopped', $this->host->fresh()->mail_status);
        $this->assertSame('running', $elsewhere->fresh()->mail_status);
    }

    public function test_a_token_needs_the_mail_scope(): void
    {
        $this->hosting();
        $user = $this->user(Role::Owner, $this->customer);
        $this->actingWith($user, ['websites:write']);
        $this->getJson('/api/v1/mail/domains')->assertForbidden();
        $this->actingWith($user, ['mail:read']);
        $this->getJson('/api/v1/mail/domains')->assertOk();
        $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example'])->assertForbidden();
        $this->actingWith($user, ['mail:write']);
        $this->postJson('/api/v1/mail/domains', ['name' => 'acme.example'])->assertAccepted();
    }

    public function test_the_account_shows_the_use_of_the_mail_limits(): void
    {
        $this->hosting();
        $this->actingWith($this->user());
        $this->mailbox($this->domain(), 'info', 2 * self::GB);
        $this->actingWith($this->user(Role::Owner, $this->customer));
        $this->getJson('/api/v1/account')->assertOk()->assertJsonPath('usage.mailboxes', 1)->assertJsonPath('usage.mailStorageBytes', 2 * self::GB)
            ->assertJsonPath('plan.mailStorageBytes', 10 * self::GB);
    }
}
