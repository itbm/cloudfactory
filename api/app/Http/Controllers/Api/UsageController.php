<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\DatabaseServer;
use App\Models\Mailbox;
use App\Models\Server;
use App\Models\Setting;
use App\Models\UsageAlert;
use App\Models\Website;
use App\Services\Audit;
use App\Services\UsageRecorder;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;

/**
 * The usage history of websites, servers and database servers, for the graphs of the panel,
 * and the usage alerts.
 */
class UsageController extends Controller
{
    use Concerns;

    /**
     * Each range: the length in hours, the length of the buckets that the panel reads, and the
     * length of each point of the answer, in minutes.
     */
    public const RANGES = [
        '24h' => [24, UsageRecorder::FINE, 5],
        '7d' => [168, UsageRecorder::COARSE, 60],
        '30d' => [720, UsageRecorder::COARSE, 180],
        '90d' => [2160, UsageRecorder::COARSE, 720],
        '1y' => [8760, UsageRecorder::COARSE, 2880],
    ];

    public function website(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $month = Carbon::now()->utc()->format('Y-m');

        return response()->json([
            ...$this->history($request, UsageAlert::WEBSITE, $website->id),
            'limits' => $website->limits,
            'bandwidth' => [
                'month' => $month,
                'bytes' => $website->bandwidth_month === $month ? (int) $website->bandwidth_bytes : 0,
                'limitBytes' => (int) ($website->limits['bandwidthBytes'] ?? 0),
            ],
            'alerts' => self::alertsOf(UsageAlert::WEBSITE, $website->id),
        ]);
    }

    public function server(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);

