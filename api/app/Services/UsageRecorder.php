<?php

namespace App\Services;

use App\Events\UsageAlertRaised;
use App\Events\UsageAlertResolved;
use App\Models\DatabaseServer;
use App\Models\Mailbox;
use App\Models\Server;
use App\Models\Setting;
use App\Models\UsageAlert;
use App\Models\Website;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Usage history and usage alerts.
 *
 * Each report of an agent adds the use of each website, of the server and of each database
 * server to a bucket of 5 minutes and to a bucket of 1 hour (usage_samples). The recorder then
 * compares the use with the limits, and opens, changes or closes the usage alerts.
 *
 * An alert opens at the warning percent of the limit, and goes to "critical" at the critical
 * percent. It closes when the use goes 5 points below the warning percent, so that a value
 * near the threshold does not open and close an alert at each report. CPU and memory use the
 * average of the last 30 minutes, so that a short peak does not open an alert.
 *
 * FR-603: the mail report of an agent gives the size of each mailbox (checkMailboxes). A
 * mailbox alert opens at 90% of the size of the mailbox and becomes critical at 98%.
 */
class UsageRecorder
{
    /** The lengths of the buckets, in minutes. */
    public const FINE = 5;

    public const COARSE = 60;

    /** The time that the panel keeps the buckets of each length, in days. */
    public const KEEP_DAYS = [self::FINE => 8, self::COARSE => 400];

    /** The default thresholds, as percents of the limit. */
    public const DEFAULT_SETTINGS = ['warningPercent' => 80, 'criticalPercent' => 95];

    /** The distance below the warning percent at which an alert closes. */
    public const HYSTERESIS = 5;

    /** CPU and memory alerts use the average of this time, with this number of reports or more. */
    public const AVERAGE_MINUTES = 30;

    public const AVERAGE_SAMPLES = 5;

    /** @return array{warningPercent: int, criticalPercent: int} */
    public static function settings(): array
    {
        $s = Setting::get('usage_alerts', []);

        return [
            'warningPercent' => (int) ($s['warningPercent'] ?? self::DEFAULT_SETTINGS['warningPercent']),
            'criticalPercent' => (int) ($s['criticalPercent'] ?? self::DEFAULT_SETTINGS['criticalPercent']),
        ];
    }

    /** The start of the bucket of a time. */
    public static function bucket(Carbon $at, int $minutes): Carbon
    {
        $at = $at->copy()->utc()->startOfMinute();

        return $at->setTime($at->hour, intdiv($at->minute, $minutes) * $minutes % 60);
    }

    /**
     * Records a report of an agent. $data is the validated report. The websites in the report
     * are only the websites of the server.
     *
     * @param  array<string, mixed>  $data
     */
    public function record(Server $server, array $data, ?Carbon $now = null): void
    {
        $now ??= Carbon::now();
        $rows = [];
        $websites = Website::where('server_id', $server->id)->whereIn('id', collect($data['websites'] ?? [])->pluck('id'))->get()->keyBy('id');
        $month = $now->copy()->utc()->format('Y-m');
        foreach ($data['websites'] ?? [] as $use) {
            $website = $websites->get($use['id']);
            if (! $website) {
                continue;
            }
            $traffic = max(0, (int) ($use['bytesIn'] ?? 0)) + max(0, (int) ($use['bytesOut'] ?? 0));
            $rows[] = self::row(UsageAlert::WEBSITE, $website->id, [
                'cpu' => (float) ($use['cpuPercent'] ?? 0),
                'memory' => (float) ($use['memoryBytes'] ?? 0),
                'disk' => (float) ($use['diskBytes'] ?? 0),
                'processes' => (int) ($use['processes'] ?? 0),
                'in' => max(0, (int) ($use['bytesIn'] ?? 0)),
                'out' => max(0, (int) ($use['bytesOut'] ?? 0)),
            ]);
            // The traffic of the calendar month, for the bandwidth limit.
            if ($website->bandwidth_month !== $month) {
                $website->forceFill(['bandwidth_month' => $month, 'bandwidth_bytes' => $traffic])->saveQuietly();
            } elseif ($traffic > 0) {
                Website::whereKey($website->id)->increment('bandwidth_bytes', $traffic);
                $website->bandwidth_bytes += $traffic;
            }
        }
        if (is_array($data['usage'] ?? null)) {
            $u = $data['usage'];
            $rows[] = self::row(UsageAlert::SERVER, $server->id, [
                'cpu' => (float) ($u['cpuPercent'] ?? 0),
                'memory' => (float) ($u['memoryPercent'] ?? 0),
                'disk' => (float) ($u['diskPercent'] ?? 0),
                'processes' => 0,
                'in' => max(0, (int) ($u['networkInBytes'] ?? 0)),
                'out' => max(0, (int) ($u['networkOutBytes'] ?? 0)),
            ]);
        }
        $databaseServers = $server->databaseServers()->whereIn('id', collect($data['databaseServers'] ?? [])->pluck('id')->map(fn ($id) => strtolower($id)))->get()->keyBy('id');
        foreach ($data['databaseServers'] ?? [] as $use) {
            if ($databaseServers->has(strtolower($use['id']))) {
                $rows[] = self::row(UsageAlert::DATABASE_SERVER, strtolower($use['id']), [
                    'cpu' => 0,
                    'memory' => (float) ($use['memoryBytes'] ?? 0),
                    'disk' => (float) ($use['diskBytes'] ?? 0),
                    'processes' => 0, 'in' => 0, 'out' => 0,
                ]);
            }
        }
        $this->add($rows, $now);
        $this->checkLimits($server, $websites->all(), $databaseServers->all(), $data, $now);
    }

