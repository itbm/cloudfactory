<?php

namespace App\Services\Exports;

/**
 * The zone file (RFC 1035) of a DNS zone in an account export. Most DNS providers can import
 * it. The lines are the lines that the agent writes for Knot DNS (agent/internal/actions/dns.go,
 * renderZone): the panel checked each value with the rules of schema/input-rules.json when the
 * user made the record, so no value can add a line or a directive.
 */
class ZoneText
{
    /**
     * @param  list<array{name: string, type: string, ttl: int, value: string, priority: ?int}>  $records
     */
    public static function render(string $zone, int $serial, array $records): string
    {
        $primary = null;
        $lines = [];
        foreach ($records as $r) {
            if ($primary === null && $r['name'] === '@' && $r['type'] === 'NS') {
                $primary = $r['value'];
            }
            $lines[] = self::line($r);
        }
        sort($lines, SORT_STRING);
        $lines = array_values(array_unique($lines));
        $out = "; The DNS zone {$zone} from an account export of Cloudfactory.\n";
        $out .= "\$ORIGIN {$zone}.\n\$TTL 3600\n";
        if ($primary !== null) {
            $out .= "@ 3600 IN SOA {$primary}. hostmaster.{$zone}. {$serial} 3600 900 1209600 300\n";
        }

        return $out.implode('', array_map(fn (string $l) => $l."\n", $lines));
    }

    /** @param array{name: string, type: string, ttl: int, value: string, priority: ?int} $r */
    private static function line(array $r): string
    {
        $data = match ($r['type']) {
            'CNAME', 'NS' => $r['value'].'.',
            'MX', 'SRV' => ((int) $r['priority']).' '.$r['value'].'.',
            'TXT' => self::quote($r['value']),
            default => $r['value'],
        };

        return "{$r['name']} {$r['ttl']} IN {$r['type']} {$data}";
    }

    /** The text of a TXT record as strings of at most 255 characters (agent: quoteTxt). */
    private static function quote(string $text): string
    {
        $parts = [];
        foreach (str_split($text, 255) as $part) {
            $parts[] = '"'.strtr($part, ['\\' => '\\\\', '"' => '\\"']).'"';
        }

        return implode(' ', $parts ?: ['""']);
    }
}
