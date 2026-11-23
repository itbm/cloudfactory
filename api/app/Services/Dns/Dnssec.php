<?php

namespace App\Services\Dns;

use RuntimeException;

/**
 * The DNSSEC key of a zone. A zone has one key for all its signatures (a combined signing
 * key), with the algorithm 13: ECDSA with the curve P-256 and SHA-256.
 *
 * The panel makes the key and sends it to each DNS server. Thus all DNS servers give the
 * same DNSKEY record, and the registrar of the domain needs only one DS record.
 */
class Dnssec
{
    /** ECDSA P-256 with SHA-256 (RFC 6605). */
    public const ALGORITHM = 13;

    /** The flags of a key that signs the keys and the zone: "zone key" and "secure entry point". */
    public const FLAGS = 257;

    /** SHA-256 (RFC 4509). */
    public const DIGEST_TYPE = 2;

    /**
     * Makes a new key.
     *
     * @return array{key: string, publicKey: string, keyTag: int} The private key in the PKCS #8
     *                                                            PEM format, the public key in Base64, and the key tag.
     */
    public static function generate(): array
    {
        $key = openssl_pkey_new(['private_key_type' => OPENSSL_KEYTYPE_EC, 'curve_name' => 'prime256v1']);
        if ($key === false || ! openssl_pkey_export($key, $pem)) {
            throw new RuntimeException('OpenSSL did not make a DNSSEC key.');
        }
        // OpenSSL 3 gives the PKCS #8 format, which the DNS server reads.
        if (! str_starts_with($pem, '-----BEGIN PRIVATE KEY-----')) {
            throw new RuntimeException('OpenSSL did not give the DNSSEC key in the PKCS #8 format.');
        }

        return self::fromPem($pem);
    }

    /**
     * The public key and the key tag of an ECDSA P-256 private key in the PKCS #8 format, in
     * the form that generate() gives.
     *
     * @return array{key: string, publicKey: string, keyTag: int}
     */
    public static function fromPem(string $pem): array
    {
        $key = openssl_pkey_get_private($pem);
        $details = $key === false ? false : openssl_pkey_get_details($key);
        if ($details === false || ($details['ec']['curve_name'] ?? null) !== 'prime256v1') {
            throw new RuntimeException('The DNSSEC key is not an ECDSA P-256 key.');
        }
        $public = str_pad($details['ec']['x'], 32, "\0", STR_PAD_LEFT).str_pad($details['ec']['y'], 32, "\0", STR_PAD_LEFT);

        return ['key' => $pem, 'publicKey' => base64_encode($public), 'keyTag' => self::keyTag($public)];
    }

    /** The data of the DNSKEY record in the format of the DNS protocol (RFC 4034, section 2.1). */
    private static function rdata(string $public): string
    {
        return pack('nCC', self::FLAGS, 3, self::ALGORITHM).$public;
    }

    /** The key tag of the DNSKEY record (RFC 4034, appendix B). */
    public static function keyTag(string $public): int
    {
        $sum = 0;
        foreach (str_split(self::rdata($public)) as $i => $byte) {
            $sum += ($i & 1) ? ord($byte) : ord($byte) << 8;
        }
        $sum += ($sum >> 16) & 0xFFFF;

        return $sum & 0xFFFF;
    }

    /**
     * The DS record of a zone for the registrar (RFC 4034, section 5).
     *
     * @return array{keyTag: int, algorithm: int, digestType: int, digest: string, record: string}
     */
    public static function ds(string $zone, string $publicKey): array
    {
        $public = (string) base64_decode($publicKey, true);
        $digest = (string) self::digest($zone, $publicKey, self::DIGEST_TYPE);
        $tag = self::keyTag($public);

        return [
            'keyTag' => $tag,
            'algorithm' => self::ALGORITHM,
            'digestType' => self::DIGEST_TYPE,
            'digest' => $digest,
            'record' => "{$zone}. IN DS {$tag} ".self::ALGORITHM.' '.self::DIGEST_TYPE." {$digest}",
        ];
    }

    /** The digest types of a DS record that the panel can check, and their hash functions. */
    private const DIGESTS = [1 => 'sha1', 2 => 'sha256', 4 => 'sha384'];

    /**
     * The digest of a DS record for a key of the zone, in upper case hexadecimal (RFC 4034,
     * section 5.1.4). Null for a digest type that the panel does not know.
     */
    public static function digest(string $zone, string $publicKey, int $type): ?string
    {
        if (! isset(self::DIGESTS[$type])) {
            return null;
        }
        $owner = '';
        foreach (explode('.', $zone) as $label) {
            $owner .= chr(strlen($label)).$label;
        }

        return strtoupper(hash(self::DIGESTS[$type], $owner."\0".self::rdata((string) base64_decode($publicKey, true))));
    }

    /**
     * True when a DS record of the registry is for a key of the zone. The registrar can make
     * the DS record from the DNSKEY record with a different digest type.
     *
     * @param  array{keyTag: int, algorithm: int, digestType: int, digest: string}  $ds
     */
    public static function matches(string $zone, string $publicKey, array $ds): bool
    {
        return $ds['keyTag'] === self::keyTag((string) base64_decode($publicKey, true)) && $ds['algorithm'] === self::ALGORITHM
            && self::digest($zone, $publicKey, $ds['digestType']) === strtoupper($ds['digest']);
    }

    /** The DNSKEY record of a zone, as text. */
    public static function dnskey(string $zone, string $publicKey): string
    {
        return "{$zone}. IN DNSKEY ".self::FLAGS.' 3 '.self::ALGORITHM." {$publicKey}";
    }
}
