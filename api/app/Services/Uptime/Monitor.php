<?php

namespace App\Services\Uptime;

use App\Enums\WebsiteStatus;
use App\Jobs\RunUptimeChecks;
use App\Models\UptimeCheck;
use App\Models\UptimeIncident;
use App\Models\UptimeResult;
use App\Services\Notifications\Notifier;
use App\Services\Plugins\Events;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Collection as EloquentCollection;
use Illuminate\Support\Carbon;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;

/**
 * Uptime checks of websites. Each minute, the panel sends the checks that are due to the queue
 * (RunUptimeChecks). The queue requests the addresses (Probe) and records the results.
 *
 * - A check is "down" after a number of failed requests in a row (2 by default). After a
 *   failed request, the next request comes after 1 minute, so that the panel finds out soon.
 * - The incident starts at the first failed request, and ends at the first request that
 *   succeeds again. The users get an email (uptime.down and uptime.up) and the plugins get a
 *   webhook.
 * - The panel runs a check only while its website runs, and while its customer is not
 *   suspended. When a check stops while it is down, its incident ends with no email.
 * - When all requests of a batch get no answer, and the batch has the websites of 3 or more
 *   servers, the cause is almost certainly the network of the panel. The panel then records
 *   nothing and tries again after 1 minute.
 */
class Monitor
{
    /** The checks of one website. */
    public const MAX_PER_WEBSITE = 5;

    /** The intervals that a user can select, in seconds. */
    public const INTERVALS = [60, 120, 300, 600, 900, 1800, 3600];

    public const DEFAULT_INTERVAL = 300;

    public const DEFAULT_TIMEOUT = 10;

    public const MIN_TIMEOUT = 2;

    public const MAX_TIMEOUT = 30;

    public const DEFAULT_FAILURES = 2;

    public const MAX_FAILURES = 5;

    /** After a failed request, the next request comes after this time, in seconds. */
    public const RETRY_SECONDS = 60;

    /** The checks of one queue job. */
    public const BATCH = 25;

    /** The checks that one run of the schedule sends to the queue, at most. */
    public const MAX_PER_RUN = 5000;

    /**
     * A check that the schedule sent to the queue is not due again for this time, in seconds.
     * The result of the check sets the real time of the next request.
     */
    public const LEASE_SECONDS = 600;

    public const KEEP_RESULT_DAYS = 8;

    public const KEEP_INCIDENT_DAYS = 90;

    /** The size of a batch with no answers that the panel takes as a problem of its own network. */
    public const GUARD_CHECKS = 5;

    public const GUARD_SERVERS = 3;

    public function __construct(private Probe $probe, private Notifier $notifier) {}

    /**
     * The schedule: stops the checks that must not run now, and sends the checks that are due
     * to the queue.
     *
     * @return int the number of checks in the queue
     */
    public function schedule(): int
    {
        $now = now();
        $this->pauseStopped();
        $ids = self::active(UptimeCheck::query())
            ->where(fn (Builder $q) => $q->whereNull('next_check_at')->orWhere('next_check_at', '<=', $now))
            ->orderBy('next_check_at')
            ->limit(self::MAX_PER_RUN)
            ->pluck('id');
        foreach ($ids->chunk(500) as $chunk) {
            UptimeCheck::whereIn('id', $chunk)->update(['next_check_at' => $now->copy()->addSeconds(self::LEASE_SECONDS)]);
        }
        foreach ($ids->chunk(self::BATCH) as $chunk) {
            RunUptimeChecks::dispatch($chunk->values()->all());
        }

        return $ids->count();
    }

    /** The checks that the panel runs: on, of a website that runs, of a customer that is not suspended. */
    public static function active(Builder $query): Builder
    {
        return $query->where('enabled', true)->whereHas('website', fn (Builder $q) => $q
            ->where('status', WebsiteStatus::Running->value)
            ->whereHas('customer', fn (Builder $q) => $q->where('suspended', false)
                ->where(fn (Builder $q) => $q->whereNull('reseller_id')->orWhereHas('reseller', fn (Builder $q) => $q->where('suspended', false)))));
    }