    /** @param array{cpu: float, memory: float, disk: float, processes: int, in: int, out: int} $v */
    private static function row(string $type, string $id, array $v): array
    {
        return [
            'subject_type' => $type, 'subject_id' => $id, 'samples' => 1,
            'cpu_sum' => $v['cpu'], 'cpu_max' => $v['cpu'], 'memory_sum' => $v['memory'], 'memory_max' => $v['memory'],
            'disk' => $v['disk'], 'processes_max' => $v['processes'], 'bytes_in' => $v['in'], 'bytes_out' => $v['out'],
        ];
    }

    /**
     * Adds the rows to their buckets of each length, in few statements. A bucket that exists
     * gets the sums, the largest values and the newest disk use.
     *
     * @param  list<array<string, mixed>>  $rows
     */
    private function add(array $rows, Carbon $now): void
    {
        if ($rows === []) {
            return;
        }
        $all = [];
        foreach ([self::FINE, self::COARSE] as $minutes) {
            $at = self::bucket($now, $minutes)->format('Y-m-d H:i:s');
            foreach ($rows as $r) {
                $all[] = $r + ['resolution' => $minutes, 'bucket_at' => $at];
            }
        }
        $driver = DB::connection()->getDriverName();
        $greatest = $driver === 'sqlite' ? 'MAX' : 'GREATEST';
        $columns = ['subject_type', 'subject_id', 'resolution', 'bucket_at', 'samples', 'cpu_sum', 'cpu_max', 'memory_sum', 'memory_max', 'disk', 'processes_max', 'bytes_in', 'bytes_out'];
        foreach (array_chunk($all, 200) as $chunk) {
            $values = implode(',', array_fill(0, count($chunk), '('.implode(',', array_fill(0, count($columns), '?')).')'));
            $bindings = [];
            foreach ($chunk as $r) {
                foreach ($columns as $c) {
                    $bindings[] = $r[$c];
                }
            }
            $insert = 'INSERT INTO usage_samples ('.implode(',', $columns).') VALUES '.$values;
            if ($driver === 'mysql' || $driver === 'mariadb') {
                $new = fn (string $c) => "VALUES({$c})";
                $sql = $insert.' ON DUPLICATE KEY UPDATE ';
            } else {
                $new = fn (string $c) => "excluded.{$c}";
                $sql = $insert.' ON CONFLICT (subject_type, subject_id, resolution, bucket_at) DO UPDATE SET ';
            }
            $own = fn (string $c) => ($driver === 'mysql' || $driver === 'mariadb') ? $c : "usage_samples.{$c}";
            $sql .= implode(', ', [
                "samples = {$own('samples')} + {$new('samples')}",
                "cpu_sum = {$own('cpu_sum')} + {$new('cpu_sum')}",
                "cpu_max = {$greatest}({$own('cpu_max')}, {$new('cpu_max')})",
                "memory_sum = {$own('memory_sum')} + {$new('memory_sum')}",
                "memory_max = {$greatest}({$own('memory_max')}, {$new('memory_max')})",
                "disk = {$new('disk')}",
                "processes_max = {$greatest}({$own('processes_max')}, {$new('processes_max')})",
                "bytes_in = {$own('bytes_in')} + {$new('bytes_in')}",
                "bytes_out = {$own('bytes_out')} + {$new('bytes_out')}",
            ]);
            DB::statement($sql, $bindings);
        }
    }

