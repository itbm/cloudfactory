<?php

namespace App\Demo;

use App\Models\Mailbox;
use App\Services\AgentCallException;
use Illuminate\Support\Facades\Cache;

/**
 * The autoresponders of the mailboxes in the demonstration mode. A real agent reads them from
 * the mail server. The demo agent keeps them in the cache. The answers have the form of the
 * JSON of a real agent: no field with an empty value.
 */
class DemoMail
{
    /** The autoresponders of the demonstration data, by address. */
    private const DEFAULTS = [
        'olu@acme.example.com' => [
            'enabled' => true,
            'subject' => 'Out of office',
            'text' => "Hello,\n\nI am on leave until 4 January, with no access to my email. For an order, write to sales@acme.example.com.\n\nOlu",
            'startsAt' => '2026-12-19T00:00:00Z',
            'endsAt' => '2027-01-04T23:59:59Z',
        ],
        'finance@acme.example.com' => ['filterScript' => 'filters'],
    ];

    /**
     * @param  array<string, mixed>  $p
     * @return array<string, mixed>
     */
    public static function call(string $method, array $p): array
    {
        $address = ($p['localPart'] ?? '').'@'.($p['domain'] ?? '');
        $exists = Mailbox::whereHas('domain', fn ($q) => $q->where('name', $p['domain'] ?? ''))->where('local_part', $p['localPart'] ?? '')->exists();
        if (! $exists) {
            throw new AgentCallException('not_found', 'the mail server does not have this mailbox: '.$address);
        }
        $key = 'demo:autoresponder:'.$address;
        $current = Cache::get($key) ?? self::DEFAULTS[$address] ?? [];

        return match ($method) {
            'GetAutoresponder' => self::answer($current),
            'SetAutoresponder' => self::set($key, $current, (array) ($p['autoresponder'] ?? [])),
            default => throw new AgentCallException('unimplemented', ''),
        };
    }

    /**
     * @param  array<string, mixed>  $current
     * @param  array<string, mixed>  $a
     * @return array<string, mixed>
     */
    private static function set(string $key, array $current, array $a): array
    {
        $enabled = (bool) ($a['enabled'] ?? false);
        $next = [
            'enabled' => $enabled,
            'subject' => (string) ($a['subject'] ?? ''),
            'text' => (string) ($a['text'] ?? ''),
            'startsAt' => (string) ($a['startsAt'] ?? ''),
            'endsAt' => (string) ($a['endsAt'] ?? ''),
            // As on a real mail server, the autoresponder stops the filter rules of the
            // mailbox, and they do not start again when the autoresponder is off.
            'filterScript' => ! $enabled && ! ($current['enabled'] ?? false) ? ($current['filterScript'] ?? '') : '',
        ];
        Cache::forever($key, $next);

        return self::answer($next);
    }

    /**
     * @param  array<string, mixed>  $a
     * @return array<string, mixed>
     */
    private static function answer(array $a): array
    {
        return array_filter($a, fn ($v) => $v !== '' && $v !== false && $v !== null);
    }
}
