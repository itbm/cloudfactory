<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Domain;
use App\Models\UptimeCheck;
use App\Models\UptimeIncident;
use App\Models\Website;
use App\Services\Audit;
use App\Services\Uptime\Monitor;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Collection as EloquentCollection;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Http\Response;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\Rule;
use Illuminate\Validation\ValidationException;

/**
 * The uptime checks of websites (App\Services\Uptime\Monitor). The panel requests an address
 * of a domain of the website on a schedule, and sends an email when the website is down and
 * when it is up again. Each user sees the checks of the websites that they can see.
 */
class UptimeController extends Controller
{
    use Concerns;

    /** A path and a query: the characters of RFC 3986, with no space and no fragment. */
    public const PATH = '#^/[A-Za-z0-9\-._~!$&\'()*+,;=:@%/?]*$#';

    /** The relations that the answer of a check needs. */
    private const WITH = ['domain', 'website.customer.reseller', 'openIncident'];

    public function __construct(private Monitor $monitor) {}

    /**
     * All checks that the user can see, the checks that are down first. A filter by state
     * ("up", "down", "pending" or "paused") or by website is optional.
     */
    public function all(Request $request): JsonResponse
    {
        $data = $request->validate([
            'state' => ['sometimes', Rule::in([UptimeCheck::UP, UptimeCheck::DOWN, UptimeCheck::PENDING, UptimeCheck::PAUSED])],
            'websiteId' => ['sometimes', 'string', 'max:26'],
        ]);
        $state = $data['state'] ?? null;
        $page = $this->visibleChecks($request)
            ->with(self::WITH)
            ->when($data['websiteId'] ?? null, fn (Builder $q, string $id) => $q->where('website_id', strtolower($id)))
            ->when($state === UptimeCheck::PAUSED, fn (Builder $q) => $q->whereNot(fn (Builder $q) => Monitor::active($q)))
            ->when($state !== null && $state !== UptimeCheck::PAUSED, fn (Builder $q) => Monitor::active($q)->where('state', $state))
            ->orderByRaw("CASE WHEN state = 'down' THEN 0 WHEN state = 'pending' THEN 1 ELSE 2 END")
            ->orderBy(Website::select('name')->whereColumn('websites.id', 'uptime_checks.website_id'))
            ->orderBy('id')
            ->paginate(self::perPage($request, 50));
        $checks = new EloquentCollection($page->items());
        $uptime = Monitor::uptime($checks);

        return response()->json([
            'data' => $checks->map(fn (UptimeCheck $c) => self::present($c, $uptime[$c->id]))->all(),
            'meta' => ['current_page' => $page->currentPage(), 'last_page' => $page->lastPage(), 'per_page' => $page->perPage(), 'total' => $page->total()],
            'summary' => $this->summary($request),
        ]);
    }

    /** The number of checks in each state, of all checks that the user can see. */
    private function summary(Request $request): array
    {
        $active = Monitor::active($this->visibleChecks($request))->select('state', DB::raw('count(*) as n'))->groupBy('state')->pluck('n', 'state');
        $total = $this->visibleChecks($request)->count();
        $out = ['total' => $total];
        foreach ([UptimeCheck::UP, UptimeCheck::DOWN, UptimeCheck::PENDING] as $s) {
            $out[$s] = (int) ($active[$s] ?? 0);
        }
        $out[UptimeCheck::PAUSED] = $total - $out[UptimeCheck::UP] - $out[UptimeCheck::DOWN] - $out[UptimeCheck::PENDING];

        return $out;
    }

