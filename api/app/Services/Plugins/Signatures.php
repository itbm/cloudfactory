<?php

namespace App\Services\Plugins;

use App\Models\PluginKey;
use Illuminate\Validation\ValidationException;

/**
 * The signatures of plugin manifests. The publisher signs the exact bytes of plugin.json with
 * an ECDSA P-256 key, for example with "cosign sign-blob --key cosign.key plugin.json". The
 * signature is the base64 text that cosign writes.
 */
class Signatures
{
    /**
     * Checks a public key in PEM format, and gives its fingerprint: the SHA-256 of the DER
     * bytes, in hexadecimal.
     *
     * @throws ValidationException
     */
    public static function fingerprint(string $pem): string
    {
        $key = @openssl_pkey_get_public($pem);
        $details = $key ? openssl_pkey_get_details($key) : false;
        if (! $details || $details['type'] !== OPENSSL_KEYTYPE_EC || ($details['ec']['curve_name'] ?? '') !== 'prime256v1') {
            throw ValidationException::withMessages(['publicKey' => 'Give an ECDSA P-256 public key in PEM format, for example the file cosign.pub.']);
        }
        $body = preg_replace('/-----[A-Z ]+-----|\s+/', '', $details['key']);

        return hash('sha256', (string) base64_decode($body, true));
    }

    /** The trusted key that signed the manifest, or null. */
    public static function signer(string $manifest, string $signature): ?PluginKey
    {
        $raw = base64_decode(trim($signature), true);
        if ($raw === false || $raw === '' || strlen($raw) > 256) {
            return null;
        }
        foreach (PluginKey::orderBy('created_at')->get() as $key) {
            $public = @openssl_pkey_get_public($key->public_key);
            if ($public && openssl_verify($manifest, $raw, $public, OPENSSL_ALGO_SHA256) === 1) {
                return $key;
            }
        }

        return null;
    }
}
