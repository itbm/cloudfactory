<?php

namespace App\Services;

use Illuminate\Support\Facades\Cache;

/**
 * CF-12: a limit for each account, in addition to the limit for each IP address. After
 * FREE_FAILURES failed attempts, the account is locked for a time that doubles after each
 * further failure, up to MAX_LOCK_SECONDS. A good sign-in clears the count.
 */
class LoginThrottle
{
    public const FREE_FAILURES = 5;

    public const FIRST_LOCK_SECONDS = 30;

    public const MAX_LOCK_SECONDS = 3600;

    /** The number of seconds that the account stays locked, or 0. */
    public static function lockedFor(string $email): int
    {
        $until = (int) Cache::get(self::key($email, 'lock'), 0);

        return max(0, $until - time());
    }

    public static function fail(string $email): void
    {
        $key = self::key($email, 'failures');
        $failures = (int) Cache::get($key, 0) + 1;
        Cache::put($key, $failures, now()->addDay());
        if ($failures >= self::FREE_FAILURES) {
            $seconds = min(self::MAX_LOCK_SECONDS, self::FIRST_LOCK_SECONDS * 2 ** ($failures - self::FREE_FAILURES));
            Cache::put(self::key($email, 'lock'), time() + $seconds, now()->addSeconds($seconds));
        }
    }

    public static function clear(string $email): void
    {
        Cache::forget(self::key($email, 'failures'));
        Cache::forget(self::key($email, 'lock'));
    }

    private static function key(string $email, string $what): string
    {
        return "login-{$what}:".hash('sha256', mb_strtolower(trim($email)));
    }
}