    /** Resets each check that has a state but does not run now. */
    private function pauseStopped(): void
    {
        UptimeCheck::where('state', '!=', UptimeCheck::PENDING)
            ->where(fn (Builder $q) => $q->where('enabled', false)
                ->orWhereDoesntHave('website', fn (Builder $q) => $q->where('status', WebsiteStatus::Running->value))
                ->orWhereHas('website.customer', fn (Builder $q) => $q->where('suspended', true))
                ->orWhereHas('website.customer.reseller', fn (Builder $q) => $q->where('suspended', true)))
            ->with('website.customer.reseller')
            ->get()
            ->each(fn (UptimeCheck $check) => $check->pausedReason() !== null && $this->pause($check));
    }

    /**
     * The check stops: an open incident ends, with no email, and the check starts again with
     * no state when it runs again.
     */
    public function pause(UptimeCheck $check): bool
    {
        DB::transaction(function () use ($check) {
            $check->openIncident?->update(['resolved_at' => now(), 'ended_by_pause' => true]);
            $check->update(['state' => UptimeCheck::PENDING, 'failures' => 0, 'failing_since' => null, 'state_changed_at' => now(), 'next_check_at' => null]);
        });

        return true;
    }

    /**
     * Requests the addresses of the checks now and records the results. A check that must
     * not run now is left out.
     *
     * @param  EloquentCollection<int, UptimeCheck>  $checks
     * @return array<string, ProbeResult> by the ID of the check
     */
    public function run(EloquentCollection $checks, bool $guard = true): array
    {
        $checks = $checks->load(['domain', 'website.customer.reseller'])
            ->filter(fn (UptimeCheck $c) => $c->domain !== null && $c->pausedReason() === null)->values();
        if ($checks->isEmpty()) {
            return [];
        }
        $results = $this->probe->run($checks);
        $at = now();
        if ($guard && self::networkProblem($checks, $results)) {
            Log::warning("Uptime: no answer from {$checks->count()} websites on {$checks->pluck('website.server_id')->unique()->count()} servers. The network of the panel can have a problem. The panel records no result and tries again in 1 minute.");
            UptimeCheck::whereIn('id', $checks->pluck('id'))->update(['next_check_at' => $at->copy()->startOfMinute()->addSeconds(self::RETRY_SECONDS)]);

            return [];
        }
        foreach ($checks as $check) {
            if (isset($results[$check->id])) {
                $this->record($check, $results[$check->id], $at);
            }
        }

        return $results;
    }

    /**
     * @param  Collection<int, UptimeCheck>  $checks
     * @param  array<string, ProbeResult>  $results
     */
    private static function networkProblem(Collection $checks, array $results): bool
    {
        return count($results) >= self::GUARD_CHECKS
            && collect($results)->every(fn (ProbeResult $r) => $r->networkError)
            && $checks->pluck('website.server_id')->unique()->count() >= self::GUARD_SERVERS;
    }

    /** Records one result, and changes the state of the check and its incident. */
    public function record(UptimeCheck $check, ProbeResult $result, Carbon $at): void
    {
        $change = DB::transaction(function () use ($check, $result, $at) {
            $locked = UptimeCheck::lockForUpdate()->find($check->id);
            if (! $locked) {
                return null;
            }
            UptimeResult::create([
                'uptime_check_id' => $locked->id, 'checked_at' => $at, 'ok' => $result->ok,
                'status' => $result->status, 'response_ms' => $result->responseMs, 'error' => $result->ok ? null : $result->error,
            ]);
            $values = ['last_checked_at' => $at, 'last_status' => $result->status, 'last_response_ms' => $result->responseMs, 'last_error' => $result->ok ? null : $result->error];
            $next = $at->copy()->startOfMinute();
            $change = null;
            if ($result->ok) {
                $values += ['failures' => 0, 'failing_since' => null, 'next_check_at' => $next->addSeconds($locked->interval_seconds)];
                if ($locked->state !== UptimeCheck::UP) {
                    $values += ['state' => UptimeCheck::UP, 'state_changed_at' => $at];
                    $incident = $locked->openIncident;
                    if ($incident) {
                        $incident->update(['resolved_at' => $at]);
                        $change = [UptimeCheck::UP, $incident];
                    }
                }
            } else {
                $since = $locked->failing_since ?? $at;
                $values += ['failures' => $locked->failures + 1, 'failing_since' => $since];
                $down = $locked->state === UptimeCheck::DOWN;
                if (! $down && $locked->failures + 1 >= $locked->failures_before_alert) {
                    $down = true;
                    $values += ['state' => UptimeCheck::DOWN, 'state_changed_at' => $since];
                    $incident = $locked->incidents()->create(['started_at' => $since, 'error' => $result->error ?? 'The request failed.']);
                    $change = [UptimeCheck::DOWN, $incident];
                }
                $values['next_check_at'] = $next->addSeconds($down ? $locked->interval_seconds : min(self::RETRY_SECONDS, $locked->interval_seconds));
            }
            $locked->update($values);

            return $change;
        });
        if ($change) {
            $this->announce($check->fresh(['domain', 'website.customer']), ...$change);
        }
    }

