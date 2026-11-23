<?php

namespace App\Services;

use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Pack;
use App\Services\Packs\PackManifest;
use App\Validation\InputRules;
use Illuminate\Support\Collection;
use Illuminate\Validation\ValidationException;

/**
 * The database engines of the panel: the built-in engines (MariaDB, PostgreSQL and Valkey) and
 * the engines of the database packs (cf-database/1, docs/packs.md). The slug of a database pack
 * is the name of its engine.
 *
 * A pack engine has the same everyday features as a built-in engine with SQL: database servers,
 * databases, users with access to more than one database, sizes, backups, restores and staging
 * copies. The first version of the contract has no move to a different server, no Adminer and
 * no access from anywhere.
 */
class DatabaseEngines
{
    /** The built-in engines. */
    public const BUILT_IN = Database::ENGINES;

    /** The number of host ports of each pack engine on the address of a customer. */
    public const PORTS = DatabaseServer::PORTS;

    /**
     * The installed database packs, by slug.
     *
     * @return Collection<string, Pack>
     */
    public static function packs(): Collection
    {
        return Pack::where('kind', PackManifest::KIND_DATABASE)->orderBy('slug')->get()->keyBy('slug');
    }

    /**
     * All engines: the built-in engines, then the engines of the database packs.
     *
     * @return list<string>
     */
    public static function all(): array
    {
        return [...self::BUILT_IN, ...self::packs()->keys()->all()];
    }

    /**
     * The engines with users that can have access to more than one database, and with dumps:
     * the built-in engines with SQL, and the pack engines. A backup has a dump of each of their
     * databases, and a staging copy copies them.
     *
     * @return list<string>
     */
    public static function withUsers(): array
    {
        return [...Database::SQL_ENGINES, ...self::packs()->keys()->all()];
    }

    /** True when the engine comes from a database pack. */
    public static function isPack(string $engine): bool
    {
        return ! in_array($engine, self::BUILT_IN, true) && self::pack($engine) !== null;
    }

    /** The database pack of a pack engine, or null. */
    public static function pack(string $engine): ?Pack
    {
        if (in_array($engine, self::BUILT_IN, true)) {
            return null;
        }

        return Pack::where('kind', PackManifest::KIND_DATABASE)->where('slug', $engine)->first();
    }

    /** The name of an engine for people, for example "MySQL". */
    public static function label(string $engine): string
    {
        return match ($engine) {
            'mariadb' => 'MariaDB',
            'postgresql' => 'PostgreSQL',
            Database::VALKEY => 'Valkey',
            default => self::pack($engine)?->name ?? $engine,
        };
    }

    /**
     * The port of an engine in its container. A website connects to a database server of a
     * pack engine on this port at the member address of the database server.
     */
    public static function port(string $engine): int
    {
        if (isset(DatabaseServer::FIRST_PORT[$engine])) {
            return DatabaseServer::FIRST_PORT[$engine];
        }

        return (int) (self::pack($engine)?->spec()['database']['port'] ?? 0);
    }

    /**
     * The first host port of the database servers of an engine on the address of a customer.
     * A customer can have PORTS database servers of each engine on a server.
     */
    public static function firstPort(string $engine): ?int
    {
        return DatabaseServer::FIRST_PORT[$engine] ?? self::pack($engine)?->database_port;
    }

    /**
     * The versions of a pack engine, newest first, as in the manifest.
     *
     * @return list<array{version: string, supportEndsAt: ?string}>
     */
    public static function versions(string $engine): array
    {
        $pack = self::pack($engine);
        if ($pack === null) {
            return [];
        }

        return array_map(fn (array $img) => ['version' => $img['version'], 'supportEndsAt' => $img['supportEndsAt']], $pack->spec()['images']);
    }

    /**
     * The version of a new database server: the version from the request, or the first
     * version of the pack. A built-in engine has no version.
     *
     * @throws ValidationException
     */
    public static function versionFor(string $engine, ?string $version, string $field = 'version'): ?string
    {
        if (! self::isPack($engine)) {
            if ($version !== null && $version !== '') {
                throw ValidationException::withMessages([$field => self::label($engine).' has no versions to select. The version comes with the agent.']);
            }

            return null;
        }
        $versions = array_column(self::versions($engine), 'version');
        if ($version === null || $version === '') {
            return $versions[0];
        }
        if (! in_array($version, $versions, true)) {
            throw ValidationException::withMessages([$field => "The pack {$engine} has no version {$version}. Select one of ".implode(', ', $versions).'.']);
        }

        return $version;
    }

    /**
     * The first port of a free block for a new database pack, or null when all blocks are in
     * use. The blocks start at InputRules::MIN_DATABASE_PACK_PORT, one for each pack.
     */
    public static function freeBlock(): ?int
    {
        $used = Pack::whereNotNull('database_port')->pluck('database_port')->map(fn ($p) => (int) $p)->all();
        for ($n = 0; $n < InputRules::MAX_PACKS; $n++) {
            $port = InputRules::MIN_DATABASE_PACK_PORT + $n * self::PORTS;
            if (! in_array($port, $used, true)) {
                return $port;
            }
        }

        return null;
    }

    /**
     * The engines for the web panel, with their versions and their features.
     *
     * @return list<array<string, mixed>>
     */
    public static function present(): array
    {
        $out = [];
        foreach (self::BUILT_IN as $engine) {
            $out[] = [
                'engine' => $engine, 'name' => self::label($engine), 'port' => self::port($engine), 'pack' => null, 'versions' => [],
                'users' => in_array($engine, Database::SQL_ENGINES, true), 'moves' => true, 'adminer' => in_array($engine, DatabaseTools::ENGINES, true), 'anywhere' => $engine === 'mariadb',
            ];
        }
        foreach (self::packs() as $pack) {
            $spec = $pack->spec();
            $out[] = [
                'engine' => $pack->slug, 'name' => $pack->name, 'port' => $spec['database']['port'],
                'pack' => ['id' => $pack->id, 'slug' => $pack->slug, 'version' => $pack->version, 'publisher' => $pack->publisher],
                'versions' => array_map(fn (array $img) => ['version' => $img['version'], 'supportEndsAt' => $img['supportEndsAt']], $spec['images']),
                'users' => true, 'moves' => false, 'adminer' => false, 'anywhere' => false,
            ];
        }

        return $out;
    }

    /**
     * Refuses a feature that a pack engine does not have in cf-database/1.
     *
     * @throws ValidationException
     */
    public static function refusePack(string $engine, string $what, string $field = 'engine'): void
    {
        if (self::isPack($engine)) {
            throw ValidationException::withMessages([$field => self::label($engine)." comes from a pack. {$what} is not available for the database engines of packs."]);
        }
    }
}
