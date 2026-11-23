<?php

namespace App\Demo;

use App\Models\Website;
use App\Services\AgentCallException;
use App\Services\Exports\TarWriter;
use App\Services\WebsiteSecurity;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Cache;

/**
 * The files and the logs of a website in the demonstration mode. A real agent reads them on
 * the server. The demo agent keeps a small tree of files for each website in the cache, so
 * that the file manager of the web panel works with no server. The answers have the form of
 * the JSON of a real agent: no field with an empty value, and each 64-bit number as text.
 */
class DemoSite
{
    private const MAX_FILE_BYTES = 5 * 1024 * 1024;

    /**
     * @param  array<string, mixed>  $p
     * @return array<string, mixed>
     */
    public static function call(string $method, array $p): array
    {
        $website = Website::find($p['websiteId'] ?? '') ?? throw new AgentCallException('not_found', 'the agent does not know this website');

        return match ($method) {
            'ListFiles' => self::list($website, (string) ($p['path'] ?? '/')),
            'ReadFile' => self::read($website, (string) ($p['path'] ?? '')),
            'WriteFile' => self::write($website, (string) ($p['path'] ?? ''), (string) base64_decode((string) ($p['content'] ?? '')), (bool) ($p['createOnly'] ?? false)),
            'MakeDirectory' => self::makeDirectory($website, (string) ($p['path'] ?? '')),
            'MoveFile' => self::move($website, (string) ($p['from'] ?? ''), (string) ($p['to'] ?? '')),
            'DeleteFile' => self::delete($website, (string) ($p['path'] ?? '')),
            'GetLogs' => ['lines' => self::logs($website, (int) ($p['lines'] ?? 200))],
            'GetFirewallEvents' => ['events' => self::firewallEvents($website, (int) ($p['limit'] ?? 50))],
            default => throw new AgentCallException('unimplemented', ''),
        };
    }

    /**
     * The files of a website: path => [type, content, modified]. The first read makes the
     * files of a small PHP application.
     *
     * @return array<string, array{type: string, content: string, modified: string}>
     */
    private static function tree(Website $website): array
    {
        return Cache::get('demo:files:'.$website->id) ?? self::defaults($website);
    }

    /** @param array<string, array{type: string, content: string, modified: string}> $tree */
    private static function save(Website $website, array $tree): void
    {
        ksort($tree);
        Cache::forever('demo:files:'.$website->id, $tree);
    }

    /** @return array<string, array{type: string, content: string, modified: string}> */
    private static function defaults(Website $website): array
    {
        $at = fn (int $hoursAgo) => ($website->created_at ?? now())->copy()->addHours(max(0, 200 - $hoursAgo))->min(now())->toIso8601ZuluString();
        $dir = fn (int $h) => ['type' => 'directory', 'content' => '', 'modified' => $at($h)];
        $file = fn (string $content, int $h) => ['type' => 'file', 'content' => $content, 'modified' => $at($h)];

        return [
            '/public' => $dir(4),
            '/public/index.php' => $file("<?php\n\nrequire __DIR__.'/../src/bootstrap.php';\n\necho render('home', ['title' => '{$website->name}']);\n", 4),
            '/public/robots.txt' => $file("User-agent: *\nAllow: /\n", 190),
            '/public/css' => $dir(30),
            '/public/css/site.css' => $file("body {\n  font-family: system-ui, sans-serif;\n  margin: 0 auto;\n  max-width: 60rem;\n}\n", 30),
            '/src' => $dir(12),
            '/src/bootstrap.php' => $file("<?php\n\nfunction render(string \$view, array \$data): string\n{\n    extract(\$data);\n    ob_start();\n    require __DIR__.\"/views/{\$view}.php\";\n\n    return ob_get_clean();\n}\n", 12),
            '/src/views' => $dir(12),
            '/src/views/home.php' => $file("<!doctype html>\n<title><?= htmlspecialchars(\$title) ?></title>\n<link rel=\"stylesheet\" href=\"/css/site.css\">\n<h1><?= htmlspecialchars(\$title) ?></h1>\n", 12),
            '/storage' => $dir(1),
            '/storage/cache' => $dir(1),
            '/composer.json' => $file("{\n    \"name\": \"example/website\",\n    \"require\": {\n        \"php\": \"^8.3\"\n    }\n}\n", 190),
            '/.env.example' => $file("APP_ENV=production\nDB_HOST=mariadb.internal\n", 190),
        ];
    }