    /** The email to the users and the webhook to the plugins. A notification never stops the check. */
    private function announce(UptimeCheck $check, string $state, UptimeIncident $incident): void
    {
        $incident->refresh();
        if ($state === UptimeCheck::DOWN) {
            $this->notifier->uptimeDown($check, $incident);
        } else {
            $this->notifier->uptimeUp($check, $incident, self::duration($incident->durationSeconds()));
        }
        Events::emit($state === UptimeCheck::DOWN ? Events::UPTIME_DOWN : Events::UPTIME_UP, [
            'check' => ['id' => $check->id, 'url' => $check->url()],
            'website' => ['id' => $check->website_id, 'name' => $check->website?->name, 'customerId' => $check->website?->customer_id],
            'incident' => self::incident($incident),
        ]);
    }

    /** @return array<string, mixed> */
    public static function incident(UptimeIncident $i): array
    {
        return [
            'id' => $i->id,
            'startedAt' => $i->started_at->toIso8601String(),
            'resolvedAt' => $i->resolved_at?->toIso8601String(),
            'durationSeconds' => $i->durationSeconds(),
            'error' => $i->error,
            // True when the check stopped while it was down: the panel does not know when the website answered again.
            'endedByPause' => $i->ended_by_pause,
        ];
    }

    /**
     * The percent of the time that each check was up in the last 24 hours, 7 days and 30 days.
     * The time of an incident is down. The time before the check was made does not count.
     * Null when the check has no result yet.
     *
     * @param  Collection<int, UptimeCheck>  $checks
     * @return array<string, array{day: ?float, week: ?float, month: ?float}>
     */
    public static function uptime(Collection $checks): array
    {
        $now = now();
        $incidents = UptimeIncident::whereIn('uptime_check_id', $checks->pluck('id'))
            ->where(fn (Builder $q) => $q->whereNull('resolved_at')->orWhere('resolved_at', '>', $now->copy()->subDays(30)))
            ->get(['uptime_check_id', 'started_at', 'resolved_at'])
            ->groupBy('uptime_check_id');
        $out = [];
        foreach ($checks as $check) {
            $mine = $incidents->get($check->id, collect());
            $out[$check->id] = [
                'day' => self::percentUp($check, $mine, $now->copy()->subDay(), $now),
                'week' => self::percentUp($check, $mine, $now->copy()->subDays(7), $now),
                'month' => self::percentUp($check, $mine, $now->copy()->subDays(30), $now),
            ];
        }

        return $out;
    }

    /** @param Collection<int, UptimeIncident> $incidents */
    public static function percentUp(UptimeCheck $check, Collection $incidents, Carbon $from, Carbon $to): ?float
    {
        if ($check->last_checked_at === null) {
            return null;
        }
        $start = $from->max($check->created_at ?? $from);
        $total = $to->getTimestamp() - $start->getTimestamp();
        if ($total < 60) {
            return null;
        }
        $down = 0;
        foreach ($incidents as $i) {
            $a = max($i->started_at->getTimestamp(), $start->getTimestamp());
            $b = min(($i->resolved_at ?? $to)->getTimestamp(), $to->getTimestamp());
            $down += max(0, $b - $a);
        }
        if ($down <= 0) {
            return 100.0;
        }

        // 99.999 is "99.99", not "100": a short incident must not look like no incident.
        return max(0.0, floor((1 - $down / $total) * 10000) / 100);
    }

