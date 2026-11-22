<?php

namespace App\Services\Imports;

use App\Validation\Input;
use InvalidArgumentException;

/**
 * The description of an account in the format "cf-account/1" (docs/imports.md). The agent
 * reads it from the backup archive of a different panel (agent/internal/migrate). The account
 * export of the panel writes the same format, so that an account can move to a different
 * Cloudfactory panel.
 *
 * The panel does not trust a description: a customer made the archive. normalise() keeps only
 * the fields of the format, with the limits of the agent, and leaves out each name that the
 * input rules refuse.
 */
final class Description
{
    public const FORMAT = 'cf-account/1';

    /** The panels that the agent can read, and this panel for an account export. */
    public const PANELS = ['cpanel', 'directadmin', 'plesk', 'hestia', 'cloudfactory'];

    public const PANEL_NAMES = ['cpanel' => 'cPanel', 'directadmin' => 'DirectAdmin', 'plesk' => 'Plesk', 'hestia' => 'HestiaCP', 'cloudfactory' => 'Cloudfactory'];

    /** The limits of the agent (agent/internal/migrate/account.go). */
    public const MAX_WEBSITES = 200;

    public const MAX_DATABASES = 500;

    public const MAX_MAILBOXES = 2000;

    public const MAX_ALIASES = 5000;

    public const MAX_ZONES = 200;

    public const MAX_WARNINGS = 200;

    public const MAX_ZONE_FILE = 1024 ** 2;

    /**
     * @return array<string, mixed>
     *
     * @throws InvalidArgumentException when the value is not a description.
     */
    public static function normalise(mixed $d): array
    {
        if (! is_array($d) || ($d['format'] ?? null) !== self::FORMAT) {
            throw new InvalidArgumentException('The description of the account does not have the format '.self::FORMAT.'.');
        }
        $source = is_array($d['source'] ?? null) ? $d['source'] : [];
        $panel = self::text($source['panel'] ?? '', 20);
        if (! in_array($panel, self::PANELS, true)) {
            throw new InvalidArgumentException('The description of the account comes from a panel that the import does not know.');
        }
        $customer = is_array($d['customer'] ?? null) ? $d['customer'] : [];
        $email = self::text($customer['email'] ?? '', 254);

        return [
            'format' => self::FORMAT,
            'source' => ['panel' => $panel, 'version' => self::text($source['version'] ?? '', 200), 'account' => self::text($source['account'] ?? '', 100)],
            'customer' => ['name' => self::text($customer['name'] ?? '', 200), 'email' => filter_var($email, FILTER_VALIDATE_EMAIL) ? $email : ''],
            'websites' => self::list($d['websites'] ?? [], self::MAX_WEBSITES, self::website(...)),
            'dnsZones' => self::list($d['dnsZones'] ?? [], self::MAX_ZONES, self::zone(...)),
            'mail' => ['domains' => self::list(is_array($d['mail'] ?? null) ? ($d['mail']['domains'] ?? []) : [], self::MAX_WEBSITES, self::mailDomain(...))],
            'warnings' => self::list($d['warnings'] ?? [], self::MAX_WARNINGS, fn ($w) => is_array($w) ? [
                'item' => self::text($w['item'] ?? '', 300),
                'message' => self::text($w['message'] ?? '', 1000),
            ] : null),
        ];
    }

    /** @return array<string, mixed>|null */
    private static function website(mixed $w): ?array
    {
        if (! is_array($w) || ! self::domain($w['name'] ?? null)) {
            return null;
        }
        $runtime = is_array($w['runtime'] ?? null) ? $w['runtime'] : [];
        $php = self::text($runtime['phpVersion'] ?? '', 10);
        $root = self::text($runtime['documentRoot'] ?? '', 200);
        $config = is_array($w['appConfig'] ?? null) ? $w['appConfig'] : null;
        $configFile = $config ? self::path($config['file'] ?? null) : null;

        return [
            'name' => $w['name'],
            'kind' => 'public',
            'runtime' => [
                'family' => in_array($runtime['family'] ?? '', ['php', 'static'], true) ? $runtime['family'] : 'php',
                'phpVersion' => preg_match('/^\d\.\d{1,2}$/', $php) ? $php : '',
                'documentRoot' => $root !== '' && Input::valid('documentRoot', $root) ? $root : '.',
            ],
            'domains' => self::list($w['domains'] ?? [], 100, fn ($x) => is_array($x) && self::domain($x['name'] ?? null) && in_array($x['kind'] ?? '', ['primary', 'alias', 'redirect'], true)
                ? ['name' => $x['name'], 'kind' => $x['kind']] : null),
            'cronJobs' => self::list($w['cronJobs'] ?? [], 500, fn ($c) => is_array($c) && Input::valid('cronSchedule', $c['schedule'] ?? null) && Input::valid('commandLine', $c['command'] ?? null)
                ? ['schedule' => $c['schedule'], 'command' => $c['command'], 'enabled' => false] : null),
            'databases' => self::list($w['databases'] ?? [], self::MAX_DATABASES, fn ($db) => is_array($db) && ($db['engine'] ?? '') === 'mariadb' && self::databaseName($db['name'] ?? null)
                ? ['engine' => 'mariadb', 'name' => $db['name'], 'content' => self::content($db['content'] ?? null)] : null),
            'databaseUsers' => self::list($w['databaseUsers'] ?? [], self::MAX_DATABASES, fn ($u) => is_array($u) && self::databaseName($u['name'] ?? null) ? [
                'engine' => 'mariadb',
                'name' => $u['name'],
                'access' => 'website',
                'grants' => self::list($u['grants'] ?? [], 100, fn ($g) => is_array($g) && self::databaseName($g['database'] ?? null) && in_array($g['privileges'] ?? '', ['readwrite', 'readonly'], true)
                    ? ['database' => $g['database'], 'privileges' => $g['privileges']] : null),
            ] : null),
            'content' => ['files' => self::content(is_array($w['content'] ?? null) ? ($w['content']['files'] ?? null) : null)],
            'diskBytes' => self::bytes($w['diskBytes'] ?? 0),
            'appConfig' => $config && ($config['kind'] ?? '') === 'wordpress' && $configFile && self::databaseName($config['database'] ?? null)
                ? ['kind' => 'wordpress', 'file' => $configFile, 'database' => $config['database']] : null,
        ];
    }

