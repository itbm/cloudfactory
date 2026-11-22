<?php

namespace App\Services\Dns;

use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Validation\InputRules;
use Illuminate\Validation\ValidationException;

/**
 * Zone files in the text format of RFC 1035, section 5 (the format of BIND). Most DNS
 * providers and hosting panels export a zone in this format, for example cPanel
 * (dnszones/<domain>.db), DirectAdmin and Cloudflare.
 *
 * parse() reads a zone file and gives the records in the form that the panel keeps, with the
 * shared input rules. It does not read the database, so the zone can be a zone that the panel
 * does not have yet, for example in the preview of an import. It gives each line that it does
 * not use, with a code and a reason. render() writes the records of a zone as a zone file.
 *
 * The parser reads only data. It does not follow $INCLUDE, and it does not run $GENERATE.
 */
class ZoneFile
{
    /** The largest zone file that the panel reads: 1 MB. */
    public const MAX_BYTES = 1048576;

    /** The codes of a line that the parser does not use. */
    public const SYNTAX = 'syntax';

    /** A directive that the panel does not use, for example $INCLUDE. */
    public const DIRECTIVE = 'directive';

    /** A record of a class that is not IN. */
    public const CLASS_NOT_IN = 'class';

    /** A name that is not in the zone. */
    public const OUTSIDE = 'outside';

    /** The SOA record. The panel or the DNS provider makes it. */
    public const SOA = 'soa';

    /** A type that the panel does not have, for example PTR. */
    public const TYPE = 'type';

    /** A record that the panel keeps itself: the name servers of the zone. */
    public const PANEL = 'panel';

    /** A value that the shared input rules refuse. */
    public const INVALID = 'invalid';

    /** The same record is on an earlier line. */
    public const DUPLICATE = 'duplicate';

    /** The record names a website: the panel makes the address records of the website. */
    public const WEBSITE = 'website';

    /** The longest text of a line in a result. */
    private const TEXT_LENGTH = 200;