    /**
     * The average CPU and memory of each subject in the last AVERAGE_MINUTES, from the buckets
     * of 5 minutes. A subject with fewer than AVERAGE_SAMPLES reports has no average.
     *
     * @param  list<string>  $ids
     * @return array<string, array{cpu: float, memory: float}>
     */
    private static function averages(string $type, array $ids, Carbon $now): array
    {
        if ($ids === []) {
            return [];
        }
        $from = self::bucket($now->copy()->subMinutes(self::AVERAGE_MINUTES), self::FINE)->format('Y-m-d H:i:s');
        $out = [];
        foreach (DB::table('usage_samples')->where(['subject_type' => $type, 'resolution' => self::FINE])->whereIn('subject_id', $ids)->where('bucket_at', '>=', $from)
            ->groupBy('subject_id')->selectRaw('subject_id, SUM(samples) AS n, SUM(cpu_sum) AS cpu, SUM(memory_sum) AS memory')->get() as $r) {
            if ((int) $r->n >= self::AVERAGE_SAMPLES) {
                $out[$r->subject_id] = ['cpu' => $r->cpu / $r->n, 'memory' => $r->memory / $r->n];
            }
        }

        return $out;
    }

    /**
     * @param  array<string, Website>  $websites
     * @param  array<string, DatabaseServer>  $databaseServers
     * @param  array<string, mixed>  $data
     */
    private function checkLimits(Server $server, array $websites, array $databaseServers, array $data, Carbon $now): void
    {
        // Each check: [type, id, customer, resource, used, limit]. A limit of 0 is no limit.
        $checks = [];
        $average = self::averages(UsageAlert::WEBSITE, array_keys($websites), $now);
        $reported = collect($data['websites'] ?? [])->keyBy('id');
        foreach ($websites as $id => $w) {
            $l = $w->limits ?? [];
            $use = $reported->get($id, []);
            $c = $w->customer_id;
            $checks[] = [UsageAlert::WEBSITE, $id, $c, 'disk', (float) ($use['diskBytes'] ?? 0), (float) ($l['diskBytes'] ?? 0)];
            $checks[] = [UsageAlert::WEBSITE, $id, $c, 'bandwidth', (float) $w->bandwidth_bytes, (float) ($l['bandwidthBytes'] ?? 0)];
            $checks[] = [UsageAlert::WEBSITE, $id, $c, 'processes', (float) ($use['processes'] ?? 0), (float) ($l['processes'] ?? 0)];
            if (isset($average[$id])) {
                $checks[] = [UsageAlert::WEBSITE, $id, $c, 'cpu', $average[$id]['cpu'], (float) ($l['vcpus'] ?? 0) * 100];
                $checks[] = [UsageAlert::WEBSITE, $id, $c, 'memory', $average[$id]['memory'], (float) ($l['memoryBytes'] ?? 0)];
            }
        }
        $ids = [];
        if (is_array($data['usage'] ?? null)) {
            $ids[UsageAlert::SERVER] = [$server->id];
            $average = self::averages(UsageAlert::SERVER, [$server->id], $now);
            $checks[] = [UsageAlert::SERVER, $server->id, null, 'disk', (float) ($data['usage']['diskPercent'] ?? 0), 100.0];
            if (isset($average[$server->id])) {
                $checks[] = [UsageAlert::SERVER, $server->id, null, 'cpu', $average[$server->id]['cpu'], 100.0];
                $checks[] = [UsageAlert::SERVER, $server->id, null, 'memory', $average[$server->id]['memory'], 100.0];
            }
        }
        $reported = collect($data['databaseServers'] ?? [])->keyBy(fn ($u) => strtolower($u['id']));
        foreach ($databaseServers as $id => $d) {
            $use = $reported->get($id, []);
            $size = (float) ($use['diskSizeBytes'] ?? 0) ?: (float) ($d->limits['diskBytes'] ?? 0);
            $checks[] = [UsageAlert::DATABASE_SERVER, $id, $d->customer_id, 'disk', (float) ($use['diskBytes'] ?? 0), $size];
            $checks[] = [UsageAlert::DATABASE_SERVER, $id, $d->customer_id, 'memory', (float) ($use['memoryBytes'] ?? 0), (float) ($d->limits['memoryBytes'] ?? 0)];
        }
        $ids[UsageAlert::WEBSITE] = array_keys($websites);
        $ids[UsageAlert::DATABASE_SERVER] = array_keys($databaseServers);

        $open = UsageAlert::whereNull('resolved_at')->where(function ($q) use ($ids) {
            foreach ($ids as $type => $list) {
                $q->orWhere(fn ($q) => $q->where('subject_type', $type)->whereIn('subject_id', $list));
            }
        })->get()->keyBy(fn (UsageAlert $a) => "{$a->subject_type}|{$a->subject_id}|{$a->resource}");

        $s = self::settings();
        $seen = [];
        foreach ($checks as [$type, $id, $customer, $resource, $used, $limit]) {
            $key = "{$type}|{$id}|{$resource}";
            if ($limit <= 0) {
                continue;
            }
            $seen[$key] = true;
            $percent = round($used / $limit * 100, 1);
            $alert = $open->get($key);
            if (! $alert) {
                if ($percent >= $s['warningPercent']) {
                    $alert = UsageAlert::create([
                        'subject_type' => $type, 'subject_id' => $id, 'customer_id' => $customer, 'server_id' => $server->id,
                        'resource' => $resource, 'level' => $percent >= $s['criticalPercent'] ? UsageAlert::CRITICAL : UsageAlert::WARNING,
                        'used' => $used, 'limit' => $limit, 'percent' => $percent, 'opened_at' => $now,
                    ]);
                    $this->raised($alert, true);
                }

                continue;
            }
            if ($percent < $s['warningPercent'] - self::HYSTERESIS) {
                $this->resolve($alert, $now);

                continue;
            }
            $level = match (true) {
                $percent >= $s['criticalPercent'] => UsageAlert::CRITICAL,
                // A critical alert stays critical near the critical threshold.
                $alert->level === UsageAlert::CRITICAL && $percent >= $s['criticalPercent'] - self::HYSTERESIS => UsageAlert::CRITICAL,
                default => UsageAlert::WARNING,
            };
            $escalated = $level === UsageAlert::CRITICAL && $alert->level !== UsageAlert::CRITICAL;
            $alert->update(['used' => $used, 'limit' => $limit, 'percent' => $percent, 'level' => $level, 'server_id' => $server->id]);
            if ($escalated) {
                $this->raised($alert, false);
            }
        }
        // An alert of a resource that has no limit now. CPU and memory with no average keep
        // their alerts until the next average.
        foreach ($open as $key => $alert) {
            if (! isset($seen[$key]) && ! in_array($alert->resource, ['cpu', 'memory'], true)) {
                $this->resolve($alert, $now);
            }
        }
    }

