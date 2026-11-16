<?php

namespace App\Services\Notifications;

use App\Providers\DemoServiceProvider;

/**
 * The checks of the SMTP server of a reseller. The panel connects to that server, so a reseller
 * must not be able to make the panel connect to an address in the private network of the
 * operator, for example the database of the panel. An administrator can use a private relay.
 */
class SmtpHost
{
    /** The SMTP ports. The panel connects to no other port. */
    public const PORTS = [25, 465, 587, 2525];

    /** A host name or an IPv4 address. */
    public const HOST = '/^(?=.{1,253}$)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/';

    /**
     * Null when the panel can connect to the host for a reseller, else the reason why not.
     */
    public static function refusalForReseller(string $host): ?string
    {
        return self::checked($host)[1];
    }

    /**
     * Adversarial review, finding A8-01: the address that the panel connects to for a reseller,
     * or the reason why it does not connect. The panel finds the addresses one time, checks
     * each one, and connects to an address that it checked. Thus the name cannot give a
     * different address between the check and the connection.
     *
     * @return array{0: ?string, 1: ?string} The address and the refusal. One of them is null.
     */
    public static function checked(string $host): array
    {
        if (DemoServiceProvider::enabled()) {
            return [null, null];
        }
        $addresses = filter_var($host, FILTER_VALIDATE_IP) ? [$host] : self::resolve($host);
        if ($addresses === []) {
            return [null, 'The panel cannot find the address of this host. Check the name.'];
        }
        foreach ($addresses as $ip) {
            if (! self::isPublic($ip)) {
                return [null, 'The host must have a public address. Ask your provider for a private SMTP relay.'];
            }
        }

        return [$addresses[0], null];
    }

    /** IPv4 ranges that are not public and that the filter of PHP does not know. */
    private const NOT_PUBLIC = [
        ['100.64.0.0', 10], ['192.0.0.0', 24], ['198.18.0.0', 15], ['224.0.0.0', 4], ['192.88.99.0', 24],
    ];

    /** True for a public unicast address. An IPv4 address in an IPv6 form has the IPv4 rules. */
    public static function isPublic(string $ip): bool
    {
        $bin = @inet_pton($ip);
        if ($bin === false) {
            return false;
        }
        if (strlen($bin) === 16) {
            $zero = str_repeat("\0", 8);
            // ::ffff:a.b.c.d, ::a.b.c.d, NAT64 (64:ff9b::/96) and 6to4 (2002:a.b.c.d::/48).
            if (str_starts_with($bin, $zero."\0\0\xff\xff") || str_starts_with($bin, "\x00\x64\xff\x9b".$zero)) {
                $bin = substr($bin, 12);
            } elseif (str_starts_with($bin, $zero."\0\0\0\0")) {
                return false;
            } elseif (str_starts_with($bin, "\x20\x02")) {
                $bin = substr($bin, 2, 4);
            }
        }
        if (strlen($bin) === 16) {
            $first = ord($bin[0]);

            // Unique local (fc00::/7), link-local and site-local (fe80::/9), multicast (ff00::/8).
            return ($first & 0xFE) !== 0xFC && $first !== 0xFF && ! ($first === 0xFE && (ord($bin[1]) & 0x80) === 0x80)
                && (bool) filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE);
        }
        $v4 = inet_ntop($bin);
        if (! filter_var($v4, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4 | FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE)) {
            return false;
        }
        $n = ip2long($v4);
        foreach (self::NOT_PUBLIC as [$net, $bits]) {
            if (($n & (-1 << (32 - $bits))) === (ip2long($net) & (-1 << (32 - $bits)))) {
                return false;
            }
        }

        return true;
    }

    /** @return list<string> */
    private static function resolve(string $host): array
    {
        $v4 = @gethostbynamel($host) ?: [];
        $v6 = array_column(@dns_get_record($host, DNS_AAAA) ?: [], 'ipv6');

        return array_values(array_unique([...$v4, ...$v6]));
    }
}
