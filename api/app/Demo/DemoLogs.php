<?php

namespace App\Demo;

use App\Models\Server;
use App\Models\Website;
use Illuminate\Support\Carbon;

/**
 * The logs of the demonstration mode. A real agent keeps the access log of the edge proxy and
 * reads the log of the containers of a website (SearchLogs). The demo agent makes sample
 * entries for the last two days and searches them as the agent does. The entries of a period
 * of ten minutes are the same for each request, so that "Load older" gives the next entries.
 * The answers have the form of the JSON of a real agent: no field with an empty value, and
 * each 64-bit number as text.
 */
class DemoLogs
{
    /** The period of the sample entries. */
    private const HOURS = 48;

    /** The entries of one period of the same seed. */
    private const SLOT_SECONDS = 600;

    private const TOP = 5;

    private const MAX_TERMS = 20;

    /**
     * @param  array<string, mixed>  $p  the SearchLogs request
     * @return array<string, mixed>
     */
    public static function search(Server $server, ?Website $website, array $p): array
    {
        $all = self::entries($server, $website);
        $sources = $website === null ? ['access'] : ($p['sources'] ?? []);
        $sources = $sources === [] ? ['access', 'output', 'error'] : $sources;
        [$include, $exclude] = self::terms((string) ($p['text'] ?? ''));
        $statuses = $p['statuses'] ?? [];
        $methods = $p['methods'] ?? [];
        $client = (string) ($p['client'] ?? '');
        $accessOnly = $statuses !== [] || $methods !== [] || $client !== '';
        $from = isset($p['from']) ? Carbon::parse($p['from']) : null;
        $to = isset($p['to']) ? Carbon::parse($p['to']) : null;

        $found = array_values(array_filter($all, function (array $e) use ($sources, $include, $exclude, $statuses, $methods, $client, $accessOnly, $from, $to): bool {
            if (! in_array($e['source'], $sources, true) || ($from !== null && $e['time']->lt($from)) || ($to !== null && $e['time']->gte($to))) {
                return false;
            }
            $a = $e['access'] ?? null;
            if ($a === null) {
                $haystack = mb_strtolower($e['text']);
                if ($accessOnly) {
                    return false;
                }
            } else {
                $haystack = mb_strtolower(implode(' ', [$a['method'], $a['host'].$a['uri'], $a['status'], $a['client'], $a['proto'], $a['userAgent'] ?? '', $a['referer'] ?? '']));
                if (! self::status($statuses, $a['status']) || ($methods !== [] && ! in_array($a['method'], $methods, true)) || ($client !== '' && ! self::inNetwork($a['client'], $client))) {
                    return false;
                }
            }
            foreach ($include as $w) {
                if (! str_contains($haystack, $w)) {
                    return false;
                }
            }
            foreach ($exclude as $w) {
                if (str_contains($haystack, $w)) {
                    return false;
                }
            }

            return true;
        }));

        $offset = self::offset((string) ($p['cursor'] ?? ''));
        $limit = (int) ($p['limit'] ?? 100);
        $page = array_slice($found, $offset, $limit);
        $answer = ['entries' => array_map(self::answer(...), $page)];
        if ($offset + $limit < count($found)) {
            $answer['next'] = rtrim(strtr(base64_encode((string) json_encode(['demo' => $offset + $limit])), '+/', '-_'), '=');
        }
        if ($p['summary'] ?? false) {
            $answer['summary'] = self::summary($found, $all);
        }

        return $answer;
    }

