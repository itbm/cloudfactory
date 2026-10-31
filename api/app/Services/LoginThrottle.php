<?php

namespace App\Services;

use Illuminate\Support\Facades\Cache;

/**
 * CF-12: a limit for each account, in addition to the limit for each IP address. After
 * FREE_FAILURES failed attempts, the account is locked for a time that doubles after each
 * further failure, up to MAX_LOCK_SECONDS. A good sign-in clears the count.
 *
 * R-09: the lock is for the account and the source address together, so that somebody who
 * knows an email cannot lock the real user out from a different address. Many failures from
 * many addresses still lock the account for all addresses, after DISTRIBUTED_FAILURES.
 */
class LoginThrottle
{
    public const FREE_FAILURES = 5;

    public const FIRST_LOCK_SECONDS = 30;

    public const MAX_LOCK_SECONDS = 3600;

    /** Failures in one hour, from all addresses, after which the account locks for all addresses. */
    public const DISTRIBUTED_FAILURES = 100;

    /** The number of seconds that the account stays locked for this address, or 0. */
    public static function lockedFor(string $email, string $ip): int
    {
        $until = max((int) Cache::get(self::key($email, $ip, 'lock'), 0), (int) Cache::get(self::key($email, '*', 'lock'), 0));

        return max(0, $until - time());
    }

    /** Records a failure. It returns true when this failure locked the account. */
    public static function fail(string $email, string $ip): bool
    {
        $locked = self::count($email, $ip, self::FREE_FAILURES, now()->addDay());

        // Many failures from many addresses: an attack on the password, not a typing error.
        return self::count($email, '*', self::DISTRIBUTED_FAILURES, now()->addHour()) || $locked;
    }

    public static function clear(string $email, string $ip): void
    {
        Cache::forget(self::key($email, $ip, 'failures'));
        Cache::forget(self::key($email, $ip, 'lock'));
    }

    private static function count(string $email, string $ip, int $free, \DateTimeInterface $keep): bool
    {
        $key = self::key($email, $ip, 'failures');
        $failures = (int) Cache::get($key, 0) + 1;
        Cache::put($key, $failures, $keep);
        if ($failures < $free) {
            return false;
        }
        $seconds = min(self::MAX_LOCK_SECONDS, self::FIRST_LOCK_SECONDS * 2 ** min(20, $failures - $free));
        Cache::put(self::key($email, $ip, 'lock'), time() + $seconds, now()->addSeconds($seconds));

        return $failures === $free;
    }

    private static function key(string $email, string $ip, string $what): string
    {
        return "login-{$what}:".hash('sha256', mb_strtolower(trim($email)).'|'.$ip);
    }
}
