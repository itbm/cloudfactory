<?php

namespace App\Services;

use RuntimeException;

/**
 * Makes an OpenPGP key for a mailbox, and changes the password of its private key.
 *
 * The key is a version 4 key (RFC 4880 with RFC 6637 and the EdDSA extension), which GnuPG,
 * Thunderbird and OpenPGP.js read:
 *
 * - a primary key (Ed25519) that signs;
 * - a subkey (Curve25519) that the mail server encrypts the messages with.
 *
 * The private key is protected with a password: AES-256 with a key from the password
 * (iterated and salted S2K with SHA-256). The panel keeps only the protected form.
 */
final class OpenPgpKey
{
    private const ED25519_OID = "\x2B\x06\x01\x04\x01\xDA\x47\x0F\x01";

    private const CURVE25519_OID = "\x2B\x06\x01\x04\x01\x97\x55\x01\x05\x01";

    private const EDDSA = 22;

    private const ECDH = 18;

    private const SHA256 = 8;

    private const AES256 = 9;

    /** The S2K count octet: 16 MiB of hash input for each password that a person tries. */
    private const S2K_COUNT = 0xE0;

    /**
     * Makes a key for an address.
     *
     * @param  ?string  $seed  32 bytes that give the keys. Without them, the keys are random.
     * @return array{publicKey: string, privateKey: string, fingerprint: string}
     */
    public static function generate(string $address, string $password, ?int $time = null, ?string $seed = null): array
    {
        $time ??= time();
        $created = pack('N', $time);

        $sign = $seed === null ? sodium_crypto_sign_keypair() : sodium_crypto_sign_seed_keypair($seed);
        $signSecret = sodium_crypto_sign_secretkey($sign);
        $signPublic = sodium_crypto_sign_publickey($sign);
        $seed = substr($signSecret, 0, 32);

        $scalar = $seed === null ? random_bytes(32) : hash('sha256', "encryption {$seed}", true);
        // The form of a Curve25519 private key.
        $scalar[0] = chr(ord($scalar[0]) & 248);
        $scalar[31] = chr((ord($scalar[31]) & 127) | 64);
        $encryptPublic = sodium_crypto_scalarmult_base($scalar);

        $primary = "\x04".$created.chr(self::EDDSA).chr(strlen(self::ED25519_OID)).self::ED25519_OID.self::mpi("\x40".$signPublic);
        // The last four octets: the key wrap uses SHA-256 and AES-128, as GnuPG does for this curve.
        $sub = "\x04".$created.chr(self::ECDH).chr(strlen(self::CURVE25519_OID)).self::CURVE25519_OID.self::mpi("\x40".$encryptPublic)."\x03\x01\x08\x07";
        $fingerprint = sha1("\x99".pack('n', strlen($primary)).$primary, true);
        $userId = '<'.$address.'>';

        $certification = self::signature(0x13, $created, $fingerprint, $signSecret,
            self::subpacket(27, "\x03").self::subpacket(11, "\x09\x08\x07").self::subpacket(21, "\x0A\x09\x08").self::subpacket(22, "\x02\x01\x00").self::subpacket(30, "\x01"),
            self::keyData($primary)."\xB4".pack('N', strlen($userId)).$userId);
        // The subkey encrypts messages in transit and in storage.
        $binding = self::signature(0x18, $created, $fingerprint, $signSecret, self::subpacket(27, "\x0C"), self::keyData($primary).self::keyData($sub));

        $public = self::packet(6, $primary).self::packet(13, $userId).self::packet(2, $certification).self::packet(14, $sub).self::packet(2, $binding);
        // The private number of Curve25519 is in the opposite octet order in OpenPGP.
        $private = self::packet(5, $primary.self::protect(self::mpi($seed), $password)).self::packet(13, $userId).self::packet(2, $certification)
            .self::packet(7, $sub.self::protect(self::mpi(strrev($scalar)), $password)).self::packet(2, $binding);
        sodium_memzero($signSecret);
        sodium_memzero($seed);
        sodium_memzero($scalar);

        return [
            'publicKey' => self::armor('PUBLIC KEY BLOCK', $public),
            'privateKey' => self::armor('PRIVATE KEY BLOCK', $private),
            'fingerprint' => strtoupper(bin2hex($fingerprint)),
        ];
    }