    /**
     * FR-603: opens, changes or closes the alert of each mailbox of the server after a mail
     * report. A mailbox alert opens at Mailbox::FULL_WARNING_PERCENT of the size of the
     * mailbox, and becomes critical at Mailbox::FULL_CRITICAL_PERCENT. It closes 5 points
     * below the warning percent, when the mailbox gets a larger size, or when the mailbox
     * is removed.
     *
     * @param  iterable<Mailbox>  $mailboxes  all mailboxes of the mail domains on the server
     */
    public function checkMailboxes(Server $server, iterable $mailboxes, ?Carbon $now = null): void
    {
        $now ??= Carbon::now();
        $open = UsageAlert::whereNull('resolved_at')->where(['subject_type' => UsageAlert::MAILBOX, 'server_id' => $server->id])
            ->get()->keyBy('subject_id');
        $seen = [];
        foreach ($mailboxes as $mailbox) {
            if ($mailbox->quota_bytes <= 0) {
                continue;
            }
            $seen[$mailbox->id] = true;
            $used = (float) $mailbox->used_bytes;
            $limit = (float) $mailbox->quota_bytes;
            $percent = round($used / $limit * 100, 1);
            $critical = $percent >= Mailbox::FULL_CRITICAL_PERCENT;
            $alert = $open->get($mailbox->id);
            if (! $alert) {
                if ($percent >= Mailbox::FULL_WARNING_PERCENT) {
                    $alert = UsageAlert::create([
                        'subject_type' => UsageAlert::MAILBOX, 'subject_id' => $mailbox->id, 'customer_id' => $mailbox->customer_id, 'server_id' => $server->id,
                        'resource' => 'storage', 'level' => $critical ? UsageAlert::CRITICAL : UsageAlert::WARNING,
                        'used' => $used, 'limit' => $limit, 'percent' => $percent, 'opened_at' => $now,
                    ]);
                    $this->raised($alert, true);
                }

                continue;
            }
            if ($percent < Mailbox::FULL_WARNING_PERCENT - self::HYSTERESIS) {
                $this->resolve($alert, $now);

                continue;
            }
            $level = match (true) {
                $critical => UsageAlert::CRITICAL,
                $alert->level === UsageAlert::CRITICAL && $percent >= Mailbox::FULL_CRITICAL_PERCENT - self::HYSTERESIS => UsageAlert::CRITICAL,
                default => UsageAlert::WARNING,
            };
            $escalated = $level === UsageAlert::CRITICAL && $alert->level !== UsageAlert::CRITICAL;
            $alert->update(['used' => $used, 'limit' => $limit, 'percent' => $percent, 'level' => $level]);
            if ($escalated) {
                $this->raised($alert, false);
            }
        }
        // The alert of a mailbox that was removed, or that moved to a different server.
        foreach ($open as $id => $alert) {
            if (! isset($seen[$id])) {
                $this->resolve($alert, $now);
            }
        }
    }

