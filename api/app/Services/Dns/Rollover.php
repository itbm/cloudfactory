<?php

namespace App\Services\Dns;

use App\Models\AgentJob;
use App\Models\DnsZone;
use Illuminate\Validation\ValidationException;

/**
 * A change of the DNSSEC key of a zone on the DNS servers of the panel (a key rollover with
 * double signatures, RFC 6781 and RFC 7583). The panel changes the key only when a user asks,
 * because the registrar of the domain must get the new DS record.
 *
 * | Step      | Keys in the zone                                     | The step ends                                         |
 * | --------- | ---------------------------------------------------- | ----------------------------------------------------- |
 * | "publish" | The old key signs the zone. The new key signs only the DNSKEY records. | Each DNS server has the new key, and the old DNSKEY records are out of the caches. |
 * | "submit"  | As "publish", and CDS and CDNSKEY records for the new key. The user gives the new DS record to the registrar. | The public DNS gives the new DS record. |
 * | "retire"  | Both keys sign the zone.                             | The old DS record and the records with only the old signatures are out of the caches. |
 *
 * Then the old key goes. A resolver that has the old DS record or the new one can always
 * check the zone. The command cloudfactory:check-dns (each 15 minutes) and "Check now" move a
 * change to the next step.
 */
class Rollover
{
    public const PUBLISH = 'publish';

    public const SUBMIT = 'submit';

    public const RETIRE = 'retire';

    /** The TTL of the DNSKEY records (dnskey-ttl in the configuration of Knot DNS), in seconds. */
    public const DNSKEY_TTL = 3600;

    /** The time in which a secondary DNS server gets a change with no NOTIFY: the refresh time of the SOA record. */
    public const REFRESH = 3600;

    /** More time for slow servers and clocks, in seconds. */
    public const MARGIN = 3600;

    /** The panel waits at least this time for the old DS record to go out of the caches, in seconds. */
    public const DS_TTL = 86400;

    /** The TTL of the records that the panel makes: SOA, NS and DNSKEY. */
    private const MIN_TTL = 3600;

    public function __construct(private Zones $zones, private Lookup $lookup) {}

    /**
     * The keys of a zone for the agent: one key, or two keys during a change. With "secret",
     * each key has its private key. The panel adds the private keys only when it sends a job.
     *
     * @return list<array{key: string, keyTag: int, state: string}>
     */
    public static function keys(DnsZone $zone, bool $secret): array
    {
        if (! $zone->dnssec || $zone->dns_account_id !== null || ! $zone->dnssec_key_tag) {
            return [];
        }
        $key = fn (string $which, string $state) => [
            'key' => $secret ? (string) $zone->getAttribute($which === 'next' ? 'dnssec_next_key' : 'dnssec_key') : '',
            'keyTag' => (int) $zone->getAttribute($which === 'next' ? 'dnssec_next_key_tag' : 'dnssec_key_tag'),
            'state' => $state,
        ];

        return match ($zone->dnssec_rollover) {
            self::PUBLISH => [$key('current', 'active'), $key('next', 'ready')],
            self::SUBMIT => [$key('current', 'active'), $key('next', 'submit')],
            self::RETIRE => [$key('next', 'active'), $key('current', 'active')],
            default => [$key('current', 'active')],
        };
    }

    /**
     * Starts a change of the key: the panel makes a new key, and the DNS servers publish it.
     *
     * @return list<AgentJob>
     *
     * @throws ValidationException
     */
    public function start(DnsZone $zone): array
    {
        $refuse = match (true) {
            ! $zone->dnssec => 'DNSSEC is off for this zone.',
            $zone->dns_account_id !== null => 'The DNS provider of the zone makes its DNSSEC key.',
            $zone->dnssec_rollover !== null => 'The key of this zone changes already.',
            default => null,
        };
        if ($refuse) {
            throw ValidationException::withMessages(['rollover' => $refuse]);
        }
        // The two keys must have different key tags, so that the DNS servers and the DS records
        // can tell them apart.
        do {
            $key = Dnssec::generate();
        } while ($key['keyTag'] === $zone->dnssec_key_tag);
        $zone->update([
            'dnssec_rollover' => self::PUBLISH, 'dnssec_next_key' => $key['key'], 'dnssec_next_public_key' => $key['publicKey'],
            'dnssec_next_key_tag' => $key['keyTag'], 'dnssec_rollover_at' => now(), 'dnssec_rollover_until' => null,
        ]);

        return $this->zones->changed($zone, "Publish a new DNSSEC key for {$zone->name}");
    }

