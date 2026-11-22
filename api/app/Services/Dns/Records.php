<?php

namespace App\Services\Dns;

use App\Models\DnsRecord;
use App\Models\DnsZone;
use App\Validation\Input;
use App\Validation\InputRules;
use Illuminate\Validation\ValidationException;

/**
 * The rules for the records of a DNS zone. The panel checks each record before it saves it,
 * so that the DNS servers can always load the zone. The agent checks each record again with
 * the same rules (schema/input-rules.json).
 */
class Records
{
    /** The types with a priority. */
    public const WITH_PRIORITY = ['MX', 'SRV'];

    /** The TTL of a new record when the request gives none: one hour. */
    public const DEFAULT_TTL = 3600;

    /**
     * Checks the values of a new record, or of a change of a record, and gives them in the form
     * that the panel keeps: lower case, "@" for the zone itself, no dot at the end of a host
     * name.
     *
     * @param  array<string, mixed>  $input  "type", "name", "value", "ttl" and "priority".
     * @param  DnsRecord|null  $editing  The record that the request changes. Its type stays.
     * @return array{name: string, type: string, ttl: int, value: string, priority: ?int}
     *
     * @throws ValidationException
     */
    public static function check(DnsZone $zone, array $input, ?DnsRecord $editing = null): array
    {
        $record = self::normalize($zone, $input, $editing);
        self::checkPlace($zone, $record['type'], $record['name'], $record['value'], $record['priority'], $editing);

        return $record;
    }

    /**
     * Checks the values of a record with the shared input rules, and gives them in the form that
     * the panel keeps. It does not compare the record with the other records of the zone, so
     * that the zone can be a zone that is not saved yet (for example the preview of an import).
     *
     * @param  array<string, mixed>  $input  "type", "name", "value", "ttl" and "priority".
     * @return array{name: string, type: string, ttl: int, value: string, priority: ?int}
     *
     * @throws ValidationException
     */
    public static function normalize(DnsZone $zone, array $input, ?DnsRecord $editing = null): array
    {
        $type = $editing?->type ?? strtoupper(trim((string) ($input['type'] ?? '')));
        if (! in_array($type, InputRules::DNS_RECORD_TYPES, true)) {
            self::fail('type', 'Select one of these types: '.implode(', ', InputRules::DNS_RECORD_TYPES).'.');
        }
        $name = self::name($zone, (string) ($input['name'] ?? $editing?->name ?? '@'));
        $ttl = $input['ttl'] ?? $editing?->ttl ?? self::DEFAULT_TTL;
        if (! is_int($ttl) || $ttl < InputRules::MIN_DNS_SHORT_TTL || $ttl > InputRules::MAX_DNS_TTL) {
            self::fail('ttl', 'Use a TTL from '.InputRules::MIN_DNS_SHORT_TTL.' to '.InputRules::MAX_DNS_TTL.' seconds.');
        }
        $value = self::value($type, (string) ($input['value'] ?? $editing?->value ?? ''));
        $priority = null;
        if (in_array($type, self::WITH_PRIORITY, true)) {
            $priority = $input['priority'] ?? $editing?->priority;
            if (! is_int($priority) || $priority < 0 || $priority > 65535) {
                self::fail('priority', 'Use a priority from 0 to 65535.');
            }
        }

        return ['name' => $name, 'type' => $type, 'ttl' => $ttl, 'value' => $value, 'priority' => $priority];
    }

    /** The name of a record in its zone. A user can also type the full name. */
    private static function name(DnsZone $zone, string $name): string
    {
        $name = strtolower(rtrim(trim($name), '.'));
        if ($name === '' || $name === $zone->name) {
            return '@';
        }
        if (str_ends_with($name, '.'.$zone->name)) {
            $name = $zone->relative($name);
        }
        if (! Input::valid('dnsRecordName', $name)) {
            self::fail('name', InputRules::MESSAGES['dnsRecordName']);
        }
        if (strlen($zone->absolute($name)) > 253) {
            self::fail('name', 'The full name of the record has more than 253 characters.');
        }

        return $name;
    }

    /** The value of a record, checked with the rule of its type. */
    private static function value(string $type, string $value): string
    {
        $value = trim($value);
        if ($value === '') {
            self::fail('value', 'Enter a value.');
        }
        [$rule, $value] = match ($type) {
            'A' => ['ipv4', $value],
            'AAAA' => ['ipv6', strtolower($value)],
            'CNAME', 'NS', 'MX' => ['dnsHost', strtolower(rtrim($value, '.'))],
            'SRV' => ['dnsSrv', strtolower(rtrim($value, '.'))],
            'CAA' => ['dnsCaa', $value],
            default => ['dnsTxt', $value],
        };
        if (! Input::valid($rule, $value)) {
            self::fail('value', match ($type) {
                'MX' => 'Enter the full host name of the mail server in lower-case letters, for example mail.example.com.',
                'CNAME' => 'Enter the full host name that this name points to, for example shop.example.net.',
                'NS' => 'Enter the full host name of the name server, for example ns1.example.net.',
                default => InputRules::MESSAGES[$rule],
            });
        }

        return $value;
    }

    /**
     * The rules of the DNS for records with the same name: a name with a CNAME record has no
     * other record, and the zone itself has no CNAME record. The panel keeps the NS records of
     * the zone itself.
     */
    private static function checkPlace(DnsZone $zone, string $type, string $name, string $value, ?int $priority, ?DnsRecord $editing): void
    {
        if ($name === '@' && $type === 'CNAME') {
            self::fail('name', 'The zone itself cannot have a CNAME record. Use an A record or an AAAA record for @.');
        }
        if ($name === '@' && $type === 'NS') {
            self::fail('name', 'The panel keeps the name servers of the zone itself. Use a name such as "shop" to send a part of the zone to other name servers.');
        }
        $others = $zone->records()->where('name', $name)->when($editing, fn ($q) => $q->whereKeyNot($editing->id))->get();
        if ($type === 'CNAME' && $others->isNotEmpty()) {
            self::fail('name', $others->contains('type', 'CNAME')
                ? 'This name has a CNAME record already. A name can have only one.'
                : 'This name has other records. A name with a CNAME record cannot have other records.');
        }
        if ($type !== 'CNAME' && $others->contains('type', 'CNAME')) {
            self::fail('name', 'This name has a CNAME record. A name with a CNAME record cannot have other records.');
        }
        if ($others->contains(fn (DnsRecord $r) => $r->type === $type && $r->value === $value && $r->priority === $priority)) {
            self::fail('value', 'The zone has this record already.');
        }
        if (! $editing && $zone->records()->count() >= InputRules::MAX_DNS_RECORDS_PER_ZONE) {
            self::fail('name', 'A zone can have '.InputRules::MAX_DNS_RECORDS_PER_ZONE.' records.');
        }
    }

    /** @throws ValidationException */
    private static function fail(string $field, string $message): never
    {
        throw ValidationException::withMessages([$field => $message]);
    }
}
