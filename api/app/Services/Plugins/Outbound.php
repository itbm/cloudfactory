<?php

namespace App\Services\Plugins;

/**
 * Audit rule for each webhook: the panel does not send a request to its own server. The panel
 * resolves the host name one time, refuses the addresses of the loopback interface, link-local
 * addresses (with the private service address 169.254.77.1), multicast addresses and
 * unspecified addresses, and then connects to the address that it checked. Thus a DNS answer
 * that changes between the check and the connection has no effect.
 *
 * Private addresses (for example 10.0.0.0/8) are permitted, because an administrator can run
 * a plugin in a private network. The developer mode also permits the loopback interface.
 */
class Outbound
{
    /** @return list<string> */
    public function addresses(string $host): array
    {
        if (filter_var($host, FILTER_VALIDATE_IP)) {
            return [$host];
        }
        $out = [];
        foreach (@dns_get_record($host, DNS_A | DNS_AAAA) ?: [] as $record) {
            if (isset($record['ip'])) {
                $out[] = $record['ip'];
            } elseif (isset($record['ipv6'])) {
                $out[] = $record['ipv6'];
            }
        }

        return array_values(array_unique($out));
    }

    /** The reason that the address is refused, or null. */
    public static function refused(string $ip, bool $developerMode): ?string
    {
        $bin = @inet_pton($ip);
        if ($bin === false) {
            return 'is not an IP address';
        }
        // An IPv4 address in IPv6 form (::ffff:a.b.c.d) has the rules of the IPv4 address.
        if (strlen($bin) === 16 && str_starts_with($bin, str_repeat("\0", 10)."\xff\xff")) {
            $bin = substr($bin, 12);
        }
        if (strlen($bin) === 4) {
            $a = ord($bin[0]);
            $b = ord($bin[1]);
            if ($a === 127) {
                return $developerMode ? null : 'is on the loopback interface';
            }
            if ($a === 0 || $a >= 224) {
                return 'is not a unicast address';
            }
            if ($a === 169 && $b === 254) {
                return 'is a link-local address';
            }

            return null;
        }
        if ($bin === str_repeat("\0", 15)."\1") {
            return $developerMode ? null : 'is on the loopback interface';
        }
        if ($bin === str_repeat("\0", 16) || ord($bin[0]) === 0xFF) {
            return 'is not a unicast address';
        }
        if (ord($bin[0]) === 0xFE && (ord($bin[1]) & 0xC0) === 0x80) {
            return 'is a link-local address';
        }

        return null;
    }
}