        return response()->json([
            ...$this->history($request, UsageAlert::SERVER, $server->id),
            'alerts' => self::alertsOf(UsageAlert::SERVER, $server->id),
        ]);
    }

    public function databaseServer(Request $request, string $id): JsonResponse
    {
        $ids = $request->user()->visibleCustomerIds();
        $databaseServer = DatabaseServer::query()->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))->findOrFail(strtolower($id));

        return response()->json([
            ...$this->history($request, UsageAlert::DATABASE_SERVER, $databaseServer->id),
            'limits' => $databaseServer->limits,
            'alerts' => self::alertsOf(UsageAlert::DATABASE_SERVER, $databaseServer->id),
        ]);
    }

    /**
     * The points of a range. A point with no report has null values, so that a graph shows a
     * gap. CPU is the average and the largest percent of one CPU (of the server for a server).
     * Memory is the average and the largest value. Disk is the last value. The traffic is the
     * sum of the point.
     *
     * @return array<string, mixed>
     */
    private function history(Request $request, string $type, string $id): array
    {
        $data = $request->validate(['range' => ['sometimes', Rule::in(array_keys(self::RANGES))]]);
        $range = $data['range'] ?? '24h';
        [$hours, $resolution, $step] = self::RANGES[$range];
        $now = Carbon::now()->utc();
        $last = UsageRecorder::bucket($now, $step >= 60 ? 60 : $step);
        // The points start at a multiple of the step from the start of the day.
        $from = $now->copy()->subHours($hours);
        $from = $from->copy()->startOfDay()->addMinutes(intdiv($from->hour * 60 + $from->minute, $step) * $step);
        $rows = DB::table('usage_samples')->where(['subject_type' => $type, 'subject_id' => $id, 'resolution' => $resolution])
            ->where('bucket_at', '>=', $from->format('Y-m-d H:i:s'))->orderBy('bucket_at')->get();

        $points = [];
        for ($at = $from->copy(); $at <= $last; $at->addMinutes($step)) {
            $points[$at->getTimestamp()] = ['n' => 0, 'cpu' => 0.0, 'cpuMax' => 0.0, 'mem' => 0.0, 'memMax' => 0.0, 'disk' => null, 'proc' => 0, 'in' => 0, 'out' => 0];
        }
        $start = $from->getTimestamp();
        foreach ($rows as $r) {
            $t = Carbon::parse($r->bucket_at, 'UTC')->getTimestamp();
            $key = $start + intdiv($t - $start, $step * 60) * $step * 60;
            if (! isset($points[$key])) {
                continue;
            }
            $p = &$points[$key];
            $p['n'] += (int) $r->samples;
            $p['cpu'] += (float) $r->cpu_sum;
            $p['cpuMax'] = max($p['cpuMax'], (float) $r->cpu_max);
            $p['mem'] += (float) $r->memory_sum;
            $p['memMax'] = max($p['memMax'], (float) $r->memory_max);
            $p['disk'] = (float) $r->disk;
            $p['proc'] = max($p['proc'], (int) $r->processes_max);
            $p['in'] += (int) $r->bytes_in;
            $p['out'] += (int) $r->bytes_out;
            unset($p);
        }

        $server = $type === UsageAlert::SERVER;
        $out = [];
        foreach ($points as $t => $p) {
            $has = $p['n'] > 0;
            $point = [
                'at' => Carbon::createFromTimestamp($t, 'UTC')->toIso8601ZuluString(),
                'cpuPercent' => $has ? round($p['cpu'] / $p['n'], 1) : null,
                'cpuMaxPercent' => $has ? round($p['cpuMax'], 1) : null,
            ];
            if ($server) {
                $point += [
                    'memoryPercent' => $has ? round($p['mem'] / $p['n'], 1) : null,
                    'memoryMaxPercent' => $has ? round($p['memMax'], 1) : null,
                    'diskPercent' => $has ? round($p['disk'], 1) : null,
                ];
            } else {
                $point += [
                    'memoryBytes' => $has ? (int) round($p['mem'] / $p['n']) : null,
                    'memoryMaxBytes' => $has ? (int) $p['memMax'] : null,
                    'diskBytes' => $has ? (int) $p['disk'] : null,
                ];
                if ($type === UsageAlert::WEBSITE) {
                    $point['processes'] = $has ? $p['proc'] : null;
                }
            }
            if ($type !== UsageAlert::DATABASE_SERVER) {
                $point += ['bytesIn' => $has ? $p['in'] : null, 'bytesOut' => $has ? $p['out'] : null];
            }
            $out[] = $point;
        }

        return ['range' => $range, 'stepMinutes' => $step, 'from' => $from->toIso8601ZuluString(), 'to' => $now->toIso8601ZuluString(), 'points' => $out];
    }

    /** @return list<array<string, mixed>> */
    private static function alertsOf(string $type, string $id): array
    {
        return UsageAlert::where(['subject_type' => $type, 'subject_id' => $id])->whereNull('resolved_at')
            ->orderByDesc('opened_at')->get()->map(fn (UsageAlert $a) => self::present($a))->all();
    }

    /**
     * The usage alerts that the user can see. An administrator sees all alerts, also the alerts
     * of the servers. A reseller sees the alerts of their customers, and an owner the alerts
     * of their websites and database servers.
     */
    public function alerts(Request $request): JsonResponse
    {
        $data = $request->validate([
            'state' => ['sometimes', Rule::in(['open', 'resolved', 'all'])],
            'subjectType' => ['sometimes', Rule::in([UsageAlert::WEBSITE, UsageAlert::SERVER, UsageAlert::DATABASE_SERVER, UsageAlert::MAILBOX])],
            'subjectId' => ['sometimes', 'string', 'max:26'],
        ]);
        $ids = $request->user()->visibleCustomerIds();
        $state = $data['state'] ?? 'open';
        $alerts = UsageAlert::query()
            ->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))
            ->when($state === 'open', fn (Builder $q) => $q->whereNull('resolved_at'))
            ->when($state === 'resolved', fn (Builder $q) => $q->whereNotNull('resolved_at'))
            ->when($data['subjectType'] ?? null, fn (Builder $q, $t) => $q->where('subject_type', $t))
            ->when($data['subjectId'] ?? null, fn (Builder $q, $id) => $q->where('subject_id', $id))
            // The critical alerts first, then the newest.
            ->orderByRaw('CASE WHEN resolved_at IS NULL THEN 0 ELSE 1 END')
            ->orderByRaw("CASE WHEN level = 'critical' THEN 0 ELSE 1 END")
            ->orderByDesc('opened_at')
            ->limit(200)->get();

        $names = self::subjectNames($alerts->all());

        return response()->json([
            'data' => $alerts->map(fn (UsageAlert $a) => self::present($a, $names))->all(),
            'settings' => UsageRecorder::settings(),
        ]);
    }

    /** An administrator sets the thresholds of the alerts, as percents of the limits. */
    public function updateSettings(Request $request): JsonResponse
    {
        $data = $request->validate([
            'warningPercent' => ['required', 'integer', 'between:1,99'],
            'criticalPercent' => ['required', 'integer', 'between:2,100', 'gt:warningPercent'],
        ]);
        Setting::put('usage_alerts', $data);
        Audit::record($request, 'Changed usage alert thresholds', "{$data['warningPercent']}% and {$data['criticalPercent']}%");

        return response()->json(UsageRecorder::settings());
    }

    /**
     * The names of the subjects of the alerts, in few queries.
     *
     * @param  list<UsageAlert>  $alerts
     * @return array<string, string>
     */
    private static function subjectNames(array $alerts): array
    {
        $by = [];
        foreach ($alerts as $a) {
            $by[$a->subject_type][] = $a->subject_id;
        }
        $names = [];
        foreach ([UsageAlert::WEBSITE => Website::class, UsageAlert::SERVER => Server::class, UsageAlert::DATABASE_SERVER => DatabaseServer::class] as $type => $model) {
            if (! empty($by[$type])) {
                foreach ($model::whereIn('id', array_unique($by[$type]))->pluck('name', 'id') as $id => $name) {
                    $names["{$type}|{$id}"] = $name;
                }
            }
        }
        // The name of a mailbox is its address.
        if (! empty($by[UsageAlert::MAILBOX])) {
            foreach (Mailbox::with('domain')->whereIn('id', array_unique($by[UsageAlert::MAILBOX]))->get() as $mailbox) {
                $names[UsageAlert::MAILBOX.'|'.$mailbox->id] = $mailbox->address();
            }
        }

        return $names;
    }

    /** @param array<string, string>|null $names */
    public static function present(UsageAlert $a, ?array $names = null): array
    {
        $names ??= self::subjectNames([$a]);

        return [
            'id' => $a->id,
            'subjectType' => $a->subject_type,
            'subjectId' => $a->subject_id,
            // Null when the website, the server, the database server or the mailbox was removed.
            'subjectName' => $names["{$a->subject_type}|{$a->subject_id}"] ?? null,
            'customerId' => $a->customer_id,
            'serverId' => $a->server_id,
            'resource' => $a->resource,
            'level' => $a->level,
            'used' => $a->used,
            'limit' => $a->limit,
            'percent' => $a->percent,
            'usedText' => UsageRecorder::amount($a, $a->used),
            'limitText' => UsageRecorder::amount($a, $a->limit),
            'openedAt' => $a->opened_at?->toIso8601String(),
            'updatedAt' => $a->updated_at?->toIso8601String(),
            'resolvedAt' => $a->resolved_at?->toIso8601String(),
        ];
    }
}
