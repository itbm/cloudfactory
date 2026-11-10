<?php

namespace App\Services;

use Illuminate\Support\Facades\Cache;

/**
 * CF-12: a limit for each account, in addition to the limit for each IP address. After
 * FREE_FAILURES failed attempts, the account is locked for a time that doubles after each
 * further failure, up to MAX_LOCK_SECONDS. A good sign-in clears the count.
 *
 * R-09: the lock is for the account and the source network together, so that somebody who
 * knows an email cannot lock the real user out from a different network.
 *
 * Audit finding S3: many failures from many networks lock the account only for the networks
 * that the user did not use before. A network from which the user signed in during the last
 * TRUSTED_DAYS days stays open, so an attacker cannot keep the administrator out of the panel.
 * An IPv6 network is the /64 prefix, because one client has all addresses of its prefix.
 * "php artisan cloudfactory:unlock" removes the lock for all networks.
 */
class LoginThrottle
{
    public const FREE_FAILURES = 5;

    public const FIRST_LOCK_SECONDS = 30;

    public const MAX_LOCK_SECONDS = 3600;

    /** Failures in one hour, from all networks, after which the account locks for new networks. */
    public const DISTRIBUTED_FAILURES = 100;

    /** The time for which the panel knows a network after a good sign-in. */
    public const TRUSTED_DAYS = 30;

    /** The number of seconds that the account stays locked for this address, or 0. */
    public static function lockedFor(string $email, string $ip): int
    {
        $network = self::network($ip);
        $until = (int) Cache::get(self::key($email, $network, 'lock'), 0);
        if (! Cache::has(self::key($email, $network, 'trusted'))) {
            $until = max($until, (int) Cache::get(self::key($email, '*', 'lock'), 0));
        }

        return max(0, $until - time());
    }

    /** Records a failure. It returns true when this failure locked the account. */
    public static function fail(string $email, string $ip): bool
    {
        $locked = self::count($email, self::network($ip), self::FREE_FAILURES, now()->addDay());

        // Many failures from many networks: an attack on the password, not a typing error.
        return self::count($email, '*', self::DISTRIBUTED_FAILURES, now()->addHour()) || $locked;
    }

    /** A good sign-in. It clears the counts, and the panel knows the network from now on. */
    public static function clear(string $email, string $ip): void
    {
        $network = self::network($ip);
        Cache::forget(self::key($email, $network, 'failures'));
        Cache::forget(self::key($email, $network, 'lock'));
        Cache::put(self::key($email, $network, 'trusted'), true, now()->addDays(self::TRUSTED_DAYS));
        self::unlock($email);
    }

    /**
     * Removes the lock of the account for all networks. The lock for one network, after
     * FREE_FAILURES failures from that network, ends by itself after MAX_LOCK_SECONDS at most.
     * With an address, this function removes that lock also.
     */
    public static function unlock(string $email, ?string $ip = null): void
    {
        Cache::forget(self::key($email, '*', 'failures'));
        Cache::forget(self::key($email, '*', 'lock'));
        if ($ip !== null) {
            Cache::forget(self::key($email, self::network($ip), 'failures'));
            Cache::forget(self::key($email, self::network($ip), 'lock'));
        }
    }

    /**
     * The network of a client address: the address itself for IPv4, and the /64 prefix for
     * IPv6. An address that PHP cannot read stays as it is.
     */
    public static function network(string $ip): string
    {
        $packed = @inet_pton($ip);
        if ($packed === false || strlen($packed) !== 16) {
            return $ip;
        }
        // An IPv4 address in IPv6 form (::ffff:192.0.2.1) is one client, not a /64 network.
        if (str_starts_with($packed, str_repeat("\0", 10)."\xff\xff")) {
            return (string) inet_ntop(substr($packed, 12));
        }

        return inet_ntop(substr($packed, 0, 8).str_repeat("\0", 8)).'/64';
    }

    private static function count(string $email, string $network, int $free, \DateTimeInterface $keep): bool
    {
        $key = self::key($email, $network, 'failures');
        $failures = (int) Cache::get($key, 0) + 1;
        Cache::put($key, $failures, $keep);
        if ($failures < $free) {
            return false;
        }
        $seconds = min(self::MAX_LOCK_SECONDS, self::FIRST_LOCK_SECONDS * 2 ** min(20, $failures - $free));
        Cache::put(self::key($email, $network, 'lock'), time() + $seconds, now()->addSeconds($seconds));

        return $failures === $free;
    }

    private static function key(string $email, string $network, string $what): string
    {
        return "login-{$what}:".hash('sha256', mb_strtolower(trim($email)).'|'.$network);
    }
}