    /**
     * Gives the private keys with a new password. $armored can have more than one block. A
     * block that $old does not open stays as it is.
     *
     * @return array{privateKey: string, changed: int, kept: int}
     */
    public static function changePassword(string $armored, string $old, string $new): array
    {
        $changed = $kept = 0;
        $out = [];
        foreach (self::blocks($armored) as $block) {
            try {
                $packets = '';
                foreach (self::packets(self::dearmor($block)) as [$tag, $body]) {
                    if ($tag === 5 || $tag === 7) {
                        [$public, $secret] = self::open($body, $old);
                        $body = $public.self::protect($secret, $new);
                    }
                    $packets .= self::packet($tag, $body);
                }
                $out[] = self::armor('PRIVATE KEY BLOCK', $packets);
                $changed++;
            } catch (RuntimeException) {
                $out[] = $block;
                $kept++;
            }
        }

        return ['privateKey' => implode('', $out), 'changed' => $changed, 'kept' => $kept];
    }

    /** The armoured blocks of a text. */
    public static function blocks(string $armored): array
    {
        preg_match_all('/-----BEGIN PGP PRIVATE KEY BLOCK-----.*?-----END PGP PRIVATE KEY BLOCK-----\n?/s', $armored, $m);

        return $m[0];
    }

    /** The secret part of a key packet: the cipher, the S2K values, and the encrypted numbers. */
    private static function protect(string $numbers, string $password): string
    {
        $salt = random_bytes(8);
        $iv = random_bytes(16);
        $key = self::s2k($password, $salt, self::S2K_COUNT);
        $cipher = openssl_encrypt($numbers.sha1($numbers, true), 'aes-256-cfb', $key, OPENSSL_RAW_DATA | OPENSSL_ZERO_PADDING, $iv);
        if ($cipher === false) {
            throw new RuntimeException('The panel could not protect the private key.');
        }

        return "\xFE".chr(self::AES256)."\x03".chr(self::SHA256).$salt.chr(self::S2K_COUNT).$iv.$cipher;
    }

    /**
     * Opens a private key packet of the form that protect makes.
     *
     * @return array{string, string} The public part, and the private numbers.
     */
    private static function open(string $body, string $password): array
    {
        $at = 5;
        $algorithm = ord($body[$at] ?? "\0");
        $at += 2 + ord($body[$at + 1] ?? "\0");
        $at += 2 + intdiv(self::uint16($body, $at) + 7, 8);
        if ($algorithm === self::ECDH) {
            $at += 1 + ord($body[$at] ?? "\0");
        } elseif ($algorithm !== self::EDDSA) {
            throw new RuntimeException('The key has a type that the panel does not make.');
        }
        $public = substr($body, 0, $at);
        $secret = substr($body, $at);
        if (strlen($secret) < 29 || substr($secret, 0, 4) !== "\xFE".chr(self::AES256)."\x03".chr(self::SHA256)) {
            throw new RuntimeException('The private key does not have the protection that the panel makes.');
        }
        $key = self::s2k($password, substr($secret, 4, 8), ord($secret[12]));
        $plain = openssl_decrypt(substr($secret, 29), 'aes-256-cfb', $key, OPENSSL_RAW_DATA | OPENSSL_ZERO_PADDING, substr($secret, 13, 16));
        if ($plain === false || strlen($plain) < 21 || ! hash_equals(sha1(substr($plain, 0, -20), true), substr($plain, -20))) {
            throw new RuntimeException('The password does not open the private key.');
        }

        return [$public, substr($plain, 0, -20)];
    }

    /** The AES key from a password (RFC 4880, section 3.7.1.3). */
    private static function s2k(string $password, string $salt, int $coded): string
    {
        $count = (16 + ($coded & 15)) << (($coded >> 4) + 6);
        $unit = $salt.$password;
        $count = max($count, strlen($unit));
        $context = hash_init('sha256');
        // Blocks of about 64 KB keep the number of calls small.
        $block = str_repeat($unit, max(1, intdiv(65536, strlen($unit))));
        for ($left = $count; $left >= strlen($block); $left -= strlen($block)) {
            hash_update($context, $block);
        }
        hash_update($context, substr($block, 0, $left));

        return hash_final($context, true);
    }