    /**
     * Reads a zone file.
     *
     * @param  string  $origin  The name of the zone, for example "example.com". A name in the
     *                          file without a dot at the end is relative to it, until $ORIGIN.
     * @return array{records: list<array{line: int, text: string, name: string, type: string, ttl: int, value: string, priority: ?int, note: ?string}>, skipped: list<array{line: int, text: string, code: string, reason: string}>}
     */
    public static function parse(string $origin, string $text): array
    {
        $origin = strtolower(rtrim(trim($origin), '.'));
        $zone = new DnsZone(['name' => $origin]);
        $records = [];
        $skipped = [];
        $seen = [];
        $current = $origin;
        $defaultTtl = null;
        $lastTtl = null;
        $lastOwner = null;
        $lines = preg_split('/\r\n|\n|\r/', $text) ?: [];
        $skip = function (array $entry, string $code, string $reason) use (&$skipped, $lines) {
            $skipped[] = ['line' => $entry['line'], 'text' => self::text($lines, $entry), 'code' => $code, 'reason' => $reason];
        };

        foreach (self::entries($text) as $entry) {
            if ($entry['error'] !== null) {
                $skip($entry, self::SYNTAX, $entry['error']);

                continue;
            }
            $tokens = $entry['tokens'];
            [$first, $quoted] = $tokens[0];
            if (! $entry['blank'] && ! $quoted && str_starts_with($first, '$')) {
                $directive = strtoupper($first);
                $argument = $tokens[1][0] ?? null;
                if ($directive === '$ORIGIN' && $argument !== null && ($name = self::absolute($argument, $current)) !== null) {
                    $current = $name;
                } elseif ($directive === '$TTL' && $argument !== null && ($ttl = self::ttl($argument)) !== null) {
                    $defaultTtl = $ttl;
                } elseif ($directive === '$INCLUDE') {
                    $skip($entry, self::DIRECTIVE, 'The panel does not read other files. Put the records of that file in the zone file.');
                } elseif ($directive === '$GENERATE') {
                    $skip($entry, self::DIRECTIVE, 'The panel does not make records from $GENERATE. Write each record on its own line.');
                } else {
                    $skip($entry, self::DIRECTIVE, "The panel does not know the directive {$first}.");
                }

                continue;
            }

            if ($entry['blank']) {
                if ($lastOwner === null) {
                    $skip($entry, self::SYNTAX, 'The line has no name, and no line before it has a name.');

                    continue;
                }
                $owner = $lastOwner;
                $rest = $tokens;
            } else {
                $owner = $quoted ? null : self::absolute($first, $current);
                $rest = array_slice($tokens, 1);
                if ($owner === null) {
                    $skip($entry, self::INVALID, "The name {$first} is not a valid DNS name.");

                    continue;
                }
                $lastOwner = $owner;
            }

            // The TTL and the class can come in each sequence, and both are optional.
            $ttl = null;
            $class = null;
            for ($i = 0; $i < 2 && $rest !== [] && ! $rest[0][1]; $i++) {
                if ($ttl === null && ($t = self::ttl($rest[0][0])) !== null) {
                    $ttl = $t;
                } elseif ($class === null && in_array(strtoupper($rest[0][0]), ['IN', 'CH', 'HS', 'CS', 'ANY'], true)) {
                    $class = strtoupper($rest[0][0]);
                } else {
                    break;
                }
                array_shift($rest);
            }
            if ($rest === [] || $rest[0][1]) {
                $skip($entry, self::SYNTAX, 'The line has no record type.');

                continue;
            }
            if ($ttl !== null) {
                $lastTtl = $ttl;
            }
            $ttl ??= $defaultTtl ?? $lastTtl ?? Records::DEFAULT_TTL;
            $type = strtoupper(array_shift($rest)[0]);

            if ($class !== null && $class !== 'IN') {
                $skip($entry, self::CLASS_NOT_IN, "The panel has only records of the class IN. This record has the class {$class}.");

                continue;
            }
            if (! $zone->contains($owner)) {
                $skip($entry, self::OUTSIDE, "The name {$owner} is not in the zone {$origin}.");

                continue;
            }
            if ($type === 'SOA') {
                $skip($entry, self::SOA, 'The panel makes the SOA record of the zone.');

                continue;
            }
            if (! in_array($type, InputRules::DNS_RECORD_TYPES, true)) {
                $skip($entry, self::TYPE, "The panel does not have records of the type {$type}. The types are ".implode(', ', InputRules::DNS_RECORD_TYPES).'.');

                continue;
            }
            $name = $zone->relative($owner);
            if ($type === 'NS' && $name === '@') {
                $skip($entry, self::PANEL, 'The panel keeps the name servers of the zone itself.');

                continue;
            }

            $input = self::input($type, $rest, $current);
            if (is_string($input)) {
                $skip($entry, self::INVALID, $input);

                continue;
            }
            $note = null;
            $clamped = max(InputRules::MIN_DNS_SHORT_TTL, min(InputRules::MAX_DNS_TTL, $ttl));
            if ($clamped !== $ttl) {
                $note = "The TTL was {$ttl} seconds. The panel uses {$clamped} seconds.";
            }
            try {
                $record = Records::normalize($zone, ['name' => $name, 'type' => $type, 'ttl' => $clamped] + $input);
            } catch (ValidationException $e) {
                $skip($entry, self::INVALID, collect($e->errors())->flatten()->first() ?? 'The record is not valid.');

                continue;
            }
            $key = implode("\0", [$record['name'], $record['type'], $record['value'], (string) $record['priority']]);
            if (isset($seen[$key])) {
                $skip($entry, self::DUPLICATE, "The same record is on line {$seen[$key]}.");

                continue;
            }
            $seen[$key] = $entry['line'];
            $records[] = ['line' => $entry['line'], 'text' => self::text($lines, $entry)] + $record + ['note' => $note];
        }

        return ['records' => $records, 'skipped' => $skipped];
    }