    /**
     * The files of a website as a tar archive with gzip, as "cf-agent site-archive" writes it
     * for an account export.
     */
    public static function archive(Website $website): string
    {
        $out = fopen('php://memory', 'w+b');
        $tar = new TarWriter($out);
        foreach (self::tree($website) as $path => $node) {
            if ($node['type'] === 'file') {
                $tar->addString(ltrim($path, '/'), $node['content'], Carbon::parse($node['modified'])->getTimestamp());
            }
        }
        fwrite($out, TarWriter::end());
        rewind($out);
        $bytes = (string) stream_get_contents($out);
        fclose($out);

        return (string) gzencode($bytes);
    }

    /** @return array<string, mixed> */
    private static function entry(string $path, array $node): array
    {
        return array_filter([
            'name' => basename($path),
            'type' => $node['type'],
            'sizeBytes' => $node['type'] === 'file' ? (string) strlen($node['content']) : null,
            'modified' => $node['modified'],
            'mode' => $node['type'] === 'file' ? '0640' : '0750',
        ], fn ($v) => $v !== null && $v !== '0');
    }

    private static function parent(array $tree, string $path): void
    {
        $parent = dirname($path);
        if ($parent !== '/' && ($tree[$parent]['type'] ?? '') !== 'directory') {
            throw new AgentCallException('not_found', 'the file or the directory does not exist');
        }
    }

    /** @return array<string, mixed> */
    private static function list(Website $website, string $path): array
    {
        $tree = self::tree($website);
        if ($path !== '/' && ($tree[$path]['type'] ?? '') !== 'directory') {
            throw isset($tree[$path]) ? new AgentCallException('invalid_argument', 'the path is not a directory') : new AgentCallException('not_found', 'the file or the directory does not exist');
        }
        $entries = [];
        foreach ($tree as $p => $node) {
            if (dirname($p) === $path) {
                $entries[] = self::entry($p, $node);
            }
        }
        usort($entries, fn ($a, $b) => [$a['type'] !== 'directory', $a['name']] <=> [$b['type'] !== 'directory', $b['name']]);

        return $entries ? ['entries' => $entries] : [];
    }

    /** @return array<string, mixed> */
    private static function read(Website $website, string $path): array
    {
        $node = self::tree($website)[$path] ?? throw new AgentCallException('not_found', 'the file or the directory does not exist');
        if ($node['type'] !== 'file') {
            throw new AgentCallException('invalid_argument', 'the path is not a file');
        }

        return array_filter(['file' => self::entry($path, $node), 'content' => base64_encode($node['content'])], fn ($v) => $v !== '');
    }

    /** @return array<string, mixed> */
    private static function write(Website $website, string $path, string $content, bool $createOnly): array
    {
        $tree = self::tree($website);
        self::parent($tree, $path);
        if (isset($tree[$path]) && ($createOnly || $tree[$path]['type'] !== 'file')) {
            throw $createOnly ? new AgentCallException('already_exists', 'a file or a directory with this name exists already') : new AgentCallException('invalid_argument', 'the path is not a file');
        }
        if (strlen($content) > self::MAX_FILE_BYTES) {
            throw new AgentCallException('out_of_range', 'the file has more than 5 MB; use SFTP to upload it');
        }
        $tree[$path] = ['type' => 'file', 'content' => $content, 'modified' => now()->toIso8601ZuluString()];
        self::save($website, $tree);

        return self::entry($path, $tree[$path]);
    }

