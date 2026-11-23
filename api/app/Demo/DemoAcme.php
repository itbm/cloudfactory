<?php

namespace App\Demo;

use App\Models\Setting;
use App\Services\Certificates\Acme;
use App\Services\Certificates\AcmeException;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Str;
use phpseclib3\Crypt\PublicKeyLoader;
use phpseclib3\File\X509;

/**
 * The certificate authority of the demonstration mode. It accepts each challenge after the
 * panel says that the record is ready, and signs the certificate with a demonstration CA.
 * Browsers do not trust that CA. To show a failure, it refuses the DNS validation of a name
 * that starts with "fail.", for example "fail.example.com".
 *
 * The demonstration CAs have Ed25519 keys, and their certificates have the dates of the clock
 * of the panel. Thus the sample certificates are the same on each run of the fixtures command
 * (php artisan demo:fixtures). OpenSSL uses the clock of the computer, and the signatures of
 * ECDSA keys are random.
 */
class DemoAcme extends Acme
{
    private const CA = 'demo_acme_ca';

    public function __construct()
    {
        parent::__construct('demo://acme/directory');
    }

    public function newOrder(array $names): array
    {
        $id = (string) Str::ulid();
        Cache::put("demo-acme:{$id}", ['names' => $names, 'valid' => [], 'certificate' => null], now()->addDay());

        return [
            'url' => "demo://acme/order/{$id}",
            'status' => 'pending',
            'authorizations' => array_map(fn (int $i) => "demo://acme/authz/{$id}/{$i}", array_keys($names)),
            'finalize' => "demo://acme/finalize/{$id}",
        ];
    }

    public function read(string $url): array
    {
        $parts = explode('/', substr($url, strlen('demo://acme/')));
        $order = Cache::get('demo-acme:'.($parts[1] ?? ''));
        if (! is_array($order)) {
            throw new AcmeException('The certificate authority does not know this order.');
        }
        if ($parts[0] === 'order') {
            return ['status' => $order['certificate'] ? 'valid' : 'processing', 'certificate' => $order['certificate'] ? "demo://acme/cert/{$parts[1]}" : null];
        }
        $name = (string) ($order['names'][(int) ($parts[2] ?? 0)] ?? '');
        $base = str_starts_with($name, '*.') ? substr($name, 2) : $name;
        $responded = in_array($name, $order['valid'], true);
        $fails = str_starts_with($base, 'fail.');

        return [
            'status' => ! $responded ? 'pending' : ($fails ? 'invalid' : 'valid'),
            'identifier' => ['type' => 'dns', 'value' => $base],
            'challenges' => [[
                'type' => 'dns-01', 'url' => "demo://acme/chall/{$parts[1]}/{$parts[2]}", 'token' => hash('sha256', $url),
                ...($responded && $fails ? ['error' => ['detail' => "No TXT record found at _acme-challenge.{$base}"]] : []),
            ]],
        ];
    }

    public function respond(string $challengeUrl): void
    {
        [, $id, $index] = explode('/', substr($challengeUrl, strlen('demo://acme/')));
        $order = Cache::get("demo-acme:{$id}");
        $order['valid'][] = $order['names'][(int) $index];
        Cache::put("demo-acme:{$id}", $order, now()->addDay());
    }

    public function finalize(string $finalizeUrl, string $csrDer): array
    {
        $id = basename($finalizeUrl);
        $order = Cache::get("demo-acme:{$id}");
        $ca = self::authority();
        $csr = "-----BEGIN CERTIFICATE REQUEST-----\n".chunk_split(base64_encode($csrDer), 64, "\n")."-----END CERTIFICATE REQUEST-----\n";
        $pem = self::certificate((string) openssl_pkey_get_details(openssl_csr_get_public_key($csr))['key'], ['commonName' => $order['names'][0]], $order['names'], $ca, 90);
        $order['certificate'] = $pem.$ca['cert'];
        Cache::put("demo-acme:{$id}", $order, now()->addDay());

        return ['status' => 'valid', 'certificate' => "demo://acme/cert/{$id}"];
    }