    /**
     * The sample entries, the newest first. Each period has its own seed.
     *
     * @return list<array<string, mixed>>
     */
    private static function entries(Server $server, ?Website $website): array
    {
        $now = Carbon::now();
        $seed = $website?->name ?? 'server:'.$server->name;
        $entries = [];
        $first = intdiv($now->getTimestamp(), self::SLOT_SECONDS) * self::SLOT_SECONDS;
        for ($slot = $first; $slot > $now->getTimestamp() - self::HOURS * 3600; $slot -= self::SLOT_SECONDS) {
            mt_srand(crc32($seed.':'.$slot));
            $hour = (int) Carbon::createFromTimestampUTC($slot)->format('G');
            $count = mt_rand(1, 3) + ($hour >= 7 && $hour <= 21 ? mt_rand(2, 6) : 0);
            for ($i = 0; $i < $count; $i++) {
                $at = Carbon::createFromTimestampUTC($slot + mt_rand(0, self::SLOT_SECONDS - 1))->addMilliseconds(mt_rand(0, 999));
                $made = $website === null ? self::serverRequest($server, $at) : self::websiteRequest($website, $at);
                foreach ($made as $e) {
                    if ($e['time']->lte($now)) {
                        $entries[] = $e;
                    }
                }
            }
        }
        mt_srand();
        usort($entries, fn (array $a, array $b) => $b['time'] <=> $a['time']);

        return $entries;
    }

    /**
     * A request for a website, and the lines that the website wrote for it.
     *
     * @return list<array<string, mixed>>
     */
    private static function websiteRequest(Website $website, Carbon $at): array
    {
        $paths = ['/' => 20, '/css/site.css' => 12, '/products' => 10, '/products/42' => 8, '/products/17' => 5, '/cart' => 6, '/favicon.ico' => 5,
            '/api/stock?id=42' => 6, '/contact' => 4, '/search?q=tea' => 4, '/robots.txt' => 2, '/wp-login.php' => 3, '/.env' => 1];
        $path = self::pick($paths);
        $scanner = in_array($path, ['/wp-login.php', '/.env'], true);
        $method = match (true) {
            $path === '/cart' && mt_rand(0, 9) < 4, $path === '/contact' && mt_rand(0, 9) < 3, $path === '/wp-login.php' && mt_rand(0, 1) === 0 => 'POST',
            mt_rand(0, 32) === 0 => 'HEAD',
            default => 'GET',
        };
        $status = match (true) {
            $scanner, $path === '/favicon.ico' => 404,
            $path === '/api/stock?id=42' && mt_rand(0, 6) === 0 => 500,
            $method === 'POST' && $path === '/cart' => 303,
            $path === '/css/site.css' && mt_rand(0, 1) === 0 => 304,
            $path === '/products/17' && mt_rand(0, 3) === 0 => 301,
            default => 200,
        };
        $host = $website->name;
        $entries = [self::access($at, [
            'client' => $scanner ? '198.51.100.250' : self::pick(['203.0.113.7' => 9, '198.51.100.23' => 6, '192.0.2.44' => 4, '2001:db8:4e::7a' => 4, '203.0.113.88' => 3, '192.0.2.130' => 2, '2001:db8:91::12' => 2, '198.51.100.61' => 1]),
            'method' => $method, 'host' => $host, 'uri' => $path, 'proto' => self::pick(['HTTP/2.0' => 7, 'HTTP/1.1' => 2, 'HTTP/3.0' => 1]), 'status' => $status,
            'sizeBytes' => match ($status) {
                304, 303, 301 => 0, 404 => 548, default => $path === '/css/site.css' ? 2416 : mt_rand(900, 48000)
            },
            'durationSeconds' => $status === 500 ? mt_rand(2500, 5000) / 1000 : mt_rand(1, 400000) / 1000000,
            'userAgent' => $scanner ? 'Mozilla/5.0 zgrab/0.x' : self::pick([
                'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:145.0) Gecko/20100101 Firefox/145.0' => 4,
                'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36' => 5,
                'Mozilla/5.0 (iPhone; CPU iPhone OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Mobile/15E148 Safari/604.1' => 4,
                'Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)' => 1,
                'curl/8.11.1' => 1,
            ]),
            'referer' => match (true) {
                $path === '/css/site.css', $path === '/cart' => "https://{$host}/products",
                ! $scanner && mt_rand(0, 4) === 0 => 'https://www.google.com/',
                default => '',
            },
        ])];
        $later = fn (int $ms) => $at->copy()->subMilliseconds($ms);
        if ($status === 500) {
            $entries[] = self::line($later(40), 'error', 'PHP Fatal error:  Uncaught RuntimeException: The stock service did not answer in /app/src/stock.php:31');
        } elseif (mt_rand(0, 14) === 0) {
            $entries[] = self::line($later(20), 'error', self::pick([
                'PHP Warning:  Undefined array key "utm_source" in /app/src/bootstrap.php on line 14' => 3,
                'PHP Deprecated:  Implicit conversion from float 19.99 to int loses precision in /app/src/cart.php on line 52' => 1,
            ]));
        }
        if ($status === 303) {
            $entries[] = self::line($later(10), 'output', 'Sent the confirmation of order '.mt_rand(1040, 1990).' to the customer.');
        } elseif (mt_rand(0, 40) === 0) {
            $entries[] = self::line($later(5), 'output', self::pick(['Cleared '.mt_rand(3, 40).' expired sessions.' => 2, 'Updated the stock of '.mt_rand(2, 30).' products.' => 2, 'Rebuilt the search index in '.mt_rand(200, 900).' ms.' => 1]));
        }

        return $entries;
    }