    /** @return array<string, mixed>|null */
    private static function zone(mixed $z): ?array
    {
        if (! is_array($z) || ! self::domain($z['name'] ?? null) || ! is_string($z['zoneFile'] ?? null) || strlen($z['zoneFile']) > self::MAX_ZONE_FILE) {
            return null;
        }

        return ['name' => $z['name'], 'zoneFile' => $z['zoneFile']];
    }

    /** @return array<string, mixed>|null */
    private static function mailDomain(mixed $m): ?array
    {
        if (! is_array($m) || ! self::domain($m['name'] ?? null)) {
            return null;
        }
        $name = $m['name'];

        return [
            'name' => $name,
            'mailboxes' => self::list($m['mailboxes'] ?? [], self::MAX_MAILBOXES, fn ($b) => is_array($b) && Input::valid('mailLocalPart', $b['localPart'] ?? null) ? [
                'localPart' => $b['localPart'],
                'quotaBytes' => self::bytes($b['quotaBytes'] ?? 0),
                'passwordHash' => is_string($b['passwordHash'] ?? null) && Input::valid('mailPasswordHash', $b['passwordHash']) ? $b['passwordHash'] : '',
                'content' => self::content($b['content'] ?? null),
                'messages' => max(0, min((int) ($b['messages'] ?? 0), PHP_INT_MAX)),
            ] : null),
            'aliases' => self::list($m['aliases'] ?? [], self::MAX_ALIASES, function ($a) {
                if (! is_array($a) || ! Input::valid('mailLocalPart', $a['localPart'] ?? null)) {
                    return null;
                }
                $destinations = array_values(array_unique(array_filter(is_array($a['destinations'] ?? null) ? $a['destinations'] : [],
                    fn ($d) => is_string($d) && Input::valid('mailAddress', $d))));

                return $destinations === [] ? null : [
                    'localPart' => $a['localPart'],
                    'kind' => ($a['kind'] ?? '') === 'alias' ? 'alias' : 'forwarder',
                    'destinations' => array_slice($destinations, 0, 50),
                ];
            }),
        ];
    }

    /** @return array{path: string, bytes: int, exclude: list<string>}|null */
    private static function content(mixed $c): ?array
    {
        if (! is_array($c) || ! ($path = self::path($c['path'] ?? null))) {
            return null;
        }

        return [
            'path' => $path,
            'bytes' => self::bytes($c['bytes'] ?? 0),
            'exclude' => self::list($c['exclude'] ?? [], 200, fn ($x) => self::path($x)),
        ];
    }

    /** A clean relative path in the archive, as the agent gives it, or null. */
    private static function path(mixed $p): ?string
    {
        if (! is_string($p) || $p === '' || strlen($p) > 1000 || str_starts_with($p, '/') || str_ends_with($p, '/') || preg_match('/[\x00-\x1f\x7f]/', $p)) {
            return null;
        }
        foreach (explode('/', $p) as $part) {
            if ($part === '' || $part === '.' || $part === '..') {
                return null;
            }
        }

        return $p;
    }

    private static function domain(mixed $name): bool
    {
        return is_string($name) && Input::valid('domain', $name);
    }

    /** A database name of the old panel: letters, digits and underscores. */
    private static function databaseName(mixed $name): bool
    {
        return is_string($name) && preg_match('/^[A-Za-z0-9_]{1,64}$/', $name) === 1;
    }

    private static function bytes(mixed $n): int
    {
        return is_int($n) || (is_float($n) && $n < PHP_INT_MAX) ? max(0, (int) $n) : 0;
    }

    private static function text(mixed $s, int $max): string
    {
        return is_string($s) ? mb_substr(trim(preg_replace('/[\x00-\x1f\x7f]/u', ' ', $s) ?? ''), 0, $max) : '';
    }

    /**
     * @template T
     *
     * @param  callable(mixed): (T|null)  $item
     * @return list<T>
     */
    private static function list(mixed $values, int $max, callable $item): array
    {
        if (! is_array($values)) {
            return [];
        }
        $out = [];
        foreach (array_slice(array_values($values), 0, $max) as $v) {
            $x = $item($v);
            if ($x !== null) {
                $out[] = $x;
            }
        }

        return $out;
    }
}