    /** The incidents of the checks that the user can see, the newest first. */
    public function incidents(Request $request): JsonResponse
    {
        $data = $request->validate([
            'state' => ['sometimes', Rule::in(['open', 'resolved', 'all'])],
            'websiteId' => ['sometimes', 'string', 'max:26'],
        ]);
        $ids = $request->user()->visibleCustomerIds();
        $state = $data['state'] ?? 'all';
        $incidents = UptimeIncident::query()
            ->with(['check.domain', 'check.website'])
            ->whereHas('check.website', fn (Builder $q) => $q->when($ids !== null, fn (Builder $q) => $q->whereIn('customer_id', $ids))
                ->when($data['websiteId'] ?? null, fn (Builder $q, string $id) => $q->where('id', strtolower($id))))
            ->when($state === 'open', fn (Builder $q) => $q->whereNull('resolved_at'))
            ->when($state === 'resolved', fn (Builder $q) => $q->whereNotNull('resolved_at'))
            ->orderByRaw('CASE WHEN resolved_at IS NULL THEN 0 ELSE 1 END')
            ->orderByDesc('started_at')
            ->limit(200)
            ->get();

        return response()->json(['data' => $incidents->map(fn (UptimeIncident $i) => self::presentIncident($i))->all()]);
    }

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        $checks = $website->uptimeChecks()->with(self::WITH)->get();
        $uptime = Monitor::uptime($checks);

