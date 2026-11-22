<?php

namespace App\Demo;

use App\Models\Setting;
use App\Services\Certificates\Acme;
use App\Services\Certificates\AcmeException;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Str;

/**
 * The certificate authority of the demonstration mode. It accepts each challenge after the
 * panel says that the record is ready, and signs the certificate with a demonstration CA.
 * Browsers do not trust that CA. To show a failure, it refuses the DNS validation of a name
 * that starts with "fail.", for example "fail.example.com".
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
        $config = tempnam(sys_get_temp_dir(), 'cf-demo-ca');
        try {
            file_put_contents($config, "[req]\ndistinguished_name = dn\n[dn]\n[ext]\nsubjectAltName = ".implode(',', array_map(fn ($n) => "DNS:{$n}", $order['names']))."\nbasicConstraints = CA:FALSE\nextendedKeyUsage = serverAuth\n");
            $csr = "-----BEGIN CERTIFICATE REQUEST-----\n".chunk_split(base64_encode($csrDer), 64, "\n")."-----END CERTIFICATE REQUEST-----\n";
            $cert = openssl_csr_sign($csr, $ca['cert'], $ca['key'], 90, ['config' => $config, 'x509_extensions' => 'ext', 'digest_alg' => 'sha256'], random_int(1, PHP_INT_MAX));
            openssl_x509_export($cert, $pem);
        } finally {
            @unlink($config);
        }
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
    public static function makeAuthority(string $name, string $organisation): array
    {
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $config = tempnam(sys_get_temp_dir(), 'cf-demo-ca');
        try {
            file_put_contents($config, "[req]\ndistinguished_name = dn\n[dn]\n[ext]\nbasicConstraints = critical,CA:TRUE\nkeyUsage = critical,keyCertSign,cRLSign\n");
            $csr = openssl_csr_new(['commonName' => $name, 'organizationName' => $organisation], $key, ['config' => $config, 'digest_alg' => 'sha256']);
            $cert = openssl_csr_sign($csr, null, $key, 3650, ['config' => $config, 'x509_extensions' => 'ext', 'digest_alg' => 'sha256'], random_int(1, PHP_INT_MAX));
            openssl_x509_export($cert, $certPem);
            openssl_pkey_export($key, $keyPem);
        } finally {
            @unlink($config);
        }

        return ['cert' => $certPem, 'key' => $keyPem];
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
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        $config = tempnam(sys_get_temp_dir(), 'cf-demo-ca');
        try {
            file_put_contents($config, "[req]\ndistinguished_name = dn\n[dn]\n[ext]\nsubjectAltName = ".implode(',', array_map(fn ($n) => "DNS:{$n}", $names))."\nbasicConstraints = CA:FALSE\nextendedKeyUsage = serverAuth\n");
            $csr = openssl_csr_new(['commonName' => $names[0]], $key, ['config' => $config, 'digest_alg' => 'sha256']);
            $cert = openssl_csr_sign($csr, $ca['cert'], $ca['key'], $days, ['config' => $config, 'x509_extensions' => 'ext', 'digest_alg' => 'sha256'], random_int(1, PHP_INT_MAX));
            openssl_x509_export($cert, $certPem);
            openssl_pkey_export($key, $keyPem);
        } finally {
            @unlink($config);
        }

        return ['cert' => $certPem.$ca['cert'], 'key' => $keyPem];
    }
}
