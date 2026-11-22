<?php

namespace App\Services\Certificates;

use App\Models\Certificate;
use App\Services\Audit;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;

/**
 * The ACME orders of the certificates with DNS validation (docs/certificates.md).
 *
 * An order has these stages. Each call of advance() does one stage, or waits:
 *
 * 1. "new": the panel makes the order, reads the authorizations, and adds one TXT record
 *    for each dns-01 challenge through the DNS provider of the name (DnsProviders).
 * 2. "dns": the panel waits until the name servers have the records, and then tells the CA
 *    that the challenges are ready.
 * 3. "validating": the panel waits until the CA checked each record. Then it makes a new key
 *    and sends the certificate signing request.
 * 4. "finalizing": the panel waits until the certificate is ready, downloads it, removes the
 *    TXT records and sends the certificate to the servers.
 *
 * When a stage fails, the panel removes the TXT records and keeps the error. The old
 * certificate of a renewal stays in use. The panel tries a renewal again after 1 hour, then 2,
 * 4 and so on, up to 1 day. A first order does not start again by itself.
 */
class AcmeOrders
{
    /** The longest time of each stage that waits, in minutes. */
    public const STAGE_MINUTES = 15;

    /** The time between two checks of a stage that waits, in seconds. */
    public const POLL_SECONDS = 10;

    /** The longest time between two tries of a renewal that failed, in hours. */
    public const RETRY_MAX_HOURS = 24;

    private ?Acme $acme = null;

    public function __construct(private DnsProviders $dns, private Certificates $certificates) {}

    /** The client of the CA. The demonstration mode has its own CA (DemoServiceProvider). */
    private function acme(): Acme
    {
        return $this->acme ??= app(Acme::class);
    }

    /** Starts a new order for the certificate. An order that runs stops first. */
    public function start(Certificate $certificate): void
    {
        if ($certificate->ordering()) {
            $this->cleanUp($certificate);
        }
        $certificate->update([
            'order' => ['stage' => 'new', 'startedAt' => now()->toIso8601String(), 'stageAt' => now()->toIso8601String()],
            'order_key' => null,
            'error' => null,
            'next_try_at' => now(),
        ]);
    }

    /**
     * Does the next stage of the order of the certificate, when it is time. It gives true when
     * the order still runs.
     */
    public function advance(Certificate $certificate): bool
    {
        $lock = Cache::lock("certificate-order:{$certificate->id}", 120);
        if (! $lock->get()) {
            return true;
        }
        try {
            $certificate->refresh();
            if (! $certificate->ordering()) {
                return false;
            }
            if ($certificate->next_try_at !== null && $certificate->next_try_at->isFuture()) {
                return true;
            }
            try {
                $this->step($certificate);
            } catch (AcmeException $e) {
                $this->fail($certificate, $e->getMessage());
            } catch (\Throwable $e) {
                Log::error('Certificate order failed', ['certificate' => $certificate->id, 'exception' => $e]);
                $this->fail($certificate, 'The order stopped because of an error in the panel. Try again later.');
            }

            return $certificate->fresh()?->ordering() ?? false;
        } finally {
            $lock->release();
        }
    }

    private function step(Certificate $certificate): void
    {
        $order = $certificate->order;
        $stageAt = Carbon::parse($order['stageAt'] ?? now());
        if (($order['stage'] ?? 'new') !== 'new' && $stageAt->lt(now()->subMinutes(self::STAGE_MINUTES))) {
            throw new AcmeException(match ($order['stage']) {
                'dns' => 'The DNS servers of the panel did not get the records of the DNS validation in time.',
                'validating' => 'The certificate authority did not check the records of the DNS validation in time.',
                default => 'The certificate authority did not give the certificate in time.',
            });
        }
        match ($order['stage'] ?? 'new') {
            'new' => $this->create($certificate),
            'dns' => $this->waitForDns($certificate),
            'validating' => $this->waitForValidation($certificate),
            'finalizing' => $this->waitForCertificate($certificate),
            default => throw new AcmeException('The order has an unknown stage.'),
        };
    }