    /**
     * The fields of a record from the data after its type, or the reason that the data is not
     * correct.
     *
     * @param  list<array{0: string, 1: bool}>  $data
     * @return array{value: string, priority?: int}|string
     */
    private static function input(string $type, array $data, string $origin): array|string
    {
        $count = count($data);
        $plain = array_map(fn (array $t) => $t[0], $data);
        $need = fn (int $n, string $what) => $count === $n ? null : "A record of the type {$type} has {$what}.";
        switch ($type) {
            case 'A':
            case 'AAAA':
                return $need(1, 'one address') ?? ['value' => $plain[0]];
            case 'CNAME':
            case 'NS':
                return $need(1, 'one host name') ?? self::target($plain[0], $origin) ?? "The host name {$plain[0]} is not valid.";
            case 'MX':
                if ($error = $need(2, 'a priority and a host name')) {
                    return $error;
                }
                $host = self::target($plain[1], $origin);

                return ! ctype_digit($plain[0]) ? 'The priority is not a number.'
                    : ($host === null ? "The host name {$plain[1]} is not valid." : $host + ['priority' => (int) $plain[0]]);
            case 'SRV':
                if ($error = $need(4, 'a priority, a weight, a port and a host name')) {
                    return $error;
                }
                $host = self::target($plain[3], $origin);
                if (! ctype_digit($plain[0]) || ! ctype_digit($plain[1]) || ! ctype_digit($plain[2])) {
                    return 'The priority, the weight and the port are numbers.';
                }
                if ($host === null || $host['value'] === '') {
                    return 'The record has no host name of a service.';
                }

                return ['value' => (int) $plain[1].' '.(int) $plain[2].' '.$host['value'], 'priority' => (int) $plain[0]];
            case 'CAA':
                if ($error = $need(3, 'flags, a tag and a value')) {
                    return $error;
                }

                return ['value' => $plain[0].' '.strtolower($plain[1]).' "'.self::unescape($plain[2]).'"'];
            default:
                // TXT: one or more strings, with or without quotation marks. The panel keeps
                // the text as one value, and the DNS server divides it again.
                return $count === 0 ? 'The record has no text.' : ['value' => implode('', array_map(fn (string $s) => self::unescape($s), $plain))];
        }
    }

    /** The host name in the data of a record, with no dot at the end, or null. */
    private static function target(string $name, string $origin): ?array
    {
        if ($name === '.') {
            return ['value' => ''];
        }
        $host = self::absolute($name, $origin);

        return $host === null ? null : ['value' => $host];
    }

    /** A name of the file as a full name: "@" is the origin, and a name with no dot at the end is relative to it. */
    private static function absolute(string $name, string $origin): ?string
    {
        if ($name === '' || str_contains($name, '\\')) {
            return null;
        }
        if ($name === '@') {
            return $origin;
        }
        $name = strtolower($name);
        $full = str_ends_with($name, '.') ? substr($name, 0, -1) : "{$name}.{$origin}";

        return $full === '' || str_contains($full, '..') || str_starts_with($full, '.') ? null : $full;
    }

    /** A TTL in seconds: a number, or a value with units as BIND writes it, for example 1h30m. */
    private static function ttl(string $text): ?int
    {
        if (preg_match('/^\d{1,10}$/D', $text)) {
            return (int) $text;
        }
        if (! preg_match('/^(\d{1,7}[wdhms])+$/Di', $text)) {
            return null;
        }
        preg_match_all('/(\d+)([wdhms])/i', $text, $parts, PREG_SET_ORDER);
        $units = ['w' => 604800, 'd' => 86400, 'h' => 3600, 'm' => 60, 's' => 1];

        return array_sum(array_map(fn (array $p) => (int) $p[1] * $units[strtolower($p[2])], $parts));
    }

    /** The text of a string of the file: "\X" is X, and "\DDD" is the byte with the decimal value DDD. */
    private static function unescape(string $text): string
    {
        return preg_replace_callback('/\\\\(\d{3}|.)/s', fn (array $m) => strlen($m[1]) === 3 ? chr(min(255, (int) $m[1])) : $m[1], $text) ?? $text;
    }