    public function download(string $certificateUrl): string
    {
        return (string) (Cache::get('demo-acme:'.basename($certificateUrl))['certificate'] ?? '');
    }

    /** The demo CA acts as a public CA, as the certificates of the demo seeder do. */
    public function trusted(array $chain): bool
    {
        return true;
    }

    public function dnsValue(string $token): string
    {
        return self::base64url(hash('sha256', $token.'.demo', true));
    }

    /**
     * The demonstration CA: a key and a self-signed certificate.
     *
     * @return array{cert: string, key: string}
     */
    public static function authority(): array
    {
        $saved = Setting::get(self::CA);
        if (is_array($saved) && isset($saved['cert'], $saved['key'])) {
            return $saved;
        }
        $saved = self::makeAuthority('Demo CA R1', 'Cloudfactory demonstration');
        Setting::put(self::CA, $saved);

        return $saved;
    }

    /**
     * A new CA for the sample data, for example the CA of an uploaded certificate.
     *
     * @return array{cert: string, key: string}
     */
    public static function makeAuthority(string $name, ?string $organisation = null, int $days = 3650): array
    {
        $key = StableValues::ed25519Key("CA {$name}");
        $private = $key->toString('PKCS8');
        $dn = ['commonName' => $name, ...($organisation === null ? [] : ['organizationName' => $organisation])];

        return ['cert' => self::certificate($key->getPublicKey()->toString('PKCS8'), $dn, [], ['key' => $private], $days), 'key' => $private];
    }

    /**
     * A certificate for the names from the demonstration CA, for the sample data.
     *
     * @param  list<string>  $names
     * @return array{cert: string, key: string}
     */
    public static function issue(array $names, int $days = 90, ?array $issuer = null): array
    {
        $ca = $issuer ?? self::authority();
        $key = StableValues::ecKey('certificate '.implode(' ', $names));
        openssl_pkey_export($key, $keyPem);
        $certPem = self::certificate((string) openssl_pkey_get_details($key)['key'], ['commonName' => $names[0]], $names, $ca, $days);

        return ['cert' => $certPem.$ca['cert'], 'key' => $keyPem];
    }

    /**
     * A certificate for the public key, from now for the number of days. A certificate with no
     * DNS names is the certificate of a CA. Without "cert", the issuer signs its own certificate.
     *
     * @param  array<string, string>  $dn  The subject, for example ['commonName' => 'example.com']
     * @param  list<string>  $names
     * @param  array{cert?: string, key: string}  $issuer
     */
    private static function certificate(string $publicKey, array $dn, array $names, array $issuer, int $days): string
    {
        $subject = new X509;
        $subject->setPublicKey(PublicKeyLoader::loadPublicKey($publicKey));
        $subject->setDN($dn);
        $signer = new X509;
        $signer->setPrivateKey(PublicKeyLoader::loadPrivateKey($issuer['key']));
        isset($issuer['cert']) ? $signer->loadX509($issuer['cert']) : $signer->setDN($dn);
        $x509 = new X509;
        $x509->setStartDate(Carbon::now()->toDateTimeImmutable());
        $x509->setEndDate(Carbon::now()->addDays($days)->toDateTimeImmutable());
        $x509->setSerialNumber(bin2hex(substr(StableValues::bytes('serial '.implode(' ', [...array_values($dn), ...$names])), 0, 8)), 16);
        if ($names === []) {
            $x509->makeCA();
        }
        // phpseclib adds the extensions to a signed certificate, and then signs it again.
        $x509->loadX509($x509->saveX509($x509->sign($signer, $subject)));
        if ($names !== []) {
            $x509->setExtension('id-ce-subjectAltName', array_map(fn (string $n) => ['dNSName' => $n], $names));
            $x509->setExtension('id-ce-basicConstraints', ['cA' => false]);
            $x509->setExtension('id-ce-extKeyUsage', ['id-kp-serverAuth']);
        } else {
            $x509->setExtension('id-ce-keyUsage', ['keyCertSign', 'cRLSign'], true);
        }

        return $x509->saveX509($x509->sign($signer, $x509))."\n";
    }
}
