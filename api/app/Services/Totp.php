<?php

namespace App\Services;

use App\Models\User;

/** RFC 6238 time-based one-time passwords with SHA-1, 6 digits and 30-second steps. */
class Totp
{
    private const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

    public static function generateSecret(): string
    {
        $bytes = random_bytes(20);
        $bits = '';
        foreach (str_split($bytes) as $b) {
            $bits .= str_pad(decbin(ord($b)), 8, '0', STR_PAD_LEFT);
        }

        return implode('', array_map(fn ($chunk) => self::ALPHABET[bindec(str_pad($chunk, 5, '0'))], str_split($bits, 5)));
    }

    public static function code(string $secret, ?int $time = null): string
    {
        $counter = intdiv($time ?? time(), 30);
        $hash = hash_hmac('sha1', pack('J', $counter), self::decode($secret), true);
        $offset = ord($hash[19]) & 0x0F;
        $value = ((ord($hash[$offset]) & 0x7F) << 24) | (ord($hash[$offset + 1]) << 16) | (ord($hash[$offset + 2]) << 8) | ord($hash[$offset + 3]);

        return str_pad((string) ($value % 1_000_000), 6, '0', STR_PAD_LEFT);
    }

    /** Accepts the current code and the codes of one step before and after, for clock drift. */
    public static function verify(string $secret, string $code, ?int $time = null): bool
    {
        return self::step($secret, $code, $time) !== null;
    }

    /**
     * Gives the time step of the code, or null when the code is not correct. A caller that
     * records the step can refuse the same code a second time (CF-12).
     */
    public static function step(string $secret, string $code, ?int $time = null): ?int
    {
        $time ??= time();
        foreach ([-30, 0, 30] as $drift) {
            if (hash_equals(self::code($secret, $time + $drift), $code)) {
                return intdiv($time + $drift, 30);
            }
        }

        return null;
    }

    /**
     * Checks the code of a user and records its step. A code is good one time only: the
     * panel refuses the step of the last code and all older steps (CF-12).
     */
    public static function consume(User $user, string $code, ?int $time = null): bool
    {
        if (! $user->totp_secret) {
            return true;
        }
        $step = self::step($user->totp_secret, $code, $time);
        if ($step === null) {
            return false;
        }
        // One update with a condition, so that two requests with the same code cannot both pass.
        $updated = User::whereKey($user->id)
            ->where(fn ($q) => $q->whereNull('totp_last_step')->orWhere('totp_last_step', '<', $step))
            ->update(['totp_last_step' => $step]);
        if ($updated === 1) {
            $user->totp_last_step = $step;
        }

        return $updated === 1;
    }

    private static function decode(string $secret): string
    {
        $bits = '';
        foreach (str_split(strtoupper(str_replace([' ', '='], '', $secret))) as $c) {
            $bits .= str_pad(decbin(strpos(self::ALPHABET, $c)), 5, '0', STR_PAD_LEFT);
        }

        return implode('', array_map(fn ($byte) => chr(bindec($byte)), array_filter(str_split($bits, 8), fn ($b) => strlen($b) === 8)));
    }
}
