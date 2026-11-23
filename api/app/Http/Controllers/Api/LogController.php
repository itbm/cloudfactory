<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Models\Server;
use App\Services\AgentCallException;
use App\Services\AgentClient;
use App\Validation\Input;
use App\Validation\InputRules;
use Closure;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Carbon;

/**
 * The logs of a website: the access log of the edge proxy, and the output and the errors of
 * the containers of the website. The agent of the server keeps the access log, and reads the
 * log of the containers as the Unix user of the website. One search covers all three sources,
 * from the newest entry to the oldest entry.
 *
 * An administrator can also search the access log of a server for the requests that are not
 * for a website: the panel, the webmail and the host name of the mail server.
 */
class LogController extends Controller
{
    use Concerns;

    /** The sources of the logs. */
    public const SOURCES = ['access', 'output', 'error'];

    public function __construct(private AgentClient $agent) {}

    public function index(Request $request, string $websiteId): JsonResponse
    {
        $website = $this->findWebsite($request, $websiteId);
        WebsiteController::refuseFailed($website);

        return response()->json($this->search($request, $website->server, $website->id, true));
    }

    public function server(Request $request, string $id): JsonResponse
    {
        $server = Server::findOrFail($id);

        return response()->json($this->search($request, $server, '', false));
    }

    /**
     * Checks the search of a request and sends it to the agent.
     *
     * @return array<string, mixed>
     */
    private function search(Request $request, ?Server $server, string $websiteId, bool $sources): array
    {
        abort_if($server === null, 409, 'This website has no server.');
        $data = $request->validate([
            'sources' => [$sources ? 'sometimes' : 'prohibited', 'string', 'max:40', self::list(self::SOURCES, 'Use access, output and error, with commas between them.')],
            'q' => ['sometimes', 'nullable', 'string', Input::rule('logSearch')],
            'status' => ['sometimes', 'string', 'max:100', self::list(null, InputRules::MESSAGES['httpStatus'], 'httpStatus')],
            'method' => ['sometimes', 'string', 'max:200', self::list(null, InputRules::MESSAGES['httpMethod'], 'httpMethod')],
            'client' => ['sometimes', 'string', 'max:64', self::client()],
            'from' => ['sometimes', 'date'],
            'to' => ['sometimes', 'date', 'after:from'],
            'cursor' => ['sometimes', 'string', Input::rule('logCursor')],
            'limit' => ['sometimes', 'integer', 'between:1,'.InputRules::MAX_LOG_ENTRIES],
            'summary' => ['sometimes', 'boolean'],
        ]);
        $split = fn (string $key) => isset($data[$key]) ? array_values(array_unique(array_map('trim', explode(',', $data[$key])))) : [];
        $time = fn (string $key) => isset($data[$key]) ? Carbon::parse($data[$key])->utc()->format('Y-m-d\TH:i:s.u\Z') : '';
        $call = array_filter([
            'websiteId' => $websiteId,
            'sources' => $split('sources'),
            'text' => trim((string) ($data['q'] ?? '')),
            'statuses' => $split('status'),
            'methods' => array_map('strtoupper', $split('method')),
            'client' => $data['client'] ?? '',
            'from' => $time('from'),
            'to' => $time('to'),
            'cursor' => $data['cursor'] ?? '',
            'limit' => (int) ($data['limit'] ?? 100),
            'summary' => (bool) ($data['summary'] ?? false),
        ], fn ($v) => $v !== '' && $v !== [] && $v !== false);
        try {
            $result = $this->agent->call($server, 'SearchLogs', $call);
        } catch (AgentCallException $e) {
            abort($e->status(), $e->userMessage());
        }

        return [
            'entries' => array_map(self::entry(...), $result['entries'] ?? []),
            'next' => ($result['next'] ?? '') !== '' ? $result['next'] : null,
            'summary' => ($call['summary'] ?? false) ? self::summary($result['summary'] ?? []) : null,
        ];
    }

