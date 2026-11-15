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
        if (DemoServiceProvider::enabled()) {
            return null;
        }
        $addresses = filter_var($host, FILTER_VALIDATE_IP) ? [$host] : self::resolve($host);
        if ($addresses === []) {
            return 'The panel cannot find the address of this host. Check the name.';
        }
        foreach ($addresses as $ip) {
            if (! filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE)) {
                return 'The host must have a public address. Ask your provider for a private SMTP relay.';
            }
        }

        return null;
    }

    /** @return list<string> */
    private static function resolve(string $host): array
    {
        $v4 = @gethostbynamel($host) ?: [];
        $v6 = array_column(@dns_get_record($host, DNS_AAAA) ?: [], 'ipv6');

        return array_values(array_unique([...$v4, ...$v6]));
    }
}