    /** Stage "new": the order, the challenges and the TXT records. */
    private function create(Certificate $certificate): void
    {
        $names = $certificate->names;
        if (($problem = $this->dns->problem($certificate->customer_id, $names)) !== null) {
            throw new AcmeException($problem);
        }
        $acme = $this->acme();
        $order = $acme->newOrder($names);
        $challenges = [];
        foreach ($order['authorizations'] as $url) {
            $authz = $acme->read($url);
            if (($authz['status'] ?? '') === 'valid') {
                continue;
            }
            $value = (string) ($authz['identifier']['value'] ?? '');
            $challenge = collect((array) ($authz['challenges'] ?? []))->firstWhere('type', 'dns-01');
            if (! is_array($challenge) || ! is_string($challenge['url'] ?? null) || ! is_string($challenge['token'] ?? null)) {
                throw new AcmeException("The certificate authority gave no DNS validation for {$value}.");
            }
            $record = DnsProviders::recordName($value);
            $provider = $this->dns->for($certificate->customer_id, $record);
            if (! $provider) {
                throw new AcmeException("The panel cannot add DNS records for {$value}.");
            }
            $challenges[] = ['authorization' => $url, 'url' => $challenge['url'], 'identifier' => $value, 'name' => $record, 'value' => $acme->dnsValue($challenge['token']), 'provider' => $provider->key()];
        }
        $state = [];
        foreach (collect($challenges)->groupBy('provider') as $key => $list) {
            $state[$key] = $this->dns->find($key)->present($certificate->customer_id, $list->map(fn ($c) => ['name' => $c['name'], 'value' => $c['value']])->values()->all());
        }
        $this->save($certificate, [
            ...$certificate->order,
            'stage' => $challenges === [] ? 'validating' : 'dns',
            'stageAt' => now()->toIso8601String(),
            'url' => $order['url'],
            'finalize' => $order['finalize'],
            'authorizations' => $order['authorizations'],
            'challenges' => $challenges,
            'dns' => $state,
        ], seconds: 3);
    }

    /** Stage "dns": the name servers have the records. The CA can check them now. */
    private function waitForDns(Certificate $certificate): void
    {
        $order = $certificate->order;
        foreach ((array) ($order['dns'] ?? []) as $key => $state) {
            if (! $this->dns->find($key)?->ready((array) $state)) {
                $this->save($certificate, $order);

                return;
            }
        }
        foreach ((array) $order['challenges'] as $c) {
            $this->acme()->respond($c['url']);
        }
        $this->save($certificate, [...$order, 'stage' => 'validating', 'stageAt' => now()->toIso8601String()], seconds: 5);
    }

    /** Stage "validating": the CA checked each record. The panel sends the request. */
    private function waitForValidation(Certificate $certificate): void
    {
        $order = $certificate->order;
        foreach ((array) $order['authorizations'] as $url) {
            $authz = $this->acme()->read($url);
            $status = (string) ($authz['status'] ?? '');
            if ($status === 'valid') {
                continue;
            }
            if ($status === 'pending') {
                $this->save($certificate, $order);

                return;
            }
            $challenge = collect((array) ($authz['challenges'] ?? []))->firstWhere('type', 'dns-01');
            $detail = trim((string) ($challenge['error']['detail'] ?? ''));
            $value = (string) ($authz['identifier']['value'] ?? '');
            throw new AcmeException("The certificate authority could not check the DNS record of {$value}".($detail !== '' ? ": {$detail}" : '.'));
        }
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        openssl_pkey_export($key, $pem);
        $certificate->update(['order_key' => (string) $pem]);
        $result = $this->acme()->finalize((string) $order['finalize'], Acme::csr($key, $certificate->names));
        $this->save($certificate, [...$order, 'stage' => 'finalizing', 'stageAt' => now()->toIso8601String(), 'certificate' => $result['certificate'] ?? null], seconds: 3);
    }