    /**
     * An entry of the agent for the API. The agent leaves out each field with an empty value,
     * and gives each 64-bit number as text.
     *
     * @param  array<string, mixed>  $e
     * @return array<string, mixed>
     */
    private static function entry(array $e): array
    {
        $source = in_array($e['source'] ?? '', self::SOURCES, true) ? $e['source'] : 'output';
        $a = $e['access'] ?? null;

        return [
            'at' => $e['at'] ?? null,
            'source' => $source,
            'text' => (string) ($e['text'] ?? ''),
            'access' => $source === 'access' && is_array($a) ? [
                'client' => (string) ($a['client'] ?? ''),
                'method' => (string) ($a['method'] ?? ''),
                'host' => (string) ($a['host'] ?? ''),
                'uri' => (string) ($a['uri'] ?? ''),
                'proto' => (string) ($a['proto'] ?? ''),
                'status' => (int) ($a['status'] ?? 0),
                'sizeBytes' => (int) ($a['sizeBytes'] ?? 0),
                'durationMs' => round((float) ($a['durationSeconds'] ?? 0) * 1000, 1),
                'userAgent' => (string) ($a['userAgent'] ?? ''),
                'referer' => (string) ($a['referer'] ?? ''),
            ] : null,
        ];
    }

    /**
     * @param  array<string, mixed>  $s
     * @return array<string, mixed>
     */
    private static function summary(array $s): array
    {
        $statuses = [];
        foreach (['1xx', '2xx', '3xx', '4xx', '5xx'] as $class) {
            $statuses[$class] = (int) ($s['statuses'][$class] ?? 0);
        }
        $counts = fn (string $key) => array_map(fn (array $c) => ['value' => (string) ($c['value'] ?? ''), 'count' => (int) ($c['count'] ?? 0)], $s[$key] ?? []);
        $sources = ['access' => (int) ($s['access'] ?? 0), 'output' => (int) ($s['output'] ?? 0), 'error' => (int) ($s['error'] ?? 0)];

        return [
            'total' => array_sum($sources),
            'sources' => $sources,
            'statuses' => $statuses,
            'topPaths' => $counts('topPaths'),
            'topClients' => $counts('topClients'),
            'sizeBytes' => (int) ($s['sizeBytes'] ?? 0),
            'oldest' => ($s['oldest'] ?? '') !== '' ? $s['oldest'] : null,
        ];
    }

    /**
     * A rule for a list of values with commas between them. Each value is in $allowed, or
     * agrees with an input rule.
     *
     * @param  list<string>|null  $allowed
     */
    private static function list(?array $allowed, string $message, ?string $rule = null): Closure
    {
        return function (string $attribute, mixed $value, Closure $fail) use ($allowed, $message, $rule): void {
            $items = array_map('trim', explode(',', (string) $value));
            $max = $rule === null ? count(self::SOURCES) : InputRules::MAX_LOG_FILTERS;
            if (count($items) > $max) {
                $fail("Give at most {$max} values.");

                return;
            }
            foreach ($items as $item) {
                $ok = $rule === null ? in_array($item, $allowed ?? [], true) : Input::valid($rule, $rule === 'httpMethod' ? strtoupper($item) : $item);
                if (! $ok) {
                    $fail($message);

                    return;
                }
            }
        };
    }

    /** A rule for the address of a client or a network, for example 203.0.113.0/24. */
    private static function client(): Closure
    {
        return function (string $attribute, mixed $value, Closure $fail): void {
            [$address, $bits] = array_pad(explode('/', (string) $value, 2), 2, null);
            $ok = filter_var($address, FILTER_VALIDATE_IP) !== false;
            if ($ok && $bits !== null) {
                $max = str_contains($address, ':') ? 128 : 32;
                $ok = ctype_digit($bits) && (int) $bits <= $max && (string) (int) $bits === $bits;
            }
            if (! $ok) {
                $fail('Enter an IP address or a network, for example 203.0.113.7 or 203.0.113.0/24.');
            }
        };
    }
}
