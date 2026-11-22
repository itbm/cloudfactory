<?php

namespace App\Services\Certificates;

use App\Validation\InputRules;
use Illuminate\Support\Carbon;
use Illuminate\Validation\ValidationException;

/**
 * Reads a certificate chain and a private key in PEM format, and checks them.
 */
class Pem
{
    /** The largest number of DNS names that the panel takes from one certificate. */
    public const MAX_NAMES = 100;

    /**
     * Checks an uploaded certificate chain and its private key. The chain can have the
     * certificates in any order. The result has the chain in the correct order: the
     * certificate first, then each intermediate certificate. A root certificate is not in it.
     *
     * @return array{certificate: string, private_key: string, names: list<string>, fingerprint: string, issuer: string, not_before: Carbon, expires_at: Carbon, trusted: bool}
     *
     * @throws ValidationException
     */
    public static function upload(string $chain, string $key): array
    {
        if (strlen($chain) > InputRules::MAX_CERTIFICATE_BYTES) {
            self::fail('certificate', 'The certificate chain is too large.');
        }
        if (strlen($key) > InputRules::MAX_CERTIFICATE_BYTES) {
            self::fail('privateKey', 'The private key is too large.');
        }
        $certs = self::certificates($chain);
        if ($certs === []) {
            self::fail('certificate', 'Paste the certificate in PEM format. It starts with "-----BEGIN CERTIFICATE-----".');
        }
        if (str_contains($key, 'ENCRYPTED')) {
            self::fail('privateKey', 'The private key has a password. Remove the password first, for example with "openssl pkey -in key.pem -out plain.pem".');
        }
        $private = @openssl_pkey_get_private($key);
        if ($private === false) {
            self::fail('privateKey', 'Paste the private key in PEM format. It starts with "-----BEGIN PRIVATE KEY-----".');
        }
        $details = openssl_pkey_get_details($private);
        $type = $details['type'] ?? -1;
        if ($type === OPENSSL_KEYTYPE_RSA && ($details['bits'] ?? 0) < 2048) {
            self::fail('privateKey', 'The RSA key is too short. Use a key with 2048 bits or more.');
        }
        if ($type !== OPENSSL_KEYTYPE_RSA && $type !== OPENSSL_KEYTYPE_EC) {
            self::fail('privateKey', 'Use an RSA or an ECDSA key. Browsers do not accept certificates with other keys.');
        }
        // The certificate of the key comes first.
        $leaf = null;
        foreach ($certs as $i => $pem) {
            if (openssl_x509_check_private_key($pem, $private)) {
                $leaf = $pem;
                unset($certs[$i]);
                break;
            }
        }
        if ($leaf === null) {
            self::fail('privateKey', 'The private key is not the key of a certificate in the chain.');
        }
        $ordered = self::order($leaf, array_values($certs));
        $info = self::info($leaf);
        if ($info['names'] === []) {
            self::fail('certificate', 'The certificate has no DNS names (subject alternative names). Browsers do not accept such a certificate.');
        }
        if ($info['expires_at']->isPast()) {
            self::fail('certificate', 'The certificate expired on '.$info['expires_at']->format('j F Y').'.');
        }
        openssl_pkey_export($private, $exported);

        return [
            'certificate' => implode('', $ordered),
            'private_key' => (string) $exported,
            ...$info,
            'trusted' => self::trusted($ordered),
        ];
    }

    /**
     * The details of a certificate in PEM format: its DNS names in lower case, its SHA-256
     * fingerprint, its issuer and its validity.
     *
     * @return array{names: list<string>, fingerprint: string, issuer: string, not_before: Carbon, expires_at: Carbon}
     */
    public static function info(string $pem): array
    {
        $parsed = openssl_x509_parse($pem) ?: [];
        $names = [];
        foreach (explode(',', (string) ($parsed['extensions']['subjectAltName'] ?? '')) as $entry) {
            $entry = trim($entry);
            if (str_starts_with($entry, 'DNS:')) {
                $name = strtolower(rtrim(substr($entry, 4), '.'));
                if (self::validName($name) && ! in_array($name, $names, true)) {
                    $names[] = $name;
                }
            }
        }
        $issuer = $parsed['issuer'] ?? [];
        $text = fn (mixed $v): string => is_array($v) ? implode(', ', array_map('strval', $v)) : (string) $v;
        $organisation = $text($issuer['O'] ?? '');
        $common = $text($issuer['CN'] ?? '');
        $issuerName = $organisation !== '' && $common !== '' ? "{$organisation} ({$common})" : ($organisation ?: $common);

        return [
            'names' => array_slice($names, 0, self::MAX_NAMES),
            'fingerprint' => strtolower((string) openssl_x509_fingerprint($pem, 'sha256')),
            'issuer' => mb_substr($issuerName, 0, 255),
            'not_before' => Carbon::createFromTimestampUTC((int) ($parsed['validFrom_time_t'] ?? 0)),
            'expires_at' => Carbon::createFromTimestampUTC((int) ($parsed['validTo_time_t'] ?? 0)),
        ];
    }

    /** True for a DNS name of a host, or a wildcard name such as "*.example.com". */
    public static function validName(string $name): bool
    {
        $host = str_starts_with($name, '*.') ? substr($name, 2) : $name;

        return strlen($name) <= 253 && (bool) preg_match('/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/', $host)
            && ! preg_match('/\.[0-9]+$/', $host);
    }

    /**
     * The certificates of a PEM text, each in PEM format.
     *
     * @return list<string>
     */
    public static function certificates(string $pem): array
    {
        preg_match_all('/-----BEGIN CERTIFICATE-----\s+[A-Za-z0-9+\/=\s]+?-----END CERTIFICATE-----/', $pem, $m);
        $out = [];
        foreach ($m[0] as $block) {
            $cert = @openssl_x509_read($block);
            if ($cert !== false && openssl_x509_export($cert, $exported)) {
                $out[] = (string) $exported;
            }
        }

        return $out;
    }

    /**
     * The chain from the certificate up: each next certificate is the issuer of the one before.
     * A self-signed root certificate is not in the chain, because the browser has it.
     *
     * @param  list<string>  $others
     * @return list<string>
     */
    private static function order(string $leaf, array $others): array
    {
        $chain = [$leaf];
        $current = $leaf;
        while ($others !== []) {
            $issuer = openssl_x509_parse($current)['issuer'] ?? null;
            $next = null;
            foreach ($others as $i => $pem) {
                $parsed = openssl_x509_parse($pem);
                if (($parsed['subject'] ?? null) == $issuer) {
                    $next = $i;
                    break;
                }
            }
            if ($next === null) {
                break;
            }
            $pem = $others[$next];
            unset($others[$next]);
            $parsed = openssl_x509_parse($pem);
            if (($parsed['subject'] ?? null) == ($parsed['issuer'] ?? false)) {
                break;
            }
            $chain[] = $pem;
            $current = $pem;
        }

        return $chain;
    }

    /** True when the chain goes to a root certificate of the system: browsers then trust it. */
    public static function trusted(array $chain): bool
    {
        $file = tempnam(sys_get_temp_dir(), 'cf-chain');
        try {
            file_put_contents($file, implode('', array_slice($chain, 1)) ?: "\n");
            $result = @openssl_x509_checkpurpose($chain[0], X509_PURPOSE_SSL_SERVER, [], count($chain) > 1 ? $file : null);

            return $result === true;
        } finally {
            @unlink($file);
        }
    }

    /** @throws ValidationException */
    private static function fail(string $field, string $message): never
    {
        throw ValidationException::withMessages([$field => $message]);
    }
}