    /** Stage "finalizing": the certificate is ready. */
    private function waitForCertificate(Certificate $certificate): void
    {
        $order = $certificate->order;
        $url = $order['certificate'] ?? null;
        if (! is_string($url)) {
            $status = $this->acme()->read((string) $order['url']);
            if (($status['status'] ?? '') === 'invalid') {
                throw new AcmeException('The certificate authority refused the order.');
            }
            if (($status['status'] ?? '') !== 'valid' || ! is_string($status['certificate'] ?? null)) {
                $this->save($certificate, $order);

                return;
            }
            $url = $status['certificate'];
        }
        $chain = $this->acme()->download($url);
        $certs = Pem::certificates($chain);
        $key = openssl_pkey_get_private((string) $certificate->order_key);
        if ($certs === [] || $key === false || ! openssl_x509_check_private_key($certs[0], $key)) {
            throw new AcmeException('The certificate from the certificate authority is not for the key of the order.');
        }
        $info = Pem::info($certs[0]);
        foreach ($certificate->names as $name) {
            if (! in_array($name, $info['names'], true)) {
                throw new AcmeException("The certificate from the certificate authority is not valid for {$name}.");
            }
        }
        $this->cleanUp($certificate);
        $lifetime = max(1, $info['expires_at']->getTimestamp() - $info['not_before']->getTimestamp());
        $renewed = $certificate->certificate !== null;
        $certificate->update([
            'certificate' => implode('', $certs),
            'private_key' => (string) $certificate->order_key,
            'fingerprint' => $info['fingerprint'],
            'issuer' => $info['issuer'],
            'not_before' => $info['not_before'],
            'expires_at' => $info['expires_at'],
            'trusted' => Pem::trusted($certs),
            // The panel renews the certificate after two thirds of its lifetime: 30 days
            // before the end for a certificate of 90 days.
            'renew_at' => $info['not_before']->copy()->addSeconds(intdiv($lifetime * 2, 3)),
            'order' => null,
            'order_key' => null,
            'error' => null,
            'tries' => 0,
            'next_try_at' => null,
        ]);
        Audit::system($renewed ? 'Renewed certificate' : 'Got certificate', implode(', ', $certificate->names));
        $this->certificates->syncCustomer($certificate->customer_id, ($renewed ? 'Install the renewed certificate ' : 'Install the certificate ').$certificate->names[0]);
    }

    /** Keeps the state of the order and the time of the next check. */
    private function save(Certificate $certificate, array $order, int $seconds = self::POLL_SECONDS): void
    {
        $certificate->update(['order' => $order, 'next_try_at' => now()->addSeconds($seconds)]);
    }

    /** Stops an order: the TXT records go, and the certificate keeps the error. */
    public function fail(Certificate $certificate, string $message): void
    {
        $this->cleanUp($certificate);
        $tries = $certificate->tries + 1;
        $certificate->update([
            'order' => null,
            'order_key' => null,
            'error' => mb_substr($message, 0, 2000),
            'tries' => $tries,
            // A renewal tries again later. The first order waits for the user.
            'next_try_at' => $certificate->certificate !== null ? now()->addHours(min(self::RETRY_MAX_HOURS, 2 ** ($tries - 1))) : null,
        ]);
        Audit::system('Certificate order failed', implode(', ', $certificate->names).': '.mb_substr($message, 0, 300));
    }

    /** Removes the TXT records of the order that runs. */
    public function cleanUp(Certificate $certificate): void
    {
        $order = $certificate->order ?? [];
        foreach (collect((array) ($order['challenges'] ?? []))->groupBy('provider') as $key => $list) {
            try {
                $this->dns->find((string) $key)?->cleanUp($certificate->customer_id, $list->map(fn ($c) => ['name' => $c['name'], 'value' => $c['value']])->values()->all(), (array) ($order['dns'][$key] ?? []));
            } catch (\Throwable $e) {
                Log::warning('The panel could not remove the DNS validation of a certificate', ['certificate' => $certificate->id, 'exception' => $e]);
            }
        }
    }
}
