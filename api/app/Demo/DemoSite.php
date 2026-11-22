<?php

namespace App\Demo;

use App\Models\Website;
use App\Services\AgentCallException;
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
            'SetFileMode' => self::setMode($website, (string) ($p['path'] ?? ''), (string) ($p['mode'] ?? ''), (bool) ($p['recursive'] ?? false), (string) ($p['fileMode'] ?? '')),
            'GetLogs' => ['lines' => self::logs($website, (int) ($p['lines'] ?? 200))],
            default => throw new AgentCallException('unimplemented', ''),
        };
    }

    /**
     * The files of a website: path => [type, content, modified, mode]. The first read makes the
     * files of a small PHP application. An archive that the demo agent made also has its
     * entries ("entries"), so that an extract gives them back.
     *
     * @return array<string, array{type: string, content: string, modified: string, mode?: string, entries?: array<string, mixed>}>
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
            // An archive of an earlier version, for the extract of the file manager.
            '/site-2026-10.zip' => $file(str_repeat("PK\x03\x04 demo archive\n", 180), 96) + ['entries' => [
                'site-2026-10' => $dir(96),
                'site-2026-10/index.php' => $file("<?php\n\necho 'Version of October 2026';\n", 96),
                'site-2026-10/css' => $dir(96),
                'site-2026-10/css/site.css' => $file("body {\n  font-family: Georgia, serif;\n}\n", 96),
            ]],
        ];
    }

    /** @return array<string, mixed> */
    private static function entry(string $path, array $node): array
    {
        return array_filter([
            'name' => basename($path),
            'type' => $node['type'],
            'sizeBytes' => $node['type'] === 'file' ? (string) strlen($node['content']) : null,
            'modified' => $node['modified'],
            'mode' => $node['mode'] ?? ($node['type'] === 'file' ? '0640' : '0750'),
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
     * The change of mode of the agent (agent/internal/sitefiles/mode.go).
     *
     * @return array<string, mixed>
     */
    private static function setMode(Website $website, string $path, string $mode, bool $recursive, string $fileMode): array
    {
        $tree = self::tree($website);
        $node = $path === '/' ? ['type' => 'directory', 'content' => '', 'modified' => now()->toIso8601ZuluString(), 'mode' => '0750'] : ($tree[$path] ?? null);
        if ($node === null) {
            throw new AgentCallException('not_found', 'the file or the directory does not exist');
        }
        $directory = $node['type'] === 'directory';
        if ($directory && (octdec($mode) & 0o500) !== 0o500) {
            throw new AgentCallException('invalid_argument', 'the owner must keep the read and the search permission of a directory (for example 0755 or 0700)');
        }
        if ($recursive && ! $directory) {
            throw new AgentCallException('invalid_argument', 'the path is a file; only a directory has files to change');
        }
        $changed = 1;
        $node['mode'] = $mode;
        if ($path !== '/') {
            $tree[$path] = $node;
        }
        if ($recursive) {
            foreach ($tree as $p => $child) {
                if ($path === '/' || str_starts_with($p, $path.'/')) {
                    $tree[$p]['mode'] = $child['type'] === 'directory' ? $mode : $fileMode;
                    $changed++;
                }
            }
        }
        self::save($website, $tree);

        return ['file' => self::entry($path === '/' ? '/' : $path, $node), 'changed' => (string) $changed];
    }

    /**
     * The archive job of the demo agent: it makes the file of the archive with the entries in
     * it, and gives the summary of a real agent.
     *
     * @param  list<string>  $paths
     * @return array<string, mixed>
     */
    public static function archive(Website $website, array $paths, string $to): array
    {
        $tree = self::tree($website);
        $entries = [];
        $summary = ['files' => 0, 'directories' => 0, 'links' => 0, 'sizeBytes' => 0, 'kept' => 0, 'skipped' => 0];
        $size = 0;
        foreach ($tree as $p => $node) {
            foreach ($paths as $selected) {
                $base = $selected === '/' ? '' : dirname($selected);
                if ($selected === '/' || $p === $selected || str_starts_with($p, $selected.'/')) {
                    $entries[ltrim(substr($p, strlen(rtrim($base, '/'))), '/')] = array_diff_key($node, ['entries' => true]);
                    $node['type'] === 'directory' ? $summary['directories']++ : $summary['files']++;
                    $size += strlen($node['content']);
                    break;
                }
            }
        }
        $tree[$to] = ['type' => 'file', 'content' => str_repeat("\x1f\x8b demo archive\n", max(4, intdiv($size, 40))), 'modified' => now()->toIso8601ZuluString(), 'entries' => $entries];
        self::save($website, $tree);
        $summary['sizeBytes'] = strlen($tree[$to]['content']);

        return $summary;
    }

    /**
     * The extract job of the demo agent. An archive that the demo agent did not make gives one
     * file with a note.
     *
     * @return array<string, mixed>
     */
    public static function extract(Website $website, string $path, string $to, bool $overwrite): array
    {
        $tree = self::tree($website);
        $entries = $tree[$path]['entries'] ?? [pathinfo($path, PATHINFO_FILENAME).'/README.txt' => ['type' => 'file', 'content' => "The demonstration mode does not read archives.\n", 'modified' => now()->toIso8601ZuluString()]];
        $summary = ['files' => 0, 'directories' => 0, 'links' => 0, 'sizeBytes' => 0, 'kept' => 0, 'skipped' => 0];
        $prefix = rtrim($to, '/');
        foreach (explode('/', trim($to, '/')) as $i => $part) {
            $dir = '/'.implode('/', array_slice(explode('/', trim($to, '/')), 0, $i + 1));
            if ($part !== '' && ! isset($tree[$dir])) {
                $tree[$dir] = ['type' => 'directory', 'content' => '', 'modified' => now()->toIso8601ZuluString()];
            }
        }
        foreach ($entries as $name => $node) {
            $target = $prefix.'/'.$name;
            $exists = isset($tree[$target]);
            if ($node['type'] === 'directory') {
                $tree[$target] ??= $node;
                $summary['directories']++;
            } elseif ($exists && ! $overwrite) {
                $summary['kept']++;
            } else {
                $tree[$target] = $node;
                $summary['files']++;
                $summary['sizeBytes'] += strlen($node['content']);
            }
        }
        self::save($website, $tree);

        return $summary;
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