    /**
     * A request for the panel, the webmail or the mail server: the server log.
     *
     * @return list<array<string, mixed>>
     */
    private static function serverRequest(Server $server, Carbon $at): array
    {
        $panel = parse_url((string) config('app.url'), PHP_URL_HOST) ?: 'panel.example.com';
        $mail = $server->mail_hostname ?? 'mail.example.com';
        [$host, $uri, $method] = explode(' ', self::pick([
            "{$panel} /api/v1/websites GET" => 6, "{$panel} /api/v1/jobs GET" => 8, "{$panel} /api/v1/me GET" => 3, "{$panel} /api/v1/auth/login POST" => 2,
            "{$panel} / GET" => 3, "{$mail} / GET" => 3, "{$mail} /.well-known/jmap GET" => 4, "{$panel} /wp-admin/ GET" => 1,
        ]));

        return [self::access($at, [
            'client' => self::pick(['203.0.113.7' => 6, '192.0.2.44' => 3, '2001:db8:4e::7a' => 2, '198.51.100.250' => 1]),
            'method' => $method, 'host' => $host, 'uri' => $uri, 'proto' => 'HTTP/2.0',
            'status' => match (true) {
                $uri === '/wp-admin/' => 404, $uri === '/api/v1/auth/login' && mt_rand(0, 3) === 0 => 422, default => 200
            },
            'sizeBytes' => mt_rand(400, 90000), 'durationSeconds' => mt_rand(1000, 250000) / 1000000,
            'userAgent' => 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:145.0) Gecko/20100101 Firefox/145.0', 'referer' => '',
        ])];
    }

    /**
     * @param  array<string, mixed>  $access
     * @return array<string, mixed>
     */
    private static function access(Carbon $at, array $access): array
    {
        return ['time' => $at, 'source' => 'access', 'text' => '', 'access' => $access];
    }

    /** @return array<string, mixed> */
    private static function line(Carbon $at, string $source, string $text): array
    {
        return ['time' => $at, 'source' => $source, 'text' => $text];
    }

    /**
     * A value with a weight, from the seed of the period.
     *
     * @param  array<string, int>  $weights
     */
    private static function pick(array $weights): string
    {
        $n = mt_rand(1, array_sum($weights));
        foreach ($weights as $value => $weight) {
            if (($n -= $weight) <= 0) {
                return (string) $value;
            }
        }

        return (string) array_key_last($weights);
    }

    /**
     * An entry in the form of the agent.
     *
     * @param  array<string, mixed>  $e
     * @return array<string, mixed>
     */
    private static function answer(array $e): array
    {
        $out = ['at' => $e['time']->format('Y-m-d\TH:i:s.v\Z'), 'source' => $e['source']];
        if ($e['text'] !== '') {
            $out['text'] = $e['text'];
        }
        if (isset($e['access'])) {
            $a = $e['access'];
            $a['sizeBytes'] = (string) $a['sizeBytes'];
            $out['access'] = array_filter($a, fn ($v) => $v !== '' && $v !== '0' && $v !== 0);
        }

        return $out;
    }

    /**
     * The counts of the entries that agree with the search, in the form of the agent.
     *
     * @param  list<array<string, mixed>>  $found
     * @param  list<array<string, mixed>>  $all
     * @return array<string, mixed>
     */
    private static function summary(array $found, array $all): array
    {
        $sources = ['access' => 0, 'output' => 0, 'error' => 0];
        $statuses = $paths = $clients = [];
        $size = 0;
        foreach ($found as $e) {
            $sources[$e['source']]++;
            if (isset($e['access'])) {
                $a = $e['access'];
                $class = intdiv($a['status'], 100).'xx';
                $statuses[$class] = ($statuses[$class] ?? 0) + 1;
                $size += $a['sizeBytes'];
                $path = explode('?', $a['uri'], 2)[0];
                $paths[$path] = ($paths[$path] ?? 0) + 1;
                $clients[$a['client']] = ($clients[$a['client']] ?? 0) + 1;
            }
        }
        $top = function (array $counts): array {
            uksort($counts, fn ($a, $b) => [$counts[$b], (string) $a] <=> [$counts[$a], (string) $b]);

            return array_map(fn ($value) => ['value' => (string) $value, 'count' => (string) $counts[$value]], array_slice(array_keys($counts), 0, self::TOP));
        };
        ksort($statuses);

        return array_filter([
            'access' => (string) $sources['access'], 'output' => (string) $sources['output'], 'error' => (string) $sources['error'],
            'statuses' => array_map('strval', $statuses), 'topPaths' => $top($paths), 'topClients' => $top($clients),
            'sizeBytes' => (string) $size, 'oldest' => $all === [] ? '' : end($all)['time']->format('Y-m-d\TH:i:s.v\Z'),
        ], fn ($v) => $v !== '0' && $v !== '' && $v !== []);
    }

    /**
     * The words of a search, as the agent reads them: a phrase in quotes is one word, and a
     * word that starts with "-" must not be in the entry.
     *
     * @return array{list<string>, list<string>}
     */
    private static function terms(string $s): array
    {
        $include = $exclude = [];
        $s = mb_strtolower($s);
        while ($s !== '' && count($include) + count($exclude) < self::MAX_TERMS) {
            $s = ltrim($s, ' ');
            if ($s === '') {
                break;
            }
            $not = strlen($s) > 1 && $s[0] === '-' && $s[1] !== ' ';
            if ($not) {
                $s = substr($s, 1);
            }
            if ($s[0] === '"') {
                $end = strpos($s, '"', 1);
                [$word, $s] = $end === false ? [substr($s, 1), ''] : [substr($s, 1, $end - 1), substr($s, $end + 1)];
            } else {
                $end = strpos($s, ' ');
                [$word, $s] = $end === false ? [$s, ''] : [substr($s, 0, $end), substr($s, $end)];
            }
            if ($word !== '') {
                $not ? $exclude[] = $word : $include[] = $word;
            }
        }

        return [$include, $exclude];
    }

    /** @param list<string> $statuses */
    private static function status(array $statuses, int $status): bool
    {
        $code = sprintf('%03d', $status);
        foreach ($statuses as $s) {
            if ($s === $code || (str_ends_with($s, 'xx') && $s[0] === $code[0])) {
                return true;
            }
        }

        return $statuses === [];
    }

    /** Whether an address is the client address or in the network of the filter. */
    private static function inNetwork(string $address, string $filter): bool
    {
        [$network, $bits] = array_pad(explode('/', $filter, 2), 2, null);
        $a = @inet_pton($address);
        $n = @inet_pton($network);
        if ($a === false || $n === false || strlen($a) !== strlen($n)) {
            return false;
        }
        $bits = $bits === null ? strlen($a) * 8 : (int) $bits;
        $bytes = intdiv($bits, 8);
        if (substr($a, 0, $bytes) !== substr($n, 0, $bytes)) {
            return false;
        }
        $rest = $bits % 8;

        return $rest === 0 || ((ord($a[$bytes]) ^ ord($n[$bytes])) & (0xFF << (8 - $rest)) & 0xFF) === 0;
    }

    /** The position of the cursor of the demo agent. */
    private static function offset(string $cursor): int
    {
        if ($cursor === '') {
            return 0;
        }
        $data = json_decode((string) base64_decode(strtr($cursor, '-_', '+/')), true);

        return is_array($data) ? max(0, (int) ($data['demo'] ?? 0)) : 0;
    }
}
