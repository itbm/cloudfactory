<?php

namespace Tests\Feature;

use App\Enums\AgentAction;
use App\Enums\DomainKind;
use App\Enums\JobState;
use App\Enums\Role;
use App\Jobs\SendJobToAgent;
use App\Models\AgentJob;
use App\Models\Certificate;
use App\Models\Customer;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Models\MailServer;
use App\Models\NotificationDelivery;
use App\Models\Server;
use App\Models\Setting;
use App\Models\Website;
use App\Services\AgentClient;
use App\Services\Certificates\Acme;
use App\Services\Certificates\AcmeOrders;
use App\Services\Certificates\Certificates;
use App\Services\Certificates\DnsProviders;
use App\Services\Certificates\PanelDns;
use App\Services\JobDispatcher;
use App\Services\JobOutcome;
use Illuminate\Http\Client\Request as HttpRequest;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\Http;
use Tests\TestCase;

/**
 * The certificates of the customers (docs/certificates.md): uploads, certificates with DNS
 * validation from an ACME CA, and the certificates that each server gets.
 */
class CertificateTest extends TestCase
{
    private const ACME = 'https://acme.test';

    private Customer $customer;

    private Server $web;

    private Website $website;

    /** The state of the fake ACME CA. */
    private array $ca = [];

    /** @var array{refuseOrder?: string, invalid?: string} */
    private array $caOptions = [];

    /** A customer with a website "shop.example.com" on a web server. An administrator is signed in. */
    private function hosting(): void
    {
        $this->customer = $this->customer();
        $this->web = $this->server(['name' => 'web-1']);
        $this->website = $this->website($this->customer, $this->web, $this->image(), 'shop.example.com');
        $this->actingWith($this->user());
    }

    /**
     * A certificate and its key in PEM format, signed by the issuer or self-signed.
     *
     * @param  list<string>  $names
     * @return array{cert: string, key: string}
     */
    private static function pem(array $names, ?array $issuer = null, int $days = 90, bool $ca = false, ?\OpenSSLAsymmetricKey $key = null, ?string $csr = null): array
    {
        $key ??= openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $config = tempnam(sys_get_temp_dir(), 'cf-test-cert');
        file_put_contents($config, "[req]\ndistinguished_name = dn\nprompt = no\n[dn]\n[ext]\n"
            .($ca ? "basicConstraints = critical,CA:TRUE\nkeyUsage = critical,keyCertSign,cRLSign\n" : 'subjectAltName = '.implode(',', array_map(fn ($n) => "DNS:{$n}", $names))."\nbasicConstraints = CA:FALSE\nextendedKeyUsage = serverAuth\n"));
        try {
            $request = $csr ?? openssl_csr_new(['commonName' => $ca ? 'Test CA' : $names[0], 'organizationName' => $ca ? 'Test Authority' : 'Acme Ltd'], $key, ['config' => $config, 'digest_alg' => 'sha256']);
            $cert = openssl_csr_sign($request, $issuer['cert'] ?? null, $issuer['key'] ?? $key, $days, ['config' => $config, 'x509_extensions' => 'ext', 'digest_alg' => 'sha256'], random_int(1, PHP_INT_MAX));
            openssl_x509_export($cert, $certPem);
            openssl_pkey_export($key, $keyPem);
        } finally {
            unlink($config);
        }

        return ['cert' => $certPem, 'key' => $keyPem];
    }

    /** @return array{cert: string, key: string} */
    private static function authority(): array
    {
        return self::pem([], ca: true, days: 3650);
    }

    /** Removes the jobs that the set-up made, so that a test sees only its own jobs. */
    private function certificateJobs(): Collection
    {
        return AgentJob::where('action', AgentAction::SetCertificates->value)->orderBy('created_at')->get();
    }

    private function finish(AgentJob $job, JobState $state = JobState::Succeeded, array $result = []): void
    {
        app(JobOutcome::class)->finish($job, $state, $state === JobState::Failed ? 'caddy: the certificate did not load' : null, $result);
    }

    /** The agent of the server sends a report with the certificates of its edge proxy. */
    private function report(Server $server, ?array $certificates): void
    {
        $server->update(['agent_token_hash' => hash('sha256', "cfa_{$server->id}"), 'local' => true]);
        config(['cloudfactory.agent.direct_addresses' => ['127.0.0.1']]);
        $this->withToken("cfa_{$server->id}")->postJson('/api/agent/v1/report', ['agentVersion' => '0.1.0', 'certificates' => $certificates])->assertOk();
    }

    /** A usable certificate of a customer, with no real chain: for the tests of the matching. */
    private function certificate(Customer $customer, array $names, array $attributes = []): Certificate
    {
        return Certificate::create($attributes + [
            'customer_id' => $customer->id, 'source' => Certificate::UPLOAD, 'names' => $names,
            'certificate' => "-----BEGIN CERTIFICATE-----\n".implode(',', $names)."\n-----END CERTIFICATE-----\n", 'private_key' => 'key',
            'fingerprint' => hash('sha256', implode(',', $names)), 'issuer' => 'Test', 'not_before' => now()->subDay(), 'expires_at' => now()->addDays(60), 'trusted' => true,
        ]);
    }

