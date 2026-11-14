<?php

namespace App\Services;

/**
 * Makes an Ed25519 SSH key pair in the OpenSSH formats, for example the key that a website
 * uses to read a private Git repository.
 */
class SshKey
{
    /**
     * @return array{public: string, private: string} The public key as one line, and the private
     *                                                key as an OpenSSH private key file.
     */
    public static function generate(string $comment): array
    {
        $pair = sodium_crypto_sign_keypair();
        $public = sodium_crypto_sign_publickey($pair);
        // The secret key of libsodium is the seed and then the public key, as in OpenSSH.
        $secret = sodium_crypto_sign_secretkey($pair);
        $blob = self::text('ssh-ed25519').self::text($public);

        $check = random_bytes(4);
        $private = $check.$check.self::text('ssh-ed25519').self::text($public).self::text($secret).self::text($comment);
        for ($i = 1; strlen($private) % 8 !== 0; $i++) {
            $private .= chr($i);
        }
        $file = "openssh-key-v1\0".self::text('none').self::text('none').self::text('').pack('N', 1).self::text($blob).self::text($private);
        sodium_memzero($secret);

        return [
            'public' => 'ssh-ed25519 '.base64_encode($blob).' '.$comment,
            'private' => "-----BEGIN OPENSSH PRIVATE KEY-----\n".chunk_split(base64_encode($file), 70, "\n")."-----END OPENSSH PRIVATE KEY-----\n",
        ];
    }

    /** A string of the SSH wire format: the length as 4 bytes, and then the bytes. */
    private static function text(string $bytes): string
    {
        return pack('N', strlen($bytes)).$bytes;
    }
}