    /** A version 4 signature of the primary key. */
    private static function signature(int $type, string $created, string $fingerprint, string $secret, string $subpackets, string $signed): string
    {
        $hashed = self::subpacket(2, $created).$subpackets.self::subpacket(33, "\x04".$fingerprint);
        $head = "\x04".chr($type).chr(self::EDDSA).chr(self::SHA256).pack('n', strlen($hashed)).$hashed;
        $digest = hash('sha256', $signed.$head."\x04\xFF".pack('N', strlen($head)), true);
        $signature = sodium_crypto_sign_detached($digest, $secret);
        $unhashed = self::subpacket(16, substr($fingerprint, -8));

        return $head.pack('n', strlen($unhashed)).$unhashed.substr($digest, 0, 2).self::mpi(substr($signature, 0, 32)).self::mpi(substr($signature, 32));
    }

    private static function keyData(string $body): string
    {
        return "\x99".pack('n', strlen($body)).$body;
    }

    private static function subpacket(int $type, string $data): string
    {
        return chr(strlen($data) + 1).chr($type).$data;
    }

    /** A number: its length in bits, then its octets with no zero octets at the start. */
    private static function mpi(string $octets): string
    {
        $octets = ltrim($octets, "\0");
        if ($octets === '') {
            return "\0\0";
        }
        $bits = strlen($octets) * 8;
        for ($first = ord($octets[0]); ($first & 0x80) === 0; $first <<= 1) {
            $bits--;
        }

        return pack('n', $bits).$octets;
    }

    private static function uint16(string $data, int $at): int
    {
        if ($at + 2 > strlen($data)) {
            throw new RuntimeException('The key is not complete.');
        }

        return unpack('n', substr($data, $at, 2))[1];
    }

    /** One packet with a new-format header. */
    private static function packet(int $tag, string $body): string
    {
        $n = strlen($body);
        $length = match (true) {
            $n < 192 => chr($n),
            $n < 8384 => chr((($n - 192) >> 8) + 192).chr(($n - 192) & 0xFF),
            default => "\xFF".pack('N', $n),
        };

        return chr(0xC0 | $tag).$length.$body;
    }

    /**
     * The packets of a key that this class made.
     *
     * @return list<array{int, string}>
     */
    private static function packets(string $data): array
    {
        $out = [];
        for ($at = 0, $end = strlen($data); $at < $end;) {
            $head = ord($data[$at++]);
            if (($head & 0xC0) !== 0xC0 || $at >= $end) {
                throw new RuntimeException('The key does not have the form that the panel makes.');
            }
            $first = ord($data[$at++]);
            if ($first < 192) {
                $n = $first;
            } elseif ($first < 224) {
                $n = (($first - 192) << 8) + ord($data[$at++] ?? "\0") + 192;
            } elseif ($first === 255) {
                $n = unpack('N', str_pad(substr($data, $at, 4), 4, "\0"))[1];
                $at += 4;
            } else {
                throw new RuntimeException('The key does not have the form that the panel makes.');
            }
            if ($at + $n > $end) {
                throw new RuntimeException('The key is not complete.');
            }
            $out[] = [$head & 0x3F, substr($data, $at, $n)];
            $at += $n;
        }

        return $out;
    }

    private static function armor(string $kind, string $data): string
    {
        $crc = 0xB704CE;
        foreach (unpack('C*', $data) as $octet) {
            $crc ^= $octet << 16;
            for ($i = 0; $i < 8; $i++) {
                $crc <<= 1;
                if ($crc & 0x1000000) {
                    $crc ^= 0x1864CFB;
                }
            }
        }
        $sum = base64_encode(substr(pack('N', $crc & 0xFFFFFF), 1));

        return "-----BEGIN PGP {$kind}-----\n\n".chunk_split(base64_encode($data), 64, "\n")."={$sum}\n-----END PGP {$kind}-----\n";
    }

    private static function dearmor(string $block): string
    {
        $lines = preg_split('/\r?\n/', trim($block));
        $body = '';
        $inBody = false;
        foreach (array_slice($lines, 1, -1) as $line) {
            if (! $inBody) {
                // The headers end with an empty line.
                $inBody = $line === '';

                continue;
            }
            if ($line !== '' && $line[0] !== '=') {
                $body .= $line;
            }
        }
        $data = base64_decode($body, true);
        if ($data === false || $data === '') {
            throw new RuntimeException('The key is not valid base64.');
        }

        return $data;
    }
}