    /** @return array<string, list<string>> certificate names => hosts */
    private function onServer(Server $server): array
    {
        $out = [];
        foreach (app(Certificates::class)->forServer($server) as $e) {
            $out[implode(',', $e['certificate']->names)] = $e['hosts'];
        }

        return $out;
    }

    public function test_an_owner_uploads_a_certificate_and_the_server_of_the_website_gets_it(): void
    {
        $this->hosting();
        $owner = $this->actingWith($this->user(Role::Owner, $this->customer));
        $ca = self::authority();
        $leaf = self::pem(['shop.example.com', 'www.example.com'], $ca);

        // The chain can have the certificates in any order. The panel keeps the leaf first.
        $response = $this->postJson('/api/v1/certificates', ['source' => 'upload', 'certificate' => $ca['cert'].$leaf['cert'], 'privateKey' => $leaf['key']])
            ->assertCreated()
            ->assertJsonPath('source', 'upload')
            ->assertJsonPath('customerId', $this->customer->id)
            ->assertJsonPath('names', ['shop.example.com', 'www.example.com'])
            ->assertJsonPath('state', 'valid')
            ->assertJsonPath('issuer', 'Test Authority (Test CA)')
            ->assertJsonPath('trusted', false)
            ->assertJsonMissingPath('privateKey');
        $certificate = Certificate::findOrFail($response->json('id'));
        $this->assertSame($owner->id, $certificate->user_id);
        $this->assertStringStartsWith($leaf['cert'], $certificate->certificate);
        $this->assertSame(openssl_x509_fingerprint($leaf['cert'], 'sha256'), $certificate->fingerprint);
        // The private key is encrypted in the database, and no answer has it.
        $this->assertStringNotContainsString('PRIVATE KEY', (string) $certificate->getRawOriginal('private_key'));
        $this->getJson("/api/v1/certificates/{$certificate->id}")->assertOk()->assertJsonMissingPath('privateKey')->assertJsonPath('certificatePem', $certificate->certificate);

        // The server of the website gets the certificate for the domain of the website.
        $job = $this->certificateJobs()->sole();
        $this->assertSame($this->web->id, $job->server_id);
        $this->assertSame([], $job->payload);
        $this->assertSame([['certificate' => $certificate->id, 'hosts' => ['shop.example.com']]],
            array_map(fn ($e) => ['certificate' => $e['certificate']->id, 'hosts' => $e['hosts']], app(Certificates::class)->forServer($this->web)));

        // The private key goes to the agent when the panel sends the job. It is not in the job.
        config(['cloudfactory.agent.certificates' => sys_get_temp_dir().'/cf-test-agents-'.uniqid()]);
        $this->web->update(['certificate_pem' => 'x', 'certificate_fingerprint' => str_repeat('a', 64)]);
        Http::fake(['*' => Http::response(['jobId' => $job->id])]);
        (new SendJobToAgent($job->id))->handle(app(AgentClient::class));
        Http::assertSent(fn (HttpRequest $r) => str_ends_with($r->url(), '/cloudfactory.agent.v1.AgentService/SetCertificates')
            && $r->data()['jobId'] === $job->id
            && $r->data()['certificates'] === [['id' => $certificate->id, 'hosts' => ['shop.example.com'], 'certificatePem' => $certificate->certificate, 'privateKeyPem' => $certificate->private_key]]);

        // The result of the job shows the certificate on the domain.
        $this->finish($job, result: ['certificates' => [['id' => $certificate->id, 'sha256' => strtoupper($certificate->fingerprint), 'hosts' => ['shop.example.com']]]]);
        $this->assertSame([['id' => $certificate->id, 'sha256' => $certificate->fingerprint, 'hosts' => ['shop.example.com']]], $this->web->fresh()->agent_certificates);
        $this->getJson("/api/v1/websites/{$this->website->id}")->assertOk()
            ->assertJsonPath('data.domains.0.certificate.source', 'upload')
            ->assertJsonPath('data.domains.0.certificate.certificateId', $certificate->id);
        $this->getJson('/api/v1/certificates')->assertOk()
            ->assertJsonPath('data.0.usedBy', [['domain' => 'shop.example.com', 'websiteId' => $this->website->id, 'websiteName' => 'shop.example.com', 'serverName' => 'web-1']]);

        // A different customer does not see the certificate.
        $other = $this->customer(attributes: ['name' => 'Other', 'email' => 'it@other.example']);
        $this->actingWith($this->user(Role::Owner, $other));
        $this->getJson("/api/v1/certificates/{$certificate->id}")->assertNotFound();
        $this->getJson('/api/v1/certificates')->assertOk()->assertJsonCount(0, 'data');
        $this->deleteJson("/api/v1/certificates/{$certificate->id}")->assertNotFound();
    }

