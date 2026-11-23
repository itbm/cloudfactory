<?php

namespace App\Services\Uptime;

use App\Models\Server;
use App\Models\UptimeCheck;
use App\Services\Notifications\SmtpHost;
use App\Services\Plugins\Outbound;
use GuzzleHttp\Psr7\FnStream;
use GuzzleHttp\Psr7\Utils;
use Illuminate\Http\Client\Pool;
use Illuminate\Http\Client\Response;
use Illuminate\Support\Facades\Http;
use Throwable;

/**
 * Sends the requests of uptime checks from the panel, in parallel.
 *
 * A customer controls the DNS records of its domains. Thus the panel finds the addresses of a
 * domain one time, refuses an address that is not public (for example the private network of
 * the operator), and connects only to the addresses that it checked. The addresses of the
 * servers of the panel are permitted also when they are private, for a panel in a private
 * network. The panel does not follow a redirect, because a redirect can go to a different
 * host. It reads 1 MiB of each answer at most.
 */
class Probe
{
    /** The part of an answer that the panel reads, also to find the text of a check. */
    public const MAX_BODY_BYTES = 1048576;

    /** The requests that the panel sends at the same time. */
    public const CONCURRENCY = 25;

    public const USER_AGENT = 'Cloudfactory-Uptime/1';

    public function __construct(private Outbound $outbound) {}

    /**
     * @param  iterable<UptimeCheck>  $checks  each with its domain
     * @return array<string, ProbeResult> the result of each check, by the ID of the check
     */
    public function run(iterable $checks): array
    {
        $results = [];
        $targets = [];
        $servers = null;
        $hosts = [];
        foreach ($checks as $check) {
            $host = (string) $check->domain?->name;
            $servers ??= Server::pluck('addresses')->flatten()->filter()->values()->all();
            $hosts[$host] ??= $this->addressesOf($host, $servers);
            [$addresses, $refusal] = $hosts[$host];
            if ($refusal !== null) {
                $results[$check->id] = $refusal;

                continue;
            }
            $targets[$check->id] = [$check, $addresses];
        }
        if ($targets === []) {
            return $results;
        }

        $responses = Http::pool(function (Pool $pool) use ($targets) {
            foreach ($targets as $id => [$check, $addresses]) {
                $host = $check->domain->name;
                $port = $check->scheme === 'http' ? 80 : 443;
                $pinned = implode(',', array_map(fn (string $ip) => str_contains($ip, ':') ? "[{$ip}]" : $ip, $addresses));
                $pool->as($id)
                    ->withOptions([
                        'allow_redirects' => false,
                        'http_errors' => false,
                        // The panel connects to the addresses that it checked, not to a new DNS answer.
                        'curl' => [CURLOPT_RESOLVE => ["{$host}:{$port}:{$pinned}"]],
                        'sink' => self::sink(),
                    ])
                    ->withHeaders(['User-Agent' => self::USER_AGENT, 'Accept' => '*/*'])
                    ->timeout($check->timeout_seconds)
                    ->connectTimeout(min(5, $check->timeout_seconds))
                    ->get($check->url());
            }
        }, self::CONCURRENCY);

        foreach ($targets as $id => [$check]) {
            $results[$id] = $this->evaluate($check, $responses[$id] ?? null);
        }

        return $results;
    }

    /**
     * The addresses of a host, IPv4 first, or the result of a check that cannot connect.
     *
     * @param  list<string>  $servers  the addresses of the servers of the panel
     * @return array{0: list<string>, 1: ?ProbeResult}
     */
    private function addressesOf(string $host, array $servers): array
    {
        $addresses = $host === '' ? [] : $this->outbound->addresses($host);
        if ($addresses === []) {
            return [[], ProbeResult::failed("The panel cannot find the address of {$host}. Check the DNS records of the domain.", networkError: true)];
        }
        foreach ($addresses as $ip) {
            if (! SmtpHost::isPublic($ip) && ! in_array($ip, $servers, true)) {
                return [[], ProbeResult::failed("{$host} has the address {$ip}, which is not public. The panel connects only to public addresses and to the addresses of its servers.")];
            }
        }
        usort($addresses, fn (string $a, string $b) => str_contains($a, ':') <=> str_contains($b, ':'));

        return [array_slice($addresses, 0, 4), null];
    }

    /** Compares the answer with the answer that the check expects. */
    private function evaluate(UptimeCheck $check, mixed $response): ProbeResult
    {
        if (! $response instanceof Response) {
            return ProbeResult::failed(self::describe($response, $check), networkError: true);
        }
        $status = $response->status();
        $seconds = $response->handlerStats()['total_time'] ?? null;
        $ms = is_numeric($seconds) ? (int) round((float) $seconds * 1000) : null;
        $expected = $check->expected_status;
        if ($expected !== null ? $status !== $expected : ($status < 200 || $status >= 400)) {
            return ProbeResult::failed($expected !== null ? "HTTP status {$status}. The check expects {$expected}." : "HTTP status {$status}.", status: $status, responseMs: $ms);
        }
        if (($check->keyword ?? '') !== '' && ! str_contains(substr($response->body(), 0, self::MAX_BODY_BYTES), $check->keyword)) {
            return ProbeResult::failed("The answer does not have the text \"{$check->keyword}\".", status: $status, responseMs: $ms);
        }

        return new ProbeResult(true, $status, $ms);
    }

    /** The reason of a request with no answer, for people. */
    public static function describe(mixed $error, UptimeCheck $check): string
    {
        $message = $error instanceof Throwable ? $error->getMessage() : 'The request did not complete.';
        if (! preg_match('/cURL error (\d+): ?(.*?)(?: \(see https?:\/\/[^)]*\))?(?: for \S+)?$/s', $message, $m)) {
            return 'No connection: '.mb_substr(trim($message), 0, 250);
        }
        $detail = mb_substr(trim($m[2]), 0, 200);

        return match ((int) $m[1]) {
            6 => "The panel cannot find the address of {$check->domain?->name}.",
            7 => 'The server refused the connection, or the panel cannot connect to it.',
            28 => "No answer in {$check->timeout_seconds} seconds.",
            52 => 'The server closed the connection with no answer.',
            56 => 'The connection failed while the panel read the answer.',
            60 => "The TLS certificate is not valid: {$detail}",
            35, 51, 53, 54, 58, 59, 64, 66, 77, 80, 82, 83, 90, 91 => "The TLS connection failed: {$detail}",
            default => "No connection: {$detail}",
        };
    }

    /**
     * A stream for the answer that keeps the first MAX_BODY_BYTES only. It accepts the rest of
     * the answer and discards it, so a large answer does not fill the memory of the panel.
     */
    private static function sink(): FnStream
    {
        $buffer = Utils::streamFor(fopen('php://temp', 'w+b'));

        return FnStream::decorate($buffer, [
            'write' => function (string $data) use ($buffer): int {
                $room = self::MAX_BODY_BYTES - (int) $buffer->getSize();
                if ($room > 0) {
                    $buffer->write(substr($data, 0, $room));
                }

                return strlen($data);
            },
        ]);
    }
}