        return response()->json(['data' => $checks->map(fn (UptimeCheck $c) => self::present($c, $uptime[$c->id]))->all()]);
    }

    /** One check with its results of the last 24 hours, its last 30 days and its last incidents. */
    public function show(Request $request, string $websiteId, string $checkId): JsonResponse
    {
        $check = $this->findCheck($request, $websiteId, $checkId);

        return response()->json([
            ...self::present($check, Monitor::uptime(new EloquentCollection([$check]))[$check->id]),
            'history' => Monitor::history($check),
            'days' => Monitor::days($check),
            'incidents' => $check->incidents()->orderByDesc('started_at')->limit(20)->get()->map(fn (UptimeIncident $i) => Monitor::incident($i))->all(),
        ]);
    }

    public function store(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        abort_if(! $website->isPublic(), 409, 'A private website and a worker have no domain, so they have no uptime check.');
        $data = $request->validate(self::rules(create: true), self::messages());
        if ($website->uptimeChecks()->count() >= Monitor::MAX_PER_WEBSITE) {
            throw ValidationException::withMessages(['domain' => 'A website can have '.Monitor::MAX_PER_WEBSITE.' uptime checks. Remove one first.']);
        }
        $values = [
            'domain_id' => self::domainOf($website, $data['domain'])->id,
            'scheme' => $data['scheme'] ?? 'https',
            'path' => $data['path'] ?? '/',
            'interval_seconds' => $data['intervalSeconds'] ?? Monitor::DEFAULT_INTERVAL,
            'timeout_seconds' => $data['timeoutSeconds'] ?? Monitor::DEFAULT_TIMEOUT,
            'expected_status' => $data['expectedStatus'] ?? null,
            'keyword' => self::keyword($data['keyword'] ?? null),
            'failures_before_alert' => $data['failuresBeforeAlert'] ?? Monitor::DEFAULT_FAILURES,
            'enabled' => $data['enabled'] ?? true,
        ];
        self::refuseDuplicate($website, $values);
        $check = $website->uptimeChecks()->create($values + ['state' => UptimeCheck::PENDING, 'state_changed_at' => now()]);
        $check->load(self::WITH);
        Audit::record($request, 'Added uptime check', "{$website->name}: {$check->url()}", $website->customer_id);

        return response()->json(['uptimeCheck' => self::present($check, Monitor::uptime(new EloquentCollection([$check]))[$check->id])], 201);
    }

    /**
     * A change of the address or of the expected answer starts the check again with no
     * state: an open incident ends with no email. The next request is within 1 minute.
     */
    public function update(Request $request, string $websiteId, string $checkId): JsonResponse
    {
        $check = $this->findCheck($request, $websiteId, $checkId);
        $website = $check->website;
        $data = $request->validate(self::rules(create: false), self::messages());
        $values = array_filter([
            'domain_id' => isset($data['domain']) ? self::domainOf($website, $data['domain'])->id : null,
            'scheme' => $data['scheme'] ?? null,
            'path' => $data['path'] ?? null,
            'interval_seconds' => $data['intervalSeconds'] ?? null,
            'timeout_seconds' => $data['timeoutSeconds'] ?? null,
            'failures_before_alert' => $data['failuresBeforeAlert'] ?? null,
            'enabled' => $data['enabled'] ?? null,
        ], fn ($v) => $v !== null);
        if (array_key_exists('expectedStatus', $data)) {
            $values['expected_status'] = $data['expectedStatus'];
        }
        if (array_key_exists('keyword', $data)) {
            $values['keyword'] = self::keyword($data['keyword']);
        }
        self::refuseDuplicate($website, $values + $check->only(['domain_id', 'scheme', 'path']), $check->id);
        $check->fill($values);
        $restart = $check->isDirty(['domain_id', 'scheme', 'path', 'expected_status', 'keyword']) || ($check->isDirty('enabled') && ! $check->enabled);
        $check->next_check_at = null;
        $check->save();
        if ($restart && $check->state !== UptimeCheck::PENDING) {
            $this->monitor->pause($check);
        }
        $check = $check->fresh(self::WITH);
        Audit::record($request, 'Changed uptime check', "{$website->name}: {$check->url()}", $website->customer_id);

        return response()->json(['uptimeCheck' => self::present($check, Monitor::uptime(new EloquentCollection([$check]))[$check->id])]);
    }

    public function destroy(Request $request, string $websiteId, string $checkId): Response
    {
        $check = $this->findCheck($request, $websiteId, $checkId);
        $check->delete();
        Audit::record($request, 'Removed uptime check', "{$check->website->name}: {$check->url()}", $check->website->customer_id);

        return response()->noContent();
    }

    /** Requests the address now and records the result, as the schedule does. */
    public function run(Request $request, string $websiteId, string $checkId): JsonResponse
    {
        $check = $this->findCheck($request, $websiteId, $checkId);
        if ($reason = $check->pausedReason()) {
            abort(409, $reason.' The panel does not run the check now.');
        }
        $result = $this->monitor->run(new EloquentCollection([$check]), guard: false)[$check->id] ?? null;
        abort_if($result === null, 409, 'The panel does not run the check now.');
        $check = $check->fresh(self::WITH);

        return response()->json([
            'uptimeCheck' => self::present($check, Monitor::uptime(new EloquentCollection([$check]))[$check->id]),
            'result' => ['ok' => $result->ok, 'status' => $result->status, 'responseMs' => $result->responseMs, 'error' => $result->error],
        ]);
    }

    private function visibleChecks(Request $request): Builder
    {
        $ids = $request->user()->visibleCustomerIds();

        return UptimeCheck::query()->when($ids !== null, fn (Builder $q) => $q->whereHas('website', fn (Builder $q) => $q->whereIn('customer_id', $ids)));
    }

    private function findCheck(Request $request, string $websiteId, string $checkId): UptimeCheck
    {
        $website = $this->findWebsite($request, $websiteId);

        return $website->uptimeChecks()->with(self::WITH)->findOrFail(strtolower($checkId));
    }

    /** @return array<string, list<mixed>> */
    private static function rules(bool $create): array
    {
        return [
            'domain' => [$create ? 'required' : 'sometimes', 'string', 'max:253'],
            'scheme' => ['sometimes', Rule::in(['https', 'http'])],
            'path' => ['sometimes', 'string', 'max:255', 'regex:'.self::PATH],
            'intervalSeconds' => ['sometimes', 'integer', Rule::in(Monitor::INTERVALS)],
            'timeoutSeconds' => ['sometimes', 'integer', 'min:'.Monitor::MIN_TIMEOUT, 'max:'.Monitor::MAX_TIMEOUT],
            'expectedStatus' => ['sometimes', 'nullable', 'integer', 'min:100', 'max:599'],
            // One line, with no control characters.
            'keyword' => ['sometimes', 'nullable', 'string', 'max:100', 'regex:/^[^\x00-\x1F\x7F]*$/u'],
            'failuresBeforeAlert' => ['sometimes', 'integer', 'min:1', 'max:'.Monitor::MAX_FAILURES],
            'enabled' => ['sometimes', 'boolean'],
        ];
    }

    /** @return array<string, string> */
    private static function messages(): array
    {
        return [
            'path.regex' => 'Write a path that starts with "/", for example "/health". Do not use spaces or "#".',
            'intervalSeconds.in' => 'Use one of these intervals in seconds: '.implode(', ', Monitor::INTERVALS).'.',
            'keyword.regex' => 'Write the text on one line.',
        ];
    }

    private static function domainOf(Website $website, string $name): Domain
    {
        $domain = $website->domains()->where('name', strtolower(trim($name)))->first();
        if (! $domain) {
            throw ValidationException::withMessages(['domain' => "The website has no domain \"{$name}\". Use a domain of the website."]);
        }

        return $domain;
    }

    private static function keyword(?string $keyword): ?string
    {
        $keyword = $keyword === null ? null : trim($keyword);

        return $keyword === '' ? null : $keyword;
    }

    /** @param array<string, mixed> $values */
    private static function refuseDuplicate(Website $website, array $values, ?string $except = null): void
    {
        $exists = $website->uptimeChecks()
            ->where(['domain_id' => $values['domain_id'], 'scheme' => $values['scheme'], 'path' => $values['path']])
            ->when($except, fn ($q) => $q->where('id', '!=', $except))
            ->exists();
        if ($exists) {
            throw ValidationException::withMessages(['path' => 'The website has a check of this address already.']);
        }
    }

    /**
     * @param  array{day: ?float, week: ?float, month: ?float}  $uptime
     * @return array<string, mixed>
     */
    public static function present(UptimeCheck $c, array $uptime): array
    {
        $paused = $c->pausedReason();
        $incident = $paused === null ? $c->openIncident : null;

        return [
            'id' => $c->id,
            'websiteId' => $c->website_id,
            'websiteName' => $c->website?->name,
            'domain' => $c->domain?->name,
            'scheme' => $c->scheme,
            'path' => $c->path,
            'url' => $c->url(),
            'intervalSeconds' => $c->interval_seconds,
            'timeoutSeconds' => $c->timeout_seconds,
            // Null: each status from 200 to 399.
            'expectedStatus' => $c->expected_status,
            'keyword' => $c->keyword,
            'failuresBeforeAlert' => $c->failures_before_alert,
            'enabled' => $c->enabled,
            // "pending" (no result yet), "up", "down" or "paused".
            'state' => $c->shownState(),
            'pausedReason' => $paused,
            'since' => $paused === null ? $c->state_changed_at?->toIso8601String() : null,
            // The failed requests in a row.
            'failures' => $c->failures,
            'lastCheckedAt' => $c->last_checked_at?->toIso8601String(),
            'lastResult' => $c->last_checked_at === null ? null : [
                'ok' => $c->last_error === null,
                'status' => $c->last_status,
                'responseMs' => $c->last_response_ms,
                'error' => $c->last_error,
            ],
            'nextCheckAt' => $paused === null ? ($c->next_check_at ?? now())->toIso8601String() : null,
            'uptime' => $uptime,
            'incident' => $incident ? Monitor::incident($incident) : null,
            'createdAt' => $c->created_at?->toIso8601String(),
        ];
    }

    /** @return array<string, mixed> */
    private static function presentIncident(UptimeIncident $i): array
    {
        return [
            ...Monitor::incident($i),
            'checkId' => $i->uptime_check_id,
            'websiteId' => $i->check?->website_id,
            'websiteName' => $i->check?->website?->name,
            'url' => $i->check?->url(),
        ];
    }
}