    public function test_the_panel_checks_an_uploaded_certificate(): void
    {
        $this->hosting();
        $leaf = self::pem(['shop.example.com']);
        $other = self::pem(['shop.example.com']);
        $upload = fn (string $cert, string $key) => $this->postJson('/api/v1/certificates', ['source' => 'upload', 'customerId' => $this->customer->id, 'certificate' => $cert, 'privateKey' => $key]);

        $upload('not a certificate', $leaf['key'])->assertUnprocessable()->assertJsonPath('errors.certificate.0', 'Paste the certificate in PEM format. It starts with "-----BEGIN CERTIFICATE-----".');
        $upload($leaf['cert'], 'not a key')->assertUnprocessable()->assertJsonPath('errors.privateKey.0', 'Paste the private key in PEM format. It starts with "-----BEGIN PRIVATE KEY-----".');
        $upload($leaf['cert'], $other['key'])->assertUnprocessable()->assertJsonPath('errors.privateKey.0', 'The private key is not the key of a certificate in the chain.');
        openssl_pkey_export(openssl_pkey_get_private($leaf['key']), $encrypted, 'secret');
        $upload($leaf['cert'], $encrypted)->assertUnprocessable()->assertJsonValidationErrors('privateKey');
        $expired = self::pem(['shop.example.com'], days: 1);
        $this->travel(2)->days();
        $upload($expired['cert'], $expired['key'])->assertUnprocessable()->assertJsonValidationErrors('certificate');
        $this->travelBack();
        $short = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_RSA, 'private_key_bits' => 1024]);
        $weak = self::pem(['shop.example.com'], key: $short);
        $upload($weak['cert'], $weak['key'])->assertUnprocessable()->assertJsonPath('errors.privateKey.0', 'The RSA key is too short. Use a key with 2048 bits or more.');
        // A certificate must not be valid for a name of the panel or of a server.
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        $wildcard = self::pem(['*.example.com']);
        $upload($wildcard['cert'], $wildcard['key'])->assertUnprocessable()
            ->assertJsonPath('errors.certificate.0', 'The certificate is valid for panel.example.com, a name of the panel. Use a certificate without this name.');
        $this->assertSame(0, Certificate::count());

        // The number of certificates of a customer has a limit.
        config(['cloudfactory.certificates_per_customer' => 1]);
        $upload($leaf['cert'], $leaf['key'])->assertCreated();
        $upload($other['cert'], $other['key'])->assertUnprocessable()->assertJsonPath('errors.certificate.0', 'A customer can have 1 certificates. Remove a certificate first.');

        // A suspended customer cannot add a certificate.
        $this->customer->update(['suspended' => true]);
        $upload($other['cert'], $other['key'])->assertStatus(409);
    }

    public function test_an_uploaded_certificate_can_be_replaced_and_removed(): void
    {
        $this->hosting();
        $first = self::pem(['shop.example.com']);
        $id = $this->postJson('/api/v1/certificates', ['source' => 'upload', 'customerId' => $this->customer->id, 'certificate' => $first['cert'], 'privateKey' => $first['key']])->assertCreated()->json('id');
        $next = self::pem(['shop.example.com', 'blog.example.com'], days: 365);

        $this->putJson("/api/v1/certificates/{$id}", ['certificate' => $next['cert'], 'privateKey' => $next['key']])->assertOk()
            ->assertJsonPath('id', $id)->assertJsonPath('names', ['shop.example.com', 'blog.example.com']);
        $this->assertSame(openssl_x509_fingerprint($next['cert'], 'sha256'), Certificate::findOrFail($id)->fingerprint);
        $this->postJson("/api/v1/certificates/{$id}/renew")->assertUnprocessable()->assertJsonPath('errors.certificate.0', 'Upload a new certificate to replace an uploaded certificate.');
        $this->assertCount(2, $this->certificateJobs());

        $this->deleteJson("/api/v1/certificates/{$id}")->assertAccepted()->assertJsonPath('jobs', 1);
        $this->assertSame(0, Certificate::count());
        $this->assertCount(3, $this->certificateJobs());
        $this->assertSame([], app(Certificates::class)->forServer($this->web));
    }

    public function test_each_domain_uses_the_best_certificate_of_its_customer(): void
    {
        $this->hosting();
        $this->website->domains()->createMany([
            ['name' => 'www.example.com', 'kind' => DomainKind::Alias, 'verification_token' => 'cf-test', 'verified_at' => now()],
            ['name' => 'example.com', 'kind' => DomainKind::Alias, 'verification_token' => 'cf-test', 'verified_at' => now()],
            ['name' => 'a.b.example.com', 'kind' => DomainKind::Alias, 'verification_token' => 'cf-test', 'verified_at' => now()],
            // A domain that is not verified does not get a certificate.
            ['name' => 'new.example.com', 'kind' => DomainKind::Alias, 'verification_token' => 'cf-test'],
        ]);
        $wildcard = $this->certificate($this->customer, ['*.example.com'], ['expires_at' => now()->addDays(80)]);
        $this->certificate($this->customer, ['shop.example.com'], ['expires_at' => now()->addDays(20)]);
        // An expired certificate and a certificate with no chain (a first order) are not usable.
        $this->certificate($this->customer, ['example.com'], ['expires_at' => now()->subDay()]);
        $this->certificate($this->customer, ['example.com'], ['source' => Certificate::ACME, 'certificate' => null, 'private_key' => null, 'expires_at' => null]);

        // A certificate with the exact name comes first. A wildcard is valid for one label only.
        $this->assertSame(['*.example.com' => ['www.example.com'], 'shop.example.com' => ['shop.example.com']], $this->onServer($this->web));

        // Of two certificates of the same kind, the certificate that expires last.
        $longer = $this->certificate($this->customer, ['*.example.com', 'example.com'], ['expires_at' => now()->addDays(85)]);
        $this->assertSame(['shop.example.com' => ['shop.example.com'], '*.example.com,example.com' => ['example.com', 'www.example.com']], $this->onServer($this->web));
        $this->assertSame($longer->id, Certificates::forHost(Certificate::all(), 'www.example.com')->id);
        $this->assertNotSame($wildcard->id, Certificates::forHost(Certificate::all(), 'www.example.com')->id);
    }

    public function test_a_certificate_does_not_go_to_a_server_where_it_is_valid_for_a_name_of_a_different_customer(): void
    {
        $this->hosting();
        $other = $this->customer(attributes: ['name' => 'Other', 'email' => 'it@other.example']);
        $this->website($other, $this->web, $this->image(['reference' => 'ghcr.io/cloudfactory/frankenphp-other']), 'blog.example.com');

        // The certificate of the customer is also valid for the domain of the other customer. The
        // edge proxy could send it for that domain, so the server does not get it.
        $both = $this->certificate($this->customer, ['shop.example.com', 'blog.example.com']);
        $this->assertSame([], $this->onServer($this->web));

        // The other customer has a certificate for its domain now: each domain has its own
        // certificate, so the first certificate goes to the server.
        $this->certificate($other, ['blog.example.com']);
        $this->assertSame(['shop.example.com,blog.example.com' => ['shop.example.com'], 'blog.example.com' => ['blog.example.com']], $this->onServer($this->web));
        // A certificate of one customer is never used for a domain of a different customer.
        $this->assertNull(Certificates::forHost(Certificate::where('customer_id', $other->id)->get(), 'shop.example.com'));
        $this->assertSame($both->id, Certificates::forHost(Certificate::where('customer_id', $this->customer->id)->get(), 'shop.example.com')->id);

        // A certificate that is valid for a host name of a server or of the panel never goes to a server.
        Certificate::query()->delete();
        $this->certificate($this->customer, ['shop.example.com', 'web-1.internal']);
        $this->assertSame([], $this->onServer($this->web));
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        Certificate::query()->delete();
        $this->certificate($this->customer, ['*.example.com']);
        $this->assertSame([], $this->onServer($this->web));
    }

    public function test_the_email_for_an_uploaded_certificate_that_expires_soon_tells_the_customer_to_replace_it(): void
    {
        $this->hosting();
        $this->user(Role::Owner, $this->customer);
        // The panel sends emails through an SMTP server.
        MailServer::create(['host' => 'smtp.example.com', 'port' => 587, 'encryption' => 'starttls', 'username' => 'panel', 'password' => 'secret', 'from_address' => 'panel@example.com', 'from_name' => 'Example Hosting']);
        $certificate = $this->certificate($this->customer, ['shop.example.com'], ['expires_at' => now()->addDays(10)]);
        $this->web->update(['agent_certificates' => [['id' => $certificate->id, 'sha256' => $certificate->fingerprint, 'hosts' => ['shop.example.com']]]]);
        $this->website->domains()->update(['certificate_state' => 'valid', 'certificate_expires_at' => $certificate->expires_at]);

        $this->artisan('cloudfactory:check-certificates')->assertSuccessful();
        $body = NotificationDelivery::firstOrFail()->body;
        $this->assertStringContainsString('You uploaded this certificate to the panel, so the panel cannot renew it.', $body);
        $this->assertStringNotContainsString('Check DNS', $body);
    }

    public function test_the_panel_sends_the_certificates_again_when_a_server_reports_other_certificates(): void
    {
        $this->hosting();
        $certificate = $this->certificate($this->customer, ['shop.example.com']);
        $wanted = [['id' => $certificate->id, 'sha256' => $certificate->fingerprint, 'hosts' => ['shop.example.com']]];

        // The edge proxy lost its certificates, for example after a restart.
        $this->report($this->web, []);
        $job = $this->certificateJobs()->sole();
        $this->assertSame("Send the certificates to {$this->web->name} again", $job->title);
        $this->assertSame([], $this->web->fresh()->agent_certificates);
        // The job is in the queue: the next report makes no new job.
        $this->report($this->web, []);
        $this->assertCount(1, $this->certificateJobs());

        // The job failed. The panel waits 1 minute, then 2 and so on.
        $this->finish($job, JobState::Failed);
        $this->report($this->web, []);
        $this->assertCount(1, $this->certificateJobs());
        $this->travel(61)->seconds();
        $this->report($this->web, []);
        $this->assertCount(2, $this->certificateJobs());
        $this->finish($this->certificateJobs()->last(), JobState::Failed);
        $this->travel(61)->seconds();
        $this->report($this->web, []);
        $this->assertCount(2, $this->certificateJobs());
        $this->travel(60)->seconds();
        $this->report($this->web, []);
        $this->assertCount(3, $this->certificateJobs());
        $this->finish($this->certificateJobs()->last(), result: ['certificates' => $wanted]);

        // The server has the certificates now. A report with the same certificates makes no job.
        $this->report($this->web, $wanted);
        $this->assertNull($this->web->fresh()->certificates_resync);
        // An agent that could not read the certificates sends no list. The panel does not compare.
        $this->report($this->web, null);
        $this->assertCount(3, $this->certificateJobs());
        $this->assertSame($wanted, $this->web->fresh()->agent_certificates);

        // A new domain on the server: the server gets the certificates when the job of the domain is complete.
        $this->certificate($this->customer, ['*.example.com']);
        $add = app(JobDispatcher::class)->dispatch($this->web, AgentAction::AddDomain, ['websiteId' => $this->website->id, 'domain' => 'www.example.com'], 'Add domain www.example.com', $this->website);
        $this->finish($add);
        $this->assertCount(4, $this->certificateJobs());
    }

    /**
     * A fake ACME CA. It checks the signature of each request with the account key, and the
     * TXT record of each challenge when the panel says that the record is ready.
     *
     * @param  array{refuseOrder?: string, invalid?: string}  $options
     */
    private function fakeCa(array $options = []): void
    {
        $this->caOptions = $options;
        if ($this->ca !== []) {
            // The CA keeps its account and its state. Only its answers change.
            $this->ca['certificate'] = null;

            return;
        }
        config(['cloudfactory.acme.directory' => self::ACME.'/directory', 'cloudfactory.acme.email' => 'certs@hosting.example']);
        $this->ca = ['authority' => self::authority(), 'nonce' => 0, 'jwk' => null, 'requests' => [], 'authz' => [], 'names' => [], 'certificate' => null];
        Http::fake(function (HttpRequest $r) {
            $options = $this->caOptions;
            $url = $r->url();
            $nonce = ['Replay-Nonce' => 'nonce-'.(++$this->ca['nonce'])];
            if ($url === self::ACME.'/directory') {
                return Http::response(['newNonce' => self::ACME.'/nonce', 'newAccount' => self::ACME.'/account', 'newOrder' => self::ACME.'/order']);
            }
            if ($url === self::ACME.'/nonce') {
                return Http::response('', 200, $nonce);
            }
            $jws = json_decode($r->body(), true);
            $protected = json_decode(self::b64($jws['protected']), true);
            $payload = $jws['payload'] === '' ? null : json_decode(self::b64($jws['payload']), true);
            $this->assertSame('application/jose+json', $r->header('Content-Type')[0]);
            $this->assertSame($url, $protected['url']);
            $this->assertSame('ES256', $protected['alg']);
            if (isset($protected['jwk'])) {
                $this->ca['jwk'] = $protected['jwk'];
            } else {
                $this->assertSame(self::ACME.'/acct/1', $protected['kid']);
            }
            $this->assertSame(1, openssl_verify("{$jws['protected']}.{$jws['payload']}", self::der(self::b64($jws['signature'])), self::publicKey($this->ca['jwk']), OPENSSL_ALGO_SHA256), "The signature of the request to {$url} is not correct.");
            $this->ca['requests'][] = [$url, $payload];

            if ($url === self::ACME.'/account') {
                $this->assertTrue($payload['termsOfServiceAgreed']);
                $this->assertSame(['mailto:certs@hosting.example'], $payload['contact']);

                return Http::response(['status' => 'valid'], 201, $nonce + ['Location' => self::ACME.'/acct/1']);
            }
            if ($url === self::ACME.'/order') {
                if (isset($options['refuseOrder'])) {
                    return Http::response(['type' => 'urn:ietf:params:acme:error:rateLimited', 'detail' => $options['refuseOrder']], 429, $nonce);
                }
                $this->ca['names'] = array_column($payload['identifiers'], 'value');
                $this->ca['authz'] = [];
                foreach ($this->ca['names'] as $i => $name) {
                    $this->ca['authz'][$i] = 'pending';
                }

                return Http::response(['status' => 'pending', 'authorizations' => array_map(fn ($i) => self::ACME."/authz/{$i}", array_keys($this->ca['names'])), 'finalize' => self::ACME.'/finalize'],
                    201, $nonce + ['Location' => self::ACME.'/order/1']);
            }
            if (preg_match('#/authz/(\d+)$#', $url, $m)) {
                $name = $this->ca['names'][$m[1]];
                $status = $this->ca['authz'][$m[1]];
                $invalid = $status === 'invalid' ? ['error' => ['type' => 'urn:ietf:params:acme:error:unauthorized', 'detail' => $options['invalid'] ?? '']] : [];

                return Http::response([
                    'status' => $status,
                    'identifier' => ['type' => 'dns', 'value' => str_starts_with($name, '*.') ? substr($name, 2) : $name],
                    ...(str_starts_with($name, '*.') ? ['wildcard' => true] : []),
                    'challenges' => [
                        ['type' => 'http-01', 'url' => self::ACME."/chall/http/{$m[1]}", 'token' => "http-{$m[1]}", 'status' => 'pending'],
                        ['type' => 'dns-01', 'url' => self::ACME."/chall/{$m[1]}", 'token' => "token-{$m[1]}", 'status' => $status, ...$invalid],
                    ],
                ], 200, $nonce);
            }
            if (preg_match('#/chall/(\d+)$#', $url, $m)) {
                // The TXT record of the challenge is in the zone now, with the correct value.
                $name = $this->ca['names'][$m[1]];
                $base = str_starts_with($name, '*.') ? substr($name, 2) : $name;
                $value = Acme::base64url(hash('sha256', "token-{$m[1]}.".self::thumbprint($this->ca['jwk']), true));
                $this->assertTrue(DnsRecord::where(['type' => 'TXT', 'value' => $value, 'managed' => DnsRecord::ACME])->get()
                    ->contains(fn (DnsRecord $r) => $r->zone->absolute($r->name) === "_acme-challenge.{$base}"), "The TXT record of {$name} is not in the zone.");
                $this->ca['authz'][$m[1]] = isset($options['invalid']) ? 'invalid' : 'valid';

                return Http::response(['status' => 'processing'], 200, $nonce);
            }
            if ($url === self::ACME.'/finalize') {
                $csr = "-----BEGIN CERTIFICATE REQUEST-----\n".chunk_split(base64_encode(self::b64($payload['csr'])), 64, "\n")."-----END CERTIFICATE REQUEST-----\n";
                $this->assertSame($this->ca['names'][0], openssl_csr_get_subject($csr)['CN']);
                $this->ca['certificate'] = self::pem($this->ca['names'], $this->ca['authority'], csr: $csr, key: openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']))['cert'];

                return Http::response(['status' => 'valid', 'certificate' => self::ACME.'/cert/1'], 200, $nonce);
            }
            if ($url === self::ACME.'/order/1') {
                return Http::response(['status' => $this->ca['certificate'] ? 'valid' : 'pending', 'certificate' => $this->ca['certificate'] ? self::ACME.'/cert/1' : null], 200, $nonce);
            }
            if ($url === self::ACME.'/cert/1') {
                $this->assertSame('application/pem-certificate-chain', $r->header('Accept')[0]);

                return Http::response($this->ca['certificate'].$this->ca['authority']['cert'], 200, $nonce + ['Content-Type' => 'application/pem-certificate-chain']);
            }

            return Http::response(['type' => 'urn:ietf:params:acme:error:malformed', 'detail' => "No such URL: {$url}"], 404, $nonce);
        });
    }

    private static function b64(string $data): string
    {
        return (string) base64_decode(strtr($data, '-_', '+/'));
    }

    /** The signature R || S of JWS in DER format, for openssl_verify. */
    private static function der(string $raw): string
    {
        $int = function (string $v): string {
            $v = ltrim($v, "\0");
            if ($v === '' || ord($v[0]) & 0x80) {
                $v = "\0".$v;
            }

            return "\x02".chr(strlen($v)).$v;
        };
        $body = $int(substr($raw, 0, 32)).$int(substr($raw, 32, 32));

        return "\x30".chr(strlen($body)).$body;
    }

    private static function publicKey(array $jwk): \OpenSSLAsymmetricKey
    {
        $der = hex2bin('3059301306072a8648ce3d020106082a8648ce3d030107034200').hex2bin('04').self::b64($jwk['x']).self::b64($jwk['y']);

        return openssl_pkey_get_public("-----BEGIN PUBLIC KEY-----\n".chunk_split(base64_encode($der), 64, "\n")."-----END PUBLIC KEY-----\n");
    }

    private static function thumbprint(array $jwk): string
    {
        return Acme::base64url(hash('sha256', json_encode(['crv' => $jwk['crv'], 'kty' => $jwk['kty'], 'x' => $jwk['x'], 'y' => $jwk['y']]), true));
    }

    /** A DNS zone "example.com" of the customer on two DNS servers, with its name servers at the registrar. */
    private function zone(): DnsZone
    {
        $this->dnsServer('ns1.hosting.example');
        $this->dnsServer('ns2.hosting.example');
        $response = $this->postJson('/api/v1/dns-zones', ['name' => 'example.com', 'customerId' => $this->customer->id])->assertAccepted();
        $this->finishZoneJobs();
        $zone = DnsZone::findOrFail($response->json('zone.id'));
        $zone->update(['delegation' => DnsZone::HERE]);

        return $zone;
    }

    private function finishZoneJobs(): void
    {
        foreach (AgentJob::where('action', AgentAction::SetDnsZones->value)->where('state', JobState::Queued->value)->get() as $job) {
            $this->finish($job);
        }
    }

    /** Does the next stage of the order, a few seconds later. */
    private function advance(Certificate $certificate, int $seconds = 10): Certificate
    {
        $this->travel($seconds)->seconds();
        app(AcmeOrders::class)->advance($certificate);

        return $certificate->fresh();
    }

    public function test_the_panel_gets_a_wildcard_certificate_with_dns_validation_in_a_zone_of_the_customer(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $this->fakeCa();

        $response = $this->postJson('/api/v1/certificates', ['source' => 'acme', 'customerId' => $this->customer->id, 'domain' => 'example.com', 'wildcard' => true])
            ->assertCreated()
            ->assertJsonPath('source', 'acme')
            ->assertJsonPath('names', ['example.com', '*.example.com'])
            ->assertJsonPath('state', 'issuing')
            ->assertJsonPath('order.stage', 'new');
        $certificate = Certificate::findOrFail($response->json('id'));
        // The customer has this certificate now.
        $this->postJson('/api/v1/certificates', ['source' => 'acme', 'customerId' => $this->customer->id, 'domain' => 'example.com', 'wildcard' => true])
            ->assertUnprocessable()->assertJsonPath('errors.domain.0', 'The customer has a certificate for these names. Renew that certificate.');

        // Stage "new": the order, and one TXT record for each name. Both records have the same name.
        $certificate = $this->advance($certificate, 0);
        $this->assertSame('dns', $certificate->order['stage']);
        $records = $zone->records()->where('managed', DnsRecord::ACME)->get();
        $this->assertSame(['_acme-challenge', '_acme-challenge'], $records->pluck('name')->all());
        $this->assertSame([PanelDns::TTL, PanelDns::TTL], $records->pluck('ttl')->all());
        $this->getJson("/api/v1/certificates/{$certificate->id}")->assertOk()->assertJsonPath('order.stage', 'dns')
            ->assertJsonPath('order.records.0.name', '_acme-challenge.example.com');
        // A user cannot change the records of the DNS validation.
        $this->patchJson("/api/v1/dns-zones/{$zone->id}/records/{$records[0]->id}", ['name' => 'x', 'type' => 'TXT', 'value' => 'x', 'ttl' => 300])->assertUnprocessable();

        // Stage "dns": the order waits until each DNS server has the new zone.
        $certificate = $this->advance($certificate);
        $this->assertSame('dns', $certificate->order['stage']);
        $this->finishZoneJobs();
        $certificate = $this->advance($certificate);
        $this->assertSame('validating', $certificate->order['stage']);

        // Stage "validating": the CA checked the records. The panel sends the request with a new key.
        $certificate = $this->advance($certificate);
        $this->assertSame('finalizing', $certificate->order['stage']);
        $this->assertNotNull($certificate->order_key);

        // Stage "finalizing": the certificate is ready. The records go, and the servers get the certificate.
        $certificate = $this->advance($certificate);
        $this->assertNull($certificate->order);
        $this->assertNull($certificate->order_key);
        $this->assertNull($certificate->error);
        $this->assertSame('valid', $certificate->state());
        $this->assertSame('Test Authority (Test CA)', $certificate->issuer);
        $this->assertStringStartsWith($this->ca['certificate'], $certificate->certificate);
        $this->assertTrue(openssl_x509_check_private_key($this->ca['certificate'], $certificate->private_key));
        // The renewal is after two thirds of the lifetime.
        $this->assertEqualsWithDelta($certificate->not_before->copy()->addDays(60)->getTimestamp(), $certificate->renew_at->getTimestamp(), 2);
        $this->assertSame(0, $zone->records()->where('managed', DnsRecord::ACME)->count());
        $this->assertSame([['certificate' => $certificate->id, 'hosts' => ['shop.example.com']]],
            array_map(fn ($e) => ['certificate' => $e['certificate']->id, 'hosts' => $e['hosts']], app(Certificates::class)->forServer($this->web)));
        $this->assertSame("Install the certificate example.com on {$this->web->name}", $this->certificateJobs()->sole()->title);
        // The panel made one account, and keeps its key encrypted.
        $accounts = Setting::get('acme_accounts');
        $this->assertSame(self::ACME.'/acct/1', $accounts[self::ACME.'/directory']['url']);
        $this->assertStringNotContainsString('PRIVATE KEY', $accounts[self::ACME.'/directory']['key']);
        $this->assertCount(1, array_filter($this->ca['requests'], fn ($r) => $r[0] === self::ACME.'/account'));
    }

    public function test_an_order_that_fails_removes_its_records_and_keeps_the_error(): void
    {
        $this->hosting();
        $zone = $this->zone();
        $this->fakeCa(['invalid' => 'No TXT record found at _acme-challenge.example.com']);
        $id = $this->postJson('/api/v1/certificates', ['source' => 'acme', 'customerId' => $this->customer->id, 'domain' => 'example.com'])->assertCreated()->json('id');
        $certificate = $this->advance(Certificate::findOrFail($id), 0);
        $this->finishZoneJobs();
        $certificate = $this->advance($certificate);
        $certificate = $this->advance($certificate);

        $this->assertNull($certificate->order);
        $this->assertSame('failed', $certificate->state());
        $this->assertSame('The certificate authority could not check the DNS record of example.com: No TXT record found at _acme-challenge.example.com', $certificate->error);
        $this->assertSame(0, $zone->records()->where('managed', DnsRecord::ACME)->count());
        // A first order does not start again by itself.
        $this->assertNull($certificate->next_try_at);
        $this->getJson("/api/v1/certificates/{$id}")->assertOk()->assertJsonPath('state', 'failed')->assertJsonPath('error', $certificate->error);

        // The user tries again.
        $this->fakeCa();
        $this->postJson("/api/v1/certificates/{$id}/renew")->assertAccepted()->assertJsonPath('state', 'issuing')->assertJsonPath('error', null);
    }

    public function test_an_order_needs_a_zone_of_the_customer_that_the_panel_serves(): void
    {
        $this->hosting();
        $request = fn () => $this->postJson('/api/v1/certificates', ['source' => 'acme', 'customerId' => $this->customer->id, 'domain' => 'example.com', 'wildcard' => true]);
        $request()->assertUnprocessable()->assertJsonPath('errors.domain.0', 'The panel cannot add DNS records for example.com. Make a DNS zone for the domain on the DNS page, and give the name servers of the panel to the registrar of the domain.');

        $zone = $this->zone();
        $zone->update(['delegation' => DnsZone::ELSEWHERE]);
        $request()->assertUnprocessable()->assertJsonPath('errors.domain.0', fn (string $m) => str_starts_with($m, 'The public DNS does not send the queries for example.com to the name servers of this panel'));

        // A zone of a different customer is not sufficient.
        $zone->update(['delegation' => DnsZone::HERE, 'customer_id' => $this->customer(attributes: ['name' => 'Other', 'email' => 'it@other.example'])->id]);
        $request()->assertUnprocessable()->assertJsonValidationErrors('domain');
        $this->assertSame(0, Certificate::count());
    }

    public function test_the_panel_renews_a_certificate_and_keeps_the_old_certificate_when_the_renewal_fails(): void
    {
        $this->hosting();
        $this->zone();
        $old = $this->certificate($this->customer, ['example.com', '*.example.com'], ['source' => Certificate::ACME, 'renew_at' => now()->subMinute()]);
        $fingerprint = $old->fingerprint;
        $this->fakeCa(['refuseOrder' => 'too many certificates already issued']);

        $this->artisan('cloudfactory:certificates', ['--seconds' => 0])->assertSuccessful();
        $old->refresh();
        $this->assertNull($old->order);
        $this->assertSame($fingerprint, $old->fingerprint);
        $this->assertSame('valid', $old->state());
        $this->assertSame('The certificate authority refused the request (rateLimited): too many certificates already issued', $old->error);
        $this->assertSame(1, $old->tries);
        $this->assertEqualsWithDelta(now()->addHour()->getTimestamp(), $old->next_try_at->getTimestamp(), 2);

        // The next try is after 1 hour, then after 2 hours.
        $this->artisan('cloudfactory:certificates', ['--seconds' => 0])->assertSuccessful();
        $this->assertSame(1, $old->fresh()->tries);
        $this->travel(61)->minutes();
        $this->artisan('cloudfactory:certificates', ['--seconds' => 0])->assertSuccessful();
        $old->refresh();
        $this->assertSame(2, $old->tries);
        $this->assertEqualsWithDelta(now()->addHours(2)->getTimestamp(), $old->next_try_at->getTimestamp(), 2);

        // The CA gives the certificate now.
        $this->travel(121)->minutes();
        $this->fakeCa();
        $this->artisan('cloudfactory:certificates', ['--seconds' => 0])->assertSuccessful();
        $old->refresh();
        $this->assertSame('dns', $old->order['stage']);
        $this->finishZoneJobs();
        foreach (range(1, 3) as $_) {
            $old = $this->advance($old);
        }
        $this->assertNull($old->order);
        $this->assertNotSame($fingerprint, $old->fingerprint);
        $this->assertSame(0, $old->tries);
        $this->assertNull($old->error);
        $this->assertTrue($old->renew_at->isFuture());
    }

    public function test_the_dns_hosting_of_the_panel_is_the_first_provider_of_the_dns_validation(): void
    {
        $providers = app(DnsProviders::class)->all();
        $this->assertInstanceOf(PanelDns::class, $providers[0]);
        $this->assertSame('panel', $providers[0]->key());
        $this->assertSame('_acme-challenge.example.com', DnsProviders::recordName('*.example.com'));
        $this->assertSame('_acme-challenge.shop.example.com', DnsProviders::recordName('shop.example.com'));
    }
}