    /**
     * The results of the last 24 hours in parts of 30 minutes: the number of requests and of
     * failed requests, and the average and the largest response time.
     *
     * @return list<array{at: string, checks: int, failures: int, responseMs: ?int, maxResponseMs: ?int}>
     */
    public static function history(UptimeCheck $check): array
    {
        $now = now()->utc();
        $step = 30 * 60;
        $from = intdiv($now->copy()->subDay()->getTimestamp(), $step) * $step + $step;
        $points = [];
        for ($t = $from; $t <= $now->getTimestamp(); $t += $step) {
            $points[$t] = ['checks' => 0, 'failures' => 0, 'sum' => 0, 'timed' => 0, 'max' => null];
        }
        $rows = UptimeResult::where('uptime_check_id', $check->id)
            ->where('checked_at', '>=', Carbon::createFromTimestamp($from, 'UTC'))
            ->get(['checked_at', 'ok', 'response_ms']);
        foreach ($rows as $r) {
            $key = intdiv($r->checked_at->getTimestamp(), $step) * $step;
            if (! isset($points[$key])) {
                continue;
            }
            $p = &$points[$key];
            $p['checks']++;
            $p['failures'] += $r->ok ? 0 : 1;
            if ($r->response_ms !== null) {
                $p['sum'] += $r->response_ms;
                $p['timed']++;
                $p['max'] = max($p['max'] ?? 0, $r->response_ms);
            }
            unset($p);
        }
        $out = [];
        foreach ($points as $t => $p) {
            $out[] = [
                'at' => Carbon::createFromTimestamp($t, 'UTC')->toIso8601ZuluString(),
                'checks' => $p['checks'],
                'failures' => $p['failures'],
                'responseMs' => $p['timed'] > 0 ? (int) round($p['sum'] / $p['timed']) : null,
                'maxResponseMs' => $p['max'],
            ];
        }

        return $out;
    }

    /**
     * The percent of each of the last 30 days (UTC) that the check was up. Null for a day
     * before the check was made.
     *
     * @return list<array{date: string, uptime: ?float, downSeconds: int}>
     */
    public static function days(UptimeCheck $check): array
    {
        $now = now()->utc();
        $incidents = $check->incidents()
            ->where(fn (Builder $q) => $q->whereNull('resolved_at')->orWhere('resolved_at', '>', $now->copy()->subDays(31)))
            ->get(['started_at', 'resolved_at']);
        $out = [];
        for ($d = 29; $d >= 0; $d--) {
            $day = $now->copy()->subDays($d)->startOfDay();
            $end = $day->copy()->addDay()->min($now);
            $before = $check->created_at !== null && $check->created_at->gte($end);
            $start = $day->copy()->max($check->created_at ?? $day);
            $down = 0;
            foreach ($incidents as $i) {
                $down += max(0, min(($i->resolved_at ?? $now)->getTimestamp(), $end->getTimestamp()) - max($i->started_at->getTimestamp(), $start->getTimestamp()));
            }
            $total = $end->getTimestamp() - $start->getTimestamp();
            $out[] = [
                'date' => $day->toDateString(),
                'uptime' => $before || $total <= 0 || $check->last_checked_at === null ? null : ($down <= 0 ? 100.0 : max(0.0, floor((1 - $down / $total) * 10000) / 100)),
                'downSeconds' => $down,
            ];
        }

        return $out;
    }

    /** A length of time for people, for example "1 hour 5 minutes". */
    public static function duration(int $seconds): string
    {
        if ($seconds < 60) {
            return 'less than 1 minute';
        }
        $minutes = intdiv($seconds, 60);
        $parts = [];
        foreach ([['day', 1440], ['hour', 60], ['minute', 1]] as [$unit, $size]) {
            $n = intdiv($minutes, $size);
            $minutes -= $n * $size;
            if ($n > 0) {
                $parts[] = $n.' '.$unit.($n === 1 ? '' : 's');
            }
        }

        return implode(' ', array_slice($parts, 0, 2));
    }

    /** Removes the old results and the old closed incidents. */
    public static function prune(): array
    {
        $results = UptimeResult::where('checked_at', '<', now()->subDays(self::KEEP_RESULT_DAYS))->delete();
        $incidents = UptimeIncident::whereNotNull('resolved_at')->where('resolved_at', '<', now()->subDays(self::KEEP_INCIDENT_DAYS))->delete();

        return [$results, $incidents];
    }
}
