<?php

namespace Tests\Support;

use OpenSSLAsymmetricKey;
use ParagonIE\ConstantTime\Base64UrlSafe;

/**
 * A passkey in software for the tests: an ES256 key, as a phone or a password manager has. It
 * gives the same JSON as PublicKeyCredential.toJSON() in a browser. The attestation is "none".
 */
class SoftAuthenticator
{
    public string $credentialId;

    public int $signCount = 0;

    public ?string $userHandle = null;

    private OpenSSLAsymmetricKey $key;

    /** The flags of the authenticator data. */
    public const USER_PRESENT = 0x01;

    public const USER_VERIFIED = 0x04;

    public const BACKUP_ELIGIBLE = 0x08;

    public const BACKED_UP = 0x10;

    private const ATTESTED_DATA = 0x40;

    public function __construct(public bool $synced = true)
    {
        $this->key = openssl_pkey_new(['curve_name' => 'prime256v1', 'private_key_type' => OPENSSL_KEYTYPE_EC]);
        $this->credentialId = random_bytes(32);
    }

    /**
     * Makes a passkey, as navigator.credentials.create() does.
     *
     * @param  array<string, mixed>  $options  the "publicKey" of the panel
     * @return array<string, mixed>
     */
    public function create(array $options, string $origin, ?int $flags = null): array
    {
        $this->userHandle = Base64UrlSafe::decodeNoPadding($options['user']['id']);
        $clientData = json_encode(['type' => 'webauthn.create', 'challenge' => $options['challenge'], 'origin' => $origin, 'crossOrigin' => false]);
        $details = openssl_pkey_get_details($this->key);
        $coseKey = self::cbor([1 => 2, 3 => -7, -1 => 1, -2 => self::bytes(str_pad($details['ec']['x'], 32, "\0", STR_PAD_LEFT)), -3 => self::bytes(str_pad($details['ec']['y'], 32, "\0", STR_PAD_LEFT))]);
        $authData = hash('sha256', $options['rp']['id'], true)
            .chr(($flags ?? $this->flags()) | self::ATTESTED_DATA)
            .pack('N', $this->signCount)
            .str_repeat("\0", 16)
            .pack('n', strlen($this->credentialId)).$this->credentialId
            .$coseKey;
        $attestation = self::cbor(['fmt' => 'none', 'attStmt' => [], 'authData' => self::bytes($authData)]);

        return [
            'id' => Base64UrlSafe::encodeUnpadded($this->credentialId),
            'rawId' => Base64UrlSafe::encodeUnpadded($this->credentialId),
            'type' => 'public-key',
            'response' => [
                'clientDataJSON' => Base64UrlSafe::encodeUnpadded($clientData),
                'attestationObject' => Base64UrlSafe::encodeUnpadded($attestation),
                'transports' => ['internal', 'hybrid'],
            ],
            'clientExtensionResults' => [],
            'authenticatorAttachment' => 'platform',
        ];
    }

    /**
     * Signs in with the passkey, as navigator.credentials.get() does.
     *
     * @param  array<string, mixed>  $options  the "publicKey" of the panel
     * @return array<string, mixed>
     */
    public function get(array $options, string $origin, ?int $flags = null, ?string $rpId = null): array
    {
        $clientData = json_encode(['type' => 'webauthn.get', 'challenge' => $options['challenge'], 'origin' => $origin, 'crossOrigin' => false]);
        $authData = hash('sha256', $rpId ?? $options['rpId'], true).chr($flags ?? $this->flags()).pack('N', $this->signCount);
        openssl_sign($authData.hash('sha256', $clientData, true), $signature, $this->key, OPENSSL_ALGO_SHA256);

        return [
            'id' => Base64UrlSafe::encodeUnpadded($this->credentialId),
            'rawId' => Base64UrlSafe::encodeUnpadded($this->credentialId),
            'type' => 'public-key',
            'response' => [
                'clientDataJSON' => Base64UrlSafe::encodeUnpadded($clientData),
                'authenticatorData' => Base64UrlSafe::encodeUnpadded($authData),
                'signature' => Base64UrlSafe::encodeUnpadded($signature),
                'userHandle' => $this->userHandle === null ? null : Base64UrlSafe::encodeUnpadded($this->userHandle),
            ],
            'clientExtensionResults' => [],
            'authenticatorAttachment' => 'platform',
        ];
    }

    private function flags(): int
    {
        return self::USER_PRESENT | self::USER_VERIFIED | ($this->synced ? self::BACKUP_ELIGIBLE | self::BACKED_UP : 0);
    }

    /** A byte string for cbor(). A PHP string is a text string. */
    private static function bytes(string $value): object
    {
        return (object) ['bytes' => $value];
    }

    /** A small CBOR encoder: integers, byte strings, text strings, lists and maps. */
    private static function cbor(mixed $value): string
    {
        $head = function (int $major, int $length): string {
            return match (true) {
                $length < 24 => chr(($major << 5) | $length),
                $length < 0x100 => chr(($major << 5) | 24).chr($length),
                $length < 0x10000 => chr(($major << 5) | 25).pack('n', $length),
                default => chr(($major << 5) | 26).pack('N', $length),
            };
        };

        return match (true) {
            is_int($value) => $value >= 0 ? $head(0, $value) : $head(1, -1 - $value),
            is_object($value) => $head(2, strlen($value->bytes)).$value->bytes,
            is_string($value) => $head(3, strlen($value)).$value,
            is_array($value) && array_is_list($value) && $value !== [] => $head(4, count($value)).implode('', array_map(self::cbor(...), $value)),
            is_array($value) => $head(5, count($value)).implode('', array_map(fn ($k, $v) => self::cbor($k).self::cbor($v), array_keys($value), $value)),
        };
    }
}