    /**
     * Divides the text into entries: one entry for each record or directive. An entry can have
     * more than one line when it has brackets. A comment starts with ";" outside a string.
     *
     * @return list<array{line: int, last: int, blank: bool, tokens: list<array{0: string, 1: bool}>, error: ?string}>
     */
    private static function entries(string $text): array
    {
        $entries = [];
        $entry = null;
        $depth = 0;
        $line = 1;
        $length = strlen($text);
        $finish = function () use (&$entry, &$entries, &$depth, &$line) {
            if ($entry !== null && ($entry['tokens'] !== [] || $entry['error'] !== null)) {
                if ($depth > 0 && $entry['error'] === null) {
                    $entry['error'] = 'A bracket "(" has no closing bracket ")".';
                }
                $entry['last'] = $line;
                $entries[] = $entry;
            }
            $entry = null;
            $depth = 0;
        };
        for ($i = 0; $i < $length; $i++) {
            $c = $text[$i];
            if ($c === "\n" || ($c === "\r" && ($text[$i + 1] ?? '') !== "\n")) {
                if ($depth === 0) {
                    $finish();
                }
                $line++;

                continue;
            }
            $entry ??= ['line' => $line, 'last' => $line, 'blank' => $c === ' ' || $c === "\t", 'tokens' => [], 'error' => null];
            if ($c === ' ' || $c === "\t" || $c === "\r") {
                continue;
            }
            if ($c === ';') {
                while ($i + 1 < $length && $text[$i + 1] !== "\n" && $text[$i + 1] !== "\r") {
                    $i++;
                }

                continue;
            }
            if ($c === '(') {
                $depth++;

                continue;
            }
            if ($c === ')') {
                if ($depth === 0) {
                    $entry['error'] ??= 'A closing bracket ")" has no bracket "(".';
                }
                $depth = max(0, $depth - 1);

                continue;
            }
            $quoted = $c === '"';
            $token = '';
            if ($quoted) {
                for ($i++; $i < $length && $text[$i] !== '"'; $i++) {
                    if ($text[$i] === "\n" || $text[$i] === "\r") {
                        $entry['error'] ??= 'A text in quotation marks has no closing quotation mark on its line.';
                        $i--;
                        break;
                    }
                    if ($text[$i] === '\\' && $i + 1 < $length) {
                        $token .= $text[$i++];
                    }
                    $token .= $text[$i];
                }
                if ($i >= $length) {
                    $entry['error'] ??= 'A text in quotation marks has no closing quotation mark on its line.';
                }
            } else {
                for (; $i < $length && ! in_array($text[$i], [' ', "\t", "\r", "\n", ';', '(', ')', '"'], true); $i++) {
                    if ($text[$i] === '\\' && $i + 1 < $length) {
                        $token .= $text[$i++];
                    }
                    $token .= $text[$i];
                }
                $i--;
            }
            $entry['tokens'][] = [$token, $quoted];
        }
        $finish();

        return $entries;
    }

    /** The lines of an entry, for a result: one line of text, with no more than TEXT_LENGTH characters. */
    private static function text(array $lines, array $entry): string
    {
        $text = trim(preg_replace('/\s+/', ' ', implode(' ', array_slice($lines, $entry['line'] - 1, $entry['last'] - $entry['line'] + 1))) ?? '');

        return mb_strlen($text) > self::TEXT_LENGTH ? mb_substr($text, 0, self::TEXT_LENGTH - 1).'…' : $text;
    }

    /**
     * Writes the records of a zone as a zone file. The file has the SOA record that the DNS
     * servers of the panel give, so that a different DNS server can load the file.
     */
    public static function render(DnsZone $zone): string
    {
        $records = $zone->records()->orderByRaw("case when name = '@' then 0 else 1 end")->orderBy('name')->orderBy('type')->orderBy('value')->get();
        $width = min(40, max([1, ...$records->map(fn (DnsRecord $r) => strlen($r->name))->all()]));
        $line = fn (string $name, int $ttl, string $type, string $data) => str_pad($name, $width).' '.str_pad((string) $ttl, 6).' IN '.str_pad($type, 5)." {$data}";
        $out = [
            "; The DNS zone {$zone->name}, from Cloudfactory at ".now()->toIso8601String().'.',
            "\$ORIGIN {$zone->name}.",
            '$TTL '.Records::DEFAULT_TTL,
        ];
        $primary = $records->first(fn (DnsRecord $r) => $r->name === '@' && $r->type === 'NS');
        if ($primary) {
            // The same SOA record as the agent writes (agent/internal/actions/dns.go).
            $out[] = $line('@', 3600, 'SOA', "{$primary->value}. hostmaster.{$zone->name}. {$zone->serial} 3600 900 1209600 300");
        }
        foreach ($records as $r) {
            $out[] = $line($r->name, $r->ttl, $r->type, match ($r->type) {
                'CNAME', 'NS' => "{$r->value}.",
                'MX', 'SRV' => ((int) $r->priority)." {$r->value}.",
                'TXT' => self::quote($r->value),
                default => $r->value,
            });
        }

        return implode("\n", $out)."\n";
    }

    /** A text as the strings of a TXT record, each with 255 characters or less. */
    private static function quote(string $text): string
    {
        $parts = [];
        foreach (str_split($text, 255) as $part) {
            $parts[] = '"'.strtr($part, ['\\' => '\\\\', '"' => '\\"']).'"';
        }

        return implode(' ', $parts);
    }
}
