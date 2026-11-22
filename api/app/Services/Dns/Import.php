<?php

namespace App\Services\Dns;

use App\Models\AgentJob;
use App\Models\DnsRecord;
use App\Models\DnsZone;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;
use Throwable;

/**
 * Adds the records of a zone file to a zone. The panel checks each record as it checks a record
 * that a user adds (Records::check), so the result is the same as when the user adds the records
 * one at a time. A preview (dry run) does the same checks in a transaction that the panel rolls
 * back.
 *
 * The import only adds records. It does not change or remove a record of the zone.
 */
class Import
{
    public function __construct(private Zones $zones) {}

    /**
     * @return array{added: list<array<string, mixed>>, existing: int, skipped: list<array{line: int, text: string, code: string, reason: string}>, jobs: list<AgentJob>}
     */
    public function run(DnsZone $zone, string $text, bool $dryRun): array
    {
        $parsed = ZoneFile::parse($zone->name, $text);
        DB::beginTransaction();
        try {
            // The lock stops a change of the records at the same time, as for one new record.
            DnsZone::whereKey($zone->id)->lockForUpdate()->first();
            $result = self::apply($zone, $parsed['records']);
            if ($dryRun || $result['added'] === []) {
                DB::rollBack();
            } else {
                DB::commit();
            }
        } catch (Throwable $e) {
            DB::rollBack();
            throw $e;
        }
        $skipped = collect([...$parsed['skipped'], ...$result['skipped']])->sortBy('line')->values()->all();
        $count = count($result['added']);
        $jobs = ! $dryRun && $count > 0
            ? $this->zones->changed($zone, "Import {$count} DNS ".($count === 1 ? 'record' : 'records')." to {$zone->name}")
            : [];

        return ['added' => $result['added'], 'existing' => $result['existing'], 'skipped' => $skipped, 'jobs' => $jobs];
    }

    /**
     * Adds the records to the zone, one at a time, in the transaction of the caller. A record that
     * the zone has already counts as "existing". The A, AAAA and CNAME records of a name that the
     * panel made for a website stay: the website keeps its address.
     *
     * @param  list<array{line: int, text: string, name: string, type: string, ttl: int, value: string, priority: ?int, note: ?string}>  $records
     * @return array{added: list<array<string, mixed>>, existing: int, skipped: list<array{line: int, text: string, code: string, reason: string}>}
     */
    public static function apply(DnsZone $zone, array $records): array
    {
        $added = [];
        $existing = 0;
        $skipped = [];
        $websites = $zone->records()->where('managed', DnsRecord::WEBSITE)->pluck('name')->flip();
        foreach ($records as $r) {
            if (in_array($r['type'], ['A', 'AAAA', 'CNAME'], true) && $websites->has($r['name'])) {
                $skipped[] = ['line' => $r['line'], 'text' => $r['text'], 'code' => ZoneFile::WEBSITE,
                    'reason' => 'The panel made the address records of this name for a website on this panel. The website keeps them.'];

                continue;
            }
            // A record that the zone has already, for example after a second import of the
            // same file. The record is in the form of the panel (ZoneFile::parse).
            if ($zone->records()->where(['name' => $r['name'], 'type' => $r['type'], 'value' => $r['value']])->where('priority', $r['priority'])->exists()) {
                $existing++;

                continue;
            }
            try {
                $record = $zone->records()->create(Records::check($zone, $r));
            } catch (ValidationException $e) {
                $skipped[] = ['line' => $r['line'], 'text' => $r['text'], 'code' => ZoneFile::INVALID, 'reason' => collect($e->errors())->flatten()->first() ?? 'The record is not valid.'];

                continue;
            }
            $added[] = [
                'line' => $r['line'],
                'name' => $record->name,
                'type' => $record->type,
                'value' => $record->value,
                'ttl' => $record->ttl,
                'priority' => $record->priority,
                'note' => $r['note'],
            ];
        }

        return ['added' => $added, 'existing' => $existing, 'skipped' => $skipped];
    }
}