    /** @return array<string, mixed> */
    private static function makeDirectory(Website $website, string $path): array
    {
        $tree = self::tree($website);
        self::parent($tree, $path);
        if (isset($tree[$path]) || $path === '/') {
            throw new AgentCallException('already_exists', 'a file or a directory with this name exists already');
        }
        $tree[$path] = ['type' => 'directory', 'content' => '', 'modified' => now()->toIso8601ZuluString()];
        self::save($website, $tree);

        return self::entry($path, $tree[$path]);
    }

    /** @return array<string, mixed> */
    private static function move(Website $website, string $from, string $to): array
    {
        $tree = self::tree($website);
        if (! isset($tree[$from])) {
            throw new AgentCallException('not_found', 'the file or the directory does not exist');
        }
        self::parent($tree, $to);
        if (isset($tree[$to])) {
            throw new AgentCallException('already_exists', 'a file or a directory with this name exists already');
        }
        if ($to === $from || str_starts_with($to, $from.'/')) {
            throw new AgentCallException('invalid_argument', 'a directory cannot be moved into itself');
        }
        foreach ($tree as $p => $node) {
            if ($p === $from || str_starts_with($p, $from.'/')) {
                unset($tree[$p]);
                $tree[$to.substr($p, strlen($from))] = $node;
            }
        }
        self::save($website, $tree);

        return self::entry($to, $tree[$to]);
    }

    /** @return array<string, mixed> */
    private static function delete(Website $website, string $path): array
    {
        $tree = self::tree($website);
        if (! isset($tree[$path])) {
            throw new AgentCallException('not_found', 'the file or the directory does not exist');
        }
        foreach (array_keys($tree) as $p) {
            if ($p === $path || str_starts_with($p, $path.'/')) {
                unset($tree[$p]);
            }
        }
        self::save($website, $tree);

        return [];
    }

    /**
     * Sample requests that the firewall of the edge proxy blocked or detected, the newest
     * first. A website with the firewall off has none. A rule that the website turned off is not
     * in the events.
     *
     * @return list<array<string, mixed>>
     */
    private static function firewallEvents(Website $website, int $limit): array
    {
        $security = WebsiteSecurity::of($website);
        if ($security['firewall'] === 'off') {
            return [];
        }
        // uri, client, rules [id, message, data], score
        $samples = [
            ['/products?id=1%27%20OR%201=1--', '198.51.100.40', [[942100, 'SQL Injection Attack Detected via libinjection', "Matched Data: s&1c found within ARGS:id: 1' OR 1=1--"]], 5],
            ['/search?q=%3Cscript%3Ealert(1)%3C/script%3E', '203.0.113.77', [
                [941100, 'XSS Attack Detected via libinjection', 'Matched Data: XSS data found within ARGS:q: <script>alert(1)</script>'],
                [941110, 'XSS Filter - Category 1: Script Tag Vector', 'Matched Data: <script> found within ARGS:q: <script>alert(1)</script>'],
            ], 10],
            ['/', '192.0.2.14', [[913100, 'Found User-Agent associated with security scanner', 'Matched Data: sqlmap found within REQUEST_HEADERS:User-Agent: sqlmap/1.8.2']], 5],
            ['/index.php?file=../../../../etc/passwd', '198.51.100.9', [
                [930100, 'Path Traversal Attack (/../ or /..;/)', 'Matched Data: /../ found within ARGS:file: ../../../../etc/passwd'],
                [930120, 'OS File Access Attempt', 'Matched Data: etc/passwd found within ARGS:file: ../../../../etc/passwd'],
            ], 10],
            ['/.env', '203.0.113.201', [[930130, 'Restricted File Access Attempt', 'Matched Data: /.env found within REQUEST_FILENAME: /.env']], 5],
            ['/wp-admin/post.php', '203.0.113.5', [[942100, 'SQL Injection Attack Detected via libinjection', 'Matched Data: 1&1 found within ARGS:content: Our opening hours: 9 to 5. Select the shop and order 1 or more items.']], 5],
            ['/?cmd=%3C%3Fphp%20system(%27id%27);%20%3F%3E', '192.0.2.88', [[933100, 'PHP Injection Attack: PHP Open Tag Found', "Matched Data: <?php found within ARGS:cmd: <?php system('id'); ?>"]], 5],
        ];
        $hour = Carbon::now()->startOfHour();
        mt_srand(crc32($website->id.$hour->timestamp));
        $events = [];
        $at = Carbon::now()->startOfMinute();
        for ($i = 0; $i < 12 && count($events) < max(1, $limit); $i++) {
            $at = $at->copy()->subMinutes(mt_rand(3, 95));
            [$uri, $client, $rules, $score] = $samples[mt_rand(0, count($samples) - 1)];
            $rules = array_values(array_filter($rules, fn (array $r) => ! in_array($r[0], $security['firewallExclusions'], true)));
            if ($rules === []) {
                continue;
            }
            $events[] = [
                'at' => $at->toIso8601ZuluString(), 'clientIp' => $client, 'host' => $security['firewall'] === 'block' ? $website->name : '', 'uri' => $uri,
                'action' => $security['firewall'] === 'block' ? 'blocked' : 'detected', 'score' => $score,
                'rules' => array_map(fn (array $r) => ['id' => $r[0], 'message' => $r[1], 'data' => $r[2]], $rules),
            ];
        }
        mt_srand();

        return $events;
    }