    /**
     * Stops a change of the key before the registry has the new DS record. The new key goes.
     *
     * @return list<AgentJob>
     *
     * @throws ValidationException
     */
    public function cancel(DnsZone $zone): array
    {
        if (! in_array($zone->dnssec_rollover, [self::PUBLISH, self::SUBMIT], true)) {
            throw ValidationException::withMessages(['rollover' => $zone->dnssec_rollover === self::RETIRE
                ? 'The registry has the new DS record. The change of the key cannot stop now.'
                : 'The key of this zone does not change.']);
        }
        // When the registry has the new DS record already, the new key must stay.
        if ($zone->dnssec_rollover === self::SUBMIT && in_array('next', $this->checkParent($zone)['keys'] ?? [], true)) {
            throw ValidationException::withMessages(['rollover' => 'The registry has the new DS record already. The change of the key continues.']);
        }
        self::clear($zone);

        return $this->zones->changed($zone, "Stop the change of the DNSSEC key of {$zone->name}");
    }

    /** Removes a change of the key from a zone, for example when DNSSEC goes off. */
    public static function clear(DnsZone $zone): void
    {
        $zone->update([
            'dnssec_rollover' => null, 'dnssec_next_key' => null, 'dnssec_next_public_key' => null, 'dnssec_next_key_tag' => null,
            'dnssec_rollover_at' => null, 'dnssec_rollover_until' => null,
        ]);
    }

    /**
     * Asks the public DNS for the DS records of the zone at the registry, and moves a change of
     * the key to the next step when it can.
     *
     * @return list<AgentJob>
     */
    public function check(DnsZone $zone): array
    {
        if (! $zone->dnssec || $zone->dns_account_id !== null) {
            return [];
        }
        $parent = $this->checkParent($zone);
        $now = now();
        switch ($zone->dnssec_rollover) {
            case self::PUBLISH:
                // Each DNS server must have the new key. Then the old DNSKEY records must go
                // out of the caches, also on the secondary DNS servers.
                if (Zones::state($zone) !== 'active') {
                    return [];
                }
                if ($zone->dnssec_rollover_until === null) {
                    $zone->update(['dnssec_rollover_until' => $now->copy()->addSeconds(self::DNSKEY_TTL + self::REFRESH + self::MARGIN)]);

                    return [];
                }
                if ($now->lt($zone->dnssec_rollover_until)) {
                    return [];
                }
                $zone->update(['dnssec_rollover' => self::SUBMIT, 'dnssec_rollover_at' => $now, 'dnssec_rollover_until' => null]);

                return $this->zones->changed($zone, "Publish the CDS record of the new DNSSEC key of {$zone->name}");
            case self::SUBMIT:
                // The registry has the new DS record, or no DS record (then no resolver checks
                // the signatures of the zone).
                if (! in_array('next', $parent['keys'], true) && $parent['state'] !== 'none') {
                    return [];
                }
                $wait = max(self::DS_TTL, $parent['ttl'], self::maxTtl($zone)) + self::MARGIN;
                $zone->update(['dnssec_rollover' => self::RETIRE, 'dnssec_rollover_at' => $now, 'dnssec_rollover_until' => $now->copy()->addSeconds($wait)]);

                return $this->zones->changed($zone, "Sign {$zone->name} with the new DNSSEC key");
            case self::RETIRE:
                // Do not remove the old key while the registry has only its DS record.
                if ($now->lt($zone->dnssec_rollover_until) || $parent['state'] === 'unknown' || $parent['keys'] === ['current']) {
                    return [];
                }
                $zone->update(['dnssec_key' => $zone->dnssec_next_key, 'dnssec_public_key' => $zone->dnssec_next_public_key, 'dnssec_key_tag' => $zone->dnssec_next_key_tag]);
                self::clear($zone);
                $this->checkParent($zone);

                return $this->zones->changed($zone, "Remove the old DNSSEC key of {$zone->name}");
        }

        return [];
    }