    private function resolve(UsageAlert $alert, Carbon $now): void
    {
        $alert->update(['resolved_at' => $now]);
        UsageAlertResolved::dispatch($alert);
    }

    /** The delivery hooks of a new alert, or of an alert that went to "critical". */
    private function raised(UsageAlert $alert, bool $opened): void
    {
        UsageAlertRaised::dispatch($alert, $opened);
        $notifier = 'App\\Services\\Notifications\\Notifier';
        if ($alert->customer_id === null || ! class_exists($notifier)) {
            return;
        }
        try {
            $customer = $alert->customer;
            if ($customer) {
                app($notifier)->usageAlert($customer, self::describe($alert), "usage:{$alert->id}:{$alert->level}");
            }
        } catch (Throwable $e) {
            // A failed email does not stop the report of the agent.
            Log::warning('usage alert: the notification failed', ['alert' => $alert->id, 'error' => $e->getMessage()]);

            return;
        }
        $alert->update(['notified_level' => $alert->level]);
    }

    /**
     * The values of an alert for people, for example for an email. "subject" names the
     * website, the database server or the mailbox, for example "mailbox info@example.com".
     *
     * @return array{resource: string, website: string, subject: string, used: string, limit: string, percent: string, level: string}
     */
    public static function describe(UsageAlert $alert): array
    {
        $resource = UsageAlert::RESOURCE_NAMES[$alert->resource] ?? $alert->resource;
        $website = $subject = '';
        if ($alert->subject_type === UsageAlert::WEBSITE) {
            $website = (string) Website::whereKey($alert->subject_id)->value('name');
            $subject = "website {$website}";
        } elseif ($alert->subject_type === UsageAlert::DATABASE_SERVER) {
            $resource = 'Database server '.strtolower($resource);
            $subject = 'database server '.DatabaseServer::whereKey($alert->subject_id)->value('name');
        } elseif ($alert->subject_type === UsageAlert::MAILBOX) {
            $subject = 'mailbox '.(Mailbox::with('domain')->find($alert->subject_id)?->address() ?? 'that was removed');
        }

        return [
            'resource' => $resource,
            'website' => $website,
            'subject' => $subject,
            'used' => self::amount($alert, $alert->used),
            'limit' => self::amount($alert, $alert->limit),
            'percent' => (string) round($alert->percent),
            'level' => $alert->level,
        ];
    }

    /** A value of an alert in the units of its resource, for example "9.1 GB" or "1.5 vCPUs". */
    public static function amount(UsageAlert $alert, float $value): string
    {
        if ($alert->subject_type === UsageAlert::SERVER) {
            return round($value, 1).'%';
        }

        return match ($alert->resource) {
            'cpu' => round($value / 100, 2).' vCPUs',
            'processes' => (string) (int) $value,
            default => self::bytes($value),
        };
    }

    public static function bytes(float $bytes): string
    {
        $units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        $i = 0;
        while ($bytes >= 1024 && $i < count($units) - 1) {
            $bytes /= 1024;
            $i++;
        }

        return ($i === 0 ? (int) $bytes : round($bytes, 1)).' '.$units[$i];
    }
}