    /**
     * Example lines of the access log and of the error log of a website. The lines are the
     * same for each request in one minute.
     *
     * @return list<array{at: string, stream: string, text: string}>
     */
    private static function logs(Website $website, int $count): array
    {
        $paths = ['/', '/css/site.css', '/products', '/products/42', '/cart', '/favicon.ico', '/api/stock?id=42', '/wp-login.php', '/contact', '/search?q=tea'];
        $errors = [
            'PHP Warning:  Undefined array key "utm_source" in /app/src/bootstrap.php on line 14',
            'PHP Deprecated:  Implicit conversion from float 19.99 to int loses precision in /app/src/cart.php on line 52',
            'PHP Fatal error:  Uncaught RuntimeException: The stock service did not answer in /app/src/stock.php:31',
        ];
        $minute = Carbon::now()->startOfMinute();
        mt_srand(crc32($website->id.$minute->timestamp));
        $lines = [];
        $at = $minute->copy();
        for ($i = 0; $i < min(max($count, 1), 40); $i++) {
            $at = $at->copy()->subSeconds(mt_rand(2, 40));
            $path = $paths[mt_rand(0, count($paths) - 1)];
            $status = match (true) {
                $path === '/wp-login.php', $path === '/favicon.ico' => 404,
                $path === '/api/stock?id=42' && mt_rand(0, 3) === 0 => 500,
                default => 200,
            };
            if ($status === 500 || mt_rand(0, 9) === 0) {
                $lines[] = ['at' => $at->toIso8601ZuluString(), 'stream' => 'error', 'text' => $status === 500 ? $errors[2] : $errors[mt_rand(0, 1)]];
            }
            $lines[] = ['at' => $at->toIso8601ZuluString(), 'stream' => 'access', 'text' => json_encode([
                'ts' => $at->timestamp, 'method' => 'GET', 'host' => $website->name, 'uri' => $path, 'status' => $status,
                'duration' => round(mt_rand(3, 400) / 1000, 3), 'size' => mt_rand(300, 48000), 'remote_ip' => '203.0.113.'.mt_rand(2, 250),
            ], JSON_UNESCAPED_SLASHES)];
        }
        mt_srand();

        return array_reverse($lines);
    }
}