    /**
     * Asks the public DNS for the DS records of the zone, and records the answer:
     *
     * - "ok": a DS record is for a key of the zone. "keys" tells which: "current", "next".
     * - "none": the registry has no DS record. Resolvers do not check the signatures.
     * - "other": no DS record is for a key of the zone. Resolvers that check DNSSEC refuse the
     *   answers for the domain.
     * - "unknown": the lookup failed.
     *
     * @return array{state: string, keys: list<string>, keyTags: list<int>, ttl: int, checkedAt: string}
     */
    public function checkParent(DnsZone $zone): array
    {
        $records = $this->lookup->ds($zone->name);
        $keys = [];
        foreach ($records ?? [] as $ds) {
            foreach (['current' => $zone->dnssec_public_key, 'next' => $zone->dnssec_next_public_key] as $which => $public) {
                if ($public && Dnssec::matches($zone->name, $public, $ds) && ! in_array($which, $keys, true)) {
                    $keys[] = $which;
                }
            }
        }
        sort($keys);
        $parent = [
            'state' => match (true) {
                $records === null => 'unknown',
                $records === [] => 'none',
                $keys === [] => 'other',
                default => 'ok',
            },
            'keys' => $keys,
            'keyTags' => array_values(array_unique(array_column($records ?? [], 'keyTag'))),
            'ttl' => (int) max([0, ...array_column($records ?? [], 'ttl')]),
            'checkedAt' => now()->toIso8601String(),
        ];
        $zone->update(['dnssec_parent' => $parent]);

        return $parent;
    }

    /** The largest TTL of a record of the zone, in seconds. */
    private static function maxTtl(DnsZone $zone): int
    {
        return max(self::MIN_TTL, (int) $zone->records()->max('ttl'));
    }

    /**
     * The change of the key and the DS records at the registry, as the API gives them.
     *
     * @return array{rollover: array<string, mixed>|null, parent: array<string, mixed>|null}
     */
    public static function present(DnsZone $zone): array
    {
        if (! $zone->dnssec || $zone->dns_account_id !== null) {
            return ['rollover' => null, 'parent' => null];
        }
        $p = $zone->dnssec_parent;
        $tag = fn (string $which) => $which === 'next' ? $zone->dnssec_next_key_tag : $zone->dnssec_key_tag;

        return [
            'rollover' => $zone->dnssec_rollover ? [
                'step' => $zone->dnssec_rollover,
                'startedAt' => $zone->dnssec_rollover_at?->toIso8601String(),
                // The time when the step can end. Null in the step "publish" until each DNS
                // server has the new key, and in the step "submit".
                'until' => $zone->dnssec_rollover_until?->toIso8601String(),
                // The DS record of the new key, which the registrar gets in the step "submit",
                // and the DS record of the old key, which the registrar removes.
                'newDs' => Dnssec::ds($zone->name, (string) $zone->dnssec_next_public_key),
                'oldDs' => Dnssec::ds($zone->name, (string) $zone->dnssec_public_key),
            ] : null,
            'parent' => $p ? [
                'state' => $p['state'] ?? 'unknown',
                // The key tags of the zone that the registry has a DS record for.
                'keyTags' => array_values(array_map($tag, $p['keys'] ?? [])),
                'otherKeyTags' => array_values(array_diff($p['keyTags'] ?? [], array_map($tag, $p['keys'] ?? []))),
                'checkedAt' => $p['checkedAt'] ?? null,
            ] : null,
        ];
    }
}
