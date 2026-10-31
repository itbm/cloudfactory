<?php

namespace App\Services\Registry;

/**
 * Verifies cosign signatures of the "simple signing" format with the release public key
 * (ECDSA P-256 and SHA-256). The agent verifies again when it pulls the image, through the
 * Podman signature policy (FR-1101).
 */
class CosignVerifier
{
    public function __construct(private ?string $publicKey) {}

    public static function fromConfig(): self
    {
        $path = config('cloudfactory.cosign_public_key');
        $key = is_string($path) && is_readable($path) ? file_get_contents($path) : false;

        return new self($key === false ? null : $key);
    }

    public function hasKey(): bool
    {
        return $this->publicKey !== null && trim($this->publicKey) !== '';
    }

    /**
     * True when one signature is good for this digest and this repository.
     *
     * @param  list<array{payload: string, signature: string}>  $signatures
     */
    public function verifies(array $signatures, string $repository, string $digest): bool
    {
        if (! $this->hasKey()) {
            return false;
        }
        $key = openssl_pkey_get_public($this->publicKey);
        if ($key === false) {
            return false;
        }
        foreach ($signatures as $s) {
            $payload = json_decode($s['payload'], true);
            if (($payload['critical']['image']['docker-manifest-digest'] ?? null) !== $digest) {
                continue;
            }
            $signed = $payload['critical']['identity']['docker-reference'] ?? '';
            if (! is_string($signed) || ! self::sameRepository($signed, $repository)) {
                continue;
            }
            $raw = base64_decode($s['signature'], true);
            if ($raw !== false && openssl_verify($s['payload'], $raw, $key, OPENSSL_ALGO_SHA256) === 1) {
                return true;
            }
        }

        return false;
    }

    private static function sameRepository(string $signed, string $repository): bool
    {
        [$a, $b] = [RegistryClient::parse($signed), RegistryClient::parse($repository)];

        return $a === $b;
    }
}
